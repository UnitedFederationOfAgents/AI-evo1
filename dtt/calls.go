package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// This file is where a tool call lives on disk (docs/terms.md, "In-place
// and pathless calls").
//
// A directory holding a .dtttc file is an IN-PLACE call: its document lives
// in it (.dtttc is empty, or a flat mapping naming it, document: <file>; else
// the call is the directory's only *.dtt.yaml), and so does everything the
// document reads from a file (localrep's source_file): a path that resolves
// outside the directory is a mistake in the document, and dtt's examples/
// aren't looked in -- save those its .dtttc includes (dtttcSpec). A
// document file whose directory holds .dtttc is an in-place call too.
//
// Any other call -- a document elsewhere, or a directory without .dtttc (its
// only *.dtt.yaml) -- is PATHLESS, staged under pathless-dir
// (~/.ufa/dtt/pathless) at <file name, dots as underscores>/<sha256 of the
// document>/, with the document copied in as document.dtt.yaml. A document
// read from stdin is named stdin; a built-in tool's inline document input,
// document. Its staging directory, or the document.dtt.yaml in it, may be
// called again: unchanged, that is the same call, staged in the same place.
//
// Either way, the image of each read, assert or delete is kept, as well as
// printed or returned, at <stage>/images/<UTC time>-<mode>.image.json, and
// dtt says where on stderr, and the call goes to the top of the recent calls
// (recent-calls, ~/.ufa/dtt/recent-calls.json), which dtt calls lists.
// Staging is best effort: a failure is a warning.

const (
	dtttcFile      = ".dtttc"            // marks a directory as an in-place call
	callInPlace    = "in-place"          // docCall.kind
	callPathless   = "pathless"          // docCall.kind
	docFileSuffix  = ".dtt.yaml"         // what an in-place call's document is named
	stagedDocName  = "document.dtt.yaml" // a pathless call's document, in its staging area
	imagesDirName  = "images"            // under the stage: the images its executions returned
	stdinDocName   = "stdin"             // the pathless name of a document read from stdin
	inlineDocName  = "document"          // the pathless name of a built-in tool's document input
	imageTimestamp = "20060102T150405Z"  // an image file's UTC time
)

// docFileKeys are the assert fields that name a file whose content the
// document takes (checked against an in-place call's directory up front,
// when they're written without ${{...}}).
var docFileKeys = []string{"source_file"}

// docCall is a document as called: its text, and where the call is staged.
type docCall struct {
	src   string
	path  string // the document's file, absolute; "" for stdin or an inline document
	kind  string // callInPlace or callPathless
	dir   string // in-place: the call's directory
	stage string // where images are kept: the in-place directory, or the pathless staging area ("" when there's none)
	// includes are the dtt example files an in-place call's .dtttc lets it
	// read from outside its directory (dtttcSpec).
	includes []string
}

// docError is a mistake in a document found only while it runs (an in-place
// call's source_file filled in by ${{...}}, say): bad input, exit 2, like
// the ones found before anything runs.
type docError struct{ msg string }

func (d docError) Error() string        { return d.msg }
func (d docError) Is(target error) bool { return target == errBadInput }

// openDocCall reads the document arg names -- "-" for stdin, a directory
// holding .dtttc, or a document file -- as dtt read/assert/delete take it.
// pathlessRoot is the pathless-dir setting.
func openDocCall(arg string, stdin io.Reader, pathlessRoot string) (*docCall, error) {
	if arg == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
		return pathlessCall(string(data), stdinDocName, pathlessRoot), nil
	}
	return openDocFile(arg, pathlessRoot)
}

// openDocFile reads the document at p (a file, or a directory holding
// .dtttc) and works out which kind of call it is.
func openDocFile(p, pathlessRoot string) (*docCall, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		staged := filepath.Join(abs, stagedDocName)
		if fi, err := os.Stat(staged); err == nil && !fi.IsDir() && pathlessStageName(staged, pathlessRoot) != "" {
			abs = staged
		} else if abs, err = dtttcDocument(abs); err != nil {
			return nil, docError{fmt.Sprintf("%s: %v", p, err)}
		}
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	if fi, err := os.Stat(filepath.Join(dir, dtttcFile)); err == nil && !fi.IsDir() {
		spec, _, err := readDtttc(dir)
		if err != nil {
			return nil, docError{fmt.Sprintf("%s: %v", p, err)}
		}
		return &docCall{src: string(data), path: abs, kind: callInPlace, dir: dir, stage: dir, includes: spec.includes}, nil
	}
	// A pathless call's staged document is called under its own name, so
	// calling it again (unchanged) is the same call, staged in the same
	// place.
	name := filepath.Base(abs)
	if staged := pathlessStageName(abs, pathlessRoot); staged != "" {
		name = staged
	}
	dc := pathlessCall(string(data), name, pathlessRoot)
	dc.path = abs
	return dc, nil
}

// pathlessStageName is the name of the pathless call whose staged document
// doc is -- <root>/<name>/<sha256>/document.dtt.yaml -- or "" when doc is
// any other file.
func pathlessStageName(doc, root string) string {
	if root == "" {
		return ""
	}
	r, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(r, doc)
	if err != nil {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 || parts[0] == ".." || parts[0] == "." || parts[2] != stagedDocName || !isSHA256Hex(parts[1]) {
		return ""
	}
	return parts[0]
}

func isSHA256Hex(s string) bool {
	if len(s) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// dtttcSpec is a .dtttc: empty, or a mapping of
//
//	document: <file in the directory>     (else its only *.dtt.yaml)
//	include:                              (optional: dtt's own example files
//	  - compositions/<file>                the call may read, though they
//	                                       aren't in the directory)
//
// An include is a path under dtt's examples/ (examples.go): a file, or a
// directory and everything under it. The document names an included file as
// it's included (compositions/x.dtt.yaml), and an included composition names
// its own files as it does anywhere (resources/y.control.yaml, found under
// compositions/), so they must be included too.
type dtttcSpec struct {
	document string
	includes []string
}

// parseDtttc reads a .dtttc's text, checking each include is one of dtt's
// examples.
func parseDtttc(src string) (dtttcSpec, error) {
	var spec dtttcSpec
	n, err := parseYAML(src)
	if err != nil {
		return spec, err
	}
	if n.kind != yMap {
		return spec, errors.New("it should be empty or a mapping (document: <file in the directory>, include: [<dtt example>...])")
	}
	if err := onlyKeys(n, dtttcFile, "document", "include"); err != nil {
		return spec, err
	}
	if spec.document, err = scalar(n, "document"); err != nil {
		return spec, err
	}
	spec.document = strings.TrimSpace(spec.document)
	inc := n.get("include")
	var items []*yNode
	switch {
	case inc == nil || isEmpty(inc):
	case inc.kind == yScalar:
		items = []*yNode{inc}
	case inc.kind == ySeq:
		items = inc.items
	default:
		return spec, fmt.Errorf("line %d: include is a list of dtt's example files (compositions/<file>)", inc.line)
	}
	for _, it := range items {
		if it.kind != yScalar {
			return spec, fmt.Errorf("line %d: an include is one of dtt's example files (compositions/<file>)", it.line)
		}
		p, err := exampleInclude(it.str)
		if err != nil {
			return spec, fmt.Errorf("line %d: include %s: %v", it.line, strings.TrimSpace(it.str), err)
		}
		spec.includes = append(spec.includes, p)
	}
	return spec, nil
}

// exampleInclude checks an include names one of dtt's example files or
// directories, returning it clean and relative to examples/.
func exampleInclude(s string) (string, error) {
	p := strings.TrimPrefix(path.Clean(filepath.ToSlash(strings.TrimSpace(s))), examplesRoot+"/")
	if p == "" || p == "." || p == ".." || strings.HasPrefix(p, "../") || path.IsAbs(p) {
		return "", fmt.Errorf("it should be a path under dtt's %s/ (compositions/<file>)", examplesRoot)
	}
	if _, err := fs.Stat(examplesFS, path.Join(examplesRoot, p)); err != nil {
		return "", fmt.Errorf("dtt has no example %s (dtt examples lists the compositions)", p)
	}
	return p, nil
}

// readDtttc reads dir's .dtttc; ok is false when there's none.
func readDtttc(dir string) (spec dtttcSpec, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, dtttcFile))
	if errors.Is(err, fs.ErrNotExist) {
		return spec, false, nil
	}
	if err != nil {
		return spec, false, err
	}
	if spec, err = parseDtttc(string(data)); err != nil {
		return spec, true, fmt.Errorf("%s: %v", dtttcFile, err)
	}
	return spec, true, nil
}

// dtttcDocument is the document of the call in dir: the one its .dtttc
// names, else its only *.dtt.yaml -- which, with no .dtttc, is a pathless
// call.
func dtttcDocument(dir string) (string, error) {
	spec, ok, err := readDtttc(dir)
	if err != nil {
		return "", err
	}
	if !ok {
		return onlyDocument(dir)
	}
	if name := spec.document; name != "" {
		p := name
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		p = filepath.Clean(p)
		if !insideDir(dir, p) {
			return "", fmt.Errorf("%s: document %s isn't in the directory", dtttcFile, name)
		}
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			return "", fmt.Errorf("%s: document %s isn't a file there", dtttcFile, name)
		}
		return p, nil
	}
	return onlyDocument(dir)
}

// onlyDocument is dir's only *.dtt.yaml.
func onlyDocument(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var docs []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), docFileSuffix) {
			docs = append(docs, e.Name())
		}
	}
	sort.Strings(docs)
	switch len(docs) {
	case 1:
		return filepath.Join(dir, docs[0]), nil
	case 0:
		return "", fmt.Errorf("it holds no *%s document -- add one (or name it with document: <file> in a %s)", docFileSuffix, dtttcFile)
	}
	return "", fmt.Errorf("it holds %d *%s documents (%s) -- name the one to call, or with document: <file> in a %s", len(docs), docFileSuffix, strings.Join(docs, ", "), dtttcFile)
}

// pathlessCall is a call of the document src, named name, staged under
// root ("" when there's no pathless-dir).
func pathlessCall(src, name, root string) *docCall {
	dc := &docCall{src: src, kind: callPathless}
	if root != "" {
		dc.stage = pathlessStage(root, name, src)
	}
	return dc
}

// pathlessStage is a pathless call's staging area:
// <root>/<name, dots as underscores>/<sha256 of the document, hex>.
func pathlessStage(root, name, src string) string {
	sum := sha256.Sum256([]byte(src))
	return filepath.Join(root, strings.ReplaceAll(name, ".", "_"), hex.EncodeToString(sum[:]))
}

// insideDir reports whether p is dir or under it (both absolute and clean).
func insideDir(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// inPlacePath resolves p, a file an in-place call's document names, in the
// call's directory: relative to it, or absolute inside it. Anything else
// (.. out of it, an absolute path elsewhere, a link out of it) is a
// docError.
func (e *engine) inPlacePath(p string) (string, error) {
	full := p
	if !filepath.IsAbs(full) {
		full = filepath.Join(e.inPlace, full)
	}
	full = filepath.Clean(full)
	outside := !insideDir(e.inPlace, full)
	if !outside {
		// A link inside the directory mustn't lead out of it.
		if resolved, err := filepath.EvalSymlinks(full); err == nil {
			dir := e.inPlace
			if d, err := filepath.EvalSymlinks(dir); err == nil {
				dir = d
			}
			outside = !insideDir(dir, resolved)
		}
	}
	if outside {
		return "", docError{fmt.Sprintf("%s is outside the in-place call's directory %s -- an in-place call only reads files in it, and the dtt examples its %s includes", p, e.inPlace, dtttcFile)}
	}
	return full, nil
}

// included is where in dtt's examples p is, when the in-place call's .dtttc
// includes it (dtttcSpec): p as named from examples/ (compositions/x), or
// from compositions/ (as an included composition names its own files).
func (e *engine) included(p string) (string, bool) {
	if len(e.includes) == 0 || filepath.IsAbs(p) {
		return "", false
	}
	clean := strings.TrimPrefix(path.Clean(filepath.ToSlash(p)), examplesRoot+"/")
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	for _, cand := range []string{clean, path.Join(strings.TrimPrefix(compositionsRoot, examplesRoot+"/"), clean)} {
		for _, inc := range e.includes {
			if cand == inc || strings.HasPrefix(cand, inc+"/") {
				return path.Join(examplesRoot, cand), true
			}
		}
	}
	return "", false
}

// checkInPlaceFiles checks, before anything runs, each file an in-place
// call's assert n names (docFileKeys, as written) is in the call's
// directory, or included. One written with ${{...}} is checked when it's
// read.
func (e *engine) checkInPlaceFiles(n *yNode) error {
	if n == nil {
		return nil
	}
	switch n.kind {
	case yMap:
		for i, k := range n.keys {
			v := n.vals[i]
			if p := strings.TrimSpace(v.str); contains(docFileKeys, k) && v.kind == yScalar && p != "" && !strings.Contains(p, "${{") {
				if _, ok := e.included(p); ok {
					// one of dtt's examples, included by .dtttc
				} else if _, err := e.inPlacePath(p); err != nil {
					return fmt.Errorf("line %d: %s: %v", v.line, k, err)
				}
			}
			if err := e.checkInPlaceFiles(v); err != nil {
				return err
			}
		}
	case ySeq:
		for _, item := range n.items {
			if err := e.checkInPlaceFiles(item); err != nil {
				return err
			}
		}
	}
	return nil
}

// keep stages the call once it has run: a pathless call's document is
// copied into its staging area (unless it's there already), and img is
// written to <stage>/images/<now, UTC>-<mode>.image.json -- with -2, -3 ...
// before .image.json should two calls of it end in the same second. It
// returns the image file's path.
func (dc *docCall) keep(img *Image, now time.Time) (string, error) {
	if dc.stage == "" {
		return "", errors.New("there's nowhere to keep it (pathless-dir is empty)")
	}
	if dc.kind == callPathless {
		if err := os.MkdirAll(dc.stage, 0o755); err != nil {
			return "", err
		}
		doc := filepath.Join(dc.stage, stagedDocName)
		if _, err := os.Stat(doc); errors.Is(err, fs.ErrNotExist) {
			if err := os.WriteFile(doc, []byte(dc.src), 0o644); err != nil {
				return "", err
			}
		}
	}
	dir := filepath.Join(dc.stage, imagesDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(img, "", "  ")
	if err != nil {
		return "", err
	}
	base := now.UTC().Format(imageTimestamp) + "-" + img.Mode
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		p := filepath.Join(dir, name+".image.json")
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) && i < 100 {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(append(data, '\n'))
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return p, werr
	}
}

// keepCall keeps the call's image (keep), saying where on stderr, and puts
// the call at the top of the recent calls; failing to is only a warning.
func (c *cli) keepCall(cfg config, dc *docCall, img *Image, now time.Time) {
	p, err := dc.keep(img, now)
	if err != nil {
		fmt.Fprintf(c.stderr, "dtt: warning: the image wasn't kept: %v\n", err)
		return
	}
	fmt.Fprintf(c.stderr, "dtt: image kept at %s\n", p)
	if cfg.recentCalls == "" {
		return
	}
	if err := noteRecentCall(cfg.recentCalls, dc.recent(img, p, now)); err != nil {
		fmt.Fprintf(c.stderr, "dtt: warning: %s wasn't updated: %v\n", cfg.recentCalls, err)
	}
}

// name is the call's name: an in-place call's directory's, or the name a
// pathless call is staged under.
func (dc *docCall) name() string {
	if dc.kind == callInPlace {
		return filepath.Base(dc.dir)
	}
	if dc.stage == "" {
		return ""
	}
	return filepath.Base(filepath.Dir(dc.stage))
}

// ---- Recent calls ----

// Every execution that keeps its image puts its call at the top of the
// recent-calls file (~/.ufa/dtt/recent-calls.json): the calls -- by their
// directory, an in-place call's or a pathless call's staging area -- most
// recently executed first, each once, at most maxRecentCalls. Either
// directory may be given back to dtt read/assert/delete (or the declare
// tool's document_file) to call it again. The file is rewritten whole
// (through a temporary file and a rename); two dtts finishing at once may
// lose one's update, which only drops a call from the list.

const (
	recentCallsName   = "recent-calls.json"
	recentCallsFormat = "dtt-recent-calls-v1"
	maxRecentCalls    = 50
	maxCallImages     = 100 // listed by dtt calls <call>, newest first
)

// RecentCall is one call in the recent-calls file, as its latest execution
// left it.
type RecentCall struct {
	Dir        string `json:"dir"`  // the call's directory: in-place, its own; pathless, its staging area
	Kind       string `json:"kind"` // in-place or pathless
	Name       string `json:"name"` // in-place, the directory's name; pathless, the name it's staged under
	Document   string `json:"document"`
	From       string `json:"from,omitempty"` // pathless: the file it was called from, if any (not stdin or an inline document)
	At         string `json:"at"`             // its latest execution, UTC
	Mode       string `json:"mode"`
	OK         bool   `json:"ok"`
	Image      string `json:"image,omitempty"` // the image that execution kept
	Executions int    `json:"executions"`      // how many times it has been executed since it came into the list
}

// RecentCalls is the recent-calls file.
type RecentCalls struct {
	Format string       `json:"format"`
	Calls  []RecentCall `json:"calls"`
}

// recent is the call's entry for an execution that returned img, kept at
// image.
func (dc *docCall) recent(img *Image, image string, now time.Time) RecentCall {
	rc := RecentCall{Dir: dc.stage, Kind: dc.kind, Name: dc.name(), Document: dc.path,
		At: now.UTC().Format(time.RFC3339), Mode: img.Mode, OK: img.OK, Image: image, Executions: 1}
	if dc.kind == callPathless {
		rc.Document = filepath.Join(dc.stage, stagedDocName)
		if dc.path != "" && dc.path != rc.Document {
			rc.From = dc.path
		}
	}
	return rc
}

// readRecentCalls reads the recent-calls file; one that doesn't exist yet
// lists none.
func readRecentCalls(file string) (RecentCalls, error) {
	rcs := RecentCalls{Format: recentCallsFormat, Calls: []RecentCall{}}
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return rcs, nil
	}
	if err != nil {
		return rcs, err
	}
	if err := json.Unmarshal(data, &rcs); err != nil {
		return rcs, fmt.Errorf("%s: %v", file, err)
	}
	if rcs.Format != recentCallsFormat {
		return rcs, fmt.Errorf("%s: format %q isn't %s", file, rcs.Format, recentCallsFormat)
	}
	if rcs.Calls == nil {
		rcs.Calls = []RecentCall{}
	}
	return rcs, nil
}

// noteRecentCall puts rc at the top of the recent-calls file, in place of
// the call's earlier entry. A file that can't be read is started afresh.
func noteRecentCall(file string, rc RecentCall) error {
	rcs, err := readRecentCalls(file)
	if err != nil {
		rcs = RecentCalls{Format: recentCallsFormat}
	}
	calls := []RecentCall{rc}
	for _, old := range rcs.Calls {
		if old.Dir == rc.Dir {
			calls[0].Executions += old.Executions
			continue
		}
		if len(calls) < maxRecentCalls {
			calls = append(calls, old)
		}
	}
	rcs.Calls = calls
	data, err := json.MarshalIndent(rcs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), file)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

// ---- CLI: dtt calls ----

// CallImage is one image kept for a call, from its file's name:
// <UTC time>-<mode>[-n].image.json.
type CallImage struct {
	Name string `json:"name"`
	Path string `json:"path"`
	At   string `json:"at,omitempty"` // UTC
	Mode string `json:"mode,omitempty"`
}

// CallInfo is one call described (dtt calls <call>): what it is, its
// document, and the images kept for it, newest first.
type CallInfo struct {
	Dir           string      `json:"dir"` // in-place, the call's directory; pathless, its staging area ("" with no pathless-dir)
	Kind          string      `json:"kind"`
	Name          string      `json:"name"`
	Document      string      `json:"document"`           // the document's file
	Staged        bool        `json:"staged"`             // Dir exists (a pathless call is staged once it's executed)
	Source        string      `json:"source"`             // the document
	Includes      []string    `json:"includes,omitempty"` // in-place: the dtt examples its .dtttc includes
	Images        []CallImage `json:"images"`
	ImagesOmitted int         `json:"images_omitted,omitempty"` // older ones past maxCallImages
	Recent        *RecentCall `json:"recent,omitempty"`         // its entry in the recent calls, if it's there
}

// calls lists the recent calls, or describes the one named.
func (c *cli) calls(cfg config, args []string) int {
	pos, err := parseArgs(newFlags("calls"), args)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	if len(pos) > 1 || (len(pos) == 1 && pos[0] == "-") {
		return c.fail(exitBadInput, fmt.Errorf("usage: dtt calls [<document|dir>]"))
	}
	rcs, rerr := RecentCalls{Calls: []RecentCall{}}, error(nil)
	if cfg.recentCalls != "" {
		rcs, rerr = readRecentCalls(cfg.recentCalls)
	}
	if len(pos) == 0 {
		if rerr != nil {
			return c.fail(exitToolFailed, rerr)
		}
		if c.json {
			c.emit(map[string]any{"ok": true, "file": cfg.recentCalls, "calls": rcs.Calls})
			return exitOK
		}
		if len(rcs.Calls) == 0 {
			fmt.Fprintf(c.stdout, "no recent calls (%s)\n", cfg.recentCalls)
			return exitOK
		}
		tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
		for _, rc := range rcs.Calls {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", rc.At, rc.Kind, rc.Mode, rc.Name, rc.Dir)
		}
		tw.Flush()
		return exitOK
	}
	dc, err := openDocFile(pos[0], cfg.pathlessDir)
	if err != nil {
		return c.fail(exitBadInput, trimBadInput(err))
	}
	if !strings.HasSuffix(dc.path, docFileSuffix) {
		return c.fail(exitBadInput, fmt.Errorf("%s isn't a call: a call's document is a *%s file (or a directory holding %s, or a pathless call's staging directory)", pos[0], docFileSuffix, dtttcFile))
	}
	info := CallInfo{Dir: dc.stage, Kind: dc.kind, Name: dc.name(), Document: dc.path, Source: dc.src, Includes: dc.includes, Images: []CallImage{}}
	if dc.stage != "" {
		if fi, err := os.Stat(dc.stage); err == nil && fi.IsDir() {
			info.Staged = true
		}
		if info.Images, info.ImagesOmitted, err = callImages(dc.stage); err != nil {
			return c.fail(exitToolFailed, err)
		}
	}
	for i := range rcs.Calls {
		if rcs.Calls[i].Dir == info.Dir && info.Dir != "" {
			info.Recent = &rcs.Calls[i]
		}
	}
	if c.json {
		c.emit(map[string]any{"ok": true, "call": info})
		return exitOK
	}
	fmt.Fprintf(c.stdout, "%s call %s\n  directory: %s\n  document:  %s\n", info.Kind, info.Name, info.Dir, info.Document)
	for _, inc := range info.Includes {
		fmt.Fprintf(c.stdout, "  includes:  dtt's %s\n", path.Join(examplesRoot, inc))
	}
	if len(info.Images) == 0 {
		fmt.Fprintln(c.stdout, "  no images kept yet")
	}
	for _, im := range info.Images {
		fmt.Fprintf(c.stdout, "  %s\n", im.Path)
	}
	if info.ImagesOmitted > 0 {
		fmt.Fprintf(c.stdout, "  ...and %d older\n", info.ImagesOmitted)
	}
	return exitOK
}

// callImages lists the images kept in stage/images, newest first, at most
// maxCallImages (omitted counts the rest).
func callImages(stage string) (images []CallImage, omitted int, err error) {
	images = []CallImage{}
	dir := filepath.Join(stage, imagesDirName)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return images, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".image.json") {
			continue
		}
		im := CallImage{Name: name, Path: filepath.Join(dir, name)}
		ts, rest, _ := strings.Cut(strings.TrimSuffix(name, ".image.json"), "-")
		if t, err := time.Parse(imageTimestamp, ts); err == nil {
			im.At = t.UTC().Format(time.RFC3339)
			im.Mode, _, _ = strings.Cut(rest, "-")
		}
		images = append(images, im)
	}
	// Names start with their UTC time, so newest first is by name, -2 after
	// the first of its second.
	sort.Slice(images, func(i, j int) bool {
		a, b := strings.TrimSuffix(images[i].Name, ".image.json"), strings.TrimSuffix(images[j].Name, ".image.json")
		if a[:min(len(a), len(imageTimestamp))] != b[:min(len(b), len(imageTimestamp))] {
			return a > b
		}
		return len(a) > len(b) || (len(a) == len(b) && a > b)
	})
	if len(images) > maxCallImages {
		omitted = len(images) - maxCallImages
		images = images[:maxCallImages]
	}
	return images, omitted, nil
}
