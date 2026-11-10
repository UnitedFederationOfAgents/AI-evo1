package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestVersion verifies that the --version flag works correctly
func TestVersion(t *testing.T) {
	// Build the binary first
	buildCmd := exec.Command("go", "build", "-o", "federation-command", ".")
	buildCmd.Dir = "."
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("failed to build binary: %v", err)
	}
	defer os.Remove("federation-command")

	// Run with --version flag
	cmd := exec.Command("./federation-command", "--version")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to run --version: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "federation-command") {
		t.Errorf("expected output to contain 'federation-command', got: %s", output)
	}
	if !strings.Contains(output, Version) {
		t.Errorf("expected output to contain version '%s', got: %s", Version, output)
	}
}

// TestVersionShortFlag verifies that the -v flag also works
func TestVersionShortFlag(t *testing.T) {
	// Build the binary first
	buildCmd := exec.Command("go", "build", "-o", "federation-command", ".")
	buildCmd.Dir = "."
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("failed to build binary: %v", err)
	}
	defer os.Remove("federation-command")

	// Run with -v flag
	cmd := exec.Command("./federation-command", "-v")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to run -v: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "federation-command") {
		t.Errorf("expected output to contain 'federation-command', got: %s", output)
	}
}

// TestIsValidAgent verifies agent validation
func TestIsValidAgent(t *testing.T) {
	tests := []struct {
		name     string
		expected bool
	}{
		{"claude", true},
		{"gemini", true},
		{"copilot", true},
		{"opencode", true},
		{"codex", true},
		{"grok", true},
		{"clod", true},
		{"unknown", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidAgent(tt.name)
			if result != tt.expected {
				t.Errorf("isValidAgent(%q) = %v, want %v", tt.name, result, tt.expected)
			}
		})
	}
}

// TestIsValidVarName verifies shell variable name validation
func TestIsValidVarName(t *testing.T) {
	tests := []struct {
		name     string
		expected bool
	}{
		{"FOO", true},
		{"foo", true},
		{"_foo", true},
		{"FOO_BAR", true},
		{"FOO123", true},
		{"123FOO", false},
		{"-FOO", false},
		{"", false},
		{"foo-bar", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidVarName(tt.name)
			if result != tt.expected {
				t.Errorf("isValidVarName(%q) = %v, want %v", tt.name, result, tt.expected)
			}
		})
	}
}

// TestAbbreviatePath verifies path abbreviation for prompts
func TestAbbreviatePath(t *testing.T) {
	// Set HOME for testing
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", "/home/user")
	defer os.Setenv("HOME", originalHome)

	tests := []struct {
		path     string
		maxLen   int
		contains string // What the result should contain
	}{
		{"/home/user", 30, "~"},
		{"/home/user/projects", 30, "~/projects"},
		{"/", 30, "/"},
		{"/home/user/very/long/path/that/exceeds/limit", 20, "..."},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := abbreviatePath(tt.path, tt.maxLen)
			if !strings.Contains(result, tt.contains) {
				t.Errorf("abbreviatePath(%q, %d) = %q, expected to contain %q", tt.path, tt.maxLen, result, tt.contains)
			}
		})
	}
}

// TestParseArgs verifies argument parsing with quotes
func TestParseArgs(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"hello world", []string{"hello", "world"}},
		{`"hello world"`, []string{"hello world"}},
		{`'hello world'`, []string{"hello world"}},
		{`-p "test prompt"`, []string{"-p", "test prompt"}},
		{`-r file.txt`, []string{"-r", "file.txt"}},
		{``, []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseArgs(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("parseArgs(%q) returned %d args, want %d", tt.input, len(result), len(tt.expected))
				return
			}
			for i, arg := range result {
				if arg != tt.expected[i] {
					t.Errorf("parseArgs(%q)[%d] = %q, want %q", tt.input, i, arg, tt.expected[i])
				}
			}
		})
	}
}

// TestCheckContinuation verifies multi-line input detection
func TestCheckContinuation(t *testing.T) {
	tests := []struct {
		line         string
		needsCont    bool
		quoteChar    rune
	}{
		{`echo hello`, false, 0},
		{`echo hello \`, true, 0},
		{`echo "hello`, true, '"'},
		{`echo 'hello`, true, '\''},
		{`echo "hello"`, false, 0},
		{`echo 'hello'`, false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			needsCont, quoteChar := checkContinuation(tt.line)
			if needsCont != tt.needsCont {
				t.Errorf("checkContinuation(%q) needsContinuation = %v, want %v", tt.line, needsCont, tt.needsCont)
			}
			if quoteChar != tt.quoteChar {
				t.Errorf("checkContinuation(%q) quoteChar = %q, want %q", tt.line, quoteChar, tt.quoteChar)
			}
		})
	}
}

// TestModeDescription verifies mode descriptions
func TestModeDescription(t *testing.T) {
	tests := []struct {
		mode     string
		expected string
	}{
		{ModePrompt, "prompt"},
		{ModeRead, "read"},
		{ModeWrite, "write"},
		{ModeExecute, "execute"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			result := modeDescription(tt.mode)
			if result != tt.expected {
				t.Errorf("modeDescription(%q) = %q, want %q", tt.mode, result, tt.expected)
			}
		})
	}
}
