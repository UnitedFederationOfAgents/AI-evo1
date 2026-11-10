package main

import (
	"encoding/json"
	"testing"
)

func newTestClient(s *Server) *wsClient {
	c := &wsClient{send: make(chan []byte, 8), done: make(chan struct{})}
	s.mu.Lock()
	s.clients[c] = true
	s.mu.Unlock()
	return c
}

// recvNotepad pops one queued message off c and decodes it as a "notepad"
// payload; ok is false if nothing was queued.
func recvNotepad(t *testing.T, c *wsClient) (NotepadMsg, bool) {
	t.Helper()
	select {
	case raw := <-c.send:
		var m wsMsg
		if err := json.Unmarshal(raw, &m); err != nil || m.Type != "notepad" {
			t.Fatalf("unexpected message %s (err %v)", raw, err)
		}
		var p NotepadMsg
		json.Unmarshal(m.Payload, &p)
		return p, true
	default:
		return NotepadMsg{}, false
	}
}

// TestSetNotepadBroadcastsToOthers verifies an edit reaches every other
// connected client but isn't echoed back to its author.
func TestSetNotepadBroadcastsToOthers(t *testing.T) {
	s := newServer()
	a, b, c := newTestClient(s), newTestClient(s), newTestClient(s)

	s.setNotepad(a, "hello", 1)

	if _, ok := recvNotepad(t, a); ok {
		t.Fatal("author should not receive its own edit back")
	}
	for _, other := range []*wsClient{b, c} {
		p, ok := recvNotepad(t, other)
		if !ok || p.Text != "hello" || p.Rev != 1 {
			t.Fatalf("got %+v (ok=%v), want text=hello rev=1", p, ok)
		}
	}
}

// TestSetNotepadDropsStaleSeq verifies an out-of-order (older) edit from the
// same tab doesn't overwrite a newer one.
func TestSetNotepadDropsStaleSeq(t *testing.T) {
	s := newServer()
	a, b := newTestClient(s), newTestClient(s)

	s.setNotepad(a, "hello world", 2)
	s.setNotepad(a, "hello", 1) // arrived late

	if s.notepadText != "hello world" || s.notepadRev != 1 {
		t.Fatalf("notepad = %q rev %d, want %q rev 1", s.notepadText, s.notepadRev, "hello world")
	}
	recvNotepad(t, b)
	if p, ok := recvNotepad(t, b); ok {
		t.Fatalf("stale edit should not be broadcast, got %+v", p)
	}
}

// TestSendNotepadOnConnect verifies a newly-connected client is handed the
// current text.
func TestSendNotepadOnConnect(t *testing.T) {
	s := newServer()
	a := newTestClient(s)
	s.setNotepad(a, "shared notes", 1)

	late := newTestClient(s)
	s.sendNotepad(late)
	p, ok := recvNotepad(t, late)
	if !ok || p.Text != "shared notes" || p.Rev != 1 {
		t.Fatalf("got %+v (ok=%v), want text=%q rev=1", p, ok, "shared notes")
	}
}
