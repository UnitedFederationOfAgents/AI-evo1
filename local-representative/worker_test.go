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
	if err := s.submitWork(WorkerSubmitRequest{Prompt: strings.Repeat("x", maxWorkerPrompt+1)}); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("a prompt past the line limit should be refused as too long, got %v", err)
	}
}

// TestHandleWorkerCommandWithoutAC: agent-coordinator's submits are answered
// over the AC link; with none (or a malformed submit) nothing should panic.
func TestHandleWorkerCommandWithoutAC(t *testing.T) {
	s := newServer("test-lr")
	s.handleWorkerCommand(`__worker:submit {"prompt":"hello"}`)
	s.handleWorkerCommand(`__worker:submit {not json`)
	s.handleWorkerCommand(`__worker:unknown`)
	s.handleWorkerCommand(`__worker:slopspace {"op":"create"}`)
	s.handleWorkerCommand(`__worker:slopspace {not json`)
}

func TestControlSlopspaceRefusesBadRequestsAndMissingWorker(t *testing.T) {
	s := newServer("test-lr")
	if err := s.controlSlopspace(WorkerSlopspaceRequest{Op: "frobnicate", ID: "x"}); err == nil {
		t.Error("an unknown op should be refused")
	}
	if err := s.controlSlopspace(WorkerSlopspaceRequest{Op: "deploy"}); err == nil {
		t.Error("an op on no slopspace should be refused")
	}
	if err := s.controlSlopspace(WorkerSlopspaceRequest{Op: "command", ID: "x", Prompt: " "}); err == nil || !strings.Contains(err.Error(), "enter a command") {
		t.Errorf("a command with no prompt should be refused, got %v", err)
	}
	if err := s.controlSlopspace(WorkerSlopspaceRequest{Op: "command", ID: "x", Prompt: "hi"}); err == nil || !strings.Contains(err.Error(), "isn't connected") {
		t.Errorf("a command is a known op; with no worker connected want refused as not connected, got %v", err)
	}
	if err := s.controlSlopspace(WorkerSlopspaceRequest{Op: "create"}); err == nil || !strings.Contains(err.Error(), "isn't connected") {
		t.Errorf("with no worker connected want refused as not connected, got %v", err)
	}
	// Revision H: reviewing DTT proposals, and browsing files.
	if err := s.controlSlopspace(WorkerSlopspaceRequest{Op: "review", ID: "x", Decision: "maybe"}); err == nil || !strings.Contains(err.Error(), "unknown review decision") {
		t.Errorf("an unknown decision should be refused, got %v", err)
	}
	if err := s.controlSlopspace(WorkerSlopspaceRequest{Op: "review", ID: "x", Decision: "modify", Note: " "}); err == nil || !strings.Contains(err.Error(), "needs a note") {
		t.Errorf("modify without a note should be refused, got %v", err)
	}
	for _, req := range []WorkerSlopspaceRequest{
		{Op: "review", ID: "x", Decision: "accept"},
		{Op: "review", ID: "x", Decision: "modify", Note: "rename it"},
		{Op: "files", ID: "x", Path: "writespaces"},
	} {
		if err := s.controlSlopspace(req); err == nil || !strings.Contains(err.Error(), "isn't connected") {
			t.Errorf("%+v is a known op; with no worker connected want refused as not connected, got %v", req, err)
		}
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

func TestFinishedSessionPrompts(t *testing.T) {
	prev := &WorkerStateMsg{Signals: []WorkerSignalInfo{
		{ID: "a", Role: sessionPromptRole, Session: "s1", Status: "processing"},
		{ID: "b", Role: sessionPromptRole, Session: "s2", Status: "pending"},
		{ID: "c", Role: "worker-tab", Status: "processing"},
		{ID: "d", Role: sessionPromptRole, Session: "s4", Status: "completed"},
	}}
	next := WorkerStateMsg{Signals: []WorkerSignalInfo{
		{ID: "a", Role: sessionPromptRole, Session: "s1", Status: "completed"},
		{ID: "b", Role: sessionPromptRole, Session: "s2", Status: "failed", Comment: "boom"},
		{ID: "c", Role: "worker-tab", Status: "completed"},
		// d was already done before; e was never seen running.
		{ID: "d", Role: sessionPromptRole, Session: "s4", Status: "completed"},
		{ID: "e", Role: sessionPromptRole, Session: "s5", Status: "completed"},
	}}
	got := finishedSessionPrompts(prev, next)
	if len(got) != 2 {
		t.Fatalf("want the 2 session prompts that just finished, got %+v", got)
	}
	if got[0].Session != "s1" || !got[0].OK || got[0].Status != "completed" {
		t.Errorf("unexpected first: %+v", got[0])
	}
	if got[1].Session != "s2" || got[1].OK || got[1].Comment != "boom" {
		t.Errorf("unexpected second: %+v", got[1])
	}
	if finishedSessionPrompts(nil, next) != nil {
		t.Error("with no earlier state nothing has been seen finishing")
	}
}

func TestSubmitSessionPromptWithoutWorkerDoesNotPanic(t *testing.T) {
	s := newServer("test-lr")
	// No worker or session-manager connected: the reply has nowhere to go.
	s.submitSessionPrompt(SessionPromptRequest{Session: "s1", Prompt: "hi"})
	s.submitSessionPrompt(SessionPromptRequest{Prompt: "hi"})
}
