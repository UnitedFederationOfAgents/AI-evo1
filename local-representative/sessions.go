package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This file is local-representative's half of the distributed-sessions
// wiring described in docs/DistributedSessionsBrainstorm.md: the two reads
// (list/file, below) any peer LR uses to discover and fetch a host's
// clauditable session files, and the pull that actually reaches out to every
// other LR-active host and fetches them, on behalf of a same-host caller
// (clauditable, before a primary write -- see clauditable/distsync.go; or
// session-manager, before rendering a session view -- see
// session-manager/repr.go's triggerSessionSync).
//
// No new agent-coordinator route is needed for the reads: a GET reaching
// this LR through AC's transparent "/host/<id>/*" passthrough is served the
// same as a direct request, exactly like files.go's handleFileRaw already
// does for the files tab -- see docs/DistributedExchange.md's "reads are far
// less obviously in need of the guard than writes" reasoning, which applies
// here too. Only the pull (a write into THIS host's own session directory,
// triggered by a same-host caller) is refused when it arrives through that
// proxy -- see handleSessionsPull.

// envAgentRecordsPath / defaultRecordsPath mirror clauditable/main.go's and
// session-manager/sessions.go's constants of the same name.
const (
	envAgentRecordsPath = "AGENT_RECORDS_PATH"
	defaultRecordsPath  = "/host-agent-files/agent-records"

	// sessionsPullHTTPTimeout bounds each outbound hop a pull makes (listing
	// or fetching one file from one peer, or listing hosts from
	// agent-coordinator) so one unresponsive peer can't hang the whole pull.
	sessionsPullHTTPTimeout = 5 * time.Second
)

// resolveRecordsPath returns AGENT_RECORDS_PATH, falling back to
// defaultRecordsPath, exactly as clauditable's getEnvOrDefault does.
func resolveRecordsPath() string {
	if v := os.Getenv(envAgentRecordsPath); v != "" {
		return v
	}
	return defaultRecordsPath
}

// SessionFileInfo is one entry in a session directory listing -- see
// handleSessionsList.
type SessionFileInfo struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// SessionFilesMsg is handleSessionsList's response body.
type SessionFilesMsg struct {
	Files []SessionFileInfo `json:"files"`
}

// SessionIndexEntry is one session this host knows about -- see
// handleSessionsIndex.
type SessionIndexEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SessionIndexMsg is handleSessionsIndex's response body.
type SessionIndexMsg struct {
	Sessions []SessionIndexEntry `json:"sessions"`
}

// RemoteSessionEntry is one session a peer host reported, tagged with which
// host it came from -- see handleSessionsDiscover.
type RemoteSessionEntry struct {
	HostID string `json:"host_id"`
	ID     string `json:"id"`
	Name   string `json:"name"`
}

// SessionDiscoveryMsg is handleSessionsDiscover's response body.
type SessionDiscoveryMsg struct {
	Hosts    int                  `json:"hosts"`    // how many other LR-active hosts were queried
	Sessions []RemoteSessionEntry `json:"sessions"` // every session reported by any of them
}

// SessionPullResultMsg is handleSessionsPull's response body -- mostly useful
// for tests/debugging; callers (clauditable, session-manager) treat the pull
// as fire-and-forget and don't inspect it.
type SessionPullResultMsg struct {
	Hosts   int `json:"hosts"`   // how many other LR-active hosts were queried
	Fetched int `json:"fetched"` // how many files were actually pulled
}

// validSessionPathSegment guards path traversal in a session id or filename,
// same posture as files.go's handleFileItem id check.
func validSessionPathSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\") && !strings.HasPrefix(s, ".")
}

// handleSessionsAPI dispatches the "/api/sessions/<id>/..." subtree: GET
// ".../list" and GET ".../file/<name>" are read-only lookups into this LR's
// own AGENT_RECORDS_PATH (ungated -- see the file-level comment above); POST
// ".../pull" is this LR reaching out as a client to pull from others (gated
// -- see handleSessionsPull). "/api/sessions/discover" is the one path in
// this subtree with no session ID segment at all -- see handleSessionsDiscover.
func (s *Server) handleSessionsAPI(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if rest == "discover" {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		s.handleSessionsDiscover(w, r)
		return
	}
	sessionID, action, hasAction := strings.Cut(rest, "/")
	if !validSessionPathSegment(sessionID) || !hasAction {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "list":
		s.handleSessionsList(w, r, sessionID)
	case r.Method == http.MethodGet && strings.HasPrefix(action, "file/"):
		s.handleSessionsFile(w, r, sessionID, strings.TrimPrefix(action, "file/"))
	case r.Method == http.MethodPost && action == "pull":
		s.handleSessionsPull(w, r, sessionID)
	default:
		http.NotFound(w, r)
	}
}

// handleSessionsIndex answers GET /api/sessions: every session this host
// has at all -- id plus its session.yaml name, no file contents -- so a peer
// (via handleSessionsDiscover, through agent-coordinator's transparent
// "/host/<id>/*" proxy, same as handleSessionsList) can learn which session
// IDs exist here without already knowing one to ask about. This is the route
// condocs/initialDistributedSessionsImpls/RemoteSessionListingGap.md
// identified as missing: every existing lookup in this file takes a session
// ID in its path, so nothing could answer "what sessions do you have at
// all" until now. Ungated, same posture as handleSessionsList/handleSessionsFile.
func (s *Server) handleSessionsIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	entries, err := os.ReadDir(s.recordsPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SessionIndexMsg{Sessions: []SessionIndexEntry{}})
		return
	}
	sessions := make([]SessionIndexEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessions = append(sessions, SessionIndexEntry{ID: e.Name(), Name: readSessionYAMLName(filepath.Join(s.recordsPath, e.Name()))})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SessionIndexMsg{Sessions: sessions})
}

// readSessionYAMLName reads a session directory's session.yaml "name" field,
// mirroring federation-command's/session-manager's own copies of the same
// lookup (each binary in this codebase keeps its own rather than sharing a
// library -- see sessions.go's file-level comment in session-manager).
func readSessionYAMLName(sessionDir string) string {
	data, err := os.ReadFile(filepath.Join(sessionDir, "session.yaml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "name: "); ok {
			return name
		}
	}
	return ""
}

// handleSessionsDiscover answers POST /api/sessions/discover: this LR
// reaching out, as a client, to every other host agent-coordinator
// currently shows as connected and asking each one's handleSessionsIndex
// which sessions it has -- the fan-out half of closing
// RemoteSessionListingGap.md's gap, called by whichever of
// federation-command/session-manager is about to render "list-sessions"
// (see Step1SubstepBPrompt.md Revision A: "any list-sessions behaviour will
// initiate this poll", plus a poll fired once more right after either binary
// connects to this LR). Deliberately read-only on both ends: unlike
// handleSessionsPull, nothing is fetched or written to this host's own
// AGENT_RECORDS_PATH -- a session appearing here does not materialize a
// local directory for it, since nobody has asked to view its contents yet
// (see RemoteSessionListingGap.md's "What would need to be added"). Refused
// through agent-coordinator's transparent passthrough for the same reason
// handleSessionsPull is: this is this host acting as a client on its own
// behalf.
func (s *Server) handleSessionsDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(proxiedHeader) != "" {
		http.Error(w, "session discovery is only permitted from a direct local-representative client", http.StatusForbidden)
		return
	}
	acAddr, ok := s.acHTTPAddr()
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SessionDiscoveryMsg{})
		return
	}
	acBase := "http://" + acAddr

	hosts := s.listPeerHosts(acBase)
	var sessions []RemoteSessionEntry
	for _, hostID := range hosts {
		sessions = append(sessions, s.indexSessionsFrom(acBase, hostID)...)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SessionDiscoveryMsg{Hosts: len(hosts), Sessions: sessions})
}

// indexSessionsFrom asks hostID's local-representative (through
// agent-coordinator's transparent "/host/<id>/*" proxy) which sessions it
// has, tagging each with hostID.
func (s *Server) indexSessionsFrom(acBase, hostID string) []RemoteSessionEntry {
	indexURL := fmt.Sprintf("%s/host/%s/api/sessions", acBase, url.PathEscape(hostID))
	resp, err := httpGetWithTimeout(indexURL)
	if err != nil {
		log.Printf("sessions discover: indexing host %s: %v", hostID, err)
		return nil
	}
	defer resp.Body.Close()
	var msg SessionIndexMsg
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		log.Printf("sessions discover: decoding index from host %s: %v", hostID, err)
		return nil
	}
	entries := make([]RemoteSessionEntry, 0, len(msg.Sessions))
	for _, sess := range msg.Sessions {
		entries = append(entries, RemoteSessionEntry{HostID: hostID, ID: sess.ID, Name: sess.Name})
	}
	return entries
}

// handleSessionsList answers GET /api/sessions/<id>/list?glob=<pattern>:
// every plain file directly inside the session directory whose name matches
// pattern (filepath.Match; an empty glob matches everything, an invalid one
// matches nothing), each with its size and sha256 so a puller can tell --
// without fetching the bytes -- whether it already has the current version
// (see handleSessionsPull's mode=sync).
func (s *Server) handleSessionsList(w http.ResponseWriter, r *http.Request, sessionID string) {
	glob := r.URL.Query().Get("glob")
	dir := filepath.Join(s.recordsPath, sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SessionFilesMsg{Files: []SessionFileInfo{}})
		return
	}

	files := make([]SessionFileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if glob != "" {
			if ok, err := filepath.Match(glob, name); err != nil || !ok {
				continue
			}
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		sum, err := fileSHA256(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		files = append(files, SessionFileInfo{Name: name, Size: info.Size(), SHA256: sum})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SessionFilesMsg{Files: files})
}

// handleSessionsFile answers GET /api/sessions/<id>/file/<name>: one session
// file's raw bytes, ungated on proxiedHeader -- same reasoning as files.go's
// handleFileRaw.
func (s *Server) handleSessionsFile(w http.ResponseWriter, r *http.Request, sessionID, name string) {
	if !validSessionPathSegment(name) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.recordsPath, sessionID, name)
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// handleSessionsPull answers POST /api/sessions/<id>/pull?glob=<pattern>&mode=once|sync:
// this LR reaching out, as a client, to every other host agent-coordinator
// currently shows as connected, pulling <id>'s files matching glob into its
// own AGENT_RECORDS_PATH.
//
//   - mode=once (the default) never re-fetches a file it already has a local
//     copy of by name, regardless of content -- see clauditable/distsync.go's
//     triggerOnceTransfer, used for "-s-processed" records ahead of a primary
//     write. Once transferred, a record is never re-transferred.
//   - mode=sync compares checksums (via handleSessionsList's reported sha256)
//     and only fetches when missing or different -- see
//     session-manager/repr.go's triggerSessionSync, used for
//     "session.jsonl"/"-processed" records ahead of rendering a session view.
//
// Refused when it arrives through agent-coordinator's transparent
// passthrough: a pull is this host acting as a client on its own behalf,
// never something another host should be able to trigger on it remotely --
// same posture as files.go's upload guard.
func (s *Server) handleSessionsPull(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Header.Get(proxiedHeader) != "" {
		http.Error(w, "session pull is only permitted from a direct local-representative client", http.StatusForbidden)
		return
	}
	glob := r.URL.Query().Get("glob")
	once := r.URL.Query().Get("mode") != "sync"

	acAddr, ok := s.acHTTPAddr()
	if !ok {
		// Not connected to agent-coordinator -- no peers to pull from. Not an
		// error: a single-host setup (or one not yet connected) simply has
		// none available.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SessionPullResultMsg{})
		return
	}
	acBase := "http://" + acAddr

	hosts := s.listPeerHosts(acBase)
	fetched := 0
	for _, hostID := range hosts {
		fetched += s.pullSessionFilesFrom(acBase, hostID, sessionID, glob, once)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SessionPullResultMsg{Hosts: len(hosts), Fetched: fetched})
}

// acHTTPAddr returns agent-coordinator's "host:port" HTTP address, and
// whether this LR is actually connected to one right now. acHost survives a
// disconnect (see connectAC), so connectivity is checked via the live
// representable client instead of just acHost being non-empty.
func (s *Server) acHTTPAddr() (addr string, ok bool) {
	if s.getACClient() == nil {
		return "", false
	}
	s.acMu.RLock()
	host := s.acHost
	s.acMu.RUnlock()
	if host == "" {
		return "", false
	}
	return net.JoinHostPort(host, s.acHTTPPort), true
}

// acHostEntry is the subset of agent-coordinator's Host (see
// agent-coordinator/main.go) this file reads back from GET /api/hosts.
type acHostEntry struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// listPeerHosts asks agent-coordinator which other hosts it currently shows
// as connected ("LR-active"), excluding this one.
func (s *Server) listPeerHosts(acBase string) []string {
	resp, err := httpGetWithTimeout(acBase + "/api/hosts")
	if err != nil {
		log.Printf("sessions pull: listing hosts via agent-coordinator: %v", err)
		return nil
	}
	defer resp.Body.Close()
	var msg struct {
		Hosts []acHostEntry `json:"hosts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		log.Printf("sessions pull: decoding host list: %v", err)
		return nil
	}
	peers := make([]string, 0, len(msg.Hosts))
	for _, h := range msg.Hosts {
		if h.Status == "connected" && h.ID != s.lrName {
			peers = append(peers, h.ID)
		}
	}
	return peers
}

// pullSessionFilesFrom lists sessionID's files matching glob on hostID
// (through agent-coordinator's transparent "/host/<id>/*" proxy) and fetches
// whichever ones "once" (never re-fetch an already-present name) or "sync"
// (re-fetch on a checksum mismatch or absence) says are worth pulling,
// writing each straight into this LR's own AGENT_RECORDS_PATH. Returns how
// many files were actually fetched.
func (s *Server) pullSessionFilesFrom(acBase, hostID, sessionID, glob string, once bool) int {
	listURL := fmt.Sprintf("%s/host/%s/api/sessions/%s/list?glob=%s",
		acBase, url.PathEscape(hostID), url.PathEscape(sessionID), url.QueryEscape(glob))
	resp, err := httpGetWithTimeout(listURL)
	if err != nil {
		log.Printf("sessions pull: listing %s on host %s: %v", sessionID, hostID, err)
		return 0
	}
	defer resp.Body.Close()
	var msg SessionFilesMsg
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		log.Printf("sessions pull: decoding listing from host %s: %v", hostID, err)
		return 0
	}
	if len(msg.Files) == 0 {
		return 0
	}

	dir := filepath.Join(s.recordsPath, sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("sessions pull: creating session directory: %v", err)
		return 0
	}

	fetched := 0
	for _, file := range msg.Files {
		localPath := filepath.Join(dir, file.Name)
		if once {
			if _, err := os.Stat(localPath); err == nil {
				continue // already have it -- once-transfer never re-fetches
			}
		} else if localSum, err := fileSHA256(localPath); err == nil && localSum == file.SHA256 {
			continue // sync: unchanged -- checksum match avoids a needless re-fetch
		}

		fileURL := fmt.Sprintf("%s/host/%s/api/sessions/%s/file/%s",
			acBase, url.PathEscape(hostID), url.PathEscape(sessionID), url.PathEscape(file.Name))
		if err := fetchSessionFile(fileURL, localPath); err != nil {
			log.Printf("sessions pull: fetching %s from host %s: %v", file.Name, hostID, err)
			continue
		}
		fetched++
	}
	return fetched
}

// fetchSessionFile downloads fileURL into localPath, via a same-directory
// temp-then-rename so a reader never sees a partially-written file.
func fetchSessionFile(fileURL, localPath string) error {
	resp, err := httpGetWithTimeout(fileURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	tmp := localPath + ".pulling"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, localPath)
}

// httpGetWithTimeout issues a bounded GET -- see sessionsPullHTTPTimeout.
func httpGetWithTimeout(target string) (*http.Response, error) {
	client := &http.Client{Timeout: sessionsPullHTTPTimeout}
	return client.Get(target)
}

// fileSHA256 hashes a local file's contents, for handleSessionsList's
// per-entry checksum and handleSessionsPull's mode=sync comparison.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
