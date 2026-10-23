package main

// The condocv2 pointer spec: one pointers/<Name>-<startTime>.yaml per v2
// condoc in the pointer repo. It shares the pointers/ directory with
// condoccer (v1) but is explicitly incompatible with it: "condocVersion: 2"
// and controlScheme slopspace-v2 mark a v2 pointer, condoccer filters every
// such file out of its listing and never drives one, and condocv2 lists
// nothing else (see isCondocV2Pointer). The same goes for the condoc files
// (a condocv2-yaml header, see condocfiles.go) and the branches (condocv2/...).
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
	// condocBranchPrefix namespaces every v2 condoc branch, so v1 and v2
	// branches never mix: v2 only accepts branches under it, v1 refuses them.
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

	// Completion and re-integration (see complete.go).
	PhasePRDrafting = "pr_drafting" // completed: the agent is drafting the PRs
	PhasePRProposed = "pr_proposed" // the drafts are ready to edit and raise
	PhasePROpen     = "pr_open"     // the PRs are raised: Merge when all are mergeable
	PhaseMerged     = "merged"      // merged, branches and slopspace removed

	// The final states (see final.go): markers only -- nothing but the
	// pointer changes, and the UI hides them unless "show final" is on.
	PhaseArchived  = "archived"  // put away after the merge
	PhaseAbandoned = "abandoned" // forced to an end from any non-final phase
)

// isFinalPhase reports whether a phase is one condocv2 never moves on from.
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
	Agent         string
	Model         string
	Description   string

	SlopspaceID string
	Holder      string   // the condocv2 driving this condoc (see slopHolder)
	Bases       []string // "owner/repo@sha" where each repo's branch began

	Phase        string
	Interactions int // completed interactions
	Lock         *InteractionLock

	// Completion: the guidance the PR drafts were asked for with, the draft
	// turn's work signal (role + signalAt) and tempdir while it runs, the
	// drafts (prs.yaml), then the raised PRs.
	PRGuidance string
	PRRole     string
	PRSignalAt int64
	PRWorkdir  string
	PRTemplate string
	PRs        []PointerPR

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
	w.line(0, "# condoc v2 pointer, kept by condocv2 -- condoccer (v1) ignores it")
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
	w.field(0, "agent", p.Agent, false)
	w.field(0, "model", p.Model, false)
	w.field(0, "description", p.Description, false)
	w.field(0, "slopspaceId", p.SlopspaceID, false)
	w.field(0, "holder", p.Holder, false)
	writeList(&w, 0, "bases", p.Bases)
	w.line(0, "state:")
	w.field(2, "phase", p.Phase, true)
	w.field(2, "interactions", strconv.Itoa(p.Interactions), true)
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

// parsePointerSpec reads a v2 pointer file. Anything else -- a v1 pointer, a
// hand-written file -- is an error: v2 never reads v1's pointers.
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
	state := yamlMap(m, "state")
	p.Phase = yamlString(state, "phase")
	p.Interactions, _ = strconv.Atoi(yamlString(state, "interactions"))
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
