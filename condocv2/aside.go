package main

// Asides (dttVisualsImpls Step 2-1): a read-only question to the agent about
// a condoc, kept off the condoc. The agent sees the condoc as an interaction
// would -- its step files, its repos, its read-only repos -- but runs in
// ambiguous-agent's read mode, and nothing it says goes into the condoc:
// no prompt, response or record section, no commit, nothing on the pointer.
//
// An aside is kept only in this host's local history, one JSON file per
// aside under --asides-dir (by default ~/.ufa/condocv2/asides/):
//
//	<asides-dir>/<pointer stem>/aside-<A>.json
//
// The pointer stem (<Name>-<startTime>) is what correlates it back to its
// condoc; the interactions completed and the scope when it ran, and its
// times, place it among the condoc's interactions on both tabs. A is 1, 2,
// ... per condoc.
//
// Each aside runs through the turn queue like an interaction's turn (keyed
// asideQueuePrefix+stem), holding the condoc's claim while it runs so nothing
// else deploys the slopspace or moves the condoc's files under it. It runs
// as an interaction's turn does -- the slopspace deployed, a slopspace work
// signal, the return -- so its agent sees everything an interaction's does
// (the repos, the read-only repos, the DTT canvas and its images), only in
// read mode (ambiguous-agent's -r for a turn's -w) and with nothing
// committed after. Once it is done, every repo is checked for changes the
// agent made anyway (Aside.Changed): they are reported, not undone.
//
// Later agent runs see the asides done before them as Markdown in the
// deployed slopspace's readspaces/condoc-asides/ (thrown away with the
// readspaces on return, so never in a repo). Their prompts say where.
//
// None of this is in the condoc's model or its timeline record: CondocState
// carries the asides (the Text tab draws each after the interaction it
// followed), and ops.go's asideOps puts each in the Visual tab's local
// operations column, a red bubble saying "aside".

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	dkslop "dungeon-keeper/pkg/slopspace"
	dktypes "dungeon-keeper/pkg/types"
	"dungeon-keeper/pkg/worksignal"
)

const (
	asideQueuePrefix = "aside:"        // an aside's entry in the turn queue
	asideReadDir     = "condoc-asides" // where later agent runs find the asides, as Markdown
	asideMode        = "read"          // ambiguous-agent's read-only mode (-r; a turn's "write" is -w)
	maxAsidePrompt   = 20000           // characters
)

// Aside statuses.
const (
	asideQueued    = "queued"    // waiting in the turn queue
	asideRunning   = "running"   // its work signal is out
	asideCompleted = "completed" // answered
	asideFailed    = "failed"    // the run failed, or condocv2 couldn't run it
	asideCancelled = "cancelled" // cancelled before it started
)

var asideFileRe = regexp.MustCompile(`^aside-(\d+)\.json$`)

// Aside is one aside, as kept in its history file and sent to the UI.
type Aside struct {
	Number      int      `json:"number"` // A: 1, 2, ... per condoc
	Stem        string   `json:"stem"`   // the condoc's pointer stem
	Interaction int      `json:"interaction"`
	Step        string   `json:"step,omitempty"` // the condoc's scope ("" the main scope)
	Prompt      string   `json:"prompt"`
	Status      string   `json:"status"`
	AskedAt     int64    `json:"askedAt"`
	StartedAt   int64    `json:"startedAt,omitempty"`
	CompletedAt int64    `json:"completedAt,omitempty"`
	Agent       string   `json:"agent,omitempty"`
	Model       string   `json:"model,omitempty"`
	Role        string   `json:"role,omitempty"`     // with SignalAt, names its work signal
	SignalAt    int64    `json:"signalAt,omitempty"` // unix seconds
	Workdir     string   `json:"workdir,omitempty"`  // an in-place run's, from before asides deployed the slopspace
	Deployed    bool     `json:"deployed,omitempty"` // it deployed the slopspace, so it returns it
	Reply       string   `json:"reply,omitempty"`
	Changed     []string `json:"changed,omitempty"` // repos the agent changed despite read mode
	Error       string   `json:"error,omitempty"`

	// The repos' status before the run (repo -> git status --porcelain),
	// to find what it changed.
	Before map[string]string `json:"before,omitempty"`
}

// asideRole is the work-signal role of a condoc's asides; ops.go's agentOps
// (role "condocv2-<stem>") never picks them up.
func asideRole(stem string) string {
	return "condocv2-aside-" + stem
}

// defaultAsidesDir is where asides are kept unless --asides-dir says
// otherwise: ~/.ufa/condocv2/asides, beside the other ~/.ufa state (dtt's,
// composer's).
func defaultAsidesDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ufa", "condocv2", "asides")
}

// ---- the history files ----

func (s *Server) asideDir(stem string) string {
	return filepath.Join(s.slop.asidesDir, stem)
}

func (s *Server) asidePath(stem string, n int) string {
	return filepath.Join(s.asideDir(stem), fmt.Sprintf("aside-%d.json", n))
}

// asides reads a condoc's asides, in order. None when asides are off.
func (s *Server) asides(stem string) []Aside {
	if s.slop.asidesDir == "" || !validStem(stem) {
		return nil
	}
	s.slop.asideMu.Lock()
	defer s.slop.asideMu.Unlock()
	return s.readAsidesLocked(stem)
}

func (s *Server) readAsidesLocked(stem string) []Aside {
	entries, err := os.ReadDir(s.asideDir(stem))
	if err != nil {
		return nil
	}
	var out []Aside
	for _, e := range entries {
		if !asideFileRe.MatchString(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.asideDir(stem), e.Name()))
		if err != nil {
			continue
		}
		var a Aside
		if err := json.Unmarshal(b, &a); err != nil || a.Number == 0 {
			log.Printf("condocv2 aside %s/%s: unreadable, skipped", stem, e.Name())
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

func (s *Server) saveAside(a *Aside) error {
	s.slop.asideMu.Lock()
	defer s.slop.asideMu.Unlock()
	return s.saveAsideLocked(a)
}

func (s *Server) saveAsideLocked(a *Aside) error {
	if err := os.MkdirAll(s.asideDir(a.Stem), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	path := s.asidePath(a.Stem, a.Number)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- asking ----

// asideBlocked reports why a condoc can't take an aside now ("" when it can):
// it needs a slopspace it can deploy, so from begin until the PRs are drafted,
// but not while it is held for DTT proposals -- dungeon-keeper won't deploy
// the slopspace until they are decided, and a deployment would put itself
// between the hold and the decisions it reads (review.go's advanceHold).
func asideBlocked(spec *PointerSpec) string {
	switch {
	case spec.SlopspaceID == "":
		return "it has no slopspace yet"
	case spec.Phase == PhaseReviewing:
		return "it is held for its DTT proposals; ask once they are decided"
	case spec.Phase != PhaseAwaitingPrompt && spec.Phase != PhaseLocked:
		return fmt.Sprintf("it is %s", spec.Phase)
	}
	return ""
}

// slopAside asks an aside: it is written to the condoc's history and queued.
// It doesn't take the claim -- nothing in the condoc changes -- so it can be
// asked while an interaction runs, and waits its turn behind it.
func (s *Server) slopAside(stem, text string) error {
	if s.slop.asidesDir == "" {
		return fmt.Errorf("asides are off: --asides-dir is blank")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("the aside is empty")
	}
	if len(text) > maxAsidePrompt {
		return fmt.Errorf("the aside is %d characters; at most %d", len(text), maxAsidePrompt)
	}
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if why := asideBlocked(&spec); why != "" {
		return fmt.Errorf("%s can't take an aside: %s", spec.Name, why)
	}
	s.slop.asideMu.Lock()
	n := 1
	for _, a := range s.readAsidesLocked(stem) {
		if a.Number >= n {
			n = a.Number + 1
		}
	}
	a := Aside{
		Number:      n,
		Stem:        stem,
		Interaction: spec.Interactions,
		Step:        spec.Step,
		Prompt:      text,
		Status:      asideQueued,
		AskedAt:     time.Now().Unix(),
	}
	err = s.saveAsideLocked(&a)
	s.slop.asideMu.Unlock()
	if err != nil {
		return fmt.Errorf("saving aside %d: %v", n, err)
	}
	log.Printf("condocv2 %s: aside %d queued", stem, n)
	s.enqueue(asideQueuePrefix + stem)
	return nil
}

// slopCancelAside stops an aside: one still queued is cancelled outright,
// a running one's agent is asked to stop as for Cancel turn (its signal's
// cancel_requested event), and the aside fails with what it said so far.
func (s *Server) slopCancelAside(stem string, n int) error {
	s.slop.asideMu.Lock()
	var a *Aside
	for _, x := range s.readAsidesLocked(stem) {
		if x.Number == n {
			x := x
			a = &x
		}
	}
	if a == nil {
		s.slop.asideMu.Unlock()
		return fmt.Errorf("%s has no aside %d", stem, n)
	}
	if a.Status == asideQueued && a.Role == "" {
		a.Status, a.CompletedAt, a.Error = asideCancelled, time.Now().Unix(), "cancelled before it started"
		err := s.saveAsideLocked(a)
		s.slop.asideMu.Unlock()
		return err
	}
	s.slop.asideMu.Unlock()
	if a.Status != asideRunning || a.Role == "" {
		return fmt.Errorf("aside %d isn't running", n)
	}
	ongoing, complete := s.signalPaths(a.Role, a.SignalAt)
	_, events, err := s.slop.signals.Read(ongoing)
	if err != nil {
		if fileExists(complete) {
			return nil
		}
		return err
	}
	if worksignal.CancelRequest(events) != nil {
		return nil
	}
	log.Printf("condocv2 %s: cancelling aside %d (%s)", stem, n, filepath.Base(ongoing))
	return s.slop.signals.RequestCancel(ongoing, "Cancel aside in condocv2")
}

// resumeAsides queues a held condoc's asides a restart left queued or
// running (the tick calls it).
func (s *Server) resumeAsides(stem string) {
	for _, a := range s.asides(stem) {
		if a.Status == asideQueued || a.Status == asideRunning {
			s.enqueue(asideQueuePrefix + stem)
			return
		}
	}
}

// ---- running ----

// runAsides runs the condoc's waiting asides, oldest first. The caller holds
// the claim. A failure is the aside's own: the condoc never records it.
// errTurnWait means another condoc's slopspace is deployed: the aside stays
// queued, for the turn loop to come back to.
func (s *Server) runAsides(stem string) error {
	for {
		var next *Aside
		for _, a := range s.asides(stem) {
			if a.Status == asideQueued || a.Status == asideRunning {
				a := a
				next = &a
				break
			}
		}
		if next == nil {
			return nil
		}
		if err := s.runAside(next); errors.Is(err, errTurnWait) {
			return err
		} else if err != nil {
			log.Printf("condocv2 %s: aside %d: %v", stem, next.Number, err)
			s.returnAsideSlopspace(next)
			if next.Workdir != "" {
				os.RemoveAll(next.Workdir)
				next.Workdir = ""
			}
			next.Status, next.Error, next.CompletedAt = asideFailed, err.Error(), time.Now().Unix()
			next.Before = nil
			if err := s.saveAside(next); err != nil {
				log.Printf("condocv2 %s: aside %d: %v", stem, next.Number, err)
				return nil
			}
		}
		s.broadcastCondocUpdate(stem)
	}
}

// runAside runs one aside to its answer: the slopspace deployed as for an
// interaction, its work signal, the wait, then the return. A restart while it
// waits picks the wait up again.
func (s *Server) runAside(a *Aside) error {
	spec, err := s.readHeldSpec(a.Stem)
	if err != nil {
		return err
	}
	id := spec.SlopspaceID
	meta, err := s.slop.ss.Get(id)
	if err != nil {
		return err
	}
	if a.Role == "" {
		if why := asideBlocked(&spec); why != "" {
			return fmt.Errorf("%s can't take an aside now: %s", spec.Name, why)
		}
		// Placed where it runs: after the interactions completed by now.
		a.Interaction, a.Step = spec.Interactions, spec.Step
		a.Before = repoStatuses(meta, &spec)
		if !meta.Deployed {
			deployed, err := s.slop.ss.GetDeployedID(dktypes.AgentTypeWorker)
			if err != nil {
				return err
			}
			if deployed != "" {
				return errTurnWait
			}
			// The same deploy an interaction gets, so the agent sees what an
			// interaction's would: the repos, the read-only repos (pulled
			// afresh, as on every deploy), the DTT canvas. Nothing comes of it
			// but the return -- no commit, nothing on the pointer.
			if err := s.slop.ss.Deploy(id, dktypes.AgentTypeWorker); err != nil {
				return err
			}
			a.Deployed = true
			if err := s.saveAside(a); err != nil {
				return err
			}
			if meta, err = s.slop.ss.Get(id); err != nil {
				return err
			}
		}
		// Otherwise it is deployed already -- an interaction that failed
		// midway left it so -- and it is left deployed after.
		if !meta.Deployed || meta.DeployPath == "" {
			return fmt.Errorf("slopspace %s isn't deployed", id)
		}
		earlier := s.doneAsidesBefore(a.Stem, a.Number)
		asidesDir := filepath.Join(meta.DeployPath, dkslop.ReadSpacesDir, asideReadDir)
		if err := writeAsideFiles(asidesDir, earlier); err != nil {
			return err
		}
		a.Agent, a.Model = s.runAgent(&spec)
		prompt := buildAsidePrompt(slopStepFile(meta, &spec), a) +
			scopeNote(slopMainFile(meta, &spec), spec.Step) +
			asideRepoNote(filepath.Join(meta.DeployPath, dkslop.WriteSpacesDir, dkslop.ReposDir), spec.Targets, spec.CondocRepo) +
			readOnlyRepoNote(filepath.Join(meta.DeployPath, dkslop.ReadSpacesDir, dkslop.ReposDir), meta.ReadspaceRepos) +
			asidesNote(asidesDir, len(earlier)) +
			dkslop.DTTPromptNote(meta.DeployPath, false) // the canvas only: an aside proposes nothing
		// Slopspace work, as an interaction's turn is, in read mode
		// (ambiguous-agent's -r where a turn's is -w).
		role, at, err := s.submitSignalAs(&spec, asideRole(a.Stem), asideMode, dktypes.WorkTypeSlopspace, "", prompt)
		if err != nil {
			return err
		}
		a.Role, a.SignalAt, a.StartedAt, a.Status = role, at, at, asideRunning
		if err := s.saveAside(a); err != nil {
			return err
		}
		s.broadcastCondocUpdate(a.Stem)
	}
	reply, err := s.awaitSignal(a.Role, a.SignalAt)
	if err != nil {
		return err
	}
	a.Reply, a.Status = reply, asideCompleted
	_, complete := s.signalPaths(a.Role, a.SignalAt)
	if signal, _, err := s.slop.signals.Read(complete); err == nil && signal.Status == dktypes.WorkStatusFailed {
		a.Status = asideFailed
	}
	returned := a.Deployed
	if a.Deployed {
		if err := s.slop.ss.Return(id); err != nil {
			return fmt.Errorf("returning slopspace %s: %v", id, err)
		}
		a.Deployed = false
	}
	if meta, err := s.slop.ss.Get(id); err == nil && a.Before != nil {
		after := repoStatuses(meta, &spec)
		for _, repo := range spec.Repos() {
			if after[repo] != a.Before[repo] {
				a.Changed = append(a.Changed, repo)
			}
		}
		// DTT proposals left despite read mode would hold the condoc for
		// review as if an interaction had made them.
		if n := len(meta.Deployments); returned && n > 0 && len(meta.Deployments[n-1].Proposals) > 0 {
			a.Changed = append(a.Changed, "dtt-requests")
		}
	}
	if len(a.Changed) > 0 {
		log.Printf("condocv2 %s: aside %d: the agent changed %s despite read mode (left as it is)", a.Stem, a.Number, strings.Join(a.Changed, ", "))
	}
	if a.Workdir != "" {
		// An aside from before asides ran in the deployed slopspace.
		os.RemoveAll(a.Workdir)
		a.Workdir = ""
	}
	a.Before = nil
	a.CompletedAt = time.Now().Unix()
	return s.saveAside(a)
}

// returnAsideSlopspace returns the slopspace a failed aside deployed, so the
// condoc's next interaction can deploy it.
func (s *Server) returnAsideSlopspace(a *Aside) {
	if !a.Deployed {
		return
	}
	spec, err := s.readHeldSpec(a.Stem)
	if err != nil {
		return
	}
	if meta, err := s.slop.ss.Get(spec.SlopspaceID); err == nil && meta.Deployed {
		if err := s.slop.ss.Return(spec.SlopspaceID); err != nil {
			log.Printf("condocv2 %s: aside %d: returning slopspace %s: %v", a.Stem, a.Number, spec.SlopspaceID, err)
			return
		}
	}
	a.Deployed = false
}

// repoStatuses is each repo's git status --porcelain where its work tree
// is now; a repo it can't read is left out.
func repoStatuses(meta *dktypes.SlopspaceMetadata, spec *PointerSpec) map[string]string {
	out := map[string]string{}
	for _, repo := range spec.Repos() {
		b, err := exec.Command("git", "--git-dir", slopGitDir(meta, repo), "--work-tree", slopCondocRoot(meta, repo),
			"status", "--porcelain").Output()
		if err == nil {
			out[repo] = string(b)
		}
	}
	return out
}

// doneAsidesBefore is the condoc's answered asides numbered below n.
func (s *Server) doneAsidesBefore(stem string, n int) []Aside {
	var out []Aside
	for _, a := range s.asides(stem) {
		if a.Number < n && (a.Status == asideCompleted || a.Status == asideFailed) && a.Reply != "" {
			out = append(out, a)
		}
	}
	return out
}

// ---- what the agent sees ----

func buildAsidePrompt(stepPath string, a *Aside) string {
	return fmt.Sprintf("You are answering aside %d on a condoc: a read-only side question from the human, asked after "+
		"interaction %d. The condoc's step file is at '%s'. Read it for context: its '## Prompt', '## Response', "+
		"'## Proposals', '## Record', '## DTT', '## Result' and '## Resource' sections are the conversation so far. "+
		"IMPORTANT: this is READ-ONLY. Do NOT create, modify or delete any file -- not the condoc, not any repo, not "+
		"anything else -- and do not commit, push or run anything that changes state. Read what you need and answer. "+
		"Your terminal response is kept as the aside's answer, outside the condoc: it is not added to the step file "+
		"or to any repo, though later agent runs on this condoc can read it.\n\nAside %d:\n\n%s",
		a.Number, a.Interaction, stepPath, a.Number, a.Prompt)
}

// asideRepoNote tells the aside's agent where the condoc's repos are in the
// deployed slopspace, and that they're for reading only.
func asideRepoNote(deployRepos string, targets []string, condocRepo string) string {
	return fmt.Sprintf("\n\nThe condoc's repos are checked out under '%s': %s, with the condoc files in %s. Read them; change nothing.",
		deployRepos, strings.Join(targets, ", "), condocRepo)
}

// asidesNote tells an agent run where the earlier asides are.
func asidesNote(dir string, n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("\n\nThe human has also asked this condoc %d read-only aside(s): side questions answered by an agent, "+
		"kept off the condoc and out of every repo. They are under '%s', one Aside-<A>.md each (its question and answer), "+
		"with when it was asked relative to the interactions. Read them if they bear on your work; do not cite them as "+
		"part of the condoc.", n, dir)
}

// writeAsideFiles writes each aside as Markdown into dir.
func writeAsideFiles(dir string, asides []Aside) error {
	if len(asides) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, a := range asides {
		if err := writeFileString(filepath.Join(dir, "Aside-"+strconv.Itoa(a.Number)+".md"), asideMarkdown(a)); err != nil {
			return err
		}
	}
	return nil
}

func asideMarkdown(a Aside) string {
	scope := "the main scope"
	if a.Step != "" {
		scope = scopeTitle(a.Step)
	}
	when := ""
	if a.StartedAt != 0 {
		when = ", " + time.Unix(a.StartedAt, 0).UTC().Format(time.RFC3339)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Aside %d\n\nAsked after interaction %d, in %s%s.", a.Number, a.Interaction, scope, when)
	if a.Status == asideFailed {
		b.WriteString(" The run failed.")
	}
	fmt.Fprintf(&b, "\n\n## Question\n\n%s\n\n## Answer\n\n%s\n", a.Prompt, a.Reply)
	return b.String()
}

// noteAsidesForTurn puts the condoc's answered asides into an interaction's
// deployed readspaces and returns the prompt's note on them. They go with
// the readspaces on return, so never into a repo. A failure leaves them out.
func (s *Server) noteAsidesForTurn(meta *dktypes.SlopspaceMetadata, spec *PointerSpec) string {
	if s.slop.asidesDir == "" || !meta.Deployed || meta.DeployPath == "" {
		return ""
	}
	done := s.doneAsidesBefore(spec.Stem(), int(^uint(0)>>1))
	dir := filepath.Join(meta.DeployPath, dkslop.ReadSpacesDir, asideReadDir)
	if err := writeAsideFiles(dir, done); err != nil {
		log.Printf("condocv2 %s: writing the asides for the agent: %v (left out)", spec.Stem(), err)
		return ""
	}
	return asidesNote(dir, len(done))
}
