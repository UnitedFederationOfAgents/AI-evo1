package main

import (
	"strings"
	"testing"
	"time"

	"dungeon-keeper/pkg/types"
)

// TestSlopspaceControlLifecycle: create, deploy, return and delete go
// through the slopspace Manager, a second deploy for the same agent type is
// refused rather than clobbering the first, and a slopspace a local agent is
// working in can't be touched.
func TestSlopspaceControlLifecycle(t *testing.T) {
	w := testWorker(t)
	ctl := func(req SlopspaceControlRequest) (string, error) { return w.slopspaceControl(&req) }

	create := func() string {
		req := SlopspaceControlRequest{Op: "create"}
		if _, err := w.slopspaceControl(&req); err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := w.slopspaceMgr.Get(req.ID); err != nil {
			t.Fatalf("create should name the new slopspace, got %q: %v", req.ID, err)
		}
		return req.ID
	}
	a, b := create(), create()

	if _, err := ctl(SlopspaceControlRequest{Op: "deploy", ID: a}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if m, _ := w.slopspaceMgr.Get(a); !m.Deployed || m.DeployedAgentType != w.config.AgentType {
		t.Errorf("want %s deployed to %s, got %+v", a, w.config.AgentType, m)
	}
	if _, err := ctl(SlopspaceControlRequest{Op: "deploy", ID: b}); err == nil {
		t.Error("deploying a second slopspace for the same agent type should be refused")
	}
	if _, err := ctl(SlopspaceControlRequest{Op: "deploy", ID: b, AgentType: "nonsense"}); err == nil {
		t.Error("an unknown agent type should be refused")
	}

	path, err := w.workSignalMgr.Create(&types.WorkSignal{
		WorkType:  types.WorkTypeSlopspace,
		AgentType: w.config.AgentType,
		Role:      "test",
		Prompt:    "work",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.workSignalMgr.TakeOwnership(path, "test"); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"return", "delete", "write"} {
		if _, err := ctl(SlopspaceControlRequest{Op: op, ID: a}); err == nil || !strings.Contains(err.Error(), "in use") {
			t.Errorf("%s on an in-use slopspace: want refused as in use, got %v", op, err)
		}
	}
	if err := w.workSignalMgr.Complete(path, true, "done"); err != nil {
		t.Fatal(err)
	}

	if _, err := ctl(SlopspaceControlRequest{Op: "return", ID: a}); err != nil {
		t.Fatalf("return: %v", err)
	}
	if m, _ := w.slopspaceMgr.Get(a); m.Deployed {
		t.Error("want it returned")
	}
	if _, err := ctl(SlopspaceControlRequest{Op: "delete", ID: a}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := w.slopspaceMgr.Get(a); err == nil {
		t.Error("want it deleted")
	}

	for _, req := range []SlopspaceControlRequest{
		{Op: "return"},
		{Op: "return", ID: "../" + b},
		{Op: "add-repo", ID: b, Repo: "no-slash", Branch: "x"},
		{Op: "add-repo", ID: b, Repo: "owner/name"},
		{Op: "frobnicate", ID: b},
	} {
		if _, err := ctl(req); err == nil {
			t.Errorf("want %+v refused", req)
		}
	}
	if detail, err := ctl(SlopspaceControlRequest{Op: "write", ID: b}); err != nil || detail != "no writespace repos" {
		t.Errorf("write with no repos: got %q, %v", detail, err)
	}
}

// TestSlopspaceControlCommand: a command needs the slopspace deployed for
// this worker's agent type, queues a write-mode slopspace signal for it, and
// keeps the slopspace from being returned until that signal has run.
func TestSlopspaceControlCommand(t *testing.T) {
	w := testWorker(t)
	ctl := func(req SlopspaceControlRequest) (string, error) { return w.slopspaceControl(&req) }

	req := SlopspaceControlRequest{Op: "create"}
	if _, err := w.slopspaceControl(&req); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := req.ID

	if _, err := ctl(SlopspaceControlRequest{Op: "command", ID: id, Prompt: "hello"}); err == nil {
		t.Error("a command to a stored slopspace should be refused")
	}
	if _, err := ctl(SlopspaceControlRequest{Op: "deploy", ID: id}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if _, err := ctl(SlopspaceControlRequest{Op: "command", ID: id, Prompt: "  "}); err == nil {
		t.Error("an empty command should be refused")
	}
	if _, err := ctl(SlopspaceControlRequest{Op: "command", ID: id, Prompt: "list the repos"}); err != nil {
		t.Fatalf("command: %v", err)
	}

	pending, err := w.workSignalMgr.FindPendingForAgentType(w.config.AgentType)
	if err != nil || len(pending) != 1 {
		t.Fatalf("want one pending signal, got %v, %v", pending, err)
	}
	signal, _, err := w.workSignalMgr.Read(pending[0])
	if err != nil {
		t.Fatal(err)
	}
	if signal.WorkType != types.WorkTypeSlopspace || signal.Role != commandRole || signal.Prompt != "list the repos" || signal.Mode != "write" {
		t.Errorf("unexpected signal %+v", signal)
	}
	if st := w.state(false); len(st.Signals) != 1 || st.Signals[0].Role != commandRole {
		t.Errorf("want the command in worker-state's signals, got %+v", st.Signals)
	}

	for _, op := range []string{"return", "delete", "command"} {
		if _, err := ctl(SlopspaceControlRequest{Op: op, ID: id, Prompt: "again"}); err == nil || !strings.Contains(err.Error(), "waiting") {
			t.Errorf("%s with a command queued: want refused as waiting, got %v", op, err)
		}
	}
	if err := w.workSignalMgr.Complete(pending[0], true, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := ctl(SlopspaceControlRequest{Op: "return", ID: id}); err != nil {
		t.Fatalf("return after the command ran: %v", err)
	}

	other := types.AgentTypeHeuristic
	if w.config.AgentType == other {
		other = types.AgentTypeWorker
	}
	if _, err := ctl(SlopspaceControlRequest{Op: "deploy", ID: id, AgentType: string(other)}); err != nil {
		t.Fatalf("deploy for %s: %v", other, err)
	}
	if _, err := ctl(SlopspaceControlRequest{Op: "command", ID: id, Prompt: "hello"}); err == nil {
		t.Error("a command to a slopspace deployed for another agent type should be refused")
	}
}
