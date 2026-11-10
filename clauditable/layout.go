package main

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"

	"clauditable/pkg/records"
)

// The agent-records layout. AGENT_RECORDS_PATH is the records ROOT; under it:
//
//	<root>/sessions/<session-id>/   every session directory
//	<root>/dtt-calls/<file>         a copy of each dtt_call record (see writeRecordCopy)
//	<root>/invocations/<file>       a copy of each ai_invocation record
//
// Sessions used to live directly under the root (<root>/<session-id>/);
// migrateLegacySessions moves them. The three names are reserved: nothing
// that walks the root may ever treat them as sessions. Other components keep
// mirrored copies of these constants (e.g. local-representative/sessions.go).
const (
	sessionsDirName    = "sessions"
	dttCallsDirName    = "dtt-calls"
	invocationsDirName = "invocations"
)

// sessionsRoot is where session directories live under the records root.
func sessionsRoot(recordsPath string) string { return filepath.Join(recordsPath, sessionsDirName) }

// isReservedRecordsName reports whether name is one of the layout's own
// directories under the records root rather than a (legacy) session.
func isReservedRecordsName(name string) bool {
	switch name {
	case sessionsDirName, dttCallsDirName, invocationsDirName:
		return true
	}
	return false
}

// isLegacySessionDir reports whether dir looks like a session directory: it
// holds session.jsonl, or session.yaml (a session that has had no command yet
// only has that).
func isLegacySessionDir(dir string) bool {
	for _, marker := range []string{sessionLogName, "session.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// migrateLegacySessions moves session directories still directly under the
// records root into sessionsRoot (clauditable's migrate-sessions). It is
// cheap when there's nothing to do (one ReadDir), so every recording
// invocation runs it. A session whose sessions/<name> already exists is left
// where it is, with a warning -- the two are never merged. A rename that
// fails because another process already moved the directory is fine.
// Returns how many directories this call moved.
func migrateLegacySessions(recordsPath string) (moved int, err error) {
	entries, err := os.ReadDir(recordsPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	root := sessionsRoot(recordsPath)
	var errs []error
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || isReservedRecordsName(name) {
			continue
		}
		src := filepath.Join(recordsPath, name)
		if !isLegacySessionDir(src) {
			continue
		}
		if err := os.MkdirAll(root, 0755); err != nil {
			return moved, fmt.Errorf("creating %s: %w", root, err)
		}
		dst := filepath.Join(root, name)
		// os.Rename would happily replace an empty directory at dst, so check
		// first rather than rely on it failing.
		if _, statErr := os.Lstat(dst); statErr == nil {
			fmt.Fprintf(os.Stderr, "clauditable: warning: legacy session %s not migrated: %s already exists\n", src, dst)
			continue
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			errs = append(errs, statErr)
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // another process moved it first
			}
			errs = append(errs, fmt.Errorf("moving %s: %w", src, err))
			continue
		}
		moved++
	}
	return moved, errors.Join(errs...)
}

// migrateLegacySessionsOrWarn runs migrateLegacySessions best-effort: an error
// is only a warning on stderr.
func migrateLegacySessionsOrWarn(recordsPath string) {
	if _, err := migrateLegacySessions(recordsPath); err != nil {
		fmt.Fprintf(os.Stderr, "clauditable: warning: migrating legacy sessions: %v\n", err)
	}
}

// runMigrateSessions is "clauditable migrate-sessions": migrate now and say
// how many sessions were moved.
func runMigrateSessions() int {
	recordsPath := getEnvOrDefault(EnvAgentRecordsPath, DefaultRecordsPath)
	moved, err := migrateLegacySessions(recordsPath)
	fmt.Printf("Migrated %d session(s) into %s\n", moved, sessionsRoot(recordsPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "clauditable migrate-sessions: %v\n", err)
		return 1
	}
	return 0
}

// recordCopyDir is the directory under the records root that gets a copy of
// a record of eventType, or "" when that kind of record isn't copied (plain
// commands, notes, resources).
func recordCopyDir(eventType string) string {
	switch eventType {
	case records.EventTypeDTTCall:
		return dttCallsDirName
	case records.EventTypeAIInvocation:
		return invocationsDirName
	}
	return ""
}

const copyNameChars = "abcdefghijklmnopqrstuvwxyz0123456789"

// randomCopySuffix returns n random characters from [a-z0-9].
func randomCopySuffix(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = copyNameChars[rand.IntN(len(copyNameChars))]
	}
	return string(b)
}

// writeRecordCopy writes a copy of record into <recordsPath>/<dirName>/ as
// "<ts>-<rrr>.txt" (ts: the record's start time in unix seconds, rrr: three
// random [a-z0-9] characters, re-picked on a clash). The content is the same
// full written-file format as the session record, with the copy's Event
// naming the session, the session record's file (sessionRecord, e.g.
// 1793101234-raw.txt) and its own path. record is taken by value so the
// caller's record is left as it was. Returns the copy's path.
func writeRecordCopy(recordsPath, dirName, session, sessionRecord string, ts int64, record records.Record) (string, error) {
	dir := filepath.Join(recordsPath, dirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	for attempt := 0; attempt < 50; attempt++ {
		path := filepath.Join(dir, fmt.Sprintf("%d-%s.txt", ts, randomCopySuffix(3)))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return "", err
		}
		record.Event.Session = session
		record.Event.SessionRecord = sessionRecord
		record.Event.RecordPath = path
		_, werr := f.WriteString(record.FormatWrittenFile())
		cerr := f.Close()
		if werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(path)
			return "", werr
		}
		return path, nil
	}
	return "", fmt.Errorf("no free file name for %d in %s", ts, dir)
}
