package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
)

// The core repo's per-sub-app build plan, per user-1/sloppo's
// condocs/switchToUfabuildImpls/Step1.md Prompt 4 (see docs/DevMode.md
// "Per-sub-app builds"). Plug-in dev repos still build with 'make
// deploy-dev-binaries' (extbuild.go); the core repo the watcher runs from
// builds only the sub-apps a .ufabuild request's commits actually changed,
// with 'make update-dev-binaries APPS="..."', and then restarts only those
// (repowatch.go's rebuild, Server.restartAfterCoreBuild).
//
// Which sub-apps changed is worked out against the commit each one was last
// built from, which the Makefile records beside the binaries (builtRecordFile)
// on every deploy-dev-binaries and update-dev-binaries. A sub-app is planned
// when:
//
//   - it has no recorded build, or its recorded commit isn't in the repo
//     (both: its binary can't be trusted to match anything); or
//   - a file under one of its inputs differs between that commit and HEAD.
//
// A sub-app's inputs are its own directory plus every local module its
// go.mod replaces in (ufa-version, representable, ufa-loader for
// restartsignal, ...), followed transitively, plus the build-wide inputs in
// globalBuildInputs. Test files and markdown never reach a binary, so they
// don't count. Library sub-projects (no BINARY_NAME in their Makefile) are
// never planned themselves: a change to one plans its dependents instead.

// builtRecordFile is the build record in the dev bin dir: one "<sub-app>
// <commit>" line per sub-app, written by scripts/record-dev-build.sh into the
// staging dir so it is swapped in together with the binaries it describes.
const builtRecordFile = ".ufabuild-built"

// globalBuildInputs are repo-root paths every sub-app's build depends on: the
// version every binary embeds is computed by scripts/compute-version.sh, and
// the root Makefile drives the build. A change to either plans everything.
var globalBuildInputs = []string{"Makefile", "scripts/compute-version.sh", "scripts/record-dev-build.sh"}

// defaultDevBinDir is the dev bin dir the Makefile deploys to unless
// $AI_EVO1_DEV_BIN says otherwise.
const defaultDevBinDir = "/AI-evo1-dev/bin"

// devBinDir is where 'make deploy-dev-binaries'/'update-dev-binaries' deploy
// to and resolveAppBinary looks: $AI_EVO1_DEV_BIN, default defaultDevBinDir.
func devBinDir() string {
	if d := os.Getenv("AI_EVO1_DEV_BIN"); d != "" {
		return d
	}
	return defaultDevBinDir
}

// subApp is one deployable sub-project of the core repo.
type subApp struct {
	name   string
	inputs []string // repo-relative directories ("ufa-version/") and files
}

// loadSubApps lists the core repo's deployable sub-projects, in the
// Makefile's SUBPROJECTS order (make list-subprojects), each with its inputs.
func (w *repoWatch) loadSubApps() ([]subApp, error) {
	out, err := runMakeQuiet(w.root, "list-subprojects")
	if err != nil {
		return nil, fmt.Errorf("make list-subprojects: %v", err)
	}
	var apps []subApp
	for _, name := range strings.Fields(out) {
		if !makefileHasBinary(filepath.Join(w.root, name, "Makefile")) {
			continue // a library: nothing to deploy
		}
		apps = append(apps, subApp{name: name, inputs: moduleInputs(w.root, name)})
	}
	if len(apps) == 0 {
		return nil, fmt.Errorf("make list-subprojects listed no deployable sub-app")
	}
	return apps, nil
}

func runMakeQuiet(dir string, args ...string) (string, error) {
	cmd := newMakeCmd(dir, append([]string{"-s", "--no-print-directory"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// makefileHasBinary reports whether a sub-project's Makefile names a binary
// (BINARY_NAME=...), i.e. whether it deploys anything.
func makefileHasBinary(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "BINARY_NAME") {
			return true
		}
	}
	return false
}

// moduleInputs is dir's own directory plus every local module its go.mod
// replaces in, transitively, as sorted repo-relative directories ending "/".
func moduleInputs(root, dir string) []string {
	seen := map[string]bool{}
	var walk func(d string)
	walk = func(d string) {
		if seen[d] {
			return
		}
		seen[d] = true
		b, err := os.ReadFile(filepath.Join(root, d, "go.mod"))
		if err != nil {
			return
		}
		for _, rel := range parseGoModLocalReplaces(string(b)) {
			dep := path.Clean(path.Join(d, rel))
			if dep == "." || strings.HasPrefix(dep, "..") {
				continue // outside the repo: not something a commit here changes
			}
			walk(dep)
		}
	}
	walk(dir)
	inputs := make([]string, 0, len(seen))
	for d := range seen {
		inputs = append(inputs, d+"/")
	}
	sort.Strings(inputs)
	return inputs
}

// parseGoModLocalReplaces returns the local paths (starting "./" or "../")
// a go.mod's replace directives point at, in single-line and block form.
func parseGoModLocalReplaces(gomod string) []string {
	var paths []string
	inBlock := false
	sc := bufio.NewScanner(strings.NewReader(gomod))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case inBlock && line == ")":
			inBlock = false
			continue
		case line == "replace (":
			inBlock = true
			continue
		case strings.HasPrefix(line, "replace "):
			line = strings.TrimPrefix(line, "replace ")
		case !inBlock:
			continue
		}
		_, target, ok := strings.Cut(line, "=>")
		if !ok {
			continue
		}
		fields := strings.Fields(target)
		if len(fields) == 0 {
			continue
		}
		if p := fields[0]; strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") {
			paths = append(paths, p)
		}
	}
	return paths
}

// readBuiltRecord reads builtRecordFile from binDir: sub-app -> the commit it
// was last built from. A missing or unreadable record is empty: everything
// plans as never built.
func readBuiltRecord(binDir string) map[string]string {
	rec := map[string]string{}
	b, err := os.ReadFile(filepath.Join(binDir, builtRecordFile))
	if err != nil {
		return rec
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			rec[f[0]] = f[1]
		}
	}
	return rec
}

// buildInputChanged reports whether any of files (repo-relative, from git
// diff --name-only) is under one of inputs or is a global build input.
// Test files and markdown never count.
func buildInputChanged(files, inputs []string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || strings.HasSuffix(f, ".md") {
			continue
		}
		if slices.Contains(globalBuildInputs, f) {
			return true
		}
		for _, in := range inputs {
			if f == in || (strings.HasSuffix(in, "/") && strings.HasPrefix(f, in)) {
				return true
			}
		}
	}
	return false
}

// planBuild works out which sub-apps HEAD needs rebuilt -- see the top of
// this file. An error means it couldn't tell (no list-subprojects target, a
// git failure): the caller falls back to a full 'make deploy-dev-binaries'.
// An empty plan means nothing deployable changed. Callers must hold opMu.
func (w *repoWatch) planBuild() ([]string, error) {
	apps, err := w.loadSubApps()
	if err != nil {
		return nil, err
	}
	return w.planSubApps(apps)
}

// planSubApps is planBuild for an already loaded list of sub-apps.
func (w *repoWatch) planSubApps(apps []subApp) ([]string, error) {
	head, err := w.runGit("rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git rev-parse HEAD: %v", err)
	}
	rec := readBuiltRecord(w.binDir)
	diffs := map[string][]string{} // built commit -> files changed since
	var plan []string
	for _, a := range apps {
		built := rec[a.name]
		if built == "" {
			plan = append(plan, a.name) // never built here
			continue
		}
		if built == head {
			continue
		}
		files, ok := diffs[built]
		if !ok {
			if _, err := w.runGit("cat-file", "-e", built+"^{commit}"); err != nil {
				files = nil // the commit it was built from is gone: rebuild
			} else {
				out, err := w.runGit("diff", "--name-only", "--no-renames", built, head)
				if err != nil {
					return nil, fmt.Errorf("git diff %s %s: %v", shortSHA(built), shortSHA(head), err)
				}
				files = strings.Fields(out)
				if files == nil {
					files = []string{}
				}
			}
			diffs[built] = files
		}
		if files == nil || buildInputChanged(files, a.inputs) {
			plan = append(plan, a.name)
		}
	}
	return plan, nil
}

// refreshBuildPlan re-plans the pending request for the panel's preview
// (RepoStateMsg.BuildPlan) when HEAD, the request or the build record
// changed since the last plan; with no request there's no plan. Callers must hold opMu but not mu. Returns
// true if the preview changed.
func (w *repoWatch) refreshBuildPlan() bool {
	w.mu.RLock()
	req, head := w.ufabuildLabel, w.head
	w.mu.RUnlock()
	key := ""
	if req != "" {
		key = head + "\x00" + w.ufabuildContentSnapshot()
		if fi, err := os.Stat(filepath.Join(w.binDir, builtRecordFile)); err == nil {
			key += "\x00" + fi.ModTime().String()
		}
	}
	w.mu.RLock()
	same := key == w.planKey
	w.mu.RUnlock()
	if same {
		return false
	}
	var plan []string
	note := ""
	if key != "" {
		var err error
		plan, err = w.planBuild()
		note = buildPlanNote(plan, err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	changed := !slices.Equal(plan, w.buildPlan) || note != w.buildPlanNote
	w.planKey, w.buildPlan, w.buildPlanNote = key, plan, note
	return changed
}

func (w *repoWatch) ufabuildContentSnapshot() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.ufabuildContent
}

// buildPlanNote says what a plan means when it isn't simply a list of
// sub-apps to build.
func buildPlanNote(plan []string, err error) string {
	switch {
	case err != nil:
		return "can't tell which sub-apps changed (" + err.Error() + "): a full make deploy-dev-binaries will run"
	case len(plan) == 0:
		return "no sub-app changed since its last build: the request will be consumed without building"
	}
	return ""
}

// managedAppsFor maps sub-apps to the names they show as on the system tab
// and the timeline: every managed app launched from one of their binaries
// ("convo" for the-conversationalist), and a sub-app no managed app runs
// from (local-representative itself, dtt, clod, ...) under its own name.
func (s *Server) managedAppsFor(subApps []string) []string {
	var out []string
	for _, sub := range subApps {
		found := false
		for app, spec := range managedApps {
			if spec.binName == sub {
				out = append(out, app)
				found = true
			}
		}
		if !found {
			out = append(out, sub)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// restartAfterCoreBuild restarts what a successful core build of subApps
// (every sub-app, when full) needs restarted -- the watcher's restartBuilt:
//
//   - LR itself rebuilt (or a full build): LR restarts, which terminates
//     every managed app and relaunches it from the new binaries (see
//     announceRestartAndExit), so nothing else is restarted on its own. The
//     restart is returned as after, for rebuild to run last. An LR that
//     isn't loader-managed can't come back up, so it's left to the operator.
//   - Otherwise: each running managed instance launched from a rebuilt
//     binary is restarted (restartManaged), in parallel, and nothing else.
//     A rebuilt sub-app with no running instance -- a CLI like dtt or clod,
//     or an app not launched -- needs nothing: its next run uses the new
//     binary. Neither does an instance run from a --fc-bin style override,
//     which isn't the binary just built.
//
// ufa-loader is never restarted from here (it is what runs LR); a rebuilt
// one is picked up the next time LR is launched from a shell.
func (s *Server) restartAfterCoreBuild(subApps []string, full bool) (restarted []string, note string, after func()) {
	loaderNote := ""
	if full || slices.Contains(subApps, "ufa-loader") {
		loaderNote = "; the rebuilt ufa-loader is picked up the next time LR is launched from a shell"
	}
	if full || slices.Contains(subApps, "local-representative") {
		why := "local-representative was rebuilt"
		if full {
			why = "every sub-app was rebuilt"
		}
		if !s.loaderManaged {
			return nil, why + ", but this LR isn't loader-managed: restart it by hand to pick the build up" + loaderNote, nil
		}
		return []string{"local-representative"},
			why + ": restarting local-representative, which restarts every managed app with it" + loaderNote,
			func() { s.requestRestart("ufabuild") }
	}

	type inst struct{ id, app string }
	var insts []inst
	s.procMu.Lock()
	for id, p := range s.managed {
		spec, ok := managedApps[p.app]
		if !ok || p.state() != "running" || s.binOverrides[p.app] != "" || !slices.Contains(subApps, spec.binName) {
			continue
		}
		insts = append(insts, inst{id, p.app})
	}
	s.procMu.Unlock()
	sort.Slice(insts, func(i, j int) bool { return insts[i].id < insts[j].id })

	errs := make([]error, len(insts))
	var wg sync.WaitGroup
	for i, in := range insts {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			log.Printf("dev-repo: restarting %s after its rebuild", id)
			errs[i] = s.restartManaged(id)
		}(i, in.id)
	}
	wg.Wait()

	running := map[string]bool{}
	var failed []string
	for i, in := range insts {
		running[managedApps[in.app].binName] = true
		if errs[i] != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", in.id, errs[i]))
			continue
		}
		restarted = append(restarted, in.id)
	}
	var idle []string
	for _, a := range subApps {
		if !running[a] {
			idle = append(idle, a)
		}
	}

	var parts []string
	if len(restarted) > 0 {
		parts = append(parts, "restarted "+strings.Join(restarted, ", "))
	}
	if len(failed) > 0 {
		parts = append(parts, "restart failed: "+strings.Join(failed, ", "))
	}
	if len(idle) > 0 {
		parts = append(parts, "not running, nothing to restart: "+strings.Join(idle, ", "))
	}
	return restarted, strings.Join(parts, "; ") + loaderNote, nil
}
