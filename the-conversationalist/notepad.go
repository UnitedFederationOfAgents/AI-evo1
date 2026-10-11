package main

// Notepad: a quick-notes area shared by every browser client connected to
// this the-conversationalist instance (condocs/initialRobotImpls/
// Step5Prompt.md). The text lives in memory here on the server -- it is not
// persisted across restarts -- and every edit is pushed to all other
// connected clients as soon as it arrives, last writer wins.

// NotepadMsg is the "notepad" WebSocket payload carrying the shared
// notepad's full current text. Rev increments on every accepted edit.
type NotepadMsg struct {
	Text string `json:"text"`
	Rev  uint64 `json:"rev"`
}

// setNotepad records an edit from client c and broadcasts the new text to
// every other connected client (c already shows what it typed; echoing it
// back would fight its cursor).
//
// seq is c's own monotonically increasing edit counter: handleWS dispatches
// each incoming message on its own goroutine, so two quick keystrokes from
// one tab can arrive here out of order -- seq lets the older one be dropped
// instead of overwriting the newer text.
func (s *Server) setNotepad(c *wsClient, text string, seq uint64) {
	s.notepadMu.Lock()
	defer s.notepadMu.Unlock()
	if c != nil {
		if seq != 0 && seq <= c.notepadSeq {
			return
		}
		c.notepadSeq = seq
	}
	s.notepadText = text
	s.notepadRev++

	// Broadcast while still holding notepadMu so concurrent edits from
	// different clients go out in the same order they were applied here.
	msg := s.marshalMsg("notepad", NotepadMsg{Text: s.notepadText, Rev: s.notepadRev})
	s.mu.RLock()
	defer s.mu.RUnlock()
	for other := range s.clients {
		if other == c {
			continue
		}
		select {
		case other.send <- msg:
		default:
		}
	}
}

// sendNotepad sends the notepad's current text to a single (usually
// newly-connected) WebSocket client.
func (s *Server) sendNotepad(c *wsClient) {
	s.notepadMu.Lock()
	msg := NotepadMsg{Text: s.notepadText, Rev: s.notepadRev}
	s.notepadMu.Unlock()
	s.sendToClient(c, "notepad", msg)
}
