package main

import (
	"net"
	"testing"
	"time"

	"representable"
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

// freeTCPAddr reserves and immediately releases an ephemeral TCP port, for
// tests that need a real address to bind a representable.Server to without a
// way to read back the port NewServer chose (it has no Addr() accessor).
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a free port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func waitForReprStatus(t *testing.T, s *Server, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.reprMu.Lock()
		got := s.reprStatus
		s.reprMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("repr status did not reach %q in time", want)
}

// TestSetAutoConnectArmsRetryLoopAndFlag verifies the persistent auto-connect
// toggle (Step3Prompt.md Revision I) is a first-class state, settable
// independent of any single connection attempt: enabling it is reported
// through repr-status's AutoConnect field and starts a connect loop.
func TestSetAutoConnectArmsRetryLoopAndFlag(t *testing.T) {
	s := newServer(t.TempDir())
	s.name = "condoccer"

	s.setAutoConnect(true, "127.0.0.1", "1") // port 1: nothing listens there
	s.reprMu.Lock()
	enabled, running := s.reprAutoConnect, s.reprStop != nil
	s.reprMu.Unlock()
	if !enabled {
		t.Fatalf("expected reprAutoConnect=true after enabling")
	}
	if !running {
		t.Fatalf("expected setAutoConnect(true, ...) to start a connect loop")
	}

	s.disconnectRepr()
	s.reprMu.Lock()
	enabled = s.reprAutoConnect
	s.reprMu.Unlock()
	if enabled {
		t.Fatalf("expected disconnectRepr to clear the auto-connect toggle")
	}
}

// TestConnectLoopResumesAfterUnintentionalDisconnect verifies that when
// auto-connect is armed, a drop that isn't accompanied by an explicit
// widget disconnect (i.e. stopCh isn't closed) restarts the retry cycle on
// its own -- Step3Prompt.md Revision I: "the auto-connect cycle begins
// automatically upon unintentional disconnection".
func TestConnectLoopResumesAfterUnintentionalDisconnect(t *testing.T) {
	addr := freeTCPAddr(t)
	if _, err := representable.NewServer(addr, representable.Mode(false)); err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	s := newServer(t.TempDir())
	s.name = "condoccer"
	s.setAutoConnect(true, host, port)
	waitForReprStatus(t, s, "connected")

	// Force-close the client directly (bypassing stopConnectLoop/stopCh) to
	// simulate the remote end dropping the connection unexpectedly, as
	// opposed to the widget's own "disconnect".
	s.reprMu.Lock()
	client := s.reprClient
	s.reprMu.Unlock()
	client.Close()

	// The loop should reconnect on its own since the test server is still up
	// and auto-connect is still armed.
	waitForReprStatus(t, s, "connected")
	s.reprMu.Lock()
	enabled := s.reprAutoConnect
	s.reprMu.Unlock()
	if !enabled {
		t.Fatalf("expected auto-connect to remain armed across the resumed connection")
	}

	s.disconnectRepr()
}

// TestConnectLoopDoesNotResumeWithoutAutoConnect verifies a one-shot manual
// connect (auto-connect not armed) does not retry after an unintentional
// drop -- only an explicitly-armed auto-connect resumes the cycle.
func TestConnectLoopDoesNotResumeWithoutAutoConnect(t *testing.T) {
	addr := freeTCPAddr(t)
	if _, err := representable.NewServer(addr, representable.Mode(false)); err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	s := newServer(t.TempDir())
	s.name = "condoccer"
	s.startConnectLoop(host, port) // manual connect -- auto-connect not armed
	waitForReprStatus(t, s, "connected")

	s.reprMu.Lock()
	client := s.reprClient
	s.reprMu.Unlock()
	client.Close()

	waitForReprStatus(t, s, "disconnected")
	time.Sleep(200 * time.Millisecond) // give a would-be (buggy) resume a moment to kick in
	s.reprMu.Lock()
	status, running := s.reprStatus, s.reprStop != nil
	s.reprMu.Unlock()
	if status != "disconnected" || running {
		t.Fatalf("expected a one-shot connection to stay disconnected after dropping, got status=%q running=%v", status, running)
	}
}
