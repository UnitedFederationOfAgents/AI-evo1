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
	if err := os.WriteFile(filepath.Join(sessionDir, "100-s-processed.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "100-s-raw.txt"), []byte("raw content"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/sess-1/list?glob=*-s-processed.txt", nil)
	w := httptest.NewRecorder()
	s.handleSessionsAPI(w, req)

	var msg SessionFilesMsg
	if err := json.NewDecoder(w.Body).Decode(&msg); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(msg.Files) != 1 || msg.Files[0].Name != "100-s-processed.txt" {
		t.Fatalf("expected only the glob-matching file, got %+v", msg.Files)
	}
	wantSum, err := fileSHA256(filepath.Join(sessionDir, "100-s-processed.txt"))
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

// TestPullSessionFilesFromOnceNeverRefetchesAnExistingName exercises the
// "once" mode end-to-end (list -> fetch) through a fake agent-coordinator
// standing in front of a real peer Server: a file already present locally by
// name is left untouched even if its remote content differs, matching
// clauditable/distsync.go's triggerOnceTransfer contract ("a session's
// directory only ever gains new secondary records, never rewrites existing
// ones").
func TestPullSessionFilesFromOnceNeverRefetchesAnExistingName(t *testing.T) {
	s := newTestSessionsServer(t)
	peer := newTestSessionsServer(t)
	peerDir := filepath.Join(peer.recordsPath, "sess-1")
	if err := os.MkdirAll(peerDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peerDir, "100-s-processed.txt"), []byte("remote content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peerDir, "200-s-processed.txt"), []byte("new record"), 0644); err != nil {
		t.Fatal(err)
	}

	localDir := filepath.Join(s.recordsPath, "sess-1")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "100-s-processed.txt"), []byte("local content, already have this name"), 0644); err != nil {
		t.Fatal(err)
	}

	ac := fakeACAndPeer(t, peer)

	fetched := s.pullSessionFilesFrom(ac.URL, "host-b", "sess-1", "*-s-processed.txt", true /* once */)
	if fetched != 1 {
		t.Fatalf("fetched = %d, want 1 (only the new 200-*)", fetched)
	}

	// The already-present name must be untouched, even though its remote
	// content differs.
	got, err := os.ReadFile(filepath.Join(localDir, "100-s-processed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "local content, already have this name" {
		t.Errorf("once-transfer overwrote an existing name: got %q", got)
	}

	// The new record should have been pulled in verbatim.
	got, err = os.ReadFile(filepath.Join(localDir, "200-s-processed.txt"))
	if err != nil {
		t.Fatalf("expected 200-s-processed.txt to be pulled in: %v", err)
	}
	if string(got) != "new record" {
		t.Errorf("pulled content = %q, want %q", got, "new record")
	}
}

// TestPullSessionFilesFromSyncRefetchesOnChecksumMismatch exercises "sync"
// mode: unlike "once", a name that's already present but whose content
// differs from the remote's is refetched, and an unchanged one is left alone
// (no needless re-transfer) -- see session-manager/repr.go's
// triggerSessionSync contract.
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

	fetched := s.pullSessionFilesFrom(ac.URL, "host-b", "sess-1", "session.jsonl", false /* sync */)
	if fetched != 1 {
		t.Fatalf("fetched = %d, want 1 (checksum mismatch)", fetched)
	}
	got, err := os.ReadFile(filepath.Join(localDir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "updated log\n" {
		t.Errorf("sync should have overwritten stale content: got %q", got)
	}

	// A second sync with matching content should not report a fetch.
	fetched = s.pullSessionFilesFrom(ac.URL, "host-b", "sess-1", "session.jsonl", false)
	if fetched != 0 {
		t.Errorf("fetched = %d on an unchanged file, want 0", fetched)
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
