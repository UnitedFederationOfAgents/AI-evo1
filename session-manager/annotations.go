package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Notes and resources: text or a file attached to a session, recorded as
// that session's "note"/"resource" entries. session-manager doesn't write
// them itself -- unlike the rest of sessions.go, which mirrors clauditable's
// logic in-process -- it runs "clauditable add-note"/"clauditable
// add-resource", so there's one writer for the record files, redaction, and
// session.jsonl append (see clauditable/annotate.go). federation-command's
// "ufa session note"/"ufa session resource" run the same subcommands.

const (
	// maxNoteBytes and maxResourceBytes bound what one "add-note"/
	// "add-resource" message may carry (a resource arrives base64-encoded in
	// a single WebSocket message).
	maxNoteBytes     = 1 << 20
	maxResourceBytes = 25 << 20

	// resourceFileInfix mirrors clauditable/pkg/records.ResourceFileInfix: a
	// resource's content lives in {record}-resource-{name}.
	resourceFileInfix = "-resource-"
)

// AnnotationResultMsg is the "annotation-result" WebSocket payload reporting
// an add-note/add-resource's outcome. Record is the new entry's record ID.
type AnnotationResultMsg struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // "note" | "resource"
	Name   string `json:"name,omitempty"`
	Record string `json:"record,omitempty"`
	Error  string `json:"error,omitempty"`
}

// validSessionID reports whether id names a single directory under the
// records path (no separators, not "." or "..") -- checked before anything
// is written into it.
func validSessionID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`)
}

// ensureLocalSession makes sure id exists in this host's records path,
// pulling it in first when it's only known remotely -- the same as
// handleSetSession.
func (s *Server) ensureLocalSession(id string) error {
	if !validSessionID(id) {
		return fmt.Errorf("invalid session id %q", id)
	}
	if _, err := os.Stat(filepath.Join(s.recordsPath, id)); err == nil {
		return nil
	}
	s.triggerSessionSync(id)
	if _, err := os.Stat(filepath.Join(s.recordsPath, id)); err != nil {
		return fmt.Errorf("session %q not found", id)
	}
	return nil
}

// handleAddNote attaches text to session id as a note.
func (s *Server) handleAddNote(c *wsClient, id, text string) {
	result := AnnotationResultMsg{ID: id, Kind: "note"}
	switch {
	case strings.TrimSpace(text) == "":
		result.Error = "note is empty"
	case len(text) > maxNoteBytes:
		result.Error = fmt.Sprintf("note is over %d KiB", maxNoteBytes>>10)
	default:
		if err := s.ensureLocalSession(id); err != nil {
			result.Error = err.Error()
			break
		}
		record, err := s.runClauditable(id, strings.NewReader(text), "add-note")
		if err != nil {
			result.Error = err.Error()
			break
		}
		result.Record = record
	}
	s.finishAnnotation(c, result)
}

// handleAddResource attaches a file (content, base64-encoded) to session id
// under name.
func (s *Server) handleAddResource(c *wsClient, id, name, contentB64 string) {
	result := AnnotationResultMsg{ID: id, Kind: "resource", Name: name}
	if err := s.addResource(id, name, contentB64, &result); err != nil {
		result.Error = err.Error()
	}
	s.finishAnnotation(c, result)
}

func (s *Server) addResource(id, name, contentB64 string, result *AnnotationResultMsg) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("resource needs a name")
	}
	if base64.StdEncoding.DecodedLen(len(contentB64)) > maxResourceBytes+2 {
		return fmt.Errorf("resource is over %d MiB", maxResourceBytes>>20)
	}
	content, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		return fmt.Errorf("decoding resource: %w", err)
	}
	if len(content) > maxResourceBytes {
		return fmt.Errorf("resource is over %d MiB", maxResourceBytes>>20)
	}
	if err := s.ensureLocalSession(id); err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "session-manager-resource-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	record, err := s.runClauditable(id, nil, "add-resource", "-name", name, tmp.Name())
	if err != nil {
		return err
	}
	result.Record = record
	return nil
}

// finishAnnotation replies with the outcome and, on success, the session's
// refreshed transcript. The transcript is read locally rather than through
// sendSessionView: the entry was just written here, and a pre-view sync
// would only add a round trip.
func (s *Server) finishAnnotation(c *wsClient, result AnnotationResultMsg) {
	s.sendToClient(c, "annotation-result", result)
	if result.Error != "" {
		return
	}
	if view, err := viewSession(s.recordsPath, result.ID); err == nil {
		s.sendToClient(c, "session-view", view)
	}
	s.broadcastSessions()
}

// runClauditable runs "clauditable <args...>" against session id, feeding it
// stdin, and returns its trimmed stdout (the new record ID).
func (s *Server) runClauditable(id string, stdin io.Reader, args ...string) (string, error) {
	path, err := findBinary("clauditable")
	if err != nil {
		return "", err
	}
	cmd := exec.Command(path, args...)
	cmd.Env = append(os.Environ(),
		EnvAgentRecordsPath+"="+s.recordsPath,
		"AGENT_SESSION="+id,
	)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", fmt.Errorf("clauditable %s: %w", args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// findBinary looks for name next to this executable, then on PATH, mirroring
// dungeon-keeper's findBinary minus its current-directory check: launched
// from the repo root, ./clauditable is clauditable's source directory.
// Candidates must be executable regular files.
func findBinary(name string) (string, error) {
	if exePath, err := os.Executable(); err == nil {
		local := filepath.Join(filepath.Dir(exePath), name)
		if info, err := os.Stat(local); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return local, nil
		}
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("%s not found next to session-manager or on PATH", name)
}

// ---- serving a resource's content ----

// findResourceFile returns the name of record's resource file in id, or "".
func findResourceFile(recordsPath, id, record string) string {
	matches, _ := filepath.Glob(filepath.Join(recordsPath, id, record+resourceFileInfix+"*"))
	if len(matches) == 0 {
		return ""
	}
	return filepath.Base(matches[0])
}

// handleResource answers GET /api/resource?session=<id>&record=<record> with
// that resource's content, pulling it from another host first if it isn't
// here yet (the transcript's sync only takes small resources along -- see
// sessionSyncGlobs). It's served sandboxed (CSP "sandbox", nosniff) since
// it's arbitrary uploaded content on this UI's origin.
func (s *Server) handleResource(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	record := r.URL.Query().Get("record")
	if !validSessionID(id) || !isRecordID(record) {
		http.Error(w, "invalid session or record", http.StatusBadRequest)
		return
	}
	file := findResourceFile(s.recordsPath, id, record)
	if file == "" {
		s.triggerSessionSyncGlobs(id, []sessionSyncGlob{{glob: record + resourceFileInfix + "*"}})
		file = findResourceFile(s.recordsPath, id, record)
	}
	if file == "" {
		http.Error(w, fmt.Sprintf("no resource for record %s in session %q", record, id), http.StatusNotFound)
		return
	}
	f, err := os.Open(filepath.Join(s.recordsPath, id, file))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := strings.TrimPrefix(file, record+resourceFileInfix)
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, info.ModTime(), f)
}
