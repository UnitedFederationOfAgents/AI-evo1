package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newTestServerWithHost creates an agent-coordinator Server with one
// connected host, "lr-a", whose local-representative HTTP dashboard is
// resolved to backend's address -- as if backend's port had been reported
// over representable's "lr-http" data message.
func newTestServerWithHost(t *testing.T, backend *httptest.Server) *Server {
	t.Helper()
	s := newServer()
	u, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	hs, _ := s.getOrCreateHost("lr-a")
	hs.mu.Lock()
	hs.connected = true
	hs.lrHTTPPort = u.Port()
	hs.mu.Unlock()
	return s
}

// TestHandleFileUploadRelay covers Path 1 of docs/DistributedExchange.md end
// to end through proxyToHost's routing: a POST to /host/<id>/api/files is
// relayed to that host's local-representative stamped with both
// proxiedHeader and relayedUploadHeader, and the backend's response is piped
// straight back to the caller.
func TestHandleFileUploadRelay(t *testing.T) {
	var gotMethod, gotPath, gotProxied, gotRelayed, gotBody string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotProxied = r.Header.Get(proxiedHeader)
		gotRelayed = r.Header.Get(relayedUploadHeader)
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"files":[]}`))
	}))
	defer backend.Close()

	s := newTestServerWithHost(t, backend)

	req := httptest.NewRequest(http.MethodPost, "/host/lr-a/api/files", strings.NewReader("multipart-body"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	rec := httptest.NewRecorder()
	s.proxyToHost(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("relay: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotMethod != http.MethodPost || gotPath != "/api/files" {
		t.Errorf("backend got %s %s, want POST /api/files", gotMethod, gotPath)
	}
	if gotProxied != acRelayStamp {
		t.Errorf("backend saw proxiedHeader = %q, want %q", gotProxied, acRelayStamp)
	}
	if gotRelayed != acRelayStamp {
		t.Errorf("backend saw relayedUploadHeader = %q, want %q", gotRelayed, acRelayStamp)
	}
	if gotBody != "multipart-body" {
		t.Errorf("backend saw body = %q, want %q", gotBody, "multipart-body")
	}
	if rec.Body.String() != `{"files":[]}` {
		t.Errorf("relay response body = %q, want the backend's response piped through", rec.Body.String())
	}
}

// TestHandleFileUploadRelayUnknownHost verifies a relay attempt against a
// host agent-coordinator has never heard of 404s rather than panicking or
// hanging.
func TestHandleFileUploadRelayUnknownHost(t *testing.T) {
	s := newServer()
	req := httptest.NewRequest(http.MethodPost, "/host/nope/api/files", strings.NewReader(""))
	rec := httptest.NewRecorder()
	s.proxyToHost(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestProxyToHostStripsClientSuppliedRelayHeader is the defense-in-depth half
// of Path 1: a client can't spoof relayedUploadHeader on a request that goes
// through the transparent /host/<id>/* passthrough (as opposed to
// handleFileUploadRelay's dedicated route) to trick LR into treating it as an
// AC-relayed upload.
func TestProxyToHostStripsClientSuppliedRelayHeader(t *testing.T) {
	var gotRelayed string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRelayed = r.Header.Get(relayedUploadHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	s := newTestServerWithHost(t, backend)

	// A GET, not a POST to api/files, so this takes the transparent proxy
	// path rather than handleFileUploadRelay.
	req := httptest.NewRequest(http.MethodGet, "/host/lr-a/api/files/some-id", nil)
	req.Header.Set(relayedUploadHeader, acRelayStamp)
	rec := httptest.NewRecorder()
	s.proxyToHost(rec, req)

	if gotRelayed != "" {
		t.Errorf("backend saw relayedUploadHeader = %q through the transparent proxy, want it stripped", gotRelayed)
	}
}
