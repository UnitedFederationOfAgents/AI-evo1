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
// is for -- so the system tab's restart control can relabel itself "update
// and restart" instead of a plain "restart" (see docs/DevMode.md "Loader").
//
// Polling "--version" rather than watching the file directly (e.g. mtime or
// a content hash) keeps this consistent with every other version check in
// the codebase (ufaversion.HandleVersionFlag): it's what "the file changed"
// actually means for a UFA binary, not merely that some byte moved.
type selfVersionWatch struct {
	binPath string
	running string // the version compiled into this running process
	notify  func()

	mu              sync.RWMutex
	updateAvailable bool
}

// newSelfVersionWatch resolves this process's own executable path. It
// returns nil (disabling update detection, not the restart control itself)
// if the path can't be resolved.
func newSelfVersionWatch(running string, notify func()) *selfVersionWatch {
	bin, err := os.Executable()
	if err != nil {
		log.Printf("self-version: could not resolve own executable path: %v -- update detection disabled", err)
		return nil
	}
	return &selfVersionWatch{binPath: bin, running: running, notify: notify}
}

// poll re-derives updateAvailable from the on-disk binary's "--version"
// output, notifying (if the verdict changed) so a fresh system-state can be
// broadcast.
func (w *selfVersionWatch) poll() {
	out, err := exec.Command(w.binPath, "--version").Output()
	if err != nil {
		log.Printf("self-version: %s --version failed: %v", w.binPath, err)
		return
	}
	updated := strings.TrimSpace(string(out)) != w.running

	w.mu.Lock()
	changed := w.updateAvailable != updated
	w.updateAvailable = updated
	w.mu.Unlock()

	if changed {
		w.notify()
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
