package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// maxWorkerPrompt caps a submitted prompt, which travels to dungeon-keeper as
// one representable command line.
const maxWorkerPrompt = 32 << 10

// The worker tab (Step5SubstepGPrompt.md): dungeon-keeper runs one-per-host
// as the managed "worker" app (see procman.go), connects over representable
// as "worker", and pushes its work signals here as "worker-state". It has no
// UI of its own; the worker tab shows those signals and has a temporary
// prompt box that submits prompt-only work for it to run in-place in /tmp.
// worker-state is forwarded to agent-coordinator too, whose per-host worker
// tab is the same panel, submitting through "__worker:submit".

// WorkerStateMsg mirrors dungeon-keeper/repr.go's type of the same name.
type WorkerStateMsg struct {
	WorkerID  string             `json:"worker_id"`
	AgentType string             `json:"agent_type"`
	Busy      bool               `json:"busy"`
	Signals   []WorkerSignalInfo `json:"signals"`
}

// WorkerSignalInfo mirrors dungeon-keeper/repr.go's type of the same name.
type WorkerSignalInfo struct {
	ID            string `json:"id"`
	File          string `json:"file"`
	Role          string `json:"role"`
	Prompt        string `json:"prompt"`
	Agent         string `json:"agent,omitempty"`
	Model         string `json:"model,omitempty"`
	Mode          string `json:"mode,omitempty"`
	WorkType      string `json:"work_type"`
	WorkLocation  string `json:"work_location,omitempty"`
	Status        string `json:"status"`
	CreatedAt     int64  `json:"created_at"`
	CompletedAt   int64  `json:"completed_at,omitempty"`
	Comment       string `json:"comment,omitempty"`
	Output        string `json:"output,omitempty"`
	OutputTrimmed bool   `json:"output_trimmed,omitempty"`
}

// WorkerSubmitRequest is the worker tab's "worker-submit" payload, passed on
// to dungeon-keeper as-is (see its SubmitRequest).
type WorkerSubmitRequest struct {
	Prompt string `json:"prompt"`
	Agent  string `json:"agent,omitempty"`
	Model  string `json:"model,omitempty"`
}

// WorkerReplyMsg answers a "worker-submit": whether it was handed to
// dungeon-keeper. The work itself shows up in the next worker-state.
type WorkerReplyMsg struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *Server) getWorkerState() *WorkerStateMsg {
	s.workerMu.RLock()
	defer s.workerMu.RUnlock()
	return s.workerState
}

func (s *Server) setWorkerState(st WorkerStateMsg) {
	s.workerMu.Lock()
	s.workerState = &st
	s.workerMu.Unlock()
	s.broadcast("worker-state", st)
	if ac := s.getACClient(); ac != nil {
		ac.SendData("worker-state", st)
	}
}

// handleWorkerCommand runs agent-coordinator's per-host worker tab:
// "__worker:submit <json>" is the same submit as this LR's own worker tab
// makes, and the answer goes back up as "worker-reply".
func (s *Server) handleWorkerCommand(cmd string) {
	rest, ok := strings.CutPrefix(cmd, "__worker:submit ")
	if !ok {
		return
	}
	reply := WorkerReplyMsg{OK: true}
	var req WorkerSubmitRequest
	if err := json.Unmarshal([]byte(rest), &req); err != nil {
		reply = WorkerReplyMsg{Error: "bad submit: " + err.Error()}
	} else if err := s.submitWork(req); err != nil {
		reply = WorkerReplyMsg{Error: err.Error()}
	}
	if ac := s.getACClient(); ac != nil {
		ac.SendData("worker-reply", reply)
	}
}

// submitWork hands a prompt to dungeon-keeper, which writes the work signal
// and runs it.
func (s *Server) submitWork(req WorkerSubmitRequest) error {
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return errors.New("enter a prompt first")
	}
	if len(req.Prompt) > maxWorkerPrompt {
		// The command to dungeon-keeper is one representable line, which
		// can't go past 64 KiB.
		return fmt.Errorf("the prompt is too long (%d KiB, the limit is %d KiB)", len(req.Prompt)>>10, maxWorkerPrompt>>10)
	}
	if s.reprServer == nil || !s.reprServer.IsHealthy("worker") {
		return errors.New("the worker (dungeon-keeper) isn't connected — launch it from the system tab")
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	s.reprServer.SendCommand("worker", "__worker:submit "+string(b))
	return nil
}
