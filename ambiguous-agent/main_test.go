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
		config, ok := agentConfigs[agent]
		if !ok {
			t.Errorf("Agent %q in availableAgents but missing from agentConfigs", agent)
			continue
		}
		if verbosityManagement(config) == "" {
			t.Errorf("Agent %q has empty verbosity management default", agent)
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

func TestVerbosityManagementDefaults(t *testing.T) {
	for _, agent := range availableAgents {
		config := agentConfigs[agent]
		if agent == "codex" {
			if config.VerbosityManagement != VerbosityManagementAfterLine {
				t.Errorf("codex VerbosityManagement = %q, want %q", config.VerbosityManagement, VerbosityManagementAfterLine)
			}
			if config.VerbosityManagementAfterLine != "tokens used" {
				t.Errorf("codex VerbosityManagementAfterLine = %q, want %q", config.VerbosityManagementAfterLine, "tokens used")
			}
			continue
		}

		if verbosityManagement(config) != VerbosityManagementOff {
			t.Errorf("%s VerbosityManagement = %q, want %q", agent, verbosityManagement(config), VerbosityManagementOff)
		}
		if config.VerbosityManagementAfterLine != "" {
			t.Errorf("%s should not have a verbosity after-line argument, got %q", agent, config.VerbosityManagementAfterLine)
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
	// OpenCode's default command treats a positional as a project directory.
	// Non-interactive prompts must go through `opencode run`.
	config := agentConfigs["opencode"]
	if len(config.CommandArgs) != 1 || config.CommandArgs[0] != "run" {
		t.Fatalf("opencode CommandArgs = %v, want [run]", config.CommandArgs)
	}
	if config.PromptFlag != "" {
		t.Errorf("opencode should have empty PromptFlag, got %q", config.PromptFlag)
	}

	args := buildAgentArgs(config, ModeRead, "xai/grok-4.7", "test prompt", "/tmp/session", nil)
	if len(args) == 0 || args[0] != "run" {
		t.Fatalf("opencode args should start with run, got: %v", args)
	}
	if args[len(args)-1] != "test prompt" {
		t.Errorf("Last arg = %q, want %q", args[len(args)-1], "test prompt")
	}
	want := []string{"run", "--model", "xai/grok-4.7", "--agent", "plan", "--auto", "test prompt"}
	for _, w := range want {
		found := false
		for _, arg := range args {
			if arg == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected opencode args to contain %q, got: %v", w, args)
		}
	}

	execArgs := buildAgentArgs(config, ModeExecute, "xai/grok-4.7", "run tests", "/tmp/session", nil)
	if !strings.Contains(strings.Join(execArgs, " "), "build") {
		t.Fatalf("execute mode should select the build agent, got: %v", execArgs)
	}
}

func TestMatchListedModel(t *testing.T) {
	models := []string{"xai/grok-4.7", "xai/grok-4.6", "opencode/big-pickle"}
	got, err := matchListedModel(models, "xai/grok-4.7")
	if err != nil || got != "xai/grok-4.7" {
		t.Fatalf("exact match = %q, %v", got, err)
	}
	got, err = matchListedModel(models, "grok-4.7")
	if err != nil || got != "xai/grok-4.7" {
		t.Fatalf("suffix match = %q, %v", got, err)
	}
	if _, err := matchListedModel(models, "missing"); err == nil {
		t.Fatal("expected error for unknown model")
	}
	ambiguous := []string{"xai/grok-4.7", "other/grok-4.7"}
	if _, err := matchListedModel(ambiguous, "grok-4.7"); err == nil {
		t.Fatal("expected error for ambiguous suffix")
	}
}

func TestFilterChatModels(t *testing.T) {
	got := filterChatModels([]string{"xai/grok-4.7", "xai/grok-imagine-video-1.5-lite", "xai/grok-imagine-image"})
	if len(got) != 1 || got[0] != "xai/grok-4.7" {
		t.Fatalf("filterChatModels = %v", got)
	}
}

func TestPiPromptHandling(t *testing.T) {
	config := agentConfigs["pi"]
	if config.Command != "pi" {
		t.Fatalf("pi Command = %q", config.Command)
	}
	if config.PromptFlag != "" {
		t.Errorf("pi PromptFlag = %q, want empty (positional; -p is --print)", config.PromptFlag)
	}
	wantPrefix := []string{"--print", "--approve", "--no-session"}
	if strings.Join(config.CommandArgs, " ") != strings.Join(wantPrefix, " ") {
		t.Fatalf("pi CommandArgs = %v, want %v", config.CommandArgs, wantPrefix)
	}

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")

	args := buildAgentArgs(config, ModeRead, "sonnet", "identify yourself", "/tmp/session", []string{"/extra"})
	for _, want := range []string{"--print", "--approve", "--no-session", "--provider", "anthropic", "--model", "sonnet", "--thinking", "low", "--tools", piReadTools, "--", "identify yourself"} {
		if !containsArg(args, want) {
			t.Errorf("read args missing %q: %v", want, args)
		}
	}
	if strings.Contains(strings.Join(args, " "), "--add-dir") {
		t.Errorf("pi has no add-dir flag, got %v", args)
	}
	if !containsSequence(args, "--model", "sonnet") || !containsSequence(args, "--provider", "anthropic") {
		t.Errorf("read args = %v", args)
	}

	promptArgs := buildAgentArgs(config, ModePrompt, "", "hi", "/tmp/session", nil)
	if !containsArg(promptArgs, "--no-tools") {
		t.Errorf("prompt mode should pass --no-tools, got %v", promptArgs)
	}

	writeArgs := buildAgentArgs(config, ModeWrite, "google/gemini", "write cats.md", "/tmp/session", nil)
	if !containsArg(writeArgs, piWriteTools) || !containsSequence(writeArgs, "--provider", "google") || !containsSequence(writeArgs, "--model", "gemini") || !containsSequence(writeArgs, "--thinking", "low") {
		t.Errorf("write args = %v", writeArgs)
	}

	execArgs := buildAgentArgs(config, ModeExecute, "xai/grok-4:high", "run tests", "/tmp/session", nil)
	if !containsArg(execArgs, piExecTools) || !containsSequence(execArgs, "--provider", "xai") || !containsSequence(execArgs, "--model", "grok-4:high") || containsArg(execArgs, "--thinking") {
		t.Errorf("execute args should keep the :thinking suffix and not pass --thinking, got %v", execArgs)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsSequence(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

func TestParsePiModels(t *testing.T) {
	out := "Warning: stale catalog\n" +
		"provider  model                 context  max-out  thinking  images\n" +
		"anthropic  claude-sonnet-4-5     200K     64K      yes       yes\n" +
		"google     gemini-2.5-pro        1M       64K      yes       yes\n" +
		"xai        grok-4                256K     32K      yes       no\n"
	got := parsePiModels(out)
	want := []string{"anthropic/claude-sonnet-4-5", "google/gemini-2.5-pro", "xai/grok-4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parsePiModels = %v", got)
	}
	resolved, err := matchPiModel(got, "sonnet")
	if err != nil || resolved != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("fuzzy sonnet = %q, %v", resolved, err)
	}
	resolved, err = matchPiModel(got, "grok")
	if err != nil || resolved != "xai/grok-4" {
		t.Fatalf("fuzzy grok = %q, %v", resolved, err)
	}
	ambiguous := append(got, "xai/grok-4-fast")
	if _, err := matchPiModel(ambiguous, "grok"); err == nil {
		t.Fatal("expected ambiguous grok match")
	}
}

func TestPiProviderFromEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "g")
	t.Setenv("XAI_API_KEY", "x")
	if got := piProviderFromEnv(); got != "google" {
		t.Fatalf("provider = %q, want google (first non-empty after anthropic)", got)
	}
}

func TestCodexPromptHandling(t *testing.T) {
	config := agentConfigs["codex"]
	if len(config.CommandArgs) != 1 || config.CommandArgs[0] != "exec" {
		t.Fatalf("codex CommandArgs = %v, want [exec]", config.CommandArgs)
	}
	if config.PromptFlag != "" {
		t.Errorf("codex should use positional prompts because -p is --profile, got %q", config.PromptFlag)
	}
	if config.AddDirFlag != "--add-dir" {
		t.Errorf("codex AddDirFlag = %q, want %q", config.AddDirFlag, "--add-dir")
	}
	if config.ModelFlag != "--model" {
		t.Errorf("codex ModelFlag = %q, want %q", config.ModelFlag, "--model")
	}

	args := buildAgentArgs(config, ModePrompt, "", "U up?", "/tmp/session", nil)
	if len(args) == 0 || args[0] != "exec" {
		t.Fatalf("codex args should start with exec for non-interactive mode, got: %v", args)
	}
	if strings.Contains(strings.Join(args, " "), "-p") {
		t.Fatalf("codex args must not contain -p: %v", args)
	}
	if len(args) == 0 || args[len(args)-1] != "U up?" {
		t.Fatalf("codex prompt should be the last positional arg, got: %v", args)
	}

	args = buildAgentArgs(config, ModeExecute, "gpt-5.2", "run tests", "/tmp/session", []string{"/extra"})
	if len(args) == 0 || args[0] != "exec" {
		t.Fatalf("codex execute args should start with exec, got: %v", args)
	}
	wantContains := []string{"exec", "--model", "gpt-5.2", "--sandbox", "danger-full-access", "--ask-for-approval", "never", "--add-dir", "/tmp/session", "/extra", "run tests"}
	for _, want := range wantContains {
		found := false
		for _, arg := range args {
			if arg == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected codex args to contain %q, got: %v", want, args)
		}
	}
}
