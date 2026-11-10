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

	PendingProposals []string `json:"pending_proposals,omitempty"`
	ProposalsOmitted int      `json:"proposals_omitted,omitempty"`

	DTTProposals bool `json:"dtt_proposals"`
}

// DeploymentInfo mirrors dungeon-keeper/repr.go's type of the same name: one
// deployment cycle of a slopspace, and the human's answer to the DTT
// proposals it returned with.
type DeploymentInfo struct {
	AgentType  string `json:"agent_type"`
	DeployedAt int64  `json:"deployed_at"`
	ReturnedAt int64  `json:"returned_at,omitempty"`
	Proposals  int    `json:"proposals,omitempty"`
	Accepted   int    `json:"accepted,omitempty"` // answered one at a time (Revision M)
	Rejected   int    `json:"rejected,omitempty"`
	Decision   string `json:"decision,omitempty"`
	DecidedAt  int64  `json:"decided_at,omitempty"`
	Note       string `json:"note,omitempty"`
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
	Session       string `json:"session,omitempty"`
	Comment       string `json:"comment,omitempty"`
	Output        string `json:"output,omitempty"`
	OutputTrimmed bool   `json:"output_trimmed,omitempty"`
}

// WorkerSubmitRequest is the worker tab's "worker-submit" payload, passed on
// to dungeon-keeper as-is (see its SubmitRequest). Session and TempDir are
// set only for session-manager's prompt box (see submitSessionPrompt).
type WorkerSubmitRequest struct {
	Prompt  string `json:"prompt"`
	Agent   string `json:"agent,omitempty"`
	Model   string `json:"model,omitempty"`
	Session string `json:"session,omitempty"`
	TempDir bool   `json:"temp_dir,omitempty"`
}

// sessionPromptRole mirrors dungeon-keeper's sessionRole: the role on work
// submitted from session-manager's prompt box.
const sessionPromptRole = "session-prompt"

// SessionPromptRequest is session-manager's "session-prompt" data message:
// run prompt, prompt-only, under session (dTT-IntegrationImpls Step 1,
// Revision O). dungeon-keeper makes a temp dir for it and removes it after.
type SessionPromptRequest struct {
	Session string `json:"session"`
	Prompt  string `json:"prompt"`
}

// SessionPromptReply goes back to session-manager as
// "__sessions:prompt-reply <json>" once the prompt was handed to the worker
// (or couldn't be), and as "__sessions:prompt-done <json>" once that work
// finishes. Status is the work signal's final status on prompt-done.
type SessionPromptReply struct {
	Session string `json:"session"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Status  string `json:"status,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// submitSessionPrompt queues session-manager's prompt for the worker and
// tells session-manager whether that worked.
func (s *Server) submitSessionPrompt(req SessionPromptRequest) {
	reply := SessionPromptReply{Session: req.Session, OK: true}
	if strings.TrimSpace(req.Session) == "" {
		reply = SessionPromptReply{Session: req.Session, Error: "pick a session first"}
	} else if err := s.submitWork(WorkerSubmitRequest{Prompt: req.Prompt, Session: req.Session, TempDir: true}); err != nil {
		reply = SessionPromptReply{Session: req.Session, Error: err.Error()}
	}
	s.sendSessionPromptMsg("prompt-reply", reply)
}

func (s *Server) sendSessionPromptMsg(verb string, reply SessionPromptReply) {
	if s.reprServer == nil || !s.reprServer.IsHealthy("sessions") {
		return
	}
	b, err := json.Marshal(reply)
	if err != nil {
		return
	}
	s.reprServer.SendCommand("sessions", "__sessions:"+verb+" "+string(b))
}

// notifySessionPromptsDone tells session-manager about each session prompt
// that finished between prev and next, so it can refresh that session's
// transcript. Only a change seen here counts: work that finished while LR
// wasn't watching isn't reported.
func (s *Server) notifySessionPromptsDone(prev *WorkerStateMsg, next WorkerStateMsg) {
	for _, reply := range finishedSessionPrompts(prev, next) {
		s.sendSessionPromptMsg("prompt-done", reply)
	}
}

// finishedSessionPrompts lists the session prompts that were still pending or
// running in prev and have finished in next.
func finishedSessionPrompts(prev *WorkerStateMsg, next WorkerStateMsg) []SessionPromptReply {
	if prev == nil {
		return nil
	}
	before := make(map[string]string, len(prev.Signals))
	for _, sig := range prev.Signals {
		before[sig.ID] = sig.Status
	}
	finished := func(status string) bool { return status == "completed" || status == "failed" }
	var out []SessionPromptReply
	for _, sig := range next.Signals {
		if sig.Role != sessionPromptRole || sig.Session == "" || !finished(sig.Status) {
			continue
		}
		if was, ok := before[sig.ID]; !ok || finished(was) {
			continue
		}
		out = append(out, SessionPromptReply{
			Session: sig.Session,
			OK:      sig.Status == "completed",
			Status:  sig.Status,
			Comment: sig.Comment,
		})
	}
	return out
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
	Prompt    string `json:"prompt,omitempty"`   // command (Revision B)
	Decision  string `json:"decision,omitempty"` // review: accept | reject | modify
	Note      string `json:"note,omitempty"`     // review
	Path      string `json:"path,omitempty"`     // files

	// review: accept or reject only these proposals (relative to
	// writespaces/dtt-requests); absent is all of them (Revision M).
	Proposals []string `json:"proposals,omitempty"`

	// deploy: whether this deployment offers DTT proposals; absent keeps the
	// slopspace's setting (dTT-IntegrationImpls Step 1, Revision J).
	DTTProposals *bool `json:"dtt_proposals,omitempty"`
}

// WorkerSlopspaceReply mirrors dungeon-keeper's SlopspaceControlReply: how a
// manual op went. dungeon-keeper sends it as "slopspace-reply" once the op is
// done; LR sends one itself when the op never reached dungeon-keeper. A
// "files" op's listing is passed through as-is.
type WorkerSlopspaceReply struct {
	Op     string          `json:"op"`
	ID     string          `json:"id,omitempty"`
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Detail string          `json:"detail,omitempty"`
	Path   string          `json:"path,omitempty"`
	Files  json.RawMessage `json:"files,omitempty"`
}

// workerSlopspaceOps are the manual control ops dungeon-keeper runs.
// "command" (Revision B) queues a prompt for the agent in a deployed
// slopspace; "review" answers its DTT proposals and "files" browses it
// (dTT-IntegrationImpls Step 1, Revision H).
var workerSlopspaceOps = map[string]bool{
	"create": true, "deploy": true, "return": true, "delete": true, "add-repo": true, "write": true, "command": true,
	"review": true, "files": true,
}

var workerReviewDecisions = map[string]bool{"accept": true, "reject": true, "modify": true}

func (s *Server) getWorkerState() *WorkerStateMsg {
	s.workerMu.RLock()
	defer s.workerMu.RUnlock()
	return s.workerState
}

func (s *Server) setWorkerState(st WorkerStateMsg) {
	s.workerMu.Lock()
	prev := s.workerState
	s.workerState = &st
	s.workerMu.Unlock()
	s.notifySessionPromptsDone(prev, st)
	if s.tools != nil {
		s.tools.workerStateChanged(st) // demo reviews keep their outcome (demoreview.go)
	}
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
	return s.sendWorkerSubmit(req.Prompt, req)
}

// demoReviewRole mirrors dungeon-keeper's: the role on a demo record's
// review (demoreview.go).
const demoReviewRole = "demo-review"

// workerReviewRequest is a demo record's review as dungeon-keeper's
// SubmitRequest takes it: read-only, in place in the record's directory.
// Only LR makes one (submitDemoReview) -- the worker tabs' submits are
// WorkerSubmitRequests, which can't name a work location or mode.
type workerReviewRequest struct {
	WorkerSubmitRequest
	WorkLocation string `json:"work_location"`
	Mode         string `json:"mode"`
	Role         string `json:"role"`
}

// demoProposalRole mirrors dungeon-keeper's too: the role on a demo
// proposal's creation job (demoproposals.go), in write mode in place in the
// proposal's directory, where the agent writes the demo.
const demoProposalRole = "demo-proposal"

// submitDemoReview hands dungeon-keeper a read-only review of the demo
// record in dir.
func (s *Server) submitDemoReview(prompt, dir, agent, model string) error {
	return s.submitInPlace(demoReviewRole, "read", prompt, dir, agent, model)
}

// submitDemoProposal hands dungeon-keeper the creation job of the demo
// proposal in dir, in write mode.
func (s *Server) submitDemoProposal(prompt, dir, agent, model string) error {
	return s.submitInPlace(demoProposalRole, "write", prompt, dir, agent, model)
}

func (s *Server) submitInPlace(role, mode, prompt, dir, agent, model string) error {
	req := workerReviewRequest{
		WorkerSubmitRequest: WorkerSubmitRequest{Prompt: strings.TrimSpace(prompt), Agent: agent, Model: model},
		WorkLocation:        dir,
		Mode:                mode,
		Role:                role,
	}
	return s.sendWorkerSubmit(req.Prompt, req)
}

// sendWorkerSubmit sends "__worker:submit <req>" to dungeon-keeper.
func (s *Server) sendWorkerSubmit(prompt string, req any) error {
	if prompt == "" {
		return errors.New("enter a prompt first")
	}
	if len(prompt) > maxWorkerPrompt {
		// The command to dungeon-keeper is one representable line, which
		// can't go past 64 KiB.
		return fmt.Errorf("the prompt is too long (%d KiB, the limit is %d KiB)", len(prompt)>>10, maxWorkerPrompt>>10)
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
	if req.Op == "review" {
		if !workerReviewDecisions[req.Decision] {
			return fmt.Errorf("unknown review decision %q (accept, reject or modify)", req.Decision)
		}
		if req.Decision == "modify" && strings.TrimSpace(req.Note) == "" {
			return errors.New("say what to change: requesting modification needs a note for the agent")
		}
		if req.Decision == "modify" && len(req.Proposals) > 0 {
			return errors.New("requesting modification covers every proposal still awaiting review; accept or reject single ones instead")
		}
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
