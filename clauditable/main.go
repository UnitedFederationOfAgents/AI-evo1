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
	ufahostid "ufa-hostid"
	ufaversion "ufa-version"
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
	EnvUFAHost                 = "UFA_HOST"
	EnvUFAHead                 = "UFA_HEAD"
	EnvUFAAgent                = "UFA_AGENT"
	EnvUFAModel                = "UFA_MODEL"
	EnvUFAMetadata             = "UFA_METADATA"
	EnvUFAEventType            = "UFA_EVENT_TYPE" // Overrides the event_type eventTypeFor would pick
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

	// --version: print version and exit instead of wrapping a command
	if os.Args[1] == "--version" {
		fmt.Println(ufaversion.Version)
		return
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

	// rename-session <new-name>: update the name field in the current session's session.yaml
	if os.Args[1] == "rename-session" {
		os.Exit(runRenameSession(os.Args[2:]))
	}

	// add-note [text...]: attach a note to the current session (text from stdin when no args)
	if os.Args[1] == "add-note" {
		os.Exit(runAddNote(os.Args[2:], os.Stdin))
	}

	// add-resource [-name <name>] <path>: attach a file to the current session
	if os.Args[1] == "add-resource" {
		os.Exit(runAddResource(os.Args[2:]))
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
	host := os.Getenv(EnvUFAHost)
	if host == "" {
		host = ufahostid.GetHostID()
	}
	head := os.Getenv(EnvUFAHead)
	agent := os.Getenv(EnvUFAAgent)
	model := os.Getenv(EnvUFAModel)
	metadata := parseMetadata(os.Getenv(EnvUFAMetadata))
	verbosity := newVerbosityRelay(os.Getenv(EnvUFAVerbosityManagement), os.Getenv(EnvUFAVerbosityAfterLine))

	// Ensure session directory exists at dispatch time
	sessionDir := filepath.Join(recordsPath, session)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "clauditable: warning: failed to create session directory: %v\n", err)
	}
	// Only today's default session (which this host always owns) is created
	// on the fly here, with its pretty "<date> Default <host>" name. Any other
	// session must already have been created by new-session or pulled in by a
	// set-session: writing a session.yaml for it here would claim it for this
	// host, which is how a session used on a second host used to fork.
	if session == defaultSessionID() {
		_ = writeSessionYAMLIfAbsent(sessionDir, session, defaultSessionName())
	}
	// Decided once, before anything is written: the owner writes
	// session.jsonl and {ts}-*.txt; any other host its own secondary log and
	// {ts}-<host>-*.txt (see sessionlog.go).
	target := writeTargetFor(sessionDir)

	// Write the writing file at dispatch time — signals that a writer is starting.
	// Its first line is "<unix ts> <agent> <model>" ("-" for an unset one), which
	// local-representative's "agent operations" reads (agentops.go); the
	// dispatched command follows.
	startTime := time.Now()
	unixTimestamp := startTime.Unix()
	prefix := recordPrefix(unixTimestamp, target.tag)
	dispatchCommand := cmdName
	if len(cmdArgs) > 0 {
		dispatchCommand = cmdName + " " + strings.Join(cmdArgs, " ")
	}
	writingFilePath := filepath.Join(sessionDir, prefix+"-writing.txt")
	if err := os.WriteFile(writingFilePath, []byte(fmt.Sprintf("%d %s %s\n%s\n", unixTimestamp, dashIfEmpty(agent), dashIfEmpty(model), dispatchCommand)), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "clauditable: warning: failed to write writing file: %v\n", err)
	}

	// Wait 200ms to detect concurrent starters, then determine primary/secondary role
	time.Sleep(200 * time.Millisecond)
	isPrimary := checkIsPrimary(sessionDir, unixTimestamp)
	if !isPrimary {
		secondaryPath := filepath.Join(sessionDir, prefix+"-s-writing.txt")
		if err := os.Rename(writingFilePath, secondaryPath); err == nil {
			writingFilePath = secondaryPath
		}
	}

	if verbosity.enabled {
		writtenPath := filepath.Join(sessionDir, prefix+"-raw.txt")
		if !isPrimary {
			writtenPath = filepath.Join(sessionDir, prefix+"-s-raw.txt")
		}
		fmt.Fprintf(os.Stdout, "Verbose output saved to %s\n\n", writtenPath)
	}

	// Prepare the command
	cmd := exec.Command(cmdName, cmdArgs...)

	// Capture buffers
	var stdoutBuf, stderrBuf strings.Builder

	cmdStartTime := time.Now()
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
	duration := time.Since(cmdStartTime)

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
	record := records.Record{
		Event: records.Event{
			Timestamp:  startTime.Format(time.RFC3339),
			EventType:  eventTypeFor(agent, os.Getenv(EnvUFAEventType)),
			Host:       host,
			Head:       head,
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

	// Write the written file (completion marker) and remove the writing file
	if _, err := writeWrittenFile(sessionDir, unixTimestamp, target.tag, isPrimary, &record); err != nil {
		fmt.Fprintf(os.Stderr, "clauditable: warning: failed to write written file: %v\n", err)
	} else {
		os.Remove(writingFilePath)
		// Primary collects all secondary written files and adds everything to
		// this host's log (session.jsonl on the owner)
		if isPrimary {
			if err := consolidatePrimaryToJSONL(recordsPath, session, unixTimestamp, target); err != nil {
				fmt.Fprintf(os.Stderr, "clauditable: warning: failed to consolidate records: %v\n", err)
			}
		}
	}

	os.Exit(exitCode)
}

// eventTypeFor picks a wrapped command's event_type. override (UFA_EVENT_TYPE)
// wins when set. Otherwise the run is an AI invocation when an agent was named
// (UFA_AGENT): ambiguous-agent and federation-command's agent paths set it to
// the agent, and federation-command sets "none" for its plain shell commands.
func eventTypeFor(agent, override string) string {
	if override = strings.TrimSpace(override); override != "" {
		return override
	}
	if agent != "" && agent != "none" {
		return records.EventTypeAIInvocation
	}
	return records.EventTypeCommandExecution
}

// dashIfEmpty keeps a writing file's header fields space-separable.
func dashIfEmpty(s string) string {
	if s = strings.Join(strings.Fields(s), "_"); s == "" {
		return "-"
	}
	return s
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
// If AGENT_SESSION is unset or "default", uses today's default session for this
// host (YYYY-MM-DD-<host>-default).
func getSession() string {
	if session := os.Getenv(EnvAgentSession); session != "" && session != "default" {
		return session
	}
	return defaultSessionID()
}

// defaultSessionName returns today's default session's human-readable display
// name ("<date> Default <host>") -- see runGetDefaultSession. Shared with the
// dispatch-time writeSessionYAMLIfAbsent fallback below so that whichever
// clauditable invocation happens to be first to create the day's default
// session.yaml (get-default-session, or a plain dispatched command racing
// ahead of it) still gets the pretty name instead of the bare session ID
// (condocs/initialDistributedSessionsImpls/Step2Prompt.md Revision D).
func defaultSessionName() string {
	return time.Now().Format("2006-01-02") + " Default " + ufahostid.GetHostID()
}

// defaultSessionID returns today's default session identifier for this host
// (YYYY-MM-DD-<host>-default). This is day-granular, not per-invocation, so
// that every instance started on the same day on the same host resolves to the
// one session already created for that day instead of piling up a fresh one
// each time. Concurrent first-creators of the day all compute this same ID and
// converge on it via ensureSession/writeSessionYAMLIfAbsent's create-if-absent
// semantics. The host is part of the ID (not just the name) so that hosts whose
// records are synced together each land on their own default session
// (condocs/initialRobotImpls/Step3SubstepCPrompt.md). The "-default" suffix is
// kept last since it is what marks the ID as a reserved daily default.
func defaultSessionID() string {
	return defaultSessionIDFor(time.Now(), ufahostid.GetHostID())
}

// defaultSessionIDFor builds the default session ID for the given day and host.
func defaultSessionIDFor(day time.Time, hostID string) string {
	return day.Format("2006-01-02") + "-" + sanitizeHostIDForSession(hostID) + "-default"
}

// sanitizeHostIDForSession makes a host ID safe to embed in a session ID (which
// is also a directory name): anything outside [A-Za-z0-9._-] becomes '-'.
func sanitizeHostIDForSession(hostID string) string {
	var b strings.Builder
	for _, r := range hostID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if s := strings.Trim(b.String(), ".-"); s != "" {
		return s
	}
	return "unknown"
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

// recordTimestamp returns the timestamp a record file name starts with, given
// the suffix it ends with: "{ts}{suffix}" on the owner, "{ts}-<host>{suffix}"
// on any other host (see sessionlog.go).
func recordTimestamp(name, suffix string) (int64, bool) {
	prefix, ok := strings.CutSuffix(name, suffix)
	if !ok {
		return 0, false
	}
	tsStr, tag, hasTag := strings.Cut(prefix, "-")
	if (hasTag && tag == "") || !isUnixTimestamp(tsStr) {
		return 0, false
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	return ts, err == nil
}

// checkIsPrimary returns true if no other primary writing file with a lower timestamp
// exists in sessionDir. The first writer (lowest timestamp) becomes primary;
// later concurrent starters become secondary.
func checkIsPrimary(sessionDir string, ourTimestamp int64) bool {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		name := entry.Name()
		// Look for primary writing files: end with -writing.txt but NOT -s-writing.txt
		if strings.HasSuffix(name, "-s-writing.txt") {
			continue
		}
		ts, ok := recordTimestamp(name, "-writing.txt")
		if !ok || ts == ourTimestamp {
			continue
		}
		if ts < ourTimestamp {
			return false // older primary exists → we are secondary
		}
	}
	return true
}

// writeWrittenFile writes the completed record as the "written file" at completion time.
// Primaries produce {timestamp}-raw.txt; secondaries produce {timestamp}-s-raw.txt.
// tag goes between the timestamp and the suffix -- "" on the session's owner,
// "-<host>" on any other host (see sessionlog.go).
// The record's RecordPath is set to the written file path before formatting.
//
// The producer of the raw/s-raw file always handles its own raw-->processed step
// immediately afterward (see writeProcessedFile), rather than leaving that work for
// whichever process later happens to run primary consolidation.
func writeWrittenFile(sessionDir string, timestamp int64, tag string, isPrimary bool, record *records.Record) (string, error) {
	suffix := "-raw.txt"
	if !isPrimary {
		suffix = "-s-raw.txt"
	}
	filePath := filepath.Join(sessionDir, recordPrefix(timestamp, tag)+suffix)
	record.Event.RecordPath = filePath
	content := record.FormatWrittenFile()
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write written file: %w", err)
	}

	if err := writeProcessedFile(sessionDir, timestamp, tag, isPrimary, content); err != nil {
		// The raw/s-raw file (the permanent record) is already written; a failure
		// to also produce the processed file is a warning, not a fatal error.
		fmt.Fprintf(os.Stderr, "clauditable: warning: failed to write processed file: %v\n", err)
	}

	return filePath, nil
}

// writeProcessedFile applies auto-maintenance (secret redaction, loading-bar stripping,
// truncation) to a just-produced raw/s-raw file's content and writes {timestamp}-processed.txt
// for a primary, or {timestamp}-s-processed.txt for a secondary (with tag after the timestamp,
// as in writeWrittenFile). Keeping the "-s-" infix on a secondary's processed file (rather than
// writing straight to its final {timestamp}-processed.txt name) is what lets a later
// consolidation tell "already processed, not yet folded into the session log" apart from
// "fully consolidated". "-s-" files are never synced to other hosts (see
// local-representative/sessions.go's syncableSessionFile). Each call processes exactly one file
// on behalf of its own producer, so it is always the first (and only) file in its batch and
// emits a no-op header when nothing needed processing.
func writeProcessedFile(sessionDir string, timestamp int64, tag string, isPrimary bool, writtenContent string) error {
	processedContent, headers := records.ApplyAutoMaintenance(writtenContent, true)
	processedFileContent := records.FormatProcessedFile(processedContent, headers)
	suffix := "-processed.txt"
	if !isPrimary {
		suffix = "-s-processed.txt"
	}
	processedPath := filepath.Join(sessionDir, recordPrefix(timestamp, tag)+suffix)
	return os.WriteFile(processedPath, []byte(processedFileContent), 0644)
}

// consolidatePrimaryToJSONL is called by the primary on completion. It folds its own
// record (via its already-produced {primaryTs}-processed.txt) and every secondary's
// already-processed record currently sitting in the session directory
// ({ts}-s-processed.txt, written by that record's own producer via writeProcessedFile)
// into this host's session log (target.logPath: session.jsonl on the owner, the
// host's secondary log elsewhere -- see sessionlog.go), in timestamp order, in a
// single locked append. Only files carrying this host's tag are considered.
//
// Folding from {ts}-s-processed.txt rather than {ts}-s-raw.txt (unlike the primary's
// own always-local -raw.txt) picks up a local secondary's concurrent invocation on
// this same host (which also leaves a sibling {ts}-s-raw.txt next to its
// {ts}-s-processed.txt, promoted to {ts}-raw.txt below), and also an -s-processed.txt
// with no -s-raw.txt sibling. No one-time auto-maintenance (redaction, etc.) ever runs
// again here: it only ever runs once, at raw.txt-->processed.txt time, by a record's
// own producer. If a processed file is unexpectedly missing but its raw/s-raw
// counterpart is available locally, it is produced here as a fallback so
// consolidation never skips it.
func consolidatePrimaryToJSONL(recordsPath, session string, primaryTimestamp int64, target sessionWriteTarget) error {
	sessionDir := filepath.Join(recordsPath, session)

	dirEntries, err := os.ReadDir(sessionDir)
	if err != nil {
		return fmt.Errorf("failed to read session directory: %w", err)
	}

	// tsWithTag accepts name only when it's one of this host's own record
	// files: "{ts}{target.tag}{suffix}".
	tsWithTag := func(name, suffix string) (int64, bool) {
		ts, ok := recordTimestamp(name, suffix)
		if !ok || name != recordPrefix(ts, target.tag)+suffix {
			return 0, false
		}
		return ts, true
	}
	sProcessedByTS := make(map[int64]string)
	sRawByTS := make(map[int64]string)
	for _, entry := range dirEntries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if ts, ok := tsWithTag(name, "-s-processed.txt"); ok {
			sProcessedByTS[ts] = filepath.Join(sessionDir, name)
		} else if ts, ok := tsWithTag(name, "-s-raw.txt"); ok {
			sRawByTS[ts] = filepath.Join(sessionDir, name)
		}
	}

	// foldEntry describes one record to append to session.jsonl: where to read its
	// already-processed session-log text from (processedPath), a raw fallback to
	// reprocess from if that's missing/unreadable, and what to rename once folded.
	type foldEntry struct {
		ts             int64
		processedPath  string // "" means "reconstruct from rawFallback"
		rawFallback    string // raw content to reprocess from if processedPath is empty/unreadable
		sProcessedPath string // {ts}-s-processed.txt to promote to {ts}-processed.txt once folded, if any
		sRawPath       string // {ts}-s-raw.txt to promote to {ts}-raw.txt once folded, if any
	}

	finalName := func(ts int64, suffix string) string {
		return filepath.Join(sessionDir, recordPrefix(ts, target.tag)+suffix)
	}
	entries := []foldEntry{{
		ts:            primaryTimestamp,
		processedPath: finalName(primaryTimestamp, "-processed.txt"),
		rawFallback:   finalName(primaryTimestamp, "-raw.txt"),
	}}

	seen := map[int64]bool{primaryTimestamp: true}
	addSecondary := func(ts int64) {
		if seen[ts] {
			return
		}
		seen[ts] = true
		e := foldEntry{ts: ts}
		if p, ok := sProcessedByTS[ts]; ok {
			e.processedPath = p
			e.sProcessedPath = p
		}
		if r, ok := sRawByTS[ts]; ok {
			e.sRawPath = r
			e.rawFallback = r
		}
		entries = append(entries, e)
	}
	for ts := range sProcessedByTS {
		addSecondary(ts)
	}
	for ts := range sRawByTS {
		addSecondary(ts)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ts < entries[j].ts
	})

	// Everything is gathered first and appended in one locked write; the
	// "-s-" files are only promoted once that write has landed.
	var logContent strings.Builder
	var promotions [][2]string
	noOpEligible := true
	for _, e := range entries {
		var processedFileContent []byte
		readErr := os.ErrNotExist
		if e.processedPath != "" {
			processedFileContent, readErr = os.ReadFile(e.processedPath)
		}
		if readErr != nil {
			if e.rawFallback == "" {
				// A remote-origin secondary with no local raw counterpart and
				// no processed file either (a broken transfer) -- nothing to
				// reconstruct from locally; skip it until a future
				// consolidation sees it.
				continue
			}
			data, rerr := os.ReadFile(e.rawFallback)
			if rerr != nil {
				continue
			}
			content, headers := records.ApplyAutoMaintenance(string(data), noOpEligible)
			noOpEligible = false
			fallback := records.FormatProcessedFile(content, headers)
			os.WriteFile(finalName(e.ts, "-processed.txt"), []byte(fallback), 0644)
			processedFileContent = []byte(fallback)
			e.sProcessedPath = "" // already written straight to the final name -- nothing to rename
		}

		sessionLogContent := records.ExtractSessionLogFromWrittenFile(string(processedFileContent))
		logContent.WriteString(sessionLogContent)
		if !strings.HasSuffix(sessionLogContent, "\n\n") {
			if !strings.HasSuffix(sessionLogContent, "\n") {
				logContent.WriteString("\n")
			}
			logContent.WriteString("\n")
		}

		if e.sProcessedPath != "" {
			promotions = append(promotions, [2]string{e.sProcessedPath, finalName(e.ts, "-processed.txt")})
		}
		if e.sRawPath != "" {
			promotions = append(promotions, [2]string{e.sRawPath, finalName(e.ts, "-raw.txt")})
		}
	}

	if logContent.Len() == 0 {
		return nil
	}
	if err := appendSessionLog(target.logPath, []byte(logContent.String())); err != nil {
		return fmt.Errorf("failed to append to %s: %w", filepath.Base(target.logPath), err)
	}
	for _, p := range promotions {
		os.Rename(p[0], p[1])
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
	name := defaultSessionName()
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

// writeSessionYAMLIfAbsent writes session.yaml only when it doesn't already
// exist. owner records the host that created the session (see
// condocs/initialDistributedSessionsImpls/Step2Prompt.md Revision C) --
// never rewritten afterward, so it stays pinned to whichever host actually
// made the directory even if another host later renames or takes over it.
func writeSessionYAMLIfAbsent(sessionDir, sessionID, name string) error {
	yamlPath := filepath.Join(sessionDir, "session.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return nil
	}
	content := fmt.Sprintf("id: %s\nname: %s\nowner: %s\ncreated: %s\n",
		sessionID, name, ufahostid.GetHostID(), time.Now().Format(time.RFC3339))
	return os.WriteFile(yamlPath, []byte(content), 0644)
}

// runRenameSession updates the name field in the current session's session.yaml.
func runRenameSession(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: clauditable rename-session <new-name>")
		return 1
	}
	newName := strings.Join(args, " ")
	recordsPath := getEnvOrDefault(EnvAgentRecordsPath, DefaultRecordsPath)
	sessionID := getSession()
	sessionDir := filepath.Join(recordsPath, sessionID)
	if err := updateSessionYAMLName(sessionDir, sessionID, newName); err != nil {
		fmt.Fprintf(os.Stderr, "clauditable rename-session: %v\n", err)
		return 1
	}
	fmt.Printf("session renamed to: %s\n", newName)
	return 0
}

// updateSessionYAMLName writes the new name into session.yaml, creating the file if needed.
func updateSessionYAMLName(sessionDir, sessionID, newName string) error {
	yamlPath := filepath.Join(sessionDir, "session.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		content := fmt.Sprintf("id: %s\nname: %s\nowner: %s\ncreated: %s\n",
			sessionID, newName, ufahostid.GetHostID(), time.Now().Format(time.RFC3339))
		return os.WriteFile(yamlPath, []byte(content), 0644)
	}
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, "name: ") {
			lines[i] = "name: " + newName
			found = true
			break
		}
	}
	if !found {
		newLines := make([]string, 0, len(lines)+1)
		idInserted := false
		for _, line := range lines {
			newLines = append(newLines, line)
			if !idInserted && strings.HasPrefix(line, "id: ") {
				newLines = append(newLines, "name: "+newName)
				idInserted = true
			}
		}
		if !idInserted {
			newLines = append(newLines, "name: "+newName)
		}
		lines = newLines
	}
	return os.WriteFile(yamlPath, []byte(strings.Join(lines, "\n")), 0644)
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
