package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleHostsAPIReportsConnectedHosts covers the one new agent-coordinator
// route docs/DistributedSessionsBrainstorm.md's session sync needs: a
// local-representative discovering which other hosts are currently
// LR-active before pulling session files from them (see
// local-representative/sessions.go's listPeerHosts) reads this instead of
// the browser-facing "hosts" WebSocket message.
func TestHandleHostsAPIReportsConnectedHosts(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	s := newTestServerWithHost(t, backend) // registers "lr-a" as connected

	req := httptest.NewRequest(http.MethodGet, "/api/hosts", nil)
	w := httptest.NewRecorder()
	s.handleHostsAPI(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", w.Code)
	}
	var msg HostsMsg
	if err := json.NewDecoder(w.Body).Decode(&msg); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(msg.Hosts) != 1 || msg.Hosts[0].ID != "lr-a" || msg.Hosts[0].Status != "connected" {
		t.Fatalf("got %+v, want one connected host %q", msg.Hosts, "lr-a")
	}
}

// TestHandleHostsAPIRejectsNonGET keeps this a read-only route, same posture
// as every other plain listing endpoint in this codebase.
func TestHandleHostsAPIRejectsNonGET(t *testing.T) {
	s := newServer()
	req := httptest.NewRequest(http.MethodPost, "/api/hosts", nil)
	w := httptest.NewRecorder()
	s.handleHostsAPI(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("got status %d, want 405", w.Code)
	}
}
