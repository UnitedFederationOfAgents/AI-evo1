package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clauditable/pkg/records"
	ufahostid "ufa-hostid"
)

// ownerTarget is the sessionWriteTarget of the session's owner.
func ownerTarget(recordsPath, session string) sessionWriteTarget {
	return sessionWriteTarget{logPath: filepath.Join(recordsPath, session, sessionLogName)}
}

func writeYAML(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestIsSessionOwner(t *testing.T) {
	root := t.TempDir()

	missing := filepath.Join(root, "missing")
	os.MkdirAll(missing, 0755)
	if isSessionOwner(missing, "me") {
		t.Error("a session with no session.yaml has no known owner and must not be treated as ours")
	}

	legacy := filepath.Join(root, "legacy")
	writeYAML(t, legacy, "id: legacy\nname: Legacy\n")
	if !isSessionOwner(legacy, "me") {
		t.Error("a session.yaml without an owner field belongs to whichever host has it")
	}

	mine := filepath.Join(root, "mine")
	writeYAML(t, mine, "id: mine\nname: Mine\nowner: me\ncreated: x\n")
	if !isSessionOwner(mine, "me") {
		t.Error("owner: me should be ours")
	}
	if isSessionOwner(mine, "other") {
		t.Error("owner: me should not belong to other")
	}
}

func TestWriteTargetFor(t *testing.T) {
	root := t.TempDir()
	host := ufahostid.GetHostID()

	owned := filepath.Join(root, "owned")
	writeYAML(t, owned, "id: owned\nowner: "+host+"\n")
	if got := writeTargetFor(owned); got.tag != "" || filepath.Base(got.logPath) != "session.jsonl" {
		t.Errorf("owner target = %+v, want session.jsonl with no tag", got)
	}

	theirs := filepath.Join(root, "theirs")
	writeYAML(t, theirs, "id: theirs\nowner: someone-else\n")
	got := writeTargetFor(theirs)
	if got.tag != "-"+hostFileTag(host) || filepath.Base(got.logPath) != secondaryLogName(host) {
		t.Errorf("non-owner target = %+v, want tag -%s and %s", got, hostFileTag(host), secondaryLogName(host))
	}
}

func TestHostFileTag(t *testing.T) {
	tests := map[string]string{
		"desktop01-itjf": "desktop01-itjf",
		"a/b c":          "a-b-c",
		"box-s":          "box-s_",
	}
	for in, want := range tests {
		if got := hostFileTag(in); got != want {
			t.Errorf("hostFileTag(%q) = %q, want %q", in, got, want)
		}
	}
	if got := secondaryLogName("desktop01-itjf"); got != "session.desktop01-itjf.jsonl" {
		t.Errorf("secondaryLogName = %q", got)
	}
}

func TestRecordTimestamp(t *testing.T) {
	tests := []struct {
		name, suffix string
		ts           int64
		ok           bool
	}{
		{"1000-writing.txt", "-writing.txt", 1000, true},
		{"1000-desktop01-itjf-writing.txt", "-writing.txt", 1000, true},
		{"1000--writing.txt", "-writing.txt", 0, false},
		{"abc-writing.txt", "-writing.txt", 0, false},
		{"1000-raw.txt", "-writing.txt", 0, false},
	}
	for _, tt := range tests {
		ts, ok := recordTimestamp(tt.name, tt.suffix)
		if ts != tt.ts || ok != tt.ok {
			t.Errorf("recordTimestamp(%q, %q) = %d, %v; want %d, %v", tt.name, tt.suffix, ts, ok, tt.ts, tt.ok)
		}
	}
}

// Host-tagged writing files on a non-owner take part in primary detection
// the same way untagged ones do on the owner.
func TestCheckIsPrimaryWithHostTag(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "1000-box-writing.txt"), nil, 0644)
	if !checkIsPrimary(dir, 1000) {
		t.Error("only our own writing file: should be primary")
	}
	os.WriteFile(filepath.Join(dir, "900-box-writing.txt"), nil, 0644)
	if checkIsPrimary(dir, 1000) {
		t.Error("an older tagged writing file should make us secondary")
	}
}

// On a non-owner, consolidation appends to the host's secondary log, folds
// and promotes only this host's tagged secondaries, and never touches
// session.jsonl.
func TestConsolidateOnNonOwner(t *testing.T) {
	tmpDir := t.TempDir()
	session := "s1"
	sessionDir := filepath.Join(tmpDir, session)
	os.MkdirAll(sessionDir, 0755)
	target := sessionWriteTarget{logPath: filepath.Join(sessionDir, "session.box.jsonl"), tag: "-box"}

	secondary := &records.Record{
		Event:   records.Event{Timestamp: "2026-01-15T10:00:00Z", EventType: "command_execution"},
		Command: "echo secondary",
	}
	if _, err := writeWrittenFile(sessionDir, 1705312800, target.tag, false, secondary); err != nil {
		t.Fatal(err)
	}
	primary := &records.Record{
		Event:   records.Event{Timestamp: "2026-01-15T10:01:00Z", EventType: "command_execution"},
		Command: "echo primary",
	}
	if _, err := writeWrittenFile(sessionDir, 1705312860, target.tag, true, primary); err != nil {
		t.Fatal(err)
	}
	if err := consolidatePrimaryToJSONL(tmpDir, session, 1705312860, target); err != nil {
		t.Fatalf("consolidatePrimaryToJSONL: %v", err)
	}

	log, err := os.ReadFile(target.logPath)
	if err != nil {
		t.Fatalf("reading secondary log: %v", err)
	}
	if !strings.Contains(string(log), "echo secondary") || !strings.Contains(string(log), "echo primary") {
		t.Errorf("secondary log missing entries:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "session.jsonl")); !os.IsNotExist(err) {
		t.Error("a non-owner must not write session.jsonl")
	}
	for _, name := range []string{"1705312800-box-raw.txt", "1705312800-box-processed.txt", "1705312860-box-raw.txt", "1705312860-box-processed.txt"} {
		if _, err := os.Stat(filepath.Join(sessionDir, name)); err != nil {
			t.Errorf("expected %s: %v", name, err)
		}
	}
}

func TestAppendSessionLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := appendSessionLog(path, []byte("a\n")); err != nil {
		t.Fatal(err)
	}
	if err := appendSessionLog(path, []byte("b\n")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "a\nb\n" {
		t.Errorf("log = %q, want %q", data, "a\nb\n")
	}
}
