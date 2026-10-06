package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"path"
	"strings"
	"sync"
	"time"
)

// This file is the control tab (condocs/initialRobotImpls/Step3Prompt.md):
// sequences of actions across every sub-app LR manages, run by LR itself so
// it can check each step's effect from what the sub-apps report back -- an
// FC instance's control state and output, for one -- rather than trusting
// that the keys were sent. Everything here is "v1": the control tab nests it
// under a v1 sub-tab, and agent-coordinator's control tab drives the same
// runs on a chosen host ("__control:run <sequence>" / "__control:cancel").
//
// Steps that need the robot hand a short list of sequence-v2 instructions
// to IANAR ("__robot:run <json>", see ianar/lrrun.go) and wait for its
// "robot-run-result".
//
// A sequence can also have a node's screen recorded from before its first
// step to after its last (Revision A): IANAR records on
// "__robot:record-start" and saves the video into the files tab on
// "__robot:record-stop" (ianar/lrrecord.go). Which sequences record, and
// where, is the sequence's own choice (controlSequence.record). A saved
// video is listed on the run (ControlRunMsg.Recordings) so the control tab
// can play it back beside the steps it shows (Revision B).
//
// Sequences begin by unlocking the robot's screen if it is locked (Revision
// B), before the recording starts: keys sent to a locked screen go to its
// password field, and there is nothing worth recording until it's unlocked.

const (
	controlLaunchTimeout = 45 * time.Second // a launched FC connecting in remote control
	controlOutputTimeout = 15 * time.Second // a command's output reaching LR
	controlStateTimeout  = 6 * time.Second  // an FC control-state change after a key press
	controlRobotTimeout  = 2 * time.Minute  // one robot run, recording included
	controlRecordStart   = 15 * time.Second // the robot starting a recording
	controlRecordSave    = 3 * time.Minute  // the robot stopping and uploading a recording
	controlRecordLead    = 1 * time.Second  // recorded before the first step
	controlRecordTail    = 2 * time.Second  // recorded after the last step
	controlPoll          = 100 * time.Millisecond
	controlMaxLogs       = 500 // FC log lines kept for the current run
)

// recordLocalRobot names, in controlSequence.record, the screen of the node
// this LR runs on, recorded by its own IANAR -- the only one for now.
const recordLocalRobot = "robot"

// ControlStepInfo describes one step of a control sequence.
type ControlStepInfo struct {
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// ControlSequenceInfo describes a sequence the control tab can run.
type ControlSequenceInfo struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Steps       []ControlStepInfo `json:"steps"`
	Record      []string          `json:"record,omitempty"` // whose screens a run records (see controlSequence.record)
}

// ControlStepResult is one step's live status in a ControlRunMsg.
type ControlStepResult struct {
	Label      string `json:"label"`
	Status     string `json:"status"` // "pending" | "running" | "success" | "error" | "skipped"
	Message    string `json:"message,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// ControlValue is a named value a run produced (the marker it echoed, the
// FC instance it launched, the robot's saved recordings).
type ControlValue struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ControlRunMsg is the current (or most recent) run.
type ControlRunMsg struct {
	ID         string              `json:"id"`
	Sequence   string              `json:"sequence"`
	Name       string              `json:"name"`
	Status     string              `json:"status"` // "running" | "success" | "error" | "cancelled"
	Error      string              `json:"error,omitempty"`
	StartedAt  int64               `json:"started_at"` // unix ms
	DurationMs int64               `json:"duration_ms,omitempty"`
	Steps      []ControlStepResult `json:"steps"`
	Values     []ControlValue      `json:"values,omitempty"`
	Recordings []ControlRecording  `json:"recordings,omitempty"`
}

// ControlRecording is a screen recording a run saved into the files tab.
type ControlRecording struct {
	Who        string `json:"who"`     // whose screen (see controlSequence.record)
	FileID     string `json:"file_id"` // GET /api/files/<file_id>
	Name       string `json:"name"`
	Video      bool   `json:"video"` // a video the browser can play, rather than a .zip of frames
	DurationMs int64  `json:"duration_ms,omitempty"`
	Via        string `json:"via,omitempty"`
}

// ControlStateMsg is the payload of "control-state" messages, sent to
// browser clients and agent-coordinator.
type ControlStateMsg struct {
	Sequences []ControlSequenceInfo `json:"sequences"`
	Run       *ControlRunMsg        `json:"run,omitempty"`
}

// controlStep is one step of a control sequence and the code that does it.
// run returns a short note on what happened. Steps marked beforeRecording
// (leading ones only) run before the sequence's recording starts.
type controlStep struct {
	ControlStepInfo
	run             func(r *controlRun) (string, error)
	beforeRecording bool
}

type controlSequence struct {
	id, name, description string
	steps                 []controlStep
	// record lists the screens recorded across a run, none for no
	// recording. recordLocalRobot is the only one so far; recording other
	// nodes (through agent-coordinator) would add names here.
	record []string
}

func (q controlSequence) info() ControlSequenceInfo {
	info := ControlSequenceInfo{ID: q.id, Name: q.name, Description: q.description, Record: q.record}
	for _, st := range q.steps {
		info.Steps = append(info.Steps, st.ControlStepInfo)
	}
	return info
}

// fcLogEvent is one FC log/output line seen during a run.
type fcLogEvent struct {
	fc, line, kind string
}

// controlEngine runs at most one control sequence at a time.
type controlEngine struct {
	s *Server

	mu       sync.Mutex
	run      *ControlRunMsg
	cancelCh chan struct{}
	logs     []fcLogEvent
	logBase  int // count of lines dropped from the front of logs
	robot    map[string]chan RobotRunMsg
	progress map[string]func(RobotRunMsg)
	recs     map[string]chan RobotRecordMsg
}

func newControlEngine(s *Server) *controlEngine {
	return &controlEngine{
		s:        s,
		robot:    make(map[string]chan RobotRunMsg),
		progress: make(map[string]func(RobotRunMsg)),
		recs:     make(map[string]chan RobotRecordMsg),
	}
}

// controlSequences lists what the control tab offers.
func controlSequences() []controlSequence {
	return []controlSequence{fcRobotHandoffSequence()}
}

func findControlSequence(id string) (controlSequence, bool) {
	for _, q := range controlSequences() {
		if q.id == id {
			return q, true
		}
	}
	return controlSequence{}, false
}

// state returns the current control-state snapshot.
func (e *controlEngine) state() ControlStateMsg {
	msg := ControlStateMsg{}
	for _, q := range controlSequences() {
		msg.Sequences = append(msg.Sequences, q.info())
	}
	e.mu.Lock()
	if e.run != nil {
		cp := *e.run
		cp.Steps = append([]ControlStepResult(nil), e.run.Steps...)
		cp.Values = append([]ControlValue(nil), e.run.Values...)
		cp.Recordings = append([]ControlRecording(nil), e.run.Recordings...)
		msg.Run = &cp
	}
	e.mu.Unlock()
	return msg
}

func (e *controlEngine) broadcast() {
	st := e.state()
	e.s.broadcast("control-state", st)
	if ac := e.s.getACClient(); ac != nil {
		ac.SendData("control-state", st)
	}
}

// handleCommand handles agent-coordinator's "__control:" commands:
//
//	__control:run <sequence id>
//	__control:cancel
func (e *controlEngine) handleCommand(raw string) {
	rest := strings.TrimSpace(strings.TrimPrefix(raw, "__control:"))
	verb, arg, _ := strings.Cut(rest, " ")
	switch verb {
	case "run":
		if err := e.start(strings.TrimSpace(arg)); err != nil {
			log.Printf("control: remote run %q: %v", arg, err)
		}
	case "cancel":
		e.cancel()
	default:
		log.Printf("control: ignoring unrecognised command %q", raw)
	}
}

// start begins a run of sequence id in the background.
func (e *controlEngine) start(id string) error {
	q, ok := findControlSequence(id)
	if !ok {
		return fmt.Errorf("no control sequence %q", id)
	}
	e.mu.Lock()
	if e.run != nil && e.run.Status == "running" {
		e.mu.Unlock()
		return errors.New("a control sequence is already running")
	}
	run := &ControlRunMsg{
		ID:        randomToken(8),
		Sequence:  q.id,
		Name:      q.name,
		Status:    "running",
		StartedAt: time.Now().UnixMilli(),
	}
	for _, st := range q.steps {
		run.Steps = append(run.Steps, ControlStepResult{Label: st.Label, Status: "pending"})
	}
	e.run = run
	e.cancelCh = make(chan struct{})
	e.logs = nil
	e.logBase = 0
	cancelCh := e.cancelCh
	e.mu.Unlock()

	e.broadcast()
	go e.execute(q, &controlRun{e: e, s: e.s, cancel: cancelCh, vars: map[string]string{}})
	return nil
}

// cancel stops the current run between (or while waiting within) steps. A
// robot run already under way finishes on IANAR's side regardless.
func (e *controlEngine) cancel() {
	e.mu.Lock()
	if e.run != nil && e.run.Status == "running" && e.cancelCh != nil {
		close(e.cancelCh)
		e.cancelCh = nil
	}
	e.mu.Unlock()
}

var errControlCancelled = errors.New("cancelled")

func (e *controlEngine) execute(q controlSequence, r *controlRun) {
	start := time.Now()
	log.Printf("control: running %q", q.id)
	recording := false
	failed := -1
	var runErr error
	for i, st := range q.steps {
		if !recording && !st.beforeRecording {
			recording = true
			for _, who := range q.record {
				r.startRecording(q, who)
			}
		}
		r.step = i
		e.updateStep(i, func(sr *ControlStepResult) { sr.Status = "running" })
		t0 := time.Now()
		note, err := st.run(r)
		took := time.Since(t0).Milliseconds()
		if err != nil {
			failed, runErr = i, err
			e.updateStep(i, func(sr *ControlStepResult) {
				sr.Status, sr.Message, sr.DurationMs = "error", err.Error(), took
			})
			break
		}
		e.updateStep(i, func(sr *ControlStepResult) {
			sr.Status, sr.Message, sr.DurationMs = "success", note, took
		})
	}
	// However the steps ended -- a failure's recording is the one most
	// worth watching.
	r.stopRecording()

	e.mu.Lock()
	run := e.run
	run.DurationMs = time.Since(start).Milliseconds()
	switch {
	case runErr == nil:
		run.Status = "success"
	case errors.Is(runErr, errControlCancelled):
		run.Status = "cancelled"
		run.Error = fmt.Sprintf("cancelled during step %d (%s)", failed+1, q.steps[failed].Label)
	default:
		run.Status = "error"
		run.Error = fmt.Sprintf("step %d (%s): %v", failed+1, q.steps[failed].Label, runErr)
	}
	if failed >= 0 {
		for i := failed + 1; i < len(run.Steps); i++ {
			run.Steps[i].Status = "skipped"
		}
	}
	e.cancelCh = nil
	status, msg := run.Status, run.Error
	e.mu.Unlock()
	log.Printf("control: %q finished: %s %s", q.id, status, msg)
	e.broadcast()
}

func (e *controlEngine) updateStep(i int, fn func(*ControlStepResult)) {
	e.mu.Lock()
	if e.run != nil && i < len(e.run.Steps) {
		fn(&e.run.Steps[i])
	}
	e.mu.Unlock()
	e.broadcast()
}

func (e *controlEngine) addValue(label, value string) {
	e.mu.Lock()
	if e.run != nil {
		e.run.Values = append(e.run.Values, ControlValue{Label: label, Value: value})
	}
	e.mu.Unlock()
	e.broadcast()
}

// setValue replaces the value under label, adding it if there's none.
func (e *controlEngine) setValue(label, value string) {
	e.mu.Lock()
	if e.run != nil {
		found := false
		for i := range e.run.Values {
			if e.run.Values[i].Label == label {
				e.run.Values[i].Value, found = value, true
			}
		}
		if !found {
			e.run.Values = append(e.run.Values, ControlValue{Label: label, Value: value})
		}
	}
	e.mu.Unlock()
	e.broadcast()
}

// noteFCLog records an FC log line while a run is under way, for steps
// waiting on a command's output.
func (e *controlEngine) noteFCLog(fc, line, kind string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.run == nil || e.run.Status != "running" {
		return
	}
	e.logs = append(e.logs, fcLogEvent{fc: fc, line: line, kind: kind})
	if over := len(e.logs) - controlMaxLogs; over > 0 {
		e.logs = e.logs[over:]
		e.logBase += over
	}
}

// logMark returns a position in the run's FC log; logsSince returns the
// lines seen after it.
func (e *controlEngine) logMark() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.logBase + len(e.logs)
}

func (e *controlEngine) logsSince(mark int) []fcLogEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	from := mark - e.logBase
	if from < 0 {
		from = 0
	}
	if from > len(e.logs) {
		return nil
	}
	return append([]fcLogEvent(nil), e.logs[from:]...)
}

// RobotRunMsg mirrors ianar's "robot-run-progress"/"robot-run-result"
// payload (ianar/lrrun.go).
type RobotRunMsg struct {
	Run         string               `json:"run"`
	Step        int                  `json:"step"`
	Status      string               `json:"status,omitempty"`
	Message     string               `json:"message,omitempty"`
	Success     bool                 `json:"success"`
	Error       string               `json:"error,omitempty"`
	Steps       []RobotRunStepResult `json:"steps,omitempty"`
	DurationMs  int64                `json:"duration_ms,omitempty"`
	KeyboardVia string               `json:"keyboard_via,omitempty"`
	SavedAs     string               `json:"saved_as,omitempty"`
	SaveError   string               `json:"save_error,omitempty"`
}

// RobotRunStepResult mirrors ianar's per-step result.
type RobotRunStepResult struct {
	Label      string `json:"label"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// robotStep is one step of a robot run request (ianar/lrrun.go's
// RobotRunStep): a label and sequence-v2 instructions.
type robotStep struct {
	Label string              `json:"label"`
	Do    []map[string]string `json:"do"`
}

// noteRobotRun delivers a robot run's progress or result to the step
// waiting on it.
func (e *controlEngine) noteRobotRun(final bool, msg RobotRunMsg) {
	e.mu.Lock()
	ch := e.robot[msg.Run]
	progress := e.progress[msg.Run]
	if final {
		delete(e.robot, msg.Run)
		delete(e.progress, msg.Run)
	}
	e.mu.Unlock()
	if final {
		if ch != nil {
			ch <- msg // buffered, see robotRun
		}
		return
	}
	if progress != nil {
		progress(msg)
	}
}

// RobotRecordMsg mirrors ianar's "robot-record-result" payload
// (ianar/lrrecord.go).
type RobotRecordMsg struct {
	Rec        string `json:"rec"`
	Status     string `json:"status"` // "recording" | "saved" | "error"
	Error      string `json:"error,omitempty"`
	Via        string `json:"via,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	SavedAs    string `json:"saved_as,omitempty"`
	SavedID    string `json:"saved_id,omitempty"`
	SaveError  string `json:"save_error,omitempty"`
}

// noteRobotRecord delivers a recording's start or stop result to the run
// waiting on it.
func (e *controlEngine) noteRobotRecord(msg RobotRecordMsg) {
	e.mu.Lock()
	ch := e.recs[msg.Rec]
	e.mu.Unlock()
	if ch != nil {
		select {
		case ch <- msg:
		default: // nobody waiting any more
		}
	}
}

// controlRun is what a running step can drive.
type controlRun struct {
	e      *controlEngine
	s      *Server
	cancel chan struct{}
	step   int
	vars   map[string]string
	rec    string // the robot recording under way across the run, "" if none
}

// recordingLabel is the run value naming the screen recording.
const recordingLabel = "screen recording"

// robotRecord sends IANAR a record-start or record-stop for rec and waits
// for its answer.
func (r *controlRun) robotRecord(verb, rec, name string, timeout time.Duration) (RobotRecordMsg, error) {
	s := r.s
	if s.reprServer == nil || !s.reprServer.IsHealthy("robot") {
		return RobotRecordMsg{}, errors.New("the robot (ianar) isn't connected to this local-representative")
	}
	req, err := json.Marshal(struct {
		Rec  string `json:"rec"`
		Name string `json:"name,omitempty"`
		Save bool   `json:"save,omitempty"`
	}{rec, name, verb == "record-stop"})
	if err != nil {
		return RobotRecordMsg{}, err
	}
	ch := make(chan RobotRecordMsg, 1)
	r.e.mu.Lock()
	r.e.recs[rec] = ch
	r.e.mu.Unlock()
	defer func() {
		r.e.mu.Lock()
		delete(r.e.recs, rec)
		r.e.mu.Unlock()
	}()
	s.reprServer.SendCommand("robot", "__robot:"+verb+" "+string(req))
	select {
	case res := <-ch:
		if res.Status == "error" {
			return res, errors.New(res.Error)
		}
		return res, nil
	case <-time.After(timeout):
		return RobotRecordMsg{}, fmt.Errorf("the robot didn't answer within %s", timeout)
	}
}

// startRecording starts recording who's screen for the run. A recording
// that can't start is noted, not fatal: the sequence runs regardless.
func (r *controlRun) startRecording(q controlSequence, who string) {
	if who != recordLocalRobot {
		r.e.addValue(recordingLabel, fmt.Sprintf("not recording %q: only this node's robot can record so far", who))
		return
	}
	rec := randomToken(8)
	if _, err := r.robotRecord("record-start", rec, q.id, controlRecordStart); err != nil {
		r.e.setValue(recordingLabel, "not recording: "+err.Error())
		return
	}
	r.rec = rec
	r.e.setValue(recordingLabel, "recording this node's screen…")
	time.Sleep(controlRecordLead)
}

// stopRecording stops the run's recording, if any, and notes where it was
// saved.
func (r *controlRun) stopRecording() {
	if r.rec == "" {
		return
	}
	rec := r.rec
	r.rec = ""
	time.Sleep(controlRecordTail)
	r.e.setValue(recordingLabel, "saving the recording…")
	res, err := r.robotRecord("record-stop", rec, "", controlRecordSave)
	switch {
	case err != nil:
		r.e.setValue(recordingLabel, "failed: "+err.Error())
	case res.SavedAs != "":
		r.e.setValue(recordingLabel, fmt.Sprintf("%s (files tab; %s via %s)", res.SavedAs, formatSeconds(res.DurationMs), res.Via))
		if res.SavedID != "" {
			r.e.addRecording(ControlRecording{
				Who:        recordLocalRobot,
				FileID:     res.SavedID,
				Name:       res.SavedAs,
				Video:      isPlayableVideo(res.SavedAs),
				DurationMs: res.DurationMs,
				Via:        res.Via,
			})
		}
	default:
		r.e.setValue(recordingLabel, "recorded but not saved: "+res.SaveError)
	}
}

// isPlayableVideo reports whether a saved recording is a video a browser
// can play -- IANAR saves one where the compositor recorded the screen, and
// a .zip of sampled frames where only screenshots could be taken.
func isPlayableVideo(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".webm", ".mp4", ".mkv":
		return true
	}
	return false
}

func (e *controlEngine) addRecording(rec ControlRecording) {
	e.mu.Lock()
	if e.run != nil {
		e.run.Recordings = append(e.run.Recordings, rec)
	}
	e.mu.Unlock()
	e.broadcast()
}

func formatSeconds(ms int64) string { return fmt.Sprintf("%.1fs", float64(ms)/1000) }

// note shows progress on the current step while it runs.
func (r *controlRun) note(msg string) {
	r.e.updateStep(r.step, func(sr *ControlStepResult) { sr.Message = msg })
}

// waitFor polls cond until it holds, the timeout passes (false) or the run
// is cancelled.
func (r *controlRun) waitFor(timeout time.Duration, cond func() bool) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-r.cancel:
			return false, errControlCancelled
		case <-time.After(controlPoll):
		}
	}
}

// waitForOutput waits for fc to print a line reading exactly want (after
// trimming) since mark.
func (r *controlRun) waitForOutput(fc string, mark int, want string, timeout time.Duration) (bool, error) {
	return r.waitFor(timeout, func() bool {
		for _, ev := range r.e.logsSince(mark) {
			if ev.fc == fc && ev.kind == "output" && strings.TrimSpace(ev.line) == want {
				return true
			}
		}
		return false
	})
}

// robotRun has IANAR carry out steps, saving the run's recording into the
// files area, and returns a one-line summary of what it did.
func (r *controlRun) robotRun(name string, steps []robotStep) (string, error) {
	s := r.s
	if s.reprServer == nil || !s.reprServer.IsHealthy("robot") {
		return "", errors.New("the robot (ianar) isn't connected to this local-representative -- launch it from the system tab")
	}
	runID := randomToken(8)
	req, err := json.Marshal(struct {
		Run      string      `json:"run"`
		Name     string      `json:"name"`
		Steps    []robotStep `json:"steps"`
		Save     bool        `json:"save"`
		NoRecord bool        `json:"no_record,omitempty"` // the run's own recording is already under way
	}{runID, name, steps, true, r.rec != ""})
	if err != nil {
		return "", err
	}
	ch := make(chan RobotRunMsg, 1)
	r.e.mu.Lock()
	r.e.robot[runID] = ch
	r.e.progress[runID] = func(p RobotRunMsg) {
		if p.Step >= 0 && p.Step < len(steps) && p.Status == "running" {
			r.note(fmt.Sprintf("robot: %s…", steps[p.Step].Label))
		}
	}
	r.e.mu.Unlock()
	defer func() {
		r.e.mu.Lock()
		delete(r.e.robot, runID)
		delete(r.e.progress, runID)
		r.e.mu.Unlock()
	}()

	s.reprServer.SendCommand("robot", "__robot:run "+string(req))

	var res RobotRunMsg
	select {
	case res = <-ch:
	case <-time.After(controlRobotTimeout):
		return "", fmt.Errorf("the robot didn't report back within %s", controlRobotTimeout)
	case <-r.cancel:
		return "", errControlCancelled
	}
	if res.SavedAs != "" {
		r.e.addValue("robot run", res.SavedAs+" (files tab)")
	} else if res.SaveError != "" {
		r.e.addValue("robot run", "not saved: "+res.SaveError)
	}
	if !res.Success {
		msg := res.Error
		if msg == "" {
			msg = "the robot run failed"
		}
		return "", fmt.Errorf("robot: %s", msg)
	}
	var notes []string
	for _, st := range res.Steps {
		if st.Message != "" {
			notes = append(notes, st.Message)
		}
	}
	return strings.Join(notes, "; "), nil
}

// randomToken returns n random lower-case letters and digits, leaving out
// the ones OCR easily mixes up (0/o, 1/l/i).
func randomToken(n int) string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			b[i] = alphabet[time.Now().UnixNano()%int64(len(alphabet))]
			continue
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}

// ---- v1 sequences ----

// fcRobotHandoffSequence is the control tab's sample sequence
// (Step3Prompt.md): launch a new FC instance, echo a marker through it over
// the remote interface, have the robot find that terminal by the marker and
// take local control, type a command into it, and hand it back to remote
// control -- checking each step from what that FC instance reports.
func fcRobotHandoffSequence() controlSequence {
	return controlSequence{
		id:          "fc-robot-handoff",
		name:        "federation-command: robot hand-off",
		description: "Launch a new federation-command, mark it over the remote interface, then have the robot find it, take local control, type into it and hand it back.",
		record:      []string{recordLocalRobot},
		steps: []controlStep{
			{
				ControlStepInfo: ControlStepInfo{
					Label:  "Unlock the screen if it is locked",
					Detail: "robot: unlock this node's screen through logind if it is locked, and wake it if it has blanked -- before the recording starts",
				},
				run:             controlRobotUnlock,
				beforeRecording: true,
			},
			{
				ControlStepInfo: ControlStepInfo{
					Label:  "Launch a new federation-command instance",
					Detail: "launch federation-command as the system tab does, then wait for the new instance to connect under its own name, in remote control",
				},
				run: controlLaunchFC,
			},
			{
				ControlStepInfo: ControlStepInfo{
					Label:  `Echo "This is the one - <random-chars>" through the remote interface`,
					Detail: "send the echo to that instance only, and wait for its output to come back -- from it and no other instance",
				},
				run: controlEchoMarker,
			},
			{
				ControlStepInfo: ControlStepInfo{
					Label:  "Find that terminal with the robot and bring it to local control",
					Detail: "robot: click the marker line on screen, press →; then check the instance reports local control",
				},
				run: controlRobotTakeLocal,
			},
			{
				ControlStepInfo: ControlStepInfo{
					Label:  `Type into it with the robot: echo "found it"`,
					Detail: "robot: clear the command line, type the command, press Enter; then check the instance printed found it",
				},
				run: controlRobotType,
			},
			{
				ControlStepInfo: ControlStepInfo{
					Label:  "Put the terminal back into remote control",
					Detail: "robot: press ←; then check the instance reports remote control",
				},
				run: controlRobotGiveBack,
			},
		},
	}
}

// controlRobotUnlock has the robot unlock (and wake) the screen, so its
// later steps act on the desktop rather than the lock screen.
func controlRobotUnlock(r *controlRun) (string, error) {
	return r.robotRun("unlock the screen", []robotStep{
		{Label: "Unlock the screen if it is locked", Do: []map[string]string{{"op": "unlock-screen"}}},
	})
}

// controlLaunchFC launches a new FC and waits for it to connect. It only
// accepts a connection under the new instance's own name, made since the
// launch: commands are routed by that name, so an instance sharing it -- an
// older federation-command build connecting as the bare
// "federation-command", or one already running when the sequence started --
// would take the marker meant for the new one, leaving the robot nothing to
// find on the new terminal (Step3Prompt.md Revision A).
func controlLaunchFC(r *controlRun) (string, error) {
	before := r.s.fcKeys()
	id, err := r.s.launchManaged(fcAppName)
	if err != nil {
		return "", fmt.Errorf("launching federation-command: %v", err)
	}
	r.vars["instance"] = id
	r.e.addValue("federation-command instance", id)
	if others := len(before); others > 0 {
		r.note(fmt.Sprintf("launched %s (%d other instance(s) already connected); waiting for it to connect…", id, others))
	} else {
		r.note(fmt.Sprintf("launched %s; waiting for it to connect…", id))
	}

	var key string
	ok, err := r.waitFor(controlLaunchTimeout, func() bool {
		key = r.s.fcKeyForInstanceID(id)
		return key == id && !before[key] && r.s.fcState(key) == "remote-control"
	})
	if err != nil {
		return "", err
	}
	if !ok {
		switch {
		case key == "":
			return "", fmt.Errorf("%s didn't connect to this local-representative within %s", id, controlLaunchTimeout)
		case key != id:
			return "", fmt.Errorf("%s connected as %q rather than under its own name, so commands for it can reach another instance -- its federation-command binary predates per-instance names; rebuild it", id, key)
		case before[key]:
			return "", fmt.Errorf("an instance calling itself %s was connected before the launch, so the new one can't be told apart from it", id)
		default:
			return "", fmt.Errorf("%s connected but is in %s, not remote control", id, orNone(r.s.fcState(key)))
		}
	}
	r.vars["fc"] = key
	note := fmt.Sprintf("%s is connected in remote control", key)
	if hint := r.s.managedDetachedHint(id); hint != "" {
		note += " (" + hint + ")"
	}
	return note, nil
}

func controlEchoMarker(r *controlRun) (string, error) {
	fc := r.vars["fc"]
	marker := "This is the one - " + randomToken(6)
	r.vars["marker"] = marker
	cmd := `echo "` + marker + `"`
	// Shown as the whole command, never the bare marker: the robot clicks the
	// line reading exactly the marker, and that must be FC's output, not
	// this run's own display of it.
	r.e.addValue("marker command", cmd)
	mark := r.e.logMark()
	r.s.sendFCCommand(fc, cmd)
	ok, err := r.waitForOutput(fc, mark, marker, controlOutputTimeout)
	if err != nil {
		return "", err
	}
	if !ok {
		if other := markerPrintedBy(r.e.logsSince(mark), marker, fc); other != "" {
			return "", fmt.Errorf("the marker came back from %s, not %s -- the echo reached the wrong instance", other, fc)
		}
		return "", fmt.Errorf("%s didn't print the marker within %s", fc, controlOutputTimeout)
	}
	if other := markerPrintedBy(r.e.logsSince(mark), marker, fc); other != "" {
		return "", fmt.Errorf("%s printed the marker, but so did %s -- the robot can't tell their terminals apart", fc, other)
	}
	return fmt.Sprintf("%s printed the marker", fc), nil
}

// markerPrintedBy returns an instance other than fc that printed marker in
// logs, "" if none did.
func markerPrintedBy(logs []fcLogEvent, marker, fc string) string {
	for _, ev := range logs {
		if ev.fc != fc && ev.kind == "output" && strings.TrimSpace(ev.line) == marker {
			return ev.fc
		}
	}
	return ""
}

func controlRobotTakeLocal(r *controlRun) (string, error) {
	fc, marker := r.vars["fc"], r.vars["marker"]
	if hint := r.s.managedDetachedHint(r.vars["instance"]); hint != "" {
		return "", fmt.Errorf("%s has no window for the robot to find: %s", fc, hint)
	}
	note, err := r.robotRun("find "+fc+" and take local control", []robotStep{
		{Label: "Wake the screen", Do: []map[string]string{{"op": "ensure-awake"}}},
		{Label: "Click the marker line in " + fc + "'s terminal", Do: []map[string]string{
			{"op": "click-text", "text": marker, "exact": "true", "pick": "bottom", "timeout": "10s"},
		}},
		{Label: "Press → for local control", Do: []map[string]string{{"op": "key", "keys": "right"}}},
	})
	if err != nil {
		return "", err
	}
	ok, err := r.waitFor(controlStateTimeout, func() bool { return r.s.fcState(fc) == "local-control" })
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("the robot pressed → but %s is still in %s -- the click may have focused another window (%s)", fc, orNone(r.s.fcState(fc)), note)
	}
	return fmt.Sprintf("%s is in local control (%s)", fc, note), nil
}

func controlRobotType(r *controlRun) (string, error) {
	fc := r.vars["fc"]
	mark := r.e.logMark()
	if _, err := r.robotRun("type into "+fc, []robotStep{
		{Label: "Clear the command line", Do: []map[string]string{{"op": "key", "keys": "end ctrl+u"}}},
		{Label: `Type: echo "found it"`, Do: []map[string]string{{"op": "type", "text": `echo "found it"`}}},
		{Label: "Press Enter", Do: []map[string]string{{"op": "key", "keys": "enter"}}},
	}); err != nil {
		return "", err
	}
	ok, err := r.waitForOutput(fc, mark, "found it", controlOutputTimeout)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("the robot typed the command but %s didn't print found it within %s", fc, controlOutputTimeout)
	}
	return fmt.Sprintf("%s printed found it", fc), nil
}

func controlRobotGiveBack(r *controlRun) (string, error) {
	fc := r.vars["fc"]
	if _, err := r.robotRun("hand "+fc+" back to remote control", []robotStep{
		{Label: "Press ← for remote control", Do: []map[string]string{{"op": "key", "keys": "left"}}},
	}); err != nil {
		return "", err
	}
	ok, err := r.waitFor(controlStateTimeout, func() bool { return r.s.fcState(fc) == "remote-control" })
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("the robot pressed ← but %s is still in %s", fc, orNone(r.s.fcState(fc)))
	}
	return fmt.Sprintf("%s is back in remote control", fc), nil
}

func orNone(state string) string {
	if state == "" {
		return "no state (disconnected)"
	}
	return state
}

// managedDetachedHint says how to reach a managed instance hosted in a
// detached tmux/screen session (so it has no window on screen), "" if it
// isn't.
func (s *Server) managedDetachedHint(instanceID string) string {
	s.procMu.Lock()
	p := s.managed[instanceID]
	s.procMu.Unlock()
	if p == nil || !p.detached {
		return ""
	}
	return "it runs in a detached " + p.termLabel + " session with no window -- " + p.attachHint
}
