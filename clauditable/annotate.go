package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"clauditable/pkg/records"
	ufahostid "ufa-hostid"
)

// Notes and resources: things a person attaches to a session, rather than
// commands clauditable wraps. Each becomes an ordinary record -- a
// {record}-raw.txt, a {record}-processed.txt and a session.jsonl entry --
// with event_type "note" or "resource", so every reader of a session
// (session-manager's transcript, federation-command, the sync globs) sees it
// in place. A resource's content is copied alongside as
// {record}-resource-{name}. session-manager and federation-command both add
// notes/resources by running these subcommands, so this is the one writer.
// They follow the same ownership rules as wrapped commands (see
// sessionlog.go): on a host that doesn't own the session, the files are
// {record}-<host>-... and the entry goes to that host's secondary log.

// runAddNote is "clauditable add-note [text...]": the note is args joined by
// spaces, or stdin when there are no args. Prints the new record ID.
func runAddNote(args []string, stdin io.Reader) int {
	text := strings.Join(args, " ")
	if len(args) == 0 {
		data, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "clauditable add-note: reading stdin: %v\n", err)
			return 1
		}
		text = string(data)
	}
	recordsPath := getEnvOrDefault(EnvAgentRecordsPath, DefaultRecordsPath)
	id, err := addNote(recordsPath, getSession(), text)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clauditable add-note: %v\n", err)
		return 1
	}
	fmt.Println(id)
	return 0
}

// runAddResource is "clauditable add-resource [-name <name>] <path>": copies
// path into the current session under name (default: path's base name).
// Prints the new record ID.
func runAddResource(args []string) int {
	name := ""
	var paths []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-name" && i+1 < len(args) {
			i++
			name = args[i]
			continue
		}
		paths = append(paths, args[i])
	}
	if len(paths) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: clauditable add-resource [-name <name>] <path>")
		return 1
	}
	recordsPath := getEnvOrDefault(EnvAgentRecordsPath, DefaultRecordsPath)
	id, err := addResource(recordsPath, getSession(), paths[0], name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clauditable add-resource: %v\n", err)
		return 1
	}
	fmt.Println(id)
	return 0
}

// addNote records text as a note in session. Returns the record ID.
func addNote(recordsPath, session, text string) (string, error) {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return "", errors.New("note is empty")
	}
	record := annotationRecord(records.EventTypeNote)
	record.Command = text
	return writeAnnotation(recordsPath, session, &record, nil)
}

// addResource copies srcPath into session as a resource named name. Returns
// the record ID.
func addResource(recordsPath, session, srcPath, name string) (string, error) {
	info, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", srcPath)
	}
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(srcPath)
	}
	safe := sanitizeResourceName(name)

	record := annotationRecord(records.EventTypeResource)
	record.Command = name
	return writeAnnotation(recordsPath, session, &record, func(sessionDir, id string) error {
		file := id + records.ResourceFileInfix + safe
		size, err := copyFile(srcPath, filepath.Join(sessionDir, file))
		if err != nil {
			return fmt.Errorf("copying resource: %w", err)
		}
		record.Event.Metadata[records.MetadataResourceName] = name
		record.Event.Metadata[records.MetadataResourceFile] = file
		record.Event.Metadata[records.MetadataResourceSize] = strconv.FormatInt(size, 10)
		record.Stdout = fmt.Sprintf("saved as %s (%d bytes)", file, size)
		return nil
	})
}

// annotationRecord starts a note/resource record with the same identifying
// fields a wrapped command gets (host, head, UFA_METADATA).
func annotationRecord(eventType string) records.Record {
	host := os.Getenv(EnvUFAHost)
	if host == "" {
		host = ufahostid.GetHostID()
	}
	metadata := parseMetadata(os.Getenv(EnvUFAMetadata))
	if metadata == nil {
		metadata = map[string]string{}
	}
	return records.Record{Event: records.Event{
		Timestamp: time.Now().Format(time.RFC3339),
		EventType: eventType,
		Host:      host,
		Head:      os.Getenv(EnvUFAHead),
		Metadata:  metadata,
	}}
}

// writeAnnotation writes record into session as an already-consolidated
// primary: {record}-raw.txt, {record}-processed.txt (redacted like any other
// record), then its session log entry appended to this host's session log
// (session.jsonl on the owner, its secondary log elsewhere) in a single
// locked write. Today's default session is created if needed; any other
// session must already exist here. prepare, when set, runs once the record
// ID is reserved and before anything is formatted (add-resource copies its
// file there). Returns the record ID: "{ts}" on the owner, "{ts}-<host>"
// elsewhere.
func writeAnnotation(recordsPath, session string, record *records.Record, prepare func(sessionDir, id string) error) (string, error) {
	sessionDir := filepath.Join(recordsPath, session)
	if session == defaultSessionID() {
		if err := ensureSession(recordsPath, session, defaultSessionName()); err != nil {
			return "", err
		}
	} else if _, err := os.Stat(sessionDir); err != nil {
		return "", fmt.Errorf("session %q not found on this host", session)
	}
	target := writeTargetFor(sessionDir)

	ts, rawFile, err := reserveRecordID(sessionDir, time.Now().Unix()-1, target.tag)
	if err != nil {
		return "", err
	}
	id := recordPrefix(ts, target.tag)
	rawPath := rawFile.Name()
	fail := func(err error) (string, error) {
		rawFile.Close()
		os.Remove(rawPath)
		return "", err
	}

	if prepare != nil {
		if err := prepare(sessionDir, id); err != nil {
			return fail(err)
		}
	}
	record.Event.RecordPath = rawPath
	if len(record.Event.Metadata) == 0 {
		record.Event.Metadata = nil
	}
	content := record.FormatWrittenFile()
	if _, err := rawFile.WriteString(content); err != nil {
		return fail(fmt.Errorf("writing raw file: %w", err))
	}
	if err := rawFile.Close(); err != nil {
		os.Remove(rawPath)
		return "", fmt.Errorf("writing raw file: %w", err)
	}
	if err := writeProcessedFile(sessionDir, ts, target.tag, true, content); err != nil {
		return "", fmt.Errorf("writing processed file: %w", err)
	}

	processed, err := os.ReadFile(filepath.Join(sessionDir, id+"-processed.txt"))
	if err != nil {
		return "", fmt.Errorf("reading processed file: %w", err)
	}
	entry := records.ExtractSessionLogFromWrittenFile(string(processed))
	entry = strings.TrimRight(entry, "\n") + "\n\n"
	if err := appendSessionLog(target.logPath, []byte(entry)); err != nil {
		return "", fmt.Errorf("appending to %s: %w", filepath.Base(target.logPath), err)
	}
	return id, nil
}

// reserveRecordID claims a record ID (a unix second) for a note/resource by
// exclusively creating its {ts}{tag}-raw.txt, returning that file open for
// writing. It starts at start and walks backwards past any second some other
// record already uses: wrapped commands take the current second when they
// start, so a free past second can't later be claimed by one of them.
func reserveRecordID(sessionDir string, start int64, tag string) (int64, *os.File, error) {
	for ts := start; ts > start-3600; ts-- {
		if recordIDInUse(sessionDir, ts) {
			continue
		}
		f, err := os.OpenFile(filepath.Join(sessionDir, recordPrefix(ts, tag)+"-raw.txt"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			return ts, f, nil
		}
		if !os.IsExist(err) {
			return 0, nil, fmt.Errorf("creating raw file: %w", err)
		}
	}
	return 0, nil, errors.New("no free record ID in the last hour")
}

// recordIDInUse reports whether any file of record ts already exists.
func recordIDInUse(sessionDir string, ts int64) bool {
	matches, _ := filepath.Glob(filepath.Join(sessionDir, fmt.Sprintf("%d-*", ts)))
	return len(matches) > 0
}

// sanitizeResourceName makes name safe as the tail of a file name in the
// session directory: its base name, with anything outside [A-Za-z0-9._-]
// replaced by '-', capped at 80 characters.
func sanitizeResourceName(name string) string {
	var b strings.Builder
	for _, r := range filepath.Base(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), ".-")
	if len(s) > 80 {
		s = s[:80]
	}
	if s == "" {
		return "resource"
	}
	return s
}

// copyFile copies src to a new file dst, returning the bytes copied.
func copyFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return 0, err
	}
	return n, nil
}
