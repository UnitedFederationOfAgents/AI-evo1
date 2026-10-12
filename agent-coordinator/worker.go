package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The per-host worker tab (condocs/initialRobotImpls/Step5SubstepGPrompt.md,
// Revision A): the same panel as local-representative's worker tab. That
// host's dungeon-keeper reports its work signals to LR, which forwards them
// here as "worker-state" -- relayed to browsers as-is, like control-state.
// The temporary prompt box submits through LR as "__worker:submit <json>",
// and LR answers with "worker-reply". See local-representative/worker.go.

// maxWorkerSubmit caps a relayed submit: representable reads a command a
// line at a time, up to 64 KiB.
const maxWorkerSubmit = 48 << 10

// LRWorkerMsg is the host-scoped "lr-worker-state" message: that host's
// last worker-state, or null when none has been reported (or the host
// disconnected).
type LRWorkerMsg struct {
	HostID string          `json:"host_id"`
	Worker json.RawMessage `json:"worker"`
}

// LRWorkerReplyMsg is the host-scoped "lr-worker-reply" message: whether a
// submit was handed to that host's dungeon-keeper. Every browser gets it;
// the work itself shows up in the next lr-worker-state.
type LRWorkerReplyMsg struct {
	HostID string          `json:"host_id"`
	Reply  json.RawMessage `json:"reply"`
}

// workerMsg builds a host-scoped lr-worker-state payload; empty means no
// worker-state yet, sent as null.
func workerMsg(hostID string, raw json.RawMessage) LRWorkerMsg {
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	return LRWorkerMsg{HostID: hostID, Worker: raw}
}

// workerReplyMsg builds an lr-worker-reply carrying an error AC itself
// found, before the submit reached the host.
func workerReplyMsg(hostID string, err error) LRWorkerReplyMsg {
	b, _ := json.Marshal(map[string]any{"ok": false, "error": err.Error()})
	return LRWorkerReplyMsg{HostID: hostID, Reply: b}
}

// workerSubmitCommand builds the "__worker:submit <json>" command for a
// browser's submit; LR checks the rest (an empty prompt, no worker running).
func workerSubmitCommand(prompt string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", errors.New("enter a prompt first")
	}
	b, err := json.Marshal(struct {
		Prompt string `json:"prompt"`
	}{prompt})
	if err != nil {
		return "", err
	}
	if len(b) > maxWorkerSubmit {
		return "", fmt.Errorf("the prompt is too long to send through agent-coordinator (%d KiB, the limit is %d KiB)", len(b)>>10, maxWorkerSubmit>>10)
	}
	return "__worker:submit " + string(b), nil
}
