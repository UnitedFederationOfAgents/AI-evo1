package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Environment variable names
const (
	EnvAgentRecordsPath      = "AGENT_RECORDS_PATH"
	EnvAgentSession          = "AGENT_SESSION"
	EnvAgentConsolidateRecords = "AGENT_CONSOLIDATE_RECORDS"

	DefaultRecordsPath = "/host-agent-files/agent-records"
)

// Event represents a simple event record inspired by the sandbox/AI-sandboxing schema
type Event struct {
	Timestamp   string `json:"timestamp"`
	EventType   string `json:"event_type"`
	Command     string `json:"command"`
	DurationMs  int64  `json:"duration_ms"`
	ExitCode    int    `json:"exit_code"`
	RecordPath  string `json:"record_path,omitempty"`
}

// RawRecord contains the full interaction record
type RawRecord struct {
	Event  Event  `json:"event"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: clauditable <command> [args...]")
		os.Exit(1)
	}

	// Extract command and args
	cmdName := os.Args[1]
	cmdArgs := os.Args[2:]

	// Get configuration from environment
	recordsPath := getEnvOrDefault(EnvAgentRecordsPath, DefaultRecordsPath)
	session := getSession()
	consolidate := getConsolidateRecords()

	// Prepare the command
	cmd := exec.Command(cmdName, cmdArgs...)

	// Create pipes for capturing output while relaying
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clauditable: failed to create stdout pipe: %v\n", err)
		os.Exit(1)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clauditable: failed to create stderr pipe: %v\n", err)
		os.Exit(1)
	}

	// Connect stdin
	cmd.Stdin = os.Stdin

	// Capture buffers
	var stdoutBuf, stderrBuf strings.Builder

	// Start the command
	startTime := time.Now()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "clauditable: failed to start command: %v\n", err)
		os.Exit(1)
	}

	// Tee stdout: copy to both os.Stdout and our buffer
	go func() {
		teeReader := io.TeeReader(stdoutPipe, &stdoutBuf)
		io.Copy(os.Stdout, teeReader)
	}()

	// Tee stderr: copy to both os.Stderr and our buffer
	go func() {
		teeReader := io.TeeReader(stderrPipe, &stderrBuf)
		io.Copy(os.Stderr, teeReader)
	}()

	// Wait for command to complete
	err = cmd.Wait()
	duration := time.Since(startTime)

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	// Build the full command string for recording
	fullCommand := cmdName
	if len(cmdArgs) > 0 {
		fullCommand = cmdName + " " + strings.Join(cmdArgs, " ")
	}

	// Create event and record
	unixTimestamp := startTime.Unix()
	event := Event{
		Timestamp:  startTime.Format(time.RFC3339),
		EventType:  "command_execution",
		Command:    fullCommand,
		DurationMs: duration.Milliseconds(),
		ExitCode:   exitCode,
	}

	rawRecord := RawRecord{
		Event:  event,
		Stdout: stdoutBuf.String(),
		Stderr: stderrBuf.String(),
	}

	// Write the raw record
	recordPath, err := writeRawRecord(recordsPath, session, unixTimestamp, rawRecord)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clauditable: warning: failed to write record: %v\n", err)
	} else {
		event.RecordPath = recordPath

		// Consolidate if enabled
		if consolidate {
			if err := consolidateRecords(recordsPath, session); err != nil {
				fmt.Fprintf(os.Stderr, "clauditable: warning: failed to consolidate records: %v\n", err)
			}
		}
	}

	os.Exit(exitCode)
}

// getEnvOrDefault returns the environment variable value or the default
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getSession returns the session identifier
// Uses AGENT_SESSION if set, otherwise uses current date (auto-updates daily)
func getSession() string {
	if session := os.Getenv(EnvAgentSession); session != "" {
		return session
	}
	return time.Now().Format("2006-01-02")
}

// getConsolidateRecords returns whether record consolidation is enabled
// Defaults to true
func getConsolidateRecords() bool {
	value := os.Getenv(EnvAgentConsolidateRecords)
	if value == "" {
		return true
	}
	// Parse as boolean, defaulting to true on error
	b, err := strconv.ParseBool(value)
	if err != nil {
		return true
	}
	return b
}

// writeRawRecord writes the raw record to <records-path>/<session>/<unix-timestamp>
func writeRawRecord(recordsPath, session string, timestamp int64, record RawRecord) (string, error) {
	sessionDir := filepath.Join(recordsPath, session)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create session directory: %w", err)
	}

	recordFile := filepath.Join(sessionDir, fmt.Sprintf("%d", timestamp))
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal record: %w", err)
	}

	if err := os.WriteFile(recordFile, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write record file: %w", err)
	}

	return recordFile, nil
}

// consolidateRecords collects all unix-timestamp format logs and appends them to session.log
func consolidateRecords(recordsPath, session string) error {
	sessionDir := filepath.Join(recordsPath, session)
	sessionLog := filepath.Join(sessionDir, "session.log")

	// Read directory entries
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return fmt.Errorf("failed to read session directory: %w", err)
	}

	// Find all unix-timestamp files (numeric filenames)
	var timestampFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "session.log" {
			continue
		}
		// Check if filename is a valid unix timestamp (all digits)
		if isUnixTimestamp(name) {
			timestampFiles = append(timestampFiles, name)
		}
	}

	if len(timestampFiles) == 0 {
		return nil
	}

	// Sort by timestamp (numeric order)
	sort.Slice(timestampFiles, func(i, j int) bool {
		ti, _ := strconv.ParseInt(timestampFiles[i], 10, 64)
		tj, _ := strconv.ParseInt(timestampFiles[j], 10, 64)
		return ti < tj
	})

	// Open session.log for appending
	f, err := os.OpenFile(sessionLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open session.log: %w", err)
	}
	defer f.Close()

	// Process each timestamp file
	for _, tsFile := range timestampFiles {
		filePath := filepath.Join(sessionDir, tsFile)
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue // Skip files we can't read
		}

		// Parse and re-encode as single-line JSON for JSONL format
		var record RawRecord
		if err := json.Unmarshal(data, &record); err != nil {
			continue // Skip invalid JSON
		}

		line, err := json.Marshal(record)
		if err != nil {
			continue
		}

		// Write to session.log
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("failed to write to session.log: %w", err)
		}

		// Delete the original file
		os.Remove(filePath)
	}

	return nil
}

// isUnixTimestamp checks if a string is a valid unix timestamp (all digits)
func isUnixTimestamp(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
