package main

import (
	"encoding/json"
	"testing"
)

func TestFCTargeted(t *testing.T) {
	if got := fcTargeted("", "ls"); got != "ls" {
		t.Errorf("untargeted = %q, want ls", got)
	}
	if got := fcTargeted("federation-command#2", "__ridealong:execute"); got != "__fc:federation-command#2 __ridealong:execute" {
		t.Errorf("targeted = %q", got)
	}
}

func TestFCInstancesMsgDefaultsToEmptyList(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage("null")} {
		b, _ := json.Marshal(fcInstancesMsg("host-a", raw))
		if string(b) != `{"host_id":"host-a","instances":[]}` {
			t.Errorf("fcInstancesMsg(%q) = %s", raw, b)
		}
	}
	b, _ := json.Marshal(fcInstancesMsg("host-a", json.RawMessage(`[{"key":"federation-command#1"}]`)))
	if string(b) != `{"host_id":"host-a","instances":[{"key":"federation-command#1"}]}` {
		t.Errorf("fcInstancesMsg = %s", b)
	}
}
