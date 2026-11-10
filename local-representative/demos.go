package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The demo tab's demos (PrivateRepoOps Step 4, Revision F). A demo is a DTT
// composition: a document whose phases each call one of dtt's examples
// (dtt.calls) and whose last phase has composer cut their renders into one
// video. Running it is one tool call -- the built-in declare tool, in
// assert mode, through the tools tab's own runs (tools.go) -- so it shows
// in the tools tab's recent runs and in dtt's call history like any other.
// What the demo tab adds is the record: each run is kept on this host, in a
// directory of its own, with the document as it ran and the image it
// returned.
//
// Demos follow the lead-in / tear-down shape the localrep-demo-* examples
// set: a general lead-in (localrep-demo-prepare), a lead-in for the subject
// area (localrep-demo-session-prepare, say), the subject itself, the area's
// tear-down and the general tear-down (localrep-demo-shutdown), then
// compose. A phase's role (DemoPhase.Role) is read from its name:
// "lead-in" or "<area>-lead-in", "tear-down" or "<area>-tear-down",
// "compose", and anything else is a subject. Repurposing a demo for
// another area means duplicating it and swapping the area's three calls.
//
// Everything lives under one host directory, the demo root (demoRootDir):
//
//	<root>/library/<name>.demo.dtt.yaml   the host's own demos (duplicates,
//	                                      edited in place)
//	<root>/records/<name>-<time>/         one run of a demo:
//	    record.json                       DemoRecord, rewritten when the run ends
//	    demo.dtt.yaml                     the document as it ran
//	    videos/<n>-<phase>-<name>          each video the run rendered (Revision G)
//	    artifacts/                        everything else the run left (Step 5):
//	        summary.md                    the record in words, for people and agents
//	        image.json                    the image dtt returned (image.json beside
//	                                      record.json in older records)
//	        outputs.txt                   each control run's outputs and errors
//	        log.txt                       the end of dtt's log
//	        screenshots/<n>-<phase>-<name> each screenshot a control run took
//	    analysis/                         frames pulled from the videos, made again from
//	                                      them whenever they're kept (demoanalysis.go)
//	    MAP.md                            the record's map: what each file is, lightest
//	                                      first, and the frames' tables (demoMap)
//	    reviews/<id>.json, <id>.md        the worker's reviews of it (demoreview.go)
//	<root>/suites/<name>.suite.yaml       the host's own suites (suites.go)
//	<root>/suite-records/<name>-<time>/   one run of a suite: suite.json
//
// A record has one primary video (DemoRender.Primary): the render of its
// first subject phase -- the demo's meat, without lead-in or tear-down --
// or, with none, the demo's own composition.
//
// The built-in demos (demos/ here) are embedded, so every LR has them; a
// run writes its document into its record and runs that file, so dtt finds
// the examples it calls among its own.
//
// A record keeps its videos itself (Revision G): the demo's own render and
// every render inside the documents it called, from the image, are linked
// (or copied, across filesystems) out of the files tab into videos/ when
// the run ends -- so they outlast the files tab's expiry, and a record's
// directory is the whole demo. They're served at
// /api/demo-records/<id>/videos/<file> (handleDemoRecordFile), which AC's
// /host/<id>/* proxy carries too; demo-keep-videos fetches them again for
// a record made before this, or one whose copy failed.

//go:embed demos/*.demo.dtt.yaml
var builtinDemosFS embed.FS

const (
	builtinDemosDir  = "demos"
	demoSuffix       = ".demo.dtt.yaml"
	demoTool         = "declare" // every provider: a demo may well use github too
	demoRecordFormat = "lr-demo-record-v1"
	demoRecordFile   = "record.json"
	demoDocFile      = "demo.dtt.yaml"
	demoImageFile    = "image.json"
	demoLibraryDir   = "library"
	demoRecordsDir   = "records"
	demoVideosDir    = "videos"
	demoArtifactsDir = "artifacts"
	demoShotsDir     = "screenshots"
	demoOutputsFile  = "outputs.txt"
	demoLogFile      = "log.txt"
	demoSummaryFile  = "summary.md"
	maxDemoRecords   = 200
	maxDemoCallDepth = 8 // how deep demoRenders follows calls inside calls
	maxDemoLogTail   = 4 << 10
	maxDemoSource    = 256 << 10
)

var (
	demoNameRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	demoRecordIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	demoVarNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// DemoVariable is one of a demo's variables, as its document declares it.
type DemoVariable struct {
	Name    string `json:"name"`
	Default string `json:"default,omitempty"`
	Help    string `json:"help,omitempty"`
}

// DemoPhase is one phase of a demo, in order: Role is lead-in, subject,
// tear-down or compose (see above), Calls the documents it calls.
type DemoPhase struct {
	Name  string   `json:"name"`
	Role  string   `json:"role"`
	Calls []string `json:"calls,omitempty"`
}

// A demo's or suite's origin (Revision N): embedded -- in-tree in
// AI-evo1-spiral, built into LR (demos/, suites/ here) -- or configured --
// on this host's filesystem, in the demo root's library/ or suites/, kept in
// no repo: a duplicate, one put there by hand, or one a proposal made
// (demoproposals.go).
const (
	demoOriginEmbedded   = "embedded"
	demoOriginConfigured = "configured"
)

// DemoInfo is one demo in the library. Origin is embedded or configured
// (Path is then its file).
type DemoInfo struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Origin      string         `json:"origin"`
	Path        string         `json:"path,omitempty"`
	Variables   []DemoVariable `json:"variables"`
	Phases      []DemoPhase    `json:"phases"`
	Source      string         `json:"source,omitempty"`
	Error       string         `json:"error,omitempty"`
	// Coverage is what of the project it exercises, focal and supporting
	// (Revision C, democoverage.go).
	Coverage *DemoCoverage `json:"coverage,omitempty"`
	// SUT is the kind of system under test it runs on: "local" (LSUT, the
	// default -- a node picked through its node variable) or "same" (SSUT,
	// declared by a "#   sut: same" header line -- the node running it,
	// with no node to pick).
	SUT string `json:"sut"`
}

// demoSUTLine reads a "#   sut: same" (or local) header line.
func demoSUTLine(line string) (string, bool) {
	text := strings.TrimSpace(strings.TrimPrefix(line, "#"))
	key, rest, found := strings.Cut(text, ":")
	if !found || key != "sut" || !strings.HasPrefix(line, "#   ") {
		return "", false
	}
	switch v := strings.TrimSpace(rest); v {
	case "same", "local":
		return v, true
	}
	return "", false
}

// DemoRecord is one run of a demo, as record.json keeps it. Status is
// running, ok or failed -- or interrupted, for a running record whose run
// this LR no longer holds (it restarted). UndoOf is the record a delete
// run undid. The times are unix ms.
type DemoRecord struct {
	Format     string            `json:"format"`
	ID         string            `json:"id"`
	Demo       string            `json:"demo"`
	Title      string            `json:"title"`
	Mode       string            `json:"mode"`
	UndoOf     string            `json:"undo_of,omitempty"`
	Variables  map[string]string `json:"variables"`
	Node       string            `json:"node"`
	Origin     string            `json:"origin"`
	Tool       string            `json:"tool"`
	RunID      string            `json:"run_id,omitempty"`
	Status     string            `json:"status"`
	StartedAt  int64             `json:"started_at"`
	FinishedAt int64             `json:"finished_at,omitempty"`
	Error      string            `json:"error,omitempty"`
	Summary    map[string]int    `json:"summary,omitempty"`
	Phases     []DemoRecordPhase `json:"phases,omitempty"`
	Renders    []DemoRender      `json:"renders,omitempty"`
	HasImage   bool              `json:"has_image"`
	LogTail    string            `json:"log_tail,omitempty"`
	Dir        string            `json:"dir"`
	// Artifacts are the files in artifacts/ (Step 5); Suite and
	// SuiteRecord the suite run this was one demo of.
	Artifacts   []DemoArtifact `json:"artifacts,omitempty"`
	Suite       string         `json:"suite,omitempty"`
	SuiteRecord string         `json:"suite_record,omitempty"`
	// Analysis is what analysis/ holds (Revision A, demoanalysis.go).
	Analysis *DemoAnalysis `json:"analysis,omitempty"`
}

// DemoArtifact is a file in a record's artifacts/: File is its path there
// ("screenshots/01-read-x.png"), Kind summary, image, outputs, log or
// screenshot. A screenshot that couldn't be kept has KeepError and no
// File; FileID is where it was in the files tab.
type DemoArtifact struct {
	Kind      string `json:"kind"`
	File      string `json:"file,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Call      string `json:"call,omitempty"`
	FileID    string `json:"file_id,omitempty"`
	KeepError string `json:"keep_error,omitempty"`
}

// DemoRecordPhase is how one of the run's phases went, from its image.
type DemoRecordPhase struct {
	Name       string `json:"name"`
	Role       string `json:"role"`
	Status     string `json:"status"` // not-run, done or failed
	DurationMs int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

// DemoRender is a video the run rendered: the demo's own composition (Call
// "") or one inside a document it called (Call is the call's alias, or the
// aliases down to it, "prep/inner"). Phase is the demo's phase it came in.
// FileID is where it was in the files tab; Kept is its file in the record's
// videos/ once kept there, KeepError why it isn't.
type DemoRender struct {
	Composition string `json:"composition"`
	Phase       string `json:"phase,omitempty"`
	Call        string `json:"call,omitempty"`
	FileID      string `json:"file_id"`
	Name        string `json:"name"`
	Kept        string `json:"kept,omitempty"`
	Size        int64  `json:"size,omitempty"`
	KeepError   string `json:"keep_error,omitempty"`
	Primary     bool   `json:"primary,omitempty"` // the record's one primary video (markPrimaryRender)
}

// demoRootDir is where demos are kept on this host: <records root>/demos
// beside dtt's call history, or LR's config directory when there's no
// records root.
func demoRootDir(recordsRoot string) string {
	if recordsRoot != "" {
		return filepath.Join(recordsRoot, "demos")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "local-representative", "demos")
}

func (w *toolsWatch) demoDir() string {
	if w == nil || w.demoRoot == nil {
		return ""
	}
	return w.demoRoot()
}

// ---- The library ----

// demoPhaseRole reads a phase's role from its name.
func demoPhaseRole(name string) string {
	switch {
	case name == "lead-in" || strings.HasSuffix(name, "-lead-in"):
		return "lead-in"
	case name == "tear-down" || strings.HasSuffix(name, "-tear-down"):
		return "tear-down"
	case name == "compose":
		return "compose"
	}
	return "subject"
}

// demoScalar is a plain or quoted YAML scalar's value.
func demoScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// parseDemo reads what the tab shows of a demo from its document, line by
// line rather than as YAML: a DTT document repeats its top-level keys
// (dtt.calls), which the control library's YAML reader refuses, and dtt
// checks the document itself when it runs. The leading comment block gives
// the title (its first line) and description ("#   dtt ..." command lines
// left out, as are "#   focal:" and "#   supporting:" ones, its declared
// coverage, and a "#   sut:" one, its kind of system under test); the
// variables block the variables; each statement's phase: and, for a call,
// its document: the phases.
func parseDemo(name, src, origin, file string) DemoInfo {
	d := DemoInfo{Name: name, Origin: origin, Path: file, Source: src, Variables: []DemoVariable{}, Phases: []DemoPhase{}, SUT: "local"}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")

	var desc, focal, supporting []string
	i := 0
	for ; i < len(lines) && strings.HasPrefix(lines[i], "#"); i++ {
		text := strings.TrimPrefix(strings.TrimPrefix(lines[i], "#"), " ")
		kind, paths, isCoverage := demoCoverageLine(lines[i])
		sut, isSUT := demoSUTLine(lines[i])
		switch {
		case i == 0:
			d.Title = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "Demo --"))
		case strings.HasPrefix(lines[i], "#   dtt "):
		case isSUT:
			d.SUT = sut
		case isCoverage && kind == "focal":
			focal = append(focal, paths...)
		case isCoverage:
			supporting = append(supporting, paths...)
		default:
			desc = append(desc, strings.TrimRight(text, " "))
		}
	}
	d.Description = strings.TrimSpace(strings.Join(desc, "\n"))
	if d.Title == "" {
		d.Title = name
	}

	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	phaseAt := map[string]int{}
	current := -1 // the phase a statement without phase: carries on
	var stmt string
	var stmtPhase, stmtDoc string
	flush := func() {
		if stmt == "" {
			return
		}
		if stmtPhase != "" {
			n, ok := phaseAt[stmtPhase]
			if !ok {
				n = len(d.Phases)
				phaseAt[stmtPhase] = n
				d.Phases = append(d.Phases, DemoPhase{Name: stmtPhase, Role: demoPhaseRole(stmtPhase)})
			}
			current = n
		}
		if stmt == "dtt.calls" && stmtDoc != "" && current >= 0 {
			d.Phases[current].Calls = append(d.Phases[current].Calls, stmtDoc)
		}
		stmt, stmtPhase, stmtDoc = "", "", ""
	}
	inVars := false
	var v *DemoVariable
	statements := 0
	for ; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		key, rest, _ := strings.Cut(trim, ":")
		switch ind := indent(line); {
		case ind == 0:
			flush()
			if v != nil {
				d.Variables = append(d.Variables, *v)
				v = nil
			}
			inVars = key == "variables"
			if !inVars {
				stmt = key
				statements++
			}
		case inVars && ind == 2:
			if v != nil {
				d.Variables = append(d.Variables, *v)
			}
			v = &DemoVariable{Name: key}
		case inVars && ind == 4 && v != nil:
			switch key {
			case "default":
				v.Default = demoScalar(rest)
			case "help":
				v.Help = demoScalar(rest)
			}
		case stmt != "" && ind == 2 && key == "phase":
			stmtPhase = demoScalar(rest)
		case stmt != "" && ind == 4 && key == "document":
			stmtDoc = demoScalar(rest)
		}
	}
	flush()
	if v != nil {
		d.Variables = append(d.Variables, *v)
	}
	if statements == 0 {
		d.Error = "the document has no statements"
	}
	d.Coverage = demoCoverage(d, focal, supporting)
	return d
}

// builtinDemos are the demos embedded in LR, by name.
func builtinDemos() []DemoInfo {
	entries, _ := fs.ReadDir(builtinDemosFS, builtinDemosDir)
	var out []DemoInfo
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), demoSuffix) {
			continue
		}
		data, err := builtinDemosFS.ReadFile(path.Join(builtinDemosDir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, parseDemo(strings.TrimSuffix(e.Name(), demoSuffix), string(data), demoOriginEmbedded, ""))
	}
	return out
}

// hostDemos are the demos in <root>/library, by name; a missing library is
// none.
func hostDemos(root string) []DemoInfo {
	if root == "" {
		return nil
	}
	dir := filepath.Join(root, demoLibraryDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []DemoInfo
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), demoSuffix)
		if e.IsDir() || !strings.HasSuffix(e.Name(), demoSuffix) || !demoNameRe.MatchString(name) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			out = append(out, DemoInfo{Name: name, Title: name, Origin: demoOriginConfigured, Path: p, Variables: []DemoVariable{}, Phases: []DemoPhase{}, Error: err.Error()})
			continue
		}
		if len(data) > maxDemoSource {
			out = append(out, DemoInfo{Name: name, Title: name, Origin: demoOriginConfigured, Path: p, Variables: []DemoVariable{}, Phases: []DemoPhase{}, Error: "the document is too large to be a demo"})
			continue
		}
		out = append(out, parseDemo(name, string(data), demoOriginConfigured, p))
	}
	return out
}

// demoLibrary is the built-in demos, then the host's. A host demo can't
// take a built-in one's name (demo-duplicate refuses it), so names are
// unique; one put there by hand that does is left out.
func (w *toolsWatch) demoLibrary() []DemoInfo {
	out := builtinDemos()
	seen := map[string]bool{}
	for _, d := range out {
		seen[d.Name] = true
	}
	for _, d := range hostDemos(w.demoDir()) {
		if !seen[d.Name] {
			out = append(out, d)
		}
	}
	if out == nil {
		out = []DemoInfo{}
	}
	return out
}

func (w *toolsWatch) findDemo(name string) (DemoInfo, error) {
	name = strings.TrimSpace(name)
	for _, d := range w.demoLibrary() {
		if d.Name == name {
			if d.Error != "" {
				return d, fmt.Errorf("demo %s: %s", name, d.Error)
			}
			return d, nil
		}
	}
	return DemoInfo{}, fmt.Errorf("no demo %q in the library", name)
}

// ---- Records ----

func writeDemoRecord(rec DemoRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(rec.Dir, demoRecordFile+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(rec.Dir, demoRecordFile))
}

func readDemoRecord(dir string) (DemoRecord, error) {
	var rec DemoRecord
	data, err := os.ReadFile(filepath.Join(dir, demoRecordFile))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, err
	}
	if rec.Format != demoRecordFormat {
		return rec, fmt.Errorf("format %q isn't %s", rec.Format, demoRecordFormat)
	}
	rec.ID, rec.Dir = filepath.Base(dir), dir
	return rec, nil
}

// settle marks a running record whose run this LR no longer holds as
// interrupted: LR restarted while it ran, so it never heard how it ended.
func (w *toolsWatch) settle(rec DemoRecord) DemoRecord {
	if rec.Status == "running" && (rec.RunID == "" || w.runCopy(rec.RunID) == nil) {
		rec.Status = "interrupted"
		if rec.Error == "" {
			rec.Error = "local-representative stopped while the demo ran, so how it ended wasn't recorded -- see the files tab and dtt's call history"
		}
	}
	return rec
}

// demoRecords lists the records under root, newest first, at most
// maxDemoRecords. One that can't be read (being written, say) is skipped.
func (w *toolsWatch) demoRecords(root string) []DemoRecord {
	out := []DemoRecord{}
	if root == "" {
		return out
	}
	dir := filepath.Join(root, demoRecordsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || !demoRecordIDRe.MatchString(e.Name()) {
			continue
		}
		rec, err := readDemoRecord(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		rec.LogTail = "" // in demo-record only
		out = append(out, w.settle(rec))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	if len(out) > maxDemoRecords {
		out = out[:maxDemoRecords]
	}
	return out
}

func (w *toolsWatch) demoRecord(root, id string) (DemoRecord, error) {
	if !demoRecordIDRe.MatchString(id) {
		return DemoRecord{}, fmt.Errorf("%q isn't a demo record", id)
	}
	rec, err := readDemoRecord(filepath.Join(root, demoRecordsDir, id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return rec, fmt.Errorf("no demo record %s on this host", id)
		}
		return rec, fmt.Errorf("demo record %s: %v", id, err)
	}
	return w.settle(rec), nil
}

// newDemoRecordDir makes <root>/records/<demo>-<time>, with a -<n> suffix
// when two runs start in the same second.
func newDemoRecordDir(root, demo string, at time.Time) (string, error) {
	return newRecordDir(filepath.Join(root, demoRecordsDir), demo, at)
}

// newRecordDir makes <base>/<name>-<time>, as newDemoRecordDir does.
func newRecordDir(base, name string, at time.Time) (string, error) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	id := name + "-" + at.Format("20060102-150405")
	for n := 1; ; n++ {
		p := filepath.Join(base, id)
		if n > 1 {
			p = fmt.Sprintf("%s-%d", p, n)
		}
		err := os.Mkdir(p, 0o755)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, fs.ErrExist) || n >= 100 {
			return "", err
		}
	}
}

// demoImage is the part of dtt's image (dtt/declare.go's Image) a record
// keeps.
type demoImage struct {
	OK      bool           `json:"ok"`
	Summary map[string]int `json:"summary"`
	Error   string         `json:"error"`
	Phases  []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		DurationMs int64  `json:"duration_ms"`
		Error      string `json:"error"`
	} `json:"phases"`
	Objects []struct {
		ID    string         `json:"id"`
		Path  string         `json:"path"`
		Alias string         `json:"alias"`
		Phase string         `json:"phase"`
		State map[string]any `json:"state"`
	} `json:"objects"`
}

// demoRenders are the renders in img: its own compositions' first, then
// those inside each call's image (a dtt.calls object keeps its document's
// whole image in state.image), in order, to maxDemoCallDepth. phase is the
// demo's phase the calls came in ("" at the top, where each object's own
// is), call the aliases down to img.
func demoRenders(img demoImage, phase, call string, depth int) []DemoRender {
	var out, inner []DemoRender
	for _, o := range img.Objects {
		at := phase
		if at == "" {
			at = o.Phase
		}
		switch o.Path {
		case "localrep.compositions":
			r, _ := o.State["render"].(map[string]any)
			id, _ := r["file_id"].(string)
			name, _ := r["name"].(string)
			if id != "" {
				out = append(out, DemoRender{Composition: o.ID, Phase: at, Call: call, FileID: id, Name: name})
			}
		case "dtt.calls":
			sub, ok := o.State["image"]
			if !ok || depth >= maxDemoCallDepth {
				continue
			}
			b, err := json.Marshal(sub)
			if err != nil {
				continue
			}
			var subImg demoImage
			if json.Unmarshal(b, &subImg) != nil {
				continue
			}
			alias := o.Alias
			if alias == "" {
				alias = o.ID
			}
			if call != "" {
				alias = call + "/" + alias
			}
			inner = append(inner, demoRenders(subImg, at, alias, depth+1)...)
		}
	}
	return append(out, inner...)
}

// demoVideoName is render n's file in videos/: its number, phase and
// name, made safe, so the directory lists in the demo's order.
func demoVideoName(n int, r DemoRender) string {
	name := r.Name
	if name == "" {
		name = displayName(r.FileID)
	}
	name = sanitizeFilename(name)
	if r.Phase != "" {
		name = sanitizeFilename(r.Phase) + "-" + name
	}
	return fmt.Sprintf("%02d-%s", n+1, name)
}

// keepDemoVideos links or copies each of rec's renders that isn't kept yet
// out of the files tab into the record's videos/, noting where each went
// or why it couldn't.
func (w *toolsWatch) keepDemoVideos(rec *DemoRecord) {
	for i := range rec.Renders {
		r := &rec.Renders[i]
		if r.Kept != "" {
			if _, err := os.Stat(filepath.Join(rec.Dir, demoVideosDir, r.Kept)); err == nil {
				continue
			}
			r.Kept = "" // removed by hand: fetch it again
		}
		name := demoVideoName(i, *r)
		size, err := w.keepDemoVideo(r.FileID, filepath.Join(rec.Dir, demoVideosDir), name)
		if err != nil {
			r.KeepError = err.Error()
		} else {
			r.Kept, r.Size, r.KeepError = name, size, ""
		}
	}
}

// keepDemoVideo puts files-tab entry id into dir as name: a hard link when
// it can (the same bytes, and the files tab's sweep can't take them), a
// copy when not.
func (w *toolsWatch) keepDemoVideo(id, dir, name string) (int64, error) {
	if id == "" || id != filepath.Base(id) || strings.HasPrefix(id, ".") {
		return 0, fmt.Errorf("%q isn't a files-tab entry", id)
	}
	if w.demoFile == nil {
		return 0, errors.New("there's no files tab here to keep it from")
	}
	src, ok := w.demoFile(id)
	if !ok {
		return 0, fmt.Errorf("%s isn't in this host's files tab (any more) -- it may have expired, or been rendered on another node", id)
	}
	info, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	dst := filepath.Join(dir, name)
	os.Remove(dst)
	if err := os.Link(src, dst); err == nil {
		return info.Size(), nil
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("copying %s: %v", id, err)
	}
	return n, nil
}

// markPrimaryRender marks renders' primary video: the first one from a
// subject phase (the demo's meat, without lead-in or tear-down), else the
// demo's own composition, else the first.
func markPrimaryRender(renders []DemoRender) {
	pick := -1
	for i, r := range renders {
		renders[i].Primary = false
		if pick < 0 && r.Phase != "" && demoPhaseRole(r.Phase) == "subject" {
			pick = i
		}
	}
	for i, r := range renders {
		if pick < 0 && r.Call == "" {
			pick = i
		}
	}
	if pick < 0 && len(renders) > 0 {
		pick = 0
	}
	if pick >= 0 {
		renders[pick].Primary = true
	}
}

// primaryRender is renders' primary video, or nil.
func primaryRender(renders []DemoRender) *DemoRender {
	for i := range renders {
		if renders[i].Primary {
			return &renders[i]
		}
	}
	return nil
}

// demoRunOutput is one control run inside a demo's image: what it printed
// (Output, by label, in label order), how it ended, the screenshots it
// took (its recordings that aren't videos), and -- for analysis/
// (demoanalysis.go) -- its steps and its video recordings, with when each
// started.
type demoRunOutput struct {
	Phase, Call, Sequence, Key, Status, Error string
	Output                                    [][2]string
	Shots                                     []DemoRender
	Steps                                     []demoRunStep
	Videos                                    []demoRunVideo
}

// demoRunStep is one step of a control run: StartedAt is unix ms on the
// run's LR, as its videos' are (0 from an LR before Revision A of Step 5).
type demoRunStep struct {
	Label, Status, Message string
	StartedAt, DurationMs  int64
}

// demoRunVideo is a video a control run recorded, of steps From to To
// (1-based).
type demoRunVideo struct {
	FileID, Name          string
	From, To              int
	StartedAt, DurationMs int64
}

// stateInt is a whole number from an image's state, where JSON made it a
// float64.
func stateInt(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// demoRunOutputs are the control runs in img, as demoRenders finds the
// renders: its own first, then those inside each call's image.
func demoRunOutputs(img demoImage, phase, call string, depth int) []demoRunOutput {
	var out, inner []demoRunOutput
	for _, o := range img.Objects {
		at := phase
		if at == "" {
			at = o.Phase
		}
		switch o.Path {
		case "localrep.control.runs":
			r := demoRunOutput{Phase: at, Call: call}
			r.Sequence, _ = o.State["sequence"].(string)
			r.Key, _ = o.State["key"].(string)
			r.Status, _ = o.State["status"].(string)
			r.Error, _ = o.State["error"].(string)
			if m, ok := o.State["output"].(map[string]any); ok {
				labels := make([]string, 0, len(m))
				for k := range m {
					labels = append(labels, k)
				}
				sort.Strings(labels)
				for _, k := range labels {
					r.Output = append(r.Output, [2]string{k, fmt.Sprint(m[k])})
				}
			}
			recs, _ := o.State["recordings"].([]any)
			for _, x := range recs {
				m, _ := x.(map[string]any)
				id, _ := m["file_id"].(string)
				name, _ := m["name"].(string)
				if id == "" {
					continue
				}
				if video, _ := m["video"].(bool); video {
					r.Videos = append(r.Videos, demoRunVideo{FileID: id, Name: name, From: int(stateInt(m, "from")), To: int(stateInt(m, "to")),
						StartedAt: stateInt(m, "started_at"), DurationMs: stateInt(m, "duration_ms")})
					continue
				}
				r.Shots = append(r.Shots, DemoRender{Phase: at, Call: call, FileID: id, Name: name})
			}
			steps, _ := o.State["steps"].([]any)
			for _, x := range steps {
				m, _ := x.(map[string]any)
				st := demoRunStep{StartedAt: stateInt(m, "started_at"), DurationMs: stateInt(m, "duration_ms")}
				st.Label, _ = m["label"].(string)
				st.Status, _ = m["status"].(string)
				st.Message, _ = m["message"].(string)
				r.Steps = append(r.Steps, st)
			}
			out = append(out, r)
		case "dtt.calls":
			sub, ok := o.State["image"]
			if !ok || depth >= maxDemoCallDepth {
				continue
			}
			b, err := json.Marshal(sub)
			if err != nil {
				continue
			}
			var subImg demoImage
			if json.Unmarshal(b, &subImg) != nil {
				continue
			}
			alias := o.Alias
			if alias == "" {
				alias = o.ID
			}
			if call != "" {
				alias = call + "/" + alias
			}
			inner = append(inner, demoRunOutputs(subImg, at, alias, depth+1)...)
		}
	}
	return append(out, inner...)
}

func fileSize(p string) int64 {
	if info, err := os.Stat(p); err == nil {
		return info.Size()
	}
	return 0
}

// keepDemoArtifacts fills rec's artifacts/ beyond the image, which
// finishDemoRecord has written: the log, the control runs' outputs, their
// screenshots (out of the files tab, as the videos are) and, last, the
// summary that names them all. rec.Artifacts lists what is there.
func (w *toolsWatch) keepDemoArtifacts(rec *DemoRecord, runs []demoRunOutput) {
	dir := filepath.Join(rec.Dir, demoArtifactsDir)
	add := func(kind, name, content string) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err == nil {
			rec.Artifacts = append(rec.Artifacts, DemoArtifact{Kind: kind, File: name, Size: int64(len(content))})
		}
	}
	if strings.TrimSpace(rec.LogTail) != "" {
		add("log", demoLogFile, rec.LogTail+"\n")
	}
	if len(runs) > 0 {
		var b strings.Builder
		n := 0
		for _, r := range runs {
			fmt.Fprintf(&b, "== %s run %s (%s)", r.Phase, r.Key, r.Status)
			if r.Call != "" {
				fmt.Fprintf(&b, ", inside call %s", r.Call)
			}
			b.WriteString(" ==\n")
			for _, kv := range r.Output {
				fmt.Fprintf(&b, "%s: %s\n", kv[0], kv[1])
			}
			if r.Error != "" {
				fmt.Fprintf(&b, "error: %s\n", r.Error)
			}
			b.WriteString("\n")
			for _, s := range r.Shots {
				shot := DemoArtifact{Kind: "screenshot", Phase: s.Phase, Call: s.Call, FileID: s.FileID}
				name := demoVideoName(n, s)
				n++
				size, err := w.keepDemoVideo(s.FileID, filepath.Join(dir, demoShotsDir), name)
				if err != nil {
					shot.KeepError = err.Error()
				} else {
					shot.File, shot.Size = demoShotsDir+"/"+name, size
				}
				rec.Artifacts = append(rec.Artifacts, shot)
			}
		}
		add("outputs", demoOutputsFile, b.String())
	}
	add("summary", demoSummaryFile, demoSummary(*rec))
}

// demoSummary is a record in words: what ran, how each phase went, the
// primary video and every other file the record keeps -- the first thing a
// reviewing agent reads (demoreview.go).
func demoSummary(rec DemoRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Demo record %s\n\n", rec.ID)
	fmt.Fprintf(&b, "- demo: %s (%s)\n- mode: %s\n- status: %s\n", rec.Demo, rec.Title, rec.Mode, rec.Status)
	if rec.Suite != "" {
		fmt.Fprintf(&b, "- suite: %s (run %s)\n", rec.Suite, rec.SuiteRecord)
	}
	fmt.Fprintf(&b, "- node: %s, started by %s\n", rec.Node, rec.Origin)
	if rec.StartedAt > 0 {
		fmt.Fprintf(&b, "- started: %s", time.UnixMilli(rec.StartedAt).UTC().Format(time.RFC3339))
		if rec.FinishedAt > rec.StartedAt {
			fmt.Fprintf(&b, ", took %s", time.Duration(rec.FinishedAt-rec.StartedAt)*time.Millisecond)
		}
		b.WriteString("\n")
	}
	if len(rec.Variables) > 0 {
		names := make([]string, 0, len(rec.Variables))
		for k := range rec.Variables {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			fmt.Fprintf(&b, "- variable %s = %s\n", k, rec.Variables[k])
		}
	}
	if rec.Error != "" {
		fmt.Fprintf(&b, "- error: %s\n", rec.Error)
	}
	if len(rec.Phases) > 0 {
		b.WriteString("\n## Phases\n\n")
		for _, p := range rec.Phases {
			fmt.Fprintf(&b, "- %s (%s): %s", p.Name, p.Role, p.Status)
			if p.DurationMs > 0 {
				fmt.Fprintf(&b, " in %s", time.Duration(p.DurationMs)*time.Millisecond)
			}
			if p.Error != "" {
				fmt.Fprintf(&b, " -- %s", p.Error)
			}
			b.WriteString("\n")
		}
	}
	if len(rec.Renders) > 0 {
		b.WriteString("\n## Videos (videos/)\n\n")
		for _, r := range rec.Renders {
			where := "not kept: " + r.KeepError
			if r.Kept != "" {
				where = demoVideosDir + "/" + r.Kept
			}
			mark := ""
			if r.Primary {
				mark = " **primary**"
			}
			fmt.Fprintf(&b, "- %s%s: %s (phase %s)\n", r.Composition, mark, where, r.Phase)
		}
	}
	b.WriteString("\n## Artifacts (artifacts/)\n\n")
	for _, a := range rec.Artifacts {
		switch {
		case a.File != "":
			fmt.Fprintf(&b, "- %s: %s\n", a.Kind, a.File)
		case a.KeepError != "":
			fmt.Fprintf(&b, "- %s from phase %s: not kept -- %s\n", a.Kind, a.Phase, a.KeepError)
		}
	}
	fmt.Fprintf(&b, "- summary: %s\n", demoSummaryFile)
	if a := rec.Analysis; a != nil {
		b.WriteString("\n## Analysis (analysis/)\n\n")
		if a.Video != "" {
			fmt.Fprintf(&b, "- from the primary video, %s: %d frames at step boundaries, %d at scene changes\n", a.Video, a.StepFrames, a.SceneFrames)
		}
		if a.FailureFrames > 0 {
			fmt.Fprintf(&b, "- the demo failed: %d more frames, in analysis/failure/\n", a.FailureFrames)
		}
		for _, n := range a.Notes {
			fmt.Fprintf(&b, "- note: %s\n", n)
		}
	}
	fmt.Fprintf(&b, "\n%s, beside record.json, maps every file in the record, with each frame's time and step.\n", demoMapFile)
	return b.String()
}

// demoImagePath is where record dir's image is: artifacts/image.json, or
// image.json beside record.json in a record from before Step 5.
func demoImagePath(dir string) string {
	p := filepath.Join(dir, demoArtifactsDir, demoImageFile)
	if _, err := os.Stat(p); err != nil {
		if _, err := os.Stat(filepath.Join(dir, demoImageFile)); err == nil {
			return filepath.Join(dir, demoImageFile)
		}
	}
	return p
}

// finishDemoRecord fills rec in from its finished run and writes it, with
// the image, the artifacts and the videos beside it.
func (w *toolsWatch) finishDemoRecord(rec DemoRecord, run ToolRun) DemoRecord {
	rec.Status = run.Status
	rec.FinishedAt = run.FinishedAt
	if rec.FinishedAt == 0 {
		rec.FinishedAt = time.Now().UnixMilli()
	}
	var image string
	if res := run.Result; res != nil {
		rec.Error = res.Error
		rec.LogTail = res.LogTail
		if len(rec.LogTail) > maxDemoLogTail {
			rec.LogTail = "..." + rec.LogTail[len(rec.LogTail)-maxDemoLogTail:]
		}
		image = res.Outputs[toolImageOutput]
		if image == "" {
			for _, f := range res.Files {
				if f.Name == "image" {
					if b, err := os.ReadFile(f.Path); err == nil {
						image = string(b)
					}
				}
			}
		}
	}
	artifacts := filepath.Join(rec.Dir, demoArtifactsDir)
	if err := os.MkdirAll(artifacts, 0o755); err != nil && rec.Error == "" {
		rec.Error = "making artifacts/: " + err.Error()
	}
	var runs []demoRunOutput
	var img demoImage
	analyse := false
	if strings.TrimSpace(image) != "" {
		if err := json.Unmarshal([]byte(image), &img); err == nil {
			rec.Summary = img.Summary
			if rec.Error == "" {
				rec.Error = img.Error
			}
			for _, p := range img.Phases {
				rec.Phases = append(rec.Phases, DemoRecordPhase{Name: p.Name, Role: demoPhaseRole(p.Name), Status: p.Status, DurationMs: p.DurationMs, Error: p.Error})
			}
			// Only an assert renders: a read's or delete's image shows the
			// renders some earlier run made, which aren't this record's --
			// nor its runs' screenshots and outputs.
			if rec.Mode == "assert" {
				rec.Renders = demoRenders(img, "", "", 0)
				markPrimaryRender(rec.Renders)
				w.keepDemoVideos(&rec)
				runs = demoRunOutputs(img, "", "", 0)
				analyse = true
			}
		}
		if err := os.WriteFile(filepath.Join(artifacts, demoImageFile), []byte(prettyJSONText(image)+"\n"), 0o644); err == nil {
			rec.HasImage = true
			rec.Artifacts = append(rec.Artifacts, DemoArtifact{Kind: "image", File: demoImageFile, Size: fileSize(filepath.Join(artifacts, demoImageFile))})
		} else if rec.Error == "" {
			rec.Error = "keeping the image: " + err.Error()
		}
	}
	if analyse {
		w.analyseDemoRecord(&rec, img, runs)
	}
	w.keepDemoArtifacts(&rec, runs)
	// The map before the record, so a record that's no longer running has
	// its map.
	writeDemoMap(rec)
	if err := writeDemoRecord(rec); err != nil {
		fmt.Fprintf(os.Stderr, "demo record %s: %v\n", rec.ID, err)
	}
	return rec
}

// ---- Ops ----

// demoRunOpts are a suite's (suites.go) on one of its demos' runs: the
// suite and its record, which the demo's record notes, and done, called
// with the finished record once the demo ends.
type demoRunOpts struct {
	suite, suiteRecord string
	done               func(DemoRecord)
}

// runDemo starts demo-run. An assert or read runs the demo by name; a
// delete undoes record req.Record's run with its image, from the document
// it ran.
func (w *toolsWatch) runDemo(req ToolsRequest, fromAC bool, node string, opts demoRunOpts) (DemoRecord, error) {
	root := w.demoDir()
	if root == "" {
		return DemoRecord{}, errors.New("there's no directory on this host to keep demo records in")
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "assert"
	}
	rec := DemoRecord{Format: demoRecordFormat, Mode: mode, Node: node, Tool: demoTool, Status: "running", Variables: map[string]string{},
		Suite: opts.suite, SuiteRecord: opts.suiteRecord}
	rec.Origin = "demo tab"
	if fromAC {
		rec.Origin = "agent-coordinator"
	}
	var src, fromImage string
	switch mode {
	case "assert", "read":
		d, err := w.findDemo(req.Demo)
		if err != nil {
			return rec, err
		}
		rec.Demo, rec.Title, src = d.Name, d.Title, d.Source
		for k, v := range req.Variables {
			if !demoVarNameRe.MatchString(k) {
				return rec, fmt.Errorf("%q isn't a variable name", k)
			}
			if strings.ContainsAny(v, "\r\n") {
				return rec, fmt.Errorf("variable %s: one line only", k)
			}
			if v = strings.TrimSpace(v); v != "" {
				rec.Variables[k] = v
			}
		}
	case "delete":
		was, err := w.demoRecord(root, strings.TrimSpace(req.Record))
		if err != nil {
			return rec, err
		}
		if was.Mode != "assert" || !was.HasImage {
			return rec, fmt.Errorf("record %s isn't an assert with an image, so there's nothing it made to delete", was.ID)
		}
		b, err := os.ReadFile(filepath.Join(was.Dir, demoDocFile))
		if err != nil {
			return rec, fmt.Errorf("record %s's document: %v", was.ID, err)
		}
		rec.Demo, rec.Title, rec.UndoOf, src = was.Demo, was.Title, was.ID, string(b)
		for k, v := range was.Variables {
			rec.Variables[k] = v
		}
		fromImage = demoImagePath(was.Dir)
	default:
		return rec, fmt.Errorf("mode %q should be assert, read or delete", mode)
	}

	w.demoMu.Lock()
	defer w.demoMu.Unlock()
	if w.demoActive != "" {
		return rec, fmt.Errorf("demo record %s is still running -- one demo runs at a time", w.demoActive)
	}
	if w.suiteActive != "" && w.suiteActive != opts.suiteRecord {
		return rec, fmt.Errorf("suite record %s is still running its demos -- one demo runs at a time", w.suiteActive)
	}
	now := time.Now()
	dir, err := newDemoRecordDir(root, rec.Demo, now)
	if err != nil {
		return rec, fmt.Errorf("making the record's directory: %v", err)
	}
	rec.ID, rec.Dir, rec.StartedAt = filepath.Base(dir), dir, now.UnixMilli()
	doc := filepath.Join(dir, demoDocFile)
	if err := os.WriteFile(doc, []byte(src), 0o644); err != nil {
		os.RemoveAll(dir)
		return rec, fmt.Errorf("writing the record's document: %v", err)
	}

	names := make([]string, 0, len(rec.Variables))
	for k := range rec.Variables {
		names = append(names, k)
	}
	sort.Strings(names)
	var vars []string
	for _, k := range names {
		vars = append(vars, k+"="+rec.Variables[k])
	}
	args := map[string]string{"document_file": doc, "mode": mode}
	if len(vars) > 0 {
		args["variables"] = strings.Join(vars, "\n")
	}
	if fromImage != "" {
		args["from_image"] = fromImage
	}
	if err := writeDemoRecord(rec); err != nil {
		os.RemoveAll(dir)
		return rec, fmt.Errorf("writing the record: %v", err)
	}
	started := rec
	id, err := w.startRunThen(demoTool, args, "demo tab ("+rec.Demo+")", fromAC, func(run ToolRun) {
		// After runDemo has written the running record (it holds demoMu
		// until then), never before.
		w.demoMu.Lock()
		r := started
		r.RunID = run.ID
		r = w.finishDemoRecord(r, run)
		if w.demoActive == r.ID {
			w.demoActive = ""
		}
		w.demoMu.Unlock()
		if w.notify != nil {
			w.notify()
		}
		if opts.done != nil {
			opts.done(r)
		}
	})
	if err != nil {
		os.RemoveAll(dir)
		return rec, err
	}
	rec.RunID = id
	w.demoActive = rec.ID
	if err := writeDemoRecord(rec); err != nil {
		return rec, fmt.Errorf("the demo is running, but its record couldn't be updated: %v", err)
	}
	return rec, nil
}

// keepRecordVideos has record id keep the videos it hasn't yet: for a record
// from before Revision G it reads the renders from its image first.
func (w *toolsWatch) keepRecordVideos(root, id string) (DemoRecord, error) {
	w.demoMu.Lock()
	defer w.demoMu.Unlock()
	rec, err := w.demoRecord(root, id)
	if err != nil {
		return rec, err
	}
	if rec.Status == "running" {
		return rec, fmt.Errorf("demo record %s is still running -- its videos are kept when it ends", rec.ID)
	}
	if rec.Mode != "assert" {
		return rec, fmt.Errorf("record %s is a %s, which renders nothing of its own", rec.ID, rec.Mode)
	}
	// A record from before Revision G lists only the demo's own renders;
	// the image has those inside its calls too.
	var img demoImage
	if rec.HasImage {
		b, err := os.ReadFile(demoImagePath(rec.Dir))
		if err != nil {
			return rec, fmt.Errorf("record %s's image: %v", rec.ID, err)
		}
		if err := json.Unmarshal(b, &img); err != nil {
			return rec, fmt.Errorf("record %s's image: %v", rec.ID, err)
		}
		rec.Renders = mergeDemoRenders(rec.Renders, demoRenders(img, "", "", 0))
	}
	if len(rec.Renders) == 0 {
		return rec, fmt.Errorf("record %s rendered no videos", rec.ID)
	}
	if primaryRender(rec.Renders) == nil {
		markPrimaryRender(rec.Renders) // a record from before Step 5
	}
	w.keepDemoVideos(&rec)
	// analysis/ is compiled from the videos, so it's made again with them;
	// the summary and the map name where each video is, so they're written
	// again too.
	w.analyseDemoRecord(&rec, img, demoRunOutputs(img, "", "", 0))
	if err := os.MkdirAll(filepath.Join(rec.Dir, demoArtifactsDir), 0o755); err == nil {
		os.WriteFile(filepath.Join(rec.Dir, demoArtifactsDir, demoSummaryFile), []byte(demoSummary(rec)), 0o644)
	}
	if err := writeDemoRecord(rec); err != nil {
		return rec, fmt.Errorf("updating record %s: %v", rec.ID, err)
	}
	writeDemoMap(rec)
	return rec, nil
}

// mergeDemoRenders is have, plus those in found it hasn't (by file).
func mergeDemoRenders(have, found []DemoRender) []DemoRender {
	seen := map[string]bool{}
	for _, r := range have {
		seen[r.FileID] = true
	}
	for _, r := range found {
		if !seen[r.FileID] {
			have = append(have, r)
		}
	}
	return have
}

// handleDemoRecordFile serves GET /api/demo-records/<id>/videos/<file>: a
// video a demo record keeps, the way /api/files/<id> serves the files tab
// (inline, or ?download=1 to save it) -- and, since Step 5, its artifacts
// (/artifacts/<file>, /artifacts/screenshots/<file>), and since its
// Revision A its map and analysis (/MAP.md, /analysis/...). Nothing else in a
// record is served here; the tools requests carry the rest.
func (w *toolsWatch) handleDemoRecordFile(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/demo-records/")
	parts := strings.Split(rest, "/")
	root := w.demoDir()
	// <id>/videos/<file>, <id>/artifacts/<file> or
	// <id>/artifacts/screenshots/<file> (Step 5); <id>/MAP.md and
	// <id>/analysis/[<dir>/[<dir>/[<dir>/]]]<file> (Revision A).
	sub := ""
	switch {
	case len(parts) == 2 && parts[1] == demoMapFile:
		sub = "."
	case len(parts) == 3 && (parts[1] == demoVideosDir || parts[1] == demoArtifactsDir || parts[1] == demoAnalysisDir):
		sub = parts[1]
	case len(parts) == 4 && parts[1] == demoArtifactsDir && parts[2] == demoShotsDir:
		sub = filepath.Join(demoArtifactsDir, demoShotsDir)
	case len(parts) >= 4 && len(parts) <= 6 && parts[1] == demoAnalysisDir:
		sub = demoAnalysisDir
		for _, d := range parts[2 : len(parts)-1] {
			if !demoRecordIDRe.MatchString(d) || strings.Contains(d, "..") {
				sub = ""
				break
			}
			sub = filepath.Join(sub, d)
		}
	}
	name := parts[len(parts)-1]
	if root == "" || sub == "" || !demoRecordIDRe.MatchString(parts[0]) ||
		name == "" || strings.HasPrefix(name, ".") || name != filepath.Base(name) || strings.HasSuffix(name, ".tmp") {
		http.NotFound(rw, r)
		return
	}
	f, err := os.Open(filepath.Join(root, demoRecordsDir, parts[0], sub, name))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(rw, r)
		return
	}
	disposition := "inline"
	if r.URL.Query().Get("download") != "" {
		disposition = "attachment"
	}
	rw.Header().Set("Content-Disposition", disposition+`; filename="`+dispositionFilename(name)+`"`)
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	rw.Header().Set("Content-Type", ctype)
	http.ServeContent(rw, r, name, info.ModTime(), f)
}

// duplicateDemo copies demo name into the host's library as newName, for
// editing there.
func (w *toolsWatch) duplicateDemo(name, newName string) (DemoInfo, error) {
	root := w.demoDir()
	if root == "" {
		return DemoInfo{}, errors.New("there's no directory on this host to keep demos in")
	}
	newName = strings.TrimSpace(newName)
	if !demoNameRe.MatchString(newName) {
		return DemoInfo{}, fmt.Errorf("%q: demo names are lowercase letters, digits and '-', starting with a letter", newName)
	}
	d, err := w.findDemo(name)
	if err != nil {
		return DemoInfo{}, err
	}
	for _, o := range w.demoLibrary() {
		if o.Name == newName {
			return DemoInfo{}, fmt.Errorf("there's a demo called %s already", newName)
		}
	}
	dir := filepath.Join(root, demoLibraryDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return DemoInfo{}, err
	}
	p := filepath.Join(dir, newName+demoSuffix)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return DemoInfo{}, err
	}
	src := strings.Replace(d.Source, d.Name+demoSuffix, newName+demoSuffix, -1)
	if _, err := f.WriteString(src); err != nil {
		f.Close()
		os.Remove(p)
		return DemoInfo{}, err
	}
	if err := f.Close(); err != nil {
		return DemoInfo{}, err
	}
	return parseDemo(newName, src, demoOriginConfigured, p), nil
}

// handleDemo answers the demo tab's ops (see ToolsRequest).
func (w *toolsWatch) handleDemo(req ToolsRequest, fromAC bool, node string, reply ToolsReply) ToolsReply {
	fail := func(err error) ToolsReply {
		reply.Error = err.Error()
		return reply
	}
	root := w.demoDir()
	reply.DemoDir = root
	switch req.Op {
	case "demos":
		reply.OK, reply.Demos, reply.DemoRecords = true, w.demoLibrary(), w.demoRecords(root)
		reply.Suites, reply.SuiteRecords = w.suiteLibrary(reply.Demos), w.suiteRecords(root)
		reply.DemoLastValues = w.readDemoLastValues()
		w.reviewMu.Lock()
		reply.DemoProposals = w.demoProposals(root)
		w.reviewMu.Unlock()
		reply.Detail = fmt.Sprintf("%d demo(s), %d suite(s), %d record(s), %d proposal(s)", len(reply.Demos), len(reply.Suites), len(reply.DemoRecords), len(reply.DemoProposals))
		if fromAC {
			fitDemosReply(&reply)
		}
		return reply
	case "demo-run":
		rec, err := w.runDemo(req, fromAC, node, demoRunOpts{})
		if err != nil {
			return fail(err)
		}
		if d, err := w.findDemo(rec.Demo); err == nil && rec.Mode != "delete" {
			w.rememberDemoValues(req.Variables, d.Variables)
		}
		reply.OK, reply.DemoRecord, reply.RunID = true, &rec, rec.RunID
		reply.Detail = fmt.Sprintf("%s: %s running, kept as %s", rec.Demo, rec.Mode, rec.ID)
		return reply
	case "suite-run":
		srec, err := w.runSuite(req, fromAC, node)
		if err != nil {
			return fail(err)
		}
		reply.OK, reply.SuiteRecord = true, &srec
		reply.Detail = fmt.Sprintf("suite %s: running its %d demo(s) in turn, kept as %s", srec.Suite, len(srec.Entries), srec.ID)
		return reply
	case "demo-review", "demo-reviews":
		return w.handleDemoReview(req, fromAC, root, reply)
	case "demo-proposal", "demo-proposal-agent-create", "demo-proposal-delete":
		return w.handleProposal(req, fromAC, root, reply)
	case "demo-record":
		rec, err := w.demoRecord(root, strings.TrimSpace(req.Record))
		if err != nil {
			return fail(err)
		}
		reply.OK, reply.DemoRecord = true, &rec
		if rec.HasImage {
			if b, err := os.ReadFile(demoImagePath(rec.Dir)); err == nil {
				reply.DemoImage = string(b)
			}
		}
		if fromAC {
			if b, err := json.Marshal(reply); err != nil || len(b) > maxRelayedTools {
				reply.DemoImage = ""
				reply.Detail = "the image is too large to relay through agent-coordinator -- it is at " + demoImagePath(rec.Dir) + " on that host"
			}
		}
		return reply
	case "demo-keep-videos":
		rec, err := w.keepRecordVideos(root, strings.TrimSpace(req.Record))
		if err != nil {
			return fail(err)
		}
		kept, missing := 0, 0
		for _, r := range rec.Renders {
			if r.Kept != "" {
				kept++
			} else {
				missing++
			}
		}
		rec.LogTail = ""
		reply.OK, reply.DemoRecord = true, &rec
		reply.Detail = fmt.Sprintf("%s keeps %d of its %d video(s)", rec.ID, kept, kept+missing)
		if missing > 0 {
			reply.Detail += " -- see each one for why the rest couldn't be"
		}
		return reply
	case "demo-duplicate":
		d, err := w.duplicateDemo(req.Demo, req.Name)
		if err != nil {
			return fail(err)
		}
		reply.OK, reply.Demos = true, []DemoInfo{d}
		reply.Detail = fmt.Sprintf("duplicated %s as %s, at %s -- edit it there", req.Demo, d.Name, d.Path)
		return reply
	}
	return fail(fmt.Errorf("unknown demo op %q", req.Op))
}

// fitDemosReply cuts a demos reply to fit the relay to agent-coordinator:
// the demos' sources first (the host's own demo tab shows them), then the
// proposals' parts (demo-proposal answers one in full), then the oldest
// records.
func fitDemosReply(reply *ToolsReply) {
	fits := func() bool {
		b, err := json.Marshal(reply)
		return err == nil && len(b) <= maxRelayedTools
	}
	if fits() {
		return
	}
	for i := range reply.Demos {
		reply.Demos[i].Source = ""
		reply.Demos[i].Description = oneLineText(reply.Demos[i].Description, 300)
	}
	for i := range reply.Suites {
		reply.Suites[i].Source = ""
	}
	for i := range reply.DemoProposals {
		p := &reply.DemoProposals[i]
		p.Description, p.Files = oneLineText(p.Description, 300), nil
	}
	for i := range reply.DemoProposals {
		if fits() {
			break
		}
		reply.DemoProposals[i].Icon = ""
	}
	for !fits() && (len(reply.DemoRecords) > 0 || len(reply.SuiteRecords) > 0) {
		if len(reply.SuiteRecords) > len(reply.DemoRecords)/4 {
			reply.SuiteRecords = reply.SuiteRecords[:len(reply.SuiteRecords)-1]
		} else {
			reply.DemoRecords = reply.DemoRecords[:len(reply.DemoRecords)-1]
		}
	}
	reply.Detail += " (cut to fit the relay -- see that host's own demo tab for all of it)"
}
