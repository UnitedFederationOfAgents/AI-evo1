package slopspace

import (
	"os"
	"path/filepath"
	"testing"

	"dungeon-keeper/pkg/types"
)

// TestReadspaceRepoFollowsRef covers read-only repos (readrepos.go): added
// at the ref's commit without .git and read-only, exported again at the
// ref's latest commit on every deploy and return -- which also throws away
// whatever the agent did to the copy -- with each new commit recorded.
func TestReadspaceRepoFollowsRef(t *testing.T) {
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
		"TF_VAR_github_pat": "", "GH_TOKEN": "", "GITHUB_TOKEN": "",
	} {
		t.Setenv(k, v)
	}
	mgr, cfg := newTestManager(t)

	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	work := filepath.Join(tmp, "work")
	shared := filepath.Join(tmp, "readspaces", "repos", "o", "r") // pkg/readspace's clone
	runGit(t, tmp, "init", "-q", "--bare", bare)
	runGit(t, tmp, "clone", "-q", bare, work)
	commit := func(content string) string {
		t.Helper()
		writeFile(t, filepath.Join(work, "README.md"), content)
		runGit(t, work, "add", ".")
		runGit(t, work, "commit", "-q", "-m", content)
		runGit(t, work, "push", "-q", "origin", "HEAD")
		return runGit(t, work, "rev-parse", "HEAD")
	}
	v1 := commit("v1\n")
	runGit(t, work, "tag", "v1")
	runGit(t, work, "push", "-q", "origin", "v1")
	runGit(t, tmp, "clone", "-q", bare, shared)

	m, err := mgr.Create()
	if err != nil {
		t.Fatal(err)
	}
	added, err := mgr.AddReadspaceRepo(m.ID, "o", "r", "", shared)
	if err != nil {
		t.Fatal(err)
	}
	if added.SHA != v1 {
		t.Fatalf("added at %s, want %s", added.SHA, v1)
	}
	idle := filepath.Join(m.RootPath, ReadSpacesDir, ReposDir, "o", "r")
	if got := readFile(t, filepath.Join(idle, "README.md")); got != "v1\n" {
		t.Errorf("README = %q", got)
	}
	if _, err := os.Stat(filepath.Join(idle, ".git")); !os.IsNotExist(err) {
		t.Errorf("the copy has a .git")
	}
	if fi, err := os.Stat(filepath.Join(idle, "README.md")); err != nil || fi.Mode().Perm()&0222 != 0 {
		t.Errorf("README isn't read-only: %v, %v", fi.Mode(), err)
	}

	// A new commit is picked up by the deploy.
	v2 := commit("v2\n")
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatal(err)
	}
	deployed := filepath.Join(cfg.DeployPathForAgentType(types.AgentTypeWorker), ReadSpacesDir, ReposDir, "o", "r")
	if got := readFile(t, filepath.Join(deployed, "README.md")); got != "v2\n" {
		t.Errorf("deployed README = %q", got)
	}
	if os.Geteuid() != 0 { // root writes anyway
		if err := os.WriteFile(filepath.Join(deployed, "README.md"), []byte("changed\n"), 0644); err == nil {
			t.Errorf("the deployed copy could be changed")
		}
	}
	// The agent changes it anyway; the return discards that and pulls again.
	os.Chmod(deployed, 0755)
	os.Chmod(filepath.Join(deployed, "README.md"), 0644)
	writeFile(t, filepath.Join(deployed, "README.md"), "changed\n")
	writeFile(t, filepath.Join(deployed, "stray.txt"), "x\n")
	v3 := commit("v3\n")
	if err := mgr.Return(m.ID); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(idle, "README.md")); got != "v3\n" {
		t.Errorf("returned README = %q", got)
	}
	if _, err := os.Stat(filepath.Join(idle, "stray.txt")); !os.IsNotExist(err) {
		t.Errorf("the agent's stray file survived the return")
	}

	// Nothing new: another deploy and return record nothing more.
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Return(m.ID); err != nil {
		t.Fatal(err)
	}
	m, err = mgr.Get(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ReadspaceRepos) != 1 {
		t.Fatalf("ReadspaceRepos = %+v", m.ReadspaceRepos)
	}
	r := m.ReadspaceRepos[0]
	if r.Repo != "o/r" || r.SHA != v3 || r.Error != "" {
		t.Errorf("repo = %+v", r)
	}
	want := []types.ReadspaceResolution{
		{SHA: v1, Iteration: 0, Event: ReadspaceEventAdd},
		{SHA: v2, Iteration: 1, Event: ReadspaceEventDeploy},
		{SHA: v3, Iteration: 1, Event: ReadspaceEventReturn},
	}
	if len(r.Resolutions) != len(want) {
		t.Fatalf("resolutions = %+v", r.Resolutions)
	}
	for i, w := range want {
		g := r.Resolutions[i]
		if g.SHA != w.SHA || g.Iteration != w.Iteration || g.Event != w.Event {
			t.Errorf("resolution %d = %+v, want %+v", i, g, w)
		}
	}

	// Following a tag pins it.
	pinned, err := mgr.AddReadspaceRepo(m.ID, "o", "r", "v1", shared)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.SHA != v1 || pinned.Ref != "v1" {
		t.Errorf("pinned = %+v", pinned)
	}
	if got := readFile(t, filepath.Join(idle, "README.md")); got != "v1\n" {
		t.Errorf("pinned README = %q", got)
	}

	// The read-only copy doesn't stop the slopspace being deleted.
	if err := mgr.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.RootPath); !os.IsNotExist(err) {
		t.Errorf("the slopspace survived Delete: %v", err)
	}
}
