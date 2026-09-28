package main

import (
	"strings"
	"testing"
)

// TestCurrentStateCapturesAutoRebuild verifies currentState reflects the
// dev-repo watcher's live auto-rebuild toggle, and reports false when this LR
// isn't watching a repo at all.
func TestCurrentStateCapturesAutoRebuild(t *testing.T) {
	s := newServer("test-lr")
	if got := s.currentState(); got.AutoRebuild {
		t.Fatalf("expected AutoRebuild=false with no repoWatch, got %+v", got)
	}

	dir := initTestRepo(t, "true")
	s.repoWatch = newRepoWatch(dir, func() {})
	if got := s.currentState(); got.AutoRebuild {
		t.Fatalf("expected AutoRebuild=false before toggling, got %+v", got)
	}

	s.repoWatch.setAutoRebuild(true)
	if got := s.currentState(); !got.AutoRebuild {
		t.Fatalf("expected AutoRebuild=true after toggling on, got %+v", got)
	}
}

// TestCurrentStateCapturesAutoUpdate verifies currentState reflects
// selfVersion's live auto-update toggle, and reports false when this LR
// isn't loader-managed (selfVersion nil) at all -- see Step5Prompt.md
// Revision E.
func TestCurrentStateCapturesAutoUpdate(t *testing.T) {
	s := newServer("test-lr")
	if got := s.currentState(); got.AutoUpdate {
		t.Fatalf("expected AutoUpdate=false with no selfVersion, got %+v", got)
	}

	s.selfVersion = &selfVersionWatch{notify: func() {}}
	if got := s.currentState(); got.AutoUpdate {
		t.Fatalf("expected AutoUpdate=false before toggling, got %+v", got)
	}

	s.selfVersion.setAutoUpdate(true)
	if got := s.currentState(); !got.AutoUpdate {
		t.Fatalf("expected AutoUpdate=true after toggling on, got %+v", got)
	}
}

// TestCurrentStateCapturesACTarget verifies currentState reports
// AutoConnect/ACHost/ACPort only while a connection is live or being
// attempted, and clears the host/port once neither is true.
func TestCurrentStateCapturesACTarget(t *testing.T) {
	s := newServer("test-lr")
	if got := s.currentState(); got.AutoConnect || got.ACHost != "" || got.ACPort != "" {
		t.Fatalf("expected zero AC state on a fresh server, got %+v", got)
	}

	s.acHost, s.acPort = "10.0.0.5", "9000"
	s.setACAutoConnecting(true)
	got := s.currentState()
	if !got.AutoConnect || got.ACHost != "10.0.0.5" || got.ACPort != "9000" {
		t.Fatalf("expected AutoConnect=true at 10.0.0.5:9000 while connecting, got %+v", got)
	}

	s.setACAutoConnecting(false)
	if got := s.currentState(); got.AutoConnect {
		t.Fatalf("expected AutoConnect=false once neither connected nor connecting, got %+v", got)
	}
}

// TestCurrentStateCapturesManagedApps verifies currentState's ManagedApps
// snapshot mirrors runningManagedTokens (see Step4Prompt.md Revision I).
func TestCurrentStateCapturesManagedApps(t *testing.T) {
	const app = "test-reststate-managed"
	managedApps[app] = launchSpec{
		binName:   "sleep",
		singleton: false,
		buildArgs: func(s *Server) []string { return []string{"30"} },
	}
	defer delete(managedApps, app)

	s := newServer("test-lr")
	if got := s.currentState(); len(got.ManagedApps) != 0 {
		t.Fatalf("expected no ManagedApps with nothing running, got %v", got.ManagedApps)
	}

	id, err := s.launchManaged(app)
	if err != nil {
		t.Fatalf("launchManaged: %v", err)
	}
	defer func() { _ = s.terminateManaged(id) }()

	got := s.currentState()
	if len(got.ManagedApps) != 1 || got.ManagedApps[0] != app {
		t.Fatalf("expected ManagedApps=[%s], got %v", app, got.ManagedApps)
	}
}

// TestLoadPreviousStateNoEnv verifies loadPreviousState reports ok=false on a
// fresh launch (UFA_LOADER_STATE unset), as it will be for every launch not
// coming out of an announced restart.
func TestLoadPreviousStateNoEnv(t *testing.T) {
	if _, ok := loadPreviousState(); ok {
		t.Fatal("expected ok=false with no restart state in the environment")
	}
}

// TestLoadPreviousStateRoundTrip verifies loadPreviousState decodes what
// UFA_LOADER_STATE carries, and that a malformed payload is rejected (ok=false)
// rather than partially applied.
func TestLoadPreviousStateRoundTrip(t *testing.T) {
	t.Setenv("UFA_LOADER_STATE", `{"auto_rebuild":true,"auto_update":true,"auto_connect":true,"ac_host":"10.0.0.5","ac_port":"9000","managed_apps":["condoccer","federation-command:2"]}`)
	st, ok := loadPreviousState()
	if !ok {
		t.Fatal("expected ok=true for a well-formed payload")
	}
	if !st.AutoRebuild || !st.AutoUpdate || !st.AutoConnect || st.ACHost != "10.0.0.5" || st.ACPort != "9000" {
		t.Fatalf("got %+v, want all fields populated from the environment", st)
	}
	if want := []string{"condoccer", "federation-command:2"}; strings.Join(st.ManagedApps, ",") != strings.Join(want, ",") {
		t.Fatalf("got ManagedApps=%v, want %v", st.ManagedApps, want)
	}

	t.Setenv("UFA_LOADER_STATE", `not json`)
	if _, ok := loadPreviousState(); ok {
		t.Fatal("expected ok=false for an unparseable payload")
	}
}

// TestApplyToConfigOverridesArguments verifies the restored state wins over
// whatever auto-connect settings cfg already carries -- both towards enabling
// it at a restored host/port, and towards disabling it outright.
func TestApplyToConfigOverridesArguments(t *testing.T) {
	cfg := appConfig{autoConnect: false, acHost: "localhost", acPort: "8084"}
	lrState{AutoConnect: true, ACHost: "10.0.0.5", ACPort: "9000"}.applyToConfig(&cfg)
	if !cfg.autoConnect || cfg.acHost != "10.0.0.5" || cfg.acPort != "9000" {
		t.Fatalf("expected restored state to enable auto-connect at the restored target, got %+v", cfg)
	}

	cfg = appConfig{autoConnect: true, acHost: "10.0.0.5", acPort: "9000"}
	lrState{AutoConnect: false}.applyToConfig(&cfg)
	if cfg.autoConnect {
		t.Fatalf("expected restored state to disable auto-connect despite cfg requesting it, got %+v", cfg)
	}
	if cfg.acHost != "10.0.0.5" || cfg.acPort != "9000" {
		t.Fatalf("expected host/port left alone when the restored state carries none, got %+v", cfg)
	}
}

// TestApplyToConfigOverridesAutoLaunch verifies the restored ManagedApps
// snapshot wins over whatever --auto-launch cfg already carries -- both
// towards relaunching what was actually running (even if that differs from
// the configured set) and towards relaunching nothing when nothing was
// running, per Step4Prompt.md Revision I.
func TestApplyToConfigOverridesAutoLaunch(t *testing.T) {
	cfg := appConfig{autoLaunch: []string{"condoccer"}}
	lrState{ManagedApps: []string{"federation-command:2"}}.applyToConfig(&cfg)
	if strings.Join(cfg.autoLaunch, ",") != "federation-command:2" {
		t.Fatalf("expected restored ManagedApps to replace configured auto-launch, got %v", cfg.autoLaunch)
	}

	cfg = appConfig{autoLaunch: []string{"condoccer", "federation-command"}}
	lrState{}.applyToConfig(&cfg)
	if len(cfg.autoLaunch) != 0 {
		t.Fatalf("expected an empty restored ManagedApps to clear configured auto-launch, got %v", cfg.autoLaunch)
	}
}
