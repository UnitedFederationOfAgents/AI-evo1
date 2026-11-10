package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
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

// autoRebuildDebounce is how long auto-rebuild waits, once a .ufabuild
// request first makes the rebuild button active, before actually building --
// restarted from scratch every time a further change is detected in the
// meantime (a new request, or HEAD moving), per Revision E ("If any further
// changes are detected the timer is bumped back to 90 seconds"). Cuts down
// on rebuild churn while a sequence of condoc commits is landing.
const autoRebuildDebounce = 90 * time.Second

// buildCompletionGrace is how long repoWatch keeps reporting a build as
// still running after it notices 'make deploy-dev-binaries's own
// '.building' lock file (see buildLockPresent) has disappeared, per
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision I --
// "to help eliminate race conditions" against anything that might still be
// settling (a slow/networked filesystem, a binary still being renamed into
// place) right as the recipe's own cleanup step removes the lock.
const buildCompletionGrace = 30 * time.Second

// gitTimeout and gitRemoteTimeout bound every git command the watcher runs
// (runGit), the latter for the ones that talk to the remote (fetch, pull).
// Every poll, scan, build and branch switch runs under opMu, so a single git
// that never returns -- a fetch stalled on the network, or waiting on a
// credential or ssh prompt nobody will answer -- would otherwise freeze the
// watcher for good: no more pulls, no more condoc-tag scans, and a panel
// still showing whatever it last reported.
const (
	gitTimeout       = 30 * time.Second
	gitRemoteTimeout = 2 * time.Minute
)

// RepoStateMsg is the "repo-state" WebSocket payload: local-representative's
// current view of the git repo it is watching for rebuild-worthy changes.
// Sent as a zero value (Watched: false) when this LR wasn't launched with
// --dev-repo.
type RepoStateMsg struct {
	Watched      bool   `json:"watched"`
	Root         string `json:"root,omitempty"`
	Dirty        bool   `json:"dirty"`         // uncommitted staged or unstaged changes relative to HEAD
	RebuildReady bool   `json:"rebuild_ready"` // the rebuild button is active -- a .ufabuild request is waiting (see ufabuild.go), and the repo isn't dirty
	// Building is true while 'make deploy-dev-binaries' is running -- i.e.
	// while its own '.building' lock file sits at the repo root -- and for
	// an additional buildCompletionGrace afterward, per
	// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision I.
	// See buildLockPresent/maybeFinishBuild.
	Building    bool `json:"building"`
	AutoRebuild bool `json:"auto_rebuild"`

	// AutoRebuildPending/AutoRebuildSeconds describe the 90s auto-rebuild
	// debounce timer (see autoRebuildDebounce): pending is true from the
	// moment a .ufabuild request makes the rebuild button active with
	// auto-rebuild on until the debounced build actually starts, and seconds
	// is how much of that 90s window is left, re-armed whenever a further
	// change lands.
	AutoRebuildPending bool `json:"auto_rebuild_pending,omitempty"`
	AutoRebuildSeconds int  `json:"auto_rebuild_seconds,omitempty"`

	Head      string `json:"head,omitempty"`
	LastError string `json:"last_error,omitempty"` // most recent rebuild failure, if any

	// BuildFailed is true from a failed rebuild until the next successful
	// one -- unlike LastError, which a new rebuild clears as it starts -- so
	// agent-coordinator's host row and topology card can keep a small red
	// dot up meanwhile (Step1SubstepEPrompt.md Revision D).
	BuildFailed bool `json:"build_failed,omitempty"`

	// Branch is the branch the watched repo currently has checked out --
	// the "dev branch" this LR is watching (pollAndMaybePull follows its
	// upstream) -- or "" when HEAD is detached. Branches lists every branch
	// a "switch" (see switchBranch) may pick from: local branches plus
	// remote-tracking ones with their remote prefix stripped, deduplicated
	// and sorted. SwitchError is the most recent refused/failed switch, if
	// any, cleared by the next successful one. Per
	// condocs/slopspaceCondocsImpls/Step1SubstepCPrompt.md Revision B.
	Branch      string   `json:"branch,omitempty"`
	Branches    []string `json:"branches,omitempty"`
	SwitchError string   `json:"switch_error,omitempty"`

	// UfabuildRequest is the pending .ufabuild request's short commit
	// ("pending" for one naming none), "" when there's no request; see
	// ufabuild.go. UfabuildNote is what's notable about it: why there isn't
	// one, or that its last build failed. UfabuildFailed is that last case on
	// its own: the waiting request is the one whose build failed, so it stays
	// ready for the rebuild button but auto-rebuild won't retry it, and
	// agent-coordinator's timeline doesn't draw it as pending. Per
	// user-1/sloppo's condocs/switchToUfabuildImpls/Step1.md.
	UfabuildRequest string `json:"ufabuild_request,omitempty"`
	UfabuildNote    string `json:"ufabuild_note,omitempty"`
	UfabuildFailed  bool   `json:"ufabuild_failed,omitempty"`

	// The per-sub-app build (ufaplan.go; Step1.md Prompt 4). BuildPlan is
	// what the pending request would rebuild: the sub-apps (Makefile
	// SUBPROJECTS names) whose inputs changed since each was last built, in
	// SUBPROJECTS order -- empty with no request. BuildPlanNote is set when
	// the plan isn't simply that list: nothing changed (the request is
	// consumed without building), or the plan couldn't be worked out (a full
	// 'make deploy-dev-binaries' runs instead).
	BuildPlan     []string `json:"build_plan,omitempty"`
	BuildPlanNote string   `json:"build_plan_note,omitempty"`

	// BuildApps is the sub-apps the running build covers while Building,
	// and the most recent build's afterwards (its failure, if BuildFailed);
	// BuildFull marks a full 'make deploy-dev-binaries' -- every sub-app --
	// instead. BuildManaged is the names those sub-apps show as on the system
	// tab and the timeline: the managed apps they run as ("convo" for
	// the-conversationalist), or the sub-app's own name when no managed app
	// runs from it (local-representative, dtt, ...). All empty for a build LR
	// didn't start (one run by hand).
	BuildApps    []string `json:"build_apps,omitempty"`
	BuildFull    bool     `json:"build_full,omitempty"`
	BuildManaged []string `json:"build_managed,omitempty"`

	// Restarted is what the most recent successful build restarted: managed
	// instance ids ("convo#1"), or "local-representative" when LR itself was
	// rebuilt -- its restart relaunches every managed app. RestartNote says
	// what happened, including why nothing was (no rebuilt sub-app was
	// running) or what failed. Both carry across LR's own restart.
	Restarted   []string `json:"restarted,omitempty"`
	RestartNote string   `json:"restart_note,omitempty"`

	// Externals are the directories beside the watched repo registered for
	// watching (see extwatch.go) -- watched only, unless registered as a
	// plug-in dev repo, which is also pulled and built from .ufabuild
	// requests (extbuild.go). ExternalError is the most recent refused/failed
	// (un)registration or build request, if any, cleared by the next
	// successful one. Per user-1/sloppo's
	// condocs/privateRepoOpsImpls/Step6Prompt.md Revisions C and D.
	Externals     []ExternalWatchState `json:"externals,omitempty"`
	ExternalError string               `json:"external_error,omitempty"`
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
	lastErr     string
	buildFailed bool     // see RepoStateMsg.BuildFailed
	branch      string   // see RepoStateMsg.Branch
	branches    []string // see RepoStateMsg.Branches
	switchErr   string   // see RepoStateMsg.SwitchError

	// The .ufabuild request (ufabuild.go). ufabuildContent/ufabuildLabel are
	// the request file's content and short commit as last read ("" when
	// there's none); ufabuildTracked is a .ufabuild git tracks, which isn't
	// a request. ufabuildFailed is the content of a request whose build
	// failed, which auto-rebuild won't retry on its own. ufabuildScanned is
	// the full HEAD the last condoc-tag scan reached, and ufabuildScanErr why
	// that scan failed, if it did. ufabuildCaughtUp is the HEAD the last
	// catch-up against the build record ran for (catchUpUfabuild). pullErr is
	// why the last poll couldn't fetch or pull its upstream, if it couldn't.
	ufabuildContent  string
	ufabuildLabel    string
	ufabuildTracked  bool
	ufabuildFailed   string
	ufabuildScanned  string
	ufabuildScanErr  string
	ufabuildCaughtUp string
	pullErr          string

	// The per-sub-app build (ufaplan.go), see the RepoStateMsg fields of the
	// same names. binDir is the dev bin dir holding the build record
	// (devBinDir; tests point it elsewhere). planKey is the HEAD, request and
	// record the preview was last planned against.
	binDir        string
	planKey       string
	buildPlan     []string
	buildPlanNote string
	buildApps     []string
	buildFull     bool
	buildManaged  []string
	restarted     []string
	restartNote   string

	// managedFor maps sub-apps to the managed apps they run as, and
	// restartBuilt restarts what a successful build of subApps (every
	// sub-app, when full) needs restarted, returning what it restarted, a
	// note, and a step (LR's own restart) to run once rebuild is otherwise
	// done -- Server.managedAppsFor/restartAfterCoreBuild, wired up in
	// main.go; nil (no mapping, no restarts) in tests that don't set them.
	managedFor   func(subApps []string) []string
	restartBuilt func(subApps []string, full bool) (restarted []string, note string, after func())

	// autoRebuildDeadline/autoRebuildArmedKey track the 90s debounce timer
	// (autoRebuildDebounce): zero deadline means no auto-rebuild is pending.
	// armedKey is the HEAD and request the deadline was last (re)armed
	// against, so a further change (a new request, or HEAD moving again while
	// still pending) is detected by comparing it to the current ones and
	// bumps the deadline back out.
	autoRebuildDeadline time.Time
	autoRebuildArmedKey string

	// buildDoneDeadline tracks the buildCompletionGrace countdown
	// (Revision I): zero means either no build is running, or one is but its
	// '.building' lock file is still present. Armed the moment
	// maybeFinishBuild first notices the lock file has disappeared; building
	// only actually clears once this deadline passes.
	buildDoneDeadline time.Time

	// ext is the registered external paths (extwatch.go); its entries, path
	// and err are guarded by mu.
	ext externalWatches
}

func newRepoWatch(root string, notify func()) *repoWatch {
	w := &repoWatch{root: root, notify: notify, binDir: devBinDir()}
	if head, err := w.headSHA(); err == nil {
		w.head = head
	}
	// Seed the condoc-tag scan with the HEAD we're starting at, so commits
	// already there when LR starts watching don't ask for a build (see
	// condocs/initialDistributedDevelopmentImpls/Step3Prompt.md Revision A);
	// a restart carries the prior instance's scan point instead (see
	// setUfabuildScanned). A .ufabuild request already sitting there -- one
	// written before a restart -- does count, and so does a tagged commit
	// the build record shows isn't built yet (catchUpUfabuild).
	w.ufabuildScanned, _ = w.runGit("rev-parse", "HEAD")
	w.branch, _ = w.currentBranch()
	w.branches, _ = w.listBranches()
	w.refreshUfabuild()
	// Seed building from the '.building' lock file itself (Revision I), so a
	// build already running when LR (re)starts -- e.g. one that outlived an
	// auto-update restart, or one kicked off by hand -- is reflected right
	// away instead of only once its own goroutine happens to notice.
	w.building = w.buildLockPresent()
	return w
}

// rebuildReadyLocked reports whether the rebuild button should be active: a
// .ufabuild request is waiting (see ufabuild.go) and the repo isn't dirty.
// HEAD moving alone never makes it ready. A dirty repo shows "dirty" but is
// deliberately not selectable -- rebuilding would silently bake in
// uncommitted, unreviewed changes; commit or revert first. Callers must hold
// mu (read or write).
func (w *repoWatch) rebuildReadyLocked() bool {
	return !w.dirty && w.ufabuildLabel != ""
}

// buildLockPresent reports whether 'make deploy-dev-binaries's own
// '.building' lock file currently sits at the watched repo's root -- see
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision I. The
// Makefile recipe itself creates and removes this file; LR only ever checks
// for its presence.
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
		BuildFailed:  w.buildFailed,
		Branch:       w.branch,
		Branches:     slices.Clone(w.branches),
		SwitchError:  w.switchErr,

		UfabuildRequest: w.ufabuildLabel,
		UfabuildNote:    w.ufabuildNoteLocked(),
		UfabuildFailed:  w.ufabuildLabel != "" && w.ufabuildContent == w.ufabuildFailed,

		BuildPlan:     slices.Clone(w.buildPlan),
		BuildPlanNote: w.buildPlanNote,
		BuildApps:     slices.Clone(w.buildApps),
		BuildFull:     w.buildFull,
		BuildManaged:  slices.Clone(w.buildManaged),
		Restarted:     slices.Clone(w.restarted),
		RestartNote:   w.restartNote,

		Externals:     w.externalSnapshotLocked(),
		ExternalError: w.ext.err,
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

// runGit runs git in the watched repo, never prompting and never for longer
// than gitTimeout (gitRemoteTimeout for fetch and pull) -- see gitTimeout.
func (w *repoWatch) runGit(args ...string) (string, error) {
	timeout := gitTimeout
	if len(args) > 0 && (args[0] == "fetch" || args[0] == "pull") {
		timeout = gitRemoteTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = w.root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	// Killing git doesn't kill an ssh it started, which still holds the
	// output pipe; don't wait on it for long.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("git %s timed out after %s", strings.Join(args, " "), timeout)
	}
	return strings.TrimSpace(string(out)), err
}

func (w *repoWatch) headSHA() (string, error) {
	return w.runGit("rev-parse", "--short", "HEAD")
}

// currentBranch returns the checked-out branch's short name, or "" when HEAD
// is detached.
func (w *repoWatch) currentBranch() (string, error) {
	b, err := w.runGit("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if b == "HEAD" {
		return "", nil
	}
	return b, nil
}

// listBranches returns every branch switchBranch may pick from -- see
// RepoStateMsg.Branches.
func (w *repoWatch) listBranches() ([]string, error) {
	out, err := w.runGit("for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	return parseBranchRefs(out), nil
}

// parseBranchRefs turns 'git for-each-ref --format=%(refname)' output into
// the deduplicated, sorted list listBranches reports: refs/heads/X is X,
// refs/remotes/<remote>/X is also X (git switch's default --guess creates
// the local tracking branch on demand), and each remote's symbolic HEAD is
// skipped.
func parseBranchRefs(out string) []string {
	seen := map[string]bool{}
	var branches []string
	for _, ref := range strings.Split(out, "\n") {
		ref = strings.TrimSpace(ref)
		var name string
		if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			name = b
		} else if r, ok := strings.CutPrefix(ref, "refs/remotes/"); ok {
			if _, b, found := strings.Cut(r, "/"); found && b != "HEAD" {
				name = b
			}
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		branches = append(branches, name)
	}
	sort.Strings(branches)
	return branches
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

// rebaseInProgress reports whether a rebase is stopped partway in the repo
// (git keeps its state in rebase-merge/ or rebase-apply/ until it finishes
// or is aborted). HEAD is detached for as long as it is.
func (w *repoWatch) rebaseInProgress() bool {
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		p, err := w.runGit("rev-parse", "--git-path", dir)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(w.root, p)
		}
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
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

	// Why the upstream couldn't be fetched or pulled this time, for the
	// panel (ufabuildNoteLocked); unchanged while dirty or mid-rebase, when
	// no pull is tried.
	pullErr := w.pullErrSnapshot()
	if !dirty && w.rebaseInProgress() {
		// Someone else's rebase is stopped here; leave it for them.
		log.Printf("dev-repo: a rebase is in progress, not pulling")
	} else if !dirty {
		// Only a clean repo checks for and pulls in remote changes --
		// rebasing over uncommitted work is exactly the conflict this avoids.
		pullErr = ""
		// A repo with no upstream has nothing to pull: a fetch failing there
		// isn't worth noting.
		_, upstreamErr := w.runGit("rev-parse", "--abbrev-ref", "@{u}")
		if out, err := w.runGit("fetch"); err != nil {
			log.Printf("dev-repo: git fetch failed: %v (%s)", err, out)
			if upstreamErr == nil {
				pullErr = fmt.Sprintf("git fetch failed: %v (%s)", err, out)
			}
		} else if behind, err := w.runGit("rev-list", "--count", "HEAD..@{u}"); err == nil {
			n, convErr := strconv.Atoi(behind)
			if convErr != nil || n == 0 {
				// Nothing to pull.
			} else {
				log.Printf("dev-repo: upstream is %d commit(s) ahead of HEAD, pulling --rebase", n)
				if out, err := w.runGit("pull", "--rebase"); err != nil {
					log.Printf("dev-repo: git pull --rebase failed: %v (%s)", err, out)
					pullErr = fmt.Sprintf("git pull --rebase failed: %v (%s)", err, out)
					// A conflicting rebase stops with HEAD detached partway
					// through; put the branch back where it was rather than
					// leaving the dev repo stranded off its branch.
					if w.rebaseInProgress() {
						if out, err := w.runGit("rebase", "--abort"); err != nil {
							log.Printf("dev-repo: git rebase --abort failed: %v (%s)", err, out)
						} else {
							log.Printf("dev-repo: aborted the failed rebase; branch left as it was")
						}
					}
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
	// Branch/branch-list failures are logged but don't abort the poll: they
	// only feed the "dev branch" readout, not rebuild readiness.
	branch, err := w.currentBranch()
	if err != nil {
		log.Printf("dev-repo: git rev-parse --abbrev-ref HEAD failed: %v", err)
	}
	branches, err := w.listBranches()
	if err != nil {
		log.Printf("dev-repo: git for-each-ref failed: %v", err)
	}

	// Whatever moved HEAD -- the pull above, a branch switch, a commit by
	// hand -- a condoc-tagged commit among those it moved over writes a
	// .ufabuild request (ufabuild.go).
	w.scanForUfabuild()

	w.mu.Lock()
	oldDirty, oldHead, oldBranch, oldBranches := w.dirty, w.head, w.branch, w.branches
	oldReady, oldNote := w.rebuildReadyLocked(), w.ufabuildNoteLocked()
	w.pullErr = pullErr
	w.dirty = dirty
	w.head = head
	w.branch = branch
	if branches != nil {
		w.branches = branches
	}
	changed := oldDirty != dirty || oldHead != head || oldBranch != w.branch ||
		!slices.Equal(oldBranches, w.branches) || oldNote != w.ufabuildNoteLocked()
	w.mu.Unlock()
	// The request file is re-read every poll, not only when HEAD moves: the
	// scan above, scripts/ufabuild.sh or a hand-made one may have written it.
	if w.refreshUfabuild() {
		changed = true
	}
	// ...and what it would rebuild re-planned whenever HEAD, the request or
	// the build record moves (ufaplan.go).
	if w.refreshBuildPlan() {
		changed = true
	}
	w.mu.RLock()
	if oldReady != w.rebuildReadyLocked() {
		changed = true
	}
	w.mu.RUnlock()
	return changed
}

func newMakeCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("make", args...)
	cmd.Dir = dir
	return cmd
}

// rebuild builds the sub-apps the pending request's commits changed (see
// ufaplan.go) with 'make update-dev-binaries APPS="..."' at the repo root,
// then has restartBuilt restart just those -- or, when LR can't work out
// which changed, runs a full 'make deploy-dev-binaries' and restarts LR
// (and with it everything). A plan with nothing in it consumes the request
// without building. Callers must hold opMu. Note that w.building isn't
// cleared here even once the command returns -- the recipe's own
// '.building' lock file (see buildLockPresent) is the source of truth for
// that, via maybeFinishBuild, plus the buildCompletionGrace settling period
// that follows it (Revision I).
func (w *repoWatch) rebuild() {
	w.mu.RLock()
	// The .ufabuild request this build covers -- the watcher only writes
	// one under opMu, which the build holds, so it's the one current now.
	req := w.ufabuildContent
	w.mu.RUnlock()

	apps, planErr := w.planBuild()
	full := planErr != nil || (req == "" && len(apps) == 0)
	if !full && len(apps) == 0 {
		log.Printf("dev-repo: the %s changes no sub-app since its last build: consumed without building", w.describeUfabuild())
		w.consumeUfabuild(req)
		w.mu.Lock()
		w.lastErr = ""
		w.buildFailed = false
		w.ufabuildFailed = ""
		w.buildApps, w.buildFull, w.buildManaged = nil, false, nil
		w.restarted, w.restartNote = nil, "the last request changed no sub-app: nothing was built or restarted"
		w.mu.Unlock()
		w.refreshUfabuild()
		w.refreshBuildPlan()
		w.notify()
		return
	}
	var managed []string
	if w.managedFor != nil && !full {
		managed = w.managedFor(apps)
	}

	w.mu.Lock()
	w.building = true
	w.lastErr = ""
	w.buildDoneDeadline = time.Time{} // a fresh build supersedes any prior grace countdown
	w.buildApps, w.buildFull, w.buildManaged = apps, full, managed
	w.restarted, w.restartNote = nil, ""
	w.mu.Unlock()
	w.notify()

	var cmd *exec.Cmd
	if full {
		if planErr != nil {
			log.Printf("dev-repo: can't tell which sub-apps changed (%v): running 'make deploy-dev-binaries' in %s", planErr, w.root)
		} else {
			log.Printf("dev-repo: manual rebuild: running 'make deploy-dev-binaries' in %s", w.root)
		}
		cmd = newMakeCmd(w.root, "deploy-dev-binaries")
	} else {
		log.Printf("dev-repo: running 'make update-dev-binaries APPS=\"%s\"' in %s", strings.Join(apps, " "), w.root)
		cmd = newMakeCmd(w.root, "update-dev-binaries", "APPS="+strings.Join(apps, " "), "DEV_BIN_DIR="+w.binDir)
	}
	out, buildErr := cmd.CombinedOutput()

	// Re-derive dirty/head from scratch rather than trusting the last poll:
	// the build may have taken long enough for the repo to move again.
	dirty, dirtyErr := w.isDirty()
	head, headErr := w.headSHA()

	if buildErr == nil {
		w.consumeUfabuild(req)
	}

	w.mu.Lock()
	if buildErr != nil {
		w.lastErr = fmt.Sprintf("%v: %s", buildErr, strings.TrimSpace(string(out)))
		w.buildFailed = true
		// Left in place for the operator to retry with the rebuild button;
		// auto-rebuild won't on its own (see maybeAutoRebuild).
		w.ufabuildFailed = req
		log.Printf("dev-repo: rebuild failed: %s", w.lastErr)
	} else {
		log.Printf("dev-repo: rebuild succeeded")
		w.buildFailed = false
		w.ufabuildFailed = ""
	}
	if dirtyErr == nil {
		w.dirty = dirty
	}
	if headErr == nil {
		w.head = head
	}
	w.mu.Unlock()

	// Only now that the new binaries are swapped in: restart what was
	// rebuilt. An LR restart is deferred (after) to the very end, so the
	// state it carries over already says what this build did.
	var after func()
	if buildErr == nil && w.restartBuilt != nil {
		restarted, note, then := w.restartBuilt(apps, full)
		log.Printf("dev-repo: after the build: %s", note)
		after = then
		w.mu.Lock()
		w.restarted, w.restartNote = restarted, note
		w.mu.Unlock()
	}

	w.refreshUfabuild()
	w.refreshBuildPlan()
	// Check right away rather than waiting for the next poll tick -- the
	// recipe's own 'rm -f .building' has almost always already run by the
	// time cmd.CombinedOutput() above returns, so this typically arms the
	// completion grace immediately.
	w.maybeFinishBuild()
	w.notify()
	if after != nil {
		after()
	}
}

// consumeUfabuild removes the request a build covered -- unless it was
// rewritten meanwhile (by hand: the watcher can't while the build holds
// opMu), in which case that one is still pending.
func (w *repoWatch) consumeUfabuild(req string) {
	reqPath := filepath.Join(w.root, ufabuildFile)
	if cur, err := os.ReadFile(reqPath); err == nil && string(cur) == req {
		if err := os.Remove(reqPath); err != nil {
			log.Printf("dev-repo: removing the built %s: %v", ufabuildFile, err)
		}
	}
}

// lastBuildState is what the most recent build did, for lrState to carry
// across LR's own restart (the one a rebuilt LR does) -- see
// setLastBuildState.
func (w *repoWatch) lastBuildState() (apps []string, full bool, managed, restarted []string, note string) {
	if w == nil {
		return nil, false, nil, nil, ""
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return slices.Clone(w.buildApps), w.buildFull, slices.Clone(w.buildManaged), slices.Clone(w.restarted), w.restartNote
}

// setLastBuildState restores what a prior instance's last build did.
func (w *repoWatch) setLastBuildState(apps []string, full bool, managed, restarted []string, note string) {
	w.mu.Lock()
	w.buildApps, w.buildFull, w.buildManaged, w.restarted, w.restartNote = apps, full, managed, restarted, note
	w.mu.Unlock()
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
		if !w.building {
			// A build LR didn't start (by hand): which sub-apps it covers
			// isn't known, so don't report the last one's.
			w.buildApps, w.buildFull, w.buildManaged = nil, false, nil
		}
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
// auto-rebuild still on and a .ufabuild request still waiting. A request
// whose build failed isn't retried until a new one lands (or the operator
// presses rebuild). Callers must hold opMu (same as pollAndMaybePull/rebuild),
// so this never races an operator-driven request. Returns true if the
// pending/countdown state visible in the snapshot changed, so the watch loop
// should notify.
func (w *repoWatch) maybeAutoRebuild() bool {
	w.mu.Lock()
	if !w.autoRebuild || w.building || !w.rebuildReadyLocked() || w.ufabuildContent == w.ufabuildFailed {
		// Nothing to debounce right now -- e.g. auto-rebuild is off, a
		// rebuild is already running, there's no request (or only one that
		// already failed), or the repo is dirty. Disarm any pending timer.
		wasPending := !w.autoRebuildDeadline.IsZero()
		w.autoRebuildDeadline = time.Time{}
		w.autoRebuildArmedKey = ""
		w.mu.Unlock()
		return wasPending
	}

	now := time.Now()
	key := w.head + "\x00" + w.ufabuildContent
	if w.autoRebuildDeadline.IsZero() || key != w.autoRebuildArmedKey {
		// A request either just made the rebuild button active, or a further
		// change landed while we were already counting down (a new request,
		// or HEAD moved again) -- either way, (re)arm the full 90s per the
		// prompt ("the timer is bumped back to 90 seconds").
		w.autoRebuildDeadline = now.Add(autoRebuildDebounce)
		w.autoRebuildArmedKey = key
		w.mu.Unlock()
		return true
	}

	due := !now.Before(w.autoRebuildDeadline)
	if due {
		// Disarm before building, so the snapshot doesn't keep reporting a
		// pending countdown at 0s for the whole build.
		w.autoRebuildDeadline = time.Time{}
		w.autoRebuildArmedKey = ""
	}
	w.mu.Unlock()
	if due {
		log.Printf("dev-repo: auto-rebuild: building for the %s", w.describeUfabuild())
		w.rebuild() // rebuild() notifies on its own
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
		// External paths don't touch the watched repo, so they're read
		// outside opMu (see extwatch.go).
		w.pollExternal()
	}
}

// requestRebuild handles an operator-driven rebuild request (the system
// tab's "rebuild"/"dirty" button). Runs in its own goroutine since a build
// can take a while; opMu keeps it from overlapping the watch loop's own
// checks or another rebuild. Refuses while a rebuild is already running, or
// while the repo is dirty -- dirty always wins, but a clean repo may always
// be rebuilt manually even without a pending .ufabuild request.
func (w *repoWatch) requestRebuild() {
	go func() {
		w.opMu.Lock()
		defer w.opMu.Unlock()
		w.mu.RLock()
		building := w.building || w.buildLockPresent()
		dirty := w.dirty
		w.mu.RUnlock()
		if building || dirty {
			return
		}
		if d, err := w.isDirty(); err == nil && d {
			w.mu.Lock()
			w.dirty = true
			w.mu.Unlock()
			return
		}
		w.rebuild()
	}()
}

// requestSwitchBranch handles an operator-driven "switch" of the watched
// repo's checked-out branch (the system tab's dev-branch control, or
// agent-coordinator's "__system:switch-branch <name>", per
// condocs/slopspaceCondocsImpls/Step1SubstepCPrompt.md Revision B). Runs in
// its own goroutine under opMu, like requestRebuild, so it never overlaps a
// poll/pull or a build. The switch moves HEAD like a pull does, so the
// condoc-tag scan covers the commits it moved over: a [condocv2-xxxx]
// commit on the new branch the old one lacked writes a .ufabuild request,
// and the rebuild button (and auto-rebuild's debounce, if on) picks it up
// the usual way.
func (w *repoWatch) requestSwitchBranch(name string) {
	go func() {
		w.opMu.Lock()
		defer w.opMu.Unlock()
		err := w.switchBranch(name)
		w.mu.Lock()
		if err != nil {
			w.switchErr = err.Error()
			log.Printf("dev-repo: switch to branch %q failed: %v", name, err)
		} else {
			w.switchErr = ""
		}
		w.mu.Unlock()
		w.pollAndMaybePull()
		w.notify()
	}()
}

// switchBranch does the actual checkout for requestSwitchBranch, refusing
// while the repo is dirty (switching would carry or clobber uncommitted
// work), while a build is running, and for any name not in the reported
// branch list. Callers must hold opMu.
func (w *repoWatch) switchBranch(name string) error {
	if name == "" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid branch name %q", name)
	}
	w.mu.RLock()
	current, building, known := w.branch, w.building, slices.Contains(w.branches, name)
	w.mu.RUnlock()
	if name == current {
		return nil
	}
	if !known {
		return fmt.Errorf("unknown branch %q", name)
	}
	if building || w.buildLockPresent() {
		return fmt.Errorf("a rebuild is running")
	}
	dirty, err := w.isDirty()
	if err != nil {
		return fmt.Errorf("git diff HEAD: %v", err)
	}
	if dirty {
		return fmt.Errorf("uncommitted changes -- commit or revert first")
	}
	log.Printf("dev-repo: switching to branch %q", name)
	if out, err := w.runGit("switch", name); err != nil {
		return fmt.Errorf("git switch %s: %v (%s)", name, err, out)
	}
	return nil
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

// switchBranch handles a dev-branch "switch" request -- the system tab's
// WebSocket "switch-branch" or agent-coordinator's
// "__system:switch-branch <name>". A no-op (logged) when no repo is being
// watched.
func (s *Server) switchBranch(name string) {
	if s.repoWatch == nil {
		log.Printf("system: branch switch to %q requested but no repo is being watched (launch with --dev-repo)", name)
		return
	}
	log.Printf("system: branch switch to %q requested", name)
	s.repoWatch.requestSwitchBranch(name)
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
