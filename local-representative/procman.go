package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ProcInfo is a snapshot of one process shown on the "system" tab: either
// local-representative itself or a child application it manages.
type ProcInfo struct {
	Name      string `json:"name"`
	PID       int    `json:"pid"`
	Status    string `json:"status"` // "running", "exited", "failed"
	Managed   bool   `json:"managed"`
	StartedAt int64  `json:"started_at"`       // unix seconds
	ExitCode  int    `json:"exit_code"`        // meaningful once status != "running"
	Detail    string `json:"detail,omitempty"` // launch/exit error text, if any
}

// SystemStateMsg is the payload of "system-state" WebSocket messages.
type SystemStateMsg struct {
	Self    ProcInfo   `json:"self"`
	Managed []ProcInfo `json:"managed"`
}

// launchSpec describes how to start one managed application.
type launchSpec struct {
	binName   string                   // executable name to resolve
	buildArgs func(s *Server) []string // argv after the program name
}

// managedApps is the fixed set of applications local-representative knows how to
// launch. For now only federation-command; adding an entry here is all it takes
// to expose another app on the system tab and to auto-launch.
var managedApps = map[string]launchSpec{
	"federation-command": {
		binName: "federation-command",
		buildArgs: func(s *Server) []string {
			// Bring FC up already wired to this LR: --auto-connect makes it retry
			// the heartbeat connection in the background, --lr-port points it at
			// our representable server.
			return []string{"--auto-connect", "--lr-host", "localhost", "--lr-port", s.heartbeatPort}
		},
	},
}

// managedProc tracks a single child process launched from the system tab.
type managedProc struct {
	name      string
	cmd       *exec.Cmd
	pid       int
	startedAt time.Time

	mu          sync.Mutex
	status      string // "running", "exited", "failed"
	exitCode    int
	detail      string
	terminating bool // an operator-requested stop is in flight
}

func (p *managedProc) state() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *managedProc) info() ProcInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return ProcInfo{
		Name:      p.name,
		PID:       p.pid,
		Status:    p.status,
		Managed:   true,
		StartedAt: p.startedAt.Unix(),
		ExitCode:  p.exitCode,
		Detail:    p.detail,
	}
}

// systemState returns the current system-tab snapshot: LR itself plus every
// managed child (running or finished).
func (s *Server) systemState() SystemStateMsg {
	s.procMu.Lock()
	procs := make([]ProcInfo, 0, len(s.managed))
	for _, p := range s.managed {
		procs = append(procs, p.info())
	}
	s.procMu.Unlock()
	sort.Slice(procs, func(i, j int) bool { return procs[i].Name < procs[j].Name })

	return SystemStateMsg{
		Self: ProcInfo{
			Name:      s.lrName,
			PID:       os.Getpid(),
			Status:    "running",
			Managed:   false,
			StartedAt: s.selfStart.Unix(),
		},
		Managed: procs,
	}
}

func (s *Server) broadcastSystemState() {
	s.broadcast("system-state", s.systemState())
}

// resolveAppBinary locates the executable for a managed application. It checks,
// in order: an explicit config override, the directory holding the LR binary,
// $AI_EVO1_DEV_BIN (default /AI-evo1-dev/bin), and finally $PATH.
func (s *Server) resolveAppBinary(name, binName string) (string, error) {
	if ov := s.binOverrides[name]; ov != "" {
		if info, err := os.Stat(ov); err != nil || info.IsDir() {
			return "", fmt.Errorf("configured %s binary %q is not usable", name, ov)
		}
		return ov, nil
	}

	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), binName))
	}
	devBin := os.Getenv("AI_EVO1_DEV_BIN")
	if devBin == "" {
		devBin = "/AI-evo1-dev/bin"
	}
	candidates = append(candidates, filepath.Join(devBin, binName))

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}
	if p, err := exec.LookPath(binName); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("could not locate %q (looked next to local-representative, in %s, and on PATH)", binName, devBin)
}

// launchManaged starts a managed application by name. It is an error to launch
// one that is already running.
func (s *Server) launchManaged(name string) error {
	spec, ok := managedApps[name]
	if !ok {
		return fmt.Errorf("unknown application %q", name)
	}

	s.procMu.Lock()
	if p := s.managed[name]; p != nil && p.state() == "running" {
		pid := p.pid
		s.procMu.Unlock()
		return fmt.Errorf("%s is already running (pid %d)", name, pid)
	}
	s.procMu.Unlock()

	bin, err := s.resolveAppBinary(name, spec.binName)
	if err != nil {
		s.recordLaunchFailure(name, err)
		return err
	}

	args := spec.buildArgs(s)
	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	// Own process group so terminate can signal the whole child tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Fan the child's output into LR's own log stream. Separate writers for
	// stdout/stderr so the exec copy goroutines never share one (no lock needed),
	// and so Wait() blocks until both are drained (no pipe close race).
	cmd.Stdout = &lineLogWriter{prefix: name}
	cmd.Stderr = &lineLogWriter{prefix: name}

	if err := cmd.Start(); err != nil {
		s.recordLaunchFailure(name, err)
		return err
	}

	p := &managedProc{
		name:      name,
		cmd:       cmd,
		pid:       cmd.Process.Pid,
		startedAt: time.Now(),
		status:    "running",
	}
	s.procMu.Lock()
	s.managed[name] = p
	s.procMu.Unlock()

	log.Printf("system: launched %s (pid %d): %s %v", name, p.pid, bin, args)

	go s.reapManaged(p)

	s.broadcastSystemState()
	return nil
}

// reapManaged waits for a child to exit and records its final status.
func (s *Server) reapManaged(p *managedProc) {
	err := p.cmd.Wait()

	p.mu.Lock()
	switch {
	case err == nil:
		p.status = "exited"
		p.exitCode = 0
	case p.terminating:
		// Operator asked for this stop; not a failure.
		p.status = "exited"
		if ee, ok := err.(*exec.ExitError); ok {
			p.exitCode = ee.ExitCode()
		}
	default:
		p.status = "failed"
		if ee, ok := err.(*exec.ExitError); ok {
			p.exitCode = ee.ExitCode()
		} else {
			p.exitCode = -1
		}
		p.detail = err.Error()
	}
	status, code := p.status, p.exitCode
	p.mu.Unlock()

	log.Printf("system: %s (pid %d) %s (exit %d)", p.name, p.pid, status, code)
	s.broadcastSystemState()
}

// terminateManaged signals a running managed application to stop; if the target
// has already exited it is simply dropped from the system-tab list.
func (s *Server) terminateManaged(name string) error {
	s.procMu.Lock()
	p := s.managed[name]
	s.procMu.Unlock()
	if p == nil {
		return fmt.Errorf("%s is not managed by this local-representative", name)
	}

	if p.state() != "running" {
		s.procMu.Lock()
		delete(s.managed, name)
		s.procMu.Unlock()
		s.broadcastSystemState()
		return nil
	}

	p.mu.Lock()
	p.terminating = true
	p.mu.Unlock()

	log.Printf("system: terminating %s (pid %d)", name, p.pid)
	if p.cmd != nil && p.cmd.Process != nil {
		if err := syscall.Kill(-p.pid, syscall.SIGTERM); err != nil {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
	}

	// Escalate to SIGKILL if it hasn't gone after a grace period.
	go func() {
		time.Sleep(5 * time.Second)
		if p.state() == "running" && p.cmd != nil && p.cmd.Process != nil {
			log.Printf("system: %s ignored SIGTERM, sending SIGKILL", name)
			if err := syscall.Kill(-p.pid, syscall.SIGKILL); err != nil {
				_ = p.cmd.Process.Kill()
			}
		}
	}()
	return nil
}

// recordLaunchFailure keeps a failed launch visible on the system tab instead of
// silently dropping it.
func (s *Server) recordLaunchFailure(name string, cause error) {
	s.procMu.Lock()
	s.managed[name] = &managedProc{
		name:      name,
		startedAt: time.Now(),
		status:    "failed",
		exitCode:  -1,
		detail:    cause.Error(),
	}
	s.procMu.Unlock()
	log.Printf("system: failed to launch %s: %v", name, cause)
	s.broadcastSystemState()
}

// lineLogWriter buffers writes from a child process and emits one LR log line
// per newline-terminated chunk. Each instance is written by a single goroutine.
type lineLogWriter struct {
	prefix string
	buf    []byte
}

func (w *lineLogWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimRight(string(w.buf[:i]), "\r"); line != "" {
			log.Printf("[%s] %s", w.prefix, line)
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// startAutoLaunch launches each configured child application shortly after
// startup, in its own goroutine so a slow or failing launch never blocks LR
// from coming up.
func (s *Server) startAutoLaunch(names []string) {
	go func() {
		for _, name := range names {
			if _, ok := managedApps[name]; !ok {
				log.Printf("auto-launch: skipping unknown application %q", name)
				continue
			}
			log.Printf("auto-launch: starting %s", name)
			if err := s.launchManaged(name); err != nil {
				log.Printf("auto-launch: %s failed: %v", name, err)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
}
