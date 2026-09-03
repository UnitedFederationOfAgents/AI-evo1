package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ProcInfo is a snapshot of one process shown on the "system" tab: either
// local-representative itself or a child application instance it manages.
type ProcInfo struct {
	Name       string `json:"name"`        // application name, e.g. "federation-command"
	InstanceID string `json:"instance_id"` // unique key for a managed instance ("" for self)
	Instance   int    `json:"instance"`    // per-app ordinal (0 for self / the first singleton)
	PID        int    `json:"pid"`
	Status     string `json:"status"` // "running", "exited", "failed"
	Managed    bool   `json:"managed"`
	StartedAt  int64  `json:"started_at"`       // unix seconds
	ExitCode   int    `json:"exit_code"`        // meaningful once status != "running"
	Detail     string `json:"detail,omitempty"` // launch/exit error text, if any
}

// SystemStateMsg is the payload of "system-state" WebSocket messages.
type SystemStateMsg struct {
	Self    ProcInfo   `json:"self"`
	Managed []ProcInfo `json:"managed"`
}

// launchSpec describes how to start one managed application.
type launchSpec struct {
	binName   string                   // executable name to resolve
	singleton bool                     // true: at most one running instance per host; false: N-per-host
	terminal  bool                     // true: an interactive TUI that must be hosted in a terminal
	buildArgs func(s *Server) []string // argv after the program name
	buildEnv  func(s *Server) []string // extra KEY=VALUE entries appended to the child environment
}

// managedApps is the fixed set of applications local-representative knows how to
// launch. For now only federation-command; adding an entry here is all it takes
// to expose another app on the system tab and to auto-launch.
var managedApps = map[string]launchSpec{
	"federation-command": {
		binName:   "federation-command",
		singleton: false, // federation-command is N-per-host
		terminal:  true,  // it is an interactive shell — needs a real terminal
		buildArgs: func(s *Server) []string {
			// Bring FC up already wired to this LR. --auto-connect makes it retry
			// the heartbeat connection in the background, --lr-port points it at
			// our representable server, and --remote makes it adopt remote control
			// on connect: a fully machine-driven auto-launch chain lands ready to
			// drive from local-representative rather than in the foreground.
			return []string{"--auto-connect", "--remote", "--lr-host", "localhost", "--lr-port", s.heartbeatPort}
		},
		buildEnv: func(s *Server) []string {
			// Belt-and-braces with buildArgs: a terminal emulator or multiplexer
			// wrapper can swallow or re-quote trailing argv, which would drop
			// --remote and leave FC connecting in *local* control — unusable in a
			// machine-driven chain because it then needs a keystroke at the FC
			// terminal to hand control to LR. Environment variables pass through
			// every wrapper untouched, and FC honours them below CLI flags.
			return []string{
				"FC_AUTO_CONNECT=1",
				"FC_REMOTE=1",
				"FC_LR_HOST=localhost",
				"FC_LR_PORT=" + s.heartbeatPort,
			}
		},
	},
}

// managedProc tracks a single child process instance launched from the system tab.
type managedProc struct {
	app        string // application name
	instanceID string // unique key, e.g. "federation-command#2"
	instance   int    // per-app ordinal
	cmd        *exec.Cmd
	pid        int
	startedAt  time.Time

	// Set when the instance is hosted in a detached terminal multiplexer
	// (tmux/screen): the launcher returns immediately and the child keeps
	// running inside its own session, so LR can't wait(2) on it. It tears the
	// session down with killCmd on terminate and shows attachHint in the UI.
	detached   bool
	termLabel  string   // chosen terminal, e.g. "tmux" ("" when launched directly)
	killCmd    []string // argv that stops a detached session (nil if unknown)
	attachHint string   // how an operator reconnects to interact

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
		Name:       p.app,
		InstanceID: p.instanceID,
		Instance:   p.instance,
		PID:        p.pid,
		Status:     p.status,
		Managed:    true,
		StartedAt:  p.startedAt.Unix(),
		ExitCode:   p.exitCode,
		Detail:     p.detail,
	}
}

// systemState returns the current system-tab snapshot: LR itself plus every
// managed instance (running or finished), ordered by app then instance ordinal.
func (s *Server) systemState() SystemStateMsg {
	s.procMu.Lock()
	procs := make([]ProcInfo, 0, len(s.managed))
	for _, p := range s.managed {
		procs = append(procs, p.info())
	}
	s.procMu.Unlock()
	sort.Slice(procs, func(i, j int) bool {
		if procs[i].Name != procs[j].Name {
			return procs[i].Name < procs[j].Name
		}
		return procs[i].Instance < procs[j].Instance
	})

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

// nextInstanceLocked allocates the next per-app ordinal and its instance id.
// Callers must hold procMu.
func (s *Server) nextInstanceLocked(app string) (int, string) {
	s.instanceSeq[app]++
	n := s.instanceSeq[app]
	return n, fmt.Sprintf("%s#%d", app, n)
}

// runningCountLocked reports how many instances of app are currently running.
// Callers must hold procMu.
func (s *Server) runningCountLocked(app string) int {
	n := 0
	for _, p := range s.managed {
		if p.app == app && p.state() == "running" {
			n++
		}
	}
	return n
}

// resolveInstanceLocked maps a target that is either an instance id or (when only
// one instance is present) a bare app name to a concrete instance id. Returns ""
// if the target is ambiguous or unknown. Callers must hold procMu.
func (s *Server) resolveInstanceLocked(target string) string {
	if _, ok := s.managed[target]; ok {
		return target
	}
	var match string
	n := 0
	for id, p := range s.managed {
		if p.app == target {
			match, n = id, n+1
		}
	}
	if n == 1 {
		return match
	}
	return ""
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

// terminalCandidate is one way to host an interactive child in a terminal. args
// are the fixed arguments that precede the child command.
type terminalCandidate struct {
	prog string
	args []string
}

// terminalCandidates lists the terminal emulators / multiplexers probed, in
// order, when no explicit "terminal" override is configured. A visible window is
// what we want: the operator should see federation-command come up. Foreground
// emulators (xterm, konsole, alacritty, kitty, foot, wezterm) come first because
// LR execs the child directly under them and keeps full PID / lifecycle /
// terminate tracking. The double-forking emulators follow. tmux and screen are
// last: they only produce a *detached* (invisible) session, so they are a
// headless last resort, not the preferred host. Focus stealing is handled
// separately — see terminalLaunchEnv. tmux and screen are special-cased in
// wrapInTerminal because they need a generated session name.
var terminalCandidates = []terminalCandidate{
	{prog: "xterm", args: []string{"-e"}},
	{prog: "konsole", args: []string{"-e"}},
	{prog: "alacritty", args: []string{"-e"}},
	{prog: "kitty", args: nil},
	{prog: "foot", args: nil},
	{prog: "wezterm", args: []string{"start", "--"}},
	{prog: "xfce4-terminal", args: []string{"-x"}},
	{prog: "x-terminal-emulator", args: []string{"-e"}},
	{prog: "gnome-terminal", args: []string{"--"}},
	{prog: "tmux", args: nil},
	{prog: "screen", args: nil},
}

// terminalLaunchEnv returns the environment for a terminal-hosted child with the
// X11 / Wayland startup-notification tokens stripped. DESKTOP_STARTUP_ID and
// XDG_ACTIVATION_TOKEN are how a newly mapped window asks the window manager /
// compositor to raise and focus it; without them an EWMH-compliant WM maps the
// terminal window *visible but unfocused*, so an auto-launched federation-command
// no longer steals focus from whatever the operator is doing. The window is
// still shown — this only suppresses the activation request, not the window.
func terminalLaunchEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		if strings.HasPrefix(kv, "DESKTOP_STARTUP_ID=") || strings.HasPrefix(kv, "XDG_ACTIVATION_TOKEN=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// terminalHosting records how a terminal-wrapped launch runs so the reaper and
// terminate path can treat a detached multiplexer session (which returns control
// the instant it is created) differently from a foreground emulator LR waits on.
type terminalHosting struct {
	detached   bool     // launcher backgrounds the child in its own session
	label      string   // chosen terminal, e.g. "tmux"
	killCmd    []string // argv that stops a detached session (nil if unknown)
	attachHint string   // how an operator reconnects to interact
}

// wrapInTerminal returns the program, argv and hosting metadata to exec so that
// bin (with appArgs) runs inside an interactive terminal. It honours the
// configured "terminal" override first — a space-separated command prefix, e.g.
// `xterm -e` or `tmux new-session -d -s fc` — otherwise it probes
// terminalCandidates on $PATH, preferring a visible windowed emulator (which
// keeps full lifecycle tracking) and falling back to a detached tmux/screen
// session only where no emulator exists. Focus stealing by the new window is
// suppressed via terminalLaunchEnv. If nothing is found it returns an actionable
// error rather than letting federation-command fail deep inside its input reader
// ("error creating cancelreader").
func (s *Server) wrapInTerminal(bin string, appArgs []string) (string, []string, terminalHosting, error) {
	child := append([]string{bin}, appArgs...)

	if ov := strings.TrimSpace(s.terminalCmd); ov != "" {
		parts := strings.Fields(ov)
		h := terminalHosting{label: filepath.Base(parts[0])}
		// A `-d` / `-dm` in the override (tmux/screen style) means the launcher
		// detaches; LR then can't wait on the child. We don't know the session
		// name the operator chose, so teardown falls back to "close it yourself".
		for _, p := range parts[1:] {
			if p == "-d" || p == "-dm" || p == "-dmS" || strings.HasPrefix(p, "-dm") {
				h.detached = true
				h.attachHint = "attach to the '" + h.label + "' session you configured"
			}
		}
		return parts[0], append(append([]string{}, parts[1:]...), child...), h, nil
	}

	for _, c := range terminalCandidates {
		path, err := exec.LookPath(c.prog)
		if err != nil {
			continue
		}
		switch c.prog {
		case "tmux":
			sess := "fc-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			return path, append([]string{"new-session", "-d", "-s", sess}, child...),
				terminalHosting{
					detached:   true,
					label:      "tmux",
					killCmd:    []string{path, "kill-session", "-t", sess},
					attachHint: "tmux attach -t " + sess,
				}, nil
		case "screen":
			sess := "fc-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			return path, append([]string{"-dmS", sess}, child...),
				terminalHosting{
					detached:   true,
					label:      "screen",
					killCmd:    []string{path, "-S", sess, "-X", "quit"},
					attachHint: "screen -r " + sess,
				}, nil
		default:
			return path, append(append([]string{}, c.args...), child...),
				terminalHosting{label: c.prog}, nil
		}
	}
	return "", nil, terminalHosting{}, fmt.Errorf(
		"no terminal found to host %s (it is an interactive shell); set 'terminal' in config "+
			"(e.g. terminal: \"xterm -e\", or terminal: \"tmux new-session -d -s fc\" for a detached fallback) "+
			"or install one of: xterm, konsole, gnome-terminal, x-terminal-emulator, tmux, screen",
		filepath.Base(bin))
}

// launchManaged starts a new instance of a managed application. Singleton apps
// reject a launch while an instance is already running; N-per-host apps (e.g.
// federation-command) may have many. Returns the new instance id (also set on a
// recorded failure so the caller/UI can correlate).
func (s *Server) launchManaged(app string) (string, error) {
	spec, ok := managedApps[app]
	if !ok {
		return "", fmt.Errorf("unknown application %q", app)
	}

	s.procMu.Lock()
	if spec.singleton && s.runningCountLocked(app) > 0 {
		s.procMu.Unlock()
		return "", fmt.Errorf("%s is already running", app)
	}
	instance, id := s.nextInstanceLocked(app)
	s.procMu.Unlock()

	bin, err := s.resolveAppBinary(app, spec.binName)
	if err != nil {
		s.recordLaunchFailure(app, instance, id, err)
		return id, err
	}

	appArgs := spec.buildArgs(s)
	prog, args := bin, appArgs
	var hosting terminalHosting
	if spec.terminal {
		// Interactive TUI shells must run in a real terminal or their input
		// reader dies on startup. Wrap the launch in a visible terminal emulator
		// (falling back to a detached tmux/screen session only where none is
		// installed); terminalLaunchEnv keeps the window from stealing focus.
		prog, args, hosting, err = s.wrapInTerminal(bin, appArgs)
		if err != nil {
			s.recordLaunchFailure(app, instance, id, err)
			return id, err
		}
	}

	cmd := exec.Command(prog, args...)
	if spec.terminal {
		// Visible terminal window, but without the startup-notification token
		// that would make the WM raise/focus it over the operator's work.
		cmd.Env = terminalLaunchEnv()
	} else {
		cmd.Env = os.Environ()
	}
	if spec.buildEnv != nil {
		cmd.Env = append(cmd.Env, spec.buildEnv(s)...)
	}
	// Own process group so terminate can signal the whole child tree (terminal
	// wrapper included).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Fan the wrapper's own output into LR's log stream. The interactive child
	// itself draws in its terminal window; this only catches launcher errors.
	cmd.Stdout = &lineLogWriter{prefix: id}
	cmd.Stderr = &lineLogWriter{prefix: id}

	if err := cmd.Start(); err != nil {
		s.recordLaunchFailure(app, instance, id, err)
		return id, err
	}

	p := &managedProc{
		app:        app,
		instanceID: id,
		instance:   instance,
		cmd:        cmd,
		pid:        cmd.Process.Pid,
		startedAt:  time.Now(),
		status:     "running",
		detached:   hosting.detached,
		termLabel:  hosting.label,
		killCmd:    hosting.killCmd,
		attachHint: hosting.attachHint,
	}
	s.procMu.Lock()
	s.managed[id] = p
	s.procMu.Unlock()

	log.Printf("system: launched %s (pid %d): %s %v", id, p.pid, prog, args)
	if hosting.detached && hosting.attachHint != "" {
		log.Printf("system: %s runs in a detached %s session — %s", id, hosting.label, hosting.attachHint)
	}

	go s.reapManaged(p)

	s.broadcastSystemState()
	return id, nil
}

// reapManaged waits for a child to exit and records its final status.
func (s *Server) reapManaged(p *managedProc) {
	err := p.cmd.Wait()

	if p.detached && err == nil {
		// A detached multiplexer launcher (tmux new-session -d, screen -dmS)
		// returns as soon as the session exists; federation-command keeps
		// running inside it. We can't wait(2) on the real child, so record it as
		// running-but-untracked and point the operator at how to attach.
		p.mu.Lock()
		p.status = "running"
		p.pid = 0
		if p.detail == "" {
			p.detail = "hosted in a detached " + p.termLabel + " session — " + p.attachHint
		}
		p.mu.Unlock()
		log.Printf("system: %s handed off to a detached %s session — %s", p.instanceID, p.termLabel, p.attachHint)
		s.broadcastSystemState()
		return
	}

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

	log.Printf("system: %s (pid %d) %s (exit %d)", p.instanceID, p.pid, status, code)
	s.broadcastSystemState()
}

// terminateManaged signals a running managed instance to stop; if the target has
// already exited it is simply dropped from the system-tab list. target is an
// instance id, or a bare app name when only one instance is present.
func (s *Server) terminateManaged(target string) error {
	s.procMu.Lock()
	id := s.resolveInstanceLocked(target)
	p := s.managed[id]
	s.procMu.Unlock()
	if p == nil {
		return fmt.Errorf("%q is not managed by this local-representative", target)
	}

	if p.detached {
		// LR never owned the child directly (it runs inside a detached
		// tmux/screen session), so there is no process group to signal. Tear the
		// session down with the recorded command and drop the entry.
		s.procMu.Lock()
		delete(s.managed, id)
		s.procMu.Unlock()
		if len(p.killCmd) > 0 {
			log.Printf("system: stopping detached session for %s: %v", id, p.killCmd)
			if out, err := exec.Command(p.killCmd[0], p.killCmd[1:]...).CombinedOutput(); err != nil {
				log.Printf("system: %s session teardown failed: %v (%s)", id, err, strings.TrimSpace(string(out)))
			}
		} else {
			log.Printf("system: %s ran in a detached terminal LR cannot address — close its session manually", id)
		}
		s.broadcastSystemState()
		return nil
	}

	if p.state() != "running" {
		s.procMu.Lock()
		delete(s.managed, id)
		s.procMu.Unlock()
		s.broadcastSystemState()
		return nil
	}

	p.mu.Lock()
	p.terminating = true
	p.mu.Unlock()

	log.Printf("system: terminating %s (pid %d)", id, p.pid)
	if p.cmd != nil && p.cmd.Process != nil {
		if err := syscall.Kill(-p.pid, syscall.SIGTERM); err != nil {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
	}

	// Escalate to SIGKILL if it hasn't gone after a grace period.
	go func() {
		time.Sleep(5 * time.Second)
		if p.state() == "running" && p.cmd != nil && p.cmd.Process != nil {
			log.Printf("system: %s ignored SIGTERM, sending SIGKILL", id)
			if err := syscall.Kill(-p.pid, syscall.SIGKILL); err != nil {
				_ = p.cmd.Process.Kill()
			}
		}
	}()
	return nil
}

// recordLaunchFailure keeps a failed launch visible on the system tab instead of
// silently dropping it.
func (s *Server) recordLaunchFailure(app string, instance int, id string, cause error) {
	s.procMu.Lock()
	s.managed[id] = &managedProc{
		app:        app,
		instanceID: id,
		instance:   instance,
		startedAt:  time.Now(),
		status:     "failed",
		exitCode:   -1,
		detail:     cause.Error(),
	}
	s.procMu.Unlock()
	log.Printf("system: failed to launch %s: %v", id, cause)
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

// autoLaunchEntry is one parsed auto-launch token: an app and how many instances
// of it to start on boot.
type autoLaunchEntry struct {
	app   string
	count int
}

// parseAutoLaunchEntry parses a single auto-launch token of the form "app" or
// "app:N" (N >= 1). federation-command is N-per-host, so "federation-command:3"
// brings up three instances.
func parseAutoLaunchEntry(tok string) (autoLaunchEntry, error) {
	name, countStr, hasCount := strings.Cut(tok, ":")
	name = strings.TrimSpace(name)
	if name == "" {
		return autoLaunchEntry{}, fmt.Errorf("empty auto-launch entry %q", tok)
	}
	e := autoLaunchEntry{app: name, count: 1}
	if hasCount {
		n, err := strconv.Atoi(strings.TrimSpace(countStr))
		if err != nil || n < 1 {
			return autoLaunchEntry{}, fmt.Errorf("invalid instance count in auto-launch entry %q", tok)
		}
		e.count = n
	}
	return e, nil
}

// startAutoLaunch launches each configured child application shortly after
// startup, in its own goroutine so a slow or failing launch never blocks LR
// from coming up. Tokens are "app" or "app:N".
func (s *Server) startAutoLaunch(tokens []string) {
	go func() {
		for _, tok := range tokens {
			entry, err := parseAutoLaunchEntry(tok)
			if err != nil {
				log.Printf("auto-launch: %v", err)
				continue
			}
			if _, ok := managedApps[entry.app]; !ok {
				log.Printf("auto-launch: skipping unknown application %q", entry.app)
				continue
			}
			for i := 0; i < entry.count; i++ {
				log.Printf("auto-launch: starting %s (%d/%d)", entry.app, i+1, entry.count)
				if _, err := s.launchManaged(entry.app); err != nil {
					log.Printf("auto-launch: %s failed: %v", entry.app, err)
				}
				time.Sleep(500 * time.Millisecond)
			}
		}
	}()
}
