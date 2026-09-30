package main

import "testing"

func TestTCAvailabilityCommand(t *testing.T) {
	if got := tcAvailabilityCommand(true); got != "__tc-availability:true" {
		t.Fatalf("tcAvailabilityCommand(true) = %q", got)
	}
	if got := tcAvailabilityCommand(false); got != "__tc-availability:false" {
		t.Fatalf("tcAvailabilityCommand(false) = %q", got)
	}
}

// TestHandleTCAvailabilityCommand covers the command relayed down from
// agent-coordinator's own aggregate (see agent-coordinator/tcavailability.go
// and condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step2Prompt.md) being applied and readable back via getTCAvailability, and
// that relaying it on to condoccer doesn't panic before a reprServer exists.
func TestHandleTCAvailabilityCommand(t *testing.T) {
	s := newServer("host-1")

	if s.getTCAvailability() {
		t.Fatal("expected getTCAvailability=false before any command arrives")
	}

	s.handleTCAvailabilityCommand("__tc-availability:true")
	if !s.getTCAvailability() {
		t.Fatal("expected getTCAvailability=true after __tc-availability:true")
	}

	s.handleTCAvailabilityCommand("__tc-availability:false")
	if s.getTCAvailability() {
		t.Fatal("expected getTCAvailability=false after __tc-availability:false")
	}
}
