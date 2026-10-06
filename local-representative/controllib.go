package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// This file is the control tab's library (condocs/initialRobotImpls/
// Step3Prompt.md, Revision C): control sequences are data, defined,
// composed and run the way IANAR's sequence-v2 tab does it for the robot
// (ianar/seqv2.go). The control tab's v1 sub-tab has three views over it:
//
//   - definer: actions -- named, reusable building blocks that expose
//     controls (parameters, with defaults) and carry out a list of
//     instructions. Each instruction is one op (see controlops.go): one LR
//     carries out itself (launch a federation-command, send it a command,
//     wait for its output or control state, ...), or "robot.<op>", one of
//     IANAR's sequence-v2 ops, which LR hands to the robot.
//   - composer: sequences -- ordered steps, each an action with values for
//     its controls. A sequence can expose controls of its own, choose whose
//     screens are recorded across a run (record), and mark leading steps to
//     run before that recording starts (before_recording).
//   - runner: runs a sequence with chosen control values (control.go).
//
// Values may refer to controls and other variables as {{name}}: the
// built-ins (timestamp), the sequence's controls, values earlier
// instructions saved (save_as), and, inside an action, its own controls.
//
// The library is persisted as YAML (by default
// ~/.config/local-representative/control-v1.yaml) and sent to browsers and
// agent-coordinator on connect and after each change ("control-library").
// Actions and sequences export to and import from YAML (yamlite.go); an
// exported sequence carries the actions it uses. The sample sequence the
// control tab started with, fc-robot-handoff, is the built-in example
// (exampleControlLibrary).

const controlLibFormat = "lr-control-v1"

// ControlParam is a control an action or sequence exposes.
type ControlParam struct {
	Name    string `json:"name"`
	Label   string `json:"label,omitempty"`
	Default string `json:"default,omitempty"`
	Help    string `json:"help,omitempty"`
}

// ControlInstruction is one op: "op" names it (see controlops.go), the
// other keys are its arguments.
type ControlInstruction map[string]string

// ControlActionDef is a definer action.
type ControlActionDef struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Controls    []ControlParam       `json:"controls"`
	Do          []ControlInstruction `json:"do"`
}

// ControlStepRef is one composer step: an action, an optional label shown
// in place of the action's name, values for the action's controls, and
// whether it runs before the sequence's recording starts (leading steps
// only).
type ControlStepRef struct {
	Action          string            `json:"action"`
	Label           string            `json:"label,omitempty"`
	With            map[string]string `json:"with,omitempty"`
	BeforeRecording bool              `json:"before_recording,omitempty"`
}

// ControlSequenceDef is a composer sequence.
type ControlSequenceDef struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Controls    []ControlParam   `json:"controls"`
	Record      []string         `json:"record,omitempty"` // whose screens a run records (see recordLocalRobot)
	Steps       []ControlStepRef `json:"steps"`
}

// ---- Validation ----

var (
	ctlIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	ctlNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	ctlTmplRe = regexp.MustCompile(`\{\{(.*?)\}\}`)
)

// ctlBuiltins are the variables every value can refer to.
var ctlBuiltins = map[string]struct {
	help  string
	value func(start time.Time) string
}{
	"timestamp": {"when the run started, as 2006-01-02T15-04-05 (safe in file names)", func(t time.Time) string { return t.Format("2006-01-02T15-04-05") }},
}

// ControlBuiltin describes a built-in for the frontend.
type ControlBuiltin struct {
	Name  string `json:"name"`
	Help  string `json:"help"`
	Value string `json:"value"`
}

func ctlBuiltinList(now time.Time) []ControlBuiltin {
	var out []ControlBuiltin
	for name, b := range ctlBuiltins {
		out = append(out, ControlBuiltin{Name: name, Help: b.help, Value: b.value(now)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func ctlParseRef(inner string) (string, error) {
	name := strings.TrimSpace(inner)
	if !ctlNameRe.MatchString(name) {
		return "", fmt.Errorf("{{%s}}: %q isn't a valid name", inner, name)
	}
	return name, nil
}

// ctlCheckTemplate reports any malformed {{...}} reference in s.
func ctlCheckTemplate(s string) error {
	for _, m := range ctlTmplRe.FindAllStringSubmatch(s, -1) {
		if _, err := ctlParseRef(m[1]); err != nil {
			return err
		}
	}
	return nil
}

// ctlExpand replaces each {{name}} in s with its value. A reference lookup
// can't fill stays as written, and the first such is returned as an error.
func ctlExpand(s string, lookup func(string) (string, bool)) (string, error) {
	var firstErr error
	out := ctlTmplRe.ReplaceAllStringFunc(s, func(m string) string {
		name, err := ctlParseRef(m[2 : len(m)-2])
		if err == nil {
			if v, ok := lookup(name); ok {
				return v
			}
			err = fmt.Errorf("{{%s}} isn't a control, built-in or saved value here", name)
		}
		if firstErr == nil {
			firstErr = err
		}
		return m
	})
	return out, firstErr
}

func ctlValidateControls(cs []ControlParam) error {
	seen := map[string]bool{}
	for _, c := range cs {
		if !ctlNameRe.MatchString(c.Name) {
			return fmt.Errorf("control %q: names are letters, digits and _ (not starting with a digit)", c.Name)
		}
		if seen[c.Name] {
			return fmt.Errorf("control %q is defined twice", c.Name)
		}
		if _, ok := ctlBuiltins[c.Name]; ok {
			return fmt.Errorf("control %q would hide the built-in {{%s}}; pick another name", c.Name, c.Name)
		}
		seen[c.Name] = true
		if err := ctlCheckTemplate(c.Default); err != nil {
			return fmt.Errorf("control %q's default: %v", c.Name, err)
		}
	}
	return nil
}

func ctlValidateIdentity(kind, id, name string) error {
	if !ctlIDRe.MatchString(id) {
		return fmt.Errorf("%s id %q: ids are lowercase letters, digits, '.', '_' and '-'", kind, id)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s %q needs a name", kind, id)
	}
	return nil
}

// validate checks an action on its own. robotOps are IANAR's ops, to check
// robot.* instructions' arguments against; nil (IANAR hasn't said) checks
// only that they name an op.
func (a ControlActionDef) validate(robotOps []ControlOpSpec) error {
	if err := ctlValidateIdentity("action", a.ID, a.Name); err != nil {
		return err
	}
	if err := ctlValidateControls(a.Controls); err != nil {
		return fmt.Errorf("action %q: %v", a.ID, err)
	}
	if len(a.Do) == 0 {
		return fmt.Errorf("action %q has no instructions", a.ID)
	}
	for i, in := range a.Do {
		if err := validateControlInstruction(in, robotOps); err != nil {
			return fmt.Errorf("action %q, instruction %d: %v", a.ID, i+1, err)
		}
	}
	return nil
}

func validateControlInstruction(in ControlInstruction, robotOps []ControlOpSpec) error {
	op := in["op"]
	if op == "" {
		return errors.New(`no "op"`)
	}
	for k, v := range in {
		if err := ctlCheckTemplate(v); err != nil {
			return fmt.Errorf("%s's %s: %v", op, k, err)
		}
	}
	spec, ok := findControlOp(op, robotOps)
	if !ok {
		if robotOp, isRobot := strings.CutPrefix(op, robotOpPrefix); isRobot && robotOps == nil {
			if !ctlIDRe.MatchString(robotOp) {
				return fmt.Errorf("%q isn't a robot op name", op)
			}
			return nil // checked by IANAR when it runs
		}
		return fmt.Errorf("unknown op %q", op)
	}
	for k := range in {
		if k != "op" && spec.arg(k) == nil {
			return fmt.Errorf("%s takes no argument %q (it takes: %s)", spec.Op, k, spec.argNames())
		}
	}
	for _, a := range spec.Args {
		if a.Required && strings.TrimSpace(in[a.Name]) == "" {
			return fmt.Errorf("%s needs %q", spec.Op, a.Name)
		}
	}
	return nil
}

// validate checks a sequence against the actions it may use.
func (q ControlSequenceDef) validate(actions map[string]ControlActionDef) error {
	if err := ctlValidateIdentity("sequence", q.ID, q.Name); err != nil {
		return err
	}
	if err := ctlValidateControls(q.Controls); err != nil {
		return fmt.Errorf("sequence %q: %v", q.ID, err)
	}
	for _, who := range q.Record {
		if who != recordLocalRobot {
			return fmt.Errorf("sequence %q: can't record %q -- only %q (this node's screen, through its robot) so far", q.ID, who, recordLocalRobot)
		}
	}
	if len(q.Steps) == 0 {
		return fmt.Errorf("sequence %q has no steps", q.ID)
	}
	recorded := false
	for i, st := range q.Steps {
		a, ok := actions[st.Action]
		if !ok {
			return fmt.Errorf("sequence %q, step %d: no action %q", q.ID, i+1, st.Action)
		}
		if st.BeforeRecording && recorded {
			return fmt.Errorf("sequence %q, step %d: only leading steps can run before the recording starts", q.ID, i+1)
		}
		recorded = recorded || !st.BeforeRecording
		if err := ctlCheckTemplate(st.Label); err != nil {
			return fmt.Errorf("sequence %q, step %d, label: %v", q.ID, i+1, err)
		}
		for k, v := range st.With {
			if !hasControlParam(a.Controls, k) {
				return fmt.Errorf("sequence %q, step %d: action %q has no control %q", q.ID, i+1, a.ID, k)
			}
			if err := ctlCheckTemplate(v); err != nil {
				return fmt.Errorf("sequence %q, step %d, %s: %v", q.ID, i+1, k, err)
			}
		}
	}
	return nil
}

func hasControlParam(cs []ControlParam, name string) bool {
	for _, c := range cs {
		if c.Name == name {
			return true
		}
	}
	return false
}

// checkControlLibrary validates a whole library. Robot instructions are
// only checked for naming an op: IANAR's ops can change under a saved
// library, and that shouldn't block editing the rest of it.
func checkControlLibrary(actions []ControlActionDef, sequences []ControlSequenceDef) error {
	byID := map[string]ControlActionDef{}
	for _, a := range actions {
		if err := a.validate(nil); err != nil {
			return err
		}
		if _, dup := byID[a.ID]; dup {
			return fmt.Errorf("action %q is defined twice", a.ID)
		}
		byID[a.ID] = a
	}
	seen := map[string]bool{}
	for _, q := range sequences {
		if err := q.validate(byID); err != nil {
			return err
		}
		if seen[q.ID] {
			return fmt.Errorf("sequence %q is defined twice", q.ID)
		}
		seen[q.ID] = true
	}
	return nil
}

// ---- YAML ----

// controlLibDoc is the YAML document actions and sequences are exchanged
// in.
type controlLibDoc struct {
	Actions   []ControlActionDef
	Sequences []ControlSequenceDef
	// Examples is only in the saved library: for each built-in example it
	// holds, the fingerprint of the shipped version it was copied from, so a
	// later build can update copies that weren't edited (as IANAR does --
	// see ianar/seqv2.go's upgradeExamples).
	Examples map[string]string
}

func encodeControlLib(doc controlLibDoc) string {
	w := &yamlWriter{}
	w.line(0, "# local-representative control library: definer actions and composer sequences.")
	w.line(0, "format: "+controlLibFormat)
	writeControls := func(ind int, cs []ControlParam) {
		if len(cs) == 0 {
			return
		}
		w.line(ind, "controls:")
		for _, c := range cs {
			w.line(ind+2, "- name: "+yamlScalar(c.Name))
			w.field(ind+4, "label", c.Label)
			w.field(ind+4, "default", c.Default)
			w.field(ind+4, "help", c.Help)
		}
	}
	if len(doc.Actions) > 0 {
		w.line(0, "actions:")
		for _, a := range doc.Actions {
			w.line(2, "- id: "+yamlScalar(a.ID))
			w.field(4, "name", a.Name)
			w.field(4, "description", a.Description)
			writeControls(4, a.Controls)
			w.line(4, "do:")
			for _, in := range a.Do {
				keys := []string{"op"}
				for k := range in {
					if k != "op" {
						keys = append(keys, k)
					}
				}
				sort.Strings(keys[1:])
				vals := make([]string, len(keys))
				for i, k := range keys {
					vals[i] = in[k]
				}
				w.line(6, "- "+flowMap(keys, vals))
			}
		}
	}
	if len(doc.Sequences) > 0 {
		w.line(0, "sequences:")
		for _, q := range doc.Sequences {
			w.line(2, "- id: "+yamlScalar(q.ID))
			w.field(4, "name", q.Name)
			w.field(4, "description", q.Description)
			if len(q.Record) > 0 {
				items := make([]string, len(q.Record))
				for i, r := range q.Record {
					items[i] = yamlScalar(r)
				}
				w.line(4, "record: ["+strings.Join(items, ", ")+"]")
			}
			writeControls(4, q.Controls)
			w.line(4, "steps:")
			for _, st := range q.Steps {
				w.line(6, "- action: "+yamlScalar(st.Action))
				w.field(8, "label", st.Label)
				if st.BeforeRecording {
					w.line(8, "before_recording: true")
				}
				if len(st.With) > 0 {
					w.line(8, "with:")
					keys := make([]string, 0, len(st.With))
					for k := range st.With {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						w.line(10, yamlScalar(k)+": "+yamlScalar(st.With[k]))
					}
				}
			}
		}
	}
	if len(doc.Examples) > 0 {
		w.line(0, "examples:")
		keys := make([]string, 0, len(doc.Examples))
		for k := range doc.Examples {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			w.line(2, yamlScalar(k)+": "+yamlScalar(doc.Examples[k]))
		}
	}
	return w.b.String()
}

// decodeControlLib reads a document written by encodeControlLib, or by
// hand in the same shape. Unknown keys are errors, so a typo isn't silently
// ignored.
func decodeControlLib(src string) (controlLibDoc, error) {
	var doc controlLibDoc
	root, err := parseYAML(src)
	if err != nil {
		return doc, err
	}
	if root.kind != yMap {
		return doc, fmt.Errorf("line %d: expected the document to be a mapping with actions: and/or sequences:", root.line)
	}
	if err := ctlOnlyKeys(root, "format", "actions", "sequences", "examples"); err != nil {
		return doc, err
	}
	if f := root.get("format"); f != nil && f.str != controlLibFormat {
		hint := ""
		if f.str == "ianar-sequence-v2" {
			hint = " -- that's a robot sequence-v2 library; import it on the robot tab"
		}
		return doc, fmt.Errorf("line %d: format is %q, expected %q%s", f.line, f.str, controlLibFormat, hint)
	}
	if n := root.get("actions"); n != nil {
		items, err := ctlItems(n, "actions")
		if err != nil {
			return doc, err
		}
		for _, it := range items {
			a, err := decodeControlAction(it)
			if err != nil {
				return doc, err
			}
			doc.Actions = append(doc.Actions, a)
		}
	}
	if n := root.get("sequences"); n != nil {
		items, err := ctlItems(n, "sequences")
		if err != nil {
			return doc, err
		}
		for _, it := range items {
			q, err := decodeControlSequence(it)
			if err != nil {
				return doc, err
			}
			doc.Sequences = append(doc.Sequences, q)
		}
	}
	if n := root.get("examples"); n != nil {
		if doc.Examples, err = ctlStringMap(n, "examples"); err != nil {
			return doc, err
		}
	}
	if len(doc.Actions) == 0 && len(doc.Sequences) == 0 {
		return doc, errors.New("the document has no actions or sequences")
	}
	return doc, nil
}

func ctlOnlyKeys(n *yNode, allowed ...string) error {
	for i, k := range n.keys {
		ok := false
		for _, a := range allowed {
			ok = ok || k == a
		}
		if !ok {
			return fmt.Errorf("line %d: unknown key %q (expected one of: %s)", n.vals[i].line, k, strings.Join(allowed, ", "))
		}
	}
	return nil
}

// ctlItems returns n's items, where n must be a list (an empty value
// counts as an empty list).
func ctlItems(n *yNode, what string) ([]*yNode, error) {
	if n.kind == yScalar && n.str == "" {
		return nil, nil
	}
	if n.kind != ySeq {
		return nil, fmt.Errorf("line %d: %s should be a list, not %s", n.line, what, n.kind)
	}
	return n.items, nil
}

// ctlScalar reads key from mapping n as a string ("" if absent).
func ctlScalar(n *yNode, key string) (string, error) {
	v := n.get(key)
	if v == nil {
		return "", nil
	}
	if v.kind != yScalar {
		return "", fmt.Errorf("line %d: %s should be a single value, not %s", v.line, key, v.kind)
	}
	return v.str, nil
}

// ctlStringMap reads a mapping of single values.
func ctlStringMap(n *yNode, what string) (map[string]string, error) {
	if n.kind == yScalar && n.str == "" {
		return nil, nil
	}
	if n.kind != yMap {
		return nil, fmt.Errorf("line %d: %s should be a mapping, not %s", n.line, what, n.kind)
	}
	m := make(map[string]string, len(n.keys))
	for i, k := range n.keys {
		v := n.vals[i]
		if v.kind != yScalar {
			return nil, fmt.Errorf("line %d: %s.%s should be a single value, not %s", v.line, what, k, v.kind)
		}
		m[k] = v.str
	}
	return m, nil
}

func ctlBool(n *yNode, key string) (bool, error) {
	s, err := ctlScalar(n, key)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(s) {
	case "", "false", "no":
		return false, nil
	case "true", "yes":
		return true, nil
	}
	return false, fmt.Errorf("line %d: %s should be true or false, not %q", n.get(key).line, key, s)
}

func decodeControlParams(n *yNode) ([]ControlParam, error) {
	if n == nil {
		return nil, nil
	}
	items, err := ctlItems(n, "controls")
	if err != nil {
		return nil, err
	}
	var out []ControlParam
	for _, it := range items {
		m, err := ctlStringMap(it, "control")
		if err != nil {
			return nil, err
		}
		if err := ctlOnlyKeys(it, "name", "label", "default", "help"); err != nil {
			return nil, err
		}
		out = append(out, ControlParam{Name: m["name"], Label: m["label"], Default: m["default"], Help: m["help"]})
	}
	return out, nil
}

func decodeControlAction(n *yNode) (ControlActionDef, error) {
	var a ControlActionDef
	if n.kind != yMap {
		return a, fmt.Errorf("line %d: each action should be a mapping, not %s", n.line, n.kind)
	}
	if err := ctlOnlyKeys(n, "id", "name", "description", "controls", "do"); err != nil {
		return a, err
	}
	var err error
	for _, f := range []struct {
		key string
		dst *string
	}{{"id", &a.ID}, {"name", &a.Name}, {"description", &a.Description}} {
		if *f.dst, err = ctlScalar(n, f.key); err != nil {
			return a, err
		}
	}
	if a.Controls, err = decodeControlParams(n.get("controls")); err != nil {
		return a, err
	}
	if d := n.get("do"); d != nil {
		items, err := ctlItems(d, "do")
		if err != nil {
			return a, err
		}
		for _, it := range items {
			m, err := ctlStringMap(it, "instruction")
			if err != nil {
				return a, err
			}
			a.Do = append(a.Do, ControlInstruction(m))
		}
	}
	return a, nil
}

func decodeControlSequence(n *yNode) (ControlSequenceDef, error) {
	var q ControlSequenceDef
	if n.kind != yMap {
		return q, fmt.Errorf("line %d: each sequence should be a mapping, not %s", n.line, n.kind)
	}
	if err := ctlOnlyKeys(n, "id", "name", "description", "record", "controls", "steps"); err != nil {
		return q, err
	}
	var err error
	for _, f := range []struct {
		key string
		dst *string
	}{{"id", &q.ID}, {"name", &q.Name}, {"description", &q.Description}} {
		if *f.dst, err = ctlScalar(n, f.key); err != nil {
			return q, err
		}
	}
	if r := n.get("record"); r != nil {
		items, err := ctlItems(r, "record")
		if err != nil {
			return q, err
		}
		for _, it := range items {
			if it.kind != yScalar {
				return q, fmt.Errorf("line %d: record lists names, not %s", it.line, it.kind)
			}
			q.Record = append(q.Record, it.str)
		}
	}
	if q.Controls, err = decodeControlParams(n.get("controls")); err != nil {
		return q, err
	}
	if s := n.get("steps"); s != nil {
		items, err := ctlItems(s, "steps")
		if err != nil {
			return q, err
		}
		for _, it := range items {
			if it.kind != yMap {
				return q, fmt.Errorf("line %d: each step should be a mapping with action:, not %s", it.line, it.kind)
			}
			if err := ctlOnlyKeys(it, "action", "label", "before_recording", "with"); err != nil {
				return q, err
			}
			var st ControlStepRef
			if st.Action, err = ctlScalar(it, "action"); err != nil {
				return q, err
			}
			if st.Label, err = ctlScalar(it, "label"); err != nil {
				return q, err
			}
			if st.BeforeRecording, err = ctlBool(it, "before_recording"); err != nil {
				return q, err
			}
			if w := it.get("with"); w != nil {
				if st.With, err = ctlStringMap(w, "with"); err != nil {
					return q, err
				}
			}
			q.Steps = append(q.Steps, st)
		}
	}
	return q, nil
}

// ---- The library ----

// controlLibrary holds the definer's actions and the composer's sequences,
// persisted to path ("" keeps them in memory only).
type controlLibrary struct {
	mu        sync.Mutex
	path      string
	actions   []ControlActionDef
	sequences []ControlSequenceDef
	examples  map[string]string // see controlLibDoc.Examples
	note      string            // a problem loading the file, shown in the UI
}

func ctlExampleKey(kind, id string) string { return kind + "/" + id }

func ctlFingerprint(doc controlLibDoc) string {
	sum := sha256.Sum256([]byte(encodeControlLib(doc)))
	return hex.EncodeToString(sum[:8])
}

func ctlActionPrint(a ControlActionDef) string {
	return ctlFingerprint(controlLibDoc{Actions: []ControlActionDef{a}})
}

func ctlSequencePrint(q ControlSequenceDef) string {
	return ctlFingerprint(controlLibDoc{Sequences: []ControlSequenceDef{q}})
}

// shippedControlExamples records every built-in example as this build
// ships it.
func shippedControlExamples() map[string]string {
	actions, sequences := exampleControlLibrary()
	rec := map[string]string{}
	for _, a := range actions {
		rec[ctlExampleKey("action", a.ID)] = ctlActionPrint(a)
	}
	for _, q := range sequences {
		rec[ctlExampleKey("sequence", q.ID)] = ctlSequencePrint(q)
	}
	return rec
}

// upgradeControlExamples replaces each copy of a built-in example that
// still matches the version recorded for it in rec (wasn't edited) with
// this build's version, returning the new lists and record and what it
// updated.
func upgradeControlExamples(actions []ControlActionDef, sequences []ControlSequenceDef, rec map[string]string) ([]ControlActionDef, []ControlSequenceDef, map[string]string, []string) {
	actions = append([]ControlActionDef(nil), actions...)
	sequences = append([]ControlSequenceDef(nil), sequences...)
	out := map[string]string{}
	for k, v := range rec {
		out[k] = v
	}
	var updated []string
	shippedA, shippedQ := exampleControlLibrary()
	for _, a := range shippedA {
		key := ctlExampleKey("action", a.ID)
		if i := indexOfControlAction(actions, a.ID); i >= 0 {
			have, want := ctlActionPrint(actions[i]), ctlActionPrint(a)
			if have != want && rec[key] == have {
				actions[i] = a
				updated = append(updated, "action "+a.ID)
			}
			if have == want || rec[key] == have {
				out[key] = want
			}
		}
	}
	for _, q := range shippedQ {
		key := ctlExampleKey("sequence", q.ID)
		if i := indexOfControlSequence(sequences, q.ID); i >= 0 {
			have, want := ctlSequencePrint(sequences[i]), ctlSequencePrint(q)
			if have != want && rec[key] == have {
				sequences[i] = q
				updated = append(updated, "sequence "+q.ID)
			}
			if have == want || rec[key] == have {
				out[key] = want
			}
		}
	}
	return actions, sequences, out, updated
}

// newControlLibrary is a library of the built-in examples, kept in memory.
func newControlLibrary() *controlLibrary {
	l := &controlLibrary{examples: shippedControlExamples()}
	l.actions, l.sequences = exampleControlLibrary()
	return l
}

// openControlLibrary loads the library at path, starting from the examples
// if there is none yet. A file that can't be read is set aside, not
// overwritten.
func openControlLibrary(path string) *controlLibrary {
	l := newControlLibrary()
	l.path = path
	if path == "" {
		return l
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		log.Printf("control: no library at %s yet; starting from the examples", path)
		return l
	case err != nil:
		l.note = fmt.Sprintf("couldn't read %s (%v); using the examples, and changes won't be saved", path, err)
		l.path = ""
		return l
	}
	doc, err := decodeControlLib(string(data))
	if err == nil {
		err = checkControlLibrary(doc.Actions, doc.Sequences)
	}
	if err != nil {
		aside := path + ".broken-" + time.Now().Format("2006-01-02T15-04-05")
		if rerr := os.Rename(path, aside); rerr != nil {
			l.note = fmt.Sprintf("couldn't load %s (%v); using the examples, and changes won't be saved", path, err)
			l.path = ""
		} else {
			l.note = fmt.Sprintf("couldn't load %s (%v); moved it to %s and started from the examples", path, err, aside)
		}
		log.Printf("control: %s", l.note)
		return l
	}
	l.actions, l.sequences, l.examples = doc.Actions, doc.Sequences, doc.Examples
	actions, sequences, rec, updated := upgradeControlExamples(doc.Actions, doc.Sequences, doc.Examples)
	if len(updated) > 0 && checkControlLibrary(actions, sequences) != nil {
		// An update that doesn't fit the rest (an edited sequence using an
		// action's old controls) is left for Restore examples.
		actions, sequences, rec, updated = doc.Actions, doc.Sequences, doc.Examples, nil
	}
	if len(updated) > 0 || !sameStringMap(rec, doc.Examples) {
		l.examples = rec
		if err := l.commit(actions, sequences); err != nil {
			log.Printf("control: %v", err)
		}
	}
	if len(updated) > 0 {
		l.note = "updated to this local-representative's version of the built-in examples (you hadn't edited them): " + strings.Join(updated, ", ")
		log.Printf("control: %s", l.note)
	}
	return l
}

func sameStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// commit validates a proposed library and, if it holds, adopts and saves
// it. Called with l.mu held.
func (l *controlLibrary) commit(actions []ControlActionDef, sequences []ControlSequenceDef) error {
	if err := checkControlLibrary(actions, sequences); err != nil {
		return err
	}
	if l.path != "" {
		data := encodeControlLib(controlLibDoc{Actions: actions, Sequences: sequences, Examples: l.examples})
		if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
			return fmt.Errorf("saving the library: %w", err)
		}
		tmp := l.path + ".tmp"
		if err := os.WriteFile(tmp, []byte(data), 0o644); err != nil {
			return fmt.Errorf("saving the library: %w", err)
		}
		if err := os.Rename(tmp, l.path); err != nil {
			return fmt.Errorf("saving the library: %w", err)
		}
	}
	l.actions, l.sequences = actions, sequences
	return nil
}

func (l *controlLibrary) actionMap() map[string]ControlActionDef {
	m := make(map[string]ControlActionDef, len(l.actions))
	for _, a := range l.actions {
		m[a.ID] = a
	}
	return m
}

// saveAction adds a, or replaces the action previously called prevID (or,
// with no prevID, one with a's id). Renaming an action updates the
// sequences that use it.
func (l *controlLibrary) saveAction(a ControlActionDef, prevID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if prevID == "" {
		prevID = a.ID
	}
	actions := make([]ControlActionDef, 0, len(l.actions)+1)
	replaced := false
	for _, x := range l.actions {
		switch {
		case x.ID == prevID:
			actions = append(actions, a)
			replaced = true
		case x.ID == a.ID:
			return fmt.Errorf("there's already an action %q", a.ID)
		default:
			actions = append(actions, x)
		}
	}
	if !replaced {
		actions = append(actions, a)
	}
	sequences := l.sequences
	if prevID != a.ID {
		sequences = make([]ControlSequenceDef, len(l.sequences))
		for i, q := range l.sequences {
			q.Steps = append([]ControlStepRef(nil), q.Steps...)
			for j := range q.Steps {
				if q.Steps[j].Action == prevID {
					q.Steps[j].Action = a.ID
				}
			}
			sequences[i] = q
		}
	}
	return l.commit(actions, sequences)
}

// deleteAction removes action id, unless a sequence uses it.
func (l *controlLibrary) deleteAction(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var users []string
	for _, q := range l.sequences {
		for _, st := range q.Steps {
			if st.Action == id {
				users = append(users, q.Name)
				break
			}
		}
	}
	if len(users) > 0 {
		return fmt.Errorf("action %q is used by: %s -- remove it from those sequences first", id, strings.Join(users, ", "))
	}
	var actions []ControlActionDef
	for _, a := range l.actions {
		if a.ID != id {
			actions = append(actions, a)
		}
	}
	if len(actions) == len(l.actions) {
		return fmt.Errorf("no action %q", id)
	}
	return l.commit(actions, l.sequences)
}

// saveSequence adds q, or replaces the sequence previously called prevID
// (or, with no prevID, one with q's id).
func (l *controlLibrary) saveSequence(q ControlSequenceDef, prevID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if prevID == "" {
		prevID = q.ID
	}
	sequences := make([]ControlSequenceDef, 0, len(l.sequences)+1)
	replaced := false
	for _, x := range l.sequences {
		switch {
		case x.ID == prevID:
			sequences = append(sequences, q)
			replaced = true
		case x.ID == q.ID:
			return fmt.Errorf("there's already a sequence %q", q.ID)
		default:
			sequences = append(sequences, x)
		}
	}
	if !replaced {
		sequences = append(sequences, q)
	}
	return l.commit(l.actions, sequences)
}

func (l *controlLibrary) deleteSequence(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var sequences []ControlSequenceDef
	for _, q := range l.sequences {
		if q.ID != id {
			sequences = append(sequences, q)
		}
	}
	if len(sequences) == len(l.sequences) {
		return fmt.Errorf("no sequence %q", id)
	}
	return l.commit(l.actions, sequences)
}

// merge adds doc's actions and sequences, replacing any with the same id,
// and describes what changed.
func (l *controlLibrary) merge(doc controlLibDoc) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	actions := append([]ControlActionDef(nil), l.actions...)
	sequences := append([]ControlSequenceDef(nil), l.sequences...)
	var added, replaced []string
	for _, a := range doc.Actions {
		if i := indexOfControlAction(actions, a.ID); i >= 0 {
			actions[i] = a
			replaced = append(replaced, "action "+a.ID)
		} else {
			actions = append(actions, a)
			added = append(added, "action "+a.ID)
		}
	}
	for _, q := range doc.Sequences {
		if i := indexOfControlSequence(sequences, q.ID); i >= 0 {
			sequences[i] = q
			replaced = append(replaced, "sequence "+q.ID)
		} else {
			sequences = append(sequences, q)
			added = append(added, "sequence "+q.ID)
		}
	}
	if err := l.commit(actions, sequences); err != nil {
		return "", err
	}
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "added "+strings.Join(added, ", "))
	}
	if len(replaced) > 0 {
		parts = append(parts, "replaced "+strings.Join(replaced, ", "))
	}
	return strings.Join(parts, "; "), nil
}

func indexOfControlAction(as []ControlActionDef, id string) int {
	for i, a := range as {
		if a.ID == id {
			return i
		}
	}
	return -1
}

func indexOfControlSequence(qs []ControlSequenceDef, id string) int {
	for i, q := range qs {
		if q.ID == id {
			return i
		}
	}
	return -1
}

// export renders actions (kind "actions") or sequences, with the actions
// they use (kind "sequences"), as YAML: those named by ids, or all of them.
func (l *controlLibrary) export(kind string, ids []string) (yaml, filename string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	want := func(id string) bool {
		if len(ids) == 0 {
			return true
		}
		for _, x := range ids {
			if x == id {
				return true
			}
		}
		return false
	}
	var doc controlLibDoc
	switch kind {
	case "actions":
		for _, a := range l.actions {
			if want(a.ID) {
				doc.Actions = append(doc.Actions, a)
			}
		}
		filename = "lr-control-actions.yaml"
		if len(doc.Actions) == 1 {
			filename = "lr-control-action-" + doc.Actions[0].ID + ".yaml"
		}
	case "sequences":
		used := map[string]bool{}
		for _, q := range l.sequences {
			if want(q.ID) {
				doc.Sequences = append(doc.Sequences, q)
				for _, st := range q.Steps {
					used[st.Action] = true
				}
			}
		}
		for _, a := range l.actions {
			if used[a.ID] {
				doc.Actions = append(doc.Actions, a)
			}
		}
		filename = "lr-control-sequences.yaml"
		if len(doc.Sequences) == 1 {
			filename = "lr-control-sequence-" + doc.Sequences[0].ID + ".yaml"
		}
	default:
		return "", "", fmt.Errorf("can't export %q", kind)
	}
	if len(doc.Actions) == 0 && len(doc.Sequences) == 0 {
		return "", "", errors.New("nothing to export")
	}
	return encodeControlLib(doc), filename, nil
}

// restoreExamples puts the built-in example actions and sequences back,
// replacing any edited copies.
func (l *controlLibrary) restoreExamples() (string, error) {
	actions, sequences := exampleControlLibrary()
	l.mu.Lock()
	rec := map[string]string{}
	for k, v := range l.examples {
		rec[k] = v
	}
	for k, v := range shippedControlExamples() {
		rec[k] = v
	}
	l.examples = rec // written by merge's commit
	l.mu.Unlock()
	return l.merge(controlLibDoc{Actions: actions, Sequences: sequences})
}

// ControlLibraryMsg is the "control-library" payload: the whole library,
// plus what the definer needs to edit it.
type ControlLibraryMsg struct {
	Actions   []ControlActionDef   `json:"actions"`
	Sequences []ControlSequenceDef `json:"sequences"`
	Ops       []ControlOpSpec      `json:"ops"`
	// RobotOps is false until IANAR has said which ops it has: robot.*
	// instructions can still be written, with any arguments, and are
	// checked when the robot runs them.
	RobotOps bool             `json:"robot_ops"`
	Builtins []ControlBuiltin `json:"builtins"`
	Path     string           `json:"path,omitempty"` // where it's saved; "" if only in memory
	Note     string           `json:"note,omitempty"`
}

func (l *controlLibrary) snapshot(robotOps []ControlOpSpec) ControlLibraryMsg {
	l.mu.Lock()
	defer l.mu.Unlock()
	return ControlLibraryMsg{
		Actions:   append([]ControlActionDef{}, l.actions...),
		Sequences: append([]ControlSequenceDef{}, l.sequences...),
		Ops:       append(append([]ControlOpSpec{}, controlOps...), robotOps...),
		RobotOps:  robotOps != nil,
		Builtins:  ctlBuiltinList(time.Now()),
		Path:      l.path,
		Note:      l.note,
	}
}

// ---- Compiling a sequence to run ----

// compile turns sequence id into a runnable controlSequence, with values
// for its controls (missing ones take their defaults). It also returns the
// run's starting variables: the built-ins and the sequence's controls.
func (l *controlLibrary) compile(id string, values map[string]string, robotOps []ControlOpSpec, start time.Time) (controlSequence, map[string]string, error) {
	l.mu.Lock()
	idx := indexOfControlSequence(l.sequences, id)
	if idx < 0 {
		l.mu.Unlock()
		return controlSequence{}, nil, fmt.Errorf("no control sequence %q", id)
	}
	q := l.sequences[idx]
	actions := l.actionMap()
	l.mu.Unlock()
	return compileControlSequence(q, actions, values, robotOps, start)
}

func compileControlSequence(q ControlSequenceDef, actions map[string]ControlActionDef, values map[string]string, robotOps []ControlOpSpec, start time.Time) (controlSequence, map[string]string, error) {
	vars := map[string]string{}
	for name, b := range ctlBuiltins {
		vars[name] = b.value(start)
	}
	lookup := func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
	for _, c := range q.Controls {
		raw, ok := values[c.Name]
		if !ok {
			raw = c.Default
		}
		v, err := ctlExpand(raw, lookup)
		if err != nil {
			return controlSequence{}, nil, fmt.Errorf("control %q: %v", c.Name, err)
		}
		vars[c.Name] = v
	}
	seq := controlSequence{id: q.ID, name: q.Name, description: q.Description, record: q.Record}
	for _, st := range q.Steps {
		a, ok := actions[st.Action]
		if !ok {
			return controlSequence{}, nil, fmt.Errorf("no action %q", st.Action)
		}
		label := st.Label
		if label == "" {
			label = a.Name
		}
		label, _ = ctlExpand(label, lookup) // a reference it can't fill stays as written
		// What the step will do, as far as it can be worked out before the
		// run: values saved during the run show as {{name}}.
		static := ctlPreviewScope(a, st, vars)
		var detail []string
		for _, in := range a.Do {
			args := map[string]string{}
			for k, v := range in {
				args[k], _ = ctlExpand(v, static)
			}
			detail = append(detail, describeControlInstruction(args, robotOps))
		}
		desc, _ := ctlExpand(a.Description, static)
		seq.steps = append(seq.steps, controlStep{
			ControlStepInfo: ControlStepInfo{Label: label, Detail: desc, Do: detail, Action: a.ID},
			run:             controlActionRunner(a, st, robotOps),
			beforeRecording: st.BeforeRecording,
		})
	}
	return seq, vars, nil
}

// ctlPreviewScope is the lookup used to describe an action's instructions
// in step st before the run: the action's controls (as the step sets them,
// else their defaults), then vars. References that can't be resolved yet
// (values saved during the run) are kept as written.
func ctlPreviewScope(a ControlActionDef, st ControlStepRef, vars map[string]string) func(string) (string, bool) {
	outer := func(k string) (string, bool) {
		if v, ok := vars[k]; ok {
			return v, true
		}
		return "{{" + k + "}}", true
	}
	ctl := map[string]string{}
	for _, c := range a.Controls {
		raw, ok := st.With[c.Name]
		if !ok {
			raw = c.Default
		}
		ctl[c.Name], _ = ctlExpand(raw, outer)
	}
	return func(k string) (string, bool) {
		if v, ok := ctl[k]; ok {
			return v, true
		}
		return outer(k)
	}
}

// controlActionRunner returns the run function for action a as used in
// step st. Each instruction's arguments are filled in just before it runs,
// so they see values saved by the instructions before it. Consecutive
// robot.* instructions go to IANAR together, as one robot run.
func controlActionRunner(a ControlActionDef, st ControlStepRef, robotOps []ControlOpSpec) func(r *controlRun) (string, error) {
	return func(r *controlRun) (string, error) {
		outer := func(k string) (string, bool) { v, ok := r.vars[k]; return v, ok }
		// Controls are resolved strictly: an unknown name is an error.
		ctl := map[string]string{}
		for _, c := range a.Controls {
			raw, ok := st.With[c.Name]
			if !ok {
				raw = c.Default
			}
			v, err := ctlExpand(raw, outer)
			if err != nil {
				return "", fmt.Errorf("control %q: %v", c.Name, err)
			}
			ctl[c.Name] = v
		}
		scope := func(k string) (string, bool) {
			if v, ok := ctl[k]; ok {
				return v, true
			}
			return outer(k)
		}
		label := st.Label
		if label == "" {
			label = a.Name
		}
		label, _ = ctlExpand(label, scope)

		var notes []string
		var batch []robotStep
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			steps := batch
			batch = nil
			note, err := r.robotRun(label, steps)
			if note != "" {
				notes = append(notes, note)
			}
			return err
		}
		for i, in := range a.Do {
			// LR's own ops' defaults may refer to values ({{fc}}), so they're
			// filled in before expanding; IANAR applies its ops' defaults.
			raw := map[string]string{}
			for k, v := range in {
				if k != "op" {
					raw[k] = v
				}
			}
			spec, native := findControlOp(in["op"], nil)
			if native {
				for _, sa := range spec.Args {
					if raw[sa.Name] == "" && sa.Default != "" {
						raw[sa.Name] = sa.Default
					}
				}
			}
			args := opArgs{}
			for k, v := range raw {
				x, err := ctlExpand(v, scope)
				if err != nil {
					return strings.Join(notes, "; "), fmt.Errorf("instruction %d (%s), %s: %v", i+1, in["op"], k, err)
				}
				args[k] = x
			}
			if robotOp, ok := strings.CutPrefix(in["op"], robotOpPrefix); ok {
				do := map[string]string{"op": robotOp}
				for k, v := range args {
					do[k] = v
				}
				desc := map[string]string{"op": in["op"]}
				for k, v := range args {
					desc[k] = v
				}
				batch = append(batch, robotStep{Label: describeControlInstruction(desc, robotOps), Do: []map[string]string{do}})
				continue
			}
			if err := flush(); err != nil {
				return strings.Join(notes, "; "), err
			}
			if !native {
				return strings.Join(notes, "; "), fmt.Errorf("instruction %d: unknown op %q", i+1, in["op"])
			}
			note, err := spec.run(r, args)
			if err != nil {
				if hint := args["hint"]; hint != "" {
					err = fmt.Errorf("%w -- %s", err, hint)
				}
				if len(a.Do) > 1 {
					err = fmt.Errorf("instruction %d (%s): %w", i+1, spec.Op, err)
				}
				return strings.Join(notes, "; "), err
			}
			if note != "" {
				notes = append(notes, note)
			}
		}
		if err := flush(); err != nil {
			return strings.Join(notes, "; "), err
		}
		return strings.Join(notes, "; "), nil
	}
}

// defaultControlLibraryPath is where the library is kept unless
// --control-library says otherwise.
func defaultControlLibraryPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "local-representative", "control-v1.yaml")
}
