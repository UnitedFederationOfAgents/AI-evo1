package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"representable"
)

// lastEnv is key's value in env as exec sees it: the last entry wins.
func lastEnv(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}

// devPluginServer is a Server watching a repo with one plug-in dev repo,
// app id "hyena", registered. Its deploy-dev-binaries builds the given
// recipe; builds deploy to a temporary AI_EVO1_DEV_PLUGIN.
func devPluginServer(t *testing.T, recipe string) (*Server, string) {
	t.Helper()
	t.Setenv("AI_EVO1_DEV_PLUGIN", t.TempDir())
	s := newServer("test-lr")
	s.repoWatch = newRepoWatch(initTestRepo(t, "true"), func() {})
	s.repoWatch.ext.built = s.restartDevPlugin
	ext := initTestRepo(t, recipe)
	if err := s.repoWatch.registerExternalAs(ext, true, "hyena"); err != nil {
		t.Fatalf("registerExternalAs: %v", err)
	}
	s.repoWatch.pollExternal()
	return s, ext
}

// buildDevPlugin asks for a build of ext and waits for it, restart included.
func buildDevPlugin(t *testing.T, s *Server, ext string) ExternalWatchState {
	t.Helper()
	if err := s.repoWatch.requestExternalBuild(ext); err != nil {
		t.Fatalf("requestExternalBuild: %v", err)
	}
	s.repoWatch.pollExternal()
	return waitExternalBuild(t, s.repoWatch)
}

// runningDevPlugin is the running instance of app, if there is one.
func runningDevPlugin(s *Server, app string) *managedProc {
	s.procMu.Lock()
	defer s.procMu.Unlock()
	for _, p := range s.managed {
		if p.app == app && p.state() == "running" {
			return p
		}
	}
	return nil
}

// TestDevPluginLifecycle walks a plug-in dev repo's plug-in through
// Revision F: it can't launch before a build has deployed it; a build
// deploys it to AI_EVO1_DEV_PLUGIN; it launches as a managed app under its
// app id; and the next build restarts it.
func TestDevPluginLifecycle(t *testing.T) {
	s, ext := devPluginServer(t, `printf '#!/bin/sh\nexec sleep 30\n' > $(DEV_PLUGIN_BIN)`)
	s.devMode = true
	t.Setenv(representable.LaunchModeEnvVar, representable.ModeOps)

	if spec, ok := s.launchSpecFor("hyena"); !ok || spec.binPath != devPluginBinary("hyena") || !spec.singleton {
		t.Fatalf("expected a singleton launch spec run from %s, got %+v (%v)", devPluginBinary("hyena"), spec, ok)
	}
	if _, err := s.launchManaged("hyena"); err == nil || !strings.Contains(err.Error(), "deployed") {
		t.Fatalf("expected launching before a build to be refused, got %v", err)
	}

	e := buildDevPlugin(t, s, ext)
	if e.BuildError != "" || !e.Deployed || e.LaunchNote != "not launched" {
		t.Fatalf("expected a deployed build, nothing restarted: %+v", e)
	}

	if _, err := s.launchManaged("hyena"); err != nil {
		t.Fatalf("launchManaged: %v", err)
	}
	first := runningDevPlugin(s, "hyena")
	if first == nil {
		t.Fatal("expected hyena running after launch")
	}
	defer func() {
		if p := runningDevPlugin(s, "hyena"); p != nil {
			_ = s.terminateManaged(p.instanceID)
		}
	}()
	if args := strings.Join(first.cmd.Args, " "); !strings.Contains(args, "--name hyena") || !strings.Contains(args, "--port ") {
		t.Fatalf("expected cats-ufa-plugin's arguments, got %q", args)
	}
	if args := strings.Join(first.cmd.Args, " "); !strings.Contains(args, "--dev-mode") {
		t.Fatalf("expected a dev-mode LR to launch the plug-in with --dev-mode, got %q", args)
	}
	// LR's own environment says ops (below); the launch's own entry, last,
	// wins.
	if mode := lastEnv(first.cmd.Env, representable.LaunchModeEnvVar); mode != representable.ModeDev {
		t.Fatalf("expected the plug-in launched with %s=dev, got %q", representable.LaunchModeEnvVar, mode)
	}

	e = buildDevPlugin(t, s, ext)
	if e.BuildError != "" || !strings.HasPrefix(e.LaunchNote, "restarted") {
		t.Fatalf("expected the build to restart the plug-in: %+v", e)
	}
	second := runningDevPlugin(s, "hyena")
	if second == nil || second.instanceID == first.instanceID || second.pid == first.pid {
		t.Fatalf("expected a fresh instance after the build, got %+v (was %s)", second, first.instanceID)
	}

	// Unregistered, it's no longer an app LR knows how to launch.
	if err := s.repoWatch.unregisterExternal(ext); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.launchSpecFor("hyena"); ok {
		t.Fatal("expected no launch spec once the repo is unregistered")
	}
}

// TestDevPluginStoppedOnModeMismatch verifies a plug-in a dev-mode LR
// launched that connects in ops mode anyway is stopped, its row saying why,
// rather than left running mismatched.
func TestDevPluginStoppedOnModeMismatch(t *testing.T) {
	s, ext := devPluginServer(t, `printf '#!/bin/sh\nexec sleep 30\n' > $(DEV_PLUGIN_BIN)`)
	s.devMode = true
	if e := buildDevPlugin(t, s, ext); e.BuildError != "" {
		t.Fatalf("build: %+v", e)
	}
	id, err := s.launchManaged("hyena")
	if err != nil {
		t.Fatalf("launchManaged: %v", err)
	}
	p := runningDevPlugin(s, "hyena")
	if p == nil {
		t.Fatal("expected hyena running after launch")
	}

	s.stopMismatchedManaged("hyena", representable.ModeOps)
	deadline := time.Now().Add(5 * time.Second)
	for p.state() == "running" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := p.state(); st == "running" {
		_ = s.terminateManaged(id)
		t.Fatal("expected the mismatched plug-in stopped")
	}
	if info := p.info(); !strings.Contains(info.Detail, "connected in ops mode") || !strings.Contains(info.Detail, "--dev-mode") {
		t.Fatalf("expected the row to say why it was stopped, got %q", info.Detail)
	}

	// Nothing of that name running: a no-op.
	s.stopMismatchedManaged("hyena", representable.ModeOps)
}

// TestDevPluginDeployNeedsDevPluginBin verifies a build whose recipe passes
// without building to $(DEV_PLUGIN_BIN) fails, leaving the deployed binary as
// it was.
func TestDevPluginDeployNeedsDevPluginBin(t *testing.T) {
	s, ext := devPluginServer(t, "echo built elsewhere")
	if err := os.WriteFile(devPluginBinary("hyena"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := buildDevPlugin(t, s, ext)
	if !strings.Contains(e.BuildError, "DEV_PLUGIN_BIN") {
		t.Fatalf("expected the build to fail for not building to DEV_PLUGIN_BIN: %+v", e)
	}
	if b, err := os.ReadFile(devPluginBinary("hyena")); err != nil || string(b) != "old" {
		t.Fatalf("expected the deployed binary untouched, got %q, %v", b, err)
	}
}

// TestDevPluginAppIDClashes verifies a plug-in dev repo's app id can't be
// one of managedApps, nor another plug-in dev repo's.
func TestDevPluginAppIDClashes(t *testing.T) {
	s, _ := devPluginServer(t, "true")
	if err := s.repoWatch.registerExternalAs(initTestRepo(t, "true"), true, examplePluginAppID); err == nil {
		t.Fatalf("expected %s to be refused: it is a managed app", examplePluginAppID)
	}
	if err := s.repoWatch.registerExternalAs(initTestRepo(t, "true"), true, "hyena"); err == nil || !strings.Contains(err.Error(), "already the app id") {
		t.Fatalf("expected a second plug-in dev repo for hyena to be refused, got %v", err)
	}
}
