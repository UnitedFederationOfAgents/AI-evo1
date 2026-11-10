// Package types defines the core data structures for dungeon-keeper.
package types

import (
	"time"
)

// AgentType represents the type of agent to invoke.
type AgentType string

const (
	AgentTypeWorker    AgentType = "agent-worker"
	AgentTypeHeuristic AgentType = "heuristic-request"
)

// WorkType represents the type of work location.
type WorkType string

const (
	WorkTypeSlopspace WorkType = "slopspace"
	WorkTypeInPlace   WorkType = "in-place"
)

// WorkStatus represents the current status of work.
type WorkStatus string

const (
	WorkStatusPending    WorkStatus = "pending"
	WorkStatusProcessing WorkStatus = "processing"
	WorkStatusCompleted  WorkStatus = "completed"
	WorkStatusFailed     WorkStatus = "failed"
)

// WorkSignal is the initial header of a work signal file (WORKING-*.jsonl).
// It defines the work location, agent configuration, and tracking metadata.
type WorkSignal struct {
	ID           string     `json:"id"`
	WorkLocation string     `json:"work_location,omitempty"` // Only for in-place work
	WorkType     WorkType   `json:"work_type"`
	AgentType    AgentType  `json:"agent_type"`
	Role         string     `json:"role"`
	Prompt       string     `json:"prompt"`
	Agent        string     `json:"agent"`
	Model        string     `json:"model"`
	Mode         string     `json:"mode,omitempty"`   // ambiguous-agent mode: prompt, read, write or execute (blank means execute)
	Holder       string     `json:"holder,omitempty"` // Current controller ID, blank when waiting
	Status       WorkStatus `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`

	// Session is the AGENT_SESSION the agent runs under; blank leaves it to
	// the agent's own default (today's session).
	Session string `json:"session,omitempty"`
	// RemoveWorkLocation (in-place only): WorkLocation is a temp dir made for
	// this signal, removed once the agent finishes (dTT-IntegrationImpls Step
	// 1, Revision O).
	RemoveWorkLocation bool `json:"remove_work_location,omitempty"`
	// TimeoutSeconds is a hard ceiling on the agent run: once it passes, the
	// worker kills the agent and fails the signal. 0 means no limit.
	TimeoutSeconds int64 `json:"timeout_seconds,omitempty"`
}

// EventCancelRequested is the status_update of the event that asks the
// worker to stop a signal's agent (see worksignal.RequestCancel). Whoever
// submitted the signal appends it; the worker watches for it while the
// agent runs and fails the signal once the agent is stopped.
const EventCancelRequested = "cancel_requested"

// WorkEvent represents a status update or action event in the work signal file.
type WorkEvent struct {
	EventID      string    `json:"event_id"`
	StatusUpdate string    `json:"status_update,omitempty"`
	Comment      string    `json:"comment,omitempty"`
	Output       string    `json:"output,omitempty"`       // Captured agent output, on the event recorded when the agent finishes
	SlopspaceID  string    `json:"slopspace_id,omitempty"` // The slopspace slopspace work runs in, on the event recorded before the agent starts
	Timestamp    time.Time `json:"timestamp"`
}

// Slopspace represents a slopspace instance with its metadata and structure.
// Note: A slopspace is NOT tied to an agent type at creation time.
// Agent type is specified during deployment.
type Slopspace struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	RootPath   string    `json:"root_path"`   // Path in slopspaces directory
	DeployPath string    `json:"deploy_path"` // Path when deployed to /agent/<type>
	Deployed   bool      `json:"deployed"`
}

// SlopspaceContent defines the structure of files within a slopspace.
type SlopspaceContent struct {
	ReadSpaces  ReadSpaces  `json:"read_spaces"`
	WriteSpaces WriteSpaces `json:"write_spaces"`
}

// ReadSpaces contains paths that the agent can read from (immutable from agent perspective).
type ReadSpaces struct {
	AgentRecords string `json:"agent_records,omitempty"` // Sessions to read
	DTTImages    string `json:"dtt_images,omitempty"`    // Declarative-tool-tools images: what the canvas's tool calls measured
	DTTCanvas    string `json:"dtt_canvas,omitempty"`    // Declarative-tool-tools calls the human supplied (read/ and write/)
	Repos       string `json:"repos,omitempty"`         // Repos visible but not modifiable
	Files        string `json:"files,omitempty"`         // Arbitrary readable files
}

// WriteSpaces contains paths that the agent can write to (changes reflected outside).
type WriteSpaces struct {
	AgentRecords string `json:"agent_records,omitempty"` // Always available for agent to write
	DTTRequests  string `json:"dtt_requests,omitempty"`  // Declarative-tool-tools calls the agent proposes (read/ and write/)
	Repos        string `json:"repos,omitempty"`         // Repos that can be modified
	Files        string `json:"files,omitempty"`         // Arbitrary modifiable files/folders
}

// SlopspaceMetadata is stored in the slopspace directory (not deployed to /agent).
// Note: DeployedAgentType is only populated when the slopspace is deployed.
type SlopspaceMetadata struct {
	Slopspace
	WorkSignalPath    string       `json:"work_signal_path"`      // Associated work signal file
	Iteration         int          `json:"iteration"`             // Number of deployment cycles
	DeployedAgentType AgentType    `json:"deployed_agent_type"`   // Agent type for current/last deployment
	Deployments       []Deployment `json:"deployments,omitempty"` // Deployment cycles, oldest first, at most MaxDeployments

	// DTTRequestsDisabled deploys the slopspace without writespaces/dtt-requests,
	// so the agent can't propose DTT tool calls and none come back for review
	// (the worker tab's deploy can set it; see pkg/slopspace/proposals.go's
	// SetDTTRequests).
	DTTRequestsDisabled bool `json:"dtt_requests_disabled,omitempty"`

	// ReadspaceRepos are the read-only repos the slopspace carries in
	// readspaces/repos. Each is exported afresh from its shared clone on every
	// deploy and return (see pkg/slopspace/readrepos.go).
	ReadspaceRepos []ReadspaceRepo `json:"readspace_repos,omitempty"`
}

// MaxDeployments bounds the deployment history kept in a slopspace's metadata.
const MaxDeployments = 50

// MaxReadspaceResolutions bounds each read-only repo's resolution history.
const MaxReadspaceResolutions = 100

// ReadspaceRepo is a read-only repo of a slopspace: a plain copy (no .git) of
// one commit of Repo, at readspaces/repos/<owner>/<name>. Ref is followed:
// every deploy and return fetches Source and exports whatever Ref resolves to
// then. Changes the agent makes to the copy are discarded on return.
type ReadspaceRepo struct {
	Repo       string    `json:"repo"`            // owner/name
	Ref        string    `json:"ref,omitempty"`   // branch, tag or commit; "" follows the default branch
	Source     string    `json:"source"`          // the shared clone it is exported from (readspaces/repos beside the slopspaces)
	SHA        string    `json:"sha,omitempty"`   // the commit in readspaces/repos now
	ResolvedAt time.Time `json:"resolved_at"`     // when SHA was last resolved
	Error      string    `json:"error,omitempty"` // the last refresh's problem (a failed fetch keeps the last commit)

	// Resolutions records every new commit Ref resolved to, oldest first (at
	// most MaxReadspaceResolutions), so it is known which commit each
	// deployment saw.
	Resolutions []ReadspaceResolution `json:"resolutions,omitempty"`
}

// ReadspaceResolution is one new commit a read-only repo's ref resolved to.
type ReadspaceResolution struct {
	SHA        string    `json:"sha"`
	ResolvedAt time.Time `json:"resolved_at"`
	Iteration  int       `json:"iteration"` // the slopspace's Iteration at the time
	Event      string    `json:"event"`     // add, deploy, return or refresh
}

// Deployment is one deployment cycle of a slopspace: from Deploy to Return
// (ReturnedAt is nil while it is still deployed).
//
// Proposals are the DTT requests the agent left in writespaces/dtt-requests
// when it was returned, relative to that dir (e.g. "write/repo.dtt.yaml").
// While there are proposals and no Decision, the slopspace can't be deployed
// again: the human must accept, reject or request modification of them first
// (see pkg/slopspace/proposals.go).
//
// Proposals can also be accepted or rejected one at a time (dTT-
// IntegrationImpls Step 1, Revision M); each such answer is kept in Decided,
// and Decision is set once none are left (Pending).
type Deployment struct {
	AgentType  AgentType  `json:"agent_type"`
	DeployedAt time.Time  `json:"deployed_at"`
	ReturnedAt *time.Time `json:"returned_at,omitempty"`

	Proposals []string          `json:"proposals,omitempty"`
	Decided   []ProposalVerdict `json:"decided,omitempty"` // per-proposal answers, in order
	Decision  ProposalDecision  `json:"decision,omitempty"`
	DecidedAt *time.Time        `json:"decided_at,omitempty"`
	Note      string            `json:"note,omitempty"` // modify: what the human asked to change
}

// ProposalVerdict is the human's answer to one of a deployment's proposals.
type ProposalVerdict struct {
	Proposal  string           `json:"proposal"`
	Decision  ProposalDecision `json:"decision"` // accept | reject
	DecidedAt time.Time        `json:"decided_at"`
}

// ProposalDecision is the human's answer to a deployment's DTT proposals.
type ProposalDecision string

const (
	DecisionAccept ProposalDecision = "accept"
	DecisionReject ProposalDecision = "reject"
	DecisionModify ProposalDecision = "modify"

	// DecisionMixed is a deployment's Decision when its proposals were
	// answered one at a time, and some were accepted and some rejected.
	DecisionMixed ProposalDecision = "mixed"
)

// Pending lists d's proposals without an answer yet: all of them until one
// is answered on its own, none once Decision is set.
func (d *Deployment) Pending() []string {
	if d.Decision != "" {
		return nil
	}
	if len(d.Decided) == 0 {
		return d.Proposals
	}
	done := make(map[string]bool, len(d.Decided))
	for _, v := range d.Decided {
		done[v.Proposal] = true
	}
	var out []string
	for _, p := range d.Proposals {
		if !done[p] {
			out = append(out, p)
		}
	}
	return out
}

// PendingReview returns the deployment whose proposals still await the
// human's decision -- the last one, once returned -- or nil.
func (m *SlopspaceMetadata) PendingReview() *Deployment {
	n := len(m.Deployments)
	if n == 0 {
		return nil
	}
	d := &m.Deployments[n-1]
	if d.ReturnedAt == nil || len(d.Pending()) == 0 {
		return nil
	}
	return d
}

// Config holds the runtime configuration for dungeon-keeper.
type Config struct {
	SlopspacesDir      string    // SLOPSPACES_DIR
	WorkSignalsDir     string    // WORK_SIGNALS_DIR
	AgentSlopspaceRoot string    // AGENT_SLOPSPACE_ROOT (base, without agent type)
	AgentRecordsPath   string    // AGENT_RECORDS_PATH
	AgentType          AgentType // Which agent type to run as
	WorkerID           string    // Unique identifier for this worker instance
	DevMode            bool      // --dev-mode on `watch`: this instance is running from an in-progress branch -- see docs/DevMode.md
}

// DefaultConfig returns a Config with default values.
func DefaultConfig() *Config {
	return &Config{
		SlopspacesDir:      "/host-agent-files/slopspaces",
		WorkSignalsDir:     "/host-agent-files/work",
		AgentSlopspaceRoot: "/agent",
		AgentRecordsPath:   "/host-agent-files/agent-records",
		AgentType:          AgentTypeWorker,
	}
}

// DeployPath returns the full path where slopspaces are deployed for this config.
func (c *Config) DeployPath() string {
	return c.AgentSlopspaceRoot + "/" + string(c.AgentType)
}

// DeployPathForAgentType returns the deploy path for a specific agent type.
func (c *Config) DeployPathForAgentType(agentType AgentType) string {
	return c.AgentSlopspaceRoot + "/" + string(agentType)
}

// OngoingWorkDir returns the path to ongoing work signals.
func (c *Config) OngoingWorkDir() string {
	return c.WorkSignalsDir + "/ongoing"
}

// CompleteWorkDir returns the path to completed work signals.
func (c *Config) CompleteWorkDir() string {
	return c.WorkSignalsDir + "/complete"
}

// WorkersDir returns the path to the running workers' lock files (see
// worksignal.HoldWorkerLock).
func (c *Config) WorkersDir() string {
	return c.WorkSignalsDir + "/workers"
}
