package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"
)

// A run's progress (dtt/progress.go): "dtt run --progress --json" prints
// the tool's progress lines on stdout as it works, ahead of its result --
// a declarative tool's plan (each step of its document, in the order they
// run, with the steps each depends on), then a line as each step begins
// and a blob as it ends. LR reads them as they come (runDTT, given a
// handler through its context) and keeps each run's steps and how each
// stands (ToolRun.Progress), which tools-state carries to the tools tab's
// visual: the steps' icons in dependency order, grey until begun, yellow
// while running, green or red as they end.

const (
	maxRunSteps    = 200 // kept per run; a plan with more keeps its first
	maxStepObjects = 20  // object ids kept per step
)

// dttProgressPrefix starts each of dtt's progress lines.
var dttProgressPrefix = []byte(`{"progress":`)

// ToolRunProgress is a run's steps as dtt's progress lines left them.
// Updated is the last line's time, unix ms.
type ToolRunProgress struct {
	Mode         string        `json:"mode,omitempty"`
	Steps        []ToolRunStep `json:"steps"`
	StepsOmitted int           `json:"steps_omitted,omitempty"`
	Updated      int64         `json:"updated"`
}

// ToolRunStep is one step of a run's plan (dtt's ProgressStep) and how it
// stands: Status pending (not begun), running, ok or failed; Stage measure
// or remeasure (measured again once an assert's changes are made).
type ToolRunStep struct {
	ID       string   `json:"id"`
	Key      string   `json:"key"`
	Path     string   `json:"path"`
	Provider string   `json:"provider"`
	Alias    string   `json:"alias,omitempty"`
	Line     int      `json:"line"`
	Phase    string   `json:"phase,omitempty"`
	Asserts  bool     `json:"asserts,omitempty"`
	After    []string `json:"after"`
	Level    int      `json:"level"`

	Status         string         `json:"status"`
	Stage          string         `json:"stage,omitempty"`
	StartedAt      int64          `json:"started_at,omitempty"`
	FinishedAt     int64          `json:"finished_at,omitempty"`
	DurationMs     int64          `json:"duration_ms,omitempty"`
	Error          string         `json:"error,omitempty"`
	Objects        []string       `json:"objects,omitempty"` // the ids of the objects it named
	ObjectsOmitted int            `json:"objects_omitted,omitempty"`
	Summary        map[string]int `json:"summary,omitempty"` // its asserted fields' outcomes
}

// dttProgressLine is one of dtt's progress lines, the parts LR keeps.
type dttProgressLine struct {
	Progress   string         `json:"progress"`
	At         int64          `json:"at"`
	Mode       string         `json:"mode"`
	Steps      []ToolRunStep  `json:"steps"`
	Step       string         `json:"step"`
	Stage      string         `json:"stage"`
	Status     string         `json:"status"`
	DurationMs int64          `json:"duration_ms"`
	Error      string         `json:"error"`
	Objects    []struct{ ID string `json:"id"` } `json:"objects"`
	Summary    map[string]int `json:"summary"`
}

// apply takes one progress line into p, reporting whether it changed
// anything. A step's slices and map are replaced, never changed in place,
// so a copy of Steps (ToolRun.copy) can be read outside w.mu.
func (p *ToolRunProgress) apply(ev dttProgressLine) bool {
	at := ev.At
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	if ev.Progress == "plan" {
		steps := ev.Steps
		p.StepsOmitted = 0
		if len(steps) > maxRunSteps {
			p.StepsOmitted = len(steps) - maxRunSteps
			steps = steps[:maxRunSteps]
		}
		p.Mode, p.Steps, p.Updated = ev.Mode, make([]ToolRunStep, len(steps)), at
		for i, s := range steps {
			if s.After == nil {
				s.After = []string{}
			}
			s.Status = "pending"
			p.Steps[i] = s
		}
		return true
	}
	i := p.step(ev.Step)
	if i < 0 {
		return false
	}
	s := &p.Steps[i]
	switch ev.Progress {
	case "begin":
		s.Status, s.Stage, s.StartedAt, s.FinishedAt, s.DurationMs, s.Error = "running", ev.Stage, at, 0, 0, ""
	case "end":
		s.Status, s.Stage, s.FinishedAt, s.DurationMs, s.Error = "failed", ev.Stage, at, ev.DurationMs, ev.Error
		if ev.Status == "ok" {
			s.Status = "ok"
		}
		objs := []string{}
		for _, o := range ev.Objects {
			if len(objs) == maxStepObjects {
				break
			}
			objs = append(objs, o.ID)
		}
		s.Objects, s.ObjectsOmitted = objs, len(ev.Objects)-len(objs)
		s.Summary = ev.Summary
	default:
		return false
	}
	p.Updated = at
	return true
}

func (p *ToolRunProgress) step(id string) int {
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			return i
		}
	}
	return -1
}

// finish closes off a run's progress when the run ends: a step still
// running ended with the run -- failed, with its error, unless it went
// well. Steps never begun stay pending.
func (p *ToolRunProgress) finish(ok bool, why string) {
	for i := range p.Steps {
		if s := &p.Steps[i]; s.Status == "running" {
			s.Status, s.FinishedAt = "ok", time.Now().UnixMilli()
			if !ok {
				s.Status, s.Error = "failed", why
			}
		}
	}
}

// copyProgress is p with its steps copied, for reading outside w.mu.
func copyProgress(p *ToolRunProgress) *ToolRunProgress {
	if p == nil {
		return nil
	}
	cp := *p
	cp.Steps = append([]ToolRunStep{}, p.Steps...)
	return &cp
}

// progress takes one of run id's progress lines.
func (w *toolsWatch) progress(id string, line []byte) {
	var ev dttProgressLine
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	changed := false
	w.mu.Lock()
	for _, r := range w.runs {
		if r.ID == id && r.Status == "running" {
			if r.Progress == nil {
				r.Progress = &ToolRunProgress{Steps: []ToolRunStep{}}
			}
			changed = r.Progress.apply(ev)
		}
	}
	w.mu.Unlock()
	if changed && w.notify != nil {
		w.notify()
	}
}

// ---- Reading dtt's stdout ----

type dttProgressKey struct{}

// withDTTProgress has runDTT give each progress line dtt prints to f, as
// it comes, and leave it out of the answer it returns.
func withDTTProgress(ctx context.Context, f func(line []byte)) context.Context {
	return context.WithValue(ctx, dttProgressKey{}, f)
}

func dttProgressFrom(ctx context.Context) func(line []byte) {
	f, _ := ctx.Value(dttProgressKey{}).(func(line []byte))
	return f
}

// dttProgressSplitter is dtt's stdout: progress lines go to emit as each
// one completes, everything else into rest.
type dttProgressSplitter struct {
	emit    func(line []byte)
	rest    *bytes.Buffer
	partial []byte
}

func (s *dttProgressSplitter) Write(p []byte) (int, error) {
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		s.line(s.partial[:i+1])
		s.partial = s.partial[i+1:]
	}
}

func (s *dttProgressSplitter) line(l []byte) {
	if t := bytes.TrimSpace(l); bytes.HasPrefix(t, dttProgressPrefix) {
		s.emit(append([]byte(nil), t...))
		return
	}
	s.rest.Write(l)
}

// flush puts an unfinished last line where it goes.
func (s *dttProgressSplitter) flush() {
	if len(s.partial) > 0 {
		s.line(s.partial)
		s.partial = nil
	}
}

// stripDTTProgress drops the progress lines from a recorded call's stdout,
// leaving dtt's answer.
func stripDTTProgress(stdout string) string {
	if !strings.Contains(stdout, string(dttProgressPrefix)) {
		return stdout
	}
	var keep []string
	for _, l := range strings.Split(stdout, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), string(dttProgressPrefix)) {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}
