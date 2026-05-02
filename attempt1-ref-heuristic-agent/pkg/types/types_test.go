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

	if cfg.DeployPath() != "/agent/agent-worker" {
		t.Errorf("unexpected DeployPath: %s", cfg.DeployPath())
	}

	cfg.AgentType = AgentTypeHeuristic
	if cfg.DeployPath() != "/agent/heuristic-request" {
		t.Errorf("unexpected DeployPath for heuristic: %s", cfg.DeployPath())
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
		t.Errorf("unexpected AgentTypeWorker value")
	}
	if AgentTypeHeuristic != "heuristic-request" {
		t.Errorf("unexpected AgentTypeHeuristic value")
	}
}

func TestWorkStatusConstants(t *testing.T) {
	if WorkStatusPending != "pending" {
		t.Errorf("unexpected WorkStatusPending value")
	}
	if WorkStatusProcessing != "processing" {
		t.Errorf("unexpected WorkStatusProcessing value")
	}
	if WorkStatusCompleted != "completed" {
		t.Errorf("unexpected WorkStatusCompleted value")
	}
	if WorkStatusFailed != "failed" {
		t.Errorf("unexpected WorkStatusFailed value")
	}
}
