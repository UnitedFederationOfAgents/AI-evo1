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
	}, vars)
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

	_, err = resolveRecordBlocks([]ControlRecordBlock{{Node: "{{this_node}}", From: 1, To: 2}, {Node: "{{second}}", From: 2, To: 3}}, vars)
	if err == nil || !strings.Contains(err.Error(), "lr-a at step 2") {
		t.Errorf("same node side by side: err = %v", err)
	}
	if _, err := resolveRecordBlocks([]ControlRecordBlock{{Node: "{{later}}", From: 1, To: 1}}, vars); err == nil {
		t.Error("a node from a value saved during the run resolved")
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
	if err := s.control.start("rec", nil); err == nil || !strings.Contains(err.Error(), "no node") {
		t.Fatalf("start = %v, want a missing node", err)
	}
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
