package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"dungeon-keeper/pkg/slopspace"
	"dungeon-keeper/pkg/types"
	"dungeon-keeper/pkg/writespace"
)

// The worker tab's slopspace manual control panel
// (SlopspaceSlopspaceCondocs Step 1 Substep B, Revision A): LR sends
// "__worker:slopspace <json>", this runs the op through the same
// pkg/slopspace Manager condoccer and the `slopspace` CLI use, and answers
// with "slopspace-reply". The slopspace list itself updates through the
// next worker-state.

// SlopspaceControlRequest is the JSON carried by "__worker:slopspace <json>".
type SlopspaceControlRequest struct {
	Op        string `json:"op"`                   // create | deploy | return | delete | add-repo | write
	ID        string `json:"id,omitempty"`         // the slopspace; every op but create
	AgentType string `json:"agent_type,omitempty"` // deploy; defaults to this worker's
	Repo      string `json:"repo,omitempty"`       // add-repo (required), write (empty: every repo); owner/name
	Branch    string `json:"branch,omitempty"`     // add-repo: the new branch to create
	Message   string `json:"message,omitempty"`    // write: the commit message
}

// SlopspaceControlReply is the "slopspace-reply" data payload.
type SlopspaceControlReply struct {
	Op     string `json:"op"`
	ID     string `json:"id,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"` // what happened, e.g. the new ID or the commits pushed
}

// handleSlopspaceCommand runs one manual control op off the command
// handler's goroutine -- add-repo clones and write pushes -- and reports
// the result back to LR.
func (w *Worker) handleSlopspaceCommand(arg string) {
	var req SlopspaceControlRequest
	if err := json.Unmarshal([]byte(arg), &req); err != nil {
		w.sendSlopspaceReply(SlopspaceControlReply{Error: "bad slopspace request: " + err.Error()})
		return
	}
	go func() {
		detail, err := w.slopspaceControl(&req)
		reply := SlopspaceControlReply{Op: req.Op, ID: req.ID}
		if err != nil {
			reply.Error = err.Error()
			log.Printf("[%s] repr: slopspace %s %s failed: %v", w.workerID, req.Op, req.ID, err)
		} else {
			reply.OK = true
			reply.Detail = detail
			log.Printf("[%s] repr: slopspace %s %s: %s", w.workerID, req.Op, req.ID, detail)
		}
		w.sendSlopspaceReply(reply)
		w.pushState()
	}()
}

func (w *Worker) sendSlopspaceReply(reply SlopspaceControlReply) {
	w.reprMu.Lock()
	client := w.repr
	w.reprMu.Unlock()
	if client != nil {
		client.SendData("slopspace-reply", reply)
	}
}

// slopspaceControl performs one manual op. Ops are serialised, and refused
// on a slopspace a local agent is working in, so a click can't move or
// delete files out from under a running turn. create fills in req.ID with
// the new slopspace, so the reply names it.
func (w *Worker) slopspaceControl(req *SlopspaceControlRequest) (string, error) {
	w.slopCtlMu.Lock()
	defer w.slopCtlMu.Unlock()

	mgr := w.slopspaceMgr
	if req.Op == "create" {
		m, err := mgr.Create()
		if err != nil {
			return "", err
		}
		req.ID = m.ID
		return "created", nil
	}

	id := strings.TrimSpace(req.ID)
	if id == "" || strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
		return "", errors.New("pick a slopspace first")
	}
	meta, err := mgr.Get(id)
	if err != nil {
		return "", fmt.Errorf("no such slopspace: %w", err)
	}
	if meta.Deployed && w.agentWorking(meta.DeployedAgentType) {
		return "", fmt.Errorf("slopspace %s is in use by a local %s agent; wait for its turn to finish", shortID(id), meta.DeployedAgentType)
	}

	switch req.Op {
	case "deploy":
		at := w.config.AgentType
		if req.AgentType != "" {
			at = types.AgentType(req.AgentType)
		}
		if at != types.AgentTypeWorker && at != types.AgentTypeHeuristic {
			return "", fmt.Errorf("unknown agent type %q", req.AgentType)
		}
		// Deploy clears the deploy path before moving in, so deploying over
		// another slopspace would throw away that one's writespaces.
		if other, err := mgr.GetDeployedID(at); err != nil {
			return "", err
		} else if other != "" {
			return "", fmt.Errorf("slopspace %s is already deployed for %s; return it first", shortID(other), at)
		}
		if err := mgr.Deploy(id, at); err != nil {
			return "", err
		}
		return fmt.Sprintf("deployed to %s (%s)", w.config.DeployPathForAgentType(at), at), nil

	case "return":
		if err := mgr.Return(id); err != nil {
			return "", err
		}
		return "returned", nil

	case "delete":
		if err := mgr.Delete(id); err != nil {
			return "", err
		}
		return "deleted", nil

	case "add-repo":
		ownerRepo := strings.TrimSpace(req.Repo)
		owner, repo, ok := strings.Cut(ownerRepo, "/")
		if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
			return "", fmt.Errorf("repo must be owner/name, got %q", req.Repo)
		}
		branch := strings.TrimSpace(req.Branch)
		if branch == "" {
			// As the CLI insists: a writespace always gets its own branch,
			// so nothing is ever pushed to main by accident.
			return "", errors.New("a new branch name is required")
		}
		// Clone (or pull) into the shared writespace pool, then copy it in.
		src, err := writespace.NewManager(w.config).CloneRepo(ownerRepo)
		if err != nil {
			return "", err
		}
		if err := mgr.AddWritespaceRepo(id, owner, repo, branch, src.RootPath); err != nil {
			return "", err
		}
		return fmt.Sprintf("added %s on new branch %s", ownerRepo, branch), nil

	case "write":
		var results []slopspace.RepoWriteResult
		if ownerRepo := strings.TrimSpace(req.Repo); ownerRepo != "" {
			owner, repo, ok := strings.Cut(ownerRepo, "/")
			if !ok {
				return "", fmt.Errorf("repo must be owner/name, got %q", req.Repo)
			}
			res, err := mgr.WriteRepoChanges(id, owner, repo, req.Message)
			if err != nil {
				return "", err
			}
			results = []slopspace.RepoWriteResult{res}
		} else {
			results, err = mgr.WriteAllRepoChanges(id, req.Message)
			if err != nil {
				return "", fmt.Errorf("%s: %w", describeWrites(results), err)
			}
		}
		return describeWrites(results), nil
	}
	return "", fmt.Errorf("unknown slopspace op %q", req.Op)
}

// agentWorking reports whether an agent of the given type is processing
// slopspace work right now -- the same test the list's "in use" uses.
func (w *Worker) agentWorking(at types.AgentType) bool {
	ongoing, _ := w.workSignalMgr.ListOngoing()
	for _, p := range ongoing {
		signal, _, err := w.workSignalMgr.Read(p)
		if err != nil {
			continue
		}
		if signal.Status == types.WorkStatusProcessing && signal.WorkType == types.WorkTypeSlopspace && signal.AgentType == at {
			return true
		}
	}
	return false
}

// describeWrites is the one-line form of printWriteResults.
func describeWrites(results []slopspace.RepoWriteResult) string {
	if len(results) == 0 {
		return "no writespace repos"
	}
	parts := make([]string, 0, len(results))
	for _, r := range results {
		status := "no changes"
		if r.Changed {
			status = "pushed " + shortID(r.SHA)
		}
		parts = append(parts, r.Repo+": "+status)
	}
	return strings.Join(parts, ", ")
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
