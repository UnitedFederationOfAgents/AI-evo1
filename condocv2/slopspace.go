package main

// condocv2's handler -- slopspaces only. It keeps the outer sequence of
// condoccer's slopspace scheme (docs/SlopspaceControlScheme.md): a condoc is
// proposed as a pointer file in the pointer repo, then accepted, which begins
// it -- a slopspace with a writespace per repo on the condoc's branch, and the
// condoc files written into the condoc repo. Past that a v2 condoc is a
// single linear step of interactions, each one:
//
//	lock (pointer commit)
//	  -> insert the prompt (the prompt commit, condoc repo)
//	  -> submit to the agent (at once with auto-submit-once, else on Submit)
//	  -> collect the response (the response commit, condoc repo)
//	  -> commit each target repo the agent changed
//	  -> map those commits into the condoc (the record commit, condoc repo)
//	  -> unlock (pointer commit)
//
// Agent turns run one at a time from a FIFO across every condoc this
// condocv2 holds: deploy the slopspace, submit a work signal, wait for the
// worker's completion file, return the slopspace. condocv2 never talks to the
// worker process itself -- only work files in, completion files out.

import (
	"errors"
	"fmt"
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
)

const (
	slopTickInterval     = 2 * time.Second
	slopTurnPollInterval = 2 * time.Second

	slopReplyFile = "CONDOCV2_TURN_REPLY.txt" // in the slopspace root, which never deploys
)

// errTurnWait means the turn can't start yet because another slopspace is
// deployed at the worker's deploy path; it stays at the head of the queue.
var errTurnWait = errors.New("another slopspace is deployed")

// slopState is the Server's slopspace bookkeeping.
type slopState struct {
	mu      sync.Mutex
	dk      *dktypes.Config
	ss      *dkslop.Manager
	ws      *dkws.Manager
	signals *worksignal.Manager
	agent   string // defaults offered for new proposals
	model   string
	queue   []string // pointer stems with a turn waiting, FIFO
	running string   // stem whose turn is running
	busy    map[string]bool
	wake    chan struct{}
	prLive  map[string]prLiveCache // stem -> the raised PRs as last seen on GitHub (see complete.go)
	records map[string]recordCache // stem -> a merged condoc's timeline record (see record.go)
}

// dungeonKeeperConfig reads the same environment dungeon-keeper does, so
// condocv2 and the worker agree on where slopspaces, work signals and the
// deploy path are.
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
	s.slop.prLive = map[string]prLiveCache{}
	s.slop.records = map[string]recordCache{}
	s.slop.wake = make(chan struct{}, 1)
}

// slopHolder identifies this condocv2 in a pointer's holder field. Only the
// holder drives a condoc; the rest show it read-only. It is condocv2's own
// identity, not condoccer's: v2 condocs are incompatible with v1, so a
// condoccer on this host must never pick them up.
func (s *Server) slopHolder() string {
	host, _ := os.Hostname()
	return "condocv2@" + host
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

func validStem(stem string) bool {
	return stem != "" && !strings.ContainsAny(stem, `/\`) && !strings.Contains(stem, "..")
}

// commitMsg is the subject of every commit condocv2 makes for a condoc --
// "condocv2: <Name> <kind>[ <interaction>]" -- which the visual timeline
// reads back (see timeline.go).
func commitMsg(spec *PointerSpec, kind string, n int) string {
	if n > 0 {
		return fmt.Sprintf("condocv2: %s %s %d", spec.Name, kind, n)
	}
	return fmt.Sprintf("condocv2: %s %s", spec.Name, kind)
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
// is detached for the rebase and the autostash hides uncommitted stage
// writes, so a read in that window could fail or see a stale spec.
func (s *Server) readSpec(stem string) (PointerSpec, error) {
	if !validStem(stem) {
		return PointerSpec{}, fmt.Errorf("invalid condoc %q", stem)
	}
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

// readHeldSpec reads a spec this condocv2 holds.
func (s *Server) readHeldSpec(stem string) (PointerSpec, error) {
	spec, err := s.readSpec(stem)
	if err != nil {
		return spec, err
	}
	if spec.Holder != s.slopHolder() {
		return spec, fmt.Errorf("%s is held by %s, not this condocv2", spec.Name, spec.Holder)
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
// committed and pushed -- the propose, begin, lock and unlock commits of the
// pointer lane; without one it is only written locally, which is how an
// interaction's stages are recorded so a restart resumes them without a
// push per stage. A failed push is recorded and retried by the next pull,
// not returned: this condocv2's copy is authoritative.
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
// condocv2's, a condoccer's or a human's edits are never swept in) and
// pushes. The caller holds the pointer op.
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
// whatever others pushed in between (each only writes the pointer files it
// holds, so the rebase doesn't conflict). The caller holds the pointer op. A
// failure is recorded for the status panel.
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

// ---- where a condoc's files are ----

// slopCondocRoot is the condoc repo's working tree: in the slopspace while
// idle, at the deploy path while a turn has it deployed.
func slopCondocRoot(meta *dktypes.SlopspaceMetadata, condocRepo string) string {
	base := meta.RootPath
	if meta.Deployed && meta.DeployPath != "" {
		base = meta.DeployPath
	}
	return filepath.Join(base, dkslop.WriteSpacesDir, dkslop.ReposDir, filepath.FromSlash(condocRepo))
}

// slopGitDir is a writespace repo's git dir, which stays in the slopspace's
// writespaces-secure whether or not the slopspace is deployed.
func slopGitDir(meta *dktypes.SlopspaceMetadata, repo string) string {
	return filepath.Join(meta.RootPath, dkslop.WriteSpacesSecure, dkslop.ReposDir, filepath.FromSlash(repo))
}

// slopStepFile is where the condoc's step file is right now.
func slopStepFile(meta *dktypes.SlopspaceMetadata, spec *PointerSpec) string {
	root := slopCondocRoot(meta, spec.CondocRepo)
	return stepFilePath(filepath.Join(root, filepath.FromSlash(spec.CondocPath)))
}

// ---- listing ----

func entryFromSpec(spec PointerSpec, file string) PointerEntry {
	stem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	e := PointerEntry{
		File:         file,
		Stem:         stem,
		Name:         spec.Name,
		Phase:        spec.Phase,
		Interactions: spec.Interactions,
		Branch:       spec.Branch,
		CondocRepo:   spec.CondocRepo,
		Targets:      spec.Targets,
		CondocPath:   spec.CondocPath,
		CondocURL:    spec.CondocURL,
		Description:  spec.Description,
		Agent:        spec.Agent,
		Model:        spec.Model,
		StartTime:    spec.StartTime,
		SlopspaceID:  spec.SlopspaceID,
		Holder:       spec.Holder,
		FinalFrom:    spec.FinalFrom,
		FinalAt:      spec.FinalAt,
		Error:        spec.Error,
	}
	if l := spec.Lock; l != nil && !isFinalPhase(spec.Phase) {
		e.LockInteraction = l.Interaction
		e.LockStage = l.Stage
		e.AutoSubmit = l.AutoSubmit
	}
	return e
}

// decoratePointerEntry adds what only this condocv2 knows: whether it holds
// the condoc, its queue position, and whether the slopspace is deployed.
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
	s.slop.mu.Unlock()
	if e.SlopspaceID != "" {
		if meta, err := s.slop.ss.Get(e.SlopspaceID); err == nil {
			e.Deployed = meta.Deployed
		}
	}
	return e
}

// ---- proposal ----

// SlopspaceProposal is the proposal form: new when Stem is empty, an edit of
// a still-proposed condoc otherwise.
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
		return fmt.Errorf("the condoc repo can't also be a target")
	}
	branch := strings.TrimSpace(p.Branch)
	if branch == "" {
		branch = fmt.Sprintf("%s%s-%d/main", condocBranchPrefix, name, spec.StartTime)
	}
	if !strings.HasPrefix(branch, condocBranchPrefix) {
		return fmt.Errorf("branch %q: condocv2 branches live under %s (v1's condoc/ branches are incompatible)", branch, condocBranchPrefix)
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
	spec.ControlScheme = condocControlScheme
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
		spec := PointerSpec{StartTime: time.Now().Unix(), Phase: PhaseProposed, Holder: s.slopHolder()}
		if err := validateProposal(p, &spec); err != nil {
			return err
		}
		if spec.Agent == "" {
			spec.Agent = s.slop.agent
		}
		if spec.Model == "" {
			spec.Model = s.slop.model
		}
		if _, err := s.readSpec(spec.Stem()); err == nil {
			return fmt.Errorf("%s already exists", spec.Stem())
		}
		return s.writeSpec(&spec, commitMsg(&spec, "propose", 0))
	}

	if !s.claim(p.Stem) {
		return errBusy(p.Stem)
	}
	defer s.release(p.Stem)
	spec, err := s.readHeldSpec(p.Stem)
	if err != nil {
		return err
	}
	if spec.Phase != PhaseProposed {
		return fmt.Errorf("%s has already begun", spec.Name)
	}
	if err := validateProposal(p, &spec); err != nil {
		return err
	}
	return s.writeSpec(&spec, commitMsg(&spec, "edit-proposal", 0))
}

func (s *Server) slopWithdraw(stem string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != PhaseProposed {
		return fmt.Errorf("only a proposal can be withdrawn; %s is %s", spec.Name, spec.Phase)
	}
	return s.deleteSpec(stem, commitMsg(&spec, "withdraw", 0))
}

// BranchCheck is one repo's answer to "does the branch exist yet".
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

// ---- acceptance and instantiation: begin ----

// slopAccept begins a proposed condoc: checks the branch is free everywhere,
// creates the slopspace with a writespace per repo on the branch, writes the
// condoc's main and step files into the condoc repo (the condoc lane's begin
// commit), publishes the branch on every target (their begin), and records
// each repo's branch point on the pointer (the pointer lane's begin commit).
func (s *Server) slopAccept(stem string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != PhaseProposed {
		return fmt.Errorf("%s has already begun", spec.Name)
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
			log.Printf("slopspace %s: cleaning up after a failed begin: %v", meta.ID, derr)
		}
		return e
	}
	var bases []string
	for _, repo := range spec.Repos() {
		owner, name, _ := strings.Cut(repo, "/")
		if err := s.slop.ss.AddWritespaceRepo(meta.ID, owner, name, spec.Branch, s.slop.ws.RepoPath(owner, name)); err != nil {
			return undo(fmt.Errorf("add %s to the slopspace: %v", repo, err))
		}
		sha, err := s.slop.ss.RepoHead(meta.ID, owner, name)
		if err != nil {
			return undo(fmt.Errorf("%s: %v", repo, err))
		}
		bases = append(bases, repo+"@"+sha)
	}

	root := slopCondocRoot(meta, spec.CondocRepo)
	main := filepath.Join(root, filepath.FromSlash(spec.CondocPath))
	step := stepFilePath(main)
	if err := os.MkdirAll(filepath.Dir(step), 0755); err != nil {
		return undo(err)
	}
	pointerCfg, _, _ := s.pointerReadyPath()
	if err := writeFileString(main, initialMainFile(spec.Name, spec.StartTime, spec.Branch, pointerCfg.Repo, spec.Description)); err != nil {
		return undo(err)
	}
	if err := writeFileString(step, initialStepFile(spec.Name)); err != nil {
		return undo(err)
	}
	owner, name, _ := strings.Cut(spec.CondocRepo, "/")
	if _, err := s.slop.ss.WriteRepoChanges(meta.ID, owner, name, commitMsg(&spec, "begin", 0)); err != nil {
		return undo(err)
	}
	for _, repo := range spec.Targets {
		owner, name, _ := strings.Cut(repo, "/")
		if err := s.slop.ss.PushRepo(meta.ID, owner, name); err != nil {
			return undo(err)
		}
	}

	spec.SlopspaceID = meta.ID
	spec.Bases = bases
	spec.Phase = PhaseAwaitingPrompt
	spec.Interactions = 0
	spec.Error = ""
	return s.writeSpec(&spec, commitMsg(&spec, "begin", 0))
}

// ---- the interaction ----

// slopPrompt starts the next interaction: it takes the lock (the pointer's
// lock commit, holding the prompt text so a failure can't lose it), then
// inserts the prompt into the condoc (the prompt commit). With autoSubmit
// the interaction goes straight to the turn queue -- once, for this
// interaction only; without it, it waits for slopSubmit.
func (s *Server) slopPrompt(stem, text string, autoSubmit bool) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("the prompt is empty")
	}
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != PhaseAwaitingPrompt || spec.Lock != nil {
		return fmt.Errorf("%s isn't ready for a prompt (phase %s)", spec.Name, spec.Phase)
	}
	if spec.Error != "" {
		return fmt.Errorf("%s has an error -- resubmit it first", spec.Name)
	}
	n := spec.Interactions + 1
	spec.Lock = &InteractionLock{Interaction: n, Stage: stageLocked, AutoSubmit: autoSubmit, Prompt: text}
	spec.Phase = PhaseLocked
	if err := s.writeSpec(&spec, commitMsg(&spec, "lock", n)); err != nil {
		return err
	}
	if err := s.insertPrompt(&spec); err != nil {
		s.setSpecError(stem, err)
		return err
	}
	return nil
}

// insertPrompt is the prompt commit: the locked prompt into the step file,
// committed and pushed in the condoc repo. Then, with auto-submit-once, the
// interaction is queued for the agent. The caller holds the claim.
func (s *Server) insertPrompt(spec *PointerSpec) error {
	l := spec.Lock
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return err
	}
	if meta.Deployed {
		return fmt.Errorf("%s's slopspace is deployed", spec.Name)
	}
	if err := appendSection(slopStepFile(meta, spec), "Prompt", l.Interaction, l.Prompt); err != nil {
		return err
	}
	if err := s.commitCondocRepo(spec, commitMsg(spec, "prompt", l.Interaction)); err != nil {
		return err
	}
	l.Prompt = ""
	l.Stage = stagePrompted
	if l.AutoSubmit {
		return s.queueInteraction(spec)
	}
	return s.writeSpec(spec, "")
}

// slopSubmit sends a prompted interaction to the agent by hand (the prompt
// was inserted without auto-submit-once).
func (s *Server) slopSubmit(stem string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Lock == nil || spec.Lock.Stage != stagePrompted {
		return fmt.Errorf("%s has no prompt waiting to be submitted", spec.Name)
	}
	if spec.Error != "" {
		return fmt.Errorf("%s has an error -- resubmit it first", spec.Name)
	}
	return s.queueInteraction(&spec)
}

// queueInteraction records the interaction as submitted and queues its turn.
func (s *Server) queueInteraction(spec *PointerSpec) error {
	spec.Lock.Stage = stageQueued
	if err := s.writeSpec(spec, ""); err != nil {
		return err
	}
	s.enqueueTurn(spec.Stem())
	return nil
}

// commitCondocRepo commits and pushes the condoc repo's writespace.
func (s *Server) commitCondocRepo(spec *PointerSpec, msg string) error {
	owner, name, _ := strings.Cut(spec.CondocRepo, "/")
	_, err := s.slop.ss.WriteRepoChanges(spec.SlopspaceID, owner, name, msg)
	return err
}

// ---- driving ----

// slopLoop resumes every interaction this condocv2 holds that isn't in the
// queue -- after a restart, or a Resubmit.
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

// readHeldSpecs lists the v2 specs this condocv2 holds, read under the
// pointer op for the same reason as readSpec.
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
		spec, err := parsePointerSpec(string(b)) // v1 pointers fail here
		if err != nil || spec.Holder != me {
			continue
		}
		out = append(out, pointerFileSpec{stem: strings.TrimSuffix(filepath.Base(f), ".yaml"), spec: spec})
	}
	return out
}

func (s *Server) slopTick() {
	for _, ps := range s.readHeldSpecs() {
		stem, spec := ps.stem, ps.spec
		if isFinalPhase(spec.Phase) {
			continue // archived or abandoned: never driven again (see final.go)
		}
		if spec.Phase == PhasePRDrafting && spec.Error == "" {
			s.enqueueTurn(stem) // the PR draft turn (see complete.go)
			continue
		}
		if spec.Lock == nil || spec.Error != "" {
			continue // idle, or waiting for Resubmit
		}
		switch spec.Lock.Stage {
		case stageLocked:
			// The lock commit landed but the prompt commit didn't.
			if !s.claim(stem) {
				continue
			}
			if spec, err := s.readHeldSpec(stem); err == nil && spec.Lock != nil && spec.Lock.Stage == stageLocked && spec.Error == "" {
				if err := s.insertPrompt(&spec); err != nil {
					log.Printf("condocv2 %s: %v", stem, err)
					s.setSpecError(stem, err)
				}
			}
			s.release(stem)
		case stagePrompted:
			// Waiting for Submit.
		default:
			s.enqueueTurn(stem)
		}
	}
}

// setSpecError records err on the pointer; the condoc then waits for
// Resubmit.
func (s *Server) setSpecError(stem string, err error) {
	spec, rerr := s.readSpec(stem)
	if rerr != nil {
		log.Printf("condocv2 %s: recording error %q: %v", stem, err, rerr)
		return
	}
	spec.Error = err.Error()
	if werr := s.writeSpec(&spec, commitMsg(&spec, "error", 0)); werr != nil {
		log.Printf("condocv2 %s: %v", stem, werr)
	}
}

// slopResubmit clears a condoc's error so the tick (or the turn queue)
// tries again from the stage it stopped at.
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
	if spec.Phase == PhasePRDrafting && spec.PRWorkdir != "" {
		// A failed draft turn starts over with a fresh work signal.
		os.RemoveAll(spec.PRWorkdir)
		spec.PRRole, spec.PRSignalAt, spec.PRWorkdir = "", 0, ""
	}
	return s.writeSpec(&spec, commitMsg(&spec, "resubmit", 0))
}

// ---- the turn queue and turns ----

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
			log.Printf("condocv2 %s: turn: %v", stem, err)
			s.setSpecError(stem, err)
		}
		s.setRunning("")
		s.release(stem)
	}
}

// runTurn advances a submitted interaction stage by stage, from the queue to
// the unlock, saving after each so a failure or restart resumes from the
// stage it stopped at. The caller holds the claim.
func (s *Server) runTurn(stem string) error {
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase == PhasePRDrafting && spec.Error == "" {
		return s.runDraftTurn(&spec)
	}
	l := spec.Lock
	if l == nil || spec.Error != "" || isFinalPhase(spec.Phase) || stageOrder[l.Stage] < stageOrder[stageQueued] {
		return nil
	}
	n := l.Interaction
	id := spec.SlopspaceID
	for {
		meta, err := s.slop.ss.Get(id)
		if err != nil {
			return err
		}
		switch l.Stage {
		case stageQueued:
			deployed, err := s.slop.ss.GetDeployedID(dktypes.AgentTypeWorker)
			if err != nil {
				return err
			}
			if deployed != "" && deployed != id {
				return errTurnWait
			}
			if len(l.Bases) == 0 {
				// Bring in anything pushed to a target's branch from outside
				// before the base is taken, so the agent sees it and the
				// target commits don't get rejected.
				if !meta.Deployed {
					for _, repo := range spec.Targets {
						owner, name, _ := strings.Cut(repo, "/")
						if _, err := s.slop.ss.SyncRepo(id, owner, name); err != nil {
							return err
						}
					}
				}
				for _, repo := range spec.Targets {
					owner, name, _ := strings.Cut(repo, "/")
					sha, err := s.slop.ss.RepoHead(id, owner, name)
					if err != nil {
						return fmt.Errorf("%s: %v", repo, err)
					}
					l.Bases = append(l.Bases, repo+"@"+sha)
				}
			}
			if !meta.Deployed {
				if err := s.slop.ss.Deploy(id, dktypes.AgentTypeWorker); err != nil {
					return err
				}
			}
			l.Stage = stageDeployed

		case stageDeployed:
			if !meta.Deployed {
				return fmt.Errorf("slopspace %s isn't deployed", id)
			}
			deployRepos := filepath.Join(meta.DeployPath, dkslop.WriteSpacesDir, dkslop.ReposDir)
			prompt := buildInteractionPrompt(slopStepFile(meta, &spec), n) +
				slopspaceRepoNote(deployRepos, spec.Targets, spec.CondocRepo)
			role, at, err := s.submitSignal(&spec, dktypes.WorkTypeSlopspace, "", prompt)
			if err != nil {
				return err
			}
			l.Role, l.SignalAt = role, at
			l.Stage = stageSignalled

		case stageSignalled:
			reply, err := s.awaitSignal(l.Role, l.SignalAt)
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
			l.Stage = stageReturned

		case stageReturned:
			// The response commit.
			if meta.Deployed {
				return fmt.Errorf("slopspace %s is deployed again before the response was written", id)
			}
			reply, err := readFileString(filepath.Join(meta.RootPath, slopReplyFile))
			if err != nil {
				return fmt.Errorf("reading the captured reply: %v", err)
			}
			if err := appendSection(slopStepFile(meta, &spec), "Response", n, reply); err != nil {
				return err
			}
			if err := s.commitCondocRepo(&spec, commitMsg(&spec, "response", n)); err != nil {
				return err
			}
			os.Remove(filepath.Join(meta.RootPath, slopReplyFile))
			l.Stage = stageResponded

		case stageResponded:
			// Each target's commit -- the "other repo commits" the record maps.
			var heads []string
			for _, repo := range spec.Targets {
				owner, name, _ := strings.Cut(repo, "/")
				res, err := s.slop.ss.WriteRepoChanges(id, owner, name, commitMsg(&spec, "commit", n))
				if err != nil {
					return fmt.Errorf("%s: %v", repo, err)
				}
				heads = append(heads, repo+"@"+res.SHA)
			}
			l.Heads = heads
			l.Stage = stageMapped

		case stageMapped:
			// The record commit.
			if err := appendSection(slopStepFile(meta, &spec), "Record", n, recordBody(spec.Targets, l.Bases, l.Heads)); err != nil {
				return err
			}
			if err := s.commitCondocRepo(&spec, commitMsg(&spec, "record", n)); err != nil {
				return err
			}
			l.Stage = stageRecorded

		case stageRecorded:
			// The unlock commit ends the interaction.
			spec.Lock = nil
			spec.Interactions = n
			spec.Phase = PhaseAwaitingPrompt
			return s.writeSpec(&spec, commitMsg(&spec, "unlock", n))

		default:
			return fmt.Errorf("unknown interaction stage %q", l.Stage)
		}
		if err := s.writeSpec(&spec, ""); err != nil {
			return err
		}
	}
}

// submitSignal writes a turn's work signal -- the only thing condocv2 hands
// the worker -- and returns its role and creation second, which name its
// files: a clash moves to the next free second. workdir is the in-place
// work's location ("" for slopspace work).
func (s *Server) submitSignal(spec *PointerSpec, workType dktypes.WorkType, workdir, prompt string) (string, int64, error) {
	role := "condocv2-" + spec.Stem()
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
		return "", 0, err
	}
	return role, created.Unix(), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// awaitSignal waits for the worker to move the turn's signal to complete/
// and returns the agent's reply from it.
func (s *Server) awaitSignal(role string, signalAt int64) (string, error) {
	created := time.Unix(signalAt, 0)
	ongoing := filepath.Join(s.slop.dk.OngoingWorkDir(), worksignal.GenerateFilename(role, created, false))
	complete := filepath.Join(s.slop.dk.CompleteWorkDir(), worksignal.GenerateFilename(role, created, true))
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

// replyFromSignal is the agent's captured output; a failed run with no
// output says why.
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

// ---- the condoc view ----

// CondocState is the full detail of the selected condoc: its pointer, its
// files' text flow, and the visual timeline.
type CondocState struct {
	Entry        PointerEntry  `json:"entry"`
	MainContent  string        `json:"mainContent,omitempty"`
	StepContent  string        `json:"stepContent,omitempty"`
	Interactions []Interaction `json:"interactions"`
	// PendingPrompt is a locked prompt not yet in the step file.
	PendingPrompt string   `json:"pendingPrompt,omitempty"`
	Timeline      Timeline `json:"timeline"`
	// RecordPrompts is the visual's prompt column when the timeline comes
	// from the record (see record.go).
	RecordPrompts []Interaction `json:"recordPrompts,omitempty"`

	// Completion (see complete.go).
	DefaultPRGuidance string   `json:"defaultPrGuidance"`
	PRGuidance        string   `json:"prGuidance,omitempty"`
	PRTemplate        string   `json:"prTemplate,omitempty"`
	PRs               []PRLive `json:"prs,omitempty"`
}

// condocState reads a condoc's state from wherever its files are right now.
// A deploy or return moves the files, so a read that lands mid-move is
// retried briefly.
func (s *Server) condocState(stem string) (CondocState, error) {
	spec, err := s.readSpec(stem)
	if err != nil {
		return CondocState{}, err
	}
	st := CondocState{
		Entry:        s.decoratePointerEntry(entryFromSpec(spec, pointerDir+"/"+stem+".yaml")),
		Interactions: []Interaction{},

		DefaultPRGuidance: defaultPRGuidance,
		PRGuidance:        spec.PRGuidance,
		PRTemplate:        spec.PRTemplate,
		PRs:               s.prLiveFor(&spec),
	}
	if spec.Lock != nil && spec.Lock.Stage == stageLocked {
		st.PendingPrompt = spec.Lock.Prompt
	}
	// A merged condoc's slopspace is gone; its files live on in the merged
	// condoc repo (CondocURL points there), and both tabs are drawn from the
	// timeline record saved there on completion.
	if spec.merged() {
		if rec, rerr := s.timelineRecord(&spec); rerr == nil {
			st.Timeline = rec.Timeline
			st.Timeline.Source, st.Timeline.RecordURL = "record", recordURL(&spec)
			st.Interactions = rec.Interactions
			st.RecordPrompts = rec.Interactions
		} else {
			st.Timeline = s.buildTimeline(&spec, nil)
			st.Timeline.Errors = append(st.Timeline.Errors, fmt.Sprintf("only the pointer lane can be drawn: no timeline record (%v)", rerr))
		}
	} else if spec.SlopspaceID != "" {
		var meta *dktypes.SlopspaceMetadata
		for attempt := 0; ; attempt++ {
			if meta, err = s.slop.ss.Get(spec.SlopspaceID); err != nil {
				return CondocState{}, err
			}
			root := slopCondocRoot(meta, spec.CondocRepo)
			main := filepath.Join(root, filepath.FromSlash(spec.CondocPath))
			mainContent, merr := readFileString(main)
			stepContent, serr := readFileString(stepFilePath(main))
			if merr == nil && serr == nil {
				st.MainContent, st.StepContent = mainContent, stepContent
				st.Interactions = parseInteractions(stepContent)
				break
			}
			if attempt >= 4 {
				if merr != nil {
					return CondocState{}, merr
				}
				return CondocState{}, serr
			}
			time.Sleep(250 * time.Millisecond)
		}
		st.Timeline = s.buildTimeline(&spec, meta)
	} else {
		st.Timeline = s.buildTimeline(&spec, nil)
	}
	return st, nil
}
