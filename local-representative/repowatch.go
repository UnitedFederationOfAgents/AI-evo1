package main

import (
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// repoWatchPollInterval is how often the dev-repo watcher (--dev-repo, see
// docs/DevMode.md and
// condocs/initialDistributedDevelopmentImpls/Step3Prompt.md) re-checks the
// watched repo's dirty/HEAD state and, on a clean repo, whether its upstream
// branch has moved.
const repoWatchPollInterval = 5 * time.Second

// RepoStateMsg is the "repo-state" WebSocket payload: local-representative's
// current view of the git repo it is watching for rebuild-worthy changes.
// Sent as a zero value (Watched: false) when this LR wasn't launched with
// --dev-repo.
type RepoStateMsg struct {
	Watched      bool   `json:"watched"`
	Root         string `json:"root,omitempty"`
	Dirty        bool   `json:"dirty"`         // uncommitted staged or unstaged changes relative to HEAD
	RebuildReady bool   `json:"rebuild_ready"` // the rebuild button is active -- dirty, or HEAD moved since the last successful rebuild
	Building     bool   `json:"building"`      // 'make deploy-dev-binaries' is running right now
	AutoRebuild  bool   `json:"auto_rebuild"`
	Head         string `json:"head,omitempty"`
	LastError    string `json:"last_error,omitempty"` // most recent rebuild failure, if any
}

// repoWatch is local-representative's dev-repo watcher for a single git
// repository -- the one --dev-repo was launched from. notify is invoked
// every time the reported state changes, so the Server can broadcast a fresh
// "repo-state"; it must be cheap and safe to call from any goroutine.
type repoWatch struct {
	root   string
	notify func()

	// opMu serializes every git/make invocation this watcher makes: the
	// watch loop's own checks (including a pull) and a rebuild never run
	// concurrently with each other or with themselves -- "the
	// check-and-rebuild process is single-threaded to avoid conflicts", per
	// the prompt. Held for a whole poll-and-maybe-pull pass, and for the
	// entire duration of a rebuild, whichever caller (the watch loop or an
	// operator-driven request) got to it first.
	opMu sync.Mutex

	mu          sync.RWMutex
	dirty       bool
	building    bool
	autoRebuild bool
	head        string
	builtHead   string // HEAD as of the last successful rebuild ("" before the first one)
	lastErr     string
}

func newRepoWatch(root string, notify func()) *repoWatch {
	return &repoWatch{root: root, notify: notify}
}

// rebuildReadyLocked reports whether the rebuild button should be active:
// there is something uncommitted (dirty -- rebuilding is how it'd land in the
// dev binaries), or HEAD has moved since the last successful rebuild.
// Callers must hold mu (read or write).
func (w *repoWatch) rebuildReadyLocked() bool {
	return w.dirty || w.head != w.builtHead
}

func (w *repoWatch) snapshot() RepoStateMsg {
	if w == nil {
		return RepoStateMsg{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return RepoStateMsg{
		Watched:      true,
		Root:         w.root,
		Dirty:        w.dirty,
		RebuildReady: w.rebuildReadyLocked(),
		Building:     w.building,
		AutoRebuild:  w.autoRebuild,
		Head:         w.head,
		LastError:    w.lastErr,
	}
}

func (w *repoWatch) setAutoRebuild(v bool) {
	w.mu.Lock()
	w.autoRebuild = v
	w.mu.Unlock()
	w.notify()
}

func (w *repoWatch) runGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = w.root
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (w *repoWatch) headSHA() (string, error) {
	return w.runGit("rev-parse", "--short", "HEAD")
}

// isDirty reports whether the repo has modified unstaged or staged changes,
// per 'git diff HEAD' -- deliberately excluding untracked files, matching the
// prompt's definition of "dirty".
func (w *repoWatch) isDirty() (bool, error) {
	diff, err := w.runGit("diff", "HEAD")
	if err != nil {
		return false, err
	}
	return diff != "", nil
}

// pollAndMaybePull re-derives dirty/head from the repo, pulling a clean
// repo's upstream forward first if it has moved ahead. Callers must hold
// opMu. Returns true if the reported snapshot changed.
func (w *repoWatch) pollAndMaybePull() bool {
	dirty, err := w.isDirty()
	if err != nil {
		log.Printf("dev-repo: git diff HEAD failed: %v", err)
		return false
	}

	if !dirty {
		// Only a clean repo checks for and pulls in remote changes --
		// rebasing over uncommitted work is exactly the conflict this avoids.
		if _, err := w.runGit("fetch"); err != nil {
			log.Printf("dev-repo: git fetch failed: %v", err)
		} else if ahead, err := w.runGit("rev-list", "--count", "HEAD..@{u}"); err == nil {
			if n, convErr := strconv.Atoi(ahead); convErr == nil && n > 0 {
				log.Printf("dev-repo: upstream is %d commit(s) ahead of HEAD, pulling --rebase", n)
				if out, err := w.runGit("pull", "--rebase"); err != nil {
					log.Printf("dev-repo: git pull --rebase failed: %v (%s)", err, out)
				}
			}
		}
		// A pull above may have just moved HEAD; re-derive before recording.
		dirty, err = w.isDirty()
		if err != nil {
			log.Printf("dev-repo: git diff HEAD failed: %v", err)
			return false
		}
	}

	head, err := w.headSHA()
	if err != nil {
		log.Printf("dev-repo: git rev-parse HEAD failed: %v", err)
		return false
	}

	w.mu.Lock()
	oldDirty, oldHead := w.dirty, w.head
	oldReady := w.rebuildReadyLocked()
	w.dirty = dirty
	w.head = head
	changed := oldDirty != dirty || oldHead != head || oldReady != w.rebuildReadyLocked()
	w.mu.Unlock()
	return changed
}

// rebuild runs 'make deploy-dev-binaries' at the repo root. Callers must
// hold opMu.
func (w *repoWatch) rebuild() {
	w.mu.Lock()
	w.building = true
	w.lastErr = ""
	w.mu.Unlock()
	w.notify()

	log.Printf("dev-repo: running 'make deploy-dev-binaries' in %s", w.root)
	cmd := exec.Command("make", "deploy-dev-binaries")
	cmd.Dir = w.root
	out, buildErr := cmd.CombinedOutput()

	// Re-derive dirty/head from scratch rather than trusting the last poll:
	// the build may have taken long enough for the repo to move again.
	dirty, dirtyErr := w.isDirty()
	head, headErr := w.headSHA()

	w.mu.Lock()
	w.building = false
	if buildErr != nil {
		w.lastErr = fmt.Sprintf("%v: %s", buildErr, strings.TrimSpace(string(out)))
		log.Printf("dev-repo: rebuild failed: %s", w.lastErr)
	} else {
		log.Printf("dev-repo: rebuild succeeded")
		if headErr == nil {
			w.builtHead = head
		}
	}
	if dirtyErr == nil {
		w.dirty = dirty
	}
	if headErr == nil {
		w.head = head
	}
	w.mu.Unlock()
	w.notify()
}

// maybeAutoRebuild triggers rebuild() if the auto-rebuild toggle is on and
// the rebuild button is currently active. Callers must hold opMu (same as
// pollAndMaybePull/rebuild), so this never races an operator-driven request.
func (w *repoWatch) maybeAutoRebuild() {
	w.mu.RLock()
	should := w.autoRebuild && !w.building && w.rebuildReadyLocked()
	w.mu.RUnlock()
	if should {
		w.rebuild()
	}
}

// watchLoop is the single goroutine that polls this repo and, when
// auto-rebuild is on, triggers a rebuild. Run in its own goroutine; never
// returns. Both this and requestRebuild go through opMu, so nothing here
// ever overlaps an operator-driven rebuild or another poll pass.
func (w *repoWatch) watchLoop() {
	ticker := time.NewTicker(repoWatchPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		w.opMu.Lock()
		if w.pollAndMaybePull() {
			w.notify()
		}
		w.maybeAutoRebuild()
		w.opMu.Unlock()
	}
}

// requestRebuild handles an operator-driven rebuild request (the system
// tab's "rebuild"/"dirty" button). Runs in its own goroutine since a build
// can take a while; opMu keeps it from overlapping the watch loop's own
// checks or another rebuild. A no-op if the button wouldn't currently be
// active (nothing to rebuild, or a rebuild is already running) -- this is
// the server-side enforcement of the same guard the dashboard's button
// disables itself on.
func (w *repoWatch) requestRebuild() {
	go func() {
		w.opMu.Lock()
		defer w.opMu.Unlock()
		w.mu.RLock()
		ready := !w.building && w.rebuildReadyLocked()
		w.mu.RUnlock()
		if !ready {
			return
		}
		w.rebuild()
	}()
}

// repoRootFromCWD resolves the git repository root for --dev-repo: LR must be
// launched from inside a git repository (the working directory or one of its
// parents), per the prompt.
func repoRootFromCWD() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// repoState returns the current dev-repo watcher snapshot -- a zero value
// (Watched: false) when this LR wasn't launched with --dev-repo.
func (s *Server) repoState() RepoStateMsg {
	return s.repoWatch.snapshot()
}

// broadcastRepoState pushes the current dev-repo watcher snapshot to every
// connected browser client and to agent-coordinator, if connected. Wired up
// as repoWatch's notify callback.
func (s *Server) broadcastRepoState() {
	rs := s.repoState()
	s.broadcast("repo-state", rs)
	if ac := s.getACClient(); ac != nil {
		ac.SendData("repo-state", rs)
	}
}

// requestRebuild handles an operator-driven rebuild request -- the system
// tab's "rebuild"/"dirty" button (WebSocket "rebuild-app") or
// agent-coordinator's "__system:rebuild". A no-op (logged) when no repo is
// being watched.
func (s *Server) requestRebuild(reason string) {
	if s.repoWatch == nil {
		log.Printf("system: rebuild requested (%s) but no repo is being watched (launch with --dev-repo)", reason)
		return
	}
	log.Printf("system: rebuild requested (%s)", reason)
	s.repoWatch.requestRebuild()
}

// setAutoRebuild toggles the dev-repo watcher's auto-rebuild flag -- the
// system tab's "auto-rebuild" toggle or agent-coordinator's
// "__system:auto-rebuild <on|off>". A no-op when no repo is being watched.
func (s *Server) setAutoRebuild(v bool) {
	if s.repoWatch == nil {
		return
	}
	s.repoWatch.setAutoRebuild(v)
}
