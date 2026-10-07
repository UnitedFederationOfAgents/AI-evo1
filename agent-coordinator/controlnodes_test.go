package main

import (
	"reflect"
	"testing"
)

// TestConnectedHostNames: the node list LRs' control tabs offer is the
// connected hosts only, sorted.
func TestConnectedHostNames(t *testing.T) {
	s := newServer()
	for name, connected := range map[string]bool{"lr-b": true, "lr-a": true, "lr-gone": false} {
		hs, _ := s.getOrCreateHost(name)
		hs.mu.Lock()
		hs.connected = connected
		hs.mu.Unlock()
	}
	if got, want := s.connectedHostNames(), []string{"lr-a", "lr-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("connectedHostNames = %v, want %v", got, want)
	}
	if !s.hostConnected("lr-a") || s.hostConnected("lr-gone") || s.hostConnected("nobody") {
		t.Error("hostConnected disagrees with the host states")
	}
}

// TestRelayNodeCaptureWithoutRepresentable: with no representable server
// (nothing to relay through) the relays do nothing rather than panic.
func TestRelayNodeCaptureWithoutRepresentable(t *testing.T) {
	s := newServer()
	hs, _ := s.getOrCreateHost("lr-a")
	hs.mu.Lock()
	hs.connected = true
	hs.mu.Unlock()
	s.relayNodeCapture("lr-a", []byte(`{"req":"r1","node":"lr-b"}`))
	s.relayNodeCapture("lr-a", []byte(`{"req":"r1","node":"lr-a"}`))
	s.relayNodeCaptureResult("lr-b", []byte(`{"req":"r1","from":"lr-a","success":true}`))
	s.relayNodeCapture("lr-a", []byte(`not json`))
	s.broadcastControlNodes()
}
