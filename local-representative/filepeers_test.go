package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleFilePeersNoAC verifies GET /api/file-peers names this LR and
// lists no peers when it isn't connected to agent-coordinator.
func TestHandleFilePeersNoAC(t *testing.T) {
	s := newTestSessionsServer(t)
	w := httptest.NewRecorder()
	s.handleFilePeers(w, httptest.NewRequest(http.MethodGet, "/api/file-peers", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", w.Code)
	}
	var msg FilePeersMsg
	if err := json.NewDecoder(w.Body).Decode(&msg); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if msg.Node != "host-a" {
		t.Errorf("node = %q, want host-a", msg.Node)
	}
	if msg.Peers == nil || len(msg.Peers) != 0 {
		t.Errorf("peers = %#v, want an empty list", msg.Peers)
	}
}

// TestFilePeersFromListsOtherNodesThroughAC verifies each connected peer
// comes back as AC's /host/<node> proxy base.
func TestFilePeersFromListsOtherNodesThroughAC(t *testing.T) {
	s := newTestSessionsServer(t)
	ac := fakeACAndPeer(t, newTestSessionsServer(t))
	peers := s.filePeersFrom(ac.URL)
	if len(peers) != 1 || peers[0].Node != "host-b" || peers[0].Base != ac.URL+"/host/host-b" {
		t.Errorf("peers = %#v, want host-b at %s/host/host-b", peers, ac.URL)
	}
}
