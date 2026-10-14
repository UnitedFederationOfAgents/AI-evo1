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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file is local-representative's half of the distributed-sessions
// wiring described in docs/DistributedSessionsBrainstorm.md: the two reads
// (list/file, below) any peer LR uses to discover and fetch a host's
// clauditable session files, and the pull that actually reaches out to every
// other LR-active host and fetches them, on behalf of a same-host caller
// (session-manager, before rendering a session view -- see
// session-manager/repr.go's triggerSessionSync), following the session's
// ownership rules (see handleSessionsPull).
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

	// sessionsDiscoverHTTPTimeout bounds each per-host indexing hop a
	// discover fan-out makes (see indexSessionsFrom). Kept well below
	// sessionsPullHTTPTimeout -- listing a peer's session index is a single
	// cheap directory read, nothing like pulling actual file bytes -- and,
	// critically, well below the callers' own end-to-end budget
	// (federation-command's sessionsDiscoveryTimeout, session-manager's
	// repr.go sessionsDiscoveryTimeout): those bound the *entire* discover
	// round trip (this host's own hop included), so every hop this host
	// makes on a caller's behalf has to leave headroom for it, not match or
	// exceed it -- see handleSessionsDiscover's concurrent fan-out, which
	// keeps the worst case close to a single hop regardless of peer count.
	sessionsDiscoverHTTPTimeout = 2 * time.Second
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

// SessionPullResultMsg is handleSessionsPull's response body. clauditable
// still treats the pull as fire-and-forget and doesn't inspect it, but
// session-manager's requestSessionPull reads Errors (see
// condocs/initialDistributedSessionsImpls/31e41125_network-debug-1790867771753.log's
// "502 from agent-coordinator, 200 from this handler" sequence, Step1SubstepBPrompt.md
// Revision G): a glob with nothing to pull and a glob nobody could reach both
// used to come back as Fetched: 0 with no way to tell them apart.
type SessionPullResultMsg struct {
	Hosts   int `json:"hosts"`   // how many other LR-active hosts were queried
	Fetched int `json:"fetched"` // how many files were actually pulled
	Errors  int `json:"errors"`  // how many of those hosts failed to list/fetch from (not just "had nothing new")
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

	// Fan out to every peer concurrently rather than one at a time: a
	// sequential loop's worst case is N * sessionsDiscoverHTTPTimeout, which
	// easily exceeds a caller's own end-to-end budget (federation-command's
	// and session-manager's own discovery timeouts) once there's more than
	// one peer -- see sessionsDiscoverHTTPTimeout's doc comment. Run in
	// parallel, the worst case stays close to a single hop regardless of
	// how many peers there are.
	results := make([][]RemoteSessionEntry, len(hosts))
	var wg sync.WaitGroup
	for i, hostID := range hosts {
		wg.Add(1)
		go func(i int, hostID string) {
			defer wg.Done()
			results[i] = s.indexSessionsFrom(acBase, hostID)
		}(i, hostID)
	}
	wg.Wait()

	var sessions []RemoteSessionEntry
	for _, entries := range results {
		sessions = append(sessions, entries...)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SessionDiscoveryMsg{Hosts: len(hosts), Sessions: sessions})
}

// indexSessionsFrom asks hostID's local-representative (through
// agent-coordinator's transparent "/host/<id>/*" proxy) which sessions it
// has, tagging each with hostID. Bounded by sessionsDiscoverHTTPTimeout,
// not sessionsPullHTTPTimeout -- see that constant's doc comment.
func (s *Server) indexSessionsFrom(acBase, hostID string) []RemoteSessionEntry {
	indexURL := fmt.Sprintf("%s/host/%s/api/sessions", acBase, url.PathEscape(hostID))
	resp, err := s.httpGetWithTimeout(indexURL, sessionsDiscoverHTTPTimeout)
	if err != nil {
		log.Printf("sessions discover: indexing host %s: %v", hostID, err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// agent-coordinator's "/host/<id>/*" proxy (or the peer itself) can
		// legitimately answer a non-200 (e.g. a 502 plain-text body when
		// that host isn't reachable, or a 404 for an unknown host) --
		// decoding that as JSON would always fail and spam the log with a
		// misleading "decoding index" error instead of the real cause.
		log.Printf("sessions discover: indexing host %s: unexpected status %d", hostID, resp.StatusCode)
		return nil
	}
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
// (see handleSessionsPull). Files that never cross hosts (see
// syncableSessionFile) aren't listed.
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
		if !syncableSessionFile(name) {
			continue
		}
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
// file's bytes, ungated on proxiedHeader -- same reasoning as files.go's
// handleFileRaw. Files that never cross hosts (raw records, in-progress
// files -- see syncableSessionFile) are 404.
func (s *Server) handleSessionsFile(w http.ResponseWriter, r *http.Request, sessionID, name string) {
	if !validSessionPathSegment(name) || !syncableSessionFile(name) {
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

// handleSessionsPull answers POST /api/sessions/<id>/pull?glob=<pattern>[&max_size=<bytes>]:
// this LR reaching out, as a client, to every other host agent-coordinator
// currently shows as connected, pulling <id>'s files matching glob into its
// own AGENT_RECORDS_PATH. What is pulled from whom follows the session's
// ownership (session.yaml's owner -- see clauditable/sessionlog.go):
//
//   - session.yaml and session.jsonl are only ever written by the owner, so
//     they're only pulled from the owner (re-fetched when the checksum
//     differs), and never by the owner itself. Before this, every host
//     pulled them from every other host, so two hosts each holding their own
//     copy kept overwriting each other.
//   - A non-owner's secondary log, session.<host>.jsonl, is only pulled by
//     the owner, and only from the host that writes it, so the owner's
//     session-manager can fuse it into session.jsonl.
//   - Record and resource files never change once written, so they're
//     fetched once by name and never re-fetched. max_size, when set, leaves
//     larger ones behind for an explicit request (session-manager's resource
//     "open").
//   - Raw, in-progress and unconsolidated files never cross hosts at all
//     (see syncableSessionFile).
//
// Pulls for one session are serialized, and a peer that keeps failing is
// skipped for a while (see peerPullAllowed) instead of being retried on
// every request; a skipped peer counts as an error.
//
// The mode query parameter of earlier versions is accepted and ignored: the
// rules above decide per file.
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
	maxSize, _ := strconv.ParseInt(r.URL.Query().Get("max_size"), 10, 64)

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

	unlock := s.lockSessionPull(sessionID)
	defer unlock()

	hosts := s.listPeerHosts(acBase)
	dir := filepath.Join(s.recordsPath, sessionID)
	owner, hasYAML := readSessionOwner(dir)
	if !hasYAML {
		owner = s.findSessionOwner(acBase, hosts, sessionID)
	}
	// The owner goes first, so a session.yaml pulled in from it is what the
	// rest of the pull is judged by.
	sort.SliceStable(hosts, func(i, j int) bool { return hosts[i] == owner && hosts[j] != owner })

	fetched, errs := 0, 0
	for _, hostID := range hosts {
		if !s.peerPullAllowed(hostID) {
			errs++
			continue
		}
		policy := s.pullPolicyFor(dir, owner)
		policy.maxSize = maxSize
		n, ok := s.pullSessionFilesFrom(acBase, hostID, sessionID, glob, policy)
		s.notePeerPull(hostID, ok)
		fetched += n
		if !ok {
			errs++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SessionPullResultMsg{Hosts: len(hosts), Fetched: fetched, Errors: errs})
}

// sessionPullPolicy is what pullSessionFilesFrom may take from a peer.
type sessionPullPolicy struct {
	self    string // this host's ID
	owner   string // the session's owner; "" when unknown
	isOwner bool   // this host owns the session
	maxSize int64  // > 0: leave record/resource files larger than this behind
}

// pullPolicyFor works out sessionPullPolicy from the local session.yaml,
// falling back to discoveredOwner (see findSessionOwner) while there isn't
// one yet. A session.yaml with no owner field predates ownership and belongs
// to whichever host has it -- the same rule clauditable writes by.
func (s *Server) pullPolicyFor(dir, discoveredOwner string) sessionPullPolicy {
	p := sessionPullPolicy{self: s.lrName, owner: discoveredOwner}
	if owner, hasYAML := readSessionOwner(dir); hasYAML {
		p.owner = owner
		p.isOwner = owner == "" || owner == s.lrName
	}
	return p
}

// allows reports whether file, listed by hostID, may be taken from it at
// all, and if so whether a local copy that already exists is refreshed when
// its checksum differs (true) or left alone (false).
func (p sessionPullPolicy) allows(hostID string, file SessionFileInfo) (allowed, refresh bool) {
	if !syncableSessionFile(file.Name) {
		return false, false
	}
	switch {
	case file.Name == sessionYAMLName || file.Name == primaryLogName:
		if p.isOwner {
			return false, false
		}
		// From the owner it's authoritative. With the owner unknown or not
		// reachable, another host's copy is only good enough to materialize
		// a session we don't have at all.
		return true, hostID == p.owner
	case isSecondaryLogName(file.Name):
		return p.isOwner && file.Name == secondaryLogNameFor(hostID), true
	}
	if p.maxSize > 0 && file.Size > p.maxSize {
		return false, false
	}
	return true, false
}

// findSessionOwner asks each peer for its session.yaml when this host has
// none yet, returning the owner a peer names itself as, else any owner a
// peer names, else "".
func (s *Server) findSessionOwner(acBase string, hosts []string, sessionID string) string {
	named := ""
	for _, hostID := range hosts {
		fileURL := fmt.Sprintf("%s/host/%s/api/sessions/%s/file/%s",
			acBase, url.PathEscape(hostID), url.PathEscape(sessionID), sessionYAMLName)
		resp, err := s.httpGetWithTimeout(fileURL, sessionsDiscoverHTTPTimeout)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		owner := parseSessionOwner(string(data))
		if owner == hostID {
			return owner
		}
		if named == "" {
			named = owner
		}
	}
	return named
}

// lockSessionPull serializes pulls of one session, so two callers can't
// fetch the same files into the same directory at once. Returns the unlock.
func (s *Server) lockSessionPull(sessionID string) func() {
	s.sessionSync.mu.Lock()
	if s.sessionSync.locks == nil {
		s.sessionSync.locks = map[string]*sync.Mutex{}
	}
	l := s.sessionSync.locks[sessionID]
	if l == nil {
		l = &sync.Mutex{}
		s.sessionSync.locks[sessionID] = l
	}
	s.sessionSync.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Peer backoff: after a failed pull from a host, further pulls from it are
// skipped for peerBackoffBase, doubling with each consecutive failure up to
// peerBackoffMax. A success clears it.
const (
	peerBackoffBase = 2 * time.Second
	peerBackoffMax  = 2 * time.Minute
)

// sessionSyncState is handleSessionsPull's bookkeeping across requests.
type sessionSyncState struct {
	mu      sync.Mutex
	locks   map[string]*sync.Mutex // per session ID
	backoff map[string]peerBackoff // per host ID
}

type peerBackoff struct {
	failures int
	until    time.Time
}

// peerPullAllowed reports whether hostID isn't currently backed off.
func (s *Server) peerPullAllowed(hostID string) bool {
	s.sessionSync.mu.Lock()
	defer s.sessionSync.mu.Unlock()
	return time.Now().After(s.sessionSync.backoff[hostID].until)
}

// notePeerPull records a pull's outcome for hostID's backoff.
func (s *Server) notePeerPull(hostID string, ok bool) {
	s.sessionSync.mu.Lock()
	defer s.sessionSync.mu.Unlock()
	if s.sessionSync.backoff == nil {
		s.sessionSync.backoff = map[string]peerBackoff{}
	}
	if ok {
		delete(s.sessionSync.backoff, hostID)
		return
	}
	b := s.sessionSync.backoff[hostID]
	b.failures++
	delay := peerBackoffBase << min(b.failures-1, 10)
	if delay > peerBackoffMax {
		delay = peerBackoffMax
	}
	b.until = time.Now().Add(delay)
	s.sessionSync.backoff[hostID] = b
	if b.failures > 1 {
		log.Printf("sessions pull: host %s failed %d times in a row; skipping it for %s", hostID, b.failures, delay)
	}
}

// Session file names with sync rules of their own -- see handleSessionsPull.
const (
	sessionYAMLName = "session.yaml"
	primaryLogName  = "session.jsonl"
)

// syncableSessionFile reports whether name may cross hosts at all. Raw
// record files ({ts}-raw.txt, -s-raw.txt) can hold unredacted secrets and
// are often verbose, so they stay where they were written. "-writing.txt"
// files exist only while a command runs, and a "-s-processed.txt" is a
// record not yet consolidated on its own host -- both change underneath a
// transfer. Dot-files and ".pulling" files are this LR's own temporaries.
func syncableSessionFile(name string) bool {
	switch {
	case strings.HasPrefix(name, "."), strings.Contains(name, ".pulling"):
		return false
	case strings.HasSuffix(name, "-raw.txt"),
		strings.HasSuffix(name, "-writing.txt"),
		strings.HasSuffix(name, "-s-processed.txt"):
		return false
	}
	return true
}

// isSecondaryLogName reports whether name is a non-owner's
// session.<host>.jsonl.
func isSecondaryLogName(name string) bool {
	if name == primaryLogName {
		return false
	}
	mid, ok := strings.CutPrefix(name, "session.")
	if !ok {
		return false
	}
	tag, ok := strings.CutSuffix(mid, ".jsonl")
	return ok && tag != ""
}

// secondaryLogNameFor and hostFileTag mirror clauditable/sessionlog.go's
// secondaryLogName/hostFileTag: the log hostID appends to in a session it
// doesn't own.
func secondaryLogNameFor(hostID string) string {
	return "session." + hostFileTag(hostID) + ".jsonl"
}

func hostFileTag(hostID string) string {
	var b strings.Builder
	for _, r := range hostID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	tag := strings.Trim(b.String(), ".-")
	if tag == "" {
		tag = "unknown"
	}
	if strings.HasSuffix(tag, "-s") {
		tag += "_"
	}
	return tag
}

// readSessionOwner returns sessionDir's session.yaml owner field, and
// whether session.yaml exists -- mirroring clauditable/sessionlog.go.
func readSessionOwner(sessionDir string) (owner string, hasYAML bool) {
	data, err := os.ReadFile(filepath.Join(sessionDir, sessionYAMLName))
	if err != nil {
		return "", false
	}
	return parseSessionOwner(string(data)), true
}

func parseSessionOwner(yaml string) string {
	for _, line := range strings.Split(yaml, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "owner: "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
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
	resp, err := s.httpGetWithTimeout(acBase+"/api/hosts", sessionsPullHTTPTimeout)
	if err != nil {
		log.Printf("sessions pull: listing hosts via agent-coordinator: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("sessions pull: listing hosts via agent-coordinator: unexpected status %d", resp.StatusCode)
		return nil
	}
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
// whichever ones policy allows (see sessionPullPolicy.allows) -- missing
// ones, plus, for the files that change, ones whose checksum differs --
// writing each straight into this LR's own AGENT_RECORDS_PATH. Returns how
// many files were actually fetched, and whether hostID was actually reachable
// and fully pulled -- ok is false on a listing failure (network error,
// non-200 -- e.g. agent-coordinator's 502 "host not reachable", decode error)
// or when any individual file fetch failed, so handleSessionsPull's caller
// can tell "0 fetched because there was nothing new" apart from "0 fetched
// because hostID couldn't be reached at all" (see SessionPullResultMsg).
func (s *Server) pullSessionFilesFrom(acBase, hostID, sessionID, glob string, policy sessionPullPolicy) (fetched int, ok bool) {
	listURL := fmt.Sprintf("%s/host/%s/api/sessions/%s/list?glob=%s",
		acBase, url.PathEscape(hostID), url.PathEscape(sessionID), url.QueryEscape(glob))
	resp, err := s.httpGetWithTimeout(listURL, sessionsPullHTTPTimeout)
	if err != nil {
		log.Printf("sessions pull: listing %s on host %s: %v", sessionID, hostID, err)
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("sessions pull: listing %s on host %s: unexpected status %d", sessionID, hostID, resp.StatusCode)
		return 0, false
	}
	var msg SessionFilesMsg
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		log.Printf("sessions pull: decoding listing from host %s: %v", hostID, err)
		return 0, false
	}
	dir := filepath.Join(s.recordsPath, sessionID)
	ok = true
	for _, file := range msg.Files {
		if !validSessionPathSegment(file.Name) {
			continue
		}
		allowed, refresh := policy.allows(hostID, file)
		if !allowed {
			continue
		}
		localPath := filepath.Join(dir, file.Name)
		if _, err := os.Stat(localPath); err == nil {
			if !refresh {
				continue // never changes once written, or not this host's to refresh
			}
			if localSum, err := fileSHA256(localPath); err == nil && localSum == file.SHA256 {
				continue // unchanged -- checksum match avoids a needless re-fetch
			}
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Printf("sessions pull: creating session directory: %v", err)
			return fetched, false
		}

		fileURL := fmt.Sprintf("%s/host/%s/api/sessions/%s/file/%s",
			acBase, url.PathEscape(hostID), url.PathEscape(sessionID), url.PathEscape(file.Name))
		if err := s.fetchSessionFile(fileURL, localPath); err != nil {
			log.Printf("sessions pull: fetching %s from host %s: %v", file.Name, hostID, err)
			ok = false
			continue
		}
		fetched++
	}
	return fetched, ok
}

// fetchSessionFile downloads fileURL into localPath, via a uniquely-named
// same-directory temp file and a rename, so a reader never sees a
// partially-written file and two fetches can't write into one temp file.
func (s *Server) fetchSessionFile(fileURL, localPath string) error {
	resp, err := s.httpGetWithTimeout(fileURL, sessionsPullHTTPTimeout)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	out, err := os.CreateTemp(filepath.Dir(localPath), "."+filepath.Base(localPath)+".pulling-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, localPath); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// httpGetWithTimeout issues a GET bounded by timeout -- see
// sessionsPullHTTPTimeout and sessionsDiscoverHTTPTimeout -- and records the
// outcome as this LR's "lr->ac" hop of the SM<->LR<->AC chain (every one of
// these calls reaches agent-coordinator, whether its own "/api/hosts" or
// its "/host/<id>/*" proxy to a peer -- see recordChainCall,
// condocs/initialDistributedSessionsImpls/Step1SubstepBPrompt.md Revision
// F).
func (s *Server) httpGetWithTimeout(target string, timeout time.Duration) (*http.Response, error) {
	start := time.Now()
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(target)
	entry := ChainCallEntry{
		Hop:        "lr->ac",
		Method:     "GET",
		URL:        target,
		DurationMS: time.Since(start).Milliseconds(),
		TS:         time.Now().Unix(),
	}
	if err != nil {
		entry.Error = err.Error()
	} else {
		entry.Status = resp.StatusCode
	}
	s.recordChainCall(entry)
	return resp, err
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
