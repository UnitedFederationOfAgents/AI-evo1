package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkerSubmitCommand(t *testing.T) {
	cmd, err := workerSubmitCommand("  say \"hi\"\nthere  ")
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(cmd, "__worker:submit ")
	if !ok || strings.Contains(rest, "\n") {
		t.Fatalf("want one __worker:submit line, got %q", cmd)
	}
	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(rest), &req); err != nil || req.Prompt != "say \"hi\"\nthere" {
		t.Errorf("unexpected submit payload %q (%v)", rest, err)
	}

	if _, err := workerSubmitCommand("   "); err == nil {
		t.Error("an empty prompt should be refused")
	}
	if _, err := workerSubmitCommand(strings.Repeat("x", maxWorkerSubmit)); err == nil {
		t.Error("a prompt past the line limit should be refused")
	}
}

func TestWorkerMsgIsNullWithoutState(t *testing.T) {
	b, _ := json.Marshal(workerMsg("h1", nil))
	if string(b) != `{"host_id":"h1","worker":null}` {
		t.Errorf("unexpected lr-worker-state: %s", b)
	}
	b, _ = json.Marshal(workerMsg("h1", json.RawMessage(`{"busy":true}`)))
	if string(b) != `{"host_id":"h1","worker":{"busy":true}}` {
		t.Errorf("unexpected lr-worker-state: %s", b)
	}
}
