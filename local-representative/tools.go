package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The tools tab (the "Bring DTTs to AI-evo1" ufa-session): LR's own tab,
// not a managed app, over T3s -- the declarative tools dtt runs (see
// dtt/README.md). dtt is a transient CLI LR calls, the way agents.go calls
// "ambiguous-agent --inspect": "dtt list --json" every 10 minutes (and on
// the tab's re-inspect) for the catalog, and "dtt run" for each run, so LR
// has nothing to supervise. The catalog goes into the system-state snapshot
// (SystemStateMsg.Tools) and, with the recent runs, into "tools-state" for
// the tab. tools-state is forwarded to agent-coordinator too, whose
// per-host tools tab is the same panel (Tools.tsx), driving this one
// through "__tools:<op> <json>".
//
// Each tool is also a control op, tool.<name> (see controlops.go): a
// control run calls it through dtt, saves its output values under their
// names and copies its output files into the files tab, as node-fetch-file
// does. The tools-allow setting limits which tools control runs -- and
// anything sent from agent-coordinator -- may run; the tab's own runner
// here isn't limited by it.

const (
	toolsPollInterval  = 10 * time.Minute
	toolsCallTimeout   = 2 * time.Minute  // list, describe and the author ops
	toolsRunMargin     = 30 * time.Second // on top of a tool's own timeout, for dtt and uv to start
	toolsDefaultLimit  = 2 * time.Minute  // a tool whose timeout couldn't be read
	dttStopGrace       = 10 * time.Second // after SIGTERM, for dtt to kill its tool and answer
	maxToolRuns        = 20
	maxToolLogRelayed  = 1 << 10  // of each run's log, in tools-state relayed to agent-coordinator
	maxRelayedTools    = 48 << 10 // a representable line is read up to 64 KiB

	dttBin = "dtt"

	// toolsAllowAll is the tools-allow entry, and its default, that allows
	// every tool.
	toolsAllowAll = "*"
)

// toolNameRe matches dtt's tool names (dtt/schema.go).
var toolNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// ToolArg mirrors dtt's ToolArg: a ControlOpArg plus the input's type and
// choices, for the runner's form.
type ToolArg struct {
	Name     string   `json:"name"`
	Help     string   `json:"help"`
	Required bool     `json:"required,omitempty"`
	Default  string   `json:"default,omitempty"`
	Type     string   `json:"type"`
	Choices  []string `json:"choices,omitempty"`
}

// ToolOutput mirrors dtt's Output: a named output value or file.
type ToolOutput struct {
	Name string `json:"name"`
	Help string `json:"help,omitempty"`
}

// ToolInfo mirrors dtt's ToolInfo -- Op, Summary, Describe and Args are a
// ControlOpSpec as they stand -- plus whether tools-allow lets control runs
// (and agent-coordinator) run it.
type ToolInfo struct {
	Op       string    `json:"op"`
	Summary  string    `json:"summary"`
	Describe string    `json:"describe"`
	Args     []ToolArg `json:"args"`

	Name        string       `json:"name"`
	Description string       `json:"description"`
	Values      []ToolOutput `json:"values"`
	Files       []ToolOutput `json:"files"`
	Runtime     string       `json:"runtime"`
	Script      string       `json:"script"`
	Timeout     string       `json:"timeout"`
	Origin      string       `json:"origin"`
	Path        string       `json:"path"`
	Source      string       `json:"source,omitempty"` // tool.yaml, from describe only

	Allowed bool `json:"allowed"`
}

// ToolDir, ToolProblem and ToolShadowed mirror dtt list's dirs, problems
// and shadowed.
type ToolDir struct {
	Origin string `json:"origin"`
	Path   string `json:"path"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type ToolProblem struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type ToolShadowed struct {
	Name string `json:"name"`
	Path string `json:"path"`
	By   string `json:"by"`
}

// ToolsState is the catalog: SystemStateMsg.Tools, and the core of
// tools-state. Status reuses the connection statuses: connChecking until
// the first "dtt list" finishes, then connOK, or connFailed (with Detail)
// when dtt couldn't be found or run.
type ToolsState struct {
	Status    string         `json:"status"`
	Detail    string         `json:"detail,omitempty"`
	Version   string         `json:"version,omitempty"` // dtt's
	Tools     []ToolInfo     `json:"tools"`
	UserDir   string         `json:"user_dir,omitempty"` // where install and create put tools
	Dirs      []ToolDir      `json:"dirs,omitempty"`
	Problems  []ToolProblem  `json:"problems,omitempty"`
	Shadowed  []ToolShadowed `json:"shadowed,omitempty"`
	CheckedAt int64          `json:"checked_at,omitempty"` // unix seconds of the last finished listing

	// Allow is tools-allow: the tools (names or globs) control runs and
	// agent-coordinator may run; empty allows every tool. AllowFrom is
	// where it was set.
	Allow     []string `json:"allow"`
	AllowFrom string   `json:"allow_from,omitempty"`
}

// ToolsStateMsg is "tools-state": the catalog plus the recent runs, newest
// first. RunsOmitted counts runs left out to fit the relay to
// agent-coordinator.
type ToolsStateMsg struct {
	ToolsState
	Runs        []ToolRun `json:"runs"`
	RunsOmitted int       `json:"runs_omitted,omitempty"`
}

// ToolRun is one run of a tool, from the tools tab, agent-coordinator or a
// control run (Origin: "tools tab", "agent-coordinator" or "control run").
// Status is running, ok or failed; the times are unix ms.
type ToolRun struct {
	ID         string            `json:"id"`
	Tool       string            `json:"tool"`
	Args       map[string]string `json:"args"`
	Origin     string            `json:"origin"`
	Status     string            `json:"status"`
	StartedAt  int64             `json:"started_at"`
	FinishedAt int64             `json:"finished_at,omitempty"`
	Result     *ToolRunResult    `json:"result,omitempty"`
	// FilesKept is whether the run's output files are still where dtt left
	// them, for "save results to files": a control run's are copied into
	// the files tab instead (Saved) and its working directory removed.
	FilesKept bool            `json:"files_kept"`
	Saved     []ToolSavedFile `json:"saved,omitempty"`

	workdir string
}

// ToolRunResult mirrors dtt's RunResult.
type ToolRunResult struct {
	OK         bool              `json:"ok"`
	Tool       string            `json:"tool"`
	Outputs    map[string]string `json:"outputs"`
	Files      []ToolRunFile     `json:"files"`
	LogTail    string            `json:"log_tail"`
	ExitCode   int               `json:"exit_code"`
	Error      string            `json:"error,omitempty"`
	TimedOut   bool              `json:"timed_out,omitempty"`
	Workdir    string            `json:"workdir,omitempty"`
	DurationMs int64             `json:"duration_ms"`
}

type ToolRunFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ToolSavedFile is a files-tab entry a run's results were saved as.
type ToolSavedFile struct {
	FileID string `json:"file_id"` // GET /api/files/<file_id>
	Name   string `json:"name"`
}

// ToolsRequest is the tools tab's "tools-request", and the JSON of
// agent-coordinator's "__tools:<op> <json>".
type ToolsRequest struct {
	Req  string `json:"req,omitempty"` // echoed in the reply
	Op   string `json:"op"`            // reinspect, run, save, describe, validate, install, create
	Tool string `json:"tool,omitempty"`
	// Args are run's inputs, by name; an empty one takes the input's default.
	Args map[string]string `json:"args,omitempty"`
	Run  string            `json:"run,omitempty"` // save: the run's id
	// validate checks Source (tool.yaml's text, schema only) or the tool at
	// Path (a directory or tool.yaml on this host); install installs the
	// tool at Path, replacing an installed copy with Force.
	Source string `json:"source,omitempty"`
	Path   string `json:"path,omitempty"`
	Force  bool   `json:"force,omitempty"`
	// create scaffolds tool Name in the tools directory, runtime uv or exec.
	Name    string `json:"name,omitempty"`
	Runtime string `json:"runtime,omitempty"`
}

// ToolsReply answers a ToolsRequest ("tools-reply").
type ToolsReply struct {
	Req    string         `json:"req,omitempty"`
	Op     string         `json:"op"`
	OK     bool           `json:"ok"`
	Error  string         `json:"error,omitempty"`
	Detail string         `json:"detail,omitempty"`
	RunID  string         `json:"run_id,omitempty"` // run
	Tool   *ToolInfo      `json:"tool,omitempty"`   // describe, validate, install, create
	File   *ToolSavedFile `json:"file,omitempty"`   // save
}

// toolsWatch keeps the catalog and the recent runs. resolve locates the
// dtt binary (Server.resolveAppBinary); dtt runs it -- both swappable by
// tests. saveFile puts a file into the files tab.
type toolsWatch struct {
	notify         func() // tools-state changed
	catalogChanged func() // ...and the catalog with it
	resolve        func() (string, error)
	dtt            func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error)
	saveFile       func(name string, src io.Reader) (FileInfo, error)
	runDir         string // where runs' working directories go

	wake chan struct{}

	mu        sync.RWMutex
	state     ToolsState // Tools without Allowed, which snapshot fills in
	allow     []string
	allowFrom string
	runs      []*ToolRun // newest first
	seq       int
}

func newToolsWatch(notify, catalogChanged func(), resolve func() (string, error)) *toolsWatch {
	return &toolsWatch{
		notify:         notify,
		catalogChanged: catalogChanged,
		resolve:        resolve,
		dtt:            runDTT,
		runDir:         filepath.Join(os.TempDir(), "local-representative-tools"),
		wake:           make(chan struct{}, 1),
		state:          ToolsState{Status: connChecking, Tools: []ToolInfo{}},
	}
}

// runDTT is the default dtt call: "<bin> args..." with stdin fed in. dtt
// answers in JSON on stdout whatever its exit status (1 a failed tool, 2 bad
// input), so stdout comes back despite an exit status; only a dtt that
// didn't run, or said nothing, is an error.
//
// Stopping a call (a cancelled control run, or LR's own limit) sends dtt
// SIGTERM rather than the default SIGKILL: dtt runs the tool in its own
// process group, which only dtt kills on its way out, so a SIGKILLed dtt
// would leave the tool (uv's python included) running. A dtt that hasn't
// exited dttStopGrace later is killed.
func runDTT(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = dttStopGrace
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && len(bytes.TrimSpace(out)) == 0 {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%v: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}

// call runs "dtt <args> --json" and decodes its answer into v (if not nil).
// An {"ok": false} answer is returned as its error.
func (w *toolsWatch) call(ctx context.Context, v any, stdin []byte, args ...string) error {
	bin, err := w.resolve()
	if err != nil {
		return err
	}
	out, err := w.dtt(ctx, bin, stdin, append(args, "--json")...)
	if err != nil {
		return fmt.Errorf("%s %s: %w", dttBin, args[0], err)
	}
	var head struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &head); err != nil {
		return fmt.Errorf("%s %s: decoding its answer: %v", dttBin, args[0], err)
	}
	if !head.OK {
		if head.Error == "" {
			head.Error = fmt.Sprintf("%s %s failed", dttBin, args[0])
		}
		return errors.New(head.Error)
	}
	if v != nil {
		if err := json.Unmarshal(out, v); err != nil {
			return fmt.Errorf("%s %s: decoding its answer: %v", dttBin, args[0], err)
		}
	}
	return nil
}

// ---- The catalog ----

// reinspect asks watchLoop to list now rather than at its next poll -- the
// tab's "re-inspect" control.
func (w *toolsWatch) reinspect() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *toolsWatch) watchLoop() {
	// Runs' working directories from a previous LR are gone with its
	// history.
	os.RemoveAll(w.runDir)
	for {
		w.check()
		select {
		case <-time.After(toolsPollInterval):
		case <-w.wake:
			// Show the re-inspection as "checking" (keeping the last
			// catalog) until it finishes.
			w.mu.Lock()
			w.state.Status, w.state.Detail = connChecking, ""
			w.mu.Unlock()
			w.notifyCatalog()
		}
	}
}

// check lists the tools once and publishes the catalog (always, so
// checked_at moves), and the control ops made from it.
func (w *toolsWatch) check() {
	next := w.list()
	w.mu.Lock()
	w.state = next
	w.mu.Unlock()
	if next.Status == connOK {
		setToolControlOps(w.controlOps(), true)
	} else {
		setToolControlOps(nil, false)
	}
	w.notifyCatalog()
}

func (w *toolsWatch) notifyCatalog() {
	if w.catalogChanged != nil {
		w.catalogChanged()
	} else if w.notify != nil {
		w.notify()
	}
}

func (w *toolsWatch) list() ToolsState {
	failed := func(err error) ToolsState {
		log.Printf("tools: listing failed: %v", err)
		return ToolsState{Status: connFailed, Detail: err.Error(), Tools: []ToolInfo{}, CheckedAt: time.Now().Unix()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolsCallTimeout)
	defer cancel()
	var out struct {
		Version  string         `json:"version"`
		Tools    []ToolInfo     `json:"tools"`
		UserDir  string         `json:"user_dir"`
		Dirs     []ToolDir      `json:"dirs"`
		Problems []ToolProblem  `json:"problems"`
		Shadowed []ToolShadowed `json:"shadowed"`
	}
	if err := w.call(ctx, &out, nil, "list"); err != nil {
		return failed(err)
	}
	if out.Tools == nil {
		out.Tools = []ToolInfo{}
	}
	for _, p := range out.Problems {
		log.Printf("tools: dtt skipped %s: %s", p.Path, p.Error)
	}
	return ToolsState{Status: connOK, Version: out.Version, Tools: out.Tools, UserDir: out.UserDir, Dirs: out.Dirs,
		Problems: out.Problems, Shadowed: out.Shadowed, CheckedAt: time.Now().Unix()}
}

// snapshot returns a copy of the catalog with each tool's allowed state;
// nil-safe so the system-state snapshot can be built before the watch
// exists.
func (w *toolsWatch) snapshot() ToolsState {
	if w == nil {
		return ToolsState{Status: connChecking, Tools: []ToolInfo{}, Allow: []string{}}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	st := w.state
	st.Tools = make([]ToolInfo, len(w.state.Tools))
	for i, t := range w.state.Tools {
		t.Allowed = toolAllowed(t.Name, w.allow)
		st.Tools[i] = t
	}
	st.Allow = append([]string{}, w.allow...)
	st.AllowFrom = w.allowFrom
	return st
}

// tabState is tools-state.
func (w *toolsWatch) tabState() ToolsStateMsg {
	st := ToolsStateMsg{ToolsState: w.snapshot(), Runs: []ToolRun{}}
	if w == nil {
		return st
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, r := range w.runs {
		cp := *r
		cp.Saved = append([]ToolSavedFile(nil), r.Saved...)
		st.Runs = append(st.Runs, cp)
	}
	return st
}

// tabStateForAC is tools-state cut to fit a representable line: runs' logs
// trimmed, and the oldest runs left out until it fits.
func (w *toolsWatch) tabStateForAC() ToolsStateMsg {
	st := w.tabState()
	for i, r := range st.Runs {
		if r.Result != nil && len(r.Result.LogTail) > maxToolLogRelayed {
			res := *r.Result
			res.LogTail = "…" + res.LogTail[len(res.LogTail)-maxToolLogRelayed:]
			st.Runs[i].Result = &res
		}
	}
	for {
		b, err := json.Marshal(st)
		if err != nil || len(b) <= maxRelayedTools {
			return st
		}
		if len(st.Runs) == 0 {
			st.Tools, st.Problems, st.Shadowed = []ToolInfo{}, nil, nil
			st.Detail = "the catalog is too large to relay through agent-coordinator -- see this host's own tools tab"
			return st
		}
		st.Runs = st.Runs[:len(st.Runs)-1]
		st.RunsOmitted++
	}
}

// tool looks name up in the catalog. known is false until dtt has listed
// its tools.
func (w *toolsWatch) tool(name string) (info ToolInfo, ok, known bool) {
	if w == nil {
		return ToolInfo{}, false, false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	known = w.state.Status == connOK || len(w.state.Tools) > 0
	for _, t := range w.state.Tools {
		if t.Name == name {
			t.Allowed = toolAllowed(t.Name, w.allow)
			return t, true, known
		}
	}
	return ToolInfo{}, false, known
}

// ---- tools-allow ----

// setAllow takes the tools-allow setting: the tools control runs and
// agent-coordinator may run, by name or glob (path.Match: *, ?, [...]); a
// tool.<name> op name works too. "*" (the default) or none allows every
// tool.
func (w *toolsWatch) setAllow(entries []string, from string) error {
	var clean []string
	all := false
	for _, e := range entries {
		if e == toolsAllowAll {
			all = true
			continue
		}
		name := strings.TrimPrefix(e, toolOpPrefix)
		if _, err := path.Match(name, ""); err != nil {
			return fmt.Errorf("tools-allow: bad pattern %q: %v", e, err)
		}
		clean = append(clean, name)
	}
	if all {
		clean = nil
	}
	w.mu.Lock()
	w.allow, w.allowFrom = clean, from
	w.mu.Unlock()
	return nil
}

func (w *toolsWatch) getAllow() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.allow
}

// toolAllowed reports whether allow lets tool name run; empty allows all.
func toolAllowed(name string, allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	for _, a := range allow {
		if ok, _ := path.Match(a, name); ok {
			return true
		}
	}
	return false
}

// checkAllowed refuses a tool tools-allow doesn't let run, saying how to
// change that.
func (w *toolsWatch) checkAllowed(name string) error {
	w.mu.RLock()
	allow, from := w.allow, w.allowFrom
	w.mu.RUnlock()
	if toolAllowed(name, allow) {
		return nil
	}
	msg := fmt.Sprintf("tool %s isn't allowed by tools-allow: this node's setting is %s", name, strings.Join(allow, ", "))
	if from != "" {
		msg += ", from " + from
	}
	return errors.New(msg + `; add it, or set it to "*", to allow every tool`)
}

// ---- Runs ----

// toolTimeout is how long LR gives a run of t: its own timeout, which dtt
// enforces, plus a margin for dtt (and uv) to start.
func toolTimeout(t ToolInfo) time.Duration {
	d, err := time.ParseDuration(t.Timeout)
	if err != nil || d <= 0 {
		d = toolsDefaultLimit
	}
	return d + toolsRunMargin
}

// execute runs tool name once through "dtt run" in workdir, with args as
// its inputs. Closing cancel (which may be nil) stops it. A tool that ran
// and failed comes back as a result with OK false; the error is only for a
// dtt that couldn't be run or answered nonsense.
func (w *toolsWatch) execute(name string, args map[string]string, workdir string, limit time.Duration, cancel <-chan struct{}) (ToolRunResult, error) {
	if args == nil {
		args = map[string]string{}
	}
	in, err := json.Marshal(args)
	if err != nil {
		return ToolRunResult{}, err
	}
	bin, err := w.resolve()
	if err != nil {
		return ToolRunResult{}, err
	}
	ctx, stop := context.WithTimeout(context.Background(), limit)
	defer stop()
	if cancel != nil {
		go func() {
			select {
			case <-cancel:
				stop()
			case <-ctx.Done():
			}
		}()
	}
	out, err := w.dtt(ctx, bin, in, "run", name, "--input", "-", "--workdir", workdir, "--json")
	if err != nil {
		if ctx.Err() != nil {
			return ToolRunResult{}, fmt.Errorf("%s run %s: stopped: %v", dttBin, name, ctx.Err())
		}
		return ToolRunResult{}, fmt.Errorf("%s run %s: %w", dttBin, name, err)
	}
	var res ToolRunResult
	if err := json.Unmarshal(out, &res); err != nil {
		return ToolRunResult{}, fmt.Errorf("%s run %s: decoding its answer: %v", dttBin, name, err)
	}
	if res.Tool == "" {
		res.Tool = name
	}
	return res, nil
}

// newRun adds a running run to the history, dropping the oldest finished
// ones (and their working directories) past maxToolRuns.
func (w *toolsWatch) newRun(tool string, args map[string]string, origin string, keepFiles bool) *ToolRun {
	w.mu.Lock()
	w.seq++
	r := &ToolRun{
		ID:        fmt.Sprintf("%s-%d", time.Now().Format("20060102-150405"), w.seq),
		Tool:      tool,
		Args:      args,
		Origin:    origin,
		Status:    "running",
		StartedAt: time.Now().UnixMilli(),
		FilesKept: keepFiles,
	}
	r.workdir = filepath.Join(w.runDir, r.ID)
	w.runs = append([]*ToolRun{r}, w.runs...)
	for i := len(w.runs) - 1; len(w.runs) > maxToolRuns && i >= 0; i-- {
		if old := w.runs[i]; old.Status != "running" {
			os.RemoveAll(old.workdir)
			w.runs = append(w.runs[:i], w.runs[i+1:]...)
		}
	}
	w.mu.Unlock()
	if w.notify != nil {
		w.notify()
	}
	return r
}

// finishRun records how run id went.
func (w *toolsWatch) finishRun(id string, res ToolRunResult, err error, saved ...ToolSavedFile) {
	if err != nil {
		res = ToolRunResult{Tool: res.Tool, Outputs: map[string]string{}, Files: []ToolRunFile{}, ExitCode: -1, Error: err.Error()}
	}
	w.mu.Lock()
	for _, r := range w.runs {
		if r.ID != id {
			continue
		}
		r.Status = "failed"
		if res.OK {
			r.Status = "ok"
		}
		if res.Tool == "" {
			res.Tool = r.Tool
		}
		r.Result = &res
		r.FinishedAt = time.Now().UnixMilli()
		r.Saved = append(r.Saved, saved...)
	}
	w.mu.Unlock()
	if w.notify != nil {
		w.notify()
	}
}

// startRun starts a run of tool name in the background, returning its id;
// it shows in tools-state as it goes. checkAllow applies tools-allow (for
// agent-coordinator's runs).
func (w *toolsWatch) startRun(name string, args map[string]string, origin string, checkAllow bool) (string, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), toolOpPrefix)
	info, ok, known := w.tool(name)
	switch {
	case !known:
		return "", errors.New("dtt hasn't listed its tools yet -- see the tools tab's status")
	case !ok:
		return "", fmt.Errorf("no tool %q -- the catalog lists the ones dtt found", name)
	}
	if checkAllow {
		if err := w.checkAllowed(name); err != nil {
			return "", err
		}
	}
	r := w.newRun(name, args, origin, true)
	go func() {
		res, err := w.execute(name, args, r.workdir, toolTimeout(info), nil)
		w.finishRun(r.ID, res, err)
	}()
	return r.ID, nil
}

// saveRun saves run id into the files tab, as a control run's "save to
// files" does (controlsave.go): a .zip of report.txt, result.json, the log
// and every output file still on disk.
func (w *toolsWatch) saveRun(id, node string) (ToolSavedFile, error) {
	w.mu.RLock()
	var run *ToolRun
	for _, r := range w.runs {
		if r.ID == id {
			cp := *r
			run = &cp
		}
	}
	w.mu.RUnlock()
	switch {
	case run == nil:
		return ToolSavedFile{}, fmt.Errorf("run %s is no longer kept -- only the last %d runs are", id, maxToolRuns)
	case run.Status == "running":
		return ToolSavedFile{}, errors.New("the run is still going -- save it once it ends")
	case w.saveFile == nil:
		return ToolSavedFile{}, errors.New("there's no files tab to save into")
	}
	data, err := toolRunZip(*run, node)
	if err != nil {
		return ToolSavedFile{}, fmt.Errorf("preparing the run's file: %v", err)
	}
	name := fmt.Sprintf("lr-tool-%s-%s.zip", run.Tool, time.UnixMilli(run.StartedAt).Format("2006-01-02T15-04-05"))
	info, err := w.saveFile(name, bytes.NewReader(data))
	if err != nil {
		return ToolSavedFile{}, err
	}
	saved := ToolSavedFile{FileID: info.ID, Name: info.Name}
	w.mu.Lock()
	for _, r := range w.runs {
		if r.ID == id {
			r.Saved = append(r.Saved, saved)
		}
	}
	w.mu.Unlock()
	if w.notify != nil {
		w.notify()
	}
	return saved, nil
}

// toolRunZip builds the .zip saveRun saves.
func toolRunZip(run ToolRun, node string) ([]byte, error) {
	res := run.Result
	if res == nil {
		res = &ToolRunResult{}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "local-representative tool run: %s\n", run.Tool)
	fmt.Fprintf(&b, "node: %s\nrun: %s (from the %s)\nstarted: %s\n", node, run.ID, run.Origin, time.UnixMilli(run.StartedAt).Format(time.RFC3339))
	if res.OK {
		fmt.Fprintf(&b, "result: succeeded in %s\n", formatSeconds(res.DurationMs))
	} else {
		fmt.Fprintf(&b, "result: FAILED after %s: %s\n", formatSeconds(res.DurationMs), res.Error)
	}
	writeMap := func(head string, m map[string]string) {
		if len(m) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n", head)
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", k, strings.ReplaceAll(m[k], "\n", "\n    "))
		}
	}
	writeMap("inputs", run.Args)
	writeMap("outputs", res.Outputs)

	var files []zipEntry
	used := map[string]bool{}
	if len(res.Files) > 0 {
		b.WriteString("\nfiles:\n")
	}
	for _, f := range res.Files {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			fmt.Fprintf(&b, "  %s: %s (not included: %v)\n", f.Name, f.Path, err)
			continue
		}
		name := uniqueZipName("files/"+sanitizeFilename(filepath.Base(f.Path)), used)
		files = append(files, zipEntry{name, data})
		fmt.Fprintf(&b, "  %s: %s\n", f.Name, name)
	}
	js, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return nil, err
	}
	entries := append([]zipEntry{{"report.txt", []byte(b.String())}, {"result.json", js}, {"log.txt", []byte(res.LogTail + "\n")}}, files...)
	return buildRunZip(entries)
}

// copyToFiles copies a tool's output file into the files tab.
func (w *toolsWatch) copyToFiles(name, p string) (FileInfo, error) {
	if w.saveFile == nil {
		return FileInfo{}, errors.New("there's no files tab to copy into")
	}
	st, err := os.Stat(p)
	if err != nil {
		return FileInfo{}, err
	}
	if st.Size() > controlMaxCopy {
		return FileInfo{}, fmt.Errorf("it's too big to copy (%d bytes; the most is %d)", st.Size(), controlMaxCopy)
	}
	f, err := os.Open(p)
	if err != nil {
		return FileInfo{}, err
	}
	defer f.Close()
	return w.saveFile(name, f)
}

// ---- Control ops ----

// controlOps makes each listed tool's tool.<name> control op.
func (w *toolsWatch) controlOps() []ControlOpSpec {
	w.mu.RLock()
	defer w.mu.RUnlock()
	ops := make([]ControlOpSpec, 0, len(w.state.Tools))
	for _, t := range w.state.Tools {
		ops = append(ops, toolControlOp(t))
	}
	return ops
}

// toolControlOp makes tool t's control op: its inputs as arguments (an
// input is a ControlOpArg as it stands), plus save_prefix and hint.
func toolControlOp(t ToolInfo) ControlOpSpec {
	args := make([]ControlOpArg, 0, len(t.Args)+2)
	for _, a := range t.Args {
		args = append(args, ControlOpArg{Name: a.Name, Help: a.Help, Required: a.Required, Default: a.Default})
	}
	args = append(args,
		ControlOpArg{Name: "save_prefix", Help: "put in front of each output value's name as it's saved, e.g. tree_ to save root as {{tree_root}}"},
		hintArg,
	)
	summary := "tool: " + t.Summary
	var saves []string
	for _, v := range t.Values {
		saves = append(saves, "{{"+v.Name+"}}")
	}
	if len(saves) > 0 {
		summary += " Saves " + strings.Join(saves, ", ") + "."
	}
	if len(t.Files) > 0 {
		summary += " Copies its output files into the files tab."
	}
	describe := t.Describe
	if describe == "" {
		describe = "run tool " + t.Name
	}
	name := t.Name
	return ControlOpSpec{
		Op:       toolOpPrefix + name,
		Summary:  summary,
		Describe: "tool: " + describe,
		Args:     args,
		run:      func(r *controlRun, a opArgs) (string, error) { return opTool(r, name, a) },
	}
}

// opTool runs tool name for a control run: through dtt, checked against
// tools-allow, its output values saved under their names (after
// save_prefix) and its output files copied into the files tab.
func opTool(r *controlRun, name string, a opArgs) (string, error) {
	w := r.s.tools
	if w == nil {
		return "", errors.New("tools aren't available on this node")
	}
	if err := w.checkAllowed(name); err != nil {
		return "", err
	}
	info, ok, _ := w.tool(name)
	if !ok {
		return "", fmt.Errorf("no tool %q here -- the tools tab lists the ones dtt found", name)
	}
	prefix := strings.TrimSpace(a["save_prefix"])
	inputs := map[string]string{}
	for k, v := range a {
		if k != "save_prefix" && k != "hint" {
			inputs[k] = v
		}
	}
	run := w.newRun(name, inputs, "control run", false)
	defer os.RemoveAll(run.workdir)
	r.note(fmt.Sprintf("running tool %s through dtt…", name))
	res, err := w.execute(name, inputs, run.workdir, toolTimeout(info), r.cancel)
	cancelled := func() bool {
		select {
		case <-r.cancel:
			return true
		default:
			return false
		}
	}
	if err != nil {
		w.finishRun(run.ID, res, err)
		if cancelled() {
			return "", errControlCancelled
		}
		return "", err
	}

	var saved []ToolSavedFile
	var skipped []string
	for _, f := range res.Files {
		fi, err := w.copyToFiles(r.s.lrName+"_"+name+"_"+filepath.Base(f.Path), f.Path)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%s): %v", f.Name, f.Path, err))
			continue
		}
		r.e.addValue("file from tool."+name, f.Name+" → "+fi.Name+" (files tab)")
		r.e.addRecording(ControlRecording{Who: r.s.lrName, FileID: fi.ID, Name: fi.Name, File: true, Path: f.Path})
		saved = append(saved, ToolSavedFile{FileID: fi.ID, Name: fi.Name})
	}
	w.finishRun(run.ID, res, nil, saved...)
	if !res.OK {
		if cancelled() {
			return "", errControlCancelled
		}
		return "", fmt.Errorf("tool %s: %s", name, res.Error)
	}

	keys := make([]string, 0, len(res.Outputs))
	for k := range res.Outputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := r.save(prefix+k, res.Outputs[k]); err != nil {
			return "", fmt.Errorf("tool %s's output %s: %v", name, k, err)
		}
		r.e.addValue("tool."+name+" {{"+prefix+k+"}}", res.Outputs[k])
	}
	note := fmt.Sprintf("tool %s: ok in %s, saved %d value(s)", name, formatSeconds(res.DurationMs), len(keys))
	if len(saved) > 0 {
		note += fmt.Sprintf(", copied %d file(s) into the files tab", len(saved))
	}
	if len(skipped) > 0 {
		note += "; not copied: " + strings.Join(skipped, "; ")
	}
	return note, nil
}

// ---- Requests ----

// handle answers one request. fromAC applies tools-allow to runs, which
// the tab's own runner isn't limited by.
func (w *toolsWatch) handle(req ToolsRequest, fromAC bool, node string) ToolsReply {
	reply := ToolsReply{Req: req.Req, Op: req.Op}
	fail := func(err error) ToolsReply {
		reply.Error = err.Error()
		return reply
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolsCallTimeout)
	defer cancel()
	// describe, validate, install and create answer {"ok", "tool"} (create
	// also "path").
	var answer struct {
		Tool *ToolInfo `json:"tool"`
		Path string    `json:"path"`
	}
	withTool := func(format string) ToolsReply {
		if answer.Tool == nil {
			return fail(fmt.Errorf("%s %s answered without the tool", dttBin, req.Op))
		}
		reply.OK, reply.Tool = true, answer.Tool
		reply.Tool.Allowed = toolAllowed(reply.Tool.Name, w.getAllow())
		if format != "" {
			reply.Detail = strings.NewReplacer("{name}", answer.Tool.Name, "{path}", answer.Tool.Path, "{dir}", answer.Path).Replace(format)
		}
		return reply
	}

	switch req.Op {
	case "reinspect":
		w.reinspect()
		reply.OK = true
		return reply
	case "run":
		origin := "tools tab"
		if fromAC {
			origin = "agent-coordinator"
		}
		id, err := w.startRun(req.Tool, req.Args, origin, fromAC)
		if err != nil {
			return fail(err)
		}
		reply.OK, reply.RunID = true, id
		return reply
	case "save":
		f, err := w.saveRun(req.Run, node)
		if err != nil {
			return fail(err)
		}
		reply.OK, reply.File = true, &f
		return reply
	case "describe":
		name := strings.TrimPrefix(strings.TrimSpace(req.Tool), toolOpPrefix)
		if !toolNameRe.MatchString(name) {
			return fail(fmt.Errorf("%q isn't a tool name", req.Tool))
		}
		if err := w.call(ctx, &answer, nil, "describe", name); err != nil {
			return fail(err)
		}
		return withTool("")
	case "validate":
		var err error
		switch {
		case strings.TrimSpace(req.Source) != "":
			err = w.call(ctx, &answer, []byte(req.Source), "validate", "-", "--schema-only")
		case strings.TrimSpace(req.Path) != "":
			p, perr := hostToolPath(req.Path)
			if perr != nil {
				return fail(perr)
			}
			err = w.call(ctx, &answer, nil, "validate", p)
		default:
			return fail(errors.New("paste a tool.yaml or pick a tool's directory first"))
		}
		if err != nil {
			return fail(err)
		}
		return withTool("{name} is a valid tool")
	case "install":
		p, err := hostToolPath(req.Path)
		if err != nil {
			return fail(err)
		}
		args := []string{"install", p}
		if req.Force {
			args = append(args, "--force")
		}
		if err := w.call(ctx, &answer, nil, args...); err != nil {
			return fail(err)
		}
		w.reinspect()
		return withTool("installed {name} at {path}")
	case "create":
		name := strings.TrimSpace(req.Name)
		if !toolNameRe.MatchString(name) {
			return fail(fmt.Errorf("%q: tool names are lowercase letters, digits and '-', starting with a letter", req.Name))
		}
		runtime := req.Runtime
		if runtime == "" {
			runtime = "uv"
		}
		if runtime != "uv" && runtime != "exec" {
			return fail(fmt.Errorf("runtime %q should be uv or exec", runtime))
		}
		if err := w.call(ctx, &answer, nil, "create", name, "--install", "--runtime", runtime); err != nil {
			return fail(err)
		}
		w.reinspect()
		return withTool("created {name} at {dir} -- edit its tool.yaml and script there, then re-inspect")
	}
	return fail(fmt.Errorf("unknown tools op %q", req.Op))
}

// hostToolPath checks a path picked on this host for validate or install:
// absolute (~ for home), a tool's directory or its tool.yaml (install takes
// the directory).
func hostToolPath(p string) (string, error) {
	p, err := expandHome(strings.TrimSpace(p))
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", errors.New("pick a tool's directory (or its tool.yaml) first")
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q isn't an absolute path", p)
	}
	if filepath.Base(p) == "tool.yaml" {
		p = filepath.Dir(p)
	}
	return filepath.Clean(p), nil
}

// ---- Server wiring ----

// broadcastToolsState sends tools-state to this LR's tabs and
// agent-coordinator.
func (s *Server) broadcastToolsState() {
	s.broadcast("tools-state", s.tools.tabState())
	if ac := s.getACClient(); ac != nil {
		ac.SendData("tools-state", s.tools.tabStateForAC())
	}
}

// toolsCatalogChanged also refreshes what the catalog feeds: the system
// tab's snapshot and the control library's ops.
func (s *Server) toolsCatalogChanged() {
	s.broadcastToolsState()
	s.broadcastSystemState()
	s.control.broadcastLibrary()
}

// saveToolFile puts a tool's file into the files tab.
func (s *Server) saveToolFile(name string, src io.Reader) (FileInfo, error) {
	info, err := s.saveFileFrom(name, src)
	if err == nil {
		s.broadcastFiles()
	}
	return info, err
}

// handleToolsCommand runs agent-coordinator's per-host tools tab:
// "__tools:<op> <json>" is a ToolsRequest (its op from the prefix), and the
// answer goes back up as "tools-reply". Requests run in the background:
// some call dtt.
func (s *Server) handleToolsCommand(cmd string) {
	rest := strings.TrimPrefix(cmd, "__tools:")
	op, body, _ := strings.Cut(rest, " ")
	var req ToolsRequest
	if strings.TrimSpace(body) != "" {
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			s.sendToolsReplyToAC(ToolsReply{Op: op, Error: "bad tools request: " + err.Error()})
			return
		}
	}
	req.Op = strings.TrimSpace(op)
	go func() {
		s.sendToolsReplyToAC(s.tools.handle(req, true, s.lrName))
	}()
}

func (s *Server) sendToolsReplyToAC(reply ToolsReply) {
	if ac := s.getACClient(); ac != nil {
		ac.SendData("tools-reply", reply)
	}
}
