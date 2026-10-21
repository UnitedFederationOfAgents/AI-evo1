// Package gitauth supplies GitHub HTTPS + PAT authentication to git commands
// without ever writing the token into a clone's .git/config or argv.
//
// The PAT travels as an Authorization header through GIT_CONFIG_* env vars
// (git >= 2.31), the same approach condoccer's pointer repo uses. That
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

// RepoURL is the credential-free HTTPS clone URL for an owner/name repo.
func RepoURL(ownerRepo string) string {
	return "https://github.com/" + ownerRepo + ".git"
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
// remote.
func RemoteBranchExists(ownerRepo, branch string) (bool, error) {
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
