package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// This file is the dtt provider: a document calling other documents
// (docs/terms.md, "Composition").
//
//	dtt.calls:
//	  select:
//	    document: localrep-demo-prepare.dtt.yaml   (found as source_file is: beside
//	                                                this document, where dtt was
//	                                                called from, or dtt's
//	                                                examples/compositions/; in an
//	                                                in-place call, in its directory
//	                                                or what its .dtttc includes)
//	    key: one                                    (optional: tells apart two calls
//	                                                of the same document)
//	  alias: prep
//	  assert:
//	    variables:                                  (the called document's variables)
//	      node: ${{var.node}}
//
// Asserting the statement calls the document in this document's mode: a read
// reads it, an assert asserts it and a delete deletes it, with --force if
// this call has it. A statement with nothing asserted only reads the
// document, with its variables at their defaults. The called document is
// checked with the rest of this one, before anything is measured.
//
// The call's state is its image, kept whole as image, and the parts a
// document calling it uses: ok, summary, error, variables (the values it was
// given), phases, and by_alias, the state of each of its objects that has an
// alias (${{prep.by_alias.prep-comp.render.file_id}}, say; var is there
// too). Its field outcomes are this document's too, each named by the call
// (dtt.calls[<document>] > <its object>), so they count in this image's
// summary and phase. Deleting with an image, the call is given the image
// that image saw it make, so its own timestamps and the rest find what it
// made.
//
// A called document isn't recorded or kept on its own (calls.go): its image
// lives in this one's.
type callsProvider struct{}

// maxCallDepth is how deep calls may nest: deeper is most likely a document
// calling itself.
const maxCallDepth = 8

// callHandle is a call's provider handle: the document as selected.
type callHandle struct{ document, key string }

func (callsProvider) checkSelect(st *statement) error {
	if st.pathString() != "dtt.calls" {
		return fmt.Errorf("the dtt provider's path is dtt.calls")
	}
	if st.sel == nil || st.sel.kind != yMap {
		return fmt.Errorf("a call's select names its document: select: {document: <file>}")
	}
	if err := onlyKeys(st.sel, "a call's select", "document", "key"); err != nil {
		return err
	}
	doc, err := scalar(st.sel, "document")
	if err != nil {
		return err
	}
	if strings.TrimSpace(doc) == "" {
		return fmt.Errorf("a call's select names its document: select: {document: <file>}")
	}
	_, err = scalar(st.sel, "key")
	return err
}

func (callsProvider) checkAssert(_ []string, body *yNode) error {
	if body.kind != yMap {
		return fmt.Errorf("line %d: a call's assert is a mapping: variables", body.line)
	}
	if err := onlyKeys(body, "a call's assert", "variables"); err != nil {
		return err
	}
	v := body.get("variables")
	if v == nil || isEmpty(v) {
		return nil
	}
	if v.kind != yMap {
		return fmt.Errorf("line %d: variables is a mapping of the called document's variables, name: value", v.line)
	}
	for i, k := range v.keys {
		if v.vals[i].kind != yScalar {
			return fmt.Errorf("line %d: variable %s should be a single value", v.vals[i].line, k)
		}
	}
	return nil
}

// checkCall checks the document a call names, as it will be called: with the
// variables its assert gives (their values are only known once it runs, so
// only their names are checked). A document named through ${{...}} is
// checked when it's called.
func (e *engine) checkCall(st *statement) error {
	doc, _ := scalar(st.sel, "document")
	if strings.Contains(doc, "${{") {
		return nil
	}
	if e.depth+1 > maxCallDepth {
		return fmt.Errorf("line %d: calls nest more than %d deep here -- does a document call itself?", st.line, maxCallDepth)
	}
	src, dir, err := e.findDocFile(strings.TrimSpace(doc))
	if err != nil {
		return fmt.Errorf("line %d: document: %v", st.line, err)
	}
	called, err := parseDocument(src)
	if err != nil {
		return fmt.Errorf("line %d: document %s: %v", st.line, doc, err)
	}
	sub := e.callEngine(e.mode, dir)
	sub.vars = map[string]string{}
	if st.assert != nil {
		if v := st.assert.get("variables"); v != nil && v.kind == yMap {
			for _, k := range v.keys {
				sub.vars[k] = ""
			}
		}
	}
	if err := sub.check(called); err != nil {
		return fmt.Errorf("line %d: document %s: %v", st.line, doc, err)
	}
	return nil
}

// callEngine is an engine for a document this one calls: the same mode
// (unless a read-only call), force, tool and clients, its own image.
func (e *engine) callEngine(mode, dir string) *engine {
	return &engine{mode: mode, force: e.force, only: e.only, log: e.log, now: e.now, sleep: e.sleep,
		docDir: dir, inPlace: e.inPlace, includes: e.includes, depth: e.depth + 1, gh: e.gh, awsRun: e.awsRun, lr: e.lr}
}

func (callsProvider) resolve(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	doc, err := e.text(st.sel.get("document"), "document")
	if err != nil {
		return nil, err
	}
	h := &callHandle{document: strings.TrimSpace(doc)}
	if k := st.sel.get("key"); k != nil {
		if h.key, err = e.text(k, "key"); err != nil {
			return nil, err
		}
		h.key = strings.TrimSpace(h.key)
	}
	id := "dtt.calls[" + h.document + "]"
	if h.key != "" {
		id = "dtt.calls[" + h.document + " " + h.key + "]"
	}
	o := e.addObject(id, "dtt.calls", st.line, map[string]any{"document": h.document, "key": h.key, "called": false}, h)
	if st.assert == nil {
		// Nothing asserted: the document is only read, as it is by default.
		if err := e.call(ctx, o, "read", nil); err != nil {
			return nil, err
		}
	}
	return []*dobj{o}, nil
}

func (callsProvider) assert(ctx context.Context, e *engine, o *dobj, body *yNode) error {
	vars := map[string]string{}
	if v := body.get("variables"); v != nil && v.kind == yMap {
		for i, k := range v.keys {
			s, err := e.text(v.vals[i], "variable "+k)
			if err != nil {
				return err
			}
			vars[k] = s
		}
	}
	return e.call(ctx, o, e.mode, vars)
}

// call runs the document o names in mode, with vars, and makes its image o's
// state.
func (e *engine) call(ctx context.Context, o *dobj, mode string, vars map[string]string) error {
	h := o.handle.(*callHandle)
	if e.depth+1 > maxCallDepth {
		return fmt.Errorf("calls nest more than %d deep here -- does a document call itself?", maxCallDepth)
	}
	src, dir, err := e.findDocFile(h.document)
	if err != nil {
		return fmt.Errorf("document: %v", err)
	}
	var prior *Image
	if p, ok := e.prior[o.img.ID]; ok && p.State["image"] != nil {
		// The image this call returned when the given image was taken.
		b, _ := json.Marshal(p.State["image"])
		if prior, err = parseImage(string(b)); err != nil {
			return fmt.Errorf("the image's call of %s: %v", h.document, err)
		}
	}
	sub := e.callEngine(mode, dir)
	sub.vars = vars
	if e.log != nil {
		fmt.Fprintf(e.log, "calling %s (%s)\n", h.document, mode)
	}
	img, err := sub.runSource(ctx, src, prior)
	if e.lr == nil {
		e.lr = sub.lr // the called document's clients serve this one's later statements too
	}
	if e.gh == nil {
		e.gh = sub.gh
	}
	if img != nil {
		for _, f := range img.Fields {
			f.Object = o.img.ID + " > " + f.Object
			e.fold(f)
		}
		o.img.State = callState(h, mode, vars, img)
		o.measured = copyState(o.img.State) // the called image keeps its own before and after
	}
	if err != nil {
		// %v, not %w: a mistake the called document only shows now is this
		// call failing, with what the phases before it made kept in the image.
		return fmt.Errorf("calling %s: %v", h.document, trimBadInput(err))
	}
	if e.log != nil {
		fmt.Fprintf(e.log, "called %s: %s\n", h.document, summaryText(img.Summary))
	}
	return nil
}

// callState is a call's state, from the image its document returned.
func callState(h *callHandle, mode string, vars map[string]string, img *Image) map[string]any {
	given := map[string]any{}
	for k, v := range vars {
		given[k] = v
	}
	byAlias := map[string]any{}
	grouped := map[string][]any{}
	for _, so := range img.Objects {
		if so.Alias != "" {
			grouped[so.Alias] = append(grouped[so.Alias], so.State)
		}
	}
	for a, states := range grouped {
		if len(states) == 1 {
			byAlias[a] = states[0]
		} else {
			byAlias[a] = states
		}
	}
	summary := map[string]any{}
	for k, n := range img.Summary {
		summary[k] = n
	}
	phases := []any{}
	for _, p := range img.Phases {
		phases = append(phases, map[string]any{"name": p.Name, "status": p.Status, "error": p.Error})
	}
	state := map[string]any{"document": h.document, "key": h.key, "called": true, "mode": mode, "ok": img.OK,
		"variables": given, "summary": summary, "phases": phases, "by_alias": byAlias, "image": img}
	if img.Error != "" {
		state["error"] = img.Error
	}
	return state
}

// summaryText is an image summary as "2 changed, 1 kept", outcomes in order.
func summaryText(s map[string]int) string {
	if len(s) == 0 {
		return "nothing asserted"
	}
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", s[k], k))
	}
	return strings.Join(parts, ", ")
}
