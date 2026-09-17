package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// newTestSessionServer returns a Server whose records path is a fresh temp
// directory, named "host-a" so ingestRemoteSession's "is this locally owned"
// checks have something concrete to compare against.
func newTestSessionServer(t *testing.T) *Server {
	t.Helper()
	s := newServer("host-a")
	s.recordsPath = t.TempDir()
	return s
}

// writeTestSession writes a minimal session.yaml directly, bypassing
// writeSessionYAML, so tests can set up fixtures independent of it.
func writeTestSession(t *testing.T, recordsPath, id, owner string) {
	t.Helper()
	dir := filepath.Join(recordsPath, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "id: " + id + "\nname: \nowner: " + owner + "\ncreated: 2026-09-17T00:00:00Z\n"
	if err := os.WriteFile(filepath.Join(dir, "session.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestParseSessionYAMLRoundTrip verifies writeSessionYAML's output is read
// back identically by parseSessionYAML.
func TestParseSessionYAMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.yaml")
	want := SessionInfo{ID: "2026-09-17_10-00-00_demo", Name: "Demo Session", Owner: "host-b", Created: "2026-09-17T10:00:00Z"}
	if err := writeSessionYAML(path, want); err != nil {
		t.Fatalf("writeSessionYAML: %v", err)
	}
	got, ok := parseSessionYAML(path)
	if !ok {
		t.Fatal("parseSessionYAML: ok = false, want true")
	}
	if got != want {
		t.Errorf("parseSessionYAML round-trip = %+v, want %+v", got, want)
	}
}

// TestParseSessionYAMLMissingID verifies a directory with no session.yaml (or
// one with no id field) is reported as "not a session" rather than a
// zero-value match.
func TestParseSessionYAMLMissingID(t *testing.T) {
	if _, ok := parseSessionYAML(filepath.Join(t.TempDir(), "session.yaml")); ok {
		t.Error("parseSessionYAML on a missing file: ok = true, want false")
	}
}

// TestListLocalSessionsSkipsNonSessionDirs verifies listLocalSessions only
// reports directories that actually have a session.yaml.
func TestListLocalSessionsSkipsNonSessionDirs(t *testing.T) {
	s := newTestSessionServer(t)
	writeTestSession(t, s.recordsPath, "2026-09-17-default", "host-a")
	if err := os.MkdirAll(filepath.Join(s.recordsPath, "not-a-session"), 0o755); err != nil {
		t.Fatal(err)
	}

	sessions := s.listLocalSessions()
	if len(sessions) != 1 || sessions[0].ID != "2026-09-17-default" || sessions[0].Owner != "host-a" {
		t.Fatalf("listLocalSessions() = %+v, want just the one real session", sessions)
	}
}

// TestHandleSessionsAPI verifies GET /api/sessions serves the same listing as
// listLocalSessions, as JSON.
func TestHandleSessionsAPI(t *testing.T) {
	s := newTestSessionServer(t)
	writeTestSession(t, s.recordsPath, "2026-09-17-default", "host-a")

	req := httptest.NewRequest("GET", "/api/sessions", nil)
	rec := httptest.NewRecorder()
	s.handleSessionsAPI(rec, req)

	var got SessionsStateMsg
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "2026-09-17-default" {
		t.Fatalf("handleSessionsAPI response = %+v", got)
	}
}

// TestIngestRemoteSessionWritesNewSession verifies a peer's session.yaml
// lands under this host's own records path, unmodified in content.
func TestIngestRemoteSessionWritesNewSession(t *testing.T) {
	s := newTestSessionServer(t)
	info := SessionInfo{ID: "2026-09-17_11-00-00_demo", Name: "Demo", Owner: "host-b", Created: "2026-09-17T11:00:00Z"}

	if !s.ingestRemoteSession(info, "host-b") {
		t.Fatal("ingestRemoteSession = false, want true for a fresh id")
	}
	got, ok := parseSessionYAML(filepath.Join(s.recordsPath, info.ID, "session.yaml"))
	if !ok || got != info {
		t.Fatalf("ingested session = %+v (ok=%v), want %+v", got, ok, info)
	}
}

// TestIngestRemoteSessionSkipsLocallyOwnedCollision verifies the collision
// guard described in docs/DistributedSessionsBrainstorm.md Step 1 Rev A: an
// id already claimed by a locally-owned session (e.g. two hosts' identically
// named default sessions) is never overwritten by an incoming remote copy.
func TestIngestRemoteSessionSkipsLocallyOwnedCollision(t *testing.T) {
	s := newTestSessionServer(t)
	writeTestSession(t, s.recordsPath, "2026-09-17-default", "host-a")

	remote := SessionInfo{ID: "2026-09-17-default", Name: "Host B's Default", Owner: "host-b"}
	if s.ingestRemoteSession(remote, "host-b") {
		t.Fatal("ingestRemoteSession = true, want false when the id is locally-owned")
	}

	got, ok := parseSessionYAML(filepath.Join(s.recordsPath, "2026-09-17-default", "session.yaml"))
	if !ok || got.Owner != "host-a" {
		t.Fatalf("local session was overwritten: %+v (ok=%v)", got, ok)
	}
}

// TestIngestRemoteSessionSkipsDifferentOwnerCollision verifies an id already
// cached from one remote owner is never clobbered by a different remote peer
// claiming the same id.
func TestIngestRemoteSessionSkipsDifferentOwnerCollision(t *testing.T) {
	s := newTestSessionServer(t)
	first := SessionInfo{ID: "2026-09-17-default", Name: "Host B's Default", Owner: "host-b"}
	if !s.ingestRemoteSession(first, "host-b") {
		t.Fatal("ingestRemoteSession(first) = false, want true")
	}

	second := SessionInfo{ID: "2026-09-17-default", Name: "Host C's Default", Owner: "host-c"}
	if s.ingestRemoteSession(second, "host-c") {
		t.Fatal("ingestRemoteSession(second) = true, want false — different remote owner, same id")
	}

	got, ok := parseSessionYAML(filepath.Join(s.recordsPath, "2026-09-17-default", "session.yaml"))
	if !ok || got.Owner != "host-b" {
		t.Fatalf("first remote owner's copy was clobbered: %+v (ok=%v)", got, ok)
	}
}

// TestIngestRemoteSessionRefreshesSameOwner verifies a re-sync from the same
// remote owner (e.g. a rename since the last pull) does update the cached copy.
func TestIngestRemoteSessionRefreshesSameOwner(t *testing.T) {
	s := newTestSessionServer(t)
	first := SessionInfo{ID: "2026-09-17_11-00-00_demo", Name: "Old Name", Owner: "host-b"}
	if !s.ingestRemoteSession(first, "host-b") {
		t.Fatal("ingestRemoteSession(first) = false, want true")
	}

	renamed := SessionInfo{ID: "2026-09-17_11-00-00_demo", Name: "New Name", Owner: "host-b"}
	if !s.ingestRemoteSession(renamed, "host-b") {
		t.Fatal("ingestRemoteSession(renamed) = false, want true — same owner should refresh")
	}

	got, ok := parseSessionYAML(filepath.Join(s.recordsPath, "2026-09-17_11-00-00_demo", "session.yaml"))
	if !ok || got.Name != "New Name" {
		t.Fatalf("refreshed session = %+v (ok=%v), want name %q", got, ok, "New Name")
	}
}

// TestIngestRemoteSessionSkipsOwnHost verifies a session whose owner is this
// very host (which should never appear in a peer's response, but is checked
// defensively) is never ingested as if it were remote.
func TestIngestRemoteSessionSkipsOwnHost(t *testing.T) {
	s := newTestSessionServer(t)
	info := SessionInfo{ID: "2026-09-17_11-00-00_demo", Owner: "host-a"}
	if s.ingestRemoteSession(info, "host-a") {
		t.Fatal("ingestRemoteSession = true, want false when owner is this host")
	}
	if _, err := os.Stat(filepath.Join(s.recordsPath, info.ID)); err == nil {
		t.Fatal("session directory was created for a self-owned session")
	}
}

// TestIngestRemoteSessionRejectsPathEscape verifies a malicious/malformed id
// can't be used to write outside the records path.
func TestIngestRemoteSessionRejectsPathEscape(t *testing.T) {
	s := newTestSessionServer(t)
	for _, id := range []string{"../escape", "a/b", `a\b`, ".", ".."} {
		if s.ingestRemoteSession(SessionInfo{ID: id, Owner: "host-b"}, "host-b") {
			t.Errorf("ingestRemoteSession(%q) = true, want false", id)
		}
	}
}
