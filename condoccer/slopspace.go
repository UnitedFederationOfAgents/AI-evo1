package main

// The slopspace control scheme's handler -- see docs/SlopspaceControlScheme.md.
//
// In the same-repo scheme federation-command is the handler and condoccer
// only writes directives. Here condoccer is the handler (S1): it proposes
// and accepts condocs (pointer files in the pointer repo), creates each
// condoc's slopspace through dungeon-keeper's packages (D1), watches the
// condoc files for the same directives the UI writes in the same-repo
// scheme, and runs agent turns one at a time from a FIFO across every condoc
// it holds (S2, D2): deploy the slopspace, submit a work signal, wait for the
// worker's completion file, return the slopspace, then commit and push every
// repo that changed and write the reply with each repo's commit link (D3,
// D6). It never talks to the worker process itself -- only work files in,
// completion files out (D13). Completing a condoc leads into propose-PR, PR
// and merge (D9, D10), using the shared pr-keeper module.

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	dkslop "dungeon-keeper/pkg/slopspace"
	dktypes "dungeon-keeper/pkg/types"
	"dungeon-keeper/pkg/worksignal"
	dkws "dungeon-keeper/pkg/writespace"
	prkeeper "pr-keeper"
)

const (
	slopPathPrefix = "slopspace:"

	slopTickInterval     = 2 * time.Second
	slopTurnPollInterval = 2 * time.Second
	slopPRPollInterval   = 30 * time.Second

	slopReplyFile      = "CONDOCCER_TURN_REPLY.txt" // in the slopspace root, which never deploys
	slopPRTemplateFile = "prs.yaml"
	slopDiffLimit      = 256 << 10
)

// Phases a slopspace condoc passes through beyond the same-repo ones (D9).
const (
	PhaseQueued     Phase = "queued"
	PhasePRProposed Phase = "pr_proposed"
	PhasePROpen     Phase = "pr_open"
	PhaseMerged     Phase = "merged"
)

// Turn kinds and the stages a turn moves through.
const (
	turnStep            = "step"
	turnRevision        = "revision"
	turnSubstep         = "substep"
	turnSubstepRevision = "substep_revision"
	turnProposePR       = "propose_pr"

	stageQueued    = "queued"
	stageDeployed  = "deployed"
	stageSignalled = "signalled"
	stageReturned  = "returned"
	stageReplied   = "replied"
)

// errTurnWait means the turn can't start yet because another slopspace is
// deployed at the worker's deploy path; it stays at the head of the queue.
var errTurnWait = errors.New("another slopspace is deployed")

// PRLive is a raised PR as last seen on GitHub.
type PRLive struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Status string `json:"status"` // see prkeeper.PullRequest.Status
	Ready  bool   `json:"ready"`
}

// slopState is the Server's slopspace bookkeeping.
type slopState struct {
	mu       sync.Mutex
	dk       *dktypes.Config
	ss       *dkslop.Manager
	ws       *dkws.Manager
	signals  *worksignal.Manager
	agent    string // defaults offered for new proposals
	model    string
	queue    []string // pointer stems with a turn waiting, FIFO
	running  string   // stem whose turn is running
	busy     map[string]bool
	wake     chan struct{}
	prLive   map[string][]PRLive
	prPolled map[string]time.Time
}

// dungeonKeeperConfig reads the same environment dungeon-keeper does, so
// condoccer and the worker agree on where slopspaces, work signals and the
// deploy path are (D13).
func dungeonKeeperConfig() *dktypes.Config {
	cfg := dktypes.DefaultConfig()
	if v := os.Getenv("SLOPSPACES_DIR"); v != "" {
		cfg.SlopspacesDir = v
	}
	if v := os.Getenv("WORK_SIGNALS_DIR"); v != "" {
		cfg.WorkSignalsDir = v
	}
	if v := os.Getenv("AGENT_SLOPSPACE_ROOT"); v != "" {
		cfg.AgentSlopspaceRoot = v
	}
	if v := os.Getenv("AGENT_RECORDS_PATH"); v != "" {
		cfg.AgentRecordsPath = v
	}
	cfg.AgentType = dktypes.AgentTypeWorker
	return cfg
}

func (s *Server) initSlopspace() {
	cfg := dungeonKeeperConfig()
	s.slop.dk = cfg
	s.slop.ss = dkslop.NewManager(cfg)
	s.slop.ws = dkws.NewManager(cfg)
	s.slop.signals = worksignal.NewManager(cfg)
	s.slop.agent = "claude"
	s.slop.busy = map[string]bool{}
	s.slop.wake = make(chan struct{}, 1)
	s.slop.prLive = map[string][]PRLive{}
	s.slop.prPolled = map[string]time.Time{}
}

// slopHolder identifies this condoccer in a pointer's holder field. Only the
// holder drives a condoc; the rest show it read-only.
func (s *Server) slopHolder() string {
	host, _ := os.Hostname()
	name := s.name
	if name == "" {
		name = "condoccer"
	}
	return name + "@" + host
}

func isSlopspacePath(rel string) bool {
	return strings.HasPrefix(rel, slopPathPrefix)
}

// claim marks a condoc as being worked on, so the driver tick, the turn
// runner and UI actions never edit the same condoc at once.
func (s *Server) claim(stem string) bool {
	s.slop.mu.Lock()
	defer s.slop.mu.Unlock()
	if s.slop.busy[stem] {
		return false
	}
	s.slop.busy[stem] = true
	return true
}

func (s *Server) release(stem string) {
	s.slop.mu.Lock()
	delete(s.slop.busy, stem)
	s.slop.mu.Unlock()
}

func errBusy(stem string) error {
	return fmt.Errorf("%s is busy -- try again in a moment", stem)
}

// ---- pointer files ----

func pointerSpecPath(repoPath, stem string) string {
	return filepath.Join(repoPath, pointerDir, stem+".yaml")
}

// pointerReadyPath returns the pointer repo's config and path when it is
// ready to use.
func (s *Server) pointerReadyPath() (PointerConfig, string, error) {
	st := s.pointerRepoStatus()
	if st.State != "ready" {
		return PointerConfig{}, "", fmt.Errorf("the pointer repo is %s", st.State)
	}
	return st.PointerConfig, st.ResolvedPath, nil
}

// readSpec reads a pointer file under the pointer op: mid-pull the checkout
// is detached for the rebase (so it reads as not ready) and the autostash
// hides uncommitted turn-stage writes, and a read in that window used to
// fail -- dropping the error setSpecError was recording -- or see a stale
// spec.
func (s *Server) readSpec(stem string) (PointerSpec, error) {
	if st := s.pointerRepoStatus(); st.State == "missing" || st.State == "cloning" {
		return PointerSpec{}, fmt.Errorf("the pointer repo is %s", st.State)
	}
	s.acquirePointerOp()
	b, err := func() ([]byte, error) {
		defer s.endPointerOp()
		_, path, err := s.pointerReadyPath()
		if err != nil {
			return nil, err
		}
		return os.ReadFile(pointerSpecPath(path, stem))
	}()
	if err != nil {
		return PointerSpec{}, err
	}
	spec, err := parsePointerSpec(string(b))
	if err != nil {
		return PointerSpec{}, fmt.Errorf("%s: %v", stem, err)
	}
	return spec, nil
}

// readHeldSpec reads a spec this condoccer holds.
func (s *Server) readHeldSpec(stem string) (PointerSpec, error) {
	spec, err := s.readSpec(stem)
	if err != nil {
		return spec, err
	}
	if spec.Holder != s.slopHolder() {
		return spec, fmt.Errorf("%s is held by %s, not this condoccer", spec.Name, spec.Holder)
	}
	return spec, nil
}

// acquirePointerOp waits for the pointer repo to be free of clones and
// pulls, then claims it.
func (s *Server) acquirePointerOp() (PointerConfig, string) {
	for {
		if cfg, path, ok := s.beginPointerOp(); ok {
			return cfg, path
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// writeSpec saves a pointer file. With a commit message it is also
// committed and pushed (on every phase change, D4); without one it is only
// written locally -- turn stages are recorded that way so a restart resumes
// them without a push per stage. A failed push is recorded and retried by
// the next pull, not returned: this condoccer's copy is authoritative.
func (s *Server) writeSpec(spec *PointerSpec, commitMsg string) error {
	spec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	cfg, path := s.acquirePointerOp()
	err := func() error {
		defer s.endPointerOp()
		rel := filepath.Join(pointerDir, spec.Stem()+".yaml")
		if err := os.MkdirAll(filepath.Join(path, pointerDir), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(path, rel), []byte(spec.render()), 0644); err != nil {
			return err
		}
		if commitMsg == "" {
			return nil
		}
		return s.commitPointerFile(cfg, path, rel, commitMsg)
	}()
	s.broadcastPointer()
	return err
}

// deleteSpec removes a pointer file and commits the removal.
func (s *Server) deleteSpec(stem, commitMsg string) error {
	cfg, path := s.acquirePointerOp()
	err := func() error {
		defer s.endPointerOp()
		rel := filepath.Join(pointerDir, stem+".yaml")
		if err := os.Remove(filepath.Join(path, rel)); err != nil {
			return err
		}
		return s.commitPointerFile(cfg, path, rel, commitMsg)
	}()
	s.broadcastPointer()
	return err
}

// pointerGit runs git in the pointer repo with the PAT available.
func pointerGit(path string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	cmd.Env = gitAuthEnv()
	return cmd.CombinedOutput()
}

// commitPointerFile commits one pointer file (only that file, so another
// condoccer's or a human's edits are never swept in) and pushes. The caller
// holds the pointer op.
func (s *Server) commitPointerFile(cfg PointerConfig, path, rel, msg string) error {
	if out, err := pointerGit(path, "add", "-A", "--", rel); err != nil {
		return fmt.Errorf("pointer add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if _, err := pointerGit(path, "diff", "--cached", "--quiet", "--", rel); err != nil {
		if out, err := pointerGit(path, "commit", "-q", "-m", msg, "--", rel); err != nil {
			return fmt.Errorf("pointer commit: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	s.pushPointerLocked(cfg, path)
	return nil
}

// pushPointerLocked pushes the pointer repo's local commits, rebasing onto
// whatever other condoccers pushed in between (each only writes the pointer
// files it holds, so the rebase doesn't conflict). The caller holds the
// pointer op. A failure is recorded for the status panel.
func (s *Server) pushPointerLocked(cfg PointerConfig, path string) {
	var lastErr string
	for attempt := 0; attempt < 3; attempt++ {
		out, err := pointerGit(path, "push", "-q", "origin", "HEAD:refs/heads/"+cfg.Branch)
		if err == nil {
			lastErr = ""
			break
		}
		lastErr = fmt.Sprintf("push: %v: %s", err, strings.TrimSpace(string(out)))
		if out, err := pointerGit(path, "pull", "-q", "--rebase", "--autostash", "origin", cfg.Branch); err != nil {
			lastErr += fmt.Sprintf("; pull: %v: %s", err, strings.TrimSpace(string(out)))
			break
		}
	}
	if lastErr != "" {
		log.Printf("pointer repo: %s", lastErr)
	}
	s.pointer.mu.Lock()
	s.pointer.pushErr = lastErr
	s.pointer.mu.Unlock()
}

// ---- where a condoc's files are (D7) ----

// slopCondocRoot is the condoc repo's working tree: in the slopspace while
// idle, at the deploy path while a turn has it deployed.
func slopCondocRoot(meta *dktypes.SlopspaceMetadata, condocRepo string) string {
	base := meta.RootPath
	if meta.Deployed && meta.DeployPath != "" {
		base = meta.DeployPath
	}
	return filepath.Join(base, dkslop.WriteSpacesDir, dkslop.ReposDir, filepath.FromSlash(condocRepo))
}

// resolveCondoc maps a condoc path from the UI to the root its CondocInfo
// paths are relative to, and its main file: same-repo paths sit under the
// working dir, "slopspace:<stem>" ones in their slopspace's condoc repo,
// wherever that is right now.
func (s *Server) resolveCondoc(rel string) (root, abs string, err error) {
	if !isSlopspacePath(rel) {
		if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			return "", "", fmt.Errorf("invalid condoc path")
		}
		root = s.getRoot()
		return root, filepath.Join(root, filepath.FromSlash(rel)), nil
	}
	stem := strings.TrimPrefix(rel, slopPathPrefix)
	if stem == "" || strings.ContainsAny(stem, `/\`) || strings.Contains(stem, "..") {
		return "", "", fmt.Errorf("invalid condoc path")
	}
	spec, err := s.readSpec(stem)
	if err != nil {
		return "", "", err
	}
	if spec.SlopspaceID == "" {
		return "", "", fmt.Errorf("%s has no slopspace (phase %s)", spec.Name, spec.Phase)
	}
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return "", "", err
	}
	root = slopCondocRoot(meta, spec.CondocRepo)
	return root, filepath.Join(root, filepath.FromSlash(spec.CondocPath)), nil
}

// slopBeginEdit claims an accepted, idle slopspace condoc this condoccer
// holds and returns where its files are, for an edit from the UI.
func (s *Server) slopBeginEdit(stem string) (spec PointerSpec, root, main string, release func(), err error) {
	if !s.claim(stem) {
		return spec, "", "", nil, errBusy(stem)
	}
	fail := func(e error) (PointerSpec, string, string, func(), error) {
		s.release(stem)
		return spec, "", "", nil, e
	}
	spec, err = s.readHeldSpec(stem)
	if err != nil {
		return fail(err)
	}
	if spec.Turn != nil {
		return fail(fmt.Errorf("%s has a turn in flight", spec.Name))
	}
	if spec.SlopspaceID == "" {
		return fail(fmt.Errorf("%s hasn't been accepted yet", spec.Name))
	}
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return fail(err)
	}
	if meta.Deployed {
		return fail(fmt.Errorf("%s's slopspace is deployed", spec.Name))
	}
	root = slopCondocRoot(meta, spec.CondocRepo)
	return spec, root, filepath.Join(root, filepath.FromSlash(spec.CondocPath)), func() { s.release(stem) }, nil
}

// slopPhaseOverrides are pointer phases the condoc file can't express.
func slopPhaseOverrides(phase string) bool {
	switch Phase(phase) {
	case PhaseQueued, PhaseAgentRunning, PhasePRProposed, PhasePROpen, PhaseMerged:
		return true
	}
	return false
}

// slopCondocState is getCondocState for a slopspace condoc, read from the
// expected location. A deploy or return moves the files, so a read that
// lands mid-move is retried briefly.
func (s *Server) slopCondocState(rel string) (CondocState, error) {
	stem := strings.TrimPrefix(rel, slopPathPrefix)
	var st CondocState
	var err error
	for attempt := 0; ; attempt++ {
		var root, abs string
		if root, abs, err = s.resolveCondoc(rel); err == nil {
			if st, err = getCondocState(root, abs); err == nil {
				break
			}
		}
		if attempt >= 4 {
			return CondocState{}, err
		}
		time.Sleep(250 * time.Millisecond)
	}
	spec, err := s.readSpec(stem)
	if err != nil {
		return CondocState{}, err
	}
	st.Info.Path = rel
	st.Info.Name = spec.Name
	if slopPhaseOverrides(spec.Phase) {
		st.Info.Phase = Phase(spec.Phase)
	}
	entry := s.decoratePointerEntry(entryFromSpec(spec, pointerDir+"/"+stem+".yaml"))
	st.Slopspace = &entry
	return st, nil
}

// ---- listing ----

func entryFromSpec(spec PointerSpec, file string) PointerEntry {
	stem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	e := PointerEntry{
		File:        file,
		Stem:        stem,
		Path:        slopPathPrefix + stem,
		Name:        spec.Name,
		Phase:       spec.Phase,
		Step:        spec.Step,
		Substep:     spec.Substep,
		Iteration:   spec.Iteration,
		Branch:      spec.Branch,
		CondocRepo:  spec.CondocRepo,
		Targets:     spec.Targets,
		CondocPath:  spec.CondocPath,
		CondocURL:   spec.CondocURL,
		Description: spec.Description,
		Agent:       spec.Agent,
		Model:       spec.Model,
		StartTime:   spec.StartTime,
		SlopspaceID: spec.SlopspaceID,
		Holder:      spec.Holder,
		Error:       spec.Error,
		PRTemplate:  spec.PRTemplate,
	}
	if spec.Turn != nil {
		e.TurnKind = spec.Turn.Kind
		e.TurnStage = spec.Turn.Stage
	}
	for _, pr := range spec.PRs {
		e.PRs = append(e.PRs, PRLive{Repo: pr.Repo, Number: pr.Number, URL: pr.URL, State: pr.State, Status: pr.State})
	}
	return e
}

// decoratePointerEntry adds what only this condoccer knows: whether it holds
// the condoc, its queue position, whether the slopspace is deployed, and the
// PRs' live mergeability.
func (s *Server) decoratePointerEntry(e PointerEntry) PointerEntry {
	e.HeldHere = e.Holder != "" && e.Holder == s.slopHolder()
	if s.slop.ss == nil {
		return e
	}
	s.slop.mu.Lock()
	for i, q := range s.slop.queue {
		if q == e.Stem {
			e.QueuePos = i + 1
		}
	}
	e.Running = s.slop.running == e.Stem
	if live, ok := s.slop.prLive[e.Stem]; ok && len(live) > 0 {
		e.PRs = live
	}
	s.slop.mu.Unlock()
	if e.SlopspaceID != "" {
		if meta, err := s.slop.ss.Get(e.SlopspaceID); err == nil {
			e.Deployed = meta.Deployed
		}
	}
	return e
}

// ---- proposal and accept ----

// SlopspaceProposal is the proposal form (D4/D5): new when Stem is empty,
// an edit of a still-proposed condoc otherwise.
type SlopspaceProposal struct {
	Stem        string   `json:"stem,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Targets     []string `json:"targets"`
	CondocRepo  string   `json:"condocRepo"`
	Branch      string   `json:"branch"`
	Agent       string   `json:"agent"`
	Model       string   `json:"model"`
}

func normalizeRepo(r string) string {
	r = strings.TrimSpace(r)
	if n := normalizeGitHubRemote(r); n != "" {
		return n
	}
	return r
}

// validateProposal fills spec's static fields from p.
func validateProposal(p SlopspaceProposal, spec *PointerSpec) error {
	name := strings.TrimSpace(p.Name)
	if !condocNamePattern.MatchString(name) {
		return fmt.Errorf("name %q: use letters, digits, - and _, starting with a letter", name)
	}
	if spec.Name != "" && spec.Name != name {
		return fmt.Errorf("a proposal's name can't change once proposed")
	}
	var targets []string
	seen := map[string]bool{}
	for _, t := range p.Targets {
		t = normalizeRepo(t)
		if t == "" {
			continue
		}
		if !pointerRepoPattern.MatchString(t) {
			return fmt.Errorf("target %q: expected owner/name", t)
		}
		if seen[strings.ToLower(t)] {
			return fmt.Errorf("target %s is listed twice", t)
		}
		seen[strings.ToLower(t)] = true
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		return fmt.Errorf("add at least one target repo")
	}
	condocRepo := normalizeRepo(p.CondocRepo)
	if !pointerRepoPattern.MatchString(condocRepo) {
		return fmt.Errorf("condoc repo %q: expected owner/name", condocRepo)
	}
	if seen[strings.ToLower(condocRepo)] {
		return fmt.Errorf("the condoc repo can't also be a target (D5)")
	}
	branch := strings.TrimSpace(p.Branch)
	if branch == "" {
		branch = fmt.Sprintf("condoc/%s-%d/main", name, spec.StartTime)
	}
	if out, err := exec.Command("git", "check-ref-format", "--branch", branch).CombinedOutput(); err != nil {
		return fmt.Errorf("branch %q: %s", branch, strings.TrimSpace(string(out)))
	}
	spec.Name = name
	spec.Description = strings.TrimSpace(p.Description)
	spec.Targets = targets
	spec.CondocRepo = condocRepo
	spec.Branch = branch
	spec.Agent = strings.TrimSpace(p.Agent)
	spec.Model = strings.TrimSpace(p.Model)
	spec.ControlScheme = "slopspace"
	spec.CondocPath = "condocs/" + name + ".md"
	spec.CondocURL = fmt.Sprintf("https://github.com/%s/blob/%s/%s", condocRepo, branch, spec.CondocPath)
	return nil
}

// slopPropose writes a new proposal, or edits one that is still proposed.
func (s *Server) slopPropose(p SlopspaceProposal) error {
	if _, _, err := s.pointerReadyPath(); err != nil {
		return err
	}
	if p.Stem == "" {
		spec := PointerSpec{StartTime: time.Now().Unix(), Phase: string(PhaseProposed), Holder: s.slopHolder()}
		if err := validateProposal(p, &spec); err != nil {
			return err
		}
		if spec.Agent == "" {
			spec.Agent = s.slop.agent
		}
		if _, err := s.readSpec(spec.Stem()); err == nil {
			return fmt.Errorf("%s already exists", spec.Stem())
		}
		return s.writeSpec(&spec, fmt.Sprintf("condoc: propose %s", spec.Name))
	}

	if !s.claim(p.Stem) {
		return errBusy(p.Stem)
	}
	defer s.release(p.Stem)
	spec, err := s.readHeldSpec(p.Stem)
	if err != nil {
		return err
	}
	if spec.Phase != string(PhaseProposed) {
		return fmt.Errorf("%s was already accepted", spec.Name)
	}
	if err := validateProposal(p, &spec); err != nil {
		return err
	}
	return s.writeSpec(&spec, fmt.Sprintf("condoc: update proposal %s", spec.Name))
}

func (s *Server) slopDeleteProposal(stem string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != string(PhaseProposed) {
		return fmt.Errorf("only a proposal can be withdrawn; %s is %s", spec.Name, spec.Phase)
	}
	return s.deleteSpec(stem, fmt.Sprintf("condoc: withdraw proposal %s", spec.Name))
}

// BranchCheck is one repo's answer to "does the branch exist yet" (D5).
type BranchCheck struct {
	Repo   string `json:"repo"`
	Exists bool   `json:"exists"`
	Error  string `json:"error,omitempty"`
}

// slopCheckBranch asks every repo's remote whether branch exists.
func slopCheckBranch(repos []string, branch string) []BranchCheck {
	out := make([]BranchCheck, len(repos))
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Add(1)
		go func(i int, repo string) {
			defer wg.Done()
			out[i].Repo = repo
			exists, err := remoteBranchExists(repo, branch)
			out[i].Exists = exists
			if err != nil {
				out[i].Error = err.Error()
			}
		}(i, repo)
	}
	wg.Wait()
	return out
}

// remoteBranchExists is a var so tests can stand in for GitHub.
var remoteBranchExists = func(repo, branch string) (bool, error) {
	cmd := exec.Command("git", "ls-remote", "--exit-code", "--heads", pointerRepoURL(repo), "refs/heads/"+branch)
	cmd.Env = gitAuthEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 2 {
		return false, nil
	}
	return false, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
}

// slopAccept turns a proposal into a running condoc: checks the branch is
// free everywhere, creates the slopspace with a writespace per repo on the
// branch, writes the condoc with step 1 templated into the condoc repo, and
// publishes the branch on every repo.
func (s *Server) slopAccept(stem string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != string(PhaseProposed) {
		return fmt.Errorf("%s was already accepted", spec.Name)
	}

	var taken []string
	for _, c := range slopCheckBranch(spec.Repos(), spec.Branch) {
		switch {
		case c.Error != "":
			return fmt.Errorf("checking %s for %s: %s", c.Repo, spec.Branch, c.Error)
		case c.Exists:
			taken = append(taken, c.Repo)
		}
	}
	if len(taken) > 0 {
		return fmt.Errorf("branch %s already exists in %s", spec.Branch, strings.Join(taken, ", "))
	}

	for _, repo := range spec.Repos() {
		if _, err := s.slop.ws.CloneRepo(repo); err != nil {
			return fmt.Errorf("writespace %s: %v", repo, err)
		}
	}
	meta, err := s.slop.ss.Create()
	if err != nil {
		return err
	}
	undo := func(e error) error {
		if derr := s.slop.ss.Delete(meta.ID); derr != nil {
			log.Printf("slopspace %s: cleaning up after a failed accept: %v", meta.ID, derr)
		}
		return e
	}
	for _, repo := range spec.Repos() {
		owner, name, _ := strings.Cut(repo, "/")
		if err := s.slop.ss.AddWritespaceRepo(meta.ID, owner, name, spec.Branch, s.slop.ws.RepoPath(owner, name)); err != nil {
			return undo(fmt.Errorf("add %s to the slopspace: %v", repo, err))
		}
	}

	root := slopCondocRoot(meta, spec.CondocRepo)
	main := filepath.Join(root, filepath.FromSlash(spec.CondocPath))
	if err := os.MkdirAll(filepath.Dir(main), 0755); err != nil {
		return undo(err)
	}
	pointerCfg, _, _ := s.pointerReadyPath()
	if err := writeFileString(main, slopspaceInitialMainFile(spec.Name, spec.StartTime, spec.Branch, pointerCfg.Repo, spec.Description)); err != nil {
		return undo(err)
	}
	owner, name, _ := strings.Cut(spec.CondocRepo, "/")
	if _, err := s.slop.ss.WriteRepoChanges(meta.ID, owner, name, "condoc: accept proposal, template step 1"); err != nil {
		return undo(err)
	}
	for _, repo := range spec.Targets {
		owner, name, _ := strings.Cut(repo, "/")
		if err := s.slop.ss.PushRepo(meta.ID, owner, name); err != nil {
			return undo(err)
		}
	}

	spec.SlopspaceID = meta.ID
	spec.Phase = string(PhaseAwaitingStep)
	spec.Step = "1"
	spec.Error = ""
	return s.writeSpec(&spec, fmt.Sprintf("condoc: accept %s", spec.Name))
}

// ---- driving: the directives in the condoc files ----

// slopLoop drives every slopspace condoc this condoccer holds.
func (s *Server) slopLoop() {
	t := time.NewTicker(slopTickInterval)
	defer t.Stop()
	for range t.C {
		s.slopTick()
	}
}

type pointerFileSpec struct {
	stem string
	spec PointerSpec
}

// readHeldSpecs lists the specs this condoccer holds, read under the pointer
// op for the same reason as readSpec.
func (s *Server) readHeldSpecs() []pointerFileSpec {
	if st := s.pointerRepoStatus(); st.State == "missing" || st.State == "cloning" {
		return nil
	}
	s.acquirePointerOp()
	defer s.endPointerOp()
	_, path, err := s.pointerReadyPath()
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(path, pointerDir, "*.yaml"))
	sort.Strings(files)
	me := s.slopHolder()
	var out []pointerFileSpec
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		spec, err := parsePointerSpec(string(b))
		if err != nil || spec.Holder != me || spec.ControlScheme != "slopspace" {
			continue
		}
		out = append(out, pointerFileSpec{stem: strings.TrimSuffix(filepath.Base(f), ".yaml"), spec: spec})
	}
	return out
}

func (s *Server) slopTick() {
	for _, ps := range s.readHeldSpecs() {
		stem, spec := ps.stem, ps.spec
		if spec.Turn != nil {
			if spec.Error == "" {
				s.enqueueTurn(stem) // also resumes turns after a restart
			}
			continue
		}
		if spec.Error != "" {
			continue // waits for Resubmit
		}
		if spec.Phase == string(PhasePROpen) {
			s.slop.mu.Lock()
			due := time.Since(s.slop.prPolled[stem]) >= slopPRPollInterval
			s.slop.mu.Unlock()
			if due {
				go s.pollPRs(stem)
			}
			continue
		}
		if !s.claim(stem) {
			continue
		}
		if err := s.driveSpec(stem, &spec); err != nil {
			log.Printf("slopspace %s: %v", stem, err)
			s.setSpecError(stem, err)
		}
		s.release(stem)
	}
}

// setSpecError records err on the pointer; the condoc then waits for
// Resubmit. The caller holds the claim.
func (s *Server) setSpecError(stem string, err error) {
	spec, rerr := s.readSpec(stem)
	if rerr != nil {
		log.Printf("slopspace %s: recording error %q: %v", stem, err, rerr)
		return
	}
	spec.Error = err.Error()
	if werr := s.writeSpec(&spec, fmt.Sprintf("condoc: %s error", spec.Name)); werr != nil {
		log.Printf("slopspace %s: %v", stem, werr)
	}
}

// slopResubmit clears a condoc's error so the driver (or the turn queue)
// tries again from where it stopped.
func (s *Server) slopResubmit(stem string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Error == "" {
		return nil
	}
	spec.Error = ""
	return s.writeSpec(&spec, fmt.Sprintf("condoc: %s resubmitted", spec.Name))
}

// driveSpec reacts to a directive in an idle condoc's files, the way
// federation-command's handleCondocTick does for same-repo condocs, and
// keeps the pointer's state in step with the files. The caller holds the
// claim.
func (s *Server) driveSpec(stem string, spec *PointerSpec) error {
	switch Phase(spec.Phase) {
	case PhaseProposed, PhasePRProposed, PhasePROpen, PhaseMerged, PhaseQueued:
		return nil
	}
	if spec.SlopspaceID == "" {
		return nil
	}
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return err
	}
	if meta.Deployed {
		return nil
	}
	root := slopCondocRoot(meta, spec.CondocRepo)
	main := filepath.Join(root, filepath.FromSlash(spec.CondocPath))
	info, err := detectPhase(root, main)
	if err != nil {
		return err
	}

	switch info.Phase {
	// A filled-in step template reads as proposed (its step file doesn't
	// exist yet) until the handoff below starts it.
	case PhaseAwaitingStep, PhaseProposed:
		content, err := readFileString(main)
		if err != nil {
			return err
		}
		switch {
		case completedDirectiveRe.MatchString(content):
			return s.slopCompleteCondoc(spec, meta, main)
		case handoffDirectiveRe.MatchString(content):
			return s.slopStartStep(spec, meta, main, content)
		}
		if info.Phase == PhaseProposed {
			return nil // mid-edit; never a pointer phase once accepted
		}

	// detectPhase reads a step or substep file holding a directive as
	// agent_running (in the same-repo scheme the handler is about to act),
	// so the directives are looked for in both phases.
	case PhaseAwaitingAction, PhaseAgentRunning:
		active := filepath.Join(root, info.StepFile)
		if info.SubstepFile != "" {
			active = filepath.Join(root, info.SubstepFile)
		}
		content, err := readFileString(active)
		if err != nil {
			return err
		}
		switch {
		case cfRevertDirectiveRe.MatchString(content):
			stripped := cfRevertDirectiveRe.ReplaceAllString(content, "")
			if err := writeFileString(active, strings.TrimRight(stripped, " \t\n")+"\n"); err != nil {
				return err
			}
			return fmt.Errorf("revert isn't available for slopspace condocs yet (D8); the directive was removed")
		case completedDirectiveRe.MatchString(content):
			if info.SubstepFile != "" {
				return s.slopCompleteSubstep(spec, meta, root, info)
			}
			return s.slopCompleteStep(spec, meta, main, active, info.StepNum)
		case handoffDirectiveRe.MatchString(content):
			if pendingRetryLetter(content) != "" {
				stripped := handoffDirectiveRe.ReplaceAllString(content, "")
				if err := writeFileString(active, strings.TrimRight(stripped, " \t\n")+"\n"); err != nil {
					return err
				}
				return fmt.Errorf("retry isn't available for slopspace condocs yet (D8); replace the Retry heading with a Revision")
			}
			if info.SubstepFile == "" {
				if letter, _ := pendingSubstep(content, main, info.StepNum); letter != "" {
					return s.slopStartSubstep(spec, meta, main, active, info.StepNum, letter)
				}
			}
			return s.slopStartRevision(spec, meta, active, content, info)
		}
		if info.Phase == PhaseAgentRunning {
			return s.slopRecoverTurn(stem, info, content)
		}

	case PhaseCompleted:
		if spec.Phase != string(PhaseCompleted) {
			break // sync below; the propose-PR turn is queued next tick
		}
		spec.Turn = &PointerTurn{Kind: turnProposePR, Stage: stageQueued}
		spec.Phase = string(PhaseQueued)
		if err := s.writeSpec(spec, fmt.Sprintf("condoc: %s propose PRs queued", spec.Name)); err != nil {
			return err
		}
		s.enqueueTurn(stem)
		return nil
	}

	return s.syncSpecState(spec, info, root)
}

// slopRecoverTurn picks back up a turn whose directive was consumed but
// which never made it onto the pointer -- the start* handlers strip the
// Human-Prompt and directive, then commit the condoc repo, then queue the
// turn, so a failed commit or push in between left the active file reading
// agent_running with no directive and no turn, and nothing ever ran it. The
// pending turn is read back from the file: a Revision with no Reply, or a
// step/substep with no Reply at all. The caller holds the claim.
func (s *Server) slopRecoverTurn(stem string, info CondocInfo, content string) error {
	// The caller's spec was read before the claim; only a fresh read can
	// rule out a turn the queue has just recorded.
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Turn != nil || spec.Error != "" {
		return nil
	}
	turn := &PointerTurn{Stage: stageQueued}
	var msg string
	switch letter := pendingRevisionLetter(content); {
	case letter != "" && info.SubstepFile != "":
		turn.Kind, turn.Letter = turnSubstepRevision, letter
		msg = fmt.Sprintf("condoc: step %d substep %s revision %s prompt", info.StepNum, info.SubstepLetter, letter)
	case letter != "":
		turn.Kind, turn.Letter = turnRevision, letter
		msg = fmt.Sprintf("condoc: step %d revision %s prompt", info.StepNum, letter)
	case cfReplyHeadingRe.MatchString(content):
		return fmt.Errorf("step %d reads as running but has no turn and nothing pending to run -- add a Revision with the !HANDOFF! directive to continue", info.StepNum)
	case info.SubstepFile != "":
		turn.Kind = turnSubstep
		msg = fmt.Sprintf("condoc: step %d substep %s started", info.StepNum, info.SubstepLetter)
	default:
		turn.Kind = turnStep
		msg = fmt.Sprintf("condoc: step %d started", info.StepNum)
	}
	log.Printf("slopspace %s: recovering a stranded %s turn", stem, turn.Kind)
	if err := s.commitCondocRepo(&spec, msg); err != nil {
		return err
	}
	return s.queueTurn(&spec, turn, info.StepNum, info.SubstepLetter)
}

// syncSpecState mirrors the condoc files' phase and position into the
// pointer, committing only when something changed.
func (s *Server) syncSpecState(spec *PointerSpec, info CondocInfo, root string) error {
	phase := string(info.Phase)
	step := ""
	if info.StepNum > 0 {
		step = fmt.Sprint(info.StepNum)
	}
	iteration := spec.Iteration
	active := info.SubstepFile
	if active == "" {
		active = info.StepFile
	}
	if active != "" {
		if content, err := readFileString(filepath.Join(root, active)); err == nil {
			iteration = lastReplyLetter(content)
		}
	}
	if spec.Phase == phase && spec.Step == step && spec.Substep == info.SubstepLetter && spec.Iteration == iteration {
		return nil
	}
	spec.Phase, spec.Step, spec.Substep, spec.Iteration = phase, step, info.SubstepLetter, iteration
	return s.writeSpec(spec, fmt.Sprintf("condoc: %s %s", spec.Name, strings.ReplaceAll(phase, "_", " ")))
}

// commitCondocRepo commits and pushes the condoc repo's writespace.
func (s *Server) commitCondocRepo(spec *PointerSpec, msg string) error {
	owner, name, _ := strings.Cut(spec.CondocRepo, "/")
	_, err := s.slop.ss.WriteRepoChanges(spec.SlopspaceID, owner, name, msg)
	return err
}

// queueTurn records a turn on the pointer, commits it, and queues it.
func (s *Server) queueTurn(spec *PointerSpec, turn *PointerTurn, step int, substep string) error {
	spec.Turn = turn
	spec.Phase = string(PhaseQueued)
	spec.Step = fmt.Sprint(step)
	spec.Substep = substep
	if err := s.writeSpec(spec, fmt.Sprintf("condoc: %s %s queued", spec.Name, turnLabel(spec, turn))); err != nil {
		return err
	}
	s.enqueueTurn(spec.Stem())
	return nil
}

func (s *Server) slopStartStep(spec *PointerSpec, meta *dktypes.SlopspaceMetadata, main, content string) error {
	step, ok := parseLastStep(content)
	if !ok {
		return fmt.Errorf("could not parse the step from the main file -- fill in the title and prompt first")
	}
	if err := writeStepFile(stepFilePath(main, step.num), main, step.prompt); err != nil {
		return err
	}
	if err := updateMainAfterStepStart(main, step.num); err != nil {
		return err
	}
	if err := s.commitCondocRepo(spec, fmt.Sprintf("condoc: step %d started", step.num)); err != nil {
		return err
	}
	return s.queueTurn(spec, &PointerTurn{Kind: turnStep, Stage: stageQueued}, step.num, "")
}

func (s *Server) slopStartRevision(spec *PointerSpec, meta *dktypes.SlopspaceMetadata, active, content string, info CondocInfo) error {
	letter := pendingRevisionLetter(content)
	if letter == "" {
		// A !HANDOFF! with nothing to run: drop it, as the same-repo
		// handler effectively does.
		return writeFileString(active, strings.TrimRight(handoffDirectiveRe.ReplaceAllString(content, ""), " \t\n")+"\n")
	}
	if err := writeFileString(active, removeHumanPromptSection(content)); err != nil {
		return err
	}
	kind := turnRevision
	msg := fmt.Sprintf("condoc: step %d revision %s prompt", info.StepNum, letter)
	if info.SubstepFile != "" {
		kind = turnSubstepRevision
		msg = fmt.Sprintf("condoc: step %d substep %s revision %s prompt", info.StepNum, info.SubstepLetter, letter)
	}
	if err := s.commitCondocRepo(spec, msg); err != nil {
		return err
	}
	return s.queueTurn(spec, &PointerTurn{Kind: kind, Letter: letter, Stage: stageQueued}, info.StepNum, info.SubstepLetter)
}

func (s *Server) slopStartSubstep(spec *PointerSpec, meta *dktypes.SlopspaceMetadata, main, stepPath string, stepNum int, letter string) error {
	if _, err := startSubstepFiles(stepPath, main, stepNum, letter); err != nil {
		return err
	}
	if err := s.commitCondocRepo(spec, fmt.Sprintf("condoc: step %d substep %s started", stepNum, letter)); err != nil {
		return err
	}
	return s.queueTurn(spec, &PointerTurn{Kind: turnSubstep, Stage: stageQueued}, stepNum, letter)
}

func (s *Server) slopCompleteSubstep(spec *PointerSpec, meta *dktypes.SlopspaceMetadata, root string, info CondocInfo) error {
	if err := finalizeIterFile(filepath.Join(root, info.SubstepFile), "Substep Completed", "substep"); err != nil {
		return err
	}
	if err := resumeStepAfterSubstep(filepath.Join(root, info.StepFile)); err != nil {
		return err
	}
	if err := s.commitCondocRepo(spec, fmt.Sprintf("condoc: step %d substep %s completed", info.StepNum, info.SubstepLetter)); err != nil {
		return err
	}
	return s.resync(spec, meta)
}

func (s *Server) slopCompleteStep(spec *PointerSpec, meta *dktypes.SlopspaceMetadata, main, stepPath string, stepNum int) error {
	if err := finalizeIterFile(stepPath, "Step Completed", "step"); err != nil {
		return err
	}
	if err := templateNextStep(main, stepNum+1); err != nil {
		return err
	}
	if err := s.commitCondocRepo(spec, fmt.Sprintf("condoc: step %d completed, template step %d", stepNum, stepNum+1)); err != nil {
		return err
	}
	return s.resync(spec, meta)
}

// slopCompleteCondoc finishes the condoc; in this scheme that leads into
// propose-PR rather than ending it (D9).
func (s *Server) slopCompleteCondoc(spec *PointerSpec, meta *dktypes.SlopspaceMetadata, main string) error {
	if err := finalizeMainFile(main); err != nil {
		return err
	}
	if err := s.commitCondocRepo(spec, "condoc: completed"); err != nil {
		return err
	}
	return s.resync(spec, meta)
}

// resync re-reads the condoc's files after an edit and mirrors them into
// the pointer.
func (s *Server) resync(spec *PointerSpec, meta *dktypes.SlopspaceMetadata) error {
	root := slopCondocRoot(meta, spec.CondocRepo)
	info, err := detectPhase(root, filepath.Join(root, filepath.FromSlash(spec.CondocPath)))
	if err != nil {
		return err
	}
	return s.syncSpecState(spec, info, root)
}

// slopPerformAction applies a UI action to a slopspace condoc. Most actions
// are the same file edits as for a same-repo condoc -- the driver then
// reacts to the directive they leave -- so they share applyAction.
func (s *Server) slopPerformAction(action ActionRequest) error {
	stem := strings.TrimPrefix(action.Path, slopPathPrefix)
	switch action.Action {
	case "resubmit":
		return s.slopResubmit(stem)
	case "retry", "revert":
		return fmt.Errorf("%s isn't supported for slopspace condocs (D8)", action.Action)
	}
	_, root, main, release, err := s.slopBeginEdit(stem)
	if err != nil {
		return err
	}
	defer release()
	info, err := detectPhase(root, main)
	if err != nil {
		return err
	}
	return s.applyAction(root, main, info, action, false)
}

// ---- the turn queue (D2) and turns (D3) ----

func (s *Server) enqueueTurn(stem string) {
	s.slop.mu.Lock()
	queued := s.slop.running == stem
	for _, q := range s.slop.queue {
		queued = queued || q == stem
	}
	if !queued {
		s.slop.queue = append(s.slop.queue, stem)
	}
	s.slop.mu.Unlock()
	if !queued {
		select {
		case s.slop.wake <- struct{}{}:
		default:
		}
		s.broadcastPointer()
	}
}

func (s *Server) dequeue(stem string) {
	s.slop.mu.Lock()
	for i, q := range s.slop.queue {
		if q == stem {
			s.slop.queue = append(s.slop.queue[:i], s.slop.queue[i+1:]...)
			break
		}
	}
	s.slop.mu.Unlock()
}

func (s *Server) setRunning(stem string) {
	s.slop.mu.Lock()
	s.slop.running = stem
	s.slop.mu.Unlock()
	s.broadcastPointer()
}

// slopTurnLoop runs queued turns one at a time, in order, whichever condoc
// they belong to.
func (s *Server) slopTurnLoop() {
	for {
		s.slop.mu.Lock()
		stem := ""
		if len(s.slop.queue) > 0 {
			stem = s.slop.queue[0]
		}
		s.slop.mu.Unlock()
		if stem == "" {
			select {
			case <-s.slop.wake:
			case <-time.After(5 * time.Second):
			}
			continue
		}
		if !s.claim(stem) {
			time.Sleep(time.Second)
			continue
		}
		s.setRunning(stem)
		err := s.runTurn(stem)
		if errors.Is(err, errTurnWait) {
			s.setRunning("")
			s.release(stem)
			time.Sleep(5 * time.Second)
			continue
		}
		s.dequeue(stem)
		if err != nil {
			log.Printf("slopspace %s: turn: %v", stem, err)
			s.setSpecError(stem, err)
		}
		s.setRunning("")
		s.release(stem)
	}
}

func turnLabel(spec *PointerSpec, t *PointerTurn) string {
	switch t.Kind {
	case turnStep:
		return fmt.Sprintf("step %s", spec.Step)
	case turnRevision:
		return fmt.Sprintf("step %s revision %s", spec.Step, t.Letter)
	case turnSubstep:
		return fmt.Sprintf("step %s substep %s", spec.Step, spec.Substep)
	case turnSubstepRevision:
		return fmt.Sprintf("step %s substep %s revision %s", spec.Step, spec.Substep, t.Letter)
	case turnProposePR:
		return "propose PRs"
	}
	return t.Kind
}

// turnReplyMessage is the commit message for a turn's reply, as
// federation-command words it.
func turnReplyMessage(spec *PointerSpec, t *PointerTurn) string {
	switch t.Kind {
	case turnRevision:
		return fmt.Sprintf("condoc: step %s revision %s reply", spec.Step, t.Letter)
	case turnSubstep:
		return fmt.Sprintf("condoc: step %s substep %s initial reply", spec.Step, spec.Substep)
	case turnSubstepRevision:
		return fmt.Sprintf("condoc: step %s substep %s revision %s reply", spec.Step, spec.Substep, t.Letter)
	}
	return fmt.Sprintf("condoc: step %s initial reply", spec.Step)
}

// runTurn advances the condoc's turn stage by stage, saving after each so a
// failure or restart resumes from the stage it stopped at. The caller holds
// the claim.
func (s *Server) runTurn(stem string) error {
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Turn == nil {
		return nil
	}
	if spec.Turn.Kind == turnProposePR {
		return s.runProposeTurn(&spec)
	}
	t := spec.Turn
	id := spec.SlopspaceID
	for {
		meta, err := s.slop.ss.Get(id)
		if err != nil {
			return err
		}
		switch t.Stage {
		case "", stageQueued:
			deployed, err := s.slop.ss.GetDeployedID(dktypes.AgentTypeWorker)
			if err != nil {
				return err
			}
			if deployed != "" && deployed != id {
				return errTurnWait
			}
			if len(t.Bases) == 0 {
				for _, repo := range spec.Repos() {
					owner, name, _ := strings.Cut(repo, "/")
					sha, err := s.slop.ss.RepoHead(id, owner, name)
					if err != nil {
						return fmt.Errorf("%s: %v", repo, err)
					}
					t.Bases = append(t.Bases, repo+"@"+sha)
				}
				if base := s.pointerBase(&spec); base != "" {
					t.Bases = append(t.Bases, base)
				}
				if err := resetAgentResources(slopAgentResourcesDir(meta)); err != nil {
					return err
				}
			}
			if !meta.Deployed {
				if err := s.slop.ss.Deploy(id, dktypes.AgentTypeWorker); err != nil {
					return err
				}
			}
			t.Stage = stageDeployed
			spec.Phase = string(PhaseAgentRunning)
			if err := s.writeSpec(&spec, fmt.Sprintf("condoc: %s %s running", spec.Name, turnLabel(&spec, t))); err != nil {
				return err
			}

		case stageDeployed:
			prompt, err := s.turnPrompt(&spec, meta)
			if err != nil {
				return err
			}
			if err := s.submitSignal(&spec, dktypes.WorkTypeSlopspace, "", prompt); err != nil {
				return err
			}
			t.Stage = stageSignalled
			if err := s.writeSpec(&spec, ""); err != nil {
				return err
			}

		case stageSignalled:
			reply, err := s.awaitSignal(t)
			if err != nil {
				return err
			}
			if err := writeFileString(filepath.Join(meta.RootPath, slopReplyFile), reply); err != nil {
				return err
			}
			if meta.Deployed {
				if err := s.slop.ss.Return(id); err != nil {
					return err
				}
			}
			t.Stage = stageReturned
			if err := s.writeSpec(&spec, ""); err != nil {
				return err
			}

		case stageReturned:
			if meta.Deployed {
				return fmt.Errorf("slopspace %s is deployed again before its reply was written", id)
			}
			reply, err := readFileString(filepath.Join(meta.RootPath, slopReplyFile))
			if err != nil {
				return fmt.Errorf("reading the captured reply: %v", err)
			}
			heads := map[string]string{}
			msg := turnReplyMessage(&spec, t)
			for _, repo := range spec.Targets {
				owner, name, _ := strings.Cut(repo, "/")
				res, err := s.slop.ss.WriteRepoChanges(id, owner, name, msg)
				if err != nil {
					return fmt.Errorf("%s: %v", repo, err)
				}
				heads[repo] = res.SHA
			}
			root := slopCondocRoot(meta, spec.CondocRepo)
			info, err := detectPhase(root, filepath.Join(root, filepath.FromSlash(spec.CondocPath)))
			if err != nil {
				return err
			}
			activeRel := info.StepFile
			if t.Kind == turnSubstep || t.Kind == turnSubstepRevision {
				activeRel = info.SubstepFile
			}
			if activeRel == "" {
				return fmt.Errorf("can't find the step/substep file to write the reply into")
			}
			active := filepath.Join(root, activeRel)
			if err := appendReply(active, t.Letter, reply, turnPreamble(spec.Targets, t.Bases, heads)); err != nil {
				return err
			}
			if err := addIterationTemplate(active, nextLetterAfter(t.Letter)); err != nil {
				return err
			}
			// The reply is in, so a failure here mustn't rerun this stage.
			replyLabel := strings.TrimSpace("Reply " + t.Letter)
			impls := implDir(filepath.Join(root, filepath.FromSlash(spec.CondocPath)))
			if err := addAgentResources(active, slopAgentResourcesDir(meta), impls, replyLabel); err != nil {
				log.Printf("slopspace %s: linking the agent's resources: %v", stem, err)
			}
			t.Stage = stageReplied
			if err := s.writeSpec(&spec, ""); err != nil {
				return err
			}

		case stageReplied:
			if err := s.commitCondocRepo(&spec, turnReplyMessage(&spec, t)); err != nil {
				return err
			}
			os.Remove(filepath.Join(meta.RootPath, slopReplyFile))
			spec.Turn = nil
			spec.Iteration = t.Letter
			spec.Phase = "" // forces syncSpecState to save the cleared turn
			root := slopCondocRoot(meta, spec.CondocRepo)
			info, err := detectPhase(root, filepath.Join(root, filepath.FromSlash(spec.CondocPath)))
			if err != nil {
				return err
			}
			return s.syncSpecState(&spec, info, root)

		default:
			return fmt.Errorf("unknown turn stage %q", t.Stage)
		}
	}
}

// turnPrompt builds the agent's instructions, pointing at the condoc files
// where the agent sees them: inside the deployed slopspace.
func (s *Server) turnPrompt(spec *PointerSpec, meta *dktypes.SlopspaceMetadata) (string, error) {
	if !meta.Deployed {
		return "", fmt.Errorf("slopspace %s isn't deployed", meta.ID)
	}
	root := slopCondocRoot(meta, spec.CondocRepo)
	info, err := detectPhase(root, filepath.Join(root, filepath.FromSlash(spec.CondocPath)))
	if err != nil {
		return "", err
	}
	stepFile := filepath.Join(root, info.StepFile)
	substepFile := filepath.Join(root, info.SubstepFile)
	var prompt string
	t := spec.Turn
	switch t.Kind {
	case turnStep:
		prompt = buildStepPrompt(stepFile)
	case turnRevision:
		prompt = buildRevisionPrompt(stepFile, t.Letter, false)
	case turnSubstep:
		prompt = buildSubstepPrompt(stepFile, substepFile)
	case turnSubstepRevision:
		prompt = buildRevisionPrompt(substepFile, t.Letter, true)
	default:
		return "", fmt.Errorf("unknown turn kind %q", t.Kind)
	}
	if (t.Kind == turnSubstep || t.Kind == turnSubstepRevision) && info.SubstepFile == "" {
		return "", fmt.Errorf("no active substep file")
	}
	repos := filepath.Join(meta.DeployPath, dkslop.WriteSpacesDir, dkslop.ReposDir)
	return prompt + slopspaceRepoNote(repos, spec.Targets, spec.CondocRepo) + agentResourcesNote(slopAgentResourcesDir(meta)), nil
}

// submitSignal writes the turn's work signal -- the only thing condoccer
// hands the worker (D13). Signal files are named by role and creation
// second, so a clash moves to the next free second.
func (s *Server) submitSignal(spec *PointerSpec, workType dktypes.WorkType, workdir, prompt string) error {
	t := spec.Turn
	role := "condoc-" + spec.Stem()
	created := time.Now()
	for {
		ongoing := filepath.Join(s.slop.dk.OngoingWorkDir(), worksignal.GenerateFilename(role, created, false))
		complete := filepath.Join(s.slop.dk.CompleteWorkDir(), worksignal.GenerateFilename(role, created, true))
		if !fileExists(ongoing) && !fileExists(complete) {
			break
		}
		created = created.Add(time.Second)
	}
	agent := spec.Agent
	if agent == "" {
		agent = s.slop.agent
	}
	model := spec.Model
	if model == "" {
		model = s.slop.model
	}
	if _, err := s.slop.signals.Create(&dktypes.WorkSignal{
		WorkType:     workType,
		WorkLocation: workdir,
		AgentType:    dktypes.AgentTypeWorker,
		Role:         role,
		Prompt:       prompt,
		Agent:        agent,
		Model:        model,
		Mode:         "write",
		CreatedAt:    created,
	}); err != nil {
		return err
	}
	t.Role = role
	t.SignalAt = created.Unix()
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// awaitSignal waits for the worker to move the turn's signal to complete/
// and returns the agent's reply from it.
func (s *Server) awaitSignal(t *PointerTurn) (string, error) {
	created := time.Unix(t.SignalAt, 0)
	ongoing := filepath.Join(s.slop.dk.OngoingWorkDir(), worksignal.GenerateFilename(t.Role, created, false))
	complete := filepath.Join(s.slop.dk.CompleteWorkDir(), worksignal.GenerateFilename(t.Role, created, true))
	for !fileExists(complete) {
		if !fileExists(ongoing) {
			time.Sleep(slopTurnPollInterval) // the worker writes complete/ before removing ongoing/
			if !fileExists(complete) {
				return "", fmt.Errorf("work signal %s vanished without completing", filepath.Base(ongoing))
			}
			break
		}
		time.Sleep(slopTurnPollInterval)
	}
	signal, events, err := s.slop.signals.Read(complete)
	if err != nil {
		return "", err
	}
	return replyFromSignal(signal, events), nil
}

// replyFromSignal is the agent's captured output, as federation-command
// captures the terminal reply; a failed run with no output says why.
func replyFromSignal(signal *dktypes.WorkSignal, events []dktypes.WorkEvent) string {
	output, comment := "", ""
	for _, e := range events {
		if e.StatusUpdate == "output" {
			output = e.Output
		}
		if e.Comment != "" {
			comment = e.Comment
		}
	}
	reply := strings.TrimSpace(stripANSI(output))
	if signal.Status == dktypes.WorkStatusFailed {
		note := "(the agent run failed"
		if comment != "" {
			note += ": " + comment
		}
		note += ")"
		if reply == "" {
			return note
		}
		return reply + "\n\n" + note
	}
	if reply == "" {
		return "(no reply captured)"
	}
	return reply
}

// pointerBase is "owner/name@<sha>" of the pointer repo's HEAD, recorded
// with a turn's bases so the reply's diff can include the turn's pointer
// changes. It is "" when the pointer repo is also one of the condoc's repos
// (their diff files would share names) or can't be read.
func (s *Server) pointerBase(spec *PointerSpec) string {
	cfg, path := s.acquirePointerOp()
	defer s.endPointerOp()
	repo := normalizeRepo(cfg.Repo)
	if !pointerRepoPattern.MatchString(repo) {
		return ""
	}
	for _, r := range spec.Repos() {
		if strings.EqualFold(r, repo) {
			return ""
		}
	}
	out, err := pointerGit(path, "rev-parse", "HEAD")
	if err != nil {
		log.Printf("slopspace %s: pointer repo HEAD: %v: %s", spec.Stem(), err, strings.TrimSpace(string(out)))
		return ""
	}
	return repo + "@" + strings.TrimSpace(string(out))
}

// turnPreamble links each target repo's commit for the reply, one line per
// repo (D6): where the branch was before the turn and the commit the turn
// made, or "(no changes)". The other repos in bases -- the condoc repo and
// the pointer repo -- are committed only after the reply is written, so
// their lines link the base and end "→ this reply"; the diff viewer finds
// their heads (see slopResolveHead).
func turnPreamble(targets, bases []string, heads map[string]string) string {
	baseOf := map[string]string{}
	var others []string
	isTarget := map[string]bool{}
	for _, t := range targets {
		isTarget[t] = true
	}
	for _, b := range bases {
		if i := strings.LastIndex(b, "@"); i > 0 {
			baseOf[b[:i]] = b[i+1:]
			if !isTarget[b[:i]] {
				others = append(others, b[:i])
			}
		}
	}
	link := func(repo, sha string) string {
		short := sha
		if len(short) > 7 {
			short = short[:7]
		}
		return fmt.Sprintf("[`%s`](https://github.com/%s/commit/%s)", short, repo, sha)
	}
	var lines []string
	for _, repo := range targets {
		base, head := baseOf[repo], heads[repo]
		switch {
		case head == "" || head == base:
			lines = append(lines, fmt.Sprintf("- %s: (no changes)", repo))
		case base == "":
			lines = append(lines, fmt.Sprintf("- %s: %s", repo, link(repo, head)))
		default:
			lines = append(lines, fmt.Sprintf("- %s: %s → %s", repo, link(repo, base), link(repo, head)))
		}
	}
	for _, repo := range others {
		lines = append(lines, fmt.Sprintf("- %s: %s → this reply", repo, link(repo, baseOf[repo])))
	}
	return strings.Join(lines, "\n")
}

// ---- diffs ----
//
// A slopspace reply's diff spans every repo the turn changed, so a diff
// request carries one range per repo, read off the reply preamble's links:
// fromCommit is "owner/name@<base>,..." and toCommit "owner/name@<head>,...".
// A repo with a head and no base diffs the head commit alone. The condoc
// repo and the pointer repo come with a base and no head ("→ this reply"):
// their heads are found here (slopResolveHead), and the pointer repo is
// listed only when the turn changed its pointer file. Files go back to the
// UI as "owner/name/<path>", which is how get-file-diff names them.

// slopDiffRange is one repo's part of a slopspace diff request.
type slopDiffRange struct {
	repo     string
	gitArgs  []string // points git at the repo: --git-dir <dir> or -C <path>
	spec     string   // git revision range
	path     string   // limits the diff to one file: the pointer repo's pointer file
	allFiles bool     // keeps condoc files, which a target's list leaves out
}

func (r slopDiffRange) git(args ...string) *exec.Cmd {
	return exec.Command("git", append(append([]string{}, r.gitArgs...), args...)...)
}

// slopResolveHead finds where a repo committed after the reply stood for a
// turn that started at base. In the condoc repo that is the first commit
// after base, the reply's own: nothing else commits there while the turn
// runs. In the pointer repo it is the first commit after base to the
// condoc's pointer file that clears its turn; other condoccers' commits to
// other pointer files are skipped. "" means the turn hasn't got that far,
// or base was rebased away by a pointer push.
func (s *Server) slopResolveHead(r slopDiffRange, base string) (string, error) {
	if r.path == "" {
		out, err := r.git("rev-list", "--first-parent", "--reverse", base+"..HEAD").Output()
		if err != nil {
			return "", fmt.Errorf("git rev-list failed in %s: %v", r.repo, err)
		}
		first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		return first, nil
	}
	s.acquirePointerOp()
	defer s.endPointerOp()
	if err := r.git("merge-base", "--is-ancestor", base, "HEAD").Run(); err != nil {
		return "", nil
	}
	out, err := r.git("rev-list", "--reverse", "--topo-order", base+"..HEAD", "--", r.path).Output()
	if err != nil {
		return "", fmt.Errorf("git rev-list failed in %s: %v", r.repo, err)
	}
	for _, c := range strings.Fields(string(out)) {
		b, err := r.git("show", c+":"+r.path).Output()
		if err != nil {
			continue
		}
		if spec, err := parsePointerSpec(string(b)); err == nil && spec.Turn == nil {
			return c, nil
		}
	}
	return "", nil
}

// parseRepoShas parses "owner/name@sha,..." into repo → sha, keeping order.
func parseRepoShas(list string) (map[string]string, []string, error) {
	shas := map[string]string{}
	var order []string
	for _, part := range strings.Split(list, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		repo, sha, ok := strings.Cut(part, "@")
		if !ok || !commitHashRe.MatchString(sha) {
			return nil, nil, fmt.Errorf("invalid commit range %q", part)
		}
		if _, dup := shas[repo]; !dup {
			order = append(order, repo)
		}
		shas[repo] = sha
	}
	return shas, order, nil
}

// slopDiffRanges resolves a slopspace diff request to a range in each repo:
// in its git dir, which stays in the slopspace's writespaces-secure whether
// or not the slopspace is deployed, or for the pointer repo in its checkout.
// A repo whose head can't be found yet is left out.
func (s *Server) slopDiffRanges(rel, fromCommit, toCommit string) ([]slopDiffRange, error) {
	stem := strings.TrimPrefix(rel, slopPathPrefix)
	if stem == "" || strings.ContainsAny(stem, `/\`) || strings.Contains(stem, "..") {
		return nil, fmt.Errorf("invalid condoc path")
	}
	bases, baseOrder, err := parseRepoShas(fromCommit)
	if err != nil {
		return nil, err
	}
	heads, order, err := parseRepoShas(toCommit)
	if err != nil {
		return nil, err
	}
	for _, repo := range baseOrder {
		if _, ok := heads[repo]; !ok {
			order = append(order, repo)
		}
	}
	spec, err := s.readSpec(stem)
	if err != nil {
		return nil, err
	}
	if spec.SlopspaceID == "" {
		return nil, fmt.Errorf("%s has no slopspace (phase %s)", spec.Name, spec.Phase)
	}
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, r := range spec.Repos() {
		known[r] = true
	}
	pointerRepo, pointerPath := "", ""
	if cfg, path, err := s.pointerReadyPath(); err == nil {
		pointerRepo, pointerPath = normalizeRepo(cfg.Repo), path
	}
	var ranges []slopDiffRange
	for _, repo := range order {
		r := slopDiffRange{repo: repo}
		switch {
		case known[repo]:
			r.gitArgs = []string{"--git-dir", filepath.Join(meta.RootPath, dkslop.WriteSpacesSecure, dkslop.ReposDir, filepath.FromSlash(repo))}
			r.allFiles = repo == spec.CondocRepo
		case repo == pointerRepo && pointerPath != "":
			r.gitArgs = []string{"-C", pointerPath}
			r.path = pointerDir + "/" + stem + ".yaml"
			r.allFiles = true
		default:
			return nil, fmt.Errorf("%s isn't one of %s's repos", repo, spec.Name)
		}
		base, head := bases[repo], heads[repo]
		if head == "" {
			if !r.allFiles {
				return nil, fmt.Errorf("%s has no head commit in this diff", repo)
			}
			if head, err = s.slopResolveHead(r, base); err != nil {
				return nil, err
			}
			if head == "" {
				continue
			}
		}
		if base == "" {
			base = head + "^"
		}
		r.spec = base + ".." + head
		ranges = append(ranges, r)
	}
	return ranges, nil
}

func (s *Server) slopHandleGetDiff(c *wsClient, rel, fromCommit, toCommit string) {
	ranges, err := s.slopDiffRanges(rel, fromCommit, toCommit)
	if err != nil {
		s.replyErr(c, err)
		return
	}
	files := []string{}
	for _, r := range ranges {
		args := []string{"diff", "--name-only", r.spec}
		if r.path != "" {
			args = append(args, "--", r.path)
		}
		out, err := r.git(args...).Output()
		if err != nil {
			s.replyErr(c, fmt.Errorf("git diff failed in %s: %v", r.repo, err))
			return
		}
		for _, f := range listedFiles(out, !r.allFiles) {
			files = append(files, r.repo+"/"+f)
		}
	}
	s.sendToClient(c, "diff-list", map[string]interface{}{
		"fromCommit": fromCommit,
		"toCommit":   toCommit,
		"files":      files,
	})
}

func (s *Server) slopHandleGetFileDiff(c *wsClient, rel, fromCommit, toCommit, file string) {
	parts := strings.SplitN(file, "/", 3)
	if len(parts) != 3 || parts[2] == "" || strings.Contains(file, "..") {
		s.replyErr(c, fmt.Errorf("invalid file path"))
		return
	}
	repo, path := parts[0]+"/"+parts[1], parts[2]
	ranges, err := s.slopDiffRanges(rel, fromCommit, toCommit)
	if err != nil {
		s.replyErr(c, err)
		return
	}
	for _, r := range ranges {
		if r.repo != repo {
			continue
		}
		if r.path != "" && path != r.path {
			break
		}
		out, err := r.git("diff", r.spec, "--", path).Output()
		if err != nil {
			s.replyErr(c, fmt.Errorf("git diff failed in %s: %v", repo, err))
			return
		}
		content := string(out)
		s.sendToClient(c, "file-diff", map[string]interface{}{
			"fromCommit": fromCommit,
			"toCommit":   toCommit,
			"file":       file,
			"content":    content,
			"hunks":      diffHunks(content),
		})
		return
	}
	s.replyErr(c, fmt.Errorf("%s isn't in this diff", repo))
}

// ---- propose PR (D10) ----

// slopRegeneratePRs queues another propose-PR turn with the human's
// instruction and the current (possibly edited) template.
func (s *Server) slopRegeneratePRs(stem, instruction, template string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != string(PhasePRProposed) {
		return fmt.Errorf("%s has no PR proposal to regenerate (phase %s)", spec.Name, spec.Phase)
	}
	if strings.TrimSpace(template) != "" {
		spec.PRTemplate = template
	}
	spec.Error = ""
	spec.Turn = &PointerTurn{Kind: turnProposePR, Stage: stageQueued, Instruction: strings.TrimSpace(instruction)}
	spec.Phase = string(PhaseQueued)
	if err := s.writeSpec(&spec, fmt.Sprintf("condoc: %s regenerate PRs queued", spec.Name)); err != nil {
		return err
	}
	s.enqueueTurn(stem)
	return nil
}

// slopSavePRTemplate stores the human's edits to the proposed template.
func (s *Server) slopSavePRTemplate(stem, template string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != string(PhasePRProposed) {
		return fmt.Errorf("%s isn't in propose PR (phase %s)", spec.Name, spec.Phase)
	}
	if _, _, err := parsePRTemplate(template); err != nil {
		return fmt.Errorf("template: %v", err)
	}
	spec.PRTemplate = template
	return s.writeSpec(&spec, fmt.Sprintf("condoc: %s PR template edited", spec.Name))
}

// runProposeTurn runs the propose-PR turn: an in-place work signal in a
// tempdir holding the condoc, each repo's diff and log, and the pointer
// spec, asking the agent to write prs.yaml there. The caller holds the
// claim.
func (s *Server) runProposeTurn(spec *PointerSpec) error {
	t := spec.Turn
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return err
	}
	if meta.Deployed {
		return fmt.Errorf("slopspace %s is deployed", meta.ID)
	}
	for {
		switch t.Stage {
		case "", stageQueued:
			dir, repos, err := s.prepareProposeWorkdir(spec, meta)
			if err != nil {
				return err
			}
			t.Workdir = dir
			if err := s.submitSignal(spec, dktypes.WorkTypeInPlace, dir, proposePRPrompt(dir, repos, t.Instruction != "")); err != nil {
				os.RemoveAll(dir)
				return err
			}
			t.Stage = stageSignalled
			spec.Phase = string(PhaseAgentRunning)
			if err := s.writeSpec(spec, fmt.Sprintf("condoc: %s propose PRs running", spec.Name)); err != nil {
				return err
			}

		case stageSignalled:
			reply, err := s.awaitSignal(t)
			if err != nil {
				return err
			}
			template, terr := readFileString(filepath.Join(t.Workdir, slopPRTemplateFile))
			if terr == nil {
				if _, drafts, perr := parsePRTemplate(template); perr != nil {
					terr = perr
				} else if _, cerr := checkPRDrafts(spec, drafts); cerr != nil {
					terr = cerr
				}
			}
			spec.Error = ""
			if terr != nil {
				bases := map[string]string{}
				for _, repo := range spec.Repos() {
					bases[repo] = s.slopBaseBranch(meta, repo)
				}
				template = defaultPRTemplate(spec, bases)
				spec.Error = fmt.Sprintf("the agent's prs.yaml was unusable (%v), so a default template was filled in. Agent reply: %s", terr, firstLine(reply))
			}
			os.RemoveAll(t.Workdir)
			spec.PRTemplate = template
			spec.Turn = nil
			spec.Phase = string(PhasePRProposed)
			return s.writeSpec(spec, fmt.Sprintf("condoc: %s PRs proposed", spec.Name))

		default:
			return fmt.Errorf("unknown propose-PR stage %q", t.Stage)
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// slopBaseBranch is a writespace repo's default branch, from its clone's
// origin/HEAD.
func (s *Server) slopBaseBranch(meta *dktypes.SlopspaceMetadata, repo string) string {
	gitDir := filepath.Join(meta.RootPath, dkslop.WriteSpacesSecure, dkslop.ReposDir, filepath.FromSlash(repo))
	out, err := exec.Command("git", "--git-dir", gitDir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output()
	if err == nil {
		if b := strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/"); b != "" {
			return b
		}
	}
	return "main"
}

// prepareProposeWorkdir builds the propose-PR tempdir inside the slopspace's
// own directory, which the worker shares with condoccer (D13).
func (s *Server) prepareProposeWorkdir(spec *PointerSpec, meta *dktypes.SlopspaceMetadata) (string, []string, error) {
	dir, err := os.MkdirTemp(meta.RootPath, "propose-pr-")
	if err != nil {
		return "", nil, err
	}
	fail := func(e error) (string, []string, error) {
		os.RemoveAll(dir)
		return "", nil, e
	}

	root := slopCondocRoot(meta, spec.CondocRepo)
	main := filepath.Join(root, filepath.FromSlash(spec.CondocPath))
	condocDir := filepath.Join(dir, "condoc")
	if err := copyTree(main, filepath.Join(condocDir, filepath.Base(main))); err != nil {
		return fail(err)
	}
	if fileExists(implDir(main)) {
		if err := copyTree(implDir(main), filepath.Join(condocDir, filepath.Base(implDir(main)))); err != nil {
			return fail(err)
		}
	}

	reposDir := filepath.Join(dir, "repos")
	if err := os.MkdirAll(reposDir, 0755); err != nil {
		return fail(err)
	}
	var repoLines []string
	for _, repo := range spec.Repos() {
		base := s.slopBaseBranch(meta, repo)
		gitDir := filepath.Join(meta.RootPath, dkslop.WriteSpacesSecure, dkslop.ReposDir, filepath.FromSlash(repo))
		slug := strings.ReplaceAll(repo, "/", "__")
		diff, _ := exec.Command("git", "--git-dir", gitDir, "diff", "origin/"+base+"...HEAD").CombinedOutput()
		if len(diff) > slopDiffLimit {
			diff = append(diff[:slopDiffLimit], []byte("\n... (diff truncated)\n")...)
		}
		logOut, _ := exec.Command("git", "--git-dir", gitDir, "log", "--oneline", "origin/"+base+"..HEAD").CombinedOutput()
		if err := writeFileString(filepath.Join(reposDir, slug+".diff"), string(diff)); err != nil {
			return fail(err)
		}
		if err := writeFileString(filepath.Join(reposDir, slug+".log"), string(logOut)); err != nil {
			return fail(err)
		}
		repoLines = append(repoLines, fmt.Sprintf("%s (base branch %s)", repo, base))
	}
	if err := writeFileString(filepath.Join(dir, "pointer.yaml"), spec.render()); err != nil {
		return fail(err)
	}
	if spec.Turn.Instruction != "" {
		if err := writeFileString(filepath.Join(dir, "previous-prs.yaml"), spec.PRTemplate); err != nil {
			return fail(err)
		}
		if err := writeFileString(filepath.Join(dir, "instruction.txt"), spec.Turn.Instruction+"\n"); err != nil {
			return fail(err)
		}
	}
	return dir, repoLines, nil
}

func proposePRPrompt(dir string, repos []string, regenerate bool) string {
	prompt := fmt.Sprintf("You are proposing pull requests for a completed condoc. Everything you need is in '%s': "+
		"condoc/ holds the condoc and its step files, repos/ holds each repo's diff (<owner>__<name>.diff) and log "+
		"(<owner>__<name>.log) against its base branch, and pointer.yaml is the condoc's pointer spec. ", dir)
	if regenerate {
		prompt += "A previous proposal is in previous-prs.yaml and the human's instruction for this one is in " +
			"instruction.txt -- follow it. "
	}
	prompt += "Write a file named " + slopPRTemplateFile + " in that directory, in exactly this YAML shape:\n\n" +
		"summary: |\n  <a short paragraph on what the condoc did overall>\nprs:\n  - repo: <owner/name>\n" +
		"    base: <base branch>\n    title: <PR title>\n    body: |\n      <PR description in markdown>\n\n" +
		"Include exactly one entry for each of these repos: " + strings.Join(repos, "; ") + ". " +
		"Describe what changed in each repo and why, drawing on the condoc. Do not modify any other file. " +
		"When you have finished, reply with one sentence saying the proposal is written."
	return prompt
}

// copyTree copies a file or directory tree.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0644)
	})
}

// ---- PRs and merge (D10) ----

func prClient() (*prkeeper.Client, error) {
	tok := githubToken()
	if tok == "" {
		return nil, fmt.Errorf("no GitHub token (TF_VAR_github_pat) to raise or merge PRs with")
	}
	return prkeeper.NewClient(tok), nil
}

// slopAcceptPRs raises the template's PRs -- one per repo, targets first and
// the condoc repo last -- links them to each other, and records them in the
// condoc and the pointer. Re-accepting after a partial failure picks up the
// PRs already open instead of duplicating them.
func (s *Server) slopAcceptPRs(stem, template string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != string(PhasePRProposed) {
		return fmt.Errorf("%s isn't in propose PR (phase %s)", spec.Name, spec.Phase)
	}
	if strings.TrimSpace(template) == "" {
		template = spec.PRTemplate
	}
	summary, drafts, err := parsePRTemplate(template)
	if err != nil {
		return fmt.Errorf("template: %v", err)
	}
	if drafts, err = checkPRDrafts(&spec, drafts); err != nil {
		return fmt.Errorf("template: %v", err)
	}
	client, err := prClient()
	if err != nil {
		return err
	}
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return err
	}
	if meta.Deployed {
		return fmt.Errorf("slopspace %s is deployed", meta.ID)
	}
	spec.PRTemplate = template

	fail := func(e error) error {
		spec.Error = e.Error()
		if werr := s.writeSpec(&spec, fmt.Sprintf("condoc: %s raising PRs failed", spec.Name)); werr != nil {
			log.Printf("slopspace %s: %v", stem, werr)
		}
		return e
	}

	bodies := map[string]string{}
	var prs []PointerPR
	for _, d := range drafts {
		body := d.Body
		if summary != "" {
			body += "\n\n### Condoc summary\n\n" + summary
		}
		body += fmt.Sprintf("\n\n---\nRaised by condoccer for condoc [%s](%s).", spec.Name, spec.CondocURL)
		bodies[d.Repo] = body
		base := d.Base
		if base == "" {
			base = s.slopBaseBranch(meta, d.Repo)
		}
		pr, err := client.FindOpen(d.Repo, spec.Branch)
		if err == nil && pr == nil {
			pr, err = client.Create(d.Repo, prkeeper.CreateRequest{Title: d.Title, Head: spec.Branch, Base: base, Body: body})
		}
		if err != nil {
			spec.PRs = prs
			return fail(fmt.Errorf("%s: %v", d.Repo, err))
		}
		prs = append(prs, PointerPR{Repo: d.Repo, Number: pr.Number, URL: pr.URL, State: "open"})
	}
	for i, pr := range prs {
		var related []string
		for j, other := range prs {
			if j != i {
				related = append(related, fmt.Sprintf("- %s#%d", other.Repo, other.Number))
			}
		}
		if len(related) == 0 {
			continue
		}
		body := bodies[pr.Repo] + "\n\nRelated PRs (merged together, this condoc's repo last):\n" + strings.Join(related, "\n")
		if err := client.UpdateBody(pr.Repo, pr.Number, body); err != nil {
			log.Printf("slopspace %s: linking %s#%d: %v", stem, pr.Repo, pr.Number, err)
		}
	}
	spec.PRs = prs

	// Mirror the PRs into the condoc itself (D9).
	root := slopCondocRoot(meta, spec.CondocRepo)
	main := filepath.Join(root, filepath.FromSlash(spec.CondocPath))
	if content, err := readFileString(main); err == nil {
		var lines []string
		for _, pr := range prs {
			lines = append(lines, fmt.Sprintf("- %s: [#%d](%s)", pr.Repo, pr.Number, pr.URL))
		}
		content = strings.TrimRight(content, "\n") + "\n\n\n## Pull Requests\n\n" + strings.Join(lines, "\n") + "\n"
		if err := writeFileString(main, content); err != nil {
			return fail(err)
		}
		if err := s.commitCondocRepo(&spec, "condoc: pull requests raised"); err != nil {
			return fail(err)
		}
	}

	spec.Error = ""
	spec.Phase = string(PhasePROpen)
	if err := s.writeSpec(&spec, fmt.Sprintf("condoc: %s PRs raised", spec.Name)); err != nil {
		return err
	}
	s.slop.mu.Lock()
	delete(s.slop.prPolled, stem)
	s.slop.mu.Unlock()
	go s.pollPRs(stem)
	return nil
}

// pollPRs refreshes the raised PRs' mergeability for the Merge control.
func (s *Server) pollPRs(stem string) {
	if !s.claim(stem) {
		return
	}
	defer s.release(stem)
	s.slop.mu.Lock()
	s.slop.prPolled[stem] = time.Now()
	s.slop.mu.Unlock()
	spec, err := s.readSpec(stem)
	if err != nil || len(spec.PRs) == 0 {
		return
	}
	client, err := prClient()
	if err != nil {
		return
	}
	live := make([]PRLive, len(spec.PRs))
	for i, pr := range spec.PRs {
		live[i] = PRLive{Repo: pr.Repo, Number: pr.Number, URL: pr.URL, State: pr.State, Status: pr.State}
		got, err := client.Get(pr.Repo, pr.Number)
		if err != nil {
			live[i].Status = "unknown: " + err.Error()
			continue
		}
		live[i].Status = got.Status()
		live[i].Ready = got.Ready()
		if got.Merged {
			live[i].State = "merged"
		} else {
			live[i].State = got.State
		}
	}
	s.slop.mu.Lock()
	s.slop.prLive[stem] = live
	s.slop.mu.Unlock()
	s.broadcastPointer()
}

// slopMerge merges every PR once all are mergeable: the targets first, the
// condoc repo last, with merge commits so the condoc's per-revision links
// keep pointing at real history (D10). A failure stops the run and leaves
// each PR's state visible; Merge again resumes past the merged ones. Once
// all are merged the branches and the slopspace are removed (D9).
func (s *Server) slopMerge(stem string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer func() {
		s.release(stem)
		go s.pollPRs(stem)
	}()
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != string(PhasePROpen) {
		return fmt.Errorf("%s has no open PRs (phase %s)", spec.Name, spec.Phase)
	}
	client, err := prClient()
	if err != nil {
		return err
	}
	fail := func(e error) error {
		spec.Error = e.Error()
		if werr := s.writeSpec(&spec, fmt.Sprintf("condoc: %s merge stopped", spec.Name)); werr != nil {
			log.Printf("slopspace %s: %v", stem, werr)
		}
		return e
	}

	// Every PR has to be mergeable before any is merged.
	live := make([]*prkeeper.PullRequest, len(spec.PRs))
	for i, pr := range spec.PRs {
		got, err := client.Get(pr.Repo, pr.Number)
		if err != nil {
			return fail(fmt.Errorf("%s#%d: %v", pr.Repo, pr.Number, err))
		}
		if !got.Merged && !got.Ready() {
			return fail(fmt.Errorf("%s#%d isn't mergeable yet (%s)", pr.Repo, pr.Number, got.Status()))
		}
		live[i] = got
	}
	for i := range spec.PRs {
		pr := &spec.PRs[i]
		if live[i].Merged {
			pr.State = "merged"
			continue
		}
		if err := client.Merge(pr.Repo, pr.Number, prkeeper.MergeMethodMerge, live[i].Head.SHA); err != nil {
			return fail(fmt.Errorf("merging %s#%d: %v", pr.Repo, pr.Number, err))
		}
		pr.State = "merged"
	}

	for _, repo := range spec.Repos() {
		if err := client.DeleteBranch(repo, spec.Branch); err != nil {
			log.Printf("slopspace %s: deleting %s in %s: %v", stem, spec.Branch, repo, err)
		}
	}
	if spec.SlopspaceID != "" {
		if err := s.slop.ss.Delete(spec.SlopspaceID); err != nil {
			log.Printf("slopspace %s: deleting slopspace %s: %v", stem, spec.SlopspaceID, err)
		}
	}
	spec.Error = ""
	spec.Phase = string(PhaseMerged)
	return s.writeSpec(&spec, fmt.Sprintf("condoc: %s merged", spec.Name))
}
