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
// object -- a dotted path whose first part is a provider (github, git,
// filesystem, aws, localrep, timestamp, dtt, delay, slopspace), or an alias an earlier statement gave --
// with any of:
//
//	select: which object(s) under the path
//	alias:  a name later statements and ${{name.field}} references use
//	read:   * (or a list of fields) -- accepted, but every object's fields
//	        are measured whatever it says
//	assert: the state the object should have
//
// The same key may start several statements (two asserts on one alias, say).
//
// Reading measures every object and returns an image: the measured state of
// all of them -- every field each one has -- plus what each asserted field
// would do. Asserting makes every asserted change first, then measures every
// selected object again, so its image is the state as the run left it; an
// object the run changed also keeps "measured", its state before. An assert
// changes a field only when what the field holds now is one of its "from"
// values: empty (always), a value listed under from.<field> beside it, the
// value a supplied image saw (--image), or anything at all (--force, as if
// every from were "*"). Any other value is held: left alone and reported.
//
// A list field (a PR's labels, say) is asserted member by member: a member
// to add was absent, so empty, and is always added; a member to remove
// changes from the from rule -- a from value naming that member, a from
// value that is a list equal to the whole list as it is, the image having
// seen the member, or force.
//
// Deleting a document undoes what asserting it would make: each object an
// assert of it could create -- a repository, a PR named by its branch, a
// file, a VPC named by its tags, a subnet -- is asserted exists: false, and
// its other fields are left alone. Removing it is decided by the same from
// rule (the image an assert returned, a from.exists value, or force), and a
// repository still needs approve_delete. What an assert can only change,
// never make (PRs found by a pattern, ECS tasks), is measured and left
// alone. A timestamp the supplied image measured keeps the moment the image
// saw, so deleting with an assert's image finds what that assert named
// (scratch-<date>) on a later day too.
//
// A document can also take variables, as a terraform module does: a
// variables statement declares them, the caller supplies values (dtt read
// --var name=value, or a built-in tool's variables input), and ${{var.name}}
// refers to one. See parseVariables.
//
// A document can call other documents (dtt.calls, declare_dtt.go): each call
// runs its document in this one's mode, as a composition -- a call inside a
// call -- and its image becomes the call object's state. And a document's
// work can come in phases, in one of two ways. YAML document delimiters
// (---) split it into phases: each part between them is one, named by a
// phase: <name> on its statements or else phase-1, phase-2 and so on. Or,
// in a document without them, phase: <name> on a statement starts that
// phase, and the statements after it are in it until another phase starts
// (the statements before the first, variables or a timestamp, are measured
// before any phase begins). Either way the phases run in document order,
// each once everything in the one before it is done, and the image reports
// each one (Image.Phases): its objects, its outcomes and how it ended.
//
// A statement may also name the statements it waits for with after: (a
// path or alias given above, or a list of them), beside those its
// ${{...}} references wait for. Statements run in document order whatever
// it says; it is what the run's plan (progress.go) draws the dependency
// graph from, for a statement that refers to nothing it waits for -- a
// delay, say.

const imageFormat = "dtt-image-v1"

// Image is what reading or asserting a document returns.
type Image struct {
	OK        bool           `json:"ok"`
	Format    string         `json:"format"`
	Mode      string         `json:"mode"` // read, assert or delete
	Force     bool           `json:"force,omitempty"`
	FromImage bool           `json:"from_image,omitempty"` // an earlier image was supplied
	Taken     string         `json:"taken"`
	Objects   []*ImageObject `json:"objects"`
	Fields    []FieldOutcome `json:"fields"`
	Summary   map[string]int `json:"summary"`          // outcome -> how many fields had it
	Phases    []*PhaseImage  `json:"phases,omitempty"` // a document with phase: statements, each phase in order
	Error     string         `json:"error,omitempty"`
}

// PhaseImage is one phase of a document's work: the objects first measured
// in it and the outcomes of its fields, the image of that phase alone.
type PhaseImage struct {
	Name       string         `json:"name"`
	Line       int            `json:"line"`   // the statement that starts it
	Status     string         `json:"status"` // not-run (an earlier phase failed), done or failed
	Started    string         `json:"started,omitempty"`
	DurationMs int64          `json:"duration_ms,omitempty"`
	Objects    []string       `json:"objects"` // the ids of the objects it measured first
	Summary    map[string]int `json:"summary"` // as Image.Summary, for its fields only
	Error      string         `json:"error,omitempty"`

	start time.Time
}

// ImageObject is one DTT object's measured state.
type ImageObject struct {
	ID    string         `json:"id"`   // its path, plus [which one] for a selected or child object
	Path  string         `json:"path"` // the dotted path it was found under
	Alias string         `json:"alias,omitempty"`
	Phase string         `json:"phase,omitempty"` // the phase it was first measured in
	Line  int            `json:"line"`            // the statement (or assert) that named it
	State map[string]any `json:"state"`           // every field, as measured last

	// Measured is the state as first measured, before this run changed it;
	// only for an object whose state the run changed.
	Measured map[string]any `json:"measured,omitempty"`
}

// FieldOutcome is what an assert did, or in read mode would do, to one field.
type FieldOutcome struct {
	Object  string `json:"object"`
	Field   string `json:"field"`
	Outcome string `json:"outcome"` // unchanged, changed, held; would-change in read mode; kept in delete mode
	Why     string `json:"why"`
	Was     any    `json:"was"`
	Want    any    `json:"want"`
	Line    int    `json:"line"`
	Phase   string `json:"phase,omitempty"`
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
	"git":        gitProvider{},
	"aws":        awsProvider{},
	"localrep":   localrepProvider{},
	"dtt":        callsProvider{},
	"delay":      delayProvider{},
	"slopspace":  slopspaceProvider{},
}

// anyToolProviders are the providers every built-in declarative tool reads,
// besides its own.
var anyToolProviders = []string{"timestamp", "dtt", "delay"}

const (
	varsKey   = "variables" // the statement declaring a document's variables
	varsAlias = "var"       // what ${{var.name}} refers to them by
)

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

// dobj is a measured object: its image entry (kept current as it changes),
// a copy of its state as first measured, and the provider's own handle on
// it.
type dobj struct {
	img      *ImageObject
	measured map[string]any
	handle   any
}

// ---- Documents ----

type statement struct {
	line   int
	key    string   // as written
	path   []string // the dotted path, an alias resolved
	alias  string
	read   []string // as written; every field is measured regardless
	sel    *yNode
	assert *yNode
	of     *statement // for a statement naming an alias, the statement that gave it
	prov   dttProvider
	vars   []docVar // for the variables statement, what it declares (and no prov)
	phase  string   // as written: the phase this statement starts (or carries on)
	in     string   // the phase it is in, "" before the first (set by check)
	part   int      // which of the document's ---separated parts it is in, from 0
	after  []string // as written: the paths and aliases it waits for, beside its references
}

// docVar is one variable a document declares.
type docVar struct {
	name, def, help string
	required        bool // declared with no default: the caller has to give it
	line            int
}

func (st *statement) pathString() string { return strings.Join(st.path, ".") }

// parseDocument splits src into statements, numbering the ---separated
// parts they are in (a part with no statements, before a leading --- say,
// isn't counted). A tab in a line's indent counts as four columns, so
// documents typed with tabs read as they look.
func parseDocument(src string) ([]*statement, error) {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var starts, parts []int
	part, inPart := 0, false
	for i, l := range lines {
		l = expandIndentTabs(l)
		if strings.TrimSpace(l) == "---" {
			if inPart {
				part, inPart = part+1, false
			}
			l = ""
		}
		lines[i] = l
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if l[0] != ' ' {
			starts, parts = append(starts, i), append(parts, part)
			inPart = true
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
		st.part = parts[k]
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
	if key == varsKey {
		var err error
		st.vars, err = parseVariables(body, line)
		return st, err
	}
	for _, p := range st.path {
		if p == "" {
			return nil, fmt.Errorf("line %d: %q has an empty part", line, key)
		}
	}
	if body.kind != yMap || len(body.keys) == 0 {
		return nil, fmt.Errorf("line %d: %s needs select, alias, read or assert under it", line, key)
	}
	if err := onlyKeys(body, "a statement", "select", "alias", "read", "assert", "phase", "after"); err != nil {
		return nil, err
	}
	if a := body.get("after"); a != nil {
		items := []*yNode{a}
		if a.kind == ySeq {
			items = a.items
		}
		for _, item := range items {
			if item.kind != yScalar || strings.TrimSpace(item.str) == "" {
				return nil, fmt.Errorf("line %d: after is a path or alias given above, or a list of them", item.line)
			}
			st.after = append(st.after, strings.TrimSpace(item.str))
		}
	}
	var err error
	if st.alias, err = scalar(body, "alias"); err != nil {
		return nil, err
	}
	if st.phase, err = scalar(body, "phase"); err != nil {
		return nil, err
	}
	st.phase = strings.TrimSpace(st.phase)
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

// parseVariables reads a document's variables statement:
//
//	variables:
//	  node:
//	    default: ""           (optional; without one the caller has to give it)
//	    help: the node to ...
//	  greeting: hello         (a single value is the default)
//
// The caller's values (dtt read --var node=x, or a built-in tool's
// variables input) replace the defaults, and ${{var.node}} is the value.
// Every value is text, like everything else in a document.
func parseVariables(body *yNode, line int) ([]docVar, error) {
	if body.kind != yMap || len(body.keys) == 0 {
		return nil, fmt.Errorf("line %d: %s is a mapping of name: {default, help} (or name: default)", line, varsKey)
	}
	vars := []docVar{}
	for i, name := range body.keys {
		n := body.vals[i]
		if !varNameRe.MatchString(name) {
			return nil, fmt.Errorf("line %d: variable %q: names are letters, digits, '_' and '-', starting with a letter", n.line, name)
		}
		v := docVar{name: name, line: n.line}
		switch n.kind {
		case yScalar:
			v.def = n.str
		case yMap:
			if err := onlyKeys(n, "variable "+name, "default", "help"); err != nil {
				return nil, err
			}
			var err error
			if v.help, err = scalar(n, "help"); err != nil {
				return nil, err
			}
			if v.def, err = scalar(n, "default"); err != nil {
				return nil, err
			}
			v.required = n.get("default") == nil
		default:
			return nil, fmt.Errorf("line %d: variable %s is a mapping of default and help, or a single default value", n.line, name)
		}
		if strings.Contains(v.def, "${{") {
			return nil, fmt.Errorf("line %d: variable %s: a default is plain text, with no ${{...}} in it", n.line, name)
		}
		vars = append(vars, v)
	}
	return vars, nil
}

var varNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// parseVarValues reads variable values given as text: a JSON object of
// name: value, or name=value lines (blank lines and # comments skipped).
func parseVarValues(s string) (map[string]string, error) {
	out := map[string]string{}
	t := strings.TrimSpace(s)
	if t == "" {
		return out, nil
	}
	if strings.HasPrefix(t, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(t), &raw); err != nil {
			return nil, fmt.Errorf("variables: %v", err)
		}
		for k, v := range raw {
			if s := strings.TrimSpace(string(v)); s == "null" {
				continue // as readInputFile does: not given, so its default
			} else if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
				return nil, fmt.Errorf("variables: %s should be a single value", k)
			}
			out[k] = stringifyValue(v)
		}
		return out, nil
	}
	for _, l := range strings.Split(t, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("variables: %q should be name=value", l)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

// ---- The engine ----

type engine struct {
	mode  string // read, assert or delete
	force bool
	only  string // a built-in tool's provider: the only one (with timestamp) its documents may use
	prior map[string]*ImageObject
	log   io.Writer
	now   func() time.Time
	sleep func(context.Context, time.Duration) error // a delay's wait (declare_delay.go)

	// vars are the caller's values for the document's variables; docDir is
	// the directory the document was read from ("" for stdin or a tool's
	// document input), which source_file paths are relative to.
	vars   map[string]string
	docDir string

	// inPlace is an in-place call's directory (calls.go): files the
	// document reads must be in it, or among includes, the dtt examples its
	// .dtttc includes. "" for a pathless call.
	inPlace  string
	includes []string

	// depth is how many calls (dtt.calls) this document is inside: 0 for
	// the one dtt was given.
	depth int

	// phases are the document's phases, in order (check finds them), and
	// phase the one running, nil outside them.
	phases []*PhaseImage
	phase  *PhaseImage

	// progress is where the run says what it's working on (progress.go):
	// nil for none, and for a call's document (dtt.calls), whose work is
	// one step of the document calling it.
	progress *progressOut

	gh     *githubClient
	awsRun awsRunner
	lr     *localrepClient
	slop   *slopEnv // dungeon-keeper's slopspaces and work signals (declare_slopspace.go)

	image  *Image
	byID   map[string]*dobj
	byStmt map[*statement][]*dobj
	names  map[string][]*dobj // a statement's path or alias -> its objects, for ${{...}}
}

func newEngine(mode string, force bool, log io.Writer) *engine {
	return &engine{mode: mode, force: force, log: log, now: time.Now, sleep: sleepCtx, awsRun: execAWS}
}

// changes: this run makes the changes it decides on (assert or delete), not
// only reports them (read).
func (e *engine) changes() bool { return e.mode == "assert" || e.mode == "delete" }

// deleting: this run removes what an assert of the document would make.
func (e *engine) deleting() bool { return e.mode == "delete" }

// keep records, in delete mode, an assert left alone because deleting
// doesn't remove what it names.
func (e *engine) keep(o *dobj, line int, why string) {
	e.record(FieldOutcome{Object: o.img.ID, Field: "exists", Outcome: "kept", Why: why, Was: o.img.State["exists"], Want: o.img.State["exists"], Line: line})
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
	if len(e.phases) > 0 {
		e.image.Phases = e.phases
	}
	e.progress.plan(e.mode, doc)
	err = e.run(ctx, doc)
	if err == nil && e.changes() && e.image.Summary["changed"] > 0 {
		err = e.remeasure(ctx, doc)
	}
	for _, o := range e.byID {
		if valueKey(o.measured) != valueKey(o.img.State) {
			o.img.Measured = o.measured
		}
	}
	if err != nil {
		e.image.Error = err.Error()
		return e.image, err
	}
	e.image.OK = true
	return e.image, nil
}

// remeasure measures every selected object again once the asserts are made,
// so the image holds what they left (a repository's PR list with the PRs it
// created, say). A timestamp is a moment, not a thing that changes: it keeps
// its first measure, and a delay the wait it made. A call (dtt.calls)
// measured its own objects again itself, so it isn't called again.
func (e *engine) remeasure(ctx context.Context, doc []*statement) error {
	for _, st := range doc {
		if st.of != nil || st.vars != nil || st.path[0] == "timestamp" || st.path[0] == "delay" || st.path[0] == "dtt" {
			continue
		}
		began, fields := e.now(), len(e.image.Fields)
		e.progress.begin(st, "remeasure")
		_, err := st.prov.resolve(ctx, e, st)
		if err != nil {
			err = fmt.Errorf("line %d: %s: measuring again after the asserts: %w", st.line, st.key, err)
		}
		e.progress.end(e, st, "remeasure", began, fields, err)
		if err != nil {
			return err
		}
	}
	return nil
}

// check checks the whole document before anything is measured.
func (e *engine) check(doc []*statement) error {
	aliases := map[string]*statement{}
	known := map[string]bool{}
	var declared []docVar
	e.phases = nil
	phaseAt := map[string]int{} // a phase -> the line that started it
	partNames, err := partPhases(doc)
	if err != nil {
		return err
	}
	cur := ""
	for _, st := range doc {
		phase := st.phase
		if partNames != nil {
			phase = partNames[st.part]
		}
		if phase != "" && phase != cur {
			switch {
			case !aliasRe.MatchString(phase):
				return fmt.Errorf("line %d: phase %q: phase names are lowercase letters, digits, '-' and '_'", st.line, phase)
			case phaseAt[phase] != 0:
				return fmt.Errorf("line %d: phase %s began on line %d and another has begun since -- a phase's statements go together, as its phases run one after another", st.line, phase, phaseAt[phase])
			}
			phaseAt[phase], cur = st.line, phase
			e.phases = append(e.phases, &PhaseImage{Name: phase, Line: st.line, Status: "not-run", Objects: []string{}, Summary: map[string]int{}})
		}
		st.in = cur
		for _, a := range st.after {
			if !known[a] {
				return fmt.Errorf("line %d: after: %s should be a path or alias given above", st.line, a)
			}
		}
		if st.vars != nil {
			if known[varsAlias] {
				return fmt.Errorf("line %d: a document has one %s statement", st.line, varsKey)
			}
			known[varsAlias] = true
			declared = st.vars
			continue
		}
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
			if e.only != "" && st.path[0] != e.only && !contains(anyToolProviders, st.path[0]) {
				return fmt.Errorf("line %d: this is the %s tool, so it reads %s, %s statements; %s needs the %s tool (or dtt %s)", st.line, e.only, e.only, strings.Join(anyToolProviders, ", "), st.key, st.path[0], e.mode)
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
			case st.alias == varsAlias || st.alias == varsKey:
				return fmt.Errorf("line %d: alias %q is kept for variables (${{var.<name>}})", st.line, st.alias)
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
			if e.inPlace != "" {
				if err := e.checkInPlaceFiles(st.assert); err != nil {
					return err
				}
			}
		}
		if st.of == nil && st.path[0] == "dtt" {
			if err := e.checkCall(st); err != nil {
				return err
			}
		}
	}
	return e.checkVarValues(declared)
}

// partPhases names the phases of a document --- splits into parts: each
// part's phase is the phase: its statements give (all the same one, if
// several give it), or phase-<n> for the nth part. nil for a document of
// one part, whose phases (if any) its phase: statements start.
func partPhases(doc []*statement) ([]string, error) {
	if len(doc) == 0 || doc[len(doc)-1].part == 0 {
		return nil, nil
	}
	names := make([]string, doc[len(doc)-1].part+1)
	named := make([]int, len(names)) // the line that named each part
	for _, st := range doc {
		switch {
		case st.phase == "":
		case names[st.part] == "":
			names[st.part], named[st.part] = st.phase, st.line
		case names[st.part] != st.phase:
			return nil, fmt.Errorf("line %d: phase %s: the part between --- this is in is phase %s (line %d) -- each part is one phase", st.line, st.phase, names[st.part], named[st.part])
		}
	}
	for i := range names {
		if names[i] == "" {
			names[i] = fmt.Sprintf("phase-%d", i+1)
		}
	}
	return names, nil
}

// checkVarValues checks the caller gave only variables the document
// declares, and every one it requires.
func (e *engine) checkVarValues(declared []docVar) error {
	names := map[string]bool{}
	var list []string
	for _, v := range declared {
		names[v.name] = true
		list = append(list, v.name)
	}
	given := make([]string, 0, len(e.vars))
	for k := range e.vars {
		given = append(given, k)
	}
	sort.Strings(given)
	for _, k := range given {
		if !names[k] {
			if len(list) == 0 {
				return fmt.Errorf("variable %s was given, but the document declares no variables", k)
			}
			return fmt.Errorf("variable %s was given, but the document only declares %s", k, strings.Join(list, ", "))
		}
	}
	for _, v := range declared {
		if _, ok := e.vars[v.name]; v.required && !ok {
			return fmt.Errorf("line %d: variable %s has no default, so give it (--var %s=..., or the variables input)", v.line, v.name, v.name)
		}
	}
	return nil
}

// varObject puts the document's variables, the caller's values over the
// defaults, in the image as the object var, for ${{var.<name>}}.
func (e *engine) varObject(st *statement) {
	state := map[string]any{}
	for _, v := range st.vars {
		val, ok := e.vars[v.name]
		if !ok {
			val = v.def
		}
		state[v.name] = val
	}
	o := e.addObject(varsAlias, varsKey, st.line, state, nil)
	o.img.Alias = varsAlias
	e.names[varsAlias] = []*dobj{o}
}

// run measures and asserts the statements in order, phase by phase: a phase
// begins once every statement of the one before it is done.
func (e *engine) run(ctx context.Context, doc []*statement) error {
	next := 0 // the next phase to start, in e.phases
	for _, st := range doc {
		if st.in != "" && (e.phase == nil || e.phase.Name != st.in) {
			e.endPhase(nil)
			e.startPhase(e.phases[next])
			next++
		}
		began, fields := e.now(), len(e.image.Fields)
		e.progress.begin(st, "measure")
		err := e.runStatement(ctx, st)
		e.progress.end(e, st, "measure", began, fields, err)
		if err != nil {
			e.endPhase(err)
			return err
		}
	}
	e.endPhase(nil)
	return nil
}

func (e *engine) startPhase(p *PhaseImage) {
	e.phase, p.start = p, e.now()
	p.Started = p.start.UTC().Format(time.RFC3339)
	if e.log != nil {
		fmt.Fprintf(e.log, "phase %s (line %d)\n", p.Name, p.Line)
	}
}

// endPhase ends the phase running, if one is, as done or (err) failed.
func (e *engine) endPhase(err error) {
	p := e.phase
	if p == nil {
		return
	}
	e.phase = nil
	p.DurationMs = e.now().Sub(p.start).Milliseconds()
	p.Status = "done"
	if err != nil {
		p.Status, p.Error = "failed", err.Error()
	}
	if e.log != nil {
		fmt.Fprintf(e.log, "phase %s %s\n", p.Name, p.Status)
	}
}

func (e *engine) runStatement(ctx context.Context, st *statement) error {
	if st.vars != nil {
		e.varObject(st)
		return nil
	}
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
		return nil
	}
	for _, o := range objs {
		if err := st.prov.assert(ctx, e, o, st.assert); err != nil {
			return fmt.Errorf("line %d: %s: %w", st.line, st.key, err)
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
	o := &dobj{img: &ImageObject{ID: id, Path: path, Line: line, State: state}, measured: copyState(state), handle: handle}
	if e.phase != nil {
		o.img.Phase = e.phase.Name
		e.phase.Objects = append(e.phase.Objects, id)
	}
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
	if !e.changes() {
		f.Outcome = "would-change"
		e.record(f)
		return false
	}
	f.Outcome = "changed"
	e.record(f)
	return true
}

func (e *engine) record(f FieldOutcome) {
	e.fold(f)
	if e.log != nil {
		fmt.Fprintf(e.log, "%-12s %s %s (line %d): %s\n", f.Outcome, f.Object, f.Field, f.Line, f.Why)
	}
}

// fold puts an outcome in the image, and in the phase running, without
// logging it: record's work, and how a call's outcomes reach the image of
// the document calling it (declare_dtt.go), its own log having shown them.
func (e *engine) fold(f FieldOutcome) {
	if e.phase != nil {
		f.Phase = e.phase.Name
		e.phase.Summary[f.Outcome]++
	}
	e.image.Fields = append(e.image.Fields, f)
	e.image.Summary[f.Outcome]++
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
// or alias (or var, for a variable), field a field of its (single) object's
// state. In read mode a field the object doesn't have stays as written.
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
		if v == nil && !e.changes() && name != varsAlias {
			// Reading: the field may be one only an assert fills in (a
			// recording a run would make, say), so it stays as written.
			return m
		}
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

// copyState copies a state deeply, so later changes to it don't reach the
// copy.
func copyState(state map[string]any) map[string]any {
	if state == nil {
		return nil
	}
	return copyValue(state).(map[string]any)
}

func copyValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, v := range x {
			m[k] = copyValue(v)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = copyValue(x[i])
		}
		return out
	case *omap:
		m := &omap{keys: append([]string(nil), x.keys...)}
		for _, v := range x.vals {
			m.vals = append(m.vals, copyValue(v))
		}
		return m
	}
	return v
}

// getPath walks state along path, nil when something on the way is missing.
// A part name[member] that isn't a key itself is a member of the list name:
// member when the list holds it, nil when it doesn't.
func getPath(state map[string]any, path []string) any {
	var cur any = state
	for _, p := range path {
		v, ok := lookupKey(cur, p)
		if !ok {
			name, member, isMember := splitMember(p)
			if !isMember {
				return nil
			}
			if list, ok := lookupKey(cur, name); ok && listHas(list, member) {
				v = member
			}
		}
		cur = v
	}
	return cur
}

func lookupKey(cur any, key string) (any, bool) {
	switch m := cur.(type) {
	case map[string]any:
		v, ok := m[key]
		return v, ok
	case *omap:
		for i, k := range m.keys {
			if k == key {
				return m.vals[i], true
			}
		}
	}
	return nil, false
}

// splitMember splits "labels[needs-review]" into labels and needs-review.
func splitMember(p string) (name, member string, ok bool) {
	i := strings.IndexByte(p, '[')
	if i <= 0 || !strings.HasSuffix(p, "]") {
		return "", "", false
	}
	return p[:i], p[i+1 : len(p)-1], true
}

func listHas(list any, member string) bool {
	for _, s := range stringList(list) {
		if s == member {
			return true
		}
	}
	return false
}

// stringList is a measured list of names ([]any of strings, as read back
// from an image, or []string), sorted; nil for anything else.
func stringList(v any) []string {
	var out []string
	switch x := v.(type) {
	case []string:
		out = append(out, x...)
	case []any:
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	default:
		return nil
	}
	sort.Strings(out)
	return out
}

// setSpec is an asserted list field (labels, reviewers): either the whole
// list (exact: has is all of it) or members it has and lacks, others left
// alone.
type setSpec struct {
	name       string
	exact      bool
	has, lacks []string
	from       []any // strings name members; lists are the whole list
	line       int
}

// checkSetField checks a list field's shape: a list (the whole list), one
// value (a list of one), or a mapping of has and lacks lists.
func checkSetField(f fieldSpec) error {
	if f.want.kind != yMap {
		return nil
	}
	if err := onlyKeys(f.want, f.name, "has", "lacks"); err != nil {
		return err
	}
	for i, v := range f.want.vals {
		if v.kind == yMap {
			return fmt.Errorf("line %d: %s.%s is a list of names (or one name)", v.line, f.name, f.want.keys[i])
		}
	}
	return nil
}

// set evaluates a list field checked by checkSetField.
func (e *engine) set(f fieldSpec) (setSpec, error) {
	s := setSpec{name: f.name, line: f.line}
	names := func(n *yNode) ([]string, error) {
		var nodes []*yNode
		switch {
		case n == nil || isEmpty(n):
		case n.kind == ySeq:
			nodes = n.items
		default:
			nodes = []*yNode{n}
		}
		var out []string
		for _, item := range nodes {
			v, err := e.text(item, f.name)
			if err != nil {
				return nil, err
			}
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out, nil
	}
	var err error
	if f.want.kind == yMap {
		if s.has, err = names(f.want.get("has")); err != nil {
			return s, err
		}
		if s.lacks, err = names(f.want.get("lacks")); err != nil {
			return s, err
		}
	} else {
		s.exact = true
		if s.has, err = names(f.want); err != nil {
			return s, err
		}
	}
	for _, n := range f.from {
		v, err := e.value(n)
		if err != nil {
			return s, err
		}
		s.from = append(s.from, v)
	}
	return s, nil
}

// decideSet applies the from rule to each member of list field s of o, whose
// members are have now. It returns the members to add and to remove (none in
// read mode).
func (e *engine) decideSet(o *dobj, s setSpec, have []string) (add, remove []string) {
	present := map[string]bool{}
	for _, m := range have {
		present[m] = true
	}
	want := map[string]bool{}
	for _, m := range s.has {
		want[m] = true
	}
	drop := map[string]bool{}
	for _, m := range s.lacks {
		if !want[m] {
			drop[m] = true
		}
	}
	if s.exact {
		for _, m := range have {
			if !want[m] {
				drop[m] = true
			}
		}
	}
	// A from value that is a list is the whole list: when the list is as
	// it says, every member may change.
	whole := false
	var memberFrom []any
	for _, v := range s.from {
		if l, ok := normalize(v).([]any); ok {
			if valueKey(stringList(l)) == valueKey(stringList(have)) {
				whole = true
			}
			continue
		}
		memberFrom = append(memberFrom, v)
	}
	members := make([]string, 0, len(want)+len(drop))
	for m := range want {
		members = append(members, m)
	}
	for m := range drop {
		members = append(members, m)
	}
	sort.Strings(members)
	for _, m := range members {
		var was, to any
		if present[m] {
			was = m
		}
		if want[m] {
			to = m
		}
		from := memberFrom
		if whole {
			from = append(append([]any(nil), memberFrom...), m)
		}
		if e.decide(o, []string{s.name + "[" + m + "]"}, s.line, to, was, from) {
			if want[m] {
				add = append(add, m)
			} else {
				remove = append(remove, m)
			}
		}
	}
	return add, remove
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

// ---- CLI: dtt read / dtt assert / dtt delete ----

// declare runs a document given as a file, a directory holding .dtttc (an
// in-place call) or - (stdin), prints its image and keeps it with the call
// (calls.go).
func (c *cli) declare(ctx context.Context, cfg config, mode string, args []string) int {
	fs := newFlags(mode)
	imagePath := fs.String("image", "", "an earlier image (JSON file, - for stdin)")
	force := fs.Bool("force", false, "change asserted fields whatever they hold")
	vars := varList{}
	fs.Var(vars, "var", "a value for one of the document's variables, name=value")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	if len(pos) != 1 {
		return c.fail(exitBadInput, fmt.Errorf("usage: dtt %s <document|dir|-> [--var name=value]... [--image <file>] [--force]", mode))
	}
	call, err := openDocCall(pos[0], c.stdin, cfg.pathlessDir)
	if err != nil {
		return c.fail(exitBadInput, trimBadInput(err))
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
	e.vars = vars
	if call.path != "" {
		e.docDir = filepath.Dir(call.path)
	}
	e.inPlace, e.includes = call.dir, call.includes
	img, err := e.runSource(ctx, call.src, prior)
	if err != nil && errors.Is(err, errBadInput) {
		return c.fail(exitBadInput, trimBadInput(err))
	}
	c.emit(img) // the image is the result, with or without --json
	c.keepCall(cfg, call, img, e.now())
	if err != nil {
		if !c.json {
			fmt.Fprintln(c.stderr, "dtt:", err)
		}
		return exitToolFailed
	}
	return exitOK
}

// varList is a repeatable --var name=value.
type varList map[string]string

func (v varList) String() string { return "" }

func (v varList) Set(s string) error {
	k, val, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(k) == "" {
		return fmt.Errorf("--var %q should be name=value", s)
	}
	v[strings.TrimSpace(k)] = val
	return nil
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

// allProvidersTool is the built-in tool that reads every provider's
// statements together (builtin/declare).
const allProvidersTool = "declare"

// toolEngine is the script behind the built-in github, git, filesystem,
// aws, localrep and declare tools (builtin/<name>/run.sh): it reads the run's
// inputs as JSON on stdin, the way dtt run passes them, and writes
// image.json and $DTT_OUTPUT. Each provider's tool reads only that
// provider's statements (and timestamp's); declare reads every provider's.
// The image is kept with the call as well (calls.go): document_file's call
// (in-place when its directory holds .dtttc), or for an inline document the
// pathless call named document.
func (c *cli) toolEngine(ctx context.Context, cfg config, args []string) int {
	if len(args) != 1 || (args[0] != allProvidersTool && (dttProviders[args[0]] == nil || contains(anyToolProviders, args[0]))) {
		return c.fail(exitBadInput, fmt.Errorf("usage: dtt tool-engine github|git|filesystem|aws|localrep|%s (the built-in tools run this)", allProvidersTool))
	}
	var in struct {
		Document     *string `json:"document"`
		DocumentFile *string `json:"document_file"`
		Mode         *string `json:"mode"`
		FromImage    *string `json:"from_image"`
		Force        bool    `json:"force"`
		Variables    *string `json:"variables"`
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
	var call *docCall
	switch {
	case in.Document != nil && in.DocumentFile != nil:
		return c.fail(exitBadInput, fmt.Errorf("give document or document_file, not both"))
	case in.Document != nil:
		call = pathlessCall(*in.Document, inlineDocName, cfg.pathlessDir)
	case in.DocumentFile != nil:
		var err error
		if call, err = openDocFile(callerPath(*in.DocumentFile), cfg.pathlessDir); err != nil {
			return c.fail(exitBadInput, trimBadInput(err))
		}
	default:
		return c.fail(exitBadInput, fmt.Errorf("give the document (document) or its file (document_file)"))
	}
	mode := "read"
	if in.Mode != nil {
		mode = *in.Mode
	}
	if mode != "read" && mode != "assert" && mode != "delete" {
		return c.fail(exitBadInput, fmt.Errorf("mode %q should be read, assert or delete", mode))
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
	if args[0] != allProvidersTool {
		e.only = args[0]
	}
	// dtt run hands the tool a pipe to say how its work goes (progress.go).
	e.progress = progressFromEnv()
	if call.path != "" {
		e.docDir = filepath.Dir(call.path)
	}
	e.inPlace, e.includes = call.dir, call.includes
	if in.Variables != nil {
		var err error
		if e.vars, err = parseVarValues(*in.Variables); err != nil {
			return c.fail(exitBadInput, err)
		}
	}
	img, err := e.runSource(ctx, call.src, prior)
	if err != nil && errors.Is(err, errBadInput) {
		return c.fail(exitBadInput, trimBadInput(err))
	}
	c.keepCall(cfg, call, img, e.now())
	data, _ := json.MarshalIndent(img, "", "  ")
	if werr := os.WriteFile(imageFileName, append(data, '\n'), 0o644); werr != nil && err == nil {
		err = werr
	}
	// The image comes back as a value too (image_json), so a caller -- the
	// tools tab's runner -- has it without reading the file. It is written
	// even when the run failed part way: the image so far is what was
	// measured, and dtt run reads a failed tool's outputs as well.
	imgJSON, _ := json.Marshal(img)
	out, _ := json.Marshal(map[string]any{
		"values": map[string]any{
			"changed":      img.Summary["changed"],
			"held":         img.Summary["held"],
			"unchanged":    img.Summary["unchanged"],
			"would_change": img.Summary["would-change"],
			"objects":      len(img.Objects),
			"image_json":   string(imgJSON),
		},
		"files": map[string]string{"image": imageFileName},
	})
	if p := os.Getenv("DTT_OUTPUT"); p != "" {
		if werr := os.WriteFile(p, out, 0o644); werr != nil && err == nil {
			err = werr
		}
	}
	if err != nil {
		fmt.Fprintln(c.stderr, "dtt:", err)
		return exitToolFailed
	}
	return exitOK
}
