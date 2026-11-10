package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// expireAutoRebuild pretends the 90s auto-rebuild debounce has run out.
func expireAutoRebuild(rw *repoWatch) {
	rw.mu.Lock()
	if !rw.autoRebuildDeadline.IsZero() {
		rw.autoRebuildDeadline = time.Now().Add(-time.Second)
	}
	rw.mu.Unlock()
}

// TestRepoWatchUfabuildFromTaggedCommit walks the watched repo's .ufabuild
// sequence: an untagged commit moving HEAD doesn't ask for a build; a
// [condocv2-xxxx] one writes an ignored .ufabuild request for the new HEAD,
// which readies the rebuild button but, with auto-rebuild off, doesn't build
// on its own; pressing rebuild builds it once and consumes the request.
func TestRepoWatchUfabuildFromTaggedCommit(t *testing.T) {
	dir := initTestRepo(t, "echo x >> builds.log")
	rw := newRepoWatch(dir, func() {})

	commitChange(t, dir, "v2\n")
	autoTick(rw)
	snap := rw.snapshot()
	if snap.RebuildReady || snap.UfabuildRequest != "" || buildCount(t, dir) != 0 {
		t.Fatalf("an untagged commit shouldn't ask for a build, got %+v (builds=%d)", snap, buildCount(t, dir))
	}
	if snap.UfabuildNote == "" {
		t.Errorf("expected a note explaining there's no request")
	}

	commitTagged(t, dir, "v3\n")
	autoTick(rw)
	snap = rw.snapshot()
	head := ufabuildGit(t, dir, "rev-parse", "--short=7", "HEAD")
	if !snap.RebuildReady || snap.UfabuildRequest != head || snap.UfabuildNote != "" {
		t.Fatalf("expected a ready request for %s after a tagged commit, got %+v", head, snap)
	}
	if out := ufabuildGit(t, dir, "check-ignore", ufabuildFile); out != ufabuildFile {
		t.Errorf("expected %s to be ignored, got %q", ufabuildFile, out)
	}
	if snap.Dirty {
		t.Errorf("the request shouldn't make the repo dirty")
	}
	autoTick(rw)
	if n := buildCount(t, dir); n != 0 {
		t.Fatalf("with auto-rebuild off a request shouldn't build on its own, got %d builds", n)
	}

	rw.opMu.Lock()
	rw.rebuild()
	rw.opMu.Unlock()
	if n := buildCount(t, dir); n != 1 {
		t.Fatalf("expected one build, got %d", n)
	}
	if snap = rw.snapshot(); snap.RebuildReady || snap.UfabuildRequest != "" {
		t.Errorf("expected the request consumed by its build, got %+v", snap)
	}

	commitChange(t, dir, "v4\n")
	autoTick(rw)
	if rw.snapshot().RebuildReady {
		t.Errorf("an untagged commit after a build shouldn't ready another")
	}
}

// TestRepoWatchUfabuildAutoRebuild verifies auto-rebuild builds a request
// only once the 90s debounce runs out, and only once.
func TestRepoWatchUfabuildAutoRebuild(t *testing.T) {
	dir := initTestRepo(t, "echo x >> builds.log")
	rw := newRepoWatch(dir, func() {})
	rw.setAutoRebuild(true)

	commitTagged(t, dir, "v2\n")
	autoTick(rw)
	if snap := rw.snapshot(); !snap.AutoRebuildPending || buildCount(t, dir) != 0 {
		t.Fatalf("expected a pending debounce and no build yet, got %+v (builds=%d)", snap, buildCount(t, dir))
	}
	expireAutoRebuild(rw)
	autoTick(rw)
	if n := buildCount(t, dir); n != 1 {
		t.Fatalf("expected the request built once the debounce ran out, got %d builds", n)
	}
	autoTick(rw)
	expireAutoRebuild(rw)
	autoTick(rw)
	if n := buildCount(t, dir); n != 1 {
		t.Errorf("expected no rebuild without a new request, got %d builds", n)
	}
}

// TestRepoWatchUfabuildFailedNotAutoRetried verifies a request whose build
// fails stays in place for the rebuild button but isn't retried by
// auto-rebuild, while a fresh request is.
func TestRepoWatchUfabuildFailedNotAutoRetried(t *testing.T) {
	dir := initTestRepo(t, "echo x >> builds.log && false")
	rw := newRepoWatch(dir, func() {})
	rw.setAutoRebuild(true)

	commitTagged(t, dir, "v2\n")
	autoTick(rw)
	expireAutoRebuild(rw)
	autoTick(rw)
	if n := buildCount(t, dir); n != 1 {
		t.Fatalf("expected one failed build, got %d", n)
	}
	snap := rw.snapshot()
	if snap.LastError == "" || !snap.RebuildReady || !snap.UfabuildFailed || !strings.Contains(snap.UfabuildNote, "failed") {
		t.Fatalf("expected the failure recorded and the request left ready for a retry, got %+v", snap)
	}
	autoTick(rw)
	expireAutoRebuild(rw)
	autoTick(rw)
	if snap = rw.snapshot(); snap.AutoRebuildPending || buildCount(t, dir) != 1 {
		t.Fatalf("auto-rebuild shouldn't retry a failed request (pending=%v, builds=%d)", snap.AutoRebuildPending, buildCount(t, dir))
	}

	commitTagged(t, dir, "v3\n")
	autoTick(rw)
	if snap = rw.snapshot(); !snap.RebuildReady || snap.UfabuildFailed {
		t.Fatalf("expected a new request ready and not marked failed, got %+v", snap)
	}
	expireAutoRebuild(rw)
	autoTick(rw)
	if n := buildCount(t, dir); n != 2 {
		t.Errorf("expected a new request to build again, got %d builds", n)
	}
}

// TestRepoWatchUfabuildPresentAtStart verifies a request already sitting at
// the repo root when the watcher starts -- one written before a restart, or
// by scripts/ufabuild.sh -- is reported ready, while the commits already
// there aren't scanned into a new one.
func TestRepoWatchUfabuildPresentAtStart(t *testing.T) {
	dir := initTestRepo(t, "true")
	commitTagged(t, dir, "v2\n")
	rw := newRepoWatch(dir, func() {})
	autoTick(rw)
	if rw.snapshot().RebuildReady {
		t.Fatalf("a tagged commit already there at start shouldn't ask for a build")
	}

	if err := os.WriteFile(filepath.Join(dir, ufabuildFile), []byte("commit: abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := newRepoWatch(dir, func() {}).snapshot()
	if !snap.RebuildReady || snap.UfabuildRequest != "abc" {
		t.Errorf("expected an existing request reported ready, got %+v", snap)
	}
}

// TestRepoWatchUfabuildScanRestored verifies a scan point carried across a
// restart (lrState.UfabuildScanned) scans the tagged commits that landed
// meanwhile.
func TestRepoWatchUfabuildScanRestored(t *testing.T) {
	dir := initTestRepo(t, "true")
	before := ufabuildGit(t, dir, "rev-parse", "HEAD")
	commitTagged(t, dir, "v2\n")

	rw := newRepoWatch(dir, func() {})
	rw.setUfabuildScanned(before)
	autoTick(rw)
	if !rw.snapshot().RebuildReady {
		t.Errorf("expected the tagged commit since the restored scan point to write a request")
	}
	if got := rw.ufabuildScannedHead(); got != ufabuildGit(t, dir, "rev-parse", "HEAD") {
		t.Errorf("expected the scan point moved to HEAD, got %s", got)
	}
}

// TestRepoWatchUfabuildBranchSwitch verifies switching to a branch carrying
// tagged commits the old one lacks writes a request -- how a host is moved
// onto a condoc's branch.
func TestRepoWatchUfabuildBranchSwitch(t *testing.T) {
	dir := initTestRepo(t, "true")
	ufabuildGit(t, dir, "branch", "-M", "main")
	ufabuildGit(t, dir, "checkout", "-q", "-b", "condoc")
	commitTagged(t, dir, "v2\n")
	ufabuildGit(t, dir, "checkout", "-q", "main")

	rw := newRepoWatch(dir, func() {})
	if err := rw.switchBranch("condoc"); err != nil {
		t.Fatalf("switchBranch: %v", err)
	}
	autoTick(rw)
	if snap := rw.snapshot(); !snap.RebuildReady || snap.Branch != "condoc" {
		t.Errorf("expected a ready request after switching onto the tagged branch, got %+v", snap)
	}
}

// TestRepoWatchUfabuildTrackedIgnored verifies a tracked .ufabuild -- the
// old committed paradigm -- isn't a request.
func TestRepoWatchUfabuildTrackedIgnored(t *testing.T) {
	dir := initTestRepo(t, "true")
	if err := os.WriteFile(filepath.Join(dir, ufabuildFile), []byte("parent: 1a2b3c4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ufabuildGit(t, dir, "add", ufabuildFile)
	ufabuildGit(t, dir, "commit", "-q", "-m", "ufabuild")
	snap := newRepoWatch(dir, func() {}).snapshot()
	if snap.RebuildReady || snap.UfabuildRequest != "" || !strings.Contains(snap.UfabuildNote, "tracked") {
		t.Errorf("a tracked %s shouldn't be a request, got %+v", ufabuildFile, snap)
	}
}
