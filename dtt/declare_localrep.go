package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// This file is the localrep provider (see declare.go): what
// local-representative and the sub-apps it runs on this host can make,
// through LR's HTTP API (condocs/dTT-IntegrationImpls/Step1Prompt.md,
// Revision V):
//
//	localrep.control.sequences:       a control sequence in LR's library
//	  select:
//	    id: <sequence id>
//	    node: <node>                  (optional: another node's LR, through
//	                                   agent-coordinator; empty for this one)
//	  assert:
//	    source_file: <file>           (lr-control-v1 YAML defining the sequence:
//	                                   relative to the document, or one of
//	                                   dtt's examples/compositions/ files; or
//	                                   source: | ...)
//	    from.source_file: "*"         (optional: the sequence is the document's
//	                                   own -- replace whatever definition it
//	                                   holds; or from.source)
//	    exists: true                  (false deletes it; so does delete mode)
//
//	localrep.control.runs:            a run of a sequence, named by key
//	  select:
//	    sequence: <sequence id>
//	    key: <a name for this run>    (asserting runs it once per key)
//	    node: <node>                  (as above: the run is that node's,
//	                                   on its robot and screen)
//	  assert:
//	    controls: {name: value}       (what the run starts with)
//	    record: <node|none>           (optional: the runner's record override)
//	    wait: 600                     (seconds to wait for it to finish)
//
//	localrep.polly.clips:             a Polly clip in the files tab, by name
//	  select:
//	    name: <name>                  (saved as polly-<name>.mp3)
//	  assert:
//	    text: ...                     (what it says, when it's made; The
//	                                   Conversationalist fills in [[config]],
//	                                   [[voice]], [[engine]], [[region]],
//	                                   [[price]], [[characters]] and [[cost]])
//	    config: <name>                (optional: The Conversationalist's Polly
//	                                   configuration to make it with; its
//	                                   active one by default)
//	    play: true                    (optional: when it's made, also play it
//	                                   out loud on the node, after any clip
//	                                   still playing -- in a TC tab open in a
//	                                   browser there; not played, it's still
//	                                   made, and the log says why)
//	    exists: true                  (false deletes it; so does delete mode)
//
//	localrep.compositions:           a composition in composer
//	  select:
//	    id: <composition id>
//	  assert:
//	    composition: |                (composer's JSON: name, width, height,
//	      {...}                        video and audio tracks, and output:
//	                                   "audio" for an audio-only render)
//	    rendered: true                (rendered since it last changed: a WebM
//	                                   in the files tab -- a .weba for audio)
//	    renderer: auto                (auto, ffmpeg or browser, when rendering)
//	    wait: 600                     (seconds to wait for the render)
//	    exists: true                  (false deletes it; so does delete mode)
//
//	localrep.coordinator:             the agent-coordinator this LR is
//	  select:                         connected to (read only)
//	    url: <url>                    (optional: use this URL instead of the
//	                                   one found; empty to find it)
//
//	localrep.files:                   a file in a file cache, by name
//	  select:
//	    name: story.md                (its name there)
//	    cache: preferred              (optional: the preferred file cache,
//	                                   the only one so far -- this host's own,
//	                                   its LR's files tab)
//	  assert:
//	    copy_of: /tmp/x/story.md      (a file on this host, uploaded as it
//	                                   is when asserted; or content: | ...)
//	    exists: true                  (false deletes it; so does delete mode)
//
// A file in the cache is the newest there with its name: its file_id,
// size, files_state (cached, held or persisted), uploaded_at, expires_at
// (a cached file is swept an hour after it's uploaded, unless it's held or
// persisted in the files tab), sha256, and content when it's text no
// bigger than lrMaxText. Asserting uploads it when there's none, and again
// when what copy_of or content holds differs from it (sha256, by the from
// rule), deleting the copy it replaces. copy_of is a file the document
// works on, as a filesystem object is, not from-file content: an in-place
// call may name one anywhere. Reading, a copy_of that isn't there yet is
// what an assert would make, so it isn't compared.
//
// A run exists once one with its key has succeeded: asserting starts it
// when none has, and waits for it; a run that failed is tried again by the
// next assert. A run is an event, so deleting leaves it (kept). Its state
// is the newest attempt's: status, recordings (each {who, file_id, name,
// video}, and the steps it spans and when it started), steps (each {label,
// status, message, started_at, duration_ms}), recording (the first video,
// for ${{run.recording.file_id}}), by_label (each labelled recording
// block's video, by its label, for ${{run.by_label.<label>.file_id}}) and
// output. LR keeps finished runs in
// memory only, so after LR restarts a key is new again.
//
// The coordinator's url is how another node's browser reaches
// agent-coordinator's web UI: http://<the host this LR dialled>:<AC's
// HTTP port>, with this host's LAN address in place of a loopback one
// (Revision Y). It's measured on this node's LR, never another's.
//
// A sequence's definition is LR's fingerprint of it and its actions, so
// asserting a source changes the library only when the source defines
// something else (and, as ever, only from empty, an image or force -- or
// from.source_file "*", or a from value naming the old definition). A
// clip's text and a run's controls are what it is made with, not fields
// measured afterwards: name the clip or the run after what makes it
// differ (a date, a node) to get a new one. A composition is compared on
// the fields the document gives, so composer's defaults don't count as a
// difference.
//
// LR is $DTT_LOCALREP_URL (LR sets it for the dtt runs it starts), else
// http://localhost:8081. Polly goes through LR's /convo/ proxy to The
// Conversationalist, composer through its /compose/ proxy.
type localrepProvider struct{}

const (
	lrSequences    = "localrep.control.sequences"
	lrRuns         = "localrep.control.runs"
	lrClips        = "localrep.polly.clips"
	lrCompositions = "localrep.compositions"
	lrCoordinator  = "localrep.coordinator"
	lrFiles        = "localrep.files"

	// lrPreferredCache is the file cache a localrep.files statement names:
	// the preferred one, which so far is always this host's own, its LR's
	// files tab.
	lrPreferredCache = "preferred"

	lrDefaultWait = 600             // seconds a run or a render is waited for
	lrMaxWait     = 55 * 60         // inside the localrep and declare tools' 60m timeout
	lrPoll        = 30              // seconds one wait for a run or render asks for
	lrMaxBody     = 8 << 20         // an answer read from LR
	lrHTTPTimeout = 2 * time.Minute // one request, a long poll included
	lrMaxUpload   = 64 << 20        // one upload to the files tab (LR's own limit, local-representative/files.go)
	lrMaxText     = 256 << 10       // a cached file's content kept in its state
)

var (
	lrIDRe          = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	lrCompositionRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	lrClipNameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
	lrFileNameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// lrSelectKeys are each path's select keys, the required ones first.
var lrSelectKeys = map[string]struct{ required, optional []string }{
	lrSequences:    {[]string{"id"}, []string{"node"}},
	lrRuns:         {[]string{"sequence", "key"}, []string{"node"}},
	lrClips:        {[]string{"name"}, nil},
	lrCompositions: {[]string{"id"}, nil},
	lrCoordinator:  {nil, []string{"url"}},
	lrFiles:        {[]string{"name"}, []string{"cache"}},
}

func (localrepProvider) checkSelect(st *statement) error {
	keys, ok := lrSelectKeys[st.pathString()]
	if !ok {
		return fmt.Errorf("localrep paths are %s, %s, %s, %s, %s and %s", lrSequences, lrRuns, lrClips, lrCompositions, lrCoordinator, lrFiles)
	}
	if st.sel == nil && len(keys.required) == 0 {
		return nil
	}
	if st.sel == nil || st.sel.kind != yMap {
		return fmt.Errorf("select it by %s", strings.Join(keys.required, " and "))
	}
	if err := onlyKeys(st.sel, st.key+"'s select", append(append([]string{}, keys.required...), keys.optional...)...); err != nil {
		return err
	}
	for _, k := range keys.required {
		v, err := scalar(st.sel, k)
		if err != nil {
			return err
		}
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("select.%s is needed", k)
		}
	}
	for _, k := range keys.optional {
		if _, err := scalar(st.sel, k); err != nil {
			return err
		}
	}
	if st.pathString() == lrFiles {
		if c, _ := scalar(st.sel, "cache"); !strings.Contains(c, "${{") && strings.TrimSpace(c) != "" && strings.TrimSpace(c) != lrPreferredCache {
			return fmt.Errorf("select.cache %q: the file cache is %s, this host's own (the only one so far)", c, lrPreferredCache)
		}
	}
	return nil
}

// lrAssertFields are each path's assert fields.
var lrAssertFields = map[string][]string{
	lrSequences:    {"exists", "source", "source_file"},
	lrRuns:         {"exists", "controls", "record", "wait"},
	lrClips:        {"config", "exists", "play", "text"},
	lrCompositions: {"exists", "composition", "rendered", "renderer", "wait"},
	lrFiles:        {"content", "copy_of", "exists"},
}

func (localrepProvider) checkAssert(p []string, body *yNode) error {
	where := strings.Join(p, ".")
	if where == lrCoordinator {
		return fmt.Errorf("line %d: %s is only read -- it has nothing to assert", body.line, lrCoordinator)
	}
	fields, err := assertFields(body, where+"'s assert", lrAssertFields[where]...)
	if err != nil {
		return err
	}
	has := map[string]*fieldSpec{}
	for i := range fields {
		f := &fields[i]
		has[f.name] = f
		if f.name == "controls" {
			if f.want.kind != yMap {
				return fmt.Errorf("line %d: controls is a mapping of control: value", f.line)
			}
			for j, v := range f.want.vals {
				if v.kind != yScalar {
					return fmt.Errorf("line %d: control %s should be a single value", v.line, f.want.keys[j])
				}
			}
		} else if f.want.kind != yScalar {
			return fmt.Errorf("line %d: %s should be a single value (a | block for text)", f.line, f.name)
		}
		if f.from != nil && f.name != "exists" && !(where == lrSequences && (f.name == "source" || f.name == "source_file")) {
			return fmt.Errorf("line %d: %s takes no from.%s -- it changes from empty, from what an image saw, or with force", f.line, f.name, f.name)
		}
		if f.name == "wait" && !strings.Contains(f.want.str, "${{") {
			if n, err := strconv.Atoi(strings.TrimSpace(f.want.str)); err != nil || n <= 0 {
				return fmt.Errorf("line %d: wait is a number of seconds", f.line)
			}
		}
		if f.name == "renderer" && !strings.Contains(f.want.str, "${{") {
			if r := strings.TrimSpace(f.want.str); r != "auto" && r != "ffmpeg" && r != "browser" {
				return fmt.Errorf("line %d: renderer is auto, ffmpeg or browser", f.line)
			}
		}
		if f.name == "play" && !strings.Contains(f.want.str, "${{") {
			if _, err := parseBool(f.want.str); err != nil {
				return fmt.Errorf("line %d: play is true or false", f.line)
			}
		}
	}
	switch where {
	case lrSequences:
		if has["source"] != nil && has["source_file"] != nil {
			return fmt.Errorf("line %d: give source or source_file, not both", body.line)
		}
	case lrFiles:
		if has["content"] != nil && has["copy_of"] != nil {
			return fmt.Errorf("line %d: give content or copy_of, not both", body.line)
		}
		if f := has["copy_of"]; f != nil && !strings.Contains(f.want.str, "${{") && !filepath.IsAbs(strings.TrimSpace(f.want.str)) {
			return fmt.Errorf("line %d: copy_of is an absolute path on this host", f.line)
		}
	case lrRuns:
		if f := has["exists"]; f != nil && !strings.Contains(f.want.str, "${{") {
			if b, err := parseBool(f.want.str); err != nil || !b {
				return fmt.Errorf("line %d: a run can only be asserted to exist -- one that has happened can't be undone", f.line)
			}
		}
	}
	return nil
}

func (localrepProvider) resolve(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	sel := map[string]string{}
	if st.sel != nil {
		for i, k := range st.sel.keys {
			v, err := e.text(st.sel.vals[i], "select."+k)
			if err != nil {
				return nil, err
			}
			sel[k] = strings.TrimSpace(v)
		}
	}
	lr := e.localrep()
	switch st.pathString() {
	case lrCoordinator:
		state, err := lr.measureCoordinator(ctx, sel["url"])
		if err != nil {
			return nil, err
		}
		return []*dobj{e.addObject(lrCoordinator, lrCoordinator, st.line, state, nil)}, nil
	case lrSequences:
		if !lrIDRe.MatchString(sel["id"]) {
			return nil, fmt.Errorf("select.id %q: sequence ids are lower-case letters, digits, '.', '_' and '-'", sel["id"])
		}
		h := &lrSequence{node: sel["node"], id: sel["id"]}
		state, err := lr.measureSequence(ctx, h)
		if err != nil {
			return nil, err
		}
		return []*dobj{e.addObject(lrObjectID(lrSequences, h.id, h.node), lrSequences, st.line, state, h)}, nil
	case lrRuns:
		h := &lrRun{node: sel["node"], sequence: sel["sequence"], key: sel["key"]}
		state, _, err := lr.measureRun(ctx, h)
		if err != nil {
			return nil, err
		}
		return []*dobj{e.addObject(lrObjectID(lrRuns, h.key, h.node), lrRuns, st.line, state, h)}, nil
	case lrClips:
		if !lrClipNameRe.MatchString(sel["name"]) {
			return nil, fmt.Errorf("select.name %q: a clip's name is lower-case letters, digits, '.', '_' and '-'", sel["name"])
		}
		state, err := lr.measureClip(ctx, sel["name"])
		if err != nil {
			return nil, err
		}
		return []*dobj{e.addObject(lrObjectID(lrClips, sel["name"], ""), lrClips, st.line, state, sel["name"])}, nil
	case lrFiles:
		h := &lrCacheFile{cache: sel["cache"], name: sel["name"]}
		if h.cache == "" {
			h.cache = lrPreferredCache
		}
		if h.cache != lrPreferredCache {
			return nil, fmt.Errorf("select.cache %q: the file cache is %s, this host's own (the only one so far)", h.cache, lrPreferredCache)
		}
		if !lrFileNameRe.MatchString(h.name) {
			return nil, fmt.Errorf("select.name %q: a cached file's name is letters, digits, '.', '_' and '-'", h.name)
		}
		state, err := lr.measureCacheFile(ctx, h)
		if err != nil {
			return nil, err
		}
		return []*dobj{e.addObject(lrObjectID(lrFiles, h.name, ""), lrFiles, st.line, state, h)}, nil
	default: // lrCompositions
		if !lrCompositionRe.MatchString(sel["id"]) {
			return nil, fmt.Errorf("select.id %q: composition ids are lower-case letters, digits and '-'", sel["id"])
		}
		state, err := lr.measureComposition(ctx, sel["id"])
		if err != nil {
			return nil, err
		}
		return []*dobj{e.addObject(lrObjectID(lrCompositions, sel["id"], ""), lrCompositions, st.line, state, sel["id"])}, nil
	}
}

// lrObjectID names an object in the image: path[name], or path[name@node]
// on another node.
func lrObjectID(p, name, node string) string {
	if node != "" {
		name += "@" + node
	}
	return p + "[" + name + "]"
}

func (localrepProvider) assert(ctx context.Context, e *engine, o *dobj, body *yNode) error {
	fields, err := assertFields(body, "assert", lrAssertFields[o.img.Path]...)
	if err != nil {
		return err
	}
	f := lrFields{}
	for i := range fields {
		f[fields[i].name] = &fields[i]
	}
	switch o.img.Path {
	case lrSequences:
		return e.assertSequence(ctx, o, f, body.line)
	case lrRuns:
		return e.assertRun(ctx, o, f, body.line)
	case lrClips:
		return e.assertClip(ctx, o, f, body.line)
	case lrFiles:
		return e.assertCacheFile(ctx, o, f, body.line)
	default:
		return e.assertComposition(ctx, o, f, body.line)
	}
}

// lrFields are an assert's fields by name.
type lrFields map[string]*fieldSpec

func (f lrFields) text(e *engine, name string) (string, bool, error) {
	s := f[name]
	if s == nil {
		return "", false, nil
	}
	v, err := e.text(s.want, name)
	return v, true, err
}

func (f lrFields) seconds(e *engine) (int, error) {
	s, ok, err := f.text(e, "wait")
	if err != nil || !ok {
		return lrDefaultWait, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("line %d: wait %q is a number of seconds", f["wait"].line, s)
	}
	if n > lrMaxWait {
		n = lrMaxWait
	}
	return n, nil
}

// decideExists applies the from rule to exists: wantExists (true unless
// the assert says false, or this is delete mode) against what o holds. It
// reports what o should be and whether that changes.
func (e *engine) decideExists(o *dobj, f lrFields, line int) (want, change bool, err error) {
	want = true
	explicit := false
	var from []any
	if s := f["exists"]; s != nil {
		v, err := e.text(s.want, "exists")
		if err != nil {
			return false, false, err
		}
		if want, err = parseBool(v); err != nil {
			return false, false, fmt.Errorf("line %d: exists: %v", s.line, err)
		}
		if from, err = e.values(s.from); err != nil {
			return false, false, err
		}
		explicit, line = true, s.line
	}
	if e.deleting() {
		want, explicit = false, true
	}
	have := o.img.State["exists"] == true
	if !explicit && have {
		return want, false, nil
	}
	return want, e.decide(o, []string{"exists"}, line, want, have, from), nil
}

// ---- Sequences ----

type lrSequence struct{ node, id string }

func (c *localrepClient) measureSequence(ctx context.Context, h *lrSequence) (map[string]any, error) {
	var ans struct {
		Node     string `json:"node"`
		Sequence *struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Controls    []string `json:"controls"`
			Steps       int      `json:"steps"`
			Recordings  int      `json:"recordings"`
			Definition  string   `json:"definition"`
			Rev         int      `json:"rev"`
			Error       string   `json:"error"`
		} `json:"sequence"`
	}
	if _, err := c.call(ctx, "GET", "/api/control/sequences/"+url.PathEscape(h.id), lrNode(h.node), nil, "", &ans); err != nil {
		return nil, err
	}
	state := map[string]any{"id": h.id, "node": ans.Node, "exists": ans.Sequence != nil}
	if q := ans.Sequence; q != nil {
		controls := []any{}
		for _, name := range q.Controls {
			controls = append(controls, name)
		}
		state["name"], state["description"], state["controls"] = q.Name, q.Description, controls
		state["steps"], state["recordings"], state["definition"], state["rev"] = q.Steps, q.Recordings, q.Definition, q.Rev
		if q.Error != "" {
			state["error"] = q.Error
		}
	}
	return state, nil
}

func (e *engine) assertSequence(ctx context.Context, o *dobj, f lrFields, line int) error {
	h, lr := o.handle.(*lrSequence), e.localrep()
	wantExists, changeExists, err := e.decideExists(o, f, line)
	if err != nil {
		return err
	}
	if !wantExists {
		if changeExists {
			if _, err := lr.call(ctx, "DELETE", "/api/control/sequences/"+url.PathEscape(h.id), lrNode(h.node), nil, "", nil); err != nil {
				return err
			}
			o.img.State = map[string]any{"id": h.id, "node": o.img.State["node"], "exists": false}
		}
		return nil
	}

	src, srcLine, err := e.sequenceSource(f)
	if err != nil {
		return err
	}
	if srcLine > 0 {
		line = srcLine
	}
	if src == "" {
		if o.img.State["exists"] != true {
			return fmt.Errorf("line %d: sequence %s isn't in the library there -- give its source (or source_file) to import it", line, h.id)
		}
		return nil
	}
	var check struct {
		Sequences []struct {
			ID         string `json:"id"`
			Definition string `json:"definition"`
		} `json:"sequences"`
	}
	q := lrNode(h.node)
	q.Set("check", "1")
	if _, err := lr.call(ctx, "POST", "/api/control/import", q, []byte(src), "application/yaml", &check); err != nil {
		return fmt.Errorf("line %d: the source: %v", line, err)
	}
	want, defined := "", []string{}
	for _, s := range check.Sequences {
		defined = append(defined, s.ID)
		if s.ID == h.id {
			want = s.Definition
		}
	}
	if want == "" {
		return fmt.Errorf("line %d: the source doesn't define sequence %s (it defines: %s)", line, h.id, strings.Join(defined, ", "))
	}
	var from []any
	for _, name := range []string{"source", "source_file"} {
		if s := f[name]; s != nil {
			if from, err = e.values(s.from); err != nil {
				return err
			}
		}
	}
	write := changeExists
	if o.img.State["exists"] == true {
		write = e.decide(o, []string{"definition"}, line, want, o.img.State["definition"], from)
	}
	if !write {
		return nil
	}
	if _, err := lr.call(ctx, "POST", "/api/control/import", lrNode(h.node), []byte(src), "application/yaml", nil); err != nil {
		return fmt.Errorf("line %d: importing: %v", line, err)
	}
	state, err := lr.measureSequence(ctx, h)
	if err != nil {
		return err
	}
	o.img.State = state
	return nil
}

// sequenceSource is the assert's source, or source_file's content, and the
// line it came from; "" when it gives neither.
func (e *engine) sequenceSource(f lrFields) (string, int, error) {
	if s := f["source"]; s != nil {
		v, err := e.text(s.want, "source")
		return v, s.line, err
	}
	s := f["source_file"]
	if s == nil {
		return "", 0, nil
	}
	p, err := e.text(s.want, "source_file")
	if err != nil {
		return "", s.line, err
	}
	data, err := e.readDocFile(strings.TrimSpace(p))
	if err != nil {
		return "", s.line, fmt.Errorf("line %d: source_file: %w", s.line, err) // %w: an in-place call's docError stays bad input
	}
	return data, s.line, nil
}

// readDocFile reads a file a document names: an absolute path as it is; a
// relative one beside the document, else where dtt was called from, else
// among dtt's own examples/compositions/ (so an example imported into a
// runner as text still finds its resources/). An in-place call's document
// reads only from the call's directory and the dtt examples its .dtttc
// includes (calls.go), with no fallback.
func (e *engine) readDocFile(p string) (string, error) {
	data, _, err := e.findDocFile(p)
	return data, err
}

// findDocFile is readDocFile, also giving the directory the file was found
// in: "" for one of dtt's own examples, whose files are found as
// readDocFile finds them.
func (e *engine) findDocFile(p string) (string, string, error) {
	if p == "" {
		return "", "", errors.New("no file named")
	}
	if e.inPlace != "" {
		if emb, ok := e.included(p); ok {
			data, err := examplesFS.ReadFile(emb)
			return string(data), "", err
		}
		full, err := e.inPlacePath(p)
		if err != nil {
			return "", "", err
		}
		data, err := os.ReadFile(full)
		return string(data), filepath.Dir(full), err
	}
	if filepath.IsAbs(p) {
		data, err := os.ReadFile(p)
		return string(data), filepath.Dir(p), err
	}
	var tried []string
	for _, dir := range []string{e.docDir, os.Getenv("DTT_CALLER_DIR")} {
		if dir == "" {
			continue
		}
		full := filepath.Join(dir, p)
		data, err := os.ReadFile(full)
		if err == nil {
			return string(data), filepath.Dir(full), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}
		tried = append(tried, full)
	}
	clean := path.Clean(filepath.ToSlash(p))
	if !strings.HasPrefix(clean, "../") && clean != ".." {
		// examples/compositions/x, compositions/x and x are all dtt's
		// examples/compositions/x.
		clean = strings.TrimPrefix(strings.TrimPrefix(clean, examplesRoot+"/"), strings.TrimPrefix(compositionsRoot, examplesRoot+"/")+"/")
		if data, err := examplesFS.ReadFile(path.Join(compositionsRoot, clean)); err == nil {
			return string(data), "", nil
		}
		tried = append(tried, "dtt's "+path.Join(compositionsRoot, clean))
	}
	return "", "", fmt.Errorf("%s isn't there (tried %s)", p, strings.Join(tried, ", "))
}

// ---- Runs ----

type lrRun struct{ node, sequence, key string }

// lrRunJSON is the part of LR's ControlRunMsg (local-representative/control.go) read here.
type lrRunJSON struct {
	ID         string            `json:"id"`
	Sequence   string            `json:"sequence"`
	Key        string            `json:"key"`
	Controls   map[string]string `json:"controls"`
	Status     string            `json:"status"`
	Error      string            `json:"error"`
	StartedAt  int64             `json:"started_at"`
	DurationMs int64             `json:"duration_ms"`
	Recordings []struct {
		Who        string `json:"who"`
		FileID     string `json:"file_id"`
		Name       string `json:"name"`
		Video      bool   `json:"video"`
		Image      bool   `json:"image"`
		File       bool   `json:"file"`
		Label      string `json:"label"`
		From       int    `json:"from"`
		To         int    `json:"to"`
		StartedAt  int64  `json:"started_at"`
		DurationMs int64  `json:"duration_ms"`
	} `json:"recordings"`
	// Steps are how each step went, with when it started (unix ms on the
	// run's LR, as the recordings' started_at), so a demo record can line
	// the steps up with a recording's frames.
	Steps []struct {
		Label      string `json:"label"`
		Status     string `json:"status"`
		Message    string `json:"message"`
		StartedAt  int64  `json:"started_at"`
		DurationMs int64  `json:"duration_ms"`
	} `json:"steps"`
	Output []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"output"`
}

// measureRun finds the runs with h's key. Its state is the newest's, and
// exists says whether any succeeded; the newest is returned too.
func (c *localrepClient) measureRun(ctx context.Context, h *lrRun) (map[string]any, *lrRunJSON, error) {
	q := lrNode(h.node)
	q.Set("key", h.key)
	var ans struct {
		Runs []lrRunJSON `json:"runs"`
	}
	if _, err := c.call(ctx, "GET", "/api/control/runs", q, nil, "", &ans); err != nil {
		return nil, nil, err
	}
	var newest *lrRunJSON
	succeeded := false
	for i := range ans.Runs {
		r := &ans.Runs[i]
		if r.Sequence != h.sequence {
			continue
		}
		newest = r
		succeeded = succeeded || r.Status == "success"
	}
	return runState(h, newest, succeeded), newest, nil
}

func runState(h *lrRun, r *lrRunJSON, succeeded bool) map[string]any {
	state := map[string]any{"sequence": h.sequence, "key": h.key, "node": h.node, "exists": succeeded, "status": "none"}
	if r == nil {
		return state
	}
	recs := []any{}
	var first map[string]any
	byLabel := map[string]any{}
	for _, rec := range r.Recordings {
		m := map[string]any{"who": rec.Who, "file_id": rec.FileID, "name": rec.Name, "video": rec.Video}
		if rec.Label != "" {
			m["label"] = rec.Label
		}
		if rec.From > 0 {
			m["from"], m["to"] = rec.From, rec.To
		}
		if rec.StartedAt > 0 {
			m["started_at"], m["duration_ms"] = rec.StartedAt, rec.DurationMs
		}
		recs = append(recs, m)
		if first == nil && rec.Video {
			first = map[string]any{"file_id": rec.FileID, "name": rec.Name, "who": rec.Who}
		}
		if _, seen := byLabel[rec.Label]; rec.Video && rec.Label != "" && !seen {
			byLabel[rec.Label] = map[string]any{"file_id": rec.FileID, "name": rec.Name, "who": rec.Who}
		}
	}
	state["by_label"] = byLabel
	output := map[string]any{}
	for _, v := range r.Output {
		output[v.Label] = v.Value
	}
	controls := map[string]any{}
	for k, v := range r.Controls {
		controls[k] = v
	}
	state["status"], state["run_id"], state["controls"] = r.Status, r.ID, controls
	state["started_at"], state["duration_ms"] = r.StartedAt, r.DurationMs
	state["recordings"], state["output"] = recs, output
	if len(r.Steps) > 0 {
		steps := []any{}
		for _, st := range r.Steps {
			m := map[string]any{"label": st.Label, "status": st.Status}
			if st.Message != "" {
				m["message"] = st.Message
			}
			if st.StartedAt > 0 {
				m["started_at"], m["duration_ms"] = st.StartedAt, st.DurationMs
			}
			steps = append(steps, m)
		}
		state["steps"] = steps
	}
	if first != nil {
		state["recording"] = first
	}
	if r.Error != "" {
		state["error"] = r.Error
	}
	return state
}

func (e *engine) assertRun(ctx context.Context, o *dobj, f lrFields, line int) error {
	h, lr := o.handle.(*lrRun), e.localrep()
	if e.deleting() {
		e.keep(o, line, "a run has happened; deleting leaves it")
		return nil
	}
	wait, err := f.seconds(e)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)

	// One with this key already under way (started by an earlier assert that
	// gave up waiting, say): wait for it rather than starting another.
	if o.img.State["status"] == "running" && e.changes() {
		if err := e.waitRun(ctx, o, h, o.img.State["run_id"].(string), deadline); err != nil {
			return err
		}
	}
	_, change, err := e.decideExists(o, f, line)
	if err != nil || !change {
		return err
	}
	controls := map[string]string{}
	if s := f["controls"]; s != nil {
		for i, k := range s.want.keys {
			v, err := e.text(s.want.vals[i], "controls."+k)
			if err != nil {
				return err
			}
			controls[k] = v
		}
	}
	record, _, err := f.text(e, "record")
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"sequence": h.sequence, "key": h.key, "controls": controls, "record_override": strings.TrimSpace(record)})
	var started struct {
		Run lrRunJSON `json:"run"`
	}
	if _, err := lr.call(ctx, "POST", "/api/control/runs", lrNode(h.node), body, "application/json", &started); err != nil {
		return fmt.Errorf("line %d: starting %s: %v", line, h.sequence, err)
	}
	if e.log != nil {
		fmt.Fprintf(e.log, "run %s of %s started (key %s)\n", started.Run.ID, h.sequence, h.key)
	}
	return e.waitRun(ctx, o, h, started.Run.ID, deadline)
}

// How long waitRun waits out a run's node being unreachable, and how often
// it asks again meanwhile (vars so tests can shorten them).
var (
	lrLostGrace  = 5 * time.Minute
	lrRetryEvery = 10 * time.Second
)

// lrUnreachable says whether status is agent-coordinator answering for a
// node it can't reach right now (its local-representative's link dropped
// and is reconnecting) -- the run itself goes on there.
func lrUnreachable(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// waitRun waits for run id to finish, then measures o again; a run that
// didn't succeed is an error, with its state in the image. The node going
// unreachable for a while (up to lrLostGrace) is waited out: the run goes
// on there regardless, and failing on the first 502 left a take's run
// orphaned (condocs/dTT-IntegrationImpls/Step2Prompt.md, Revision C).
func (e *engine) waitRun(ctx context.Context, o *dobj, h *lrRun, id string, deadline time.Time) error {
	lr := e.localrep()
	q := lrNode(h.node)
	var lostSince time.Time
	for {
		left := int(time.Until(deadline).Seconds())
		if left <= 0 {
			return fmt.Errorf("run %s of %s didn't finish in time -- it goes on in LR's control tab; assert again (with a longer wait) to wait for it", id, h.sequence)
		}
		if left > lrPoll {
			left = lrPoll
		}
		q.Set("wait", strconv.Itoa(left))
		var ans struct {
			Run lrRunJSON `json:"run"`
		}
		if status, err := lr.call(ctx, "GET", "/api/control/runs/"+url.PathEscape(id), q, nil, "", &ans); err != nil {
			if !lrUnreachable(status) {
				return err
			}
			if lostSince.IsZero() {
				lostSince = time.Now()
				if e.log != nil {
					fmt.Fprintf(e.log, "run %s: %v -- waiting up to %s for it to be reachable again\n", id, err, lrLostGrace)
				}
			} else if time.Since(lostSince) > lrLostGrace {
				return fmt.Errorf("%v (unreachable for over %s; run %s may still be going on in its node's control tab)", err, lrLostGrace, id)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(lrRetryEvery):
			}
			continue
		}
		if !lostSince.IsZero() {
			if e.log != nil {
				fmt.Fprintf(e.log, "run %s: reachable again after %s\n", id, time.Since(lostSince).Round(time.Second))
			}
			lostSince = time.Time{}
		}
		if ans.Run.Status == "running" {
			continue
		}
		state, _, err := lr.measureRun(ctx, h)
		if err != nil {
			return err
		}
		o.img.State = state
		if ans.Run.Status != "success" {
			return fmt.Errorf("run %s of %s ended %s: %s", id, h.sequence, ans.Run.Status, ans.Run.Error)
		}
		if e.log != nil {
			fmt.Fprintf(e.log, "run %s of %s succeeded\n", id, h.sequence)
		}
		return nil
	}
}

// ---- agent-coordinator ----

// measureCoordinator asks this node's LR which agent-coordinator it is
// connected to (GET /api/control/coordinator). override, when given, is
// the url instead, and LR not answering that request is then only noted.
func (c *localrepClient) measureCoordinator(ctx context.Context, override string) (map[string]any, error) {
	override = strings.TrimRight(override, "/")
	if override != "" {
		if u, err := url.Parse(override); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("select.url %q should be agent-coordinator's http(s)://host:port", override)
		}
	}
	var ans struct {
		Node      string `json:"node"`
		Connected bool   `json:"connected"`
		Host      string `json:"host"`
		HTTPPort  string `json:"http_port"`
		URL       string `json:"url"`
		Note      string `json:"note"`
	}
	if _, err := c.call(ctx, "GET", "/api/control/coordinator", nil, nil, "", &ans); err != nil {
		if override == "" {
			return nil, err
		}
		ans.Note = "local-representative didn't say which agent-coordinator it's connected to: " + err.Error()
	}
	state := map[string]any{"node": ans.Node, "connected": ans.Connected, "host": ans.Host, "http_port": ans.HTTPPort,
		"found_url": ans.URL, "url": ans.URL, "overridden": false}
	if ans.Note != "" {
		state["note"] = ans.Note
	}
	if override != "" {
		state["url"], state["overridden"] = override, true
		return state, nil
	}
	if ans.URL == "" {
		why := ans.Note
		if why == "" {
			why = "it reports no address for agent-coordinator"
		}
		return nil, fmt.Errorf("can't tell agent-coordinator's URL: %s -- connect this node's local-representative to it, or give select.url", why)
	}
	return state, nil
}

// ---- Polly clips ----

// lrFileJSON is the part of LR's FileInfo (local-representative/files.go) read here.
type lrFileJSON struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	UploadedAt int64  `json:"uploaded_at"`
	ExpiresAt  int64  `json:"expires_at"`
	State      string `json:"state"`
}

func clipFileName(name string) string { return "polly-" + strings.TrimPrefix(name, "polly-") + ".mp3" }

// newestFile is the files tab's newest file named name; nil when there's none.
func newestFile(files []lrFileJSON, name string) *lrFileJSON {
	var newest *lrFileJSON
	for i := range files {
		f := &files[i]
		if f.Name == name && (newest == nil || f.UploadedAt >= newest.UploadedAt) {
			newest = f
		}
	}
	return newest
}

func (c *localrepClient) measureClip(ctx context.Context, name string) (map[string]any, error) {
	var ans struct {
		Files []lrFileJSON `json:"files"`
	}
	if _, err := c.call(ctx, "GET", "/api/files", nil, nil, "", &ans); err != nil {
		return nil, err
	}
	state := map[string]any{"name": name, "exists": false}
	if newest := newestFile(ans.Files, clipFileName(name)); newest != nil {
		state["exists"], state["file_id"], state["file_name"], state["size"] = true, newest.ID, newest.Name, newest.Size
		state["uploaded_at"], state["files_state"] = newest.UploadedAt, newest.State
		if newest.ExpiresAt > 0 {
			state["expires_at"] = newest.ExpiresAt
		}
		c.clipMaking(ctx, newest.ID, state)
	}
	return state, nil
}

// clipMaking adds how a clip was made -- its Polly configuration, voice,
// engine, the region Polly was asked in, characters and cost, and the text as spoken -- to state, from
// The Conversationalist's recent clips (GET /convo/api/polly/clips), when
// it still holds the clip whose saved copy is fileID. That's only a while:
// it keeps the last 20, in memory, so these fields are best effort, and an
// older clip, or one whose Conversationalist isn't answering, has none.
func (c *localrepClient) clipMaking(ctx context.Context, fileID string, state map[string]any) {
	var ans struct {
		Clips []struct {
			Text       string  `json:"text"`
			Config     string  `json:"config"`
			Voice      string  `json:"voice"`
			Engine     string  `json:"engine"`
			Region     string  `json:"region"`
			Characters int     `json:"characters"`
			CostUSD    float64 `json:"cost_usd"`
			Cost       string  `json:"cost"`
			Uses       int     `json:"uses"`
			FileID     string  `json:"file_id"`
		} `json:"clips"`
	}
	if _, err := c.call(ctx, "GET", "/convo/api/polly/clips", nil, nil, "", &ans); err != nil {
		return
	}
	for _, k := range ans.Clips {
		if k.FileID == fileID {
			state["config"], state["voice"], state["engine"], state["spoken_text"] = k.Config, k.Voice, k.Engine, k.Text
			if k.Region != "" {
				state["region"] = k.Region
			}
			state["characters"], state["cost_usd"], state["cost"], state["uses"] = k.Characters, k.CostUSD, k.Cost, k.Uses
			return
		}
	}
}

func (e *engine) assertClip(ctx context.Context, o *dobj, f lrFields, line int) error {
	name, lr := o.handle.(string), e.localrep()
	wantExists, change, err := e.decideExists(o, f, line)
	if err != nil || !change {
		return err
	}
	if !wantExists {
		if _, err := lr.call(ctx, "DELETE", "/api/files/"+url.PathEscape(fmt.Sprint(o.img.State["file_id"])), nil, nil, "", nil); err != nil {
			return err
		}
		o.img.State = map[string]any{"name": name, "exists": false}
		return nil
	}
	text, ok, err := f.text(e, "text")
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(text) == "" {
		return fmt.Errorf("line %d: clip %s doesn't exist yet -- give the text it should say", line, name)
	}
	config, _, err := f.text(e, "config")
	if err != nil {
		return err
	}
	play := false
	if s, ok, err := f.text(e, "play"); err != nil {
		return err
	} else if ok {
		if play, err = parseBool(strings.TrimSpace(s)); err != nil {
			return fmt.Errorf("line %d: play is true or false, not %q", line, s)
		}
	}
	req := map[string]any{"text": text, "name": name}
	if config = strings.TrimSpace(config); config != "" {
		req["config"] = config
	}
	if play {
		req["play"] = true
	}
	body, _ := json.Marshal(req)
	var made struct {
		SavedAs    string `json:"saved_as"`
		Config     string `json:"config"`
		Characters int    `json:"characters"`
		Cost       string `json:"cost"`
		Reused     bool   `json:"reused"`
		Played     bool   `json:"played"`
		PlayedOn   string `json:"played_on"`
		PlayError  string `json:"play_error"`
	}
	if _, err := lr.call(ctx, "POST", "/convo/api/polly", nil, body, "application/json", &made); err != nil {
		return fmt.Errorf("line %d: making clip %s with Polly (The Conversationalist, through local-representative): %v", line, name, err)
	}
	if e.log != nil {
		how := fmt.Sprintf("%s, %d characters, about %s", made.Config, made.Characters, made.Cost)
		if made.Reused {
			how = made.Config + ", reusing a recent clip of the same text -- no Polly call"
		}
		fmt.Fprintf(e.log, "made Polly clip %s (%s)\n", made.SavedAs, how)
		switch {
		case !play:
		case made.Played:
			fmt.Fprintf(e.log, "  playing it on the node, in %s, after any clip before it\n", made.PlayedOn)
		case made.PlayError != "":
			fmt.Fprintf(e.log, "  not played: %s\n", made.PlayError)
		default:
			fmt.Fprintf(e.log, "  not played: this Conversationalist doesn't play clips (update it)\n")
		}
	}
	state, err := lr.measureClip(ctx, name)
	if err != nil {
		return err
	}
	o.img.State = state
	return nil
}

// ---- Compositions ----

type lrRenderJSON struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	At     int64  `json:"at"`
}

func (c *localrepClient) measureComposition(ctx context.Context, id string) (map[string]any, error) {
	var comp struct {
		Name      string           `json:"name"`
		Width     int              `json:"width"`
		Height    int              `json:"height"`
		Output    string           `json:"output"`
		Video     []map[string]any `json:"video"`
		Audio     []map[string]any `json:"audio"`
		UpdatedAt int64            `json:"updated_at"`
		Renders   []lrRenderJSON   `json:"renders"`
	}
	status, err := c.call(ctx, "GET", "/compose/api/compositions/"+url.PathEscape(id), nil, nil, "", &comp)
	if err != nil {
		return nil, err
	}
	state := map[string]any{"id": id, "exists": status != http.StatusNotFound}
	if status == http.StatusNotFound {
		return state, nil
	}
	tracks := func(clips []map[string]any) []any {
		out := []any{}
		for _, clip := range clips {
			out = append(out, clip)
		}
		return out
	}
	// The composition as a document gives it: composer's JSON numbers come
	// back as numbers, as a document's JSON text gives them.
	composition := map[string]any{"name": comp.Name, "width": float64(comp.Width), "height": float64(comp.Height),
		"video": tracks(comp.Video), "audio": tracks(comp.Audio)}
	if comp.Output != "" {
		composition["output"] = comp.Output // "audio": an audio-only render
	}
	state["composition"] = composition
	state["name"], state["updated_at"], state["rendered"] = comp.Name, comp.UpdatedAt, false
	if len(comp.Renders) > 0 {
		// The newest render, by its time: composer lists them newest first,
		// but nothing here depends on the order.
		last := comp.Renders[0]
		for _, r := range comp.Renders[1:] {
			if r.At > last.At {
				last = r
			}
		}
		state["render"] = map[string]any{"file_id": last.FileID, "name": last.Name, "size": last.Size, "at": last.At}
		state["rendered"] = last.At >= comp.UpdatedAt
	}
	return state, nil
}

func (e *engine) assertComposition(ctx context.Context, o *dobj, f lrFields, line int) error {
	id, lr := o.handle.(string), e.localrep()
	wantExists, changeExists, err := e.decideExists(o, f, line)
	if err != nil {
		return err
	}
	if !wantExists {
		if changeExists {
			if _, err := lr.call(ctx, "DELETE", "/compose/api/compositions/"+url.PathEscape(id), nil, nil, "", nil); err != nil {
				return err
			}
			o.img.State = map[string]any{"id": id, "exists": false}
		}
		return nil
	}

	exists := o.img.State["exists"] == true
	write, pending := changeExists, false // pending: reading, and an assert would write it
	var want any
	if s := f["composition"]; s != nil {
		src, err := e.text(s.want, "composition")
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(src), &want); err != nil {
			return fmt.Errorf("line %d: composition isn't JSON: %v", s.line, err)
		}
		if _, ok := want.(map[string]any); !ok {
			return fmt.Errorf("line %d: composition is a JSON object: name, width, height, video and audio", s.line)
		}
		if exists {
			have := o.img.State["composition"]
			if subsetMatch(want, have) {
				have = want // composer's defaults for what the document leaves out don't count
			}
			write = e.decide(o, []string{"composition"}, s.line, want, have, nil)
			pending = e.lastOutcome() == "would-change"
		}
	} else if !exists {
		return fmt.Errorf("line %d: composition %s doesn't exist yet -- give its composition", line, id)
	}
	if write && e.changes() {
		body, _ := json.Marshal(want)
		if _, err := lr.call(ctx, "PUT", "/compose/api/compositions/"+url.PathEscape(id), nil, body, "application/json", nil); err != nil {
			return fmt.Errorf("line %d: saving composition %s in composer: %v", line, id, err)
		}
		if o.img.State, err = lr.measureComposition(ctx, id); err != nil {
			return err
		}
	}

	s := f["rendered"]
	if s == nil {
		return nil
	}
	v, err := e.text(s.want, "rendered")
	if err != nil {
		return err
	}
	wantRendered, err := parseBool(v)
	if err != nil {
		return fmt.Errorf("line %d: rendered: %v", s.line, err)
	}
	have := o.img.State["rendered"] == true
	if pending {
		have = false // the composition would change, so its render would be out of date
	}
	if !wantRendered {
		if have {
			return fmt.Errorf("line %d: rendered: false can't be asserted -- a render can't be taken back; leave rendered out", s.line)
		}
		return nil
	}
	if !e.decide(o, []string{"rendered"}, s.line, true, have, nil) {
		return nil
	}
	renderer, _, err := f.text(e, "renderer")
	if err != nil {
		return err
	}
	if renderer = strings.TrimSpace(renderer); renderer == "" {
		renderer = "auto"
	}
	wait, err := f.seconds(e)
	if err != nil {
		return err
	}
	return e.render(ctx, o, id, renderer, wait, s.line)
}

// render has composer render composition id into the files tab, and waits.
func (e *engine) render(ctx context.Context, o *dobj, id, renderer string, wait, line int) error {
	lr := e.localrep()
	var status map[string]any
	if _, err := lr.call(ctx, "GET", "/compose/api/status", nil, nil, "", &status); err != nil {
		return fmt.Errorf("line %d: composer: %v", line, err)
	}
	if status["repr_status"] != "connected" {
		return fmt.Errorf("line %d: composer is %v, not connected, to local-representative, whose files tab the render goes into -- connect it from the compose tab", line, status["repr_status"])
	}
	type job struct {
		ID       string `json:"id"`
		Renderer string `json:"renderer"`
		State    string `json:"state"`
		Error    string `json:"error"`
	}
	var j job
	body, _ := json.Marshal(map[string]string{"composition": id, "renderer": renderer})
	if _, err := lr.call(ctx, "POST", "/compose/api/render-jobs", nil, body, "application/json", &j); err != nil {
		return fmt.Errorf("line %d: starting the render: %v", line, err)
	}
	if e.log != nil {
		fmt.Fprintf(e.log, "render job %s of %s started (renderer %s)\n", j.ID, id, j.Renderer)
		if j.Renderer == "browser" {
			fmt.Fprintln(e.log, "composer has no ffmpeg, so an open compose tab renders it: open the compose tab, click in it once (or press its \"render now\"), and keep it visible while it records")
		}
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for j.State != "done" && j.State != "failed" {
		left := int(time.Until(deadline).Seconds())
		if left <= 0 {
			return fmt.Errorf("line %d: the render didn't finish within %ds (job %s is %s) -- composer keeps working on it", line, wait, j.ID, j.State)
		}
		if left > lrPoll {
			left = lrPoll
		}
		q := url.Values{"wait": {strconv.Itoa(left)}}
		if _, err := lr.call(ctx, "GET", "/compose/api/render-jobs/"+url.PathEscape(j.ID), q, nil, "", &j); err != nil {
			return fmt.Errorf("line %d: checking render job %s: %v", line, j.ID, err)
		}
	}
	if j.State == "failed" {
		return fmt.Errorf("line %d: the render failed: %s", line, j.Error)
	}
	state, err := lr.measureComposition(ctx, id)
	if err != nil {
		return err
	}
	o.img.State = state
	return nil
}

// lastOutcome is the outcome decide recorded last.
func (e *engine) lastOutcome() string {
	if n := len(e.image.Fields); n > 0 {
		return e.image.Fields[n-1].Outcome
	}
	return ""
}

// subsetMatch reports whether have holds everything want gives: each key of
// a mapping, each member of a list (the lists the same length), and equal
// values.
func subsetMatch(want, have any) bool {
	switch w := want.(type) {
	case map[string]any:
		h, ok := have.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range w {
			if !subsetMatch(v, h[k]) {
				return false
			}
		}
		return true
	case []any:
		h, ok := have.([]any)
		if !ok || len(h) != len(w) {
			return false
		}
		for i := range w {
			if !subsetMatch(w[i], h[i]) {
				return false
			}
		}
		return true
	}
	return valueKey(want) == valueKey(have)
}

// ---- The file cache ----

// lrCacheFile is a localrep.files object: a file, by name, in a file cache.
type lrCacheFile struct{ cache, name string }

func (c *localrepClient) measureCacheFile(ctx context.Context, h *lrCacheFile) (map[string]any, error) {
	var ans struct {
		Files []lrFileJSON `json:"files"`
	}
	if _, err := c.call(ctx, "GET", "/api/files", nil, nil, "", &ans); err != nil {
		return nil, err
	}
	state := map[string]any{"cache": h.cache, "name": h.name, "lr_url": c.base, "exists": false}
	newest := newestFile(ans.Files, h.name)
	if newest == nil {
		return state, nil
	}
	state["exists"], state["file_id"], state["size"] = true, newest.ID, newest.Size
	state["uploaded_at"], state["files_state"] = newest.UploadedAt, newest.State
	if newest.ExpiresAt > 0 {
		state["expires_at"] = newest.ExpiresAt
	}
	if newest.Size > lrMaxBody {
		state["content_omitted"] = fmt.Sprintf("%d bytes, over %d", newest.Size, lrMaxBody)
		return state, nil
	}
	data, err := c.raw(ctx, "/api/files/"+url.PathEscape(newest.ID))
	if err != nil {
		return nil, err
	}
	state["sha256"] = sha256Hex(data)
	if len(data) <= lrMaxText && utf8.Valid(data) {
		state["content"] = string(data)
	}
	return state, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (e *engine) assertCacheFile(ctx context.Context, o *dobj, f lrFields, line int) error {
	h, lr := o.handle.(*lrCacheFile), e.localrep()
	wantExists := true
	if s, ok, err := f.text(e, "exists"); err != nil {
		return err
	} else if ok {
		if wantExists, err = parseBool(strings.TrimSpace(s)); err != nil {
			return fmt.Errorf("line %d: exists: %v", f["exists"].line, err)
		}
	}
	_, change, err := e.decideExists(o, f, line)
	if err != nil {
		return err
	}
	if !wantExists || e.deleting() {
		if !change {
			return nil
		}
		if _, err := lr.call(ctx, "DELETE", "/api/files/"+url.PathEscape(fmt.Sprint(o.img.State["file_id"])), nil, nil, "", nil); err != nil {
			return err
		}
		o.img.State = map[string]any{"cache": h.cache, "name": h.name, "lr_url": lr.base, "exists": false}
		return nil
	}
	data, dataLine, err := e.cacheFileData(f)
	if err != nil {
		return err
	}
	if o.img.State["exists"] != true {
		if !change {
			return nil
		}
		if data == nil {
			return fmt.Errorf("line %d: %s isn't in the %s file cache -- give its copy_of or content to upload it", line, h.name, h.cache)
		}
		return e.uploadCacheFile(ctx, o, h, data)
	}
	if data == nil {
		return nil // nothing to compare it with
	}
	var have any = o.img.State["sha256"]
	if why, ok := o.img.State["content_omitted"].(string); ok {
		have = "(" + why + ")" // not read: held, unless an image or force
	}
	if !e.decide(o, []string{"sha256"}, dataLine, sha256Hex(data), have, nil) {
		return nil
	}
	return e.uploadCacheFile(ctx, o, h, data)
}

// cacheFileData is what an assert uploads, and the line giving it: content's
// text, or the bytes of the file copy_of names. nil when it gives neither,
// or -- reading -- copy_of isn't there yet (an assert makes it first, say).
func (e *engine) cacheFileData(f lrFields) ([]byte, int, error) {
	if s, ok, err := f.text(e, "content"); err != nil || ok {
		return []byte(s), f["content"].line, err
	}
	p, ok, err := f.text(e, "copy_of")
	if err != nil || !ok {
		return nil, 0, err
	}
	line := f["copy_of"].line
	p = strings.TrimSpace(p)
	if !e.changes() && strings.Contains(p, "${{") {
		return nil, line, nil // refers to a field only an assert fills in
	}
	if !filepath.IsAbs(p) {
		return nil, line, fmt.Errorf("line %d: copy_of %q is an absolute path on this host", line, p)
	}
	info, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) && !e.changes() {
		return nil, line, nil
	}
	if err != nil {
		return nil, line, fmt.Errorf("line %d: copy_of: %w", line, err)
	}
	if !info.Mode().IsRegular() {
		return nil, line, fmt.Errorf("line %d: copy_of %s isn't a file", line, p)
	}
	if info.Size() > lrMaxUpload {
		return nil, line, fmt.Errorf("line %d: copy_of %s is %d bytes, over the files tab's %d", line, p, info.Size(), lrMaxUpload)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, line, fmt.Errorf("line %d: copy_of: %w", line, err)
	}
	return data, line, nil
}

// uploadCacheFile uploads data as o's file, deletes the copy it replaces,
// and measures o again.
func (e *engine) uploadCacheFile(ctx context.Context, o *dobj, h *lrCacheFile, data []byte) error {
	lr := e.localrep()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", h.name)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	var ans struct {
		Files []lrFileJSON `json:"files"`
	}
	if _, err := lr.call(ctx, "POST", "/api/files", nil, body.Bytes(), w.FormDataContentType(), &ans); err != nil {
		return err
	}
	if len(ans.Files) != 1 {
		return fmt.Errorf("POST /api/files: %d files saved, not 1", len(ans.Files))
	}
	if e.log != nil {
		fmt.Fprintf(e.log, "%s uploaded to the %s file cache (%s) as %s\n", h.name, h.cache, lr.base, ans.Files[0].ID)
	}
	if old, _ := o.img.State["file_id"].(string); old != "" && old != ans.Files[0].ID {
		if _, err := lr.call(ctx, "DELETE", "/api/files/"+url.PathEscape(old), nil, nil, "", nil); err != nil {
			return err
		}
	}
	state, err := lr.measureCacheFile(ctx, h)
	if err != nil {
		return err
	}
	o.img.State = state
	return nil
}

// ---- The client ----

// localrepClient calls local-representative's HTTP API.
type localrepClient struct {
	base string
	http *http.Client
}

func (e *engine) localrep() *localrepClient {
	if e.lr == nil {
		base := strings.TrimRight(os.Getenv("DTT_LOCALREP_URL"), "/")
		if base == "" {
			base = "http://localhost:8081"
		}
		e.lr = &localrepClient{base: base, http: &http.Client{Timeout: lrHTTPTimeout}}
	}
	return e.lr
}

// lrNode is the query sending a control request to node ("" for this LR).
func lrNode(node string) url.Values {
	q := url.Values{}
	if node != "" {
		q.Set("node", node)
	}
	return q
}

// call sends body (with contentType) and decodes a JSON answer into out (if
// not nil). A 404 comes back as its status, for the caller to read as
// "there's none"; any other answer outside 2xx is an error carrying LR's (or
// the sub-app's) message.
func (c *localrepClient) call(ctx context.Context, method, p string, q url.Values, body []byte, contentType string, out any) (int, error) {
	target := c.base + p
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("local-representative isn't answering at %s (%v) -- is it running? ($DTT_LOCALREP_URL says where)", c.base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, lrMaxBody))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusNotFound && method == "GET" && strings.HasPrefix(p, "/compose/api/compositions/") {
		return resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return resp.StatusCode, fmt.Errorf("%s %s: %d: %s", method, p, resp.StatusCode, msg)
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: unexpected answer: %v", method, p, err)
		}
	}
	return resp.StatusCode, nil
}

// raw GETs p's bytes (a file in the files tab, say), up to lrMaxBody.
func (c *localrepClient) raw(ctx context.Context, p string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+p, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local-representative isn't answering at %s (%v) -- is it running? ($DTT_LOCALREP_URL says where)", c.base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, lrMaxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("GET %s: %d: %s", p, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}
