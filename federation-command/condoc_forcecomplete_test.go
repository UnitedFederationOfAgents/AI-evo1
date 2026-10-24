package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestCondocAgentExecKilledByForceComplete verifies a running agent -- here
// a `bash -c '… | tee …'` pipeline like teeCommand builds -- is killed,
// grandchildren included, once !FORCE-COMPLETED! lands on the main file.
func TestCondocAgentExecKilledByForceComplete(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "X.md")
	if err := os.WriteFile(mainPath, []byte("# X\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "sleep.pid")
	agent := exec.Command("bash", "-c", "sleep 60 & echo $! > "+shellQuote(pidFile)+"; wait")
	run := &condocAgentExec{cmd: teeCommand(agent, filepath.Join(dir, "reply.txt")), mainPath: mainPath}

	go func() {
		time.Sleep(300 * time.Millisecond)
		f, _ := os.OpenFile(mainPath, os.O_APPEND|os.O_WRONLY, 0644)
		f.WriteString("\n!FORCE-COMPLETED!\n")
		f.Close()
	}()

	start := time.Now()
	errc := make(chan error, 1)
	go func() { errc <- run.Run() }()
	select {
	case err := <-errc:
		if err == nil {
			t.Error("Run returned nil, want the kill's error")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("agent was not killed")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %v to kill", d)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if processAlive(pid) {
		t.Errorf("grandchild sleep (%d) survived", pid)
	}
}

// TestCondocAgentExecSkippedWhenAlreadyForceCompleted verifies an agent due
// to start after the directive landed is never started.
func TestCondocAgentExecSkippedWhenAlreadyForceCompleted(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "X.md")
	if err := os.WriteFile(mainPath, []byte("# X\n\n!FORCE-COMPLETED!\n"), 0644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "ran")
	run := &condocAgentExec{cmd: exec.Command("touch", marker), mainPath: mainPath}
	if err := run.Run(); !errors.Is(err, errCondocForceCompleted) {
		t.Errorf("Run = %v, want errCondocForceCompleted", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("agent ran")
	}
}
