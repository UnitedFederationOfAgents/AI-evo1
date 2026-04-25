// Package records provides the shared event schema for clauditable and related tools.
// Records are directly collected signals, while reports are processed/composed records.
package records

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Constants for record formatting
const (
	MaxPreviewLines   = 20
	TruncationMarker  = "..."
	InputPrefix       = "IN>> "
	OutputPrefix      = "OUT>> "
	ErrorPrefix       = "ERR>> "
	ResponseSeparator = "\n\n----------RESPONSE----------\n\n"
)

// Event represents metadata about a command execution.
// The Event is stored as a JSON blob and contains only metadata,
// not the actual command input/output content.
type Event struct {
	Timestamp  string            `json:"timestamp"`             // RFC3339 formatted timestamp
	EventType  string            `json:"event_type"`            // Type of event (e.g., "command_execution")
	Agent      string            `json:"agent,omitempty"`       // Agent identifier (from UFA_AGENT)
	Model      string            `json:"model,omitempty"`       // Model identifier (from UFA_MODEL)
	DurationMs int64             `json:"duration_ms"`           // Execution duration in milliseconds
	ExitCode   int               `json:"exit_code"`             // Command exit code
	RecordPath string            `json:"record_path,omitempty"` // Path to the record file (used for both consolidated and raw)
	Metadata   map[string]string `json:"metadata,omitempty"`    // Unstructured key-value metadata
}

// NewEvent creates a new Event with the current timestamp.
func NewEvent(eventType string) Event {
	return Event{
		Timestamp: time.Now().Format(time.RFC3339),
		EventType: eventType,
	}
}

// Record contains the full interaction data for writing to files.
// This is not JSON-serialized directly; instead, the Event is serialized
// as JSON and the content (stdout/stderr) is appended as plaintext.
type Record struct {
	Event   Event
	Command string // The command that was executed (stored separately from Event)
	Stdout  string
	Stderr  string
}

// FormatSessionLog formats a Record for the session.log JSONL file.
// Format: JSON metadata on first line, then IN>> prefixed command lines,
// then OUT>> and ERR>> prefixed response lines, each up to MaxPreviewLines.
func (r *Record) FormatSessionLog() string {
	var sb strings.Builder

	// Write JSON event metadata
	eventJSON, err := json.Marshal(r.Event)
	if err != nil {
		// Fallback to a minimal JSON on error
		eventJSON = []byte(fmt.Sprintf(`{"error":"marshal_failed","timestamp":%q}`, r.Event.Timestamp))
	}
	sb.Write(eventJSON)
	sb.WriteByte('\n')

	// Write input command with IN>> prefix
	sb.WriteString(formatWithPrefix(r.Command, InputPrefix, MaxPreviewLines))

	// Write stdout with OUT>> prefix
	if r.Stdout != "" {
		sb.WriteString(formatWithPrefix(r.Stdout, OutputPrefix, MaxPreviewLines))
	}

	// Write stderr with ERR>> prefix
	if r.Stderr != "" {
		sb.WriteString(formatWithPrefix(r.Stderr, ErrorPrefix, MaxPreviewLines))
	}

	return sb.String()
}

// FormatRawFile formats the full command and response for the -raw.txt file.
// This file contains the complete untruncated content.
func (r *Record) FormatRawFile() string {
	var sb strings.Builder

	sb.WriteString(r.Command)
	sb.WriteString(ResponseSeparator)

	if r.Stdout != "" {
		sb.WriteString(r.Stdout)
	}
	if r.Stderr != "" {
		if r.Stdout != "" && !strings.HasSuffix(r.Stdout, "\n") {
			sb.WriteByte('\n')
		}
		sb.WriteString("[STDERR]\n")
		sb.WriteString(r.Stderr)
	}

	return sb.String()
}

// formatWithPrefix formats text with a prefix on each line, limiting to maxLines.
// If the text exceeds maxLines, adds a truncation marker.
func formatWithPrefix(text, prefix string, maxLines int) string {
	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")

	// Remove trailing empty line if text ends with newline
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var sb strings.Builder
	truncated := len(lines) > maxLines

	displayLines := lines
	if truncated {
		displayLines = lines[:maxLines]
	}

	for _, line := range displayLines {
		sb.WriteString(prefix)
		sb.WriteString(line)
		sb.WriteByte('\n')
	}

	if truncated {
		sb.WriteString(prefix)
		sb.WriteString(TruncationMarker)
		sb.WriteByte('\n')
	}

	return sb.String()
}

// ParseSessionLogEntry parses a session log entry back into an Event.
// It reads the first line as JSON and ignores the plaintext that follows.
func ParseSessionLogEntry(entry string) (*Event, error) {
	lines := strings.SplitN(entry, "\n", 2)
	if len(lines) == 0 || lines[0] == "" {
		return nil, fmt.Errorf("empty entry")
	}

	var event Event
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		return nil, fmt.Errorf("failed to parse event JSON: %w", err)
	}

	return &event, nil
}
