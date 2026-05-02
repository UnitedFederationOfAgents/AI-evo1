package executor

import (
	"os"
	"path/filepath"
	"testing"

	"heuristic-agent/pkg/types"
)

func TestNewExecutorWithOptions(t *testing.T) {
	cfg := types.DefaultConfig()

	e := NewExecutorWithOptions(cfg)
	if e == nil {
		t.Fatal("expected non-nil executor")
	}

	if e.config != cfg {
		t.Error("config not set correctly")
	}
}

func TestWithClauditablePath(t *testing.T) {
	cfg := types.DefaultConfig()

	customPath := "/custom/clauditable"
	e := NewExecutorWithOptions(cfg, WithClauditablePath(customPath))

	if e.clauditablePath != customPath {
		t.Errorf("expected clauditablePath %s, got %s", customPath, e.clauditablePath)
	}
}

func TestIsClauditableWrapped(t *testing.T) {
	cfg := types.DefaultConfig()

	// Without path
	e := NewExecutorWithOptions(cfg, WithClauditablePath(""))
	if e.IsClauditableWrapped() {
		t.Error("expected not wrapped when path is empty")
	}

	// With path
	e = NewExecutorWithOptions(cfg, WithClauditablePath("/path/to/clauditable"))
	if !e.IsClauditableWrapped() {
		t.Error("expected wrapped when path is set")
	}
}

func TestCommandWithoutClauditable(t *testing.T) {
	cfg := types.DefaultConfig()
	cfg.AgentRecordsPath = "/test/records"

	e := NewExecutorWithOptions(cfg, WithClauditablePath(""))

	cmd := e.Command("echo", "hello")

	if cmd.Path == "" {
		t.Error("expected non-empty path")
	}

	// Check args
	if len(cmd.Args) < 2 {
		t.Fatalf("expected at least 2 args, got %d", len(cmd.Args))
	}
	if cmd.Args[1] != "hello" {
		t.Errorf("expected args to contain 'hello', got %v", cmd.Args)
	}
}

func TestCommandWithClauditable(t *testing.T) {
	cfg := types.DefaultConfig()
	cfg.AgentRecordsPath = "/test/records"

	clauditablePath := "/path/to/clauditable"
	e := NewExecutorWithOptions(cfg, WithClauditablePath(clauditablePath))

	cmd := e.Command("echo", "hello")

	if cmd.Path != clauditablePath {
		t.Errorf("expected path %s, got %s", clauditablePath, cmd.Path)
	}

	// Check args - should be: clauditable echo hello
	if len(cmd.Args) < 3 {
		t.Fatalf("expected at least 3 args, got %d", len(cmd.Args))
	}
	// cmd.Args[0] is the command name
	if cmd.Args[1] != "echo" {
		t.Errorf("expected first arg to be 'echo', got %s", cmd.Args[1])
	}
	if cmd.Args[2] != "hello" {
		t.Errorf("expected second arg to be 'hello', got %s", cmd.Args[2])
	}
}

func TestFormatPromptForAgent(t *testing.T) {
	signal := &types.WorkSignal{
		Role:   "code-implementer",
		Prompt: "Implement the feature described in FEATURE.md",
	}

	workdir := "/agent/agent-worker/write-spaces/primary"

	prompt := FormatPromptForAgent(signal, workdir)

	if prompt == "" {
		t.Error("expected non-empty prompt")
	}

	// Check it contains the role
	if !contains(prompt, "Role: code-implementer") {
		t.Error("expected prompt to contain role")
	}

	// Check it contains the working directory
	if !contains(prompt, "Working Directory: /agent/agent-worker/write-spaces/primary") {
		t.Error("expected prompt to contain working directory")
	}

	// Check it contains the instructions
	if !contains(prompt, "Implement the feature described in FEATURE.md") {
		t.Error("expected prompt to contain instructions")
	}
}

func TestFindBinaryLocal(t *testing.T) {
	// Create a temporary directory and binary
	tmpDir := t.TempDir()

	// Save and restore working directory
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	// Create a fake binary
	fakeBinary := filepath.Join(tmpDir, "test-binary")
	if err := os.WriteFile(fakeBinary, []byte("#!/bin/bash\n"), 0755); err != nil {
		t.Fatalf("failed to create fake binary: %v", err)
	}

	// Find it
	path, err := findBinary("test-binary")
	if err != nil {
		t.Fatalf("failed to find binary: %v", err)
	}

	if path != fakeBinary {
		t.Errorf("expected path %s, got %s", fakeBinary, path)
	}
}

func TestFindBinaryNotFound(t *testing.T) {
	_, err := findBinary("definitely-not-a-real-binary-12345")
	if err == nil {
		t.Error("expected error when binary not found")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
