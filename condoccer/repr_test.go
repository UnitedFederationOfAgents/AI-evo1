package main

import (
	"testing"
	"time"
)

// TestPushCondoccerStateNoClient verifies the state push is a safe no-op before
// the representable link to local-representative is established.
func TestPushCondoccerStateNoClient(t *testing.T) {
	s := newServer(t.TempDir())
	s.httpPort = "8080"
	s.name = "condoccer"
	s.pushCondoccerState() // must not panic or block
}

// TestHandleReprCommandToleratesJunk verifies condoccer only acts on the
// "__condoccer:" namespace and ignores everything else without panicking.
func TestHandleReprCommandToleratesJunk(t *testing.T) {
	s := newServer(t.TempDir())
	s.name = "condoccer"
	for _, cmd := range []string{
		"",
		"plain-command",
		"__ridealong:next",
		"__condoccer:",
		"__condoccer:bogus arg",
		"__condoccer:refresh",
		"__condoccer:action {not json}",
	} {
		s.handleReprCommand(cmd)
	}
}

// TestCondoccerStateMsgShape is a compile-time guard that the wire payload keeps
// the fields the rest of the stack reads (http_port drives the reverse proxy).
func TestCondoccerStateMsgShape(t *testing.T) {
	m := CondoccerStateMsg{HTTPPort: "8080", Root: "/repo", Condocs: []CondocInfo{{Path: "X.md", Phase: "proposed"}}}
	if m.HTTPPort != "8080" || m.Root != "/repo" || len(m.Condocs) != 1 {
		t.Fatalf("unexpected CondoccerStateMsg round-trip: %+v", m)
	}
}

// TestStartConnectLoopThenStop verifies the manual widget path: starting a
// connect loop against an address nothing answers on moves status to
// "connecting", and stopConnectLoop (the widget's "Disconnect") reports
// "disconnected" and clears reprStop immediately rather than waiting out the
// retry interval.
func TestStartConnectLoopThenStop(t *testing.T) {
	s := newServer(t.TempDir())
	s.name = "condoccer"

	s.startConnectLoop("127.0.0.1", "1") // port 1: nothing listens there
	time.Sleep(50 * time.Millisecond)    // let connectLoop reach its first dial attempt
	s.reprMu.Lock()
	status := s.reprStatus
	s.reprMu.Unlock()
	if status != "connecting" {
		t.Fatalf("status after start = %q, want connecting", status)
	}

	s.stopConnectLoop()
	s.reprMu.Lock()
	status, stop := s.reprStatus, s.reprStop
	s.reprMu.Unlock()
	if status != "disconnected" {
		t.Errorf("status after stop = %q, want disconnected", status)
	}
	if stop != nil {
		t.Errorf("reprStop should be nil after stopConnectLoop")
	}
}

// TestStopConnectLoopNoopWhenIdle verifies disconnecting a condoccer that was
// never connected (e.g. started without --auto-connect, widget's Disconnect
// clicked with nothing running) is a safe no-op.
func TestStopConnectLoopNoopWhenIdle(t *testing.T) {
	s := newServer(t.TempDir())
	s.name = "condoccer"
	s.stopConnectLoop() // must not panic or block
	s.reprMu.Lock()
	status := s.reprStatus
	s.reprMu.Unlock()
	if status != "disconnected" {
		t.Errorf("status = %q, want disconnected", status)
	}
}
