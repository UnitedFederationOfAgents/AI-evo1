package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseGoModLocalReplaces(t *testing.T) {
	gomod := `module x

go 1.21

replace ufa-version => ../ufa-version

replace (
	representable => ../representable // the wire protocol
	example.com/remote => example.com/fork v1.2.3
	./sub => ./sub
)

require ufa-version v0.0.0
`
	got := parseGoModLocalReplaces(gomod)
	want := []string{"../ufa-version", "../representable", "./sub"}
	if !slices.Equal(got, want) {
		t.Fatalf("parseGoModLocalReplaces = %v, want %v", got, want)
	}
}

func TestBuildInputChanged(t *testing.T) {
	inputs := []string{"app/", "lib/"}
	for _, c := range []struct {
		files []string
		want  bool
	}{
		{[]string{"app/main.go"}, true},
		{[]string{"lib/frontend/src/App.tsx"}, true},
		{[]string{"other/main.go"}, false},
		{[]string{"app/main_test.go", "app/README.md"}, false}, // never reach a binary
		{[]string{"application/main.go"}, false},               // not under app/
		{[]string{"scripts/compute-version.sh"}, true},         // a global input
		{[]string{"docs/DevMode.md", "condocs/x.md"}, false},
	} {
		if got := buildInputChanged(c.files, inputs); got != c.want {
			t.Errorf("buildInputChanged(%v) = %v, want %v", c.files, got, c.want)
		}
	}
}

// initPlanRepo is a core-repo stand-in: deployable sub-apps "app" (whose
// go.mod replaces in ../lib) and "tool", a library "lib", and a Makefile with
// list-subprojects and an update-dev-binaries that logs its APPS to
// apps.log (untracked, so the repo stays clean). binDir is the dev bin dir
// the watcher reads the build record from.
func initPlanRepo(t *testing.T) (dir, binDir string) {
	t.Helper()
	dir = initTestRepo(t, "echo full >> apps.log")
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Makefile", "deploy-dev-binaries:\n\techo full >> apps.log\n"+
		"update-dev-binaries:\n\techo \"$(APPS)\" >> apps.log\n"+
		"list-subprojects:\n\t@echo lib app tool\n")
	write("app/Makefile", "BINARY_NAME=app\n")
	write("app/go.mod", "module app\n\nreplace lib => ../lib\n")
	write("app/main.go", "package main\n")
	write("tool/Makefile", "BINARY_NAME=tool\n")
	write("tool/main.go", "package main\n")
	write("lib/Makefile", "deploy-dev-binary:\n\t@echo library\n")
	write("lib/lib.go", "package lib\n")
	ufabuildGit(t, dir, "add", ".")
	ufabuildGit(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "sub-apps")
	return dir, t.TempDir()
}

func writeBuiltRecord(t *testing.T, binDir string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(binDir, builtRecordFile), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitFile(t *testing.T, dir, rel, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ufabuildGit(t, dir, "add", rel)
	ufabuildGit(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", msg)
}

func TestPlanBuild(t *testing.T) {
	dir, binDir := initPlanRepo(t)
	rw := newRepoWatch(dir, func() {})
	rw.binDir = binDir

	// No record: everything deployable is planned, the library isn't.
	plan, err := rw.planBuild()
	if err != nil || !slices.Equal(plan, []string{"app", "tool"}) {
		t.Fatalf("no record: plan = %v, %v; want [app tool]", plan, err)
	}

	base := ufabuildGit(t, dir, "rev-parse", "HEAD")
	writeBuiltRecord(t, binDir, "app "+base, "tool "+base, "lib "+base)
	if plan, err = rw.planBuild(); err != nil || len(plan) != 0 {
		t.Fatalf("built at HEAD: plan = %v, %v; want none", plan, err)
	}

	commitFile(t, dir, "tool/main.go", "package main // v2\n", "tool change")
	if plan, _ = rw.planBuild(); !slices.Equal(plan, []string{"tool"}) {
		t.Errorf("tool changed: plan = %v, want [tool]", plan)
	}

	// The library changing plans its dependent, not itself.
	commitFile(t, dir, "lib/lib.go", "package lib // v2\n", "lib change")
	if plan, _ = rw.planBuild(); !slices.Equal(plan, []string{"app", "tool"}) {
		t.Errorf("lib and tool changed: plan = %v, want [app tool]", plan)
	}

	// Docs and tests never plan anything; an unknown built commit plans
	// its sub-app.
	head := ufabuildGit(t, dir, "rev-parse", "HEAD")
	writeBuiltRecord(t, binDir, "app "+head, "tool 0123456789abcdef0123456789abcdef01234567")
	commitFile(t, dir, "app/README.md", "docs\n", "docs")
	if plan, _ = rw.planBuild(); !slices.Equal(plan, []string{"tool"}) {
		t.Errorf("docs only, tool's commit unknown: plan = %v, want [tool]", plan)
	}
}

// TestRepoWatchRebuildPerSubApp checks the core rebuild end to end: a
// request whose commits change one sub-app runs update-dev-binaries for just
// that one and hands just that one to restartBuilt; a request changing no
// sub-app is consumed without building; and a repo the watcher can't plan
// (no list-subprojects) falls back to a full deploy-dev-binaries.
func TestRepoWatchRebuildPerSubApp(t *testing.T) {
	dir, binDir := initPlanRepo(t)
	rw := newRepoWatch(dir, func() {})
	rw.binDir = binDir
	base := ufabuildGit(t, dir, "rev-parse", "HEAD")
	writeBuiltRecord(t, binDir, "app "+base, "tool "+base)

	var restartCalls [][]string
	rw.managedFor = func(sub []string) []string { return append([]string{"managed"}, sub...) }
	rw.restartBuilt = func(sub []string, full bool) ([]string, string, func()) {
		restartCalls = append(restartCalls, slices.Clone(sub))
		return []string{"tool#1"}, "restarted tool#1", nil
	}

	commitFile(t, dir, "tool/main.go", "package main // v2\n", "condocv2: [condocv2-t3st] tool")
	rw.pollAndMaybePull()
	snap := rw.snapshot()
	if !snap.RebuildReady || !slices.Equal(snap.BuildPlan, []string{"tool"}) {
		t.Fatalf("after a tagged tool commit: ready %v plan %v (%s)", snap.RebuildReady, snap.BuildPlan, snap.BuildPlanNote)
	}

	rw.rebuild()
	if b, _ := os.ReadFile(filepath.Join(dir, "apps.log")); string(b) != "tool\n" {
		t.Errorf("apps.log = %q, want update-dev-binaries APPS=tool", b)
	}
	snap = rw.snapshot()
	if !slices.Equal(snap.BuildApps, []string{"tool"}) || snap.BuildFull || !slices.Equal(snap.BuildManaged, []string{"managed", "tool"}) {
		t.Errorf("build apps %v full %v managed %v", snap.BuildApps, snap.BuildFull, snap.BuildManaged)
	}
	if len(restartCalls) != 1 || !slices.Equal(restartCalls[0], []string{"tool"}) {
		t.Errorf("restartBuilt calls = %v, want [[tool]]", restartCalls)
	}
	if !slices.Equal(snap.Restarted, []string{"tool#1"}) || snap.RestartNote != "restarted tool#1" {
		t.Errorf("restarted %v note %q", snap.Restarted, snap.RestartNote)
	}
	if snap.UfabuildRequest != "" {
		t.Errorf("request not consumed: %q", snap.UfabuildRequest)
	}

	// A tagged docs-only commit: consumed, nothing built or restarted. (The
	// record is what a real update-dev-binaries would have written.)
	writeBuiltRecord(t, binDir, "app "+base, "tool "+ufabuildGit(t, dir, "rev-parse", "HEAD"))
	commitFile(t, dir, "tool/README.md", "docs\n", "condocv2: [condocv2-t3st] docs")
	rw.pollAndMaybePull()
	if snap = rw.snapshot(); !snap.RebuildReady || snap.BuildPlanNote == "" {
		t.Fatalf("docs request: ready %v plan %v note %q", snap.RebuildReady, snap.BuildPlan, snap.BuildPlanNote)
	}
	rw.rebuild()
	if b, _ := os.ReadFile(filepath.Join(dir, "apps.log")); string(b) != "tool\n" {
		t.Errorf("apps.log = %q: a docs-only request built something", b)
	}
	if snap = rw.snapshot(); snap.UfabuildRequest != "" || len(restartCalls) != 1 || len(snap.BuildApps) != 0 {
		t.Errorf("docs request: request %q restarts %v build apps %v", snap.UfabuildRequest, restartCalls, snap.BuildApps)
	}

	// No list-subprojects: a full build, and restartBuilt told so.
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("deploy-dev-binaries:\n\techo full >> apps.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ufabuildGit(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-am", "condocv2: [condocv2-t3st] makefile")
	var fullCall bool
	rw.restartBuilt = func(sub []string, full bool) ([]string, string, func()) {
		fullCall = full
		return nil, "", nil
	}
	rw.pollAndMaybePull()
	rw.rebuild()
	if b, _ := os.ReadFile(filepath.Join(dir, "apps.log")); !strings.HasSuffix(string(b), "full\n") || !fullCall || !rw.snapshot().BuildFull {
		t.Errorf("unplannable repo: apps.log %q, restartBuilt full %v", b, fullCall)
	}
}

// TestRepoWatchUfabuildCatchUp checks the catch-up against the build record:
// a tagged commit the incremental scan never saw (already at HEAD when the
// watcher starts, which counts as scanned) still writes a request while the
// sub-app it changed isn't built since; once that sub-app is built, an
// untagged change to another one doesn't ride on it.
func TestRepoWatchUfabuildCatchUp(t *testing.T) {
	dir, binDir := initPlanRepo(t)
	base := ufabuildGit(t, dir, "rev-parse", "HEAD")
	writeBuiltRecord(t, binDir, "app "+base, "tool "+base)
	commitFile(t, dir, "tool/main.go", "package main // v2\n", "condocv2: [condocv2-t3st] tool")
	tagged := ufabuildGit(t, dir, "rev-parse", "HEAD")

	rw := newRepoWatch(dir, func() {})
	rw.binDir = binDir
	rw.pollAndMaybePull()
	snap := rw.snapshot()
	if !snap.RebuildReady || snap.UfabuildRequest != shortSHA(tagged) || !slices.Equal(snap.BuildPlan, []string{"tool"}) {
		t.Fatalf("expected a caught-up request for %s planning [tool], got ready %v request %q plan %v (%s)",
			shortSHA(tagged), snap.RebuildReady, snap.UfabuildRequest, snap.BuildPlan, snap.UfabuildNote)
	}

	// tool built (and its request consumed); then an untagged app change.
	if err := os.Remove(filepath.Join(dir, ufabuildFile)); err != nil {
		t.Fatal(err)
	}
	writeBuiltRecord(t, binDir, "app "+base, "tool "+tagged)
	commitFile(t, dir, "app/main.go", "package main // v2\n", "app change")
	rw.pollAndMaybePull()
	if snap = rw.snapshot(); snap.RebuildReady || snap.UfabuildRequest != "" {
		t.Errorf("an untagged app change shouldn't be caught up on a tagged tool commit, got ready %v request %q",
			snap.RebuildReady, snap.UfabuildRequest)
	}
}

func TestManagedAppsFor(t *testing.T) {
	s := &Server{}
	got := s.managedAppsFor([]string{"the-conversationalist", "dtt", "local-representative"})
	want := []string{"convo", "dtt", "local-representative"}
	if !slices.Equal(got, want) {
		t.Errorf("managedAppsFor = %v, want %v", got, want)
	}
}
