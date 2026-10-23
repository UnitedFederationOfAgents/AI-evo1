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
}

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
	DTTImages    string `json:"dtt_images,omitempty"`    // Declarative-tool-tools read results
	Repos        string `json:"repos,omitempty"`         // Repos visible but not modifiable
	Files        string `json:"files,omitempty"`         // Arbitrary readable files
}

// WriteSpaces contains paths that the agent can write to (changes reflected outside).
type WriteSpaces struct {
	AgentRecords string `json:"agent_records,omitempty"` // Always available for agent to write
	DTTCanvas    string `json:"dtt_canvas,omitempty"`    // Declarative-tool-tools output
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
}

// MaxDeployments bounds the deployment history kept in a slopspace's metadata.
const MaxDeployments = 50

// Deployment is one deployment cycle of a slopspace: from Deploy to Return
// (ReturnedAt is nil while it is still deployed).
type Deployment struct {
	AgentType  AgentType  `json:"agent_type"`
	DeployedAt time.Time  `json:"deployed_at"`
	ReturnedAt *time.Time `json:"returned_at,omitempty"`
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
