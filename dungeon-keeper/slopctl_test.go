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
