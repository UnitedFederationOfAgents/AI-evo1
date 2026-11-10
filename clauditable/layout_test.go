package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"clauditable/pkg/records"
)

func TestSessionsRoot(t *testing.T) {
	if got, want := sessionsRoot("/records"), filepath.Join("/records", "sessions"); got != want {
		t.Errorf("sessionsRoot = %q, want %q", got, want)
	}
}

// writeFileIn creates dir (and parents) and writes name in it.
func writeFileIn(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateLegacySessions(t *testing.T) {
	root := t.TempDir()
	writeFileIn(t, filepath.Join(root, "with-log"), "session.jsonl", "{}\n")
	writeFileIn(t, filepath.Join(root, "with-yaml"), "session.yaml", "id: with-yaml\n")
	writeFileIn(t, filepath.Join(root, "plain"), "notes.txt", "not a session")
	// Reserved names are never sessions, whatever they hold.
	writeFileIn(t, filepath.Join(root, dttCallsDirName), "session.yaml", "id: x\n")
	writeFileIn(t, filepath.Join(root, invocationsDirName), "session.jsonl", "{}\n")
	// A session already present under sessions/ is not merged or replaced.
	writeFileIn(t, filepath.Join(root, "clash"), "session.yaml", "id: clash\nname: legacy\n")
	writeFileIn(t, filepath.Join(sessionsRoot(root), "clash"), "session.yaml", "id: clash\nname: current\n")
	// A plain file at the root is ignored.
	writeFileIn(t, root, "session.jsonl", "stray")

	moved, err := migrateLegacySessions(root)
	if err != nil {
		t.Fatalf("migrateLegacySessions: %v", err)
	}
	if moved != 2 {
		t.Errorf("moved = %d, want 2", moved)
	}

	for _, name := range []string{"with-log", "with-yaml"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have left the root: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(sessionsRoot(root), name)); err != nil {
			t.Errorf("%s should be under sessions/: %v", name, err)
		}
	}
	for _, name := range []string{"plain", dttCallsDirName, invocationsDirName, "clash"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("%s should have stayed at the root: %v", name, err)
		}
	}
	for _, name := range []string{"plain", dttCallsDirName, invocationsDirName} {
		if _, err := os.Stat(filepath.Join(sessionsRoot(root), name)); !os.IsNotExist(err) {
			t.Errorf("%s should not be under sessions/: %v", name, err)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(sessionsRoot(root), "clash", "session.yaml")); !strings.Contains(string(data), "name: current") {
		t.Errorf("existing sessions/clash should be untouched, got %q", data)
	}

	// Nothing left to do the second time.
	if moved, err := migrateLegacySessions(root); err != nil || moved != 0 {
		t.Errorf("second migrateLegacySessions = %d, %v; want 0, nil", moved, err)
	}
}

func TestMigrateLegacySessionsMissingRoot(t *testing.T) {
	moved, err := migrateLegacySessions(filepath.Join(t.TempDir(), "absent"))
	if err != nil || moved != 0 {
		t.Errorf("migrateLegacySessions(absent) = %d, %v; want 0, nil", moved, err)
	}
}

func TestRecordCopyDir(t *testing.T) {
	tests := map[string]string{
		records.EventTypeDTTCall:          dttCallsDirName,
		records.EventTypeAIInvocation:     invocationsDirName,
		records.EventTypeCommandExecution: "",
		records.EventTypeNote:             "",
		records.EventTypeResource:         "",
	}
	for eventType, want := range tests {
		if got := recordCopyDir(eventType); got != want {
			t.Errorf("recordCopyDir(%q) = %q, want %q", eventType, got, want)
		}
	}
}

func TestWriteRecordCopy(t *testing.T) {
	root := t.TempDir()
	record := records.Record{
		Event: records.Event{
			Timestamp:  "2026-10-27T10:00:00Z",
			EventType:  records.EventTypeDTTCall,
			RecordPath: "/elsewhere/1793101234-raw.txt",
		},
		Command: "dtt read thing.dtt.yaml",
		Stdout:  "out\n",
		Stderr:  "err\n",
	}

	path, err := writeRecordCopy(root, dttCallsDirName, "s1", "1793101234-raw.txt", 1793101234, record)
	if err != nil {
		t.Fatalf("writeRecordCopy: %v", err)
	}
	if filepath.Dir(path) != filepath.Join(root, dttCallsDirName) {
		t.Errorf("copy written to %s, want it in %s", path, filepath.Join(root, dttCallsDirName))
	}
	name := filepath.Base(path)
	if !regexp.MustCompile(`^\d+-[a-z0-9]{3}\.txt$`).MatchString(name) || !strings.HasPrefix(name, "1793101234-") {
		t.Errorf("copy name %q doesn't match <unix>-<rrr>.txt", name)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	firstLine, _, _ := strings.Cut(content, "\n")
	ev, err := records.ParseSessionLogEntry(firstLine)
	if err != nil {
		t.Fatalf("parsing first line %q: %v", firstLine, err)
	}
	if ev.Session != "s1" || ev.SessionRecord != "1793101234-raw.txt" || ev.RecordPath != path || ev.EventType != records.EventTypeDTTCall {
		t.Errorf("copy event = %+v", ev)
	}
	if !strings.Contains(firstLine, `"session":"s1"`) || !strings.Contains(firstLine, `"session_record":"1793101234-raw.txt"`) {
		t.Errorf("first line missing session fields: %s", firstLine)
	}
	if !strings.Contains(content, records.WrittenFileSeparator+"dtt read thing.dtt.yaml"+records.ResponseSeparator+"out\n[STDERR]\nerr\n") {
		t.Errorf("copy lacks the full written-file body:\n%s", content)
	}
	// The caller's record is left alone.
	if record.Event.Session != "" || record.Event.RecordPath != "/elsewhere/1793101234-raw.txt" {
		t.Errorf("caller's record was modified: %+v", record.Event)
	}

	// A second copy for the same second gets its own file.
	path2, err := writeRecordCopy(root, dttCallsDirName, "s1", "1793101234-raw.txt", 1793101234, record)
	if err != nil {
		t.Fatalf("second writeRecordCopy: %v", err)
	}
	if path2 == path {
		t.Errorf("both copies written to %s", path)
	}
}
