package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"path"
	"strconv"
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
// runs and edits the same library on a chosen host ("__control:" commands,
// see handleCommand).
//
// Sequences are data (Revision C): the definer's actions and the composer's
// sequences live in the control library (controllib.go), and a run compiles
// one with the control values chosen in the runner. The sample sequence the
// tab was first built around, fc-robot-handoff, is the library's built-in
// example (controlexamples.go).
//
// Steps that need the robot hand a short list of sequence-v2 instructions
// to IANAR ("__robot:run <json>", see ianar/lrrun.go) and wait for its
// "robot-run-result".
//
// A sequence can also have a node's screen recorded from before its first
// step to after its last (Revision A): IANAR records on
// "__robot:record-start" and saves the video into the files tab on
// "__robot:record-stop" (ianar/lrrecord.go). Which sequences record, and
// where, is the sequence's own choice (ControlSequenceDef.Record). A saved
// video is listed on the run (ControlRunMsg.Recordings) so the control tab
// can play it back beside the steps it shows (Revision B). Leading steps
// marked before_recording -- the example's screen unlock -- run before the
// recording starts.
//
// Steps can reach other nodes' robots too (Revision F): the node-capture op
// has a node's robot take a native capture into the files tab, relayed
// through agent-coordinator for a node other than this one, and controls of
// type "node" choose among the nodes connected to agent-coordinator (see
// controlnodes.go). The capture-two-nodes example does that for two of them.
//
// And a step can hand the run to the person following it (Revision G): the
// ask-user op shows a message with a continue button and waits for it
// (ControlRunMsg.Prompt), and the output op adds to what the run brings back
// (ControlRunMsg.Output). The you-tell-me example uses them to capture a
// phrase someone echoes into the terminal the robot left ready for them.
//
// For long runs that rebuild another node, a step can also wait for a node
// to connect anew (node-wait-connected) and copy files from it into the
// files tab (node-fetch-file), and fc-expect-output can fail at once on a
// line saying the command failed (fail_text), and look back to the run's
// start for a command sent steps earlier (since) -- see controlnodes.go.

const (
	controlLaunchTimeout = 45 * time.Second // a launched FC connecting in remote control
	controlOutputTimeout = 15 * time.Second // a command's output reaching LR
	controlStateTimeout  = 6 * time.Second  // an FC control-state change after a key press
	controlRobotTimeout  = 2 * time.Minute  // one robot run, recording included
	controlRecordStart   = 15 * time.Second // the robot starting a recording
	controlRecordSave    = 3 * time.Minute  // the robot stopping and uploading a recording
	controlRecordLead    = 1 * time.Second  // recorded before the first step
	controlRecordTail    = 2 * time.Second  // recorded after the last step
	controlAskTimeout    = 30 * time.Minute // someone pressing continue (ask-user)
	controlEchoTimeout   = 5 * time.Second  // an echo typed in local control, and its output, reaching LR
	controlPoll          = 100 * time.Millisecond
	controlMaxLogs       = 500 // FC log lines kept for the current run
)

// recordLocalRobot names, in a sequence's record list, the screen of the
// node this LR runs on, recorded by its own IANAR -- the only one for now.
const recordLocalRobot = "robot"

// ControlStepInfo describes one step of a control sequence: its label, the
// action's description, and what each of its instructions will do.
type ControlStepInfo struct {
	Label  string   `json:"label"`
	Detail string   `json:"detail,omitempty"`
	Do     []string `json:"do,omitempty"`
	Action string   `json:"action,omitempty"`
}

// ControlSequenceInfo describes a sequence the control tab can run, as it
// would run with its controls' defaults.
type ControlSequenceInfo struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Controls    []ControlParam    `json:"controls,omitempty"`
	Steps       []ControlStepInfo `json:"steps"`
	Record      []string          `json:"record,omitempty"` // whose screens a run records
	Error       string            `json:"error,omitempty"`  // why it can't be compiled, if it can't
}

// ControlStepResult is one step's live status in a ControlRunMsg, with what
// it does as compiled for the run.
type ControlStepResult struct {
	ControlStepInfo
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
	Controls   map[string]string   `json:"controls,omitempty"` // the values it ran with
	Status     string              `json:"status"`             // "running" | "success" | "error" | "cancelled"
	Error      string              `json:"error,omitempty"`
	StartedAt  int64               `json:"started_at"` // unix ms
	DurationMs int64               `json:"duration_ms,omitempty"`
	Steps      []ControlStepResult `json:"steps"`
	Values     []ControlValue      `json:"values,omitempty"`
	Recordings []ControlRecording  `json:"recordings,omitempty"`
	Output     []ControlValue      `json:"output,omitempty"` // what the run brings back (the output op)
	Prompt     *ControlPrompt      `json:"prompt,omitempty"` // set while a step waits for continue
	Saved      []ControlSavedRun   `json:"saved,omitempty"`  // where the run was saved to the files tab (controlsave.go)
}

// ControlPrompt is a step waiting for the person following the run
// (ask-user): the control tab shows Message with a continue button, which
// sends "control-continue" (or agent-coordinator's "__control:continue").
type ControlPrompt struct {
	Step    int    `json:"step"`
	Message string `json:"message"`
}

// ControlRecording is a screen recording, a screenshot, or a file fetched
// from a node, that a run saved into the files tab.
type ControlRecording struct {
	Who        string `json:"who"`     // whose screen (recordLocalRobot, or a node's name)
	FileID     string `json:"file_id"` // GET /api/files/<file_id>
	Name       string `json:"name"`
	Video      bool   `json:"video"`           // a video the browser can play, rather than a .zip of frames
	Image      bool   `json:"image,omitempty"` // a screenshot (node-capture), rather than a recording
	File       bool   `json:"file,omitempty"`  // a file fetched from Who (node-fetch-file), rather than a recording
	Path       string `json:"path,omitempty"`  // where on Who a fetched file came from
	DurationMs int64  `json:"duration_ms,omitempty"`
	Via        string `json:"via,omitempty"`
}

// ControlStateMsg is the payload of "control-state" messages, sent to
// browser clients and agent-coordinator. Nodes are the ones a "node"
// control can choose: those connected to agent-coordinator, none while this
// LR isn't.
type ControlStateMsg struct {
	Sequences []ControlSequenceInfo `json:"sequences"`
	Run       *ControlRunMsg        `json:"run,omitempty"`
	Node      string                `json:"node"`  // this LR's own node
	Nodes     []string              `json:"nodes"` // connected to agent-coordinator
}

// controlStep is one step of a compiled control sequence and the code that
// does it. run returns a short note on what happened. Steps marked
// beforeRecording (leading ones only) run before the sequence's recording
// starts.
type controlStep struct {
	ControlStepInfo
	run             func(r *controlRun) (string, error)
	beforeRecording bool
}

// controlSequence is a sequence compiled to run (see compileControlSequence).
type controlSequence struct {
	id, name, description string
	steps                 []controlStep
	// record lists the screens recorded across a run, none for no
	// recording. recordLocalRobot is the only one so far; recording other
	// nodes (through agent-coordinator) would add names here.
	record []string
	// controls are the sequence's own, so a run can check its node
	// controls' values (checkNodeControls).
	controls []ControlParam
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
	s   *Server
	lib *controlLibrary

	mu       sync.Mutex
	run      *ControlRunMsg
	cancelCh chan struct{}
	logs     []fcLogEvent
	logBase  int // count of lines dropped from the front of logs
	robot    map[string]chan RobotRunMsg
	progress map[string]func(RobotRunMsg)
	recs     map[string]chan RobotRecordMsg
	// robotOps are IANAR's ops as robot.<op> specs (see controlops.go), nil
	// until it has reported them.
	robotOps []ControlOpSpec
	// nodes are those connected to agent-coordinator, as it last said
	// (nil while this LR isn't connected); caps and nodeCaps are captures
	// under way, by this node's robot and through agent-coordinator (see
	// controlnodes.go).
	nodes    []string
	caps     map[string]chan RobotCaptureMsg
	nodeCaps map[string]chan NodeCaptureResult

	// nodeConnects counts each node's connections as seen in AC's node
	// lists (node-wait-connected's fresh); nodeFetches are fetches under
	// way through agent-coordinator; fetchAllow is the control-fetch-allow
	// setting, what this node hands over to them (see controlnodes.go).
	nodeConnects map[string]int
	nodeFetches  map[string]chan NodeFetchResult
	fetchAllow   []string

	// promptCh is closed when someone presses continue on the run's
	// prompt, nil while nothing waits for it.
	promptCh chan struct{}
}

// newControlEngine starts with the built-in examples in memory; main
// replaces lib with the one saved on disk (openControlLibrary).
func newControlEngine(s *Server) *controlEngine {
	return &controlEngine{
		s:        s,
		lib:      newControlLibrary(),
		robot:    make(map[string]chan RobotRunMsg),
		progress: make(map[string]func(RobotRunMsg)),
		recs:     make(map[string]chan RobotRecordMsg),
		caps:     make(map[string]chan RobotCaptureMsg),
		nodeCaps: make(map[string]chan NodeCaptureResult),

		nodeConnects: make(map[string]int),
		nodeFetches:  make(map[string]chan NodeFetchResult),
	}
}

func (e *controlEngine) getRobotOps() []ControlOpSpec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.robotOps
}

// setRobotOps takes the ops IANAR reported ("robot-ops").
func (e *controlEngine) setRobotOps(ops []ControlOpSpec) {
	e.mu.Lock()
	e.robotOps = robotControlOps(ops)
	e.mu.Unlock()
	e.broadcastLibrary()
	e.broadcast() // steps' descriptions can now use the robot ops' wording
}

// library returns the "control-library" snapshot.
func (e *controlEngine) library() ControlLibraryMsg {
	return e.lib.snapshot(e.getRobotOps(), e.s.lrName)
}

// maxRelayedLibrary caps the library sent to agent-coordinator: the
// representable link reads a line at a time, up to 64 KiB, and a longer
// one drops the connection.
const maxRelayedLibrary = 56 << 10

// libraryForAC is library(), cut down to fit the link to agent-coordinator
// if it has to be: first without the ops' documentation, then without the
// library itself.
func (e *controlEngine) libraryForAC() ControlLibraryMsg {
	msg := e.library()
	fits := func() bool {
		b, err := json.Marshal(msg)
		return err == nil && len(b) <= maxRelayedLibrary
	}
	if fits() {
		return msg
	}
	msg.Ops, msg.RobotOps = nil, false
	msg.Note = strings.TrimSpace(msg.Note + " The ops' documentation was left out to fit the link to agent-coordinator; instructions are checked when saved.")
	if fits() {
		return msg
	}
	msg.Actions, msg.Sequences = nil, nil
	msg.Note = "This host's control library is too large to send through agent-coordinator; edit it on the host's own control tab."
	return msg
}

func (e *controlEngine) broadcastLibrary() {
	e.s.broadcast("control-library", e.library())
	if ac := e.s.getACClient(); ac != nil {
		ac.SendData("control-library", e.libraryForAC())
	}
}

// sequenceInfos lists the library's sequences as they'd run with their
// controls' defaults.
func (e *controlEngine) sequenceInfos() []ControlSequenceInfo {
	robotOps := e.getRobotOps()
	snap := e.lib.snapshot(robotOps, e.s.lrName)
	actions := map[string]ControlActionDef{}
	for _, a := range snap.Actions {
		actions[a.ID] = a
	}
	var out []ControlSequenceInfo
	for _, def := range snap.Sequences {
		q, _, err := compileControlSequence(def, actions, nil, robotOps, time.Now(), e.s.lrName)
		info := q.info()
		if err != nil {
			info = ControlSequenceInfo{ID: def.ID, Name: def.Name, Description: def.Description, Record: def.Record, Error: err.Error()}
		}
		info.Controls = def.Controls
		out = append(out, info)
	}
	return out
}

// state returns the current control-state snapshot.
func (e *controlEngine) state() ControlStateMsg {
	msg := ControlStateMsg{Sequences: e.sequenceInfos(), Node: e.s.lrName}
	e.mu.Lock()
	msg.Nodes = append([]string{}, e.nodes...)
	e.mu.Unlock()
	msg.Run = e.runCopy()
	return msg
}

// runCopy returns a copy of the current (or most recent) run, nil if none.
func (e *controlEngine) runCopy() *ControlRunMsg {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.run == nil {
		return nil
	}
	cp := *e.run
	cp.Steps = append([]ControlStepResult(nil), e.run.Steps...)
	cp.Values = append([]ControlValue(nil), e.run.Values...)
	cp.Recordings = append([]ControlRecording(nil), e.run.Recordings...)
	cp.Output = append([]ControlValue(nil), e.run.Output...)
	cp.Saved = append([]ControlSavedRun(nil), e.run.Saved...)
	if e.run.Prompt != nil {
		p := *e.run.Prompt
		cp.Prompt = &p
	}
	return &cp
}

func (e *controlEngine) broadcast() {
	st := e.state()
	e.s.broadcast("control-state", st)
	if ac := e.s.getACClient(); ac != nil {
		ac.SendData("control-state", st)
	}
}

// ControlLibRequest is a definer/composer request ("control-lib" from a
// browser, "__control:lib <json>" from agent-coordinator).
type ControlLibRequest struct {
	Req      string              `json:"req"` // the requester's id for it, echoed in the reply
	Op       string              `json:"op"`  // save-action | delete-action | save-sequence | delete-sequence | import | export | restore-examples | save-run
	ID       string              `json:"id,omitempty"`
	PrevID   string              `json:"previous_id,omitempty"`
	Action   *ControlActionDef   `json:"action,omitempty"`
	Sequence *ControlSequenceDef `json:"sequence,omitempty"`
	YAML     string              `json:"yaml,omitempty"`
	Kind     string              `json:"kind,omitempty"` // export: "actions" | "sequences"
	IDs      []string            `json:"ids,omitempty"`  // export: which ones; none for all
}

// ControlLibReply is the "control-reply" payload answering a
// ControlLibRequest (or a run that couldn't start, Op "run").
type ControlLibReply struct {
	Req      string `json:"req,omitempty"`
	Op       string `json:"op"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
	Message  string `json:"message,omitempty"`
	YAML     string `json:"yaml,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// handleLibRequest carries out a library request and, if it changed the
// library, sends everyone the new one.
func (e *controlEngine) handleLibRequest(req ControlLibRequest) ControlLibReply {
	reply := ControlLibReply{Req: req.Req, Op: req.Op}
	var err error
	changed := true
	switch req.Op {
	case "save-action":
		if req.Action == nil {
			err = errors.New("no action given")
			break
		}
		if err = req.Action.validate(e.getRobotOps()); err == nil {
			err = e.lib.saveAction(*req.Action, req.PrevID)
			reply.Message = fmt.Sprintf("saved action %q", req.Action.ID)
		}
	case "delete-action":
		err = e.lib.deleteAction(req.ID)
		reply.Message = fmt.Sprintf("deleted action %q", req.ID)
	case "save-sequence":
		if req.Sequence == nil {
			err = errors.New("no sequence given")
			break
		}
		err = e.lib.saveSequence(*req.Sequence, req.PrevID)
		reply.Message = fmt.Sprintf("saved sequence %q", req.Sequence.ID)
	case "delete-sequence":
		err = e.lib.deleteSequence(req.ID)
		reply.Message = fmt.Sprintf("deleted sequence %q", req.ID)
	case "import":
		var doc controlLibDoc
		if doc, err = decodeControlLib(req.YAML); err == nil {
			doc.Examples = nil // only the saved library records those
			reply.Message, err = e.lib.merge(doc)
			if err == nil {
				reply.Message = "imported: " + reply.Message
			}
		}
	case "export":
		changed = false
		reply.YAML, reply.Filename, err = e.lib.export(req.Kind, req.IDs)
	case "restore-examples":
		reply.Message, err = e.lib.restoreExamples()
		if err == nil {
			reply.Message = "restored the examples: " + reply.Message
		}
	case "save-run":
		// Not a library change: saveRun broadcasts the run (and files) itself.
		changed = false
		var info FileInfo
		if info, err = e.saveRun(req.ID); err == nil {
			reply.Message = "saved to the files tab as " + info.Name
			reply.Filename = info.Name
		}
	default:
		err = fmt.Errorf("unknown library request %q", req.Op)
	}
	if err != nil {
		reply.Error, reply.Message = err.Error(), ""
		return reply
	}
	reply.Success = true
	if changed {
		e.broadcastLibrary()
		e.broadcast()
	}
	return reply
}

// handleCommand handles agent-coordinator's "__control:" commands:
//
//	__control:run <sequence id> [<json control values>]
//	__control:cancel
//	__control:continue [<run id>]
//	__control:lib <json ControlLibRequest>
//
// Replies to lib (and runs that can't start) go back as "control-reply".
func (e *controlEngine) handleCommand(raw string) {
	rest := strings.TrimSpace(strings.TrimPrefix(raw, "__control:"))
	verb, arg, _ := strings.Cut(rest, " ")
	reply := func(r ControlLibReply) {
		if b, err := json.Marshal(r); err == nil && len(b) > maxRelayedLibrary {
			r = ControlLibReply{Req: r.Req, Op: r.Op, Error: "the answer is too large to send through agent-coordinator -- export from this host's own control tab"}
		}
		if ac := e.s.getACClient(); ac != nil {
			ac.SendData("control-reply", r)
		}
	}
	switch verb {
	case "run":
		id, values, _ := strings.Cut(strings.TrimSpace(arg), " ")
		var controls map[string]string
		if strings.TrimSpace(values) != "" {
			if err := json.Unmarshal([]byte(values), &controls); err != nil {
				reply(ControlLibReply{Op: "run", Error: "bad control values: " + err.Error()})
				return
			}
		}
		if err := e.start(id, controls); err != nil {
			log.Printf("control: remote run %q: %v", id, err)
			reply(ControlLibReply{Op: "run", Error: err.Error()})
		}
	case "cancel":
		e.cancel()
	case "continue":
		if err := e.continueRun(strings.TrimSpace(arg)); err != nil {
			log.Printf("control: remote continue: %v", err)
		}
	case "lib":
		var req ControlLibRequest
		if err := json.Unmarshal([]byte(arg), &req); err != nil {
			reply(ControlLibReply{Op: "lib", Error: "bad request: " + err.Error()})
			return
		}
		reply(e.handleLibRequest(req))
	case "refresh":
		e.broadcastLibrary()
		e.broadcast()
	case "nodes", "node-capture", "node-capture-result", "node-fetch", "node-fetch-result":
		e.handleNodeCommand(verb, arg) // see controlnodes.go
	default:
		log.Printf("control: ignoring unrecognised command %q", raw)
	}
}

// start begins a run of sequence id, with values for its controls (missing
// ones take their defaults), in the background.
func (e *controlEngine) start(id string, values map[string]string) error {
	startedAt := time.Now()
	q, vars, err := e.lib.compile(id, values, e.getRobotOps(), startedAt, e.s.lrName)
	if err != nil {
		return err
	}
	if err := e.checkNodeControls(q.controls, vars); err != nil {
		return err
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
		Controls:  values,
		Status:    "running",
		StartedAt: startedAt.UnixMilli(),
	}
	for _, st := range q.steps {
		run.Steps = append(run.Steps, ControlStepResult{ControlStepInfo: st.ControlStepInfo, Status: "pending"})
	}
	e.run = run
	e.cancelCh = make(chan struct{})
	e.logs = nil
	e.logBase = 0
	cancelCh := e.cancelCh
	e.mu.Unlock()

	e.broadcast()
	go e.execute(q, &controlRun{e: e, s: e.s, cancel: cancelCh, vars: vars})
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

// setPrompt shows message as the run's prompt and returns the channel
// continueRun closes.
func (e *controlEngine) setPrompt(step int, message string) <-chan struct{} {
	ch := make(chan struct{})
	e.mu.Lock()
	e.promptCh = ch
	if e.run != nil {
		e.run.Prompt = &ControlPrompt{Step: step, Message: message}
	}
	e.mu.Unlock()
	e.broadcast()
	return ch
}

func (e *controlEngine) clearPrompt() {
	e.mu.Lock()
	e.promptCh = nil
	if e.run != nil {
		e.run.Prompt = nil
	}
	e.mu.Unlock()
	e.broadcast()
}

// continueRun answers the run's prompt (someone pressed continue). runID,
// if given, must be the run's, so a stale page can't continue a later run.
func (e *controlEngine) continueRun(runID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case e.run == nil || e.promptCh == nil:
		return errors.New("no control sequence is waiting for continue")
	case runID != "" && runID != e.run.ID:
		return fmt.Errorf("run %s isn't the one waiting for continue", runID)
	}
	close(e.promptCh)
	e.promptCh = nil
	return nil
}

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
		r.stepMark = e.logMark()
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

// addOutput adds to what the run brings back, shown under its steps.
func (e *controlEngine) addOutput(label, value string) {
	e.mu.Lock()
	if e.run != nil {
		e.run.Output = append(e.run.Output, ControlValue{Label: label, Value: value})
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
	e        *controlEngine
	s        *Server
	cancel   chan struct{}
	step     int
	stepMark int               // the FC log position when the current step started
	vars     map[string]string // built-ins, the sequence's controls, and values saved so far
	rec      string            // the robot recording under way across the run, "" if none
}

// save stores value under name for later instructions' {{name}}.
func (r *controlRun) save(name, value string) error {
	name = strings.TrimSpace(name)
	if !ctlNameRe.MatchString(name) {
		return fmt.Errorf("save_as %q isn't a valid name", name)
	}
	if _, builtin := ctlBuiltins[name]; builtin {
		return fmt.Errorf("save_as %q would hide the built-in {{%s}}", name, name)
	}
	r.vars[name] = value
	return nil
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
			r.note(fmt.Sprintf("%s…", steps[p.Step].Label))
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
	budget := robotRunBudget(steps)
	select {
	case res = <-ch:
	case <-time.After(budget):
		return "", fmt.Errorf("the robot didn't report back within %s", budget)
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

// robotRunBudget is how long to wait for a robot run: controlRobotTimeout,
// plus the timeouts and waits its instructions set themselves, so a run
// told to wait-for-text for 10m isn't given up on after 2. A bare number is
// milliseconds, as IANAR reads it.
func robotRunBudget(steps []robotStep) time.Duration {
	budget := controlRobotTimeout
	for _, st := range steps {
		for _, in := range st.Do {
			for _, k := range []string{"timeout", "duration"} {
				v := strings.TrimSpace(in[k])
				if v == "" {
					continue
				}
				d, err := time.ParseDuration(v)
				if err != nil {
					ms, err := strconv.Atoi(v)
					if err != nil {
						continue
					}
					d = time.Duration(ms) * time.Millisecond
				}
				if d > 0 {
					budget += d
				}
			}
		}
	}
	return budget
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
