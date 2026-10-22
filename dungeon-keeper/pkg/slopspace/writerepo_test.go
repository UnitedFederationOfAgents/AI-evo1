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

// TestWriteRepoChangesPushesAfterFailedPush covers a write whose commit landed
// but whose push failed: the retry has nothing new to commit, yet must still
// push that commit.
func TestWriteRepoChangesPushesAfterFailedPush(t *testing.T) {
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
	if err := mgr.AddWritespaceRepo(meta.ID, "o", "r", "feature", src); err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(meta.RootPath, WriteSpacesDir, ReposDir, "o", "r")
	secureGitPath := filepath.Join(meta.RootPath, WriteSpacesSecure, ReposDir, "o", "r")
	if err := os.WriteFile(filepath.Join(repoPath, "new.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Break the remote so the commit is made but the push fails.
	runGit(t, tmp, "--git-dir", secureGitPath, "remote", "set-url", "origin", filepath.Join(tmp, "missing.git"))
	res, err := mgr.WriteRepoChanges(meta.ID, "o", "r", "condoc: step 1 reply")
	if err == nil || res.Changed {
		t.Fatalf("write with a broken remote = %+v, %v; want a push error", res, err)
	}
	committed := res.SHA
	if head, _ := mgr.RepoHead(meta.ID, "o", "r"); head != committed {
		t.Fatalf("RepoHead after failed push = %s, want %s", head, committed)
	}

	// The retry has nothing to commit but must push the stranded commit.
	runGit(t, tmp, "--git-dir", secureGitPath, "remote", "set-url", "origin", bare)
	res, err = mgr.WriteRepoChanges(meta.ID, "o", "r", "condoc: step 1 reply")
	if err != nil || res.Changed || res.SHA != committed {
		t.Fatalf("retry = %+v, %v (want SHA %s, unchanged)", res, err, committed)
	}
	if got := runGit(t, tmp, "--git-dir", bare, "rev-parse", "refs/heads/feature"); got != committed {
		t.Errorf("remote feature = %s, want %s", got, committed)
	}
}

// TestWriteRepoChangesIntegratesOutsidePush covers a branch someone pushed to
// from outside the slopspace (e.g. scripts/ufabuild.sh): SyncRepo brings the
// commit in, a write rebases its own commit onto a later outside push instead
// of being rejected, and a conflicting push leaves the repo untouched.
func TestWriteRepoChangesIntegratesOutsidePush(t *testing.T) {
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
	if err := mgr.AddWritespaceRepo(meta.ID, "o", "r", "feature", src); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PushRepo(meta.ID, "o", "r"); err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(meta.RootPath, WriteSpacesDir, ReposDir, "o", "r")

	// An outside checkout of the branch pushes a commit.
	outside := filepath.Join(tmp, "outside")
	runGit(t, tmp, "clone", "-q", "-b", "feature", bare, outside)
	outsidePush := func(file, body string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(outside, file), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		runGit(t, outside, "add", ".")
		runGit(t, outside, "commit", "-q", "-m", "outside "+file)
		runGit(t, outside, "push", "-q", "origin", "HEAD")
		return runGit(t, outside, "rev-parse", "HEAD")
	}
	first := outsidePush(".ufabuild", "parent: x\n")

	head, err := mgr.SyncRepo(meta.ID, "o", "r")
	if err != nil || head != first {
		t.Fatalf("SyncRepo = %s, %v; want %s", head, err, first)
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".ufabuild")); err != nil {
		t.Errorf("synced file missing from the writespace: %v", err)
	}

	// Another outside push lands while the agent works; the write must rebase
	// onto it rather than be rejected.
	second := outsidePush("other.txt", "o\n")
	if err := os.WriteFile(filepath.Join(repoPath, "agent.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	res, err := mgr.WriteRepoChanges(meta.ID, "o", "r", "condoc: reply")
	if err != nil || !res.Changed {
		t.Fatalf("write after an outside push = %+v, %v", res, err)
	}
	if got := runGit(t, tmp, "--git-dir", bare, "rev-parse", "refs/heads/feature"); got != res.SHA {
		t.Errorf("remote feature = %s, want %s", got, res.SHA)
	}
	if got := runGit(t, tmp, "--git-dir", bare, "rev-parse", res.SHA+"^"); got != second {
		t.Errorf("reply commit's parent = %s, want the outside commit %s", got, second)
	}

	// A conflicting outside push: the write fails, the rebase is aborted and
	// the local commit is left as it was.
	runGit(t, outside, "pull", "-q", "--ff-only")
	outsidePush("agent.txt", "outside\n")
	if err := os.WriteFile(filepath.Join(repoPath, "agent.txt"), []byte("agent\n"), 0644); err != nil {
		t.Fatal(err)
	}
	res, err = mgr.WriteRepoChanges(meta.ID, "o", "r", "condoc: conflicting reply")
	if err == nil || res.Changed || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("conflicting write = %+v, %v; want a conflict error", res, err)
	}
	if head, _ := mgr.RepoHead(meta.ID, "o", "r"); head != res.SHA {
		t.Errorf("RepoHead after the aborted rebase = %s, want the local commit %s", head, res.SHA)
	}
	if b, _ := os.ReadFile(filepath.Join(repoPath, "agent.txt")); string(b) != "agent\n" {
		t.Errorf("agent.txt after the aborted rebase = %q", b)
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
