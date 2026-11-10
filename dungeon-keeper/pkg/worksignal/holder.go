package worksignal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"dungeon-keeper/pkg/types"
)

// A running worker holds an flock on workers/<workerID>.lock for as long as
// its process lives. The kernel drops the lock when the process dies --
// however it dies -- so a signal still "processing" under a holder whose
// lock nobody holds was orphaned mid-run: nothing will ever complete it.
// Worker IDs are fresh on every start, so the ID alone can't tell a dead
// holder from a live one.

const workerLockSuffix = ".lock"

func workerLockPath(cfg *types.Config, workerID string) string {
	return filepath.Join(cfg.WorkersDir(), workerID+workerLockSuffix)
}

// HoldWorkerLock takes workerID's lock and returns the file holding it. Keep
// the file open (and referenced) for the life of the worker: closing it, or
// letting it be garbage collected, releases the lock.
func HoldWorkerLock(cfg *types.Config, workerID string) (*os.File, error) {
	if err := os.MkdirAll(cfg.WorkersDir(), 0755); err != nil {
		return nil, fmt.Errorf("failed to create workers dir: %w", err)
	}
	f, err := os.OpenFile(workerLockPath(cfg, workerID), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to create worker lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to take worker lock: %w", err)
	}
	return f, nil
}

// workerAlive reports whether some process holds workerID's lock. A holder
// with no lock file at all is taken as dead: it is a worker from before the
// lock files, or one whose lock was already found stale and removed.
func workerAlive(cfg *types.Config, workerID string) bool {
	f, err := os.OpenFile(workerLockPath(cfg, workerID), os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// FailOrphaned fails every ongoing signal for agentType that a dead worker
// was processing, so whatever waits on its complete/ file (condocv2's turn,
// say) stops waiting. self is the calling worker, never taken as dead. It
// returns the signals failed. The agent the dead worker ran may still be
// running if it outlived it; its process group is not this worker's to kill.
func (m *Manager) FailOrphaned(agentType types.AgentType, self string) ([]string, error) {
	files, err := m.ListOngoing()
	if err != nil {
		return nil, err
	}
	var failed []string
	dead := map[string]bool{}
	for _, path := range files {
		signal, _, err := m.Read(path)
		if err != nil || signal.AgentType != agentType || signal.Status != types.WorkStatusProcessing {
			continue
		}
		holder := signal.Holder
		if holder == "" || holder == self {
			continue
		}
		if _, seen := dead[holder]; !seen {
			dead[holder] = !workerAlive(m.config, holder)
		}
		if !dead[holder] {
			continue
		}
		if err := m.Complete(path, false, fmt.Sprintf("worker %s stopped mid-run (found on worker %s's start)", holder, self)); err != nil {
			return failed, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		failed = append(failed, path)
	}
	m.removeStaleWorkerLocks(self)
	return failed, nil
}

// removeStaleWorkerLocks removes the lock files no process holds, which every
// worker that stopped leaves behind. One made in the last minute is left, in
// case its worker hasn't taken the lock yet.
func (m *Manager) removeStaleWorkerLocks(self string) {
	entries, err := os.ReadDir(m.config.WorkersDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		id, ok := strings.CutSuffix(name, workerLockSuffix)
		if !ok || id == self {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) < time.Minute {
			continue
		}
		if !workerAlive(m.config, id) {
			os.Remove(filepath.Join(m.config.WorkersDir(), name))
		}
	}
}
