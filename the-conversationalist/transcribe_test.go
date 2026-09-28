package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestUploadToFilesSendsMultipartFile verifies uploadToFiles posts the
// transcript as a single multipart "file" field -- the same shape
// local-representative's browser upload widget sends -- and parses the
// FileInfo local-representative's POST /api/files returns.
func TestUploadToFilesSendsMultipartFile(t *testing.T) {
	var gotFilename string
	var gotContent []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/api/files", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %s", r.Method)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		fh := r.MultipartForm.File["file"]
		if len(fh) != 1 {
			t.Fatalf("expected exactly one file field, got %d", len(fh))
		}
		gotFilename = fh[0].Filename
		f, err := fh[0].Open()
		if err != nil {
			t.Fatalf("opening uploaded file: %v", err)
		}
		defer f.Close()
		buf := make([]byte, fh[0].Size)
		if _, err := f.Read(buf); err != nil {
			t.Fatalf("reading uploaded file: %v", err)
		}
		gotContent = buf

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"files": []map[string]interface{}{
				{"id": "abc12345_" + gotFilename, "name": gotFilename},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	uploaded, err := uploadToFiles(srv.URL, "transcript-2026-01-01T00-00-00.txt", []byte("hello world"))
	if err != nil {
		t.Fatalf("uploadToFiles: %v", err)
	}
	if gotFilename != "transcript-2026-01-01T00-00-00.txt" {
		t.Errorf("uploaded filename = %q, want the transcript's name", gotFilename)
	}
	if string(gotContent) != "hello world" {
		t.Errorf("uploaded content = %q, want %q", gotContent, "hello world")
	}
	if uploaded.ID != "abc12345_"+gotFilename || uploaded.Name != gotFilename {
		t.Errorf("unexpected uploaded file: %+v", uploaded)
	}
}

// TestUploadToFilesUnexpectedStatus verifies a non-200 response from
// local-representative surfaces as an error rather than a silent success.
func TestUploadToFilesUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := uploadToFiles(srv.URL, "transcript.txt", []byte("x")); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

// TestSaveTranscriptNoSession verifies saveTranscript reports a clean
// "nothing to save" result rather than panicking when no transcription
// session has ever run for this client.
func TestSaveTranscriptNoSession(t *testing.T) {
	s := newServer()
	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	s.saveTranscript(c)

	select {
	case raw := <-c.send:
		var m wsMsg
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Type != "save-result" {
			t.Fatalf("expected a save-result message, got %q", m.Type)
		}
		var p SaveResultMsg
		json.Unmarshal(m.Payload, &p)
		if p.Success {
			t.Error("expected Success=false with no transcript accumulated")
		}
	default:
		t.Fatal("expected a save-result message to be queued")
	}
}

// TestSaveTranscriptNoReprConnection verifies saveTranscript reports a clean
// error (rather than a nil-pointer panic) when there's an accumulated
// transcript but no live connection to local-representative to upload it to.
func TestSaveTranscriptNoReprConnection(t *testing.T) {
	s := newServer()
	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	c.transcribe = &transcribeSession{}
	c.transcribe.final.WriteString("some transcript text")

	s.saveTranscript(c)

	select {
	case raw := <-c.send:
		var m wsMsg
		json.Unmarshal(raw, &m)
		var p SaveResultMsg
		json.Unmarshal(m.Payload, &p)
		if p.Success {
			t.Error("expected Success=false with no representable connection")
		}
	default:
		t.Fatal("expected a save-result message to be queued")
	}
}

// TestStopTranscriptionNoSession verifies stopTranscription tolerates being
// called with no session running (e.g. a stray message, or handleWS's
// disconnect cleanup after a tab that never started recording).
func TestStopTranscriptionNoSession(t *testing.T) {
	s := newServer()
	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	s.stopTranscription(c) // must not panic
}
