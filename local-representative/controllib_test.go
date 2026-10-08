package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExampleLibraryIsValid(t *testing.T) {
	actions, sequences := exampleControlLibrary()
	if err := checkControlLibrary(actions, sequences); err != nil {
		t.Fatal(err)
	}
}

// TestControlLibYAMLRoundTrip: what export writes, import reads back the
// same.
func TestControlLibYAMLRoundTrip(t *testing.T) {
	actions, sequences := exampleControlLibrary()
	doc := controlLibDoc{Actions: actions, Sequences: sequences, Examples: map[string]string{"action/x": "abc"}}
	src := encodeControlLib(doc)
	got, err := decodeControlLib(src)
	if err != nil {
		t.Fatalf("decoding:\n%s\n%v", src, err)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Errorf("round trip changed the library:\n%s\ngot  %+v\nwant %+v", src, got, doc)
	}
}

func TestDecodeControlLibByHand(t *testing.T) {
	src := `
format: lr-control-v1
actions:
  - id: hello
    name: Say hello
    controls:
      - name: who
        default: world
    do:
      - {op: set, save_as: greeting, value: "hello {{who}}"}
      - op: show
        label: greeting
        value: '{{greeting}}'
sequences:
  - id: greet
    name: Greet
    record: []
    steps:
      - action: hello
        with:
          who: there
`
	doc, err := decodeControlLib(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkControlLibrary(doc.Actions, doc.Sequences); err != nil {
		t.Fatal(err)
	}
	if v := doc.Actions[0].Do[1]["value"]; v != "{{greeting}}" {
		t.Errorf("show value = %q", v)
	}
	if w := doc.Sequences[0].Steps[0].With["who"]; w != "there" {
		t.Errorf("with.who = %q", w)
	}
}

func TestDecodeControlLibRejects(t *testing.T) {
	for name, src := range map[string]string{
		"unknown key":   "actions:\n  - id: a\n    name: A\n    dox: []\n",
		"robot library": "format: ianar-sequence-v2\nactions:\n  - id: a\n",
		"empty":         "format: lr-control-v1\n",
		"not a list":    "actions: hello\n",
	} {
		if _, err := decodeControlLib(src); err == nil {
			t.Errorf("%s: decoded without an error", name)
		}
	}
}

func TestValidateControlInstruction(t *testing.T) {
	robotOps := robotControlOps([]ControlOpSpec{{Op: "key", Args: []ControlOpArg{{Name: "keys", Required: true}}}})
	for _, tc := range []struct {
		in       ControlInstruction
		robotOps []ControlOpSpec
		ok       bool
	}{
		{ControlInstruction{"op": "fc-send", "command": "ls"}, nil, true},
		{ControlInstruction{"op": "fc-send"}, nil, false},
		{ControlInstruction{"op": "fc-send", "command": "ls", "nope": "x"}, nil, false},
		{ControlInstruction{"op": "no-such-op"}, nil, false},
		// IANAR hasn't said which ops it has: checked when it runs.
		{ControlInstruction{"op": "robot.anything", "x": "y"}, nil, true},
		{ControlInstruction{"op": "robot.key", "keys": "right"}, robotOps, true},
		{ControlInstruction{"op": "robot.key"}, robotOps, false},
		{ControlInstruction{"op": "robot.nope"}, robotOps, false},
		{ControlInstruction{"op": "set", "save_as": "x", "value": "{{bad name}}"}, nil, false},
	} {
		err := validateControlInstruction(tc.in, tc.robotOps)
		if (err == nil) != tc.ok {
			t.Errorf("%v: err = %v, want ok=%v", tc.in, err, tc.ok)
		}
	}
}

func TestSequenceValidation(t *testing.T) {
	actions := map[string]ControlActionDef{"a": {ID: "a", Name: "A", Controls: []ControlParam{{Name: "x"}}, Do: []ControlInstruction{{"op": "show", "label": "x"}}}}
	for _, tc := range []struct {
		name string
		q    ControlSequenceDef
		ok   bool
	}{
		{"fine", ControlSequenceDef{ID: "q", Name: "Q", Steps: []ControlStepRef{{Action: "a", With: map[string]string{"x": "1"}}}}, true},
		{"missing action", ControlSequenceDef{ID: "q", Name: "Q", Steps: []ControlStepRef{{Action: "b"}}}, false},
		{"unknown control", ControlSequenceDef{ID: "q", Name: "Q", Steps: []ControlStepRef{{Action: "a", With: map[string]string{"y": "1"}}}}, false},
		{"recording", ControlSequenceDef{ID: "q", Name: "Q", Steps: []ControlStepRef{{Action: "a"}, {Action: "a"}}, Recordings: []ControlRecordBlock{{Node: "elsewhere", From: 1, To: 2}}}, true},
		{"recording past the end", ControlSequenceDef{ID: "q", Name: "Q", Steps: []ControlStepRef{{Action: "a"}}, Recordings: []ControlRecordBlock{{Node: "{{this_node}}", From: 1, To: 2}}}, false},
		{"bad id", ControlSequenceDef{ID: "Q Q", Name: "Q", Steps: []ControlStepRef{{Action: "a"}}}, false},
		{"no steps", ControlSequenceDef{ID: "q", Name: "Q"}, false},
	} {
		if err := tc.q.validate(actions); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

// TestCompileControlSequence: controls take the runner's values, then their
// defaults; labels and details are filled in as far as they can be before
// the run.
func TestCompileControlSequence(t *testing.T) {
	actions := map[string]ControlActionDef{
		"hello": {ID: "hello", Name: "Say hello to {{who}}", Controls: []ControlParam{{Name: "who", Default: "{{target}}"}},
			Do: []ControlInstruction{{"op": "show", "label": "greeting", "value": "hello {{who}} from {{later}}"}}},
	}
	q := ControlSequenceDef{ID: "greet", Name: "Greet", Controls: []ControlParam{{Name: "target", Default: "world"}},
		Steps: []ControlStepRef{{Action: "hello"}, {Action: "hello", Label: "Again", With: map[string]string{"who": "you"}}}}

	seq, vars, err := compileControlSequence(q, actions, map[string]string{"target": "everyone"}, nil, time.Now(), "test-lr")
	if err != nil {
		t.Fatal(err)
	}
	if vars["target"] != "everyone" || vars["timestamp"] == "" || vars["this_node"] != "test-lr" {
		t.Errorf("vars = %v", vars)
	}
	if got := seq.steps[0].Label; got != "Say hello to everyone" {
		t.Errorf("step 1 label = %q", got)
	}
	if got := seq.steps[0].Do; len(got) != 1 || got[0] != "show greeting: hello everyone from {{later}}" {
		t.Errorf("step 1 do = %q", got)
	}
	if got := seq.steps[1].Do[0]; got != "show greeting: hello you from {{later}}" {
		t.Errorf("step 2 do = %q", got)
	}

	if _, _, err := compileControlSequence(q, actions, map[string]string{"target": "{{nope}}"}, nil, time.Now(), "test-lr"); err == nil {
		t.Error("a control value referring to nothing compiled")
	}
}

// TestActionRunnerNativeOps runs an action of LR's own ops end to end.
func TestActionRunnerNativeOps(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)
	r.vars["target"] = "world"
	a := ControlActionDef{ID: "a", Name: "A", Controls: []ControlParam{{Name: "who", Default: "{{target}}"}}, Do: []ControlInstruction{
		{"op": "random", "save_as": "tok", "length": "5"},
		{"op": "set", "save_as": "greeting", "value": "hello {{who}} {{tok}}"},
		{"op": "show", "label": "greeting", "value": "{{greeting}}"},
	}}
	run := controlActionRunner(a, ControlStepRef{Action: "a"}, nil)
	if _, err := run(r); err != nil {
		t.Fatal(err)
	}
	if len(r.vars["tok"]) != 5 {
		t.Errorf("tok = %q", r.vars["tok"])
	}
	vals := s.control.state().Run.Values
	if len(vals) != 1 || vals[0].Value != "hello world "+r.vars["tok"] {
		t.Errorf("values = %+v", vals)
	}
}

// TestActionRunnerStopsAtFirstFailure: the instruction that fails is named,
// with the action's hint.
func TestActionRunnerStopsAtFirstFailure(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)
	a := ControlActionDef{ID: "a", Name: "A", Do: []ControlInstruction{
		{"op": "fc-expect-state", "fc": "federation-command#9", "state": "local-control", "timeout": "200ms", "hint": "check the window"},
		{"op": "set", "save_as": "after", "value": "x"},
	}}
	_, err := controlActionRunner(a, ControlStepRef{Action: "a"}, nil)(r)
	if err == nil || !strings.Contains(err.Error(), "instruction 1") || !strings.Contains(err.Error(), "check the window") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := r.vars["after"]; ok {
		t.Error("ran past the failure")
	}
}

// TestActionRunnerNeedsTheRobotForRobotOps: robot.* instructions go to IANAR.
func TestActionRunnerNeedsTheRobotForRobotOps(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)
	a := ControlActionDef{ID: "a", Name: "A", Do: []ControlInstruction{{"op": "robot.key", "keys": "right"}}}
	_, err := controlActionRunner(a, ControlStepRef{Action: "a"}, nil)(r)
	if err == nil || !strings.Contains(err.Error(), "robot") {
		t.Fatalf("err = %v, want the robot isn't connected", err)
	}
}

func TestLibrarySaveRenameDelete(t *testing.T) {
	l := newControlLibrary()
	a := ControlActionDef{ID: "say", Name: "Say", Do: []ControlInstruction{{"op": "show", "label": "x"}}}
	if err := l.saveAction(a, ""); err != nil {
		t.Fatal(err)
	}
	q := ControlSequenceDef{ID: "q", Name: "Q", Steps: []ControlStepRef{{Action: "say"}}}
	if err := l.saveSequence(q, ""); err != nil {
		t.Fatal(err)
	}
	if err := l.deleteAction("say"); err == nil {
		t.Error("deleted an action a sequence uses")
	}
	// Renaming the action updates the sequence using it.
	a.ID = "speak"
	if err := l.saveAction(a, "say"); err != nil {
		t.Fatal(err)
	}
	if got := l.sequences[indexOfControlSequence(l.sequences, "q")].Steps[0].Action; got != "speak" {
		t.Errorf("step action after rename = %q", got)
	}
	if err := l.saveAction(ControlActionDef{ID: "launch-fc", Name: "dup", Do: a.Do}, "speak"); err == nil {
		t.Error("renamed onto an existing action")
	}
	if err := l.deleteSequence("q"); err != nil {
		t.Fatal(err)
	}
	if err := l.deleteAction("speak"); err != nil {
		t.Fatal(err)
	}
}

// TestExportImport: an exported sequence carries its actions and imports
// into an empty library.
func TestExportImport(t *testing.T) {
	src := newControlLibrary()
	yaml, filename, err := src.export("sequences", []string{"fc-robot-handoff"})
	if err != nil {
		t.Fatal(err)
	}
	if filename != "lr-control-sequence-fc-robot-handoff.yaml" {
		t.Errorf("filename = %q", filename)
	}
	doc, err := decodeControlLib(yaml)
	if err != nil {
		t.Fatal(err)
	}
	dst := &controlLibrary{}
	msg, err := dst.merge(doc)
	if err != nil {
		t.Fatal(err)
	}
	// The sequence's 8 steps each use a distinct action, all exported with it.
	if len(dst.actions) != 8 || len(dst.sequences) != 1 || !strings.Contains(msg, "added") {
		t.Errorf("imported %d actions, %d sequences (%s)", len(dst.actions), len(dst.sequences), msg)
	}
	if _, _, err := src.export("sequences", []string{"nope"}); err == nil {
		t.Error("exported nothing without an error")
	}
}

// TestLibraryPersists: saved to disk, read back, and a broken file is set
// aside rather than overwritten.
func TestLibraryPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-v1.yaml")
	l := openControlLibrary(path)
	if err := l.saveAction(ControlActionDef{ID: "mine", Name: "Mine", Do: []ControlInstruction{{"op": "show", "label": "x"}}}, ""); err != nil {
		t.Fatal(err)
	}
	again := openControlLibrary(path)
	if indexOfControlAction(again.actions, "mine") < 0 || again.note != "" {
		t.Errorf("reopened: note %q, actions %+v", again.note, again.actions)
	}

	if err := os.WriteFile(path, []byte("actions: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := openControlLibrary(path)
	if !strings.Contains(broken.note, "moved it to") || len(broken.sequences) != 3 {
		t.Errorf("broken file: note %q, %d sequences", broken.note, len(broken.sequences))
	}
}

// TestUpgradeControlExamples: an unedited copy of a built-in example takes
// this build's version; an edited one is kept.
func TestUpgradeControlExamples(t *testing.T) {
	actions, sequences := exampleControlLibrary()
	old := actions[0]
	old.Description = "an older build's wording"
	rec := map[string]string{ctlExampleKey("action", old.ID): ctlActionPrint(old)}
	stale := append([]ControlActionDef{old}, actions[1:]...)

	got, _, _, updated := upgradeControlExamples(stale, sequences, rec)
	if got[0].Description != actions[0].Description || len(updated) != 1 {
		t.Errorf("unedited copy not upgraded: %q, updated %v", got[0].Description, updated)
	}

	edited := old
	edited.Description = "my own wording"
	got, _, _, updated = upgradeControlExamples(append([]ControlActionDef{edited}, actions[1:]...), sequences, rec)
	if got[0].Description != "my own wording" || len(updated) != 0 {
		t.Errorf("edited copy replaced: %q, updated %v", got[0].Description, updated)
	}
}

// TestUpgradeAddsNewExamples: a library saved before this build's
// capture-two-nodes example gains it (and its action); one that deleted it
// doesn't get it back.
func TestUpgradeAddsNewExamples(t *testing.T) {
	actions, sequences := exampleControlLibrary()
	rec := shippedControlExamples()
	delete(rec, ctlExampleKey("sequence", "capture-two-nodes"))
	delete(rec, ctlExampleKey("action", "node-screenshot"))
	older := []ControlActionDef{}
	for _, a := range actions {
		if a.ID != "node-screenshot" {
			older = append(older, a)
		}
	}
	gotA, gotQ, out, updated := upgradeControlExamples(older, sequences[:1], rec)
	if len(gotA) != len(actions) || len(gotQ) != 2 || len(updated) != 2 {
		t.Fatalf("got %d actions, %d sequences, updated %v", len(gotA), len(gotQ), updated)
	}
	if err := checkControlLibrary(gotA, gotQ); err != nil {
		t.Errorf("upgraded library isn't valid: %v", err)
	}
	if _, ok := out[ctlExampleKey("sequence", "capture-two-nodes")]; !ok {
		t.Error("the added example isn't recorded")
	}

	// Deleted after it was recorded: stays deleted.
	_, gotQ, _, updated = upgradeControlExamples(actions, sequences[:1], shippedControlExamples())
	if len(gotQ) != 1 || len(updated) != 0 {
		t.Errorf("deleted example came back: %d sequences, updated %v", len(gotQ), updated)
	}
}

func TestNodeControlType(t *testing.T) {
	if err := ctlValidateControls([]ControlParam{{Name: "n", Type: controlTypeNode}}); err != nil {
		t.Errorf("node control: %v", err)
	}
	if err := ctlValidateControls([]ControlParam{{Name: "n", Type: "colour"}}); err == nil {
		t.Error("an unknown control type was accepted")
	}
	src := encodeControlLib(controlLibDoc{Sequences: []ControlSequenceDef{{ID: "q", Name: "Q",
		Controls: []ControlParam{{Name: "n", Type: controlTypeNode}}, Steps: []ControlStepRef{{Action: "a"}}}}})
	if !strings.Contains(src, "type: node") {
		t.Errorf("type not written:\n%s", src)
	}
	doc, err := decodeControlLib(src)
	if err != nil || doc.Sequences[0].Controls[0].Type != controlTypeNode {
		t.Errorf("type not read back: %+v, %v", doc, err)
	}
}

func TestHandleLibRequest(t *testing.T) {
	s := newServer("test-lr")
	e := s.control
	r := e.handleLibRequest(ControlLibRequest{Req: "1", Op: "save-action", Action: &ControlActionDef{ID: "x", Name: "X", Do: []ControlInstruction{{"op": "nope"}}}})
	if r.Success || r.Req != "1" || !strings.Contains(r.Error, "unknown op") {
		t.Errorf("bad action: %+v", r)
	}
	r = e.handleLibRequest(ControlLibRequest{Op: "export", Kind: "actions", IDs: []string{"launch-fc"}})
	if !r.Success || !strings.Contains(r.YAML, "op: launch-fc") {
		t.Errorf("export: %+v", r)
	}
	r = e.handleLibRequest(ControlLibRequest{Op: "import", YAML: strings.Replace(r.YAML, "id: launch-fc", "id: launch-fc-2", 1)})
	if !r.Success || !strings.Contains(r.Message, "added action launch-fc-2") {
		t.Errorf("import: %+v", r)
	}
	if r = e.handleLibRequest(ControlLibRequest{Op: "restore-examples"}); !r.Success {
		t.Errorf("restore: %+v", r)
	}
	if r = e.handleLibRequest(ControlLibRequest{Op: "frobnicate"}); r.Success {
		t.Errorf("unknown op succeeded: %+v", r)
	}
}

// TestRobotOpsDescribeSteps: once IANAR reports its ops, robot steps are
// described in its words.
func TestRobotOpsDescribeSteps(t *testing.T) {
	s := newServer("test-lr")
	s.control.setRobotOps([]ControlOpSpec{{Op: "key", Summary: "press keys", Describe: "press {keys}", Args: []ControlOpArg{{Name: "keys", Required: true}}}})
	lib := s.control.library()
	if !lib.RobotOps {
		t.Error("library doesn't say IANAR's ops arrived")
	}
	found := false
	for _, st := range s.control.state().Sequences[0].Steps {
		for _, d := range st.Do {
			found = found || d == "robot: press left"
		}
	}
	if !found {
		t.Errorf("no step reads %q: %+v", "robot: press left", s.control.state().Sequences[0].Steps)
	}
}
