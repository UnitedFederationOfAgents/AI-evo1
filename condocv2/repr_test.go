package main

import (
	"net"
	"testing"
	"time"

	"representable"
)

// TestPushStateNoClient verifies the state push is a safe no-op before the
// representable link to local-representative is established.
func TestPushStateNoClient(t *testing.T) {
	s := newServer()
	s.httpPort = "8088"
	s.name = "condocv2"
	s.pushState() // must not panic or block
}

// TestHandleReprCommandToleratesJunk verifies condocv2 only acts on the
// "__condocv2:" namespace -- not condoccer's "__condoccer:" -- and ignores
// everything else without panicking.
func TestHandleReprCommandToleratesJunk(t *testing.T) {
	s := newServer()
	s.name = "condocv2"
	for _, cmd := range []string{
		"",
		"plain-command",
		"__ridealong:next",
		"__condoccer:refresh",
		"__condoccer:action {}",
		"__condocv2:",
		"__condocv2:bogus arg",
		"__condocv2:refresh",
	} {
		s.handleReprCommand(cmd)
	}
}

// TestStartConnectLoopThenStop verifies the manual widget path: starting a
// connect loop against an address nothing answers on moves status to
// "connecting", and stopConnectLoop (the widget's "Disconnect") reports
// "disconnected" and clears reprStop immediately rather than waiting out the
// retry interval.
func TestStartConnectLoopThenStop(t *testing.T) {
	s := newServer()
	s.name = "condocv2"

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

// TestStopConnectLoopNoopWhenIdle verifies disconnecting a condocv2 that was
// never connected is a safe no-op.
func TestStopConnectLoopNoopWhenIdle(t *testing.T) {
	s := newServer()
	s.name = "condocv2"
	s.stopConnectLoop() // must not panic or block
	s.reprMu.Lock()
	status := s.reprStatus
	s.reprMu.Unlock()
	if status != "disconnected" {
		t.Errorf("status = %q, want disconnected", status)
	}
}

// freeTCPAddr reserves and immediately releases an ephemeral TCP port, for
// tests that need a real address to bind a representable.Server to.
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
// toggle is a first-class state: enabling it starts a connect loop, and an
// explicit disconnect clears it.
func TestSetAutoConnectArmsRetryLoopAndFlag(t *testing.T) {
	s := newServer()
	s.name = "condocv2"

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
// auto-connect is armed, a drop that isn't an explicit disconnect restarts
// the retry cycle on its own.
func TestConnectLoopResumesAfterUnintentionalDisconnect(t *testing.T) {
	addr := freeTCPAddr(t)
	if _, err := representable.NewServer(addr, representable.Mode(false)); err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	s := newServer()
	s.name = "condocv2"
	s.setAutoConnect(true, host, port)
	waitForReprStatus(t, s, "connected")

	s.reprMu.Lock()
	client := s.reprClient
	s.reprMu.Unlock()
	client.Close()

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
// connect does not retry after an unintentional drop.
func TestConnectLoopDoesNotResumeWithoutAutoConnect(t *testing.T) {
	addr := freeTCPAddr(t)
	if _, err := representable.NewServer(addr, representable.Mode(false)); err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	s := newServer()
	s.name = "condocv2"
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
