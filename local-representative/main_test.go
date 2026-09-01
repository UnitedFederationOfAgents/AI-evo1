package main

import (
	"testing"
	"time"
)

// TestGetACStateReportsConnecting verifies the auto-connect retry indicator is
// surfaced through the ac-state payload the UI consumes.
func TestGetACStateReportsConnecting(t *testing.T) {
	s := newServer("test-lr")

	if s.getACState().Connecting {
		t.Fatalf("fresh server should not report Connecting")
	}

	s.setACAutoConnecting(true)
	if !s.getACState().Connecting {
		t.Fatalf("expected Connecting=true after setACAutoConnecting(true)")
	}

	s.setACAutoConnecting(false)
	if s.getACState().Connecting {
		t.Fatalf("expected Connecting=false after setACAutoConnecting(false)")
	}
}

// TestACStateMsgStampsTarget verifies acStateMsg carries the explicit host/port
// plus the current retry status.
func TestACStateMsgStampsTarget(t *testing.T) {
	s := newServer("test-lr")
	s.setACAutoConnecting(true)

	msg := s.acStateMsg(false, "10.0.0.5", "9000")
	if msg.Host != "10.0.0.5" || msg.Port != "9000" {
		t.Fatalf("expected host/port to be carried through, got %s:%s", msg.Host, msg.Port)
	}
	if msg.Connected {
		t.Fatalf("expected Connected=false")
	}
	if !msg.Connecting {
		t.Fatalf("expected Connecting=true while the retry loop is active")
	}
}

// TestStopAutoConnectAC verifies the retry loop can be started and cancelled, and
// that a redundant stop is safe (no double-close panic).
func TestStopAutoConnectAC(t *testing.T) {
	s := newServer("test-lr")

	// Target a closed port so connectAC fails fast rather than hanging.
	s.startAutoConnectAC("127.0.0.1", "1")
	s.stopAutoConnectAC()
	s.stopAutoConnectAC() // must not panic

	// The loop goroutine should clear its cancel channel shortly after cancel.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.acMu.RLock()
		cleared := s.acAutoConnectCancel == nil
		s.acMu.RUnlock()
		if cleared {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("auto-connect loop did not release its cancel channel after stop")
}
