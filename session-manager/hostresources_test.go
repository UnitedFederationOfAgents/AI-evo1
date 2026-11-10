package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"representable"
)

// connectFakeLR starts a session-manager connected to a representable server
// that discloses lr's port as local-representative's HTTP port, so lrBase
// resolves to lr.
func connectFakeLR(t *testing.T, lr *httptest.Server) *Server {
	t.Helper()
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
	t.Cleanup(s.disconnectRepr)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := s.lrBase(); err == nil {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatal("local-representative's HTTP port was never disclosed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHandleAddHostResourcesFetchesThroughLR(t *testing.T) {
	fake := fakeClauditable(t)
	var gotPath string
	lr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/host-files/raw" {
			http.NotFound(w, r)
			return
		}
		gotPath = r.URL.Query().Get("path")
		w.Write([]byte("host bytes"))
	}))
	defer lr.Close()
	s := connectFakeLR(t, lr)
	s.recordsPath = t.TempDir()
	os.MkdirAll(filepath.Join(sessionsRoot(s.recordsPath), "s1"), 0755)
	c := newTestClient()

	s.handleAddHostResources(c, "s1", []string{"/home/me/notes & plans.txt"})

	res := recvAnnotationResult(t, c)
	if res.Error != "" || res.Name != "notes & plans.txt" || res.Record != "1234" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if gotPath != "/home/me/notes & plans.txt" {
		t.Errorf("LR was asked for %q", gotPath)
	}
	if got, _ := os.ReadFile(filepath.Join(fake, "resource")); string(got) != "host bytes" {
		t.Errorf("resource = %q, want host bytes", got)
	}
	if args, _ := os.ReadFile(filepath.Join(fake, "args")); !strings.HasPrefix(string(args), "add-resource -name notes & plans.txt ") {
		t.Errorf("args = %q", args)
	}
}

func TestHandleAddHostResourcesReportsLRError(t *testing.T) {
	fakeClauditable(t)
	lr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "/etc/shadow: permission denied", http.StatusForbidden)
	}))
	defer lr.Close()
	s := connectFakeLR(t, lr)
	s.recordsPath = t.TempDir()
	os.MkdirAll(filepath.Join(sessionsRoot(s.recordsPath), "s1"), 0755)
	c := newTestClient()

	s.handleAddHostResources(c, "s1", []string{"/etc/shadow"})
	if res := recvAnnotationResult(t, c); !strings.Contains(res.Error, "permission denied") {
		t.Errorf("expected LR's error, got %+v", res)
	}
	s.handleAddHostResources(c, "s1", nil)
	if res := recvAnnotationResult(t, c); res.Error == "" {
		t.Error("expected an error with no paths")
	}
}

func TestHandleAddHighlightedResourcesTakesOnlyHighlighted(t *testing.T) {
	fake := fakeClauditable(t)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/files":
			json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
				{"id": "a.txt", "name": "a.txt", "highlighted": true}, // already listed by this LR
				{"id": "p.txt", "name": "p.txt", "highlighted": true},
			}})
		case "/api/files/p.txt":
			w.Write([]byte("peer bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer peer.Close()
	var fetched []string
	lr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/files":
			json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
				{"id": "a.txt", "name": "a.txt", "highlighted": true},
				{"id": "b.txt", "name": "b.txt", "highlighted": false},
			}})
		case "/api/file-peers":
			json.NewEncoder(w).Encode(map[string]any{"peers": []fileSource{{Node: "box2", Base: peer.URL}}})
		case "/api/files/a.txt", "/api/files/b.txt":
			fetched = append(fetched, r.URL.Path)
			w.Write([]byte("local bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer lr.Close()
	s := connectFakeLR(t, lr)
	s.recordsPath = t.TempDir()
	os.MkdirAll(filepath.Join(sessionsRoot(s.recordsPath), "s1"), 0755)
	c := newTestClient()

	s.handleAddHighlightedResources(c, "s1")

	res := recvAnnotationResult(t, c)
	if res.Error != "" || res.Name != "a.txt, p.txt (box2)" || len(res.Records) != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(fetched) != 1 || fetched[0] != "/api/files/a.txt" {
		t.Errorf("fetched from this LR: %v, want only a.txt", fetched)
	}
	// The fake clauditable keeps the last file it was handed: the peer's.
	if got, _ := os.ReadFile(filepath.Join(fake, "resource")); string(got) != "peer bytes" {
		t.Errorf("last resource = %q, want peer bytes", got)
	}
}

func TestHandleAddHighlightedResourcesNeedsSomething(t *testing.T) {
	fakeClauditable(t)
	lr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/files" {
			json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{}})
			return
		}
		http.NotFound(w, r)
	}))
	defer lr.Close()
	s := connectFakeLR(t, lr)
	s.recordsPath = t.TempDir()
	os.MkdirAll(filepath.Join(sessionsRoot(s.recordsPath), "s1"), 0755)
	c := newTestClient()

	s.handleAddHighlightedResources(c, "s1")
	if res := recvAnnotationResult(t, c); !strings.Contains(res.Error, "no highlighted files") {
		t.Errorf("expected no highlighted files, got %+v", res)
	}
}

func TestAddLRResourcesNotConnected(t *testing.T) {
	s := newServer()
	s.recordsPath = t.TempDir()
	os.MkdirAll(filepath.Join(sessionsRoot(s.recordsPath), "s1"), 0755)
	c := newTestClient()

	s.handleAddHighlightedResources(c, "s1")
	if res := recvAnnotationResult(t, c); !strings.Contains(res.Error, "not connected") {
		t.Errorf("expected not connected, got %+v", res)
	}
	s.handleAddHostResources(c, "s1", []string{"/tmp/x"})
	if res := recvAnnotationResult(t, c); !strings.Contains(res.Error, "not connected") {
		t.Errorf("expected not connected, got %+v", res)
	}
}
