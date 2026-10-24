package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	dktypes "dungeon-keeper/pkg/types"
	"dungeon-keeper/pkg/worksignal"
)

// newDeadWorkerTurn sets up a slopspace server whose turn has signalled a
// worker that will never complete its signal -- the worker died mid-turn.
func newDeadWorkerTurn(t *testing.T) (*Server, *PointerSpec) {
	t.Helper()
	t.Setenv("WORK_SIGNALS_DIR", filepath.Join(t.TempDir(), "work"))
	s := newServer(t.TempDir())
	s.initSlopspace()
	spec := &PointerSpec{Name: "X", StartTime: 1, Turn: &PointerTurn{Kind: turnStep, Stage: stageSignalled}}
	if err := s.submitSignal(spec, dktypes.WorkTypeSlopspace, "", "do it"); err != nil {
		t.Fatal(err)
	}
	return s, spec
}

// TestAwaitSignalAbandonedByForceComplete verifies a turn waiting on a dead
// worker stops waiting once a force complete is flagged for it.
func TestAwaitSignalAbandonedByForceComplete(t *testing.T) {
	s, spec := newDeadWorkerTurn(t)
	stem := spec.Stem()
	s.slop.running = stem
	errc := make(chan error, 1)
	go func() {
		_, err := s.awaitSignal(stem, spec.Turn)
		errc <- err
	}()
	time.Sleep(300 * time.Millisecond)
	if !s.deferForceCompleteRunning(stem) {
		t.Fatal("force complete wasn't held for the running turn")
	}
	select {
	case err := <-errc:
		if !errors.Is(err, errTurnAbandoned) {
			t.Errorf("awaitSignal = %v, want errTurnAbandoned", err)
		}
	case <-time.After(3 * slopTurnPollInterval):
		t.Fatal("awaitSignal kept waiting on the dead worker")
	}
}

// deferForceCompleteRunning is deferForceComplete without the pointer read,
// which needs a pointer repo.
func (s *Server) deferForceCompleteRunning(stem string) bool {
	s.slop.mu.Lock()
	defer s.slop.mu.Unlock()
	if s.slop.running != stem {
		return false
	}
	s.slop.force[stem] = true
	return true
}

// TestAbandonTurnFailsSignal verifies abandoning a turn moves its signal to
// complete/ as failed, so a worker still holding it sees it's not wanted.
func TestAbandonTurnFailsSignal(t *testing.T) {
	s, spec := newDeadWorkerTurn(t)
	created := time.Unix(spec.Turn.SignalAt, 0)
	ongoing := filepath.Join(s.slop.dk.OngoingWorkDir(), worksignal.GenerateFilename(spec.Turn.Role, created, false))
	complete := filepath.Join(s.slop.dk.CompleteWorkDir(), worksignal.GenerateFilename(spec.Turn.Role, created, true))
	meta := &dktypes.SlopspaceMetadata{RootPath: t.TempDir()}
	if _, err := s.abandonTurn(spec, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ongoing); !os.IsNotExist(err) {
		t.Errorf("ongoing signal still there (err=%v)", err)
	}
	signal, _, err := s.slop.signals.Read(complete)
	if err != nil {
		t.Fatal(err)
	}
	if signal.Status != dktypes.WorkStatusFailed {
		t.Errorf("signal status = %v, want %v", signal.Status, dktypes.WorkStatusFailed)
	}
	// Again, as a retry after a later step failed: nothing left to do.
	if _, err := s.abandonTurn(spec, meta); err != nil {
		t.Errorf("second abandon: %v", err)
	}
}
