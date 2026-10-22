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

func TestParsePointerFile(t *testing.T) {
	content := `# pointer
name: SlopspaceCondocs
startTime: 1792092722
controlScheme: slopspace
branch: condoc/SlopspaceCondocs-1792092722/main
condocRepo: je-sidestuff/sloppo
targets:
  - repo: UnitedFederationOfAgents/AI-evo1
  - "je-sidestuff/other"
state:
  phase: awaiting_action # comment
  step: 2
  substep: ""
  iteration: B
`
	got := parsePointerFile(content)
	want := PointerEntry{
		Name:       "SlopspaceCondocs",
		Phase:      "awaiting_action",
		Step:       "2",
		Iteration:  "B",
		Branch:     "condoc/SlopspaceCondocs-1792092722/main",
		CondocRepo: "je-sidestuff/sloppo",
		Targets:    []string{"UnitedFederationOfAgents/AI-evo1", "je-sidestuff/other"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePointerFile:\n got %+v\nwant %+v", got, want)
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
	s := newServer(base)
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
	if err := os.WriteFile(filepath.Join(repo, pointerDir, "Foo-1.yaml"), []byte("name: Foo\nstate:\n  phase: proposed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "init")

	st := s.pointerStatus()
	if st.State != "ready" {
		t.Fatalf("ready: got state %q message %q", st.State, st.Message)
	}
	if len(st.Pointers) != 1 || st.Pointers[0].Name != "Foo" || st.Pointers[0].Phase != "proposed" || st.Pointers[0].File != "pointers/Foo-1.yaml" {
		t.Fatalf("pointers = %+v", st.Pointers)
	}

	gitT(t, repo, "checkout", "-q", "-b", "other")
	if st := s.pointerStatus(); st.State != "invalid" || !strings.Contains(st.Message, "other") {
		t.Fatalf("wrong branch: got state %q message %q", st.State, st.Message)
	}
}

// TestReadPointersSkipsCondocV2 verifies condocv2's pointers, which share
// pointers/ with condoccer's, are filtered out of condoccer's listing.
func TestReadPointersSkipsCondocV2(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, pointerDir), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"V1-1.yaml": "name: V1\ncontrolScheme: slopspace\nstate:\n  phase: proposed\n",
		"V2-2.yaml": "# condoc v2 pointer, kept by condocv2 -- condoccer (v1) ignores it\ncondocVersion: 2\nname: V2\ncontrolScheme: slopspace-v2\nstate:\n  phase: proposed\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(repo, pointerDir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got := readPointers(repo)
	if len(got) != 1 || got[0].Name != "V1" {
		t.Fatalf("readPointers = %+v, want only V1", got)
	}
}

// TestValidateProposalRefusesCondocV2Branch verifies a v1 proposal can't
// take a condocv2/ branch.
func TestValidateProposalRefusesCondocV2Branch(t *testing.T) {
	p := SlopspaceProposal{Name: "Foo", Targets: []string{"a/one"}, CondocRepo: "a/condocs", Branch: "condocv2/Foo-1/main"}
	if err := validateProposal(p, &PointerSpec{StartTime: 1}); err == nil {
		t.Fatal("validateProposal accepted a condocv2/ branch")
	}
}
