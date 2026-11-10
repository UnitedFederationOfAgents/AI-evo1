package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// This file is the git provider's path that writes to any repository (see
// declare_git_trees.go for the ones that read): one commit of a set of
// files, pushed to a branch or tag of a repository by its URL.
//
//	git.commits:
//	  select:
//	    url: ${{repo.clone_url}}          (any URL git push takes)
//	    ref: refs/tags/v1.0               (refs/heads/<branch> or refs/tags/<tag>; a bare name is a branch)
//	  alias: published
//	  assert:
//	    files_from: ${{source.tree}}      (a git.trees or git.commits object's tree, or an absolute directory)
//	    parent: none                      (optional: none, the default, for a commit with no history;
//	                                       current, on top of what ref holds; or a commit)
//	    message: v1.0                     (optional: the commit's message)
//	    author: dtt <dtt@localhost>       (optional, and the default: its author and committer)
//
// Its state is url, ref and exists, and once ref holds a commit what a
// tree's is (commit, short_commit, tree_hash, file_count, bytes, files),
// tree its object's id: git.files, a later files_from and slopspace.files'
// copy_from name it by that, as they do a git.trees object. Measuring
// fetches ref into dtt's git cache, as a tree does; a github.com URL is
// fetched and pushed to with the github tool's token, so private
// repositories work.
//
// What is asserted is tree_hash: the files of files_from. A tree's are its
// files, with no .git (a tree lists only files); a directory's are every
// file under it, ignored ones too, but never a .git. When ref already holds
// them it is unchanged, whatever parent and message say, so asserting again
// does nothing. Otherwise one commit of them is made in the cache and
// pushed to ref. A commit on top of what ref holds (parent current, or
// parent the commit it holds) replaces nothing, so it is always made; any
// other -- parent none on a ref that holds a commit, say -- replaces ref's
// history, so it is the from rule's (from empty, a from.files_from value
// naming a tree_hash, the image's, or force), and pushed with --force.
// The author is dtt's own unless given, so a commit carries nobody's name.
// Delete mode leaves pushed commits (kept): delete the repository to
// remove them.
//
// A tree in another repository's cache is reached through git's alternates
// (the cache for url lists the other's objects), so dtt's git cache is
// removed whole, never one repository of it. Only the objects are borrowed,
// not the other's refs (gitIgnoreAlternateRefs): its commits are shallow.

// gitCommitRef is a git.commits object's handle: where it pushes, and the
// tree ref holds (nil before its first commit).
type gitCommitRef struct {
	url, ref string
	tree     *gitTree
}

var gitCommitFields = []string{"files_from", "parent", "message", "author"}

// gitDefaultAuthor is who a commit is by unless its author is given.
const gitDefaultAuthor = "dtt <dtt@localhost>"

var gitAuthorRe = regexp.MustCompile(`^\s*([^<>]+?)\s*<([^<>\s]+)>\s*$`)

func checkGitCommitSelect(st *statement) error {
	if st.sel == nil || st.sel.kind != yMap {
		return fmt.Errorf("git.commits needs a select mapping: url (a repository's, ${{repo.clone_url}} say) and ref (refs/heads/<branch> or refs/tags/<tag>)")
	}
	if err := onlyKeys(st.sel, "git.commits' select", "url", "ref"); err != nil {
		return err
	}
	for _, k := range []string{"url", "ref"} {
		v, err := scalar(st.sel, k)
		if err != nil {
			return err
		}
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("git.commits' select needs %s", k)
		}
		if k == "ref" && !unresolved(v) {
			if _, err := gitPushRef(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// gitPushRef is ref as a full branch or tag: refs/heads/... and
// refs/tags/... as they are, a bare name as a branch.
func gitPushRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	full := ref
	if !strings.HasPrefix(full, "refs/") {
		full = "refs/heads/" + full
	}
	name := strings.TrimPrefix(strings.TrimPrefix(full, "refs/heads/"), "refs/tags/")
	if name == full || name == "" || strings.HasPrefix(ref, "-") || strings.Contains(full, "..") ||
		strings.ContainsAny(full, " \t~^:?*[\\") || strings.HasSuffix(full, "/") || strings.HasSuffix(full, ".lock") {
		return "", fmt.Errorf("ref %q should be refs/heads/<branch>, refs/tags/<tag> or a branch's name", ref)
	}
	return full, nil
}

func checkGitCommitAssert(body *yNode) error {
	fields, err := assertFields(body, "git.commits' assert", gitCommitFields...)
	if err != nil {
		return err
	}
	given := false
	for _, f := range fields {
		if f.want.kind != yScalar {
			return fmt.Errorf("line %d: %s should be a single value", f.line, f.name)
		}
		if f.from != nil && f.name != "files_from" {
			return fmt.Errorf("line %d: %s takes no from.%s (from.files_from names the tree_hash ref may be changed from)", f.line, f.name, f.name)
		}
		v := strings.TrimSpace(f.want.str)
		switch {
		case f.name == "files_from":
			given = v != ""
		case f.name == "author" && !unresolved(v) && !gitAuthorRe.MatchString(v):
			return fmt.Errorf("line %d: author is Name <email>", f.line)
		}
	}
	if !given {
		return fmt.Errorf("line %d: git.commits' assert needs files_from: a tree (${{<a git.trees alias>.tree}}) or an absolute directory", body.line)
	}
	return nil
}

func resolveGitCommit(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	url, err := gitSelText(e, st, "url")
	if err != nil {
		return nil, err
	}
	ref, err := gitSelText(e, st, "ref")
	if err != nil {
		return nil, err
	}
	url, ref = strings.TrimSpace(url), strings.TrimSpace(ref)
	if unresolved(url, ref) {
		return gitPending(e, st, "git.commits["+url+"@"+ref+"]", map[string]any{"url": url, "ref": ref}), nil
	}
	if url == "" || strings.HasPrefix(url, "-") {
		return nil, fmt.Errorf("select.url %q isn't a repository's URL", url)
	}
	if ref, err = gitPushRef(ref); err != nil {
		return nil, err
	}
	id := "git.commits[" + url + "@" + ref + "]"
	h := &gitCommitRef{url: url, ref: ref}
	state := map[string]any{"url": url, "ref": ref, "exists": false}
	env := e.gitAuthEnv(url)
	cache, err := gitCache(ctx, url)
	if err != nil {
		return nil, err
	}
	out, err := gitEnvOut(ctx, cache, env, "ls-remote", url, ref)
	if err != nil {
		return nil, fmt.Errorf("listing %s's refs: %w", url, err)
	}
	for _, l := range strings.Split(out, "\n") {
		if _, name, ok := strings.Cut(l, "\t"); ok && name == ref {
			if h.tree, err = fetchGitTree(ctx, env, url, ref); err != nil {
				return nil, err
			}
			break
		}
	}
	if h.tree != nil {
		state = treeState(h.tree)
		state["url"], state["ref"], state["exists"], state["tree"] = url, ref, true, id
	}
	return []*dobj{e.addObject(id, st.pathString(), st.line, state, h)}, nil
}

func assertGitCommit(ctx context.Context, e *engine, o *dobj, body *yNode) error {
	fields, err := assertFields(body, "git.commits' assert", gitCommitFields...)
	if err != nil {
		return err
	}
	f := lrFields{}
	for i := range fields {
		f[fields[i].name] = &fields[i]
	}
	line := f["files_from"].line
	if e.deleting() {
		e.keep(o, line, "a pushed commit is history; deleting leaves it (delete the repository to remove it)")
		return nil
	}
	src, _, err := f.text(e, "files_from")
	if err != nil {
		return err
	}
	src = strings.TrimSpace(src)
	from, err := e.values(f["files_from"].from)
	if err != nil {
		return err
	}
	parent, _, err := f.text(e, "parent")
	if err != nil {
		return err
	}
	parent = strings.TrimSpace(parent)
	h, _ := o.handle.(*gitCommitRef)
	have := o.img.State["tree_hash"] // nil before ref's first commit
	pending := func() error {
		// Reading, with what the commit is of (or where it goes) not made yet.
		if e.changes() {
			return fmt.Errorf("line %d: %s names something not measured", line, o.img.ID)
		}
		e.decide(o, []string{"tree_hash"}, line, "(the files of "+src+", once made)", have, from)
		return nil
	}
	if h == nil || unresolved(src, parent) {
		return pending()
	}
	cache, err := gitCache(ctx, h.url)
	if err != nil {
		return err
	}
	want, err := e.gitFilesTree(ctx, cache, src, line)
	if errors.Is(err, fs.ErrNotExist) && !e.changes() {
		return pending()
	}
	if err != nil {
		return err
	}

	cur := ""
	if h.tree != nil {
		cur = h.tree.commit
	}
	var parents []string
	switch parent {
	case "", "none":
	case "current":
		if cur != "" {
			parents = []string{cur}
		}
	default:
		p, err := gitFindCommit(ctx, cache, e.gitAuthEnv(h.url), h.url, parent)
		if err != nil {
			return fmt.Errorf("line %d: parent: %v", f["parent"].line, err)
		}
		parents = []string{p}
	}
	onTop := cur == "" || (len(parents) == 1 && parents[0] == cur)

	change := false
	if onTop && have != nil && have != want {
		// A commit on top of what ref holds replaces nothing.
		out := FieldOutcome{Object: o.img.ID, Field: "tree_hash", Outcome: "would-change", Why: "a commit on top of what it holds",
			Was: have, Want: want, Line: line}
		if e.changes() {
			out.Outcome = "changed"
		}
		e.record(out)
		change = e.changes()
	} else {
		change = e.decide(o, []string{"tree_hash"}, line, want, have, from)
	}
	if !change {
		return nil
	}

	message, ok, err := f.text(e, "message")
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(message) == "" {
		message = "Files of " + src
	}
	author, _, err := f.text(e, "author")
	if err != nil {
		return err
	}
	if strings.TrimSpace(author) == "" {
		author = gitDefaultAuthor
	}
	who := gitAuthorRe.FindStringSubmatch(author)
	if who == nil {
		return fmt.Errorf("line %d: author %q is Name <email>", f["author"].line, author)
	}
	args := []string{"commit-tree", want, "-m", message}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	commit, err := gitEnvOut(ctx, cache, []string{"GIT_AUTHOR_NAME=" + who[1], "GIT_AUTHOR_EMAIL=" + who[2],
		"GIT_COMMITTER_NAME=" + who[1], "GIT_COMMITTER_EMAIL=" + who[2]}, args...)
	if err != nil {
		return err
	}
	push := []string{"push", "--quiet"}
	if !onTop || (cur != "" && strings.HasPrefix(h.ref, "refs/tags/")) {
		push = append(push, "--force") // decided by the from rule above
	}
	if _, err := gitEnvRaw(ctx, cache, e.gitAuthEnv(h.url), nil, append(push, h.url, commit+":"+h.ref)...); err != nil {
		return fmt.Errorf("pushing %s to %s %s: %w", commit, h.url, h.ref, err)
	}
	if e.log != nil {
		fmt.Fprintf(e.log, "git: %s pushed to %s %s\n", commit, h.url, h.ref)
	}
	if h.tree, err = listGitTree(ctx, cache, h.url, h.ref, commit); err != nil {
		return err
	}
	state := treeState(h.tree)
	state["url"], state["ref"], state["exists"], state["tree"] = h.url, h.ref, true, o.img.ID
	o.img.State = state
	return nil
}

// gitFilesTree is the hash, in cache, of the tree of the files src names: a
// git.trees or git.commits object's tree (its objects reached from cache
// through git's alternates), or every file under an absolute directory
// (added to cache).
func (e *engine) gitFilesTree(ctx context.Context, cache, src string, line int) (string, error) {
	if t := e.gitTreeByID(src); t != nil {
		if t.cache != cache {
			if err := gitAlternate(cache, t.cache); err != nil {
				return "", err
			}
		}
		return t.hash, nil
	}
	if !filepath.IsAbs(src) {
		return "", fmt.Errorf("line %d: files_from %q is neither a tree measured above (${{<a git.trees or git.commits alias>.tree}}) nor an absolute directory", line, src)
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("line %d: files_from: %w", line, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("line %d: files_from %s isn't a directory", line, src)
	}
	return gitDirTree(ctx, cache, src)
}

// gitWorkEnv is the environment git needs to treat dir as cache's work
// tree, with an index of its own (idx) rather than the cache's.
func gitWorkEnv(cache, dir, idx string) []string {
	return []string{"GIT_DIR=" + cache, "GIT_WORK_TREE=" + dir, "GIT_INDEX_FILE=" + idx}
}

// gitTempIndex is a path for an index git makes, and its removal.
func gitTempIndex() (string, func(), error) {
	d, err := os.MkdirTemp("", "dtt-index-*")
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(d, "index"), func() { os.RemoveAll(d) }, nil
}

// gitDirTree adds every file under dir to cache -- ignored ones too, never
// a .git -- and returns their tree's hash.
func gitDirTree(ctx context.Context, cache, dir string) (string, error) {
	idx, done, err := gitTempIndex()
	if err != nil {
		return "", err
	}
	defer done()
	env := gitWorkEnv(cache, dir, idx)
	if _, err := gitEnvRaw(ctx, dir, env, nil, "add", "--all", "--force", "--", "."); err != nil {
		return "", err
	}
	return gitEnvOut(ctx, dir, env, "write-tree")
}

// gitCheckoutTree writes t's files into dir, made if it isn't there.
func gitCheckoutTree(ctx context.Context, t *gitTree, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	idx, done, err := gitTempIndex()
	if err != nil {
		return err
	}
	defer done()
	env := gitWorkEnv(t.cache, dir, idx)
	if _, err := gitEnvRaw(ctx, dir, env, nil, "read-tree", t.commit); err != nil {
		return err
	}
	_, err = gitEnvRaw(ctx, dir, env, nil, "checkout-index", "--all", "--force")
	return err
}

// gitAlternate lets cache read other's objects (git's alternates).
func gitAlternate(cache, other string) error {
	p := filepath.Join(cache, "objects", "info", "alternates")
	objects := filepath.Join(other, "objects")
	data, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == objects {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(file, objects); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// gitFindCommit is the commit rev names in cache, fetched from url when
// cache doesn't have it.
func gitFindCommit(ctx context.Context, cache string, env []string, url, rev string) (string, error) {
	if strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("%q would be read as one of git's options", rev)
	}
	if c, err := gitOut(ctx, cache, "rev-parse", "--verify", "--quiet", rev+"^{commit}"); err == nil && c != "" {
		return c, nil
	}
	if _, err := gitEnvRaw(ctx, cache, env, nil, "fetch", "--quiet", "--depth", "1", "--no-tags", url, rev); err != nil {
		return "", fmt.Errorf("%s isn't in dtt's git cache, and fetching it from %s: %w", rev, url, err)
	}
	c, err := gitOut(ctx, cache, "rev-parse", "--verify", "--quiet", "FETCH_HEAD^{commit}")
	if err != nil || c == "" {
		return "", fmt.Errorf("%s names no commit", rev)
	}
	return c, nil
}
