package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	ufaconfig "ufa-configurable"
)

// initTestRepo creates a temp git repo with one commit and a Makefile
// exposing a 'deploy-dev-binaries' target running buildCmd (a single shell
// word/command, e.g. "true" or "false") -- mirroring what --dev-repo expects
// to find at the real repo root. Returns the repo directory.
func initTestRepo(t *testing.T, buildCmd string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("deploy-dev-binaries:\n\t"+buildCmd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "initial")
	return dir
}

// TestResolveConfigDevRepo verifies --dev-repo / dev-repo layers the same way
// every other bool flag does (see TestResolveConfigDevMode). Whether it
// forces dev-mode on is main()'s job, not resolveConfig's, so that's not
// asserted here.
func TestResolveConfigDevRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local-representative.yaml"),
		[]byte("dev-repo: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conf, err := ufaconfig.Load("local-representative", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got, err := resolveConfig(conf, map[string]bool{}, appConfig{})
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if !got.devRepo {
		t.Errorf("devRepo not applied from config")
	}

	// A CLI-set --dev-repo=false wins over the config file.
	got, err = resolveConfig(conf, map[string]bool{"dev-repo": true}, appConfig{devRepo: false})
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if got.devRepo {
		t.Errorf("CLI-set dev-repo=false should win over dev-repo: true in config")
	}
}

// TestRepoWatchPollAndMaybePull verifies pollAndMaybePull's dirty/head/
// rebuild-ready derivation against a real git repo: rebuild_ready before any
// build has happened, dirty once a tracked file is modified.
func TestRepoWatchPollAndMaybePull(t *testing.T) {
	dir := initTestRepo(t, "true")
	rw := newRepoWatch(dir, func() {})

	if changed := rw.pollAndMaybePull(); !changed {
		t.Fatalf("expected the first poll to report a change (nothing built yet)")
	}
	snap := rw.snapshot()
	if snap.Dirty {
		t.Errorf("freshly-committed repo should not be dirty")
	}
	if !snap.RebuildReady {
		t.Errorf("expected rebuild_ready before any build has happened")
	}
	if snap.Head == "" {
		t.Errorf("expected a HEAD sha to be recorded")
	}

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rw.pollAndMaybePull()
	snap = rw.snapshot()
	if !snap.Dirty {
		t.Errorf("expected dirty after modifying a tracked file")
	}
	if !snap.RebuildReady {
		t.Errorf("expected rebuild_ready while dirty")
	}
}

// TestRepoWatchRebuildSuccessClearsRebuildReady verifies a successful
// 'make deploy-dev-binaries' run records the built HEAD, clears
// rebuild_ready (a clean repo has nothing left to rebuild), and notifies.
func TestRepoWatchRebuildSuccessClearsRebuildReady(t *testing.T) {
	dir := initTestRepo(t, "true")
	var notified int32
	rw := newRepoWatch(dir, func() { atomic.AddInt32(&notified, 1) })

	rw.pollAndMaybePull()
	if !rw.snapshot().RebuildReady {
		t.Fatalf("expected rebuild_ready before the first build")
	}

	rw.rebuild()
	snap := rw.snapshot()
	if snap.Building {
		t.Errorf("expected building=false once rebuild() returns")
	}
	if snap.LastError != "" {
		t.Errorf("expected no error, got %q", snap.LastError)
	}
	if snap.RebuildReady {
		t.Errorf("expected rebuild_ready=false immediately after a clean successful build")
	}
	if got := atomic.LoadInt32(&notified); got < 2 {
		t.Errorf("expected at least 2 notify() calls (build start + end), got %d", got)
	}
}

// TestRepoWatchRebuildFailureRecordsError verifies a failing
// 'make deploy-dev-binaries' is recorded in LastError rather than being
// treated as success.
func TestRepoWatchRebuildFailureRecordsError(t *testing.T) {
	dir := initTestRepo(t, "exit 1")
	rw := newRepoWatch(dir, func() {})

	rw.rebuild()
	snap := rw.snapshot()
	if snap.Building {
		t.Errorf("expected building=false once rebuild() returns")
	}
	if snap.LastError == "" {
		t.Errorf("expected a recorded error for a failing build")
	}
}

// TestRepoWatchRequestRebuildSkipsWhenNotReady verifies the operator-driven
// path refuses (without triggering a build) when the button wouldn't
// currently be active -- the server-side half of the same guard the
// dashboard's button disables itself on.
func TestRepoWatchRequestRebuildSkipsWhenNotReady(t *testing.T) {
	dir := initTestRepo(t, "true")
	var notified int32
	rw := newRepoWatch(dir, func() { atomic.AddInt32(&notified, 1) })

	rw.rebuild() // leaves rebuild_ready == false: nothing has changed since
	before := atomic.LoadInt32(&notified)

	rw.requestRebuild() // async; should be a no-op
	time.Sleep(300 * time.Millisecond)

	if got := atomic.LoadInt32(&notified); got != before {
		t.Errorf("requestRebuild triggered a rebuild despite rebuild_ready=false (notify count %d -> %d)", before, got)
	}
}

// TestServerRebuildControlsNoRepoWatched verifies the Server-level entry
// points (the "rebuild-app"/"set-auto-rebuild" WebSocket messages and
// "__system:rebuild"/"__system:auto-rebuild" both route through) are safe,
// logged no-ops when this LR wasn't launched with --dev-repo -- s.repoWatch
// is nil in that case.
func TestServerRebuildControlsNoRepoWatched(t *testing.T) {
	s := newServer("test-lr")

	if s.repoState().Watched {
		t.Fatalf("expected Watched=false with no dev-repo watcher configured")
	}

	// Must not panic on a nil s.repoWatch.
	s.requestRebuild("test")
	s.setAutoRebuild(true)
	s.handleSystemCommand("__system:rebuild")
	s.handleSystemCommand("__system:auto-rebuild on")
}
