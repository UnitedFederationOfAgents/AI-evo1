package main

import (
	"bytes"
	"fmt"
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
	s.hostStoreDir = t.TempDir()
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

// TestFileUploadAllowedWhenRelayedByAC verifies the one exception to
// TestFileUploadRejectedWhenProxied: a request carrying both proxiedHeader
// and a correctly-stamped relayedUploadHeader -- agent-coordinator's
// dedicated upload-relay route, not its transparent passthrough -- is
// accepted (see docs/DistributedExchange.md, Path 1).
func TestFileUploadAllowedWhenRelayedByAC(t *testing.T) {
	s := newTestFileServer(t)

	req := newUploadRequest(t, "hello.txt", []byte("hello"))
	req.Header.Set(proxiedHeader, acRelayStamp)
	req.Header.Set(relayedUploadHeader, acRelayStamp)
	rec := httptest.NewRecorder()
	s.handleFilesAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("relayed upload: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if files := s.listFiles(); len(files) != 1 {
		t.Fatalf("relayed upload should have written a file, got %+v", files)
	}
}

// TestFileUploadRejectedWithWrongRelayStamp verifies the exception requires
// the exact expected stamp, not merely the header's presence.
func TestFileUploadRejectedWithWrongRelayStamp(t *testing.T) {
	s := newTestFileServer(t)

	req := newUploadRequest(t, "hello.txt", []byte("hello"))
	req.Header.Set(proxiedHeader, acRelayStamp)
	req.Header.Set(relayedUploadHeader, "someone-else")
	rec := httptest.NewRecorder()
	s.handleFilesAPI(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong relay stamp: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if files := s.listFiles(); len(files) != 0 {
		t.Fatalf("wrongly-stamped upload should not have written a file, got %+v", files)
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

// uploadOne is a small helper that uploads one file and returns its listFiles
// ID, for tests that go on to exercise hold/persist/delete.
func uploadOne(t *testing.T, s *Server, filename string, content []byte) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleFilesAPI(rec, newUploadRequest(t, filename, content))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	files := s.listFiles()
	if len(files) != 1 {
		t.Fatalf("listFiles() after upload = %d files, want 1", len(files))
	}
	return files[0].ID
}

// TestHandleFileHold covers the file-details dialog's "hold" button end to
// end through handleFileItem's routing: the entry stays in the host-cache,
// but its state flips to "held" and its expiry jumps out to holdTTL.
func TestHandleFileHold(t *testing.T) {
	s := newTestFileServer(t)
	id := uploadOne(t, s, "hello.txt", []byte("hello"))

	req := httptest.NewRequest(http.MethodPost, "/api/files/"+id+"/hold", nil)
	rec := httptest.NewRecorder()
	s.handleFileItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("hold: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	files := s.listFiles()
	if len(files) != 1 || files[0].State != "held" {
		t.Fatalf("listFiles() after hold = %+v, want one file with state \"held\"", files)
	}
	wantExpiry := time.Now().Add(holdTTL)
	if diff := wantExpiry.Sub(time.Unix(files[0].ExpiresAt, 0)); diff < -time.Minute || diff > time.Minute {
		t.Errorf("held expires_at = %v, want ~%v (holdTTL from now)", time.Unix(files[0].ExpiresAt, 0), wantExpiry)
	}

	if _, err := os.Stat(filepath.Join(s.fileCacheDir, manifestName(id))); err != nil {
		t.Errorf("hold should have (re)written the manifest sidecar: %v", err)
	}
}

// TestHandleFileHoldUnknownID verifies holding an id that isn't in the
// host-cache 404s instead of fabricating an entry.
func TestHandleFileHoldUnknownID(t *testing.T) {
	s := newTestFileServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/files/nope/hold", nil)
	rec := httptest.NewRecorder()
	s.handleFileItem(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("hold unknown id: status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandleFilePersist covers the file-details dialog's "persist" button:
// the entry moves out of the host-cache into the host-store, its state
// flips to "persisted", and its host-cache manifest sidecar is dropped
// (a host-store entry needs none).
func TestHandleFilePersist(t *testing.T) {
	s := newTestFileServer(t)
	id := uploadOne(t, s, "hello.txt", []byte("hello world"))

	req := httptest.NewRequest(http.MethodPost, "/api/files/"+id+"/persist", nil)
	rec := httptest.NewRecorder()
	s.handleFileItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("persist: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(filepath.Join(s.fileCacheDir, id)); !os.IsNotExist(err) {
		t.Errorf("persisted file should be gone from host-cache, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.hostStoreDir, id)); err != nil {
		t.Errorf("persisted file should be in host-store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.fileCacheDir, manifestName(id))); !os.IsNotExist(err) {
		t.Errorf("persist should have dropped the host-cache manifest sidecar, stat err = %v", err)
	}

	files := s.listFiles()
	if len(files) != 1 || files[0].State != "persisted" || files[0].ExpiresAt != 0 {
		t.Fatalf("listFiles() after persist = %+v, want one file, state \"persisted\", expires_at 0", files)
	}

	// A persisted file's bytes are still reachable through the raw endpoint.
	rawReq := httptest.NewRequest(http.MethodGet, "/api/files/"+id, nil)
	rawRec := httptest.NewRecorder()
	s.handleFileItem(rawRec, rawReq)
	if rawRec.Code != http.StatusOK || rawRec.Body.String() != "hello world" {
		t.Errorf("raw view of persisted file: status = %d, body = %q", rawRec.Code, rawRec.Body.String())
	}
}

// TestHandleFileDelete covers the file-details dialog's "delete" button for
// a plain cached entry: the file and its manifest sidecar are both removed.
func TestHandleFileDelete(t *testing.T) {
	s := newTestFileServer(t)
	id := uploadOne(t, s, "hello.txt", []byte("hello"))

	req := httptest.NewRequest(http.MethodDelete, "/api/files/"+id, nil)
	rec := httptest.NewRecorder()
	s.handleFileItem(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if files := s.listFiles(); len(files) != 0 {
		t.Fatalf("listFiles() after delete = %+v, want none", files)
	}
	if _, err := os.Stat(filepath.Join(s.fileCacheDir, manifestName(id))); !os.IsNotExist(err) {
		t.Errorf("delete should have removed the manifest sidecar too, stat err = %v", err)
	}
}

// TestHandleFileDeletePersisted verifies delete also works on a persisted
// (host-store) entry, not just a host-cache one.
func TestHandleFileDeletePersisted(t *testing.T) {
	s := newTestFileServer(t)
	id := uploadOne(t, s, "hello.txt", []byte("hello"))
	s.handleFileItem(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/files/"+id+"/persist", nil))

	req := httptest.NewRequest(http.MethodDelete, "/api/files/"+id, nil)
	rec := httptest.NewRecorder()
	s.handleFileItem(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete persisted: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.hostStoreDir, id)); !os.IsNotExist(err) {
		t.Errorf("delete should have removed the host-store entry, stat err = %v", err)
	}
}

// TestHandleFileItemUnknownAction verifies a POST to an action other than
// "hold"/"persist" 404s rather than silently doing nothing.
func TestHandleFileItemUnknownAction(t *testing.T) {
	s := newTestFileServer(t)
	id := uploadOne(t, s, "hello.txt", []byte("hello"))
	req := httptest.NewRequest(http.MethodPost, "/api/files/"+id+"/frobnicate", nil)
	rec := httptest.NewRecorder()
	s.handleFileItem(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action: status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestSweepHeldFileSurvivesPastFileCacheTTL verifies a held file is not swept
// just because it's older than the plain fileCacheTTL -- its manifest's own
// (later) expires_at governs instead.
func TestSweepHeldFileSurvivesPastFileCacheTTL(t *testing.T) {
	s := newTestFileServer(t)
	id := "aaaaaaaa_held.txt"
	path := filepath.Join(s.fileCacheDir, id)
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * fileCacheTTL)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	writeTestManifest(t, s.fileCacheDir, id, true, time.Now().Add(holdTTL))

	s.sweepExpiredFiles()

	if files := s.listFiles(); len(files) != 1 || files[0].State != "held" {
		t.Fatalf("listFiles() after sweep = %+v, want the held file to survive", files)
	}
}

// TestSweepRemovesHeldFileAfterHoldExpiry verifies a held file IS swept once
// its own (manifest-recorded) expiry has passed.
func TestSweepRemovesHeldFileAfterHoldExpiry(t *testing.T) {
	s := newTestFileServer(t)
	id := "aaaaaaaa_held.txt"
	path := filepath.Join(s.fileCacheDir, id)
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestManifest(t, s.fileCacheDir, id, true, time.Now().Add(-time.Minute))

	s.sweepExpiredFiles()

	if files := s.listFiles(); len(files) != 0 {
		t.Fatalf("listFiles() after sweep = %+v, want the expired held file gone", files)
	}
}

// TestSweepNeverTouchesHostStore verifies a persisted (host-store) entry is
// left alone by the sweep no matter its age -- the sweep only ever reads the
// host-cache directory.
func TestSweepNeverTouchesHostStore(t *testing.T) {
	s := newTestFileServer(t)
	id := "aaaaaaaa_persisted.txt"
	path := filepath.Join(s.hostStoreDir, id)
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	veryOld := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(path, veryOld, veryOld); err != nil {
		t.Fatal(err)
	}

	s.sweepExpiredFiles()

	if files := s.listFiles(); len(files) != 1 || files[0].State != "persisted" {
		t.Fatalf("listFiles() after sweep = %+v, want the host-store entry untouched", files)
	}
}

// writeTestManifest writes a manifest sidecar directly (bypassing
// writeManifest/handleFileHold) so sweep tests can set an arbitrary
// held/expires_at without waiting on holdTTL in real time.
func writeTestManifest(t *testing.T, dir, id string, held bool, expiresAt time.Time) {
	t.Helper()
	body := fmt.Sprintf("id: %q\nheld: %t\nexpires_at: %s\n", id, held, expiresAt.UTC().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(dir, manifestName(id)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
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
