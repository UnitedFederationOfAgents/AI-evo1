package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// This file is the git provider (see declare.go): git repositories with no
// remote, on this host, under one root directory -- the repos a slopspace
// names local/<name> (dungeon-keeper's gitauth), which live at
// $UFA_LOCAL_ROOT/<name>, ~/workspace/ufa-local/<name> by default. It is
// not the github provider: nothing here talks to a server. Any repository's
// files at a ref, fetched from its URL, are its other paths, git.trees and
// git.files (declare_git_trees.go), and a commit of files pushed to one is
// git.commits (declare_git_commits.go).
//
//	git.repos:
//	  select:
//	    root: ~/workspace/ufa-local       (optional: the default is $UFA_LOCAL_ROOT, else that)
//	  assert:
//	    repos:                            (one, or a list)
//	      select:
//	        name: example-content-${{the-time.unix}}
//	      state:
//	        exists: true                  (the default, so it may be left out)
//	        default_branch: main          (create-time: the branch HEAD names)
//	        initial_commit: true          (create-time: a first commit, so the branch exists)
//	        description: ...              (create-time: the first commit's README.md)
//	        approve_delete: local/<name>  (needed beside exists: false)
//
// The statement's own object is the root: whether it exists and the git
// repositories directly under it. Each selected repository is a child
// object, git.repos[<name>]: its name, path, ref (local/<name>), exists,
// default_branch (the branch HEAD names), head (its commit, "" before the
// first) and branches. An assert creates a repository that doesn't exist
// (making the root if need be) with git init, HEAD on default_branch (main
// if not given) and, with initial_commit, one commit of README.md holding
// description (or a line naming it). default_branch, initial_commit and
// description are only applied then; on a repository that exists they're
// measured, not changed.
//
// exists: false removes the repository's directory, which can't be undone,
// so it is refused unless approve_delete beside it names the repository as
// local/<name>; the from rule then applies as for any field (exists: true
// isn't empty). Only a git repository directly under the root is removed --
// a directory there that isn't one is an error, never deleted. Delete mode
// asserts exists: false on every selected repository, under the same
// approval and from rule, as the github provider's repositories do.
type gitProvider struct{}

// gitRepoFields are the fields a local repository's state may assert.
var gitRepoFields = []string{"exists", "default_branch", "initial_commit", "description", "approve_delete"}

// gitLocalOwner is the owner slopspaces name these repos by: local/<name>.
const gitLocalOwner = "local"

var gitRepoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// gitRoot is handle of a git.repos statement's object: the root directory.
type gitRoot struct{ dir string }

type gitRepoSpec struct {
	name   *yNode
	fields []fieldSpec
	line   int
}

// gitDefaultRoot is where local repos live unless a statement says:
// $UFA_LOCAL_ROOT, else ~/workspace/ufa-local (gitauth.LocalRoot's rule).
func gitDefaultRoot() string {
	if v := strings.TrimSpace(os.Getenv("UFA_LOCAL_ROOT")); v != "" {
		return v
	}
	return "~/workspace/ufa-local"
}

func (gitProvider) checkSelect(st *statement) error {
	if len(st.path) == 2 && st.path[1] == "trees" {
		return checkGitTreeSelect(st)
	}
	if len(st.path) == 2 && st.path[1] == "files" {
		return checkGitFilesSelect(st)
	}
	if len(st.path) == 2 && st.path[1] == "commits" {
		return checkGitCommitSelect(st)
	}
	if len(st.path) != 2 || st.path[1] != "repos" {
		return fmt.Errorf("git paths are git.repos (local repositories under a root; select: {root: ...} names another than $UFA_LOCAL_ROOT or ~/workspace/ufa-local), git.trees (a repository's files at a ref, fetched from its url), git.files (a filter of a tree's files) and git.commits (a commit of files pushed to a repository's branch or tag)")
	}
	if st.sel == nil {
		return nil
	}
	if st.sel.kind != yMap {
		return fmt.Errorf("git.repos' select is a mapping (root: <dir>), or left out for the default root")
	}
	if err := onlyKeys(st.sel, "git.repos' select", "root"); err != nil {
		return err
	}
	root, err := scalar(st.sel, "root")
	if err != nil {
		return err
	}
	if root != "" && !strings.Contains(root, "${{") && !filepath.IsAbs(root) && root != "~" && !strings.HasPrefix(root, "~/") {
		return fmt.Errorf("select.root %q should be an absolute path (or ~/...)", root)
	}
	return nil
}

func (gitProvider) checkAssert(path []string, body *yNode) error {
	if path[1] == "commits" {
		return checkGitCommitAssert(body)
	}
	if path[1] != "repos" {
		return fmt.Errorf("git.%s can only be read (declare_git_trees.go): a tree is what its ref names on the remote -- git.commits pushes files to one", path[1])
	}
	_, err := parseGitRepoSpecs(body)
	return err
}

func parseGitRepoSpecs(body *yNode) ([]gitRepoSpec, error) {
	if body.kind != yMap {
		return nil, fmt.Errorf("line %d: git.repos' assert is a mapping with repos under it", body.line)
	}
	if err := onlyKeys(body, "git.repos' assert", "repos"); err != nil {
		return nil, err
	}
	repos := body.get("repos")
	if repos == nil {
		return nil, fmt.Errorf("line %d: git.repos' assert needs repos", body.line)
	}
	items := []*yNode{repos}
	if repos.kind == ySeq {
		items = repos.items
	}
	var out []gitRepoSpec
	for _, n := range items {
		if n.kind != yMap {
			return nil, fmt.Errorf("line %d: each of repos is a mapping of select and state", n.line)
		}
		if err := onlyKeys(n, "a local repository", "select", "state"); err != nil {
			return nil, err
		}
		sel := n.get("select")
		if sel == nil || sel.kind != yMap {
			return nil, fmt.Errorf("line %d: a local repository needs a select mapping (name)", n.line)
		}
		if err := onlyKeys(sel, "a local repository's select", "name"); err != nil {
			return nil, err
		}
		spec := gitRepoSpec{name: sel.get("name"), line: n.line}
		if spec.name == nil || isEmpty(spec.name) || spec.name.kind != yScalar {
			return nil, fmt.Errorf("line %d: a local repository's select needs name", sel.line)
		}
		if s := spec.name.str; !strings.Contains(s, "${{") && !gitRepoNameRe.MatchString(s) {
			return nil, fmt.Errorf("line %d: %q: a local repository's name is letters, digits, '.', '_' and '-'", sel.line, s)
		}
		var err error
		if spec.fields, err = assertFields(n.get("state"), "a local repository's state", gitRepoFields...); err != nil {
			return nil, err
		}
		removes, approved := false, false
		for _, f := range spec.fields {
			if f.want.kind != yScalar {
				return nil, fmt.Errorf("line %d: a local repository's %s should be a single value", f.line, f.name)
			}
			switch f.name {
			case "exists":
				b, err := parseBool(f.want.str)
				removes = err == nil && !b
			case "approve_delete":
				approved = !isEmpty(f.want)
				if len(f.from) > 0 {
					return nil, fmt.Errorf("line %d: approve_delete isn't a field of the repository; it takes no from", f.line)
				}
			}
		}
		if removes && !approved {
			return nil, fmt.Errorf("line %d: exists: false removes the repository, which can't be undone; it needs approve_delete: local/<name> beside it", n.line)
		}
		out = append(out, spec)
	}
	return out, nil
}

func (gitProvider) resolve(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	switch st.path[1] {
	case "trees":
		return resolveGitTree(ctx, e, st)
	case "files":
		return resolveGitFiles(ctx, e, st)
	case "commits":
		return resolveGitCommit(ctx, e, st)
	}
	root := ""
	if st.sel != nil && st.sel.get("root") != nil {
		var err error
		if root, err = e.text(st.sel.get("root"), "select.root"); err != nil {
			return nil, err
		}
	}
	if root = strings.TrimSpace(root); root == "" {
		root = gitDefaultRoot()
	}
	root, err := expandHome(root)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("select.root %q isn't absolute", root)
	}
	root = filepath.Clean(root)
	state := map[string]any{"root": root, "exists": false, "repos": []any{}}
	entries, err := os.ReadDir(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		state["exists"] = true
		repos := []any{}
		for _, en := range entries {
			if en.IsDir() && isGitRepo(filepath.Join(root, en.Name())) {
				repos = append(repos, en.Name())
			}
		}
		state["repos"] = repos
	}
	return []*dobj{e.addObject("git.repos["+root+"]", st.pathString(), st.line, state, &gitRoot{root})}, nil
}

// isGitRepo: dir is a git repository's work tree (it has a .git).
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// gitOut runs git in dir and returns its output, trimmed.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// gitRepoState measures the repository name under root.
func gitRepoState(ctx context.Context, root, name string) (map[string]any, error) {
	dir := filepath.Join(root, name)
	state := map[string]any{"name": name, "path": dir, "ref": gitLocalOwner + "/" + name, "exists": false,
		"initial_commit": false, "description": ""}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return state, nil
	case err != nil:
		return nil, err
	case !info.IsDir() || !isGitRepo(dir):
		return nil, fmt.Errorf("%s is there but isn't a git repository; the git provider leaves it alone", dir)
	}
	state["exists"] = true
	head, err := gitOut(ctx, dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		head = "" // a detached HEAD names no branch
	}
	state["default_branch"] = head
	commit, _ := gitOut(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD")
	state["head"] = commit
	refs, err := gitOut(ctx, dir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	branches := []any{}
	for _, b := range strings.Split(refs, "\n") {
		if b = strings.TrimSpace(b); b != "" {
			branches = append(branches, b)
		}
	}
	state["branches"] = branches
	return state, nil
}

func (gitProvider) assert(ctx context.Context, e *engine, o *dobj, body *yNode) error {
	if o.img.Path == "git.commits" {
		return assertGitCommit(ctx, e, o, body)
	}
	specs, err := parseGitRepoSpecs(body)
	if err != nil {
		return err
	}
	root := o.handle.(*gitRoot).dir
	for _, s := range specs {
		if err := assertGitRepo(ctx, e, o, root, s); err != nil {
			return err
		}
	}
	return nil
}

func assertGitRepo(ctx context.Context, e *engine, rootObj *dobj, root string, s gitRepoSpec) error {
	name, err := e.text(s.name, "select.name")
	if err != nil {
		return err
	}
	if !gitRepoNameRe.MatchString(name) {
		return fmt.Errorf("line %d: %q: a local repository's name is letters, digits, '.', '_' and '-'", s.line, name)
	}
	state, err := gitRepoState(ctx, root, name)
	if err != nil {
		return err
	}
	o := e.addObject("git.repos["+name+"]", rootObj.img.Path, s.line, state, nil)
	exists := state["exists"] == true
	ref := gitLocalOwner + "/" + name

	wantExists, explicit, existsLine := true, false, s.line
	var existsFrom []any
	approval, branch, description, initial := "", "main", "", false
	type createField struct {
		name, want string
		from       []any
		line       int
	}
	var creates []createField
	for _, f := range s.fields {
		want, err := e.text(f.want, f.name)
		if err != nil {
			return err
		}
		from, err := e.values(f.from)
		if err != nil {
			return err
		}
		switch f.name {
		case "exists":
			if wantExists, err = parseBool(want); err != nil {
				return fmt.Errorf("line %d: exists: %v", f.line, err)
			}
			existsFrom, explicit, existsLine = from, true, f.line
		case "approve_delete":
			approval = want
		case "initial_commit":
			b, err := parseBool(want)
			if err != nil {
				return fmt.Errorf("line %d: initial_commit: %v", f.line, err)
			}
			creates = append(creates, createField{f.name, fmt.Sprint(b), from, f.line})
		default:
			creates = append(creates, createField{f.name, want, from, f.line})
		}
	}
	if e.deleting() {
		// Delete mode: the repository goes, whatever else is asserted on it.
		wantExists = false
	}

	dir := filepath.Join(root, name)
	if !wantExists {
		if approval != ref {
			if approval == "" {
				return fmt.Errorf("line %d: removing %s (%s) can't be undone; it needs approve_delete: %s in its state", s.line, ref, dir, ref)
			}
			return fmt.Errorf("line %d: approve_delete %q should be %s to remove that repository", existsLine, approval, ref)
		}
		if exists && e.decide(o, []string{"exists"}, existsLine, false, true, existsFrom) {
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			o.img.State, _ = gitRepoState(ctx, root, name)
		} else if !exists {
			e.decide(o, []string{"exists"}, existsLine, false, false, existsFrom)
		}
		return nil
	}

	if exists && !explicit {
		// Create-time fields are measured on a repository that exists, never
		// changed: there's nothing to do.
		return nil
	}
	changeExists := e.decide(o, []string{"exists"}, existsLine, true, exists, existsFrom)
	if exists {
		return nil
	}
	// Making it: the create-time fields, each decided against nothing (in
	// read mode, each says it would change).
	for _, f := range creates {
		if !e.decide(o, []string{f.name}, f.line, f.want, nil, f.from) {
			continue
		}
		switch f.name {
		case "default_branch":
			branch = f.want
		case "description":
			description = f.want
		case "initial_commit":
			initial = f.want == "true"
		}
	}
	if !changeExists {
		return nil
	}
	if err := createGitRepo(ctx, dir, branch, description, initial); err != nil {
		os.RemoveAll(dir) // don't leave half a repository to be refused next time
		return err
	}
	o.img.State, err = gitRepoState(ctx, root, name)
	return err
}

// createGitRepo makes a git repository at dir with HEAD on branch and, if
// initial, one commit of README.md (description, or a line naming it).
func createGitRepo(ctx context.Context, dir, branch, description string, initial bool) error {
	if branch == "" {
		branch = "main"
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	if _, err := gitOut(ctx, dir, "init", "--quiet"); err != nil {
		return err
	}
	if _, err := gitOut(ctx, dir, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
		return err
	}
	if !initial {
		return nil
	}
	if description == "" {
		description = "# " + filepath.Base(dir) + "\n\nA local repository (no remote), made by dtt's git tool.\n"
	}
	if !strings.HasSuffix(description, "\n") {
		description += "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(description), 0o644); err != nil {
		return err
	}
	if _, err := gitOut(ctx, dir, "add", "README.md"); err != nil {
		return err
	}
	// A host with no git identity set still gets its first commit.
	args := []string{"commit", "--quiet", "-m", "Initial commit"}
	if email, _ := gitOut(ctx, dir, "config", "user.email"); email == "" {
		args = append([]string{"-c", "user.name=dtt", "-c", "user.email=dtt@localhost"}, args...)
	}
	_, err := gitOut(ctx, dir, args...)
	return err
}
