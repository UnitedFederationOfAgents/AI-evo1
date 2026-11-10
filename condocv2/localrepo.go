package main

// Local repos (PrivateRepoOps Step 5, Revision J): a target or condoc repo
// named local/<name> is the git repo at $UFA_LOCAL_ROOT/<name>
// (~/workspace/ufa-local by default), with no remote -- dungeon-keeper's
// gitauth. dungeon-keeper already clones a slopspace's
// copy from that path and pushes the condoc branch straight back into it, so
// what condocv2 adds is:
//
//   - the branch check against the local repo (gitauth.RemoteBranchExists);
//   - links: ufa-local:<name>/commit/<sha> and ufa-local:<name>/blob/<ref>/<path>
//     in place of github.com ones (the frontend shows them as text);
//   - a local merge in place of each PR: Raise PRs records one per local repo
//     (Number 0, its URL ufa-local:<name>/compare/<base>...<branch>), the PRs
//     tab shows it ready while the branch can merge into its base, and Merge
//     merges the condoc branch into the base in the local repo itself, with a
//     merge commit, as GitHub's merge would -- so a local condoc runs its full
//     lifecycle, through merged and archived;
//   - the merged condoc's timeline record, read from the local repo.
//
// GitHub and local repos may be mixed in one condoc; each PR goes its own way.
// The pointer repo is still on GitHub.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"dungeon-keeper/pkg/gitauth"
	prkeeper "pr-keeper"
)

// localName is local/<name>'s <name>.
func localName(repo string) string {
	return strings.TrimPrefix(repo, gitauth.LocalOwner+"/")
}

// repoCommitURL links sha in repo: on GitHub, or ufa-local:<name>/commit/<sha>.
func repoCommitURL(repo, sha string) string {
	if gitauth.IsLocal(repo) {
		return "ufa-local:" + localName(repo) + "/commit/" + sha
	}
	return fmt.Sprintf("https://github.com/%s/commit/%s", repo, sha)
}

// repoBlobURL links path at ref in repo: on GitHub, or
// ufa-local:<name>/blob/<ref>/<path>.
func repoBlobURL(repo, ref, path string) string {
	if gitauth.IsLocal(repo) {
		return "ufa-local:" + localName(repo) + "/blob/" + ref + "/" + path
	}
	return fmt.Sprintf("https://github.com/%s/blob/%s/%s", repo, ref, path)
}

// allLocal: every draft's repo is local, so raising needs no GitHub token.
func allLocal(drafts []prDraft) bool {
	for _, d := range drafts {
		if !gitauth.IsLocal(d.Repo) {
			return false
		}
	}
	return true
}

// allLocalPRs: every PR is a local merge, so merging needs no GitHub token.
func allLocalPRs(prs []PointerPR) bool {
	for _, pr := range prs {
		if !gitauth.IsLocal(pr.Repo) {
			return false
		}
	}
	return true
}

// localGit runs git in local repo's directory and returns its output, trimmed.
func localGit(repo string, args ...string) (string, error) {
	dir := gitauth.LocalPath(repo)
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: git %s: %v: %s", repo, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// localPR is the PR condocv2 records for merging branch into base in local
// repo: there's nowhere to raise one, so it is the merge to come.
func localPR(repo, branch, base string) PointerPR {
	return PointerPR{Repo: repo, URL: "ufa-local:" + localName(repo) + "/compare/" + base + "..." + branch, State: "open"}
}

// localCompare reads a local PR's base and branch back from its URL.
func localCompare(pr PointerPR) (base, branch string, ok bool) {
	_, rest, found := strings.Cut(pr.URL, "/compare/")
	if !found {
		return "", "", false
	}
	return strings.Cut(rest, "...")
}

// localPRLive is a local PR as the local repo has it: merged once branch is
// in base, else ready while branch is there to merge.
func localPRLive(pr PointerPR) PRLive {
	live := PRLive{PointerPR: pr, Status: pr.State}
	base, branch, ok := localCompare(pr)
	if !ok {
		live.Status = "unknown: not a local merge (" + pr.URL + ")"
		return live
	}
	if err := gitauth.CanClone(pr.Repo); err != nil {
		live.Status = "unknown: " + err.Error()
		return live
	}
	if _, err := localGit(pr.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		live.Status = "unknown: " + branch + " isn't in " + gitauth.LocalPath(pr.Repo)
		return live
	}
	if _, err := localGit(pr.Repo, "merge-base", "--is-ancestor", branch, base); err == nil {
		live.State, live.Status = "merged", "merged"
		return live
	}
	live.Status, live.Ready = "local merge ready", true
	return live
}

// localMerge merges branch into base in local repo with a merge commit, and
// returns what GitHub's merge would: the PR merged, with its merge commit.
// A branch already in base is merged already. The base is merged into where
// it is checked out -- the repo's own work tree, which must be clean -- or,
// when it isn't, in a work tree of its own, removed afterwards.
func localMerge(pr PointerPR, title string) (*prkeeper.PullRequest, error) {
	base, branch, ok := localCompare(pr)
	if !ok {
		return nil, fmt.Errorf("%s: %s isn't a local merge", pr.Repo, pr.URL)
	}
	if err := gitauth.CanClone(pr.Repo); err != nil {
		return nil, err
	}
	merged := func() (*prkeeper.PullRequest, error) {
		sha, err := localGit(pr.Repo, "rev-parse", "refs/heads/"+base)
		if err != nil {
			return nil, err
		}
		return &prkeeper.PullRequest{State: "closed", Merged: true, MergeCommitSHA: sha, MergedAt: time.Now().UTC().Format(time.RFC3339),
			URL: pr.URL, Base: prkeeper.Ref{Ref: base}, Head: prkeeper.Ref{Ref: branch}}, nil
	}
	if _, err := localGit(pr.Repo, "merge-base", "--is-ancestor", branch, base); err == nil {
		return merged()
	}
	args := []string{"merge", "--no-ff", "--no-edit", "-m", fmt.Sprintf("Merge %s into %s: %s", branch, base, title), branch}
	if email, _ := localGit(pr.Repo, "config", "user.email"); email == "" {
		args = append([]string{"-c", "user.name=condocv2", "-c", "user.email=condocv2@localhost"}, args...)
	}
	dir := gitauth.LocalPath(pr.Repo)
	current, _ := localGit(pr.Repo, "symbolic-ref", "--short", "HEAD")
	if current != base {
		wt, err := os.MkdirTemp("", "condocv2-local-merge-")
		if err != nil {
			return nil, err
		}
		os.Remove(wt) // worktree add makes it
		if _, err := localGit(pr.Repo, "worktree", "add", "--quiet", wt, base); err != nil {
			return nil, err
		}
		defer func() {
			localGit(pr.Repo, "worktree", "remove", "--force", wt)
			os.RemoveAll(wt)
		}()
		dir = wt
	} else if dirty, err := localGit(pr.Repo, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return nil, err
	} else if dirty != "" {
		return nil, fmt.Errorf("%s has uncommitted changes on %s, its checked-out branch; commit or stash them, then Merge again", dir, base)
	}
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		exec.Command("git", "-C", dir, "merge", "--abort").Run()
		return nil, fmt.Errorf("merging %s into %s in %s: %v: %s", branch, base, gitauth.LocalPath(pr.Repo), err, strings.TrimSpace(string(out)))
	}
	return merged()
}

// localFileContent is path at ref in local repo, as prkeeper's FileContent is
// on GitHub; ref "" is the repo's checked-out branch.
func localFileContent(repo, ref, path string) ([]byte, error) {
	if err := gitauth.CanClone(repo); err != nil {
		return nil, err
	}
	if ref == "" {
		ref = "HEAD"
	}
	out, err := exec.Command("git", "-C", gitauth.LocalPath(repo), "show", ref+":"+path).Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %s isn't at %s", repo, path, ref)
	}
	return out, nil
}
