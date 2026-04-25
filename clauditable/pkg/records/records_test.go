package records

import (
	"strings"
	"testing"
)

func TestFormatWithPrefix(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		prefix    string
		maxLines  int
		wantLines []string
	}{
		{
			name:      "empty text",
			text:      "",
			prefix:    "IN>> ",
			maxLines:  20,
			wantLines: nil,
		},
		{
			name:      "single line",
			text:      "hello",
			prefix:    "IN>> ",
			maxLines:  20,
			wantLines: []string{"IN>> hello"},
		},
		{
			name:      "multiple lines under limit",
			text:      "line1\nline2\nline3",
			prefix:    "OUT>> ",
			maxLines:  20,
			wantLines: []string{"OUT>> line1", "OUT>> line2", "OUT>> line3"},
		},
		{
			name:      "lines at limit",
			text:      "1\n2\n3",
			prefix:    "X>> ",
			maxLines:  3,
			wantLines: []string{"X>> 1", "X>> 2", "X>> 3"},
		},
		{
			name:      "lines over limit",
			text:      "1\n2\n3\n4",
			prefix:    "X>> ",
			maxLines:  3,
			wantLines: []string{"X>> 1", "X>> 2", "X>> 3", "X>> ..."},
		},
		{
			name:      "trailing newline handled",
			text:      "hello\n",
			prefix:    "IN>> ",
			maxLines:  20,
			wantLines: []string{"IN>> hello"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatWithPrefix(tt.text, tt.prefix, tt.maxLines)

			if tt.wantLines == nil {
				if result != "" {
					t.Errorf("expected empty string, got %q", result)
				}
				return
			}

			lines := strings.Split(strings.TrimSuffix(result, "\n"), "\n")
			if len(lines) != len(tt.wantLines) {
				t.Errorf("expected %d lines, got %d: %v", len(tt.wantLines), len(lines), lines)
				return
			}

			for i, want := range tt.wantLines {
				if lines[i] != want {
					t.Errorf("line %d: expected %q, got %q", i, want, lines[i])
				}
			}
		})
	}
}

func TestRecordFormatSessionLog(t *testing.T) {
	record := Record{
		Event: Event{
			Timestamp:  "2026-04-24T10:30:00Z",
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

	result := record.FormatSessionLog()

	// Should start with JSON
	if !strings.HasPrefix(result, "{") {
		t.Error("session log should start with JSON")
	}

	// Should NOT contain the command in JSON (command is now in plaintext only)
	if strings.Contains(result, `"command"`) {
		t.Error("session log should NOT contain command in JSON")
	}

	// Should contain agent and model in JSON
	if !strings.Contains(result, `"agent":"claude"`) {
		t.Error("session log should contain agent in JSON")
	}
	if !strings.Contains(result, `"model":"opus-4"`) {
		t.Error("session log should contain model in JSON")
	}

	// Should have IN>> prefixed command
	if !strings.Contains(result, "IN>> echo hello") {
		t.Error("session log should contain IN>> prefixed command")
	}

	// Should have OUT>> prefixed output
	if !strings.Contains(result, "OUT>> hello") {
		t.Error("session log should contain OUT>> prefixed output")
	}
}

func TestRecordFormatSessionLogWithStderr(t *testing.T) {
	record := Record{
		Event: Event{
			Timestamp:  "2026-04-24T10:30:00Z",
			EventType:  "command_execution",
			DurationMs: 10,
			ExitCode:   1,
		},
		Command: "badcmd",
		Stdout:  "",
		Stderr:  "command not found\n",
	}

	result := record.FormatSessionLog()

	// Should have ERR>> prefixed error
	if !strings.Contains(result, "ERR>> command not found") {
		t.Error("session log should contain ERR>> prefixed error")
	}

	// Should NOT have OUT>> since stdout is empty
	if strings.Contains(result, "OUT>> ") {
		t.Error("session log should not contain OUT>> for empty stdout")
	}
}

func TestRecordFormatSessionLogWithMetadata(t *testing.T) {
	record := Record{
		Event: Event{
			Timestamp: "2026-04-24T10:30:00Z",
			EventType: "command_execution",
			Metadata: map[string]string{
				"pwd":        "/home/user",
				"git_branch": "main",
			},
		},
		Command: "ls",
		Stdout:  "file.txt\n",
	}

	result := record.FormatSessionLog()

	// Should contain metadata in JSON
	if !strings.Contains(result, `"metadata"`) {
		t.Error("session log should contain metadata field")
	}
	if !strings.Contains(result, `"pwd":"/home/user"`) {
		t.Error("session log should contain pwd in metadata")
	}
}

func TestRecordFormatRawFile(t *testing.T) {
	record := Record{
		Event:   Event{},
		Command: "echo hello",
		Stdout:  "hello\n",
		Stderr:  "",
	}

	result := record.FormatRawFile()

	// Should start with command
	if !strings.HasPrefix(result, "echo hello") {
		t.Error("raw file should start with command")
	}

	// Should contain response separator
	if !strings.Contains(result, ResponseSeparator) {
		t.Error("raw file should contain response separator")
	}

	// Should contain output
	if !strings.Contains(result, "hello") {
		t.Error("raw file should contain output")
	}
}

func TestRecordFormatRawFileWithStderr(t *testing.T) {
	record := Record{
		Event:   Event{},
		Command: "test cmd",
		Stdout:  "output\n",
		Stderr:  "error\n",
	}

	result := record.FormatRawFile()

	// Should contain [STDERR] marker
	if !strings.Contains(result, "[STDERR]") {
		t.Error("raw file should contain [STDERR] marker when stderr present")
	}

	// Should contain stderr content
	if !strings.Contains(result, "error") {
		t.Error("raw file should contain stderr content")
	}
}

func TestParseSessionLogEntry(t *testing.T) {
	entry := `{"timestamp":"2026-04-24T10:30:00Z","event_type":"command_execution","agent":"claude","model":"opus-4","duration_ms":50,"exit_code":0}
IN>> echo hello
OUT>> hello
`

	event, err := ParseSessionLogEntry(entry)
	if err != nil {
		t.Fatalf("ParseSessionLogEntry failed: %v", err)
	}

	if event.Agent != "claude" {
		t.Errorf("expected agent 'claude', got %q", event.Agent)
	}

	if event.Model != "opus-4" {
		t.Errorf("expected model 'opus-4', got %q", event.Model)
	}

	if event.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", event.ExitCode)
	}

	if event.DurationMs != 50 {
		t.Errorf("expected duration 50, got %d", event.DurationMs)
	}
}

func TestParseSessionLogEntryInvalid(t *testing.T) {
	tests := []struct {
		name  string
		entry string
	}{
		{"empty", ""},
		{"no json", "IN>> hello\nOUT>> world"},
		{"invalid json", "{not valid json}\nIN>> hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSessionLogEntry(tt.entry)
			if err == nil {
				t.Error("expected error for invalid entry")
			}
		})
	}
}

func TestNewEvent(t *testing.T) {
	event := NewEvent("command_execution")

	if event.EventType != "command_execution" {
		t.Errorf("expected event_type 'command_execution', got %q", event.EventType)
	}

	if event.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
}
