package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFileImportControlSequence covers the files tab's "import" on a control
// sequence: it lands in the control tab's library.
func TestFileImportControlSequence(t *testing.T) {
	s := newTestFileServer(t)
	id := "aaaaaaaa_ctl.yaml"
	if err := os.WriteFile(filepath.Join(s.fileCacheDir, id), []byte(testControlSeqYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.control.lib.seqRev("hello"); ok {
		t.Fatal("sequence hello is already in the library")
	}

	rec := httptest.NewRecorder()
	s.handleFileItem(rec, httptest.NewRequest(http.MethodPost, "/api/files/"+id+"/import", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("import: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "imported") {
		t.Errorf("import: body = %s, want an \"imported\" message", rec.Body.String())
	}
	if _, ok := s.control.lib.seqRev("hello"); !ok {
		t.Error("sequence hello isn't in the library after the import")
	}
}

// TestFileImportRejectsNonSequence checks a file that isn't a sequence YAML
// is refused, and a missing one is a 404.
func TestFileImportRejectsNonSequence(t *testing.T) {
	s := newTestFileServer(t)
	id := "bbbbbbbb_plain.yaml"
	if err := os.WriteFile(filepath.Join(s.fileCacheDir, id), []byte("a: b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleFileItem(rec, httptest.NewRequest(http.MethodPost, "/api/files/"+id+"/import?run=1", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("plain yaml: status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	rec = httptest.NewRecorder()
	s.handleFileItem(rec, httptest.NewRequest(http.MethodPost, "/api/files/cccccccc_gone.yaml/import", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing file: status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestFileImportRobotNeedsRobot checks a robot sequence's import fails
// cleanly while IANAR isn't connected.
func TestFileImportRobotNeedsRobot(t *testing.T) {
	s := newTestFileServer(t)
	if _, err := s.importRobotSequence(testRobotSeqYAML, false); err == nil || !strings.Contains(err.Error(), "isn't connected") {
		t.Errorf("err = %v, want \"isn't connected\"", err)
	}
}

// TestNoteRobotSeqImportDelivers checks IANAR's answer reaches the import
// waiting on it, and one nobody waits for is dropped.
func TestNoteRobotSeqImportDelivers(t *testing.T) {
	s := newTestFileServer(t)
	ch := make(chan RobotSeqImportMsg, 1)
	s.robotImports = map[string]chan RobotSeqImportMsg{"r1": ch}
	s.noteRobotSeqImport(RobotSeqImportMsg{Req: "r1", Success: true, Message: "imported: added sequence roundtrip"})
	s.noteRobotSeqImport(RobotSeqImportMsg{Req: "nobody"})
	select {
	case got := <-ch:
		if !got.Success || got.Req != "r1" {
			t.Errorf("got %+v", got)
		}
	default:
		t.Fatal("the answer wasn't delivered")
	}
}
