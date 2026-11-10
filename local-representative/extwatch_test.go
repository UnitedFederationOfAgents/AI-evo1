package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveExternalPath verifies relative paths are taken from the watched
// repo's root, and that the root itself and anything inside it are refused.
func TestResolveExternalPath(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work", "AI-evo1-spiral")
	got, err := resolveExternalPath(root, " ../HyenaExternalUfaPlugin ")
	if want := filepath.Join(string(filepath.Separator), "work", "HyenaExternalUfaPlugin"); err != nil || got != want {
		t.Fatalf("relative: got %q, %v; want %q", got, err, want)
	}
	abs := filepath.Join(string(filepath.Separator), "elsewhere", "plugin")
	if got, err := resolveExternalPath(root, abs); err != nil || got != abs {
		t.Fatalf("absolute: got %q, %v; want %q", got, err, abs)
	}
	for _, p := range []string{"", ".", "sub/dir", root, "../AI-evo1-spiral/x"} {
		if _, err := resolveExternalPath(root, p); err == nil {
			t.Errorf("%q: expected a refusal", p)
		}
	}
	// A sibling sharing the root's name as a prefix is not inside it.
	if _, err := resolveExternalPath(root, "../AI-evo1-spiral-plugin"); err != nil {
		t.Errorf("sibling with a shared prefix refused: %v", err)
	}
}

// TestRepoWatchExternalRegisterPollPersist walks an external path through
// its life: registered relative to the watched repo, polled (and its change
// noticed), refused a second time, reloaded from the saved file by a new
// watcher, then unregistered.
func TestRepoWatchExternalRegisterPollPersist(t *testing.T) {
	repo := initTestRepo(t, "true")
	ext := initTestRepo(t, "true") // t.TempDir siblings, like ../HyenaExternalUfaPlugin
	rel, err := filepath.Rel(repo, ext)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "external-watches.json")

	w := newRepoWatch(repo, func() {})
	w.loadExternalWatches(file)
	if err := w.registerExternal(rel); err != nil {
		t.Fatalf("registerExternal: %v", err)
	}
	w.pollExternal()
	snap := w.snapshot()
	if len(snap.Externals) != 1 {
		t.Fatalf("expected 1 external, got %+v", snap.Externals)
	}
	e := snap.Externals[0]
	if e.Path != rel || e.Abs != filepath.Clean(ext) || !e.Exists || !e.Git || e.Head == "" || e.Dirty || e.ChangedAt != 0 || e.Note != "" {
		t.Fatalf("unexpected first read: %+v", e)
	}
	if snap.ExternalError != "" {
		t.Fatalf("unexpected ExternalError %q", snap.ExternalError)
	}

	commitChange(t, ext, "v2\n")
	w.pollExternal()
	e2 := w.snapshot().Externals[0]
	if e2.Head == e.Head || e2.ChangedAt == 0 {
		t.Fatalf("expected the new commit to be noticed: before %+v, after %+v", e, e2)
	}

	if err := w.registerExternal(ext); err == nil {
		t.Fatalf("expected registering the same directory (absolute) again to be refused")
	}
	if !strings.Contains(w.snapshot().ExternalError, "already registered") {
		t.Fatalf("expected ExternalError to explain the refusal, got %q", w.snapshot().ExternalError)
	}
	if err := w.registerExternal("../does-not-exist-anywhere"); err == nil {
		t.Fatalf("expected a missing directory to be refused")
	}

	w2 := newRepoWatch(repo, func() {})
	w2.loadExternalWatches(file)
	if got := w2.snapshot().Externals; len(got) != 1 || got[0].Path != rel || !got[0].Git {
		t.Fatalf("expected the registration to be reloaded (and polled), got %+v", got)
	}
	// Another repo's watcher sees none of it.
	other := newRepoWatch(initTestRepo(t, "true"), func() {})
	other.loadExternalWatches(file)
	if got := other.snapshot().Externals; len(got) != 0 {
		t.Fatalf("expected no externals for another repo, got %+v", got)
	}

	if err := w2.unregisterExternal(rel); err != nil {
		t.Fatalf("unregisterExternal: %v", err)
	}
	if err := w2.unregisterExternal(rel); err == nil {
		t.Fatalf("expected unregistering twice to fail")
	}
	w3 := newRepoWatch(repo, func() {})
	w3.loadExternalWatches(file)
	if got := w3.snapshot().Externals; len(got) != 0 {
		t.Fatalf("expected the unregistration to be saved, got %+v", got)
	}
}

// TestReadExternalNotes verifies a directory that isn't (the root of) a git
// repo is reported with a note rather than refused or treated as a repo.
func TestReadExternalNotes(t *testing.T) {
	plain := t.TempDir()
	if st := readExternal(plain); !st.Exists || st.Git || !strings.Contains(st.Note, "not a git repository") {
		t.Fatalf("plain dir: %+v", st)
	}
	repo := initTestRepo(t, "true")
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if st := readExternal(sub); st.Git || !strings.Contains(st.Note, "not the root") {
		t.Fatalf("subdirectory of a repo: %+v", st)
	}
	if st := readExternal(filepath.Join(plain, "missing")); st.Exists || st.Note == "" {
		t.Fatalf("missing dir: %+v", st)
	}
}

// TestServerExternalWatchNoRepoWatched verifies the external-path controls
// are logged no-ops without --dev-repo.
func TestServerExternalWatchNoRepoWatched(t *testing.T) {
	s := newServer("test-lr")
	s.watchExternal("../HyenaExternalUfaPlugin")
	s.unwatchExternal("../HyenaExternalUfaPlugin")
	s.handleSystemCommand("__system:watch-external ../HyenaExternalUfaPlugin")
	s.handleSystemCommand("__system:unwatch-external ../HyenaExternalUfaPlugin")
	s.handleSystemCommand("__system:watch-external")
	s.watchExternalPlugin("../HyenaExternalUfaPlugin", "")
	s.buildExternal("../HyenaExternalUfaPlugin")
	s.handleSystemCommand("__system:watch-external-plugin ../HyenaExternalUfaPlugin hyena")
	s.handleSystemCommand("__system:build-external ../HyenaExternalUfaPlugin")
	s.handleSystemCommand("__system:build-external")
}
