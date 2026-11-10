package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitSourceRepo is a repository holding files, one commit on main, as a
// remote for git.trees to fetch; it returns its directory.
func gitSourceRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@localhost"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "--quiet")
	run("symbolic-ref", "HEAD", "refs/heads/main")
	// GitHub serves a commit asked for by its id; a local remote only when told to.
	run("config", "uploadpack.allowAnySHA1InWant", "true")
	for p, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "--quiet", "-m", "files")
	return dir
}

func gitHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// fakeRepoAPI is GitHub's GET /repos/<full> for one repository, whose
// clone_url is cloneURL.
func fakeRepoAPI(t *testing.T, full, cloneURL string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/repos/"+full {
			http.NotFound(w, r)
			return
		}
		name := full[strings.Index(full, "/")+1:]
		json.NewEncoder(w).Encode(map[string]any{"name": name, "full_name": full, "default_branch": "main",
			"html_url": "https://github.test/" + full, "clone_url": cloneURL})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestGitMdLines runs the git-md-lines specification: github.repos finds
// the repository's clone_url, git.trees fetches main from it, git.files
// keeps its **/*.md and counts their lines, and the count is written and
// uploaded to the preferred file cache.
func TestGitMdLines(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git here")
	}
	t.Setenv("DTT_GIT_CACHE", t.TempDir())
	src := gitSourceRepo(t, map[string]string{
		"README.md":      "# Sandbox\n\nHello.\n", // 3 lines
		"docs/a.md":      "one\ntwo",              // 2: the last has no newline
		"docs/deep/c.md": "\n",                    // 1
		"docs/notes.txt": "not markdown\n",
		"main.go":        "package main\n",
	})
	commit := gitHead(t, src)
	short := commit[:12]
	gh := fakeRepoAPI(t, "user-1/AI-sandboxing", "file://"+src)
	lr := newFakeLR()
	lrSrv := httptest.NewServer(lr)
	t.Cleanup(lrSrv.Close)
	dir := t.TempDir() + "/"
	engine := func(mode string) *engine {
		e := testEngine(mode, false)
		e.gh = &githubClient{api: gh.URL, http: gh.Client()}
		e.lr = &localrepClient{base: lrSrv.URL, http: lrSrv.Client()}
		e.vars = map[string]string{"dir": dir}
		return e
	}
	doc, ctx := readSpec(t, "git-md-lines"), context.Background()
	out := filepath.Join(dir, "git-md-lines-"+short, "md-lines.txt")
	upload := "md-lines-" + short + ".txt"
	tree := "git.trees[file://" + src + "@main]"
	files := "git.files[file://" + src + "@main:**/*.md]"

	read, err := engine("read").runSource(ctx, doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err == nil || lr.uploads != 0 {
		t.Fatalf("read wrote the count or uploaded it (%d)", lr.uploads)
	}
	if got := outcomes(read); !strings.Contains(got, "would-change") {
		t.Errorf("read: %s", got)
	}
	ts := objectState(read, tree)
	if ts["commit"] != commit || ts["short_commit"] != short || ts["tree"] != tree || fmt.Sprint(ts["file_count"]) != "5" {
		t.Errorf("tree %+v", ts)
	}
	md := objectState(read, files)
	if fmt.Sprint(md["lines"]) != "6" || fmt.Sprint(md["file_count"]) != "3" || fmt.Sprint(md["filter"]) != "[**/*.md]" {
		t.Errorf("md files %+v", md)
	}
	nodes, _ := md["nodes"].(map[string]any)
	docs, _ := nodes["docs/"].(map[string]any)
	deep, _ := docs["deep/"].(map[string]any)
	if c, _ := deep["c.md"].(map[string]any); fmt.Sprint(c["lines"]) != "1" || nodes["README.md"] == nil || docs["notes.txt"] != nil || nodes["main.go"] != nil {
		t.Errorf("nodes %+v", nodes)
	}
	if r := objectState(read, "github.repos[user-1/AI-sandboxing]"); r["exists"] != true || r["clone_url"] != "file://"+src || r["owner"] != "user-1" {
		t.Errorf("repo %+v", r)
	}

	made, err := engine("assert").runSource(ctx, doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "user-1/AI-sandboxing at main (commit " + commit + ") has 6 lines of md, in 3 files matching **/*.md.\n"
	if b, err := os.ReadFile(out); err != nil || string(b) != want {
		t.Errorf("%s: %q (%v)", out, b, err)
	}
	if got := lrCached(lr, upload); len(got) != 1 || got[0] != want {
		t.Errorf("the files tab's %s: %q", upload, got)
	}

	// Again at the same commit: nothing to change, nothing uploaded again.
	again, err := engine("assert").runSource(ctx, doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(again); strings.Contains(got, "=changed") || lr.uploads != 1 {
		t.Errorf("again: %s, uploaded %d", got, lr.uploads)
	}

	// Deleting with the image removes the count and the upload.
	if _, err := engine("delete").runSource(ctx, doc, roundTrip(t, made)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(out)); err == nil {
		t.Error("delete left the count's directory")
	}
	if got := lrCached(lr, upload); len(got) != 0 {
		t.Errorf("delete left %s in the files tab", upload)
	}
}

// lrCached is what the fake LR's files tab holds named name.
func lrCached(lr *fakeLR, name string) []string {
	var out []string
	for _, f := range lr.files {
		if f["name"] == name {
			out = append(out, string(lr.blobs[f["id"].(string)]))
		}
	}
	return out
}

// TestGitTreeRefs: a tree follows its ref (a branch that moves, a commit),
// HEAD when none is given, and a filter of none keeps every file.
func TestGitTreeRefs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git here")
	}
	t.Setenv("DTT_GIT_CACHE", t.TempDir())
	src := gitSourceRepo(t, map[string]string{"a.md": "a\n"})
	first := gitHead(t, src)
	os.WriteFile(filepath.Join(src, "b.md"), []byte("b\nb\n"), 0o644)
	if out, err := exec.Command("git", "-C", src, "add", "b.md").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", src, "-c", "user.name=t", "-c", "user.email=t@localhost", "commit", "--quiet", "-m", "b").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	second := gitHead(t, src)
	ctx := context.Background()
	doc := func(ref string) string {
		s := "git.trees:\n  select:\n    url: file://" + src + "\n"
		if ref != "" {
			s += "    ref: " + ref + "\n"
		}
		return s + "  alias: t\ngit.files:\n  select:\n    tree: ${{t.tree}}\n  alias: f\n"
	}
	for _, c := range []struct{ ref, commit, lines string }{{"", second, "3"}, {"main", second, "3"}, {first, first, "1"}} {
		img, err := testEngine("read", false).runSource(ctx, doc(c.ref), nil)
		if err != nil {
			t.Fatalf("ref %q: %v", c.ref, err)
		}
		var tree, files map[string]any
		for _, o := range img.Objects {
			switch o.Alias {
			case "t":
				tree = o.State
			case "f":
				files = o.State
			}
		}
		if tree["commit"] != c.commit || fmt.Sprint(files["lines"]) != c.lines {
			t.Errorf("ref %q: tree %+v, files %+v", c.ref, tree, files)
		}
	}
}

// TestGitTreeChecks: trees and filters are only read, and their selects
// are checked before anything is fetched.
func TestGitTreeChecks(t *testing.T) {
	for _, bad := range []string{
		"git.trees:\n  select:\n    url: file:///x\n  assert:\n    repos: x\n",
		"git.trees:\n  select:\n    ref: main\n",
		"git.trees:\n  select:\n    url: file:///x\n    branch: main\n",
		"git.files:\n  select:\n    filter: \"*.md\"\n",
		"git.files:\n  select:\n    tree: x\n    filter: \"a**b\"\n",
		"git.files:\n  select:\n    tree: x\n    filter: \"[\"\n",
		"github.repos:\n  select:\n    repo: just-a-name\n",
		"github.repos:\n  select:\n    repo: a/b\n  assert:\n    prs: x\n",
	} {
		doc, err := parseDocument(bad)
		if err == nil {
			err = testEngine("read", false).check(doc)
		}
		if err == nil {
			t.Errorf("no mistake found in %q", bad)
		}
	}
	if _, err := testEngine("read", false).runSource(context.Background(), "git.files:\n  select:\n    tree: git.trees[nowhere@main]\n", nil); err == nil || !strings.Contains(err.Error(), "isn't a git.trees object") {
		t.Errorf("a tree that wasn't measured: %v", err)
	}
}

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"**/*.md", "README.md", true},
		{"**/*.md", "docs/a/b.md", true},
		{"**/*.md", "docs/a/b.txt", false},
		{"*.md", "docs/a.md", false},
		{"docs/**", "docs/a/b.md", true},
		{"docs/**/b.md", "docs/b.md", true},
		{"docs/*/b.md", "docs/b.md", false},
		{"**", "x", true},
	} {
		if got, err := globMatch(c.pattern, c.name); err != nil || got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, %v", c.pattern, c.name, got, err)
		}
	}
}
