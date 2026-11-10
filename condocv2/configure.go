package main

// Configure condoc (sloppo privateRepoOpsImpls Step 6, Revision B; sloppo
// switchToUfabuildImpls Step 1, Prompt 8): a condoc can be reconfigured
// between interactions. For now there are two things to configure, both in
// its repos: the branch (or tag, or commit) a read-only repo follows, and a
// new read-only repo, added mid-condoc (RepoConfigure.Kind).
//
// Like a DTT tool call, it is done within a momentary lock in the pointer
// repo -- the read-only repos and the refs they follow are part of the
// version-controlled record, so the change is too:
//
//	configure-lock N.c (pointer)    the change asked for (RepoConfigure)
//	  -> the slopspace's read-only repo follows the new ref (or, added, is
//	     cloned into the shared readspaces and registered with the slopspace),
//	     exported afresh at the commit it resolves to (dungeon-keeper's
//	     AddReadspaceRepo)
//	  -> configure-unlock N.c (pointer)  readOnly holds the new ref (or the
//	     new repo), the configuration is kept in Configurations (also when it
//	     failed, with why), and the new commit in ReadResolutions
//
// N is the interactions completed (0 before the first) and c counts the
// configurations since. The claim is held throughout, so nothing else touches
// the condoc meanwhile; a lock left behind by a restart is finished by the
// tick (tickConfigure). A proposal has no slopspace yet: there only the
// pointer changes, in the same two commits.
//
// The pointer alone keeps all of it: readOnly is what the condoc reads, from
// whichever interaction on, and Configurations and ReadResolutions say when
// each repo and ref came in. The condoc's own files say nothing about repos
// (its main file names only its pointer repo), so a condocv2 that later takes
// a condoc up from its pointer alone rebuilds the same read-only repos.

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// Configuration stages (RepoConfigure.Stage).
const (
	configureLocked  = "locked"  // configure-lock is committed; the slopspace is next
	configureApplied = "applied" // the slopspace follows the new ref; configure-unlock is next
)

// What a configuration changes (RepoConfigure.Kind).
const (
	configureRef = ""    // the ref a read-only repo follows
	configureAdd = "add" // a new read-only repo, following To
)

// RepoConfigure is the configuration in flight. Its presence in the pointer
// is the lock: committed when it starts and cleared, in its own commit, when
// it ends.
type RepoConfigure struct {
	Interaction int    // N: the interactions completed when it was asked for
	Seq         int    // c: its number among the configurations after interaction N
	Kind        string // configureRef or configureAdd
	Repo        string // the read-only repo
	From        string // the ref it followed ("" the default branch; always "" for an add)
	To          string // the ref it is to follow ("" the default branch)
	Stage       string
	SHA         string // applied: the commit To resolved to
	Failed      string // why the slopspace couldn't follow To (the ref, or the repos, are left as they were)
}

// RepoConfiguration is one configuration done, kept on the pointer.
type RepoConfiguration struct {
	Interaction int
	Seq         int
	Kind        string
	Repo        string
	From        string
	To          string
	SHA         string // the commit To resolved to ("" for a proposal, or when it failed)
	Failed      string // why it wasn't applied
	At          string // when it was unlocked (RFC 3339)
}

// nextConfigureSeq is c for the next configuration after interaction n.
func (p *PointerSpec) nextConfigureSeq(n int) int {
	c := 1
	for _, h := range p.Configurations {
		if h.Interaction == n && h.Seq >= c {
			c = h.Seq + 1
		}
	}
	return c
}

// readOnlyRepo is the condoc's read-only repo named repo, or nil.
func (p *PointerSpec) readOnlyRepo(repo string) *ReadOnlyRepo {
	for i := range p.ReadOnly {
		if strings.EqualFold(p.ReadOnly[i].Repo, repo) {
			return &p.ReadOnly[i]
		}
	}
	return nil
}

// configurable reports why the condoc can't be configured now, or "".
func (p *PointerSpec) configurable() string {
	switch {
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
	case PhaseProposed, PhaseAwaitingPrompt, PhaseReviewing:
		return ""
	}
	return fmt.Sprintf("it is %s", p.Phase)
}

// slopConfigureRepo makes the condoc's read-only repo follow ref ("" its
// default branch): configure-lock, the slopspace, configure-unlock. A ref the
// slopspace can't follow is still unlocked, recorded as failed, and returned.
func (s *Server) slopConfigureRepo(stem, repo, ref string) error {
	repo, ref = normalizeRepo(repo), strings.TrimSpace(ref)
	if ref != "" && !readOnlyRefPattern.MatchString(ref) {
		return fmt.Errorf("%q isn't a branch, tag or commit name", ref)
	}
	return s.startConfigure(stem, func(spec *PointerSpec) (*RepoConfigure, error) {
		r := spec.readOnlyRepo(repo)
		if r == nil {
			return nil, fmt.Errorf("%s isn't a read-only repo of %s", repo, spec.Name)
		}
		if r.Ref == ref {
			return nil, fmt.Errorf("%s already follows %s", r.Repo, refName(ref))
		}
		return &RepoConfigure{Kind: configureRef, Repo: r.Repo, From: r.Ref, To: ref}, nil
	})
}

// slopAddReadOnlyRepo adds a read-only repo (owner/name[@ref]) to a condoc
// that may already be under way: configure-lock, the repo cloned into the
// shared readspaces and added to the slopspace, configure-unlock. From the
// next deploy on it is pulled afresh like the others, and the agent's prompt
// lists it. A repo the slopspace can't add is still unlocked, recorded as
// failed, and returned.
func (s *Server) slopAddReadOnlyRepo(stem, repo string) error {
	r := parseReadOnlyRepo(repo)
	if !pointerRepoPattern.MatchString(r.Repo) {
		return fmt.Errorf("read-only repo %q: expected owner/name or owner/name@ref", repo)
	}
	if r.Ref != "" && !readOnlyRefPattern.MatchString(r.Ref) {
		return fmt.Errorf("read-only repo %s: %q isn't a branch, tag or commit name", r.Repo, r.Ref)
	}
	return s.startConfigure(stem, func(spec *PointerSpec) (*RepoConfigure, error) {
		if why := spec.addableReadOnly(r.Repo); why != "" {
			return nil, fmt.Errorf("%s can't be added to %s: %s", r.Repo, spec.Name, why)
		}
		return &RepoConfigure{Kind: configureAdd, Repo: r.Repo, To: r.Ref}, nil
	})
}

// addableReadOnly reports why repo can't become one of the condoc's
// read-only repos, or "": the proposal form's rules (validateProposal).
func (p *PointerSpec) addableReadOnly(repo string) string {
	switch {
	case p.isReadOnly(repo):
		return "it is already read-only"
	case strings.EqualFold(repo, p.CondocRepo):
		return "it is the condoc repo"
	}
	for _, t := range p.Targets {
		if strings.EqualFold(repo, t) {
			return "it is a target"
		}
	}
	return ""
}

// startConfigure takes the claim, checks the condoc can be configured now,
// and commits configure-lock N.c for the configuration change asks for, then
// runs it to its unlock.
func (s *Server) startConfigure(stem string, change func(*PointerSpec) (*RepoConfigure, error)) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if why := spec.configurable(); why != "" {
		return fmt.Errorf("%s can't be configured now: %s", spec.Name, why)
	}
	c, err := change(&spec)
	if err != nil {
		return err
	}
	if spec.SlopspaceID != "" {
		meta, err := s.slop.ss.Get(spec.SlopspaceID)
		if err != nil {
			return err
		}
		if meta.Deployed {
			return fmt.Errorf("slopspace %s is deployed; configure %s once it is returned", spec.SlopspaceID, spec.Name)
		}
	}
	n := spec.Interactions
	c.Interaction, c.Seq, c.Stage = n, spec.nextConfigureSeq(n), configureLocked
	spec.Configure = c
	if err := s.writeSpec(&spec, subCommitMsg(&spec, "configure-lock", n, c.Seq)); err != nil {
		return err
	}
	return s.advanceConfigure(&spec)
}

// advanceConfigure runs the configuration in flight from its stage to its
// unlock, returning why it failed, if it did. The caller holds the claim.
func (s *Server) advanceConfigure(spec *PointerSpec) error {
	c := spec.Configure
	if c == nil {
		return nil
	}
	if c.Stage == configureLocked {
		c.SHA, c.Failed = "", ""
		if err := s.applyConfigure(spec, c); err != nil {
			c.Failed = err.Error()
		}
		c.Stage = configureApplied
		if err := s.writeSpec(spec, ""); err != nil {
			return err
		}
	}
	return s.unlockConfigure(spec)
}

// applyConfigure makes the slopspace's read-only repo follow c.To, exported
// afresh from its shared clone (brought up to date first, or made, for an
// add), and records the commit it resolved to. AddReadspaceRepo replaces a
// repo the slopspace has and appends one it hasn't, so an add is the same
// call. A proposal has no slopspace: nothing to do.
func (s *Server) applyConfigure(spec *PointerSpec, c *RepoConfigure) error {
	if spec.SlopspaceID == "" {
		return nil
	}
	if _, err := s.slop.rs.CloneRepo(c.Repo); err != nil {
		return fmt.Errorf("updating %s: %v", c.Repo, err)
	}
	owner, name, _ := strings.Cut(c.Repo, "/")
	r, err := s.slop.ss.AddReadspaceRepo(spec.SlopspaceID, owner, name, c.To, s.slop.rs.RepoPath(owner, name))
	if err != nil {
		return err
	}
	c.SHA = r.SHA
	return nil
}

// unlockConfigure is configure-unlock N.c: the pointer takes the new ref or
// repo (unless it failed), keeps the configuration and any new commit, and
// drops the lock. It returns the failure, once it is recorded.
func (s *Server) unlockConfigure(spec *PointerSpec) error {
	c := spec.Configure
	spec.takeConfigure(time.Now())
	if err := s.writeSpec(spec, subCommitMsg(spec, "configure-unlock", c.Interaction, c.Seq)); err != nil {
		return err
	}
	switch {
	case c.Failed == "":
		return nil
	case c.Kind == configureAdd:
		return fmt.Errorf("%s wasn't added: %s", c.Repo, c.Failed)
	}
	return fmt.Errorf("%s still follows %s: %s", c.Repo, refName(c.From), c.Failed)
}

// takeConfigure is the pointer side of configure-unlock: readOnly takes the
// new ref, or the added repo (unless it failed), a new commit joins
// ReadResolutions, the configuration joins Configurations, and the lock goes.
func (p *PointerSpec) takeConfigure(now time.Time) {
	c := p.Configure
	if c.Failed == "" {
		if r := p.readOnlyRepo(c.Repo); r != nil {
			r.Ref = c.To
		} else if c.Kind == configureAdd {
			p.ReadOnly = append(p.ReadOnly, ReadOnlyRepo{Repo: c.Repo, Ref: c.To})
		}
		if c.SHA != "" && p.lastReadSHA(c.Repo) != c.SHA {
			p.ReadResolutions = append(p.ReadResolutions, ReadResolution{
				Repo: c.Repo, Ref: c.To, SHA: c.SHA, Interaction: c.Interaction, At: readAtConfigure, Time: now.Unix(),
			})
		}
	}
	p.Configurations = append(p.Configurations, RepoConfiguration{
		Interaction: c.Interaction, Seq: c.Seq, Kind: c.Kind, Repo: c.Repo, From: c.From, To: c.To,
		SHA: c.SHA, Failed: c.Failed, At: now.UTC().Format(time.RFC3339),
	})
	p.Configure = nil
}

// tickConfigure finishes a configuration a restart left locked. It reports
// whether the condoc had one.
func (s *Server) tickConfigure(stem string, spec PointerSpec) bool {
	if spec.Configure == nil {
		return false
	}
	if !s.claim(stem) {
		return true
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil || spec.Configure == nil {
		return true
	}
	if err := s.advanceConfigure(&spec); err != nil {
		log.Printf("condocv2 %s: configure: %v", stem, err)
	}
	return true
}

// configurationLine is a configuration as the UI lists it.
func configurationLine(n, c int, kind, repo, from, to, failed string) string {
	line := fmt.Sprintf("%s %s: %s -> %s", sectionLabel(n, c), repo, refName(from), refName(to))
	if kind == configureAdd {
		line = fmt.Sprintf("%s %s: added, following %s", sectionLabel(n, c), repo, refName(to))
	}
	if failed != "" {
		line += " (failed: " + failed + ")"
	}
	return line
}

// refName is a ref as the UI says it.
func refName(ref string) string {
	if ref == "" {
		return "the default branch"
	}
	return ref
}
