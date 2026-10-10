package main

import (
	"archive/zip"
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
	links, err := fetchHighlightedFilesFrom(srv.URL, destDir, nil)
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

	links, err := fetchHighlightedFilesFrom(srv.URL, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("expected no links, got %+v", links)
	}
}

// fakeFilesTab serves a files tab at prefix ("" or "/host/<node>") on mux,
// listing files (id -> highlighted) with each file's content being its id.
func fakeFilesTab(mux *http.ServeMux, prefix string, files map[string]bool) {
	mux.HandleFunc(prefix+"/api/files", func(w http.ResponseWriter, r *http.Request) {
		var list []map[string]interface{}
		for id, hl := range files {
			list = append(list, map[string]interface{}{"id": id, "name": strings.SplitN(id, "_", 2)[1], "highlighted": hl})
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"files": list})
	})
	mux.HandleFunc(prefix+"/api/files/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.TrimPrefix(r.URL.Path, prefix+"/api/files/")))
	})
}

// TestFetchAllHighlightedFilesIncludesPeers verifies the "Highlighted"
// source (Revision K) copies the highlighted files of this LR and of every
// peer LR it reports, labels peer files with their node, skips an id
// already copied, and carries on past an unreachable peer.
func TestFetchAllHighlightedFilesIncludesPeers(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	fakeFilesTab(mux, "", map[string]bool{"aaa_local.txt": true, "bbb_off.txt": false})
	fakeFilesTab(mux, "/host/node-b", map[string]bool{"ccc_peer.txt": true, "aaa_local.txt": true})
	mux.HandleFunc("/api/file-peers", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"node": "node-a",
			"peers": []highlightedSource{
				{Node: "node-b", Base: srv.URL + "/host/node-b"},
				{Node: "node-c", Base: srv.URL + "/host/node-c"}, // 404s
			},
		})
	})

	destDir := t.TempDir()
	links, err := fetchAllHighlightedFiles(srv.URL, destDir)
	if err != nil {
		t.Fatalf("fetchAllHighlightedFiles: %v", err)
	}
	want := []resourceLink{
		{Name: "local.txt", Filename: "aaa_local.txt"},
		{Name: "peer.txt (node-b)", Filename: "ccc_peer.txt"},
	}
	if len(links) != len(want) || links[0] != want[0] || links[1] != want[1] {
		t.Fatalf("links = %+v, want %+v", links, want)
	}
	if got, err := os.ReadFile(filepath.Join(destDir, "ccc_peer.txt")); err != nil || string(got) != "ccc_peer.txt" {
		t.Errorf("peer file = %q, %v", got, err)
	}
}

// TestFetchAllHighlightedFilesOlderLR verifies an LR without
// GET /api/file-peers still gives its own highlighted files.
func TestFetchAllHighlightedFilesOlderLR(t *testing.T) {
	mux := http.NewServeMux()
	fakeFilesTab(mux, "", map[string]bool{"aaa_local.txt": true})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	links, err := fetchAllHighlightedFiles(srv.URL, t.TempDir())
	if err != nil || len(links) != 1 || links[0].Filename != "aaa_local.txt" {
		t.Fatalf("links = %+v, err = %v", links, err)
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

// TestAddVoiceNoteResource verifies the "Voice Note" source (Revision B of
// Step2Prompt.md) inserts a "## Resource N" block carrying the dictated text
// as its description, with no linked files at all -- unlike "Highlighted"
// and "Upload", it never touches the condoc's Impls folder, connected
// local-representative or not.
func TestAddVoiceNoteResource(t *testing.T) {
	root, mainRelPath, stepPath := newUploadResourceFixture(t)
	s := newServer(root)
	info := CondocInfo{Name: "X", StepFile: "condocs/xImpls/Step1Prompt.md"}

	err := s.addResource(filepath.Join(s.root, mainRelPath), info, ActionRequest{
		ResourceType: "voice-note",
		ResourceName: "Meeting recap",
		Content:      "we agreed to ship on Friday",
	})
	if err != nil {
		t.Fatalf("addResource: %v", err)
	}

	got, err := os.ReadFile(stepPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(got)
	if !strings.Contains(content, "## Resource 1 -- Meeting recap") {
		t.Errorf("expected resource heading, got:\n%s", content)
	}
	if !strings.Contains(content, "we agreed to ship on Friday") {
		t.Errorf("expected dictated text, got:\n%s", content)
	}

	implDirPath := filepath.Dir(stepPath)
	entries, err := os.ReadDir(implDirPath)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's own step file lives there; a voice note must add nothing else.
	for _, e := range entries {
		if e.Name() != filepath.Base(stepPath) {
			t.Errorf("expected no files written to the Impls folder besides the step file, got %q", e.Name())
		}
	}
}

// TestAddVoiceNoteResourceRequiresContent verifies an empty (or
// whitespace-only) transcript is rejected rather than inserting an empty
// resource block.
func TestAddVoiceNoteResourceRequiresContent(t *testing.T) {
	s := newServer(t.TempDir())
	info := CondocInfo{Name: "X", StepFile: "condocs/xImpls/Step1Prompt.md"}
	err := s.addResource(filepath.Join(s.root, "condocs", "X.md"), info, ActionRequest{ResourceType: "voice-note", Content: "   "})
	if err == nil {
		t.Fatal("expected an error for an empty voice note")
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

// buildZip returns a zip archive holding the given name -> content entries
// (a name ending in "/" becomes a directory entry).
func buildZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestExtractZipResource verifies a zip is unpacked into a sibling folder
// named after it minus the extension, keeping nested paths (Revision F of
// condocs/initialRobotImpls/Step2Prompt.md).
func TestExtractZipResource(t *testing.T) {
	dir := t.TempDir()
	data := buildZip(t, map[string]string{
		"report.txt":               "all good",
		"recording/":               "",
		"recording/recording.webm": "webm bytes",
	})
	if err := os.WriteFile(filepath.Join(dir, "abcd1234_run.ZIP"), data, 0644); err != nil {
		t.Fatal(err)
	}
	note, err := extractZipResource(dir, "abcd1234_run.ZIP")
	if err != nil {
		t.Fatalf("extractZipResource: %v", err)
	}
	if note != "unzipped to abcd1234_run/" {
		t.Errorf("note = %q, want %q", note, "unzipped to abcd1234_run/")
	}
	for rel, want := range map[string]string{
		"report.txt":               "all good",
		"recording/recording.webm": "webm bytes",
	} {
		got, err := os.ReadFile(filepath.Join(dir, "abcd1234_run", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("expected %s to be extracted: %v", rel, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "abcd1234_run.ZIP")); err != nil {
		t.Errorf("the zip itself should be kept: %v", err)
	}
}

// TestExtractZipResourceIgnoresNonZip verifies other files are left alone.
func TestExtractZipResourceIgnoresNonZip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if note, err := extractZipResource(dir, "note.txt"); err != nil || note != "" {
		t.Fatalf("extractZipResource = %q, %v; want no note and no error", note, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected only note.txt, got %d entries", len(entries))
	}
}

// TestExtractZipResourceRejectsTraversal verifies an entry that would land
// outside the extraction folder fails the extraction and leaves no folder.
func TestExtractZipResourceRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	data := buildZip(t, map[string]string{"../evil.txt": "nope"})
	if err := os.WriteFile(filepath.Join(dir, "bad.zip"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := extractZipResource(dir, "bad.zip"); err == nil {
		t.Fatal("expected an error for a path-traversal entry")
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); !os.IsNotExist(err) {
		t.Error("traversal entry should not have been written")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad")); !os.IsNotExist(err) {
		t.Error("partial extraction folder should have been removed")
	}
}

// TestExtractZipResourceInvalidZip verifies a corrupt .zip is reported
// rather than silently skipped.
func TestExtractZipResourceInvalidZip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.zip"), []byte("not a zip"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := extractZipResource(dir, "broken.zip"); err == nil {
		t.Fatal("expected an error for an invalid zip")
	}
}

// TestExtractZipResourceTooLarge verifies a zip whose contents exceed
// maxZipExtractSize isn't unpacked and isn't an error either: the note says
// it was too large, no folder is left behind, and the zip is kept (Revision E
// of condocs/initialRobotImpls/Step4Prompt.md).
func TestExtractZipResourceTooLarge(t *testing.T) {
	old := maxZipExtractSize
	maxZipExtractSize = 4
	defer func() { maxZipExtractSize = old }()

	dir := t.TempDir()
	data := buildZip(t, map[string]string{"big.txt": "more than four bytes"})
	if err := os.WriteFile(filepath.Join(dir, "big.zip"), data, 0644); err != nil {
		t.Fatal(err)
	}
	note, err := extractZipResource(dir, "big.zip")
	if err != nil {
		t.Fatalf("extractZipResource: %v", err)
	}
	if note != "too large to unzip: over 4 bytes uncompressed" {
		t.Errorf("note = %q", note)
	}
	if _, err := os.Stat(filepath.Join(dir, "big")); !os.IsNotExist(err) {
		t.Error("no extraction folder should be left for a too-large zip")
	}
	if _, err := os.Stat(filepath.Join(dir, "big.zip")); err != nil {
		t.Errorf("the zip itself should be kept: %v", err)
	}
}

// TestInsertResourceBlockWritesNote verifies a link's note lands as an
// indented "(note)" line right under it.
func TestInsertResourceBlockWritesNote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Step1Prompt.md")
	if err := os.WriteFile(path, []byte("# Prompt\n\nDo it.\n\n## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>"), 0644); err != nil {
		t.Fatal(err)
	}
	links := []resourceLink{
		{Name: "run.zip", Filename: "abcd1234_run.zip", Note: "unzipped to abcd1234_run/"},
		{Name: "x.txt", Filename: "id_x.txt"},
	}
	if err := insertResourceBlock(path, "", "", links); err != nil {
		t.Fatalf("insertResourceBlock: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "- [run.zip](abcd1234_run.zip)\n  (unzipped to abcd1234_run/)\n- [x.txt](id_x.txt)\n"
	if !strings.Contains(string(got), want) {
		t.Errorf("block missing %q:\n%s", want, got)
	}
}

// TestHandleUploadResourceExtractsZip verifies the "Upload" source unpacks
// an uploaded zip beside it in the Impls folder.
func TestHandleUploadResourceExtractsZip(t *testing.T) {
	root, mainRelPath, stepPath := newUploadResourceFixture(t)
	s := newServer(root)

	data := buildZip(t, map[string]string{"report.txt": "from the zip"})
	req := newUploadResourceRequest(t, mainRelPath, "", "", "run.zip", string(data))
	rec := httptest.NewRecorder()
	s.handleUploadResource(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}

	implDirPath := filepath.Dir(stepPath)
	entries, err := os.ReadDir(implDirPath)
	if err != nil {
		t.Fatal(err)
	}
	var folder string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), "_run") {
			folder = e.Name()
		}
	}
	if folder == "" {
		t.Fatal("expected the uploaded zip to be extracted into a <id>_run folder")
	}
	got, err := os.ReadFile(filepath.Join(implDirPath, folder, "report.txt"))
	if err != nil || string(got) != "from the zip" {
		t.Errorf("extracted report.txt = %q, %v", got, err)
	}
}

// TestFetchHighlightedFilesFromExtractsZip verifies the "Highlighted"
// source unpacks a zip copied from local-representative.
func TestFetchHighlightedFilesFromExtractsZip(t *testing.T) {
	data := buildZip(t, map[string]string{"step-1.jpg": "jpeg bytes"})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/files", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"files": []map[string]interface{}{
				{"id": "aaa_seq.zip", "name": "seq.zip", "highlighted": true},
			},
		})
	})
	mux.HandleFunc("/api/files/aaa_seq.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	destDir := t.TempDir()
	links, err := fetchHighlightedFilesFrom(srv.URL, destDir, nil)
	if err != nil {
		t.Fatalf("fetchHighlightedFilesFrom: %v", err)
	}
	if len(links) != 1 || links[0].Note != "unzipped to aaa_seq/" {
		t.Errorf("links = %+v, want one with note %q", links, "unzipped to aaa_seq/")
	}
	got, err := os.ReadFile(filepath.Join(destDir, "aaa_seq", "step-1.jpg"))
	if err != nil || string(got) != "jpeg bytes" {
		t.Errorf("extracted step-1.jpg = %q, %v", got, err)
	}
}
