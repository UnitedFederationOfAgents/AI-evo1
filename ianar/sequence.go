package main

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"strings"
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
//
// runSequence also runs the sequence-v2 tab's sequences (seqv2.go), which
// are compiled into the same form; those use seqEnv's vars and outputs.

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
	Outputs     []SequenceOutput     `json:"outputs,omitempty"` // what the run printed
	Warnings    []string             `json:"warnings,omitempty"` // e.g. a step used an out-of-date example action
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
	vars  map[string]string // the run's starting values (sequence-v2)
}

func (q sequence) def() SequenceDef {
	d := SequenceDef{ID: q.id, Name: q.name}
	for _, st := range q.steps {
		d.Steps = append(d.Steps, st.SequenceStepDef)
	}
	return d
}

// focusFederationCommand finds federation-command's terminal window and
// focuses it (see window.go): by sight first, then through the window
// manager. It also returns an image of what visual detection saw, if it got
// that far. Overridable in tests.
var focusFederationCommand = func() (string, string, error) {
	desc, shot, visErr := focusViaVision(fcWindowTitle)
	if visErr == nil {
		return desc, shot, nil
	}
	log.Printf("robot: visual detection of %s's window failed (%v); trying the window manager", fcProcessName, visErr)
	t, err := federationCommandTarget()
	if err == nil {
		desc, err = focusWindow(t, focusAttempts())
		if err == nil {
			return fmt.Sprintf("%s (visual detection failed: %v)", desc, visErr), shot, nil
		}
	}
	return "", shot, fmt.Errorf("visual detection: %v; window-manager fallback: %v", visErr, err)
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
// federation-command, and fcHelloOutput what it prints.
const (
	fcHelloCommand = `echo "hello world!"`
	fcHelloOutput  = "hello world!"
)

// countOutputLines reads the screen and counts the lines showing output as
// a command's output (see outputLines), returning an image of the screen
// with them boxed. Overridable in tests.
var countOutputLines = func(output, command string) (int, string, error) {
	sr, err := readScreen()
	if err != nil {
		return 0, "", err
	}
	lines := outputLines(sr.lines, output, command)
	var boxes []image.Rectangle
	for _, l := range lines {
		boxes = append(boxes, l.rect())
	}
	shot := jpegDataURL(annotate(sr.img, map[color.RGBA][]image.Rectangle{matchColor: boxes}), inspectMaxWidth)
	return len(lines), shot, nil
}

// outputLines returns the lines containing output (after normalizeText)
// other than those showing the command that printed it -- the input line
// before Enter, and FC's echo of it after -- which contain it too.
func outputLines(lines []OCRLine, output, command string) []OCRLine {
	want, cmd := normalizeText(output), normalizeText(command)
	if want == "" {
		return nil
	}
	var out []OCRLine
	for _, l := range lines {
		got := normalizeText(l.Text)
		if strings.Contains(got, want) && !strings.Contains(got, cmd) {
			out = append(out, l)
		}
	}
	return out
}

// submitAndVerify presses Enter and checks the command ran: one more line of
// fcHelloOutput must be on screen afterwards than before. Key presses are
// only ever confirmed as sent, so without this a run whose keys went
// somewhere other than federation-command's input (Revision E) still
// reported success.
func submitAndVerify(env *seqEnv) (string, error) {
	before, _, err := countOutputLines(fcHelloOutput, fcHelloCommand)
	if err != nil {
		return "", fmt.Errorf("reading the screen before submitting, to check the command runs: %w", err)
	}
	if err := env.kb.tap("enter"); err != nil {
		return "", err
	}
	sleep(commandRunWait)
	after, shot, err := countOutputLines(fcHelloOutput, fcHelloCommand)
	env.shot = shot
	if err != nil {
		return "", fmt.Errorf("sent Enter, but couldn't read the screen to check the command ran: %w", err)
	}
	// The messages don't quote the output: IANAR's own tab may be on screen
	// for the next run's count.
	if after <= before {
		return "", fmt.Errorf("sent Enter, but no new line of the command's output appeared on screen (%d before, %d after): the keys didn't reach federation-command's input", before, after)
	}
	return fmt.Sprintf("the command's output appeared on screen (%d such lines before, %d after)", before, after), nil
}

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
						"check the screen is unlocked, and wake it if it has blanked",
						"capture the screen and read its text (OCR)",
						`find the line reading exactly "federation-command" -- FC's terminal title bar -- and click it`,
						"if that fails, ask the window manager to focus FC's window",
					},
				},
				func(env *seqEnv) (string, error) {
					woke, err := ensureScreenAwake()
					if err != nil {
						return "", err
					}
					desc, shot, err := focusFederationCommand()
					env.shot = shot
					if err != nil {
						return "", err
					}
					sleep(focusSettle)
					if woke != "" {
						desc = woke + "; " + desc
					}
					return desc, nil
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
					Detail: []string{
						"read the screen and count the lines showing the command's output",
						"press Enter",
						fmt.Sprintf("wait %s for the command to run", commandRunWait),
						"read the screen again: there must be one more such line, or the keys didn't reach FC",
					},
				},
				submitAndVerify,
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
	// A run that got as far as starting its recording can be saved, failed
	// or not -- a failed one most of all.
	if res.Recording != nil {
		res.ArtifactID = s.artifacts.keep(sequenceArtifact(q.def(), res))
	}
	s.sendToClient(c, "sequence-result", res)
}
