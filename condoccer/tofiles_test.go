package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestResourceOriginalName(t *testing.T) {
	for in, want := range map[string]string{
		"43e77743_agent-resource-flow.svg": "agent-resource-flow.svg",
		"117e9380_run.zip":                 "run.zip",
		"plain.txt":                        "plain.txt",
		"nothex12_x.txt":                   "nothex12_x.txt",
		"43e77743_":                        "43e77743_",
	} {
		if got := resourceOriginalName(in); got != want {
			t.Errorf("resourceOriginalName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckToFilesSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aaaaaaaa_ok.yaml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "aaaaaaaa_run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkToFilesSource(dir, "aaaaaaaa_ok.yaml"); err != nil {
		t.Errorf("valid file: %v", err)
	}
	for _, bad := range []string{"", "../x", "a/b", ".hidden", "missing.txt", "aaaaaaaa_run"} {
		if err := checkToFilesSource(dir, bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestUploadToFiles(t *testing.T) {
	dir := t.TempDir()
	content := "format: lr-control-v1\n"
	if err := os.WriteFile(filepath.Join(dir, "abcdef01_seq.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotName, gotContent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/files" {
			http.NotFound(w, r)
			return
		}
		f, fh, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		gotName, gotContent = fh.Filename, string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"files":[]}`))
	}))
	defer srv.Close()

	name, err := uploadToFiles(srv.URL, dir, "abcdef01_seq.yaml")
	if err != nil {
		t.Fatalf("uploadToFiles: %v", err)
	}
	if name != "seq.yaml" || gotName != "seq.yaml" {
		t.Errorf("uploaded as %q (LR saw %q), want seq.yaml", name, gotName)
	}
	if gotContent != content {
		t.Errorf("LR got content %q, want %q", gotContent, content)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no file could be saved", http.StatusInternalServerError)
	}))
	defer failing.Close()
	if _, err := uploadToFiles(failing.URL, dir, "abcdef01_seq.yaml"); err == nil {
		t.Error("expected an error from a failing local-representative")
	}
}

func TestFindInFiles(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"abcdef01_seq.yaml":  "steps: []\n",
		"abcdef02_diff.yaml": "steps: [a]\n",
		"abcdef03_gone.txt":  "x",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// LR's files tab: seq.yaml twice (newest first), diff.yaml with the same
	// size but other bytes, and nothing named gone.txt.
	lrFiles := map[string]string{
		"11111111_seq.yaml":  "steps: []\n",
		"22222222_seq.yaml":  "steps: []\n",
		"33333333_diff.yaml": "steps: [b]\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/files" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"files":[` +
				`{"id":"11111111_seq.yaml","name":"seq.yaml","size":10},` +
				`{"id":"22222222_seq.yaml","name":"seq.yaml","size":10},` +
				`{"id":"33333333_diff.yaml","name":"diff.yaml","size":11}]}`))
			return
		}
		content, ok := lrFiles[filepath.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(content))
	}))
	defer srv.Close()

	present, err := findInFiles(srv.URL, dir, []string{"abcdef01_seq.yaml", "abcdef02_diff.yaml", "abcdef03_gone.txt"})
	if err != nil {
		t.Fatalf("findInFiles: %v", err)
	}
	if len(present) != 1 || present["abcdef01_seq.yaml"] != "11111111_seq.yaml" {
		t.Errorf("present = %v, want only abcdef01_seq.yaml -> 11111111_seq.yaml", present)
	}
}
