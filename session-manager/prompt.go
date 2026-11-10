package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Prompting the worker under a session (dTT-IntegrationImpls Step 1,
// Revision O): the transcript's prompt box sends its text here, and this
// passes it to local-representative as a "session-prompt" data message. LR
// hands it to the worker (dungeon-keeper), which makes a temp dir, runs the
// agent there prompt-only with AGENT_SESSION set to the session, and removes
// the dir once the agent is done -- so the turn lands in this session's
// transcript. LR answers twice, as "__sessions:prompt-reply" once the work is
// queued (or couldn't be) and "__sessions:prompt-done" once it finishes; both
// reach the browser as "prompt-result".

// maxPromptBytes mirrors local-representative's maxWorkerPrompt: the prompt
// travels on to dungeon-keeper as one representable line.
const maxPromptBytes = 32 << 10

// PromptResultMsg is the "prompt-result" WebSocket payload. Stage is
// "queued" (handed to the worker), "done" (the work finished; OK says
// whether it succeeded) or "error" (it never reached the worker).
type PromptResultMsg struct {
	ID      string `json:"id"`
	Stage   string `json:"stage"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Status  string `json:"status,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// sessionPromptRequest mirrors local-representative's SessionPromptRequest.
type sessionPromptRequest struct {
	Session string `json:"session"`
	Prompt  string `json:"prompt"`
}

// sessionPromptReply mirrors local-representative's SessionPromptReply.
type sessionPromptReply struct {
	Session string `json:"session"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Status  string `json:"status,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// handlePromptSession checks the prompt and session and passes them to
// local-representative. Its answer arrives later through handleReprCommand.
func (s *Server) handlePromptSession(c *wsClient, id, prompt string) {
	fail := func(msg string) {
		s.sendToClient(c, "prompt-result", PromptResultMsg{ID: id, Stage: "error", Error: msg})
	}
	prompt = strings.TrimSpace(prompt)
	switch {
	case prompt == "":
		fail("enter a prompt first")
		return
	case len(prompt) > maxPromptBytes:
		fail(fmt.Sprintf("the prompt is over %d KiB", maxPromptBytes>>10))
		return
	}
	if err := s.ensureLocalSession(id); err != nil {
		fail(err.Error())
		return
	}
	s.reprMu.Lock()
	client := s.reprClient
	s.reprMu.Unlock()
	if client == nil {
		fail("not connected to local-representative, which passes the prompt to the worker")
		return
	}
	client.SendData("session-prompt", sessionPromptRequest{Session: id, Prompt: prompt})
}

// handlePromptReply turns LR's "__sessions:prompt-reply"/"prompt-done"
// answer into a "prompt-result" for every browser, since the one that asked
// may have reconnected since.
func (s *Server) handlePromptReply(verb, arg string) {
	var r sessionPromptReply
	if err := json.Unmarshal([]byte(arg), &r); err != nil {
		return
	}
	msg := PromptResultMsg{ID: r.Session, OK: r.OK, Error: r.Error, Status: r.Status, Comment: r.Comment}
	switch {
	case verb == "prompt-done":
		msg.Stage = "done"
	case r.OK:
		msg.Stage = "queued"
	default:
		msg.Stage = "error"
	}
	b := s.marshalMsg("prompt-result", msg)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- b:
		default:
		}
	}
	if verb == "prompt-done" {
		// The run added a turn (and maybe made the session's dir).
		go s.broadcastSessions()
	}
}
