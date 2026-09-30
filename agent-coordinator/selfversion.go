package main

import (
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// selfVersionPollInterval is how often the self-update watcher (see
// newSelfVersionWatch) re-checks whether the on-disk binary this process was
// launched from now answers "--version" differently than the version
// compiled into the process currently running -- see
// condocs/initialDistributedDevelopmentImpls/Step3Prompt.md Revision B.
const selfVersionPollInterval = 5 * time.Second

// selfVersionWatch periodically polls this process's own executable path with
// '--version' and compares the answer against the version compiled into the
// binary that is actually running (ufaversion.Version, captured as running).
// A mismatch means the file on disk has been replaced with a newer build
// since this process started -- exactly the situation ufa-loader's restart
// is for -- so the global topology view's "restart agent-coordinator"
// control can relabel itself "restart and update" instead of a plain
// "restart" (see docs/DevMode.md "Loader").
//
// This is a deliberate duplicate of local-representative's own
// selfVersionWatch (local-representative/selfversion.go) rather than a
// shared package: it's a legitimate deployment to run agent-coordinator
// alone on a box with only off-node local-representatives, so AC has to
// detect its own on-disk binary drifting independently of any LR -- see
// condocs/initialDistributedDevelopmentImpls/Step4Prompt.md Revision E.
//
// Polling "--version" rather than watching the file directly (e.g. mtime or
// a content hash) keeps this consistent with every other version check in
// the codebase (ufaversion.HandleVersionFlag): it's what "the file changed"
// actually means for a UFA binary, not merely that some byte moved.
type selfVersionWatch struct {
	binPath string
	running string // the version compiled into this running process
	notify  func()

	// restart is invoked (outside the lock, see poll/setAutoUpdate) whenever
	// autoUpdate is on and updateAvailable is -- or just became -- true. Set
	// to the Server's own requestRestart by newSelfVersionWatch; nil in a
	// bare struct literal (as every existing test here uses) is fine since
	// both call sites only reach it once autoUpdate has actually been turned
	// on. Mirrors local-representative's own field of the same name --
	// see condocs/initialShellsSessionManagerAndTheConversationalistImpls/
	// Step1SubstepCPrompt.md Revision D.
	restart func()

	mu              sync.RWMutex
	updateAvailable bool
	pendingVersion  string // last on-disk "--version" answer that triggered updateAvailable; "" if none
	autoUpdate      bool
}

// newSelfVersionWatch resolves this process's own executable path. It
// returns nil (disabling update detection, not the restart control itself)
// if the path can't be resolved.
func newSelfVersionWatch(running string, notify func(), restart func()) *selfVersionWatch {
	bin, err := os.Executable()
	if err != nil {
		log.Printf("self-version: could not resolve own executable path: %v -- update detection disabled", err)
		return nil
	}
	return &selfVersionWatch{binPath: bin, running: running, notify: notify, restart: restart}
}

// poll re-derives updateAvailable from the on-disk binary's "--version"
// output, notifying (if the verdict changed) so a fresh self-info can be
// broadcast. If auto-update is on and an update is available -- whether it
// just landed or was already sitting there when auto-update was turned on --
// this also fires restart, the same as pressing "restart and update AC" by
// hand (see setAutoUpdate for the other trigger of that).
func (w *selfVersionWatch) poll() {
	out, err := exec.Command(w.binPath, "--version").Output()
	if err != nil {
		log.Printf("self-version: %s --version failed: %v", w.binPath, err)
		return
	}
	onDisk := strings.TrimSpace(string(out))
	updated := onDisk != w.running

	w.mu.Lock()
	changed := w.updateAvailable != updated
	w.updateAvailable = updated
	w.pendingVersion = onDisk
	shouldRestart := updated && w.autoUpdate
	w.mu.Unlock()

	if changed {
		w.notify()
	}
	if shouldRestart && w.restart != nil {
		w.restart()
	}
}

// available reports whether the on-disk binary currently differs from the
// version running in this process. Safe to call on a nil watch (a process
// that isn't loader-managed never starts one) -- always false in that case.
func (w *selfVersionWatch) available() bool {
	if w == nil {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.updateAvailable
}

// pending returns the on-disk version that would replace running on the
// next restart, or "" if no update is available (or none has been observed
// yet). Safe to call on a nil watch, mirroring available().
func (w *selfVersionWatch) pending() string {
	if w == nil {
		return ""
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if !w.updateAvailable {
		return ""
	}
	return w.pendingVersion
}

// setAutoUpdate toggles whether an available update should trigger AC
// restarting itself on its own, without an operator pressing "restart and
// update AC" -- the global topology view's "agent-coordinator" section's own
// **auto-update** checkbox (see condocs/
// initialShellsSessionManagerAndTheConversationalistImpls/
// Step1SubstepCPrompt.md Revision D; previously that checkbox only swept
// every connected host's LR, never AC's own restart). Unlike auto-rebuild,
// there is no debounce here: an update landing on disk means a rebuild
// already happened and settled, so there is nothing further to wait out. If
// an update is already available the moment this turns on, it fires restart
// immediately rather than waiting for the next poll to notice nothing
// changed.
func (w *selfVersionWatch) setAutoUpdate(v bool) {
	w.mu.Lock()
	w.autoUpdate = v
	shouldRestart := v && w.updateAvailable
	w.mu.Unlock()
	w.notify()
	if shouldRestart && w.restart != nil {
		w.restart()
	}
}

// autoUpdateEnabled reports whether auto-update is currently on. Safe to
// call on a nil watch (mirrors available()) -- always false in that case,
// which is also why the toggle itself is a no-op when this process isn't
// loader-managed (see Server.setAutoUpdate).
func (w *selfVersionWatch) autoUpdateEnabled() bool {
	if w == nil {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.autoUpdate
}

// watchLoop polls on selfVersionPollInterval, forever. Run in its own
// goroutine; never returns.
func (w *selfVersionWatch) watchLoop() {
	w.poll()
	ticker := time.NewTicker(selfVersionPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		w.poll()
	}
}

// setAutoUpdate toggles AC's own auto-update flag -- the global topology
// view's "agent-coordinator" section's auto-update checkbox, or the
// WebSocket "ac-set-auto-update" message it sends. A no-op when this process
// isn't loader-managed (selfVersion is nil in that case -- see main.go),
// same guard the restart control itself is disabled on.
func (s *Server) setAutoUpdate(v bool) {
	if s.selfVersion == nil {
		return
	}
	s.selfVersion.setAutoUpdate(v)
}
