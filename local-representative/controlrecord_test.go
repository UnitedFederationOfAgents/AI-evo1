package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// recordingRun puts s's engine in the middle of a run with recording blocks
// (pending), without starting any steps.
func recordingRun(s *Server, blocks ...ControlRecordBlock) *controlRun {
	r := runningEngine(s)
	e := s.control
	e.mu.Lock()
	for k, b := range blocks {
		e.run.Records = append(e.run.Records, ControlRecordState{ControlRecordBlock: b, Status: "pending"})
		r.records = append(r.records, &runRecord{ControlRecordBlock: b, idx: k})
	}
	e.mu.Unlock()
	return r
}

func TestCheckRecordBlocks(t *testing.T) {
	b := func(node string, from, to int) ControlRecordBlock { return ControlRecordBlock{Node: node, From: from, To: to} }
	for _, tc := range []struct {
		name   string
		blocks []ControlRecordBlock
		ok     bool
	}{
		{"none", nil, true},
		{"one across all", []ControlRecordBlock{b("{{this_node}}", 1, 4)}, true},
		{"overlapping, different nodes", []ControlRecordBlock{b("a", 1, 3), b("b", 2, 4), b("{{node}}", 3, 3)}, true},
		{"same node one after another", []ControlRecordBlock{b("a", 1, 2), b("a", 3, 4)}, true},
		{"same node side by side", []ControlRecordBlock{b("a", 1, 2), b("a", 2, 4)}, false},
		{"same node, spaces aside", []ControlRecordBlock{b("a", 1, 2), b(" a ", 2, 2)}, false},
		{"four side by side", []ControlRecordBlock{b("a", 1, 4), b("b", 2, 3), b("c", 3, 3), b("d", 3, 4)}, false},
		{"four, but never at once", []ControlRecordBlock{b("a", 1, 4), b("b", 1, 2), b("c", 2, 2), b("d", 3, 4)}, true},
		{"no node", []ControlRecordBlock{b(" ", 1, 1)}, false},
		{"bad reference", []ControlRecordBlock{b("{{a b}}", 1, 1)}, false},
		{"backwards", []ControlRecordBlock{b("a", 3, 2)}, false},
		{"before the first step", []ControlRecordBlock{b("a", 0, 2)}, false},
		{"past the last step", []ControlRecordBlock{b("a", 2, 5)}, false},
	} {
		if err := checkRecordBlocks(tc.blocks, 4); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

// TestResolveRecordBlocks: nodes are filled in from the controls and
// built-ins, and two references that turn out to be the same node can't
// record side by side.
func TestResolveRecordBlocks(t *testing.T) {
	vars := map[string]string{"this_node": "lr-a", "first": "lr-b", "second": "lr-a", "unset": ""}
	got, err := resolveRecordBlocks([]ControlRecordBlock{
		{Node: "{{this_node}}", From: 1, To: 2, Label: "on {{this_node}}"},
		{Node: "{{first}}", From: 2, To: 3},
		{Node: "{{second}}", From: 3, To: 3},
		{Node: "{{unset}}", From: 1, To: 3},
	}, vars, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []ControlRecordBlock{{Node: "lr-a", From: 1, To: 2, Label: "on lr-a"}, {Node: "lr-b", From: 2, To: 3}, {Node: "lr-a", From: 3, To: 3}, {Node: "", From: 1, To: 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolved %+v, want %+v", got, want)
	}
	if err := checkRecordNodes(got); err == nil || !strings.Contains(err.Error(), "recording 4") {
		t.Errorf("checkRecordNodes = %v, want recording 4 to need a node", err)
	}

	_, err = resolveRecordBlocks([]ControlRecordBlock{{Node: "{{this_node}}", From: 1, To: 2}, {Node: "{{second}}", From: 2, To: 3}}, vars, nil)
	if err == nil || !strings.Contains(err.Error(), "lr-a at step 2") {
		t.Errorf("same node side by side: err = %v", err)
	}
	if _, err := resolveRecordBlocks([]ControlRecordBlock{{Node: "{{later}}", From: 1, To: 1}}, vars, nil); err == nil {
		t.Error("a node from a value nothing saves resolved")
	}
}

// TestRecordNodeSavedDuringRun: a block's node may refer to a value a step
// before it saves -- a node that only joins agent-coordinator during the
// run -- left as written until the block starts; not to one saved at or
// after its first step.
func TestRecordNodeSavedDuringRun(t *testing.T) {
	vars := map[string]string{"this_node": "lr-a", "node": "laptop01"}
	saved := map[string]int{"rebuilt_node": 3}
	got, err := resolveRecordBlocks([]ControlRecordBlock{
		{Node: "{{this_node}}", From: 1, To: 2},
		{Node: "{{rebuilt_node}}", From: 4, To: 5, Label: "{{node}} as {{rebuilt_node}}"},
	}, vars, saved)
	if err != nil {
		t.Fatal(err)
	}
	if got[1].Node != "{{rebuilt_node}}" || !got[1].later() || got[0].later() || got[1].Label != "laptop01 as {{rebuilt_node}}" {
		t.Errorf("resolved %+v", got)
	}
	for _, from := range []int{3, 2} {
		_, err := resolveRecordBlocks([]ControlRecordBlock{{Node: "{{rebuilt_node}}", From: from, To: 5}}, vars, saved)
		if err == nil || !strings.Contains(err.Error(), "before step") {
			t.Errorf("from step %d, saved by step 3: err = %v", from, err)
		}
	}
	if _, err := resolveRecordBlocks([]ControlRecordBlock{{Node: "{{rebuilt_nod}}", From: 4, To: 5}}, vars, saved); err == nil {
		t.Error("a misspelt saved value resolved")
	}
}

// TestCompileRecordsSavedNode: compiling works out which values steps save
// -- save_as, as a step's controls fill it in, or the op's default -- so
// a recording can follow a node-wait-connected.
func TestCompileRecordsSavedNode(t *testing.T) {
	actions := map[string]ControlActionDef{
		"wait":   {ID: "wait", Name: "Wait", Controls: []ControlParam{{Name: "as", Default: "joined"}}, Do: []ControlInstruction{{"op": "node-wait-connected", "node": "{{node}}", "save_as": "{{as}}"}}},
		"launch": {ID: "launch", Name: "Launch", Do: []ControlInstruction{{"op": "launch-fc"}}},
		"shot":   {ID: "shot", Name: "Shot", Do: []ControlInstruction{{"op": "node-capture", "node": "{{rebuilt_node}}"}}},
	}
	savedBy := ctlSavedBy(ControlSequenceDef{Steps: []ControlStepRef{{Action: "launch"}, {Action: "wait", With: map[string]string{"as": "rebuilt_node"}}, {Action: "shot"}}}, actions, map[string]string{})
	if want := map[string]int{"fc": 1, "rebuilt_node": 2}; !reflect.DeepEqual(savedBy, want) {
		t.Errorf("saved by = %v, want %v", savedBy, want)
	}

	q := ControlSequenceDef{ID: "q", Name: "Q", Controls: []ControlParam{{Name: "node", Default: "laptop01"}},
		Steps:      []ControlStepRef{{Action: "launch"}, {Action: "wait", With: map[string]string{"as": "rebuilt_node"}}, {Action: "shot"}},
		Recordings: []ControlRecordBlock{{Node: "{{this_node}}", From: 1, To: 2}, {Node: "{{rebuilt_node}}", From: 3, To: 3}}}
	seq, _, err := compileControlSequence(q, actions, nil, nil, time.Now(), "lr-a")
	if err != nil {
		t.Fatal(err)
	}
	if want := []ControlRecordBlock{{Node: "lr-a", From: 1, To: 2}, {Node: "{{rebuilt_node}}", From: 3, To: 3}}; !reflect.DeepEqual(seq.records, want) {
		t.Errorf("records = %+v, want %+v", seq.records, want)
	}
	q.Steps[1].With = nil // saves joined instead
	if _, _, err := compileControlSequence(q, actions, nil, nil, time.Now(), "lr-a"); err == nil {
		t.Error("compiled a recording of a value no step saves")
	}
}

// TestRecordNodeFilledInWhenItStarts: the run's saved value names the node
// once the block starts, shown on the run; a value never saved fails only
// that recording.
func TestRecordNodeFilledInWhenItStarts(t *testing.T) {
	s := newServer("test-lr")
	r := recordingRun(s, ControlRecordBlock{Node: "{{rebuilt_node}}", From: 1, To: 1, Label: "{{rebuilt_node}} rebuilt"}, ControlRecordBlock{Node: "{{gone}}", From: 1, To: 1})
	r.vars["rebuilt_node"] = "laptop01-ab3d"
	q := exampleSequence(t)
	if r.startRecord(q, r.records[0]) || r.startRecord(q, r.records[1]) {
		t.Fatal("started recording with no agent-coordinator")
	}
	recs := s.control.state().Run.Records
	if recs[0].Node != "laptop01-ab3d" || recs[0].Label != "laptop01-ab3d rebuilt" || !strings.Contains(recs[0].Message, "agent-coordinator") {
		t.Errorf("record 1 = %+v, want laptop01-ab3d, failing for want of agent-coordinator", recs[0])
	}
	if recs[1].Node != "{{gone}}" || recs[1].Status != "error" || !strings.Contains(recs[1].Message, "didn't") {
		t.Errorf("record 2 = %+v, want an error for the value never saved", recs[1])
	}
}

// TestRecordNodeAlreadyRecording: a block whose node turns out, when it
// starts, to be one another block is recording fails rather than taking
// over its recorder.
func TestRecordNodeAlreadyRecording(t *testing.T) {
	s := newServer("test-lr")
	r := recordingRun(s, ControlRecordBlock{Node: "lr-b", From: 1, To: 2}, ControlRecordBlock{Node: "{{again}}", From: 2, To: 2})
	r.records[0].rec = "x"
	r.vars["again"] = "lr-b"
	if r.startRecord(exampleSequence(t), r.records[1]) {
		t.Fatal("started a second recording of lr-b")
	}
	if rec := s.control.state().Run.Records[1]; rec.Status != "error" || !strings.Contains(rec.Message, "recording 1 is already recording lr-b") {
		t.Errorf("record 2 = %+v", rec)
	}
}

// TestRunStartNeedsRecordingNodes: a run doesn't start with a recording
// whose node is left empty.
func TestRunStartNeedsRecordingNodes(t *testing.T) {
	s := newServer("test-lr")
	q := ControlSequenceDef{ID: "rec", Name: "Rec", Controls: []ControlParam{{Name: "where"}},
		Steps:      []ControlStepRef{{Action: "unlock-screen"}},
		Recordings: []ControlRecordBlock{{Node: "{{where}}", From: 1, To: 1}}}
	if err := s.control.lib.saveSequence(q, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.control.start("rec", nil, ""); err == nil || !strings.Contains(err.Error(), "no node") {
		t.Fatalf("start = %v, want a missing node", err)
	}
}

// TestOverrideRecordBlocks: the runner's override recording (Revision D)
// replaces a sequence's blocks with none, or one block of a node across
// every step -- this node, or one connected to agent-coordinator.
func TestOverrideRecordBlocks(t *testing.T) {
	s := newServer("test-lr")
	e := s.control
	if got, err := e.overrideRecordBlocks(controlRecordNone, 5); err != nil || len(got) != 0 {
		t.Errorf("none = %v, %v; want no blocks", got, err)
	}
	for _, node := range []string{"test-lr", "{{this_node}}"} {
		got, err := e.overrideRecordBlocks(node, 5)
		if err != nil || len(got) != 1 || got[0].Node != "test-lr" || got[0].From != 1 || got[0].To != 5 {
			t.Errorf("%s = %+v, %v; want test-lr across steps 1–5", node, got, err)
		}
	}
	if _, err := e.overrideRecordBlocks("lr-b", 5); err == nil || !strings.Contains(err.Error(), "agent-coordinator") {
		t.Errorf("lr-b without agent-coordinator: err = %v", err)
	}
	e.setNodes([]string{"lr-b", "test-lr"})
	if got, err := e.overrideRecordBlocks("lr-b", 3); err != nil || len(got) != 1 || got[0].Node != "lr-b" || got[0].To != 3 {
		t.Errorf("lr-b = %+v, %v", got, err)
	}
	if _, err := e.overrideRecordBlocks("lr-gone", 3); err == nil || !strings.Contains(err.Error(), `"lr-gone"`) {
		t.Errorf("lr-gone: err = %v", err)
	}
}

// TestRunStartOverridesRecordings: overriding with "none" lets a sequence
// whose own recording has no node run, without recordings; overriding with
// a node records it across the whole run, in place of the sequence's own.
func TestRunStartOverridesRecordings(t *testing.T) {
	s := newServer("test-lr")
	q := ControlSequenceDef{ID: "rec", Name: "Rec", Controls: []ControlParam{{Name: "where"}},
		Steps:      []ControlStepRef{{Action: "unlock-screen"}, {Action: "unlock-screen"}},
		Recordings: []ControlRecordBlock{{Node: "{{where}}", From: 2, To: 2}}}
	if err := s.control.lib.saveSequence(q, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.control.start("rec", nil, "none"); err != nil {
		t.Fatalf("start with no recordings: %v", err)
	}
	run := s.control.state().Run
	if run.RecordOverride != "none" || len(run.Records) != 0 {
		t.Errorf("run = override %q, records %+v; want none", run.RecordOverride, run.Records)
	}
	s.control.cancel()
	waitRunDone(t, s)
	if err := s.control.start("rec", nil, "test-lr"); err != nil {
		t.Fatalf("start recording test-lr: %v", err)
	}
	recs := s.control.state().Run.Records
	if len(recs) != 1 || recs[0].Node != "test-lr" || recs[0].From != 1 || recs[0].To != 2 {
		t.Errorf("records = %+v; want test-lr across steps 1–2", recs)
	}
	s.control.cancel()
	waitRunDone(t, s)
}

func waitRunDone(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if run := s.control.state().Run; run == nil || run.Status != "running" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the run didn't finish")
}

// TestLegacyRecordIsOneBlock: a sequence saved before recording blocks
// (record: [robot], leading steps before_recording) reads as one block of
// this node from the first recorded step to the last.
func TestLegacyRecordIsOneBlock(t *testing.T) {
	src := `
format: lr-control-v1
sequences:
  - id: old
    name: Old
    record: [robot]
    steps:
      - action: a
        before_recording: true
      - action: b
      - action: c
`
	doc, err := decodeControlLib(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []ControlRecordBlock{{Node: "{{this_node}}", From: 2, To: 3}}
	if got := doc.Sequences[0].Recordings; !reflect.DeepEqual(got, want) {
		t.Errorf("recordings = %+v, want %+v", got, want)
	}
	if enc := encodeControlLib(doc); strings.Contains(enc, "record:") || strings.Contains(enc, "before_recording") {
		t.Errorf("re-encoded with the old keys:\n%s", enc)
	}

	if _, err := decodeControlLib(strings.Replace(src, "    steps:", "    recordings:\n      - {node: x, from: 1, to: 1}\n    steps:", 1)); err == nil {
		t.Error("decoded a sequence with both record and recordings")
	}
}

func TestRecordBlocksYAMLRoundTrip(t *testing.T) {
	q := ControlSequenceDef{ID: "q", Name: "Q", Steps: []ControlStepRef{{Action: "a"}, {Action: "a"}, {Action: "a"}},
		Recordings: []ControlRecordBlock{{Node: "{{this_node}}", From: 1, To: 3}, {Node: "lr-b", From: 2, To: 2, Label: "the other screen: {{x}}"}}}
	doc := controlLibDoc{Sequences: []ControlSequenceDef{q}}
	src := encodeControlLib(doc)
	got, err := decodeControlLib(src)
	if err != nil {
		t.Fatalf("decoding:\n%s\n%v", src, err)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Errorf("round trip changed it:\n%s\ngot  %+v\nwant %+v", src, got, doc)
	}
	if _, err := decodeControlLib(strings.Replace(src, "from: 2", "from: two", 1)); err == nil {
		t.Error("decoded a recording from step \"two\"")
	}
}

// TestFinishRecordingsSkipsUnstarted: a recording whose first step the run
// never reached is marked skipped; one that failed to start stays failed.
func TestFinishRecordingsSkipsUnstarted(t *testing.T) {
	s := newServer("test-lr")
	r := recordingRun(s, ControlRecordBlock{Node: "test-lr", From: 1, To: 1}, ControlRecordBlock{Node: "lr-b", From: 3, To: 4})
	r.startRecordings(exampleSequence(t), 0) // no robot: fails, not fatal
	r.finishRecordings()
	recs := s.control.state().Run.Records
	if len(recs) != 2 || recs[0].Status != "error" || recs[1].Status != "skipped" {
		t.Errorf("records = %+v", recs)
	}
}

// TestHandsOff: a recording whose node's next recording starts with the
// following step hands the screen straight over (stops without its tail);
// one with a gap, or followed by another node's, doesn't.
func TestHandsOff(t *testing.T) {
	s := newServer("test-lr")
	r := recordingRun(s,
		ControlRecordBlock{Node: "test-lr", From: 2, To: 4},
		ControlRecordBlock{Node: "test-lr", From: 5, To: 7},
		ControlRecordBlock{Node: "lr-b", From: 1, To: 3},
		ControlRecordBlock{Node: "lr-b", From: 5, To: 5},
		ControlRecordBlock{Node: "lr-c", From: 6, To: 6},
	)
	for k, want := range []bool{true, false, false, false, false} {
		if got := r.handsOff(r.records[k]); got != want {
			t.Errorf("recording %d hands off = %v, want %v", k+1, got, want)
		}
	}
}

// TestUpgradeFormerHandoff: an unedited copy of the hand-off as Step4Prompt.md
// first shipped it (steps 2-8 as one block) -- or read from a library saved
// before recording blocks -- takes the two blocks, whatever the library
// recorded for it; an edited one is kept.
func TestUpgradeFormerHandoff(t *testing.T) {
	actions, sequences := exampleControlLibrary()
	former := sequences[0]
	former.Recordings = []ControlRecordBlock{{Node: "{{this_node}}", From: 2, To: 8}}
	rec := map[string]string{ctlExampleKey("sequence", former.ID): "a print from an older encoding"}
	_, got, _, updated := upgradeControlExamples(actions, append([]ControlSequenceDef{former}, sequences[1:]...), rec)
	if !reflect.DeepEqual(got[0].Recordings, sequences[0].Recordings) || len(updated) != 1 {
		t.Errorf("former hand-off not upgraded: %+v, updated %v", got[0].Recordings, updated)
	}

	edited := former
	edited.Recordings = []ControlRecordBlock{{Node: "{{this_node}}", From: 3, To: 8}}
	_, got, _, updated = upgradeControlExamples(actions, append([]ControlSequenceDef{edited}, sequences[1:]...), rec)
	if !reflect.DeepEqual(got[0].Recordings, edited.Recordings) || len(updated) != 0 {
		t.Errorf("edited hand-off replaced: %+v, updated %v", got[0].Recordings, updated)
	}
}

// TestRecordingHere: while a block records this node (or is still saving),
// the robot's own runs mustn't record themselves.
func TestRecordingHere(t *testing.T) {
	s := newServer("test-lr")
	r := recordingRun(s, ControlRecordBlock{Node: "lr-b", From: 1, To: 1}, ControlRecordBlock{Node: "test-lr", From: 1, To: 2})
	if r.recordingHere() {
		t.Fatal("recording here before anything started")
	}
	r.records[0].rec = "x"
	if r.recordingHere() {
		t.Fatal("another node's recording counts as here")
	}
	r.records[1].rec = "y"
	if !r.recordingHere() {
		t.Fatal("this node's recording doesn't count")
	}
	r.records[1].rec = ""
	r.records[1].done = make(chan struct{})
	if !r.recordingHere() {
		t.Fatal("a recording still being saved doesn't count")
	}
	close(r.records[1].done)
	if r.recordingHere() {
		t.Fatal("a saved recording still counts")
	}
}

func TestRecordOnNeedsAgentCoordinator(t *testing.T) {
	s := newServer("test-lr")
	if _, err := s.control.recordOn("lr-b", "record-start", "k", "x", time.Second); err == nil || !strings.Contains(err.Error(), "agent-coordinator") {
		t.Fatalf("err = %v, want not connected to agent-coordinator", err)
	}
}

func TestNoteNodeRecordDelivers(t *testing.T) {
	s := newServer("test-lr")
	e := s.control
	ch := make(chan NodeRecordResult, 1)
	e.mu.Lock()
	e.nodeRecs["r1"] = ch
	e.mu.Unlock()
	e.handleNodeCommand("node-record-result", `{"req":"other","success":true}`)
	e.handleNodeCommand("node-record-result", `{"req":"r1","node":"lr-b","success":true,"record":{"rec":"k","status":"saved","saved_as":"v.webm","saved_id":"id_v.webm"}}`)
	e.handleNodeCommand("node-record-result", `{"req":"r1","success":true}`) // a duplicate doesn't block
	if got := <-ch; got.Node != "lr-b" || got.Record.SavedID != "id_v.webm" {
		t.Errorf("delivered %+v", got)
	}
}
