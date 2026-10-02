package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ufahostid "ufa-hostid"
)

// This file gives session-manager parity with federation-command's "ufa
// session" sub-menu (main.go's handleUFACommand: list/select/new/set/get/
// describe/rename/archive), plus a "view" capability neither federation-
// command nor clauditable's TUI has -- rendering a session's session.jsonl
// as a readable transcript instead of raw log text.
//
// Sessions live as directories under AGENT_RECORDS_PATH, one per session,
// each holding a session.yaml (id/name/created) and a session.jsonl
// event/transcript log -- see clauditable/pkg/records and
// clauditable/main.go's ensureSession/writeSessionYAMLIfAbsent. The path
// resolution, YAML field helpers, and ID-generation below mirror
// clauditable's and federation-command's own copies of the same logic
// (each of those two already carries its own copy rather than sharing a
// library); this is a third.

// ---- WebSocket glue ----
//
// main.go's handleClientMsg dispatches "list-sessions"/"new-session"/
// "set-session"/"rename-session"/"describe-session"/"view-session"/
// "archive-sessions" into the handlers below, which wrap the pure functions
// below (path resolution, listing, describing, creating, renaming,
// archiving, viewing) with WS request/response and s.currentSession
// bookkeeping.

// SessionsMsg is the "sessions" WebSocket payload: the full session list
// plus which one (if any) is current, pushed on connect and after any
// mutation.
type SessionsMsg struct {
	Sessions []SessionSummary `json:"sessions"`
	Current  string           `json:"current"`
}

// ArchiveResultMsg is the "archive-result" WebSocket payload reporting
// "ufa session archive" parity's outcome.
type ArchiveResultMsg struct {
	Count int    `json:"count"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
}

func (s *Server) getCurrentSession() string {
	s.sessMu.RLock()
	defer s.sessMu.RUnlock()
	return s.currentSession
}

func (s *Server) setCurrentSession(id string) {
	s.sessMu.Lock()
	s.currentSession = id
	s.sessMu.Unlock()
	s.pushCurrentSessionStateboard()
}

// sendSessions replies to c only.
func (s *Server) sendSessions(c *wsClient) {
	sessions, err := s.listSessionsWithRemote()
	if err != nil {
		s.sendToClient(c, "error", err.Error())
		return
	}
	s.sendToClient(c, "sessions", SessionsMsg{Sessions: sessions, Current: s.getCurrentSession()})
}

// broadcastSessions notifies every connected client -- used after a
// mutation (new/set/rename/archive) so other open tabs stay in sync.
func (s *Server) broadcastSessions() {
	sessions, err := s.listSessionsWithRemote()
	msg := SessionsMsg{Current: s.getCurrentSession()}
	if err != nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		for c := range s.clients {
			s.sendToClient(c, "error", err.Error())
		}
		return
	}
	msg.Sessions = sessions
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		s.sendToClient(c, "sessions", msg)
	}
}

// handleNewSession is "ufa session new [name]" parity: create a session and
// make it current, mirroring federation-command's handleNewSession
// switching to the session it just created.
func (s *Server) handleNewSession(c *wsClient, name string) {
	id, err := createSession(s.recordsPath, name)
	if err != nil {
		s.sendToClient(c, "error", "new-session: "+err.Error())
		return
	}
	s.setCurrentSession(id)
	s.broadcastSessions()
}

// handleSetSession is "ufa session set <id>" parity. id may name a session
// this host has only ever seen as a Remote entry from listSessionsWithRemote
// (discovered on another host, nothing pulled locally yet) -- triggerSessionSync
// materializes it (session.yaml/session.jsonl/-processed, see sessionSyncGlobs)
// before the existence check, the same way sendSessionView already does for
// viewing, so selecting a remote session doesn't require it to already exist
// in this host's own AGENT_RECORDS_PATH.
func (s *Server) handleSetSession(c *wsClient, id string) {
	if id == "" {
		s.sendToClient(c, "error", "set-session: id is required")
		return
	}
	if _, err := os.Stat(filepath.Join(s.recordsPath, id)); err != nil {
		s.triggerSessionSync(id)
		if _, err := os.Stat(filepath.Join(s.recordsPath, id)); err != nil {
			s.sendToClient(c, "error", fmt.Sprintf("set-session: session %q not found", id))
			return
		}
	}
	s.setCurrentSession(id)
	s.broadcastSessions()
}

// handleRenameSession is "ufa session rename <name>" parity (the agent-
// assisted "-a" variant is out of scope for this basic pass).
func (s *Server) handleRenameSession(c *wsClient, id, name string) {
	if id == "" {
		s.sendToClient(c, "error", "rename-session: id is required")
		return
	}
	if err := renameSession(s.recordsPath, id, name); err != nil {
		s.sendToClient(c, "error", "rename-session: "+err.Error())
		return
	}
	s.broadcastSessions()
	s.sendSessionInfo(c, id)
}

// sendSessionInfo is "ufa session describe" parity, replying to c only. Like
// sendSessionView, this syncs id in first -- the frontend's selectSession
// fires describe-session and view-session together for a row that may be a
// Remote-tagged entry (listSessionsWithRemote) never pulled locally before,
// so without this describeSession's plain os.Stat would reliably lose the
// race against view-session's own sync and report "not found".
func (s *Server) sendSessionInfo(c *wsClient, id string) {
	if id == "" {
		s.sendToClient(c, "error", "describe-session: id is required")
		return
	}
	s.triggerSessionSync(id)
	info, err := describeSession(s.recordsPath, id)
	if err != nil {
		s.sendToClient(c, "error", "describe-session: "+err.Error())
		return
	}
	s.sendToClient(c, "session-info", info)
}

// sendSessionView renders id's session.jsonl as a readable transcript,
// replying to c only. Before reading, it asks local-representative to
// sync-refresh id's "session.jsonl"/"-processed" files from every other
// LR-active host (see repr.go's triggerSessionSync) -- this is "bringing it
// up in session-manager for viewing" from
// docs/DistributedSessionsBrainstorm.md's sync trigger.
//
// When that sync reports it couldn't actually reach a peer (Revision G's
// fix, see triggerSessionSync), the rendered view is tagged SyncIncomplete so
// the frontend can tell "this session is genuinely empty/short" apart from
// "this session may be missing turns because a remote host was unreachable
// when we tried to refresh it" -- previously indistinguishable, which is what
// made a remote session like "Hambone23" look like it simply wasn't there.
func (s *Server) sendSessionView(c *wsClient, id string) {
	if id == "" {
		s.sendToClient(c, "error", "view-session: id is required")
		return
	}
	incomplete := s.triggerSessionSync(id)
	view, err := viewSession(s.recordsPath, id)
	if err != nil {
		s.sendToClient(c, "error", "view-session: "+err.Error())
		return
	}
	view.SyncIncomplete = incomplete
	s.sendToClient(c, "session-view", view)
}

// handleArchiveSessions is "ufa session archive" parity: archiving the
// current session (if any) clears it, since its directory no longer exists
// under recordsPath.
func (s *Server) handleArchiveSessions(c *wsClient) {
	count, path, err := archiveSessions(s.recordsPath)
	if err != nil {
		s.sendToClient(c, "archive-result", ArchiveResultMsg{Count: count, Path: path, Error: err.Error()})
		if count == 0 {
			return
		}
	} else {
		s.sendToClient(c, "archive-result", ArchiveResultMsg{Count: count, Path: path})
	}
	if count > 0 {
		s.setCurrentSession("")
		s.broadcastSessions()
	}
}

const (
	// EnvAgentRecordsPath and DefaultRecordsPath mirror
	// clauditable/main.go and federation-command/main.go's constants of the
	// same name.
	EnvAgentRecordsPath        = "AGENT_RECORDS_PATH"
	EnvAgentRecordsArchivePath = "AGENT_RECORDS_ARCHIVE_PATH"
	DefaultRecordsPath         = "/host-agent-files/agent-records"

	// sessionLogPrefix* mirror clauditable/pkg/records' InputPrefix/
	// OutputPrefix/ErrorPrefix -- the line prefixes FormatSessionLog uses
	// when writing each record's command/stdout/stderr into session.jsonl.
	sessionLogInputPrefix  = "IN>> "
	sessionLogOutputPrefix = "OUT>> "
	sessionLogErrorPrefix  = "ERR>> "
)

// resolveRecordsPath returns AGENT_RECORDS_PATH, falling back to
// DefaultRecordsPath, exactly as clauditable's getEnvOrDefault does.
func resolveRecordsPath() string {
	if v := os.Getenv(EnvAgentRecordsPath); v != "" {
		return v
	}
	return DefaultRecordsPath
}

// ---- listing / describing ----

// SessionSummary is one row of "ufa session list" parity data. Remote/Host
// are set only for a session discovered on another host but not (yet)
// present in this host's own AGENT_RECORDS_PATH -- see listSessionsWithRemote.
type SessionSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	FileCount int    `json:"file_count"`
	Current   bool   `json:"current"`
	Remote    bool   `json:"remote,omitempty"`
	Host      string `json:"host,omitempty"`
}

// listSessions returns every session directory under recordsPath, newest
// first (matching federation-command's renderSessions sort order), flagging
// whichever one equals currentID.
func listSessions(recordsPath, currentID string) ([]SessionSummary, error) {
	entries, err := os.ReadDir(recordsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading records directory: %w", err)
	}

	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))

	summaries := make([]SessionSummary, 0, len(ids))
	for _, id := range ids {
		sessionDir := filepath.Join(recordsPath, id)
		files, _ := os.ReadDir(sessionDir)
		summaries = append(summaries, SessionSummary{
			ID:        id,
			Name:      readSessionName(sessionDir),
			FileCount: len(files),
			Current:   id == currentID,
		})
	}
	return summaries, nil
}

// listSessionsWithRemote returns listSessions's purely-local summaries plus
// every session triggerSessionsDiscovery (repr.go) reports from another
// LR-active host whose ID isn't already in that local list, tagged Remote
// and with which host it came from -- closing the gap
// condocs/initialDistributedSessionsImpls/RemoteSessionListingGap.md
// describes: "list-sessions never leaves the local filesystem." Called by
// sendSessions/broadcastSessions, so "any list-sessions behaviour" (both the
// WS "list-sessions" verb and every other trigger that reaches those two --
// a new browser connection, a mutation, or the connect-time refresh --
// initiates the same discovery poll (Step1SubstepBPrompt.md Revision A). A
// session already present locally (e.g. because it was previously pulled)
// is left as its local entry, never duplicated as remote.
func (s *Server) listSessionsWithRemote() ([]SessionSummary, error) {
	summaries, err := listSessions(s.recordsPath, s.getCurrentSession())
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(summaries))
	for _, sum := range summaries {
		known[sum.ID] = true
	}
	for _, r := range s.triggerSessionsDiscovery() {
		if known[r.ID] {
			continue
		}
		known[r.ID] = true
		summaries = append(summaries, SessionSummary{ID: r.ID, Name: r.Name, Remote: true, Host: r.HostID})
	}
	return summaries, nil
}

// SessionInfo is "ufa session describe" parity data: the current session's
// ID, on-disk location, and every field session.yaml carries (not just
// name).
type SessionInfo struct {
	ID       string      `json:"id"`
	Location string      `json:"location"`
	Fields   [][2]string `json:"fields"`
}

// describeSession reads id's session.yaml fields, mirroring federation-
// command's renderSessionDescribe.
func describeSession(recordsPath, id string) (SessionInfo, error) {
	sessionDir := filepath.Join(recordsPath, id)
	if _, err := os.Stat(sessionDir); err != nil {
		return SessionInfo{}, fmt.Errorf("session %q not found", id)
	}
	fields := readSessionYAMLFields(sessionDir)
	kept := make([][2]string, 0, len(fields))
	for _, f := range fields {
		if f[0] == "id" {
			continue
		}
		kept = append(kept, f)
	}
	return SessionInfo{ID: id, Location: sessionDir, Fields: kept}, nil
}

// readSessionName mirrors federation-command/main.go's readSessionName.
func readSessionName(sessionDir string) string {
	for _, f := range readSessionYAMLFields(sessionDir) {
		if f[0] == "name" {
			return f[1]
		}
	}
	return ""
}

// readSessionYAMLFields mirrors federation-command/main.go's function of the
// same name: every "key: value" line from session.yaml, in file order.
func readSessionYAMLFields(sessionDir string) [][2]string {
	data, err := os.ReadFile(filepath.Join(sessionDir, "session.yaml"))
	if err != nil {
		return nil
	}
	var fields [][2]string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if parts := strings.SplitN(line, ": ", 2); len(parts) == 2 && parts[0] != "" {
			fields = append(fields, [2]string{parts[0], parts[1]})
		}
	}
	return fields
}

// ---- creating / renaming / archiving ----

// createSession makes a new session directory + session.yaml under
// recordsPath and returns its generated ID, mirroring clauditable's
// new-session (ensureSession + generateSessionID).
func createSession(recordsPath, name string) (string, error) {
	name = stripSurroundingQuotes(name)
	if name == "" {
		name = "Unnamed - " + time.Now().Format("2006-01-02 - 15:04:05")
	}
	id := generateSessionID(name)
	if strings.HasSuffix(id, "-default") {
		return "", fmt.Errorf("session IDs ending in '-default' are reserved for daily defaults")
	}
	sessionDir := filepath.Join(recordsPath, id)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return "", fmt.Errorf("creating session directory: %w", err)
	}
	if err := writeSessionYAMLIfAbsent(sessionDir, id, name); err != nil {
		return "", err
	}
	return id, nil
}

// generateSessionID mirrors clauditable/main.go's function of the same name.
func generateSessionID(name string) string {
	slug := slugify(name)
	if slug == "" {
		slug = "session"
	}
	return fmt.Sprintf("%s_%s", time.Now().Format("2006-01-02_15-04-05"), slug)
}

// slugify mirrors clauditable/main.go's function of the same name: a
// lowercase, hyphen-separated, filesystem-safe slug capped at 40 chars.
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

// stripSurroundingQuotes mirrors federation-command/main.go's function of
// the same name: removes one matching pair of leading/trailing double or
// single quotes from s, e.g. `"My New Session"` -> `My New Session`. The
// frontend's "new session name…" field (and the rename equivalent) is a
// plain text input, not shell-parsed, so a user quoting a multi-word name
// out of habit would otherwise end up with the quotes baked into the name.
func stripSurroundingQuotes(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' || first == '\'') && first == last {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// writeSessionYAMLIfAbsent mirrors clauditable/main.go's function of the
// same name, including the owner field recording the creating host (see
// condocs/initialDistributedSessionsImpls/Step2Prompt.md Revision C).
func writeSessionYAMLIfAbsent(sessionDir, id, name string) error {
	yamlPath := filepath.Join(sessionDir, "session.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return nil
	}
	content := fmt.Sprintf("id: %s\nname: %s\nowner: %s\ncreated: %s\n", id, name, ufahostid.GetHostID(), time.Now().Format(time.RFC3339))
	return os.WriteFile(yamlPath, []byte(content), 0644)
}

// renameSession mirrors federation-command/main.go's updateSessionName plus
// handleRenameSession's default-session guard.
func renameSession(recordsPath, id, newName string) error {
	newName = stripSurroundingQuotes(newName)
	if strings.HasSuffix(id, "-default") {
		return fmt.Errorf("cannot rename a default session")
	}
	if newName == "" {
		return fmt.Errorf("rename-session: provide a name")
	}
	sessionDir := filepath.Join(recordsPath, id)
	if _, err := os.Stat(sessionDir); err != nil {
		return fmt.Errorf("session %q not found", id)
	}
	yamlPath := filepath.Join(sessionDir, "session.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return writeSessionYAMLIfAbsent(sessionDir, id, newName)
	}
	lines := strings.Split(string(data), "\n")
	found := false
	for i, l := range lines {
		if strings.HasPrefix(l, "name: ") {
			lines[i] = "name: " + newName
			found = true
			break
		}
	}
	if !found {
		newLines := make([]string, 0, len(lines)+1)
		idInserted := false
		for _, l := range lines {
			newLines = append(newLines, l)
			if !idInserted && strings.HasPrefix(l, "id: ") {
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

// archiveSessions moves every session directory out of recordsPath into a
// timestamped subdirectory of AGENT_RECORDS_ARCHIVE_PATH (default
// recordsPath+"-archive"), mirroring clauditable's runArchive ("ufa session
// archive" shells out to `clauditable archive`).
func archiveSessions(recordsPath string) (count int, archiveDir string, err error) {
	archiveBase := os.Getenv(EnvAgentRecordsArchivePath)
	if archiveBase == "" {
		archiveBase = recordsPath + "-archive"
	}

	entries, err := os.ReadDir(recordsPath)
	if err != nil {
		return 0, "", fmt.Errorf("reading records directory: %w", err)
	}
	var sessions []string
	for _, e := range entries {
		if e.IsDir() {
			sessions = append(sessions, e.Name())
		}
	}
	if len(sessions) == 0 {
		return 0, "", nil
	}

	archiveDir = filepath.Join(archiveBase, time.Now().Format("2006-01-02_15-04-05"))
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		return 0, "", fmt.Errorf("creating archive directory: %w", err)
	}

	var firstErr error
	for _, session := range sessions {
		src := filepath.Join(recordsPath, session)
		dst := filepath.Join(archiveDir, session)
		if err := os.Rename(src, dst); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("moving session %q: %w", session, err)
			}
			continue
		}
		count++
	}
	return count, archiveDir, firstErr
}

// ---- viewing (session.jsonl -> readable transcript) ----

// sessionLogEvent is the JSON header FormatSessionLog writes at the start of
// each record block in session.jsonl, mirroring the subset of
// clauditable/pkg/records.Event this viewer displays.
type sessionLogEvent struct {
	Timestamp  string `json:"timestamp"`
	EventType  string `json:"event_type"`
	Agent      string `json:"agent"`
	Model      string `json:"model"`
	DurationMs int64  `json:"duration_ms"`
	ExitCode   int    `json:"exit_code"`
}

// SessionEntry is one readable turn of a session's transcript: the event
// header plus its IN>>/OUT>>/ERR>> blocks with prefixes stripped.
type SessionEntry struct {
	Timestamp  string `json:"timestamp"`
	EventType  string `json:"event_type,omitempty"`
	Agent      string `json:"agent,omitempty"`
	Model      string `json:"model,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	ExitCode   int    `json:"exit_code"`
	Input      string `json:"input,omitempty"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
}

// SessionView is the "view session" response: id/name plus its parsed
// transcript.
type SessionView struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Entries []SessionEntry `json:"entries"`
	// SyncIncomplete is set by sendSessionView, not viewSession (a purely
	// local read has no notion of it) -- see sendSessionView's doc comment.
	SyncIncomplete bool `json:"sync_incomplete,omitempty"`
}

// viewSession reads id's session.jsonl and parses it into a readable
// transcript.
func viewSession(recordsPath, id string) (SessionView, error) {
	sessionDir := filepath.Join(recordsPath, id)
	if _, err := os.Stat(sessionDir); err != nil {
		return SessionView{}, fmt.Errorf("session %q not found", id)
	}
	data, err := os.ReadFile(filepath.Join(sessionDir, "session.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			// A brand-new session has no session.jsonl yet -- an empty
			// transcript, not an error. Entries is set (not nil) so it
			// marshals to "[]" rather than "null" for the frontend.
			return SessionView{ID: id, Name: readSessionName(sessionDir), Entries: []SessionEntry{}}, nil
		}
		return SessionView{}, fmt.Errorf("reading session log: %w", err)
	}
	return SessionView{ID: id, Name: readSessionName(sessionDir), Entries: parseSessionLog(data)}, nil
}

// parseSessionLog splits session.jsonl's alternating JSON-header /
// prefixed-text blocks (see clauditable/pkg/records.Record.FormatSessionLog)
// back into structured entries. A line is treated as a new entry's header
// only when it parses as JSON *and* carries a timestamp or event type --
// this skips over FormatProcessedFile's optional auto-maintenance headers
// (redact_secrets/strip_loading_bars/...), which are JSON too but carry
// neither field, without special-casing them.
func parseSessionLog(data []byte) []SessionEntry {
	// Non-nil (not "var entries") so an empty log marshals to the "session-
	// view" payload's entries as "[]" rather than "null".
	entries := make([]SessionEntry, 0)
	var cur *SessionEntry
	var in, out, errBlock strings.Builder

	flush := func() {
		if cur == nil {
			return
		}
		cur.Input = strings.TrimRight(in.String(), "\n")
		cur.Output = strings.TrimRight(out.String(), "\n")
		cur.Error = strings.TrimRight(errBlock.String(), "\n")
		entries = append(entries, *cur)
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "{") {
			var ev sessionLogEvent
			if err := json.Unmarshal([]byte(line), &ev); err == nil && (ev.Timestamp != "" || ev.EventType != "") {
				flush()
				cur = &SessionEntry{
					Timestamp:  ev.Timestamp,
					EventType:  ev.EventType,
					Agent:      ev.Agent,
					Model:      ev.Model,
					DurationMs: ev.DurationMs,
					ExitCode:   ev.ExitCode,
				}
				in.Reset()
				out.Reset()
				errBlock.Reset()
			}
			// Malformed or header-shaped-but-fieldless JSON (e.g. a
			// processing header) -- not a new entry; drop the line.
			continue
		}
		switch {
		case strings.HasPrefix(line, sessionLogInputPrefix):
			in.WriteString(strings.TrimPrefix(line, sessionLogInputPrefix))
			in.WriteByte('\n')
		case strings.HasPrefix(line, sessionLogOutputPrefix):
			out.WriteString(strings.TrimPrefix(line, sessionLogOutputPrefix))
			out.WriteByte('\n')
		case strings.HasPrefix(line, sessionLogErrorPrefix):
			errBlock.WriteString(strings.TrimPrefix(line, sessionLogErrorPrefix))
			errBlock.WriteByte('\n')
		}
		// Any other stray/blank line between blocks is ignored.
	}
	flush()
	return entries
}
