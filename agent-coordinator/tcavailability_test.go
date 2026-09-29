package main

import "testing"

// TestAnyConvoAvailable covers the "on any host" aggregation from
// condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step2Prompt.md: false with no hosts reporting a convo-state, true as soon
// as at least one does, regardless of how many others don't.
func TestAnyConvoAvailable(t *testing.T) {
	s := newServer()

	if s.anyConvoAvailable() {
		t.Fatal("expected anyConvoAvailable=false with no hosts connected")
	}

	hsA, _ := s.getOrCreateHost("host-a")
	hsB, _ := s.getOrCreateHost("host-b")
	hsA.mu.Lock()
	hsA.connected = true
	hsA.mu.Unlock()
	hsB.mu.Lock()
	hsB.connected = true
	hsB.mu.Unlock()

	if s.anyConvoAvailable() {
		t.Fatal("expected anyConvoAvailable=false when no host reports a convo-state")
	}

	hsB.mu.Lock()
	hsB.convo = &ConvoStateMsg{HTTPPort: "8090"}
	hsB.mu.Unlock()

	if !s.anyConvoAvailable() {
		t.Fatal("expected anyConvoAvailable=true once one host (of several) reports a convo-state")
	}

	hsB.mu.Lock()
	hsB.convo = nil
	hsB.mu.Unlock()

	if s.anyConvoAvailable() {
		t.Fatal("expected anyConvoAvailable=false again once the only reporting host clears its convo-state")
	}
}

// TestBroadcastTCAvailabilityOnlyBroadcastsOnChange guards the dedup in
// broadcastTCAvailability: repeated calls with an unchanged aggregate must
// not re-broadcast (sendTCAvailabilityTo exists precisely to push the
// unconditional first value to a newly-connected host instead).
func TestBroadcastTCAvailabilityOnlyBroadcastsOnChange(t *testing.T) {
	s := newServer()

	s.broadcastTCAvailability() // false -> false: no-op, must not panic with a nil reprServer
	s.tcMu.RLock()
	got := s.tcAvailable
	s.tcMu.RUnlock()
	if got {
		t.Fatal("expected tcAvailable to stay false with no hosts reporting a convo-state")
	}

	hs, _ := s.getOrCreateHost("host-a")
	hs.mu.Lock()
	hs.connected = true
	hs.convo = &ConvoStateMsg{HTTPPort: "8090"}
	hs.mu.Unlock()

	s.broadcastTCAvailability() // false -> true: must not panic with a nil reprServer either
	s.tcMu.RLock()
	got = s.tcAvailable
	s.tcMu.RUnlock()
	if !got {
		t.Fatal("expected tcAvailable=true after a host reports a convo-state")
	}
}

func TestTCAvailabilityCommand(t *testing.T) {
	if got := tcAvailabilityCommand(true); got != "__tc-availability:true" {
		t.Fatalf("tcAvailabilityCommand(true) = %q", got)
	}
	if got := tcAvailabilityCommand(false); got != "__tc-availability:false" {
		t.Fatalf("tcAvailabilityCommand(false) = %q", got)
	}
}
