package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Agent installs (condocs/switchToUfabuildImpls/Step1.md Prompt 9):
// agent-coordinator's debug -> install tab shows which agents this host has
// (the "agents & models" inspection, agents.go) and, for an installable one
// that's missing, a control that runs its install script here -- one of the
// core repo's scripts/install-<agent>.sh, replicating the agent's hello-*
// devcontainer install. Only the agents in agentInstallScripts can be
// installed; the script runs as LR's own user, with LR's environment and no
// terminal, and its output is kept so the tab can show it. When it finishes
// the agents are re-inspected, so a successful install shows as installed.

// agentInstallScripts maps each installable agent to its script's name in
// the core repo's scripts/ directory.
var agentInstallScripts = map[string]string{
	"pi":       "install-pi.sh",
	"opencode": "install-opencode.sh",
}

const (
	// agentInstallTimeout bounds one install script run (downloads included).
	agentInstallTimeout = 15 * time.Minute
	// agentInstallLogLines is how many of a run's last output lines are kept.
	agentInstallLogLines = 300
	// agentInstallNotifyEvery throttles state pushes while a script is
	// producing output.
	agentInstallNotifyEvery = time.Second
)

// Install states.
const (
	installRunning   = "running"
	installSucceeded = "succeeded"
	installFailed    = "failed"
)

// AgentInstall is one installable agent in AgentsState.Installs: where its
// install script is, and how its latest run (this LR session) went.
type AgentInstall struct {
	Agent string `json:"agent"`
	// Script is the install script's path, "" when it couldn't be found
	// (Detail then says where it was looked for).
	Script     string   `json:"script,omitempty"`
	State      string   `json:"state,omitempty"` // "" (never run) | installRunning | installSucceeded | installFailed
	Detail     string   `json:"detail,omitempty"`
	StartedAt  int64    `json:"started_at,omitempty"`  // unix seconds
	FinishedAt int64    `json:"finished_at,omitempty"` // unix seconds
	ExitCode   *int     `json:"exit_code,omitempty"`
	Log        []string `json:"log,omitempty"` // the run's last agentInstallLogLines output lines
}

// agentInstaller runs install scripts, at most one at a time per agent.
// scriptDirs lists the directories to look for scripts in, in order; run
// starts one (swappable by tests); done is called after every run.
type agentInstaller struct {
	notify     func()
	done       func()
	scriptDirs func() []string
	run        func(ctx context.Context, script string, out io.Writer) (int, error)

	mu    sync.Mutex
	runs  map[string]*AgentInstall
	dirty bool
}

func newAgentInstaller(notify, done func(), scriptDirs func() []string) *agentInstaller {
	return &agentInstaller{
		notify:     notify,
		done:       done,
		scriptDirs: scriptDirs,
		run:        runInstallScript,
		runs:       map[string]*AgentInstall{},
	}
}

// installScriptDirs is the default agentInstaller.scriptDirs: the watched
// --dev-repo checkout's scripts/ (the freshest copy), then the copies 'make
// deploy-dev-binaries' / 'update-dev-binaries' leave in the dev bin dir, and
// next to LR's binary.
func (s *Server) installScriptDirs() []string {
	var dirs []string
	if s.repoWatch != nil {
		dirs = append(dirs, filepath.Join(s.repoWatch.root, "scripts"))
	}
	dirs = append(dirs, filepath.Join(devBinDir(), "scripts"))
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "scripts"))
	}
	return dirs
}

// installAgent starts agent's install script -- the debug -> install tab's
// "install" control ("install-agent" / "__system:install-agent"). A refusal
// is logged and kept as that agent's failed run, so the tab shows why.
func (s *Server) installAgent(agent string) {
	if err := s.agentInstalls.start(agent); err != nil {
		log.Printf("agents: install %q refused: %v", agent, err)
		s.agentInstalls.refused(agent, err)
	}
}

// refused records a start that didn't run, for an installable agent that
// isn't already being installed.
func (a *agentInstaller) refused(agent string, err error) {
	if a == nil {
		return
	}
	if _, ok := agentInstallScripts[agent]; !ok {
		return
	}
	a.mu.Lock()
	if r := a.runs[agent]; r != nil && r.State == installRunning {
		a.mu.Unlock()
		return
	}
	now := time.Now().Unix()
	a.runs[agent] = &AgentInstall{Agent: agent, State: installFailed, Detail: err.Error(), StartedAt: now, FinishedAt: now}
	a.mu.Unlock()
	a.push()
}

// findScript locates agent's install script, or explains why it can't.
func (a *agentInstaller) findScript(agent string) (string, error) {
	name, ok := agentInstallScripts[agent]
	if !ok {
		return "", fmt.Errorf("agent %q has no install script", agent)
	}
	var dirs []string
	if a.scriptDirs != nil {
		dirs = a.scriptDirs()
	}
	seen := map[string]bool{}
	var looked []string
	for _, d := range dirs {
		if seen[d] {
			continue
		}
		seen[d] = true
		looked = append(looked, d)
		p := filepath.Join(d, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found (looked in %v)", name, looked)
}

// snapshot lists every installable agent, sorted by name, with its script
// and latest run; nil-safe.
func (a *agentInstaller) snapshot() []AgentInstall {
	if a == nil {
		return []AgentInstall{}
	}
	names := make([]string, 0, len(agentInstallScripts))
	for name := range agentInstallScripts {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]AgentInstall, 0, len(names))
	for _, name := range names {
		a.mu.Lock()
		var st AgentInstall
		if r := a.runs[name]; r != nil {
			st = *r
			st.Log = append([]string{}, r.Log...)
		}
		a.mu.Unlock()
		st.Agent = name
		if st.State != installRunning {
			if script, err := a.findScript(name); err != nil {
				st.Script = ""
				if st.State == "" {
					st.Detail = err.Error()
				}
			} else {
				st.Script = script
			}
		}
		out = append(out, st)
	}
	return out
}

// start runs agent's install script in the background. It fails when the
// agent isn't installable, its script can't be found, or it's already
// being installed.
func (a *agentInstaller) start(agent string) error {
	if a == nil {
		return errors.New("agent installs aren't available")
	}
	script, err := a.findScript(agent)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if r := a.runs[agent]; r != nil && r.State == installRunning {
		a.mu.Unlock()
		return fmt.Errorf("%s is already being installed", agent)
	}
	r := &AgentInstall{Agent: agent, Script: script, State: installRunning, StartedAt: time.Now().Unix()}
	a.runs[agent] = r
	a.mu.Unlock()
	log.Printf("agents: installing %s with %s", agent, script)
	a.push()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), agentInstallTimeout)
		defer cancel()
		stop := make(chan struct{})
		go a.notifyWhileRunning(stop)
		code, err := a.run(ctx, script, &installLogWriter{a: a, agent: agent})
		close(stop)

		a.mu.Lock()
		r.FinishedAt = time.Now().Unix()
		r.ExitCode = &code
		switch {
		case ctx.Err() == context.DeadlineExceeded:
			r.State, r.Detail = installFailed, fmt.Sprintf("timed out after %s", agentInstallTimeout)
		case err != nil:
			r.State, r.Detail = installFailed, err.Error()
		case code != 0:
			r.State, r.Detail = installFailed, fmt.Sprintf("%s exited %d", filepath.Base(script), code)
		default:
			r.State, r.Detail = installSucceeded, ""
		}
		state, detail := r.State, r.Detail
		a.mu.Unlock()
		if detail != "" {
			log.Printf("agents: installing %s %s: %s", agent, state, detail)
		} else {
			log.Printf("agents: installing %s %s", agent, state)
		}
		a.push()
		if a.done != nil {
			a.done()
		}
	}()
	return nil
}

// appendLog adds one output line to agent's running install.
func (a *agentInstaller) appendLog(agent, line string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := a.runs[agent]
	if r == nil {
		return
	}
	r.Log = append(r.Log, line)
	if over := len(r.Log) - agentInstallLogLines; over > 0 {
		r.Log = append([]string{}, r.Log[over:]...)
	}
	a.dirty = true
}

// notifyWhileRunning pushes the state at most every agentInstallNotifyEvery
// while new output keeps arriving, until stop closes.
func (a *agentInstaller) notifyWhileRunning(stop <-chan struct{}) {
	t := time.NewTicker(agentInstallNotifyEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			a.mu.Lock()
			dirty := a.dirty
			a.dirty = false
			a.mu.Unlock()
			if dirty && a.notify != nil {
				a.notify()
			}
		}
	}
}

func (a *agentInstaller) push() {
	a.mu.Lock()
	a.dirty = false
	a.mu.Unlock()
	if a.notify != nil {
		a.notify()
	}
}

// installLogWriter splits a script's combined output into log lines.
type installLogWriter struct {
	a       *agentInstaller
	agent   string
	partial []byte
}

func (w *installLogWriter) Write(p []byte) (int, error) {
	buf := append(w.partial, p...)
	for {
		i := indexLineEnd(buf)
		if i < 0 {
			break
		}
		w.a.appendLog(w.agent, string(buf[:i]))
		if buf[i] == '\r' && i+1 < len(buf) && buf[i+1] == '\n' {
			i++ // one "\r\n" ends one line
		}
		buf = buf[i+1:]
	}
	w.partial = append([]byte{}, buf...)
	return len(p), nil
}

// Flush logs any unterminated last line.
func (w *installLogWriter) Flush() {
	if len(w.partial) > 0 {
		w.a.appendLog(w.agent, string(w.partial))
		w.partial = nil
	}
}

// indexLineEnd finds the first '\n' or '\r' (curl/npm progress redraws a
// line with '\r'), or -1.
func indexLineEnd(b []byte) int {
	for i, c := range b {
		if c == '\n' || c == '\r' {
			return i
		}
	}
	return -1
}

// runInstallScript is the default agentInstaller.run: "bash <script>" in the
// script's directory, stdout and stderr both into out, no stdin. It returns
// the exit code, and an error only when the script couldn't be run at all.
func runInstallScript(ctx context.Context, script string, out io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Dir = filepath.Dir(script)
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	if f, ok := out.(interface{ Flush() }); ok {
		f.Flush()
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), nil
	default:
		return -1, err
	}
}
