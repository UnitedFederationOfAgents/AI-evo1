package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file is dtt's declarative side: DTT documents, read and asserted
// (README.md "Declarative tools").
//
// A document is a list of statements. Each is a top-level key naming a DTT
// object -- a dotted path whose first part is a provider (github,
// filesystem, aws, timestamp), or an alias an earlier statement gave --
// with any of:
//
//	select: which object(s) under the path
//	alias:  a name later statements and ${{name.field}} references use
//	read:   * (or a list of fields): measure the object's extra fields too
//	assert: the state the object should have
//
// The same key may start several statements (two asserts on one alias, say).
//
// Reading measures every object and returns an image: the measured state of
// all of them, plus what each asserted field would do. Asserting also makes
// the changes, and returns the image as it left things. An assert changes a
// field only when what the field holds now is one of its "from" values:
// empty (always), a value listed under from.<field> beside it, the value a
// supplied image saw (--image), or anything at all (--force, as if every
// from were "*"). Any other value is held: left alone and reported.

const imageFormat = "dtt-image-v1"

// Image is what reading or asserting a document returns.
type Image struct {
	OK        bool           `json:"ok"`
	Format    string         `json:"format"`
	Mode      string         `json:"mode"` // read or assert
	Force     bool           `json:"force,omitempty"`
	FromImage bool           `json:"from_image,omitempty"` // an earlier image was supplied
	Taken     string         `json:"taken"`
	Objects   []*ImageObject `json:"objects"`
	Fields    []FieldOutcome `json:"fields"`
	Summary   map[string]int `json:"summary"` // outcome -> how many fields had it
	Error     string         `json:"error,omitempty"`
}

// ImageObject is one DTT object's measured state.
type ImageObject struct {
	ID    string         `json:"id"`   // its path, plus [which one] for a selected or child object
	Path  string         `json:"path"` // the dotted path it was found under
	Alias string         `json:"alias,omitempty"`
	Line  int            `json:"line"` // the statement (or assert) that named it
	State map[string]any `json:"state"`
}

// FieldOutcome is what an assert did, or in read mode would do, to one field.
type FieldOutcome struct {
	Object  string `json:"object"`
	Field   string `json:"field"`
	Outcome string `json:"outcome"` // unchanged, changed, held; would-change in read mode
	Why     string `json:"why"`
	Was     any    `json:"was"`
	Want    any    `json:"want"`
	Line    int    `json:"line"`
}

var (
	aliasRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	refRe   = regexp.MustCompile(`\$\{\{\s*([^}]*?)\s*\}\}`)
)

// dttProviders are the first parts of a DTT path.
var dttProviders = map[string]dttProvider{
	"timestamp":  timestampProvider{},
	"filesystem": fsProvider{},
	"github":     githubProvider{},
	"aws":        awsProvider{},
}

func providerNames() string {
	names := make([]string, 0, len(dttProviders))
	for n := range dttProviders {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// dttProvider measures and changes one kind of DTT object. check* run
// before anything is measured, so a document with a mistake anywhere in it
// changes nothing.
type dttProvider interface {
	checkSelect(st *statement) error              // the path, select and read of a statement that names a path
	checkAssert(path []string, body *yNode) error // an assert's shape
	resolve(ctx context.Context, e *engine, st *statement) ([]*dobj, error)
	assert(ctx context.Context, e *engine, o *dobj, body *yNode) error
}

// dobj is a measured object: its image entry (kept current as it changes)
// and the provider's own handle on it.
type dobj struct {
	img    *ImageObject
	handle any
}

// ---- Documents ----

type statement struct {
	line   int
	key    string   // as written
	path   []string // the dotted path, an alias resolved
	alias  string
	read   []string // nil: no read; ["*"]: every field
	sel    *yNode
	assert *yNode
	of     *statement // for a statement naming an alias, the statement that gave it
	prov   dttProvider
}

// reads reports whether the statement asked for field to be read.
func (st *statement) reads(field string) bool {
	for _, r := range st.read {
		if r == "*" || r == field {
			return true
		}
	}
	return false
}

func (st *statement) pathString() string { return strings.Join(st.path, ".") }

// parseDocument splits src into statements. A tab in a line's indent counts
// as four columns, so documents typed with tabs read as they look.
func parseDocument(src string) ([]*statement, error) {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var starts []int
	for i, l := range lines {
		l = expandIndentTabs(l)
		if strings.TrimSpace(l) == "---" {
			l = ""
		}
		lines[i] = l
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if l[0] != ' ' {
			starts = append(starts, i)
		}
	}
	var doc []*statement
	for k, s := range starts {
		end := len(lines)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		// Leading newlines keep yamlite's line numbers the document's.
		n, err := parseYAML(strings.Repeat("\n", s) + strings.Join(lines[s:end], "\n"))
		if err != nil {
			return nil, err
		}
		if n.kind != yMap || len(n.keys) != 1 {
			return nil, fmt.Errorf("line %d: a statement is one key (a DTT path or an alias) with select/alias/read/assert under it", s+1)
		}
		st, err := parseStatement(n.keys[0], n.vals[0], s+1)
		if err != nil {
			return nil, err
		}
		doc = append(doc, st)
	}
	if len(doc) == 0 {
		return nil, fmt.Errorf("the document has no statements")
	}
	return doc, nil
}

func expandIndentTabs(l string) string {
	col, i := 0, 0
	for ; i < len(l) && (l[i] == ' ' || l[i] == '\t'); i++ {
		if l[i] == '\t' {
			col += 4 - col%4
		} else {
			col++
		}
	}
	return strings.Repeat(" ", col) + l[i:]
}

func parseStatement(key string, body *yNode, line int) (*statement, error) {
	st := &statement{line: line, key: key, path: strings.Split(key, ".")}
	for _, p := range st.path {
		if p == "" {
			return nil, fmt.Errorf("line %d: %q has an empty part", line, key)
		}
	}
	if body.kind != yMap || len(body.keys) == 0 {
		return nil, fmt.Errorf("line %d: %s needs select, alias, read or assert under it", line, key)
	}
	if err := onlyKeys(body, "a statement", "select", "alias", "read", "assert"); err != nil {
		return nil, err
	}
	var err error
	if st.alias, err = scalar(body, "alias"); err != nil {
		return nil, err
	}
	st.sel = body.get("select")
	st.assert = body.get("assert")
	if r := body.get("read"); r != nil {
		switch r.kind {
		case yScalar:
			st.read = []string{r.str}
		case ySeq:
			for _, item := range r.items {
				if item.kind != yScalar {
					return nil, fmt.Errorf("line %d: read lists field names", item.line)
				}
				st.read = append(st.read, item.str)
			}
		default:
			return nil, fmt.Errorf("line %d: read is * or a list of fields", r.line)
		}
	}
	return st, nil
}

// ---- The engine ----

type engine struct {
	mode  string // read or assert
	force bool
	only  string // a built-in tool's provider: the only one (with timestamp) its documents may use
	prior map[string]*ImageObject
	log   io.Writer
	now   func() time.Time

	gh     *githubClient
	awsRun awsRunner

	image  *Image
	byID   map[string]*dobj
	byStmt map[*statement][]*dobj
	names  map[string][]*dobj // a statement's path or alias -> its objects, for ${{...}}
}

func newEngine(mode string, force bool, log io.Writer) *engine {
	return &engine{mode: mode, force: force, log: log, now: time.Now, awsRun: execAWS}
}

func badInputf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errBadInput, fmt.Sprintf(format, a...))
}

// trimBadInput drops errBadInput's prefix from a message meant for people.
func trimBadInput(err error) error {
	return errors.New(strings.TrimPrefix(err.Error(), errBadInput.Error()+": "))
}

// runSource reads or asserts the document src. prior is an earlier image or
// nil. A document that doesn't parse or check comes back as an error
// wrapping errBadInput, before anything is measured; one that fails part way
// comes back with the image so far.
func (e *engine) runSource(ctx context.Context, src string, prior *Image) (*Image, error) {
	t := e.now()
	e.image = &Image{Format: imageFormat, Mode: e.mode, Force: e.force, FromImage: prior != nil,
		Taken: t.UTC().Format(time.RFC3339), Objects: []*ImageObject{}, Fields: []FieldOutcome{}, Summary: map[string]int{}}
	e.byID, e.byStmt, e.names = map[string]*dobj{}, map[*statement][]*dobj{}, map[string][]*dobj{}
	if prior != nil {
		e.prior = map[string]*ImageObject{}
		for _, o := range prior.Objects {
			e.prior[o.ID] = o
		}
	}
	doc, err := parseDocument(src)
	if err != nil {
		return nil, badInputf("%v", err)
	}
	if err := e.check(doc); err != nil {
		return nil, badInputf("%v", err)
	}
	if err := e.run(ctx, doc); err != nil {
		e.image.Error = err.Error()
		return e.image, err
	}
	e.image.OK = true
	return e.image, nil
}

// check checks the whole document before anything is measured.
func (e *engine) check(doc []*statement) error {
	aliases := map[string]*statement{}
	known := map[string]bool{}
	for _, st := range doc {
		if origin, ok := aliases[st.key]; ok {
			if st.sel != nil || st.alias != "" || st.read != nil {
				return fmt.Errorf("line %d: %s is an alias (given on line %d); select, alias and read go on that statement", st.line, st.key, origin.line)
			}
			st.of, st.path, st.prov = origin, origin.path, origin.prov
		} else {
			p, ok := dttProviders[st.path[0]]
			if !ok {
				return fmt.Errorf("line %d: %s is neither a DTT path (%s) nor an alias given above", st.line, st.key, providerNames())
			}
			if e.only != "" && st.path[0] != e.only && st.path[0] != "timestamp" {
				return fmt.Errorf("line %d: this is the %s tool, so it reads %s and timestamp statements; %s needs the %s tool (or dtt %s)", st.line, e.only, e.only, st.key, st.path[0], e.mode)
			}
			st.prov = p
			if err := p.checkSelect(st); err != nil {
				return fmt.Errorf("line %d: %s: %v", st.line, st.key, err)
			}
			if err := checkRefs(st.sel, known); err != nil {
				return err
			}
		}
		if st.alias != "" {
			switch {
			case !aliasRe.MatchString(st.alias):
				return fmt.Errorf("line %d: alias %q: aliases are lowercase letters, digits, '-' and '_'", st.line, st.alias)
			case dttProviders[st.alias] != nil:
				return fmt.Errorf("line %d: alias %q is a provider's name", st.line, st.alias)
			case aliases[st.alias] != nil:
				return fmt.Errorf("line %d: alias %q was already given on line %d", st.line, st.alias, aliases[st.alias].line)
			}
			aliases[st.alias] = st
			known[st.alias] = true
		}
		known[st.key] = true
		if st.of == nil {
			known[st.pathString()] = true
		}
		if st.assert != nil {
			if err := st.prov.checkAssert(st.path, st.assert); err != nil {
				return fmt.Errorf("line %d: %s: %v", st.line, st.key, err)
			}
			if err := checkRefs(st.assert, known); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *engine) run(ctx context.Context, doc []*statement) error {
	for _, st := range doc {
		objs, ok := e.byStmt[st.of]
		if !ok {
			var err error
			if objs, err = st.prov.resolve(ctx, e, st); err != nil {
				return fmt.Errorf("line %d: %s: %w", st.line, st.key, err)
			}
			e.byStmt[st] = objs
			e.names[st.pathString()] = objs
			if st.alias != "" {
				e.names[st.alias] = objs
				for _, o := range objs {
					o.img.Alias = st.alias
				}
			}
		}
		if st.assert == nil {
			continue
		}
		for _, o := range objs {
			if err := st.prov.assert(ctx, e, o, st.assert); err != nil {
				return fmt.Errorf("line %d: %s: %w", st.line, st.key, err)
			}
		}
	}
	return nil
}

// addObject puts an object in the image (replacing the state of one already
// there under id) and returns it.
func (e *engine) addObject(id, path string, line int, state map[string]any, handle any) *dobj {
	if o, ok := e.byID[id]; ok {
		o.img.State, o.handle = state, handle
		return o
	}
	o := &dobj{img: &ImageObject{ID: id, Path: path, Line: line, State: state}, handle: handle}
	e.byID[id] = o
	e.image.Objects = append(e.image.Objects, o.img)
	return o
}

// decide reports whether field (a path into o's state) should change from
// have to want, and records the outcome. It is the "from" rule: a field
// changes only from empty, from one of its from values, from what the
// supplied image saw, or (force) from anything. In read mode nothing
// changes; the outcome says what an assert would do.
func (e *engine) decide(o *dobj, field []string, line int, want, have any, from []any) bool {
	f := FieldOutcome{Object: o.img.ID, Field: strings.Join(field, "."), Was: have, Want: want, Line: line}
	hk := valueKey(have)
	why := ""
	switch {
	case valueKey(want) == hk:
		f.Outcome, f.Why = "unchanged", "it already holds that"
		e.record(f)
		return false
	case e.force:
		why = "force"
	case isZero(have):
		why = "it was empty"
	default:
		for _, v := range from {
			if s, ok := v.(string); ok && strings.TrimSpace(s) == "*" {
				why = "from *"
				break
			}
			if valueKey(v) == hk {
				why = "it held one of its from values"
				break
			}
		}
		if why == "" && e.prior != nil {
			if p, ok := e.prior[o.img.ID]; ok && valueKey(getPath(p.State, field)) == hk {
				why = "it was as the image saw it"
			}
		}
	}
	if why == "" {
		f.Outcome = "held"
		f.Why = "it holds something else (not empty, not a from value"
		if e.prior != nil {
			f.Why += ", not as the image saw it"
		}
		f.Why += ") -- assert with an image of it, a from value or force to change it"
		e.record(f)
		return false
	}
	f.Why = why
	if e.mode != "assert" {
		f.Outcome = "would-change"
		e.record(f)
		return false
	}
	f.Outcome = "changed"
	e.record(f)
	return true
}

func (e *engine) record(f FieldOutcome) {
	e.image.Fields = append(e.image.Fields, f)
	e.image.Summary[f.Outcome]++
	if e.log != nil {
		fmt.Fprintf(e.log, "%-12s %s %s (line %d): %s\n", f.Outcome, f.Object, f.Field, f.Line, f.Why)
	}
}

// ---- Values ----

// omap is a mapping value, in document order.
type omap struct {
	keys []string
	vals []any
}

func (m *omap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(m.vals[i])
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// value converts a document node to a value, ${{...}} references filled in.
// Every scalar is a string (yamlite reads them all as strings).
func (e *engine) value(n *yNode) (any, error) {
	switch n.kind {
	case ySeq:
		out := []any{}
		for _, item := range n.items {
			v, err := e.value(item)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yMap:
		m := &omap{}
		for i, k := range n.keys {
			v, err := e.value(n.vals[i])
			if err != nil {
				return nil, err
			}
			m.keys, m.vals = append(m.keys, k), append(m.vals, v)
		}
		return m, nil
	}
	return e.interpolate(n.str, n.line)
}

// text is value for a node that must be a single value.
func (e *engine) text(n *yNode, what string) (string, error) {
	if n.kind != yScalar {
		return "", fmt.Errorf("line %d: %s should be a single value, not %s", n.line, what, n.kind)
	}
	return e.interpolate(n.str, n.line)
}

func (e *engine) values(ns []*yNode) ([]any, error) {
	var out []any
	for _, n := range ns {
		v, err := e.value(n)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// interpolate replaces each ${{name.field}} in s: name is a statement's path
// or alias, field a field of its (single) object's state.
func (e *engine) interpolate(s string, line int) (string, error) {
	var err error
	out := refRe.ReplaceAllStringFunc(s, func(m string) string {
		expr := refRe.FindStringSubmatch(m)[1]
		name, field, ok := splitRef(expr, func(n string) bool { _, ok := e.names[n]; return ok })
		if !ok {
			err = fmt.Errorf("line %d: ${{%s}} names nothing measured yet", line, expr)
			return m
		}
		objs := e.names[name]
		if len(objs) != 1 {
			err = fmt.Errorf("line %d: ${{%s}}: %s is %d objects, not one", line, expr, name, len(objs))
			return m
		}
		v := getPath(objs[0].img.State, strings.Split(field, "."))
		if v == nil {
			err = fmt.Errorf("line %d: ${{%s}}: %s has no %s", line, expr, name, field)
			return m
		}
		if str, ok := v.(string); ok {
			return str
		}
		b, _ := json.Marshal(v)
		return string(b)
	})
	return out, err
}

// splitRef splits name.field at the longest name known accepts.
func splitRef(expr string, known func(string) bool) (name, field string, ok bool) {
	parts := strings.Split(expr, ".")
	for i := len(parts) - 1; i >= 1; i-- {
		if n := strings.Join(parts[:i], "."); known(n) {
			return n, strings.Join(parts[i:], "."), true
		}
	}
	return "", "", false
}

// checkRefs checks every ${{...}} under n names something known.
func checkRefs(n *yNode, known map[string]bool) error {
	if n == nil {
		return nil
	}
	switch n.kind {
	case yScalar:
		for _, m := range refRe.FindAllStringSubmatch(n.str, -1) {
			if _, _, ok := splitRef(m[1], func(s string) bool { return known[s] }); !ok {
				return fmt.Errorf("line %d: ${{%s}} should be a path or alias given above, then a field (${{timestamp.unix}}, say)", n.line, m[1])
			}
		}
	case yMap:
		for _, v := range n.vals {
			if err := checkRefs(v, known); err != nil {
				return err
			}
		}
	case ySeq:
		for _, v := range n.items {
			if err := checkRefs(v, known); err != nil {
				return err
			}
		}
	}
	return nil
}

// normalize turns omaps into plain maps, for comparing.
func normalize(v any) any {
	switch x := v.(type) {
	case *omap:
		m := map[string]any{}
		for i, k := range x.keys {
			m[k] = normalize(x.vals[i])
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalize(x[i])
		}
		return out
	case map[string]any:
		m := map[string]any{}
		for k, v := range x {
			m[k] = normalize(v)
		}
		return m
	}
	return v
}

// valueKey is what two values are compared by: text with its line endings
// and trailing whitespace evened out, anything else as canonical JSON.
func valueKey(v any) string {
	switch x := normalize(v).(type) {
	case nil:
		return ""
	case string:
		return strings.TrimRight(strings.ReplaceAll(x, "\r\n", "\n"), " \t\n")
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// isZero: missing, blank, false or an empty mapping or list.
func isZero(v any) bool {
	switch valueKey(v) {
	case "", "false", "{}", "[]", "null":
		return true
	}
	return false
}

// getPath walks state along path, nil when something on the way is missing.
func getPath(state map[string]any, path []string) any {
	var cur any = state
	for _, p := range path {
		switch m := cur.(type) {
		case map[string]any:
			cur = m[p]
		case *omap:
			cur = nil
			for i, k := range m.keys {
				if k == p {
					cur = m.vals[i]
				}
			}
		default:
			return nil
		}
	}
	return cur
}

// fieldSpec is one asserted field: the value it should hold and the from
// values (besides empty) it may be changed from.
type fieldSpec struct {
	name string
	want *yNode
	from []*yNode
	line int
}

// assertFields splits a mapping of field: value into fields and the
// from.<field> values beside them. allowed limits the field names (none:
// any name). A from value is one value, a list, or a mapping whose keys only
// label its values.
func assertFields(n *yNode, what string, allowed ...string) ([]fieldSpec, error) {
	if n == nil || isEmpty(n) {
		return nil, nil
	}
	if n.kind != yMap {
		return nil, fmt.Errorf("line %d: %s should be a mapping of field: value, not %s", n.line, what, n.kind)
	}
	var out []fieldSpec
	for i, k := range n.keys {
		if strings.HasPrefix(k, "from.") {
			continue
		}
		if len(allowed) > 0 && !contains(allowed, k) {
			return nil, fmt.Errorf("line %d: %s takes no %q (it takes: %s, each with an optional from.<field> beside it)", n.vals[i].line, what, k, strings.Join(allowed, ", "))
		}
		out = append(out, fieldSpec{name: k, want: n.vals[i], line: n.vals[i].line})
	}
	for i, k := range n.keys {
		name, ok := strings.CutPrefix(k, "from.")
		if !ok {
			continue
		}
		j := -1
		for x := range out {
			if out[x].name == name {
				j = x
			}
		}
		if j < 0 {
			return nil, fmt.Errorf("line %d: %s has no %s beside it to be changed from", n.vals[i].line, k, name)
		}
		switch v := n.vals[i]; v.kind {
		case yScalar:
			out[j].from = append(out[j].from, v)
		case ySeq:
			out[j].from = append(out[j].from, v.items...)
		case yMap:
			out[j].from = append(out[j].from, v.vals...)
		}
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- CLI: dtt read / dtt assert ----

func (c *cli) declare(ctx context.Context, mode string, args []string) int {
	fs := newFlags(mode)
	imagePath := fs.String("image", "", "an earlier image (JSON file, - for stdin)")
	force := fs.Bool("force", false, "change asserted fields whatever they hold")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	if len(pos) != 1 {
		return c.fail(exitBadInput, fmt.Errorf("usage: dtt %s <document|-> [--image <file>] [--force]", mode))
	}
	src, err := readFileOrStdin(pos[0], c.stdin)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	var prior *Image
	if *imagePath != "" {
		if *imagePath == "-" && pos[0] == "-" {
			return c.fail(exitBadInput, fmt.Errorf("the document and the image can't both come from stdin"))
		}
		data, err := readFileOrStdin(*imagePath, c.stdin)
		if err != nil {
			return c.fail(exitBadInput, err)
		}
		if prior, err = parseImage(data); err != nil {
			return c.fail(exitBadInput, fmt.Errorf("--image %s: %v", *imagePath, err))
		}
	}
	e := newEngine(mode, *force, c.stderr)
	img, err := e.runSource(ctx, src, prior)
	if err != nil && errors.Is(err, errBadInput) {
		return c.fail(exitBadInput, trimBadInput(err))
	}
	c.emit(img) // the image is the result, with or without --json
	if err != nil {
		if !c.json {
			fmt.Fprintln(c.stderr, "dtt:", err)
		}
		return exitToolFailed
	}
	return exitOK
}

func readFileOrStdin(p string, stdin io.Reader) (string, error) {
	var data []byte
	var err error
	if p == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(p)
	}
	return string(data), err
}

func parseImage(data string) (*Image, error) {
	var img Image
	if err := json.Unmarshal([]byte(data), &img); err != nil {
		return nil, fmt.Errorf("not an image: %v", err)
	}
	if img.Format != imageFormat {
		return nil, fmt.Errorf("format %q isn't %s", img.Format, imageFormat)
	}
	return &img, nil
}

// ---- The built-in tools: dtt tool-engine <provider> ----

// imageFileName is where a built-in declarative tool writes its image, in
// the run's working directory.
const imageFileName = "image.json"

// toolEngine is the script behind the built-in github, filesystem and aws
// tools (builtin/<name>/run.sh): it reads the run's inputs as JSON on
// stdin, the way dtt run passes them, and writes image.json and
// $DTT_OUTPUT.
func (c *cli) toolEngine(ctx context.Context, args []string) int {
	if len(args) != 1 || dttProviders[args[0]] == nil || args[0] == "timestamp" {
		return c.fail(exitBadInput, fmt.Errorf("usage: dtt tool-engine github|filesystem|aws (the built-in tools run this)"))
	}
	var in struct {
		Document     *string `json:"document"`
		DocumentFile *string `json:"document_file"`
		Mode         *string `json:"mode"`
		FromImage    *string `json:"from_image"`
		Force        bool    `json:"force"`
	}
	if err := json.NewDecoder(c.stdin).Decode(&in); err != nil {
		return c.fail(exitBadInput, fmt.Errorf("reading the run's inputs: %v", err))
	}
	callerPath := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(os.Getenv("DTT_CALLER_DIR"), p)
	}
	var src string
	switch {
	case in.Document != nil && in.DocumentFile != nil:
		return c.fail(exitBadInput, fmt.Errorf("give document or document_file, not both"))
	case in.Document != nil:
		src = *in.Document
	case in.DocumentFile != nil:
		data, err := os.ReadFile(callerPath(*in.DocumentFile))
		if err != nil {
			return c.fail(exitBadInput, err)
		}
		src = string(data)
	default:
		return c.fail(exitBadInput, fmt.Errorf("give the document (document) or its file (document_file)"))
	}
	mode := "read"
	if in.Mode != nil {
		mode = *in.Mode
	}
	if mode != "read" && mode != "assert" {
		return c.fail(exitBadInput, fmt.Errorf("mode %q should be read or assert", mode))
	}
	var prior *Image
	if in.FromImage != nil {
		data := *in.FromImage
		if !strings.HasPrefix(strings.TrimSpace(data), "{") {
			b, err := os.ReadFile(callerPath(strings.TrimSpace(data)))
			if err != nil {
				return c.fail(exitBadInput, fmt.Errorf("from_image: %v", err))
			}
			data = string(b)
		}
		var err error
		if prior, err = parseImage(data); err != nil {
			return c.fail(exitBadInput, fmt.Errorf("from_image: %v", err))
		}
	}

	e := newEngine(mode, in.Force, c.stderr)
	e.only = args[0]
	img, err := e.runSource(ctx, src, prior)
	if err != nil && errors.Is(err, errBadInput) {
		return c.fail(exitBadInput, trimBadInput(err))
	}
	data, _ := json.MarshalIndent(img, "", "  ")
	if werr := os.WriteFile(imageFileName, append(data, '\n'), 0o644); werr != nil && err == nil {
		err = werr
	}
	if err != nil {
		fmt.Fprintln(c.stderr, "dtt:", err)
		return exitToolFailed
	}
	out, _ := json.Marshal(map[string]any{
		"values": map[string]any{
			"changed":      img.Summary["changed"],
			"held":         img.Summary["held"],
			"unchanged":    img.Summary["unchanged"],
			"would_change": img.Summary["would-change"],
			"objects":      len(img.Objects),
		},
		"files": map[string]string{"image": imageFileName},
	})
	if p := os.Getenv("DTT_OUTPUT"); p != "" {
		if err := os.WriteFile(p, out, 0o644); err != nil {
			fmt.Fprintln(c.stderr, "dtt:", err)
			return exitToolFailed
		}
	}
	return exitOK
}
