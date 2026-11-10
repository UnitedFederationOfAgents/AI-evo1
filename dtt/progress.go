package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file is a run's progress: as a declarative tool works through its
// document, it says when work on each step begins and, when the step is
// done, returns a blob of what it did. "dtt run" passes it on as it comes --
// as text on stdout, or with --json --progress as JSON lines on stdout
// ahead of the result (local-representative's tools tab draws its visual
// from them).
//
// A step is one of the document's statements: the objects it names (or
// the variables it declares). The run first states its plan -- every step,
// in the order they run, each with the steps it depends on (those whose
// objects it refers to with ${{...}}, or whose alias it names), so a
// reader can draw the dependency graph -- then a begin and an end for each
// step as it runs. An assert that changed something measures its steps
// again once every assert is made: those come as a second begin and end,
// stage remeasure. Steps after one that failed never begin.
//
// The tool writes the lines to the file descriptor $DTT_PROGRESS_FD names
// (dtt run hands it a pipe), each one compact JSON beginning
// {"progress": -- any tool may, not only dtt's own engine. Lines that
// aren't are dropped. A document a dtt.calls statement calls is one step
// of the document calling it: its own steps aren't reported.

const (
	envProgressFD  = "DTT_PROGRESS_FD"
	progressFormat = "dtt-progress-v1"

	// progressDrain is how long dtt run waits, once the tool has exited,
	// for its progress to finish arriving (a child of it may still hold
	// the pipe).
	progressDrain = 2 * time.Second
	// maxProgressLine is the longest progress line passed on; a longer
	// step's blob is cut to its summary.
	maxProgressLine = 4 << 20
)

// progressPrefix starts every progress line.
var progressPrefix = []byte(`{"progress":`)

// ProgressEvent is one progress line. Progress is plan, begin or end.
type ProgressEvent struct {
	Progress string `json:"progress"`
	At       int64  `json:"at"` // unix ms

	// plan's: the format, the mode and every step, in the order they run.
	Format string         `json:"format,omitempty"`
	Mode   string         `json:"mode,omitempty"`
	Steps  []ProgressStep `json:"steps,omitempty"`

	// begin's and end's: the step, and whether it's measured (the first
	// time) or measured again once the asserts are made.
	Step  string `json:"step,omitempty"`
	Stage string `json:"stage,omitempty"` // measure or remeasure

	// end's blob: how the step went (ok or failed), the objects it named
	// as it left them, and the outcomes of its asserted fields.
	Status     string         `json:"status,omitempty"`
	DurationMs int64          `json:"duration_ms,omitempty"`
	Error      string         `json:"error,omitempty"`
	Objects    []*ImageObject `json:"objects,omitempty"`
	Fields     []FieldOutcome `json:"fields,omitempty"`
	Summary    map[string]int `json:"summary,omitempty"`
}

// ProgressStep is one step of a plan.
type ProgressStep struct {
	ID       string   `json:"id"`
	Key      string   `json:"key"`      // the statement's key, as written
	Path     string   `json:"path"`     // its dotted path, an alias resolved
	Provider string   `json:"provider"` // its path's first part (github, git, filesystem, aws, localrep, timestamp, dtt, delay, slopspace), or variables
	Alias    string   `json:"alias,omitempty"`
	Line     int      `json:"line"`
	Phase    string   `json:"phase,omitempty"`
	Asserts  bool     `json:"asserts,omitempty"`
	After    []string `json:"after"` // the steps it depends on
	Level    int      `json:"level"` // 0 with none, else one more than the deepest of them
}

// progressOut writes a run's progress lines. A nil one writes nothing, and
// one that fails to write stops trying.
type progressOut struct {
	mu   sync.Mutex
	w    io.Writer
	now  func() time.Time
	ids  map[*statement]string
	dead bool
}

func newProgressOut(w io.Writer) *progressOut {
	return &progressOut{w: w, now: time.Now}
}

// progressFromEnv is the progress $DTT_PROGRESS_FD names, nil without one.
func progressFromEnv() *progressOut {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(envProgressFD)))
	if err != nil || n < 3 {
		return nil
	}
	f := os.NewFile(uintptr(n), "dtt-progress")
	if f == nil {
		return nil
	}
	return newProgressOut(f)
}

func (p *progressOut) emit(ev ProgressEvent) {
	if p == nil {
		return
	}
	ev.At = p.now().UnixMilli()
	b, err := json.Marshal(ev)
	if err == nil && len(b) > maxProgressLine {
		ev.Objects, ev.Fields = nil, nil
		b, err = json.Marshal(ev)
	}
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return
	}
	if _, err := p.w.Write(append(b, '\n')); err != nil {
		p.dead = true
	}
}

// plan states the steps of doc, which check has checked.
func (p *progressOut) plan(mode string, doc []*statement) {
	if p == nil {
		return
	}
	var steps []ProgressStep
	steps, p.ids = planSteps(doc)
	p.emit(ProgressEvent{Progress: "plan", Format: progressFormat, Mode: mode, Steps: steps})
}

func (p *progressOut) begin(st *statement, stage string) {
	if p == nil {
		return
	}
	p.emit(ProgressEvent{Progress: "begin", Step: p.ids[st], Stage: stage})
}

// end returns step st's blob: its objects and the outcomes recorded since
// it began (fields, from e.image.Fields).
func (p *progressOut) end(e *engine, st *statement, stage string, began time.Time, fields int, err error) {
	if p == nil {
		return
	}
	ev := ProgressEvent{Progress: "end", Step: p.ids[st], Stage: stage, Status: "ok",
		DurationMs: p.now().Sub(began).Milliseconds(), Summary: map[string]int{}}
	if err != nil {
		ev.Status, ev.Error = "failed", err.Error()
	}
	for _, o := range e.stepObjects(st) {
		ev.Objects = append(ev.Objects, o.img)
	}
	if fields < len(e.image.Fields) {
		ev.Fields = e.image.Fields[fields:]
		for _, f := range ev.Fields {
			ev.Summary[f.Outcome]++
		}
	}
	p.emit(ev)
}

// stepObjects are the objects statement st names: its own, an alias's
// statement's, or the variables.
func (e *engine) stepObjects(st *statement) []*dobj {
	switch {
	case st.vars != nil:
		if o := e.byID[varsAlias]; o != nil {
			return []*dobj{o}
		}
		return nil
	case st.of != nil:
		return e.byStmt[st.of]
	}
	return e.byStmt[st]
}

// planSteps makes doc's steps, s1 on, and the ids it gave its statements.
// A step depends on those its references and after: name (the last step
// to give or assert the name before it), and the first
// steps of a phase on the last of the phase before it -- those nothing else
// in that phase depends on -- so a phase's steps all come after everything
// in the one before it.
func planSteps(doc []*statement) ([]ProgressStep, map[*statement]string) {
	ids := map[*statement]string{}
	giver := map[string]string{} // a path or alias given -> the step that gave it last
	level := map[string]int{}
	steps := make([]ProgressStep, 0, len(doc))
	var prevEnds, phaseSteps []string // the phase before's last steps; the steps of the one running
	phase, depended := "", map[string]bool{}
	for i, st := range doc {
		id := fmt.Sprintf("s%d", i+1)
		ids[st] = id
		if st.in != phase {
			if len(phaseSteps) > 0 {
				prevEnds = nil
				for _, p := range phaseSteps {
					if !depended[p] {
						prevEnds = append(prevEnds, p)
					}
				}
			}
			phase, phaseSteps = st.in, nil
		}
		s := ProgressStep{ID: id, Key: st.key, Line: st.line, Phase: st.in, After: []string{}}
		after := map[string]bool{}
		dep := func(d string) {
			if d != "" && d != id && !after[d] {
				after[d] = true
				s.After = append(s.After, d)
				if level[d]+1 > s.Level {
					s.Level = level[d] + 1
				}
			}
		}
		known := func(n string) bool { return giver[n] != "" }
		if st.vars != nil {
			s.Provider, s.Path = varsKey, varsKey
			giver[varsAlias] = id
		} else {
			s.Path, s.Provider, s.Alias, s.Asserts = st.pathString(), st.path[0], st.alias, st.assert != nil
			if st.of != nil {
				s.Alias = st.of.alias
				dep(ids[st.of])
			}
			for _, n := range refNames(st.sel, known) {
				dep(giver[n])
			}
			for _, n := range refNames(st.assert, known) {
				dep(giver[n])
			}
			for _, n := range st.after {
				dep(giver[n])
			}
			if st.of == nil {
				giver[st.pathString()] = id
			}
			// A statement naming an alias gives it too: what comes after
			// waits for the alias's objects as that statement left them.
			giver[st.key] = id
			if st.alias != "" {
				giver[st.alias] = id
			}
		}
		// A step waiting for nothing in its own phase waits for the phase
		// before it.
		waits := false
		for _, d := range s.After {
			waits = waits || contains(phaseSteps, d)
		}
		if !waits {
			for _, d := range prevEnds {
				dep(d)
			}
		}
		for _, d := range s.After {
			depended[d] = true
		}
		phaseSteps = append(phaseSteps, id)
		sort.Strings(s.After)
		level[id] = s.Level
		steps = append(steps, s)
	}
	return steps, ids
}

// refNames are the paths and aliases the ${{...}} references under n name.
func refNames(n *yNode, known func(string) bool) []string {
	if n == nil {
		return nil
	}
	var out []string
	switch n.kind {
	case yScalar:
		for _, m := range refRe.FindAllStringSubmatch(n.str, -1) {
			if name, _, ok := splitRef(m[1], known); ok {
				out = append(out, name)
			}
		}
	case yMap:
		for _, v := range n.vals {
			out = append(out, refNames(v, known)...)
		}
	case ySeq:
		for _, v := range n.items {
			out = append(out, refNames(v, known)...)
		}
	}
	return out
}

// readProgress passes each progress line r carries to emit, until r ends.
func readProgress(r io.Reader, emit func([]byte)) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 && len(line) <= maxProgressLine &&
			bytes.HasPrefix(line, progressPrefix) && json.Valid(line) {
			emit(line)
		}
		if err != nil {
			return
		}
	}
}

// progressText prints progress lines as text: the plan, then a line as
// each step begins and ends.
type progressText struct {
	w    io.Writer
	keys map[string]string // step -> what it names
}

func (t *progressText) line(b []byte) {
	var ev ProgressEvent
	if json.Unmarshal(b, &ev) != nil {
		return
	}
	if t.keys == nil {
		t.keys = map[string]string{}
	}
	stage := ""
	if ev.Stage == "remeasure" {
		stage = " (measured again)"
	}
	switch ev.Progress {
	case "plan":
		fmt.Fprintf(t.w, "plan: %d step(s), %s\n", len(ev.Steps), ev.Mode)
		for _, s := range ev.Steps {
			t.keys[s.ID] = fmt.Sprintf("%s (line %d)", s.Key, s.Line)
			after := ""
			if len(s.After) > 0 {
				after = ", after " + strings.Join(s.After, ", ")
			}
			fmt.Fprintf(t.w, "  %-4s %s %s%s\n", s.ID, s.Provider, t.keys[s.ID], after)
		}
	case "begin":
		fmt.Fprintf(t.w, "%-4s begin  %s%s\n", ev.Step, t.keys[ev.Step], stage)
	case "end":
		word := "done  "
		if ev.Status != "ok" {
			word = "FAILED"
		}
		fmt.Fprintf(t.w, "%-4s %s %s%s: %d object(s)", ev.Step, word, t.keys[ev.Step], stage, len(ev.Objects))
		if len(ev.Summary) > 0 {
			fmt.Fprintf(t.w, ", %s", summaryText(ev.Summary))
		}
		fmt.Fprintf(t.w, " in %s\n", time.Duration(ev.DurationMs)*time.Millisecond)
		if ev.Error != "" {
			fmt.Fprintf(t.w, "     %s\n", ev.Error)
		}
	}
}
