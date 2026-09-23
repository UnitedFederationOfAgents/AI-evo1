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
// condocs/initialDistributedDevelopmentImpls/Step4Prompt.md Revision E.
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
}
