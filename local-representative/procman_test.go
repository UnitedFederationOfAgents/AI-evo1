package main

import (
	"os"
	"testing"
	"time"
)

// TestSplitList covers the comma/whitespace list parsing used for --auto-launch.
func TestSplitList(t *testing.T) {
	cases := map[string][]string{
		"":                           {},
		"federation-command":         {"federation-command"},
		"federation-command, worker": {"federation-command", "worker"},
		"  a ,b\tc\n d ":             {"a", "b", "c", "d"},
	}
	for in, want := range cases {
		got := splitList(in)
		if len(got) != len(want) {
			t.Fatalf("splitList(%q) = %v, want %v", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("splitList(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

// TestParseAutoLaunchEntry covers "app" / "app:N" auto-launch tokens.
func TestParseAutoLaunchEntry(t *testing.T) {
	ok := map[string]autoLaunchEntry{
		"federation-command":   {app: "federation-command", count: 1},
		"federation-command:3": {app: "federation-command", count: 3},
		" fc : 2 ":             {app: "fc", count: 2},
	}
	for in, want := range ok {
		got, err := parseAutoLaunchEntry(in)
		if err != nil {
			t.Fatalf("parseAutoLaunchEntry(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("parseAutoLaunchEntry(%q) = %+v, want %+v", in, got, want)
		}
	}
	for _, in := range []string{"", ":", "fc:0", "fc:-1", "fc:x"} {
		if _, err := parseAutoLaunchEntry(in); err == nil {
			t.Fatalf("parseAutoLaunchEntry(%q) expected an error", in)
		}
	}
}

// TestWrapInTerminal verifies a configured "terminal" prefix is honoured.
func TestWrapInTerminal(t *testing.T) {
	s := newServer("test-lr")
	s.terminalCmd = "myterm -e"
	prog, args, err := s.wrapInTerminal("/bin/fc", []string{"--auto-connect", "--remote"})
	if err != nil {
		t.Fatalf("wrapInTerminal: %v", err)
	}
	if prog != "myterm" {
		t.Errorf("prog = %q, want myterm", prog)
	}
	want := []string{"-e", "/bin/fc", "--auto-connect", "--remote"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

// TestSystemStateSelf verifies LR reports itself as a running, non-managed
// process with its real PID.
func TestSystemStateSelf(t *testing.T) {
	s := newServer("test-lr")
	st := s.systemState()
	if st.Self.Name != "test-lr" {
		t.Errorf("Self.Name = %q, want test-lr", st.Self.Name)
	}
	if st.Self.PID != os.Getpid() {
		t.Errorf("Self.PID = %d, want %d", st.Self.PID, os.Getpid())
	}
	if st.Self.Status != "running" || st.Self.Managed {
		t.Errorf("Self = %+v, want running & unmanaged", st.Self)
	}
	if len(st.Managed) != 0 {
		t.Errorf("fresh server should manage nothing, got %v", st.Managed)
	}
}

// TestLaunchManagedUnknown verifies an unrecognised application name is an error.
func TestLaunchManagedUnknown(t *testing.T) {
	s := newServer("test-lr")
	if _, err := s.launchManaged("no-such-app"); err == nil {
		t.Fatal("expected error launching an unknown application")
	}
}

// TestResolveAppBinaryOverride verifies a configured binary path is honoured, and
// a missing one is rejected.
func TestResolveAppBinaryOverride(t *testing.T) {
	s := newServer("test-lr")

	s.binOverrides["federation-command"] = "/bin/sh"
	if got, err := s.resolveAppBinary("federation-command", "federation-command"); err != nil || got != "/bin/sh" {
		t.Fatalf("resolveAppBinary override = %q, %v; want /bin/sh, nil", got, err)
	}

	s.binOverrides["federation-command"] = "/definitely/not/here"
	if _, err := s.resolveAppBinary("federation-command", "federation-command"); err == nil {
		t.Fatal("expected error for a missing override binary")
	}
}

// TestLaunchNInstancesAndTerminate drives the full multi-instance lifecycle
// against a stand-in "sleep" process injected into managedApps.
func TestLaunchNInstancesAndTerminate(t *testing.T) {
	const app = "test-sleep"
	managedApps[app] = launchSpec{
		binName:   "sleep",
		singleton: false, // N-per-host, like federation-command
		buildArgs: func(s *Server) []string { return []string{"30"} },
	}
	defer delete(managedApps, app)

	s := newServer("test-lr")

	id1, err := s.launchManaged(app)
	if err != nil {
		t.Fatalf("launchManaged #1: %v", err)
	}
	id2, err := s.launchManaged(app)
	if err != nil {
		t.Fatalf("launchManaged #2 (N-per-host should allow it): %v", err)
	}
	if id1 == id2 {
		t.Fatalf("two instances share an id: %q", id1)
	}

	s.procMu.Lock()
	p1, p2 := s.managed[id1], s.managed[id2]
	s.procMu.Unlock()
	if p1 == nil || p2 == nil || p1.state() != "running" || p2.state() != "running" {
		t.Fatalf("expected two running instances, got %+v / %+v", p1, p2)
	}
	if p1.pid <= 0 || p2.pid <= 0 || p1.pid == p2.pid {
		t.Fatalf("expected two distinct pids, got %d / %d", p1.pid, p2.pid)
	}

	running := 0
	for _, mp := range s.systemState().Managed {
		if mp.Name == app && mp.Status == "running" {
			running++
		}
	}
	if running != 2 {
		t.Fatalf("systemState should surface 2 running instances, got %d", running)
	}

	// Terminate the first instance and wait for the reaper to record the exit.
	if err := s.terminateManaged(id1); err != nil {
		t.Fatalf("terminateManaged(%q): %v", id1, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if p1.state() != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p1.state() == "running" {
		t.Fatal("instance #1 still running after terminateManaged")
	}
	if p2.state() != "running" {
		t.Fatal("instance #2 should be unaffected by terminating #1")
	}

	// A second terminate on the stopped instance drops it from the list.
	if err := s.terminateManaged(id1); err != nil {
		t.Fatalf("terminateManaged (dismiss): %v", err)
	}
	s.procMu.Lock()
	_, still := s.managed[id1]
	_, other := s.managed[id2]
	s.procMu.Unlock()
	if still {
		t.Fatal("stopped instance #1 should be dropped after a second terminate")
	}
	if !other {
		t.Fatal("instance #2 should still be managed")
	}

	_ = s.terminateManaged(id2)
}

// TestLaunchManagedSingleton verifies a singleton app rejects a second launch
// while an instance is running.
func TestLaunchManagedSingleton(t *testing.T) {
	const app = "test-singleton"
	managedApps[app] = launchSpec{
		binName:   "sleep",
		singleton: true,
		buildArgs: func(s *Server) []string { return []string{"30"} },
	}
	defer delete(managedApps, app)

	s := newServer("test-lr")
	id, err := s.launchManaged(app)
	if err != nil {
		t.Fatalf("launchManaged: %v", err)
	}
	if _, err := s.launchManaged(app); err == nil {
		t.Fatal("expected error launching a second singleton instance")
	}
	_ = s.terminateManaged(id)
}

// TestRecordLaunchFailure verifies a failed launch stays visible on the system
// tab as a "failed" entry.
func TestRecordLaunchFailure(t *testing.T) {
	const app = "test-missing"
	managedApps[app] = launchSpec{
		binName:   "this-binary-does-not-exist-anywhere",
		buildArgs: func(s *Server) []string { return nil },
	}
	defer delete(managedApps, app)

	s := newServer("test-lr")
	if _, err := s.launchManaged(app); err == nil {
		t.Fatal("expected launch of a missing binary to fail")
	}

	var got *ProcInfo
	for _, mp := range s.systemState().Managed {
		if mp.Name == app {
			m := mp
			got = &m
		}
	}
	if got == nil || got.Status != "failed" {
		t.Fatalf("expected a failed entry for %q, got %+v", app, got)
	}
}
