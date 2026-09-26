package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

// autoRebuildDebounce is how long auto-rebuild waits, once the rebuild
// button first becomes active, before actually building -- restarted from
// scratch every time a further change is detected in the meantime, per
// Revision E ("If any further changes are detected the timer is bumped back
// to 90 seconds"). Cuts down on rebuild churn while a sequence of commits is
// landing.
const autoRebuildDebounce = 90 * time.Second

// buildCompletionGrace is how long repoWatch keeps reporting a build as
// still running after it notices 'make deploy-dev-binaries's own
// '.building' lock file (see buildLockPresent) has disappeared, per
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision I --
// "to help eliminate race conditions" against anything that might still be
// settling (a slow/networked filesystem, a binary still being renamed into
// place) right as the recipe's own cleanup step removes the lock.
const buildCompletionGrace = 30 * time.Second

// RepoStateMsg is the "repo-state" WebSocket payload: local-representative's
// current view of the git repo it is watching for rebuild-worthy changes.
// Sent as a zero value (Watched: false) when this LR wasn't launched with
// --dev-repo.
type RepoStateMsg struct {
	Watched      bool   `json:"watched"`
	Root         string `json:"root,omitempty"`
	Dirty        bool   `json:"dirty"`         // uncommitted staged or unstaged changes relative to HEAD
	RebuildReady bool   `json:"rebuild_ready"` // the rebuild button is active -- HEAD moved since the last successful rebuild, and the repo isn't dirty
	// Building is true while 'make deploy-dev-binaries' is running -- i.e.
	// while its own '.building' lock file sits at the repo root -- and for
	// an additional buildCompletionGrace afterward, per
	// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision I.
	// See buildLockPresent/maybeFinishBuild.
	Building    bool `json:"building"`
	AutoRebuild bool `json:"auto_rebuild"`

	// CondocLocked mirrors condocLockPresent: true while condoccer's
	// '.condoc' lock file sits at the repo root (see
	// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md), which
	// forces RebuildReady false regardless of dirty/head below -- a condoc is
	// mid-transition, so rebuilding now would be wasted or land at a bad
	// moment. Reported separately so the UI can explain *why* rebuild is
	// unavailable even when the repo looks otherwise ready.
	CondocLocked bool `json:"condoc_locked,omitempty"`

	// AutoRebuildPending/AutoRebuildSeconds describe the 90s auto-rebuild
	// debounce timer (see autoRebuildDebounce): pending is true from the
	// moment the rebuild button first becomes active with auto-rebuild on
	// until the debounced build actually starts, and seconds is how much of
	// that 90s window is left, re-armed whenever a further change lands.
	AutoRebuildPending bool `json:"auto_rebuild_pending,omitempty"`
	AutoRebuildSeconds int  `json:"auto_rebuild_seconds,omitempty"`

	Head      string `json:"head,omitempty"`
	LastError string `json:"last_error,omitempty"` // most recent rebuild failure, if any
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
	builtHead   string // HEAD as of the last successful rebuild (the watcher's starting HEAD before the first one)
	lastErr     string

	// autoRebuildDeadline/autoRebuildArmedHead track the 90s debounce timer
	// (autoRebuildDebounce): zero deadline means no auto-rebuild is pending.
	// armedHead is the HEAD the deadline was last (re)armed against, so a
	// further change (HEAD moving again while still pending) is detected by
	// comparing it to the current head and bumps the deadline back out.
	autoRebuildDeadline  time.Time
	autoRebuildArmedHead string

	// buildDoneDeadline tracks the buildCompletionGrace countdown
	// (Revision I): zero means either no build is running, or one is but its
	// '.building' lock file is still present. Armed the moment
	// maybeFinishBuild first notices the lock file has disappeared; building
	// only actually clears once this deadline passes.
	buildDoneDeadline time.Time
}

func newRepoWatch(root string, notify func()) *repoWatch {
	w := &repoWatch{root: root, notify: notify}
	// Best-effort: seed builtHead with the HEAD we're starting at, so the
	// rebuild button doesn't light up the moment LR starts watching a repo
	// that hasn't actually changed since it was last built (see
	// condocs/initialDistributedDevelopmentImpls/Step3Prompt.md Revision A).
	// If this fails, head/builtHead both stay "" -- still equal, so the
	// button starts inactive either way; pollAndMaybePull sorts out the real
	// head shortly after.
	if head, err := w.headSHA(); err == nil {
		w.head = head
		w.builtHead = head
	}
	// Seed building from the '.building' lock file itself (Revision I), so a
	// build already running when LR (re)starts -- e.g. one that outlived an
	// auto-update restart, or one kicked off by hand -- is reflected right
	// away instead of only once its own goroutine happens to notice.
	w.building = w.buildLockPresent()
	return w
}

// rebuildReadyLocked reports whether the rebuild button should be active:
// HEAD has moved since the last successful rebuild (or since the watcher
// started, per newRepoWatch) and the repo isn't dirty. A dirty repo shows
// "dirty" but is deliberately not selectable -- rebuilding would silently
// bake in uncommitted, unreviewed changes; commit or revert first. It is
// also never selectable while condoccer's '.condoc' lock file is present
// (see condocLockPresent), regardless of dirty/head -- per
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md, the point of
// that file is to prevent excessive rebuilds while a condoc is
// mid-transition. Callers must hold mu (read or write).
func (w *repoWatch) rebuildReadyLocked() bool {
	if w.condocLockPresent() {
		return false
	}
	return !w.dirty && w.head != w.builtHead
}

// condocLockPresent reports whether condoccer's '.condoc' lock file (see
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md) currently sits
// at the watched repo's root. condoccer owns this file's contents; LR only
// ever checks for its presence.
func (w *repoWatch) condocLockPresent() bool {
	_, err := os.Stat(filepath.Join(w.root, ".condoc"))
	return err == nil
}

// buildLockPresent reports whether 'make deploy-dev-binaries's own
// '.building' lock file currently sits at the watched repo's root -- see
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision I. The
// Makefile recipe itself creates and removes this file (unlike '.condoc',
// which condoccer owns); LR only ever checks for its presence.
func (w *repoWatch) buildLockPresent() bool {
	_, err := os.Stat(filepath.Join(w.root, ".building"))
	return err == nil
}

func (w *repoWatch) snapshot() RepoStateMsg {
	if w == nil {
		return RepoStateMsg{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	msg := RepoStateMsg{
		Watched:      true,
		Root:         w.root,
		Dirty:        w.dirty,
		RebuildReady: w.rebuildReadyLocked(),
		Building:     w.building,
		AutoRebuild:  w.autoRebuild,
		Head:         w.head,
		LastError:    w.lastErr,
		CondocLocked: w.condocLockPresent(),
	}
	if !w.autoRebuildDeadline.IsZero() {
		msg.AutoRebuildPending = true
		if left := time.Until(w.autoRebuildDeadline); left > 0 {
			msg.AutoRebuildSeconds = int(left.Round(time.Second) / time.Second)
		}
	}
	return msg
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
// hold opMu. Note that w.building isn't cleared here even once the command
// returns -- the recipe's own '.building' lock file (see buildLockPresent)
// is the source of truth for that, via maybeFinishBuild, plus the
// buildCompletionGrace settling period that follows it (Revision I).
func (w *repoWatch) rebuild() {
	w.mu.Lock()
	w.building = true
	w.lastErr = ""
	w.buildDoneDeadline = time.Time{} // a fresh build supersedes any prior grace countdown
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
	// Check right away rather than waiting for the next poll tick -- the
	// recipe's own 'rm -f .building' has almost always already run by the
	// time cmd.CombinedOutput() above returns, so this typically arms the
	// completion grace immediately.
	w.maybeFinishBuild()
	w.notify()
}

// maybeFinishBuild clears w.building once 'make deploy-dev-binaries's own
// '.building' lock file (see buildLockPresent) has been gone for a full
// buildCompletionGrace -- per
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision I.
// While the lock file is present, building is left true (or set true, if
// something -- e.g. a manually-run build -- created it independently of
// this watcher) and any pending grace countdown is disarmed, since the
// build is (still, or again) actually running. Callers must hold opMu (same
// as pollAndMaybePull/maybeAutoRebuild/rebuild). Returns true if the
// reported state changed, so the watch loop should notify.
func (w *repoWatch) maybeFinishBuild() bool {
	locked := w.buildLockPresent()

	w.mu.Lock()
	defer w.mu.Unlock()

	if locked {
		wasArmed := !w.buildDoneDeadline.IsZero()
		w.buildDoneDeadline = time.Time{}
		changed := wasArmed || !w.building
		w.building = true
		return changed
	}

	if !w.building {
		return false
	}

	now := time.Now()
	if w.buildDoneDeadline.IsZero() {
		// The lock file just disappeared -- start the settling period rather
		// than clearing building immediately.
		w.buildDoneDeadline = now.Add(buildCompletionGrace)
		return true
	}
	if now.Before(w.buildDoneDeadline) {
		return false // still settling
	}
	w.building = false
	w.buildDoneDeadline = time.Time{}
	return true
}

// maybeAutoRebuild manages the 90s auto-rebuild debounce timer
// (autoRebuildDebounce) and triggers rebuild() once it expires with
// auto-rebuild still on. Callers must hold opMu (same as
// pollAndMaybePull/rebuild), so this never races an operator-driven request.
// Returns true if the pending/countdown state visible in the snapshot
// changed, so the watch loop should notify.
func (w *repoWatch) maybeAutoRebuild() bool {
	w.mu.Lock()
	if !w.autoRebuild || w.building || !w.rebuildReadyLocked() {
		// Nothing to debounce right now -- e.g. auto-rebuild is off, a
		// rebuild is already running, or there's nothing to rebuild (clean
		// repo, unmoved HEAD, or dirty). Disarm any pending timer.
		wasPending := !w.autoRebuildDeadline.IsZero()
		w.autoRebuildDeadline = time.Time{}
		w.autoRebuildArmedHead = ""
		w.mu.Unlock()
		return wasPending
	}

	now := time.Now()
	if w.autoRebuildDeadline.IsZero() || w.head != w.autoRebuildArmedHead {
		// The rebuild button either just became active, or a further change
		// landed while we were already counting down (HEAD moved again) --
		// either way, (re)arm the full 90s per the prompt ("the timer is
		// bumped back to 90 seconds").
		w.autoRebuildDeadline = now.Add(autoRebuildDebounce)
		w.autoRebuildArmedHead = w.head
		w.mu.Unlock()
		return true
	}

	due := !now.Before(w.autoRebuildDeadline)
	w.mu.Unlock()
	if due {
		w.rebuild() // rebuild() notifies on its own; deadline clears next pass
		return false
	}
	return true // still counting down -- the reported seconds-left changed
}

// watchLoop is the single goroutine that polls this repo and, when
// auto-rebuild is on, counts down (and eventually triggers) a rebuild. Run
// in its own goroutine; never returns. Both this and requestRebuild go
// through opMu, so nothing here ever overlaps an operator-driven rebuild or
// another poll pass.
func (w *repoWatch) watchLoop() {
	ticker := time.NewTicker(repoWatchPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		w.opMu.Lock()
		changed := w.pollAndMaybePull()
		if w.maybeFinishBuild() {
			changed = true
		}
		if w.maybeAutoRebuild() {
			changed = true
		}
		if changed {
			w.notify()
		}
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
