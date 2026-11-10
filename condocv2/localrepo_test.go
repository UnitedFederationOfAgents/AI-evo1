package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// localRepoFixture makes local/<name> under a temp UFA_LOCAL_ROOT: main with
// one commit, checked out, and branch with one more commit on top of it.
func localRepoFixture(t *testing.T, name, branch string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git here")
	}
	root := os.Getenv("UFA_LOCAL_ROOT")
	if root == "" {
		root = t.TempDir()
		t.Setenv("UFA_LOCAL_ROOT", root)
	}
	dir := filepath.Join(root, name)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	os.MkdirAll(dir, 0o755)
	run("init", "-q")
	run("symbolic-ref", "HEAD", "refs/heads/main")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+name+"\n"), 0o644)
	run("add", "README.md")
	run("commit", "-q", "-m", "first")
	run("checkout", "-q", "-b", branch)
	os.WriteFile(filepath.Join(dir, "cat-haiku.md"), []byte("soft paws\n"), 0o644)
	run("add", "cat-haiku.md")
	run("commit", "-q", "-m", "condoc's work")
	run("checkout", "-q", "main")
	return dir
}

func TestLocalRepoLinks(t *testing.T) {
	if got := repoCommitURL("local/content", "abc"); got != "ufa-local:content/commit/abc" {
		t.Errorf("local commit %q", got)
	}
	if got := repoBlobURL("local/content", "main", "condocs/x.md"); got != "ufa-local:content/blob/main/condocs/x.md" {
		t.Errorf("local blob %q", got)
	}
	if got := repoCommitURL("o/r", "abc"); got != "https://github.com/o/r/commit/abc" {
		t.Errorf("github commit %q", got)
	}
	if got := commitLink("local/content", "0123456789"); got != "[`0123456`](ufa-local:content/commit/0123456789)" {
		t.Errorf("commitLink %q", got)
	}
	pr := localPR("local/content", "condocv2/X-1/main", "main")
	if base, branch, ok := localCompare(pr); !ok || base != "main" || branch != "condocv2/X-1/main" || pr.Number != 0 || pr.State != "open" {
		t.Errorf("local PR %+v -> %q %q %v", pr, base, branch, ok)
	}
	if !allLocal([]prDraft{{Repo: "local/a"}, {Repo: "local/b"}}) || allLocal([]prDraft{{Repo: "local/a"}, {Repo: "o/b"}}) {
		t.Error("allLocal")
	}
}

// TestLocalMerge: a local PR is ready while its branch can merge, Merge
// merges it into the checked-out main with a merge commit, and after that it
// reads as merged; merging again changes nothing.
func TestLocalMerge(t *testing.T) {
	branch := "condocv2/LocalCondoc-1/main"
	dir := localRepoFixture(t, "content", branch)
	pr := localPR("local/content", branch, "main")

	if live := localPRLive(pr); !live.Ready || live.Status != "local merge ready" {
		t.Fatalf("before merging: %+v", live)
	}
	got, err := localMerge(pr, "LocalCondoc-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Merged || got.MergeCommitSHA == "" || got.Base.Ref != "main" {
		t.Fatalf("merged %+v", got)
	}
	parents, _ := exec.Command("git", "-C", dir, "rev-list", "--parents", "-n", "1", "main").Output()
	if len(strings.Fields(string(parents))) != 3 {
		t.Errorf("main isn't a merge commit: %s", parents)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cat-haiku.md")); err != nil || string(b) != "soft paws\n" {
		t.Errorf("the work tree after the merge: %q %v", b, err)
	}
	if live := localPRLive(pr); live.State != "merged" || live.Ready {
		t.Errorf("after merging: %+v", live)
	}
	again, err := localMerge(pr, "LocalCondoc-1")
	if err != nil || again.MergeCommitSHA != got.MergeCommitSHA {
		t.Errorf("merging again: %+v %v", again, err)
	}
	if b, err := localFileContent("local/content", "", "cat-haiku.md"); err != nil || string(b) != "soft paws\n" {
		t.Errorf("localFileContent %q %v", b, err)
	}
}

// TestLocalMergeElsewhere: a base that isn't checked out is merged in a work
// tree of its own, leaving the repo's checkout alone; a dirty checked-out
// base is refused.
func TestLocalMergeElsewhere(t *testing.T) {
	branch := "condocv2/LocalCondoc-2/main"
	dir := localRepoFixture(t, "slopspace", branch)
	exec.Command("git", "-C", dir, "checkout", "-q", "-b", "elsewhere").Run()
	if _, err := localMerge(localPR("local/slopspace", branch, "main"), "LocalCondoc-2"); err != nil {
		t.Fatal(err)
	}
	if head, _ := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "HEAD").Output(); strings.TrimSpace(string(head)) != "elsewhere" {
		t.Errorf("checkout moved to %s", head)
	}
	if out, _ := exec.Command("git", "-C", dir, "worktree", "list").Output(); strings.Count(string(out), "\n") != 1 {
		t.Errorf("a work tree was left: %s", out)
	}

	dirty := localRepoFixture(t, "dirty", branch)
	os.WriteFile(filepath.Join(dirty, "README.md"), []byte("changed\n"), 0o644)
	if _, err := localMerge(localPR("local/dirty", branch, "main"), "x"); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("a dirty checkout: %v", err)
	}
}
