package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeGitHubRemote(t *testing.T) {
	cases := map[string]string{
		"https://github.com/je-sidestuff/sloppo.git":        "je-sidestuff/sloppo",
		"https://github.com/je-sidestuff/sloppo":            "je-sidestuff/sloppo",
		"https://ghp_abc@github.com/je-sidestuff/sloppo.git": "je-sidestuff/sloppo",
		"git@github.com:je-sidestuff/sloppo.git":            "je-sidestuff/sloppo",
		"ssh://git@github.com/je-sidestuff/sloppo.git\n":    "je-sidestuff/sloppo",
		"https://gitlab.com/je-sidestuff/sloppo.git":        "",
		"/some/local/path":                                  "",
	}
	for in, want := range cases {
		if got := normalizeGitHubRemote(in); got != want {
			t.Errorf("normalizeGitHubRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidatePointerConfig(t *testing.T) {
	cfg, err := validatePointerConfig(PointerConfig{Repo: " https://github.com/a/b.git ", Branch: "main", Path: "../b"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo != "a/b" {
		t.Errorf("repo = %q, want a/b", cfg.Repo)
	}
	for _, bad := range []PointerConfig{
		{Repo: "noslash", Branch: "main", Path: "x"},
		{Repo: "a/b", Branch: "", Path: "x"},
		{Repo: "a/b", Branch: "bad..branch", Path: "x"},
		{Repo: "a/b", Branch: "main", Path: " "},
	} {
		if _, err := validatePointerConfig(bad); err == nil {
			t.Errorf("validatePointerConfig(%+v) succeeded, want error", bad)
		}
	}
}

// v1Pointer is a condoccer (v1) pointer file, which condocv2 must ignore.
const v1Pointer = `# condoc pointer, kept by condoccer -- see docs/SlopspaceControlScheme.md
name: SlopspaceCondocs
startTime: 1792092722
controlScheme: slopspace
branch: condoc/SlopspaceCondocs-1792092722/main
condocRepo: je-sidestuff/sloppo
targets:
  - repo: UnitedFederationOfAgents/AI-evo1
holder: condoccer@box
state:
  phase: awaiting_action
  step: 2
`

func TestParsePointerSpecRejectsV1(t *testing.T) {
	if isCondocV2Pointer(v1Pointer) {
		t.Fatal("a v1 pointer reads as v2")
	}
	if _, err := parsePointerSpec(v1Pointer); err == nil {
		t.Fatal("parsePointerSpec accepted a v1 pointer")
	}
	// A v2 version marker with v1's control scheme is still not a v2 condoc.
	if _, err := parsePointerSpec("condocVersion: 2\n" + v1Pointer); err == nil {
		t.Fatal("parsePointerSpec accepted controlScheme slopspace")
	}
}

func TestPointerSpecRoundTrip(t *testing.T) {
	spec := PointerSpec{
		Name: "Foo", StartTime: 7, ControlScheme: condocControlScheme,
		Branch: "condocv2/Foo-7/main", CondocRepo: "a/condocs", CondocPath: "condocs/Foo.md",
		Targets: []string{"a/one", "a/two"}, Agent: "claude", Description: "two\nlines",
		SlopspaceID: "ss1", Holder: "condocv2@box", Bases: []string{"a/one@111", "a/condocs@222"},
		Phase: PhaseLocked, Interactions: 2,
		Lock: &InteractionLock{
			Interaction: 3, Stage: stageMapped, AutoSubmit: true, Prompt: "do it\n## not a heading",
			Role: "condocv2-Foo-7", SignalAt: 99, Bases: []string{"a/one@111"}, Heads: []string{"a/one@333"},
		},
		PRGuidance: defaultPRGuidance, PRRole: "condocv2-Foo-7", PRSignalAt: 100, PRWorkdir: "/ss1/pr-draft-1",
		PRTemplate: "prs:\n  - repo: a/one\n    title: T\n",
		PRs:        []PointerPR{{Repo: "a/one", Number: 4, URL: "https://github.com/a/one/pull/4", State: "open"}},
	}
	got, err := parsePointerSpec(spec.render())
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, spec.render())
	}
	got.UpdatedAt = ""
	if !reflect.DeepEqual(got, spec) {
		t.Errorf("round trip:\n got %+v\nwant %+v\n lock %+v", got, spec, got.Lock)
	}
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func TestPointerStatus(t *testing.T) {
	base := t.TempDir()
	s := newServer()
	s.initPointer(PointerConfig{Repo: "je-sidestuff/sloppo", Branch: "main", Path: "sloppo"}, base)

	if st := s.pointerStatus(); st.State != "missing" || st.ResolvedPath != filepath.Join(base, "sloppo") {
		t.Fatalf("missing: got state %q path %q", st.State, st.ResolvedPath)
	}

	repo := filepath.Join(base, "sloppo")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if st := s.pointerStatus(); st.State != "invalid" {
		t.Fatalf("non-git dir: got state %q", st.State)
	}

	gitT(t, base, "init", "-q", "-b", "main", repo)
	gitT(t, repo, "remote", "add", "origin", "https://github.com/someone/else.git")
	if st := s.pointerStatus(); st.State != "invalid" || !strings.Contains(st.Message, "someone/else") {
		t.Fatalf("wrong remote: got state %q message %q", st.State, st.Message)
	}

	gitT(t, repo, "remote", "set-url", "origin", "git@github.com:je-sidestuff/sloppo.git")
	if err := os.MkdirAll(filepath.Join(repo, pointerDir), 0755); err != nil {
		t.Fatal(err)
	}
	v2 := PointerSpec{Name: "Foo", StartTime: 1, ControlScheme: condocControlScheme, Branch: "condocv2/Foo-1/main",
		CondocRepo: "a/b", Targets: []string{"a/c"}, Phase: PhaseProposed}
	if err := os.WriteFile(filepath.Join(repo, pointerDir, "Foo-1.yaml"), []byte(v2.render()), 0644); err != nil {
		t.Fatal(err)
	}
	// condoccer's pointers share the directory and are filtered out.
	if err := os.WriteFile(filepath.Join(repo, pointerDir, "SlopspaceCondocs-1792092722.yaml"), []byte(v1Pointer), 0644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "init")

	st := s.pointerStatus()
	if st.State != "ready" {
		t.Fatalf("ready: got state %q message %q", st.State, st.Message)
	}
	if len(st.Pointers) != 1 || st.Pointers[0].Name != "Foo" || st.Pointers[0].Phase != PhaseProposed || st.Pointers[0].File != "pointers/Foo-1.yaml" {
		t.Fatalf("pointers = %+v", st.Pointers)
	}

	gitT(t, repo, "checkout", "-q", "-b", "other")
	if st := s.pointerStatus(); st.State != "invalid" || !strings.Contains(st.Message, "other") {
		t.Fatalf("wrong branch: got state %q message %q", st.State, st.Message)
	}
}
