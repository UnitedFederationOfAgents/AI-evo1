package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvAgentRecordsPath / DefaultRecordsPath mirror the identically-named
// constants in clauditable and federation-command -- all three need to agree
// on where a host's session directories live. There's no shared package for
// this today (see condocs/InitialDistributedSessions.md Step 1), so the
// literal values are kept in sync by hand.
const (
	EnvAgentRecordsPath = "AGENT_RECORDS_PATH"
	DefaultRecordsPath  = "/host-agent-files/agent-records"
)

// sessionSyncHTTPTimeout bounds each outbound HTTP call a "list" sync makes
// (to agent-coordinator, and through it to each peer) -- a slow or vanished
// participant should never hold up the others.
const sessionSyncHTTPTimeout = 2 * time.Second

// SessionInfo is one session.yaml's worth of identity, as clauditable writes
// it: id (also the session's directory name), name, and owner (the host that
// created it -- see clauditable's resolveHost/writeSessionYAMLIfAbsent).
// Created is carried along for display but nothing here parses it further.
type SessionInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Created string `json:"created,omitempty"`
}

// SessionsStateMsg is the payload of GET /api/sessions: this host's current
// un-archived session.yaml listing. Archived sessions never appear here --
// "clauditable archive" moves their directories out of the records path
// entirely, so a plain directory scan already excludes them (see
// docs/DistributedSessionsBrainstorm.md, "only un-archived ones").
type SessionsStateMsg struct {
	Sessions []SessionInfo `json:"sessions"`
}

// parseSessionYAML reads the small flat "key: value" subset of session.yaml
// that clauditable writes. ok is false when the file is missing or has no id
// field -- callers treat that directory as not-a-session.
func parseSessionYAML(path string) (SessionInfo, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionInfo{}, false
	}
	var info SessionInfo
	for _, line := range strings.Split(string(data), "\n") {
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "id":
			info.ID = val
		case "name":
			info.Name = val
		case "owner":
			info.Owner = val
		case "created":
			info.Created = val
		}
	}
	if info.ID == "" {
		return SessionInfo{}, false
	}
	return info, true
}

// writeSessionYAML writes a session.yaml in the same shape clauditable does,
// so nothing downstream (clauditable's own owner check, FC's renderSessions)
// can tell a pulled copy from a locally-written one.
func writeSessionYAML(path string, info SessionInfo) error {
	content := fmt.Sprintf("id: %s\nname: %s\nowner: %s\ncreated: %s\n",
		info.ID, info.Name, info.Owner, info.Created)
	return os.WriteFile(path, []byte(content), 0o644)
}

// listLocalSessions scans this host's records path and returns every
// directory that has a session.yaml -- i.e. every un-archived session,
// whether it's owned by this host or was itself pulled in from a peer.
func (s *Server) listLocalSessions() []SessionInfo {
	entries, err := os.ReadDir(s.recordsPath)
	if err != nil {
		return nil
	}
	sessions := make([]SessionInfo, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, ok := parseSessionYAML(filepath.Join(s.recordsPath, e.Name(), "session.yaml"))
		if !ok {
			continue
		}
		sessions = append(sessions, info)
	}
	return sessions
}

// handleSessionsAPI serves GET /api/sessions: this host's un-archived session
// listing (session.yaml fields only -- never the session's raw/processed
// content). Deliberately not gated on proxiedHeader, same reasoning as
// handleFileRaw in files.go: a read isn't the ambiguous-target write that
// upload is, so it already works unmodified through agent-coordinator's
// transparent /host/<id>/* proxy -- which is exactly how a peer LR reaches it
// (see pullRemoteSessionLists below).
func (s *Server) handleSessionsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SessionsStateMsg{Sessions: s.listLocalSessions()})
}

// handleSessionSyncRequest is the receiving end of FC's "session-sync-request"
// data message (see federation-command's notifyDistributedSessionSync /
// awaitDistributedSessionSync). Must be called in its own goroutine -- a
// "list" sync makes outbound HTTP calls that shouldn't block the representable
// connection's read loop.
func (s *Server) handleSessionSyncRequest(req SessionSyncRequestMsg) {
	switch req.Kind {
	case "append":
		// Syncing one session's full processed+session-file glob ahead of an
		// append is real, separate infrastructure work -- see
		// docs/DistributedSessionsBrainstorm.md's "next steps". Not attempted
		// yet; FC does not wait on this kind (see notifyDistributedSessionSync).
		log.Printf("session-sync-request: append glob for session %q (processed + session files only) — no cross-host sync backend yet", req.SessionID)
	case "list":
		n := s.pullRemoteSessionLists()
		log.Printf("session-sync-request: list sync pulled %d remote session(s)", n)
		// FC's awaitDistributedSessionSync blocks on this so list-sessions/
		// select-session render a fresh picture rather than always eating the
		// full timeout -- sent even when nothing was pulled (e.g. AC
		// unreachable), so FC never waits longer than necessary.
		if s.reprServer != nil {
			s.reprServer.SendCommand("federation-command", "__session-sync-done:list")
		}
	default:
		log.Printf("session-sync-request: unknown kind %q", req.Kind)
	}
}

// acHostsResponse mirrors agent-coordinator's HostsMsg (agent-coordinator/main.go)
// closely enough to decode GET /api/hosts -- kept as a local copy rather than
// a shared import, same tradeoff as EnvAgentRecordsPath above.
type acHostsResponse struct {
	Hosts []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"hosts"`
}

// pullRemoteSessionLists implements the "list" half of distributed session
// sync: pull-only, per the design in docs/DistributedSessionsBrainstorm.md --
// this host only ever reads another participant's session.yaml files through
// agent-coordinator's proxy, it never pushes its own. Mirrors Path 2 of
// docs/DistributedExchange.md (LR-to-LR transfer brokered through AC), using
// GET /api/hosts (this increment's addition to AC) to discover participants
// and GET /host/<id>/api/sessions (this increment's addition to LR) as the
// per-peer content endpoint, in place of the single-file id Path 2 sketched.
// Returns the number of remote sessions ingested (for logging only).
func (s *Server) pullRemoteSessionLists() int {
	ac := s.getACClient()
	if ac == nil {
		log.Printf("session-sync-request: not connected to agent-coordinator, nothing to pull")
		return 0
	}
	s.acMu.RLock()
	acAddr := s.acHost + ":" + s.acHTTPPort
	s.acMu.RUnlock()

	client := &http.Client{Timeout: sessionSyncHTTPTimeout}

	var hosts acHostsResponse
	if err := getJSON(client, "http://"+acAddr+"/api/hosts", &hosts); err != nil {
		log.Printf("session-sync-request: fetching participant list from agent-coordinator: %v", err)
		return 0
	}

	pulled := 0
	for _, h := range hosts.Hosts {
		if h.ID == "" || h.ID == s.lrName || h.Status != "connected" {
			continue
		}
		var remote SessionsStateMsg
		url := "http://" + acAddr + "/host/" + h.ID + "/api/sessions"
		if err := getJSON(client, url, &remote); err != nil {
			log.Printf("session-sync-request: fetching sessions from %q: %v", h.ID, err)
			continue
		}
		for _, info := range remote.Sessions {
			if s.ingestRemoteSession(info, h.ID) {
				pulled++
			}
		}
	}
	return pulled
}

// ingestRemoteSession writes a peer's session.yaml into this host's own
// records path so clauditable's existing owner check (a session is always
// secondary here when session.yaml's owner differs from the local host -- see
// clauditable/main.go) and FC's renderSessions/buildSessionPicker (which
// already scan the records path directly) pick it up with no further
// plumbing. Only session.yaml is ever written -- never the session's raw or
// processed content, matching the "list" kind's lazy-loading contract.
//
// Guards against two collision shapes rather than ever overwriting a session
// this host doesn't recognize as a remote-cached copy of the *same* peer:
//   - an id that's actually locally-owned here (e.g. two hosts' default
//     sessions both land on "YYYY-MM-DD-default") -- skipped, this host's own
//     session always wins.
//   - an id already cached from a *different* remote owner -- also skipped,
//     first writer wins; logged so the (rare) collision is visible.
//
// Returns true when a session.yaml was written or refreshed.
func (s *Server) ingestRemoteSession(info SessionInfo, peerHost string) bool {
	if info.ID == "" || strings.ContainsAny(info.ID, "/\\") || info.ID == "." || info.ID == ".." {
		return false
	}
	owner := info.Owner
	if owner == "" {
		// Predates owner tracking on the peer -- attribute it to the host we
		// pulled it from rather than letting it read back as "locally owned".
		owner = peerHost
	}
	if owner == s.lrName {
		return false
	}

	dir := filepath.Join(s.recordsPath, info.ID)
	yamlPath := filepath.Join(dir, "session.yaml")
	if existing, ok := parseSessionYAML(yamlPath); ok {
		existingOwner := existing.Owner
		if existingOwner == "" || existingOwner == s.lrName {
			log.Printf("session-sync-request: skipping remote session %q from %q — id collides with a locally-owned session", info.ID, peerHost)
			return false
		}
		if existingOwner != owner {
			log.Printf("session-sync-request: skipping remote session %q from %q — id already cached from a different owner %q", info.ID, peerHost, existingOwner)
			return false
		}
		// Same remote owner already cached -- fall through to refresh (e.g. a
		// rename since the last sync).
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("session-sync-request: creating cache dir for remote session %q: %v", info.ID, err)
		return false
	}
	if err := writeSessionYAML(yamlPath, SessionInfo{ID: info.ID, Name: info.Name, Owner: owner, Created: info.Created}); err != nil {
		log.Printf("session-sync-request: writing remote session %q: %v", info.ID, err)
		return false
	}
	return true
}

// getJSON is a small helper for the plain-GET-and-decode calls
// pullRemoteSessionLists makes.
func getJSON(client *http.Client, url string, out interface{}) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// getEnvOrDefault returns the named environment variable's value, or
// defaultValue when it's unset or empty. Mirrors the identically-named
// helper in clauditable and federation-command.
func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
