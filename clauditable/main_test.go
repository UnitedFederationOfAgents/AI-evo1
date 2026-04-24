package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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

func TestWriteRawRecord(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "clauditable-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	record := RawRecord{
		Event: Event{
			Timestamp:  "2026-01-15T10:30:00Z",
			EventType:  "command_execution",
			Command:    "echo hello",
			DurationMs: 50,
			ExitCode:   0,
		},
		Stdout: "hello\n",
		Stderr: "",
	}

	recordPath, err := writeRawRecord(tmpDir, "test-session", 1705312200, record)
	if err != nil {
		t.Fatalf("writeRawRecord failed: %v", err)
	}

	// Verify the file was created
	expectedPath := filepath.Join(tmpDir, "test-session", "1705312200")
	if recordPath != expectedPath {
		t.Errorf("expected path %s, got %s", expectedPath, recordPath)
	}

	// Verify file contents
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("failed to read record file: %v", err)
	}

	var readRecord RawRecord
	if err := json.Unmarshal(data, &readRecord); err != nil {
		t.Fatalf("failed to unmarshal record: %v", err)
	}

	if readRecord.Event.Command != "echo hello" {
		t.Errorf("expected command 'echo hello', got '%s'", readRecord.Event.Command)
	}
	if readRecord.Stdout != "hello\n" {
		t.Errorf("expected stdout 'hello\\n', got '%s'", readRecord.Stdout)
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

	// Create two timestamp files
	record1 := RawRecord{
		Event: Event{
			Timestamp: "2026-01-15T10:00:00Z",
			EventType: "command_execution",
			Command:   "echo first",
		},
	}
	record2 := RawRecord{
		Event: Event{
			Timestamp: "2026-01-15T10:01:00Z",
			EventType: "command_execution",
			Command:   "echo second",
		},
	}

	data1, _ := json.Marshal(record1)
	data2, _ := json.Marshal(record2)

	os.WriteFile(filepath.Join(sessionDir, "1705312800"), data1, 0644)
	os.WriteFile(filepath.Join(sessionDir, "1705312860"), data2, 0644)

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
	if !contains(logStr, "echo first") {
		t.Error("session.log should contain 'echo first'")
	}
	if !contains(logStr, "echo second") {
		t.Error("session.log should contain 'echo second'")
	}

	// Verify original files were deleted
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312800")); !os.IsNotExist(err) {
		t.Error("timestamp file should have been deleted after consolidation")
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "1705312860")); !os.IsNotExist(err) {
		t.Error("timestamp file should have been deleted after consolidation")
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
