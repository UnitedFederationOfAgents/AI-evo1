// Package records provides the shared event schema for clauditable and related tools.
// Records are directly collected signals, while reports are processed/composed records.
package records

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Constants for record formatting
const (
	MaxPreviewLines      = 20
	TruncationMarker     = "..."
	InputPrefix          = "IN>> "
	OutputPrefix         = "OUT>> "
	ErrorPrefix          = "ERR>> "
	ResponseSeparator    = "\n\n----------RESPONSE----------\n\n"
	WrittenFileSeparator = "\n----------WRITTEN_RAW----------\n"

	LoadingBarMinLength       = 10
	LongResponseLineThreshold = 1000
)

// ProcessingHeader is a JSON snippet inserted after the first event header
// in a processed file to record which auto-maintenance steps were applied.
type ProcessingHeader struct {
	ProcessingType string `json:"processing_type"`
	AppliedAt      string `json:"applied_at"`
	Count          int    `json:"count,omitempty"`
}

var (
	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(api[_-]?key|api[_-]?token|access[_-]?token|auth[_-]?token)\s*[=:]\s*["']?[A-Za-z0-9+/._~-]{10,}`),
		regexp.MustCompile(`(?i)(password|passwd|secret)\s*=\s*["']?\S+`),
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9_.~+/\-]{20,}`),
		regexp.MustCompile(`sk-[A-Za-z0-9_\-]{20,}`),
		regexp.MustCompile(`gh[psor]_[A-Za-z0-9]{36}`),
	}
	loadingBarPattern = regexp.MustCompile(`#{10,}`)
)

// Event represents metadata about a command execution.
// The Event is stored as a JSON blob and contains only metadata,
// not the actual command input/output content.
type Event struct {
	Timestamp  string            `json:"timestamp"`             // RFC3339 formatted timestamp
	EventType  string            `json:"event_type"`            // Type of event (e.g., "command_execution")
	Host       string            `json:"host,omitempty"`        // Host identifier (from UFA_HOST or ~/.ufa/host.yaml)
	Head       string            `json:"head,omitempty"`        // Head identifier (from UFA_HEAD)
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

// FormatSessionLog formats a Record for the session.jsonl file.
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

// FormatWrittenFile creates the full written file content, combining the session log
// entry and the raw content with a separator. This is the "written file" format used
// at completion time to replace the in-progress writing file marker.
func (r *Record) FormatWrittenFile() string {
	return r.FormatSessionLog() + WrittenFileSeparator + r.FormatRawFile()
}

// ExtractSessionLogFromWrittenFile extracts the session log portion (everything before
// WrittenFileSeparator) from a written file's content, for appending to session.jsonl.
func ExtractSessionLogFromWrittenFile(content string) string {
	if idx := strings.Index(content, WrittenFileSeparator); idx >= 0 {
		return content[:idx]
	}
	return content
}

// ApplyAutoMaintenance applies secret redaction, loading-bar stripping, and long-response
// truncation to written file content. emitNoOp controls whether a no-op header is returned
// when no processing is needed; pass true only for the first file in a consolidation batch.
func ApplyAutoMaintenance(content string, emitNoOp bool) (processed string, headers []ProcessingHeader) {
	processed = content
	now := time.Now().Format(time.RFC3339)

	redactCount := 0
	for _, re := range secretPatterns {
		processed = re.ReplaceAllStringFunc(processed, func(_ string) string {
			redactCount++
			return fmt.Sprintf("<REDACTED-%d>", redactCount)
		})
	}

	strippedCount := 0
	processed = loadingBarPattern.ReplaceAllStringFunc(processed, func(_ string) string {
		strippedCount++
		return "<STRIPPED>"
	})

	truncated := false
	if idx := strings.Index(processed, WrittenFileSeparator); idx >= 0 {
		rawPart := processed[idx+len(WrittenFileSeparator):]
		lines := strings.Split(rawPart, "\n")
		if len(lines) > LongResponseLineThreshold {
			half := len(lines) / 2
			processedRaw := strings.Join(lines[:half], "\n") + "\n...<CONTINUES>...\n" + strings.Join(lines[len(lines)-half:], "\n")
			processed = processed[:idx+len(WrittenFileSeparator)] + processedRaw
			truncated = true
		}
	}

	if redactCount > 0 {
		headers = append(headers, ProcessingHeader{ProcessingType: "redact_secrets", AppliedAt: now, Count: redactCount})
	}
	if strippedCount > 0 {
		headers = append(headers, ProcessingHeader{ProcessingType: "strip_loading_bars", AppliedAt: now, Count: strippedCount})
	}
	if truncated {
		headers = append(headers, ProcessingHeader{ProcessingType: "truncate_long_response", AppliedAt: now})
	}
	if len(headers) == 0 && emitNoOp {
		headers = append(headers, ProcessingHeader{ProcessingType: "no_op", AppliedAt: now})
	}

	return processed, headers
}

// FormatProcessedFile inserts processing headers after the first JSON line of the session log
// in processedContent. Returns processedContent unchanged when headers is empty.
func FormatProcessedFile(processedContent string, headers []ProcessingHeader) string {
	if len(headers) == 0 {
		return processedContent
	}

	var sessionLog, rawSection string
	if idx := strings.Index(processedContent, WrittenFileSeparator); idx >= 0 {
		sessionLog = processedContent[:idx]
		rawSection = processedContent[idx+len(WrittenFileSeparator):]
	} else {
		sessionLog = processedContent
	}

	firstNL := strings.IndexByte(sessionLog, '\n')
	var firstLine, rest string
	if firstNL >= 0 {
		firstLine = sessionLog[:firstNL+1]
		rest = sessionLog[firstNL+1:]
	} else {
		firstLine = sessionLog + "\n"
	}

	var sb strings.Builder
	sb.WriteString(firstLine)
	for _, h := range headers {
		headerJSON, _ := json.Marshal(h)
		sb.Write(headerJSON)
		sb.WriteByte('\n')
	}
	sb.WriteString(rest)
	if rawSection != "" {
		sb.WriteString(WrittenFileSeparator)
		sb.WriteString(rawSection)
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
