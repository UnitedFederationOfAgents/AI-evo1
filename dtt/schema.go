package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// This file is a T3's schema: tool.yaml, format dtt-v1 (versioned the way
// local-representative's lr-control-v1 library is). A T3's inputs carry the
// same fields as a control op's arguments (local-representative/
// controlops.go's ControlOpArg: name, help, required, default) plus a type,
// so a T3 is a control op as it stands -- "dtt list --json" lists each one
// in ControlOpSpec's shape (ToolInfo below).

const (
	toolFormat   = "dtt-v1"
	toolFile     = "tool.yaml"
	toolOpPrefix = "tool."

	defaultToolTimeout = 60 * time.Second
	maxToolTimeout     = time.Hour
)

var (
	toolNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	// fieldNameRe names inputs and outputs. Outputs become control-run
	// values ({{name}}), so it's a subset of LR's ctlNameRe.
	fieldNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// reservedInputs are the arguments local-representative adds to every
// tool.<name> control op (see local-representative/tools.go), so no input
// may take their names.
var reservedInputs = map[string]bool{"op": true, "save_prefix": true, "hint": true}

// Input types. Every value still travels as a string (on the command line
// and in DTT_IN_*); the type decides how it's checked and how it appears in
// the JSON on the tool's stdin.
var inputTypes = map[string]string{
	"string": "one line of text",
	"text":   "text that may span lines",
	"int":    "a whole number",
	"number": "a number",
	"bool":   "true or false",
	"path":   "a path on this host (~ for home)",
}

// Tool is a parsed tool.yaml.
type Tool struct {
	Format      string
	Name        string
	Description string
	Describe    string // how a control step lists a call, {input} replaced by its value
	Inputs      []Input
	Values      []Output // named output values
	Files       []Output // named output files
	Runtime     string   // "uv" or "exec"
	Script      string   // relative to the tool's directory
	Timeout     time.Duration

	// Where it was found (catalog.go): Origin is "builtin", "user" or
	// "tools-dirs"; Dir its directory ("" for a built-in, which lives in the
	// binary); Source the tool.yaml text.
	Origin string
	Dir    string
	Source string
}

type Input struct {
	Name     string
	Type     string
	Help     string
	Required bool
	Default  string
	Choices  []string
}

type Output struct {
	Name string `json:"name"`
	Help string `json:"help,omitempty"`
}

// parseTool parses and checks tool.yaml's text. It checks the schema only;
// checkScript checks the entrypoint is there.
func parseTool(src string) (*Tool, error) {
	doc, err := parseYAML(src)
	if err != nil {
		return nil, err
	}
	if doc.kind != yMap {
		return nil, fmt.Errorf("%s should be a mapping (key: value lines), not %s", toolFile, doc.kind)
	}
	if err := onlyKeys(doc, "tool.yaml", "format", "name", "description", "describe", "inputs", "outputs", "entrypoint"); err != nil {
		return nil, err
	}
	t := &Tool{Source: src}
	if t.Format, err = scalar(doc, "format"); err != nil {
		return nil, err
	}
	if t.Format != toolFormat {
		return nil, fmt.Errorf("format is %q; this dtt reads format: %s", t.Format, toolFormat)
	}
	if t.Name, err = scalar(doc, "name"); err != nil {
		return nil, err
	}
	if !toolNameRe.MatchString(t.Name) {
		return nil, fmt.Errorf("name %q: tool names are lowercase letters, digits and '-', starting with a letter", t.Name)
	}
	if t.Description, err = scalar(doc, "description"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(t.Description) == "" {
		return nil, fmt.Errorf("%s needs a description", t.Name)
	}
	if t.Describe, err = scalar(doc, "describe"); err != nil {
		return nil, err
	}

	seen := map[string]string{}
	claim := func(name, what string) error {
		if !fieldNameRe.MatchString(name) {
			return fmt.Errorf("%s %q: names are lowercase letters, digits and '_', starting with a letter", what, name)
		}
		if prev, dup := seen[name]; dup {
			return fmt.Errorf("%s %q has the same name as %s", what, name, prev)
		}
		seen[name] = what
		return nil
	}

	if n := doc.get("inputs"); n != nil && !isEmpty(n) {
		if n.kind != ySeq {
			return nil, fmt.Errorf("line %d: inputs should be a list (- name: ... items), not %s", n.line, n.kind)
		}
		for _, item := range n.items {
			in, err := parseInput(item)
			if err != nil {
				return nil, err
			}
			if err := claim(in.Name, "input"); err != nil {
				return nil, err
			}
			if reservedInputs[in.Name] {
				return nil, fmt.Errorf("input %q: that name is reserved for the control op's own arguments", in.Name)
			}
			t.Inputs = append(t.Inputs, in)
		}
	}

	if n := doc.get("outputs"); n != nil && !isEmpty(n) {
		if n.kind != yMap {
			return nil, fmt.Errorf("line %d: outputs should be a mapping with values and files lists, not %s", n.line, n.kind)
		}
		if err := onlyKeys(n, "outputs", "values", "files"); err != nil {
			return nil, err
		}
		for _, part := range []struct {
			key  string
			what string
			dst  *[]Output
		}{{"values", "output value", &t.Values}, {"files", "output file", &t.Files}} {
			list := n.get(part.key)
			if list == nil || isEmpty(list) {
				continue
			}
			if list.kind != ySeq {
				return nil, fmt.Errorf("line %d: outputs.%s should be a list, not %s", list.line, part.key, list.kind)
			}
			for _, item := range list.items {
				o, err := parseOutput(item, part.key)
				if err != nil {
					return nil, err
				}
				if err := claim(o.Name, part.what); err != nil {
					return nil, err
				}
				*part.dst = append(*part.dst, o)
			}
		}
	}

	ep := doc.get("entrypoint")
	if ep == nil || ep.kind != yMap {
		return nil, fmt.Errorf("%s needs an entrypoint mapping (runtime, script, timeout)", t.Name)
	}
	if err := onlyKeys(ep, "entrypoint", "runtime", "script", "timeout"); err != nil {
		return nil, err
	}
	if t.Runtime, err = scalar(ep, "runtime"); err != nil {
		return nil, err
	}
	switch t.Runtime {
	case "":
		t.Runtime = "uv"
	case "uv", "exec":
	default:
		return nil, fmt.Errorf("entrypoint.runtime %q should be uv or exec", t.Runtime)
	}
	if t.Script, err = scalar(ep, "script"); err != nil {
		return nil, err
	}
	if err := checkRelPath(t.Script); err != nil {
		return nil, fmt.Errorf("entrypoint.script: %v", err)
	}
	timeout, err := scalar(ep, "timeout")
	if err != nil {
		return nil, err
	}
	t.Timeout = defaultToolTimeout
	if timeout != "" {
		if t.Timeout, err = time.ParseDuration(timeout); err != nil || t.Timeout <= 0 || t.Timeout > maxToolTimeout {
			return nil, fmt.Errorf("entrypoint.timeout %q should be a duration like 30s or 5m, at most %s", timeout, maxToolTimeout)
		}
	}
	return t, nil
}

func parseInput(n *yNode) (Input, error) {
	if n.kind != yMap {
		return Input{}, fmt.Errorf("line %d: each input should be a mapping (name, type, help, ...), not %s", n.line, n.kind)
	}
	if err := onlyKeys(n, "an input", "name", "type", "help", "required", "default", "choices"); err != nil {
		return Input{}, err
	}
	var in Input
	var err error
	if in.Name, err = scalar(n, "name"); err != nil {
		return in, err
	}
	if in.Type, err = scalar(n, "type"); err != nil {
		return in, err
	}
	if in.Type == "" {
		in.Type = "string"
	}
	if _, ok := inputTypes[in.Type]; !ok {
		return in, fmt.Errorf("input %q: type %q should be one of %s", in.Name, in.Type, typeNames())
	}
	if in.Help, err = scalar(n, "help"); err != nil {
		return in, err
	}
	req, err := scalar(n, "required")
	if err != nil {
		return in, err
	}
	if in.Required, err = parseBool(req); err != nil {
		return in, fmt.Errorf("input %q: required: %v", in.Name, err)
	}
	if in.Default, err = scalar(n, "default"); err != nil {
		return in, err
	}
	if c := n.get("choices"); c != nil && !isEmpty(c) {
		if c.kind != ySeq {
			return in, fmt.Errorf("input %q: choices should be a list", in.Name)
		}
		for _, item := range c.items {
			if item.kind != yScalar {
				return in, fmt.Errorf("input %q: each choice should be a single value", in.Name)
			}
			in.Choices = append(in.Choices, item.str)
		}
	}
	if in.Required && in.Default != "" {
		return in, fmt.Errorf("input %q is required, so it takes no default", in.Name)
	}
	if in.Default != "" {
		if _, err := convertInput(in, in.Default); err != nil {
			return in, fmt.Errorf("input %q: default: %v", in.Name, err)
		}
	}
	return in, nil
}

func parseOutput(n *yNode, list string) (Output, error) {
	if n.kind != yMap {
		return Output{}, fmt.Errorf("line %d: each of outputs.%s should be a mapping (name, help), not %s", n.line, list, n.kind)
	}
	if err := onlyKeys(n, "an output", "name", "help"); err != nil {
		return Output{}, err
	}
	var o Output
	var err error
	if o.Name, err = scalar(n, "name"); err != nil {
		return o, err
	}
	o.Help, err = scalar(n, "help")
	return o, err
}

// onlyKeys rejects keys a mapping doesn't take, so typos surface as errors
// rather than being silently dropped.
func onlyKeys(n *yNode, what string, allowed ...string) error {
	for _, k := range n.keys {
		ok := false
		for _, a := range allowed {
			ok = ok || k == a
		}
		if !ok {
			return fmt.Errorf("line %d: %s takes no %q (it takes: %s)", n.line, what, k, strings.Join(allowed, ", "))
		}
	}
	return nil
}

// scalar returns a mapping's single-value key, "" when absent.
func scalar(n *yNode, key string) (string, error) {
	v := n.get(key)
	if v == nil {
		return "", nil
	}
	if v.kind != yScalar {
		return "", fmt.Errorf("line %d: %s should be a single value, not %s", v.line, key, v.kind)
	}
	return v.str, nil
}

func isEmpty(n *yNode) bool { return n.kind == yScalar && n.str == "" }

func typeNames() string {
	return "string, text, int, number, bool or path"
}

// checkRelPath accepts a clean path inside the tool's directory.
func checkRelPath(p string) error {
	if p == "" {
		return fmt.Errorf("no script given")
	}
	if filepath.IsAbs(p) || filepath.Clean(p) != p || p == "." || strings.HasPrefix(p, "../") || p == ".." {
		return fmt.Errorf("%q should be a plain relative path inside the tool's directory", p)
	}
	return nil
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "false", "no", "0", "off":
		return false, nil
	case "true", "yes", "1", "on":
		return true, nil
	}
	return false, fmt.Errorf("%q should be true or false", s)
}

// checkScript checks the entrypoint script is in dir, and runnable for the
// exec runtime.
func (t *Tool) checkScript(dir string) error {
	st, err := os.Stat(filepath.Join(dir, t.Script))
	if err != nil {
		return fmt.Errorf("entrypoint.script %s: %v", t.Script, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("entrypoint.script %s isn't a regular file", t.Script)
	}
	if t.Runtime == "exec" && st.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("entrypoint.script %s isn't executable (runtime exec runs it directly) -- chmod +x it", t.Script)
	}
	return nil
}

// ---- Input values ----

// convertInput checks one value against its input's type and choices,
// returning it as it appears in the stdin JSON.
func convertInput(in Input, v string) (any, error) {
	if len(in.Choices) > 0 {
		ok := false
		for _, c := range in.Choices {
			ok = ok || c == v
		}
		if !ok {
			return nil, fmt.Errorf("%q isn't one of %s", v, strings.Join(in.Choices, ", "))
		}
	}
	switch in.Type {
	case "int":
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q isn't a whole number", v)
		}
		return n, nil
	case "number":
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil, fmt.Errorf("%q isn't a number", v)
		}
		return f, nil
	case "bool":
		return parseBool(v)
	case "string":
		if strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("a string input is one line (use type text for more)")
		}
	case "path":
		return expandHome(strings.TrimSpace(v))
	}
	return v, nil
}

// checkInputs checks given (input name -> value) against t's inputs,
// applying defaults. It returns the typed values (for the stdin JSON; an
// optional input with no value and no default is nil) and the string values
// (for DTT_IN_*; such an input is left out).
func (t *Tool) checkInputs(given map[string]string) (map[string]any, map[string]string, error) {
	for k := range given {
		if t.input(k) == nil {
			return nil, nil, fmt.Errorf("%s takes no input %q (it takes: %s)", t.Name, k, t.inputNames())
		}
	}
	typed := map[string]any{}
	strs := map[string]string{}
	for _, in := range t.Inputs {
		v, ok := given[in.Name]
		if !ok || (in.Type != "text" && strings.TrimSpace(v) == "") || (in.Type == "text" && v == "") {
			if in.Required {
				return nil, nil, fmt.Errorf("%s needs input %q (%s)", t.Name, in.Name, orDash(in.Help))
			}
			v = in.Default
		}
		if v == "" {
			typed[in.Name] = nil
			continue
		}
		x, err := convertInput(in, v)
		if err != nil {
			return nil, nil, fmt.Errorf("input %q: %v", in.Name, err)
		}
		typed[in.Name] = x
		strs[in.Name] = fmt.Sprint(x)
		if s, ok := x.(string); ok {
			strs[in.Name] = s
		}
	}
	return typed, strs, nil
}

func (t *Tool) input(name string) *Input {
	for i := range t.Inputs {
		if t.Inputs[i].Name == name {
			return &t.Inputs[i]
		}
	}
	return nil
}

func (t *Tool) inputNames() string {
	if len(t.Inputs) == 0 {
		return "nothing"
	}
	names := make([]string, len(t.Inputs))
	for i, in := range t.Inputs {
		names[i] = in.Name
	}
	return strings.Join(names, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// expandHome replaces a leading ~ with the home directory.
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("can't expand ~ in %q: %v", p, err)
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}

// ---- JSON views ----

// ToolArg is a ControlOpArg (local-representative/controlops.go) plus what
// the tools tab's runner form needs.
type ToolArg struct {
	Name     string   `json:"name"`
	Help     string   `json:"help"`
	Required bool     `json:"required,omitempty"`
	Default  string   `json:"default,omitempty"`
	Type     string   `json:"type"`
	Choices  []string `json:"choices,omitempty"`
}

// ToolInfo is one T3 as "dtt list --json" and "dtt describe --json" report
// it. Op, Summary, Describe and Args are a ControlOpSpec as they stand.
type ToolInfo struct {
	Op       string    `json:"op"`
	Summary  string    `json:"summary"`
	Describe string    `json:"describe"`
	Args     []ToolArg `json:"args"`

	Name        string   `json:"name"`
	Description string   `json:"description"`
	Values      []Output `json:"values"`
	Files       []Output `json:"files"`
	Runtime     string   `json:"runtime"`
	Script      string   `json:"script"`
	Timeout     string   `json:"timeout"`
	Origin      string   `json:"origin"`
	Path        string   `json:"path"` // its directory; "builtin/<name>" for a built-in
	Source      string   `json:"source,omitempty"`
}

func (t *Tool) info(withSource bool) ToolInfo {
	describe := t.Describe
	if describe == "" {
		describe = "run tool " + t.Name
	}
	info := ToolInfo{
		Op:          toolOpPrefix + t.Name,
		Summary:     strings.Join(strings.Fields(t.Description), " "),
		Describe:    describe,
		Args:        []ToolArg{},
		Name:        t.Name,
		Description: t.Description,
		Values:      append([]Output{}, t.Values...),
		Files:       append([]Output{}, t.Files...),
		Runtime:     t.Runtime,
		Script:      t.Script,
		Timeout:     t.Timeout.String(),
		Origin:      t.Origin,
		Path:        t.Dir,
	}
	if t.Dir == "" {
		info.Path = builtinRoot + "/" + t.Name
	}
	for _, in := range t.Inputs {
		help := in.Help
		if help == "" {
			help = inputTypes[in.Type]
		}
		info.Args = append(info.Args, ToolArg{Name: in.Name, Help: help, Required: in.Required, Default: in.Default, Type: in.Type, Choices: in.Choices})
	}
	if withSource {
		info.Source = t.Source
	}
	return info
}

// stringifyValue renders an output value from the tool's JSON as the string
// a control run saves: strings as they are, everything else as JSON.
func stringifyValue(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}
