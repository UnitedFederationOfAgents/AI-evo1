package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withHostID(t *testing.T, id string) {
	t.Helper()
	prev := fcHostID
	fcHostID = id
	t.Cleanup(func() { fcHostID = prev })
}

func writeSessionYAML(t *testing.T, dir, owner string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "id: " + filepath.Base(dir) + "\nname: N\nowner: " + owner + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// federation-command's own records go to session.jsonl only on the owner;
// elsewhere they go to the host's secondary log.
func TestSessionEncoderRoutesByOwner(t *testing.T) {
	withHostID(t, "box-a")
	root := t.TempDir()

	owned := filepath.Join(root, "owned")
	writeSessionYAML(t, owned, "box-a")
	if err := sessionEncoder(owned).Encode(CommandRecord{ID: "1", Command: "ls"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(owned, "session.jsonl")); !strings.Contains(string(data), `"cmd":"ls"`) {
		t.Errorf("owner's session.jsonl = %q", data)
	}

	theirs := filepath.Join(root, "theirs")
	writeSessionYAML(t, theirs, "box-b")
	if err := sessionEncoder(theirs).Encode(CommandRecord{ID: "2", Command: "pwd"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(theirs, "session.jsonl")); !os.IsNotExist(err) {
		t.Error("a non-owner must not write session.jsonl")
	}
	if data, _ := os.ReadFile(filepath.Join(theirs, "session.box-a.jsonl")); !strings.Contains(string(data), `"cmd":"pwd"`) {
		t.Errorf("secondary log = %q", data)
	}
}

// set-session on an ID that's nowhere to be found must not create anything
// without confirmation.
func TestHandleSetSessionNeverCreatesSilently(t *testing.T) {
	withHostID(t, "box-a")
	root := t.TempDir()
	cur := filepath.Join(root, "current")
	writeSessionYAML(t, cur, "box-a")
	m := appModel{recordsPath: root, sessionID: "current", sessionDir: cur, encoder: sessionEncoder(cur)}

	m2, cmd := m.handleSetSession("2026-10-13_14-30-48_robot-work", "set-session", "set-session 2026-10-13_14-30-48_robot-work", time.Now(), 0)
	if cmd == nil {
		t.Fatal("expected a confirmation prompt")
	}
	if m2.sessionID != "current" {
		t.Errorf("switched to %q before confirmation", m2.sessionID)
	}
	if _, err := os.Stat(filepath.Join(root, "2026-10-13_14-30-48_robot-work")); !os.IsNotExist(err) {
		t.Error("the session directory must not be created before confirmation")
	}
}

func TestHandleSetSessionJoinsExisting(t *testing.T) {
	withHostID(t, "box-a")
	root := t.TempDir()
	cur := filepath.Join(root, "current")
	writeSessionYAML(t, cur, "box-a")
	writeSessionYAML(t, filepath.Join(root, "theirs"), "box-b")
	m := appModel{recordsPath: root, sessionID: "current", sessionDir: cur, encoder: sessionEncoder(cur)}

	m2, _ := m.handleSetSession("theirs", "set-session", "set-session theirs", time.Now(), 0)
	if m2.sessionID != "theirs" {
		t.Fatalf("sessionID = %q, want theirs", m2.sessionID)
	}
	// The set-session record itself lands in this host's secondary log.
	if _, err := os.Stat(filepath.Join(root, "theirs", "session.box-a.jsonl")); err != nil {
		t.Errorf("expected the record in the secondary log: %v", err)
	}
}

func TestFinishSessionCreateConfirm(t *testing.T) {
	withHostID(t, "box-a")
	root := t.TempDir()
	cur := filepath.Join(root, "current")
	writeSessionYAML(t, cur, "box-a")
	m := appModel{recordsPath: root, sessionID: "current", sessionDir: cur, encoder: sessionEncoder(cur), blinker: NewBlinker(false)}

	m2, _ := m.finishSessionCreateConfirm(sessionCreateConfirmMsg{id: "fresh", confirmed: false, line: "set-session fresh"})
	if m2.sessionID != "current" {
		t.Error("declining must not switch")
	}
	if _, err := os.Stat(filepath.Join(root, "fresh")); !os.IsNotExist(err) {
		t.Error("declining must not create the session")
	}

	m3, _ := m.finishSessionCreateConfirm(sessionCreateConfirmMsg{id: "fresh", confirmed: true, line: "set-session fresh"})
	if m3.sessionID != "fresh" {
		t.Fatalf("sessionID = %q, want fresh", m3.sessionID)
	}
	if owner, _ := readSessionOwner(filepath.Join(root, "fresh")); owner != "box-a" {
		t.Errorf("owner = %q, want box-a", owner)
	}
}

func TestBuildCreateSessionConfirmScriptWarnsAboutOldIDs(t *testing.T) {
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.Local)
	script := buildCreateSessionConfirmScript("2026-10-13_14-30-48_robot-work", true, now)
	if !strings.Contains(script, "most likely already exists") {
		t.Errorf("an ID with a past timestamp should warn:\n%s", script)
	}
	script = buildCreateSessionConfirmScript("my-session", false, now)
	if !strings.Contains(script, "Not every other host could be checked") {
		t.Errorf("an incomplete check should warn:\n%s", script)
	}
	if strings.Contains(buildCreateSessionConfirmScript("my-session", true, now), "separate session") {
		t.Error("no warning expected for a fresh name with every host checked")
	}
}

func TestValidSessionIDArg(t *testing.T) {
	for id, want := range map[string]bool{"abc": true, "": false, "..": false, "a/b": false, ".hidden": false} {
		if got := validSessionIDArg(id); got != want {
			t.Errorf("validSessionIDArg(%q) = %v, want %v", id, got, want)
		}
	}
}
