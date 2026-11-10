package main

import (
	"strings"
	"testing"
	"time"

	dktypes "dungeon-keeper/pkg/types"
	"dungeon-keeper/pkg/worksignal"
)

func TestTurnSignal(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec PointerSpec
		role string
	}{
		{"idle", PointerSpec{Phase: PhaseAwaitingPrompt}, ""},
		{"queued", PointerSpec{Phase: PhaseLocked, Lock: &InteractionLock{Stage: stageQueued, Role: "r", SignalAt: 1}}, ""},
		{"signalled", PointerSpec{Phase: PhaseLocked, Lock: &InteractionLock{Stage: stageSignalled, Role: "r", SignalAt: 1}}, "r"},
		{"returned", PointerSpec{Phase: PhaseLocked, Lock: &InteractionLock{Stage: stageReturned, Role: "r", SignalAt: 1}}, ""},
		{"drafting", PointerSpec{Phase: PhasePRDrafting, PRRole: "pr", PRSignalAt: 2}, "pr"},
		{"drafting, not signalled yet", PointerSpec{Phase: PhasePRDrafting}, ""},
		{"abandoned", PointerSpec{Phase: PhaseAbandoned, Lock: &InteractionLock{Stage: stageSignalled, Role: "r", SignalAt: 1}}, ""},
	} {
		if role, _ := turnSignal(&tc.spec); role != tc.role {
			t.Errorf("%s: role %q, want %q", tc.name, role, tc.role)
		}
	}
}

// TestAwaitSignalCancelFallback: a cancel the worker never acts on is failed
// by awaitSignal itself once slopCancelGrace has passed.
func TestAwaitSignalCancelFallback(t *testing.T) {
	cfg := &dktypes.Config{WorkSignalsDir: t.TempDir()}
	s := &Server{}
	s.slop.dk = cfg
	s.slop.signals = worksignal.NewManager(cfg)

	created := time.Unix(1793883545, 0)
	path, err := s.slop.signals.Create(&dktypes.WorkSignal{Role: "condocv2-x", AgentType: dktypes.AgentTypeWorker, CreatedAt: created})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.slop.signals.TakeOwnership(path, "wedged"); err != nil {
		t.Fatal(err)
	}
	// Asked long enough ago that the worker should have stopped the agent.
	if err := s.slop.signals.AppendEvent(path, &dktypes.WorkEvent{
		StatusUpdate: dktypes.EventCancelRequested,
		Comment:      "Cancel turn in condocv2",
		Timestamp:    time.Now().Add(-2 * slopCancelGrace),
	}); err != nil {
		t.Fatal(err)
	}

	reply, err := s.awaitSignal("condocv2-x", created.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "the agent run failed: cancelled: Cancel turn in condocv2") {
		t.Errorf("reply %q", reply)
	}
}
