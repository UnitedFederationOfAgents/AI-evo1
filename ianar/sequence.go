package main

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// This file is IANAR's "sequence-v1" tab (condocs/initialRobotImpls/
// Step2Prompt.md, Revision B): an ordered list of high-level actions, each
// carried out as more detailed native instructions -- focusing a window,
// key presses, typing. A run is recorded from start to finish and reports
// success or failure for every step and for the sequence as a whole.
//
// Sequences are defined here, in the backend, and sent to the frontend on
// connect ("sequence-defs") so the tab shows each step with the instructions
// it stands for. "run-sequence" runs one; progress streams back as
// "sequence-progress" and the outcome, with the recording, as
// "sequence-result".

const (
	seqFrameInterval = 250 * time.Millisecond // fallback recording rate (~4 frames/s)
	seqMaxRecording  = 60 * time.Second       // fallback recording cap
	seqRecordLead    = 500 * time.Millisecond // recorded before the first step
	seqRecordTail    = 1 * time.Second        // recorded after the last step
	stepSettle       = 300 * time.Millisecond // pause after each step so its effect lands (and shows in the recording)
	focusSettle      = 300 * time.Millisecond // pause after focusing a window, before checking or typing into it
	commandRunWait   = 1 * time.Second        // time given to the submitted command to run
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

// SequenceDefsMsg is the "sequence-defs" WebSocket payload listing the
// sequences this instance can run.
type SequenceDefsMsg struct {
	Sequences []SequenceDef `json:"sequences"`
}

// SequenceProgressMsg is the "sequence-progress" WebSocket payload reporting
// one step starting or finishing.
type SequenceProgressMsg struct {
	SequenceID string `json:"sequence_id"`
	Step       int    `json:"step"`
	Status     string `json:"status"` // "running" | "success" | "error"
	Message    string `json:"message,omitempty"`
}

// SequenceStepResult is one step's outcome in a SequenceResultMsg.
type SequenceStepResult struct {
	Status     string `json:"status"` // "success" | "error" | "skipped"
	Message    string `json:"message,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// SequenceResultMsg is the "sequence-result" WebSocket payload reporting a
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
}

// seqEnv is what a running step can drive.
type seqEnv struct {
	kb keyboard
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
}

func (q sequence) def() SequenceDef {
	d := SequenceDef{ID: q.id, Name: q.name}
	for _, st := range q.steps {
		d.Steps = append(d.Steps, st.SequenceStepDef)
	}
	return d
}

// focusFederationCommand finds federation-command's terminal window and
// focuses it (see window.go). Overridable in tests.
var focusFederationCommand = func() (string, error) {
	t, err := federationCommandTarget()
	if err != nil {
		return "", err
	}
	return focusWindow(t, focusAttempts())
}

// tapStep returns a step run that taps each key in turn.
func tapStep(keys ...[]string) func(*seqEnv) (string, error) {
	return func(env *seqEnv) (string, error) {
		for _, k := range keys {
			if err := env.kb.tap(k[0], k[1:]...); err != nil {
				return "", err
			}
		}
		sleep(stepSettle)
		return "", nil
	}
}

// fcHelloCommand is what the fc-hello-world sequence types into
// federation-command.
const fcHelloCommand = `echo "hello world!"`

// sequences are the sequences the sequence-v1 tab offers, in display order.
var sequences = []sequence{
	{
		id:   "fc-hello-world",
		name: "federation-command: hello world",
		steps: []seqStep{
			{
				SequenceStepDef{
					Label: "Select the terminal with federation-command",
					Detail: []string{
						"find the running federation-command process",
						`focus its terminal window: the one titled "federation-command", else the window of its nearest ancestor process`,
					},
				},
				func(*seqEnv) (string, error) {
					desc, err := focusFederationCommand()
					if err != nil {
						return "", err
					}
					sleep(focusSettle)
					return "focused " + desc, nil
				},
			},
			{
				SequenceStepDef{
					Label:  "Bring federation-command to local control",
					Detail: []string{"press Right (remote control → local control; FC focuses its input)"},
				},
				tapStep([]string{"right"}),
			},
			{
				SequenceStepDef{
					Label: "Bring the cursor to the command line input",
					Detail: []string{
						"press End (cursor to the end of the input line)",
						"press Ctrl+U (clear anything before the cursor, leaving an empty command line)",
					},
				},
				tapStep([]string{"end"}, []string{"u", "ctrl"}),
			},
			{
				SequenceStepDef{
					Label:  "Enter: '" + fcHelloCommand + "'",
					Detail: []string{"type `" + fcHelloCommand + "` one key at a time"},
				},
				func(env *seqEnv) (string, error) {
					if err := env.kb.typeText(fcHelloCommand); err != nil {
						return "", err
					}
					sleep(stepSettle)
					return "", nil
				},
			},
			{
				SequenceStepDef{
					Label:  "Press enter to submit the command",
					Detail: []string{"press Enter", fmt.Sprintf("wait %s for the command to run", commandRunWait)},
				},
				func(env *seqEnv) (string, error) {
					if err := env.kb.tap("enter"); err != nil {
						return "", err
					}
					sleep(commandRunWait)
					return "", nil
				},
			},
			{
				SequenceStepDef{
					Label:  "Bring federation-command back to remote control",
					Detail: []string{"press Left on the now-empty input (local control → remote control)"},
				},
				tapStep([]string{"left"}),
			},
		},
	},
}

func sequenceDefs() SequenceDefsMsg {
	var m SequenceDefsMsg
	for _, q := range sequences {
		m.Sequences = append(m.Sequences, q.def())
	}
	return m
}

func findSequence(id string) (sequence, bool) {
	for _, q := range sequences {
		if q.id == id {
			return q, true
		}
	}
	return sequence{}, false
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
	if !clipMu.TryLock() {
		res.Error = "a native clip is recording; try again once it finishes"
		return res
	}
	defer clipMu.Unlock()

	kb, via, closeKb, err := openKeyboard()
	if err != nil {
		res.Error = fmt.Sprintf("opening keyboard input: %v", err)
		return res
	}
	defer closeKb()
	res.KeyboardVia = via

	log.Printf("robot: running sequence %q (keyboard via %s)", q.id, via)
	stopRecording := startNativeRecording(seqMaxRecording, seqFrameInterval)
	sleep(seqRecordLead)

	env := &seqEnv{kb: kb}
	start := clock()
	for i, st := range q.steps {
		progress(SequenceProgressMsg{SequenceID: q.id, Step: i, Status: "running"})
		t0 := clock()
		note, err := st.run(env)
		took := clock().Sub(t0).Milliseconds()
		if err != nil {
			res.Steps[i] = SequenceStepResult{Status: "error", Message: err.Error(), DurationMs: took}
			res.FailedStep = i
			res.Error = fmt.Sprintf("step %d (%s): %v", i+1, st.Label, err)
			progress(SequenceProgressMsg{SequenceID: q.id, Step: i, Status: "error", Message: err.Error()})
			log.Printf("robot: sequence %q failed at %s", q.id, res.Error)
			break
		}
		res.Steps[i] = SequenceStepResult{Status: "success", Message: note, DurationMs: took}
		progress(SequenceProgressMsg{SequenceID: q.id, Step: i, Status: "success", Message: note})
	}
	res.DurationMs = clock().Sub(start).Milliseconds()
	res.Success = res.FailedStep < 0

	sleep(seqRecordTail)
	rec := stopRecording()
	res.Recording = &rec
	return res
}

// handleRunSequence runs the sequence named id, streaming its progress to
// the requesting client and finishing with a "sequence-result".
func (s *Server) handleRunSequence(c *wsClient, id string) {
	q, ok := findSequence(id)
	if !ok {
		s.sendToClient(c, "sequence-result", SequenceResultMsg{SequenceID: id, FailedStep: -1, Error: fmt.Sprintf("no sequence %q", id)})
		return
	}
	res := runSequence(q, func(p SequenceProgressMsg) { s.sendToClient(c, "sequence-progress", p) })
	s.sendToClient(c, "sequence-result", res)
}
