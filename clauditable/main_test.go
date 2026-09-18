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

func TestCheckIsPrimary(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "clauditable-primary-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Empty dir: we are primary
	if !checkIsPrimary(tmpDir, 1000) {
		t.Error("should be primary with no other writing files")
	}

	// Only our own writing file: still primary
	os.WriteFile(filepath.Join(tmpDir, "1000-writing.txt"), []byte("1000\n"), 0644)
	if !checkIsPrimary(tmpDir, 1000) {
		t.Error("should be primary with only our own writing file")
	}

	// Another primary writing file with lower timestamp: we are secondary
	os.WriteFile(filepath.Join(tmpDir, "900-writing.txt"), []byte("900\n"), 0644)
	if checkIsPrimary(tmpDir, 1000) {
		t.Error("should be secondary when a lower-timestamp primary writing file exists")
	}

	// Remove the lower-ts primary; add one with higher timestamp: we stay primary
	os.Remove(filepath.Join(tmpDir, "900-writing.txt"))
	os.WriteFile(filepath.Join(tmpDir, "1100-writing.txt"), []byte("1100\n"), 0644)
	if !checkIsPrimary(tmpDir, 1000) {
		t.Error("should be primary when only a higher-timestamp writing file exists")
	}

	// Replace primary with secondary writing file at lower timestamp: we stay primary
	os.Remove(filepath.Join(tmpDir, "1100-writing.txt"))
	os.WriteFile(filepath.Join(tmpDir, "900-s-writing.txt"), []byte("900\n"), 0644)
	if !checkIsPrimary(tmpDir, 1000) {
		t.Error("secondary writing files should not affect primary detection")
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

func TestWriteWrittenFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "clauditable-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sessionDir := filepath.Join(tmpDir, "test-session")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("failed to create session dir: %v", err)
	}

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
	}

	// Test primary: creates {ts}-raw.txt
	path, err := writeWrittenFile(sessionDir, 1705312200, writerFileSuffix(true, false, "", "raw.txt"), record)
	if err != nil {
		t.Fatalf("writeWrittenFile (primary) failed: %v", err)
	}
	expectedPath := filepath.Join(sessionDir, "1705312200-raw.txt")
	if path != expectedPath {
		t.Errorf("primary written file path: got %s, want %s", path, expectedPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read primary written file: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "{") {
		t.Error("written file should start with JSON (session log)")
	}
	if !strings.Contains(content, "IN>> echo hello") {
		t.Error("written file should contain IN>> prefixed command")
	}
	if !strings.Contains(content, `"agent":"claude"`) {
		t.Error("written file should contain agent in JSON")
	}
	if !strings.Contains(content, records.WrittenFileSeparator) {
		t.Error("written file should contain WrittenFileSeparator")
	}
	if !strings.Contains(content, records.ResponseSeparator) {
		t.Error("written file should contain ResponseSeparator in raw section")
	}
	// RecordPath should be set in the JSON to the written file path
	if !strings.Contains(content, "1705312200-raw.txt") {
		t.Error("written file JSON should reference the written file path")
	}

	// Test secondary: creates {ts}-s-raw.txt
	record2 := &records.Record{
		Event: records.Event{
			Timestamp: "2026-01-15T10:31:00Z",
			EventType: "command_execution",
		},
		Command: "echo secondary",
		Stdout:  "secondary\n",
	}
	path2, err := writeWrittenFile(sessionDir, 1705312260, writerFileSuffix(false, false, "", "raw.txt"), record2)
	if err != nil {
		t.Fatalf("writeWrittenFile (secondary) failed: %v", err)
	}
	expectedPath2 := filepath.Join(sessionDir, "1705312260-s-raw.txt")
	if path2 != expectedPath2 {
		t.Errorf("secondary written file path: got %s, want %s", path2, expectedPath2)
	}

	// Test remote-owned (non-owner host): creates {ts}-{host}-raw.txt
	record3 := &records.Record{
		Event: records.Event{
			Timestamp: "2026-01-15T10:32:00Z",
			EventType: "command_execution",
		},
		Command: "echo remote",
		Stdout:  "remote\n",
	}
	path3, err := writeWrittenFile(sessionDir, 1705312320, writerFileSuffix(false, true, "peer-host-ab12", "raw.txt"), record3)
	if err != nil {
		t.Fatalf("writeWrittenFile (remote-owned) failed: %v", err)
	}
	expectedPath3 := filepath.Join(sessionDir, "1705312320-peer-host-ab12-raw.txt")
	if path3 != expectedPath3 {
		t.Errorf("remote-owned written file path: got %s, want %s", path3, expectedPath3)
	}
}

func TestWriterFileSuffix(t *testing.T) {
	tests := []struct {
		name          string
		isPrimary     bool
		isRemoteOwned bool
		host          string
		kind          string
		want          string
	}{
		{"primary raw", true, false, "irrelevant", "raw.txt", "-raw.txt"},
		{"secondary raw", false, false, "irrelevant", "raw.txt", "-s-raw.txt"},
		{"remote-owned raw", false, true, "peer-host-ab12", "raw.txt", "-peer-host-ab12-raw.txt"},
		{"remote-owned writing", false, true, "peer-host-ab12", "writing.txt", "-peer-host-ab12-writing.txt"},
	}
	for _, tt := range tests {
		if got := writerFileSuffix(tt.isPrimary, tt.isRemoteOwned, tt.host, tt.kind); got != tt.want {
			t.Errorf("%s: writerFileSuffix() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestSelfProcessRemoteRecord(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "clauditable-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	record := &records.Record{
		Event: records.Event{
			Timestamp: "2026-01-15T10:33:00Z",
			EventType: "command_execution",
		},
		Command: "echo secret",
		Stdout:  "api_key=abcdefghijklmnop\n",
	}

	if err := selfProcessRemoteRecord(tmpDir, 1705312380, "peer-host-ab12", record); err != nil {
		t.Fatalf("selfProcessRemoteRecord failed: %v", err)
	}

	processedPath := filepath.Join(tmpDir, "1705312380-peer-host-ab12-processed.txt")
	data, err := os.ReadFile(processedPath)
	if err != nil {
		t.Fatalf("expected processed file at %s: %v", processedPath, err)
	}
	if !strings.Contains(string(data), "<REDACTED-") {
		t.Error("self-processed record should have redacted the secret")
	}
	// session.jsonl must never be touched by a non-owner host's self-processing.
	if _, err := os.Stat(filepath.Join(tmpDir, "session.jsonl")); !os.IsNotExist(err) {
		t.Error("selfProcessRemoteRecord must not create/touch session.jsonl")
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

func TestConsolidatePrimaryToJSONL(t *testing.T) {
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

	secondary := &records.Record{
		Event: records.Event{
			Timestamp: "2026-01-15T10:00:00Z",
			EventType: "command_execution",
			Agent:     "claude",
		},
		Command: "echo secondary",
		Stdout:  "secondary\n",
	}
	primary := &records.Record{
		Event: records.Event{
			Timestamp: "2026-01-15T10:01:00Z",
			EventType: "command_execution",
			Agent:     "claude",
		},
		Command: "echo primary",
		Stdout:  "primary\n",
	}

	// Write secondary's written file and primary's written file
	os.WriteFile(filepath.Join(sessionDir, "1705312800-s-raw.txt"), []byte(secondary.FormatWrittenFile()), 0644)
	os.WriteFile(filepath.Join(sessionDir, "1705312860-raw.txt"), []byte(primary.FormatWrittenFile()), 0644)

	if err := consolidatePrimaryToJSONL(tmpDir, session, 1705312860); err != nil {
		t.Fatalf("consolidatePrimaryToJSONL failed: %v", err)
	}

	// session.jsonl should contain both commands
	logData, err := os.ReadFile(filepath.Join(sessionDir, "session.jsonl"))
	if err != nil {
		t.Fatalf("failed to read session.jsonl: %v", err)
	}
	logStr := string(logData)
	if !strings.Contains(logStr, "echo secondary") {
		t.Error("session.jsonl should contain secondary command")
	}
	if !strings.Contains(logStr, "echo primary") {
		t.Error("session.jsonl should contain primary command")
	}
	// Secondary entry must appear before primary (sorted by timestamp)
	secIdx := strings.Index(logStr, "echo secondary")
	priIdx := strings.Index(logStr, "echo primary")
	if secIdx > priIdx {
		t.Error("secondary entry should appear before primary entry in session.jsonl")
	}
	// session.jsonl should not contain raw content (only session log portion)
	if strings.Contains(logStr, records.WrittenFileSeparator) {
		t.Error("session.jsonl should not contain WrittenFileSeparator")
	}

	// Secondary written file should be renamed (promoted to primary)
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312800-s-raw.txt")); !os.IsNotExist(err) {
		t.Error("secondary written file should be renamed after consolidation")
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312800-raw.txt")); os.IsNotExist(err) {
		t.Error("secondary written file should be renamed to 1705312800-raw.txt")
	}

	// Primary written file should NOT be deleted
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312860-raw.txt")); os.IsNotExist(err) {
		t.Error("primary written file should NOT be deleted")
	}

	// Processed files should be created for all consolidated records
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312800-processed.txt")); os.IsNotExist(err) {
		t.Error("processed file should be created for secondary record")
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312860-processed.txt")); os.IsNotExist(err) {
		t.Error("processed file should be created for primary record")
	}
}

func TestConsolidatePrimaryToJSONLHostTagged(t *testing.T) {
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

	remote := &records.Record{
		Event: records.Event{
			Timestamp: "2026-01-15T10:00:00Z",
			EventType: "command_execution",
			Agent:     "claude",
		},
		Command: "echo remote",
		Stdout:  "remote\n",
	}
	primary := &records.Record{
		Event: records.Event{
			Timestamp: "2026-01-15T10:01:00Z",
			EventType: "command_execution",
			Agent:     "claude",
		},
		Command: "echo primary",
		Stdout:  "primary\n",
	}

	host := "peer-host-ab12"
	// Simulate the non-owner host having already self-processed its own record
	// (selfProcessRemoteRecord), plus the raw file it also always writes.
	if err := selfProcessRemoteRecord(sessionDir, 1705312800, host, remote); err != nil {
		t.Fatalf("selfProcessRemoteRecord failed: %v", err)
	}
	os.WriteFile(filepath.Join(sessionDir, "1705312800-peer-host-ab12-raw.txt"), []byte(remote.FormatWrittenFile()), 0644)
	os.WriteFile(filepath.Join(sessionDir, "1705312860-raw.txt"), []byte(primary.FormatWrittenFile()), 0644)

	if err := consolidatePrimaryToJSONL(tmpDir, session, 1705312860); err != nil {
		t.Fatalf("consolidatePrimaryToJSONL failed: %v", err)
	}

	logData, err := os.ReadFile(filepath.Join(sessionDir, "session.jsonl"))
	if err != nil {
		t.Fatalf("failed to read session.jsonl: %v", err)
	}
	logStr := string(logData)
	if !strings.Contains(logStr, "echo remote") {
		t.Error("session.jsonl should contain the host-tagged remote command")
	}
	if !strings.Contains(logStr, "echo primary") {
		t.Error("session.jsonl should contain primary command")
	}

	// Host-tagged raw file must be marked consolidated (not left matching the
	// {ts}-{host}-raw.txt glob) so a later consolidation run doesn't re-append it.
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312800-peer-host-ab12-raw.txt")); !os.IsNotExist(err) {
		t.Error("host-tagged raw file should be renamed away after consolidation")
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312800-peer-host-ab12-raw.txt.consolidated")); os.IsNotExist(err) {
		t.Error("host-tagged raw file should be renamed to add a .consolidated marker")
	}

	// Running consolidation again (as a later owner command would trigger) must
	// not duplicate the host-tagged entry in session.jsonl.
	if err := consolidatePrimaryToJSONL(tmpDir, session, 1705312860); err != nil {
		t.Fatalf("second consolidatePrimaryToJSONL failed: %v", err)
	}
	logData2, err := os.ReadFile(filepath.Join(sessionDir, "session.jsonl"))
	if err != nil {
		t.Fatalf("failed to read session.jsonl: %v", err)
	}
	if n := strings.Count(string(logData2), "echo remote"); n != 1 {
		t.Errorf("echo remote should appear exactly once in session.jsonl after re-consolidation, got %d", n)
	}
}
