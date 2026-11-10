package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The demo tab's suites (PrivateRepoOps Step 5). A suite is a list of demos
// from the library run one after another on the same host, each as its own
// demo run with its own record (demos.go). Their variables are asked for
// once, up front: the suite's variables are every variable its demos
// declare, by name, so one the demos share -- node, the local system under
// test, above all -- is given once and goes to each demo that declares it.
// A demo that fails doesn't stop the suite unless it says on_failure: stop.
//
// A suite is a small YAML file, read line by line as demos are:
//
//	# Suite -- <title>
//	# <description>
//	demos:
//	  - <demo name>
//	  - ...
//	on_failure: continue     # or stop
//
// The embedded suites (suites/ here) are in-tree and built into LR; a
// host's configured ones are <root>/suites/<name>.suite.yaml (Revision N:
// see demoOriginEmbedded). Each run of a suite is kept as
// <root>/suite-records/<name>-<time>/suite.json, a SuiteRecord naming the
// demo record each of its demos made.
//
// Free-text variables -- those without a pick-list, i.e. every one but the
// node ones -- remember the value they were last run with, demo or suite,
// as the next run's starting value. They're kept in LR's config directory
// (demoLastValuesPath), on this host's filesystem, not with the records.

//go:embed suites/*.suite.yaml
var builtinSuitesFS embed.FS

const (
	builtinSuitesDir  = "suites"
	suiteSuffix       = ".suite.yaml"
	suiteRecordFormat = "lr-demo-suite-record-v1"
	suiteRecordFile   = "suite.json"
	suiteLibraryDir   = "suites"
	suiteRecordsDir   = "suite-records"
	maxSuiteRecords   = 100
	maxSuiteSource    = 32 << 10
	demoLastValueMax  = 1 << 10
)

// SuiteVariable is one of a suite's variables: Demos are the suite's demos
// that declare it; Default and Help are the first of them's.
type SuiteVariable struct {
	Name    string   `json:"name"`
	Default string   `json:"default,omitempty"`
	Help    string   `json:"help,omitempty"`
	Demos   []string `json:"demos"`
}

// SuiteInfo is one suite in the library. OnFailure is continue or stop.
type SuiteInfo struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Origin      string          `json:"origin"`
	Path        string          `json:"path,omitempty"`
	Demos       []string        `json:"demos"`
	OnFailure   string          `json:"on_failure"`
	Variables   []SuiteVariable `json:"variables"`
	Source      string          `json:"source,omitempty"`
	Error       string          `json:"error,omitempty"`
	// Coverage is its demos' together (Revision C, democoverage.go).
	Coverage *DemoCoverage `json:"coverage,omitempty"`
}

// SuiteEntry is one demo of a suite run: Status pending, running, ok,
// failed or skipped (after a failure, with on_failure: stop); Record the
// demo record it made; Variables those of the suite's it was given.
type SuiteEntry struct {
	Demo       string            `json:"demo"`
	Record     string            `json:"record,omitempty"`
	Status     string            `json:"status"`
	Error      string            `json:"error,omitempty"`
	Variables  map[string]string `json:"variables"`
	StartedAt  int64             `json:"started_at,omitempty"`
	FinishedAt int64             `json:"finished_at,omitempty"`
}

// SuiteRecord is one run of a suite, as suite.json keeps it. Status is
// running, ok (every demo was), failed (any wasn't) or interrupted (LR
// stopped while it ran).
type SuiteRecord struct {
	Format     string            `json:"format"`
	ID         string            `json:"id"`
	Suite      string            `json:"suite"`
	Title      string            `json:"title"`
	Variables  map[string]string `json:"variables"`
	Node       string            `json:"node"`
	Origin     string            `json:"origin"`
	OnFailure  string            `json:"on_failure"`
	Status     string            `json:"status"`
	StartedAt  int64             `json:"started_at"`
	FinishedAt int64             `json:"finished_at,omitempty"`
	Entries    []SuiteEntry      `json:"entries"`
	Dir        string            `json:"dir"`
}

// isDemoNodeVariable: a variable naming a node ("node", "<x>_node"), which
// the demo tab offers as a pick-list of the connected nodes (Demo.tsx's
// isNodeVariable) -- so its value isn't remembered.
func isDemoNodeVariable(name string) bool {
	return name == "node" || strings.HasSuffix(name, "_node")
}

// ---- The library ----

// parseSuite reads suite name from its source, as the file comment above
// says. The demos' variables are filled in by suiteLibrary.
func parseSuite(name, src, origin, file string) SuiteInfo {
	s := SuiteInfo{Name: name, Origin: origin, Path: file, Source: src, Demos: []string{}, OnFailure: "continue", Variables: []SuiteVariable{}}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var desc []string
	i := 0
	for ; i < len(lines) && strings.HasPrefix(lines[i], "#"); i++ {
		text := strings.TrimPrefix(strings.TrimPrefix(lines[i], "#"), " ")
		if i == 0 {
			s.Title = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "Suite --"))
		} else {
			desc = append(desc, strings.TrimRight(text, " "))
		}
	}
	s.Description = strings.TrimSpace(strings.Join(desc, "\n"))
	if s.Title == "" {
		s.Title = name
	}
	inDemos := false
	for ; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		trim := strings.TrimSpace(line)
		if c := strings.Index(trim, " #"); c >= 0 {
			trim = strings.TrimSpace(trim[:c])
		}
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if line[0] != ' ' && line[0] != '-' {
			key, rest, _ := strings.Cut(trim, ":")
			inDemos = key == "demos"
			switch key {
			case "demos":
			case "on_failure":
				s.OnFailure = demoScalar(rest)
			default:
				s.Error = fmt.Sprintf("unknown key %q -- a suite has demos and on_failure", key)
			}
			continue
		}
		if !inDemos || !strings.HasPrefix(trim, "-") {
			s.Error = fmt.Sprintf("line %d: expected a demo, \"- <name>\", under demos:", i+1)
			continue
		}
		d := demoScalar(strings.TrimPrefix(trim, "-"))
		if !demoNameRe.MatchString(d) {
			s.Error = fmt.Sprintf("%q isn't a demo name", d)
			continue
		}
		s.Demos = append(s.Demos, d)
	}
	switch {
	case s.Error != "":
	case len(s.Demos) == 0:
		s.Error = "the suite lists no demos"
	case s.OnFailure != "continue" && s.OnFailure != "stop":
		s.Error = fmt.Sprintf("on_failure %q should be continue or stop", s.OnFailure)
	}
	return s
}

func builtinSuites() []SuiteInfo {
	entries, _ := fs.ReadDir(builtinSuitesFS, builtinSuitesDir)
	var out []SuiteInfo
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), suiteSuffix) {
			continue
		}
		data, err := builtinSuitesFS.ReadFile(path.Join(builtinSuitesDir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, parseSuite(strings.TrimSuffix(e.Name(), suiteSuffix), string(data), demoOriginEmbedded, ""))
	}
	return out
}

func hostSuites(root string) []SuiteInfo {
	if root == "" {
		return nil
	}
	dir := filepath.Join(root, suiteLibraryDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []SuiteInfo
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), suiteSuffix)
		if e.IsDir() || !strings.HasSuffix(e.Name(), suiteSuffix) || !demoNameRe.MatchString(name) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		switch {
		case err != nil:
			out = append(out, SuiteInfo{Name: name, Title: name, Origin: demoOriginConfigured, Path: p, Demos: []string{}, Variables: []SuiteVariable{}, Error: err.Error()})
		case len(data) > maxSuiteSource:
			out = append(out, SuiteInfo{Name: name, Title: name, Origin: demoOriginConfigured, Path: p, Demos: []string{}, Variables: []SuiteVariable{}, Error: "the file is too large to be a suite"})
		default:
			out = append(out, parseSuite(name, string(data), demoOriginConfigured, p))
		}
	}
	return out
}

// suiteLibrary is the built-in suites, then the host's (a host suite can't
// take a built-in one's name), each with its variables gathered from the
// demos -- the library's, as demoLibrary gives them.
func (w *toolsWatch) suiteLibrary(demos []DemoInfo) []SuiteInfo {
	out := builtinSuites()
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Name] = true
	}
	for _, s := range hostSuites(w.demoDir()) {
		if !seen[s.Name] {
			out = append(out, s)
		}
	}
	byName := map[string]DemoInfo{}
	for _, d := range demos {
		byName[d.Name] = d
	}
	for i := range out {
		resolveSuite(&out[i], byName)
	}
	if out == nil {
		out = []SuiteInfo{}
	}
	return out
}

// resolveSuite fills s's variables in from its demos, in the order they
// first declare them, and its coverage from theirs.
func resolveSuite(s *SuiteInfo, demos map[string]DemoInfo) {
	at := map[string]int{}
	var found []DemoInfo
	for _, name := range s.Demos {
		d, ok := demos[name]
		if !ok {
			if s.Error == "" {
				s.Error = fmt.Sprintf("no demo %q in the library", name)
			}
			continue
		}
		found = append(found, d)
		if d.Error != "" && s.Error == "" {
			s.Error = fmt.Sprintf("demo %s: %s", name, d.Error)
		}
		for _, v := range d.Variables {
			n, ok := at[v.Name]
			if !ok {
				n = len(s.Variables)
				at[v.Name] = n
				s.Variables = append(s.Variables, SuiteVariable{Name: v.Name, Default: v.Default, Help: v.Help})
			}
			s.Variables[n].Demos = append(s.Variables[n].Demos, name)
		}
	}
	s.Coverage = suiteCoverage(found)
}

func (w *toolsWatch) findSuite(name string) (SuiteInfo, error) {
	name = strings.TrimSpace(name)
	for _, s := range w.suiteLibrary(w.demoLibrary()) {
		if s.Name == name {
			if s.Error != "" {
				return s, fmt.Errorf("suite %s: %s", name, s.Error)
			}
			return s, nil
		}
	}
	return SuiteInfo{}, fmt.Errorf("no suite %q in the library", name)
}

// ---- Records ----

func writeSuiteRecord(rec SuiteRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(rec.Dir, suiteRecordFile+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(rec.Dir, suiteRecordFile))
}

func readSuiteRecord(dir string) (SuiteRecord, error) {
	var rec SuiteRecord
	data, err := os.ReadFile(filepath.Join(dir, suiteRecordFile))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, err
	}
	if rec.Format != suiteRecordFormat {
		return rec, fmt.Errorf("format %q isn't %s", rec.Format, suiteRecordFormat)
	}
	rec.ID, rec.Dir = filepath.Base(dir), dir
	return rec, nil
}

// suiteRecords lists the suite runs under root, newest first; one still
// marked running that this LR isn't running (it restarted) was
// interrupted.
func (w *toolsWatch) suiteRecords(root string) []SuiteRecord {
	out := []SuiteRecord{}
	if root == "" {
		return out
	}
	entries, err := os.ReadDir(filepath.Join(root, suiteRecordsDir))
	if err != nil {
		return out
	}
	w.demoMu.Lock()
	active := w.suiteActive
	w.demoMu.Unlock()
	for _, e := range entries {
		if !e.IsDir() || !demoRecordIDRe.MatchString(e.Name()) {
			continue
		}
		rec, err := readSuiteRecord(filepath.Join(root, suiteRecordsDir, e.Name()))
		if err != nil {
			continue
		}
		if rec.Status == "running" && rec.ID != active {
			rec.Status = "interrupted"
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	if len(out) > maxSuiteRecords {
		out = out[:maxSuiteRecords]
	}
	return out
}

// ---- Running ----

// runSuite starts suite-run: req.Suite's demos, one after another, each
// given those of req.Variables it declares. It answers once the suite's
// record is written; the demos run in the background (suiteLoop).
func (w *toolsWatch) runSuite(req ToolsRequest, fromAC bool, node string) (SuiteRecord, error) {
	root := w.demoDir()
	if root == "" {
		return SuiteRecord{}, errors.New("there's no directory on this host to keep demo records in")
	}
	s, err := w.findSuite(req.Suite)
	if err != nil {
		return SuiteRecord{}, err
	}
	vars := map[string]string{}
	for k, v := range req.Variables {
		if !demoVarNameRe.MatchString(k) {
			return SuiteRecord{}, fmt.Errorf("%q isn't a variable name", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return SuiteRecord{}, fmt.Errorf("variable %s: one line only", k)
		}
		if v = strings.TrimSpace(v); v != "" {
			vars[k] = v
		}
	}
	rec := SuiteRecord{Format: suiteRecordFormat, Suite: s.Name, Title: s.Title, Variables: vars, Node: node, Origin: "demo tab",
		OnFailure: s.OnFailure, Status: "running", Entries: []SuiteEntry{}}
	if fromAC {
		rec.Origin = "agent-coordinator"
	}
	for _, name := range s.Demos {
		e := SuiteEntry{Demo: name, Status: "pending", Variables: map[string]string{}}
		for _, v := range s.Variables {
			if val, ok := vars[v.Name]; ok && containsString(v.Demos, name) {
				e.Variables[v.Name] = val
			}
		}
		rec.Entries = append(rec.Entries, e)
	}

	w.demoMu.Lock()
	switch {
	case w.suiteActive != "":
		w.demoMu.Unlock()
		return rec, fmt.Errorf("suite record %s is still running -- one suite runs at a time", w.suiteActive)
	case w.demoActive != "":
		w.demoMu.Unlock()
		return rec, fmt.Errorf("demo record %s is still running -- one demo runs at a time", w.demoActive)
	}
	now := time.Now()
	dir, err := newRecordDir(filepath.Join(root, suiteRecordsDir), s.Name, now)
	if err != nil {
		w.demoMu.Unlock()
		return rec, fmt.Errorf("making the suite record's directory: %v", err)
	}
	rec.ID, rec.Dir, rec.StartedAt = filepath.Base(dir), dir, now.UnixMilli()
	if err := writeSuiteRecord(rec); err != nil {
		os.RemoveAll(dir)
		w.demoMu.Unlock()
		return rec, fmt.Errorf("writing the suite record: %v", err)
	}
	w.suiteActive = rec.ID
	w.demoMu.Unlock()

	free := make([]DemoVariable, 0, len(s.Variables))
	for _, v := range s.Variables {
		free = append(free, DemoVariable{Name: v.Name})
	}
	w.rememberDemoValues(req.Variables, free)
	// The loop gets entries of its own: the reply still holds rec's.
	loop := rec
	loop.Entries = append([]SuiteEntry(nil), rec.Entries...)
	go w.suiteLoop(loop, fromAC, node)
	return rec, nil
}

// suiteLoop runs rec's demos in turn, waiting for each to end before the
// next, and keeps rec up to date as it goes. A demo that fails -- or can't
// start -- is noted and the next one runs, unless the suite stops on
// failure, when the rest are skipped.
func (w *toolsWatch) suiteLoop(rec SuiteRecord, fromAC bool, node string) {
	save := func() {
		if err := writeSuiteRecord(rec); err != nil {
			fmt.Fprintf(os.Stderr, "suite record %s: %v\n", rec.ID, err)
		}
		if w.notify != nil {
			w.notify()
		}
	}
	stop := false
	for i := range rec.Entries {
		e := &rec.Entries[i]
		if stop {
			e.Status = "skipped"
			continue
		}
		e.Status, e.StartedAt = "running", time.Now().UnixMilli()
		done := make(chan DemoRecord, 1)
		started, err := w.runDemo(ToolsRequest{Demo: e.Demo, Mode: "assert", Variables: e.Variables}, fromAC, node,
			demoRunOpts{suite: rec.Suite, suiteRecord: rec.ID, done: func(r DemoRecord) { done <- r }})
		e.Record = started.ID
		if err != nil && started.RunID == "" {
			e.Status, e.Error, e.FinishedAt = "failed", err.Error(), time.Now().UnixMilli()
		} else {
			save()
			r := <-done
			e.Status, e.Error, e.FinishedAt = r.Status, r.Error, r.FinishedAt
		}
		save()
		if e.Status != "ok" && rec.OnFailure == "stop" {
			stop = true
		}
	}
	rec.Status = "ok"
	for _, e := range rec.Entries {
		if e.Status != "ok" {
			rec.Status = "failed"
		}
	}
	rec.FinishedAt = time.Now().UnixMilli()
	// Written finished before it stops being the active suite, so a listing
	// never takes it for interrupted.
	if err := writeSuiteRecord(rec); err != nil {
		fmt.Fprintf(os.Stderr, "suite record %s: %v\n", rec.ID, err)
	}
	w.demoMu.Lock()
	if w.suiteActive == rec.ID {
		w.suiteActive = ""
	}
	w.demoMu.Unlock()
	if w.notify != nil {
		w.notify()
	}
}

// ---- Remembered values ----

// demoLastValuesMu guards the last-values file.
var demoLastValuesMu sync.Mutex

// demoLastValuesPath is where the free-text variables' last values are
// kept: LR's config directory, so on this host's filesystem and in no
// repo or records root.
func demoLastValuesPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "local-representative", "demo-last-values.json")
}

func (w *toolsWatch) demoLastValuesFile() string {
	if w == nil || w.demoLastValues == nil {
		return ""
	}
	return w.demoLastValues()
}

// readDemoLastValues is the last value of each free-text variable, by
// name; none when there's no file.
func (w *toolsWatch) readDemoLastValues() map[string]string {
	out := map[string]string{}
	p := w.demoLastValuesFile()
	if p == "" {
		return out
	}
	demoLastValuesMu.Lock()
	defer demoLastValuesMu.Unlock()
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// rememberDemoValues keeps the values given for declared's free-text
// variables (not the node pick-lists) as their last values: one given
// empty is forgotten, so the default shows next time; one not given at
// all is left as it was.
func (w *toolsWatch) rememberDemoValues(given map[string]string, declared []DemoVariable) {
	p := w.demoLastValuesFile()
	if p == "" || len(given) == 0 {
		return
	}
	demoLastValuesMu.Lock()
	defer demoLastValuesMu.Unlock()
	values := map[string]string{}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &values)
	}
	changed := false
	for _, v := range declared {
		val, ok := given[v.Name]
		if !ok || isDemoNodeVariable(v.Name) || !demoVarNameRe.MatchString(v.Name) {
			continue
		}
		val = strings.TrimSpace(val)
		switch {
		case val == "" || strings.ContainsAny(val, "\r\n") || len(val) > demoLastValueMax:
			if _, had := values[v.Name]; had {
				delete(values, v.Name)
				changed = true
			}
		case values[v.Name] != val:
			values[v.Name] = val
			changed = true
		}
	}
	if !changed {
		return
	}
	b, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o644) == nil {
		os.Rename(tmp, p)
	}
}
