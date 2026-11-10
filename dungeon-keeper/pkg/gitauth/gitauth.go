// Package gitauth supplies GitHub HTTPS + PAT authentication to git commands
// without ever writing the token into a clone's .git/config or argv.
//
// The PAT travels as an Authorization header through GIT_CONFIG_* env vars
// (git >= 2.31), the same approach condocv2's pointer repo uses. That
// matters for writespaces in particular: deploying a slopspace moves its
// repos into the agent's reach, and their .git dirs into writespaces-secure,
// so a token embedded in a remote URL would sit one directory away from the
// agent (see docs/SlopspaceControlScheme.md, D11).
package gitauth

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Token returns the GitHub PAT: TF_VAR_github_pat, falling back to the usual
// gh/Actions names.
func Token() string {
	for _, k := range []string{"TF_VAR_github_pat", "GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// LocalOwner is the reserved owner for repos with no remote: local/<name> is
// the git repo at <LocalRoot>/<name>, used in place of a GitHub URL.
const LocalOwner = "local"

// LocalRoot is where local/<name> repos live: UFA_LOCAL_ROOT, defaulting to
// ~/workspace/ufa-local.
func LocalRoot() string {
	if v := strings.TrimSpace(os.Getenv("UFA_LOCAL_ROOT")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("workspace", "ufa-local")
	}
	return filepath.Join(home, "workspace", "ufa-local")
}

// IsLocal reports whether ownerRepo names a local repo rather than a GitHub one.
func IsLocal(ownerRepo string) bool {
	owner, _, _ := strings.Cut(ownerRepo, "/")
	return owner == LocalOwner
}

// LocalPath is the path of a local/<name> repo.
func LocalPath(ownerRepo string) string {
	_, name, _ := strings.Cut(ownerRepo, "/")
	return filepath.Join(LocalRoot(), name)
}

// RepoURL is the credential-free URL to clone an owner/name repo from: HTTPS
// on GitHub, or the repo's path for a local one. A clone's origin is this URL,
// so a local repo's slopspace branches are fetched from and pushed to it.
func RepoURL(ownerRepo string) string {
	if IsLocal(ownerRepo) {
		return LocalPath(ownerRepo)
	}
	return "https://github.com/" + ownerRepo + ".git"
}

// CanClone reports why ownerRepo can't be cloned, or nil: a GitHub repo needs
// the PAT, and a local one must be a git repo under LocalRoot.
func CanClone(ownerRepo string) error {
	if IsLocal(ownerRepo) {
		path := LocalPath(ownerRepo)
		if err := exec.Command("git", "-C", path, "rev-parse", "--git-dir").Run(); err != nil {
			return fmt.Errorf("local repo %s: no git repo at %s (set UFA_LOCAL_ROOT to change the root)", ownerRepo, path)
		}
		return nil
	}
	if Token() == "" {
		return fmt.Errorf("TF_VAR_github_pat environment variable not set")
	}
	return nil
}

// Env returns the environment for a git command against GitHub: the current
// environment, no interactive prompting, and (when a token is set) the PAT as
// an extra HTTP header scoped to https://github.com/.
func Env() []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	tok := Token()
	if tok == "" {
		return env
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
	return append(env,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+basic,
	)
}

// Command builds a git command with Env applied. dir may be "" for commands
// that don't run inside a repo.
func Command(dir string, args ...string) *exec.Cmd {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = Env()
	return cmd
}

// RemoteBranchExists reports whether branch exists on ownerRepo's GitHub
// remote, or in the local repo itself for local/<name>.
func RemoteBranchExists(ownerRepo, branch string) (bool, error) {
	if IsLocal(ownerRepo) {
		if err := CanClone(ownerRepo); err != nil {
			return false, err
		}
	}
	cmd := Command("", "ls-remote", "--exit-code", "--heads", RepoURL(ownerRepo), "refs/heads/"+branch)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 2 {
		return false, nil // --exit-code: no matching ref
	}
	return false, fmt.Errorf("ls-remote %s: %v: %s", ownerRepo, err, strings.TrimSpace(string(out)))
}

// ScrubRemote resets repoPath's origin to the credential-free URL when it
// carries embedded credentials, as clones made before this package did.
func ScrubRemote(repoPath, ownerRepo string) error {
	out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	if err != nil {
		return nil // no origin; nothing to scrub
	}
	url := strings.TrimSpace(string(out))
	if !strings.HasPrefix(url, "https://") || !strings.Contains(url, "@github.com/") {
		return nil
	}
	if out, err := exec.Command("git", "-C", repoPath, "remote", "set-url", "origin", RepoURL(ownerRepo)).CombinedOutput(); err != nil {
		return fmt.Errorf("scrub origin: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
