package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clauditable/pkg/records"
	ufahostid "ufa-hostid"
)

func TestEventTypeFor(t *testing.T) {
	tests := []struct {
		agent, override, want string
	}{
		{"", "", records.EventTypeCommandExecution},
		{"none", "", records.EventTypeCommandExecution},
		{"claude", "", records.EventTypeAIInvocation},
		{"opencode", "", records.EventTypeAIInvocation},
		{"none", "custom", "custom"},
		{"claude", " command_execution ", records.EventTypeCommandExecution},
	}
	for _, tt := range tests {
		if got := eventTypeFor(tt.agent, tt.override); got != tt.want {
			t.Errorf("eventTypeFor(%q, %q) = %q, want %q", tt.agent, tt.override, got, tt.want)
		}
	}
}

// readSessionLogEvents parses every event header in a session.jsonl,
// skipping the processing headers (no timestamp or event_type) that follow
// one.
func readSessionLogEvents(t *testing.T, sessionDir string) []*records.Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sessionDir, "session.jsonl"))
	if err != nil {
		t.Fatalf("reading session.jsonl: %v", err)
	}
	var events []*records.Event
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		ev, err := records.ParseSessionLogEntry(line)
		if err != nil {
			t.Fatalf("parsing %q: %v", line, err)
		}
		if ev.Timestamp == "" && ev.EventType == "" {
			continue
		}
		events = append(events, ev)
	}
	return events
}

// newOwnedSession creates session s1 owned by this host, as new-session would.
func newOwnedSession(t *testing.T) string {
	t.Helper()
	recordsPath := t.TempDir()
	if err := ensureSession(recordsPath, "s1", "S1"); err != nil {
		t.Fatal(err)
	}
	return recordsPath
}

func TestAddNote(t *testing.T) {
	recordsPath := newOwnedSession(t)
	ts, err := addNote(recordsPath, "s1", "remember the milk\nand the eggs\n")
	if err != nil {
		t.Fatalf("addNote: %v", err)
	}
	sessionDir := filepath.Join(recordsPath, "s1")

	for _, suffix := range []string{"-raw.txt", "-processed.txt"} {
		if _, err := os.Stat(filepath.Join(sessionDir, ts+suffix)); err != nil {
			t.Errorf("expected %s%s: %v", ts, suffix, err)
		}
	}

	events := readSessionLogEvents(t, sessionDir)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventType != records.EventTypeNote {
		t.Errorf("event_type = %q, want %q", events[0].EventType, records.EventTypeNote)
	}
	if !strings.HasSuffix(events[0].RecordPath, "/"+ts+"-raw.txt") {
		t.Errorf("record_path = %q, want it to name %s-raw.txt", events[0].RecordPath, ts)
	}

	log, _ := os.ReadFile(filepath.Join(sessionDir, "session.jsonl"))
	if !strings.Contains(string(log), "IN>> remember the milk\nIN>> and the eggs\n") {
		t.Errorf("session.jsonl missing note text:\n%s", log)
	}
	if !strings.HasSuffix(string(log), "\n\n") {
		t.Errorf("session.jsonl entry should end with a blank line:\n%q", log)
	}
}

func TestAddNoteRejectsEmpty(t *testing.T) {
	if _, err := addNote(newOwnedSession(t), "s1", "  \n"); err == nil {
		t.Error("expected an error for a blank note")
	}
}

// A session that doesn't exist on this host isn't created by a note: that
// would claim it for this host (see sessionlog.go).
func TestAddNoteRejectsUnknownSession(t *testing.T) {
	recordsPath := t.TempDir()
	if _, err := addNote(recordsPath, "s1", "hello"); err == nil {
		t.Error("expected an error for a session that isn't here")
	}
	if _, err := os.Stat(filepath.Join(recordsPath, "s1")); !os.IsNotExist(err) {
		t.Errorf("the session directory should not have been created: %v", err)
	}
}

// On a host that doesn't own the session, a note goes to that host's
// secondary log under host-tagged file names, and session.jsonl is untouched.
func TestAddNoteOnNonOwnerUsesSecondaryLog(t *testing.T) {
	recordsPath := t.TempDir()
	sessionDir := filepath.Join(recordsPath, "s1")
	os.MkdirAll(sessionDir, 0755)
	os.WriteFile(filepath.Join(sessionDir, "session.yaml"), []byte("id: s1\nname: S1\nowner: some-other-host\n"), 0644)

	id, err := addNote(recordsPath, "s1", "from elsewhere")
	if err != nil {
		t.Fatalf("addNote: %v", err)
	}
	tag := hostFileTag(ufahostid.GetHostID())
	if !strings.HasSuffix(id, "-"+tag) {
		t.Errorf("record ID = %q, want it to end in -%s", id, tag)
	}
	for _, suffix := range []string{"-raw.txt", "-processed.txt"} {
		if _, err := os.Stat(filepath.Join(sessionDir, id+suffix)); err != nil {
			t.Errorf("expected %s%s: %v", id, suffix, err)
		}
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "session.jsonl")); !os.IsNotExist(err) {
		t.Error("a non-owner must not write session.jsonl")
	}
	log, err := os.ReadFile(filepath.Join(sessionDir, secondaryLogName(ufahostid.GetHostID())))
	if err != nil || !strings.Contains(string(log), "IN>> from elsewhere") {
		t.Errorf("secondary log = %q, %v; want the note", log, err)
	}
}

func TestAddNoteRedactsProcessedButNotRaw(t *testing.T) {
	recordsPath := newOwnedSession(t)
	ts, err := addNote(recordsPath, "s1", "key is AKIAABCDEFGHIJKLMNOP")
	if err != nil {
		t.Fatalf("addNote: %v", err)
	}
	sessionDir := filepath.Join(recordsPath, "s1")
	processed, _ := os.ReadFile(filepath.Join(sessionDir, ts+"-processed.txt"))
	if strings.Contains(string(processed), "AKIAABCDEFGHIJKLMNOP") {
		t.Error("processed file should be redacted")
	}
	log, _ := os.ReadFile(filepath.Join(sessionDir, "session.jsonl"))
	if strings.Contains(string(log), "AKIAABCDEFGHIJKLMNOP") {
		t.Error("session.jsonl should be redacted")
	}
	raw, _ := os.ReadFile(filepath.Join(sessionDir, ts+"-raw.txt"))
	if !strings.Contains(string(raw), "AKIAABCDEFGHIJKLMNOP") {
		t.Error("raw file should keep the original text")
	}
}

func TestAddResource(t *testing.T) {
	recordsPath := newOwnedSession(t)
	src := filepath.Join(t.TempDir(), "diagram.png")
	if err := os.WriteFile(src, []byte("not really a png"), 0644); err != nil {
		t.Fatal(err)
	}
	ts, err := addResource(recordsPath, "s1", src, "My Diagram (v2).png")
	if err != nil {
		t.Fatalf("addResource: %v", err)
	}
	sessionDir := filepath.Join(recordsPath, "s1")
	file := ts + "-resource-My-Diagram--v2-.png"
	data, err := os.ReadFile(filepath.Join(sessionDir, file))
	if err != nil {
		t.Fatalf("expected resource copy %s: %v", file, err)
	}
	if string(data) != "not really a png" {
		t.Errorf("resource content = %q", data)
	}

	events := readSessionLogEvents(t, sessionDir)
	if len(events) != 1 || events[0].EventType != records.EventTypeResource {
		t.Fatalf("expected one resource event, got %+v", events)
	}
	md := events[0].Metadata
	if md[records.MetadataResourceName] != "My Diagram (v2).png" || md[records.MetadataResourceFile] != file || md[records.MetadataResourceSize] != "16" {
		t.Errorf("unexpected metadata: %v", md)
	}
}

func TestAddResourceDefaultsNameAndRejectsDirectory(t *testing.T) {
	recordsPath := newOwnedSession(t)
	srcDir := t.TempDir()
	if _, err := addResource(recordsPath, "s1", srcDir, ""); err == nil {
		t.Error("expected an error for a directory")
	}
	src := filepath.Join(srcDir, "notes.txt")
	os.WriteFile(src, []byte("x"), 0644)
	ts, err := addResource(recordsPath, "s1", src, "")
	if err != nil {
		t.Fatalf("addResource: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recordsPath, "s1", ts+"-resource-notes.txt")); err != nil {
		t.Errorf("expected the resource to be named after its file: %v", err)
	}
}

func TestReserveRecordIDSkipsUsedSeconds(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "1000-writing.txt"), nil, 0644)
	os.WriteFile(filepath.Join(dir, "999-s-processed.txt"), nil, 0644)
	ts, f, err := reserveRecordID(dir, 1000, "")
	if err != nil {
		t.Fatalf("reserveRecordID: %v", err)
	}
	f.Close()
	if ts != 998 {
		t.Errorf("ts = %d, want 998", ts)
	}
	// 998 is now taken too.
	ts, f, err = reserveRecordID(dir, 1000, "")
	if err != nil {
		t.Fatalf("reserveRecordID: %v", err)
	}
	f.Close()
	if ts != 997 {
		t.Errorf("ts = %d, want 997", ts)
	}
}

func TestTwoNotesBothLand(t *testing.T) {
	recordsPath := newOwnedSession(t)
	a, err := addNote(recordsPath, "s1", "first")
	if err != nil {
		t.Fatal(err)
	}
	b, err := addNote(recordsPath, "s1", "second")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("both notes got record ID %s", a)
	}
	if n := len(readSessionLogEvents(t, filepath.Join(recordsPath, "s1"))); n != 2 {
		t.Errorf("expected 2 events, got %d", n)
	}
}

func TestSanitizeResourceName(t *testing.T) {
	tests := map[string]string{
		"report.pdf":             "report.pdf",
		"../../etc/passwd":       "passwd",
		"my file (1).txt":        "my-file--1-.txt",
		"...":                    "resource",
		"":                       "resource",
		strings.Repeat("a", 100): strings.Repeat("a", 80),
	}
	for in, want := range tests {
		if got := sanitizeResourceName(in); got != want {
			t.Errorf("sanitizeResourceName(%q) = %q, want %q", in, got, want)
		}
	}
}
