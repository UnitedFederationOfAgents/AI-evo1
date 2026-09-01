package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	ufaconfig "ufa-configurable"
)

// TestResolveConfigLayering verifies config files feed the resolved startup
// config (per-app file beating global.yaml per key) and that a flag set on the
// command line still wins over both.
func TestResolveConfigLayering(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "global.yaml"),
		[]byte("port: 9000\nname: from-global\nac-host: gc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "local-representative.yaml"),
		[]byte("name: from-app\nauto-connect: yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conf, err := ufaconfig.Load("local-representative", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	defaults := appConfig{
		httpPort: "8081", heartbeatPort: "8082", name: "host",
		acHost: "localhost", acPort: "8084",
	}

	// Nothing set on the CLI: config wins, and the per-app file beats global.
	got, err := resolveConfig(conf, map[string]bool{}, defaults)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if got.httpPort != "9000" {
		t.Errorf("httpPort = %q, want 9000 (from global.yaml)", got.httpPort)
	}
	if got.name != "from-app" {
		t.Errorf("name = %q, want from-app (per-app file wins)", got.name)
	}
	if got.acHost != "gc" {
		t.Errorf("acHost = %q, want gc", got.acHost)
	}
	if !got.autoConnect {
		t.Errorf("autoConnect not applied from config")
	}
	if got.acPort != "8084" {
		t.Errorf("acPort = %q, want default 8084", got.acPort)
	}

	// A flag set on the command line beats the config files.
	got, err = resolveConfig(conf, map[string]bool{"name": true, "port": true}, defaults)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if got.name != "host" || got.httpPort != "8081" {
		t.Errorf("CLI-set flags should win, got name=%q port=%q", got.name, got.httpPort)
	}
}

// TestResolveConfigRejectsBadBool verifies a malformed boolean in a config file
// is a startup error rather than being silently ignored.
func TestResolveConfigRejectsBadBool(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local-representative.yaml"),
		[]byte("auto-connect: banana\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conf, err := ufaconfig.Load("local-representative", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := resolveConfig(conf, map[string]bool{}, appConfig{}); err == nil {
		t.Fatal("expected error for non-boolean auto-connect")
	}
}

// TestGetACStateReportsConnecting verifies the auto-connect retry indicator is
// surfaced through the ac-state payload the UI consumes.
func TestGetACStateReportsConnecting(t *testing.T) {
	s := newServer("test-lr")

	if s.getACState().Connecting {
		t.Fatalf("fresh server should not report Connecting")
	}

	s.setACAutoConnecting(true)
	if !s.getACState().Connecting {
		t.Fatalf("expected Connecting=true after setACAutoConnecting(true)")
	}

	s.setACAutoConnecting(false)
	if s.getACState().Connecting {
		t.Fatalf("expected Connecting=false after setACAutoConnecting(false)")
	}
}

// TestACStateMsgStampsTarget verifies acStateMsg carries the explicit host/port
// plus the current retry status.
func TestACStateMsgStampsTarget(t *testing.T) {
	s := newServer("test-lr")
	s.setACAutoConnecting(true)

	msg := s.acStateMsg(false, "10.0.0.5", "9000")
	if msg.Host != "10.0.0.5" || msg.Port != "9000" {
		t.Fatalf("expected host/port to be carried through, got %s:%s", msg.Host, msg.Port)
	}
	if msg.Connected {
		t.Fatalf("expected Connected=false")
	}
	if !msg.Connecting {
		t.Fatalf("expected Connecting=true while the retry loop is active")
	}
}

// TestStopAutoConnectAC verifies the retry loop can be started and cancelled, and
// that a redundant stop is safe (no double-close panic).
func TestStopAutoConnectAC(t *testing.T) {
	s := newServer("test-lr")

	// Target a closed port so connectAC fails fast rather than hanging.
	s.startAutoConnectAC("127.0.0.1", "1")
	s.stopAutoConnectAC()
	s.stopAutoConnectAC() // must not panic

	// The loop goroutine should clear its cancel channel shortly after cancel.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.acMu.RLock()
		cleared := s.acAutoConnectCancel == nil
		s.acMu.RUnlock()
		if cleared {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("auto-connect loop did not release its cancel channel after stop")
}
