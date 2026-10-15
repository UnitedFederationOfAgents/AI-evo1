package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// recvNotesResult pops one queued message off c and decodes it as a
// "notes-result" payload.
func recvNotesResult(t *testing.T, c *wsClient) NotesResultMsg {
	t.Helper()
	select {
	case raw := <-c.send:
		var m wsMsg
		if err := json.Unmarshal(raw, &m); err != nil || m.Type != "notes-result" {
			t.Fatalf("unexpected message %s (err %v)", raw, err)
		}
		var p NotesResultMsg
		json.Unmarshal(m.Payload, &p)
		return p
	default:
		t.Fatal("no notes-result queued")
		return NotesResultMsg{}
	}
}

// TestAppendNotesAppends verifies successive appends land one after another,
// each ending on its own line, and that the file is created if missing.
func TestAppendNotesAppends(t *testing.T) {
	s := newServer()
	s.notesFile = filepath.Join(t.TempDir(), "notes-input.txt")
	c := newTestClient(s)

	s.handleAppendNotes(c, "notepad", "first note")
	if p := recvNotesResult(t, c); !p.Success || p.Source != "notepad" || p.Path != s.notesFile {
		t.Fatalf("got %+v, want success for notepad at %s", p, s.notesFile)
	}
	s.handleAppendNotes(c, "transcript", "second\n")
	if p := recvNotesResult(t, c); !p.Success || p.Source != "transcript" {
		t.Fatalf("got %+v, want success for transcript", p)
	}

	got, err := os.ReadFile(s.notesFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := "first note\nsecond\n"; string(got) != want {
		t.Fatalf("notes file = %q, want %q", got, want)
	}
}

// TestAppendNotesRejectsEmpty verifies blank text is refused and nothing is
// written.
func TestAppendNotesRejectsEmpty(t *testing.T) {
	s := newServer()
	s.notesFile = filepath.Join(t.TempDir(), "notes-input.txt")
	c := newTestClient(s)

	s.handleAppendNotes(c, "notepad", "  \n")
	if p := recvNotesResult(t, c); p.Success || p.Error == "" {
		t.Fatalf("got %+v, want an error", p)
	}
	if _, err := os.Stat(s.notesFile); !os.IsNotExist(err) {
		t.Fatalf("notes file should not exist, stat err = %v", err)
	}
}
