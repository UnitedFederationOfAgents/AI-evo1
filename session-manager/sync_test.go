package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// TestTriggerSessionSyncPostsBothGlobsToPeerHTTPPort verifies the end-to-end
// wiring described in docs/DistributedSessionsBrainstorm.md: once connected
// to local-representative (which discloses its own HTTP port over
// representable's "hello" message -- see representable.Client.PeerHTTPPort),
// triggerSessionSync POSTs a sync pull for both "session.jsonl" and
// "*-processed.txt" to that host's "/api/sessions/<id>/pull".
func TestTriggerSessionSyncPostsBothGlobsToPeerHTTPPort(t *testing.T) {
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
	if len(gotPaths) != 2 {
		t.Fatalf("got %d pull requests, want 2 (one per glob): paths=%v", len(gotPaths), gotPaths)
	}
	for i, p := range gotPaths {
		if p != "/api/sessions/my-session/pull" {
			t.Errorf("request %d path = %q, want /api/sessions/my-session/pull", i, p)
		}
		if gotModes[i] != "sync" {
			t.Errorf("request %d mode = %q, want sync", i, gotModes[i])
		}
	}
	wantGlobs := map[string]bool{"session.jsonl": false, "*-processed.txt": false}
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
	}))
	defer lr.Close()
	lrURL, err := url.Parse(lr.URL)
	if err != nil {
		t.Fatal(err)
	}

	requestSessionPull(lrURL.Hostname(), lrURL.Port(), "sess with spaces", "*-processed.txt")

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
