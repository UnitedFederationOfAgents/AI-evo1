package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveRun: a finished run saves into the files tab as a .zip of its
// report, run.json and the files it saved, noting what's gone; the run then
// lists the saved file.
func TestSaveRun(t *testing.T) {
	s := newServer("test-lr")
	s.fileCacheDir = t.TempDir()
	s.hostStoreDir = t.TempDir()
	shot, err := s.saveFileFrom("shot.png", strings.NewReader("png bytes"))
	if err != nil {
		t.Fatal(err)
	}
	e := s.control
	e.mu.Lock()
	e.run = &ControlRunMsg{
		ID: "run1", Sequence: "seq", Name: "A sequence", Status: "running",
		Controls: map[string]string{"node": "n1"},
		Steps: []ControlStepResult{
			{ControlStepInfo: ControlStepInfo{Label: "First"}, Status: "success", Message: "did it"},
			{ControlStepInfo: ControlStepInfo{Label: "Second"}, Status: "error", Message: "broke"},
		},
		Output:     []ControlValue{{Label: "phrase", Value: "hello"}},
		Recordings: []ControlRecording{{Who: "n1", FileID: shot.ID, Name: "shot.png", Image: true}, {Who: recordLocalRobot, FileID: "gone_x.webm", Name: "x.webm", Video: true}},
	}
	e.mu.Unlock()

	if r := e.handleLibRequest(ControlLibRequest{Op: "save-run", ID: "run1"}); r.Success || !strings.Contains(r.Error, "still going") {
		t.Fatalf("saving a running run: %+v", r)
	}
	e.mu.Lock()
	e.run.Status, e.run.Error = "error", "step 2 (Second): broke"
	e.mu.Unlock()
	if r := e.handleLibRequest(ControlLibRequest{Op: "save-run", ID: "other"}); r.Success {
		t.Fatalf("saving a run that isn't kept: %+v", r)
	}

	r := e.handleLibRequest(ControlLibRequest{Op: "save-run", ID: "run1"})
	if !r.Success || !strings.HasPrefix(r.Filename, "lr-control-seq-") || !strings.HasSuffix(r.Filename, ".zip") {
		t.Fatalf("save: %+v", r)
	}
	saved := e.state().Run.Saved
	if len(saved) != 1 || saved[0].Name != r.Filename {
		t.Fatalf("run.saved = %+v", saved)
	}

	data, err := os.ReadFile(filepath.Join(s.fileCacheDir, saved[0].FileID))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	if got["files/shot.png"] != "png bytes" {
		t.Errorf("the screenshot wasn't included: %v", got)
	}
	if _, ok := got["run.json"]; !ok {
		t.Error("no run.json")
	}
	report := got["report.txt"]
	for _, want := range []string{"A sequence (seq)", "ERROR", "node: n1", "[success] First", "[error] Second", "broke", "phrase: hello", "x.webm", "not included"} {
		if !strings.Contains(report, want) {
			t.Errorf("report.txt lacks %q:\n%s", want, report)
		}
	}
}

func TestSaveRunWithoutRun(t *testing.T) {
	s := newServer("test-lr")
	if r := s.control.handleLibRequest(ControlLibRequest{Op: "save-run"}); r.Success {
		t.Fatalf("saving with no run: %+v", r)
	}
}

func TestUniqueZipName(t *testing.T) {
	used := map[string]bool{}
	for _, want := range []string{"files/a.png", "files/a-2.png", "files/a-3.png"} {
		if got := uniqueZipName("files/a.png", used); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
