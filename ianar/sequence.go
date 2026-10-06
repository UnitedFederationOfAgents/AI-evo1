package main

import (
	"fmt"
	"image"
	"log"
	"sync"
	"time"
)

// This file is the engine that runs the sequence-v2 tab's sequences
// (seqv2.go compiles each into a sequence): an ordered list of high-level
// steps, each carried out as more detailed native instructions -- focusing
// a window, key presses, typing. A run is recorded from start to finish and
// reports success or failure for every step and for the sequence as a
// whole.
//
// It was first written for the sequence-v1 tab (condocs/initialRobotImpls/
// Step2Prompt.md, Revision B), whose hard-coded sequence and tab were
// removed in Revision M; its federation-command "hello world" lives on as a
// sequence-v2 example (seqv2_examples.go).

const (
	seqFrameInterval = 250 * time.Millisecond // fallback recording rate (~4 frames/s)
	seqMaxRecording  = 60 * time.Second       // fallback recording cap
	seqRecordLead    = 500 * time.Millisecond // recorded before the first step
	seqRecordTail    = 1 * time.Second        // recorded after the last step
	stepSettle       = 300 * time.Millisecond // pause after each step so its effect lands (and shows in the recording)
	focusSettle      = 300 * time.Millisecond // pause after focusing a window, before checking or typing into it
)

// SequenceStepDef is one high-level step as the frontend shows it, with the
// lower-level instructions it is carried out as.
type SequenceStepDef struct {
	Label  string   `json:"label"`
	Detail []string `json:"detail"`
}

// SequenceDef is a named sequence of steps.
type SequenceDef struct {
	ID    string            `json:"id"`
	Name  string            `json:"name"`
	Steps []SequenceStepDef `json:"steps"`
}

// SequenceProgressMsg is the "seq2-progress" WebSocket payload reporting
// one step starting or finishing.
type SequenceProgressMsg struct {
	SequenceID string           `json:"sequence_id"`
	Step       int              `json:"step"`
	Status     string           `json:"status"` // "running" | "success" | "error"
	Message    string           `json:"message,omitempty"`
	ImageURL   string           `json:"image_url,omitempty"` // what the step saw, if it looked at the screen
	Outputs    []SequenceOutput `json:"outputs,omitempty"`   // everything the run has printed so far
}

// SequenceOutput is a value a run printed (sequence-v2's print and
// read-text ops), shown with its result.
type SequenceOutput struct {
	Label string `json:"label,omitempty"`
	Value string `json:"value"`
}

// SequenceStepResult is one step's outcome in a SequenceResultMsg.
type SequenceStepResult struct {
	Status     string `json:"status"` // "success" | "error" | "skipped"
	Message    string `json:"message,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	ImageURL   string `json:"image_url,omitempty"` // what the step saw, if it looked at the screen
}

// SequenceResultMsg is the "seq2-result" WebSocket payload reporting a
// finished run. Success is the run's own outcome; the recording reports its
// own success separately, so a failed recording doesn't hide a run that
// worked (or vice versa).
type SequenceResultMsg struct {
	SequenceID  string               `json:"sequence_id"`
	Success     bool                 `json:"success"`
	FailedStep  int                  `json:"failed_step"` // -1 if no step failed
	Error       string               `json:"error,omitempty"`
	Steps       []SequenceStepResult `json:"steps"`
	DurationMs  int64                `json:"duration_ms"`
	KeyboardVia string               `json:"keyboard_via,omitempty"`
	Recording   *ClipResultMsg       `json:"recording,omitempty"`
	Outputs     []SequenceOutput     `json:"outputs,omitempty"`      // what the run printed
	Warnings    []string             `json:"warnings,omitempty"`     // e.g. a step used an out-of-date example action
	ArtifactID  string               `json:"artifact_id,omitempty"` // names the run for "save-artifact" (see artifacts.go)
}

// seqEnv is what a running step can drive. A step that looked at the screen
// leaves what it saw in shot (a data: URL), to be shown with its result.
// vars are the run's named values (sequence-v2's controls, built-ins and
// saved values) and outputs what it has printed. seen holds where the lines
// a count-text counted were, under its save_as name, so a later click-text
// can pick a line that wasn't there then (new_since).
type seqEnv struct {
	kb      keyboard
	shot    string
	vars    map[string]string
	outputs []SequenceOutput
	seen    map[string][]image.Rectangle
}

// seqStep is a SequenceStepDef plus the code that carries it out. run
// returns a short note on what it did.
type seqStep struct {
	SequenceStepDef
	run func(env *seqEnv) (string, error)
}

type sequence struct {
	id    string
	name  string
	steps []seqStep
	vars  map[string]string // the run's starting values
	// noRecord skips the run's own recording: something else is already
	// recording the screen around it (an LR control sequence -- see
	// lrrecord.go).
	noRecord bool
}

func (q sequence) def() SequenceDef {
	d := SequenceDef{ID: q.id, Name: q.name}
	for _, st := range q.steps {
		d.Steps = append(d.Steps, st.SequenceStepDef)
	}
	return d
}

// seqMu allows one sequence run at a time.
var seqMu sync.Mutex

// runSequence runs q's steps in order, stopping at the first failure, while
// recording the desktop from just before the first step to just after the
// last. progress is called as each step starts and finishes.
func runSequence(q sequence, progress func(SequenceProgressMsg)) SequenceResultMsg {
	res := SequenceResultMsg{SequenceID: q.id, FailedStep: -1, Steps: make([]SequenceStepResult, len(q.steps))}
	for i := range res.Steps {
		res.Steps[i].Status = "skipped"
	}
	if !seqMu.TryLock() {
		res.Error = "a sequence is already running"
		return res
	}
	defer seqMu.Unlock()
	// The run's recording shares the one-recording-at-a-time lock with
	// Native Clip.
	if !q.noRecord {
		if !clipMu.TryLock() {
			res.Error = "a native clip is recording; try again once it finishes"
			return res
		}
		defer clipMu.Unlock()
	}

	kb, via, closeKb, err := openKeyboard()
	if err != nil {
		res.Error = fmt.Sprintf("opening keyboard input: %v", err)
		return res
	}
	defer closeKb()
	res.KeyboardVia = via

	log.Printf("robot: running sequence %q (keyboard via %s)", q.id, via)
	var stopRecording func() ClipResultMsg
	if !q.noRecord {
		stopRecording = startNativeRecording(seqMaxRecording, seqFrameInterval)
		sleep(seqRecordLead)
	}

	env := &seqEnv{kb: kb, vars: map[string]string{}, seen: map[string][]image.Rectangle{}}
	for k, v := range q.vars {
		env.vars[k] = v
	}
	start := clock()
	for i, st := range q.steps {
		progress(SequenceProgressMsg{SequenceID: q.id, Step: i, Status: "running"})
		t0 := clock()
		env.shot = ""
		note, err := st.run(env)
		took := clock().Sub(t0).Milliseconds()
		if err != nil {
			res.Steps[i] = SequenceStepResult{Status: "error", Message: err.Error(), DurationMs: took, ImageURL: env.shot}
			res.FailedStep = i
			res.Error = fmt.Sprintf("step %d (%s): %v", i+1, st.Label, err)
			progress(SequenceProgressMsg{SequenceID: q.id, Step: i, Status: "error", Message: err.Error(), ImageURL: env.shot, Outputs: env.outputs})
			log.Printf("robot: sequence %q failed at %s", q.id, res.Error)
			break
		}
		res.Steps[i] = SequenceStepResult{Status: "success", Message: note, DurationMs: took, ImageURL: env.shot}
		progress(SequenceProgressMsg{SequenceID: q.id, Step: i, Status: "success", Message: note, ImageURL: env.shot, Outputs: env.outputs})
	}
	res.DurationMs = clock().Sub(start).Milliseconds()
	res.Success = res.FailedStep < 0
	res.Outputs = env.outputs

	if stopRecording != nil {
		sleep(seqRecordTail)
		rec := stopRecording()
		res.Recording = &rec
	}
	return res
}
