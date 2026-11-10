package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// This file is the git provider's other paths (see declare_git.go): the
// files of any git repository at a ref, fetched from its URL, and a filter
// of them. They are only read (git.commits, declare_git_commits.go, writes
// a commit of a tree's files). The github provider knows GitHub -- which
// repository, and its clone_url (github.repos) -- and these know git
// repositories, so a document hands one to the other:
//
//	git.trees:
//	  select:
//	    url: ${{repo.clone_url}}          (any URL or path git fetch takes)
//	    ref: main                         (optional: a branch, tag or commit; HEAD, the remote's default, if left out)
//	  alias: source
//
//	git.files:
//	  select:
//	    tree: ${{source.tree}}            (a git.trees object's tree field)
//	    filter: "**/*.md"                 (optional: one pattern or a list; every file if left out)
//	  alias: md
//
// A tree fetches ref, one commit deep, into dtt's git cache ($DTT_GIT_CACHE,
// else ~/.ufa/dtt/git-cache: a bare repository per URL), then lists the
// files at that commit. Its state is url, ref, commit, short_commit, tree
// (its object's id, which git.files names it by), tree_hash (git's id for
// its files), file_count, bytes and files (their paths, the first
// gitTreeMaxListed of them). Reading fetches too: a tree is what the ref
// names on the remote now, and the cache is dtt's own, not a thing a
// document makes. A github.com URL is fetched with the github tool's token
// (declare_github.go), so a private repository's files can be read.
//
// Reading a document whose url (or tree) names something only an assert
// above it makes -- ${{repo.clone_url}} of a repository not created yet --
// leaves that reference as written; the tree (or filter) is then reported
// pending, exists: false, rather than fetched. Deleting, so does one to
// something the delete removed above (gitSelText).
//
// A filter (git.files) is a new tree of file nodes: the tree's files whose
// paths match one of its patterns. A pattern is path.Match's, with ** for
// any number of directories (none included), so **/*.md matches README.md
// and docs/a/b.md. Its state is tree, url, commit, filter, file_count,
// lines and bytes (all its files' together), files (each one's path, lines
// and bytes) and nodes: the files as a tree, each directory a mapping
// (its name ending in /) and each file its lines and bytes. A file's lines
// are its newlines, plus one for a last line without one.

// gitTreeMaxListed is how many paths a tree's files lists; file_count is
// every one of them.
const gitTreeMaxListed = 2000

// gitTree is a git.trees object's handle: the cache holding the commit,
// its tree's hash, and the files at it.
type gitTree struct {
	cache, url, ref, commit, hash string
	files                         []gitTreeFile
}

type gitTreeFile struct {
	path, blob string
	size       int64
}

// gitCacheDir is where trees are fetched to: $DTT_GIT_CACHE, else
// ~/.ufa/dtt/git-cache.
func gitCacheDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("DTT_GIT_CACHE")); v != "" {
		return expandHome(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ufa", "dtt", "git-cache"), nil
}

// gitCache is the cache's bare repository for url, made if it isn't there.
func gitCache(ctx context.Context, url string) (string, error) {
	root, err := gitCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(url))
	cache := filepath.Join(root, hex.EncodeToString(sum[:8])+".git")
	if _, err := os.Stat(cache); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", err
		}
		tmp, err := os.MkdirTemp(root, ".init-*")
		if err != nil {
			return "", err
		}
		if _, err := gitOut(ctx, tmp, "init", "--bare", "--quiet"); err != nil {
			os.RemoveAll(tmp)
			return "", err
		}
		// Another dtt making it at the same moment wins; this one's goes.
		if err := os.Rename(tmp, cache); err != nil {
			os.RemoveAll(tmp)
		}
	} else if err != nil {
		return "", err
	}
	if err := gitIgnoreAlternateRefs(ctx, cache); err != nil {
		return "", err
	}
	return cache, nil
}

// gitIgnoreAlternateRefs stops git, in cache, from treating the refs of the
// caches it borrows objects from (gitAlternate) as its own history. Those
// caches are shallow -- a commit's parents were never fetched -- but cache's
// own shallow list doesn't say so, so a fetch walking from their refs (to
// tell the remote what it has, and to check what it got) fails: "Could not
// read <parent>", "remote did not send all necessary objects". Set in
// cache's config, so caches made before this get it too.
func gitIgnoreAlternateRefs(ctx context.Context, cache string) error {
	if b, err := os.ReadFile(filepath.Join(cache, "config")); err == nil && strings.Contains(string(b), "alternateRefsCommand") {
		return nil
	}
	// The command lists the refs; true lists none.
	_, err := gitOut(ctx, cache, "config", "core.alternateRefsCommand", "true")
	return err
}

// gitAuthEnv is what git's environment needs to reach url: for a
// github.com URL, the github tool's token as an http header, so a private
// repository is fetched and pushed to as GitHub's API is called. It goes
// in the environment, not the arguments, so ps never shows it.
func (e *engine) gitAuthEnv(url string) []string {
	if !strings.HasPrefix(url, "https://github.com/") {
		return nil
	}
	token := e.github().token
	if token == "" {
		return nil
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + basic}
}

// unresolved: one of vs still holds a ${{...}} -- in read mode, a
// reference to a field only an assert above would fill in.
func unresolved(vs ...string) bool {
	for _, v := range vs {
		if strings.Contains(v, "${{") {
			return true
		}
	}
	return false
}

// gitSelText is select's key as text. Deleting, a reference to a field
// that is gone -- the clone_url of a repository the delete removed above --
// is left as written, so the object is pending: git's paths remove
// nothing, and the delete carries on past them.
func gitSelText(e *engine, st *statement, key string) (string, error) {
	n := st.sel.get(key)
	s, err := e.text(n, "select."+key)
	if err != nil && e.deleting() && n.kind == yScalar && unresolved(n.str) {
		return n.str, nil
	}
	return s, err
}

// gitPending puts an object whose select names something not made yet in
// the image, unfetched (see unresolved).
func gitPending(e *engine, st *statement, id string, state map[string]any) []*dobj {
	state["exists"] = false
	state["pending"] = "it names something an assert above makes; measured once that is made"
	return []*dobj{e.addObject(id, st.pathString(), st.line, state, nil)}
}

// gitTreeByID is the tree a git.trees or git.commits object's tree field
// names; nil if it names neither.
func (e *engine) gitTreeByID(id string) *gitTree {
	o, ok := e.byID[strings.TrimSpace(id)]
	if !ok {
		return nil
	}
	switch h := o.handle.(type) {
	case *gitTree:
		return h
	case *gitCommitRef:
		return h.tree
	}
	return nil
}

// treeState is a tree's fields: its commit, its tree's hash and its files.
func treeState(t *gitTree) map[string]any {
	var size int64
	listed := []any{}
	for i, f := range t.files {
		size += f.size
		if i < gitTreeMaxListed {
			listed = append(listed, f.path)
		}
	}
	state := map[string]any{"commit": t.commit, "short_commit": t.commit[:min(12, len(t.commit))],
		"tree_hash": t.hash, "file_count": len(t.files), "bytes": size, "files": listed}
	if len(t.files) > gitTreeMaxListed {
		state["files_listed"] = gitTreeMaxListed
	}
	return state
}

func checkGitTreeSelect(st *statement) error {
	if st.sel == nil || st.sel.kind != yMap {
		return fmt.Errorf("git.trees needs a select mapping: url (a repository's, ${{repo.clone_url}} say) and ref")
	}
	if err := onlyKeys(st.sel, "git.trees' select", "url", "ref"); err != nil {
		return err
	}
	if u, err := scalar(st.sel, "url"); err != nil {
		return err
	} else if strings.TrimSpace(u) == "" {
		return fmt.Errorf("git.trees' select needs url")
	}
	_, err := scalar(st.sel, "ref")
	return err
}

func checkGitFilesSelect(st *statement) error {
	if st.sel == nil || st.sel.kind != yMap {
		return fmt.Errorf("git.files needs a select mapping: tree (${{<a git.trees alias>.tree}}) and filter")
	}
	if err := onlyKeys(st.sel, "git.files' select", "tree", "filter"); err != nil {
		return err
	}
	if t, err := scalar(st.sel, "tree"); err != nil {
		return err
	} else if strings.TrimSpace(t) == "" {
		return fmt.Errorf("git.files' select needs tree, a git.trees object's tree field (${{source.tree}}, say)")
	}
	if f := st.sel.get("filter"); f != nil {
		items := []*yNode{f}
		if f.kind == ySeq {
			items = f.items
		}
		for _, item := range items {
			if item.kind != yScalar {
				return fmt.Errorf("line %d: filter is a pattern (**/*.md, say) or a list of them", item.line)
			}
			if !strings.Contains(item.str, "${{") {
				if _, err := globMatch(item.str, ""); err != nil {
					return fmt.Errorf("line %d: filter %q: %v", item.line, item.str, err)
				}
			}
		}
	}
	return nil
}

func resolveGitTree(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	url, err := gitSelText(e, st, "url")
	if err != nil {
		return nil, err
	}
	if url = strings.TrimSpace(url); url == "" {
		return nil, fmt.Errorf("select.url is empty")
	}
	ref := "HEAD"
	if n := st.sel.get("ref"); n != nil && !isEmpty(n) {
		if ref, err = gitSelText(e, st, "ref"); err != nil {
			return nil, err
		}
		if ref = strings.TrimSpace(ref); ref == "" {
			ref = "HEAD"
		}
	}
	id := "git.trees[" + url + "@" + ref + "]"
	if unresolved(url, ref) {
		return gitPending(e, st, id, map[string]any{"url": url, "ref": ref}), nil
	}
	if strings.HasPrefix(ref, "-") || strings.HasPrefix(url, "-") {
		return nil, fmt.Errorf("select: a url or ref starting with - would be read as one of git's options")
	}
	t, err := fetchGitTree(ctx, e.gitAuthEnv(url), url, ref)
	if err != nil {
		return nil, err
	}
	state := treeState(t)
	state["url"], state["ref"], state["tree"] = url, ref, id
	if e.log != nil {
		fmt.Fprintf(e.log, "git: %s at %s is %s, %d file(s)\n", url, ref, t.commit, len(t.files))
	}
	return []*dobj{e.addObject(id, st.pathString(), st.line, state, t)}, nil
}

// fetchGitTree fetches ref from url, one commit deep, into the cache's bare
// repository for url (env, gitAuthEnv's, reaching it), and lists the files
// at the commit it names.
func fetchGitTree(ctx context.Context, env []string, url, ref string) (*gitTree, error) {
	cache, err := gitCache(ctx, url)
	if err != nil {
		return nil, err
	}
	if _, err := gitEnvRaw(ctx, cache, env, nil, "fetch", "--quiet", "--depth", "1", "--no-tags", url, ref); err != nil {
		return nil, fmt.Errorf("fetching %s from %s: %w", ref, url, err)
	}
	commit, err := gitOut(ctx, cache, "rev-parse", "--verify", "--quiet", "FETCH_HEAD^{commit}")
	if err != nil || commit == "" {
		return nil, fmt.Errorf("%s from %s names no commit", ref, url)
	}
	return listGitTree(ctx, cache, url, ref, commit)
}

// listGitTree lists the files at commit, in cache, and keeps it there.
func listGitTree(ctx context.Context, cache, url, ref, commit string) (*gitTree, error) {
	// Keep the commit referenced, so the cache's gc leaves it for git.files.
	if _, err := gitOut(ctx, cache, "update-ref", "refs/dtt/"+gitRefName(ref), commit); err != nil {
		return nil, err
	}
	hash, err := gitOut(ctx, cache, "rev-parse", "--verify", "--quiet", commit+"^{tree}")
	if err != nil {
		return nil, err
	}
	out, err := gitRaw(ctx, cache, nil, "ls-tree", "-r", "-l", "-z", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	t := &gitTree{cache: cache, url: url, ref: ref, commit: commit, hash: hash}
	for _, entry := range strings.Split(string(out), "\x00") {
		// <mode> <type> <object> <size>\t<path>
		meta, p, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 || fields[1] != "blob" {
			continue // a submodule's commit, or the empty end
		}
		size, _ := strconv.ParseInt(fields[3], 10, 64)
		t.files = append(t.files, gitTreeFile{path: p, blob: fields[2], size: size})
	}
	sort.Slice(t.files, func(i, j int) bool { return t.files[i].path < t.files[j].path })
	return t, nil
}

// gitRefName makes a ref (main, v1.2, refs/heads/x, a commit) a safe last
// part for refs/dtt/.
func gitRefName(ref string) string {
	var b strings.Builder
	for _, c := range ref {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// gitRaw runs git in dir with stdin and returns its output untrimmed.
func gitRaw(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	return gitEnvRaw(ctx, dir, nil, stdin, args...)
}

// gitEnvRaw is gitRaw with env added to git's environment.
func gitEnvRaw(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// gitEnvOut is gitEnvRaw's output, trimmed.
func gitEnvOut(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, err := gitEnvRaw(ctx, dir, env, nil, args...)
	return strings.TrimSpace(string(out)), err
}

func resolveGitFiles(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	treeID, err := gitSelText(e, st, "tree")
	if err != nil {
		return nil, err
	}
	treeID = strings.TrimSpace(treeID)
	if unresolved(treeID) {
		return gitPending(e, st, fmt.Sprintf("git.files[line %d]", st.line), map[string]any{"tree": treeID}), nil
	}
	t := e.gitTreeByID(treeID)
	if t == nil {
		return nil, fmt.Errorf("select.tree %q isn't a git.trees object (or a git.commits one holding a commit) measured above; give its tree field (${{<its alias>.tree}})", treeID)
	}
	var patterns []string
	if f := st.sel.get("filter"); f != nil && !isEmpty(f) {
		items := []*yNode{f}
		if f.kind == ySeq {
			items = f.items
		}
		for _, item := range items {
			p, err := e.text(item, "select.filter")
			if err != nil {
				return nil, err
			}
			if p = strings.TrimSpace(p); p != "" {
				if _, err := globMatch(p, ""); err != nil {
					return nil, fmt.Errorf("line %d: filter %q: %v", item.line, p, err)
				}
				patterns = append(patterns, p)
			}
		}
	}
	var kept []gitTreeFile
	for _, f := range t.files {
		if len(patterns) == 0 {
			kept = append(kept, f)
			continue
		}
		for _, p := range patterns {
			if m, _ := globMatch(p, f.path); m {
				kept = append(kept, f)
				break
			}
		}
	}
	lines, err := gitBlobLines(ctx, t.cache, kept)
	if err != nil {
		return nil, err
	}
	var total, size int64
	files := []any{}
	nodes := map[string]any{}
	for i, f := range kept {
		total += lines[i]
		size += f.size
		files = append(files, map[string]any{"path": f.path, "lines": lines[i], "bytes": f.size})
		addFileNode(nodes, f.path, map[string]any{"lines": lines[i], "bytes": f.size})
	}
	filter := []any{}
	for _, p := range patterns {
		filter = append(filter, p)
	}
	state := map[string]any{"tree": treeID, "url": t.url, "commit": t.commit, "filter": filter,
		"file_count": len(kept), "lines": total, "bytes": size, "files": files, "nodes": nodes}
	id := "git.files[" + strings.TrimSuffix(strings.TrimPrefix(treeID, "git.trees["), "]") + ":" + strings.Join(patterns, ",") + "]"
	if e.log != nil {
		fmt.Fprintf(e.log, "git: %d file(s) of %s match %s, %d line(s)\n", len(kept), t.commit, strings.Join(patterns, ", "), total)
	}
	return []*dobj{e.addObject(id, st.pathString(), st.line, state, nil)}, nil
}

// addFileNode puts leaf at p in nodes, each directory on the way a mapping
// named with a trailing /.
func addFileNode(nodes map[string]any, p string, leaf map[string]any) {
	parts := strings.Split(p, "/")
	cur := nodes
	for _, d := range parts[:len(parts)-1] {
		next, ok := cur[d+"/"].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[d+"/"] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = leaf
}

// gitBlobLines counts each file's lines, reading their blobs from cache in
// one git cat-file --batch.
func gitBlobLines(ctx context.Context, cache string, files []gitTreeFile) ([]int64, error) {
	if len(files) == 0 {
		return nil, nil
	}
	var in bytes.Buffer
	for _, f := range files {
		in.WriteString(f.blob + "\n")
	}
	out, err := gitRaw(ctx, cache, &in, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(bytes.NewReader(out))
	counts := make([]int64, len(files))
	for i, f := range files {
		// <object> blob <size>\n<content>\n
		head, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("git cat-file: %s: %v", f.path, err)
		}
		fields := strings.Fields(head)
		if len(fields) != 3 || fields[1] != "blob" {
			return nil, fmt.Errorf("git cat-file: %s: %q", f.path, strings.TrimSpace(head))
		}
		n, _ := strconv.ParseInt(fields[2], 10, 64)
		data := make([]byte, n)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, fmt.Errorf("git cat-file: %s: %v", f.path, err)
		}
		r.ReadByte() // the newline after the content
		counts[i] = int64(bytes.Count(data, []byte("\n")))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			counts[i]++
		}
	}
	return counts, nil
}

// globMatch matches name (a slash-separated path) against pattern: each
// part as path.Match does, and a ** part as any number of parts, none
// included. An empty name only checks the pattern.
func globMatch(pattern, name string) (bool, error) {
	pp := strings.Split(pattern, "/")
	for _, p := range pp {
		if p == "**" {
			continue
		}
		if strings.Contains(p, "**") {
			return false, fmt.Errorf("** is a whole part of a pattern (**/*.md), not part of one")
		}
		if _, err := path.Match(p, ""); err != nil {
			return false, err
		}
	}
	if name == "" {
		return false, nil
	}
	return globParts(pp, strings.Split(name, "/")), nil
}

func globParts(pp, np []string) bool {
	for len(pp) > 0 {
		if pp[0] == "**" {
			for i := 0; i <= len(np); i++ {
				if globParts(pp[1:], np[i:]) {
					return true
				}
			}
			return false
		}
		if len(np) == 0 {
			return false
		}
		if ok, _ := path.Match(pp[0], np[0]); !ok {
			return false
		}
		pp, np = pp[1:], np[1:]
	}
	return len(np) == 0
}
