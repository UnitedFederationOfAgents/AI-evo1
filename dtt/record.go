package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	ufaconfig "ufa-configurable"
)

// This file records dtt's calls. "dtt run|execute|read|assert|delete ..."
// runs itself again under clauditable -- clauditable <dtt> <the same
// args...> -- so every call lands in the agent records like any other
// command: in the session, and (UFA_EVENT_TYPE=dtt_call) copied to
// <records>/dtt-calls/, which local-representative's tools tab lists as its
// history. The re-run has DTT_CALL_RECORDED=1, so it runs directly.
//
// Recording is best effort: with record: off (dtt.yaml), DTT_RECORD=off,
// no clauditable to be found, or one that won't start, the call runs
// directly as it always did. Other commands (list, describe, examples,
// tool-engine ...) are never recorded: they change nothing, or (tool-engine)
// are already inside a recorded dtt run.

const (
	envCallRecorded = "DTT_CALL_RECORDED" // set on the recorded re-run, so it doesn't record itself again
	envRecord       = "DTT_RECORD"        // off: don't record this call, whatever dtt.yaml says
	dttCallEvent    = "dtt_call"          // the event_type clauditable records the call as
)

// recordedCommands are the commands recorded: calls that run something.
var recordedCommands = map[string]bool{"run": true, "execute": true, "read": true, "assert": true, "delete": true}

// recordStrippedEnv are removed from the recorded re-run's environment:
// CLAUDITABLE_ALREADY_ACTIVE would make clauditable pass the call through
// unrecorded (inside an agent's recorded command, say), and the verbosity
// settings would hide the call's output from whoever called dtt.
var recordStrippedEnv = []string{"CLAUDITABLE_ALREADY_ACTIVE", "UFA_VERBOSITY_MANAGEMENT", "UFA_VERBOSITY_AFTER_LINE"}

// recordCommand decides whether the call args (dtt's arguments, as given)
// is recorded, and if so returns the command line and environment that
// record it: clauditable, then self (dtt's own path) and args. env is dtt's
// environment; lookPath finds an executable (exec.LookPath). nil means run
// the call directly.
func recordCommand(args []string, cfg config, env []string, self string, lookPath func(string) (string, error)) (argv, newEnv []string) {
	_, rest, err := ufaconfig.ExtractConfigDir(args)
	if err != nil || len(rest) == 0 || !recordedCommands[rest[0]] {
		return nil, nil
	}
	if envValue(env, envCallRecorded) != "" || !cfg.record {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(envValue(env, envRecord))) {
	case "off", "false", "no", "0":
		return nil, nil
	}
	bin := findClauditable(cfg, self, lookPath)
	if bin == "" || self == "" {
		return nil, nil
	}
	argv = append([]string{bin, self}, args...)
	drop := append([]string{envCallRecorded, "UFA_EVENT_TYPE"}, recordStrippedEnv...)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !contains(drop, name) {
			newEnv = append(newEnv, kv)
		}
	}
	newEnv = append(newEnv, envCallRecorded+"=1", "UFA_EVENT_TYPE="+dttCallEvent)
	return argv, newEnv
}

// findClauditable is the clauditable to record through: the clauditable
// setting, else clauditable on PATH, else one beside dtt's own executable;
// "" when there's none.
func findClauditable(cfg config, self string, lookPath func(string) (string, error)) string {
	candidates := []string{}
	if cfg.clauditableBin != "" {
		candidates = append(candidates, cfg.clauditableBin)
	}
	candidates = append(candidates, "clauditable")
	if self != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(self), "clauditable"))
	}
	for _, c := range candidates {
		if p, err := lookPath(c); err == nil && p != "" {
			return p
		}
	}
	return ""
}

// envValue is name's value in env (KEY=value entries), "" when it's unset.
// The last entry wins, as it does for exec.
func envValue(env []string, name string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == name {
			v = val
		}
	}
	return v
}

// runRecorded runs this call (args: os.Args[1:]) under clauditable when it
// should be recorded, and returns the exit code to leave with; ok is false
// when it wasn't (not a recorded command, recording off, or no clauditable
// that starts), and the call should run directly.
func runRecorded(args []string) (code int, ok bool) {
	dir, _, err := ufaconfig.ExtractConfigDir(args)
	if err != nil {
		return 0, false // run reports it
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return 0, false
	}
	self, err := os.Executable()
	if err != nil {
		return 0, false
	}
	argv, env := recordCommand(args, cfg, os.Environ(), self, exec.LookPath)
	if argv == nil {
		return 0, false
	}

	// Signals: dtt waits for clauditable, passing SIGINT and SIGTERM on to
	// it (clauditable passes them to the call and records it, cancelled).
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "dtt: not recording this call (%s won't start: %v)\n", argv[0], err)
		return 0, false
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	err = cmd.Wait()
	close(done)
	if err == nil {
		return exitOK, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), true
		}
		return ee.ExitCode(), true
	}
	fmt.Fprintln(os.Stderr, "dtt:", err)
	return exitToolFailed, true
}
