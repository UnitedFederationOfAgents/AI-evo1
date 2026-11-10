package main

// Read-only repos (sloppo privateRepoOpsImpls Step 6, Revision A): a condoc
// can name repos its agent reads but never changes, beside its targets. They
// get no branch, no commits and no PRs. At begin each is cloned into
// dungeon-keeper's shared readspaces and added to the slopspace as a
// read-only repo (dungeon-keeper's pkg/slopspace/readrepos.go), which
// dungeon-keeper then pulls afresh -- the ref's latest commit, copied without
// .git and read-only -- on every deploy and every return, discarding whatever
// the agent did to its copy.
//
// For traceability every new commit a read-only repo resolves to is kept on
// the pointer (ReadResolutions) in a commit of its own,
//
//	condocv2: <Name> readspace <N>
//
// N being the interaction whose deploy or return pulled it (none at begin,
// which records the first commits in its own commit). The visual tab draws
// each one as a blue dotted circle in the repo's own read-only lane
// (timeline.go).

import (
	"fmt"
	"regexp"
	"strings"

	dktypes "dungeon-keeper/pkg/types"
)

// ReadOnlyRepo is a read-only repo of a condoc and the ref it follows.
type ReadOnlyRepo struct {
	Repo string `json:"repo"`          // owner/name
	Ref  string `json:"ref,omitempty"` // branch, tag or commit; "" follows the default branch
}

// String is the repo as the proposal form takes it: owner/name[@ref].
func (r ReadOnlyRepo) String() string {
	if r.Ref == "" {
		return r.Repo
	}
	return r.Repo + "@" + r.Ref
}

// readOnlyRefPattern is a ref a read-only repo may follow: a plain branch,
// tag or commit name (never one git could take for an option).
var readOnlyRefPattern = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_./-]*$`)

// parseReadOnlyRepo reads owner/name[@ref] (owner/name may be a pasted URL).
func parseReadOnlyRepo(s string) ReadOnlyRepo {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "@"); i > 0 {
		if repo := normalizeRepo(s[:i]); pointerRepoPattern.MatchString(repo) {
			return ReadOnlyRepo{Repo: repo, Ref: strings.TrimSpace(s[i+1:])}
		}
	}
	return ReadOnlyRepo{Repo: normalizeRepo(s)}
}

// readOnlyStrings is the pointer's read-only repos as owner/name[@ref].
func readOnlyStrings(repos []ReadOnlyRepo) []string {
	var out []string
	for _, r := range repos {
		out = append(out, r.String())
	}
	return out
}

// Where a read-only repo's commit was resolved (ReadResolution.At).
const (
	readAtBegin  = "begin"
	readAtDeploy = "deploy"
	readAtReturn = "return"

	readAtConfigure = "configure" // the ref it follows was changed (configure.go)
)

// ReadResolution is one new commit a read-only repo's ref resolved to.
type ReadResolution struct {
	Repo        string
	Ref         string // the ref it followed then ("" the default branch)
	SHA         string
	Interaction int    // the interaction whose deploy or return pulled it (the last completed, for a configure); 0 at begin
	At          string // begin, deploy, return or configure
	Time        int64  // when dungeon-keeper resolved it (unix seconds)
}

// isReadOnly reports whether repo is one of the condoc's read-only repos.
func (p *PointerSpec) isReadOnly(repo string) bool {
	for _, r := range p.ReadOnly {
		if strings.EqualFold(r.Repo, repo) {
			return true
		}
	}
	return false
}

// lastReadSHA is the commit last recorded for a read-only repo.
func (p *PointerSpec) lastReadSHA(repo string) string {
	for i := len(p.ReadResolutions) - 1; i >= 0; i-- {
		if strings.EqualFold(p.ReadResolutions[i].Repo, repo) {
			return p.ReadResolutions[i].SHA
		}
	}
	return ""
}

// appendReadResolutions records each read-only repo of the slopspace whose
// commit differs from the last one on the pointer, reporting whether there
// were any.
func appendReadResolutions(spec *PointerSpec, repos []dktypes.ReadspaceRepo, n int, at string) bool {
	changed := false
	for _, r := range repos {
		if r.SHA == "" || !spec.isReadOnly(r.Repo) || spec.lastReadSHA(r.Repo) == r.SHA {
			continue
		}
		spec.ReadResolutions = append(spec.ReadResolutions, ReadResolution{
			Repo: r.Repo, Ref: r.Ref, SHA: r.SHA, Interaction: n, At: at, Time: r.ResolvedAt.Unix(),
		})
		changed = true
	}
	return changed
}

// cloneReadOnlyRepos clones (or brings up to date) each read-only repo in
// dungeon-keeper's shared readspaces, before begin creates the slopspace.
func (s *Server) cloneReadOnlyRepos(spec *PointerSpec) error {
	for _, r := range spec.ReadOnly {
		if _, err := s.slop.rs.CloneRepo(r.Repo); err != nil {
			return fmt.Errorf("read-only repo %s: %v", r.Repo, err)
		}
	}
	return nil
}

// addReadOnlyRepos adds each read-only repo to the slopspace at begin and
// records the commits they start at (in the begin commit).
func (s *Server) addReadOnlyRepos(spec *PointerSpec, id string) error {
	if len(spec.ReadOnly) == 0 {
		return nil
	}
	for _, r := range spec.ReadOnly {
		owner, name, _ := strings.Cut(r.Repo, "/")
		if _, err := s.slop.ss.AddReadspaceRepo(id, owner, name, r.Ref, s.slop.rs.RepoPath(owner, name)); err != nil {
			return fmt.Errorf("add read-only repo %s to the slopspace: %v", r.Repo, err)
		}
	}
	meta, err := s.slop.ss.Get(id)
	if err != nil {
		return err
	}
	appendReadResolutions(spec, meta.ReadspaceRepos, 0, readAtBegin)
	return nil
}

// noteReadResolutions is the pointer's "readspace N" commit, when the
// deploy or return of interaction n pulled a new commit into any read-only
// repo. It is idempotent, so a resumed stage can call it again.
func (s *Server) noteReadResolutions(spec *PointerSpec, n int, at string) error {
	if len(spec.ReadOnly) == 0 {
		return nil
	}
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return err
	}
	if !appendReadResolutions(spec, meta.ReadspaceRepos, n, at) {
		return nil
	}
	return s.writeSpec(spec, commitMsg(spec, "readspace", n))
}

// readOnlyRepoNote tells the agent where the read-only repos are in the
// deployed slopspace, and at which commits.
func readOnlyRepoNote(deployReadRepos string, repos []dktypes.ReadspaceRepo) string {
	if len(repos) == 0 {
		return ""
	}
	var list []string
	for _, r := range repos {
		ref := r.Ref
		if ref == "" {
			ref = "its default branch"
		}
		sha := r.SHA
		if len(sha) > 12 {
			sha = sha[:12]
		}
		list = append(list, fmt.Sprintf("%s (%s, at %s)", r.Repo, ref, sha))
	}
	return fmt.Sprintf("\n\nRead-only repos are under '%s': %s. They are copies, pulled afresh each time the slopspace is "+
		"deployed and returned: read them, but do not modify them in an effort to make persistent changes -- they are "+
		"read-only, and any change to them is discarded when you finish.",
		deployReadRepos, strings.Join(list, ", "))
}
