package main

import (
	"strings"
	"testing"
)

// TestWorkerManagedSpec checks dungeon-keeper is launched one-per-host as
// the "worker" app, in watch mode, wired back to this LR.
func TestWorkerManagedSpec(t *testing.T) {
	spec, ok := managedApps["worker"]
	if !ok {
		t.Fatal("worker should be a managed application")
	}
	if spec.binName != "dungeon-keeper" || !spec.singleton || spec.terminal {
		t.Errorf("worker should be a singleton, non-terminal dungeon-keeper: %+v", spec)
	}
	s := newServer("test-lr")
	s.heartbeatPort = "8082"
	s.devMode = true
	args := strings.Join(spec.buildArgs(s, "worker#1"), " ")
	if !strings.HasPrefix(args, "watch ") {
		t.Errorf("worker buildArgs should start with the watch command: %q", args)
	}
	for _, want := range []string{"--auto-connect", "--lr-port 8082", "--name worker", "--dev-mode"} {
		if !strings.Contains(args, want) {
			t.Errorf("worker buildArgs %q missing %q", args, want)
		}
	}
}

func TestSubmitWorkRefusesEmptyPromptAndMissingWorker(t *testing.T) {
	s := newServer("test-lr")
	if err := s.submitWork(WorkerSubmitRequest{Prompt: "  "}); err == nil {
		t.Error("an empty prompt should be refused")
	}
	if err := s.submitWork(WorkerSubmitRequest{Prompt: "hello"}); err == nil {
		t.Error("submitting with no worker connected should fail")
	}
}

func TestWorkerStateIsKept(t *testing.T) {
	s := newServer("test-lr")
	if s.getWorkerState() != nil {
		t.Fatal("no worker state before dungeon-keeper reports")
	}
	s.setWorkerState(WorkerStateMsg{WorkerID: "abc", Signals: []WorkerSignalInfo{{ID: "1", Status: "completed", Output: "hi"}}})
	got := s.getWorkerState()
	if got == nil || got.WorkerID != "abc" || len(got.Signals) != 1 || got.Signals[0].Output != "hi" {
		t.Errorf("unexpected worker state: %+v", got)
	}
}
