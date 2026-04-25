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

	// Test without environment variable (should use date format)
	t.Run("without AGENT_SESSION", func(t *testing.T) {
		os.Unsetenv(EnvAgentSession)

		session := getSession()
		// Should match YYYY-MM-DD format
		if len(session) != 10 || session[4] != '-' || session[7] != '-' {
			t.Errorf("expected date format YYYY-MM-DD, got '%s'", session)
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
		{"session.log", false},
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

	// Verify session.log was created
	sessionLog := filepath.Join(sessionDir, "session.log")
	logData, err := os.ReadFile(sessionLog)
	if err != nil {
		t.Fatalf("failed to read session.log: %v", err)
	}

	// Verify the log contains both records
	logStr := string(logData)
	if !strings.Contains(logStr, "echo first") {
		t.Error("session.log should contain 'echo first'")
	}
	if !strings.Contains(logStr, "echo second") {
		t.Error("session.log should contain 'echo second'")
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
