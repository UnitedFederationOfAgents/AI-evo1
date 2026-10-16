package slopspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dungeon-keeper/pkg/types"
	"dungeon-keeper/pkg/writespace"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestWriteRepoChangesReportsSHA covers the D6 groundwork in
// docs/SlopspaceControlScheme.md: writes report the commit they made (or the
// unchanged HEAD), RepoHead/WritespaceRepos read writespaces-secure directly,
// and the shared clone's WRITESPACE.json never reaches the slopspace branch.
func TestWriteRepoChangesReportsSHA(t *testing.T) {
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
		"TF_VAR_github_pat": "", "GH_TOKEN": "", "GITHUB_TOKEN": "",
	} {
		t.Setenv(k, v)
	}

	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	src := filepath.Join(tmp, "src")
	runGit(t, tmp, "init", "-q", "--bare", bare)
	runGit(t, tmp, "clone", "-q", bare, src)
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hi\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", ".")
	runGit(t, src, "commit", "-q", "-m", "init")
	runGit(t, src, "push", "-q", "origin", "HEAD")
	if err := os.WriteFile(filepath.Join(src, writespace.MetadataFile), []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = filepath.Join(tmp, "slopspaces")
	cfg.AgentSlopspaceRoot = filepath.Join(tmp, "agent")
	mgr := NewManager(cfg)
	meta, err := mgr.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddWritespaceRepo(meta.ID, "o", "r", "feature", src); err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(meta.RootPath, WriteSpacesDir, ReposDir, "o", "r")
	if _, err := os.Stat(filepath.Join(repoPath, writespace.MetadataFile)); !os.IsNotExist(err) {
		t.Errorf("%s was copied into the slopspace repo", writespace.MetadataFile)
	}

	repos, err := mgr.WritespaceRepos(meta.ID)
	if err != nil || !reflect.DeepEqual(repos, []string{"o/r"}) {
		t.Fatalf("WritespaceRepos = %v, %v", repos, err)
	}
	base, err := mgr.RepoHead(meta.ID, "o", "r")
	if err != nil || base == "" {
		t.Fatalf("RepoHead = %q, %v", base, err)
	}

	// Nothing changed yet: no commit, HEAD reported as-is.
	res, err := mgr.WriteRepoChanges(meta.ID, "o", "r", "noop")
	if err != nil || res.Changed || res.SHA != base {
		t.Fatalf("no-op write = %+v, %v (base %s)", res, err, base)
	}

	if err := os.WriteFile(filepath.Join(repoPath, "new.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	res, err = mgr.WriteRepoChanges(meta.ID, "o", "r", "condoc: step 1 initial reply")
	if err != nil || !res.Changed || res.SHA == base {
		t.Fatalf("write = %+v, %v", res, err)
	}
	if got := runGit(t, tmp, "--git-dir", bare, "rev-parse", "refs/heads/feature"); got != res.SHA {
		t.Errorf("remote feature = %s, want %s", got, res.SHA)
	}
	if head, _ := mgr.RepoHead(meta.ID, "o", "r"); head != res.SHA {
		t.Errorf("RepoHead after write = %s, want %s", head, res.SHA)
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git was left in the writespace after the write")
	}

	results, err := mgr.WriteAllRepoChanges(meta.ID, "again")
	if err != nil || len(results) != 1 || results[0].Changed {
		t.Errorf("WriteAllRepoChanges = %+v, %v", results, err)
	}
}

func TestPushRepoPublishesBranch(t *testing.T) {
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
	} {
		t.Setenv(k, v)
	}
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	src := filepath.Join(tmp, "src")
	runGit(t, tmp, "init", "-q", "--bare", bare)
	runGit(t, tmp, "clone", "-q", bare, src)
	runGit(t, src, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, src, "push", "-q", "origin", "HEAD")

	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = filepath.Join(tmp, "slopspaces")
	cfg.AgentSlopspaceRoot = filepath.Join(tmp, "agent")
	mgr := NewManager(cfg)
	meta, err := mgr.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddWritespaceRepo(meta.ID, "o", "r", "condoc/X-1/main", src); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PushRepo(meta.ID, "o", "r"); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, tmp, "--git-dir", bare, "branch", "--list", "condoc/X-1/main"); !strings.Contains(got, "condoc/X-1/main") {
		t.Errorf("branch not pushed: %q", got)
	}
}
