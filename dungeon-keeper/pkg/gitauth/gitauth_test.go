package gitauth

import (
	"encoding/base64"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvCarriesTokenAsHeader(t *testing.T) {
	t.Setenv("TF_VAR_github_pat", "secret-pat")
	env := strings.Join(Env(), "\n")
	want := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:secret-pat"))
	if !strings.Contains(env, want) {
		t.Errorf("Env() lacks the auth header")
	}
	if !strings.Contains(env, "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader") {
		t.Errorf("Env() lacks the extraheader key")
	}
}

func TestEnvWithoutToken(t *testing.T) {
	t.Setenv("TF_VAR_github_pat", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	if strings.Contains(strings.Join(Env(), "\n"), "GIT_CONFIG_VALUE_0=AUTHORIZATION") {
		t.Errorf("Env() set a header without a token")
	}
}

func TestLocalRepos(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UFA_LOCAL_ROOT", root)
	t.Setenv("TF_VAR_github_pat", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	if !IsLocal("local/proj") || IsLocal("o/r") || IsLocal("o/local") {
		t.Errorf("IsLocal misclassifies")
	}
	if got, want := RepoURL("local/proj"), filepath.Join(root, "proj"); got != want {
		t.Errorf("RepoURL(local/proj) = %q, want %q", got, want)
	}
	if got := RepoURL("o/r"); got != "https://github.com/o/r.git" {
		t.Errorf("RepoURL(o/r) = %q", got)
	}
	if err := CanClone("local/proj"); err == nil {
		t.Errorf("CanClone passed a missing local repo")
	}
	if out, err := exec.Command("git", "init", "-q", filepath.Join(root, "proj")).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := CanClone("local/proj"); err != nil {
		t.Errorf("CanClone(local/proj) = %v", err)
	}
	// A GitHub repo still needs the PAT; a local one never does.
	if err := CanClone("o/r"); err == nil {
		t.Errorf("CanClone(o/r) passed without a token")
	}
}

func TestLocalRootDefault(t *testing.T) {
	t.Setenv("UFA_LOCAL_ROOT", "")
	t.Setenv("HOME", "/home/someone")
	if got := LocalRoot(); got != "/home/someone/workspace/ufa-local" {
		t.Errorf("LocalRoot() = %q", got)
	}
}

func TestScrubRemote(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "r")
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "https://ghp_x@github.com/o/r.git").CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v %s", err, out)
	}
	if err := ScrubRemote(dir, "o/r"); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if got := strings.TrimSpace(string(out)); got != "https://github.com/o/r.git" {
		t.Errorf("origin = %q", got)
	}
}
