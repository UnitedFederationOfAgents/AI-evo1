package representable

import (
	"encoding/json"
	"testing"
	"time"
)

// TestMode verifies the Mode helper every Connect/NewServer caller uses to
// turn a --dev-mode flag into the wire value (see docs/DevMode.md).
func TestMode(t *testing.T) {
	if got := Mode(true); got != ModeDev {
		t.Errorf("Mode(true) = %q, want %q", got, ModeDev)
	}
	if got := Mode(false); got != ModeOps {
		t.Errorf("Mode(false) = %q, want %q", got, ModeOps)
	}
}

// TestClientNoteServerMode is a network-free, white-box test of the mismatch
// bookkeeping Client.readLoop drives from an incoming "hello": the handler
// fires only when the verdict changes, and ModeMismatch/PeerMode always
// reflect the latest disclosure even before a handler is registered.
func TestClientNoteServerMode(t *testing.T) {
	c := &Client{mode: ModeOps}

	// No handler registered yet: state still updates.
	c.noteServerMode(ModeDev)
	if !c.ModeMismatch() {
		t.Error("ModeMismatch should be true after noteServerMode(ModeDev) on a mode-ops client")
	}
	if got := c.PeerMode(); got != ModeDev {
		t.Errorf("PeerMode = %q, want %q", got, ModeDev)
	}

	// Registering a handler after the fact doesn't retroactively fire it —
	// only a subsequent change does.
	var calls []struct {
		mismatched bool
		peerMode   string
	}
	c.SetModeMismatchHandler(func(mismatched bool, peerMode string) {
		calls = append(calls, struct {
			mismatched bool
			peerMode   string
		}{mismatched, peerMode})
	})
	if len(calls) != 0 {
		t.Fatalf("SetModeMismatchHandler should not itself invoke the handler, got %v", calls)
	}

	// Same verdict again: no call.
	c.noteServerMode(ModeDev)
	if len(calls) != 0 {
		t.Fatalf("an unchanged verdict should not re-invoke the handler, got %v", calls)
	}

	// Verdict flips back to matching: handler fires once with mismatched=false.
	c.noteServerMode(ModeOps)
	if len(calls) != 1 || calls[0].mismatched || calls[0].peerMode != ModeOps {
		t.Fatalf("expected one call (false, %q), got %v", ModeOps, calls)
	}
	if c.ModeMismatch() {
		t.Error("ModeMismatch should be false once the peer's mode matches")
	}
}

// TestConnStateSetMode is the server-side mirror of TestClientNoteServerMode,
// covering connState.setMode's change-detection directly.
func TestConnStateSetMode(t *testing.T) {
	cs := &connState{}

	if changed := cs.setMode(ModeDev, ModeOps); !changed {
		t.Error("first setMode call should report changed")
	}
	if peer, mismatched := cs.getMode(); peer != ModeDev || !mismatched {
		t.Errorf("getMode = (%q, %v), want (%q, true)", peer, mismatched, ModeDev)
	}

	if changed := cs.setMode(ModeDev, ModeOps); changed {
		t.Error("repeating the same verdict should report unchanged")
	}

	if changed := cs.setMode(ModeOps, ModeOps); !changed {
		t.Error("a verdict flip should report changed")
	}
	if _, mismatched := cs.getMode(); mismatched {
		t.Error("matching modes should no longer be mismatched")
	}
}

// newTestServer starts a Server on an OS-assigned loopback port and returns
// it along with the address to dial.
func newTestServer(t *testing.T, mode string) (*Server, string) {
	t.Helper()
	s, err := NewServer("127.0.0.1:0", mode)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { s.ln.Close() })
	return s, s.ln.Addr().String()
}

// waitFor polls cond every 5ms for up to 2s, failing the test if it never
// becomes true. Used throughout since the protocol is asynchronous (a
// background goroutine on each side of the TCP connection).
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// TestModeMatchAllowsFullTraffic is an end-to-end (real TCP loopback) check
// that a client and server sharing a mode exchange state/log/data messages
// normally, with no mismatch disclosed on either side.
func TestModeMatchAllowsFullTraffic(t *testing.T) {
	s, addr := newTestServer(t, ModeOps)

	var gotState string
	stateCh := make(chan struct{}, 1)
	s.SetStateChangeHandler(func(name, state string) {
		gotState = state
		select {
		case stateCh <- struct{}{}:
		default:
		}
	})

	c, err := Connect(addr, "client", ModeOps, time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	c.SendState("remote-control")
	select {
	case <-stateCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for state change")
	}
	if gotState != "remote-control" {
		t.Errorf("server saw state %q, want remote-control", gotState)
	}

	waitFor(t, func() bool { return s.IsHealthy("client") }, "server never saw client as healthy")
	if s.ModeMismatch("client") {
		t.Error("matching modes should not report a mismatch")
	}
	if c.ModeMismatch() {
		t.Error("client should not report a mismatch against a matching server")
	}
	if got := s.PeerMode("client"); got != ModeOps {
		t.Errorf("server PeerMode = %q, want %q", got, ModeOps)
	}
	if got := c.PeerMode(); got != ModeOps {
		t.Errorf("client PeerMode = %q, want %q", got, ModeOps)
	}
}

// TestModeMismatchRefusesEverythingButHealth is an end-to-end (real TCP
// loopback) check of the core dev/ops contract (see docs/DevMode.md): a
// mismatched pair still exchanges heartbeats, but state/log/data traffic is
// dropped by the receiver, and both sides can see the mismatch. Getters
// (ModeMismatch/PeerMode), not the mismatch-handler callback, are the
// assertions here — the callback's exact firing time races the "hello" round
// trip, which TestClientNoteServerMode/TestConnStateSetMode cover instead.
func TestModeMismatchRefusesEverythingButHealth(t *testing.T) {
	s, addr := newTestServer(t, ModeDev)

	stateSeen := make(chan struct{}, 4)
	s.SetStateChangeHandler(func(name, state string) { stateSeen <- struct{}{} })
	dataSeen := make(chan struct{}, 4)
	s.SetDataHandler(func(name, dataType string, data json.RawMessage) { dataSeen <- struct{}{} })

	c, err := Connect(addr, "client", ModeOps, time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	waitFor(t, c.ModeMismatch, "client never saw a mode mismatch against the mode-dev server")
	if got := c.PeerMode(); got != ModeDev {
		t.Errorf("client PeerMode = %q, want %q", got, ModeDev)
	}
	waitFor(t, func() bool { return s.ModeMismatch("client") }, "server never saw a mode mismatch against the mode-ops client")
	if got := s.PeerMode("client"); got != ModeOps {
		t.Errorf("server PeerMode = %q, want %q", got, ModeOps)
	}

	// Health still flows despite the mismatch.
	waitFor(t, func() bool { return s.IsHealthy("client") }, "server never saw client as healthy despite the mismatch")

	// Everything else is refused.
	c.SendState("remote-control")
	c.SendData("system-state", map[string]string{"x": "y"})
	select {
	case <-stateSeen:
		t.Error("mismatched state message should have been dropped")
	case <-dataSeen:
		t.Error("mismatched data message should have been dropped")
	case <-time.After(200 * time.Millisecond):
		// expected: nothing arrives
	}

	// A command from the server is likewise dropped on the client side.
	var gotCmd string
	c.SetCommandHandler(func(cmd string) { gotCmd = cmd })
	s.SendCommand("client", "should-not-arrive")
	time.Sleep(200 * time.Millisecond)
	if gotCmd != "" {
		t.Errorf("mismatched command should have been dropped, got %q", gotCmd)
	}
}
