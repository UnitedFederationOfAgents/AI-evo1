package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeLR serves the slice of local-representative's files API composer
// uses: GET /api/files, GET /api/files/<id> and POST /api/files.
type fakeLR struct {
	files    []map[string]any
	content  map[string]string
	uploaded map[string][]byte
}

func newFakeLR(t *testing.T) (*fakeLR, *httptest.Server) {
	f := &fakeLR{content: map[string]string{}, uploaded: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/files":
			json.NewEncoder(w).Encode(map[string]any{"files": f.files})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/files/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/files/")
			body, ok := f.content[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			http.ServeContent(w, r, id, time.Time{}, strings.NewReader(body))
		case r.Method == http.MethodPost && r.URL.Path == "/api/files":
			file, hdr, err := r.FormFile("file")
			if err != nil {
				t.Errorf("upload without a file field: %v", err)
				http.Error(w, "bad upload", http.StatusBadRequest)
				return
			}
			b, _ := io.ReadAll(file)
			f.uploaded[hdr.Filename] = b
			json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{{"id": "abc_" + hdr.Filename, "name": hdr.Filename}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func TestMediaKindAndOrigin(t *testing.T) {
	for name, want := range map[string][2]string{
		"ianar-recording-fc-2026.webm":   {"video", "ianar"},
		"polly-Joanna-hello-2026.mp3":    {"audio", "polly"},
		"composer-hello-world-2026.webm": {"video", "composer"},
		"Shot.PNG":                       {"image", ""},
		"ianar-inspect-x.zip":            {"", "ianar"},
		"transcript-2026.txt":            {"", ""},
	} {
		if got := mediaKind(name); got != want[0] {
			t.Errorf("mediaKind(%q) = %q, want %q", name, got, want[0])
		}
		if got := sourceOrigin(name); got != want[1] {
			t.Errorf("sourceOrigin(%q) = %q, want %q", name, got, want[1])
		}
	}
}

func TestListSourcesFiltersAndSorts(t *testing.T) {
	f, srv := newFakeLR(t)
	f.files = []map[string]any{
		{"id": "1_transcript.txt", "name": "transcript.txt", "size": 10, "uploaded_at": 300},
		{"id": "2_rec.webm", "name": "ianar-recording-x.webm", "size": 20, "uploaded_at": 100},
		{"id": "3_clip.mp3", "name": "polly-Joanna-hi.mp3", "size": 30, "uploaded_at": 200},
	}
	got, err := listSources(srv.URL)
	if err != nil {
		t.Fatalf("listSources: %v", err)
	}
	if len(got) != 2 || got[0].ID != "3_clip.mp3" || got[1].ID != "2_rec.webm" {
		t.Fatalf("sources = %+v; want the mp3 then the webm", got)
	}
	if got[0].Media != "audio" || got[0].Origin != "polly" || got[1].Media != "video" || got[1].Origin != "ianar" {
		t.Errorf("sources = %+v; wrong media/origin", got)
	}
}

func TestSourcesMsgWithoutLR(t *testing.T) {
	s := newServer(t.TempDir())
	msg := s.sourcesMsg()
	if msg.Error == "" || len(msg.Sources) != 0 {
		t.Errorf("sourcesMsg = %+v; want a not-connected error", msg)
	}
}

func TestHandleSourcePassesRange(t *testing.T) {
	f, srv := newFakeLR(t)
	f.content["2_rec.webm"] = "0123456789"
	s := newServer(t.TempDir())
	s.lrBaseOverride = srv.URL

	req := httptest.NewRequest(http.MethodGet, "/api/source/2_rec.webm", nil)
	req.Header.Set("Range", "bytes=2-4")
	rec := httptest.NewRecorder()
	s.handleSource(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "234" {
		t.Errorf("got %d %q; want 206 \"234\"", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Range") == "" {
		t.Error("Content-Range was not passed through")
	}

	rec = httptest.NewRecorder()
	s.handleSource(rec, httptest.NewRequest(http.MethodGet, "/api/source/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing source: got %d, want 404", rec.Code)
	}
}

func TestHandleRenderUploadsAndRecords(t *testing.T) {
	f, srv := newFakeLR(t)
	s := newServer(t.TempDir())
	s.lrBaseOverride = srv.URL
	c, err := s.store.fromExample("hello-world")
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.handleRender(rec, httptest.NewRequest(http.MethodPost, "/api/render?composition="+c.ID, strings.NewReader("webm-bytes")))
	if rec.Code != http.StatusOK {
		t.Fatalf("render: got %d %s", rec.Code, rec.Body.String())
	}
	var res RenderResultMsg
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Error != "" || !strings.HasPrefix(res.Name, "composer-hello-world-") || !strings.HasSuffix(res.Name, ".webm") {
		t.Errorf("result = %+v", res)
	}
	if string(f.uploaded[res.Name]) != "webm-bytes" {
		t.Errorf("uploaded %q, want webm-bytes", f.uploaded[res.Name])
	}
	got, _ := s.store.get(c.ID)
	if len(got.Renders) != 1 || got.Renders[0].FileID != res.FileID {
		t.Errorf("renders = %+v, want the new one recorded", got.Renders)
	}
}

func TestHandleRenderErrors(t *testing.T) {
	s := newServer(t.TempDir())
	rec := httptest.NewRecorder()
	s.handleRender(rec, httptest.NewRequest(http.MethodPost, "/api/render?composition=nope", strings.NewReader("x")))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown composition: got %d, want 404", rec.Code)
	}

	c, _ := s.store.fromExample("hello-world")
	rec = httptest.NewRecorder()
	s.handleRender(rec, httptest.NewRequest(http.MethodPost, "/api/render?composition="+c.ID, strings.NewReader("")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty body: got %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	s.handleRender(rec, httptest.NewRequest(http.MethodPost, "/api/render?composition="+c.ID, strings.NewReader("x")))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no LR: got %d, want 503", rec.Code)
	}
}

func TestComposeStateCountsCompositions(t *testing.T) {
	s := newServer(t.TempDir())
	s.httpPort = "8089"
	s.store.fromExample("hello-world")
	st := s.composeState()
	if st.HTTPPort != "8089" || st.Compositions != 1 {
		t.Errorf("composeState = %+v", st)
	}
	b, _ := json.Marshal(st)
	if string(b) != `{"http_port":"8089","compositions":1}` {
		t.Errorf("compose-state JSON = %s; local-representative and agent-coordinator read http_port", b)
	}
}

func TestPushComposeStateNoClient(t *testing.T) {
	s := newServer(t.TempDir())
	s.pushComposeState() // must not panic without a connection
}
