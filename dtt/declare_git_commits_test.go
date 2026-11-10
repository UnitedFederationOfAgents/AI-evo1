package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	dktypes "dungeon-keeper/pkg/types"
)

// gitBareRepo is an empty bare repository, a remote for git.commits to push
// to; it returns its directory.
func gitBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "--bare", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return dir
}

// gitIn runs git in dir and returns its output, trimmed.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestGitCommitsScrubAndPublish copies a repository's files at a ref into a
// working repository, has an agent scrub them in a slopspace, commits the
// result on top, and publishes it as a tag of another repository with no
// history -- the shape of publishing a private repository.
func TestGitCommitsScrubAndPublish(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git here")
	}
	t.Setenv("DTT_GIT_CACHE", t.TempDir())
	src := gitSourceRepo(t, map[string]string{
		"README.md":            "# Spiral\n\nRun by alice at 10.0.0.1.\n",
		"condocs/Step1.md":     "# Step 1\n",
		"code/main.go":         "package main\n",
		"code/.gitignore":      "*.log\n",
		"condocv2/condocv2.go": "package condocv2\n",
	})
	// v1 has a parent, so the source's cache is shallow, as a real
	// repository's is: the working cache borrows its objects, and fetching
	// main back once seeded mustn't walk to the parent it lacks.
	gitIn(t, src, "-c", "user.name=t", "-c", "user.email=t@localhost", "commit", "--quiet", "--allow-empty", "-m", "more")
	gitIn(t, src, "tag", "v1")
	working, published := gitBareRepo(t), gitBareRepo(t)

	st := newSlopTest(t)
	st.agent = func(files string, sig *dktypes.WorkSignal) bool {
		repo := filepath.Join(files, "repo")
		if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
			t.Error("the copy has a .git")
		}
		if err := os.RemoveAll(filepath.Join(repo, "condocs")); err != nil {
			return false
		}
		p := filepath.Join(repo, "README.md")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Error(err)
			return false
		}
		clean := strings.NewReplacer("alice", "user-1", "10.0.0.1", "10.0.0.1").Replace(string(b))
		return os.WriteFile(p, []byte(clean), 0o644) == nil
	}
	doc := `git.trees:
  select:
    url: file://` + src + `
    ref: v1
  alias: source
  phase: seed

git.commits:
  select:
    url: file://` + working + `
    ref: main
  alias: seed
  assert:
    files_from: ${{source.tree}}
    message: source at v1

slopspace.spaces:
  select:
    key: scrub
  alias: space
  phase: scrub
  assert:
    exists: true

slopspace.files:
  select:
    space: ${{space.key}}
    path: repo
  alias: copied
  assert:
    copy_from: ${{seed.tree}}

slopspace.work.executions:
  select:
    space: ${{space.key}}
    key: scrub
  alias: scrubbed-work
  after: copied
  assert:
    prompt: Scrub writespaces/files/repo/.

git.commits:
  select:
    url: file://` + working + `
    ref: refs/heads/main
  alias: scrubbed
  after: scrubbed-work
  assert:
    files_from: ${{space.files}}/repo
    parent: current
    message: Scrubbed

git.commits:
  select:
    url: file://` + published + `
    ref: refs/tags/v1
  alias: tag
  phase: publish
  assert:
    files_from: ${{scrubbed.tree}}
    message: v1
`
	ctx := context.Background()

	if _, err := st.engine("read").runSource(ctx, doc, nil); err != nil {
		t.Fatal(err)
	}
	if out := gitIn(t, working, "for-each-ref"); out != "" {
		t.Fatalf("read pushed: %s", out)
	}

	made, err := st.engine("assert").runSource(ctx, doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.ran != 1 {
		t.Errorf("the agent ran %d times", st.ran)
	}
	// The working repository: the copy, then the scrub on top of it.
	if n := gitIn(t, working, "rev-list", "--count", "refs/heads/main"); n != "2" {
		t.Errorf("working main has %s commits", n)
	}
	if got := gitIn(t, working, "log", "-1", "--format=%an <%ae>|%s", "refs/heads/main"); got != gitDefaultAuthor+"|Scrubbed" {
		t.Errorf("working main's last commit: %s", got)
	}
	if got := gitIn(t, working, "ls-tree", "-r", "--name-only", "refs/heads/main~1"); !strings.Contains(got, "condocs/Step1.md") {
		t.Errorf("the copy: %s", got)
	}
	// The tag: the scrubbed files, a commit with no history.
	files := gitIn(t, published, "ls-tree", "-r", "--name-only", "refs/tags/v1")
	if strings.Contains(files, "condocs/") || !strings.Contains(files, "condocv2/condocv2.go") || !strings.Contains(files, "code/.gitignore") {
		t.Errorf("v1's files: %s", files)
	}
	if readme := gitIn(t, published, "show", "refs/tags/v1:README.md"); strings.Contains(readme, "alice") || !strings.Contains(readme, "user-1") {
		t.Errorf("v1's README: %s", readme)
	}
	if n := gitIn(t, published, "rev-list", "--count", "refs/tags/v1"); n != "1" {
		t.Errorf("v1 has %s commits", n)
	}
	tag := objectState(made, "git.commits[file://"+published+"@refs/tags/v1]")
	if tag["exists"] != true || tag["commit"] != gitIn(t, published, "rev-parse", "refs/tags/v1") {
		t.Errorf("tag %+v", tag)
	}

	// Again: every commit holds its files already, and the scrubbed copy
	// in the slopspace is held rather than copied over.
	again, err := st.engine("assert").runSource(ctx, doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(again); strings.Contains(got, "=changed") || st.ran != 1 {
		t.Errorf("again: %s, ran %d", got, st.ran)
	}
}

// TestGitCommitsPending: reading a document whose commit goes to a
// repository not made yet reports it pending, and fetches nothing.
func TestGitCommitsPending(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git here")
	}
	t.Setenv("DTT_GIT_CACHE", t.TempDir())
	src := gitSourceRepo(t, map[string]string{"a.md": "a\n"})
	gh := fakeRepoAPI(t, "o/source", "file://"+src)
	doc := `github.repos:
  select:
    repo: o/source
  alias: source
git.trees:
  select:
    url: ${{source.clone_url}}
  alias: tree
github.repos:
  select:
    repo: o/not-made-yet
  alias: target
git.commits:
  select:
    url: ${{target.clone_url}}
    ref: refs/tags/v1
  alias: tag
  assert:
    files_from: ${{tree.tree}}
git.files:
  select:
    tree: ${{tag.tree}}
  alias: tagged
`
	e := testEngine("read", false)
	e.gh = &githubClient{api: gh.URL, http: gh.Client()}
	img, err := e.runSource(context.Background(), doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range img.Objects {
		if (o.Alias == "tag" || o.Alias == "tagged") && (o.State["pending"] == nil || o.State["exists"] != false) {
			t.Errorf("%s: %+v", o.Alias, o.State)
		}
	}
	if got := outcomes(img); got != "tree_hash=would-change" {
		t.Errorf("read: %s", got)
	}
}

// TestGitCommitsChecks: git.commits' select and assert are checked before
// anything is fetched.
func TestGitCommitsChecks(t *testing.T) {
	for _, bad := range []string{
		"git.commits:\n  select:\n    url: file:///x\n",
		"git.commits:\n  select:\n    url: file:///x\n    ref: refs/notes/x\n  assert:\n    files_from: /tmp/x\n",
		"git.commits:\n  select:\n    url: file:///x\n    ref: main\n  assert:\n    message: m\n",
		"git.commits:\n  select:\n    url: file:///x\n    ref: main\n  assert:\n    files_from: /tmp/x\n    author: nobody\n",
		"git.commits:\n  select:\n    url: file:///x\n    ref: main\n  assert:\n    files_from: /tmp/x\n    parent: none\n    from.parent: current\n",
		"slopspace.files:\n  select:\n    space: s\n    path: repo\n  assert:\n    copy_from: x\n    content: y\n",
	} {
		doc, err := parseDocument(bad)
		if err == nil {
			err = testEngine("read", false).check(doc)
		}
		if err == nil {
			t.Errorf("no mistake found in %q", bad)
		}
	}
	for ref, want := range map[string]string{"main": "refs/heads/main", "refs/tags/v0.0.1": "refs/tags/v0.0.1", "refs/heads/a/b": "refs/heads/a/b"} {
		if got, err := gitPushRef(ref); err != nil || got != want {
			t.Errorf("gitPushRef(%q) = %q, %v", ref, got, err)
		}
	}
}
