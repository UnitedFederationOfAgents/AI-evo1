package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lockContent reads the '.condoc' lock file's content, failing the test if
// it isn't present.
func lockContent(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".condoc"))
	if err != nil {
		t.Fatalf("expected .condoc lock file to be present: %v", err)
	}
	return string(b)
}

func lockAbsent(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, ".condoc")); !os.IsNotExist(err) {
		t.Fatalf("expected no .condoc lock file, got err=%v", err)
	}
}

// TestUpdateCondocLockFirstSighting verifies condoccer "begins working" on a
// condoc it sees for the first time by creating the lock file -- unless it's
// already sitting at a safe-to-rebuild phase (awaiting_action or completed),
// e.g. after condoccer restarts mid-condoc.
func TestUpdateCondocLockFirstSighting(t *testing.T) {
	s := newServer(t.TempDir())

	s.updateCondocLock(CondocInfo{Path: "X.md", Name: "X", Phase: PhaseAwaitingStep}, "", false)
	content := lockContent(t, s.root)
	if !strings.Contains(content, "Condoccer began work on X") {
		t.Errorf("expected lock content to mention beginning work on X, got %q", content)
	}

	// A fresh Server/condoc pair that's already awaiting_action (or
	// completed) the first time it's observed shouldn't lock at all.
	s2 := newServer(t.TempDir())
	s2.updateCondocLock(CondocInfo{Path: "Y.md", Name: "Y", Phase: PhaseAwaitingAction}, "", false)
	lockAbsent(t, s2.root)

	s3 := newServer(t.TempDir())
	s3.updateCondocLock(CondocInfo{Path: "Z.md", Name: "Z", Phase: PhaseCompleted}, "", false)
	lockAbsent(t, s3.root)
}

// TestUpdateCondocLockPerRepo verifies that with a working dir above two
// repos, each condoc's lock lands at the root of its own repo (where
// local-representative and federation-command look for it), never at the
// scan root, and that one repo's condoc reaching a safe point doesn't release
// the other repo's lock.
func TestUpdateCondocLockPerRepo(t *testing.T) {
	parent := t.TempDir()
	repoA := filepath.Join(parent, "repoA")
	repoB := filepath.Join(parent, "repoB")
	for _, r := range []string{repoA, repoB} {
		if err := os.MkdirAll(filepath.Join(r, ".git"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(r, "condocs"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	s := newServer(parent)

	s.updateCondocLock(CondocInfo{Path: "repoA/condocs/A.md", Name: "A", Phase: PhaseAgentRunning}, "", false)
	s.updateCondocLock(CondocInfo{Path: "repoB/condocs/B.md", Name: "B", Phase: PhaseAgentRunning}, "", false)
	if !strings.Contains(lockContent(t, repoA), "began work on A") {
		t.Error("expected repoA's lock to describe A")
	}
	if !strings.Contains(lockContent(t, repoB), "began work on B") {
		t.Error("expected repoB's lock to describe B")
	}
	lockAbsent(t, parent)

	// A finishing releases only repoA's lock.
	s.updateCondocLock(CondocInfo{Path: "repoA/condocs/A.md", Name: "A", Phase: PhaseAwaitingAction}, PhaseAgentRunning, true)
	lockAbsent(t, repoA)
	lockContent(t, repoB)
}

// TestSetRoot verifies "Set Working Dir" accepts existing directories
// (absolute, or relative to the current root), rejects anything else, and
// clears client subscriptions whose paths were relative to the old root.
func TestSetRoot(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	s := newServer(base)
	c := &wsClient{subscribed: "condocs/X.md", send: make(chan []byte, 8)}
	s.clients[c] = true

	if err := s.setRoot(filepath.Join(base, "missing")); err == nil {
		t.Error("expected an error for a missing dir")
	}
	if err := s.setRoot(file); err == nil {
		t.Error("expected an error for a non-directory")
	}
	if s.getRoot() != base {
		t.Fatalf("root changed on a rejected setRoot: %q", s.getRoot())
	}

	if err := s.setRoot("sub"); err != nil {
		t.Fatalf("setRoot(relative): %v", err)
	}
	if s.getRoot() != sub {
		t.Errorf("root = %q, want %q", s.getRoot(), sub)
	}
	if c.subscribed != "" {
		t.Errorf("expected subscription cleared, got %q", c.subscribed)
	}

	if err := s.setRoot(base); err != nil {
		t.Fatalf("setRoot(absolute): %v", err)
	}
	if s.getRoot() != base {
		t.Errorf("root = %q, want %q", s.getRoot(), base)
	}
}

// TestUpdateCondocLockTransitions verifies the lock file is (re)created on
// ordinary transitions, and removed exactly when a condoc reaches
// awaiting_action (right after an agent finishes) or completed.
func TestUpdateCondocLockTransitions(t *testing.T) {
	s := newServer(t.TempDir())

	// awaiting_step -> agent_running: an ordinary transition, locks.
	s.updateCondocLock(CondocInfo{Path: "X.md", Name: "X", Phase: PhaseAgentRunning}, PhaseAwaitingStep, true)
	content := lockContent(t, s.root)
	if !strings.Contains(content, "Condoccer advanced X to agent_running") {
		t.Errorf("expected lock content to describe the transition, got %q", content)
	}

	// agent_running -> awaiting_action: the agent just finished -- unlocks.
	s.updateCondocLock(CondocInfo{Path: "X.md", Name: "X", Phase: PhaseAwaitingAction}, PhaseAgentRunning, true)
	lockAbsent(t, s.root)

	// awaiting_action -> agent_running (handoff after a revision): locks again.
	s.updateCondocLock(CondocInfo{Path: "X.md", Name: "X", Phase: PhaseAgentRunning}, PhaseAwaitingAction, true)
	lockContent(t, s.root)

	// agent_running -> completed: unlocks.
	s.updateCondocLock(CondocInfo{Path: "X.md", Name: "X", Phase: PhaseCompleted}, PhaseAgentRunning, true)
	lockAbsent(t, s.root)

	// No phase change: leaves whatever state was already there alone.
	s.updateCondocLock(CondocInfo{Path: "X.md", Name: "X", Phase: PhaseCompleted}, PhaseCompleted, true)
	lockAbsent(t, s.root)
}
