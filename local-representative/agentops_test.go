package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentOpsWatch(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sess1")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "session.yaml"), []byte("id: sess1\nname: My session\n"), 0644)
	write := func(name, content string) { os.WriteFile(filepath.Join(dir, name), []byte(content), 0644) }
	write("1000-writing.txt", "1000 claude opus\nclaude -p hello\n")
	write("1001-s-writing.txt", "1001 none -\ngit status\n")
	write("1002-box-writing.txt", "1002\nold-clauditable\n")
	write("999-raw.txt", "done\n")

	notified := 0
	w := newAgentOpsWatch(root, func() { notified++ })
	now := time.Now()
	w.scan(now)
	ops := w.snapshot()
	if len(ops) != 2 || notified != 1 {
		t.Fatalf("first scan: %+v (notified %d)", ops, notified)
	}
	if o := ops[1]; o.ID != "sess1/1000" || o.Agent != "claude" || o.Model != "opus" || o.Command != "claude -p hello" ||
		o.StartedAt != 1000 || o.SessionName != "My session" || o.EndedAt != 0 {
		t.Errorf("claude op: %+v", o)
	}
	if o := ops[0]; o.ID != "sess1/1002-box" || o.Agent != "" || o.Command != "old-clauditable" {
		t.Errorf("legacy op: %+v", o)
	}

	// A secondary's rename to -s-writing.txt is the same call.
	os.Rename(filepath.Join(dir, "1000-writing.txt"), filepath.Join(dir, "1000-s-writing.txt"))
	w.scan(now)
	if notified != 1 {
		t.Errorf("rename notified")
	}

	os.Remove(filepath.Join(dir, "1000-s-writing.txt"))
	w.scan(now)
	if o := w.snapshot()[1]; o.EndedAt != now.Unix() || notified != 2 {
		t.Errorf("after finishing: %+v (notified %d)", o, notified)
	}
	w.scan(now.Add(agentOpsHold + time.Second))
	if ops := w.snapshot(); len(ops) != 1 || ops[0].ID != "sess1/1002-box" {
		t.Errorf("after the hold: %+v", ops)
	}
}
