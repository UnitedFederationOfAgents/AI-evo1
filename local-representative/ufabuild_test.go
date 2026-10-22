package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseUfabuild(t *testing.T) {
	good := []struct{ in, want string }{
		{"parent: 1a2b3c4\n", "1a2b3c4"},
		{"# build the commit before this one\nparent: 1A2B3C4D\n", "1a2b3c4d"},
		{"parent: \"0123456789abcdef0123456789abcdef01234567\"\n", "0123456789abcdef0123456789abcdef01234567"},
		{"note: anything\nparent: abcdef0\n", "abcdef0"},
	}
	for _, c := range good {
		got, err := parseUfabuild(c.in)
		if err != nil || got != c.want {
			t.Errorf("parseUfabuild(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	bad := []string{
		"",
		"commit: 1a2b3c4\n",
		"parent: 1a2b3c\n", // too short
		"parent: not-a-sha\n",
		"- parent: 1a2b3c4\n",
		"parent:\n  - 1a2b3c4\n",
	}
	for _, in := range bad {
		if got, err := parseUfabuild(in); err == nil {
			t.Errorf("parseUfabuild(%q) = %q, want an error", in, got)
		}
	}
}

// ufabuildGit runs git in dir with a fixed identity, returning its trimmed
// output.
func ufabuildGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitUfabuild commits a .ufabuild naming parent as its sole change.
func commitUfabuild(t *testing.T, dir, parent string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ufabuildFile), []byte("parent: "+parent+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ufabuildGit(t, dir, "add", ufabuildFile)
	ufabuildGit(t, dir, "commit", "-q", "-m", "ufabuild")
}

// buildCount reads how many times the test Makefile's deploy-dev-binaries
// ran (each run appends a line to builds.log, which is untracked and so
// doesn't make the repo dirty).
func buildCount(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "builds.log"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// autoTick runs one watch-loop pass's poll and auto-rebuild, then clears the
// post-build completion grace so the next tick isn't blocked by it.
func autoTick(rw *repoWatch) {
	rw.opMu.Lock()
	rw.pollAndMaybePull()
	rw.maybeAutoRebuild()
	rw.opMu.Unlock()
	rw.mu.Lock()
	rw.building = false
	rw.mu.Unlock()
}

// TestRepoWatchUfabuildWatch walks ufabuild watch through its cases: HEAD
// moving alone doesn't build; a .ufabuild naming its commit's parent builds
// once, straight away, with auto-rebuild off; later commits don't rebuild
// it; one naming some other commit isn't a request; and turning the watch
// off falls back to the legacy HEAD-moved readiness.
func TestRepoWatchUfabuildWatch(t *testing.T) {
	dir := initTestRepo(t, "echo x >> builds.log")
	rw := newRepoWatch(dir, func() {})
	rw.setUfabuildWatch(true)

	commitChange(t, dir, "v2\n")
	autoTick(rw)
	snap := rw.snapshot()
	if !snap.UfabuildWatch {
		t.Fatalf("expected ufabuild_watch on")
	}
	if snap.RebuildReady || buildCount(t, dir) != 0 {
		t.Fatalf("HEAD moving without a .ufabuild request shouldn't build (ready=%v, builds=%d)", snap.RebuildReady, buildCount(t, dir))
	}
	if snap.UfabuildNote == "" {
		t.Errorf("expected a note explaining there's no request")
	}

	parent := ufabuildGit(t, dir, "rev-parse", "--short", "HEAD")
	commitUfabuild(t, dir, parent)
	rw.opMu.Lock()
	rw.pollAndMaybePull()
	rw.opMu.Unlock()
	snap = rw.snapshot()
	if !snap.RebuildReady || snap.UfabuildRequest == "" || snap.UfabuildNote != "" {
		t.Fatalf("expected a ready request after committing .ufabuild naming %s, got %+v", parent, snap)
	}
	autoTick(rw)
	if n := buildCount(t, dir); n != 1 {
		t.Fatalf("expected the request to build once with auto-rebuild off, got %d builds", n)
	}
	if rw.snapshot().RebuildReady {
		t.Errorf("expected rebuild_ready=false once the request is built")
	}

	commitChange(t, dir, "v3\n")
	autoTick(rw)
	if n := buildCount(t, dir); n != 1 {
		t.Errorf("a commit after a built request shouldn't rebuild, got %d builds", n)
	}

	// Name the very first commit, which isn't this new commit's parent.
	commitUfabuild(t, dir, ufabuildGit(t, dir, "rev-list", "--max-parents=0", "HEAD"))
	autoTick(rw)
	snap = rw.snapshot()
	if n := buildCount(t, dir); n != 1 || snap.RebuildReady {
		t.Errorf("a .ufabuild naming a commit other than its parent shouldn't build (builds=%d, ready=%v)", n, snap.RebuildReady)
	}
	if !strings.Contains(snap.UfabuildNote, "not its parent") {
		t.Errorf("expected a not-its-parent note, got %q", snap.UfabuildNote)
	}

	rw.setUfabuildWatch(false)
	if !rw.snapshot().RebuildReady {
		t.Errorf("with ufabuild watch off, HEAD having moved since the last build should be ready again")
	}
}

// TestRepoWatchUfabuildSeededAsBuilt verifies a request already committed
// when the watcher starts counts as built, so turning ufabuild watch on
// doesn't immediately build it.
func TestRepoWatchUfabuildSeededAsBuilt(t *testing.T) {
	dir := initTestRepo(t, "echo x >> builds.log")
	commitUfabuild(t, dir, ufabuildGit(t, dir, "rev-parse", "HEAD"))
	rw := newRepoWatch(dir, func() {})
	rw.setUfabuildWatch(true)
	autoTick(rw)
	if n := buildCount(t, dir); n != 0 || rw.snapshot().RebuildReady {
		t.Errorf("an already-committed request shouldn't build on start (builds=%d)", n)
	}
	if rw.snapshot().UfabuildRequest == "" {
		t.Errorf("expected the existing request to be reported")
	}
}

// TestRepoWatchUfabuildFailedNotRetried verifies a request whose build fails
// isn't rebuilt on every poll, but a fresh request is.
func TestRepoWatchUfabuildFailedNotRetried(t *testing.T) {
	dir := initTestRepo(t, "echo x >> builds.log && false")
	rw := newRepoWatch(dir, func() {})
	rw.setUfabuildWatch(true)

	commitUfabuild(t, dir, ufabuildGit(t, dir, "rev-parse", "HEAD"))
	autoTick(rw)
	autoTick(rw)
	if n := buildCount(t, dir); n != 1 {
		t.Fatalf("expected one failed build and no retry, got %d builds", n)
	}
	if rw.snapshot().LastError == "" {
		t.Errorf("expected the failure recorded")
	}

	commitUfabuild(t, dir, ufabuildGit(t, dir, "rev-parse", "HEAD"))
	autoTick(rw)
	if n := buildCount(t, dir); n != 2 {
		t.Errorf("expected a new request to build again, got %d builds", n)
	}
}

// TestRepoWatchUfabuildRemoved verifies deleting .ufabuild leaves no request.
func TestRepoWatchUfabuildRemoved(t *testing.T) {
	dir := initTestRepo(t, "true")
	commitUfabuild(t, dir, ufabuildGit(t, dir, "rev-parse", "HEAD"))
	ufabuildGit(t, dir, "rm", "-q", ufabuildFile)
	ufabuildGit(t, dir, "commit", "-q", "-m", "drop ufabuild")
	rw := newRepoWatch(dir, func() {})
	snap := rw.snapshot()
	if snap.UfabuildRequest != "" || !strings.Contains(snap.UfabuildNote, "removed") {
		t.Errorf("expected no request and a removed note, got %+v", snap)
	}
}
