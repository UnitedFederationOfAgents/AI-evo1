package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---- Sequence YAML in the files tab (Revision E of
// condocs/slopspaceSlopspaceCondocsImpls/Step1Prompt.md) ----
//
// A .yaml/.yml file in the host-cache or host-store that would import
// cleanly as a sequence gets its own icon in the files tab, one for each of
// the two importers: the control tab's (an lr-control-v1 document, see
// controllib.go) and the robot tab's (IANAR's ianar-sequence-v2, see
// ianar/seqv2.go). FileInfo.Sequence carries which, alongside Kind, so the
// text viewer and every other kind-driven check stay as they were.

const (
	seqFileControl = "control"
	seqFileRobot   = "robot"
)

// maxSeqFileSize caps what classifySequenceFile reads: a sequence library is
// a few KiB, and the check runs on every listing.
const maxSeqFileSize = 1 << 20 // 1MiB

// robotSeqFormat is IANAR's library format (ianar/seqv2.go's seqLibFormat).
const robotSeqFormat = "ianar-sequence-v2"

// classifySequenceFile reports whether dir/id is a YAML file that validates
// for import as a control sequence or a robot sequence ("" if neither).
// robotOps are IANAR's ops as robot.<op> specs (nil until it's reported
// them), to check a robot sequence's instructions against.
func classifySequenceFile(dir, id string, robotOps []ControlOpSpec) string {
	ext := strings.ToLower(filepath.Ext(id))
	if ext != ".yaml" && ext != ".yml" {
		return ""
	}
	fi, err := os.Stat(filepath.Join(dir, id))
	if err != nil || fi.IsDir() || fi.Size() > maxSeqFileSize {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, id))
	if err != nil {
		return ""
	}
	return classifySequenceYAML(string(data), robotOps)
}

// classifySequenceYAML is classifySequenceFile's check on a document's
// content. Only a document with at least one sequence counts -- an actions-
// only export imports, but isn't a sequence. A document naming its format is
// only tried against that importer; one that doesn't is tried as a control
// sequence first, since LR's own ops are a closed set and IANAR's (while
// unreported) aren't.
func classifySequenceYAML(src string, robotOps []ControlOpSpec) string {
	format := ""
	if root, err := parseYAML(src); err == nil && root.kind == yMap {
		if f := root.get("format"); f != nil {
			format = f.str
		}
	}
	if format == "" || format == controlLibFormat {
		if doc, err := decodeControlLib(src); err == nil && len(doc.Sequences) > 0 &&
			checkControlLibrary(doc.Actions, doc.Sequences) == nil {
			return seqFileControl
		}
	}
	if format == "" || format == robotSeqFormat {
		if n, err := checkRobotSeqLib(src, robotOps); err == nil && n > 0 {
			return seqFileRobot
		}
	}
	return ""
}

// ---- Robot sequence-v2 validation ----
//
// A port of what IANAR's "seq2-import" checks (ianar/seqv2.go's
// decodeSeqLib and checkLibrary), against a standalone document: it's a
// separate module, so LR can't call it. Keep the two in step.

type robotSeqControl struct{ name, def string }

type robotSeqAction struct {
	id, name string
	controls []robotSeqControl
	do       []map[string]string
}

type robotSeqStep struct {
	action, label string
	with          map[string]string
}

// robotTmplFilters are the filters IANAR's {{name|filter}} allows.
var robotTmplFilters = map[string]bool{"url": true, "base": true}

// robotBuiltins are IANAR's built-in variables, which no control may hide.
var robotBuiltins = map[string]bool{"desktop": true, "home": true, "timestamp": true}

// checkRobotSeqLib decodes and validates src as IANAR would on import,
// returning how many sequences it holds.
func checkRobotSeqLib(src string, robotOps []ControlOpSpec) (int, error) {
	root, err := parseYAML(src)
	if err != nil {
		return 0, err
	}
	if root.kind != yMap {
		return 0, errors.New("expected the document to be a mapping")
	}
	if err := ctlOnlyKeys(root, "format", "actions", "sequences", "examples"); err != nil {
		return 0, err
	}
	if f := root.get("format"); f != nil && f.str != robotSeqFormat {
		return 0, fmt.Errorf("format is %q, expected %q", f.str, robotSeqFormat)
	}

	actions := map[string]robotSeqAction{}
	if n := root.get("actions"); n != nil {
		items, err := ctlItems(n, "actions")
		if err != nil {
			return 0, err
		}
		for _, it := range items {
			a, err := decodeRobotSeqAction(it)
			if err != nil {
				return 0, err
			}
			if err := a.validate(robotOps); err != nil {
				return 0, err
			}
			if _, dup := actions[a.id]; dup {
				return 0, fmt.Errorf("action %q is defined twice", a.id)
			}
			actions[a.id] = a
		}
	}

	count := 0
	seen := map[string]bool{}
	if n := root.get("sequences"); n != nil {
		items, err := ctlItems(n, "sequences")
		if err != nil {
			return 0, err
		}
		for _, it := range items {
			id, err := checkRobotSeqSequence(it, actions)
			if err != nil {
				return 0, err
			}
			if seen[id] {
				return 0, fmt.Errorf("sequence %q is defined twice", id)
			}
			seen[id] = true
			count++
		}
	}
	if n := root.get("examples"); n != nil {
		if _, err := ctlStringMap(n, "examples"); err != nil {
			return 0, err
		}
	}
	return count, nil
}

func robotCheckTemplate(s string) error {
	for _, m := range ctlTmplRe.FindAllStringSubmatch(s, -1) {
		name, filter, _ := strings.Cut(m[1], "|")
		name, filter = strings.TrimSpace(name), strings.TrimSpace(filter)
		if !ctlNameRe.MatchString(name) {
			return fmt.Errorf("{{%s}}: %q isn't a valid name", m[1], name)
		}
		if filter != "" && !robotTmplFilters[filter] {
			return fmt.Errorf("{{%s}}: unknown filter %q", m[1], filter)
		}
	}
	return nil
}

func decodeRobotSeqControls(n *yNode) ([]robotSeqControl, error) {
	if n == nil {
		return nil, nil
	}
	items, err := ctlItems(n, "controls")
	if err != nil {
		return nil, err
	}
	var out []robotSeqControl
	seen := map[string]bool{}
	for _, it := range items {
		m, err := ctlStringMap(it, "control")
		if err != nil {
			return nil, err
		}
		if err := ctlOnlyKeys(it, "name", "label", "default", "help"); err != nil {
			return nil, err
		}
		c := robotSeqControl{name: m["name"], def: m["default"]}
		switch {
		case !ctlNameRe.MatchString(c.name):
			return nil, fmt.Errorf("control %q: not a valid name", c.name)
		case seen[c.name]:
			return nil, fmt.Errorf("control %q is defined twice", c.name)
		case robotBuiltins[c.name]:
			return nil, fmt.Errorf("control %q would hide a built-in", c.name)
		}
		if err := robotCheckTemplate(c.def); err != nil {
			return nil, fmt.Errorf("control %q's default: %v", c.name, err)
		}
		seen[c.name] = true
		out = append(out, c)
	}
	return out, nil
}

func decodeRobotSeqAction(n *yNode) (robotSeqAction, error) {
	var a robotSeqAction
	if n.kind != yMap {
		return a, fmt.Errorf("line %d: each action should be a mapping", n.line)
	}
	if err := ctlOnlyKeys(n, "id", "name", "description", "controls", "do"); err != nil {
		return a, err
	}
	var err error
	if a.id, err = ctlScalar(n, "id"); err != nil {
		return a, err
	}
	if a.name, err = ctlScalar(n, "name"); err != nil {
		return a, err
	}
	if _, err = ctlScalar(n, "description"); err != nil {
		return a, err
	}
	if a.controls, err = decodeRobotSeqControls(n.get("controls")); err != nil {
		return a, fmt.Errorf("action %q: %v", a.id, err)
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
			a.do = append(a.do, m)
		}
	}
	return a, nil
}

// validate mirrors ianar's ActionDef.validate. With robotOps nil (IANAR
// hasn't reported its ops), an instruction is only checked for naming an op.
func (a robotSeqAction) validate(robotOps []ControlOpSpec) error {
	if err := ctlValidateIdentity("action", a.id, a.name); err != nil {
		return err
	}
	if len(a.do) == 0 {
		return fmt.Errorf("action %q has no instructions", a.id)
	}
	for i, in := range a.do {
		op := in["op"]
		if op == "" {
			return fmt.Errorf("action %q, instruction %d: no \"op\"", a.id, i+1)
		}
		for k, v := range in {
			if err := robotCheckTemplate(v); err != nil {
				return fmt.Errorf("action %q, instruction %d, %s: %v", a.id, i+1, k, err)
			}
		}
		if robotOps == nil {
			if !ctlIDRe.MatchString(op) || strings.HasPrefix(op, robotOpPrefix) {
				return fmt.Errorf("action %q, instruction %d: %q isn't a robot op name", a.id, i+1, op)
			}
			continue
		}
		spec, ok := findControlOp(robotOpPrefix+op, robotOps)
		if !ok {
			return fmt.Errorf("action %q, instruction %d: unknown op %q", a.id, i+1, op)
		}
		for k := range in {
			if k != "op" && spec.arg(k) == nil {
				return fmt.Errorf("action %q, instruction %d: %s takes no argument %q", a.id, i+1, op, k)
			}
		}
		for _, arg := range spec.Args {
			if arg.Required && strings.TrimSpace(in[arg.Name]) == "" {
				return fmt.Errorf("action %q, instruction %d: %s needs %q", a.id, i+1, op, arg.Name)
			}
		}
	}
	return nil
}

// checkRobotSeqSequence decodes one sequence and validates it against
// actions, mirroring ianar's decodeSequence and SequenceV2.validate.
func checkRobotSeqSequence(n *yNode, actions map[string]robotSeqAction) (string, error) {
	if n.kind != yMap {
		return "", fmt.Errorf("line %d: each sequence should be a mapping", n.line)
	}
	if err := ctlOnlyKeys(n, "id", "name", "description", "controls", "steps"); err != nil {
		return "", err
	}
	id, err := ctlScalar(n, "id")
	if err != nil {
		return "", err
	}
	name, err := ctlScalar(n, "name")
	if err != nil {
		return "", err
	}
	if _, err := ctlScalar(n, "description"); err != nil {
		return "", err
	}
	if err := ctlValidateIdentity("sequence", id, name); err != nil {
		return "", err
	}
	if _, err := decodeRobotSeqControls(n.get("controls")); err != nil {
		return "", fmt.Errorf("sequence %q: %v", id, err)
	}
	var steps []robotSeqStep
	if s := n.get("steps"); s != nil {
		items, err := ctlItems(s, "steps")
		if err != nil {
			return "", err
		}
		for _, it := range items {
			if it.kind != yMap {
				return "", fmt.Errorf("line %d: each step should be a mapping with action:", it.line)
			}
			if err := ctlOnlyKeys(it, "action", "label", "with"); err != nil {
				return "", err
			}
			var st robotSeqStep
			if st.action, err = ctlScalar(it, "action"); err != nil {
				return "", err
			}
			if st.label, err = ctlScalar(it, "label"); err != nil {
				return "", err
			}
			if w := it.get("with"); w != nil {
				if st.with, err = ctlStringMap(w, "with"); err != nil {
					return "", err
				}
			}
			steps = append(steps, st)
		}
	}
	if len(steps) == 0 {
		return "", fmt.Errorf("sequence %q has no steps", id)
	}
	for i, st := range steps {
		a, ok := actions[st.action]
		if !ok {
			return "", fmt.Errorf("sequence %q, step %d: no action %q", id, i+1, st.action)
		}
		if err := robotCheckTemplate(st.label); err != nil {
			return "", fmt.Errorf("sequence %q, step %d, label: %v", id, i+1, err)
		}
		for k, v := range st.with {
			found := false
			for _, c := range a.controls {
				found = found || c.name == k
			}
			if !found {
				return "", fmt.Errorf("sequence %q, step %d: action %q has no control %q", id, i+1, a.id, k)
			}
			if err := robotCheckTemplate(v); err != nil {
				return "", fmt.Errorf("sequence %q, step %d, %s: %v", id, i+1, k, err)
			}
		}
	}
	return id, nil
}
