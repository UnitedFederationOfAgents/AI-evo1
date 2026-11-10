package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitInstall polls the snapshot until agent's install is no longer running.
func waitInstall(t *testing.T, a *agentInstaller, agent string) AgentInstall {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, st := range a.snapshot() {
			if st.Agent == agent && st.State != installRunning && st.State != "" {
				return st
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("install of %s didn't finish", agent)
	return AgentInstall{}
}

func TestAgentInstaller(t *testing.T) {
	missing := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "install-pi.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 4)
	a := newAgentInstaller(nil, func() { done <- struct{}{} }, func() []string { return []string{missing, dir, dir} })

	// Every installable agent is listed; one whose script can't be found
	// says where it was looked for (each dir once).
	snap := a.snapshot()
	if len(snap) != len(agentInstallScripts) {
		t.Fatalf("snapshot lists %d agents, want %d: %+v", len(snap), len(agentInstallScripts), snap)
	}
	for _, st := range snap {
		switch st.Agent {
		case "pi":
			if st.Script != filepath.Join(dir, "install-pi.sh") || st.State != "" {
				t.Errorf("pi before installing: %+v", st)
			}
		case "opencode":
			if st.Script != "" || !strings.Contains(st.Detail, "install-opencode.sh not found") || strings.Count(st.Detail, dir) != 1 {
				t.Errorf("opencode without a script: %+v", st)
			}
		}
	}

	if err := a.start("claude"); err == nil {
		t.Errorf("starting an agent without an install script should fail")
	}
	if err := a.start("opencode"); err == nil {
		t.Errorf("starting an install whose script is missing should fail")
	}

	// A run's output is kept line by line ("\r\n" and "\r" end lines too).
	release := make(chan struct{})
	a.run = func(ctx context.Context, script string, out io.Writer) (int, error) {
		fmt.Fprint(out, "first\r\nsecond\rthi")
		fmt.Fprint(out, "rd\n")
		<-release
		return 0, nil
	}
	if err := a.start("pi"); err != nil {
		t.Fatalf("start pi: %v", err)
	}
	if err := a.start("pi"); err == nil || !strings.Contains(err.Error(), "already being installed") {
		t.Errorf("a second start while running: %v", err)
	}
	close(release)
	st := waitInstall(t, a, "pi")
	if st.State != installSucceeded || st.ExitCode == nil || *st.ExitCode != 0 || st.FinishedAt == 0 {
		t.Errorf("pi after a successful run: %+v", st)
	}
	if got := strings.Join(st.Log, "|"); got != "first|second|third" {
		t.Errorf("pi log = %q", got)
	}
	<-done

	// A non-zero exit fails the install; only the last lines are kept.
	a.run = func(ctx context.Context, script string, out io.Writer) (int, error) {
		for i := 0; i < agentInstallLogLines+5; i++ {
			fmt.Fprintf(out, "line %d\n", i)
		}
		return 3, nil
	}
	if err := a.start("pi"); err != nil {
		t.Fatalf("restart pi: %v", err)
	}
	st = waitInstall(t, a, "pi")
	if st.State != installFailed || !strings.Contains(st.Detail, "exited 3") || len(st.Log) != agentInstallLogLines || st.Log[0] != "line 5" {
		t.Errorf("pi after a failed run: state %q detail %q, %d log lines starting %q", st.State, st.Detail, len(st.Log), st.Log[0])
	}
	<-done

	// A refused start is kept as a failed run so the tab can show why.
	s := &Server{agentInstalls: a}
	s.installAgent("opencode")
	for _, st := range a.snapshot() {
		if st.Agent == "opencode" && (st.State != installFailed || !strings.Contains(st.Detail, "not found")) {
			t.Errorf("opencode after a refused install: %+v", st)
		}
	}

	var nilInstaller *agentInstaller
	if got := nilInstaller.snapshot(); got == nil || len(got) != 0 {
		t.Errorf("nil installer snapshot = %v", got)
	}
	if err := nilInstaller.start("pi"); err == nil {
		t.Errorf("nil installer should refuse to start")
	}
}

func TestRunInstallScript(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "install-x.sh")
	if err := os.WriteFile(script, []byte("echo out; echo err >&2; pwd; exit 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newAgentInstaller(nil, nil, nil)
	a.runs["x"] = &AgentInstall{Agent: "x", State: installRunning}
	w := &installLogWriter{a: a, agent: "x"}
	code, err := runInstallScript(context.Background(), script, w)
	if err != nil || code != 4 {
		t.Fatalf("runInstallScript = %d, %v", code, err)
	}
	got := strings.Join(a.runs["x"].Log, "|")
	if !strings.Contains(got, "out") || !strings.Contains(got, "err") || !strings.Contains(got, dir) {
		t.Errorf("log = %q, want stdout, stderr and the script's dir", got)
	}
}
