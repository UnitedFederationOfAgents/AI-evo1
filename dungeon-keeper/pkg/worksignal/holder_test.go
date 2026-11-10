package worksignal

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dungeon-keeper/pkg/types"
)

// processingSignal creates an ongoing signal held by holder, as a worker that
// took it would leave it.
func processingSignal(t *testing.T, m *Manager, role string, at time.Time, agentType types.AgentType, holder string) string {
	t.Helper()
	path, err := m.Create(&types.WorkSignal{Role: role, AgentType: agentType, CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.TakeOwnership(path, holder); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFailOrphaned(t *testing.T) {
	cfg := &types.Config{WorkSignalsDir: t.TempDir()}
	m := NewManager(cfg)
	at := time.Unix(1793883545, 0)

	live, err := HoldWorkerLock(cfg, "live")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	dead, err := HoldWorkerLock(cfg, "dead")
	if err != nil {
		t.Fatal(err)
	}
	dead.Close() // the kernel drops the lock, as it would for a dead process

	held := processingSignal(t, m, "held", at, types.AgentTypeWorker, "live")
	orphan := processingSignal(t, m, "orphan", at, types.AgentTypeWorker, "dead")
	nolock := processingSignal(t, m, "nolock", at, types.AgentTypeWorker, "prelock") // a worker from before lock files
	mine := processingSignal(t, m, "mine", at, types.AgentTypeWorker, "self")
	other := processingSignal(t, m, "other", at, types.AgentTypeHeuristic, "dead")
	pending, err := m.Create(&types.WorkSignal{Role: "pending", AgentType: types.AgentTypeWorker, CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}

	failed, err := m.FailOrphaned(types.AgentTypeWorker, "self")
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 2 {
		t.Fatalf("failed %v, want the orphan and nolock signals", failed)
	}
	for _, path := range []string{held, mine, other, pending} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s should still be ongoing: %v", filepath.Base(path), err)
		}
	}
	for _, role := range []string{"orphan", "nolock"} {
		complete := filepath.Join(cfg.CompleteWorkDir(), GenerateFilename(role, at, true))
		signal, _, err := m.Read(complete)
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if signal.Status != types.WorkStatusFailed {
			t.Errorf("%s: status %s, want failed", role, signal.Status)
		}
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan should have left ongoing/")
	}
	if _, err := os.Stat(nolock); !os.IsNotExist(err) {
		t.Errorf("nolock should have left ongoing/")
	}
}

func TestRemoveStaleWorkerLocks(t *testing.T) {
	cfg := &types.Config{WorkSignalsDir: t.TempDir()}
	m := NewManager(cfg)
	old := time.Now().Add(-time.Hour)

	live, err := HoldWorkerLock(cfg, "live")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	for _, id := range []string{"stale", "fresh"} {
		f, err := HoldWorkerLock(cfg, id)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	for _, id := range []string{"live", "stale"} {
		os.Chtimes(workerLockPath(cfg, id), old, old)
	}

	m.removeStaleWorkerLocks("self")
	for id, want := range map[string]bool{"live": true, "stale": false, "fresh": true} {
		_, err := os.Stat(workerLockPath(cfg, id))
		if got := err == nil; got != want {
			t.Errorf("%s lock exists = %v, want %v", id, got, want)
		}
	}
}

func TestCancelRequest(t *testing.T) {
	cfg := &types.Config{WorkSignalsDir: t.TempDir()}
	m := NewManager(cfg)
	path, err := m.Create(&types.WorkSignal{Role: "r", AgentType: types.AgentTypeWorker})
	if err != nil {
		t.Fatal(err)
	}
	_, events, _ := m.Read(path)
	if CancelRequest(events) != nil {
		t.Fatal("a new signal has no cancel request")
	}
	if err := m.RequestCancel(path, "from the UI"); err != nil {
		t.Fatal(err)
	}
	_, events, _ = m.Read(path)
	ev := CancelRequest(events)
	if ev == nil {
		t.Fatal("cancel request not found")
	}
	if got := CancelReason(ev); got != "cancelled: from the UI" {
		t.Errorf("reason %q", got)
	}
	if got := CancelReason(&types.WorkEvent{}); got != "cancelled" {
		t.Errorf("blank reason %q", got)
	}
}
