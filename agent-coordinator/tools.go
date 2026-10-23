package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
)

// The per-host tools tab: the same panel as local-representative's tools
// tab (local-representative/tools.go, frontend/src/Tools.tsx). That host's
// LR reports its catalog and recent runs here as "tools-state" -- relayed
// to browsers as-is, like worker-state. Its requests go through LR as
// "__tools:<op> <json>" (run, reinspect, save, describe, validate,
// install, create), and LR answers with "tools-reply". LR applies its
// tools-allow setting to runs that come this way.

// toolsOpRe is the ops LR's tools tab takes, which become the command's
// prefix.
var toolsOpRe = regexp.MustCompile(`^(reinspect|run|save|describe|validate|install|create)$`)

// LRToolsMsg is the host-scoped "lr-tools-state" message: that host's last
// tools-state, or null when none has been reported (or the host
// disconnected).
type LRToolsMsg struct {
	HostID string          `json:"host_id"`
	Tools  json.RawMessage `json:"tools"`
}

// LRToolsReplyMsg is the host-scoped "lr-tools-reply" message: LR's answer
// to one request. Every browser gets it; each picks out its own by req.
type LRToolsReplyMsg struct {
	HostID string          `json:"host_id"`
	Reply  json.RawMessage `json:"reply"`
}

// toolsMsg builds a host-scoped lr-tools-state payload; empty means no
// tools-state yet, sent as null.
func toolsMsg(hostID string, raw json.RawMessage) LRToolsMsg {
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	return LRToolsMsg{HostID: hostID, Tools: raw}
}

// toolsReplyMsg builds an lr-tools-reply carrying an error AC itself found,
// before the request reached the host. It echoes the request's req and op
// so the asking browser can match it up.
func toolsReplyMsg(hostID string, req json.RawMessage, err error) LRToolsReplyMsg {
	var probe struct {
		Req string `json:"req"`
		Op  string `json:"op"`
	}
	_ = json.Unmarshal(req, &probe)
	b, _ := json.Marshal(map[string]any{"req": probe.Req, "op": probe.Op, "ok": false, "error": err.Error()})
	return LRToolsReplyMsg{HostID: hostID, Reply: b}
}

// toolsCommand builds the "__tools:<op> <json>" command for a browser's
// request. The request is passed on as-is, compacted onto one line (a
// command is read a line at a time); LR checks the rest.
func toolsCommand(req json.RawMessage) (string, error) {
	var probe struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(req, &probe); err != nil || probe.Op == "" {
		return "", errors.New("bad tools request")
	}
	if !toolsOpRe.MatchString(probe.Op) {
		return "", errors.New("unknown tools op " + probe.Op)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, req); err != nil {
		return "", err
	}
	if buf.Len() > maxWorkerSubmit {
		return "", errors.New("the tools request is too long to send through agent-coordinator -- use that host's own tools tab")
	}
	return "__tools:" + probe.Op + " " + buf.String(), nil
}
