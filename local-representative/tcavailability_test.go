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

	s.convoMu.Lock()
	s.convoState = &ConvoStateMsg{HTTPPort: "8086", TranscribeAvailable: true}
	s.convoMu.Unlock()

	s.handleTCAvailabilityCommand("__tc-availability:true")
	if !s.getTCAvailability() {
		t.Fatal("expected getTCAvailability=true after __tc-availability:true")
	}

	s.handleTCAvailabilityCommand("__tc-availability:false")
	if s.getTCAvailability() {
		t.Fatal("expected getTCAvailability=false after __tc-availability:false")
	}
}

// TestTCAvailabilityGatedOnLocalTranscribe covers Step5Prompt.md Revision
// B: even with agent-coordinator reporting TC available somewhere, this
// LR's mics stay dark unless its own the-conversationalist is running and
// can reach its transcription engine -- they all capture through this LR's
// own /convo/ proxy.
func TestTCAvailabilityGatedOnLocalTranscribe(t *testing.T) {
	s := newServer("host-1")
	s.handleTCAvailabilityCommand("__tc-availability:true")

	if s.getTCAvailability() {
		t.Fatal("expected getTCAvailability=false with no local the-conversationalist")
	}

	s.convoMu.Lock()
	s.convoState = &ConvoStateMsg{HTTPPort: "8086", TranscribeAvailable: false, TranscribeReason: "no AWS credentials found"}
	s.convoMu.Unlock()
	if s.getTCAvailability() {
		t.Fatal("expected getTCAvailability=false while the local TC can't reach its transcription engine")
	}

	s.convoMu.Lock()
	s.convoState = &ConvoStateMsg{HTTPPort: "8086", TranscribeAvailable: true}
	s.convoMu.Unlock()
	if !s.getTCAvailability() {
		t.Fatal("expected getTCAvailability=true once the local TC reports transcription available")
	}
}
