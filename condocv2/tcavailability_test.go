package main

import (
	"encoding/json"
	"testing"
)

// TestHandleTCAvailabilityCommand covers the command relayed down from
// local-representative (originating at agent-coordinator's own aggregate --
// see agent-coordinator/tcavailability.go and
// condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step2Prompt.md) being applied and readable back via getTCAvailability, and
// that it's broadcast to every connected browser client.
func TestHandleTCAvailabilityCommand(t *testing.T) {
	s := newServer(t.TempDir())

	if s.getTCAvailability() {
		t.Fatal("expected getTCAvailability=false before any command arrives")
	}

	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	s.mu.Lock()
	s.clients[c] = true
	s.mu.Unlock()

	s.handleTCAvailabilityCommand("__tc-availability:true")
	if !s.getTCAvailability() {
		t.Fatal("expected getTCAvailability=true after __tc-availability:true")
	}

	select {
	case raw := <-c.send:
		var m wsMsg
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal wsMsg: %v", err)
		}
		if m.Type != "tc-availability" {
			t.Fatalf("expected a tc-availability broadcast, got %q", m.Type)
		}
		var payload TCAvailabilityMsg
		if err := json.Unmarshal(m.Payload, &payload); err != nil {
			t.Fatalf("unmarshal TCAvailabilityMsg: %v", err)
		}
		if !payload.Available {
			t.Fatal("expected Available=true in the broadcast payload")
		}
	default:
		t.Fatal("expected a tc-availability message to be queued for the connected client")
	}

	s.handleTCAvailabilityCommand("__tc-availability:false")
	if s.getTCAvailability() {
		t.Fatal("expected getTCAvailability=false after __tc-availability:false")
	}
}

// TestHandleReprCommandIgnoresTCAvailabilityAsCondoccerNamespace guards
// against the "__tc-availability:" branch added ahead of the "__condoccer:"
// namespace check in handleReprCommand regressing back to a plain prefix
// check that would swallow it into the condoccer-only warning log.
func TestHandleReprCommandIgnoresTCAvailabilityAsCondoccerNamespace(t *testing.T) {
	s := newServer(t.TempDir())
	s.handleReprCommand("__tc-availability:true") // must not panic
	if !s.getTCAvailability() {
		t.Fatal("expected handleReprCommand to route __tc-availability: through to handleTCAvailabilityCommand")
	}
}
