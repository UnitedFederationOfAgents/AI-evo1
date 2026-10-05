package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// This file is IANAR's "sequence-v2" tab (condocs/initialRobotImpls/
// Step2Prompt.md, Revision G). Where sequence-v1's sequences are written in
// Go, v2's are data, built in three sub-tabs:
//
//   - definer: actions -- named, reusable building blocks that expose
//     controls (parameters, with defaults) and carry out a list of
//     instructions. Each instruction is one primitive operation IANAR
//     implements natively (see seqv2_ops.go): press keys, type text, find
//     and click text or an app's icon on screen, read a value off the
//     screen, create/check/delete a file, save a screenshot, ...
//   - composer: sequences -- ordered steps, each an action from the definer
//     with values for its controls. Sequences can expose controls of their
//     own (e.g. the weather example's country) for steps to use.
//   - runner: runs a sequence with chosen control values, reporting each
//     step and anything the run printed, recorded like a v1 run (it shares
//     sequence.go's runSequence, so also its recording and Save to file).
//
// Values may refer to controls and other variables as {{name}}, optionally
// through a filter: {{name|url}} (URL-encoded) or {{name|base}} (a path's
// file name). The variables in scope are the built-ins (desktop, home,
// timestamp), the sequence's controls, values earlier instructions saved
// (save_as), and, inside an action, its own controls.
//
// The library (actions and sequences) lives in the backend, persisted as
// YAML (by default ~/.config/ianar/sequence-v2.yaml), and is sent to every
// client on connect and after each change ("seq2-library"). Actions and
// sequences can be exported and imported as YAML (see yamlite.go for the
// subset understood); an exported sequence carries the actions it uses.

const seqLibFormat = "ianar-sequence-v2"

// Control is a parameter an action or sequence exposes.
type Control struct {
	Name    string `json:"name"`
	Label   string `json:"label,omitempty"`
	Default string `json:"default,omitempty"`
	Help    string `json:"help,omitempty"`
}

// Instruction is one primitive operation: "op" names it (see seqOps), the
// other keys are its arguments.
type Instruction map[string]string

// ActionDef is a definer action.
type ActionDef struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Controls    []Control     `json:"controls"`
	Do          []Instruction `json:"do"`
}

// StepRef is one composer step: an action, an optional label shown in place
// of the action's name, and values for the action's controls.
type StepRef struct {
	Action string            `json:"action"`
	Label  string            `json:"label,omitempty"`
	With   map[string]string `json:"with,omitempty"`
}

// SequenceV2 is a composer sequence.
type SequenceV2 struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Controls    []Control `json:"controls"`
	Steps       []StepRef `json:"steps"`
}

// ---- Validation ----

var (
	idRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	varNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	tmplRe    = regexp.MustCompile(`\{\{(.*?)\}\}`)
)

// tmplFilters are the filters a {{name|filter}} reference may apply.
var tmplFilters = map[string]func(string) string{
	"url":  url.QueryEscape,
	"base": filepath.Base,
}

// parseRef splits the inside of a {{...}} into its variable name and filter.
func parseRef(inner string) (name, filter string, err error) {
	name, filter, _ = strings.Cut(inner, "|")
	name, filter = strings.TrimSpace(name), strings.TrimSpace(filter)
	if !varNameRe.MatchString(name) {
		return "", "", fmt.Errorf("{{%s}}: %q isn't a valid name", inner, name)
	}
	if filter != "" && tmplFilters[filter] == nil {
		return "", "", fmt.Errorf("{{%s}}: unknown filter %q (have: url, base)", inner, filter)
	}
	return name, filter, nil
}

// checkTemplate reports any malformed {{...}} reference in s.
func checkTemplate(s string) error {
	for _, m := range tmplRe.FindAllStringSubmatch(s, -1) {
		if _, _, err := parseRef(m[1]); err != nil {
			return err
		}
	}
	return nil
}

// expand replaces each {{name}} / {{name|filter}} in s with its value.
func expand(s string, lookup func(string) (string, bool)) (string, error) {
	var firstErr error
	out := tmplRe.ReplaceAllStringFunc(s, func(m string) string {
		name, filter, err := parseRef(m[2 : len(m)-2])
		if err == nil {
			v, ok := lookup(name)
			if !ok {
				err = fmt.Errorf("{{%s}} isn't a control, built-in or saved value here", name)
			} else {
				if filter != "" {
					v = tmplFilters[filter](v)
				}
				return v
			}
		}
		if firstErr == nil {
			firstErr = err
		}
		return m
	})
	return out, firstErr
}

func validateControls(cs []Control) error {
	seen := map[string]bool{}
	for _, c := range cs {
		if !varNameRe.MatchString(c.Name) {
			return fmt.Errorf("control %q: names are letters, digits and _ (not starting with a digit)", c.Name)
		}
		if seen[c.Name] {
			return fmt.Errorf("control %q is defined twice", c.Name)
		}
		if _, ok := builtinVars[c.Name]; ok {
			return fmt.Errorf("control %q would hide the built-in {{%s}}; pick another name", c.Name, c.Name)
		}
		seen[c.Name] = true
		if err := checkTemplate(c.Default); err != nil {
			return fmt.Errorf("control %q's default: %v", c.Name, err)
		}
	}
	return nil
}

func validateIdentity(kind, id, name string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("%s id %q: ids are lowercase letters, digits, '.', '_' and '-'", kind, id)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s %q needs a name", kind, id)
	}
	return nil
}

// validate checks an action on its own: its id, controls, and that every
// instruction is a known op with known arguments and its required ones.
func (a ActionDef) validate() error {
	if err := validateIdentity("action", a.ID, a.Name); err != nil {
		return err
	}
	if err := validateControls(a.Controls); err != nil {
		return fmt.Errorf("action %q: %v", a.ID, err)
	}
	if len(a.Do) == 0 {
		return fmt.Errorf("action %q has no instructions", a.ID)
	}
	for i, in := range a.Do {
		if err := validateInstruction(in); err != nil {
			return fmt.Errorf("action %q, instruction %d: %v", a.ID, i+1, err)
		}
	}
	return nil
}

func validateInstruction(in Instruction) error {
	spec, ok := findOp(in["op"])
	if !ok {
		if in["op"] == "" {
			return errors.New(`no "op"`)
		}
		return fmt.Errorf("unknown op %q", in["op"])
	}
	for k, v := range in {
		if k == "op" {
			continue
		}
		if spec.arg(k) == nil {
			return fmt.Errorf("%s takes no argument %q (it takes: %s)", spec.Op, k, spec.argNames())
		}
		if err := checkTemplate(v); err != nil {
			return fmt.Errorf("%s's %s: %v", spec.Op, k, err)
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
func (q SequenceV2) validate(actions map[string]ActionDef) error {
	if err := validateIdentity("sequence", q.ID, q.Name); err != nil {
		return err
	}
	if err := validateControls(q.Controls); err != nil {
		return fmt.Errorf("sequence %q: %v", q.ID, err)
	}
	if len(q.Steps) == 0 {
		return fmt.Errorf("sequence %q has no steps", q.ID)
	}
	for i, st := range q.Steps {
		a, ok := actions[st.Action]
		if !ok {
			return fmt.Errorf("sequence %q, step %d: no action %q", q.ID, i+1, st.Action)
		}
		if err := checkTemplate(st.Label); err != nil {
			return fmt.Errorf("sequence %q, step %d, label: %v", q.ID, i+1, err)
		}
		for k, v := range st.With {
			if !hasControl(a.Controls, k) {
				return fmt.Errorf("sequence %q, step %d: action %q has no control %q", q.ID, i+1, a.ID, k)
			}
			if err := checkTemplate(v); err != nil {
				return fmt.Errorf("sequence %q, step %d, %s: %v", q.ID, i+1, k, err)
			}
		}
	}
	return nil
}

func hasControl(cs []Control, name string) bool {
	for _, c := range cs {
		if c.Name == name {
			return true
		}
	}
	return false
}

// ---- Built-in variables ----

// builtinVars are the variables every value can refer to.
var builtinVars = map[string]struct {
	help  string
	value func(start time.Time) string
}{
	"desktop":   {"the desktop folder (XDG_DESKTOP_DIR, usually ~/Desktop)", func(time.Time) string { return desktopDir() }},
	"home":      {"the home folder", func(time.Time) string { return homeDir() }},
	"timestamp": {"when the run started, as 2006-01-02T15-04-05 (safe in file names)", func(t time.Time) string { return t.Format("2006-01-02T15-04-05") }},
}

// BuiltinVar describes a built-in for the frontend.
type BuiltinVar struct {
	Name  string `json:"name"`
	Help  string `json:"help"`
	Value string `json:"value"`
}

func builtinList() []BuiltinVar {
	var out []BuiltinVar
	now := clock()
	for name, b := range builtinVars {
		out = append(out, BuiltinVar{Name: name, Help: b.help, Value: b.value(now)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// homeDir and desktopDir are overridable in tests.
var homeDir = func() string {
	h, _ := os.UserHomeDir()
	return h
}

var desktopDir = func() string {
	home := homeDir()
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	if data, err := os.ReadFile(filepath.Join(cfg, "user-dirs.dirs")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			v, ok := strings.CutPrefix(strings.TrimSpace(line), "XDG_DESKTOP_DIR=")
			if !ok {
				continue
			}
			v = strings.Trim(v, `"`)
			v = strings.Replace(v, "$HOME", home, 1)
			if filepath.IsAbs(v) {
				return v
			}
		}
	}
	return filepath.Join(home, "Desktop")
}

// ---- YAML ----

// seqLibDoc is the YAML document actions and sequences are exchanged in.
type seqLibDoc struct {
	Actions   []ActionDef
	Sequences []SequenceV2
}

func encodeSeqLib(doc seqLibDoc) string {
	w := &yamlWriter{}
	w.line(0, "# IANAR sequence-v2 library: definer actions and composer sequences.")
	w.line(0, "format: "+seqLibFormat)
	writeControls := func(ind int, cs []Control) {
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
			writeControls(4, q.Controls)
			w.line(4, "steps:")
			for _, st := range q.Steps {
				w.line(6, "- action: "+yamlScalar(st.Action))
				w.field(8, "label", st.Label)
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
	return w.b.String()
}

// decodeSeqLib reads a document written by encodeSeqLib, or by hand in the
// same shape. Unknown keys are errors, so a typo isn't silently ignored.
func decodeSeqLib(src string) (seqLibDoc, error) {
	var doc seqLibDoc
	root, err := parseYAML(src)
	if err != nil {
		return doc, err
	}
	if root.kind != yMap {
		return doc, fmt.Errorf("line %d: expected the document to be a mapping with actions: and/or sequences:", root.line)
	}
	if err := onlyKeys(root, "format", "actions", "sequences"); err != nil {
		return doc, err
	}
	if f := root.get("format"); f != nil && f.str != seqLibFormat {
		return doc, fmt.Errorf("line %d: format is %q, expected %q", f.line, f.str, seqLibFormat)
	}
	if n := root.get("actions"); n != nil {
		items, err := seqItems(n, "actions")
		if err != nil {
			return doc, err
		}
		for _, it := range items {
			a, err := decodeAction(it)
			if err != nil {
				return doc, err
			}
			doc.Actions = append(doc.Actions, a)
		}
	}
	if n := root.get("sequences"); n != nil {
		items, err := seqItems(n, "sequences")
		if err != nil {
			return doc, err
		}
		for _, it := range items {
			q, err := decodeSequence(it)
			if err != nil {
				return doc, err
			}
			doc.Sequences = append(doc.Sequences, q)
		}
	}
	if len(doc.Actions) == 0 && len(doc.Sequences) == 0 {
		return doc, errors.New("the document has no actions or sequences")
	}
	return doc, nil
}

func onlyKeys(n *yNode, allowed ...string) error {
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

// seqItems returns n's items, where n must be a list (an empty value counts
// as an empty list).
func seqItems(n *yNode, what string) ([]*yNode, error) {
	if n.kind == yScalar && n.str == "" {
		return nil, nil
	}
	if n.kind != ySeq {
		return nil, fmt.Errorf("line %d: %s should be a list, not %s", n.line, what, n.kind)
	}
	return n.items, nil
}

// scalarField reads key from mapping n as a string ("" if absent).
func scalarField(n *yNode, key string) (string, error) {
	v := n.get(key)
	if v == nil {
		return "", nil
	}
	if v.kind != yScalar {
		return "", fmt.Errorf("line %d: %s should be a single value, not %s", v.line, key, v.kind)
	}
	return v.str, nil
}

// stringMap reads a mapping of single values.
func stringMap(n *yNode, what string) (map[string]string, error) {
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

func decodeControls(n *yNode) ([]Control, error) {
	if n == nil {
		return nil, nil
	}
	items, err := seqItems(n, "controls")
	if err != nil {
		return nil, err
	}
	var out []Control
	for _, it := range items {
		m, err := stringMap(it, "control")
		if err != nil {
			return nil, err
		}
		if err := onlyKeys(it, "name", "label", "default", "help"); err != nil {
			return nil, err
		}
		out = append(out, Control{Name: m["name"], Label: m["label"], Default: m["default"], Help: m["help"]})
	}
	return out, nil
}

func decodeAction(n *yNode) (ActionDef, error) {
	var a ActionDef
	if n.kind != yMap {
		return a, fmt.Errorf("line %d: each action should be a mapping, not %s", n.line, n.kind)
	}
	if err := onlyKeys(n, "id", "name", "description", "controls", "do"); err != nil {
		return a, err
	}
	var err error
	for _, f := range []struct {
		key string
		dst *string
	}{{"id", &a.ID}, {"name", &a.Name}, {"description", &a.Description}} {
		if *f.dst, err = scalarField(n, f.key); err != nil {
			return a, err
		}
	}
	if a.Controls, err = decodeControls(n.get("controls")); err != nil {
		return a, err
	}
	if d := n.get("do"); d != nil {
		items, err := seqItems(d, "do")
		if err != nil {
			return a, err
		}
		for _, it := range items {
			m, err := stringMap(it, "instruction")
			if err != nil {
				return a, err
			}
			a.Do = append(a.Do, Instruction(m))
		}
	}
	return a, nil
}

func decodeSequence(n *yNode) (SequenceV2, error) {
	var q SequenceV2
	if n.kind != yMap {
		return q, fmt.Errorf("line %d: each sequence should be a mapping, not %s", n.line, n.kind)
	}
	if err := onlyKeys(n, "id", "name", "description", "controls", "steps"); err != nil {
		return q, err
	}
	var err error
	for _, f := range []struct {
		key string
		dst *string
	}{{"id", &q.ID}, {"name", &q.Name}, {"description", &q.Description}} {
		if *f.dst, err = scalarField(n, f.key); err != nil {
			return q, err
		}
	}
	if q.Controls, err = decodeControls(n.get("controls")); err != nil {
		return q, err
	}
	if s := n.get("steps"); s != nil {
		items, err := seqItems(s, "steps")
		if err != nil {
			return q, err
		}
		for _, it := range items {
			if it.kind != yMap {
				return q, fmt.Errorf("line %d: each step should be a mapping with action:, not %s", it.line, it.kind)
			}
			if err := onlyKeys(it, "action", "label", "with"); err != nil {
				return q, err
			}
			var st StepRef
			if st.Action, err = scalarField(it, "action"); err != nil {
				return q, err
			}
			if st.Label, err = scalarField(it, "label"); err != nil {
				return q, err
			}
			if w := it.get("with"); w != nil {
				if st.With, err = stringMap(w, "with"); err != nil {
					return q, err
				}
			}
			q.Steps = append(q.Steps, st)
		}
	}
	return q, nil
}

// ---- The library ----

// seqLibrary holds the definer's actions and the composer's sequences,
// persisted to path ("" keeps them in memory only).
type seqLibrary struct {
	mu        sync.Mutex
	path      string
	actions   []ActionDef
	sequences []SequenceV2
	note      string // a problem loading the file, shown in the UI
}

// openSeqLibrary loads the library at path, starting from the examples if
// there is none yet. A file that can't be read is set aside, not
// overwritten.
func openSeqLibrary(path string) *seqLibrary {
	l := &seqLibrary{path: path}
	l.actions, l.sequences = exampleLibrary()
	if path == "" {
		return l
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		log.Printf("sequence-v2: no library at %s yet; starting from the examples", path)
		return l
	case err != nil:
		l.note = fmt.Sprintf("couldn't read %s (%v); using the examples, and changes won't be saved", path, err)
		l.path = ""
		return l
	}
	doc, err := decodeSeqLib(string(data))
	if err == nil {
		err = checkLibrary(doc.Actions, doc.Sequences)
	}
	if err != nil {
		aside := path + ".broken-" + clock().Format("2006-01-02T15-04-05")
		if rerr := os.Rename(path, aside); rerr != nil {
			l.note = fmt.Sprintf("couldn't load %s (%v); using the examples, and changes won't be saved", path, err)
			l.path = ""
		} else {
			l.note = fmt.Sprintf("couldn't load %s (%v); moved it to %s and started from the examples", path, err, aside)
		}
		log.Printf("sequence-v2: %s", l.note)
		return l
	}
	l.actions, l.sequences = doc.Actions, doc.Sequences
	return l
}

// checkLibrary validates a whole library.
func checkLibrary(actions []ActionDef, sequences []SequenceV2) error {
	byID := map[string]ActionDef{}
	for _, a := range actions {
		if err := a.validate(); err != nil {
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

// commit validates a proposed library and, if it holds, adopts and saves
// it. Called with l.mu held.
func (l *seqLibrary) commit(actions []ActionDef, sequences []SequenceV2) error {
	if err := checkLibrary(actions, sequences); err != nil {
		return err
	}
	if l.path != "" {
		data := encodeSeqLib(seqLibDoc{Actions: actions, Sequences: sequences})
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

func (l *seqLibrary) actionMap() map[string]ActionDef {
	m := make(map[string]ActionDef, len(l.actions))
	for _, a := range l.actions {
		m[a.ID] = a
	}
	return m
}

// saveAction adds a, or replaces the action previously called prevID (or,
// with no prevID, one with a's id). Renaming an action updates the
// sequences that use it.
func (l *seqLibrary) saveAction(a ActionDef, prevID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if prevID == "" {
		prevID = a.ID
	}
	actions := make([]ActionDef, 0, len(l.actions)+1)
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
		sequences = renameActionRefs(l.sequences, prevID, a.ID)
	}
	return l.commit(actions, sequences)
}

func renameActionRefs(sequences []SequenceV2, from, to string) []SequenceV2 {
	out := make([]SequenceV2, len(sequences))
	for i, q := range sequences {
		q.Steps = append([]StepRef(nil), q.Steps...)
		for j := range q.Steps {
			if q.Steps[j].Action == from {
				q.Steps[j].Action = to
			}
		}
		out[i] = q
	}
	return out
}

// deleteAction removes action id, unless a sequence uses it.
func (l *seqLibrary) deleteAction(id string) error {
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
	var actions []ActionDef
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
func (l *seqLibrary) saveSequence(q SequenceV2, prevID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if prevID == "" {
		prevID = q.ID
	}
	sequences := make([]SequenceV2, 0, len(l.sequences)+1)
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

func (l *seqLibrary) deleteSequence(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var sequences []SequenceV2
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
func (l *seqLibrary) merge(doc seqLibDoc) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	actions := append([]ActionDef(nil), l.actions...)
	sequences := append([]SequenceV2(nil), l.sequences...)
	var added, replaced []string
	for _, a := range doc.Actions {
		i := indexOfAction(actions, a.ID)
		if i >= 0 {
			actions[i] = a
			replaced = append(replaced, "action "+a.ID)
		} else {
			actions = append(actions, a)
			added = append(added, "action "+a.ID)
		}
	}
	for _, q := range doc.Sequences {
		i := indexOfSequence(sequences, q.ID)
		if i >= 0 {
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

func indexOfAction(as []ActionDef, id string) int {
	for i, a := range as {
		if a.ID == id {
			return i
		}
	}
	return -1
}

func indexOfSequence(qs []SequenceV2, id string) int {
	for i, q := range qs {
		if q.ID == id {
			return i
		}
	}
	return -1
}

// export renders actions (kind "actions") or sequences, with the actions
// they use (kind "sequences"), as YAML: those named by ids, or all of them.
func (l *seqLibrary) export(kind string, ids []string) (yaml, filename string, err error) {
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
	var doc seqLibDoc
	switch kind {
	case "actions":
		for _, a := range l.actions {
			if want(a.ID) {
				doc.Actions = append(doc.Actions, a)
			}
		}
		filename = "ianar-actions.yaml"
		if len(doc.Actions) == 1 {
			filename = "ianar-action-" + doc.Actions[0].ID + ".yaml"
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
		filename = "ianar-sequences.yaml"
		if len(doc.Sequences) == 1 {
			filename = "ianar-sequence-" + doc.Sequences[0].ID + ".yaml"
		}
	default:
		return "", "", fmt.Errorf("can't export %q", kind)
	}
	if len(doc.Actions) == 0 && len(doc.Sequences) == 0 {
		return "", "", fmt.Errorf("nothing to export")
	}
	return encodeSeqLib(doc), filename, nil
}

// restoreExamples puts the built-in example actions and sequences back,
// replacing any edited copies.
func (l *seqLibrary) restoreExamples() (string, error) {
	actions, sequences := exampleLibrary()
	return l.merge(seqLibDoc{Actions: actions, Sequences: sequences})
}

// SeqLibraryMsg is the "seq2-library" WebSocket payload: the whole library,
// plus what the definer needs to edit it.
type SeqLibraryMsg struct {
	Actions   []ActionDef  `json:"actions"`
	Sequences []SequenceV2 `json:"sequences"`
	Ops       []opSpec     `json:"ops"`
	Builtins  []BuiltinVar `json:"builtins"`
	Path      string       `json:"path,omitempty"` // where it's saved; "" if only in memory
	Note      string       `json:"note,omitempty"`
}

func (l *seqLibrary) snapshot() SeqLibraryMsg {
	l.mu.Lock()
	defer l.mu.Unlock()
	return SeqLibraryMsg{
		Actions:   append([]ActionDef{}, l.actions...),
		Sequences: append([]SequenceV2{}, l.sequences...),
		Ops:       seqOps,
		Builtins:  builtinList(),
		Path:      l.path,
		Note:      l.note,
	}
}

// ---- Compiling a sequence to run ----

// compile turns sequence id into a runnable sequence (see sequence.go), with
// values for its controls (missing ones take their defaults).
func (l *seqLibrary) compile(id string, values map[string]string, start time.Time) (sequence, error) {
	l.mu.Lock()
	idx := indexOfSequence(l.sequences, id)
	if idx < 0 {
		l.mu.Unlock()
		return sequence{}, fmt.Errorf("no sequence %q", id)
	}
	q := l.sequences[idx]
	actions := l.actionMap()
	l.mu.Unlock()
	return compileSequence(q, actions, values, start)
}

func compileSequence(q SequenceV2, actions map[string]ActionDef, values map[string]string, start time.Time) (sequence, error) {
	vars := map[string]string{}
	for name, b := range builtinVars {
		vars[name] = b.value(start)
	}
	lookupIn := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	for _, c := range q.Controls {
		raw, ok := values[c.Name]
		if !ok {
			raw = c.Default
		}
		v, err := expand(raw, lookupIn(vars))
		if err != nil {
			return sequence{}, fmt.Errorf("control %q: %v", c.Name, err)
		}
		vars[c.Name] = v
	}
	seq := sequence{id: q.ID, name: q.Name, vars: vars}
	for _, st := range q.Steps {
		a, ok := actions[st.Action]
		if !ok {
			return sequence{}, fmt.Errorf("no action %q", st.Action)
		}
		label := st.Label
		if label == "" {
			label = a.Name
		}
		label, _ = expand(label, lookupIn(vars)) // a reference it can't fill stays as written
		// What the step will do, as far as it can be worked out before the
		// run: values saved during the run show as {{name}}.
		static := previewScope(a, st, vars)
		var detail []string
		for _, in := range a.Do {
			args := map[string]string{}
			for k, v := range in {
				args[k], _ = expand(v, static)
			}
			detail = append(detail, describeInstruction(args))
		}
		seq.steps = append(seq.steps, seqStep{SequenceStepDef{Label: label, Detail: detail}, actionRunner(a, st)})
	}
	return seq, nil
}

// previewScope returns the lookup used to describe an action's
// instructions in step st before the run: the action's controls (as the
// step sets them, else their defaults), then vars. References that can't be
// resolved yet (values saved during the run) are kept as written.
func previewScope(a ActionDef, st StepRef, vars map[string]string) func(string) (string, bool) {
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
		ctl[c.Name], _ = expand(raw, outer)
	}
	return func(k string) (string, bool) {
		if v, ok := ctl[k]; ok {
			return v, true
		}
		return outer(k)
	}
}

// actionRunner returns the run function for action a as used in step st.
// Each instruction's arguments are filled in just before it runs, so they
// see values saved by the instructions before it.
func actionRunner(a ActionDef, st StepRef) func(env *seqEnv) (string, error) {
	return func(env *seqEnv) (string, error) {
		outer := func(k string) (string, bool) { v, ok := env.vars[k]; return v, ok }
		// Controls are resolved strictly: an unknown name is an error.
		ctl := map[string]string{}
		for _, c := range a.Controls {
			raw, ok := st.With[c.Name]
			if !ok {
				raw = c.Default
			}
			v, err := expand(raw, outer)
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
		var notes []string
		for i, in := range a.Do {
			spec, _ := findOp(in["op"])
			args := opArgs{}
			for k, v := range in {
				if k == "op" {
					continue
				}
				x, err := expand(v, scope)
				if err != nil {
					return strings.Join(notes, "; "), fmt.Errorf("instruction %d (%s), %s: %v", i+1, spec.Op, k, err)
				}
				args[k] = x
			}
			for _, sa := range spec.Args {
				if args[sa.Name] == "" && sa.Default != "" {
					args[sa.Name] = sa.Default
				}
			}
			note, err := spec.run(env, args)
			if err != nil {
				if len(a.Do) > 1 {
					err = fmt.Errorf("instruction %d (%s): %w", i+1, spec.Op, err)
				}
				return strings.Join(notes, "; "), err
			}
			if note != "" {
				notes = append(notes, note)
			}
		}
		return strings.Join(notes, "; "), nil
	}
}

// ---- WebSocket ----

// Seq2ReplyMsg is the "seq2-reply" payload answering a library edit,
// import or export. Req echoes the request's req, so the sub-tab that asked
// knows the answer is its own.
type Seq2ReplyMsg struct {
	Req      string `json:"req,omitempty"`
	Op       string `json:"op"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
	Message  string `json:"message,omitempty"`
	YAML     string `json:"yaml,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// Seq2StartedMsg is the "seq2-started" payload: a run has begun, with its
// steps as compiled (labels, and what each does with the values chosen).
type Seq2StartedMsg struct {
	SequenceID string      `json:"sequence_id"`
	Def        SequenceDef `json:"def"`
}

// broadcast sends a message to every connected client.
func (s *Server) broadcast(typ string, payload interface{}) {
	msg := s.marshalMsg(typ, payload)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

// handleSeq2 handles the sequence-v2 tab's "seq2-*" messages.
func (s *Server) handleSeq2(c *wsClient, m wsMsg) {
	var p struct {
		Req      string            `json:"req"`
		ID       string            `json:"id"`
		PrevID   string            `json:"previous_id"`
		Action   *ActionDef        `json:"action"`
		Sequence *SequenceV2       `json:"sequence"`
		YAML     string            `json:"yaml"`
		Kind     string            `json:"kind"`
		IDs      []string          `json:"ids"`
		Controls map[string]string `json:"controls"`
	}
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		s.sendToClient(c, "seq2-reply", Seq2ReplyMsg{Op: m.Type, Error: "bad request: " + err.Error()})
		return
	}
	if m.Type == "seq2-run" {
		s.handleRunSequenceV2(c, p.ID, p.Controls)
		return
	}

	reply := Seq2ReplyMsg{Req: p.Req, Op: m.Type}
	var err error
	changed := true
	switch m.Type {
	case "seq2-save-action":
		if p.Action == nil {
			err = errors.New("no action given")
			break
		}
		if err = p.Action.validate(); err == nil {
			err = s.seqLib.saveAction(*p.Action, p.PrevID)
			reply.Message = fmt.Sprintf("saved action %q", p.Action.ID)
		}
	case "seq2-delete-action":
		err = s.seqLib.deleteAction(p.ID)
		reply.Message = fmt.Sprintf("deleted action %q", p.ID)
	case "seq2-save-sequence":
		if p.Sequence == nil {
			err = errors.New("no sequence given")
			break
		}
		err = s.seqLib.saveSequence(*p.Sequence, p.PrevID)
		reply.Message = fmt.Sprintf("saved sequence %q", p.Sequence.ID)
	case "seq2-delete-sequence":
		err = s.seqLib.deleteSequence(p.ID)
		reply.Message = fmt.Sprintf("deleted sequence %q", p.ID)
	case "seq2-import":
		var doc seqLibDoc
		if doc, err = decodeSeqLib(p.YAML); err == nil {
			reply.Message, err = s.seqLib.merge(doc)
			if err == nil {
				reply.Message = "imported: " + reply.Message
			}
		}
	case "seq2-export":
		changed = false
		reply.YAML, reply.Filename, err = s.seqLib.export(p.Kind, p.IDs)
	case "seq2-restore-examples":
		reply.Message, err = s.seqLib.restoreExamples()
		if err == nil {
			reply.Message = "restored the examples: " + reply.Message
		}
	default:
		return
	}
	if err != nil {
		reply.Error, reply.Message = err.Error(), ""
		changed = false
	} else {
		reply.Success = true
	}
	s.sendToClient(c, "seq2-reply", reply)
	if changed {
		s.broadcast("seq2-library", s.seqLib.snapshot())
	}
}

// handleRunSequenceV2 runs composer sequence id with the given control
// values, streaming "seq2-started", "seq2-progress" and finally
// "seq2-result" to the requesting client.
func (s *Server) handleRunSequenceV2(c *wsClient, id string, values map[string]string) {
	q, err := s.seqLib.compile(id, values, clock())
	if err != nil {
		s.sendToClient(c, "seq2-result", SequenceResultMsg{SequenceID: id, FailedStep: -1, Error: err.Error()})
		return
	}
	s.sendToClient(c, "seq2-started", Seq2StartedMsg{SequenceID: id, Def: q.def()})
	res := runSequence(q, func(p SequenceProgressMsg) { s.sendToClient(c, "seq2-progress", p) })
	if res.Recording != nil {
		res.ArtifactID = s.artifacts.keep(sequenceArtifact(q.def(), res))
	}
	s.sendToClient(c, "seq2-result", res)
}

// defaultSeqLibraryPath is where the library is kept unless
// --sequence-library says otherwise.
func defaultSeqLibraryPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "ianar", "sequence-v2.yaml")
}
