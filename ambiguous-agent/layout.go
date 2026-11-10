package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The agent-records layout, mirroring clauditable/layout.go.
// AGENT_RECORDS_PATH is the records ROOT; under it:
//
//	<root>/sessions/<session-id>/   every session directory
//	<root>/dtt-calls/<file>         clauditable's copy of each dtt_call record
//	<root>/invocations/<file>       clauditable's copy of each ai_invocation record
//
// Sessions used to live directly under the root; migrateLegacySessions moves
// them. The three names are reserved and never treated as sessions.
const (
	sessionsDirName    = "sessions"
	dttCallsDirName    = "dtt-calls"
	invocationsDirName = "invocations"
)

// sessionLogName is the session log whose presence marks a session directory.
const sessionLogName = "session.jsonl"

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
// records root into sessionsRoot (clauditable's migrate-sessions). A session
// whose sessions/<name> already exists is left where it is, with a warning --
// the two are never merged. A rename that fails because another process
// already moved the directory is fine. Returns how many directories this call
// moved.
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
			fmt.Fprintf(os.Stderr, "ambiguous-agent: warning: legacy session %s not migrated: %s already exists\n", src, dst)
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
