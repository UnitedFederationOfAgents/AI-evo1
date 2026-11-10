package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dungeon-keeper/pkg/slopspace"
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
	if _, err := ctl(SlopspaceControlRequest{Op: "command", ID: id, Prompt: "list the repos", Agent: "pi", Model: "anthropic/sonnet"}); err != nil {
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
	if signal.WorkType != types.WorkTypeSlopspace || signal.Role != commandRole || signal.Prompt != "list the repos" || signal.Mode != "write" || signal.Agent != "pi" || signal.Model != "anthropic/sonnet" {
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

// TestSlopspaceControlReview (dTT-IntegrationImpls Step 1, Revision H): a
// slopspace returned with DTT proposals can't be deployed until reviewed;
// requesting modification deploys it again and queues the note for the
// agent; worker-state reports what awaits review and how it was answered.
func TestSlopspaceControlReview(t *testing.T) {
	w := testWorker(t)
	ctl := func(req SlopspaceControlRequest) (string, error) { return w.slopspaceControl(&req) }

	req := SlopspaceControlRequest{Op: "create"}
	if _, err := w.slopspaceControl(&req); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := req.ID
	propose := func() {
		t.Helper()
		if _, err := ctl(SlopspaceControlRequest{Op: "deploy", ID: id}); err != nil {
			t.Fatalf("deploy: %v", err)
		}
		dir := filepath.Join(w.config.DeployPathForAgentType(w.config.AgentType), slopspace.WriteSpacesDir, slopspace.DTTRequestsDir, slopspace.DTTWriteDir)
		if err := os.WriteFile(filepath.Join(dir, "repo.dtt.yaml"), []byte("assert: repo\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if detail, err := ctl(SlopspaceControlRequest{Op: "return", ID: id}); err != nil || !strings.Contains(detail, "1 DTT proposal") {
			t.Fatalf("return: got %q, %v", detail, err)
		}
	}
	propose()

	if _, err := ctl(SlopspaceControlRequest{Op: "deploy", ID: id}); err == nil || !strings.Contains(err.Error(), "awaiting review") {
		t.Errorf("deploy with proposals pending: want refused as awaiting review, got %v", err)
	}
	st := w.state(false)
	if len(st.Slopspaces) != 1 || len(st.Slopspaces[0].PendingProposals) != 1 || st.Slopspaces[0].PendingProposals[0] != "write/repo.dtt.yaml" {
		t.Fatalf("want the proposal in worker-state, got %+v", st.Slopspaces)
	}
	if _, err := ctl(SlopspaceControlRequest{Op: "review", ID: id, Decision: "modify"}); err == nil {
		t.Error("modify without a note should be refused")
	}

	detail, err := ctl(SlopspaceControlRequest{Op: "review", ID: id, Decision: "modify", Note: "name it by date"})
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	if !strings.Contains(detail, "queued") {
		t.Errorf("modify detail = %q", detail)
	}
	if m, _ := w.slopspaceMgr.Get(id); !m.Deployed {
		t.Error("modify should deploy the slopspace again")
	}
	pending, err := w.workSignalMgr.FindPendingForAgentType(w.config.AgentType)
	if err != nil || len(pending) != 1 {
		t.Fatalf("want one pending signal, got %v, %v", pending, err)
	}
	signal, _, _ := w.workSignalMgr.Read(pending[0])
	if signal.Role != reviewRole || !strings.Contains(signal.Prompt, "name it by date") || !strings.Contains(signal.Prompt, "write/repo.dtt.yaml") {
		t.Errorf("unexpected signal %+v", signal)
	}
	if err := w.workSignalMgr.Complete(pending[0], true, "done"); err != nil {
		t.Fatal(err)
	}

	// Browsing works while deployed, and sees the proposal for the agent.
	reply := w.browseFiles(SlopspaceControlRequest{Op: "files", ID: id, Path: "writespaces/dtt-requests/write/repo.dtt.yaml"})
	if !reply.OK || reply.Files == nil || reply.Files.Content != "assert: repo\n" {
		t.Errorf("browse: %+v", reply)
	}
	if reply := w.browseFiles(SlopspaceControlRequest{Op: "files", ID: id, Path: "SLOPSPACE_ID"}); reply.OK {
		t.Error("browsing outside the spaces should fail")
	}

	// Returned unchanged, they await review again; accepting answers them.
	if _, err := ctl(SlopspaceControlRequest{Op: "return", ID: id}); err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := ctl(SlopspaceControlRequest{Op: "review", ID: id, Decision: "accept"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	st = w.state(false)
	ds := st.Slopspaces[0].Deployments
	if len(st.Slopspaces[0].PendingProposals) != 0 || len(ds) != 2 ||
		ds[0].Decision != "modify" || ds[0].Note != "name it by date" || ds[1].Decision != "accept" || ds[1].Proposals != 1 || ds[1].DecidedAt == 0 {
		t.Errorf("want both reviews in worker-state, got %+v", st.Slopspaces[0])
	}
	if _, err := ctl(SlopspaceControlRequest{Op: "deploy", ID: id}); err != nil {
		t.Errorf("deploy after accepting: %v", err)
	}
}

// TestSlopspaceControlDeployDTTProposals (Revision J): deploy can turn DTT
// proposals off or on for that deployment, absent keeps the setting, and
// worker-state reports it.
func TestSlopspaceControlDeployDTTProposals(t *testing.T) {
	w := testWorker(t)
	ctl := func(req SlopspaceControlRequest) (string, error) { return w.slopspaceControl(&req) }
	req := SlopspaceControlRequest{Op: "create"}
	if _, err := w.slopspaceControl(&req); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := req.ID
	requests := filepath.Join(w.config.DeployPathForAgentType(w.config.AgentType), slopspace.WriteSpacesDir, slopspace.DTTRequestsDir)
	off, on := false, true

	for _, c := range []struct {
		set  *bool
		want bool
	}{{&off, false}, {nil, false}, {&on, true}, {nil, true}} {
		detail, err := ctl(SlopspaceControlRequest{Op: "deploy", ID: id, DTTProposals: c.set})
		if err != nil {
			t.Fatalf("deploy: %v", err)
		}
		if got := strings.Contains(detail, "without DTT proposals"); got == c.want {
			t.Errorf("deploy %v: detail %q", c.set, detail)
		}
		if _, err := os.Stat(requests); (err == nil) != c.want {
			t.Errorf("deploy %v: dtt-requests deployed = %v, want %v", c.set, err == nil, c.want)
		}
		if st := w.state(false); len(st.Slopspaces) != 1 || st.Slopspaces[0].DTTProposals != c.want {
			t.Errorf("deploy %v: worker-state %+v", c.set, st.Slopspaces)
		}
		if _, err := ctl(SlopspaceControlRequest{Op: "return", ID: id}); err != nil {
			t.Fatalf("return: %v", err)
		}
	}
}
