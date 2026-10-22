package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFetchHostResource: a host file is copied from LR's
// /api/host-files/raw under a collision-free id named after it, and LR's
// refusal comes back as the error.
func TestFetchHostResource(t *testing.T) {
	lr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/host-files/raw" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("path") != "/home/x/shot one.png" {
			http.Error(w, "open: no such file or directory", http.StatusNotFound)
			return
		}
		w.Write([]byte("png bytes"))
	}))
	defer lr.Close()
	dest := t.TempDir()

	link, err := fetchHostResource(lr.URL, "/home/x/shot one.png", dest)
	if err != nil {
		t.Fatal(err)
	}
	if link.Name != "shot one.png" || !strings.HasSuffix(link.Filename, "_shot one.png") {
		t.Fatalf("link = %+v", link)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, link.Filename)); string(b) != "png bytes" {
		t.Fatalf("content = %q", b)
	}

	if _, err := fetchHostResource(lr.URL, "/home/x/missing.png", dest); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("missing file: err = %v", err)
	}
}
