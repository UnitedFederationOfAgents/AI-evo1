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
	"strconv"
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
// anything sent from agent-coordinator -- may run; the tab's own calls
// sub-tab here isn't limited by it.
//
// The tab's history sub-tab lists earlier dtt calls from anywhere on this
// host -- an agent's, a shell's, the tab's own -- as clauditable keeps them
// in <records root>/dtt-calls (see "History" below), read on request.

const (
	toolsPollInterval  = 10 * time.Minute
	toolsCallTimeout   = 2 * time.Minute  // list, describe and the author ops
	toolsRunMargin     = 2 * time.Minute  // on top of a tool's own timeout, for dtt to start uv or build a go tool
	toolsDefaultLimit  = 2 * time.Minute  // a tool whose timeout couldn't be read
	dttStopGrace       = 10 * time.Second // after SIGTERM, for dtt to kill its tool and answer
	maxToolRuns        = 20
	maxToolRunEvents   = 200      // held per run; past it, only the last event goes on being replaced
	maxToolLogRelayed  = 1 << 10  // of each run's log, in tools-state relayed to agent-coordinator
	maxRelayedTools    = 48 << 10 // a representable line is read up to 64 KiB

	// toolImageOutput is the output a declarative tool returns its image in.
	toolImageOutput = "image_json"

	dttBin = "dtt"

	// toolsAllowAll is the tools-allow entry, and its default, that allows
	// every tool.
	toolsAllowAll = "*"
)

// toolNameRe matches dtt's tool names (dtt/schema.go).
var toolNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// ToolArg mirrors dtt's ToolArg: a ControlOpArg plus the input's type and
// choices, for the calls sub-tab's form.
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
	// Events are the run's coarse events, LR's and (once it answers) dtt's,
	// in order -- held here, in memory, with the run, until "to file" sends
	// them (with the rest of the run) into the files tab. EventCount is
	// len(Events), kept when the relay to agent-coordinator leaves them out.
	// Past maxToolRunEvents the last one is replaced by each new one, and
	// EventsDropped counts those lost.
	Events        []ToolRunEvent `json:"events"`
	EventCount    int            `json:"event_count"`
	EventsDropped int            `json:"events_dropped,omitempty"`
	// Progress is how a declarative tool's work went, step by step, as
	// dtt reported it while running (toolprogress.go); nil for a tool that
	// reports none.
	Progress *ToolRunProgress `json:"progress,omitempty"`

	workdir string
}

// ToolRunEvent is one coarse step of a run: At is unix ms, From "lr" or
// "dtt" (dtt's RunEvent, from its answer).
type ToolRunEvent struct {
	At     int64  `json:"at"`
	From   string `json:"from"`
	Event  string `json:"event"`
	Detail string `json:"detail,omitempty"`
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
	// Events are dtt's, which finishRun moves into the run's own.
	Events []ToolRunEvent `json:"events,omitempty"`
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

// ToolExample mirrors dtt's Example ("dtt examples --json"): an example
// built into dtt -- a DTT document, or (Kind "call") a tool call's inputs --
// which the tab's examples sub-tab imports into its calls sub-tab as a runnable
// instance of Tool.
type ToolExample struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Tool        string            `json:"tool"` // "" when no one built-in tool runs it
	Providers   []string          `json:"providers"`
	Inputs      map[string]string `json:"inputs,omitempty"` // a call's inputs
	Commands    []string          `json:"commands"`
	Path        string            `json:"path"`
	Source      string            `json:"source"`
	Error       string            `json:"error,omitempty"`
}

// ToolsRequest is the tools tab's "tools-request", and the JSON of
// agent-coordinator's "__tools:<op> <json>".
type ToolsRequest struct {
	Req  string `json:"req,omitempty"` // echoed in the reply
	Op   string `json:"op"`            // reinspect, run, save, to-file, describe, validate, install, create, examples, example-calls, history, history-detail, recent-calls, call-detail
	Tool string `json:"tool,omitempty"`
	// Call is history-detail's call: its file name in dtt-calls.
	Call string `json:"call,omitempty"`
	// Args are run's inputs, by name; an empty one takes the input's default.
	Args map[string]string `json:"args,omitempty"`
	Run  string            `json:"run,omitempty"` // save, to-file: the run's id
	// validate checks Source (tool.yaml's text, schema only) or the tool at
	// Path (a directory or tool.yaml on this host); install installs the
	// tool at Path, replacing an installed copy with Force. protosurf maps
	// the project at Path, else this host's own (protosurf.go). call-detail
	// describes the call at Path: a call's directory or document.
	Source string `json:"source,omitempty"`
	Path   string `json:"path,omitempty"`
	Force  bool   `json:"force,omitempty"`
	// create scaffolds tool Name in the tools directory, runtime python or go.
	Name    string `json:"name,omitempty"`
	Runtime string `json:"runtime,omitempty"`
	// The demo tab's ops (demos.go): demo-run runs Demo in Mode (assert by
	// default) with Variables, or -- Mode delete -- undoes Record's run;
	// demo-record is Record in full; demo-duplicate copies Demo into the
	// host's demo library as Name; demo-keep-videos fetches Record's videos
	// from the files tab into it, those it hasn't kept yet.
	Demo      string            `json:"demo,omitempty"`
	Mode      string            `json:"mode,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`
	Record    string            `json:"record,omitempty"`
	// suite-run runs Suite's demos in turn with Variables (suites.go);
	// demo-review asks the worker to review Record, read-only, with an
	// optional Question and Agent/Model; demo-reviews lists Record's
	// reviews (demoreview.go).
	Suite    string `json:"suite,omitempty"`
	Question string `json:"question,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Model    string `json:"model,omitempty"`
	// The proposals sub-tab's (demoproposals.go): demo-proposal is Name in
	// full (its description and files); demo-proposal-agent-create keeps
	// Proposal's name, title and description as a new proposal and has the
	// worker's agent build its demo, with an optional Agent/Model;
	// demo-proposal-delete deletes Name.
	Proposal *DemoProposal `json:"proposal,omitempty"`
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
	File   *ToolSavedFile `json:"file,omitempty"`   // save, to-file
	// Examples are dtt's example documents (examples).
	Examples []ToolExample `json:"examples,omitempty"`
	// History is the recorded dtt calls, newest first (history);
	// HistoryOmitted counts older ones left out (past maxToolHistory, or to
	// fit the relay to agent-coordinator). Call is one of them in full
	// (history-detail).
	History        []ToolHistoryEntry `json:"history,omitempty"`
	HistoryOmitted int                `json:"history_omitted,omitempty"`
	Call           *ToolHistoryDetail `json:"call,omitempty"`
	// RecentCalls are dtt's recent calls, most recently executed first, as
	// listed in RecentCallsFile (recent-calls); CallDetail is one call
	// described (call-detail).
	RecentCalls     []ToolCall      `json:"recent_calls,omitempty"`
	RecentCallsFile string          `json:"recent_calls_file,omitempty"`
	CallDetail      *ToolCallDetail `json:"call_detail,omitempty"`
	// ExampleCalls are dtt's example tool call specifications, each an
	// in-place call unpacked on this host (example-calls).
	ExampleCalls []ToolExampleCall `json:"example_calls,omitempty"`
	// The demo tab's (demos.go): demos lists the library (Demos) and the
	// records of past runs (DemoRecords, newest first), kept under DemoDir;
	// demo-run answers the record it started (DemoRecord, with RunID);
	// demo-record answers one record with its image (DemoImage, the image's
	// JSON).
	Demos       []DemoInfo   `json:"demos,omitempty"`
	DemoRecords []DemoRecord `json:"demo_records,omitempty"`
	DemoRecord  *DemoRecord  `json:"demo_record,omitempty"`
	DemoImage   string       `json:"demo_image,omitempty"`
	DemoDir     string       `json:"demo_dir,omitempty"`
	// demos also lists the suites (Suites) and their runs (SuiteRecords,
	// newest first), and the free-text variables' last values
	// (DemoLastValues); suite-run answers the suite record it started
	// (SuiteRecord). demo-review and demo-reviews answer Record's reviews,
	// newest first (DemoReviews).
	Suites         []SuiteInfo       `json:"suites,omitempty"`
	SuiteRecords   []SuiteRecord     `json:"suite_records,omitempty"`
	SuiteRecord    *SuiteRecord      `json:"suite_record,omitempty"`
	DemoLastValues map[string]string `json:"demo_last_values,omitempty"`
	DemoReviews    []DemoReview      `json:"demo_reviews,omitempty"`
	// demos lists the demo proposals too (DemoProposals, newest first); the
	// demo-proposal-* ops answer one in full (DemoProposal).
	DemoProposals []DemoProposal `json:"demo_proposals,omitempty"`
	DemoProposal  *DemoProposal  `json:"demo_proposal,omitempty"`
	// protosurf answers the map of the project's content (protosurf.go).
	Protosurf *ProtosurfMap `json:"protosurf,omitempty"`
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
	// recordsRoot is AGENT_RECORDS_PATH, the records root whose dtt-calls
	// the history ops read; nil (or "") when there is none.
	recordsRoot func() string
	// demoRoot is the host directory the demo tab keeps its library and
	// records in (demos.go); nil (or "") when there is none.
	demoRoot func() string
	// demoFile is where files-tab entry id is on this host
	// (Server.locateFile), for a demo record to keep its videos; nil when
	// there's no files tab.
	demoFile    func(id string) (string, bool)
	demoMu      sync.Mutex // guards demoActive and suiteActive, and records' videos while kept
	demoActive  string     // the record of the demo running now, "" for none
	suiteActive string     // the suite record running now, "" for none (suites.go)
	// demoLastValues is the file the demo tab's free-text variables' last
	// values are kept in (suites.go); nil (or "") to keep none.
	demoLastValues func() string
	// reviewSubmit hands a demo record's review to the worker (Server.
	// submitDemoReview); workerSignals are the worker's signals as its last
	// state gave them. Either nil: no reviews here (demoreview.go).
	reviewSubmit  func(prompt, dir, agent, model string) error
	workerSignals func() []WorkerSignalInfo
	reviewMu      sync.Mutex // guards records' reviews/ and proposals' jobs/ while synced
	// proposalSubmit hands a demo proposal's creation job to the worker
	// (Server.submitDemoProposal), in write mode; nil: no proposals here
	// (demoproposals.go).
	proposalSubmit func(prompt, dir, agent, model string) error
	// protosurfRoot is the project the demo tab's protosurf area maps when
	// a request gives no Path (protosurf.go); nil for none.
	protosurfRoot func() (string, error)

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
//
// A ctx withDTTProgress has each of dtt's progress lines handed over as it
// prints it, and left out of what comes back.
func runDTT(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = dttStopGrace
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	var split *dttProgressSplitter
	if f := dttProgressFrom(ctx); f != nil {
		split = &dttProgressSplitter{emit: f, rest: &stdout}
		cmd.Stdout = split
	}
	err := cmd.Run()
	if split != nil {
		split.flush()
	}
	out := stdout.Bytes()
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
		st.Runs = append(st.Runs, r.copy())
	}
	return st
}

// copy is r with its slices copied, for reading outside w.mu.
func (r *ToolRun) copy() ToolRun {
	cp := *r
	cp.Saved = append([]ToolSavedFile(nil), r.Saved...)
	cp.Events = append([]ToolRunEvent{}, r.Events...)
	cp.EventCount = len(cp.Events)
	cp.Progress = copyProgress(r.Progress)
	return cp
}

// tabStateForAC is tools-state cut to fit a representable line: runs' logs
// trimmed, only the newest run's events and image (a declarative tool's
// image_json output, which the calls sub-tab puts in its form) kept, and then --
// until it fits -- those dropped too and the oldest runs left out. "to
// file" still sends a run's events whole, from here.
func (w *toolsWatch) tabStateForAC() ToolsStateMsg {
	st := w.tabState()
	for i := 1; i < len(st.Runs); i++ {
		st.Runs[i].Events = []ToolRunEvent{}
	}
	// Runs' progress comes without its steps' object ids (counted in
	// objects_omitted), and -- oldest first -- not at all when that's what
	// it takes to fit.
	for _, r := range st.Runs {
		if p := r.Progress; p != nil {
			for i := range p.Steps {
				p.Steps[i].ObjectsOmitted += len(p.Steps[i].Objects)
				p.Steps[i].Objects = nil
			}
		}
	}
	dropProgress := func() bool {
		for i := len(st.Runs) - 1; i >= 0; i-- {
			if st.Runs[i].Progress != nil {
				st.Runs[i].Progress = nil
				return true
			}
		}
		return false
	}
	dropImage := func(i int) bool {
		res := st.Runs[i].Result
		if res == nil {
			return false
		}
		if _, ok := res.Outputs[toolImageOutput]; !ok {
			return false
		}
		cp := *res
		cp.Outputs = map[string]string{}
		for k, v := range res.Outputs {
			if k != toolImageOutput {
				cp.Outputs[k] = v
			}
		}
		st.Runs[i].Result = &cp
		return true
	}
	kept := false
	for i, r := range st.Runs { // newest first
		if r.Result != nil && len(r.Result.LogTail) > maxToolLogRelayed {
			res := *r.Result
			res.LogTail = "…" + res.LogTail[len(res.LogTail)-maxToolLogRelayed:]
			st.Runs[i].Result = &res
		}
		if r.Result != nil && r.Result.Outputs[toolImageOutput] != "" {
			if kept {
				dropImage(i)
			}
			kept = true
		}
	}
	for {
		b, err := json.Marshal(st)
		if err != nil || len(b) <= maxRelayedTools {
			return st
		}
		dropped := false
		for i := range st.Runs {
			if dropImage(i) {
				dropped = true
				break
			}
		}
		if dropped || dropProgress() {
			continue
		}
		if len(st.Runs) > 0 && len(st.Runs[0].Events) > 0 {
			st.Runs[0].Events = []ToolRunEvent{}
			continue
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
// dtt that couldn't be run or answered nonsense. progress, when it isn't
// nil, is given the tool's progress lines as dtt prints them (dtt run
// --progress; toolprogress.go).
func (w *toolsWatch) execute(name string, args map[string]string, workdir string, limit time.Duration, cancel <-chan struct{}, progress func(line []byte)) (ToolRunResult, error) {
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
	argv := []string{"run", name, "--input", "-", "--workdir", workdir, "--json"}
	if progress != nil {
		ctx = withDTTProgress(ctx, progress)
		argv = []string{"run", name, "--input", "-", "--progress", "--workdir", workdir, "--json"}
	}
	out, err := w.dtt(ctx, bin, in, argv...)
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
	names := make([]string, 0, len(args))
	for k := range args {
		names = append(names, k)
	}
	sort.Strings(names)
	detail := fmt.Sprintf("from the %s, %d input(s)", origin, len(names))
	if len(names) > 0 {
		detail += ": " + strings.Join(names, ", ")
	}
	r.addEvent("lr", "run started", detail)
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

// addEvent adds one of the run's coarse events, now; the caller holds w.mu
// (or has the run to itself, in newRun).
func (r *ToolRun) addEvent(from, event, detail string) {
	r.addEventAt(ToolRunEvent{At: time.Now().UnixMilli(), From: from, Event: event, Detail: detail})
}

func (r *ToolRun) addEventAt(ev ToolRunEvent) {
	if len(r.Events) >= maxToolRunEvents {
		r.Events[len(r.Events)-1] = ev
		r.EventsDropped++
		return
	}
	r.Events = append(r.Events, ev)
}

// event adds an event to run id, if it's still kept.
func (w *toolsWatch) event(id, event, detail string) {
	w.mu.Lock()
	for _, r := range w.runs {
		if r.ID == id {
			r.addEvent("lr", event, detail)
		}
	}
	w.mu.Unlock()
	if w.notify != nil {
		w.notify()
	}
}

// finishRun records how run id went: dtt's events join the run's own,
// then LR's verdict.
func (w *toolsWatch) finishRun(id string, res ToolRunResult, err error, saved ...ToolSavedFile) {
	if err != nil {
		res = ToolRunResult{Tool: res.Tool, Outputs: map[string]string{}, Files: []ToolRunFile{}, ExitCode: -1, Error: err.Error()}
	}
	dttEvents := res.Events
	res.Events = nil
	w.mu.Lock()
	for _, r := range w.runs {
		if r.ID != id {
			continue
		}
		for _, ev := range dttEvents {
			ev.From = "dtt"
			r.addEventAt(ev)
		}
		// dtt's events took place while LR waited on it: into time order
		// with LR's own.
		sort.SliceStable(r.Events, func(i, j int) bool { return r.Events[i].At < r.Events[j].At })
		r.Status = "failed"
		if res.OK {
			r.Status = "ok"
		}
		if r.Progress != nil {
			r.Progress.finish(res.OK, res.Error)
		}
		if res.Tool == "" {
			res.Tool = r.Tool
		}
		r.Result = &res
		r.FinishedAt = time.Now().UnixMilli()
		r.Saved = append(r.Saved, saved...)
		for _, f := range saved {
			r.addEvent("lr", "copied into the files tab", f.Name)
		}
		took := formatSeconds(r.FinishedAt - r.StartedAt)
		switch {
		case err != nil:
			r.addEvent("lr", "run failed", fmt.Sprintf("after %s, dtt didn't answer: %v", took, err))
		case res.OK:
			r.addEvent("lr", "run ok", fmt.Sprintf("after %s, %d value(s), %d file(s)", took, len(res.Outputs), len(res.Files)))
		default:
			r.addEvent("lr", "run failed", fmt.Sprintf("after %s: %s", took, res.Error))
		}
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
	return w.startRunThen(name, args, origin, checkAllow, nil)
}

// startRunThen is startRun, calling done (when it isn't nil) with the run
// as it finished -- the demo tab's runs keep their record that way
// (demos.go).
func (w *toolsWatch) startRunThen(name string, args map[string]string, origin string, checkAllow bool, done func(ToolRun)) (string, error) {
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
		w.event(r.ID, "dtt called", dttCallDetail(name, info))
		res, err := w.execute(name, args, r.workdir, toolTimeout(info), nil, func(line []byte) { w.progress(r.ID, line) })
		w.finishRun(r.ID, res, err)
		if done != nil {
			if run := w.runCopy(r.ID); run != nil {
				done(*run)
			}
		}
	}()
	return r.ID, nil
}

// dttCallDetail is the "dtt called" event's detail.
func dttCallDetail(name string, info ToolInfo) string {
	return fmt.Sprintf("%s run %s (%s %s), allowed %s", dttBin, name, info.Runtime, info.Origin, toolTimeout(info))
}

// saveRun saves run id into the files tab, as a control run's "save to
// files" does (controlsave.go): a .zip of report.txt, result.json, the log
// and every output file still on disk.
func (w *toolsWatch) saveRun(id, node string) (ToolSavedFile, error) {
	run := w.runCopy(id)
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
	return w.keepSaved(id, name, data, "results saved to files")
}

// runCopy is a copy of run id, or nil when it's no longer kept.
func (w *toolsWatch) runCopy(id string) *ToolRun {
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, r := range w.runs {
		if r.ID == id {
			cp := r.copy()
			return &cp
		}
	}
	return nil
}

// keepSaved puts data into the files tab as name and lists it on run id,
// with an event saying so.
func (w *toolsWatch) keepSaved(id, name string, data []byte, event string) (ToolSavedFile, error) {
	info, err := w.saveFile(name, bytes.NewReader(data))
	if err != nil {
		return ToolSavedFile{}, err
	}
	saved := ToolSavedFile{FileID: info.ID, Name: info.Name}
	w.mu.Lock()
	for _, r := range w.runs {
		if r.ID == id {
			r.Saved = append(r.Saved, saved)
			r.addEvent("lr", event, saved.Name)
		}
	}
	w.mu.Unlock()
	if w.notify != nil {
		w.notify()
	}
	return saved, nil
}

// runToFile is the recent runs' "to file": run id's details -- its events
// above all, then its inputs, outputs, files and log -- as one text file in
// the files tab, for inspection there. Unlike saveRun it works while the
// run is still going (the details so far), and copies no output files.
func (w *toolsWatch) runToFile(id, node string) (ToolSavedFile, error) {
	run := w.runCopy(id)
	switch {
	case run == nil:
		return ToolSavedFile{}, fmt.Errorf("run %s is no longer kept -- only the last %d runs are", id, maxToolRuns)
	case w.saveFile == nil:
		return ToolSavedFile{}, errors.New("there's no files tab to send it to")
	}
	name := fmt.Sprintf("lr-tool-%s-%s-details.txt", run.Tool, time.UnixMilli(run.StartedAt).Format("2006-01-02T15-04-05"))
	return w.keepSaved(id, name, []byte(toolRunDetails(*run, node, time.Now())), "details sent to file")
}

// toolRunDetails is runToFile's text; now is when it was asked for.
func toolRunDetails(run ToolRun, node string, now time.Time) string {
	var b strings.Builder
	writeToolRunHead(&b, run, node)
	if run.Status == "running" {
		fmt.Fprintf(&b, "result: still running at %s\n", now.Format(time.RFC3339))
	}
	writeToolRunEvents(&b, run)
	res := run.Result
	if res == nil {
		res = &ToolRunResult{}
	}
	writeToolRunMap(&b, "inputs", run.Args)
	writeToolRunMap(&b, "outputs", res.Outputs)
	if len(res.Files) > 0 {
		b.WriteString("\nfiles:\n")
		for _, f := range res.Files {
			fmt.Fprintf(&b, "  %s: %s (%d bytes)\n", f.Name, f.Path, f.Size)
		}
	}
	if len(run.Saved) > 0 {
		b.WriteString("\nin the files tab:\n")
		for _, f := range run.Saved {
			fmt.Fprintf(&b, "  %s\n", f.Name)
		}
	}
	if res.Workdir != "" {
		fmt.Fprintf(&b, "\nworkdir: %s\n", res.Workdir)
	}
	if res.LogTail != "" {
		fmt.Fprintf(&b, "\nlog (its tail):\n%s\n", res.LogTail)
	}
	return b.String()
}

// writeToolRunHead writes what a run was and how it went.
func writeToolRunHead(b *strings.Builder, run ToolRun, node string) {
	res := run.Result
	if res == nil {
		res = &ToolRunResult{}
	}
	fmt.Fprintf(b, "local-representative tool run: %s\n", run.Tool)
	fmt.Fprintf(b, "node: %s\nrun: %s (from the %s)\nstarted: %s\n", node, run.ID, run.Origin, time.UnixMilli(run.StartedAt).Format(time.RFC3339))
	switch {
	case run.Status == "running":
	case res.OK:
		fmt.Fprintf(b, "result: succeeded in %s\n", formatSeconds(res.DurationMs))
	default:
		fmt.Fprintf(b, "result: FAILED after %s: %s\n", formatSeconds(res.DurationMs), res.Error)
	}
}

// writeToolRunEvents writes a run's events, each at its time since the run
// started.
func writeToolRunEvents(b *strings.Builder, run ToolRun) {
	if len(run.Events) == 0 {
		return
	}
	b.WriteString("\nevents:\n")
	for _, ev := range run.Events {
		fmt.Fprintf(b, "  +%-8s %-4s %s", formatSeconds(ev.At-run.StartedAt), ev.From, ev.Event)
		if ev.Detail != "" {
			fmt.Fprintf(b, ": %s", strings.ReplaceAll(ev.Detail, "\n", "\n      "))
		}
		b.WriteString("\n")
	}
	if run.EventsDropped > 0 {
		fmt.Fprintf(b, "  (%d more weren't kept: a run holds at most %d)\n", run.EventsDropped, maxToolRunEvents)
	}
}

// writeToolRunMap writes inputs or outputs, by name.
func writeToolRunMap(b *strings.Builder, head string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:\n", head)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "  %s: %s\n", k, strings.ReplaceAll(m[k], "\n", "\n    "))
	}
}

// toolRunZip builds the .zip saveRun saves.
func toolRunZip(run ToolRun, node string) ([]byte, error) {
	res := run.Result
	if res == nil {
		res = &ToolRunResult{}
	}
	var b strings.Builder
	writeToolRunHead(&b, run, node)
	writeToolRunMap(&b, "inputs", run.Args)
	writeToolRunMap(&b, "outputs", res.Outputs)
	writeToolRunEvents(&b, run)

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
	w.event(run.ID, "dtt called", dttCallDetail(name, info))
	res, err := w.execute(name, inputs, run.workdir, toolTimeout(info), r.cancel, func(line []byte) { w.progress(run.ID, line) })
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
			w.event(run.ID, "not copied into the files tab", skipped[len(skipped)-1])
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
// the tab's own calls sub-tab isn't limited by. The history ops trim their
// answer to fit the relay when fromAC.
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
	case "to-file":
		f, err := w.runToFile(req.Run, node)
		if err != nil {
			return fail(err)
		}
		reply.OK, reply.File = true, &f
		reply.Detail = "sent to the files tab as " + f.Name
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
		if runtime == "" || runtime == "uv" {
			runtime = "python"
		}
		if runtime != "python" && runtime != "go" {
			return fail(fmt.Errorf("runtime %q should be python or go", runtime))
		}
		if err := w.call(ctx, &answer, nil, "create", name, "--install", "--runtime", runtime); err != nil {
			return fail(err)
		}
		w.reinspect()
		return withTool("created {name} at {dir} -- edit its tool.yaml and script there, then re-inspect")
	case "examples":
		var out struct {
			Examples []ToolExample `json:"examples"`
		}
		if err := w.call(ctx, &out, nil, "examples"); err != nil {
			return fail(err)
		}
		if out.Examples == nil {
			out.Examples = []ToolExample{}
		}
		reply.OK, reply.Examples = true, out.Examples
		if b, err := json.Marshal(reply); fromAC && (err != nil || len(b) > maxRelayedTools) {
			return fail(errors.New("dtt's examples are too large to relay through agent-coordinator -- see this host's own tools tab"))
		}
		reply.Detail = fmt.Sprintf("%d example(s)", len(out.Examples))
		return reply
	case "history":
		entries, omitted, detail, err := readToolHistory(w.historyDir())
		if err != nil {
			return fail(err)
		}
		reply.OK, reply.History, reply.HistoryOmitted, reply.Detail = true, entries, omitted, detail
		if fromAC {
			fitHistoryReply(&reply)
		}
		return reply
	case "demos", "demo-run", "demo-record", "demo-duplicate", "demo-keep-videos", "suite-run", "demo-review", "demo-reviews",
		"demo-proposal", "demo-proposal-agent-create", "demo-proposal-delete":
		return w.handleDemo(req, fromAC, node, reply)
	case "protosurf":
		return w.handleProtosurf(req, fromAC, reply)
	case "history-detail":
		d, err := readToolHistoryDetail(w.historyDir(), req.Call)
		if err != nil {
			return fail(err)
		}
		reply.OK, reply.Call = true, d
		if fromAC {
			fitHistoryDetailReply(&reply)
		}
		return reply
	case "recent-calls":
		var out struct {
			File  string     `json:"file"`
			Calls []ToolCall `json:"calls"`
		}
		if err := w.call(ctx, &out, nil, "calls"); err != nil {
			return fail(err)
		}
		if out.Calls == nil {
			out.Calls = []ToolCall{}
		}
		reply.OK, reply.RecentCalls, reply.RecentCallsFile = true, out.Calls, out.File
		reply.Detail = fmt.Sprintf("%d recent call(s) in %s", len(out.Calls), out.File)
		if fromAC {
			fitRecentCallsReply(&reply)
		}
		return reply
	case "example-calls":
		var out struct {
			Specifications []ToolExampleCall `json:"specifications"`
		}
		if err := w.call(ctx, &out, nil, "examples", "--specifications"); err != nil {
			return fail(err)
		}
		if out.Specifications == nil {
			out.Specifications = []ToolExampleCall{}
		}
		reply.OK, reply.ExampleCalls = true, out.Specifications
		reply.Detail = fmt.Sprintf("%d example call(s)", len(out.Specifications))
		if fromAC {
			fitExampleCallsReply(&reply)
		}
		return reply
	case "call-detail":
		p, err := expandHome(strings.TrimSpace(req.Path))
		if err != nil {
			return fail(err)
		}
		if !filepath.IsAbs(p) {
			return fail(fmt.Errorf("%q isn't an absolute path -- give a call's directory or document on %s", req.Path, node))
		}
		var out struct {
			Call *ToolCallDetail `json:"call"`
		}
		if err := w.call(ctx, &out, nil, "calls", filepath.Clean(p)); err != nil {
			return fail(err)
		}
		if out.Call == nil {
			return fail(fmt.Errorf("%s calls answered without the call", dttBin))
		}
		if out.Call.Images == nil {
			out.Call.Images = []ToolCallImage{}
		}
		reply.OK, reply.CallDetail = true, out.Call
		if fromAC {
			fitCallDetailReply(&reply)
		}
		return reply
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

// ---- History ----

// The history sub-tab reads the copies clauditable keeps of dtt's calls: dtt
// re-runs itself under clauditable as a dtt_call event, and clauditable,
// besides recording it in the session as usual, copies the record to
// <records root>/dtt-calls/<unix-ts>-<rrr>.txt -- in its written-file
// format, the event naming the session and the session's own record
// (clauditable/layout.go). Nothing is held here: each request reads the
// directory afresh.

const (
	// toolHistoryDirName mirrors clauditable/layout.go's dttCallsDirName.
	toolHistoryDirName = "dtt-calls"

	maxToolHistory        = 200
	toolHistoryListRead   = 256 << 10 // of each record, for the list
	toolHistoryTailRead   = 8 << 10   // ...and of a longer one's end, for its image line
	toolHistoryDetailRead = 4 << 20   // of the record, for history-detail
	toolHistoryCommandMax = 300       // of a list entry's command

	// These mirror clauditable/pkg/records' written-file format:
	// WrittenFileSeparator, ResponseSeparator, and the line stderr follows.
	recordWrittenSeparator  = "\n----------WRITTEN_RAW----------\n"
	recordResponseSeparator = "\n\n----------RESPONSE----------\n\n"
	recordStderrMarker      = "[STDERR]\n"
	recordInputPrefix       = "IN>>"

	// dttImageFormat is dtt's image format (dtt/declare.go); dttImageKept
	// starts the stderr line naming where dtt kept a call's image.
	dttImageFormat = "dtt-image-v1"
	dttImageKept   = "dtt: image kept at "
)

// toolHistoryNameRe matches a dtt-calls file name -- a plain name, never a
// path.
var toolHistoryNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.txt$`)

// ToolHistoryEntry is one recorded dtt call. ID is its file name in
// dtt-calls, At unix ms. Mode is run, read, assert or delete (execute is
// run); Tool is run's tool, Document read/assert/delete's document ("stdin"
// for -). Partial is set when only a prefix of a long record was read, so
// Summary may be missing.
type ToolHistoryEntry struct {
	ID            string              `json:"id"`
	At            int64               `json:"at"`
	Session       string              `json:"session,omitempty"`
	SessionRecord string              `json:"session_record,omitempty"`
	Host          string              `json:"host,omitempty"`
	Agent         string              `json:"agent,omitempty"`
	Command       string              `json:"command"`
	Mode          string              `json:"mode,omitempty"`
	Tool          string              `json:"tool,omitempty"`
	Document      string              `json:"document,omitempty"`
	OK            bool                `json:"ok"`
	ExitCode      int                 `json:"exit_code"`
	DurationMs    int64               `json:"duration_ms"`
	Summary       *ToolHistorySummary `json:"summary,omitempty"`
	ImagePath     string              `json:"image_path,omitempty"`
	Partial       bool                `json:"partial,omitempty"`
}

// ToolHistorySummary is what a call's stdout said: Kind "result" (dtt run's
// RunResult; Mode and Image from its image_json output, if that's an image),
// "image" (read/assert/delete's image) or "answer" (any other dtt answer,
// e.g. an {"ok": false} for bad input). Image is the image's summary:
// outcome -> how many fields had it.
type ToolHistorySummary struct {
	Kind  string         `json:"kind"`
	OK    bool           `json:"ok"`
	Error string         `json:"error,omitempty"`
	Mode  string         `json:"mode,omitempty"`
	Image map[string]int `json:"image,omitempty"`
}

// ToolHistoryDetail is one recorded call in full: Stdout pretty-printed when
// it's JSON. Note says what was left out (a record past
// toolHistoryDetailRead, or cut to fit the relay to agent-coordinator).
type ToolHistoryDetail struct {
	ToolHistoryEntry
	RawCommand string `json:"raw_command"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Note       string `json:"note,omitempty"`
}

// dttCallEvent is the part of a record's event (clauditable's
// records.Event) the history uses.
type dttCallEvent struct {
	Timestamp     string `json:"timestamp"`
	Host          string `json:"host"`
	Agent         string `json:"agent"`
	DurationMs    int64  `json:"duration_ms"`
	ExitCode      int    `json:"exit_code"`
	Session       string `json:"session"`
	SessionRecord string `json:"session_record"`
}

// historyDir is <records root>/dtt-calls, or "" with no records root.
func (w *toolsWatch) historyDir() string {
	if w == nil || w.recordsRoot == nil {
		return ""
	}
	root := w.recordsRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, toolHistoryDirName)
}

// readToolHistory lists the calls recorded in dir, newest first, at most
// maxToolHistory (omitted counts the rest). A dir that doesn't exist yet is
// no calls, explained in detail. Records that can't be read or parsed (one
// still being written, say) are skipped.
func readToolHistory(dir string) (entries []ToolHistoryEntry, omitted int, detail string, err error) {
	entries = []ToolHistoryEntry{}
	if dir == "" {
		return entries, 0, "there's no records root (AGENT_RECORDS_PATH) here, so no recorded dtt calls", nil
	}
	des, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return entries, 0, fmt.Sprintf("no dtt calls recorded yet: %s doesn't exist -- calls are recorded there once dtt runs through clauditable", dir), nil
	}
	if err != nil {
		return nil, 0, "", fmt.Errorf("reading %s: %v", dir, err)
	}
	var names []string
	for _, de := range des {
		if de.Type().IsRegular() && toolHistoryNameRe.MatchString(de.Name()) {
			names = append(names, de.Name())
		}
	}
	sortToolHistoryNames(names)
	if len(names) > maxToolHistory {
		omitted = len(names) - maxToolHistory
		names = names[:maxToolHistory]
	}
	skipped := 0
	for _, name := range names {
		head, tail, size, err := readToolHistoryFile(filepath.Join(dir, name), toolHistoryListRead)
		if err != nil {
			skipped++
			continue
		}
		ev, command, stdout, stderr, err := parseDTTCallRecord(string(head))
		if err != nil {
			skipped++
			continue
		}
		e := newToolHistoryEntry(name, ev, command, stdout, stderr, string(tail))
		e.Partial = size > toolHistoryListRead
		entries = append(entries, e)
	}
	detail = fmt.Sprintf("%d recorded dtt call(s) in %s", len(entries), dir)
	if omitted > 0 {
		detail += fmt.Sprintf(", the newest %d of %d", maxToolHistory, len(names)+omitted)
	}
	if skipped > 0 {
		detail += fmt.Sprintf("; %d unreadable record(s) skipped", skipped)
	}
	return entries, omitted, detail, nil
}

// sortToolHistoryNames puts dtt-calls names newest first: by their unix
// time prefix, then by name.
func sortToolHistoryNames(names []string) {
	ts := func(name string) int64 {
		prefix, _, _ := strings.Cut(name, "-")
		n, _ := strconv.ParseInt(prefix, 10, 64)
		return n
	}
	sort.SliceStable(names, func(i, j int) bool {
		ti, tj := ts(names[i]), ts(names[j])
		if ti != tj {
			return ti > tj
		}
		return names[i] > names[j]
	})
}

// readToolHistoryFile reads up to limit bytes of record p, and of a longer
// one also its last toolHistoryTailRead bytes (where the image line is).
func readToolHistoryFile(p string, limit int64) (head, tail []byte, size int64, err error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, 0, err
	}
	head, err = io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, nil, 0, err
	}
	if size = st.Size(); size > limit {
		n := min(int64(toolHistoryTailRead), size-limit)
		tail = make([]byte, n)
		if _, err := f.ReadAt(tail, size-n); err != nil && err != io.EOF {
			tail = nil
		}
	}
	return head, tail, size, nil
}

// parseDTTCallRecord splits a record in clauditable's written-file format:
// the event JSON on the first line, the command between the WRITTEN_RAW and
// RESPONSE separators, then stdout, and stderr after a [STDERR] line. A
// record cut short (a prefix) gives what it holds -- the command from its
// IN>> preview lines when the raw part isn't reached.
func parseDTTCallRecord(content string) (ev dttCallEvent, command, stdout, stderr string, err error) {
	first, _, _ := strings.Cut(content, "\n")
	if err := json.Unmarshal([]byte(first), &ev); err != nil {
		return ev, "", "", "", fmt.Errorf("its first line isn't a record's event: %v", err)
	}
	i := strings.Index(content, recordWrittenSeparator)
	if i < 0 {
		var in []string
		for _, line := range strings.Split(content, "\n") {
			if l, ok := strings.CutPrefix(line, recordInputPrefix); ok {
				in = append(in, l)
			}
		}
		return ev, strings.Join(in, "\n"), "", "", nil
	}
	command, resp, found := strings.Cut(content[i+len(recordWrittenSeparator):], recordResponseSeparator)
	if !found {
		return ev, command, "", "", nil
	}
	if s, ok := strings.CutPrefix(resp, recordStderrMarker); ok {
		return ev, command, "", s, nil
	}
	if j := strings.Index(resp, "\n"+recordStderrMarker); j >= 0 {
		return ev, command, resp[:j+1], resp[j+1+len(recordStderrMarker):], nil
	}
	return ev, command, resp, "", nil
}

// newToolHistoryEntry derives a record's entry; tail is the end of a record
// read only in part, searched for the image line when stderr wasn't reached.
func newToolHistoryEntry(name string, ev dttCallEvent, command, stdout, stderr, tail string) ToolHistoryEntry {
	e := ToolHistoryEntry{
		ID:            name,
		Session:       ev.Session,
		SessionRecord: ev.SessionRecord,
		Host:          ev.Host,
		Agent:         ev.Agent,
		Command:       oneLineText(command, toolHistoryCommandMax),
		OK:            ev.ExitCode == 0,
		ExitCode:      ev.ExitCode,
		DurationMs:    ev.DurationMs,
		Summary:       dttOutputSummary(stdout),
		ImagePath:     dttImagePath(stderr),
	}
	if t, err := time.Parse(time.RFC3339, ev.Timestamp); err == nil {
		e.At = t.UnixMilli()
	} else if prefix, _, _ := strings.Cut(name, "-"); prefix != "" {
		if n, err := strconv.ParseInt(prefix, 10, 64); err == nil {
			e.At = n * 1000
		}
	}
	if e.ImagePath == "" && tail != "" {
		e.ImagePath = dttImagePath(tail)
	}
	e.Mode, e.Tool, e.Document = parseDTTCommand(command)
	return e
}

// parseDTTCommand reads a recorded "dtt <mode> <arg> [flags]" command (dtt
// as clauditable ran it, often a full path): run's (or execute's) tool, or
// read/assert/delete's document -- the first argument that isn't a flag.
func parseDTTCommand(command string) (mode, tool, document string) {
	f := strings.Fields(command)
	if len(f) < 2 {
		return "", "", ""
	}
	mode = f[1]
	if mode == "execute" {
		mode = "run"
	}
	for _, a := range f[2:] {
		switch {
		case mode == "run" && toolNameRe.MatchString(a):
			return mode, a, ""
		case (mode == "read" || mode == "assert" || mode == "delete") && a == "-":
			return mode, "", "stdin"
		case (mode == "read" || mode == "assert" || mode == "delete") && !strings.HasPrefix(a, "-"):
			return mode, "", a
		}
	}
	return mode, "", ""
}

// dttOutputSummary sums up a call's stdout, when it's one of dtt's JSON
// answers; nil otherwise (not JSON, or cut short).
func dttOutputSummary(stdout string) *ToolHistorySummary {
	s := strings.TrimSpace(stripDTTProgress(stdout))
	if !strings.HasPrefix(s, "{") {
		return nil
	}
	// Summary and Outputs stay raw: other answers may use the names
	// differently.
	var probe struct {
		OK      bool            `json:"ok"`
		Error   string          `json:"error"`
		Format  string          `json:"format"`
		Mode    string          `json:"mode"`
		Summary json.RawMessage `json:"summary"`
		Tool    string          `json:"tool"`
		Outputs json.RawMessage `json:"outputs"`
	}
	if json.Unmarshal([]byte(s), &probe) != nil {
		return nil
	}
	sum := &ToolHistorySummary{Kind: "answer", OK: probe.OK, Error: probe.Error}
	switch {
	case probe.Format == dttImageFormat:
		sum.Kind, sum.Mode = "image", probe.Mode
		_ = json.Unmarshal(probe.Summary, &sum.Image)
	case probe.Tool != "" || len(probe.Outputs) > 0:
		sum.Kind = "result"
		var outputs map[string]string
		_ = json.Unmarshal(probe.Outputs, &outputs)
		var img struct {
			Format  string         `json:"format"`
			Mode    string         `json:"mode"`
			Summary map[string]int `json:"summary"`
		}
		if raw := outputs[toolImageOutput]; raw != "" && json.Unmarshal([]byte(raw), &img) == nil && img.Format == dttImageFormat {
			sum.Mode, sum.Image = img.Mode, img.Summary
		}
	}
	return sum
}

// dttImagePath finds dtt's "dtt: image kept at <path>" line in stderr (the
// last, if more than one).
func dttImagePath(stderr string) string {
	found := ""
	for _, line := range strings.Split(stderr, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), dttImageKept); ok {
			found = strings.TrimSpace(p)
		}
	}
	return found
}

// readToolHistoryDetail reads recorded call name (a file in dir) in full,
// up to toolHistoryDetailRead.
func readToolHistoryDetail(dir, name string) (*ToolHistoryDetail, error) {
	if dir == "" {
		return nil, errors.New("there's no records root (AGENT_RECORDS_PATH) here, so no recorded dtt calls")
	}
	if !toolHistoryNameRe.MatchString(name) {
		return nil, fmt.Errorf("%q isn't a recorded call's name", name)
	}
	head, tail, size, err := readToolHistoryFile(filepath.Join(dir, name), toolHistoryDetailRead)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("call %s isn't in %s (any more)", name, dir)
	}
	if err != nil {
		return nil, err
	}
	ev, command, stdout, stderr, err := parseDTTCallRecord(string(head))
	if err != nil {
		return nil, fmt.Errorf("call %s: %v", name, err)
	}
	d := &ToolHistoryDetail{
		ToolHistoryEntry: newToolHistoryEntry(name, ev, command, stdout, stderr, string(tail)),
		RawCommand:       command,
		Stdout:           prettyJSONText(stdout),
		Stderr:           stderr,
	}
	if size > toolHistoryDetailRead {
		d.Partial = true
		d.Note = fmt.Sprintf("only the first %d MiB of this %d-byte record were read", toolHistoryDetailRead>>20, size)
	}
	return d, nil
}

// prettyJSONText indents s when it's JSON, and leaves it as it is if not.
func prettyJSONText(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") && !strings.HasPrefix(t, "[") {
		return s
	}
	var buf bytes.Buffer
	if json.Indent(&buf, []byte(t), "", "  ") != nil {
		return s
	}
	return buf.String()
}

// oneLineText is s on one line, cut to at most limit bytes (and "…").
func oneLineText(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= limit {
		return s
	}
	return strings.ToValidUTF8(s[:limit], "") + "…"
}

// fitHistoryReply leaves out the oldest entries of a history reply until
// it fits the relay to agent-coordinator.
func fitHistoryReply(reply *ToolsReply) {
	cut := 0
	for {
		b, err := json.Marshal(reply)
		if err != nil || len(b) <= maxRelayedTools || len(reply.History) == 0 {
			break
		}
		drop := max(1, len(reply.History)/8)
		reply.History = reply.History[:len(reply.History)-drop]
		reply.HistoryOmitted += drop
		cut += drop
	}
	if cut > 0 {
		reply.Detail += fmt.Sprintf("; the %d oldest not relayed through agent-coordinator -- see this host's own tools tab", cut)
	}
}

// fitHistoryDetailReply cuts a history-detail reply's stdout, then stderr,
// then raw command, until it fits the relay to agent-coordinator.
func fitHistoryDetailReply(reply *ToolsReply) {
	d := reply.Call
	if d == nil {
		return
	}
	var cut []string
	fields := []struct {
		name string
		s    *string
	}{{"stdout", &d.Stdout}, {"stderr", &d.Stderr}, {"the command", &d.RawCommand}}
	limit := maxRelayedTools - 512 // room for the note
	for _, f := range fields {
		for {
			b, err := json.Marshal(reply)
			if err != nil || len(b) <= limit {
				break
			}
			if *f.s == "" {
				break
			}
			if len(cut) == 0 || cut[len(cut)-1] != f.name {
				cut = append(cut, f.name)
			}
			// Each cut takes off at least the excess (a byte of text is a
			// byte or more of JSON), so this ends.
			keep := len(*f.s) - (len(b) - limit) - 256
			if keep <= 0 {
				*f.s = ""
			} else {
				*f.s = strings.ToValidUTF8((*f.s)[:keep], "")
			}
		}
	}
	if len(cut) > 0 {
		d.Partial = true
		note := strings.Join(cut, ", ") + " cut to fit the relay through agent-coordinator -- see this host's own tools tab for the whole"
		if d.Note != "" {
			note = d.Note + "; " + note
		}
		d.Note = note
	}
}

// ---- Recent calls ----

// The calls sub-tab is keyed by calls' directories (dtt/docs/terms.md, "In-
// place and pathless calls"): an in-place call's own, or the staging area
// of a pathless one. dtt lists the calls executed lately in its
// recent-calls file (~/.ufa/dtt/recent-calls.json), "dtt calls" answers
// that list and "dtt calls <dir|document>" describes one call, listed or
// not. Nothing is held here: each request asks dtt.

// ToolCall is one of dtt's recent calls (dtt/calls.go's RecentCall): its
// directory, and its latest execution -- At (UTC), Mode, OK and the Image
// it kept.
type ToolCall struct {
	Dir        string `json:"dir"`
	Kind       string `json:"kind"` // in-place or pathless
	Name       string `json:"name"`
	Document   string `json:"document"`
	From       string `json:"from,omitempty"` // pathless: the file it was called from
	At         string `json:"at"`
	Mode       string `json:"mode"`
	OK         bool   `json:"ok"`
	Image      string `json:"image,omitempty"`
	Executions int    `json:"executions"`
}

// ToolCallImage is one image kept for a call (dtt's CallImage).
type ToolCallImage struct {
	Name string `json:"name"`
	Path string `json:"path"`
	At   string `json:"at,omitempty"`
	Mode string `json:"mode,omitempty"`
}

// ToolCallDetail is one call described (dtt's CallInfo): its document
// (Source) and the images kept for it, newest first. Staged is whether Dir
// exists yet; Note says what was left out to fit the relay.
type ToolCallDetail struct {
	Dir           string          `json:"dir"`
	Kind          string          `json:"kind"`
	Name          string          `json:"name"`
	Document      string          `json:"document"`
	Staged        bool            `json:"staged"`
	Source        string          `json:"source"`
	Images        []ToolCallImage `json:"images"`
	ImagesOmitted int             `json:"images_omitted,omitempty"`
	Recent        *ToolCall       `json:"recent,omitempty"`
	Includes      []string        `json:"includes,omitempty"` // in-place: the dtt examples its .dtttc includes
	Note          string          `json:"note,omitempty"`
}

// ToolExampleCall is one of dtt's example tool call specifications (dtt's
// SpecExample, from "dtt examples --specifications"): an in-place call,
// unpacked at Dir on this host -- the directory the calls sub-tab opens it
// by.
type ToolExampleCall struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Commands    []string `json:"commands"`
	Path        string   `json:"path"` // in dtt's source
	Dir         string   `json:"dir"`
	Document    string   `json:"document"`
	Includes    []string `json:"includes"`
	Source      string   `json:"source"`
	Error       string   `json:"error,omitempty"`
}

// fitExampleCallsReply leaves out the example calls' sources (the call's
// detail has its document) and then the last of them, until the reply fits
// the relay to agent-coordinator.
func fitExampleCallsReply(reply *ToolsReply) {
	fits := func() bool {
		b, err := json.Marshal(reply)
		return err != nil || len(b) <= maxRelayedTools
	}
	for i := range reply.ExampleCalls {
		if fits() {
			return
		}
		reply.ExampleCalls[i].Source = ""
	}
	cut := 0
	for !fits() && len(reply.ExampleCalls) > 0 {
		reply.ExampleCalls = reply.ExampleCalls[:len(reply.ExampleCalls)-1]
		cut++
	}
	if cut > 0 {
		reply.Detail += fmt.Sprintf("; %d not relayed through agent-coordinator -- see this host's own tools tab", cut)
	}
}

// fitRecentCallsReply leaves out the oldest recent calls until the reply
// fits the relay to agent-coordinator.
func fitRecentCallsReply(reply *ToolsReply) {
	cut := 0
	for {
		b, err := json.Marshal(reply)
		if err != nil || len(b) <= maxRelayedTools || len(reply.RecentCalls) == 0 {
			break
		}
		reply.RecentCalls = reply.RecentCalls[:len(reply.RecentCalls)-1]
		cut++
	}
	if cut > 0 {
		reply.Detail += fmt.Sprintf("; the %d oldest not relayed through agent-coordinator -- see this host's own tools tab", cut)
	}
}

// fitCallDetailReply leaves out a call-detail reply's older images, then
// cuts its document, until it fits the relay to agent-coordinator.
func fitCallDetailReply(reply *ToolsReply) {
	d := reply.CallDetail
	if d == nil {
		return
	}
	limit := maxRelayedTools - 512 // room for the note
	over := func() int {
		b, err := json.Marshal(reply)
		if err != nil {
			return 0
		}
		return len(b) - limit
	}
	var cut []string
	for over() > 0 && len(d.Images) > 0 {
		drop := max(1, len(d.Images)/4)
		d.Images = d.Images[:len(d.Images)-drop]
		d.ImagesOmitted += drop
		if len(cut) == 0 {
			cut = append(cut, "older images")
		}
	}
	for n := over(); n > 0 && d.Source != ""; n = over() {
		if len(cut) == 0 || cut[len(cut)-1] != "the document" {
			cut = append(cut, "the document")
		}
		// As fitHistoryDetailReply: each cut takes off at least the excess.
		if keep := len(d.Source) - n - 256; keep <= 0 {
			d.Source = ""
		} else {
			d.Source = strings.ToValidUTF8(d.Source[:keep], "")
		}
	}
	if len(cut) > 0 {
		d.Note = strings.Join(cut, ", ") + " left out to fit the relay through agent-coordinator -- see this host's own tools tab for the whole"
	}
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
