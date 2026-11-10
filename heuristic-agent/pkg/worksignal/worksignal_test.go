package worksignal

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"heuristic-agent/pkg/types"
)

func TestGenerateFilename(t *testing.T) {
	ts := time.Unix(1777744989, 0)

	tests := []struct {
		name     string
		role     string
		complete bool
		expected string
	}{
		{
			name:     "basic working",
			role:     "cat_webserver_container",
			complete: false,
			expected: "WORKING-cat_webserver_container-1777744989.jsonl",
		},
		{
			name:     "basic complete",
			role:     "cat_webserver_1",
			complete: true,
			expected: "COMPLETE-cat_webserver_1-1777744989.jsonl",
		},
		{
			name:     "with spaces",
			role:     "code implementer",
			complete: false,
			expected: "WORKING-code_implementer-1777744989.jsonl",
		},
		{
			name:     "with special chars",
			role:     "task@123!",
			complete: false,
			expected: "WORKING-task123-1777744989.jsonl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateFilename(tt.role, ts, tt.complete)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestParseFilename(t *testing.T) {
	tests := []struct {
		filename       string
		expectedName   string
		expectedTS     int64
		expectComplete bool
		expectError    bool
	}{
		{
			filename:       "WORKING-cat_webserver_container-1777744989.jsonl",
			expectedName:   "cat_webserver_container",
			expectedTS:     1777744989,
			expectComplete: false,
		},
		{
			filename:       "COMPLETE-cat_webserver_1-1777744989.jsonl",
			expectedName:   "cat_webserver_1",
			expectedTS:     1777744989,
			expectComplete: true,
		},
		{
			filename:    "invalid.jsonl",
			expectError: true,
		},
		{
			filename:    "WORKING-notsimestamp.jsonl",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			name, ts, complete, err := ParseFilename(tt.filename)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if name != tt.expectedName {
				t.Errorf("expected name %s, got %s", tt.expectedName, name)
			}
			if ts != tt.expectedTS {
				t.Errorf("expected timestamp %d, got %d", tt.expectedTS, ts)
			}
			if complete != tt.expectComplete {
				t.Errorf("expected complete %v, got %v", tt.expectComplete, complete)
			}
		})
	}
}

func TestManagerCreateAndRead(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.WorkSignalsDir = tmpDir

	mgr := NewManager(cfg)

	signal := &types.WorkSignal{
		WorkType:  types.WorkTypeSlopspace,
		AgentType: types.AgentTypeWorker,
		Role:      "test_role",
		Prompt:    "Test prompt",
		Agent:     "claude",
		Model:     "opus",
	}

	path, err := mgr.Create(signal)
	if err != nil {
		t.Fatalf("failed to create signal: %v", err)
	}

	// Check file was created
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("signal file was not created")
	}

	// Read it back
	readSignal, events, err := mgr.Read(path)
	if err != nil {
		t.Fatalf("failed to read signal: %v", err)
	}

	if readSignal.ID != signal.ID {
		t.Errorf("ID mismatch: expected %s, got %s", signal.ID, readSignal.ID)
	}
	if readSignal.Role != signal.Role {
		t.Errorf("Role mismatch: expected %s, got %s", signal.Role, readSignal.Role)
	}
	if readSignal.Status != types.WorkStatusPending {
		t.Errorf("Status should be pending, got %s", readSignal.Status)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestManagerAppendEvent(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.WorkSignalsDir = tmpDir

	mgr := NewManager(cfg)

	signal := &types.WorkSignal{
		WorkType:  types.WorkTypeSlopspace,
		AgentType: types.AgentTypeWorker,
		Role:      "test_role",
		Prompt:    "Test prompt",
		Agent:     "claude",
		Model:     "opus",
	}

	path, err := mgr.Create(signal)
	if err != nil {
		t.Fatalf("failed to create signal: %v", err)
	}

	// Append an event
	event := &types.WorkEvent{
		StatusUpdate: "processing",
		Comment:      "Starting work",
	}
	if err := mgr.AppendEvent(path, event); err != nil {
		t.Fatalf("failed to append event: %v", err)
	}

	// Read it back
	_, events, err := mgr.Read(path)
	if err != nil {
		t.Fatalf("failed to read signal: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].StatusUpdate != "processing" {
		t.Errorf("expected status update 'processing', got %s", events[0].StatusUpdate)
	}
	if events[0].Comment != "Starting work" {
		t.Errorf("expected comment 'Starting work', got %s", events[0].Comment)
	}
}

func TestManagerTakeAndReleaseOwnership(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.WorkSignalsDir = tmpDir

	mgr := NewManager(cfg)

	signal := &types.WorkSignal{
		WorkType:  types.WorkTypeSlopspace,
		AgentType: types.AgentTypeWorker,
		Role:      "test_role",
		Prompt:    "Test prompt",
		Agent:     "claude",
		Model:     "opus",
	}

	path, err := mgr.Create(signal)
	if err != nil {
		t.Fatalf("failed to create signal: %v", err)
	}

	// Take ownership
	holderID := "worker-12345678"
	if err := mgr.TakeOwnership(path, holderID); err != nil {
		t.Fatalf("failed to take ownership: %v", err)
	}

	readSignal, _, err := mgr.Read(path)
	if err != nil {
		t.Fatalf("failed to read signal: %v", err)
	}

	if readSignal.Holder != holderID {
		t.Errorf("expected holder %s, got %s", holderID, readSignal.Holder)
	}
	if readSignal.Status != types.WorkStatusProcessing {
		t.Errorf("expected status processing, got %s", readSignal.Status)
	}
	if readSignal.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}

	// Release ownership
	if err := mgr.ReleaseOwnership(path); err != nil {
		t.Fatalf("failed to release ownership: %v", err)
	}

	readSignal, _, err = mgr.Read(path)
	if err != nil {
		t.Fatalf("failed to read signal: %v", err)
	}

	if readSignal.Holder != "" {
		t.Errorf("expected empty holder, got %s", readSignal.Holder)
	}
}

func TestManagerComplete(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.WorkSignalsDir = tmpDir

	mgr := NewManager(cfg)

	signal := &types.WorkSignal{
		WorkType:  types.WorkTypeSlopspace,
		AgentType: types.AgentTypeWorker,
		Role:      "test_role",
		Prompt:    "Test prompt",
		Agent:     "claude",
		Model:     "opus",
	}

	path, err := mgr.Create(signal)
	if err != nil {
		t.Fatalf("failed to create signal: %v", err)
	}

	// Complete the signal
	if err := mgr.Complete(path, true, "Work done"); err != nil {
		t.Fatalf("failed to complete signal: %v", err)
	}

	// Original file should be gone
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("original file should be deleted")
	}

	// Check complete directory
	completeFiles, err := mgr.ListComplete()
	if err != nil {
		t.Fatalf("failed to list complete: %v", err)
	}

	if len(completeFiles) != 1 {
		t.Fatalf("expected 1 complete file, got %d", len(completeFiles))
	}

	// Verify the complete file
	readSignal, events, err := mgr.Read(completeFiles[0])
	if err != nil {
		t.Fatalf("failed to read complete signal: %v", err)
	}

	if readSignal.Status != types.WorkStatusCompleted {
		t.Errorf("expected completed status, got %s", readSignal.Status)
	}
	if readSignal.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event (completion), got %d", len(events))
	}
}

func TestManagerFindPendingForAgentType(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.WorkSignalsDir = tmpDir

	mgr := NewManager(cfg)

	// Create signals for different agent types
	workerSignal := &types.WorkSignal{
		WorkType:  types.WorkTypeSlopspace,
		AgentType: types.AgentTypeWorker,
		Role:      "worker_task",
		Prompt:    "Worker prompt",
		Agent:     "claude",
		Model:     "opus",
	}

	heuristicSignal := &types.WorkSignal{
		WorkType:  types.WorkTypeSlopspace,
		AgentType: types.AgentTypeHeuristic,
		Role:      "heuristic_task",
		Prompt:    "Heuristic prompt",
		Agent:     "claude",
		Model:     "opus",
	}

	_, err := mgr.Create(workerSignal)
	if err != nil {
		t.Fatalf("failed to create worker signal: %v", err)
	}

	_, err = mgr.Create(heuristicSignal)
	if err != nil {
		t.Fatalf("failed to create heuristic signal: %v", err)
	}

	// Find pending for worker
	workerPending, err := mgr.FindPendingForAgentType(types.AgentTypeWorker)
	if err != nil {
		t.Fatalf("failed to find pending for worker: %v", err)
	}
	if len(workerPending) != 1 {
		t.Errorf("expected 1 pending worker signal, got %d", len(workerPending))
	}

	// Find pending for heuristic
	heuristicPending, err := mgr.FindPendingForAgentType(types.AgentTypeHeuristic)
	if err != nil {
		t.Fatalf("failed to find pending for heuristic: %v", err)
	}
	if len(heuristicPending) != 1 {
		t.Errorf("expected 1 pending heuristic signal, got %d", len(heuristicPending))
	}

	// Verify filenames contain correct prefix
	workerFilename := filepath.Base(workerPending[0])
	if workerFilename[:8] != "WORKING-" {
		t.Errorf("expected WORKING- prefix, got %s", workerFilename[:8])
	}
}
