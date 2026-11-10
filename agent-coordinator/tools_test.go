package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestToolsCommand(t *testing.T) {
	cmd, err := toolsCommand(json.RawMessage("{\n  \"req\": \"ab-1\",\n  \"op\": \"run\",\n  \"tool\": \"materialize-tree\",\n  \"args\": {\"spec\": \"a: |\\n  two\\n\"}\n}"))
	if err != nil {
		t.Fatal(err)
	}
	if cmd != `__tools:run {"req":"ab-1","op":"run","tool":"materialize-tree","args":{"spec":"a: |\n  two\n"}}` {
		t.Errorf("want one compact __tools:run line, got %q", cmd)
	}
	if cmd, err := toolsCommand(json.RawMessage(`{"op":"reinspect"}`)); err != nil || cmd != `__tools:reinspect {"op":"reinspect"}` {
		t.Errorf("reinspect: %q, %v", cmd, err)
	}
	if cmd, err := toolsCommand(json.RawMessage(`{"op":"to-file","run":"r-1"}`)); err != nil || cmd != `__tools:to-file {"op":"to-file","run":"r-1"}` {
		t.Errorf("to-file: %q, %v", cmd, err)
	}
	if cmd, err := toolsCommand(json.RawMessage(`{"op":"history"}`)); err != nil || cmd != `__tools:history {"op":"history"}` {
		t.Errorf("history: %q, %v", cmd, err)
	}
	if cmd, err := toolsCommand(json.RawMessage(`{"op":"history-detail","call":"1793101234-a1b.txt"}`)); err != nil || cmd != `__tools:history-detail {"op":"history-detail","call":"1793101234-a1b.txt"}` {
		t.Errorf("history-detail: %q, %v", cmd, err)
	}
	if cmd, err := toolsCommand(json.RawMessage(`{"op":"recent-calls"}`)); err != nil || cmd != `__tools:recent-calls {"op":"recent-calls"}` {
		t.Errorf("recent-calls: %q, %v", cmd, err)
	}
	if cmd, err := toolsCommand(json.RawMessage(`{"op":"call-detail","path":"/w/my-call"}`)); err != nil || cmd != `__tools:call-detail {"op":"call-detail","path":"/w/my-call"}` {
		t.Errorf("call-detail: %q, %v", cmd, err)
	}
	if cmd, err := toolsCommand(json.RawMessage(`{"op":"example-calls"}`)); err != nil || cmd != `__tools:example-calls {"op":"example-calls"}` {
		t.Errorf("example-calls: %q, %v", cmd, err)
	}
	if cmd, err := toolsCommand(json.RawMessage(`{"op":"demo-run","demo":"robot-cool","variables":{"node":"n2"}}`)); err != nil || cmd != `__tools:demo-run {"op":"demo-run","demo":"robot-cool","variables":{"node":"n2"}}` {
		t.Errorf("demo-run: %q, %v", cmd, err)
	}
	if cmd, err := toolsCommand(json.RawMessage(`{"op":"protosurf"}`)); err != nil || cmd != `__tools:protosurf {"op":"protosurf"}` {
		t.Errorf("protosurf: %q, %v", cmd, err)
	}
	for _, bad := range []string{``, `{not json`, `{"tool":"x"}`, `{"op":"rm -rf"}`, `{"op":"explode"}`} {
		if _, err := toolsCommand(json.RawMessage(bad)); err == nil {
			t.Errorf("want %q refused", bad)
		}
	}
	if _, err := toolsCommand(json.RawMessage(`{"op":"validate","source":"` + strings.Repeat("x", maxWorkerSubmit) + `"}`)); err == nil {
		t.Error("a request past the line limit should be refused")
	}
}

func TestToolsMsgs(t *testing.T) {
	b, _ := json.Marshal(toolsMsg("h1", nil))
	if string(b) != `{"host_id":"h1","tools":null}` {
		t.Errorf("unexpected lr-tools-state: %s", b)
	}
	b, _ = json.Marshal(toolsReplyMsg("h1", json.RawMessage(`{"req":"ab-2","op":"save"}`), errors.New("too long")))
	var got struct {
		HostID string `json:"host_id"`
		Reply  struct {
			Req   string `json:"req"`
			Op    string `json:"op"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"reply"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.HostID != "h1" || got.Reply.Req != "ab-2" || got.Reply.Op != "save" || got.Reply.OK || got.Reply.Error != "too long" {
		t.Errorf("unexpected lr-tools-reply: %s", b)
	}
}
