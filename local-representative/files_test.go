package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClassifyKind covers the small wireframe-icon classification set.
func TestClassifyKind(t *testing.T) {
	cases := map[string]string{
		"notes.txt":   "text",
		"README.md":   "text",
		"diagram.PNG": "image",
		"photo.jpeg":  "image",
		"archive.zip": "other",
		"noextension": "other",
	}
	for name, want := range cases {
		if got := classifyKind(name); got != want {
			t.Errorf("classifyKind(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestSanitizeFilename ensures an upload's claimed filename can't escape the
// host-cache directory.
func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"report.pdf":          "report.pdf",
		"../../etc/passwd":    "passwd",
		"a/b/c.txt":           "c.txt",
		`C:\Users\x\file.txt`: "file.txt",
		"":                    "file",
		"..":                  "file",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDisplayNameRoundTrip verifies displayName recovers the name saveUploadedFile
// encoded into the on-disk id.
func TestDisplayNameRoundTrip(t *testing.T) {
	id := randomID() + "_" + "my report (final).txt"
	if got := displayName(id); got != "my report (final).txt" {
		t.Errorf("displayName(%q) = %q, want original name", id, got)
	}
	// A plain filename with no matching prefix is returned unchanged.
	if got := displayName("plain.txt"); got != "plain.txt" {
		t.Errorf("displayName(%q) = %q, want unchanged", "plain.txt", got)
	}
}

func newTestFileServer(t *testing.T) *Server {
	t.Helper()
	s := newServer("test-lr")
	s.fileCacheDir = t.TempDir()
	return s
}

// TestFileUploadAndList covers a direct (non-proxied) upload landing in the
// host-cache and then showing up in listFiles/GET /api/files.
func TestFileUploadAndList(t *testing.T) {
	s := newTestFileServer(t)

	req := newUploadRequest(t, "hello.txt", []byte("hello world"))
	rec := httptest.NewRecorder()
	s.handleFilesAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	files := s.listFiles()
	if len(files) != 1 {
		t.Fatalf("listFiles() after upload = %d files, want 1", len(files))
	}
	if files[0].Name != "hello.txt" || files[0].Kind != "text" || files[0].Size != int64(len("hello world")) {
		t.Fatalf("unexpected file info: %+v", files[0])
	}
}

// TestFileUploadRejectedWhenProxied verifies the enforcement mechanism behind
// "no upload input through agent-coordinator": a request carrying the header
// agent-coordinator's reverse proxy stamps is refused.
func TestFileUploadRejectedWhenProxied(t *testing.T) {
	s := newTestFileServer(t)

	req := newUploadRequest(t, "hello.txt", []byte("hello"))
	req.Header.Set(proxiedHeader, "agent-coordinator")
	rec := httptest.NewRecorder()
	s.handleFilesAPI(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("proxied upload: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if files := s.listFiles(); len(files) != 0 {
		t.Fatalf("proxied upload should not have written a file, got %+v", files)
	}
}

// TestSweepExpiredFiles verifies files older than fileCacheTTL are removed and
// fresh ones are left alone.
func TestSweepExpiredFiles(t *testing.T) {
	s := newTestFileServer(t)

	old := filepath.Join(s.fileCacheDir, "aaaaaaaa_old.txt")
	fresh := filepath.Join(s.fileCacheDir, "bbbbbbbb_fresh.txt")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * fileCacheTTL)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	s.sweepExpiredFiles()

	files := s.listFiles()
	if len(files) != 1 || files[0].Name != "fresh.txt" {
		t.Fatalf("sweepExpiredFiles left %+v, want only fresh.txt", files)
	}
}

// TestUploadWritesManifestHiddenFromListing verifies an upload gets a
// ".manifest_<id>.yaml" sidecar on disk that listFiles/GET never surfaces.
func TestUploadWritesManifestHiddenFromListing(t *testing.T) {
	s := newTestFileServer(t)

	req := newUploadRequest(t, "hello.txt", []byte("hello world"))
	rec := httptest.NewRecorder()
	s.handleFilesAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	files := s.listFiles()
	if len(files) != 1 {
		t.Fatalf("listFiles() = %d files, want 1 (manifest should not be listed)", len(files))
	}
	id := files[0].ID

	manifestPath := filepath.Join(s.fileCacheDir, manifestName(id))
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("manifest %s not written: %v", manifestPath, err)
	}
	if !strings.Contains(string(body), `name: "hello.txt"`) {
		t.Errorf("manifest %s missing expected name field, got:\n%s", manifestPath, body)
	}
}

// TestUploadRejectsManifestPrefixedName verifies a claimed filename starting
// with manifestPrefix is refused rather than silently shadowing a real
// manifest sidecar.
func TestUploadRejectsManifestPrefixedName(t *testing.T) {
	s := newTestFileServer(t)

	req := newUploadRequest(t, manifestPrefix+"x.yaml", []byte("nope"))
	rec := httptest.NewRecorder()
	s.handleFilesAPI(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("manifest-prefixed upload: status = %d, want %d (no file could be saved)", rec.Code, http.StatusInternalServerError)
	}
	if files := s.listFiles(); len(files) != 0 {
		t.Fatalf("manifest-prefixed upload should not have written a file, got %+v", files)
	}
}

// TestSweepExpiredFilesRemovesManifest verifies the manifest sidecar goes with
// its data file when the sweep removes an expired entry.
func TestSweepExpiredFilesRemovesManifest(t *testing.T) {
	s := newTestFileServer(t)

	id := "aaaaaaaa_old.txt"
	old := filepath.Join(s.fileCacheDir, id)
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(s.fileCacheDir, manifestName(id))
	if err := os.WriteFile(manifestPath, []byte("id: \"aaaaaaaa_old.txt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * fileCacheTTL)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	s.sweepExpiredFiles()

	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Errorf("manifest %s should have been removed alongside its expired data file, stat err = %v", manifestPath, err)
	}
}

// TestHandleFileRaw covers GET /api/files/<id>: inline by default, an
// attachment Content-Disposition with ?download=1, and a 404 for a manifest
// id (which should never be independently addressable).
func TestHandleFileRaw(t *testing.T) {
	s := newTestFileServer(t)

	req := newUploadRequest(t, "hello.txt", []byte("hello world"))
	rec := httptest.NewRecorder()
	s.handleFilesAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	id := s.listFiles()[0].ID

	viewReq := httptest.NewRequest(http.MethodGet, "/api/files/"+id, nil)
	viewRec := httptest.NewRecorder()
	s.handleFileRaw(viewRec, viewReq)
	if viewRec.Code != http.StatusOK {
		t.Fatalf("raw view: status = %d, body = %s", viewRec.Code, viewRec.Body.String())
	}
	if viewRec.Body.String() != "hello world" {
		t.Errorf("raw view body = %q, want %q", viewRec.Body.String(), "hello world")
	}
	if got := viewRec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline;") {
		t.Errorf("raw view Content-Disposition = %q, want inline", got)
	}

	dlReq := httptest.NewRequest(http.MethodGet, "/api/files/"+id+"?download=1", nil)
	dlRec := httptest.NewRecorder()
	s.handleFileRaw(dlRec, dlReq)
	if got := dlRec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Errorf("raw download Content-Disposition = %q, want attachment", got)
	}

	manifestReq := httptest.NewRequest(http.MethodGet, "/api/files/"+manifestName(id), nil)
	manifestRec := httptest.NewRecorder()
	s.handleFileRaw(manifestRec, manifestReq)
	if manifestRec.Code != http.StatusNotFound {
		t.Errorf("raw fetch of a manifest id: status = %d, want %d", manifestRec.Code, http.StatusNotFound)
	}
}

// newUploadRequest builds a multipart POST /api/files request carrying a
// single "file" field.
func newUploadRequest(t *testing.T, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/files", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}
