package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacySessions(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, file string) {
		t.Helper()
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte("x\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mk("legacy", "session.jsonl")
	mk("plain-dir", "notes.txt")
	mk("invocations", "session.yaml") // reserved: never a session

	moved, err := migrateLegacySessions(root)
	if err != nil || moved != 1 {
		t.Fatalf("moved %d, err %v; want 1, nil", moved, err)
	}
	if _, err := os.Stat(filepath.Join(sessionsRoot(root), "legacy", "session.jsonl")); err != nil {
		t.Errorf("legacy session not moved into sessions/: %v", err)
	}
	for _, rel := range []string{"plain-dir", "invocations"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s should have been left alone: %v", rel, err)
		}
	}
}

func TestPrepareRecordsForAgentReadsSessionsRoot(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(sessionsRoot(root), "s1")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "session.yaml"), []byte("id: s1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tmp, err := prepareRecordsForAgent([]string{"default"}, root, "s1")
	if tmp != "" {
		defer os.RemoveAll(tmp)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "s1", "session.yaml")); err != nil {
		t.Errorf("session s1 not copied from sessions/: %v", err)
	}
}
