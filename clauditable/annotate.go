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
	ts, err := addNote(recordsPath, getSession(), text)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clauditable add-note: %v\n", err)
		return 1
	}
	fmt.Println(ts)
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
	ts, err := addResource(recordsPath, getSession(), paths[0], name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clauditable add-resource: %v\n", err)
		return 1
	}
	fmt.Println(ts)
	return 0
}

// addNote records text as a note in session.
func addNote(recordsPath, session, text string) (int64, error) {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return 0, errors.New("note is empty")
	}
	record := annotationRecord(records.EventTypeNote)
	record.Command = text
	return writeAnnotation(recordsPath, session, &record, nil)
}

// addResource copies srcPath into session as a resource named name.
func addResource(recordsPath, session, srcPath, name string) (int64, error) {
	info, err := os.Stat(srcPath)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", srcPath)
	}
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(srcPath)
	}
	safe := sanitizeResourceName(name)

	record := annotationRecord(records.EventTypeResource)
	record.Command = name
	return writeAnnotation(recordsPath, session, &record, func(sessionDir string, ts int64) error {
		file := fmt.Sprintf("%d%s%s", ts, records.ResourceFileInfix, safe)
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
// primary: {ts}-raw.txt, {ts}-processed.txt (redacted like any other record),
// then its session log entry appended to session.jsonl in a single write.
// prepare, when set, runs once ts is reserved and before anything is
// formatted (add-resource copies its file there). Returns the record ID.
func writeAnnotation(recordsPath, session string, record *records.Record, prepare func(sessionDir string, ts int64) error) (int64, error) {
	sessionDir := filepath.Join(recordsPath, session)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return 0, fmt.Errorf("creating session directory: %w", err)
	}
	name := session
	if session == defaultSessionID() {
		name = defaultSessionName()
	}
	_ = writeSessionYAMLIfAbsent(sessionDir, session, name)

	ts, rawFile, err := reserveRecordID(sessionDir, time.Now().Unix()-1)
	if err != nil {
		return 0, err
	}
	rawPath := rawFile.Name()
	fail := func(err error) (int64, error) {
		rawFile.Close()
		os.Remove(rawPath)
		return 0, err
	}

	if prepare != nil {
		if err := prepare(sessionDir, ts); err != nil {
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
		return 0, fmt.Errorf("writing raw file: %w", err)
	}
	if err := writeProcessedFile(sessionDir, ts, true, content); err != nil {
		return 0, fmt.Errorf("writing processed file: %w", err)
	}

	processed, err := os.ReadFile(filepath.Join(sessionDir, fmt.Sprintf("%d-processed.txt", ts)))
	if err != nil {
		return 0, fmt.Errorf("reading processed file: %w", err)
	}
	entry := records.ExtractSessionLogFromWrittenFile(string(processed))
	entry = strings.TrimRight(entry, "\n") + "\n\n"
	f, err := os.OpenFile(filepath.Join(sessionDir, "session.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return 0, fmt.Errorf("opening session.jsonl: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(entry); err != nil {
		return 0, fmt.Errorf("appending to session.jsonl: %w", err)
	}
	return ts, nil
}

// reserveRecordID claims a record ID (a unix second) for a note/resource by
// exclusively creating its {ts}-raw.txt, returning that file open for writing.
// It starts at start and walks backwards past any second some other record
// already uses: wrapped commands take the current second when they start, so
// a free past second can't later be claimed by one of them.
func reserveRecordID(sessionDir string, start int64) (int64, *os.File, error) {
	for ts := start; ts > start-3600; ts-- {
		if recordIDInUse(sessionDir, ts) {
			continue
		}
		f, err := os.OpenFile(filepath.Join(sessionDir, fmt.Sprintf("%d-raw.txt", ts)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
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
