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
