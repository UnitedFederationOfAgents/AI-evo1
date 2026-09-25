package main

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeVersionBin writes an executable script that prints out to stdout when
// run (with any arguments, --version included) and returns its path.
func fakeVersionBin(t *testing.T, out string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-bin")
	script := "#!/bin/sh\necho " + out + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSelfVersionWatchPoll verifies poll() flips updateAvailable (and fires
// notify) exactly when the on-disk binary's --version output starts/stops
// disagreeing with the version this process is running -- see
// condocs/initialDistributedDevelopmentImpls/Step3Prompt.md Revision B.
func TestSelfVersionWatchPoll(t *testing.T) {
	bin := fakeVersionBin(t, "v2")

	notified := 0
	w := &selfVersionWatch{binPath: bin, running: "v1", notify: func() { notified++ }}

	w.poll()
	if !w.available() {
		t.Fatal("expected an update to be available: on-disk v2 != running v1")
	}
	if notified != 1 {
		t.Fatalf("notify fired %d times, want 1", notified)
	}

	// Polling again with nothing changed must not re-notify.
	w.poll()
	if notified != 1 {
		t.Fatalf("notify fired %d times after an unchanged poll, want still 1", notified)
	}

	// The running version "catches up" (as it would after an actual
	// restart) -- the verdict flips back and notify fires once more.
	w.running = "v2"
	w.poll()
	if w.available() {
		t.Fatal("expected no update available once running matches the on-disk version")
	}
	if notified != 2 {
		t.Fatalf("notify fired %d times after the flip back, want 2", notified)
	}
}

// TestSelfVersionWatchPollFailure verifies a failing --version invocation
// (e.g. the binary vanished mid-replace) leaves the previous verdict alone
// rather than flipping it.
func TestSelfVersionWatchPollFailure(t *testing.T) {
	notified := 0
	w := &selfVersionWatch{binPath: filepath.Join(t.TempDir(), "does-not-exist"), running: "v1", notify: func() { notified++ }}
	w.poll()
	if w.available() {
		t.Fatal("a failed poll should not report an update available")
	}
	if notified != 0 {
		t.Fatalf("a failed poll should not notify, got %d calls", notified)
	}
}

// TestSelfVersionWatchNilSafe verifies available() is safe to call on a nil
// watch (the case for a process that isn't loader-managed, per
// newSelfVersionWatch's caller in main.go).
func TestSelfVersionWatchNilSafe(t *testing.T) {
	var w *selfVersionWatch
	if w.available() {
		t.Fatal("a nil watch should report no update available")
	}
	if w.autoUpdateEnabled() {
		t.Fatal("a nil watch should report auto-update disabled")
	}
}

// TestSelfVersionWatchAutoUpdateFiresOnPoll verifies that with auto-update on,
// poll() fires restart the moment it notices an update landed -- see
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision E.
func TestSelfVersionWatchAutoUpdateFiresOnPoll(t *testing.T) {
	bin := fakeVersionBin(t, "v2")

	restarted := 0
	w := &selfVersionWatch{binPath: bin, running: "v1", notify: func() {}, restart: func() { restarted++ }}
	w.setAutoUpdate(true)
	if restarted != 0 {
		t.Fatalf("restart fired %d times before any update was available, want 0", restarted)
	}

	w.poll()
	if !w.available() {
		t.Fatal("expected an update to be available: on-disk v2 != running v1")
	}
	if restarted != 1 {
		t.Fatalf("restart fired %d times after the update landed, want 1", restarted)
	}

	// A further unchanged poll must not fire restart again -- the process is
	// expected to actually go down and come back up once restart() runs.
	w.poll()
	if restarted != 1 {
		t.Fatalf("restart fired %d times after an unchanged poll, want still 1", restarted)
	}
}

// TestSelfVersionWatchAutoUpdateFiresOnToggle verifies that turning
// auto-update on while an update is already available fires restart right
// away, rather than waiting for the next poll to notice nothing changed.
func TestSelfVersionWatchAutoUpdateFiresOnToggle(t *testing.T) {
	bin := fakeVersionBin(t, "v2")

	restarted := 0
	w := &selfVersionWatch{binPath: bin, running: "v1", notify: func() {}, restart: func() { restarted++ }}
	w.poll()
	if !w.available() {
		t.Fatal("expected an update to be available: on-disk v2 != running v1")
	}
	if restarted != 0 {
		t.Fatalf("restart fired %d times with auto-update still off, want 0", restarted)
	}

	w.setAutoUpdate(true)
	if restarted != 1 {
		t.Fatalf("restart fired %d times after turning auto-update on with an update already available, want 1", restarted)
	}
	if !w.autoUpdateEnabled() {
		t.Fatal("expected auto-update to read enabled after setAutoUpdate(true)")
	}

	w.setAutoUpdate(false)
	if restarted != 1 {
		t.Fatalf("restart fired %d times after turning auto-update back off, want still 1", restarted)
	}
}

// TestServerSetAutoUpdateNoSelfVersion verifies the Server-level entry points
// (the "set-auto-update" WebSocket message and "__system:auto-update" both
// route through Server.setAutoUpdate) are safe, logged no-ops when this LR
// isn't loader-managed -- s.selfVersion is nil in that case, mirroring
// TestServerRebuildControlsNoRepoWatched in repowatch_test.go.
func TestServerSetAutoUpdateNoSelfVersion(t *testing.T) {
	s := newServer("test-lr")

	// Must not panic on a nil s.selfVersion.
	s.setAutoUpdate(true)
	s.handleSystemCommand("__system:auto-update on")

	if s.systemState().Self.AutoUpdate {
		t.Fatalf("expected AutoUpdate=false with no selfVersion to toggle")
	}
}
