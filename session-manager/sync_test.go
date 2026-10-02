package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"representable"
)

// TestTriggerSessionSyncNoClientIsANoOp verifies the sync trigger is a safe
// no-op before any local-representative connection exists -- sendSessionView
// (sessions.go) must still render a purely local view in that case.
func TestTriggerSessionSyncNoClientIsANoOp(t *testing.T) {
	s := newServer()
	s.name = "sessions"
	s.triggerSessionSync("some-session") // must not panic or block
}

// TestTriggerSessionSyncPostsAllGlobsToPeerHTTPPort verifies the end-to-end
// wiring described in docs/DistributedSessionsBrainstorm.md: once connected
// to local-representative (which discloses its own HTTP port over
// representable's "hello" message -- see representable.Client.PeerHTTPPort),
// triggerSessionSync POSTs a sync pull for each of sessionSyncGlobs
// ("session.jsonl", "session.yaml", "*-processed.txt") to that host's
// "/api/sessions/<id>/pull".
func TestTriggerSessionSyncPostsAllGlobsToPeerHTTPPort(t *testing.T) {
	var mu sync.Mutex
	var gotPaths []string
	var gotGlobs []string
	var gotModes []string
	lr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPaths = append(gotPaths, r.URL.Path)
		gotGlobs = append(gotGlobs, r.URL.Query().Get("glob"))
		gotModes = append(gotModes, r.URL.Query().Get("mode"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessionPullResultMsg{})
	}))
	defer lr.Close()
	lrURL, err := url.Parse(lr.URL)
	if err != nil {
		t.Fatal(err)
	}

	reprAddr := freeTCPAddr(t)
	reprSrv, err := representable.NewServer(reprAddr, representable.Mode(false))
	if err != nil {
		t.Fatalf("representable.NewServer: %v", err)
	}
	reprSrv.SetHTTPPort(lrURL.Port())

	host, port, err := net.SplitHostPort(reprAddr)
	if err != nil {
		t.Fatal(err)
	}

	s := newServer()
	s.name = "sessions"
	s.startConnectLoop(host, port)
	waitForReprStatus(t, s, "connected")

	s.triggerSessionSync("my-session")

	mu.Lock()
	defer mu.Unlock()
	if len(gotPaths) != len(sessionSyncGlobs) {
		t.Fatalf("got %d pull requests, want %d (one per glob): paths=%v", len(gotPaths), len(sessionSyncGlobs), gotPaths)
	}
	for i, p := range gotPaths {
		if p != "/api/sessions/my-session/pull" {
			t.Errorf("request %d path = %q, want /api/sessions/my-session/pull", i, p)
		}
		if gotModes[i] != "sync" {
			t.Errorf("request %d mode = %q, want sync", i, gotModes[i])
		}
	}
	wantGlobs := map[string]bool{"session.jsonl": false, "session.yaml": false, "*-processed.txt": false}
	for _, g := range gotGlobs {
		if _, ok := wantGlobs[g]; !ok {
			t.Errorf("unexpected glob %q", g)
			continue
		}
		wantGlobs[g] = true
	}
	for g, seen := range wantGlobs {
		if !seen {
			t.Errorf("expected a pull request for glob %q", g)
		}
	}

	s.disconnectRepr()
}

// TestTriggerSessionSyncNoHTTPPortIsANoOp verifies a connected
// local-representative that never disclosed an HTTP port (SetHTTPPort not
// called -- not every representable.Server caller has one) is also a safe
// no-op, same as no connection at all.
func TestTriggerSessionSyncNoHTTPPortIsANoOp(t *testing.T) {
	reprAddr := freeTCPAddr(t)
	if _, err := representable.NewServer(reprAddr, representable.Mode(false)); err != nil {
		t.Fatalf("representable.NewServer: %v", err)
	}
	host, port, err := net.SplitHostPort(reprAddr)
	if err != nil {
		t.Fatal(err)
	}

	s := newServer()
	s.name = "sessions"
	s.startConnectLoop(host, port)
	waitForReprStatus(t, s, "connected")

	s.triggerSessionSync("my-session") // must not panic or block

	s.disconnectRepr()
}

// TestRequestSessionPullBuildsExpectedURL is a narrow unit check on the
// request-shaping helper itself, independent of the representable plumbing
// above.
func TestRequestSessionPullBuildsExpectedURL(t *testing.T) {
	var gotPath, gotQuery string
	lr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessionPullResultMsg{})
	}))
	defer lr.Close()
	lrURL, err := url.Parse(lr.URL)
	if err != nil {
		t.Fatal(err)
	}

	if ok := requestSessionPull(nil, lrURL.Hostname(), lrURL.Port(), "sess with spaces", "*-processed.txt"); !ok {
		t.Errorf("requestSessionPull = false, want true (no errors reported)")
	}

	if gotPath != "/api/sessions/sess with spaces/pull" {
		t.Errorf("path = %q, want /api/sessions/sess with spaces/pull (percent-decoded)", gotPath)
	}
	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("glob") != "*-processed.txt" || q.Get("mode") != "sync" {
		t.Errorf("query = %q, want glob=*-processed.txt&mode=sync", gotQuery)
	}
}

// TestRequestSessionPullReportsIncompleteOnUpstreamErrors exercises the
// Step1SubstepBPrompt.md Revision G fix: local-representative reporting
// SessionPullResultMsg.Errors > 0 (a peer it couldn't reach, same as
// condocs/initialDistributedSessionsImpls/31e41125_network-debug-1790867771753.log's
// "502 then 200" sequence) must make requestSessionPull return false, not
// true -- even though the HTTP call to local-representative itself succeeded.
func TestRequestSessionPullReportsIncompleteOnUpstreamErrors(t *testing.T) {
	lr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessionPullResultMsg{Errors: 1})
	}))
	defer lr.Close()
	lrURL, err := url.Parse(lr.URL)
	if err != nil {
		t.Fatal(err)
	}

	if ok := requestSessionPull(nil, lrURL.Hostname(), lrURL.Port(), "sess-1", "session.jsonl"); ok {
		t.Errorf("requestSessionPull = true, want false (local-representative reported an unreachable peer)")
	}
}

// TestTriggerSessionsDiscoveryNoClientIsANoOp mirrors
// TestTriggerSessionSyncNoClientIsANoOp: listSessionsWithRemote must still
// render a purely local list before any local-representative connection
// exists.
func TestTriggerSessionsDiscoveryNoClientIsANoOp(t *testing.T) {
	s := newServer()
	s.name = "sessions"
	if got := s.triggerSessionsDiscovery(); got != nil {
		t.Errorf("got %+v, want nil with no local-representative connection", got)
	}
}

// TestTriggerSessionsDiscoveryPostsToPeerHTTPPort verifies the wiring for
// Step1SubstepBPrompt.md Revision A's poll: once connected to
// local-representative, triggerSessionsDiscovery POSTs to
// "/api/sessions/discover" and returns the sessions it reports.
func TestTriggerSessionsDiscoveryPostsToPeerHTTPPort(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotMethod string
	lr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotMethod = r.Method
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessionDiscoveryMsg{Sessions: []remoteSessionEntry{
			{HostID: "host-b", ID: "sess-remote", Name: "Remote Session"},
		}})
	}))
	defer lr.Close()
	lrURL, err := url.Parse(lr.URL)
	if err != nil {
		t.Fatal(err)
	}

	reprAddr := freeTCPAddr(t)
	reprSrv, err := representable.NewServer(reprAddr, representable.Mode(false))
	if err != nil {
		t.Fatalf("representable.NewServer: %v", err)
	}
	reprSrv.SetHTTPPort(lrURL.Port())

	host, port, err := net.SplitHostPort(reprAddr)
	if err != nil {
		t.Fatal(err)
	}

	s := newServer()
	s.name = "sessions"
	s.startConnectLoop(host, port)
	waitForReprStatus(t, s, "connected")

	got := s.triggerSessionsDiscovery()

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != http.MethodPost || gotPath != "/api/sessions/discover" {
		t.Errorf("request = %s %s, want POST /api/sessions/discover", gotMethod, gotPath)
	}
	if len(got) != 1 || got[0].HostID != "host-b" || got[0].ID != "sess-remote" {
		t.Errorf("got %+v, want one entry from host-b", got)
	}

	s.disconnectRepr()
}

// TestListSessionsWithRemoteSkipsAlreadyKnownIDs verifies
// listSessionsWithRemote's merge: a discovered session is appended as a
// Remote entry only when its ID isn't already in the local list (e.g.
// already pulled).
func TestListSessionsWithRemoteSkipsAlreadyKnownIDs(t *testing.T) {
	lr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessionDiscoveryMsg{Sessions: []remoteSessionEntry{
			{HostID: "host-b", ID: "sess-local", Name: "Already have this one"},
			{HostID: "host-b", ID: "sess-remote-only", Name: "Remote Session"},
		}})
	}))
	defer lr.Close()
	lrURL, err := url.Parse(lr.URL)
	if err != nil {
		t.Fatal(err)
	}

	reprAddr := freeTCPAddr(t)
	reprSrv, err := representable.NewServer(reprAddr, representable.Mode(false))
	if err != nil {
		t.Fatalf("representable.NewServer: %v", err)
	}
	reprSrv.SetHTTPPort(lrURL.Port())
	host, port, err := net.SplitHostPort(reprAddr)
	if err != nil {
		t.Fatal(err)
	}

	s := newServer()
	s.name = "sessions"
	s.recordsPath = t.TempDir()
	localDir := filepath.Join(s.recordsPath, "sess-local")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionYAMLIfAbsent(localDir, "sess-local", "Already have this one"); err != nil {
		t.Fatal(err)
	}
	s.startConnectLoop(host, port)
	waitForReprStatus(t, s, "connected")

	summaries, err := s.listSessionsWithRemote()
	if err != nil {
		t.Fatalf("listSessionsWithRemote: %v", err)
	}
	var sawRemoteOnly, sawLocalAsRemote bool
	for _, sum := range summaries {
		if sum.ID == "sess-remote-only" {
			sawRemoteOnly = true
			if !sum.Remote || sum.Host != "host-b" {
				t.Errorf("sess-remote-only summary = %+v, want Remote=true Host=host-b", sum)
			}
		}
		if sum.ID == "sess-local" && sum.Remote {
			sawLocalAsRemote = true
		}
	}
	if !sawRemoteOnly {
		t.Errorf("got %+v, want a remote entry for sess-remote-only", summaries)
	}
	if sawLocalAsRemote {
		t.Errorf("got %+v, sess-local should never be tagged remote (it's a local ID, coincidentally also reported by a peer)", summaries)
	}

	s.disconnectRepr()
}

// TestHandleSetSessionPullsRemoteOnlySession verifies Step1Prompt.md Revision
// B's "select remote sessions" fix: handleSetSession must not reject an id
// just because nothing under s.recordsPath has that name yet -- it has to
// give triggerSessionSync a chance to materialize it (the real
// local-representative would pull session.yaml/session.jsonl/-processed into
// s.recordsPath here) before concluding the session truly doesn't exist
// anywhere.
func TestHandleSetSessionPullsRemoteOnlySession(t *testing.T) {
	recordsPath := t.TempDir()
	lr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stand in for local-representative's real pull handler: materialize
		// the session directory on disk, exactly as pullSessionFilesFrom
		// would for a session discovered on another host.
		if err := os.MkdirAll(filepath.Join(recordsPath, "remote-only-session"), 0755); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessionPullResultMsg{})
	}))
	defer lr.Close()
	lrURL, err := url.Parse(lr.URL)
	if err != nil {
		t.Fatal(err)
	}

	reprAddr := freeTCPAddr(t)
	reprSrv, err := representable.NewServer(reprAddr, representable.Mode(false))
	if err != nil {
		t.Fatalf("representable.NewServer: %v", err)
	}
	reprSrv.SetHTTPPort(lrURL.Port())
	host, port, err := net.SplitHostPort(reprAddr)
	if err != nil {
		t.Fatal(err)
	}

	s := newServer()
	s.name = "sessions"
	s.recordsPath = recordsPath
	s.startConnectLoop(host, port)
	waitForReprStatus(t, s, "connected")

	s.handleSetSession(nil, "remote-only-session")

	if got := s.getCurrentSession(); got != "remote-only-session" {
		t.Errorf("getCurrentSession() = %q, want remote-only-session", got)
	}
	if _, err := os.Stat(filepath.Join(recordsPath, "remote-only-session")); err != nil {
		t.Errorf("session directory was not materialized locally: %v", err)
	}

	s.disconnectRepr()
}
