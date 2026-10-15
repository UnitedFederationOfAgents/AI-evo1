package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClauditable puts a stand-in "clauditable" first on PATH that records
// its arguments, AGENT_SESSION and stdin (or, for add-resource, the file it
// was handed) under dir, then prints record ID 1234.
func fakeClauditable(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	script := `#!/bin/sh
echo "$@" > "` + dir + `/args"
echo "$AGENT_SESSION" > "` + dir + `/session"
if [ "$1" = "add-note" ]; then cat > "` + dir + `/stdin"; fi
if [ "$1" = "add-resource" ]; then cp "$4" "` + dir + `/resource"; fi
echo 1234
`
	if err := os.WriteFile(filepath.Join(dir, "clauditable"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// recvAnnotationResult returns the first "annotation-result" sent to c.
func recvAnnotationResult(t *testing.T, c *wsClient) AnnotationResultMsg {
	t.Helper()
	for {
		select {
		case raw := <-c.send:
			var m wsMsg
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			if m.Type != "annotation-result" {
				continue
			}
			var res AnnotationResultMsg
			if err := json.Unmarshal(m.Payload, &res); err != nil {
				t.Fatal(err)
			}
			return res
		default:
			t.Fatal("no annotation-result was sent")
		}
	}
}

func newTestClient() *wsClient {
	return &wsClient{send: make(chan []byte, 64), done: make(chan struct{})}
}

func TestHandleAddNoteRunsClauditable(t *testing.T) {
	fake := fakeClauditable(t)
	s := newServer()
	s.recordsPath = t.TempDir()
	os.MkdirAll(filepath.Join(s.recordsPath, "s1"), 0755)
	c := newTestClient()

	s.handleAddNote(c, "s1", "a note\nwith two lines")

	res := recvAnnotationResult(t, c)
	if res.Error != "" || res.Record != "1234" || res.Kind != "note" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if args, _ := os.ReadFile(filepath.Join(fake, "args")); strings.TrimSpace(string(args)) != "add-note" {
		t.Errorf("args = %q, want add-note", args)
	}
	if sess, _ := os.ReadFile(filepath.Join(fake, "session")); strings.TrimSpace(string(sess)) != "s1" {
		t.Errorf("AGENT_SESSION = %q, want s1", sess)
	}
	if stdin, _ := os.ReadFile(filepath.Join(fake, "stdin")); string(stdin) != "a note\nwith two lines" {
		t.Errorf("stdin = %q", stdin)
	}
}

func TestHandleAddNoteRejectsBlankAndUnknownSession(t *testing.T) {
	fakeClauditable(t)
	s := newServer()
	s.recordsPath = t.TempDir()
	c := newTestClient()

	s.handleAddNote(c, "s1", "   ")
	if res := recvAnnotationResult(t, c); res.Error == "" {
		t.Error("expected an error for a blank note")
	}
	s.handleAddNote(c, "missing", "text")
	if res := recvAnnotationResult(t, c); !strings.Contains(res.Error, "not found") {
		t.Errorf("expected not found, got %+v", res)
	}
	s.handleAddNote(c, "../escape", "text")
	if res := recvAnnotationResult(t, c); !strings.Contains(res.Error, "invalid session id") {
		t.Errorf("expected invalid session id, got %+v", res)
	}
}

func TestHandleAddResourcePassesDecodedFile(t *testing.T) {
	fake := fakeClauditable(t)
	s := newServer()
	s.recordsPath = t.TempDir()
	os.MkdirAll(filepath.Join(s.recordsPath, "s1"), 0755)
	c := newTestClient()

	s.handleAddResource(c, "s1", "report.pdf", base64.StdEncoding.EncodeToString([]byte("PDF bytes")))

	res := recvAnnotationResult(t, c)
	if res.Error != "" || res.Record != "1234" || res.Name != "report.pdf" {
		t.Fatalf("unexpected result: %+v", res)
	}
	args, _ := os.ReadFile(filepath.Join(fake, "args"))
	if !strings.HasPrefix(string(args), "add-resource -name report.pdf ") {
		t.Errorf("args = %q", args)
	}
	if data, _ := os.ReadFile(filepath.Join(fake, "resource")); string(data) != "PDF bytes" {
		t.Errorf("resource handed to clauditable = %q", data)
	}
}

func TestHandleAddResourceRejectsBadInput(t *testing.T) {
	fakeClauditable(t)
	s := newServer()
	s.recordsPath = t.TempDir()
	os.MkdirAll(filepath.Join(s.recordsPath, "s1"), 0755)
	c := newTestClient()

	s.handleAddResource(c, "s1", "", base64.StdEncoding.EncodeToString([]byte("x")))
	if res := recvAnnotationResult(t, c); res.Error == "" {
		t.Error("expected an error for a missing name")
	}
	s.handleAddResource(c, "s1", "x.bin", "!!not base64!!")
	if res := recvAnnotationResult(t, c); !strings.Contains(res.Error, "decoding") {
		t.Errorf("expected a decode error, got %+v", res)
	}
}

func TestHandleResourceServesFileSandboxed(t *testing.T) {
	s := newServer()
	s.recordsPath = t.TempDir()
	dir := filepath.Join(s.recordsPath, "s1")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "1234-resource-notes.txt"), []byte("hello"), 0644)

	rec := httptest.NewRecorder()
	s.handleResource(rec, httptest.NewRequest("GET", "/api/resource?session=s1&record=1234", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != "hello" {
		t.Errorf("body = %q", body)
	}
	if rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Error("expected a sandbox CSP")
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "notes.txt") {
		t.Errorf("Content-Disposition = %q", cd)
	}
}

func TestHandleResourceRejectsBadParamsAndMissing(t *testing.T) {
	s := newServer()
	s.recordsPath = t.TempDir()
	os.MkdirAll(filepath.Join(s.recordsPath, "s1"), 0755)

	for _, q := range []string{"session=../x&record=1", "session=s1&record=1*", "session=s1&record="} {
		rec := httptest.NewRecorder()
		s.handleResource(rec, httptest.NewRequest("GET", "/api/resource?"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	s.handleResource(rec, httptest.NewRequest("GET", "/api/resource?session=s1&record=99", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestParseSessionLogCarriesTypeAndMetadata(t *testing.T) {
	data := `{"timestamp":"2026-10-13T10:00:00Z","event_type":"ai_invocation","agent":"claude","record_path":"/x/1000-raw.txt"}
IN>> ambiguous-agent -p hi

{"timestamp":"2026-10-13T10:01:00Z","event_type":"note","record_path":"/x/1001-raw.txt"}
IN>> remember this

{"timestamp":"2026-10-13T10:02:00Z","event_type":"resource","record_path":"/x/1002-raw.txt","metadata":{"resource_name":"a.png","resource_file":"1002-resource-a.png","resource_size":"3"}}
IN>> a.png
OUT>> saved as 1002-resource-a.png (3 bytes)
`
	entries := parseSessionLog([]byte(data))
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].EventType != "ai_invocation" || entries[1].EventType != "note" || entries[2].EventType != "resource" {
		t.Errorf("event types = %q, %q, %q", entries[0].EventType, entries[1].EventType, entries[2].EventType)
	}
	if entries[1].Input != "remember this" {
		t.Errorf("note input = %q", entries[1].Input)
	}
	if entries[2].Record != "1002" || entries[2].Metadata["resource_name"] != "a.png" {
		t.Errorf("resource entry = %+v", entries[2])
	}
}
