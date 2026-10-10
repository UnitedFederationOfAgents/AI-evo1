package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHostFilesListing covers GET /api/host-files: directories first, then
// files by name, with the parent recorded.
func TestHostFilesListing(t *testing.T) {
	s := newTestFileServer(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bee"), 0o644)
	os.WriteFile(filepath.Join(dir, "A.txt"), []byte("a"), 0o644)
	os.Mkdir(filepath.Join(dir, "zdir"), 0o755)

	rec := httptest.NewRecorder()
	s.handleHostFiles(rec, httptest.NewRequest(http.MethodGet, "/api/host-files?path="+url.QueryEscape(dir), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got HostDirListing
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != dir || got.Parent != filepath.Dir(dir) {
		t.Fatalf("path/parent = %q/%q", got.Path, got.Parent)
	}
	var names []string
	for _, e := range got.Entries {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "zdir,A.txt,b.txt" {
		t.Fatalf("entries = %v", names)
	}
	if !got.Entries[0].Dir || got.Entries[2].Size != 3 {
		t.Fatalf("unexpected entries: %+v", got.Entries)
	}
}

// TestHostFilesRejectsRelativePath: the picker always sends absolute (or
// "~"-relative) paths.
func TestHostFilesRejectsRelativePath(t *testing.T) {
	s := newTestFileServer(t)
	rec := httptest.NewRecorder()
	s.handleHostFiles(rec, httptest.NewRequest(http.MethodGet, "/api/host-files?path=etc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.handleHostFiles(rec, httptest.NewRequest(http.MethodGet, "/api/host-files?path="+url.QueryEscape(filepath.Join(t.TempDir(), "missing")), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing dir: status = %d, want 404", rec.Code)
	}
}

// TestHostFileRaw covers GET /api/host-files/raw, which refuses directories.
func TestHostFileRaw(t *testing.T) {
	s := newTestFileServer(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "seq.yaml")
	os.WriteFile(p, []byte("format: lr-control-v1\n"), 0o644)

	rec := httptest.NewRecorder()
	s.handleHostFileRaw(rec, httptest.NewRequest(http.MethodGet, "/api/host-files/raw?path="+url.QueryEscape(p), nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "format: lr-control-v1\n" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="seq.yaml"`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	rec = httptest.NewRecorder()
	s.handleHostFileRaw(rec, httptest.NewRequest(http.MethodGet, "/api/host-files/raw?path="+url.QueryEscape(dir), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("directory: status = %d, want 400", rec.Code)
	}
}

func newHostImportRequest(paths ...string) *http.Request {
	b, _ := json.Marshal(hostImportRequest{HostPaths: paths})
	req := httptest.NewRequest(http.MethodPost, "/api/files", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestHostImportIntoHostCache: a JSON POST /api/files copies host files into
// the host-cache under their own names, skipping ones that can't be read.
func TestHostImportIntoHostCache(t *testing.T) {
	s := newTestFileServer(t)
	p := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(p, []byte("from the host"), 0o644)

	rec := httptest.NewRecorder()
	s.handleFilesAPI(rec, newHostImportRequest(p, filepath.Join(t.TempDir(), "missing.txt")))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	files := s.listFiles()
	if len(files) != 1 || files[0].Name != "notes.txt" {
		t.Fatalf("listFiles() = %+v", files)
	}
	f, _ := os.Open(filepath.Join(s.fileCacheDir, files[0].ID))
	defer f.Close()
	if b, _ := io.ReadAll(f); string(b) != "from the host" {
		t.Fatalf("copied content = %q", b)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the host file should be left in place: %v", err)
	}
}

// TestHostImportGatedLikeUpload: through agent-coordinator's transparent
// proxy the import is refused, through its upload relay it's allowed.
func TestHostImportGatedLikeUpload(t *testing.T) {
	s := newTestFileServer(t)
	p := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(p, []byte("x"), 0o644)

	req := newHostImportRequest(p)
	req.Header.Set(proxiedHeader, acRelayStamp)
	rec := httptest.NewRecorder()
	s.handleFilesAPI(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("proxied import: status = %d, want 403", rec.Code)
	}

	req = newHostImportRequest(p)
	req.Header.Set(proxiedHeader, acRelayStamp)
	req.Header.Set(relayedUploadHeader, acRelayStamp)
	rec = httptest.NewRecorder()
	s.handleFilesAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("relayed import: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHostImportNothingSaved: no readable file is an error naming why.
func TestHostImportNothingSaved(t *testing.T) {
	s := newTestFileServer(t)
	rec := httptest.NewRecorder()
	s.handleFilesAPI(rec, newHostImportRequest(t.TempDir()))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not a regular file") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
