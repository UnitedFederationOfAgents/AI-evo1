package main

// The pointer spec: one pointers/<Name>-<startTime>.yaml per slopspace
// condoc in the pointer repo (docs/SlopspaceControlScheme.md, D4). The
// static part is set by the proposal; the runtime part (slopspace, holder,
// state, turn, PRs) is kept current by the condoccer holding the condoc.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// condocNamePattern is a condoc name: it becomes a file name, a branch
// segment and a work-signal role, so it stays plain.
var condocNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// PointerTurn is an agent turn in flight (or waiting in the queue). Stage
// records how far it got, so a turn interrupted by an error or a condoccer
// restart picks up where it stopped (see runTurn).
type PointerTurn struct {
	Kind        string   // turnStep, turnRevision, turnSubstep, turnSubstepRevision, turnProposePR
	Letter      string   // the revision letter; "" for a step's or substep's initial reply
	Stage       string   // stageQueued ... stageReplied
	Role        string   // the work signal's role; with SignalAt it names the signal file
	SignalAt    int64    // the work signal's CreatedAt (unix seconds)
	Bases       []string // "owner/repo@sha" of every repo before the turn, for the reply's links
	Instruction string   // propose_pr: the human's regenerate instruction
	Workdir     string   // propose_pr: the in-place tempdir
}

// PointerPR is a raised pull request.
type PointerPR struct {
	Repo   string
	Number int
	URL    string
	State  string // "open", "merged" or "closed", as last seen
}

// PointerSpec is a pointer file in full.
type PointerSpec struct {
	Name          string
	StartTime     int64
	ControlScheme string
	Branch        string
	CondocRepo    string
	CondocPath    string // within the condoc repo
	CondocURL     string // the condoc on its branch -- the pointer's link to the condoc (D6)
	Targets       []string
	Agent         string
	Model         string
	Description   string

	SlopspaceID string
	Holder      string // the condoccer driving this condoc (see slopHolder)

	Phase     string
	Step      string
	Substep   string
	Iteration string

	Turn       *PointerTurn
	PRTemplate string
	PRs        []PointerPR
	Error      string
	UpdatedAt  string
}

// Stem is the pointer file's name without extension.
func (p *PointerSpec) Stem() string {
	return fmt.Sprintf("%s-%d", p.Name, p.StartTime)
}

// Repos is every repo the condoc touches: the targets, then the condoc repo.
func (p *PointerSpec) Repos() []string {
	return append(append([]string{}, p.Targets...), p.CondocRepo)
}

func (p *PointerSpec) render() string {
	var w yamlWriter
	w.line(0, "# condoc pointer, kept by condoccer -- see docs/SlopspaceControlScheme.md")
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
	w.line(0, "state:")
	w.field(2, "phase", p.Phase, true)
	w.field(2, "step", p.Step, false)
	w.field(2, "substep", p.Substep, false)
	w.field(2, "iteration", p.Iteration, false)
	if t := p.Turn; t != nil {
		w.line(0, "turn:")
		w.field(2, "kind", t.Kind, true)
		w.field(2, "letter", t.Letter, false)
		w.field(2, "stage", t.Stage, true)
		w.field(2, "role", t.Role, false)
		if t.SignalAt != 0 {
			w.field(2, "signalAt", strconv.FormatInt(t.SignalAt, 10), true)
		}
		if len(t.Bases) > 0 {
			w.line(2, "bases:")
			for _, b := range t.Bases {
				w.line(4, "- "+yamlScalar(b))
			}
		}
		w.field(2, "instruction", t.Instruction, false)
		w.field(2, "workdir", t.Workdir, false)
	}
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
	w.field(0, "error", p.Error, false)
	w.field(0, "updatedAt", p.UpdatedAt, false)
	return w.String()
}

func parsePointerSpec(content string) (PointerSpec, error) {
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
		PRTemplate:    yamlString(m, "prTemplate"),
		Error:         yamlString(m, "error"),
		UpdatedAt:     yamlString(m, "updatedAt"),
	}
	p.StartTime, _ = strconv.ParseInt(yamlString(m, "startTime"), 10, 64)
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
	p.Step = yamlString(state, "step")
	p.Substep = yamlString(state, "substep")
	p.Iteration = yamlString(state, "iteration")
	if tm, ok := m["turn"].(map[string]any); ok {
		t := &PointerTurn{
			Kind:        yamlString(tm, "kind"),
			Letter:      yamlString(tm, "letter"),
			Stage:       yamlString(tm, "stage"),
			Role:        yamlString(tm, "role"),
			Instruction: yamlString(tm, "instruction"),
			Workdir:     yamlString(tm, "workdir"),
		}
		t.SignalAt, _ = strconv.ParseInt(yamlString(tm, "signalAt"), 10, 64)
		for _, b := range yamlList(tm, "bases") {
			if s, ok := b.(string); ok {
				t.Bases = append(t.Bases, s)
			}
		}
		p.Turn = t
	}
	for _, item := range yamlList(m, "prs") {
		pm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		pr := PointerPR{Repo: yamlString(pm, "repo"), URL: yamlString(pm, "url"), State: yamlString(pm, "state")}
		pr.Number, _ = strconv.Atoi(yamlString(pm, "number"))
		p.PRs = append(p.PRs, pr)
	}
	return p, nil
}

// ---- the propose-PR template (D10) ----

// prDraft is one PR of the template the agent proposes and the human edits.
type prDraft struct {
	Repo  string
	Base  string
	Title string
	Body  string
}

// parsePRTemplate reads
//
//	summary: |       (optional; appended to every body)
//	  ...
//	prs:
//	  - repo: owner/name
//	    base: main
//	    title: ...
//	    body: |
//	      ...
func parsePRTemplate(text string) (string, []prDraft, error) {
	m, err := parseMiniYAML(text)
	if err != nil {
		return "", nil, err
	}
	summary := strings.TrimSpace(yamlString(m, "summary"))
	var drafts []prDraft
	for i, item := range yamlList(m, "prs") {
		pm, ok := item.(map[string]any)
		if !ok {
			return "", nil, fmt.Errorf("prs[%d]: expected a mapping", i)
		}
		d := prDraft{
			Repo:  strings.TrimSpace(yamlString(pm, "repo")),
			Base:  strings.TrimSpace(yamlString(pm, "base")),
			Title: strings.TrimSpace(yamlString(pm, "title")),
			Body:  strings.TrimSpace(yamlString(pm, "body")),
		}
		if !pointerRepoPattern.MatchString(d.Repo) {
			return "", nil, fmt.Errorf("prs[%d]: repo %q: expected owner/name", i, d.Repo)
		}
		if d.Title == "" {
			return "", nil, fmt.Errorf("prs[%d] (%s): title is empty", i, d.Repo)
		}
		drafts = append(drafts, d)
	}
	if len(drafts) == 0 {
		return "", nil, fmt.Errorf("the template lists no prs")
	}
	return summary, drafts, nil
}

// checkPRDrafts makes sure the template covers exactly the condoc's repos,
// and orders the drafts for raising and merging: targets in pointer order,
// the condoc repo last (D10).
func checkPRDrafts(spec *PointerSpec, drafts []prDraft) ([]prDraft, error) {
	byRepo := map[string]prDraft{}
	for _, d := range drafts {
		key := strings.ToLower(d.Repo)
		if _, dup := byRepo[key]; dup {
			return nil, fmt.Errorf("%s is listed twice", d.Repo)
		}
		byRepo[key] = d
	}
	var ordered []prDraft
	for _, repo := range spec.Repos() {
		d, ok := byRepo[strings.ToLower(repo)]
		if !ok {
			return nil, fmt.Errorf("no PR for %s", repo)
		}
		d.Repo = repo
		ordered = append(ordered, d)
		delete(byRepo, strings.ToLower(repo))
	}
	if len(byRepo) > 0 {
		var extra []string
		for _, d := range byRepo {
			extra = append(extra, d.Repo)
		}
		return nil, fmt.Errorf("not one of this condoc's repos: %s", strings.Join(extra, ", "))
	}
	return ordered, nil
}

// defaultPRTemplate is the fallback when the agent doesn't produce one.
func defaultPRTemplate(spec *PointerSpec, bases map[string]string) string {
	var w yamlWriter
	w.field(0, "summary", strings.TrimSpace(spec.Description)+"\n", false)
	w.line(0, "prs:")
	for _, repo := range spec.Repos() {
		base := bases[repo]
		if base == "" {
			base = "main"
		}
		w.line(2, "- repo: "+yamlScalar(repo))
		w.field(4, "base", base, true)
		w.field(4, "title", spec.Name, true)
		w.field(4, "body", fmt.Sprintf("Changes from condoc %s.\n", spec.Name), true)
	}
	return w.String()
}
