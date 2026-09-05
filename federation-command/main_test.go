package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ufaconfig "ufa-configurable"
)

// writeConfigFile is a test helper for laying down ufa-configurable YAML files.
func writeConfigFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

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

func TestParseRidealongCommand(t *testing.T) {
	tests := []struct {
		line     string
		filePath string
		debug    bool
	}{
		{"ridealong tour.md", "tour.md", false},
		{"ridealong docs/tours/brief-tour.md", "docs/tours/brief-tour.md", false},
		{"ridealong --debug tour.md", "tour.md", true},
		{"ridealong --debug docs/tours/brief-tour.md", "docs/tours/brief-tour.md", true},
		{"ridealong --debug", "", true},
		{"ridealong", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			filePath, debug, _ := parseRidealongCommand(tt.line)
			if filePath != tt.filePath {
				t.Errorf("parseRidealongCommand(%q) filePath = %q, want %q", tt.line, filePath, tt.filePath)
			}
			if debug != tt.debug {
				t.Errorf("parseRidealongCommand(%q) debug = %v, want %v", tt.line, debug, tt.debug)
			}
		})
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
		line      string
		needsCont bool
		quoteChar rune
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

// TestParseCLIArgs verifies startup flag parsing for auto-connect and --lr-port.
func TestParseCLIArgs(t *testing.T) {
	// Isolate from any real ~/.ufa/config on the host running the tests.
	t.Setenv("UFA_CONFIG_DIR", t.TempDir())

	defaultAddr := "localhost:8082"

	tests := []struct {
		name        string
		args        []string
		wantAuto    bool
		wantAddr    string
		wantHandled bool
		wantErr     bool
	}{
		{"no args", nil, false, defaultAddr, false, false},
		{"auto-connect long", []string{"--auto-connect"}, true, defaultAddr, false, false},
		{"auto-connect short", []string{"-auto-connect"}, true, defaultAddr, false, false},
		{"lr-port separate", []string{"--lr-port", "9001"}, false, "localhost:9001", false, false},
		{"lr-port equals", []string{"--lr-port=9002"}, false, "localhost:9002", false, false},
		{"auto-connect with port", []string{"--auto-connect", "--lr-port", "9003"}, true, "localhost:9003", false, false},
		{"version handled", []string{"--version"}, false, "", true, false},
		{"lr-port missing value", []string{"--lr-port"}, false, "", false, true},
		{"lr-port not a number", []string{"--lr-port", "abc"}, false, "", false, true},
		{"lr-port out of range", []string{"--lr-port", "70000"}, false, "", false, true},
		{"unknown ignored", []string{"--frobnicate", "--auto-connect"}, true, defaultAddr, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, handled, err := parseCLIArgs(tt.args)
			if handled != tt.wantHandled {
				t.Fatalf("handled = %v, want %v", handled, tt.wantHandled)
			}
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if handled {
				return
			}
			if cfg.autoConnect != tt.wantAuto {
				t.Errorf("autoConnect = %v, want %v", cfg.autoConnect, tt.wantAuto)
			}
			if cfg.lrAddr != tt.wantAddr {
				t.Errorf("lrAddr = %q, want %q", cfg.lrAddr, tt.wantAddr)
			}
		})
	}
}

// TestParseCLIArgsConfigFilePrecedence verifies config files feed cliConfig
// (per-app file beating global.yaml per key) and that command-line flags still
// win over both.
func TestParseCLIArgsConfigFilePrecedence(t *testing.T) {
	dir := t.TempDir()
	writeConfigFile(t, filepath.Join(dir, "global.yaml"), "lr-host: globalhost\nlr-port: 7000\n")
	writeConfigFile(t, filepath.Join(dir, "federation-command.yaml"), "auto-connect: true\nlr-port: 7100\n")

	conf, err := ufaconfig.Load("federation-command", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Config only: app file wins over global for lr-port; global supplies lr-host.
	cfg, handled, err := parseCLIArgsWithConfig(nil, conf)
	if err != nil || handled {
		t.Fatalf("unexpected handled=%v err=%v", handled, err)
	}
	if !cfg.autoConnect {
		t.Errorf("auto-connect from config not applied")
	}
	if cfg.lrAddr != "globalhost:7100" {
		t.Errorf("lrAddr = %q, want globalhost:7100", cfg.lrAddr)
	}

	// Flags beat config on a per-item basis.
	cfg, _, err = parseCLIArgsWithConfig([]string{"--lr-host", "flaghost", "--lr-port=9999"}, conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.lrAddr != "flaghost:9999" {
		t.Errorf("lrAddr = %q, want flaghost:9999", cfg.lrAddr)
	}
	if !cfg.autoConnect {
		t.Errorf("auto-connect from config should still apply when unrelated flags are set")
	}
}

// TestParseCLIArgsRejectsBadConfig verifies a malformed config value is a
// startup error rather than being silently ignored.
func TestParseCLIArgsRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()
	writeConfigFile(t, filepath.Join(dir, "federation-command.yaml"), "auto-connect: banana\n")
	conf, err := ufaconfig.Load("federation-command", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, _, err := parseCLIArgsWithConfig(nil, conf); err == nil {
		t.Fatalf("expected error for non-boolean auto-connect")
	}
}

// TestAutoConnectControlState verifies a completed background auto-connect adopts
// remote control when preferRemote is set (which auto-connect always sets), or
// when the user was angling for it (dot selected or a manual connect already in
// flight); an active entry-prompt session otherwise keeps local control.
func TestAutoConnectControlState(t *testing.T) {
	tests := []struct {
		name         string
		state        BlinkerState
		preferRemote bool
		want         BlinkerState
	}{
		{"dot selected -> remote", BlinkerSelect, false, BlinkerConnected},
		{"manual connect in flight -> remote", BlinkerConnecting, false, BlinkerConnected},
		{"idle entry prompt -> local", BlinkerIdle, false, BlinkerLocalControl},
		{"typing at prompt -> local", BlinkerInactive, false, BlinkerLocalControl},
		{"preferRemote forces remote from idle", BlinkerIdle, true, BlinkerConnected},
		{"preferRemote forces remote while typing", BlinkerInactive, true, BlinkerConnected},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBlinker()
			b.SetState(tt.state)
			if got := autoConnectControlState(&b, tt.preferRemote); got != tt.want {
				t.Errorf("autoConnectControlState(%v, %v) = %v, want %v", tt.state, tt.preferRemote, got, tt.want)
			}
		})
	}
}

// TestAutoConnectImpliesRemote verifies there is no separate --remote switch:
// auto-connect (flag, config key, or FC_AUTO_CONNECT) is what selects remote
// control, and an FC started without it stays locally controlled. A stray
// --remote / remote: is ignored for backward compatibility.
func TestAutoConnectImpliesRemote(t *testing.T) {
	t.Setenv("UFA_CONFIG_DIR", t.TempDir())

	cfg, handled, err := parseCLIArgs([]string{"--auto-connect"})
	if err != nil || handled {
		t.Fatalf("unexpected handled=%v err=%v", handled, err)
	}
	if !cfg.autoConnect || !cfg.remote {
		t.Errorf("--auto-connect should set autoConnect and imply remote: %+v", cfg)
	}

	cfg, _, err = parseCLIArgs(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.remote || cfg.autoConnect {
		t.Errorf("defaults should be local control (no auto-connect): %+v", cfg)
	}

	// A leftover --remote flag no longer does anything on its own.
	cfg, _, err = parseCLIArgs([]string{"--remote"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.autoConnect || cfg.remote {
		t.Errorf("bare --remote should be ignored now: %+v", cfg)
	}

	dir := t.TempDir()
	writeConfigFile(t, filepath.Join(dir, "federation-command.yaml"), "auto-connect: true\n")
	conf, err := ufaconfig.Load("federation-command", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg, _, err = parseCLIArgsWithConfig(nil, conf)
	if err != nil {
		t.Fatalf("parseCLIArgsWithConfig: %v", err)
	}
	if !cfg.autoConnect || !cfg.remote {
		t.Errorf("auto-connect: true in config should imply remote: %+v", cfg)
	}
}

// TestParseCLIArgsEnvOverrides verifies the FC_* environment variables (set by
// local-representative when it auto-launches FC through a terminal wrapper)
// configure the LR connection, sitting above the config file and below CLI flags.
func TestParseCLIArgsEnvOverrides(t *testing.T) {
	t.Run("FC_AUTO_CONNECT implies remote and sets the address", func(t *testing.T) {
		t.Setenv("UFA_CONFIG_DIR", t.TempDir())
		t.Setenv("FC_AUTO_CONNECT", "1")
		t.Setenv("FC_LR_HOST", "reprhost")
		t.Setenv("FC_LR_PORT", "8091")
		cfg, handled, err := parseCLIArgs(nil)
		if err != nil || handled {
			t.Fatalf("unexpected handled=%v err=%v", handled, err)
		}
		if !cfg.autoConnect || !cfg.remote {
			t.Errorf("FC_AUTO_CONNECT did not set autoConnect+remote: %+v", cfg)
		}
		if cfg.lrAddr != "reprhost:8091" {
			t.Errorf("lrAddr = %q, want reprhost:8091", cfg.lrAddr)
		}
	})

	t.Run("FC_AUTO_CONNECT=off keeps local control", func(t *testing.T) {
		dir := t.TempDir()
		writeConfigFile(t, filepath.Join(dir, "federation-command.yaml"), "auto-connect: true\n")
		conf, err := ufaconfig.Load("federation-command", dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		t.Setenv("FC_AUTO_CONNECT", "off")
		cfg, _, err := parseCLIArgsWithConfig(nil, conf)
		if err != nil {
			t.Fatalf("parseCLIArgsWithConfig: %v", err)
		}
		if cfg.autoConnect || cfg.remote {
			t.Errorf("FC_AUTO_CONNECT=off should override auto-connect: true from the config file: %+v", cfg)
		}
	})

	t.Run("CLI flag beats env", func(t *testing.T) {
		t.Setenv("UFA_CONFIG_DIR", t.TempDir())
		t.Setenv("FC_AUTO_CONNECT", "0")
		cfg, _, err := parseCLIArgs([]string{"--auto-connect"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.autoConnect || !cfg.remote {
			t.Errorf("--auto-connect flag should win over FC_AUTO_CONNECT=0: %+v", cfg)
		}
	})

	t.Run("bad FC_LR_PORT is an error", func(t *testing.T) {
		t.Setenv("UFA_CONFIG_DIR", t.TempDir())
		t.Setenv("FC_LR_PORT", "not-a-port")
		if _, _, err := parseCLIArgs(nil); err == nil {
			t.Fatal("expected an error for a non-numeric FC_LR_PORT")
		}
	})
}
