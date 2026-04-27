package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"clauditable/pkg/records"
)

// Environment variable names
const (
	EnvAgentRecordsPath        = "AGENT_RECORDS_PATH"
	EnvAgentSession            = "AGENT_SESSION"
	EnvAgentConsolidateRecords = "AGENT_CONSOLIDATE_RECORDS"
	EnvUFAAgent                = "UFA_AGENT"
	EnvUFAModel                = "UFA_MODEL"
	EnvUFAMetadata             = "UFA_METADATA"
	EnvIsClauditable           = "IS_CLAUDITABLE" // Used to prevent double-wrapping

	DefaultRecordsPath = "/host-agent-files/agent-records"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: clauditable <command> [args...]")
		os.Exit(1)
	}

	// Check for double-wrapping: if IS_CLAUDITABLE is already set, pass through
	// without recording to prevent duplicate logging
	if os.Getenv(EnvIsClauditable) == "true" {
		// Already in clauditable context, pass through directly
		cmdName := os.Args[1]
		cmdArgs := os.Args[2:]
		cmd := exec.Command(cmdName, cmdArgs...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Set IS_CLAUDITABLE for child processes to detect
	os.Setenv(EnvIsClauditable, "true")

	// Extract command and args
	cmdName := os.Args[1]
	cmdArgs := os.Args[2:]

	// Get configuration from environment
	recordsPath := getEnvOrDefault(EnvAgentRecordsPath, DefaultRecordsPath)
	session := getSession()
	consolidate := getConsolidateRecords()
	agent := os.Getenv(EnvUFAAgent)
	model := os.Getenv(EnvUFAModel)
	metadata := parseMetadata(os.Getenv(EnvUFAMetadata))

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

	// Create record using the pkg/records package
	unixTimestamp := startTime.Unix()
	record := records.Record{
		Event: records.Event{
			Timestamp:  startTime.Format(time.RFC3339),
			EventType:  "command_execution",
			Agent:      agent,
			Model:      model,
			DurationMs: duration.Milliseconds(),
			ExitCode:   exitCode,
			Metadata:   metadata,
		},
		Command: fullCommand,
		Stdout:  stdoutBuf.String(),
		Stderr:  stderrBuf.String(),
	}

	// Write the records
	recordPath, err := writeRecord(recordsPath, session, unixTimestamp, &record)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clauditable: warning: failed to write record: %v\n", err)
	} else {
		record.Event.RecordPath = recordPath

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
// Note: Uses local time with proper timezone handling to avoid "tomorrow" date bugs
func getSession() string {
	if session := os.Getenv(EnvAgentSession); session != "" {
		return session
	}
	// Use local time but truncate to start of day to ensure consistency
	now := time.Now()
	localDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return localDate.Format("2006-01-02")
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

// parseMetadata parses the UFA_METADATA environment variable
// Format: "key1=value1,key2=value2" or "key1=value1;key2=value2"
// Returns nil if empty or unparseable
func parseMetadata(s string) map[string]string {
	if s == "" {
		return nil
	}

	result := make(map[string]string)

	// Support both comma and semicolon as separators
	s = strings.ReplaceAll(s, ";", ",")
	pairs := strings.Split(s, ",")

	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			if key != "" {
				result[key] = value
			}
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// writeRecord writes both the session log entry and the raw file
// Returns the record path (used for both the consolidated entry and raw file reference)
func writeRecord(recordsPath, session string, timestamp int64, record *records.Record) (string, error) {
	sessionDir := filepath.Join(recordsPath, session)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create session directory: %w", err)
	}

	tsStr := fmt.Sprintf("%d", timestamp)
	recordFile := filepath.Join(sessionDir, tsStr)
	rawFile := filepath.Join(sessionDir, tsStr+"-raw.txt")

	// Set record path in the event for reference (used for both purposes)
	record.Event.RecordPath = recordFile

	// Write the session log entry (will be consolidated later)
	sessionLogContent := record.FormatSessionLog()
	if err := os.WriteFile(recordFile, []byte(sessionLogContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write record file: %w", err)
	}

	// Write the raw file (not consolidated, kept as permanent record)
	rawContent := record.FormatRawFile()
	if err := os.WriteFile(rawFile, []byte(rawContent), 0644); err != nil {
		// Record file was written, log warning but don't fail
		fmt.Fprintf(os.Stderr, "clauditable: warning: failed to write raw file: %v\n", err)
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

	// Find all unix-timestamp files (numeric filenames, not -raw.txt files)
	var timestampFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "session.log" {
			continue
		}
		// Skip -raw.txt files (they are not consolidated)
		if strings.HasSuffix(name, "-raw.txt") {
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

		// Write the content directly to session.log
		// The content is already in the correct format (JSON line + plaintext)
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("failed to write to session.log: %w", err)
		}

		// Ensure there's a blank line between entries for readability
		if !strings.HasSuffix(string(data), "\n\n") {
			if !strings.HasSuffix(string(data), "\n") {
				f.Write([]byte("\n"))
			}
			f.Write([]byte("\n"))
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
