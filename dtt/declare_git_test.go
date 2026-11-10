package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestGitReposLocal makes the example's two local repositories under a temp
// root, then removes them in delete mode -- held without the image, removed
// with it.
func TestGitReposLocal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git here")
	}
	root := t.TempDir()
	t.Setenv("UFA_LOCAL_ROOT", root)
	src := readExample(t, "git-repos-local.dtt.yaml")
	ctx := context.Background()
	at := strconv.FormatInt(testNow.Unix(), 10)
	content := filepath.Join(root, "example-content-"+at)

	img, err := testEngine("read", false).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); !strings.Contains(got, "exists=would-change") || !strings.Contains(got, "initial_commit=would-change") {
		t.Errorf("read: %s", got)
	}
	if _, err := os.Stat(content); err == nil {
		t.Fatal("read made the repository")
	}

	made, err := testEngine("assert", false).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	var repo map[string]any
	for _, o := range made.Objects {
		if o.ID == "git.repos[example-content-"+at+"]" {
			repo = o.State
		}
	}
	if repo == nil || repo["exists"] != true || repo["default_branch"] != "main" || repo["head"] == "" || repo["ref"] != "local/example-content-"+at {
		t.Fatalf("content repository %+v", repo)
	}
	if b, err := os.ReadFile(filepath.Join(content, "README.md")); err != nil || !strings.Contains(string(b), "An example content repository") {
		t.Errorf("README.md %q (%v)", b, err)
	}
	if out, err := exec.Command("git", "-C", content, "log", "--oneline").CombinedOutput(); err != nil || !strings.Contains(string(out), "Initial commit") {
		t.Errorf("log %s (%v)", out, err)
	}

	// Asserting again changes nothing.
	if img, _ = testEngine("assert", false).runSource(ctx, src, nil); outcomes(img) != "exists=unchanged exists=unchanged" {
		t.Errorf("second assert: %s", outcomes(img))
	}

	// Delete mode is held without the image, and removes both with it.
	if img, _ = testEngine("delete", false).runSource(ctx, src, nil); outcomes(img) != "exists=held exists=held" {
		t.Errorf("delete without the image: %s", outcomes(img))
	}
	if _, err := os.Stat(content); err != nil {
		t.Fatal("a held delete removed the repository")
	}
	if img, err = testEngine("delete", false).runSource(ctx, src, roundTrip(t, made)); err != nil || outcomes(img) != "exists=changed exists=changed" {
		t.Fatalf("delete with the image: %v %s", err, outcomes(img))
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("left %v under the root", entries)
	}
}

// TestGitReposRefuses: removing needs approve_delete naming local/<name>,
// and a directory under the root that isn't a git repository is never
// touched.
func TestGitReposRefuses(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git here")
	}
	root := t.TempDir()
	ctx := context.Background()
	doc := func(state string) string {
		return "git.repos:\n  select:\n    root: " + root + "\n  assert:\n    repos:\n      select:\n        name: x\n      state:\n" + state
	}
	if _, err := testEngine("read", false).runSource(ctx, doc("        exists: false\n"), nil); err == nil || !strings.Contains(err.Error(), "approve_delete") {
		t.Errorf("exists: false without approval: %v", err)
	}
	if _, err := testEngine("delete", true).runSource(ctx, doc("        approve_delete: local/y\n"), nil); err == nil || !strings.Contains(err.Error(), "should be local/x") {
		t.Errorf("the wrong approval: %v", err)
	}
	os.MkdirAll(filepath.Join(root, "x"), 0o755)
	os.WriteFile(filepath.Join(root, "x", "keep.txt"), []byte("mine"), 0o644)
	if _, err := testEngine("delete", true).runSource(ctx, doc("        approve_delete: local/x\n"), nil); err == nil || !strings.Contains(err.Error(), "isn't a git repository") {
		t.Errorf("a plain directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "x", "keep.txt")); err != nil {
		t.Error("a directory that isn't a repository was removed")
	}
	if _, err := testEngine("read", false).runSource(ctx, "git.repos:\n  select:\n    root: relative/dir\n", nil); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("a relative root: %v", err)
	}
}
