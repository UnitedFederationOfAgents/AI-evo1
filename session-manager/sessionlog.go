package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Primary and secondary session logs. Only a session's owner (session.yaml's
// owner field) writes session.jsonl; every other host appends to its own
// session.<host>.jsonl instead -- see clauditable/sessionlog.go, which
// federation-command mirrors. local-representative pulls session.jsonl to
// non-owners and each non-owner's secondary log to the owner (see
// local-representative/sessions.go's handleSessionsPull). This file is the
// rest of that:
//
//   - viewSession splices every secondary log into the transcript, marking
//     those turns with the host they came from.
//   - fuseSecondaryLogs (owner only) appends the secondary-log turns that
//     session.jsonl doesn't have yet.
//   - pruneSecondaryLog (non-owner only) drops the turns of this host's own
//     secondary log that have come back in the owner's session.jsonl, and
//     removes the file once it's empty -- so a secondary log comes and goes
//     as a host falls behind and catches up.
//
// A turn is identified by its header line (the JSON line it starts with):
// fusing copies turns verbatim, so the same turn has the same header line in
// every log.

const primaryLogName = "session.jsonl"

// readSessionOwner mirrors clauditable/sessionlog.go: session.yaml's owner
// field, and whether session.yaml exists.
func readSessionOwner(sessionDir string) (owner string, hasYAML bool) {
	for _, f := range readSessionYAMLFields(sessionDir) {
		if f[0] == "owner" {
			return strings.TrimSpace(f[1]), true
		}
	}
	_, err := os.Stat(filepath.Join(sessionDir, "session.yaml"))
	return "", err == nil
}

// isSessionOwner mirrors clauditable/sessionlog.go's rule: a session with no
// session.yaml isn't ours, one whose session.yaml has no owner field is.
func isSessionOwner(sessionDir, hostID string) bool {
	owner, hasYAML := readSessionOwner(sessionDir)
	return hasYAML && (owner == "" || owner == hostID)
}

// hostFileTag mirrors clauditable/sessionlog.go's function of the same name.
func hostFileTag(hostID string) string {
	var b strings.Builder
	for _, r := range hostID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	tag := strings.Trim(b.String(), ".-")
	if tag == "" {
		tag = "unknown"
	}
	if strings.HasSuffix(tag, "-s") {
		tag += "_"
	}
	return tag
}

// secondaryLogName is the log hostID appends to in a session it doesn't own.
func secondaryLogName(hostID string) string {
	return "session." + hostFileTag(hostID) + ".jsonl"
}

// secondaryLogHost returns the host tag in a secondary log's file name.
func secondaryLogHost(name string) (string, bool) {
	if name == primaryLogName {
		return "", false
	}
	mid, ok := strings.CutPrefix(name, "session.")
	if !ok {
		return "", false
	}
	tag, ok := strings.CutSuffix(mid, ".jsonl")
	return tag, ok && tag != ""
}

// secondaryLogs lists sessionDir's secondary logs, sorted by name.
func secondaryLogs(sessionDir string) []string {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if _, ok := secondaryLogHost(e.Name()); ok && !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// ---- turns as blocks of text ----

// logBlock is one turn's text in a session log: its header line and every
// line after it up to the next header.
type logBlock struct {
	header string
	text   string
	time   time.Time // zero when the header's timestamp doesn't parse
}

// entryHeader reports whether line starts a turn, mirroring parseSessionLog:
// a JSON object carrying a timestamp or event type.
func entryHeader(line string) (sessionLogEvent, bool) {
	var ev sessionLogEvent
	if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &ev) != nil {
		return ev, false
	}
	return ev, ev.Timestamp != "" || ev.EventType != ""
}

// fcRecord is the shape of federation-command's own one-line records
// (its CommandRecord): not shown as turns -- a wrapped command already has
// clauditable's record -- but they still have to move between logs whole.
type fcRecord struct {
	Cmd string `json:"cmd"`
	TS  string `json:"ts"`
}

// blockStart reports whether line starts a block of a session log -- a
// turn's header or a federation-command record -- and its timestamp.
func blockStart(line string) (time.Time, bool) {
	if ev, ok := entryHeader(line); ok {
		t, _ := time.Parse(time.RFC3339, ev.Timestamp)
		return t, true
	}
	var rec fcRecord
	if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &rec) == nil && rec.TS != "" && rec.Cmd != "" {
		t, _ := time.Parse(time.RFC3339, rec.TS)
		return t, true
	}
	return time.Time{}, false
}

// splitLogBlocks splits a session log into blocks (see blockStart). Text
// before the first block is dropped (parseSessionLog ignores it too). A last
// line with no newline may still be being written, so a block containing
// one is left out unless complete is set.
func splitLogBlocks(data []byte, complete bool) []logBlock {
	text := string(data)
	if !complete {
		if i := strings.LastIndexByte(text, '\n'); i < 0 {
			text = ""
		} else {
			text = text[:i+1]
		}
	}
	var blocks []logBlock
	var cur *logBlock
	var body strings.Builder
	flush := func() {
		if cur != nil {
			cur.text = body.String()
			blocks = append(blocks, *cur)
		}
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			continue
		}
		if t, ok := blockStart(strings.TrimRight(line, "\n")); ok {
			flush()
			cur = &logBlock{header: strings.TrimRight(line, "\n"), time: t}
			body.Reset()
		}
		if cur != nil {
			body.WriteString(line)
		}
	}
	flush()
	return blocks
}

// headerSet returns the header lines in data.
func headerSet(data []byte) map[string]bool {
	set := map[string]bool{}
	for _, b := range splitLogBlocks(data, true) {
		set[b.header] = true
	}
	return set
}

// ---- splicing (viewer) ----

// spliceSecondaryEntries merges the turns of every secondary log in
// sessionDir that primary doesn't already have into primary, each tagged
// with the host it came from. Primary's own order is kept; a secondary turn
// goes after the primary turns that aren't later than it.
func spliceSecondaryEntries(sessionDir string, primary []SessionEntry) []SessionEntry {
	seen := map[string]bool{}
	for _, e := range primary {
		seen[e.header] = true
	}
	var extra []SessionEntry
	for _, name := range secondaryLogs(sessionDir) {
		host, _ := secondaryLogHost(name)
		data, err := os.ReadFile(filepath.Join(sessionDir, name))
		if err != nil {
			continue
		}
		for _, e := range parseSessionLog(data) {
			if seen[e.header] {
				continue
			}
			seen[e.header] = true
			e.SecondaryHost = host
			extra = append(extra, e)
		}
	}
	if len(extra) == 0 {
		return primary
	}
	sort.SliceStable(extra, func(i, j int) bool { return entryTime(extra[i]).Before(entryTime(extra[j])) })

	merged := make([]SessionEntry, 0, len(primary)+len(extra))
	i, j := 0, 0
	for i < len(primary) && j < len(extra) {
		if entryTime(extra[j]).Before(entryTime(primary[i])) {
			merged = append(merged, extra[j])
			j++
		} else {
			merged = append(merged, primary[i])
			i++
		}
	}
	merged = append(merged, primary[i:]...)
	return append(merged, extra[j:]...)
}

// entryTime is e's timestamp; one that doesn't parse sorts last.
func entryTime(e SessionEntry) time.Time {
	t, err := time.Parse(time.RFC3339, e.Timestamp)
	if err != nil {
		return time.Unix(1<<62, 0)
	}
	return t
}

// ---- fusing (owner) and pruning (non-owner) ----

// reconcileSessionLogs runs whichever of fuseSecondaryLogs/pruneSecondaryLog
// applies to hostID in sessionDir. Errors are logged by the caller, never
// fatal: the viewer splices regardless.
func reconcileSessionLogs(sessionDir, hostID string) error {
	if isSessionOwner(sessionDir, hostID) {
		_, err := fuseSecondaryLogs(sessionDir)
		return err
	}
	return pruneSecondaryLog(sessionDir, hostID)
}

// fuseSecondaryLogs appends to session.jsonl every complete turn from the
// secondary logs in sessionDir that it doesn't have yet, oldest first, in
// one write under session.jsonl's lock. The secondary copies are left in
// place: their writers prune them once they see their turns came back.
// Returns how many turns were appended.
func fuseSecondaryLogs(sessionDir string) (int, error) {
	names := secondaryLogs(sessionDir)
	if len(names) == 0 {
		return 0, nil
	}
	var added int
	err := withLockedLog(filepath.Join(sessionDir, primaryLogName), true, func(f *os.File, data []byte) error {
		have := headerSet(data)
		var pending []logBlock
		for _, name := range names {
			sec, err := os.ReadFile(filepath.Join(sessionDir, name))
			if err != nil {
				continue
			}
			for _, b := range splitLogBlocks(sec, false) {
				if have[b.header] {
					continue
				}
				have[b.header] = true
				pending = append(pending, b)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		sort.SliceStable(pending, func(i, j int) bool { return pending[i].time.Before(pending[j].time) })
		var out strings.Builder
		if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
			out.WriteString("\n")
		}
		for _, b := range pending {
			out.WriteString(b.text)
		}
		if _, err := f.Write([]byte(out.String())); err != nil {
			return err
		}
		added = len(pending)
		return nil
	})
	return added, err
}

// pruneSecondaryLog drops from hostID's own secondary log in sessionDir the
// turns session.jsonl already has, rewriting it in place under its lock (so
// a writer waiting on the lock still appends to the same file), or removing
// it when nothing is left.
func pruneSecondaryLog(sessionDir, hostID string) error {
	path := filepath.Join(sessionDir, secondaryLogName(hostID))
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	primary, err := os.ReadFile(filepath.Join(sessionDir, primaryLogName))
	if err != nil {
		return nil // nothing has come back yet
	}
	have := headerSet(primary)
	return withLockedLog(path, false, func(f *os.File, data []byte) error {
		var keep strings.Builder
		for _, b := range splitLogBlocks(data, true) {
			if !have[b.header] {
				keep.WriteString(b.text)
			}
		}
		if keep.Len() == len(data) {
			return nil
		}
		if keep.Len() == 0 {
			return os.Remove(path)
		}
		if err := f.Truncate(0); err != nil {
			return err
		}
		_, err := f.WriteAt([]byte(keep.String()), 0)
		return err
	})
}

// withLockedLog opens path (creating it when create is set), takes the same
// exclusive flock clauditable's and federation-command's appenders take, and
// calls fn with the file and its current contents, positioned at the end.
func withLockedLog(path string, create bool, fn func(f *os.File, data []byte) error) error {
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	for attempt := 0; attempt < 5; attempt++ {
		f, err := os.OpenFile(path, flags, 0644)
		if err != nil {
			if os.IsNotExist(err) && !create {
				return nil
			}
			return err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return err
		}
		held, err1 := f.Stat()
		current, err2 := os.Stat(path)
		if err1 != nil || err2 != nil || !os.SameFile(held, current) {
			// Replaced or removed while we waited -- start over on the
			// current file.
			f.Close()
			continue
		}
		data, err := os.ReadFile(path)
		if err == nil {
			if _, err = f.Seek(0, io.SeekEnd); err == nil {
				err = fn(f, data)
			}
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	return fmt.Errorf("%s kept being replaced while locking it", path)
}
