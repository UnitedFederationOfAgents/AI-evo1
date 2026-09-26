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

// commitChange writes file.txt with the given content and commits it,
// moving HEAD forward in dir -- used to get a watcher past its
// newRepoWatch-seeded builtHead so rebuild_ready can go true.
func commitChange(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
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
	run("commit", "-q", "-am", "change")
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
// rebuild-ready derivation against a real git repo: not rebuild_ready right
// after the watcher starts (HEAD hasn't moved since), and not rebuild_ready
// once the repo goes dirty either.
func TestRepoWatchPollAndMaybePull(t *testing.T) {
	dir := initTestRepo(t, "true")
	rw := newRepoWatch(dir, func() {})

	rw.pollAndMaybePull()
	snap := rw.snapshot()
	if snap.Dirty {
		t.Errorf("freshly-committed repo should not be dirty")
	}
	if snap.RebuildReady {
		t.Errorf("expected rebuild_ready=false right after the watcher starts (HEAD hasn't moved since)")
	}
	if snap.Head == "" {
		t.Errorf("expected a HEAD sha to be recorded")
	}

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed := rw.pollAndMaybePull(); !changed {
		t.Fatalf("expected becoming dirty to report a change")
	}
	snap = rw.snapshot()
	if !snap.Dirty {
		t.Errorf("expected dirty after modifying a tracked file")
	}
	if snap.RebuildReady {
		t.Errorf("expected rebuild_ready=false while dirty")
	}
}

// TestRepoWatchRebuildSuccessClearsRebuildReady verifies a successful
// 'make deploy-dev-binaries' run records the built HEAD, clears
// rebuild_ready (a clean repo has nothing left to rebuild), and notifies --
// and that building stays true until the buildCompletionGrace elapses
// (Revision I), only then clearing.
func TestRepoWatchRebuildSuccessClearsRebuildReady(t *testing.T) {
	dir := initTestRepo(t, "true")
	var notified int32
	rw := newRepoWatch(dir, func() { atomic.AddInt32(&notified, 1) })

	// Move HEAD past the watcher's starting point so there's something to
	// build -- a fresh watcher on an unchanged repo is never rebuild_ready
	// (Revision A).
	commitChange(t, dir, "v2\n")
	rw.pollAndMaybePull()
	if !rw.snapshot().RebuildReady {
		t.Fatalf("expected rebuild_ready once HEAD has moved past the watcher's starting point")
	}

	rw.rebuild()
	snap := rw.snapshot()
	if !snap.Building {
		t.Errorf("expected building=true immediately after rebuild() returns -- the completion grace hasn't elapsed yet")
	}
	if snap.LastError != "" {
		t.Errorf("expected no error, got %q", snap.LastError)
	}
	if snap.RebuildReady {
		t.Errorf("expected rebuild_ready=false immediately after a clean successful build")
	}

	// Simulate the completion grace having elapsed, rather than waiting the
	// full 30s for real.
	rw.mu.Lock()
	rw.buildDoneDeadline = time.Now().Add(-time.Second)
	rw.mu.Unlock()
	rw.opMu.Lock()
	rw.maybeFinishBuild()
	rw.opMu.Unlock()

	if snap = rw.snapshot(); snap.Building {
		t.Errorf("expected building=false once the completion grace elapses")
	}
	if got := atomic.LoadInt32(&notified); got < 2 {
		t.Errorf("expected at least 2 notify() calls (build start + end), got %d", got)
	}
}

// TestRepoWatchRebuildFailureRecordsError verifies a failing
// 'make deploy-dev-binaries' is recorded in LastError rather than being
// treated as success, and still leaves building true pending the completion
// grace (Revision I) rather than clearing it outright.
func TestRepoWatchRebuildFailureRecordsError(t *testing.T) {
	dir := initTestRepo(t, "exit 1")
	rw := newRepoWatch(dir, func() {})

	rw.rebuild()
	snap := rw.snapshot()
	if !snap.Building {
		t.Errorf("expected building=true immediately after rebuild() returns -- the completion grace hasn't elapsed yet")
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

// TestRepoWatchRequestRebuildSkipsWhenDirty verifies the operator-driven path
// refuses a rebuild while the repo is dirty even though HEAD has also moved
// -- dirty always wins, per Revision A ("we should not be able to select the
// control" while dirty).
func TestRepoWatchRequestRebuildSkipsWhenDirty(t *testing.T) {
	dir := initTestRepo(t, "true")
	var notified int32
	rw := newRepoWatch(dir, func() { atomic.AddInt32(&notified, 1) })

	commitChange(t, dir, "v2\n") // moves HEAD past builtHead
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v3\n"), 0o644); err != nil {
		t.Fatal(err) // and leaves the repo dirty
	}
	rw.pollAndMaybePull()
	if snap := rw.snapshot(); !snap.Dirty || snap.RebuildReady {
		t.Fatalf("expected dirty=true, rebuild_ready=false, got dirty=%v rebuild_ready=%v", snap.Dirty, snap.RebuildReady)
	}

	before := atomic.LoadInt32(&notified)
	rw.requestRebuild() // async; should be a no-op
	time.Sleep(300 * time.Millisecond)

	if got := atomic.LoadInt32(&notified); got != before {
		t.Errorf("requestRebuild triggered a rebuild despite the repo being dirty (notify count %d -> %d)", before, got)
	}
}

// TestRepoWatchAutoRebuildDebounces verifies that auto-rebuild doesn't fire
// the instant the button becomes active: it arms a ~90s debounce timer
// instead (Step3Prompt.md Revision E).
func TestRepoWatchAutoRebuildDebounces(t *testing.T) {
	dir := initTestRepo(t, "true")
	rw := newRepoWatch(dir, func() {})
	rw.setAutoRebuild(true)

	commitChange(t, dir, "v2\n") // moves HEAD past builtHead
	rw.opMu.Lock()
	rw.pollAndMaybePull()
	if changed := rw.maybeAutoRebuild(); !changed {
		t.Fatalf("expected maybeAutoRebuild to report a change when first arming the debounce timer")
	}
	rw.opMu.Unlock()

	snap := rw.snapshot()
	if !snap.AutoRebuildPending {
		t.Fatalf("expected auto-rebuild to be pending once the button becomes active")
	}
	if snap.Building {
		t.Fatalf("expected no rebuild to have started yet -- the debounce timer should still be counting down")
	}
	if snap.AutoRebuildSeconds < 85 || snap.AutoRebuildSeconds > 90 {
		t.Errorf("expected ~90s remaining, got %d", snap.AutoRebuildSeconds)
	}
}

// TestRepoWatchAutoRebuildFiresWhenDeadlineElapses verifies the debounced
// rebuild actually runs once the timer reaches 0 with auto-rebuild still on.
func TestRepoWatchAutoRebuildFiresWhenDeadlineElapses(t *testing.T) {
	dir := initTestRepo(t, "true")
	rw := newRepoWatch(dir, func() {})
	rw.setAutoRebuild(true)

	commitChange(t, dir, "v2\n")
	rw.opMu.Lock()
	rw.pollAndMaybePull()
	rw.maybeAutoRebuild() // arms the timer
	rw.opMu.Unlock()

	// Simulate the 90s debounce having elapsed, rather than actually
	// waiting on it.
	rw.mu.Lock()
	rw.autoRebuildDeadline = time.Now().Add(-time.Second)
	rw.mu.Unlock()

	rw.opMu.Lock()
	rw.maybeAutoRebuild()
	rw.opMu.Unlock()

	snap := rw.snapshot()
	if snap.AutoRebuildPending {
		t.Errorf("expected the debounce timer to clear once the rebuild fires")
	}
	if snap.RebuildReady {
		t.Errorf("expected rebuild_ready=false after the debounced auto-rebuild ran")
	}
	if snap.LastError != "" {
		t.Errorf("expected no error, got %q", snap.LastError)
	}
}

// TestRepoWatchAutoRebuildResetsOnFurtherChange verifies a further change
// detected while the debounce timer is counting down bumps it back to a
// full 90s rather than letting the original deadline fire.
func TestRepoWatchAutoRebuildResetsOnFurtherChange(t *testing.T) {
	dir := initTestRepo(t, "true")
	rw := newRepoWatch(dir, func() {})
	rw.setAutoRebuild(true)

	commitChange(t, dir, "v2\n")
	rw.opMu.Lock()
	rw.pollAndMaybePull()
	rw.maybeAutoRebuild() // arms the timer
	rw.opMu.Unlock()

	// Pretend the timer is about to fire...
	rw.mu.Lock()
	rw.autoRebuildDeadline = time.Now().Add(time.Second)
	rw.mu.Unlock()

	// ...then a further change lands before it does.
	commitChange(t, dir, "v3\n")
	rw.opMu.Lock()
	rw.pollAndMaybePull()
	if changed := rw.maybeAutoRebuild(); !changed {
		t.Fatalf("expected maybeAutoRebuild to report a change when re-arming on a further change")
	}
	rw.opMu.Unlock()

	snap := rw.snapshot()
	if !snap.AutoRebuildPending {
		t.Fatalf("expected auto-rebuild to still be pending after being bumped back out")
	}
	if snap.Building {
		t.Fatalf("expected no rebuild yet -- the further change should have reset the timer to 90s")
	}
	if snap.AutoRebuildSeconds < 85 {
		t.Errorf("expected the timer to be bumped back close to 90s, got %d", snap.AutoRebuildSeconds)
	}
}

// TestRepoWatchAutoRebuildDisarmsWhenNotReady verifies the pending debounce
// timer is cancelled (not left to fire later) if the repo stops being
// rebuild-ready in the meantime -- e.g. it goes dirty.
func TestRepoWatchAutoRebuildDisarmsWhenNotReady(t *testing.T) {
	dir := initTestRepo(t, "true")
	rw := newRepoWatch(dir, func() {})
	rw.setAutoRebuild(true)

	commitChange(t, dir, "v2\n")
	rw.opMu.Lock()
	rw.pollAndMaybePull()
	rw.maybeAutoRebuild() // arms the timer
	rw.opMu.Unlock()
	if !rw.snapshot().AutoRebuildPending {
		t.Fatalf("expected the timer to be armed before this test's own change")
	}

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v3\n"), 0o644); err != nil {
		t.Fatal(err) // leaves the repo dirty
	}
	rw.opMu.Lock()
	rw.pollAndMaybePull()
	changed := rw.maybeAutoRebuild()
	rw.opMu.Unlock()

	if !changed {
		t.Errorf("expected a reported change when the pending timer is disarmed by going dirty")
	}
	if snap := rw.snapshot(); snap.AutoRebuildPending {
		t.Errorf("expected auto-rebuild pending to clear once the repo goes dirty")
	}
}

// TestRepoWatchAutoRebuildOffStaysDisarmed verifies the debounce timer never
// arms while the auto-rebuild toggle is off.
func TestRepoWatchAutoRebuildOffStaysDisarmed(t *testing.T) {
	dir := initTestRepo(t, "true")
	rw := newRepoWatch(dir, func() {}) // auto-rebuild left off (default)

	commitChange(t, dir, "v2\n")
	rw.opMu.Lock()
	rw.pollAndMaybePull()
	if changed := rw.maybeAutoRebuild(); changed {
		t.Errorf("expected no debounce state change with auto-rebuild off")
	}
	rw.opMu.Unlock()

	if snap := rw.snapshot(); snap.AutoRebuildPending {
		t.Errorf("expected auto-rebuild not pending when the toggle is off")
	}
}

// TestRepoWatchCondocLockForcesRebuildNotReady verifies that a '.condoc'
// lock file at the watched repo's root (see
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md) forces
// rebuild_ready false even once HEAD has moved past builtHead on a clean
// repo -- and that removing it lets rebuild_ready go true again without any
// further git activity.
func TestRepoWatchCondocLockForcesRebuildNotReady(t *testing.T) {
	dir := initTestRepo(t, "true")
	rw := newRepoWatch(dir, func() {})

	commitChange(t, dir, "v2\n") // moves HEAD past builtHead
	rw.pollAndMaybePull()
	if !rw.snapshot().RebuildReady {
		t.Fatalf("expected rebuild_ready once HEAD has moved past the watcher's starting point")
	}

	lockPath := filepath.Join(dir, ".condoc")
	if err := os.WriteFile(lockPath, []byte("Condoccer advanced X to agent_running at ... (0)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if snap := rw.snapshot(); snap.RebuildReady || !snap.CondocLocked {
		t.Fatalf("expected rebuild_ready=false and condoc_locked=true with the lock file present, got rebuild_ready=%v condoc_locked=%v", snap.RebuildReady, snap.CondocLocked)
	}

	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if snap := rw.snapshot(); !snap.RebuildReady || snap.CondocLocked {
		t.Fatalf("expected rebuild_ready=true and condoc_locked=false once the lock file is removed, got rebuild_ready=%v condoc_locked=%v", snap.RebuildReady, snap.CondocLocked)
	}
}

// TestRepoWatchBuildLockDefersCompletionWhilePresent verifies that
// maybeFinishBuild leaves (or sets) building=true and never arms the
// completion grace while 'make deploy-dev-binaries's own '.building' lock
// file is present -- and that removing it arms the grace, which only then
// counts down to building=false (Revision I).
func TestRepoWatchBuildLockDefersCompletionWhilePresent(t *testing.T) {
	dir := initTestRepo(t, "true")
	rw := newRepoWatch(dir, func() {})

	lockPath := filepath.Join(dir, ".building")
	if err := os.WriteFile(lockPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	rw.opMu.Lock()
	changed := rw.maybeFinishBuild()
	rw.opMu.Unlock()
	if !changed {
		t.Errorf("expected maybeFinishBuild to report a change picking up a lock file it didn't itself create")
	}
	if !rw.snapshot().Building {
		t.Fatalf("expected building=true while the '.building' lock file is present")
	}

	rw.opMu.Lock()
	changed = rw.maybeFinishBuild()
	rw.opMu.Unlock()
	if changed {
		t.Errorf("expected no further change while the lock file remains present")
	}

	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	rw.opMu.Lock()
	if changed := rw.maybeFinishBuild(); !changed {
		t.Errorf("expected arming the completion grace to report a change once the lock file disappears")
	}
	rw.opMu.Unlock()
	if !rw.snapshot().Building {
		t.Errorf("expected building to stay true during the completion grace")
	}

	rw.mu.Lock()
	rw.buildDoneDeadline = time.Now().Add(-time.Second)
	rw.mu.Unlock()
	rw.opMu.Lock()
	rw.maybeFinishBuild()
	rw.opMu.Unlock()
	if rw.snapshot().Building {
		t.Errorf("expected building=false once the completion grace elapses")
	}
}

// TestRepoWatchNewRepoWatchSeedsBuildingFromLockFile verifies that a
// '.building' lock file already sitting at the repo root when the watcher
// starts (e.g. a build that outlived an LR restart) is reflected as
// building=true right away, per Revision I.
func TestRepoWatchNewRepoWatchSeedsBuildingFromLockFile(t *testing.T) {
	dir := initTestRepo(t, "true")
	if err := os.WriteFile(filepath.Join(dir, ".building"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	rw := newRepoWatch(dir, func() {})
	if !rw.snapshot().Building {
		t.Errorf("expected building=true when a '.building' lock file already exists at construction")
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
