package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// This file is a control sequence's recordings
// (condocs/initialRobotImpls/Step4Prompt.md): rather than one recording of
// this node's screen across the whole run, a sequence has recording blocks,
// each recording one node's screen across a span of its steps. The composer
// shows them as slim blocks beside the steps:
//
//   - a block spans steps From to To (1-based, inclusive): its recording
//     starts just before step From runs and stops just after step To ends
//     (or when the run ends, if it ends sooner);
//   - blocks may overlap, up to controlMaxParallelRecordings at any step;
//   - two blocks covering the same step never record the same node -- a
//     node's robot records one thing at a time;
//   - Node is a node connected to agent-coordinator, as a literal name or a
//     {{reference}} to the sequence's controls and built-ins ({{this_node}},
//     a node control), filled in when the run starts.
//
// This node's screen is recorded by its own robot ("__robot:record-start" /
// "__robot:record-stop", ianar/lrrecord.go). Another node's is recorded by
// that node's robot, asked through agent-coordinator the way node-capture
// asks for a screenshot (see controlnodes.go):
//
//	this LR  --"node-record" {req, node, verb, rec, name}-->  AC
//	AC       --"__control:node-record" {req, from, ...}-->  node's LR
//	node's LR has its robot start (or stop and save) the recording, then
//	node's LR --"node-record-result" {req, from, node, ...}-->  AC
//	AC       --"__control:node-record-result" {...}-->  this LR
//
// and once stopped this LR copies the video from the node's files tab into
// its own, through AC's /host/<node>/* proxy, so every recording a run made
// is gathered beside its steps (ControlRunMsg.Recordings) and in its saved
// results (controlsave.go).
//
// A recording that can't start or be saved is noted on the run
// (ControlRunMsg.Records), not fatal: the sequence runs regardless.
//
// Libraries saved before recording blocks (record: [robot], with leading
// steps marked before_recording) are read as one block of this node from
// the first step not marked before_recording to the last.

const (
	controlMaxParallelRecordings = 3
	controlRecordRelayExtra      = 15 * time.Second // on top of the node's own wait, for the relay through AC
	controlRecordCopyTimeout     = 10 * time.Minute // copying another node's recording into this files tab
	controlMaxRecordingCopy      = 1 << 30          // the most a copied recording may be
)

// ControlRecordBlock is one recording block of a sequence: Node's screen
// recorded from step From to step To (1-based, inclusive). In a sequence's
// definition Node may use {{references}}; in ControlSequenceInfo and
// ControlRecordState it is the node as the run fills it in.
type ControlRecordBlock struct {
	Node  string `json:"node"`
	From  int    `json:"from"`
	To    int    `json:"to"`
	Label string `json:"label,omitempty"`
}

// ControlRecordState is a recording block's progress in a run.
type ControlRecordState struct {
	ControlRecordBlock
	Status  string `json:"status"` // "pending" | "starting" | "recording" | "saving" | "saved" | "error" | "skipped"
	Message string `json:"message,omitempty"`
}

func (b ControlRecordBlock) steps() string {
	if b.From == b.To {
		return fmt.Sprintf("step %d", b.From)
	}
	return fmt.Sprintf("steps %d–%d", b.From, b.To)
}

// covers reports whether the block records step i (1-based).
func (b ControlRecordBlock) covers(i int) bool { return b.From <= i && i <= b.To }

// checkRecordBlocks checks a sequence's blocks against its nSteps steps:
// each within them, at most controlMaxParallelRecordings at any step, and
// no node twice at a step. Nodes are compared as written, so check again
// once references are filled in (compileControlSequence).
func checkRecordBlocks(blocks []ControlRecordBlock, nSteps int) error {
	for k, b := range blocks {
		what := fmt.Sprintf("recording %d", k+1)
		if strings.TrimSpace(b.Node) == "" {
			return fmt.Errorf("%s has no node", what)
		}
		if err := ctlCheckTemplate(b.Node); err != nil {
			return fmt.Errorf("%s's node: %v", what, err)
		}
		if err := ctlCheckTemplate(b.Label); err != nil {
			return fmt.Errorf("%s's label: %v", what, err)
		}
		if b.From < 1 || b.To < b.From || b.To > nSteps {
			return fmt.Errorf("%s spans steps %d to %d; it has to be within steps 1 to %d, first to last", what, b.From, b.To, nSteps)
		}
	}
	for i := 1; i <= nSteps; i++ {
		var at []int
		for k, b := range blocks {
			if b.covers(i) {
				at = append(at, k)
			}
		}
		if len(at) > controlMaxParallelRecordings {
			return fmt.Errorf("step %d has %d recordings; at most %d can run side by side", i, len(at), controlMaxParallelRecordings)
		}
		for x := 0; x < len(at); x++ {
			for y := x + 1; y < len(at); y++ {
				a, b := blocks[at[x]], blocks[at[y]]
				if strings.TrimSpace(a.Node) == strings.TrimSpace(b.Node) {
					return fmt.Errorf("recordings %d and %d both record %s at step %d; a node can only be in one recording at a time", at[x]+1, at[y]+1, strings.TrimSpace(a.Node), i)
				}
			}
		}
	}
	return nil
}

// legacyRecordBlocks reads a sequence saved before recording blocks: record
// lists whose screens were recorded across the run ("robot", this node's,
// the only one there was), from the first step not marked
// before_recording to the last.
func legacyRecordBlocks(record []string, before []bool) []ControlRecordBlock {
	from := 0
	for i, b := range before {
		if !b {
			from = i + 1
			break
		}
	}
	if from == 0 {
		return nil
	}
	var out []ControlRecordBlock
	for _, who := range record {
		node := strings.TrimSpace(who)
		if node == recordLocalRobot {
			node = "{{this_node}}"
		}
		out = append(out, ControlRecordBlock{Node: node, From: from, To: len(before)})
	}
	return out
}

// encodeRecordBlocks writes a sequence's recordings: as encodeControlLib's
// w.line(4, ...) lines.
func encodeRecordBlocks(w *yamlWriter, blocks []ControlRecordBlock) {
	if len(blocks) == 0 {
		return
	}
	w.line(4, "recordings:")
	for _, b := range blocks {
		// Step numbers plain, not quoted as flowMap would.
		s := fmt.Sprintf("{node: %s, from: %d, to: %d", yamlScalar(b.Node), b.From, b.To)
		if b.Label != "" {
			s += ", label: " + yamlScalar(b.Label)
		}
		w.line(6, "- "+s+"}")
	}
}

func decodeRecordBlocks(n *yNode) ([]ControlRecordBlock, error) {
	items, err := ctlItems(n, "recordings")
	if err != nil {
		return nil, err
	}
	var out []ControlRecordBlock
	for _, it := range items {
		m, err := ctlStringMap(it, "recording")
		if err != nil {
			return nil, err
		}
		if err := ctlOnlyKeys(it, "node", "from", "to", "label"); err != nil {
			return nil, err
		}
		b := ControlRecordBlock{Node: m["node"], Label: m["label"]}
		for _, f := range []struct {
			key string
			dst *int
		}{{"from", &b.From}, {"to", &b.To}} {
			if *f.dst, err = strconv.Atoi(strings.TrimSpace(m[f.key])); err != nil {
				return nil, fmt.Errorf("line %d: a recording's %s should be a step number, not %q", it.line, f.key, m[f.key])
			}
		}
		out = append(out, b)
	}
	return out, nil
}

// resolveRecordBlocks fills in the blocks' node and label references from
// vars (the built-ins and the sequence's controls) and checks no node is
// recorded twice at a step. A node left empty (a node control not yet
// chosen) is caught when the run starts (checkRecordNodes).
func resolveRecordBlocks(blocks []ControlRecordBlock, vars map[string]string) ([]ControlRecordBlock, error) {
	lookup := func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
	out := make([]ControlRecordBlock, len(blocks))
	for k, b := range blocks {
		node, err := ctlExpand(b.Node, lookup)
		if err != nil {
			return nil, fmt.Errorf("recording %d's node can only use the sequence's controls and built-ins: %v", k+1, err)
		}
		label, _ := ctlExpand(b.Label, lookup)
		out[k] = ControlRecordBlock{Node: strings.TrimSpace(node), From: b.From, To: b.To, Label: label}
	}
	for x := range out {
		for y := x + 1; y < len(out); y++ {
			a, b := out[x], out[y]
			if a.Node == "" || a.Node != b.Node || a.To < b.From || b.To < a.From {
				continue
			}
			step := a.From
			if b.From > step {
				step = b.From
			}
			return nil, fmt.Errorf("recordings %d and %d would both record %s at step %d; a node can only be in one recording at a time", x+1, y+1, a.Node, step)
		}
	}
	return out, nil
}

// checkRecordNodes checks each of a run's recordings names a node.
func checkRecordNodes(blocks []ControlRecordBlock) error {
	for k, b := range blocks {
		if b.Node == "" {
			return fmt.Errorf("recording %d (%s) has no node -- choose one for the control it uses", k+1, b.steps())
		}
	}
	return nil
}

// ---- Recording during a run ----

// runRecord is one of a run's recordings under way.
type runRecord struct {
	ControlRecordBlock
	idx  int           // in ControlRunMsg.Records
	rec  string        // the robot's id for it while it records, "" otherwise
	done chan struct{} // closed once it has stopped and been saved; nil until it stops
}

func (rr *runRecord) stopping() bool {
	if rr.done == nil {
		return false
	}
	select {
	case <-rr.done:
		return false
	default:
		return true
	}
}

// setRecord updates the run's record k.
func (e *controlEngine) setRecord(k int, status, msg string) {
	e.mu.Lock()
	if e.run != nil && k < len(e.run.Records) {
		e.run.Records[k].Status, e.run.Records[k].Message = status, msg
	}
	e.mu.Unlock()
	e.broadcast()
}

// recordingHere reports whether this node's robot is recording for the run
// (or still finishing one), so its robot runs shouldn't record themselves.
func (r *controlRun) recordingHere() bool {
	for _, rr := range r.records {
		if rr.Node == r.s.lrName && (rr.rec != "" || rr.stopping()) {
			return true
		}
	}
	return false
}

// startRecordings starts the recordings that begin at step i (0-based).
func (r *controlRun) startRecordings(q controlSequence, i int) {
	started := false
	for _, rr := range r.records {
		if rr.From == i+1 {
			started = r.startRecord(q, rr) || started
		}
	}
	if started {
		time.Sleep(controlRecordLead)
	}
}

// stopRecordings stops, in the background, the recordings that end with step
// i (0-based). One whose node's next recording starts with the next step
// (back-to-back blocks, Step4Prompt.md Revision A) stops without its tail:
// the next one picks up the screen, as soon as this one lets go of the
// node's recorder.
func (r *controlRun) stopRecordings(i int) {
	for _, rr := range r.records {
		if rr.To == i+1 && rr.rec != "" {
			r.stopRecord(rr, !r.handsOff(rr))
		}
	}
}

// handsOff reports whether another of the run's recordings of rr's node
// starts with the step after rr's last.
func (r *controlRun) handsOff(rr *runRecord) bool {
	for _, next := range r.records {
		if next != rr && next.Node == rr.Node && next.From == rr.To+1 {
			return true
		}
	}
	return false
}

// finishRecordings stops every recording still under way, marks those that
// never started skipped, and waits for all of them to be saved.
func (r *controlRun) finishRecordings() {
	for _, rr := range r.records {
		switch {
		case rr.rec != "":
			r.stopRecord(rr, true)
		case rr.done == nil:
			r.e.mu.Lock()
			pending := r.e.run != nil && rr.idx < len(r.e.run.Records) && r.e.run.Records[rr.idx].Status == "pending"
			r.e.mu.Unlock()
			if pending {
				r.e.setRecord(rr.idx, "skipped", fmt.Sprintf("the run ended before step %d", rr.From))
			}
		}
	}
	for _, rr := range r.records {
		if rr.done != nil {
			<-rr.done
		}
	}
}

// startRecord starts rr. It reports whether it started. An earlier
// recording of the same node still stopping needn't be waited for here:
// the node's robot waits for it to let go of the recorder (up to
// ianar's lrRecordHandoffWait), not for it to be saved.
func (r *controlRun) startRecord(q controlSequence, rr *runRecord) bool {
	msg := ""
	for _, other := range r.records {
		if other != rr && other.Node == rr.Node && other.stopping() {
			msg = "taking over " + rr.Node + "'s screen from the recording before it…"
		}
	}
	r.e.setRecord(rr.idx, "starting", msg)
	rec := randomToken(8)
	name := fmt.Sprintf("%s-%s-steps-%d-%d", q.id, rr.Node, rr.From, rr.To)
	var err error
	if rr.Node == r.s.lrName {
		_, err = r.e.robotRecord("record-start", rec, name, controlRecordStart)
	} else {
		_, err = r.e.recordOn(rr.Node, "record-start", rec, name, controlRecordStart)
	}
	if err != nil {
		r.e.setRecord(rr.idx, "error", "not recording: "+err.Error())
		return false
	}
	rr.rec = rec
	r.e.setRecord(rr.idx, "recording", "")
	return true
}

// stopRecord stops rr in the background, after the tail if tail is set, and
// saves it into this files tab; rr.done is closed once it's done.
func (r *controlRun) stopRecord(rr *runRecord, tail bool) {
	rec := rr.rec
	rr.rec = ""
	rr.done = make(chan struct{})
	go func() {
		defer close(rr.done)
		if tail {
			time.Sleep(controlRecordTail)
		}
		r.e.setRecord(rr.idx, "saving", "")
		here := rr.Node == r.s.lrName
		var res RobotRecordMsg
		var err error
		if here {
			res, err = r.e.robotRecord("record-stop", rec, "", controlRecordSave)
		} else {
			res, err = r.e.recordOn(rr.Node, "record-stop", rec, "", controlRecordSave)
		}
		switch {
		case err != nil:
			r.e.setRecord(rr.idx, "error", "failed: "+err.Error())
			return
		case res.SavedAs == "" || res.SavedID == "":
			r.e.setRecord(rr.idx, "error", "recorded but not saved: "+res.SaveError)
			return
		}
		fileID, name := res.SavedID, res.SavedAs
		if !here {
			r.e.setRecord(rr.idx, "saving", fmt.Sprintf("%s saved %s; copying it into this node's files tab…", rr.Node, res.SavedAs))
			info, err := r.s.copyNodeFileLimit(rr.Node, res.SavedID, res.SavedAs, controlMaxRecordingCopy, controlRecordCopyTimeout)
			if err != nil {
				r.e.setRecord(rr.idx, "error", fmt.Sprintf("%s saved %s in its own files tab, but copying it here failed: %v", rr.Node, res.SavedAs, err))
				return
			}
			fileID, name = info.ID, info.Name
		}
		r.e.addRecording(ControlRecording{
			Who:        rr.Node,
			FileID:     fileID,
			Name:       name,
			Video:      isPlayableVideo(name),
			DurationMs: res.DurationMs,
			Via:        res.Via,
			From:       rr.From,
			To:         rr.To,
			Label:      rr.Label,
		})
		r.e.setRecord(rr.idx, "saved", fmt.Sprintf("%s (files tab; %s via %s)", name, formatSeconds(res.DurationMs), res.Via))
	}()
}

// robotRecord sends this node's robot a record-start or record-stop for rec
// and waits for its answer.
func (e *controlEngine) robotRecord(verb, rec, name string, timeout time.Duration) (RobotRecordMsg, error) {
	s := e.s
	if !s.robotUp() {
		return RobotRecordMsg{}, fmt.Errorf("the robot (ianar) isn't connected to %s's local-representative", s.lrName)
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
	e.mu.Lock()
	e.recs[rec] = ch
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.recs, rec)
		e.mu.Unlock()
	}()
	s.reprServer.SendCommand("robot", "__robot:"+verb+" "+string(req))
	select {
	case res := <-ch:
		if res.Status == "error" {
			return res, errors.New(res.Error)
		}
		return res, nil
	case <-time.After(timeout):
		return RobotRecordMsg{}, fmt.Errorf("%s's robot didn't answer within %s", s.lrName, timeout)
	}
}

// NodeRecordRequest asks another node's robot to start or stop a recording:
// sent to AC as "node-record", and by AC to that node as
// "__control:node-record" with From set to the node asking.
type NodeRecordRequest struct {
	Req  string `json:"req"`
	From string `json:"from,omitempty"`
	Node string `json:"node"`
	Verb string `json:"verb"` // "record-start" | "record-stop"
	Rec  string `json:"rec"`
	Name string `json:"name,omitempty"`
}

// NodeRecordResult is the node's answer: sent to AC as
// "node-record-result", and by AC back to From as
// "__control:node-record-result". Record is its robot's answer; a stopped
// recording is saved in the node's own files tab (Record.SavedID).
type NodeRecordResult struct {
	Req     string         `json:"req"`
	From    string         `json:"from,omitempty"`
	Node    string         `json:"node"`
	Success bool           `json:"success"`
	Error   string         `json:"error,omitempty"`
	NoRobot bool           `json:"no_robot,omitempty"`
	Record  RobotRecordMsg `json:"record"`
}

// recordOn asks node's robot, through AC, to start or stop recording rec,
// waiting up to the node's own timeout and the relay's.
func (e *controlEngine) recordOn(node, verb, rec, name string, timeout time.Duration) (RobotRecordMsg, error) {
	ac := e.s.getACClient()
	if ac == nil {
		return RobotRecordMsg{}, fmt.Errorf("this local-representative isn't connected to agent-coordinator, so it can't reach %s", node)
	}
	req := randomToken(8)
	ch := make(chan NodeRecordResult, 1)
	e.mu.Lock()
	e.nodeRecs[req] = ch
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.nodeRecs, req)
		e.mu.Unlock()
	}()
	ac.SendData("node-record", NodeRecordRequest{Req: req, Node: node, Verb: verb, Rec: rec, Name: name})
	select {
	case res := <-ch:
		if !res.Success {
			msg := res.Error
			if msg == "" {
				msg = "the recording failed"
			}
			return res.Record, fmt.Errorf("%s: %s", node, msg)
		}
		return res.Record, nil
	case <-time.After(timeout + controlRecordRelayExtra):
		return RobotRecordMsg{}, fmt.Errorf("%s didn't answer through agent-coordinator within %s", node, timeout+controlRecordRelayExtra)
	}
}

// recordForNode has this node's robot start or stop the recording another
// node asked for, and answers it through AC.
func (e *controlEngine) recordForNode(req NodeRecordRequest) {
	log.Printf("control: %q asked through agent-coordinator to %s on this node (%s)", req.From, req.Verb, req.Rec)
	out := NodeRecordResult{Req: req.Req, From: req.From, Node: e.s.lrName}
	timeout := controlRecordStart
	switch req.Verb {
	case "record-start":
	case "record-stop":
		timeout = controlRecordSave
	default:
		out.Error = fmt.Sprintf("there's no recording request %q", req.Verb)
	}
	if out.Error == "" {
		res, err := e.robotRecord(req.Verb, req.Rec, req.Name, timeout)
		out.Record = res
		if err != nil {
			out.Error = err.Error()
			out.NoRobot = !e.s.robotUp()
		} else {
			out.Success = true
		}
	}
	if ac := e.s.getACClient(); ac != nil {
		ac.SendData("node-record-result", out)
	}
}

// noteNodeRecord delivers a node's "node-record-result" to the run waiting
// on it.
func (e *controlEngine) noteNodeRecord(res NodeRecordResult) {
	e.mu.Lock()
	ch := e.nodeRecs[res.Req]
	e.mu.Unlock()
	if ch != nil {
		select {
		case ch <- res:
		default: // nobody waiting any more
		}
	}
}
