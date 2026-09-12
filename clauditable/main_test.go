package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clauditable/pkg/records"
)

func TestGetSession(t *testing.T) {
	// Test with environment variable set
	t.Run("with AGENT_SESSION set", func(t *testing.T) {
		os.Setenv(EnvAgentSession, "test-session-123")
		defer os.Unsetenv(EnvAgentSession)

		session := getSession()
		if session != "test-session-123" {
			t.Errorf("expected 'test-session-123', got '%s'", session)
		}
	})

	// Test without environment variable (should use YYYY-MM-DD-default format)
	t.Run("without AGENT_SESSION", func(t *testing.T) {
		os.Unsetenv(EnvAgentSession)

		session := getSession()
		// Should match YYYY-MM-DD-default
		if !strings.HasSuffix(session, "-default") || len(session) != 18 || session[4] != '-' || session[7] != '-' {
			t.Errorf("expected YYYY-MM-DD-default format, got '%s'", session)
		}
	})

	// Test with AGENT_SESSION=default (should also map to today's default)
	t.Run("with AGENT_SESSION=default", func(t *testing.T) {
		os.Setenv(EnvAgentSession, "default")
		defer os.Unsetenv(EnvAgentSession)

		session := getSession()
		if !strings.HasSuffix(session, "-default") {
			t.Errorf("expected '-default' suffix for AGENT_SESSION=default, got '%s'", session)
		}
	})
}

func TestGetConsolidateRecords(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected bool
	}{
		{"default (unset)", "", true},
		{"explicit true", "true", true},
		{"explicit false", "false", false},
		{"1 as true", "1", true},
		{"0 as false", "0", false},
		{"invalid defaults to true", "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue == "" {
				os.Unsetenv(EnvAgentConsolidateRecords)
			} else {
				os.Setenv(EnvAgentConsolidateRecords, tt.envValue)
			}
			defer os.Unsetenv(EnvAgentConsolidateRecords)

			result := getConsolidateRecords()
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestIsUnixTimestamp(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"1234567890", true},
		{"0", true},
		{"123", true},
		{"", false},
		{"abc", false},
		{"123abc", false},
		{"session.jsonl", false},
		{"-123", false},
		{"12.34", false},
		{"1234567890-raw.txt", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isUnixTimestamp(tt.input)
			if result != tt.expected {
				t.Errorf("isUnixTimestamp(%q): expected %v, got %v", tt.input, tt.expected, result)
			}
		})
	}
}

func TestParseMetadata(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:  "single pair",
			input: "key=value",
			expected: map[string]string{
				"key": "value",
			},
		},
		{
			name:  "multiple pairs comma separated",
			input: "key1=value1,key2=value2",
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name:  "multiple pairs semicolon separated",
			input: "key1=value1;key2=value2",
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name:  "with spaces",
			input: "key1 = value1 , key2 = value2",
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name:     "invalid format no equals",
			input:    "keyonly",
			expected: nil,
		},
		{
			name:  "value with equals sign",
			input: "key=value=with=equals",
			expected: map[string]string{
				"key": "value=with=equals",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseMetadata(tt.input)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d entries, got %d", len(tt.expected), len(result))
				return
			}
			for k, v := range tt.expected {
				if result[k] != v {
					t.Errorf("expected %s=%s, got %s=%s", k, v, k, result[k])
				}
			}
		})
	}
}

func TestVerbosityRelayAfterLine(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "clauditable-verbosity-*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	relay := newVerbosityRelay(VerbosityManagementAfterLine, "tokens used")
	relay.write(tmpFile, []byte("noisy line\nmore noise\n"))
	relay.write(tmpFile, []byte("tokens"))
	relay.write(tmpFile, []byte(" used\nremaining text\n"))

	if _, err := tmpFile.Seek(0, 0); err != nil {
		t.Fatalf("failed to seek temp file: %v", err)
	}
	data, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to read temp file: %v", err)
	}

	got := string(data)
	want := "tokens used\nremaining text\n"
	if got != want {
		t.Errorf("verbosity relay output = %q, want %q", got, want)
	}
}

func TestVerbosityRelayRevealsOnContainingLine(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "clauditable-verbosity-*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	relay := newVerbosityRelay(VerbosityManagementAfterLine, "tokens used")
	relay.write(tmpFile, []byte("noisy line\n"))
	relay.write(tmpFile, []byte("123 tokens used\nshown\n"))

	data, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to read temp file: %v", err)
	}

	got := string(data)
	want := "123 tokens used\nshown\n"
	if got != want {
		t.Errorf("verbosity relay output = %q, want %q", got, want)
	}
}

func TestRunPassthroughAppliesVerbosityRelay(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "emit.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho noisy\necho '123 tokens used'\necho shown\n"), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	stdoutPath := filepath.Join(tmpDir, "stdout")
	oldStdout := os.Stdout
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("failed to capture stdout: %v", err)
	}
	os.Stdout = stdoutFile
	defer func() {
		os.Stdout = oldStdout
	}()

	exitCode := runPassthrough(scriptPath, nil, newVerbosityRelay(VerbosityManagementAfterLine, "tokens used"))
	if exitCode != 0 {
		t.Fatalf("runPassthrough exitCode = %d, want 0", exitCode)
	}
	if err := stdoutFile.Close(); err != nil {
		t.Fatalf("failed to close stdout capture: %v", err)
	}

	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatalf("failed to read stdout capture: %v", err)
	}
	got := string(data)
	want := "123 tokens used\nshown\n"
	if got != want {
		t.Errorf("runPassthrough output = %q, want %q", got, want)
	}
}

func TestVerbosityRelayOff(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "clauditable-verbosity-*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	relay := newVerbosityRelay("", "")
	relay.write(tmpFile, []byte("all output\n"))

	data, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to read temp file: %v", err)
	}
	if string(data) != "all output\n" {
		t.Errorf("verbosity relay off output = %q", string(data))
	}
}

func TestExpectedRawRecordPath(t *testing.T) {
	got := expectedRawRecordPath("/records", "session", 1705312200)
	want := filepath.Join("/records", "session", "1705312200-raw.txt")
	if got != want {
		t.Errorf("expectedRawRecordPath = %q, want %q", got, want)
	}
}

func TestWriteRecord(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "clauditable-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	record := &records.Record{
		Event: records.Event{
			Timestamp:  "2026-01-15T10:30:00Z",
			EventType:  "command_execution",
			Agent:      "claude",
			Model:      "opus-4",
			DurationMs: 50,
			ExitCode:   0,
		},
		Command: "echo hello",
		Stdout:  "hello\n",
		Stderr:  "",
	}

	recordPath, err := writeRecord(tmpDir, "test-session", 1705312200, record)
	if err != nil {
		t.Fatalf("writeRecord failed: %v", err)
	}

	// Verify the record file was created
	expectedRecordPath := filepath.Join(tmpDir, "test-session", "1705312200")
	if recordPath != expectedRecordPath {
		t.Errorf("expected record path %s, got %s", expectedRecordPath, recordPath)
	}

	// Verify the raw file was also created
	expectedRawPath := filepath.Join(tmpDir, "test-session", "1705312200-raw.txt")
	if _, err := os.Stat(expectedRawPath); os.IsNotExist(err) {
		t.Error("expected raw file to be created")
	}

	// Verify record file contents (should have JSON + prefixed lines)
	recordData, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("failed to read record file: %v", err)
	}
	recordStr := string(recordData)

	if !strings.HasPrefix(recordStr, "{") {
		t.Error("record file should start with JSON")
	}
	if !strings.Contains(recordStr, "IN>> echo hello") {
		t.Error("record file should contain IN>> prefixed command")
	}
	if !strings.Contains(recordStr, "OUT>> hello") {
		t.Error("record file should contain OUT>> prefixed output")
	}
	if !strings.Contains(recordStr, `"agent":"claude"`) {
		t.Error("record file should contain agent in JSON")
	}

	// Verify raw file contents
	rawData, err := os.ReadFile(expectedRawPath)
	if err != nil {
		t.Fatalf("failed to read raw file: %v", err)
	}
	rawStr := string(rawData)

	if !strings.HasPrefix(rawStr, "echo hello") {
		t.Error("raw file should start with command")
	}
	if !strings.Contains(rawStr, records.ResponseSeparator) {
		t.Error("raw file should contain response separator")
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello World", "hello-world"},
		{"My New Feature", "my-new-feature"},
		{"  spaces  ", "spaces"},
		{"special!@#chars", "special-chars"},
		{"already-slug", "already-slug"},
		{"  multiple   spaces  ", "multiple-spaces"},
		{"123numbers456", "123numbers456"},
		{"", ""},
		{"!!!only-special!!!", "only-special"},
	}
	for _, tt := range tests {
		got := slugify(tt.input)
		if got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGenerateSessionID(t *testing.T) {
	id := generateSessionID("My Test Session")
	if !strings.Contains(id, "my-test-session") {
		t.Errorf("generateSessionID should contain slugified name, got %q", id)
	}
	// Should have date prefix
	if len(id) < 20 || id[4] != '-' || id[7] != '-' {
		t.Errorf("generateSessionID should start with YYYY-MM-DD, got %q", id)
	}
	// Should not end in -default
	if strings.HasSuffix(id, "-default") {
		t.Errorf("generateSessionID should not produce -default suffix, got %q", id)
	}
}

func TestEnsureSession(t *testing.T) {
	tmpDir := t.TempDir()
	sessionID := "2026-01-15-test"
	name := "Test Session"

	if err := ensureSession(tmpDir, sessionID, name); err != nil {
		t.Fatalf("ensureSession failed: %v", err)
	}

	// Directory should exist
	sessionDir := filepath.Join(tmpDir, sessionID)
	if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
		t.Error("session directory should exist")
	}

	// session.yaml should exist with correct content
	yamlPath := filepath.Join(sessionDir, "session.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("session.yaml should exist: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "id: "+sessionID) {
		t.Errorf("session.yaml should contain id, got: %s", content)
	}
	if !strings.Contains(content, "name: "+name) {
		t.Errorf("session.yaml should contain name, got: %s", content)
	}

	// Calling again should not overwrite
	if err := ensureSession(tmpDir, sessionID, "Different Name"); err != nil {
		t.Fatalf("second ensureSession failed: %v", err)
	}
	data2, _ := os.ReadFile(yamlPath)
	if string(data2) != content {
		t.Error("ensureSession should not overwrite existing session.yaml")
	}
}

func TestConsolidateRecords(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "clauditable-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	session := "test-session"
	sessionDir := filepath.Join(tmpDir, session)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("failed to create session dir: %v", err)
	}

	// Create two timestamp files using the new format
	record1 := &records.Record{
		Event: records.Event{
			Timestamp: "2026-01-15T10:00:00Z",
			EventType: "command_execution",
			Agent:     "claude",
		},
		Command: "echo first",
		Stdout:  "first\n",
	}
	record2 := &records.Record{
		Event: records.Event{
			Timestamp: "2026-01-15T10:01:00Z",
			EventType: "command_execution",
			Agent:     "claude",
		},
		Command: "echo second",
		Stdout:  "second\n",
	}

	os.WriteFile(filepath.Join(sessionDir, "1705312800"), []byte(record1.FormatSessionLog()), 0644)
	os.WriteFile(filepath.Join(sessionDir, "1705312860"), []byte(record2.FormatSessionLog()), 0644)

	// Also create -raw.txt files (these should NOT be consolidated)
	os.WriteFile(filepath.Join(sessionDir, "1705312800-raw.txt"), []byte("raw content 1"), 0644)
	os.WriteFile(filepath.Join(sessionDir, "1705312860-raw.txt"), []byte("raw content 2"), 0644)

	// Run consolidation
	if err := consolidateRecords(tmpDir, session); err != nil {
		t.Fatalf("consolidateRecords failed: %v", err)
	}

	// Verify session.jsonl was created
	sessionLog := filepath.Join(sessionDir, "session.jsonl")
	logData, err := os.ReadFile(sessionLog)
	if err != nil {
		t.Fatalf("failed to read session.jsonl: %v", err)
	}

	// Verify the log contains both records
	logStr := string(logData)
	if !strings.Contains(logStr, "echo first") {
		t.Error("session.jsonl should contain 'echo first'")
	}
	if !strings.Contains(logStr, "echo second") {
		t.Error("session.jsonl should contain 'echo second'")
	}

	// Verify original timestamp files were deleted
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312800")); !os.IsNotExist(err) {
		t.Error("timestamp file should have been deleted after consolidation")
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312860")); !os.IsNotExist(err) {
		t.Error("timestamp file should have been deleted after consolidation")
	}

	// Verify -raw.txt files were NOT deleted
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312800-raw.txt")); os.IsNotExist(err) {
		t.Error("-raw.txt file should NOT have been deleted")
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312860-raw.txt")); os.IsNotExist(err) {
		t.Error("-raw.txt file should NOT have been deleted")
	}
}
