package ufaversion

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func withArgs(args []string, fn func()) {
	orig := os.Args
	os.Args = args
	defer func() { os.Args = orig }()
	fn()
}

func TestHandleVersionFlag_NotPresent(t *testing.T) {
	withArgs([]string{"appname", "--dev-mode", "--port", "8080"}, func() {
		var handled bool
		out := captureStdout(t, func() { handled = HandleVersionFlag() })
		if handled {
			t.Fatal("expected HandleVersionFlag to return false with no --version arg")
		}
		if out != "" {
			t.Fatalf("expected no output, got %q", out)
		}
	})
}

func TestHandleVersionFlag_Present(t *testing.T) {
	origVersion := Version
	Version = "v1.2.3-test-abcdef1"
	defer func() { Version = origVersion }()

	withArgs([]string{"appname", "--dev-mode", "--version"}, func() {
		var handled bool
		out := captureStdout(t, func() { handled = HandleVersionFlag() })
		if !handled {
			t.Fatal("expected HandleVersionFlag to return true with --version arg present")
		}
		if out != "v1.2.3-test-abcdef1\n" {
			t.Fatalf("expected version printed on its own line, got %q", out)
		}
	})
}

func TestHandleVersionFlag_IgnoresProgramName(t *testing.T) {
	// A binary named "--version" (pathological, but os.Args[0] is skipped
	// deliberately) must not trip the flag.
	withArgs([]string{"--version"}, func() {
		if HandleVersionFlag() {
			t.Fatal("expected os.Args[0] to be ignored")
		}
	})
}
