package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	ufahostid "ufa-hostid"
)

// Session ownership. A session's session.yaml names its owner -- the host
// that created it (see writeSessionYAMLIfAbsent). Only the owner ever writes
// session.jsonl. Any other host appends its entries to its own secondary log,
// session.<host>.jsonl, and names its record files {ts}-<host>-raw.txt etc.
// so they can't collide with the owner's {ts}-raw.txt. session-manager splices
// the secondary logs into the transcript it shows; on the owner it also fuses
// them into session.jsonl, and on a non-owner it drops the entries that have
// since come back in the owner's session.jsonl (see
// session-manager/sessionlog.go). federation-command keeps its own copy of
// these rules (federation-command/sessionlog.go).
//
// A session with no session.yaml at all has no known owner, so it is treated
// as someone else's: writing there as the owner is what forked a session
// before (a set-session on another host created an empty copy whose
// session.yaml then overwrote the owner's).

const sessionLogName = "session.jsonl"

// readSessionOwner returns session.yaml's owner field, and whether
// session.yaml exists at all.
func readSessionOwner(sessionDir string) (owner string, hasYAML bool) {
	data, err := os.ReadFile(filepath.Join(sessionDir, "session.yaml"))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "owner: "); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", true
}

// isSessionOwner reports whether hostID owns the session in sessionDir. A
// session.yaml with no owner field predates ownership and belongs to
// whichever host has it.
func isSessionOwner(sessionDir, hostID string) bool {
	owner, hasYAML := readSessionOwner(sessionDir)
	return hasYAML && (owner == "" || owner == hostID)
}

// hostFileTag makes hostID safe to embed in a record file name. A trailing
// "-s" gets a "_" appended so "{ts}-<host>-raw.txt" can never be mistaken for
// a local secondary's "-s-raw.txt".
func hostFileTag(hostID string) string {
	tag := sanitizeHostIDForSession(hostID)
	if strings.HasSuffix(tag, "-s") {
		tag += "_"
	}
	return tag
}

// secondaryLogName is the log a non-owner host appends to.
func secondaryLogName(hostID string) string {
	return "session." + hostFileTag(hostID) + ".jsonl"
}

// sessionWriteTarget is where this host writes in a session: the log to
// append entries to, and the tag that goes between a record's timestamp and
// its suffix ("" on the owner, "-<host>" elsewhere).
type sessionWriteTarget struct {
	logPath string
	tag     string
}

// writeTargetFor resolves sessionWriteTarget for this host.
func writeTargetFor(sessionDir string) sessionWriteTarget {
	host := ufahostid.GetHostID()
	if isSessionOwner(sessionDir, host) {
		return sessionWriteTarget{logPath: filepath.Join(sessionDir, sessionLogName)}
	}
	return sessionWriteTarget{
		logPath: filepath.Join(sessionDir, secondaryLogName(host)),
		tag:     "-" + hostFileTag(host),
	}
}

// recordPrefix is the name every file of one record starts with.
func recordPrefix(ts int64, tag string) string {
	return fmt.Sprintf("%d%s", ts, tag)
}

// appendSessionLog appends data to a session log in one write, holding an
// exclusive flock so it can't interleave with another writer or with
// session-manager fusing or pruning the same file. If the file was replaced
// or removed while we waited for the lock, it reopens and tries again.
func appendSessionLog(path string, data []byte) error {
	for attempt := 0; attempt < 5; attempt++ {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return err
		}
		held, err1 := f.Stat()
		current, err2 := os.Stat(path)
		if err1 == nil && err2 == nil && os.SameFile(held, current) {
			_, werr := f.Write(data)
			cerr := f.Close()
			if werr != nil {
				return werr
			}
			return cerr
		}
		f.Close()
	}
	return fmt.Errorf("%s kept being replaced while appending", path)
}
