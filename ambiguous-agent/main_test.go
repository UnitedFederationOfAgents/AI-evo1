package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentConfigs(t *testing.T) {
	// Verify all agents in availableAgents have configurations
	for _, agent := range availableAgents {
		if _, ok := agentConfigs[agent]; !ok {
			t.Errorf("Agent %q in availableAgents but missing from agentConfigs", agent)
		}
	}

	// Verify all agents have all required mode args
	for name, config := range agentConfigs {
		for _, mode := range []string{ModePrompt, ModeRead, ModeWrite, ModeExecute} {
			if _, ok := config.ModeArgs[mode]; !ok {
				t.Errorf("Agent %q missing ModeArgs for mode %q", name, mode)
			}
		}
	}
}

func TestAgentColors(t *testing.T) {
	// Verify all agents have colors defined
	for _, agent := range availableAgents {
		if _, ok := agentColors[agent]; !ok {
			t.Errorf("Agent %q missing from agentColors", agent)
		}
	}
}

func TestBuildAgentArgs(t *testing.T) {
	tests := []struct {
		name           string
		agent          string
		mode           string
		model          string
		prompt         string
		additionalDirs []string
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:         "claude read mode",
			agent:        "claude",
			mode:         ModeRead,
			model:        "",
			prompt:       "test prompt",
			wantContains: []string{"-p", "test prompt"},
		},
		{
			name:         "claude write mode",
			agent:        "claude",
			mode:         ModeWrite,
			model:        "",
			prompt:       "test prompt",
			wantContains: []string{"-p", "test prompt", "--permission-mode", "acceptEdits"},
		},
		{
			name:         "claude execute mode",
			agent:        "claude",
			mode:         ModeExecute,
			model:        "",
			prompt:       "test prompt",
			wantContains: []string{"-p", "test prompt", "--dangerously-skip-permissions"},
		},
		{
			name:         "claude with model",
			agent:        "claude",
			mode:         ModeRead,
			model:        "opus",
			prompt:       "test prompt",
			wantContains: []string{"--model", "opus", "-p", "test prompt"},
		},
		{
			name:           "claude with additional dirs",
			agent:          "claude",
			mode:           ModeRead,
			model:          "",
			prompt:         "test prompt",
			additionalDirs: []string{"/extra/dir1", "/extra/dir2"},
			wantContains:   []string{"--add-dir", "/extra/dir1", "/extra/dir2"},
		},
		{
			name:         "clod write mode",
			agent:        "clod",
			mode:         ModeWrite,
			model:        "",
			prompt:       "test prompt",
			wantContains: []string{"-p", "test prompt", "--permission-mode", "acceptEdits"},
		},
		{
			name:           "gemini no add-dir support",
			agent:          "gemini",
			mode:           ModeRead,
			model:          "",
			prompt:         "test prompt",
			additionalDirs: []string{"/extra/dir1"},
			wantNotContain: []string{"--add-dir"}, // gemini doesn't support add-dir
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := agentConfigs[tt.agent]
			if config == nil {
				t.Fatalf("Agent %q not found in agentConfigs", tt.agent)
			}

			sessionDir := "/tmp/test-session"
			args := buildAgentArgs(config, tt.mode, tt.model, tt.prompt, sessionDir, tt.additionalDirs)
			argsStr := strings.Join(args, " ")

			for _, want := range tt.wantContains {
				found := false
				for _, arg := range args {
					if arg == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected args to contain %q, got: %v", want, args)
				}
			}

			for _, notWant := range tt.wantNotContain {
				if strings.Contains(argsStr, notWant) {
					t.Errorf("Expected args NOT to contain %q, got: %v", notWant, args)
				}
			}
		})
	}
}

func TestModeDescription(t *testing.T) {
	tests := []struct {
		mode string
		want string
	}{
		{ModePrompt, "prompt (chat only)"},
		{ModeRead, "read (files read-only)"},
		{ModeWrite, "write (files read/write)"},
		{ModeExecute, "execute (full access)"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			got := modeDescription(tt.mode)
			if got != tt.want {
				t.Errorf("modeDescription(%q) = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

func TestGetAgentStyle(t *testing.T) {
	// Test that known agents get their specific colors
	for agent := range agentColors {
		style := getAgentStyle(agent)
		// Just verify it doesn't panic and returns a style
		rendered := style.Render("test")
		if rendered == "" {
			t.Errorf("getAgentStyle(%q) returned empty render", agent)
		}
	}

	// Test unknown agent gets fallback color
	style := getAgentStyle("unknown-agent")
	rendered := style.Render("test")
	if rendered == "" {
		t.Error("getAgentStyle for unknown agent returned empty render")
	}
}

func TestSessionDirCreation(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "ambiguous-agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sessionDir := filepath.Join(tmpDir, "test-session-123")
	err = os.MkdirAll(sessionDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create session dir: %v", err)
	}

	// Verify directory was created
	info, err := os.Stat(sessionDir)
	if err != nil {
		t.Fatalf("Session directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("Session path is not a directory")
	}
}

func TestClodAgentConfig(t *testing.T) {
	// Verify clod agent is properly configured for testing
	config, ok := agentConfigs["clod"]
	if !ok {
		t.Fatal("clod agent not found in agentConfigs")
	}

	if config.Command != "clod" {
		t.Errorf("clod command = %q, want %q", config.Command, "clod")
	}

	if config.PromptFlag != "-p" {
		t.Errorf("clod PromptFlag = %q, want %q", config.PromptFlag, "-p")
	}

	// Verify write mode has permission-mode flag
	writeArgs, ok := config.ModeArgs[ModeWrite]
	if !ok {
		t.Fatal("clod missing ModeArgs for write mode")
	}

	found := false
	for _, arg := range writeArgs {
		if arg == "--permission-mode" {
			found = true
			break
		}
	}
	if !found {
		t.Error("clod write mode missing --permission-mode flag")
	}
}

func TestOpenCodePromptHandling(t *testing.T) {
	// OpenCode uses positional prompts (no -p flag)
	config := agentConfigs["opencode"]
	if config.PromptFlag != "" {
		t.Errorf("opencode should have empty PromptFlag, got %q", config.PromptFlag)
	}

	// Verify prompt is passed as last argument
	args := buildAgentArgs(config, ModeRead, "", "test prompt", "/tmp/session", nil)
	if len(args) == 0 {
		t.Fatal("buildAgentArgs returned empty args")
	}

	// Last arg should be the prompt
	lastArg := args[len(args)-1]
	if lastArg != "test prompt" {
		t.Errorf("Last arg = %q, want %q", lastArg, "test prompt")
	}
}
