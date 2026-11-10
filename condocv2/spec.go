package main

// The condocv2 pointer spec: one pointers/<Name>-<startTime>.yaml per v2
// condoc in the pointer repo. "condocVersion: 2" and controlScheme
// slopspace-v2 mark a v2 pointer, and condocv2 lists nothing else (see
// isCondocV2Pointer) -- older v1 pointers may still sit in the same pointers/
// directory and are skipped. The condoc files carry a condocv2-yaml header
// (see condocfiles.go) and the branches live under condocv2/.
//
// The static part is set by the proposal; the runtime part (slopspace,
// holder, phase, the interaction lock) is kept current by the condocv2
// holding the condoc.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	condocVersion       = "2"
	condocControlScheme = "slopspace-v2"
	// condocBranchPrefix namespaces every v2 condoc branch: condocv2 only
	// accepts branches under it.
	condocBranchPrefix = "condocv2/"
)

// condocNamePattern is a condoc name: it becomes a file name, a branch
// segment and a work-signal role, so it stays plain.
var condocNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// condocV2PointerRe recognizes a v2 pointer file by its version marker.
var condocV2PointerRe = regexp.MustCompile(`(?m)^condocVersion:\s*["']?2["']?\s*(?:#.*)?$`)

// isCondocV2Pointer reports whether a pointer file's content is a v2 pointer.
func isCondocV2Pointer(content string) bool {
	return condocV2PointerRe.MatchString(content)
}

// Pointer phases.
const (
	PhaseProposed       = "proposed"        // a proposal: no slopspace yet
	PhaseAwaitingPrompt = "awaiting_prompt" // begun and unlocked: ready for the next prompt
	PhaseLocked         = "locked"          // an interaction holds the lock (see InteractionLock)
	PhaseReviewing      = "reviewing"       // DTT proposals came back: held for the human (see ProposalHold, review.go)

	// Completion and re-integration (see complete.go).
	PhasePRDrafting = "pr_drafting" // completed: the agent is drafting the PRs
	PhasePRProposed = "pr_proposed" // the drafts are ready to edit and raise
	PhasePROpen     = "pr_open"     // the PRs are raised: Merge when all are mergeable
	PhaseMerged     = "merged"      // merged, branches and slopspace removed

	// The final states (see final.go): markers only -- nothing but the
	// pointer changes, and the UI hides them unless "show final" is on.
	PhaseArchived  = "archived"  // put away after the merge
	PhaseAbandoned = "abandoned" // forced to an end before merge (Unabandon undoes it)
)

// isFinalPhase reports whether a phase is one condocv2 doesn't drive on from
// (only Unabandon, by hand, leaves one).
func isFinalPhase(phase string) bool {
	return phase == PhaseArchived || phase == PhaseAbandoned
}

// Interaction stages, in order. An interaction is the one linear step of a v2
// condoc repeated: lock (pointer) -> prompt commit (condoc) -> the agent ->
// response commit (condoc) -> each target's commit -> record commit (condoc)
// -> unlock (pointer). Stage records how far it got, so an error or a restart
// resumes it where it stopped.
const (
	stageLocked    = "locked"    // the lock is committed; the prompt isn't in the condoc yet
	stagePrompted  = "prompted"  // the prompt commit is in; waiting for a manual submit
	stageQueued    = "queued"    // submitted: waiting its turn in the queue
	stageDeployed  = "deployed"  // the slopspace is deployed at the worker's deploy path
	stageSignalled = "signalled" // the work signal is out; waiting for the agent
	stageReturned  = "returned"  // the reply is captured and the slopspace returned
	stageResponded = "responded" // the response commit is in
	stageMapped    = "mapped"    // every target's commit is in
	stageRecorded  = "recorded"  // the record commit is in; unlock next
)

// stageOrder ranks the stages, for "has the interaction got this far".
var stageOrder = map[string]int{
	stageLocked: 0, stagePrompted: 1, stageQueued: 2, stageDeployed: 3, stageSignalled: 4,
	stageReturned: 5, stageResponded: 6, stageMapped: 7, stageRecorded: 8,
}

// InteractionLock is the interaction in flight. Its presence in the pointer
// is the lock: committed when the interaction starts and cleared, in its own
// commit, when it ends.
type InteractionLock struct {
	Interaction int
	Stage       string
	AutoSubmit  bool     // submit to the agent as soon as the prompt is committed (once, for this interaction)
	Prompt      string   // the prompt text until the prompt commit has it
	Role        string   // the work signal's role; with SignalAt it names the signal file
	SignalAt    int64    // the work signal's CreatedAt (unix seconds)
	Bases       []string // "owner/repo@sha" of each target before the agent ran
	Heads       []string // "owner/repo@sha" of each target after its commit
}

// Tool call (sub-iteration) stages, in order (see review.go).
const (
	toolLocked   = "locked"   // lock N.k is committed; the proposal commit is next
	toolProposed = "proposed" // the proposal commit is in; an accepted one runs next, a rejected one unlocks
	toolRan      = "ran"      // dtt has run; the image commit is next
	toolImaged   = "imaged"   // the image commit is in; unlock N.k next
)

// ProposalHold is the hold on a condoc whose last interaction came back with
// DTT proposals: no prompt and no Complete until every one is accepted or
// rejected (or changes are asked for, which is the next interaction). It is
// set by that interaction's unlock commit itself, and cleared by the unlock
// of the last proposal's sub-iteration.
type ProposalHold struct {
	Interaction int       // N: the interaction whose turn proposed them
	Proposals   []string  // relative to writespaces/dtt-requests, e.g. "write/repo.dtt.yaml"
	Done        []string  // the proposals whose sub-iteration has unlocked, in order
	PreReads    []string  // the proposals read before deciding, one per such sub-iteration, in order
	Call        *ToolCall // the sub-iteration in flight; its presence is lock N.k
}

// nextSub is k for the next sub-iteration: every answer and pre-read so far
// had one.
func (h *ProposalHold) nextSub() int {
	return len(h.Done) + len(h.PreReads) + 1
}

// decisionPreRead is a ToolCall's Decision when it reads a write proposal
// before the human decides it (privateRepoOpsImpls Step 4, Revision B): it
// runs with dtt read, and the proposal still awaits accept or reject.
const decisionPreRead = "read"

// ToolCall is one sub-iteration, N.k: one proposal, accepted or rejected --
// or read before deciding.
//
//	lock N.k (pointer)
//	  -> the proposal commit (condoc): "dtt-read|dtt-assert|dtt-delete|dtt-reject|dtt-preread N.k"
//	  -> accepted or pre-read: dtt runs it, once that commit is pushed
//	  -> accepted or pre-read: the image commit (condoc): "dtt-image N.k", or "dtt-failed N.k"
//	  -> unlock N.k (pointer)
type ToolCall struct {
	Sub      int // k
	Proposal string
	Decision string // accept, reject, or read (decisionPreRead)
	Mode     string // accepted: read, assert or delete; pre-read: read
	Stage    string
	Image    string // ran: the image dtt wrote, in the slopspace ("" when there is none)
	Failed   string // ran: why there's no good image
}

// runs reports whether the call runs dtt: accepted, or pre-read.
func (c *ToolCall) runs() bool {
	return c.Decision == "accept" || c.Decision == decisionPreRead
}

// pending lists the proposals no sub-iteration has taken yet -- a pre-read
// doesn't take one.
func (h *ProposalHold) pending() []string {
	taken := map[string]bool{}
	for _, p := range h.Done {
		taken[p] = true
	}
	if h.Call != nil && h.Call.Decision != decisionPreRead {
		taken[h.Call.Proposal] = true
	}
	var out []string
	for _, p := range h.Proposals {
		if !taken[p] {
			out = append(out, p)
		}
	}
	return out
}

// PointerSpec is a v2 pointer file in full.
type PointerSpec struct {
	Name          string
	StartTime     int64
	ControlScheme string
	Branch        string
	CondocRepo    string
	CondocPath    string // within the condoc repo
	CondocURL     string // the condoc on its branch
	Targets       []string
	ReadOnly      []ReadOnlyRepo // repos the agent reads but never changes (see readonly.go)
	Agent         string
	Model         string
	Description   string

	SlopspaceID string
	Holder      string   // the condocv2 driving this condoc (see slopHolder)
	Bases       []string // "owner/repo@sha" where each repo's branch began

	// Every new commit a read-only repo's ref resolved to, oldest first: at
	// begin, then whenever a turn's deploy or return pulled a new one (each
	// its own "readspace N" commit, see readonly.go).
	ReadResolutions []ReadResolution

	// The configuration in flight -- its presence is configure-lock N.c --
	// and every one done, oldest first (see configure.go).
	Configure      *RepoConfigure
	Configurations []RepoConfiguration

	// Every repo branch found moved from outside condocv2 between
	// interactions, oldest first (see external.go).
	Externals []ExternalChange

	// The scope interactions go into now -- "" the main scope, else a step's
	// path ("1", "1-1", ...) -- and every step entered, in order (see
	// steps.go).
	Step  string
	Steps []StepScope

	// Every resource added, in order (see resources.go).
	Resources []Resource

	Phase       string
	Interactions int // completed interactions
	Lock         *InteractionLock
	Hold         *ProposalHold

	// Completion: the guidance the PR drafts were asked for with, the draft
	// turn's work signal (role + signalAt) and tempdir while it runs, the
	// drafts (prs.yaml), then the raised PRs.
	PRGuidance string
	PRRole     string
	PRSignalAt int64
	PRWorkdir  string
	PRTemplate string
	PRs        []PointerPR

	// What the merged commit keeps of the completion (see Finalization).
	Finalization *Finalization

	// The final-state marker: the phase the condoc left and when.
	FinalFrom string
	FinalAt   string

	Error     string
	UpdatedAt string
}

// PointerPR is a raised pull request.
type PointerPR struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"` // "open" or "merged", as last recorded
}

// Finalization is written by the pointer's merged commit, once GitHub has
// confirmed every merge: the condoc's final state and the visual tab's
// completion nodes that nothing else keeps once the slopspace is gone -- the
// condoc lane's complete and prs commits, and each repo's merge commit. (The
// timeline record is saved at complete, so it stops before them; the
// pointer's own completion commits are read from its history.)
type Finalization struct {
	State     string // "merged"
	At        string // when the last merge was confirmed (RFC 3339)
	RecordURL string // the timeline record
	Events    []FinalEvent
}

// FinalEvent is one kept timeline node; its URL is its commit's.
type FinalEvent struct {
	Lane string // "condoc", or a target's owner/name
	Repo string
	Kind string // complete, prs, or merged
	SHA  string
	Time int64
	PR   string // merged: the PR it merged
}

// Stem is the pointer file's name without extension.
func (p *PointerSpec) Stem() string {
	return fmt.Sprintf("%s-%d", p.Name, p.StartTime)
}

// merged reports whether the condoc's PRs were merged -- and so its slopspace
// deleted -- whatever final state it has moved to since.
func (p *PointerSpec) merged() bool {
	return p.Phase == PhaseMerged || p.FinalFrom == PhaseMerged
}

// Repos is every repo the condoc touches: the targets, then the condoc repo.
func (p *PointerSpec) Repos() []string {
	return append(append([]string{}, p.Targets...), p.CondocRepo)
}

func (p *PointerSpec) render() string {
	var w yamlWriter
	w.line(0, "# condoc v2 pointer, kept by condocv2")
	w.field(0, "condocVersion", condocVersion, true)
	w.field(0, "name", p.Name, true)
	w.field(0, "startTime", strconv.FormatInt(p.StartTime, 10), true)
	w.field(0, "controlScheme", p.ControlScheme, true)
	w.field(0, "branch", p.Branch, true)
	w.field(0, "condocRepo", p.CondocRepo, true)
	w.field(0, "condocPath", p.CondocPath, false)
	w.field(0, "condocUrl", p.CondocURL, false)
	if len(p.Targets) == 0 {
		w.line(0, "targets: []")
	} else {
		w.line(0, "targets:")
		for _, t := range p.Targets {
			w.line(2, "- repo: "+yamlScalar(t))
		}
	}
	if len(p.ReadOnly) > 0 {
		w.line(0, "readOnly:")
		for _, r := range p.ReadOnly {
			w.line(2, "- repo: "+yamlScalar(r.Repo))
			w.field(4, "ref", r.Ref, false)
		}
	}
	w.field(0, "agent", p.Agent, false)
	w.field(0, "model", p.Model, false)
	w.field(0, "description", p.Description, false)
	w.field(0, "slopspaceId", p.SlopspaceID, false)
	w.field(0, "holder", p.Holder, false)
	writeList(&w, 0, "bases", p.Bases)
	if len(p.ReadResolutions) > 0 {
		w.line(0, "readResolutions:")
		for _, r := range p.ReadResolutions {
			w.line(2, "- repo: "+yamlScalar(r.Repo))
			w.field(4, "ref", r.Ref, false)
			w.field(4, "sha", r.SHA, true)
			w.field(4, "interaction", strconv.Itoa(r.Interaction), true)
			w.field(4, "at", r.At, true)
			if r.Time != 0 {
				w.field(4, "time", strconv.FormatInt(r.Time, 10), true)
			}
		}
	}
	if c := p.Configure; c != nil {
		w.line(0, "configure:")
		w.field(2, "interaction", strconv.Itoa(c.Interaction), true)
		w.field(2, "seq", strconv.Itoa(c.Seq), true)
		w.field(2, "kind", c.Kind, false)
		w.field(2, "repo", c.Repo, true)
		w.field(2, "from", c.From, false)
		w.field(2, "to", c.To, false)
		w.field(2, "stage", c.Stage, true)
		w.field(2, "sha", c.SHA, false)
		w.field(2, "failed", c.Failed, false)
	}
	if len(p.Configurations) > 0 {
		w.line(0, "configurations:")
		for _, c := range p.Configurations {
			w.line(2, "- repo: "+yamlScalar(c.Repo))
			w.field(4, "interaction", strconv.Itoa(c.Interaction), true)
			w.field(4, "seq", strconv.Itoa(c.Seq), true)
			w.field(4, "kind", c.Kind, false)
			w.field(4, "from", c.From, false)
			w.field(4, "to", c.To, false)
			w.field(4, "sha", c.SHA, false)
			w.field(4, "failed", c.Failed, false)
			w.field(4, "at", c.At, false)
		}
	}
	if len(p.Externals) > 0 {
		w.line(0, "externals:")
		for _, x := range p.Externals {
			w.line(2, "- repo: "+yamlScalar(x.Repo))
			w.field(4, "interaction", strconv.Itoa(x.Interaction), true)
			w.field(4, "seq", strconv.Itoa(x.Seq), true)
			w.field(4, "from", x.From, true)
			w.field(4, "to", x.To, true)
			w.field(4, "commits", strconv.Itoa(x.Commits), true)
			w.field(4, "at", x.At, true)
			if x.Time != 0 {
				w.field(4, "time", strconv.FormatInt(x.Time, 10), true)
			}
		}
	}
	if len(p.Steps) > 0 {
		w.line(0, "steps:")
		for _, s := range p.Steps {
			w.line(2, "- path: "+yamlScalar(s.Path))
			w.field(4, "title", s.Title, false)
			w.field(4, "from", strconv.Itoa(s.From), true)
			if s.EnteredAt != 0 {
				w.field(4, "enteredAt", strconv.FormatInt(s.EnteredAt, 10), true)
			}
			if s.Completed {
				w.field(4, "to", strconv.Itoa(s.To), true)
				w.field(4, "completedAt", strconv.FormatInt(s.CompletedAt, 10), true)
			}
		}
	}
	if len(p.Resources) > 0 {
		w.line(0, "resources:")
		for _, r := range p.Resources {
			w.line(2, "- number: "+strconv.Itoa(r.Number))
			w.field(4, "interaction", strconv.Itoa(r.Interaction), true)
			w.field(4, "step", r.Step, false)
			w.field(4, "title", r.Title, false)
			w.field(4, "source", r.Source, true)
			writeList(&w, 4, "files", r.Files)
			if r.Time != 0 {
				w.field(4, "time", strconv.FormatInt(r.Time, 10), true)
			}
		}
	}
	w.line(0, "state:")
	w.field(2, "phase", p.Phase, true)
	w.field(2, "interactions", strconv.Itoa(p.Interactions), true)
	w.field(2, "step", p.Step, false)
	if l := p.Lock; l != nil {
		w.line(0, "lock:")
		w.field(2, "interaction", strconv.Itoa(l.Interaction), true)
		w.field(2, "stage", l.Stage, true)
		if l.AutoSubmit {
			w.field(2, "autoSubmit", "true", true)
		}
		w.field(2, "prompt", l.Prompt, false)
		w.field(2, "role", l.Role, false)
		if l.SignalAt != 0 {
			w.field(2, "signalAt", strconv.FormatInt(l.SignalAt, 10), true)
		}
		writeList(&w, 2, "bases", l.Bases)
		writeList(&w, 2, "heads", l.Heads)
	}
	if h := p.Hold; h != nil {
		w.line(0, "hold:")
		w.field(2, "interaction", strconv.Itoa(h.Interaction), true)
		writeList(&w, 2, "proposals", h.Proposals)
		writeList(&w, 2, "done", h.Done)
		writeList(&w, 2, "preReads", h.PreReads)
		if c := h.Call; c != nil {
			w.line(2, "call:")
			w.field(4, "sub", strconv.Itoa(c.Sub), true)
			w.field(4, "proposal", c.Proposal, true)
			w.field(4, "decision", c.Decision, true)
			w.field(4, "mode", c.Mode, false)
			w.field(4, "stage", c.Stage, true)
			w.field(4, "image", c.Image, false)
			w.field(4, "failed", c.Failed, false)
		}
	}
	w.field(0, "prGuidance", p.PRGuidance, false)
	w.field(0, "prRole", p.PRRole, false)
	if p.PRSignalAt != 0 {
		w.field(0, "prSignalAt", strconv.FormatInt(p.PRSignalAt, 10), true)
	}
	w.field(0, "prWorkdir", p.PRWorkdir, false)
	w.field(0, "prTemplate", p.PRTemplate, false)
	if len(p.PRs) > 0 {
		w.line(0, "prs:")
		for _, pr := range p.PRs {
			w.line(2, "- repo: "+yamlScalar(pr.Repo))
			w.field(4, "number", strconv.Itoa(pr.Number), true)
			w.field(4, "url", pr.URL, false)
			w.field(4, "state", pr.State, false)
		}
	}
	if f := p.Finalization; f != nil {
		w.line(0, "finalization:")
		w.field(2, "state", f.State, true)
		w.field(2, "at", f.At, false)
		w.field(2, "recordUrl", f.RecordURL, false)
		if len(f.Events) > 0 {
			w.line(2, "events:")
			for _, e := range f.Events {
				w.line(4, "- lane: "+yamlScalar(e.Lane))
				w.field(6, "repo", e.Repo, true)
				w.field(6, "kind", e.Kind, true)
				w.field(6, "sha", e.SHA, false)
				if e.Time != 0 {
					w.field(6, "time", strconv.FormatInt(e.Time, 10), true)
				}
				w.field(6, "pr", e.PR, false)
			}
		}
	}
	w.field(0, "finalFrom", p.FinalFrom, false)
	w.field(0, "finalAt", p.FinalAt, false)
	w.field(0, "error", p.Error, false)
	w.field(0, "updatedAt", p.UpdatedAt, false)
	return w.String()
}

func writeList(w *yamlWriter, indent int, key string, items []string) {
	if len(items) == 0 {
		return
	}
	w.line(indent, key+":")
	for _, it := range items {
		w.line(indent+2, "- "+yamlScalar(it))
	}
}

func readList(m map[string]any, key string) []string {
	var out []string
	for _, it := range yamlList(m, key) {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// parsePointerSpec reads a v2 pointer file. Anything else -- a legacy v1
// pointer, a hand-written file -- is an error.
func parsePointerSpec(content string) (PointerSpec, error) {
	if !isCondocV2Pointer(content) {
		return PointerSpec{}, fmt.Errorf("not a condocv2 pointer")
	}
	m, err := parseMiniYAML(content)
	if err != nil {
		return PointerSpec{}, err
	}
	p := PointerSpec{
		Name:          yamlString(m, "name"),
		ControlScheme: yamlString(m, "controlScheme"),
		Branch:        yamlString(m, "branch"),
		CondocRepo:    yamlString(m, "condocRepo"),
		CondocPath:    yamlString(m, "condocPath"),
		CondocURL:     yamlString(m, "condocUrl"),
		Agent:         yamlString(m, "agent"),
		Model:         yamlString(m, "model"),
		Description:   yamlString(m, "description"),
		SlopspaceID:   yamlString(m, "slopspaceId"),
		Holder:        yamlString(m, "holder"),
		Bases:         readList(m, "bases"),
		PRGuidance:    yamlString(m, "prGuidance"),
		PRRole:        yamlString(m, "prRole"),
		PRWorkdir:     yamlString(m, "prWorkdir"),
		PRTemplate:    yamlString(m, "prTemplate"),
		FinalFrom:     yamlString(m, "finalFrom"),
		FinalAt:       yamlString(m, "finalAt"),
		Error:        yamlString(m, "error"),
		UpdatedAt:     yamlString(m, "updatedAt"),
	}
	if p.ControlScheme != condocControlScheme {
		return PointerSpec{}, fmt.Errorf("control scheme %q, expected %s", p.ControlScheme, condocControlScheme)
	}
	p.StartTime, _ = strconv.ParseInt(yamlString(m, "startTime"), 10, 64)
	p.PRSignalAt, _ = strconv.ParseInt(yamlString(m, "prSignalAt"), 10, 64)
	for _, it := range yamlList(m, "prs") {
		if pm, ok := it.(map[string]any); ok {
			pr := PointerPR{Repo: yamlString(pm, "repo"), URL: yamlString(pm, "url"), State: yamlString(pm, "state")}
			pr.Number, _ = strconv.Atoi(yamlString(pm, "number"))
			p.PRs = append(p.PRs, pr)
		}
	}
	if fm, ok := m["finalization"].(map[string]any); ok {
		f := &Finalization{State: yamlString(fm, "state"), At: yamlString(fm, "at"), RecordURL: yamlString(fm, "recordUrl")}
		for _, it := range yamlList(fm, "events") {
			if em, ok := it.(map[string]any); ok {
				e := FinalEvent{Lane: yamlString(em, "lane"), Repo: yamlString(em, "repo"), Kind: yamlString(em, "kind"),
					SHA: yamlString(em, "sha"), PR: yamlString(em, "pr")}
				e.Time, _ = strconv.ParseInt(yamlString(em, "time"), 10, 64)
				f.Events = append(f.Events, e)
			}
		}
		p.Finalization = f
	}
	for _, t := range yamlList(m, "targets") {
		switch v := t.(type) {
		case string:
			p.Targets = append(p.Targets, v)
		case map[string]any:
			if r := yamlString(v, "repo"); r != "" {
				p.Targets = append(p.Targets, r)
			}
		}
	}
	for _, it := range yamlList(m, "readOnly") {
		switch v := it.(type) {
		case string:
			p.ReadOnly = append(p.ReadOnly, parseReadOnlyRepo(v))
		case map[string]any:
			if r := yamlString(v, "repo"); r != "" {
				p.ReadOnly = append(p.ReadOnly, ReadOnlyRepo{Repo: r, Ref: yamlString(v, "ref")})
			}
		}
	}
	for _, it := range yamlList(m, "readResolutions") {
		if rm, ok := it.(map[string]any); ok {
			r := ReadResolution{Repo: yamlString(rm, "repo"), Ref: yamlString(rm, "ref"), SHA: yamlString(rm, "sha"), At: yamlString(rm, "at")}
			r.Interaction, _ = strconv.Atoi(yamlString(rm, "interaction"))
			r.Time, _ = strconv.ParseInt(yamlString(rm, "time"), 10, 64)
			p.ReadResolutions = append(p.ReadResolutions, r)
		}
	}
	if cm, ok := m["configure"].(map[string]any); ok {
		c := &RepoConfigure{Kind: yamlString(cm, "kind"), Repo: yamlString(cm, "repo"), From: yamlString(cm, "from"), To: yamlString(cm, "to"),
			Stage: yamlString(cm, "stage"), SHA: yamlString(cm, "sha"), Failed: yamlString(cm, "failed")}
		c.Interaction, _ = strconv.Atoi(yamlString(cm, "interaction"))
		c.Seq, _ = strconv.Atoi(yamlString(cm, "seq"))
		p.Configure = c
	}
	for _, it := range yamlList(m, "configurations") {
		if cm, ok := it.(map[string]any); ok {
			c := RepoConfiguration{Kind: yamlString(cm, "kind"), Repo: yamlString(cm, "repo"), From: yamlString(cm, "from"), To: yamlString(cm, "to"),
				SHA: yamlString(cm, "sha"), Failed: yamlString(cm, "failed"), At: yamlString(cm, "at")}
			c.Interaction, _ = strconv.Atoi(yamlString(cm, "interaction"))
			c.Seq, _ = strconv.Atoi(yamlString(cm, "seq"))
			p.Configurations = append(p.Configurations, c)
		}
	}
	for _, it := range yamlList(m, "externals") {
		if xm, ok := it.(map[string]any); ok {
			x := ExternalChange{Repo: yamlString(xm, "repo"), From: yamlString(xm, "from"), To: yamlString(xm, "to"), At: yamlString(xm, "at")}
			x.Interaction, _ = strconv.Atoi(yamlString(xm, "interaction"))
			x.Seq, _ = strconv.Atoi(yamlString(xm, "seq"))
			x.Commits, _ = strconv.Atoi(yamlString(xm, "commits"))
			x.Time, _ = strconv.ParseInt(yamlString(xm, "time"), 10, 64)
			p.Externals = append(p.Externals, x)
		}
	}
	for _, it := range yamlList(m, "steps") {
		if sm, ok := it.(map[string]any); ok {
			s := StepScope{Path: yamlString(sm, "path"), Title: yamlString(sm, "title")}
			s.From, _ = strconv.Atoi(yamlString(sm, "from"))
			s.EnteredAt, _ = strconv.ParseInt(yamlString(sm, "enteredAt"), 10, 64)
			if to := yamlString(sm, "to"); to != "" {
				s.Completed = true
				s.To, _ = strconv.Atoi(to)
				s.CompletedAt, _ = strconv.ParseInt(yamlString(sm, "completedAt"), 10, 64)
			}
			p.Steps = append(p.Steps, s)
		}
	}
	for _, it := range yamlList(m, "resources") {
		if rm, ok := it.(map[string]any); ok {
			r := Resource{Step: yamlString(rm, "step"), Title: yamlString(rm, "title"), Source: yamlString(rm, "source"),
				Files: readList(rm, "files")}
			r.Number, _ = strconv.Atoi(yamlString(rm, "number"))
			r.Interaction, _ = strconv.Atoi(yamlString(rm, "interaction"))
			r.Time, _ = strconv.ParseInt(yamlString(rm, "time"), 10, 64)
			p.Resources = append(p.Resources, r)
		}
	}
	state := yamlMap(m, "state")
	p.Phase = yamlString(state, "phase")
	p.Interactions, _ = strconv.Atoi(yamlString(state, "interactions"))
	p.Step = yamlString(state, "step")
	if lm, ok := m["lock"].(map[string]any); ok {
		l := &InteractionLock{
			Stage:      yamlString(lm, "stage"),
			AutoSubmit: yamlString(lm, "autoSubmit") == "true",
			Prompt:     yamlString(lm, "prompt"),
			Role:       yamlString(lm, "role"),
			Bases:      readList(lm, "bases"),
			Heads:      readList(lm, "heads"),
		}
		l.Interaction, _ = strconv.Atoi(yamlString(lm, "interaction"))
		l.SignalAt, _ = strconv.ParseInt(yamlString(lm, "signalAt"), 10, 64)
		p.Lock = l
	}
	if hm, ok := m["hold"].(map[string]any); ok {
		h := &ProposalHold{
			Proposals: readList(hm, "proposals"),
			Done:      readList(hm, "done"),
			PreReads:  readList(hm, "preReads"),
		}
		h.Interaction, _ = strconv.Atoi(yamlString(hm, "interaction"))
		if cm, ok := hm["call"].(map[string]any); ok {
			c := &ToolCall{
				Proposal: yamlString(cm, "proposal"),
				Decision: yamlString(cm, "decision"),
				Mode:     yamlString(cm, "mode"),
				Stage:    yamlString(cm, "stage"),
				Image:    yamlString(cm, "image"),
				Failed:   yamlString(cm, "failed"),
			}
			c.Sub, _ = strconv.Atoi(yamlString(cm, "sub"))
			h.Call = c
		}
		p.Hold = h
	}
	return p, nil
}

// splitRepoSha splits "owner/name@sha".
func splitRepoSha(s string) (repo, sha string, ok bool) {
	i := strings.LastIndex(s, "@")
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// repoShaMap turns "owner/name@sha" items into repo -> sha.
func repoShaMap(items []string) map[string]string {
	out := map[string]string{}
	for _, it := range items {
		if repo, sha, ok := splitRepoSha(it); ok {
			out[repo] = sha
		}
	}
	return out
}
