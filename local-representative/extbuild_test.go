package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPluginAppIDFromPath(t *testing.T) {
	for in, want := range map[string]string{
		"/work/HyenaExternalUfaPlugin":   "hyena-external-ufa-plugin",
		"/work/HyenaExternalUfaPlugin01": "hyena-external-ufa-plugin01",
		"/work/cats_ufa.plugin":          "cats-ufa-plugin",
		"/work/UFAPlugin":                "ufaplugin",
	} {
		if got := pluginAppIDFromPath(in); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

func TestCondocTagRe(t *testing.T) {
	for _, s := range []string{
		"feat: [SRJ-3456] [condocv2-g6jc] Message blah blah",
		"condocv2: [condocv2-0a1z] Demo commit 2",
		"fix: [ufabuild] rebuild please",
		"feat: [SRJ-3456] [ufabuild-k3x9] Message",
	} {
		if !condocTagRe.MatchString(s) {
			t.Errorf("%q: expected a condoc tag", s)
		}
	}
	// "[condoc-xxxx]" is condocs v1's tag, which no longer asks for a build.
	for _, s := range []string{"condocv2: Demo commit 1", "[condoc-76h4] v1", "[condocv2-ABCD] upper", "[condocv3-abcd] other", "[condocv2-abc] short", "ufabuild untagged", "[ufabuild-] empty", "[ufabuild-ABCD] upper", "[ufabuilds] other"} {
		if condocTagRe.MatchString(s) {
			t.Errorf("%q: expected no condoc tag", s)
		}
	}
}

// pluginDevRepo makes a plug-in dev repo like ../HyenaExternalUfaPlugin: a
// clone (whose deploy-dev-binaries appends to builds.log, and "builds" it to
// $(DEV_PLUGIN_BIN)) tracking a bare upstream, plus a second clone of that
// upstream to push from. Builds deploy to a temporary AI_EVO1_DEV_PLUGIN.
func pluginDevRepo(t *testing.T) (ext, work string) {
	t.Helper()
	t.Setenv("AI_EVO1_DEV_PLUGIN", t.TempDir())
	ext = initTestRepo(t, "echo built >> builds.log && cp builds.log $(DEV_PLUGIN_BIN)")
	branch := ufabuildGit(t, ext, "rev-parse", "--abbrev-ref", "HEAD")
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	ufabuildGit(t, ext, "clone", "-q", "--bare", ext, upstream)
	ufabuildGit(t, ext, "remote", "add", "origin", upstream)
	ufabuildGit(t, ext, "fetch", "-q", "origin")
	ufabuildGit(t, ext, "branch", "-q", "--set-upstream-to=origin/"+branch)
	work = filepath.Join(t.TempDir(), "work")
	ufabuildGit(t, ext, "clone", "-q", upstream, work)
	return ext, work
}

func pushChange(t *testing.T, work, content, subject string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, "file.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ufabuildGit(t, work, "commit", "-q", "-am", subject)
	ufabuildGit(t, work, "push", "-q")
}

// waitExternalBuild waits for the first external path's build to finish.
func waitExternalBuild(t *testing.T, w *repoWatch) ExternalWatchState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		e := w.snapshot().Externals[0]
		if !e.Building {
			return e
		}
		if time.Now().After(deadline) {
			t.Fatalf("build didn't finish: %+v", e)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestPluginDevRepoUfabuild walks a plug-in dev repo through the revised
// .ufabuild sequence: registered (and approved) in one call; an untagged
// commit is pulled but not built; a condoc-tagged one is pulled, writes an
// ignored .ufabuild, builds, removes it and approves the plug-in again; and
// "build" writes a request by hand.
func TestPluginDevRepoUfabuild(t *testing.T) {
	repo := initTestRepo(t, "true")
	ext, work := pluginDevRepo(t)

	var mu sync.Mutex
	var approved []string
	w := newRepoWatch(repo, func() {})
	w.ext.approve = func(appID string) error {
		mu.Lock()
		defer mu.Unlock()
		approved = append(approved, appID)
		return nil
	}
	w.loadExternalWatches(filepath.Join(t.TempDir(), "external-watches.json"))
	if err := w.registerExternalAs(ext, true, "hyena"); err != nil {
		t.Fatalf("registerExternalAs: %v", err)
	}
	mu.Lock()
	if len(approved) != 1 || approved[0] != "hyena" {
		t.Fatalf("expected hyena approved at registration, got %v", approved)
	}
	mu.Unlock()
	if out := ufabuildGit(t, ext, "check-ignore", ufabuildFile); out != ufabuildFile {
		t.Fatalf("expected %s to be ignored, got %q", ufabuildFile, out)
	}
	w.pollExternal()

	pushChange(t, work, "v2\n", "an ordinary commit")
	w.pollExternal()
	e := w.snapshot().Externals[0]
	if e.Head != shortSHA(ufabuildGit(t, work, "rev-parse", "HEAD")) {
		t.Fatalf("expected the upstream to be pulled: %+v", e)
	}
	if e.Building || e.Ufabuild != "" || buildCount(t, ext) != 0 {
		t.Fatalf("an untagged commit shouldn't build: %+v", e)
	}

	pushChange(t, work, "v3\n", "feat: [condocv2-g6jc] agent work")
	w.pollExternal()
	e = waitExternalBuild(t, w)
	want := ufabuildGit(t, work, "rev-parse", "HEAD")
	if buildCount(t, ext) != 1 || e.BuiltHead != shortSHA(want) || e.BuildError != "" {
		t.Fatalf("expected one build of %s: %+v (builds %d)", shortSHA(want), e, buildCount(t, ext))
	}
	if _, err := os.Stat(filepath.Join(ext, ufabuildFile)); !os.IsNotExist(err) {
		t.Fatalf("expected the request to be removed after its build: %v", err)
	}
	if dirty := ufabuildGit(t, ext, "status", "--porcelain", "--", ufabuildFile); dirty != "" {
		t.Fatalf("expected no trace of %s in git status, got %q", ufabuildFile, dirty)
	}
	mu.Lock()
	if len(approved) != 2 {
		t.Fatalf("expected the plug-in approved again after its build, got %v", approved)
	}
	mu.Unlock()
	if !e.Deployed || e.Bin != devPluginBinary("hyena") {
		t.Fatalf("expected the build deployed to %s: %+v", devPluginBinary("hyena"), e)
	}
	if fi, err := os.Stat(devPluginBinary("hyena")); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("expected an executable deployed at %s: %v", devPluginBinary("hyena"), err)
	}
	if _, err := os.Stat(devPluginStagedBinary("hyena")); !os.IsNotExist(err) {
		t.Fatalf("expected the staged binary moved into place: %v", err)
	}

	w.pollExternal()
	if buildCount(t, ext) != 1 {
		t.Fatalf("expected no rebuild without a new request")
	}

	if err := w.requestExternalBuild(ext); err != nil {
		t.Fatalf("requestExternalBuild: %v", err)
	}
	w.pollExternal()
	waitExternalBuild(t, w)
	if buildCount(t, ext) != 2 {
		t.Fatalf("expected the hand-made request to build, got %d builds", buildCount(t, ext))
	}

	// The registration (with its app id and seen head) survives a restart.
	w2 := newRepoWatch(repo, func() {})
	w2.loadExternalWatches(w.ext.path)
	if got := w2.snapshot().Externals; len(got) != 1 || !got[0].Plugin || got[0].AppID != "hyena" {
		t.Fatalf("expected the plug-in dev repo to be reloaded, got %+v", got)
	}
}

// TestPluginDevRepoRegistration verifies a watched path can be turned into a
// plug-in dev repo, the app id defaults to the directory's name, and a
// plain path can't be built.
func TestPluginDevRepoRegistration(t *testing.T) {
	repo := initTestRepo(t, "true")
	ext := initTestRepo(t, "true")
	w := newRepoWatch(repo, func() {})
	if err := w.registerExternal(ext); err != nil {
		t.Fatal(err)
	}
	if err := w.requestExternalBuild(ext); err == nil || !strings.Contains(err.Error(), "plug-in dev repo") {
		t.Fatalf("expected a plain path's build to be refused, got %v", err)
	}
	if err := w.registerExternalAs(ext, true, "hyena"); err != nil {
		t.Fatalf("turning a watched path into a plug-in dev repo: %v", err)
	}
	e := w.snapshot().Externals[0]
	if !e.Plugin || e.AppID != "hyena" || e.PluginNote == "" {
		t.Fatalf("unexpected entry: %+v", e)
	}
	if err := w.registerExternalAs(ext, true, "hyena"); err == nil {
		t.Fatalf("expected registering it twice to be refused")
	}
	if err := w.registerExternalAs(initTestRepo(t, "true"), true, "Not Valid"); err == nil {
		t.Fatalf("expected an invalid app id to be refused")
	}

	// The app id defaults to the directory's name...
	named := filepath.Join(t.TempDir(), "HyenaExternalUfaPlugin")
	if err := os.Mkdir(named, 0o755); err != nil {
		t.Fatal(err)
	}
	ufabuildGit(t, named, "init", "-q")
	if err := w.registerExternalAs(named, true, ""); err != nil {
		t.Fatalf("registering %s: %v", named, err)
	}
	for _, e := range w.snapshot().Externals {
		if e.Abs == named && e.AppID != "hyena-external-ufa-plugin" {
			t.Fatalf("expected the derived app id, got %+v", e)
		}
	}
	// ...which has to be a valid one: t.TempDir's are numbered.
	if err := w.registerExternalAs(t.TempDir(), true, ""); err == nil || !strings.Contains(err.Error(), "give one") {
		t.Fatalf("expected a numbered directory's derived app id to be refused, got %v", err)
	}
}
