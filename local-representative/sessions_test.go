package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestSessionsServer(t *testing.T) *Server {
	t.Helper()
	s := newServer("host-a")
	s.recordsPath = t.TempDir()
	return s
}

// TestHandleSessionsListMatchesGlobAndReportsChecksum verifies the listing
// endpoint only reports files matching the glob, each with a correct
// size/sha256 -- what a peer LR's pull (see pullSessionFilesFrom) relies on
// to decide what's worth fetching.
func TestHandleSessionsListMatchesGlobAndReportsChecksum(t *testing.T) {
	s := newTestSessionsServer(t)
	sessionDir := filepath.Join(s.recordsPath, "sess-1")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "100-processed.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "100-raw.txt"), []byte("raw content"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/sess-1/list?glob=*-processed.txt", nil)
	w := httptest.NewRecorder()
	s.handleSessionsAPI(w, req)

	var msg SessionFilesMsg
	if err := json.NewDecoder(w.Body).Decode(&msg); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(msg.Files) != 1 || msg.Files[0].Name != "100-processed.txt" {
		t.Fatalf("expected only the glob-matching file, got %+v", msg.Files)
	}
	wantSum, err := fileSHA256(filepath.Join(sessionDir, "100-processed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Files[0].SHA256 != wantSum {
		t.Errorf("reported sha256 %q, want %q", msg.Files[0].SHA256, wantSum)
	}
	if msg.Files[0].Size != 5 {
		t.Errorf("reported size %d, want 5", msg.Files[0].Size)
	}
}

// TestHandleSessionsFileServesBytesAndGuardsTraversal mirrors files.go's
// handleFileRaw guard posture for the analogous session-file endpoint.
func TestHandleSessionsFileServesBytesAndGuardsTraversal(t *testing.T) {
	s := newTestSessionsServer(t)
	sessionDir := filepath.Join(s.recordsPath, "sess-1")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "100-processed.txt"), []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/sess-1/file/100-processed.txt", nil)
	w := httptest.NewRecorder()
	s.handleSessionsAPI(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "payload" {
		t.Fatalf("got status %d body %q, want 200 %q", w.Code, w.Body.String(), "payload")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/sessions/sess-1/file/..%2f..%2fetc%2fpasswd", nil)
	w = httptest.NewRecorder()
	s.handleSessionsAPI(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("path-traversal attempt: got status %d, want 404", w.Code)
	}
}

// TestHandleSessionsPullRefusesProxiedRequest mirrors files.go's upload
// guard: a pull is this host acting as a client on its own behalf, so it is
// refused when it arrives through agent-coordinator's transparent proxy.
func TestHandleSessionsPullRefusesProxiedRequest(t *testing.T) {
	s := newTestSessionsServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/pull?glob=*-s-processed.txt", nil)
	req.Header.Set(proxiedHeader, "agent-coordinator")
	w := httptest.NewRecorder()
	s.handleSessionsAPI(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("proxied pull request: got status %d, want 403", w.Code)
	}
}

// TestHandleSessionsPullNoACIsANoOp verifies a pull with no live
// agent-coordinator connection succeeds trivially rather than erroring --
// the ordinary case for a single-host or not-yet-connected setup.
func TestHandleSessionsPullNoACIsANoOp(t *testing.T) {
	s := newTestSessionsServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/pull?glob=*-s-processed.txt", nil)
	w := httptest.NewRecorder()
	s.handleSessionsAPI(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", w.Code)
	}
	var msg SessionPullResultMsg
	if err := json.NewDecoder(w.Body).Decode(&msg); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if msg.Hosts != 0 || msg.Fetched != 0 {
		t.Errorf("got %+v, want zero hosts/fetched with no agent-coordinator connection", msg)
	}
}

// fakeACAndPeer builds an httptest.Server standing in for agent-coordinator:
// it answers "/api/hosts" with a single connected peer "host-b" and proxies
// "/host/host-b/*" straight through to peerSrv (a real Server instance,
// exercising the peer's own handleSessionsAPI) -- the same shape as AC's real
// "/host/<id>/*" transparent passthrough.
func fakeACAndPeer(t *testing.T, peerSrv *Server) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/hosts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hosts": []acHostEntry{{ID: "host-b", Status: "connected"}},
		})
	})
	mux.HandleFunc("/host/host-b/", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/host/host-b")
		// Mirrors setupRoutes' exact-vs-prefix split between the unscoped
		// index and the per-session subtree.
		if r.URL.Path == "/api/sessions" {
			peerSrv.handleSessionsIndex(w, r)
			return
		}
		peerSrv.handleSessionsAPI(w, r)
	})
	ac := httptest.NewServer(mux)
	t.Cleanup(ac.Close)
	return ac
}

// TestPullSessionFilesFromNeverRefetchesARecordFile exercises a record pull
// end-to-end (list -> fetch) through a fake agent-coordinator standing in
// front of a real peer Server: record files never change once written, so a
// name already present locally is left untouched even if its remote content
// differs -- which is also what stops two hosts' differing copies of one
// name from overwriting each other back and forth.
func TestPullSessionFilesFromNeverRefetchesARecordFile(t *testing.T) {
	s := newTestSessionsServer(t)
	peer := newTestSessionsServer(t)
	peerDir := filepath.Join(peer.recordsPath, "sess-1")
	if err := os.MkdirAll(peerDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peerDir, "100-processed.txt"), []byte("remote content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peerDir, "200-processed.txt"), []byte("new record"), 0644); err != nil {
		t.Fatal(err)
	}

	localDir := filepath.Join(s.recordsPath, "sess-1")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "100-processed.txt"), []byte("local content, already have this name"), 0644); err != nil {
		t.Fatal(err)
	}

	ac := fakeACAndPeer(t, peer)

	fetched, ok := s.pullSessionFilesFrom(ac.URL, "host-b", "sess-1", "*-processed.txt", sessionPullPolicy{self: "host-a"})
	if fetched != 1 {
		t.Fatalf("fetched = %d, want 1 (only the new 200-*)", fetched)
	}
	if !ok {
		t.Errorf("ok = false, want true (peer was reachable)")
	}

	// The already-present name must be untouched, even though its remote
	// content differs.
	got, err := os.ReadFile(filepath.Join(localDir, "100-processed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "local content, already have this name" {
		t.Errorf("pull overwrote an existing record: got %q", got)
	}

	// The new record should have been pulled in verbatim.
	got, err = os.ReadFile(filepath.Join(localDir, "200-processed.txt"))
	if err != nil {
		t.Fatalf("expected 200-processed.txt to be pulled in: %v", err)
	}
	if string(got) != "new record" {
		t.Errorf("pulled content = %q, want %q", got, "new record")
	}
}

// TestPullSessionFilesFromSyncRefetchesOnChecksumMismatch: session.jsonl
// pulled from the session's owner is refetched when its content differs from
// the local copy, and an unchanged one is left alone (no needless
// re-transfer).
func TestPullSessionFilesFromSyncRefetchesOnChecksumMismatch(t *testing.T) {
	s := newTestSessionsServer(t)
	peer := newTestSessionsServer(t)
	peerDir := filepath.Join(peer.recordsPath, "sess-1")
	if err := os.MkdirAll(peerDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peerDir, "session.jsonl"), []byte("updated log\n"), 0644); err != nil {
		t.Fatal(err)
	}

	localDir := filepath.Join(s.recordsPath, "sess-1")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "session.jsonl"), []byte("stale log\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ac := fakeACAndPeer(t, peer)

	fromOwner := sessionPullPolicy{self: "host-a", owner: "host-b"}
	fetched, ok := s.pullSessionFilesFrom(ac.URL, "host-b", "sess-1", "session.jsonl", fromOwner)
	if fetched != 1 {
		t.Fatalf("fetched = %d, want 1 (checksum mismatch)", fetched)
	}
	if !ok {
		t.Errorf("ok = false, want true (peer was reachable)")
	}
	got, err := os.ReadFile(filepath.Join(localDir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "updated log\n" {
		t.Errorf("sync should have overwritten stale content: got %q", got)
	}

	// A second sync with matching content should not report a fetch.
	fetched, ok = s.pullSessionFilesFrom(ac.URL, "host-b", "sess-1", "session.jsonl", fromOwner)
	if fetched != 0 {
		t.Errorf("fetched = %d on an unchanged file, want 0", fetched)
	}
	if !ok {
		t.Errorf("ok = false, want true (peer was reachable, just had nothing new)")
	}
}

// TestPullSessionFilesFromReportsNotOKOnUnreachableHost exercises the
// condocs/initialDistributedSessionsImpls/31e41125_network-debug-1790867771753.log
// scenario (Step1SubstepBPrompt.md Revision G): agent-coordinator's proxy
// returning 502 "host not reachable" for a peer that's dropped off the
// network must come back as ok=false, not get folded into fetched=0 the same
// way a peer with nothing new to send does.
func TestPullSessionFilesFromReportsNotOKOnUnreachableHost(t *testing.T) {
	s := newTestSessionsServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/host/host-b/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "host host-b not reachable: dial tcp: i/o timeout", http.StatusBadGateway)
	})
	ac := httptest.NewServer(mux)
	t.Cleanup(ac.Close)

	fetched, ok := s.pullSessionFilesFrom(ac.URL, "host-b", "sess-1", "session.jsonl", sessionPullPolicy{self: "host-a"})
	if fetched != 0 {
		t.Errorf("fetched = %d, want 0 (host unreachable)", fetched)
	}
	if ok {
		t.Errorf("ok = true, want false (host unreachable, not just empty)")
	}
}

// writeSessionFiles creates dir with the given files.
func writeSessionFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// The owner never takes session.yaml/session.jsonl from anyone -- this is
// what used to let a non-owner's empty copy replace the owner's -- but it
// does take the peer's own secondary log.
func TestPullOnOwnerKeepsPrimaryAndTakesPeersSecondaryLog(t *testing.T) {
	s := newTestSessionsServer(t)
	peer := newTestSessionsServer(t)
	writeSessionFiles(t, filepath.Join(peer.recordsPath, "sess-1"), map[string]string{
		"session.yaml":             "id: sess-1\nowner: host-b\n",
		"session.jsonl":            "peer's copy\n",
		"session.host-b.jsonl":     "peer's own entries\n",
		"session.host-c.jsonl":     "someone else's entries\n",
		"100-host-b-processed.txt": "peer record",
	})
	localDir := filepath.Join(s.recordsPath, "sess-1")
	writeSessionFiles(t, localDir, map[string]string{
		"session.yaml":  "id: sess-1\nowner: host-a\n",
		"session.jsonl": "owner's log\n",
	})
	ac := fakeACAndPeer(t, peer)

	if _, ok := s.pullSessionFilesFrom(ac.URL, "host-b", "sess-1", "", s.pullPolicyFor(localDir, "")); !ok {
		t.Fatal("pull failed")
	}
	for name, want := range map[string]string{
		"session.yaml":             "id: sess-1\nowner: host-a\n",
		"session.jsonl":            "owner's log\n",
		"session.host-b.jsonl":     "peer's own entries\n",
		"100-host-b-processed.txt": "peer record",
	} {
		if got, _ := os.ReadFile(filepath.Join(localDir, name)); string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(localDir, "session.host-c.jsonl")); !os.IsNotExist(err) {
		t.Error("a secondary log is only taken from the host that writes it")
	}
}

// A non-owner takes session.yaml/session.jsonl from the owner, refreshing a
// stale copy, and never takes anyone's secondary log.
func TestPullOnNonOwnerFollowsOwner(t *testing.T) {
	s := newTestSessionsServer(t)
	peer := newTestSessionsServer(t)
	writeSessionFiles(t, filepath.Join(peer.recordsPath, "sess-1"), map[string]string{
		"session.yaml":         "id: sess-1\nowner: host-b\n",
		"session.jsonl":        "owner's log, newer\n",
		"session.host-c.jsonl": "someone else's entries\n",
	})
	localDir := filepath.Join(s.recordsPath, "sess-1")
	writeSessionFiles(t, localDir, map[string]string{
		"session.yaml":  "id: sess-1\nowner: host-b\n",
		"session.jsonl": "owner's log\n",
	})
	ac := fakeACAndPeer(t, peer)

	if _, ok := s.pullSessionFilesFrom(ac.URL, "host-b", "sess-1", "", s.pullPolicyFor(localDir, "")); !ok {
		t.Fatal("pull failed")
	}
	if got, _ := os.ReadFile(filepath.Join(localDir, "session.jsonl")); string(got) != "owner's log, newer\n" {
		t.Errorf("session.jsonl = %q, want the owner's newer copy", got)
	}
	if _, err := os.Stat(filepath.Join(localDir, "session.host-c.jsonl")); !os.IsNotExist(err) {
		t.Error("a non-owner must not take secondary logs")
	}
}

// When this host has no copy of the session and no peer is its owner, a
// peer's session.yaml/session.jsonl materialize it once but never replace
// what's already here.
func TestPullFromNonOwnerOnlyMaterializes(t *testing.T) {
	s := newTestSessionsServer(t)
	peer := newTestSessionsServer(t)
	writeSessionFiles(t, filepath.Join(peer.recordsPath, "sess-1"), map[string]string{
		"session.yaml":  "id: sess-1\nowner: host-z\n",
		"session.jsonl": "copy v1\n",
	})
	ac := fakeACAndPeer(t, peer)
	localDir := filepath.Join(s.recordsPath, "sess-1")

	policy := sessionPullPolicy{self: "host-a", owner: "host-z"}
	if n, _ := s.pullSessionFilesFrom(ac.URL, "host-b", "sess-1", "", policy); n != 2 {
		t.Fatalf("fetched %d, want 2", n)
	}
	os.WriteFile(filepath.Join(peer.recordsPath, "sess-1", "session.jsonl"), []byte("copy v2\n"), 0644)
	if n, _ := s.pullSessionFilesFrom(ac.URL, "host-b", "sess-1", "", s.pullPolicyFor(localDir, "")); n != 0 {
		t.Errorf("fetched %d from a non-owner over an existing copy, want 0", n)
	}
}

// Raw and in-progress files are never listed or served, and max_size leaves
// larger record files behind.
func TestRawAndInProgressFilesNeverCrossHosts(t *testing.T) {
	s := newTestSessionsServer(t)
	writeSessionFiles(t, filepath.Join(s.recordsPath, "sess-1"), map[string]string{
		"100-raw.txt":                "secret",
		"100-host-b-raw.txt":         "secret",
		"101-s-raw.txt":              "secret",
		"101-s-processed.txt":        "unconsolidated",
		"102-writing.txt":            "running",
		".100-processed.txt.pulling": "temp",
		"100-processed.txt":          "ok",
	})
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/sess-1/list", nil)
	w := httptest.NewRecorder()
	s.handleSessionsAPI(w, req)
	var msg SessionFilesMsg
	if err := json.NewDecoder(w.Body).Decode(&msg); err != nil {
		t.Fatal(err)
	}
	if len(msg.Files) != 1 || msg.Files[0].Name != "100-processed.txt" {
		t.Errorf("listed %+v, want only 100-processed.txt", msg.Files)
	}
	for _, name := range []string{"100-raw.txt", "101-s-processed.txt", "102-writing.txt"} {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/sess-1/file/"+name, nil)
		w := httptest.NewRecorder()
		s.handleSessionsAPI(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", name, w.Code)
		}
	}

	p := sessionPullPolicy{self: "host-a", maxSize: 10}
	if allowed, _ := p.allows("host-b", SessionFileInfo{Name: "100-resource-big.bin", Size: 11}); allowed {
		t.Error("a record file over max_size should be left behind")
	}
	if allowed, _ := p.allows("host-b", SessionFileInfo{Name: "100-resource-small.bin", Size: 10}); !allowed {
		t.Error("a record file within max_size should be pulled")
	}
}

func TestPeerBackoff(t *testing.T) {
	s := newTestSessionsServer(t)
	if !s.peerPullAllowed("host-b") {
		t.Fatal("a peer with no failures should be allowed")
	}
	s.notePeerPull("host-b", false)
	if s.peerPullAllowed("host-b") {
		t.Error("a peer that just failed should be backed off")
	}
	s.notePeerPull("host-b", true)
	if !s.peerPullAllowed("host-b") {
		t.Error("a success should clear the backoff")
	}
}

// A full chain-call buffer of long session-pull URLs must still fit in one
// representable line when mirrored to agent-coordinator.
func TestTrimChainCallForAC(t *testing.T) {
	entries := make([]ChainCallEntry, maxChainCallEntries)
	for i := range entries {
		entries[i] = ChainCallEntry{Hop: "lr->ac", Method: "GET", URL: "http://ac:8080/host/desktop01-itjf/api/sessions/2026-10-13_14-30-48_a-fairly-long-session-name/file/1791912169-desktop01-itjf-processed.txt", TS: int64(i)}
	}
	got := trimChainCallForAC(ChainCallStateMsg{Entries: entries})
	data, _ := json.Marshal(got)
	if len(data) > maxChainCallACBytes {
		t.Errorf("trimmed payload is %d bytes, want <= %d", len(data), maxChainCallACBytes)
	}
	if len(got.Entries) == 0 || got.Entries[len(got.Entries)-1].TS != int64(maxChainCallEntries-1) {
		t.Error("trimming should keep the newest entries")
	}
}

func TestSecondaryLogNames(t *testing.T) {
	if got := secondaryLogNameFor("desktop01-itjf"); got != "session.desktop01-itjf.jsonl" {
		t.Errorf("secondaryLogNameFor = %q", got)
	}
	if got := hostFileTag("box-s"); got != "box-s_" {
		t.Errorf("hostFileTag(box-s) = %q", got)
	}
	for name, want := range map[string]bool{
		"session.jsonl":                false,
		"session.desktop01-itjf.jsonl": true,
		"session.yaml":                 false,
	} {
		if got := isSecondaryLogName(name); got != want {
			t.Errorf("isSecondaryLogName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestHandleSessionsIndexListsEverySessionWithName exercises the unscoped
// listing route RemoteSessionListingGap.md identified as missing: given no
// session ID at all, every session this host has, each with its
// session.yaml name.
func TestHandleSessionsIndexListsEverySessionWithName(t *testing.T) {
	s := newTestSessionsServer(t)
	for _, sess := range []struct{ id, name string }{{"sess-1", "First"}, {"sess-2", "Second"}} {
		dir := filepath.Join(s.recordsPath, sess.id)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session.yaml"), []byte("id: "+sess.id+"\nname: "+sess.name+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	s.handleSessionsIndex(w, req)

	var msg SessionIndexMsg
	if err := json.NewDecoder(w.Body).Decode(&msg); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	got := map[string]string{}
	for _, e := range msg.Sessions {
		got[e.ID] = e.Name
	}
	want := map[string]string{"sess-1": "First", "sess-2": "Second"}
	if len(got) != len(want) || got["sess-1"] != want["sess-1"] || got["sess-2"] != want["sess-2"] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestHandleSessionsDiscoverRefusesProxiedRequest mirrors
// TestHandleSessionsPullRefusesProxiedRequest: discovery is this host acting
// as a client on its own behalf, so it too is refused when it arrives
// through agent-coordinator's transparent proxy.
func TestHandleSessionsDiscoverRefusesProxiedRequest(t *testing.T) {
	s := newTestSessionsServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/discover", nil)
	req.Header.Set(proxiedHeader, "agent-coordinator")
	w := httptest.NewRecorder()
	s.handleSessionsAPI(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("proxied discover request: got status %d, want 403", w.Code)
	}
}

// TestIndexSessionsFromTagsEachEntryWithItsHost exercises discovery's
// per-peer fetch (see handleSessionsDiscover) through a fake
// agent-coordinator standing in front of a real peer Server -- the same
// shape as pullSessionFilesFrom's own tests above, since handleSessionsPull
// has no full-path test through acHTTPAddr either (see
// TestHandleSessionsPullNoACIsANoOp). Nothing is fetched or written into
// this host's own AGENT_RECORDS_PATH: discovery only ever lists, it never
// materializes a local session directory (see RemoteSessionListingGap.md's
// "What would need to be added").
func TestIndexSessionsFromTagsEachEntryWithItsHost(t *testing.T) {
	s := newTestSessionsServer(t)
	peer := newTestSessionsServer(t)
	peerDir := filepath.Join(peer.recordsPath, "sess-remote")
	if err := os.MkdirAll(peerDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peerDir, "session.yaml"), []byte("id: sess-remote\nname: Remote Session\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ac := fakeACAndPeer(t, peer)

	entries := s.indexSessionsFrom(ac.URL, "host-b")
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	got := entries[0]
	if got.HostID != "host-b" || got.ID != "sess-remote" || got.Name != "Remote Session" {
		t.Errorf("got %+v, want {host-b sess-remote Remote Session}", got)
	}

	if dirEntries, err := os.ReadDir(s.recordsPath); err != nil || len(dirEntries) != 0 {
		t.Errorf("discovery must not create local session directories, found %d entries (err %v)", len(dirEntries), err)
	}
}

// TestHandleSessionsDiscoverNoACIsANoOp mirrors
// TestHandleSessionsPullNoACIsANoOp: discovery with no live
// agent-coordinator connection succeeds trivially rather than erroring.
func TestHandleSessionsDiscoverNoACIsANoOp(t *testing.T) {
	s := newTestSessionsServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/discover", nil)
	w := httptest.NewRecorder()
	s.handleSessionsAPI(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", w.Code)
	}
	var msg SessionDiscoveryMsg
	if err := json.NewDecoder(w.Body).Decode(&msg); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if msg.Hosts != 0 || len(msg.Sessions) != 0 {
		t.Errorf("got %+v, want zero hosts/sessions with no agent-coordinator connection", msg)
	}
}
