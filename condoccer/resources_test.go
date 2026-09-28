package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNextResourceNum verifies the "## Resource N" numbering picks up
// after the highest one already present, starting from 1 in an empty file,
// whether or not a block carries a " -- <name>" suffix (Revision B).
func TestNextResourceNum(t *testing.T) {
	if got := nextResourceNum(""); got != 1 {
		t.Errorf("empty content: got %d, want 1", got)
	}
	content := "## Resource 1\n\nfoo\n\n## Resource 3 -- Screenshots\n\nbar\n"
	if got := nextResourceNum(content); got != 4 {
		t.Errorf("got %d, want 4", got)
	}
}

// TestInsertResourceBlockAboveDPlaceholder verifies the block lands
// immediately above the pending "## <REPLACE-Revision|Retry> X" placeholder,
// with blank-line spacing on both sides, and is numbered starting at 1.
func TestInsertResourceBlockAboveDPlaceholder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Step1Prompt.md")
	original := "# Prompt\n\nDo the thing.\n\n## Reply\n\nDone.\n\n## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	links := []resourceLink{{Name: "report.pdf", Filename: "abcd1234_report.pdf"}}
	if err := insertResourceBlock(path, "", "why these matter", links); err != nil {
		t.Fatalf("insertResourceBlock: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(got)

	want := "# Prompt\n\nDo the thing.\n\n## Reply\n\nDone.\n\n" +
		"## Resource 1\n\nwhy these matter\n\n- [report.pdf](abcd1234_report.pdf)\n\n" +
		"## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>"
	if content != want {
		t.Errorf("content mismatch:\ngot:\n%s\nwant:\n%s", content, want)
	}
}

// TestInsertResourceBlockWithName verifies a non-empty name is appended to
// the heading as "## Resource N -- <name>" (Revision B).
func TestInsertResourceBlockWithName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Step1Prompt.md")
	original := "## Reply\n\nDone.\n\n## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>"
	os.WriteFile(path, []byte(original), 0644)

	links := []resourceLink{{Name: "x.txt", Filename: "id_x.txt"}}
	if err := insertResourceBlock(path, "  Screenshots  ", "", links); err != nil {
		t.Fatalf("insertResourceBlock: %v", err)
	}
	content, _ := os.ReadFile(path)
	want := "## Reply\n\nDone.\n\n## Resource 1 -- Screenshots\n\n- [x.txt](id_x.txt)\n\n## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>"
	if string(content) != want {
		t.Errorf("content mismatch:\ngot:\n%s\nwant:\n%s", content, want)
	}
}

// TestInsertResourceBlockNoDescription verifies an empty description is
// simply omitted, rather than leaving a blank line in its place.
func TestInsertResourceBlockNoDescription(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Step1Prompt.md")
	original := "## Reply\n\nDone.\n\n## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>"
	os.WriteFile(path, []byte(original), 0644)

	links := []resourceLink{{Name: "x.txt", Filename: "id_x.txt"}}
	if err := insertResourceBlock(path, "", "   ", links); err != nil {
		t.Fatalf("insertResourceBlock: %v", err)
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "## Resource 1\n\n\n") {
		t.Errorf("expected no blank-description gap, got:\n%s", content)
	}
	want := "## Reply\n\nDone.\n\n## Resource 1\n\n- [x.txt](id_x.txt)\n\n## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>"
	if string(content) != want {
		t.Errorf("content mismatch:\ngot:\n%s\nwant:\n%s", content, want)
	}
}

// TestInsertResourceBlockNoPlaceholder verifies a clear error when the file
// has no pending revision/retry placeholder to insert above.
func TestInsertResourceBlockNoPlaceholder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Step1Prompt.md")
	os.WriteFile(path, []byte("# Prompt\n\n## Reply\n\nDone.\n"), 0644)

	err := insertResourceBlock(path, "", "", []resourceLink{{Name: "x", Filename: "y"}})
	if err == nil {
		t.Fatal("expected an error when no placeholder is present")
	}
}

// TestFetchHighlightedFilesFrom exercises the HTTP round trip against a fake
// local-representative: it should list files, skip non-highlighted ones, and
// copy the highlighted ones' bytes into destDir under their existing id.
func TestFetchHighlightedFilesFrom(t *testing.T) {
	fileBytes := map[string]string{
		"aaa_keep.txt": "keep me",
		"bbb_skip.txt": "skip me",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"files": []map[string]interface{}{
				{"id": "aaa_keep.txt", "name": "keep.txt", "highlighted": true},
				{"id": "bbb_skip.txt", "name": "skip.txt", "highlighted": false},
			},
		})
	})
	mux.HandleFunc("/api/files/aaa_keep.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fileBytes["aaa_keep.txt"]))
	})
	mux.HandleFunc("/api/files/bbb_skip.txt", func(w http.ResponseWriter, r *http.Request) {
		t.Error("should never fetch a non-highlighted file's content")
		w.Write([]byte(fileBytes["bbb_skip.txt"]))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	destDir := t.TempDir()
	links, err := fetchHighlightedFilesFrom(srv.URL, destDir)
	if err != nil {
		t.Fatalf("fetchHighlightedFilesFrom: %v", err)
	}
	if len(links) != 1 || links[0].Name != "keep.txt" || links[0].Filename != "aaa_keep.txt" {
		t.Fatalf("unexpected links: %+v", links)
	}
	got, err := os.ReadFile(filepath.Join(destDir, "aaa_keep.txt"))
	if err != nil {
		t.Fatalf("expected the highlighted file to be copied: %v", err)
	}
	if string(got) != "keep me" {
		t.Errorf("copied content = %q, want %q", got, "keep me")
	}
	if _, err := os.Stat(filepath.Join(destDir, "bbb_skip.txt")); !os.IsNotExist(err) {
		t.Error("non-highlighted file should not have been copied")
	}
}

// TestFetchHighlightedFilesFromNoneHighlighted verifies an empty (not nil
// error) result when local-representative has files but none highlighted.
func TestFetchHighlightedFilesFromNoneHighlighted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"files": []map[string]interface{}{
				{"id": "aaa", "name": "a.txt", "highlighted": false},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	links, err := fetchHighlightedFilesFrom(srv.URL, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("expected no links, got %+v", links)
	}
}

// TestAddResourceRequiresConnection verifies addResource fails clearly when
// condoccer isn't currently connected to local-representative, rather than
// panicking on a nil client.
func TestAddResourceRequiresConnection(t *testing.T) {
	s := newServer(t.TempDir())
	info := CondocInfo{Name: "X", StepFile: "condocs/xImpls/Step1Prompt.md"}
	err := s.addResource(filepath.Join(s.root, "condocs", "X.md"), info, ActionRequest{ResourceType: "highlighted"})
	if err == nil {
		t.Fatal("expected an error with no local-representative connection")
	}
}

// TestAddResourceUnknownType verifies an unrecognised resourceType is
// rejected before anything else runs.
func TestAddResourceUnknownType(t *testing.T) {
	s := newServer(t.TempDir())
	info := CondocInfo{Name: "X", StepFile: "condocs/xImpls/Step1Prompt.md"}
	err := s.addResource(filepath.Join(s.root, "condocs", "X.md"), info, ActionRequest{ResourceType: "bogus"})
	if err == nil {
		t.Fatal("expected an error for an unknown resource type")
	}
}

// TestParseIterationsResource verifies "## Resource N[ -- <name>]" headings
// get their own Iteration entry (Revision B) -- interleaved by position with
// Reply/Revision/Retry/Substep, rather than folded into whichever section
// precedes them.
func TestParseIterationsResource(t *testing.T) {
	content := "## Reply\n\nDone.\n\n" +
		"## Resource 1\n\nWhy this matters.\n\n- [a.png](aaa_a.png)\n\n" +
		"## Revision A\n\nDo more.\n\n" +
		"## Resource 2 -- Screenshots\n\n- [b.png](bbb_b.png)\n"

	got := parseIterations(content)
	want := []Iteration{
		{ID: "reply-initial", Label: "Reply", Type: "reply"},
		{ID: "resource-1", Label: "Resource 1", Type: "resource"},
		{ID: "revision-A", Label: "Revision A", Type: "revision"},
		{ID: "resource-2", Label: "Resource 2 | Screenshots", Type: "resource"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d iterations, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("iteration %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestHandleResourceFile verifies the /api/resource/<filename>?condoc=<path>
// route serves a resource file's raw bytes out of the right condoc's Impls
// folder, 404s on path-traversal attempts, and honors ?download=1 like
// local-representative's equivalent file route.
func TestHandleResourceFile(t *testing.T) {
	root := t.TempDir()
	implDirPath := filepath.Join(root, "condocs", "xImpls")
	if err := os.MkdirAll(implDirPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(implDirPath, "aaa_a.png"), []byte("pngbytes"), 0644); err != nil {
		t.Fatal(err)
	}
	s := newServer(root)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/resource/aaa_a.png?condoc=condocs/X.md", nil)
	s.handleResourceFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "pngbytes" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "pngbytes")
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline;") {
		t.Errorf("Content-Disposition = %q, want inline by default", got)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/resource/aaa_a.png?condoc=condocs/X.md&download=1", nil)
	s.handleResourceFile(rec2, req2)
	if got := rec2.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Errorf("Content-Disposition = %q, want attachment with ?download=1", got)
	}

	for _, badCondoc := range []string{"", "../../etc/passwd", "/etc/passwd"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/resource/aaa_a.png?condoc="+badCondoc, nil)
		s.handleResourceFile(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("condoc=%q: status = %d, want 404", badCondoc, rec.Code)
		}
	}

	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/api/resource/../secret?condoc=condocs/X.md", nil)
	s.handleResourceFile(rec3, req3)
	if rec3.Code != http.StatusNotFound {
		t.Errorf("path-traversal filename: status = %d, want 404", rec3.Code)
	}
}

// newUploadResourceFixture lays out the minimal condoc a test can point
// handleUploadResource at: a main file with one step, and that step's file
// awaiting action (a "## Human-Prompt" section and a pending
// "## <REPLACE-Revision|Retry> A" placeholder), mirroring the shape
// detectPhase and insertResourceBlock expect.
func newUploadResourceFixture(t *testing.T) (root, mainRelPath, stepPath string) {
	t.Helper()
	root = t.TempDir()
	condocDir := filepath.Join(root, "condocs")
	if err := os.MkdirAll(condocDir, 0755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(condocDir, "X.md")
	if err := os.WriteFile(mainPath, []byte("# X\n\n### Step 1 - Do it\n"), 0644); err != nil {
		t.Fatal(err)
	}
	implDirPath := filepath.Join(condocDir, "xImpls")
	if err := os.MkdirAll(implDirPath, 0755); err != nil {
		t.Fatal(err)
	}
	stepPath = filepath.Join(implDirPath, "Step1Prompt.md")
	stepContent := "## Human-Prompt\n\n## Reply\n\nDone.\n\n## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>"
	if err := os.WriteFile(stepPath, []byte(stepContent), 0644); err != nil {
		t.Fatal(err)
	}
	return root, "condocs/X.md", stepPath
}

// newUploadResourceRequest builds a multipart POST /api/upload-resource
// request carrying one file plus the given form fields.
func newUploadResourceRequest(t *testing.T, path, name, description, filename, fileContent string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range map[string]string{"path": path, "name": name, "description": description} {
		if v == "" {
			continue
		}
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if filename != "" {
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(fileContent)); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload-resource", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// TestHandleUploadResource verifies the "Upload" source of "Add Resources"
// (Revision C): a plain multipart POST with a real file body lands straight
// in the condoc's Impls folder and gets linked into a "## Resource N" block,
// without ever going through local-representative or its host-cache.
func TestHandleUploadResource(t *testing.T) {
	root, mainRelPath, stepPath := newUploadResourceFixture(t)
	s := newServer(root)

	req := newUploadResourceRequest(t, mainRelPath, "Screenshots", "why this matters", "note.txt", "hello upload")
	rec := httptest.NewRecorder()
	s.handleUploadResource(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}

	got, err := os.ReadFile(stepPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(got)
	if !strings.Contains(content, "## Resource 1 -- Screenshots") {
		t.Errorf("expected resource heading, got:\n%s", content)
	}
	if !strings.Contains(content, "why this matters") {
		t.Errorf("expected description, got:\n%s", content)
	}

	implDirPath := filepath.Dir(stepPath)
	entries, err := os.ReadDir(implDirPath)
	if err != nil {
		t.Fatal(err)
	}
	var uploaded string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "_note.txt") {
			uploaded = e.Name()
		}
	}
	if uploaded == "" {
		t.Fatal("expected uploaded file to land in the Impls folder")
	}
	b, err := os.ReadFile(filepath.Join(implDirPath, uploaded))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello upload" {
		t.Errorf("uploaded content = %q, want %q", b, "hello upload")
	}
	if !strings.Contains(content, uploaded) {
		t.Errorf("expected resource block to link %q, got:\n%s", uploaded, content)
	}

	if _, err := os.Stat(filepath.Join(root, ".condoc")); !os.IsNotExist(err) {
		t.Error("expected .condoc lock to be removed once the upload completes")
	}
}

// TestHandleUploadResourceRejectsNonPost verifies only POST is accepted.
func TestHandleUploadResourceRejectsNonPost(t *testing.T) {
	s := newServer(t.TempDir())
	req := httptest.NewRequest(http.MethodGet, "/api/upload-resource", nil)
	rec := httptest.NewRecorder()
	s.handleUploadResource(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// TestHandleUploadResourceRequiresFile verifies a request with no "file"
// part is rejected before touching the filesystem.
func TestHandleUploadResourceRequiresFile(t *testing.T) {
	root, mainRelPath, _ := newUploadResourceFixture(t)
	s := newServer(root)

	req := newUploadResourceRequest(t, mainRelPath, "", "", "", "")
	rec := httptest.NewRecorder()
	s.handleUploadResource(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleUploadResourceRejectsBadPath verifies an empty or path-traversal
// "path" field is rejected rather than resolved against the repo root.
func TestHandleUploadResourceRejectsBadPath(t *testing.T) {
	root, _, _ := newUploadResourceFixture(t)
	s := newServer(root)

	for _, badPath := range []string{"", "../../etc/passwd", "/etc/passwd"} {
		req := newUploadResourceRequest(t, badPath, "", "", "note.txt", "x")
		rec := httptest.NewRecorder()
		s.handleUploadResource(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("path=%q: status = %d, want 400", badPath, rec.Code)
		}
	}
}

// TestSaveUploadedResourceCollisionFree verifies two uploads sharing a
// filename land on disk as distinct entries, each keeping the original name
// for its resourceLink.Name.
func TestSaveUploadedResourceCollisionFree(t *testing.T) {
	destDir := t.TempDir()
	req1 := newUploadResourceRequest(t, "condocs/X.md", "", "", "dup.txt", "one")
	if err := req1.ParseMultipartForm(32 << 20); err != nil {
		t.Fatal(err)
	}
	req2 := newUploadResourceRequest(t, "condocs/X.md", "", "", "dup.txt", "two")
	if err := req2.ParseMultipartForm(32 << 20); err != nil {
		t.Fatal(err)
	}

	link1, err := saveUploadedResource(req1.MultipartForm.File["file"][0], destDir)
	if err != nil {
		t.Fatal(err)
	}
	link2, err := saveUploadedResource(req2.MultipartForm.File["file"][0], destDir)
	if err != nil {
		t.Fatal(err)
	}
	if link1.Filename == link2.Filename {
		t.Fatalf("expected distinct on-disk filenames, got %q twice", link1.Filename)
	}
	if link1.Name != "dup.txt" || link2.Name != "dup.txt" {
		t.Errorf("expected both links to keep the original name, got %q and %q", link1.Name, link2.Name)
	}
}

// TestSanitizeFilename verifies directory components are stripped so an
// upload's claimed filename can't escape the Impls folder.
func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"note.txt":              "note.txt",
		"../../etc/passwd":      "passwd",
		"..\\..\\windows\\x.txt": "x.txt",
		"":                      "file",
		".":                     "file",
		"..":                    "file",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}
