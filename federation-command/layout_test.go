package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacySessions(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, files ...string) {
		t.Helper()
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("with-log", "session.jsonl")
	mk("with-yaml", "session.yaml")
	mk("not-a-session", "notes.txt")
	mk("dtt-calls", "session.yaml") // reserved: never a session
	mk("clash", "session.jsonl")
	mk(filepath.Join(sessionsDirName, "clash"), "session.jsonl")

	moved, err := migrateLegacySessions(root)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 2 {
		t.Errorf("moved = %d, want 2", moved)
	}
	for _, id := range []string{"with-log", "with-yaml"} {
		if _, err := os.Stat(filepath.Join(sessionsRoot(root), id)); err != nil {
			t.Errorf("%s not in sessions/: %v", id, err)
		}
		if _, err := os.Stat(filepath.Join(root, id)); !os.IsNotExist(err) {
			t.Errorf("%s still under the root", id)
		}
	}
	for _, rel := range []string{"not-a-session", "dtt-calls", "clash"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s should have been left alone: %v", rel, err)
		}
	}

	// Nothing left to do; a missing root is fine too.
	if moved, err := migrateLegacySessions(root); err != nil || moved != 0 {
		t.Errorf("second run: moved %d, err %v", moved, err)
	}
	if moved, err := migrateLegacySessions(filepath.Join(root, "missing")); err != nil || moved != 0 {
		t.Errorf("missing root: moved %d, err %v", moved, err)
	}
}
