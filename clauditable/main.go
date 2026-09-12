package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"clauditable/pkg/records"
)

// openPTY opens a PTY master/slave pair. Returns (master, slave, error).
// This lets child processes detect they are writing to a terminal and
// enable color output (e.g., git status showing red/green text).
func openPTY() (*os.File, *os.File, error) {
	ptm, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}

	// Unlock the slave PTY
	unlock := uint32(0)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, ptm.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		ptm.Close()
		return nil, nil, errno
	}

	// Get the slave PTY index
	var ptyno uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, ptm.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&ptyno))); errno != 0 {
		ptm.Close()
		return nil, nil, errno
	}

	slavePath := fmt.Sprintf("/dev/pts/%d", ptyno)
	pts, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		ptm.Close()
		return nil, nil, err
	}

	return ptm, pts, nil
}

// isTerminal reports whether fd is connected to a terminal.
func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}

// Environment variable names
const (
	EnvAgentRecordsPath        = "AGENT_RECORDS_PATH"
	EnvAgentRecordsArchivePath = "AGENT_RECORDS_ARCHIVE_PATH"
	EnvAgentSession            = "AGENT_SESSION"
	EnvAgentConsolidateRecords = "AGENT_CONSOLIDATE_RECORDS"
	EnvUFAAgent                = "UFA_AGENT"
	EnvUFAModel                = "UFA_MODEL"
	EnvUFAMetadata             = "UFA_METADATA"
	EnvUFAVerbosityManagement  = "UFA_VERBOSITY_MANAGEMENT"
	EnvUFAVerbosityAfterLine   = "UFA_VERBOSITY_AFTER_LINE"
	EnvClauditableAlreadyActive = "CLAUDITABLE_ALREADY_ACTIVE" // Set by clauditable for its children to prevent double-wrapping

	DefaultRecordsPath = "/host-agent-files/agent-records"

	VerbosityManagementAfterLine = "after-line"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: clauditable <command> [args...]")
		os.Exit(1)
	}

	// archive subcommand: move all sessions to archive directory
	if os.Args[1] == "archive" {
		os.Exit(runArchive())
	}

	// get-default-session: ensure today's default session exists, print its ID
	if os.Args[1] == "get-default-session" {
		os.Exit(runGetDefaultSession())
	}

	// new-session <name>: create a new named session, print its ID
	if os.Args[1] == "new-session" {
		os.Exit(runNewSession(os.Args[2:]))
	}

	// If a parent clauditable already set the guard, pass through without recording.
	if os.Getenv(EnvClauditableAlreadyActive) == "true" {
		cmdName := os.Args[1]
		cmdArgs := os.Args[2:]
		verbosity := newVerbosityRelay(os.Getenv(EnvUFAVerbosityManagement), os.Getenv(EnvUFAVerbosityAfterLine))
		os.Exit(runPassthrough(cmdName, cmdArgs, verbosity))
	}

	// Mark the environment so nested clauditable invocations pass through.
	os.Setenv(EnvClauditableAlreadyActive, "true")

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
	verbosity := newVerbosityRelay(os.Getenv(EnvUFAVerbosityManagement), os.Getenv(EnvUFAVerbosityAfterLine))

	// Prepare the command
	cmd := exec.Command(cmdName, cmdArgs...)

	// Capture buffers
	var stdoutBuf, stderrBuf strings.Builder

	startTime := time.Now()
	rawRecordPath := expectedRawRecordPath(recordsPath, session, startTime.Unix())
	if verbosity.enabled {
		fmt.Fprintf(os.Stdout, "Verbose output saved to %s\n\n", rawRecordPath)
	}
	var err error

	// When our stdout is a terminal, use a PTY for the child's stdout so that
	// programs like git detect they're writing to a terminal and enable colors.
	// Fall back to pipes if PTY allocation fails or stdout is not a terminal.
	usingPTY := false
	if isTerminal(os.Stdout.Fd()) {
		ptm, pts, ptyErr := openPTY()
		if ptyErr == nil {
			usingPTY = true
			cmd.Stdin = os.Stdin
			cmd.Stdout = pts
			cmd.Stderr = pts

			if err := cmd.Start(); err != nil {
				pts.Close()
				ptm.Close()
				fmt.Fprintf(os.Stderr, "clauditable: failed to start command: %v\n", err)
				os.Exit(1)
			}
			pts.Close() // parent doesn't need the slave after the child starts

			// Relay PTY output to our terminal and capture for the record.
			// PTY line discipline converts \n → \r\n; strip the extra \r for storage.
			buf := make([]byte, 4096)
			for {
				n, readErr := ptm.Read(buf)
				if n > 0 {
					stripped := bytes.ReplaceAll(buf[:n], []byte("\r\n"), []byte("\n"))
					stdoutBuf.Write(stripped)
					verbosity.write(os.Stdout, stripped)
				}
				if readErr != nil {
					break
				}
			}
			ptm.Close()
		}
	}

	if !usingPTY {
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

		cmd.Stdin = os.Stdin

		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "clauditable: failed to start command: %v\n", err)
			os.Exit(1)
		}

		done := make(chan struct{}, 2)
		go func() {
			teeReader := io.TeeReader(stdoutPipe, &stdoutBuf)
			if verbosity.enabled {
				io.Copy(&verbosityWriter{relay: verbosity, out: os.Stdout}, teeReader)
			} else {
				io.Copy(os.Stdout, teeReader)
			}
			done <- struct{}{}
		}()
		go func() {
			teeReader := io.TeeReader(stderrPipe, &stderrBuf)
			if verbosity.enabled {
				io.Copy(&verbosityWriter{relay: verbosity, out: os.Stderr}, teeReader)
			} else {
				io.Copy(os.Stderr, teeReader)
			}
			done <- struct{}{}
		}()
		<-done
		<-done
	}

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

type verbosityRelay struct {
	enabled   bool
	afterLine string
	revealed  bool
	pending   []byte
	mu        sync.Mutex
}

type verbosityWriter struct {
	relay *verbosityRelay
	out   *os.File
}

func (w *verbosityWriter) Write(p []byte) (int, error) {
	w.relay.write(w.out, p)
	return len(p), nil
}

func newVerbosityRelay(mode, afterLine string) *verbosityRelay {
	return &verbosityRelay{
		enabled:   mode == VerbosityManagementAfterLine && afterLine != "",
		afterLine: afterLine,
	}
}

func runPassthrough(cmdName string, cmdArgs []string, verbosity *verbosityRelay) int {
	cmd := exec.Command(cmdName, cmdArgs...)
	cmd.Stdin = os.Stdin

	if verbosity == nil || !verbosity.enabled {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return exitErr.ExitCode()
			}
			return 1
		}
		return 0
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clauditable: failed to create stdout pipe: %v\n", err)
		return 1
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clauditable: failed to create stderr pipe: %v\n", err)
		return 1
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "clauditable: failed to start command: %v\n", err)
		return 1
	}

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(&verbosityWriter{relay: verbosity, out: os.Stdout}, stdoutPipe)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(&verbosityWriter{relay: verbosity, out: os.Stderr}, stderrPipe)
		done <- struct{}{}
	}()
	<-done
	<-done

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

func (r *verbosityRelay) write(out *os.File, p []byte) {
	if !r.enabled {
		out.Write(p)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.revealed {
		out.Write(p)
		return
	}

	r.pending = append(r.pending, p...)
	for {
		lineEnd := bytes.IndexByte(r.pending, '\n')
		if lineEnd < 0 {
			return
		}

		lineWithEnding := append([]byte(nil), r.pending[:lineEnd+1]...)
		r.pending = r.pending[lineEnd+1:]

		line := strings.TrimSuffix(string(lineWithEnding), "\n")
		line = strings.TrimSuffix(line, "\r")
		if strings.Contains(line, r.afterLine) {
			out.Write(lineWithEnding)
			r.revealed = true
			if len(r.pending) > 0 {
				out.Write(r.pending)
				r.pending = nil
			}
			return
		}
	}
}

func expectedRawRecordPath(recordsPath, session string, timestamp int64) string {
	return filepath.Join(recordsPath, session, fmt.Sprintf("%d-raw.txt", timestamp))
}

// getSession returns the session identifier.
// If AGENT_SESSION is unset or "default", uses today's default session (YYYY-MM-DD-default).
func getSession() string {
	if session := os.Getenv(EnvAgentSession); session != "" && session != "default" {
		return session
	}
	return defaultSessionID()
}

// defaultSessionID returns today's default session identifier.
func defaultSessionID() string {
	return time.Now().Format("2006-01-02") + "-default"
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
	// Lazily create session.yaml on first use of this session directory.
	_ = writeSessionYAMLIfAbsent(sessionDir, session, session)

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

// consolidateRecords collects all unix-timestamp format logs and appends them to session.jsonl
func consolidateRecords(recordsPath, session string) error {
	sessionDir := filepath.Join(recordsPath, session)
	sessionLog := filepath.Join(sessionDir, "session.jsonl")

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
		if name == "session.jsonl" {
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

	// Open session.jsonl for appending
	f, err := os.OpenFile(sessionLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open session.jsonl: %w", err)
	}
	defer f.Close()

	// Process each timestamp file
	for _, tsFile := range timestampFiles {
		filePath := filepath.Join(sessionDir, tsFile)
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue // Skip files we can't read
		}

		// Write the content directly to session.jsonl
		// The content is already in the correct format (JSON line + plaintext)
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("failed to write to session.jsonl: %w", err)
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

// runGetDefaultSession ensures today's default session exists and prints its ID.
func runGetDefaultSession() int {
	recordsPath := getEnvOrDefault(EnvAgentRecordsPath, DefaultRecordsPath)
	sessionID := defaultSessionID()
	name := time.Now().Format("2006-01-02") + " Default"
	if err := ensureSession(recordsPath, sessionID, name); err != nil {
		fmt.Fprintf(os.Stderr, "clauditable get-default-session: %v\n", err)
		return 1
	}
	fmt.Println(sessionID)
	return 0
}

// runNewSession creates a new named session and prints its ID.
// Expects args = [name words...].
func runNewSession(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: clauditable new-session <name>")
		return 1
	}
	name := strings.Join(args, " ")
	recordsPath := getEnvOrDefault(EnvAgentRecordsPath, DefaultRecordsPath)
	sessionID := generateSessionID(name)
	if strings.HasSuffix(sessionID, "-default") {
		fmt.Fprintf(os.Stderr, "clauditable new-session: session IDs ending in '-default' are reserved\n")
		return 1
	}
	if err := ensureSession(recordsPath, sessionID, name); err != nil {
		fmt.Fprintf(os.Stderr, "clauditable new-session: %v\n", err)
		return 1
	}
	fmt.Println(sessionID)
	return 0
}

// generateSessionID produces a filesystem-safe ID from a human-readable name.
func generateSessionID(name string) string {
	slug := slugify(name)
	if slug == "" {
		slug = "session"
	}
	return fmt.Sprintf("%s_%s", time.Now().Format("2006-01-02_15-04-05"), slug)
}

// slugify converts a human-readable name to a lowercase, hyphen-separated slug
// suitable for filesystem use (max 40 chars).
func slugify(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	prevDash := true
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteRune('-')
			prevDash = true
		}
	}
	result := strings.TrimRight(b.String(), "-")
	if len(result) > 40 {
		result = strings.TrimRight(result[:40], "-")
	}
	return result
}

// ensureSession creates the session directory and session.yaml if they don't exist.
func ensureSession(recordsPath, sessionID, name string) error {
	sessionDir := filepath.Join(recordsPath, sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}
	return writeSessionYAMLIfAbsent(sessionDir, sessionID, name)
}

// writeSessionYAMLIfAbsent writes session.yaml only when it doesn't already exist.
func writeSessionYAMLIfAbsent(sessionDir, sessionID, name string) error {
	yamlPath := filepath.Join(sessionDir, "session.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return nil
	}
	content := fmt.Sprintf("id: %s\nname: %s\ncreated: %s\n",
		sessionID, name, time.Now().Format(time.RFC3339))
	return os.WriteFile(yamlPath, []byte(content), 0644)
}

// runArchive moves all session directories from AGENT_RECORDS_PATH to
// AGENT_RECORDS_ARCHIVE_PATH/<datetime>. Returns an exit code.
func runArchive() int {
	recordsPath := getEnvOrDefault(EnvAgentRecordsPath, DefaultRecordsPath)
	archiveBase := getEnvOrDefault(EnvAgentRecordsArchivePath, recordsPath+"-archive")

	entries, err := os.ReadDir(recordsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clauditable archive: failed to read records path %q: %v\n", recordsPath, err)
		return 1
	}

	var sessions []string
	for _, entry := range entries {
		if entry.IsDir() {
			sessions = append(sessions, entry.Name())
		}
	}

	if len(sessions) == 0 {
		fmt.Println("clauditable archive: no sessions to archive")
		return 0
	}

	archiveDir := filepath.Join(archiveBase, time.Now().Format("2006-01-02_15-04-05"))
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "clauditable archive: failed to create archive directory %q: %v\n", archiveDir, err)
		return 1
	}

	exitCode := 0
	for _, session := range sessions {
		src := filepath.Join(recordsPath, session)
		dst := filepath.Join(archiveDir, session)
		if err := os.Rename(src, dst); err != nil {
			fmt.Fprintf(os.Stderr, "clauditable archive: failed to move session %q: %v\n", session, err)
			exitCode = 1
		}
	}

	if exitCode == 0 {
		fmt.Printf("Archived %d session(s) to %s\n", len(sessions), archiveDir)
	} else {
		fmt.Printf("Archived with errors — %d session(s) targeted, destination: %s\n", len(sessions), archiveDir)
	}
	return exitCode
}
