package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDailyDefaultDay(t *testing.T) {
	cases := []struct {
		id  string
		day string
		ok  bool
	}{
		{"2026-10-06-box-a1b2-default", "2026-10-06", true},
		{"2026-10-06-default", "2026-10-06", true},
		{"2026-10-06_12-00-00_my-session", "", false},
		{"not-a-date-default", "", false},
		{"default", "", false},
	}
	for _, c := range cases {
		day, ok := dailyDefaultDay(c.id)
		if day != c.day || ok != c.ok {
			t.Errorf("dailyDefaultDay(%q) = %q, %v; want %q, %v", c.id, day, ok, c.day, c.ok)
		}
	}
}

func TestStaleDailyDefault(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local)
	if !staleDailyDefault("2026-10-11-box-a1b2-default", now) {
		t.Error("yesterday's default should be stale")
	}
	if staleDailyDefault("2026-10-12-box-a1b2-default", now) {
		t.Error("today's default should not be stale")
	}
	if staleDailyDefault("2026-10-11_09-00-00_named", now) {
		t.Error("a named session is never a stale default")
	}
}

func TestParseFollowsFlag(t *testing.T) {
	cases := []struct {
		args, follows, rest string
		wantErr             bool
	}{
		{"", "", "", false},
		{" My Session", "", "My Session", false},
		{" -f abc My Session", "abc", "My Session", false},
		{` -f "Robot work" Part 2`, "Robot work", "Part 2", false},
		{" -f abc", "abc", "", false},
		{" -f", "", "", true},
		{` -f "unterminated`, "", "", true},
		{" -fancy name", "", "-fancy name", false},
	}
	for _, c := range cases {
		follows, rest, err := parseFollowsFlag(c.args)
		if (err != nil) != c.wantErr || follows != c.follows || rest != c.rest {
			t.Errorf("parseFollowsFlag(%q) = %q, %q, %v; want %q, %q, err=%v", c.args, follows, rest, err, c.follows, c.rest, c.wantErr)
		}
	}
}

func TestResolveSessionRefAndFollowsFrom(t *testing.T) {
	dir := t.TempDir()
	prev := filepath.Join(dir, "2026-10-11_10-00-00_robot-work")
	if err := os.MkdirAll(prev, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prev, "session.yaml"), []byte("id: 2026-10-11_10-00-00_robot-work\nname: Robot work\n"), 0644); err != nil {
		t.Fatal(err)
	}

	for _, ref := range []string{"2026-10-11_10-00-00_robot-work", "Robot work"} {
		if id, err := resolveSessionRef(dir, ref); err != nil || id != "2026-10-11_10-00-00_robot-work" {
			t.Errorf("resolveSessionRef(%q) = %q, %v", ref, id, err)
		}
	}
	if _, err := resolveSessionRef(dir, "nope"); err == nil {
		t.Error("expected an error for an unknown session")
	}
	if _, err := resolveSessionRef(dir, "../x"); err == nil {
		t.Error("expected an error for a path-like ref")
	}

	next := filepath.Join(dir, "next")
	if err := os.MkdirAll(next, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(next, "session.yaml"), []byte("id: next\nname: Next\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := applyFollowsFrom(dir, "next", "2026-10-11_10-00-00_robot-work"); err != nil {
		t.Fatal(err)
	}
	if got := readSessionYAMLField(next, sessionFollowsFromKey); got != "2026-10-11_10-00-00_robot-work" {
		t.Errorf("follows_from = %q", got)
	}
	if got := readSessionYAMLField(next, "name"); got != "Next" {
		t.Errorf("name clobbered: %q", got)
	}
}

func TestRolloverDailyDefault(t *testing.T) {
	prevHost := fcHostID
	fcHostID = "box-a1b2"
	defer func() { fcHostID = prevHost }()
	// Force the fallback path so the test doesn't depend on a clauditable binary.
	t.Setenv("PATH", t.TempDir())

	dir := t.TempDir()
	now := time.Now()
	oldID := fallbackDefaultSessionID(now.AddDate(0, 0, -1), fcHostID)
	oldDir := filepath.Join(dir, oldID)
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(oldDir, "session.jsonl"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	m := appModel{sessionID: oldID, sessionDir: oldDir, recordsPath: dir, logFile: logFile, hadActivity: true}

	m2, notice := m.rolloverDailyDefault(now)
	defer m2.logFile.Close()
	wantID := fallbackDefaultSessionID(now, fcHostID)
	if m2.sessionID != wantID || notice == "" {
		t.Fatalf("rollover: session %q (notice %q), want %q", m2.sessionID, notice, wantID)
	}
	if m2.hadActivity {
		t.Error("hadActivity should reset after a rollover")
	}
	if got := readSessionYAMLField(filepath.Join(dir, wantID), sessionFollowsFromKey); got != oldID {
		t.Errorf("follows_from = %q, want %q", got, oldID)
	}

	// Already on today's default: nothing to do.
	if m3, notice := m2.rolloverDailyDefault(now); notice != "" || m3.sessionID != wantID {
		t.Errorf("unexpected second rollover to %q (%q)", m3.sessionID, notice)
	}
}

func TestRolloverAfterIdleDaysSkipsIntermediateDays(t *testing.T) {
	prevHost := fcHostID
	fcHostID = "box-a1b2"
	defer func() { fcHostID = prevHost }()
	t.Setenv("PATH", t.TempDir())

	dir := t.TempDir()
	now := time.Now()
	oldID := fallbackDefaultSessionID(now.AddDate(0, 0, -3), fcHostID)
	oldDir := filepath.Join(dir, oldID)
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(oldDir, "session.jsonl"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	m := appModel{sessionID: oldID, sessionDir: oldDir, recordsPath: dir, logFile: logFile, hadActivity: true}
	m2, _ := m.rolloverDailyDefault(now)
	defer m2.logFile.Close()

	if want := fallbackDefaultSessionID(now, fcHostID); m2.sessionID != want {
		t.Fatalf("session %q, want %q", m2.sessionID, want)
	}
	if got := readSessionYAMLField(m2.sessionDir, sessionFollowsFromKey); got != oldID {
		t.Errorf("follows_from = %q, want %q", got, oldID)
	}
	for _, d := range []int{-2, -1} {
		if _, err := os.Stat(filepath.Join(dir, fallbackDefaultSessionID(now.AddDate(0, 0, d), fcHostID))); !os.IsNotExist(err) {
			t.Errorf("intermediate day %d's default was created", d)
		}
	}
}

func TestRolloverWithoutActivityLeavesNoFollowsFrom(t *testing.T) {
	prevHost := fcHostID
	fcHostID = "box-a1b2"
	defer func() { fcHostID = prevHost }()
	t.Setenv("PATH", t.TempDir())

	dir := t.TempDir()
	now := time.Now()
	oldID := fallbackDefaultSessionID(now.AddDate(0, 0, -1), fcHostID)
	oldDir := filepath.Join(dir, oldID)
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(oldDir, "session.jsonl"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	m := appModel{sessionID: oldID, sessionDir: oldDir, recordsPath: dir, logFile: logFile}
	m2, _ := m.rolloverDailyDefault(now)
	defer m2.logFile.Close()
	if got := readSessionYAMLField(m2.sessionDir, sessionFollowsFromKey); got != "" {
		t.Errorf("follows_from = %q, want none for an idle instance", got)
	}
}
