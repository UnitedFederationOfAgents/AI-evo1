package main

import (
	"os"
	"testing"
	"time"
)

// TestSplitList covers the comma/whitespace list parsing used for --auto-launch.
func TestSplitList(t *testing.T) {
	cases := map[string][]string{
		"":                              {},
		"federation-command":            {"federation-command"},
		"federation-command, worker":    {"federation-command", "worker"},
		"  a ,b\tc\n d ":                {"a", "b", "c", "d"},
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
	if err := s.launchManaged("no-such-app"); err == nil {
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

// TestLaunchAndTerminateManaged drives the full lifecycle against a stand-in
// "sleep" process injected into managedApps.
func TestLaunchAndTerminateManaged(t *testing.T) {
	const name = "test-sleep"
	managedApps[name] = launchSpec{
		binName:   "sleep",
		buildArgs: func(s *Server) []string { return []string{"30"} },
	}
	defer delete(managedApps, name)

	s := newServer("test-lr")

	if err := s.launchManaged(name); err != nil {
		t.Fatalf("launchManaged: %v", err)
	}

	s.procMu.Lock()
	p := s.managed[name]
	s.procMu.Unlock()
	if p == nil || p.state() != "running" || p.pid <= 0 {
		t.Fatalf("expected a running managed proc with a pid, got %+v", p)
	}

	// system-state should now surface it.
	found := false
	for _, mp := range s.systemState().Managed {
		if mp.Name == name && mp.Status == "running" {
			found = true
		}
	}
	if !found {
		t.Fatal("launched process missing from systemState().Managed")
	}

	// A second launch while running is rejected.
	if err := s.launchManaged(name); err == nil {
		t.Fatal("expected error launching an already-running application")
	}

	// Terminate and wait for the reaper to record the exit.
	if err := s.terminateManaged(name); err != nil {
		t.Fatalf("terminateManaged: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if p.state() != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p.state() == "running" {
		t.Fatal("process still running after terminateManaged")
	}

	// terminate again on the stopped proc drops it from the list.
	if err := s.terminateManaged(name); err != nil {
		t.Fatalf("terminateManaged (dismiss): %v", err)
	}
	s.procMu.Lock()
	_, still := s.managed[name]
	s.procMu.Unlock()
	if still {
		t.Fatal("stopped process should be dropped after a second terminate")
	}
}

// TestRecordLaunchFailure verifies a failed launch stays visible on the system
// tab as a "failed" entry.
func TestRecordLaunchFailure(t *testing.T) {
	const name = "test-missing"
	managedApps[name] = launchSpec{
		binName:   "this-binary-does-not-exist-anywhere",
		buildArgs: func(s *Server) []string { return nil },
	}
	defer delete(managedApps, name)

	s := newServer("test-lr")
	if err := s.launchManaged(name); err == nil {
		t.Fatal("expected launch of a missing binary to fail")
	}

	var got *ProcInfo
	for _, mp := range s.systemState().Managed {
		if mp.Name == name {
			m := mp
			got = &m
		}
	}
	if got == nil || got.Status != "failed" {
		t.Fatalf("expected a failed entry for %q, got %+v", name, got)
	}
}
