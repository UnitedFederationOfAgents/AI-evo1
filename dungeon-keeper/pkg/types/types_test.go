package types

import (
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.SlopspacesDir != "/host-agent-files/slopspaces" {
		t.Errorf("unexpected SlopspacesDir: %s", cfg.SlopspacesDir)
	}
	if cfg.WorkSignalsDir != "/host-agent-files/work" {
		t.Errorf("unexpected WorkSignalsDir: %s", cfg.WorkSignalsDir)
	}
	if cfg.AgentSlopspaceRoot != "/agent" {
		t.Errorf("unexpected AgentSlopspaceRoot: %s", cfg.AgentSlopspaceRoot)
	}
	if cfg.AgentRecordsPath != "/host-agent-files/agent-records" {
		t.Errorf("unexpected AgentRecordsPath: %s", cfg.AgentRecordsPath)
	}
	if cfg.AgentType != AgentTypeWorker {
		t.Errorf("unexpected AgentType: %s", cfg.AgentType)
	}
}

func TestConfigDeployPath(t *testing.T) {
	cfg := DefaultConfig()

	// Test agent-worker deploy path
	cfg.AgentType = AgentTypeWorker
	if cfg.DeployPath() != "/agent/agent-worker" {
		t.Errorf("unexpected DeployPath: %s", cfg.DeployPath())
	}

	// Test heuristic-request deploy path
	cfg.AgentType = AgentTypeHeuristic
	if cfg.DeployPath() != "/agent/heuristic-request" {
		t.Errorf("unexpected DeployPath: %s", cfg.DeployPath())
	}
}

func TestConfigDeployPathForAgentType(t *testing.T) {
	cfg := DefaultConfig()

	workerPath := cfg.DeployPathForAgentType(AgentTypeWorker)
	if workerPath != "/agent/agent-worker" {
		t.Errorf("unexpected worker deploy path: %s", workerPath)
	}

	heuristicPath := cfg.DeployPathForAgentType(AgentTypeHeuristic)
	if heuristicPath != "/agent/heuristic-request" {
		t.Errorf("unexpected heuristic deploy path: %s", heuristicPath)
	}
}

func TestConfigWorkDirs(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.OngoingWorkDir() != "/host-agent-files/work/ongoing" {
		t.Errorf("unexpected OngoingWorkDir: %s", cfg.OngoingWorkDir())
	}
	if cfg.CompleteWorkDir() != "/host-agent-files/work/complete" {
		t.Errorf("unexpected CompleteWorkDir: %s", cfg.CompleteWorkDir())
	}
}

func TestAgentTypeConstants(t *testing.T) {
	if AgentTypeWorker != "agent-worker" {
		t.Errorf("unexpected AgentTypeWorker: %s", AgentTypeWorker)
	}
	if AgentTypeHeuristic != "heuristic-request" {
		t.Errorf("unexpected AgentTypeHeuristic: %s", AgentTypeHeuristic)
	}
}

func TestWorkStatusConstants(t *testing.T) {
	if WorkStatusPending != "pending" {
		t.Errorf("unexpected WorkStatusPending: %s", WorkStatusPending)
	}
	if WorkStatusProcessing != "processing" {
		t.Errorf("unexpected WorkStatusProcessing: %s", WorkStatusProcessing)
	}
	if WorkStatusCompleted != "completed" {
		t.Errorf("unexpected WorkStatusCompleted: %s", WorkStatusCompleted)
	}
	if WorkStatusFailed != "failed" {
		t.Errorf("unexpected WorkStatusFailed: %s", WorkStatusFailed)
	}
}

func TestWorkTypeConstants(t *testing.T) {
	if WorkTypeSlopspace != "slopspace" {
		t.Errorf("unexpected WorkTypeSlopspace: %s", WorkTypeSlopspace)
	}
	if WorkTypeInPlace != "in-place" {
		t.Errorf("unexpected WorkTypeInPlace: %s", WorkTypeInPlace)
	}
}
