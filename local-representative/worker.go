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
// tab is the same panel, submitting through "__worker:submit". The slopspace
// sub-tab's manual control panel goes through "__worker:slopspace"
// (SlopspaceSlopspaceCondocs Step 1 Substep B, Revision A).

// WorkerStateMsg mirrors dungeon-keeper/repr.go's type of the same name.
type WorkerStateMsg struct {
	WorkerID  string             `json:"worker_id"`
	AgentType string             `json:"agent_type"`
	Busy      bool               `json:"busy"`
	Signals   []WorkerSignalInfo `json:"signals"`

	Slopspaces        []SlopspaceInfo `json:"slopspaces"`
	SlopspacesOmitted int             `json:"slopspaces_omitted,omitempty"`
}

// SlopspaceInfo mirrors dungeon-keeper/repr.go's type of the same name: one
// of this host's slopspaces, shown on the worker tab's slopspace view.
type SlopspaceInfo struct {
	ID         string   `json:"id"`
	CreatedAt  int64    `json:"created_at"`
	Iteration  int      `json:"iteration"`
	Deployed   bool     `json:"deployed"`
	AgentType  string   `json:"agent_type,omitempty"`
	InUse      bool     `json:"in_use"`
	Repos      []string `json:"repos,omitempty"`
	Path       string   `json:"path"`
	DeployPath string   `json:"deploy_path,omitempty"`

	Deployments []DeploymentInfo `json:"deployments,omitempty"`
}

// DeploymentInfo mirrors dungeon-keeper/repr.go's type of the same name: one
// deployment cycle of a slopspace.
type DeploymentInfo struct {
	AgentType  string `json:"agent_type"`
	DeployedAt int64  `json:"deployed_at"`
	ReturnedAt int64  `json:"returned_at,omitempty"`
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
	StartedAt     int64  `json:"started_at,omitempty"`
	CompletedAt   int64  `json:"completed_at,omitempty"`
	AgentType     string `json:"agent_type,omitempty"`
	SlopspaceID   string `json:"slopspace_id,omitempty"`
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

// WorkerSlopspaceRequest is the slopspace sub-tab's manual control
// ("worker-slopspace"), passed on to dungeon-keeper as
// "__worker:slopspace <json>". It mirrors dungeon-keeper/slopctl.go's
// SlopspaceControlRequest.
type WorkerSlopspaceRequest struct {
	Op        string `json:"op"`
	ID        string `json:"id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Message   string `json:"message,omitempty"`
	Prompt    string `json:"prompt,omitempty"` // command (Revision B)
}

// WorkerSlopspaceReply mirrors dungeon-keeper's SlopspaceControlReply: how a
// manual op went. dungeon-keeper sends it as "slopspace-reply" once the op is
// done; LR sends one itself when the op never reached dungeon-keeper.
type WorkerSlopspaceReply struct {
	Op     string `json:"op"`
	ID     string `json:"id,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// workerSlopspaceOps are the manual control ops dungeon-keeper runs.
// "command" (Revision B) queues a prompt for the agent in a deployed
// slopspace.
var workerSlopspaceOps = map[string]bool{
	"create": true, "deploy": true, "return": true, "delete": true, "add-repo": true, "write": true, "command": true,
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

// setWorkerSlopspaceReply passes a manual slopspace op's outcome to this
// LR's worker tab and to agent-coordinator's, whichever sent it.
func (s *Server) setWorkerSlopspaceReply(reply WorkerSlopspaceReply) {
	s.broadcast("worker-slopspace-reply", reply)
	if ac := s.getACClient(); ac != nil {
		ac.SendData("worker-slopspace-reply", reply)
	}
}

// handleWorkerCommand runs agent-coordinator's per-host worker tab:
// "__worker:submit <json>" is the same submit as this LR's own worker tab
// makes, and the answer goes back up as "worker-reply";
// "__worker:slopspace <json>" is the slopspace sub-tab's manual control,
// answered as "worker-slopspace-reply".
func (s *Server) handleWorkerCommand(cmd string) {
	if rest, ok := strings.CutPrefix(cmd, "__worker:slopspace "); ok {
		var req WorkerSlopspaceRequest
		if err := json.Unmarshal([]byte(rest), &req); err != nil {
			s.setWorkerSlopspaceReply(WorkerSlopspaceReply{Error: "bad slopspace request: " + err.Error()})
		} else if err := s.controlSlopspace(req); err != nil {
			s.setWorkerSlopspaceReply(WorkerSlopspaceReply{Op: req.Op, ID: req.ID, Error: err.Error()})
		}
		return
	}
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

// controlSlopspace hands a manual slopspace op to dungeon-keeper, which runs
// it through its slopspace Manager and answers with "slopspace-reply".
func (s *Server) controlSlopspace(req WorkerSlopspaceRequest) error {
	if !workerSlopspaceOps[req.Op] {
		return fmt.Errorf("unknown slopspace op %q", req.Op)
	}
	if req.Op != "create" && strings.TrimSpace(req.ID) == "" {
		return errors.New("pick a slopspace first")
	}
	if req.Op == "command" && strings.TrimSpace(req.Prompt) == "" {
		return errors.New("enter a command first")
	}
	if s.reprServer == nil || !s.reprServer.IsHealthy("worker") {
		return errors.New("the worker (dungeon-keeper) isn't connected — launch it from the system tab")
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if len(b) > maxWorkerPrompt {
		return errors.New("the slopspace request is too long")
	}
	s.reprServer.SendCommand("worker", "__worker:slopspace "+string(b))
	return nil
}
