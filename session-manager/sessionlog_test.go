package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Log blocks as clauditable and federation-command write them: clauditable
// records (header, processing header, IN/OUT lines, blank line) and
// federation-command's single-line records.
const (
	turnOwnerA = `{"timestamp":"2026-10-13T14:33:00Z","event_type":"command_execution","record_path":"/r/s/1791902034-raw.txt"}
{"processing_type":"no_op","applied_at":"2026-10-13T14:33:01Z"}
IN>> ls
OUT>> launches.txt

`
	turnOwnerB = `{"timestamp":"2026-10-13T17:40:00Z","event_type":"command_execution","record_path":"/r/s/1791914400-raw.txt"}
IN>> date

`
	turnDesktop = `{"timestamp":"2026-10-13T17:33:00Z","event_type":"ai_invocation","agent":"claude","record_path":"/r/s/1791913980-desktop01-raw.txt"}
IN>> U up?
OUT>> yes

`
	turnFC = `{"id":"ab12cd34","cmd":"set-session s","ts":"2026-10-13T17:21:48Z","delta_ms":0,"exit":0,"host":"desktop01"}
`
)

func newSessionDir(t *testing.T, owner string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "s")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	files["session.yaml"] = "id: s\nname: S\nowner: " + owner + "\n"
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestViewSplicesSecondaryLogByTime(t *testing.T) {
	dir := newSessionDir(t, "provisioner01", map[string]string{
		"session.jsonl":           turnOwnerA + turnOwnerB,
		"session.desktop01.jsonl": turnFC + turnDesktop,
	})
	view, err := viewSession(filepath.Dir(dir), "s")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range view.Entries {
		got = append(got, e.Input+"@"+e.SecondaryHost)
	}
	// federation-command's own records aren't shown as turns.
	want := []string{"ls@", "U up?@desktop01", "date@"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func TestViewDoesNotDuplicateFusedTurns(t *testing.T) {
	dir := newSessionDir(t, "provisioner01", map[string]string{
		"session.jsonl":           turnOwnerA + turnDesktop,
		"session.desktop01.jsonl": turnDesktop,
	})
	view, err := viewSession(filepath.Dir(dir), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(view.Entries))
	}
	for _, e := range view.Entries {
		if e.SecondaryHost != "" {
			t.Errorf("a turn already in session.jsonl shouldn't be marked as from a secondary: %+v", e)
		}
	}
}

func TestViewWarnsAboutBadLinesAndMissingRecords(t *testing.T) {
	dir := newSessionDir(t, "provisioner01", map[string]string{
		"session.jsonl": turnOwnerA + "{not json\nstray text\n",
	})
	view, err := viewSession(filepath.Dir(dir), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Entries) != 1 || !view.Entries[0].RecordMissing {
		t.Fatalf("entries = %+v, want one entry with record_missing", view.Entries)
	}
	if len(view.Warnings) != 2 {
		t.Errorf("warnings = %v, want one for the bad line and one for the missing record", view.Warnings)
	}
}

func TestFuseSecondaryLogsOnOwner(t *testing.T) {
	dir := newSessionDir(t, "provisioner01", map[string]string{
		"session.jsonl": turnOwnerA,
		// The last turn is still being written (no trailing newline).
		"session.desktop01.jsonl": turnFC + turnDesktop + `{"timestamp":"2026-10-13T17:50:00Z","event_type":"note"}`,
	})
	n, err := fuseSecondaryLogs(dir)
	if err != nil || n != 2 {
		t.Fatalf("fuseSecondaryLogs = %d, %v; want 2, nil", n, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "session.jsonl"))
	if string(data) != turnOwnerA+turnFC+turnDesktop {
		t.Errorf("session.jsonl after fuse:\n%s", data)
	}
	// Fusing again adds nothing.
	if n, err := fuseSecondaryLogs(dir); err != nil || n != 0 {
		t.Errorf("second fuse = %d, %v; want 0, nil", n, err)
	}
}

func TestReconcileFusesOnlyOnOwner(t *testing.T) {
	dir := newSessionDir(t, "provisioner01", map[string]string{
		"session.jsonl":           turnOwnerA,
		"session.desktop01.jsonl": turnDesktop,
	})
	if err := reconcileSessionLogs(dir, "some-other-host"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "session.jsonl")); string(data) != turnOwnerA {
		t.Errorf("a non-owner must not write session.jsonl:\n%s", data)
	}
	if err := reconcileSessionLogs(dir, "provisioner01"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "session.jsonl")); string(data) != turnOwnerA+turnDesktop {
		t.Errorf("the owner should fuse:\n%s", data)
	}
}

func TestPruneSecondaryLogOnNonOwner(t *testing.T) {
	dir := newSessionDir(t, "provisioner01", map[string]string{
		"session.jsonl":           turnOwnerA + turnFC,
		"session.desktop01.jsonl": turnFC + turnDesktop,
	})
	if err := pruneSecondaryLog(dir, "desktop01"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "session.desktop01.jsonl")); string(data) != turnDesktop {
		t.Errorf("secondary log after prune = %q, want only the turn not yet back", data)
	}

	// Once the owner's log has everything, the secondary log goes away.
	os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(turnOwnerA+turnFC+turnDesktop), 0644)
	if err := pruneSecondaryLog(dir, "desktop01"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "session.desktop01.jsonl")); !os.IsNotExist(err) {
		t.Error("an empty secondary log should be removed")
	}
}

func TestIsRecordIDAcceptsHostTag(t *testing.T) {
	for in, want := range map[string]bool{
		"1791912169":                true,
		"1791912169-desktop01-itjf": true,
		"1791912169-":               false,
		"1791912169-a/b":            false,
		"1791912169-..":             false,
		"x1791912169":               false,
	} {
		if got := isRecordID(in); got != want {
			t.Errorf("isRecordID(%q) = %v, want %v", in, got, want)
		}
	}
	if got := recordIDFromPath("/r/s/1791912169-desktop01-itjf-raw.txt"); got != "1791912169-desktop01-itjf" {
		t.Errorf("recordIDFromPath(host-tagged) = %q", got)
	}
}
