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
