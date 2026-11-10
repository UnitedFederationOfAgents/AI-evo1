package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"representable"
)

// This file is the dev-repo watcher's "external plug-in" paths, per
// user-1/sloppo's condocs/privateRepoOpsImpls/Step6Prompt.md Revision
// C: an operator registers a directory beside the watched repo -- the first
// one being a checkout of the HyenaExternalUfaPlugin repo at
// ../HyenaExternalUfaPlugin -- from the system tab's dev-repo panel, and the
// watcher then reports that directory's git state alongside its own.
//
// A plain registration only watches: it never builds, launches or pulls
// anything for the path (not even a fetch). Registered as a plug-in dev repo
// (Revision D, see extbuild.go) it also pulls the path's upstream, writes a
// gitignored .ufabuild build request when condoc-tagged commits come in,
// builds it, and approves the plug-in's app id on this host; its plug-in is
// then a managed app, launched and restarted by LR (Revision F, see
// extlaunch.go). Registrations
// are kept per watched repo root in a JSON file beside the plug-in
// approvals (--external-watches), so they survive restarts of any kind.

// maxExternalWatches caps how many paths one watched repo may register.
const maxExternalWatches = 16

// ExternalWatchState is one registered external path in "repo-state"
// (RepoStateMsg.Externals).
type ExternalWatchState struct {
	Path         string `json:"path"` // as registered: relative to the watched repo's root, or absolute
	Abs          string `json:"abs"`  // the absolute, cleaned directory it resolves to
	RegisteredAt int64  `json:"registered_at"`

	Exists bool   `json:"exists"`           // the directory is there
	Git    bool   `json:"git"`              // it is the root of its own git repository, with at least one commit
	Branch string `json:"branch,omitempty"` // "" when detached or unknown
	Head   string `json:"head,omitempty"`
	Dirty  bool   `json:"dirty"` // per 'git diff HEAD', like the watched repo itself

	// ChangedAt is when head or dirty last changed between two polls (unix
	// seconds), 0 if neither has moved since LR began watching the path --
	// the "something new to build" signal a later auto-rebuild would use.
	ChangedAt int64  `json:"changed_at,omitempty"`
	Note      string `json:"note,omitempty"` // why the path can't be fully read, if it can't
	CheckedAt int64  `json:"checked_at,omitempty"`

	// The plug-in dev repo side (extbuild.go), for a path registered with
	// Plugin set. AppID is the plug-in's app id, approved on this host at
	// registration and after each successful build (PluginNote says how
	// that went). PullNote says why the last poll didn't pull the
	// upstream, if it didn't. Ufabuild is the pending .ufabuild request
	// (the short commit it names, or "pending"), Building is true while
	// 'make deploy-dev-binaries' runs for it, and BuiltHead/BuildAt/
	// BuildError are the last build's outcome.
	Plugin     bool   `json:"plugin,omitempty"`
	AppID      string `json:"app_id,omitempty"`
	PluginNote string `json:"plugin_note,omitempty"`
	PullNote   string `json:"pull_note,omitempty"`
	Ufabuild   string `json:"ufabuild,omitempty"`
	Building   bool   `json:"building,omitempty"`
	BuiltHead  string `json:"built_head,omitempty"`
	BuildAt    int64  `json:"build_at,omitempty"`
	BuildError string `json:"build_error,omitempty"`

	// The plug-in's lifecycle (Revision F, extlaunch.go): Bin is where its
	// builds are deployed (/AI-evo1-dev/plugin/<app-id>), Deployed whether
	// one is there to launch, and LaunchNote what became of its running
	// instance after the last build. Whether it is running is the system
	// tab's managed list, under the app id.
	Bin        string `json:"bin,omitempty"`
	Deployed   bool   `json:"deployed,omitempty"`
	LaunchNote string `json:"launch_note,omitempty"`

	// fullHead is Head unabbreviated: what the next head move is scanned
	// from for condoc-tagged commits. Saved (as seen_head) for plug-in dev
	// repos, so commits pulled in while LR was down aren't missed.
	fullHead string
}

// externalWatchRecord is one registration as saved on disk.
type externalWatchRecord struct {
	Path         string `json:"path"`
	RegisteredAt int64  `json:"registered_at"`
	Plugin       bool   `json:"plugin,omitempty"`
	AppID        string `json:"app_id,omitempty"`
	SeenHead     string `json:"seen_head,omitempty"`
}

// externalWatchesFile is the on-disk form: registrations per watched repo
// root, so LRs watching different repos on one host don't see each other's.
type externalWatchesFile struct {
	Version int                              `json:"version"`
	Repos   map[string][]externalWatchRecord `json:"repos"`
}

// externalWatches is the repoWatch's set of registered external paths. Its
// zero value is ready (registrations then live in memory only).
type externalWatches struct {
	// pollMu serializes polls, so the watch loop and a registration's
	// immediate first check never read the same paths at once. Never held
	// together with repoWatch.opMu: an external path's git commands don't
	// touch the watched repo.
	pollMu sync.Mutex

	// Guarded by repoWatch.mu.
	entries []*ExternalWatchState // sorted by Path
	path    string                // the --external-watches file, "" for memory only
	err     string                // the most recent refused registration or failed save

	// approve approves a plug-in dev repo's app id on this host (the
	// Server's setPluginApproval, wired up in main.go); nil leaves plug-in
	// approval to the operator. Set before loadExternalWatches.
	approve func(appID string) error

	// built is told a plug-in dev repo's build was deployed, and returns
	// what it did about the plug-in's running instance (the Server's
	// restartDevPlugin, wired up in main.go); nil does nothing.
	built func(appID string) string
}

// defaultExternalWatchesPath is where registrations are kept unless
// --external-watches says otherwise -- beside the plug-in approvals.
func defaultExternalWatchesPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "local-representative", "external-watches.json")
}

// resolveExternalPath turns a path as typed into the absolute directory it
// names, relative paths being taken from root. The watched repo itself and
// anything inside it are refused: those are already watched.
func resolveExternalPath(root, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("no path given")
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)
	cleanRoot := filepath.Clean(root)
	if abs == cleanRoot || strings.HasPrefix(abs, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is inside the watched repo, which is already watched", p)
	}
	return abs, nil
}

func readExternalWatchesFile(path string) (externalWatchesFile, error) {
	f := externalWatchesFile{Version: 1, Repos: map[string][]externalWatchRecord{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &f)
	}
	if f.Repos == nil {
		f.Repos = map[string][]externalWatchRecord{}
	}
	return f, err
}

// loadExternalWatches reads this repo's registrations from path ("" keeps
// them in memory only). A missing file is no registrations; an unreadable
// one is reported in ExternalError and also treated as none.
func (w *repoWatch) loadExternalWatches(path string) {
	w.mu.Lock()
	w.ext.path = path
	w.ext.entries = nil
	w.ext.err = ""
	if path != "" {
		f, err := readExternalWatchesFile(path)
		if err != nil {
			w.ext.err = "reading " + path + ": " + err.Error()
			log.Printf("dev-repo: %s", w.ext.err)
		}
		for _, r := range f.Repos[w.root] {
			abs, err := resolveExternalPath(w.root, r.Path)
			if err != nil || w.externalIndexLocked(abs) >= 0 || len(w.ext.entries) >= maxExternalWatches {
				continue
			}
			e := &ExternalWatchState{Path: r.Path, Abs: abs, RegisteredAt: r.RegisteredAt}
			if r.Plugin && representable.ValidAppID(r.AppID) {
				e.Plugin, e.AppID, e.fullHead = true, r.AppID, r.SeenHead
			}
			w.ext.entries = append(w.ext.entries, e)
		}
		w.sortExternalLocked()
	}
	n := len(w.ext.entries)
	w.mu.Unlock()
	if n > 0 {
		log.Printf("dev-repo: watching %d registered external path(s)", n)
		w.pollExternal()
	}
}

// saveExternalWatchesLocked writes this repo's registrations back, leaving
// other repos' entries in the file as they were. Call with w.mu held.
func (w *repoWatch) saveExternalWatchesLocked() error {
	path := w.ext.path
	if path == "" {
		return nil
	}
	f, err := readExternalWatchesFile(path)
	if err != nil {
		// Rather than drop whatever is unreadable there, refuse to save.
		return fmt.Errorf("reading %s: %v", path, err)
	}
	f.Version = 1
	recs := make([]externalWatchRecord, 0, len(w.ext.entries))
	for _, e := range w.ext.entries {
		r := externalWatchRecord{Path: e.Path, RegisteredAt: e.RegisteredAt}
		if e.Plugin {
			r.Plugin, r.AppID, r.SeenHead = true, e.AppID, e.fullHead
		}
		recs = append(recs, r)
	}
	if len(recs) == 0 {
		delete(f.Repos, w.root)
	} else {
		f.Repos[w.root] = recs
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("writing %s: %v", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %v", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("writing %s: %v", path, err)
	}
	return nil
}

// externalIndexLocked returns abs's index in the registrations, or -1. Call
// with w.mu held.
func (w *repoWatch) externalIndexLocked(abs string) int {
	for i, e := range w.ext.entries {
		if e.Abs == abs {
			return i
		}
	}
	return -1
}

// pluginAppAbsLocked returns the plug-in dev repo whose app id is appID, or
// "". Call with w.mu held.
func (w *repoWatch) pluginAppAbsLocked(appID string) string {
	for _, e := range w.ext.entries {
		if e.Plugin && e.AppID == appID {
			return e.Abs
		}
	}
	return ""
}

func (w *repoWatch) sortExternalLocked() {
	sort.Slice(w.ext.entries, func(i, j int) bool { return w.ext.entries[i].Path < w.ext.entries[j].Path })
}

// externalSnapshotLocked copies the registrations for RepoStateMsg. Call
// with w.mu held (read or write).
func (w *repoWatch) externalSnapshotLocked() []ExternalWatchState {
	if len(w.ext.entries) == 0 {
		return nil
	}
	out := make([]ExternalWatchState, len(w.ext.entries))
	for i, e := range w.ext.entries {
		out[i] = *e
	}
	return out
}

// registerExternal adds p (relative to the watched repo's root, or
// absolute) to the watched external paths and saves the registration. The
// directory has to exist already; it needn't be a git repo yet (that is
// reported, not refused). The watch loop checks it from its next pass on;
// Server.watchExternal also checks it straight away.
func (w *repoWatch) registerExternal(p string) error {
	return w.registerExternalAs(p, false, "")
}

// registerExternalAs is registerExternal, optionally as a plug-in dev repo
// (extbuild.go) whose plug-in has app id appID -- "" derives it from the
// directory's name (pluginAppIDFromPath). Registering a path already watched
// as a plug-in dev repo turns it into one; anything else already registered
// is refused. A plug-in dev repo's app id is approved on this host straight
// away, so the plug-in's hello lands online once its build launches it.
func (w *repoWatch) registerExternalAs(p string, plugin bool, appID string) error {
	p = strings.TrimSpace(p)
	appID = strings.TrimSpace(appID)
	abs, err := resolveExternalPath(w.root, p)
	if err == nil {
		var fi os.FileInfo
		if fi, err = os.Stat(abs); err == nil && !fi.IsDir() {
			err = fmt.Errorf("%s is not a directory", abs)
		} else if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("%s does not exist", abs)
		}
	}
	if err == nil && plugin {
		if appID == "" {
			appID = pluginAppIDFromPath(abs)
		}
		if !representable.ValidAppID(appID) {
			err = fmt.Errorf("plug-in app id %q isn't valid (lowercase letters, digits and dashes, starting with a letter, at most 32); give one", appID)
		} else if _, ok := managedApps[appID]; ok {
			// Its plug-in is launched under its app id (extlaunch.go).
			err = fmt.Errorf("%s is already one of this host's managed apps; give another app id", appID)
		}
	}
	// The head a plug-in dev repo is first scanned from (extbuild.go), so
	// tagged commits its first poll pulls in are built.
	seenHead := ""
	if err == nil && plugin {
		seenHead = readExternal(abs).fullHead
	}
	w.mu.Lock()
	var upgraded *ExternalWatchState
	if err == nil {
		i := w.externalIndexLocked(abs)
		switch {
		case plugin && w.pluginAppAbsLocked(appID) != "":
			err = fmt.Errorf("%s is already the app id of the plug-in dev repo at %s", appID, w.pluginAppAbsLocked(appID))
		case i >= 0 && plugin && !w.ext.entries[i].Plugin:
			upgraded = w.ext.entries[i]
		case i >= 0:
			err = fmt.Errorf("%s is already registered", abs)
		case len(w.ext.entries) >= maxExternalWatches:
			err = fmt.Errorf("at most %d external paths may be registered", maxExternalWatches)
		}
	}
	if err == nil && upgraded != nil {
		upgraded.Plugin, upgraded.AppID = true, appID
		if upgraded.fullHead == "" {
			upgraded.fullHead = seenHead
		}
		if err = w.saveExternalWatchesLocked(); err != nil {
			upgraded.Plugin, upgraded.AppID = false, ""
		} else {
			log.Printf("dev-repo: external path %s is now a plug-in dev repo for %s", upgraded.Path, appID)
		}
	} else if err == nil {
		e := &ExternalWatchState{Path: p, Abs: abs, RegisteredAt: time.Now().Unix()}
		if plugin {
			e.Plugin, e.AppID, e.fullHead = true, appID, seenHead
		}
		w.ext.entries = append(w.ext.entries, e)
		w.sortExternalLocked()
		if err = w.saveExternalWatchesLocked(); err != nil {
			// Not saved means not registered: it would vanish on restart.
			i := w.externalIndexLocked(abs)
			w.ext.entries = append(w.ext.entries[:i], w.ext.entries[i+1:]...)
		} else if plugin {
			log.Printf("dev-repo: registered external path %s (%s) as the plug-in dev repo for %s", p, abs, appID)
		} else {
			log.Printf("dev-repo: registered external path %s (%s) for watching", p, abs)
		}
	}
	w.ext.err = ""
	if err != nil {
		w.ext.err = err.Error()
		log.Printf("dev-repo: registering external path %q: %v", p, err)
	}
	w.mu.Unlock()
	if err == nil && plugin {
		w.preparePluginDevRepo(abs, appID)
	}
	w.notify()
	return err
}

// unregisterExternal stops watching p, given as registered or as the
// absolute path it resolved to, and saves.
func (w *repoWatch) unregisterExternal(p string) error {
	p = strings.TrimSpace(p)
	w.mu.Lock()
	i := -1
	for j, e := range w.ext.entries {
		if e.Path == p || e.Abs == p {
			i = j
			break
		}
	}
	var err error
	if i < 0 {
		err = fmt.Errorf("%s is not registered", p)
	} else {
		w.ext.entries = append(w.ext.entries[:i], w.ext.entries[i+1:]...)
		err = w.saveExternalWatchesLocked()
		if err == nil {
			log.Printf("dev-repo: stopped watching external path %s", p)
		}
	}
	w.ext.err = ""
	if err != nil {
		w.ext.err = err.Error()
		log.Printf("dev-repo: unregistering external path %q: %v", p, err)
	}
	w.mu.Unlock()
	w.notify()
	return err
}

// externalGit runs git in the external path abs.
func externalGit(abs string) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = abs
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
}

// readExternal reads one external path's state from disk and git. It never
// writes anything there.
func readExternal(abs string) ExternalWatchState {
	st := ExternalWatchState{Abs: abs, CheckedAt: time.Now().Unix()}
	fi, err := os.Stat(abs)
	if err != nil {
		st.Note = "missing: " + err.Error()
		return st
	}
	if !fi.IsDir() {
		st.Note = "not a directory"
		return st
	}
	st.Exists = true
	git := externalGit(abs)
	top, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		st.Note = "not a git repository"
		return st
	}
	// Compare with symlinks resolved on both sides, as git reports them.
	want := abs
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		want = r
	}
	if r, err := filepath.EvalSymlinks(top); err == nil {
		top = r
	}
	if filepath.Clean(top) != filepath.Clean(want) {
		st.Note = "not the root of a git repository (inside " + top + ")"
		return st
	}
	full, err := git("rev-parse", "HEAD")
	if err != nil {
		st.Note = "git repository with no commits yet"
		return st
	}
	st.Git = true
	st.Head = shortSHA(full)
	st.fullHead = full
	if b, err := git("rev-parse", "--abbrev-ref", "HEAD"); err == nil && b != "HEAD" {
		st.Branch = b
	}
	if diff, err := git("diff", "HEAD"); err != nil {
		st.Note = "git diff HEAD failed: " + diff
	} else {
		st.Dirty = diff != ""
	}
	return st
}

// pollExternal re-reads every registered external path, notifying if
// anything reported changed. Called from the watch loop (outside opMu) and
// once right after a registration. For a plug-in dev repo it first pulls
// the upstream (unless a build is running there), then scans any head move
// for condoc-tagged commits and starts the build a .ufabuild asks for (see
// extbuild.go).
func (w *repoWatch) pollExternal() {
	w.ext.pollMu.Lock()
	defer w.ext.pollMu.Unlock()

	type target struct {
		abs, appID       string
		plugin, building bool
	}
	w.mu.RLock()
	targets := make([]target, len(w.ext.entries))
	for i, e := range w.ext.entries {
		targets[i] = target{e.Abs, e.AppID, e.Plugin, e.Building}
	}
	w.mu.RUnlock()
	if len(targets) == 0 {
		return
	}
	pulls := map[string]string{}  // abs -> PullNote
	deployed := map[string]bool{} // abs -> whether its plug-in's binary is deployed
	read := make(map[string]ExternalWatchState, len(targets))
	for _, t := range targets {
		if t.plugin && !t.building {
			pulls[t.abs] = pullExternal(t.abs)
		}
		if t.plugin {
			fi, err := os.Stat(devPluginBinary(t.appID))
			deployed[t.abs] = err == nil && !fi.IsDir()
		}
		read[t.abs] = readExternal(t.abs)
	}

	type move struct{ abs, from, to string }
	var moves []move
	changed, save := false, false
	w.mu.Lock()
	for _, e := range w.ext.entries {
		st, ok := read[e.Abs]
		if !ok {
			continue // registered while this poll was reading
		}
		from := e.fullHead
		if applyExternalRead(e, st) {
			changed = true
		}
		if !e.Plugin {
			continue
		}
		if note, ok := pulls[e.Abs]; ok && note != e.PullNote {
			e.PullNote = note
			changed = true
		}
		if bin := devPluginBinary(e.AppID); bin != e.Bin {
			e.Bin = bin
			changed = true
		}
		if dep := deployed[e.Abs]; dep != e.Deployed {
			e.Deployed = dep
			changed = true
		}
		if st.Git && st.fullHead != from {
			save = true
			if from != "" {
				moves = append(moves, move{e.Abs, from, st.fullHead})
			}
		}
	}
	if save {
		if err := w.saveExternalWatchesLocked(); err != nil {
			log.Printf("dev-repo: saving external paths' seen heads: %v", err)
		}
	}
	w.mu.Unlock()

	for _, m := range moves {
		if err := requestTaggedBuild(m.abs, m.from, m.to); err != nil {
			log.Printf("dev-repo: %s: %v", m.abs, err)
		}
	}
	if w.startExternalBuilds() {
		changed = true
	}
	if changed {
		w.notify()
	}
}

// applyExternalRead folds a fresh read into e, stamping ChangedAt when head
// or dirty moved since a previous successful read. Returns whether anything
// worth re-broadcasting changed (CheckedAt alone isn't).
func applyExternalRead(e *ExternalWatchState, st ExternalWatchState) bool {
	seen := e.Git
	moved := seen && st.Git && (e.Head != st.Head || e.Dirty != st.Dirty)
	changed := e.Exists != st.Exists || e.Git != st.Git || e.Branch != st.Branch ||
		e.Head != st.Head || e.Dirty != st.Dirty || e.Note != st.Note
	e.Exists, e.Git, e.Branch, e.Head, e.Dirty, e.Note = st.Exists, st.Git, st.Branch, st.Head, st.Dirty, st.Note
	if st.Git {
		e.fullHead = st.fullHead
	}
	e.CheckedAt = st.CheckedAt
	if moved {
		e.ChangedAt = st.CheckedAt
	}
	return changed
}

// watchExternal handles registering an external path for watching -- the
// system tab's WebSocket "watch-external" or agent-coordinator's
// "__system:watch-external <path>". A no-op (logged) when no repo is being
// watched: paths are relative to, and saved under, the watched repo.
func (s *Server) watchExternal(path string) {
	if s.repoWatch == nil {
		log.Printf("system: external watch of %q requested but no repo is being watched (launch with --dev-repo)", path)
		return
	}
	if s.repoWatch.registerExternal(path) == nil {
		go s.repoWatch.pollExternal()
	}
}

// watchExternalPlugin registers path as a plug-in dev repo (extbuild.go),
// its plug-in's app id being appID ("" derives it from the directory's
// name) -- the system tab's WebSocket "watch-external" with plugin set, or
// agent-coordinator's "__system:watch-external-plugin <path> [app-id]".
func (s *Server) watchExternalPlugin(path, appID string) {
	if s.repoWatch == nil {
		log.Printf("system: plug-in dev repo %q requested but no repo is being watched (launch with --dev-repo)", path)
		return
	}
	if s.repoWatch.registerExternalAs(path, true, appID) == nil {
		go s.repoWatch.pollExternal()
	}
}

// buildExternal asks for a build of a plug-in dev repo's current HEAD by
// writing its .ufabuild -- WebSocket "build-external" or
// "__system:build-external <path>".
func (s *Server) buildExternal(path string) {
	if s.repoWatch == nil {
		log.Printf("system: external build of %q requested but no repo is being watched (launch with --dev-repo)", path)
		return
	}
	if s.repoWatch.requestExternalBuild(path) == nil {
		go s.repoWatch.pollExternal()
	}
}

// unwatchExternal is watchExternal's undo -- WebSocket "unwatch-external"
// or "__system:unwatch-external <path>".
func (s *Server) unwatchExternal(path string) {
	if s.repoWatch == nil {
		log.Printf("system: external unwatch of %q requested but no repo is being watched (launch with --dev-repo)", path)
		return
	}
	s.repoWatch.unregisterExternal(path)
}
