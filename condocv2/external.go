package main

// Refresh remotes (sloppo condocv2WithDiagrammographerImpls Step 1, Prompt 5):
// commits pushed to a condoc's branch from outside condocv2 -- another client,
// a hand-made fix -- are pulled into the slopspace between interactions and
// kept on the pointer as an external change, so the next interaction starts
// from them, its record maps only the agent's work, and the timeline shows
// where they came in.
//
// A refresh brings each writespace repo (every target, then the condoc repo)
// up to date with its remote branch (dungeon-keeper's SyncRepo). One that
// pulls in anything is recorded in its own pointer commit,
//
//	external N.e (pointer)   each repo that moved: from, to, how many commits
//
// N is the interactions completed and e counts the external changes found
// since. One that pulls in nothing records nothing. It runs on *Refresh
// remotes*, and by itself just before a prompt takes the lock (slopPrompt, and
// a request for changes, modifyHeld), so the agent always starts from what the
// remote has.
//
// Only pushes made while the condoc isn't locked are handled this way. One
// made while an interaction holds the lock is still pulled in when the turn is
// queued (runTurn's stageQueued) or rebased under its commits, unrecorded.

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// When an external change was found (ExternalChange.At).
const (
	externalAtRefresh = "refresh" // Refresh remotes, by hand
	externalAtPrompt  = "prompt"  // by itself, as a prompt was about to take the lock
)

// ExternalChange is one repo's branch moved from outside condocv2, as one
// refresh found it. A refresh that moved several repos has one for each, all
// with its N.e.
type ExternalChange struct {
	Interaction int    // N: the interactions completed when it was found
	Seq         int    // e: its number among the external changes after interaction N
	Repo        string // a target, or the condoc repo
	From        string // the branch's head in the slopspace before
	To          string // its head once the remote's commits were pulled in
	Commits     int    // how many commits came in
	At          string // externalAtRefresh or externalAtPrompt
	Time        int64  // when it was found (unix seconds)
}

// nextExternalSeq is e for the next external change after interaction n.
func (p *PointerSpec) nextExternalSeq(n int) int {
	e := 1
	for _, x := range p.Externals {
		if x.Interaction == n && x.Seq >= e {
			e = x.Seq + 1
		}
	}
	return e
}

// externalsAfter is every external change found after interaction n, before
// the next one began.
func (p *PointerSpec) externalsAfter(n int) []ExternalChange {
	var out []ExternalChange
	for _, x := range p.Externals {
		if x.Interaction == n {
			out = append(out, x)
		}
	}
	return out
}

// refreshable reports why the condoc's remotes can't be refreshed now, or "".
// Only between interactions: an outside push is assumed to land while the
// condoc isn't locked.
func (p *PointerSpec) refreshable() string {
	switch {
	case p.Phase == PhaseProposed || p.SlopspaceID == "":
		return "it hasn't begun"
	case p.Configure != nil:
		return fmt.Sprintf("configuration %s is still in progress", sectionLabel(p.Configure.Interaction, p.Configure.Seq))
	case p.Lock != nil:
		return fmt.Sprintf("interaction %d holds the lock", p.Lock.Interaction)
	case p.Hold != nil && p.Hold.Call != nil:
		return fmt.Sprintf("sub-iteration %s is running", sectionLabel(p.Hold.Interaction, p.Hold.Call.Sub))
	case p.Error != "":
		return "it has an error: resubmit first"
	}
	switch p.Phase {
	case PhaseAwaitingPrompt, PhaseReviewing:
		return ""
	}
	return fmt.Sprintf("it is %s", p.Phase)
}

// slopRefreshRemotes is Refresh remotes: pull in whatever was pushed to the
// condoc's branches from outside, recording it if there was any.
func (s *Server) slopRefreshRemotes(stem string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if why := spec.refreshable(); why != "" {
		return fmt.Errorf("%s's remotes can't be refreshed now: %s", spec.Name, why)
	}
	return s.refreshRemotes(&spec, externalAtRefresh)
}

// refreshBeforeLock is the refresh a prompt makes by itself just before it
// takes the lock. The caller holds the claim.
func (s *Server) refreshBeforeLock(spec *PointerSpec) error {
	if why := spec.refreshable(); why != "" {
		return nil // startInteraction refuses it with its own reason
	}
	if err := s.refreshRemotes(spec, externalAtPrompt); err != nil {
		return fmt.Errorf("refreshing remotes before the lock: %v", err)
	}
	return nil
}

// refreshRemotes brings each of the condoc's writespace repos up to date with
// its remote branch, and records those that moved as external change N.e in
// one pointer commit. A repo that can't be brought up to date (its remote
// conflicts) stops the refresh; any moved before it are still recorded. The
// caller holds the claim.
func (s *Server) refreshRemotes(spec *PointerSpec, at string) error {
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return err
	}
	if meta.Deployed {
		return fmt.Errorf("slopspace %s is deployed", spec.SlopspaceID)
	}
	n := spec.Interactions
	e := spec.nextExternalSeq(n)
	now := time.Now().Unix()
	var found []ExternalChange
	var syncErr error
	for _, repo := range spec.Repos() {
		owner, name, _ := strings.Cut(repo, "/")
		from, err := s.slop.ss.RepoHead(spec.SlopspaceID, owner, name)
		if err != nil {
			syncErr = fmt.Errorf("%s: %v", repo, err)
			break
		}
		to, err := s.slop.ss.SyncRepo(spec.SlopspaceID, owner, name)
		if err != nil {
			syncErr = fmt.Errorf("%s: %v", repo, err)
			break
		}
		if to == from {
			continue
		}
		found = append(found, ExternalChange{
			Interaction: n, Seq: e, Repo: repo, From: from, To: to,
			Commits: countCommits(slopGitDir(meta, repo), from, to), At: at, Time: now,
		})
	}
	if len(found) > 0 {
		spec.Externals = append(spec.Externals, found...)
		if err := s.writeSpec(spec, subCommitMsg(spec, "external", n, e)); err != nil {
			return err
		}
	}
	return syncErr
}

// countCommits is how many commits to has that from hasn't (0 when git can't
// say).
func countCommits(gitDir, from, to string) int {
	out, err := exec.Command("git", "--git-dir", gitDir, "rev-list", "--count", from+".."+to).Output()
	if err != nil {
		return 0
	}
	c, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return c
}

// externalLine is an external change as the UI lists it.
func externalLine(x ExternalChange) string {
	how := "on refresh"
	if x.At == externalAtPrompt {
		how = "before the next prompt"
	}
	return fmt.Sprintf("%s %s: %s (%s)", sectionLabel(x.Interaction, x.Seq), x.Repo, commitCount(x.Commits), how)
}

func commitCount(c int) string {
	switch c {
	case 0:
		return "commits" // git couldn't count them
	case 1:
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", c)
}

// externalRecord is what interaction n's record says of the external changes
// pulled in before it began, or "".
func externalRecord(changes []ExternalChange) string {
	if len(changes) == 0 {
		return ""
	}
	lines := []string{"Pulled in from outside condocv2 before this interaction:"}
	for _, x := range changes {
		lines = append(lines, fmt.Sprintf("- %s: %s → %s, %s (external %s)",
			x.Repo, commitLink(x.Repo, x.From), commitLink(x.Repo, x.To), commitCount(x.Commits), sectionLabel(x.Interaction, x.Seq)))
	}
	return strings.Join(lines, "\n")
}

// externalEvents is a node for each external change, in its repo's lane
// (the condoc repo's is "condoc").
func externalEvents(spec *PointerSpec) []TimelineEvent {
	var events []TimelineEvent
	for _, x := range spec.Externals {
		lane := x.Repo
		if x.Repo == spec.CondocRepo {
			lane = "condoc"
		}
		events = append(events, TimelineEvent{
			Lane: lane, Kind: "external", Interaction: x.Interaction, Seq: x.Seq, Note: x.At, Commits: x.Commits,
			SHA: x.To, Time: x.Time, URL: commitURL(x.Repo, x.To),
			Subject: fmt.Sprintf("%s pushed to %s from outside condocv2, pulled in %s", commitCount(x.Commits), x.Repo,
				map[string]string{externalAtRefresh: "by Refresh remotes", externalAtPrompt: "as the next prompt took the lock"}[x.At]),
		})
	}
	return events
}
