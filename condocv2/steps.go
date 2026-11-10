package main

// Steps (sloppo condocv2WithDiagrammographerImpls): a condoc can indent its
// scope into a step, and a step into a substep, as condoccer v1's steps and
// substeps did -- but self-similar: a step is just another scope of the same
// interactions, with a file of its own (Step-<path>.md beside the main
// scope's, see condocfiles.go) so no one file, and no agent's context, takes
// the whole condoc.
//
// The main scope is "". The first step entered from it is "1", the next "2";
// the first entered from step 1 is "1-1", then "1-1-1", and so on. Entering
// one doesn't reset the interaction counter: interaction N is N whichever
// scope it is made in, which is what places it.
//
//	step <path> (condoc)            its file, linked from the scope it was
//	                                entered from under "## Step <path>"
//	  -> step <path> (pointer)      it becomes the condoc's scope (Step, Steps)
//	...interactions in the step...
//	step-complete <path> (condoc)   "## Step <path> Completed", in its file
//	                                and the one it was entered from
//	  -> step-complete <path> (pointer)  back out to that scope
//
// The condoc commit comes first and is idempotent, so a failure between the
// two is retried by doing the same again; the pointer commit is the switch.
// Both are made between interactions only, so an interaction, its DTT
// sub-iterations and the record of them all stay in the one scope.
//
// The pointer keeps every step's span -- the interactions completed when it
// was entered (From) and left (To) -- so which scope any interaction,
// external change or configuration was made in is derived from it (scopeAt),
// and the views pick out a scope's own events by it (scopeTimeline).

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// StepScope is one step entered, kept on the pointer.
type StepScope struct {
	Path        string // "1", "1-1", ...
	Title       string
	From        int   // the interactions completed when it was entered
	EnteredAt   int64 // unix seconds
	Completed   bool
	To          int // completed: the interactions completed when it was left
	CompletedAt int64
}

// StepEntry is a step as the UI lists it (PointerEntry.Steps).
type StepEntry struct {
	Path        string `json:"path"`
	Title       string `json:"title,omitempty"`
	From        int    `json:"from"`
	EnteredAt   int64  `json:"enteredAt,omitempty"`
	Completed   bool   `json:"completed,omitempty"`
	To          int    `json:"to,omitempty"`
	CompletedAt int64  `json:"completedAt,omitempty"`
}

// stepParent is the scope a step was entered from: "1-2" -> "1", "1" -> "".
func stepParent(path string) string {
	if i := strings.LastIndex(path, "-"); i >= 0 {
		return path[:i]
	}
	return ""
}

// stepDepth is 0 for the main scope, 1 for a step, 2 for a substep, ...
func stepDepth(path string) int {
	if path == "" {
		return 0
	}
	return strings.Count(path, "-") + 1
}

// stepAncestors are the scopes around a step, outermost first: "1-2" ->
// "", "1".
func stepAncestors(path string) []string {
	var out []string
	for p := stepParent(path); ; p = stepParent(p) {
		out = append([]string{p}, out...)
		if p == "" {
			return out
		}
	}
}

func (p *PointerSpec) stepScope(path string) *StepScope {
	for i := range p.Steps {
		if p.Steps[i].Path == path {
			return &p.Steps[i]
		}
	}
	return nil
}

// nextStepPath is the path of the next step entered from the current scope.
func (p *PointerSpec) nextStepPath() string {
	k := 1
	for _, s := range p.Steps {
		if stepParent(s.Path) == p.Step {
			k++
		}
	}
	if p.Step == "" {
		return fmt.Sprint(k)
	}
	return fmt.Sprintf("%s-%d", p.Step, k)
}

// scopeAt is the scope the condoc was in once n interactions had completed,
// at time t (unix seconds; 0 for after every step entered or left then): the
// deepest step entered and not yet left.
func (p *PointerSpec) scopeAt(n int, t int64) string {
	scope := ""
	for _, s := range p.Steps {
		entered := s.From < n || (s.From == n && (t <= 0 || s.EnteredAt <= t))
		left := s.Completed && (s.To < n || (s.To == n && (t <= 0 || s.CompletedAt <= t)))
		if entered && !left && stepDepth(s.Path) > stepDepth(scope) {
			scope = s.Path
		}
	}
	return scope
}

// interactionScope is the scope interaction n was made in.
func (p *PointerSpec) interactionScope(n int) string {
	if n < 1 {
		return ""
	}
	return p.scopeAt(n-1, 0)
}

// stepBlocked says why the scope can't change now ("" when it can): only
// between interactions, with nothing held or in flight.
func (p *PointerSpec) stepBlocked() string {
	switch {
	case p.Phase == PhaseProposed || p.SlopspaceID == "":
		return "it hasn't begun"
	case p.Configure != nil:
		return fmt.Sprintf("configuration %s is still in progress", sectionLabel(p.Configure.Interaction, p.Configure.Seq))
	case p.Lock != nil:
		return fmt.Sprintf("interaction %d holds the lock", p.Lock.Interaction)
	case p.Hold != nil:
		return fmt.Sprintf("interaction %d's DTT proposals await answers", p.Hold.Interaction)
	case p.Error != "":
		return "it has an error: resubmit first"
	case p.Phase != PhaseAwaitingPrompt:
		return fmt.Sprintf("it is %s", p.Phase)
	}
	return ""
}

func stepCommitMsg(spec *PointerSpec, kind, path string) string {
	return fmt.Sprintf("condocv2: %s %s %s", spec.Name, kind, path)
}

// slopEnterStep indents the condoc's scope into a new step of the current
// one: the step commit, in the condoc and then the pointer.
func (s *Server) slopEnterStep(stem, title string) error {
	title = strings.Join(strings.Fields(title), " ") // one line, on the pointer too
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if why := spec.stepBlocked(); why != "" {
		return fmt.Errorf("%s can't enter a step now: %s", spec.Name, why)
	}
	_, main, err := s.idleMainFile(&spec)
	if err != nil {
		return err
	}
	parent, path, n := spec.Step, spec.nextStepPath(), spec.Interactions
	file := scopeFilePath(main, path)
	if !fileExists(file) {
		if err := writeFileString(file, initialScopeFile(path, parent, title)); err != nil {
			return err
		}
	}
	body := fmt.Sprintf("[Step %s](%s)", path, scopeFileName(path))
	if title != "" {
		body += " — " + escapeSectionHeadings(title)
	}
	body += fmt.Sprintf("\n\nEntered after interaction %d; its interactions are in its own file.", n)
	if err := appendStepSection(scopeFilePath(main, parent), path, false, body); err != nil {
		return err
	}
	if err := s.commitCondocRepo(&spec, stepCommitMsg(&spec, "step", path)); err != nil {
		return err
	}
	spec.Steps = append(spec.Steps, StepScope{Path: path, Title: title, From: n, EnteredAt: time.Now().Unix()})
	spec.Step = path
	return s.writeSpec(&spec, stepCommitMsg(&spec, "step", path))
}

// slopCompleteStep ends the condoc's current step and comes back up to the
// scope it was entered from: the step-complete commit, in the condoc and then
// the pointer.
func (s *Server) slopCompleteStep(stem string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	path := spec.Step
	if path == "" {
		return fmt.Errorf("%s is in its main scope: there is no step to complete", spec.Name)
	}
	if why := spec.stepBlocked(); why != "" {
		return fmt.Errorf("%s can't complete %s now: %s", spec.Name, scopeTitle(path), why)
	}
	step := spec.stepScope(path)
	if step == nil {
		return fmt.Errorf("%s has no record of %s", spec.Name, scopeTitle(path))
	}
	_, main, err := s.idleMainFile(&spec)
	if err != nil {
		return err
	}
	n := spec.Interactions
	made := n - step.From
	body := fmt.Sprintf("Completed after interaction %d, with no interactions of its own.", n)
	if made > 0 {
		body = fmt.Sprintf("Completed after interaction %d: it (with any substeps) spans interactions %d–%d.", n, step.From+1, n)
	}
	for _, f := range []string{scopeFilePath(main, path), scopeFilePath(main, stepParent(path))} {
		if err := appendStepSection(f, path, true, body); err != nil {
			return err
		}
	}
	if err := s.commitCondocRepo(&spec, stepCommitMsg(&spec, "step-complete", path)); err != nil {
		return err
	}
	step.Completed, step.To, step.CompletedAt = true, n, time.Now().Unix()
	spec.Step = stepParent(path)
	return s.writeSpec(&spec, stepCommitMsg(&spec, "step-complete", path))
}

// scopeFiles is every scope's file, the main scope's first, as paths under
// the condoc's main file.
func scopeFiles(spec *PointerSpec, main string) []string {
	files := []string{stepFilePath(main)}
	for _, s := range spec.Steps {
		files = append(files, scopeFilePath(main, s.Path))
	}
	return files
}

// readScopeInteractions parses every scope's file into one list of the
// condoc's interactions, each marked with its scope. read reads a file by its
// path under the condoc's main file; the main scope's must be there, a step's
// that isn't yet (a step commit not made) is skipped.
func readScopeInteractions(spec *PointerSpec, main string, read func(string) (string, error)) ([]Interaction, string, error) {
	var all []Interaction
	mainContent := ""
	for i, f := range scopeFiles(spec, main) {
		content, err := read(f)
		if err != nil {
			if i == 0 {
				return nil, "", err
			}
			continue
		}
		if i == 0 {
			mainContent = content
		}
		all = append(all, parseInteractions(content)...)
	}
	return spec.scopeInteractions(all), mainContent, nil
}

// scopeInteractions marks each interaction with the scope it was made in, in
// interaction order.
func (p *PointerSpec) scopeInteractions(its []Interaction) []Interaction {
	byN := map[int]Interaction{}
	for _, it := range its {
		if prev, ok := byN[it.N]; ok {
			// A file can't hold another scope's interaction, but be safe:
			// keep the fuller one.
			if len(prev.Response)+len(prev.Record) >= len(it.Response)+len(it.Record) {
				continue
			}
		}
		byN[it.N] = it
	}
	out := make([]Interaction, 0, len(byN))
	for _, it := range byN {
		it.Step = p.interactionScope(it.N)
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out
}

// scopeTimeline marks each event with the scope it belongs to (TimelineEvent
// .Step), and places a step's step and step-complete after the interaction
// they followed. Those belong to the scope it was entered from; the views of
// a step show them too, as its start and end.
func (p *PointerSpec) scopeTimeline(tl Timeline) Timeline {
	events := make([]TimelineEvent, len(tl.Events))
	for i, e := range tl.Events {
		switch {
		case isStepKind(e.Kind):
			e.Step = stepParent(e.Path)
			if s := p.stepScope(e.Path); s != nil {
				switch {
				case e.Kind == "step":
					e.Interaction = s.From
				case s.Completed:
					e.Interaction = s.To
				default:
					// The condoc's commit is in but the pointer's isn't yet
					// (it is retried): it is being left now.
					e.Interaction = p.Interactions
				}
			}
		case e.Kind == "resource":
			// The scope it was added in, and what it is (resources.go).
			if r := p.resource(e.Seq); r != nil {
				e.Step, e.Note = r.Step, resourceNote(r)
			} else {
				e.Step = p.scopeAt(e.Interaction, e.Time)
			}
		case e.Kind == "propose" || e.Kind == "begin" || isCompletionKind(e.Kind):
			e.Step = ""
		case e.Kind == "external" || (e.Kind == "resolve" && e.Note == readAtConfigure):
			e.Step = p.scopeAt(e.Interaction, e.Time)
		case e.Kind == "resolve" && e.Interaction == 0:
			e.Step = ""
		default:
			e.Step = p.interactionScope(e.Interaction)
		}
		events[i] = e
	}
	tl.Events = events
	return tl
}

func isCompletionKind(kind string) bool {
	_, ok := completionKindSlot[kind]
	return ok
}

// stepEntries lists the steps for the UI.
func stepEntries(steps []StepScope) []StepEntry {
	var out []StepEntry
	for _, s := range steps {
		out = append(out, StepEntry{Path: s.Path, Title: s.Title, From: s.From, EnteredAt: s.EnteredAt,
			Completed: s.Completed, To: s.To, CompletedAt: s.CompletedAt})
	}
	return out
}
