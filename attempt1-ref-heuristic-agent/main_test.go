package main

import (
	"os"
	"testing"
	"time"

	"heuristic-agent/pkg/types"
)

func TestLoadConfig(t *testing.T) {
	// Save original env vars
	origSlopspaces := os.Getenv("SLOPSPACES_DIR")
	origWorkSignals := os.Getenv("WORK_SIGNALS_DIR")
	origAgentRoot := os.Getenv("AGENT_SLOPSPACE_ROOT")
	origRecords := os.Getenv("AGENT_RECORDS_PATH")

	defer func() {
		os.Setenv("SLOPSPACES_DIR", origSlopspaces)
		os.Setenv("WORK_SIGNALS_DIR", origWorkSignals)
		os.Setenv("AGENT_SLOPSPACE_ROOT", origAgentRoot)
		os.Setenv("AGENT_RECORDS_PATH", origRecords)
	}()

	// Test with defaults
	os.Unsetenv("SLOPSPACES_DIR")
	os.Unsetenv("WORK_SIGNALS_DIR")
	os.Unsetenv("AGENT_SLOPSPACE_ROOT")
	os.Unsetenv("AGENT_RECORDS_PATH")

	cfg := loadConfig()

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
	if cfg.WorkerID == "" {
		t.Error("expected non-empty WorkerID")
	}

	// Test with custom values
	os.Setenv("SLOPSPACES_DIR", "/custom/slopspaces")
	os.Setenv("WORK_SIGNALS_DIR", "/custom/work")
	os.Setenv("AGENT_SLOPSPACE_ROOT", "/custom/agent")
	os.Setenv("AGENT_RECORDS_PATH", "/custom/records")

	cfg = loadConfig()

	if cfg.SlopspacesDir != "/custom/slopspaces" {
		t.Errorf("unexpected custom SlopspacesDir: %s", cfg.SlopspacesDir)
	}
	if cfg.WorkSignalsDir != "/custom/work" {
		t.Errorf("unexpected custom WorkSignalsDir: %s", cfg.WorkSignalsDir)
	}
	if cfg.AgentSlopspaceRoot != "/custom/agent" {
		t.Errorf("unexpected custom AgentSlopspaceRoot: %s", cfg.AgentSlopspaceRoot)
	}
	if cfg.AgentRecordsPath != "/custom/records" {
		t.Errorf("unexpected custom AgentRecordsPath: %s", cfg.AgentRecordsPath)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h"},
		{5 * time.Hour, "5h"},
		{30 * time.Hour, "1d"},
		{48 * time.Hour, "2d"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatDuration(tt.duration)
			if result != tt.expected {
				t.Errorf("formatDuration(%v) = %s, expected %s", tt.duration, result, tt.expected)
			}
		})
	}
}

func TestBackoffLevels(t *testing.T) {
	expected := []time.Duration{
		30 * time.Second,
		5 * time.Minute,
		1 * time.Hour,
		24 * time.Hour,
	}

	if len(backoffLevels) != len(expected) {
		t.Errorf("expected %d backoff levels, got %d", len(expected), len(backoffLevels))
	}

	for i, e := range expected {
		if backoffLevels[i] != e {
			t.Errorf("backoffLevels[%d] = %v, expected %v", i, backoffLevels[i], e)
		}
	}
}

func TestNewWorker(t *testing.T) {
	cfg := types.DefaultConfig()
	cfg.WorkerID = "test1234"

	worker := NewWorker(cfg)

	if worker == nil {
		t.Fatal("expected non-nil worker")
	}
	if worker.workerID != "test1234" {
		t.Errorf("unexpected workerID: %s", worker.workerID)
	}
	if worker.config != cfg {
		t.Error("config not set correctly")
	}
	if worker.slopspaceMgr == nil {
		t.Error("slopspaceMgr not initialized")
	}
	if worker.workSignalMgr == nil {
		t.Error("workSignalMgr not initialized")
	}
	if worker.executor == nil {
		t.Error("executor not initialized")
	}
}

func TestWorkerEnsureDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = tmpDir + "/slopspaces"
	cfg.WorkSignalsDir = tmpDir + "/work"
	cfg.AgentSlopspaceRoot = tmpDir + "/agent"
	cfg.AgentRecordsPath = tmpDir + "/records"
	cfg.WorkerID = "test1234"

	worker := NewWorker(cfg)

	if err := worker.ensureDirectories(); err != nil {
		t.Fatalf("failed to ensure directories: %v", err)
	}

	expectedDirs := []string{
		cfg.SlopspacesDir,
		cfg.OngoingWorkDir(),
		cfg.CompleteWorkDir(),
		cfg.AgentRecordsPath,
		cfg.DeployPath(),
	}

	for _, dir := range expectedDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("expected directory to exist: %s", dir)
		}
	}
}
