package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNextResourceNum verifies the "## Resource (N)" numbering picks up
// after the highest one already present, starting from 1 in an empty file.
func TestNextResourceNum(t *testing.T) {
	if got := nextResourceNum(""); got != 1 {
		t.Errorf("empty content: got %d, want 1", got)
	}
	content := "## Resource (1)\n\nfoo\n\n## Resource (3)\n\nbar\n"
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
	if err := insertResourceBlock(path, "why these matter", links); err != nil {
		t.Fatalf("insertResourceBlock: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(got)

	want := "# Prompt\n\nDo the thing.\n\n## Reply\n\nDone.\n\n" +
		"## Resource (1)\n\nwhy these matter\n\n- [report.pdf](abcd1234_report.pdf)\n\n" +
		"## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>"
	if content != want {
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
	if err := insertResourceBlock(path, "   ", links); err != nil {
		t.Fatalf("insertResourceBlock: %v", err)
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "## Resource (1)\n\n\n") {
		t.Errorf("expected no blank-description gap, got:\n%s", content)
	}
	want := "## Reply\n\nDone.\n\n## Resource (1)\n\n- [x.txt](id_x.txt)\n\n## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>"
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

	err := insertResourceBlock(path, "", []resourceLink{{Name: "x", Filename: "y"}})
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
