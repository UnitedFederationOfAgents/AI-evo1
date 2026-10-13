package main

import (
	"path/filepath"
	"testing"
)

func TestParseResourceArgs(t *testing.T) {
	tests := []struct {
		args, path, name string
		wantErr          bool
	}{
		{" report.pdf", "report.pdf", "", false},
		{` "my report.pdf" -name "Q3 report"`, "my report.pdf", "Q3 report", false},
		{" -name diagram ./out/d.png", "./out/d.png", "diagram", false},
		{"", "", "", true},
		{" a.txt b.txt", "", "", true},
		{" a.txt -name", "", "", true},
	}
	for _, tt := range tests {
		path, name, err := parseResourceArgs(tt.args)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseResourceArgs(%q) err = %v, wantErr %v", tt.args, err, tt.wantErr)
			continue
		}
		if path != tt.path || name != tt.name {
			t.Errorf("parseResourceArgs(%q) = (%q, %q), want (%q, %q)", tt.args, path, name, tt.path, tt.name)
		}
	}
}

func TestResolveUserPath(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	tests := map[string]string{
		"a.txt":       "/work/a.txt",
		"../b.txt":    "/b.txt",
		"/abs/c.txt":  "/abs/c.txt",
		"~/notes.txt": "/home/u/notes.txt",
	}
	for in, want := range tests {
		if got := resolveUserPath(in, "/work"); got != filepath.Clean(want) {
			t.Errorf("resolveUserPath(%q) = %q, want %q", in, got, want)
		}
	}
}
