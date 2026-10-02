package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ufahostid "ufa-hostid"
)

// TestCreateAndListSessions verifies createSession writes a session.yaml
// listSessions can then read back, with the freshly-created one marked
// current, mirroring "ufa session new" + "ufa session list" parity.
func TestCreateAndListSessions(t *testing.T) {
	dir := t.TempDir()

	id, err := createSession(dir, "My Test Session")
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if id == "" {
		t.Fatal("createSession returned empty id")
	}

	sessions, err := listSessions(dir, id)
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ID != id {
		t.Errorf("ID = %q, want %q", sessions[0].ID, id)
	}
	if sessions[0].Name != "My Test Session" {
		t.Errorf("Name = %q, want %q", sessions[0].Name, "My Test Session")
	}
	if !sessions[0].Current {
		t.Error("expected newly-created session to be marked Current")
	}
}

// TestCreateSessionRecordsOwner verifies createSession stamps session.yaml
// with an owner field naming the creating host, surfaced generically by
// describeSession like every other field (see
// condocs/initialDistributedSessionsImpls/Step2Prompt.md Revision C).
func TestCreateSessionRecordsOwner(t *testing.T) {
	dir := t.TempDir()

	id, err := createSession(dir, "Owner Check")
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	info, err := describeSession(dir, id)
	if err != nil {
		t.Fatalf("describeSession: %v", err)
	}
	var owner string
	for _, f := range info.Fields {
		if f[0] == "owner" {
			owner = f[1]
		}
	}
	if owner == "" {
		t.Fatalf("expected an owner field, got fields: %v", info.Fields)
	}
	if owner != ufahostid.GetHostID() {
		t.Errorf("owner = %q, want %q", owner, ufahostid.GetHostID())
	}
}

// TestListSessionsMissingRecordsPath verifies a nonexistent records
// directory yields an empty list rather than an error -- a fresh host
// before any session has ever been created.
func TestListSessionsMissingRecordsPath(t *testing.T) {
	sessions, err := listSessions(filepath.Join(t.TempDir(), "does-not-exist"), "")
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

// TestDescribeSessionEmptyFieldsMarshalsAsArray verifies a session.yaml
// carrying nothing but "id" (so describeSession's non-id Fields is empty)
// still marshals to "[]", not "null" -- the frontend calls .map() on
// info.fields without a nil guard.
func TestDescribeSessionEmptyFieldsMarshalsAsArray(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "bare-session")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "session.yaml"), []byte("id: bare-session\n"), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := describeSession(dir, "bare-session")
	if err != nil {
		t.Fatalf("describeSession: %v", err)
	}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"fields":[]`) {
		t.Errorf("info.Fields did not marshal to an empty array: %s", b)
	}
}

// TestRenameSessionRejectsDefault verifies rename-session parity's guard
// against renaming a "-default" session, mirroring federation-command's
// handleRenameSession.
func TestRenameSessionRejectsDefault(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "2026-01-01_00-00-00-default")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := renameSession(dir, "2026-01-01_00-00-00-default", "new name"); err == nil {
		t.Error("expected error renaming a -default session, got nil")
	}
}

// TestRenameSessionUpdatesName verifies a direct rename both succeeds and
// is reflected by describeSession/readSessionName afterward.
func TestRenameSessionUpdatesName(t *testing.T) {
	dir := t.TempDir()
	id, err := createSession(dir, "original")
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if err := renameSession(dir, id, "renamed"); err != nil {
		t.Fatalf("renameSession: %v", err)
	}
	info, err := describeSession(dir, id)
	if err != nil {
		t.Fatalf("describeSession: %v", err)
	}
	got := ""
	for _, f := range info.Fields {
		if f[0] == "name" {
			got = f[1]
		}
	}
	if got != "renamed" {
		t.Errorf("name field = %q, want %q", got, "renamed")
	}
}

// TestArchiveSessionsMovesAll verifies archive-session parity relocates
// every session directory and leaves the records path empty, mirroring
// clauditable's runArchive.
func TestArchiveSessionsMovesAll(t *testing.T) {
	dir := t.TempDir()
	if _, err := createSession(dir, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := createSession(dir, "two"); err != nil {
		t.Fatal(err)
	}

	count, archiveDir, err := archiveSessions(dir)
	if err != nil {
		t.Fatalf("archiveSessions: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
	if archiveDir == "" {
		t.Error("expected non-empty archiveDir")
	}

	remaining, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected records path empty after archive, got %d entries", len(remaining))
	}
}

// TestArchiveSessionsNoneToArchive verifies archiving an empty records path
// is a no-op, not an error.
func TestArchiveSessionsNoneToArchive(t *testing.T) {
	dir := t.TempDir()
	count, archiveDir, err := archiveSessions(dir)
	if err != nil {
		t.Fatalf("archiveSessions: %v", err)
	}
	if count != 0 || archiveDir != "" {
		t.Errorf("expected no-op (0, \"\"), got (%d, %q)", count, archiveDir)
	}
}

// TestParseSessionLog verifies parseSessionLog reconstructs structured
// entries from clauditable/pkg/records.Record.FormatSessionLog's on-disk
// shape: a JSON header line followed by IN>>/OUT>>/ERR>> prefixed blocks,
// repeated per record.
func TestParseSessionLog(t *testing.T) {
	data := `{"timestamp":"2026-01-01T00:00:00Z","event_type":"command_execution","agent":"claude","model":"opus","duration_ms":1500,"exit_code":0}
IN>> list files
OUT>> a.txt
OUT>> b.txt
{"timestamp":"2026-01-01T00:01:00Z","event_type":"command_execution","exit_code":1}
IN>> boom
ERR>> failed
`
	entries := parseSessionLog([]byte(data))
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	first := entries[0]
	if first.Agent != "claude" || first.Model != "opus" || first.DurationMs != 1500 {
		t.Errorf("first entry header mismatch: %+v", first)
	}
	if first.Input != "list files" {
		t.Errorf("first.Input = %q", first.Input)
	}
	if first.Output != "a.txt\nb.txt" {
		t.Errorf("first.Output = %q", first.Output)
	}

	second := entries[1]
	if second.ExitCode != 1 {
		t.Errorf("second.ExitCode = %d, want 1", second.ExitCode)
	}
	if second.Input != "boom" || second.Error != "failed" {
		t.Errorf("second entry mismatch: %+v", second)
	}
}

// TestParseSessionLogSkipsProcessingHeaders verifies a FormatProcessedFile-
// style auto-maintenance header (JSON with neither timestamp nor
// event_type) doesn't get mistaken for a new entry boundary.
func TestParseSessionLogSkipsProcessingHeaders(t *testing.T) {
	data := `{"timestamp":"2026-01-01T00:00:00Z","event_type":"command_execution"}
{"processing_type":"redact_secrets","applied_at":"2026-01-01T00:00:01Z","count":1}
IN>> hello
OUT>> world
`
	entries := parseSessionLog([]byte(data))
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Input != "hello" || entries[0].Output != "world" {
		t.Errorf("entry mismatch: %+v", entries[0])
	}
}

// TestViewSessionNoLog verifies viewing a session with no session.jsonl yet
// (a brand-new session) returns an empty transcript rather than an error.
func TestViewSessionNoLog(t *testing.T) {
	dir := t.TempDir()
	id, err := createSession(dir, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	view, err := viewSession(dir, id)
	if err != nil {
		t.Fatalf("viewSession: %v", err)
	}
	if len(view.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(view.Entries))
	}
	if view.Name != "fresh" {
		t.Errorf("Name = %q, want %q", view.Name, "fresh")
	}
	// Entries must marshal to "[]", not "null" -- the frontend calls
	// .map()/.length on view.entries without a nil guard.
	b, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"entries":[]`) {
		t.Errorf("view.Entries did not marshal to an empty array: %s", b)
	}
}

// TestSlugify documents generateSessionID's ID-safe slug shape (lowercase,
// hyphen-separated, capped at 40 chars), mirroring clauditable's slugify.
func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"My Test Session!": "my-test-session",
		"  leading/trail  ": "leading-trail",
		"":                  "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStripSurroundingQuotes mirrors federation-command/main_test.go's test
// of the same name.
func TestStripSurroundingQuotes(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{`"My New Session"`, "My New Session"},
		{`'My New Session'`, "My New Session"},
		{"My New Session", "My New Session"},
		{`"unterminated`, `"unterminated`},
		{`"`, `"`},
		{"", ""},
		{`"mismatched'`, `"mismatched'`},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := stripSurroundingQuotes(tt.in); got != tt.want {
				t.Errorf("stripSurroundingQuotes(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestCreateSessionStripsQuotes verifies createSession strips a user's
// habitual quoting of a multi-word name (see resource "Debug Quotes": the
// Session Manager web UI's "new session name…" field is a plain text input,
// not shell-parsed, so typed quotes previously ended up baked into the name).
func TestCreateSessionStripsQuotes(t *testing.T) {
	dir := t.TempDir()

	id, err := createSession(dir, `"Name Without Quotes"`)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}

	sessions, err := listSessions(dir, id)
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Name != "Name Without Quotes" {
		t.Errorf("Name = %q, want %q", sessions[0].Name, "Name Without Quotes")
	}
}

// TestRenameSessionStripsQuotes is TestCreateSessionStripsQuotes's
// rename-session equivalent.
func TestRenameSessionStripsQuotes(t *testing.T) {
	dir := t.TempDir()

	id, err := createSession(dir, "Original Name")
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if err := renameSession(dir, id, `"Renamed Without Quotes"`); err != nil {
		t.Fatalf("renameSession: %v", err)
	}

	sessions, err := listSessions(dir, id)
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Name != "Renamed Without Quotes" {
		t.Errorf("Name = %q, want %q", sessions[0].Name, "Renamed Without Quotes")
	}
}
