package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"representable"
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

// TestResolveConfigDevMode verifies --dev-mode / dev-mode (see
// docs/DevMode.md) resolve independently of the unrelated --dev flag.
func TestResolveConfigDevMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local-representative.yaml"),
		[]byte("dev-mode: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conf, err := ufaconfig.Load("local-representative", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got, err := resolveConfig(conf, map[string]bool{}, appConfig{})
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if !got.devMode {
		t.Errorf("devMode not applied from config")
	}
	if got.dev {
		t.Errorf("dev-mode: true should not also set the unrelated dev flag")
	}

	// A CLI-set --dev-mode wins over the config file.
	got, err = resolveConfig(conf, map[string]bool{"dev-mode": true}, appConfig{devMode: false})
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if got.devMode {
		t.Errorf("CLI-set dev-mode=false should win over dev-mode: true in config")
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

// freeTCPAddr reserves and immediately releases an ephemeral TCP port, for
// tests that need a real address to bind a representable.Server to without a
// way to read back the port NewServer chose (it has no Addr() accessor).
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a free port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// TestSetAutoConnectACTogglesEnabledFlag verifies the persistent auto-connect
// toggle (Step3Prompt.md Revision I) is a first-class state, settable
// independent of any single connection attempt: enabling it is reported
// through ac-state's AutoConnect field, and disabling it clears that flag and
// stops the retry loop.
func TestSetAutoConnectACTogglesEnabledFlag(t *testing.T) {
	s := newServer("test-lr")

	// Target a closed port so the retry loop's dial attempts fail fast
	// rather than hanging or actually connecting.
	s.setAutoConnectAC(true, "127.0.0.1", "1")
	if !s.getACState().AutoConnect {
		t.Fatalf("expected AutoConnect=true after enabling")
	}

	s.setAutoConnectAC(false, "", "")
	if s.getACState().AutoConnect {
		t.Fatalf("expected AutoConnect=false after disabling")
	}

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
	t.Fatalf("disabling auto-connect did not stop the retry loop")
}

// TestDisconnectACTerminatesAutoConnect verifies an explicit, operator-driven
// disconnect (a) actually drops the connection, (b) clears the persistent
// auto-connect toggle, and (c) -- being marked intentional -- does not resume
// the retry cycle, unlike an unintentional drop (Step3Prompt.md Revision I:
// "Intentionally disconnect terminates auto-connect").
func TestDisconnectACTerminatesAutoConnect(t *testing.T) {
	addr := freeTCPAddr(t)
	srv, err := representable.NewServer(addr, representable.Mode(false))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	s := newServer("test-lr")
	s.startAutoConnectAC(host, port)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !s.getACState().Connected {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.getACState().Connected {
		t.Fatalf("expected LR to connect to the test agent-coordinator server")
	}
	if !s.getACState().AutoConnect {
		t.Fatalf("expected AutoConnect=true once armed and connected")
	}

	s.stopAutoConnectAC()
	s.disconnectAC()

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := s.getACState()
		if !st.Connected && !st.AutoConnect && !st.Connecting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := s.getACState()
	if st.Connected {
		t.Fatalf("expected disconnectAC to drop the connection")
	}
	if st.AutoConnect {
		t.Fatalf("expected an intentional disconnect to clear the auto-connect toggle")
	}

	// Give a would-be (buggy) resume a moment to kick in, then confirm it
	// didn't: no retry loop running, still disconnected.
	time.Sleep(200 * time.Millisecond)
	s.acMu.RLock()
	stillNoLoop := s.acAutoConnectCancel == nil
	s.acMu.RUnlock()
	if !stillNoLoop {
		t.Fatalf("expected an intentional disconnect to not resume the auto-connect retry loop")
	}
	if s.getACState().Connected || s.getACState().Connecting {
		t.Fatalf("expected LR to stay disconnected after an intentional disconnect")
	}

	_ = srv // keep the test server alive for the duration of the test
}
