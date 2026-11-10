package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// treeListJSON is "dtt list --json" with the reference tool in it.
const treeListJSON = `{
  "ok": true,
  "version": "test",
  "tools": [{
    "op": "tool.materialize-tree",
    "summary": "Build a directory tree.",
    "describe": "materialize a tree under {root}",
    "args": [
      {"name": "spec", "help": "the tree", "required": true, "type": "text"},
      {"name": "root", "help": "where", "type": "path"},
      {"name": "overwrite", "help": "replace", "default": "false", "type": "bool"}
    ],
    "name": "materialize-tree",
    "description": "Build a directory tree.",
    "values": [{"name": "root"}, {"name": "file_count"}],
    "files": [{"name": "archive"}],
    "runtime": "uv",
    "script": "main.py",
    "timeout": "2s",
    "origin": "builtin",
    "path": "builtin/materialize-tree"
  }],
  "user_dir": "/home/x/.ufa/tools",
  "dirs": [{"origin": "builtin", "path": "builtin", "status": "ok"}],
  "problems": [],
  "shadowed": []
}`

// fakeDTT answers dtt calls: list with treeListJSON, run with run (given
// the inputs it was fed), and anything else with other.
type fakeDTT struct {
	run   func(args []string, inputs map[string]string) string
	other func(args []string, stdin []byte) string
	calls [][]string
}

func (f *fakeDTT) call(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	switch args[0] {
	case "list":
		return []byte(treeListJSON), nil
	case "run":
		var in map[string]string
		if err := json.Unmarshal(stdin, &in); err != nil {
			return nil, err
		}
		if f.run == nil {
			return nil, errors.New("no run")
		}
		return []byte(f.run(args, in)), nil
	}
	if f.other == nil {
		return nil, errors.New("unexpected call")
	}
	return []byte(f.other(args, stdin)), nil
}

// toolsServer is a test LR whose tools watch talks to fake, listed once.
func toolsServer(t *testing.T, fake *fakeDTT) *Server {
	t.Helper()
	s := newServer("test-lr")
	s.fileCacheDir = t.TempDir()
	s.hostStoreDir = t.TempDir()
	s.tools.resolve = func() (string, error) { return "/fake/dtt", nil }
	s.tools.dtt = fake.call
	s.tools.runDir = t.TempDir()
	t.Cleanup(func() { setToolControlOps(nil, false) })
	s.tools.check()
	return s
}

func waitRun(t *testing.T, w *toolsWatch, id string) ToolRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range w.tabState().Runs {
			if r.ID == id && r.Status != "running" {
				return r
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s didn't finish", id)
	return ToolRun{}
}

func TestToolsCatalogAndControlOps(t *testing.T) {
	s := toolsServer(t, &fakeDTT{})
	st := s.tools.snapshot()
	if st.Status != connOK || len(st.Tools) != 1 || st.Version != "test" || st.UserDir != "/home/x/.ufa/tools" {
		t.Fatalf("catalog %+v", st)
	}
	if !st.Tools[0].Allowed {
		t.Errorf("with tools-allow unset every tool is allowed: %+v", st.Tools[0])
	}
	if sys := s.systemState(); len(sys.Tools.Tools) != 1 {
		t.Errorf("system-state's tools: %+v", sys.Tools)
	}

	// The tool is a control op: its inputs, then save_prefix and hint.
	spec, ok := findControlOp("tool.materialize-tree", nil)
	if !ok || spec.run == nil {
		t.Fatalf("tool.materialize-tree isn't a control op: %+v", spec)
	}
	var names []string
	for _, a := range spec.Args {
		names = append(names, a.Name)
	}
	if got := strings.Join(names, ","); got != "spec,root,overwrite,save_prefix,hint" {
		t.Errorf("args %s", got)
	}
	if a := spec.arg("spec"); a == nil || !a.Required {
		t.Errorf("spec should be required: %+v", a)
	}
	if a := spec.arg("overwrite"); a == nil || a.Default != "false" {
		t.Errorf("overwrite's default: %+v", a)
	}
	if !strings.Contains(spec.Summary, "{{root}}") || !strings.Contains(spec.Summary, "files tab") {
		t.Errorf("summary %q", spec.Summary)
	}
	if got := describeControlInstruction(map[string]string{"op": "tool.materialize-tree", "root": "/tmp/t"}, nil); got != "tool: materialize a tree under /tmp/t" {
		t.Errorf("described as %q", got)
	}
	lib := s.control.library()
	found := false
	for _, o := range lib.Ops {
		found = found || o.Op == "tool.materialize-tree"
	}
	if !found {
		t.Error("the control library's ops don't offer the tool")
	}

	// Validation: arguments are checked against the tool; a tool dtt
	// doesn't list is only checked for its name.
	if err := validateControlInstruction(ControlInstruction{"op": "tool.materialize-tree", "spec": "a: b"}, nil); err != nil {
		t.Errorf("a valid instruction: %v", err)
	}
	if err := validateControlInstruction(ControlInstruction{"op": "tool.materialize-tree"}, nil); err == nil || !strings.Contains(err.Error(), `needs "spec"`) {
		t.Errorf("missing spec: %v", err)
	}
	if err := validateControlInstruction(ControlInstruction{"op": "tool.materialize-tree", "spec": "x", "colour": "red"}, nil); err == nil {
		t.Error("an unknown argument passed")
	}
	if err := validateControlInstruction(ControlInstruction{"op": "tool.not-installed-yet"}, nil); err != nil {
		t.Errorf("an unlisted tool should pass with a valid name: %v", err)
	}
	if err := validateControlInstruction(ControlInstruction{"op": "tool.Bad Name"}, nil); err == nil {
		t.Error("a bad tool name passed")
	}
}

func TestToolsListFailure(t *testing.T) {
	s := newServer("test-lr")
	s.tools.resolve = func() (string, error) { return "", errors.New(`could not locate "dtt"`) }
	t.Cleanup(func() { setToolControlOps(nil, false) })
	s.tools.check()
	st := s.tools.snapshot()
	if st.Status != connFailed || !strings.Contains(st.Detail, "could not locate") {
		t.Fatalf("state %+v", st)
	}
	if _, err := s.tools.startRun("materialize-tree", nil, "tools tab", false); err == nil {
		t.Error("a run started with no catalog")
	}
}

func TestToolsAllow(t *testing.T) {
	s := toolsServer(t, &fakeDTT{})
	w := s.tools
	if err := w.setAllow([]string{"tool.lint-*", "format"}, "a config file"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"lint-go": true, "format": true, "materialize-tree": false} {
		if got := toolAllowed(name, w.getAllow()); got != want {
			t.Errorf("allowed(%s) = %v, want %v", name, got, want)
		}
	}
	err := w.checkAllowed("materialize-tree")
	if err == nil || !strings.Contains(err.Error(), "tools-allow") || !strings.Contains(err.Error(), "a config file") {
		t.Errorf("refusal: %v", err)
	}
	if st := w.snapshot(); st.Tools[0].Allowed || len(st.Allow) != 2 {
		t.Errorf("snapshot %+v", st)
	}
	// agent-coordinator's runs are refused; the tab's own aren't.
	if r := w.handle(ToolsRequest{Op: "run", Tool: "materialize-tree"}, true, "test-lr"); r.OK || !strings.Contains(r.Error, "tools-allow") {
		t.Errorf("a run from agent-coordinator: %+v", r)
	}
	if err := w.setAllow([]string{"*"}, ""); err != nil || len(w.getAllow()) != 0 {
		t.Errorf("* should allow every tool: %v %v", err, w.getAllow())
	}
	if err := w.setAllow([]string{"[bad"}, ""); err == nil {
		t.Error("a bad pattern was accepted")
	}
}

// TestToolsTabRunAndSave: a run from the tab finishes in the background,
// and saves into the files tab as a .zip with its output file.
func TestToolsTabRunAndSave(t *testing.T) {
	var gotInputs map[string]string
	fake := &fakeDTT{run: func(args []string, in map[string]string) string {
		gotInputs = in
		wd := args[len(args)-2] // ... --workdir <wd> --json
		os.MkdirAll(wd, 0o755)
		zipPath := filepath.Join(wd, "tree.zip")
		os.WriteFile(zipPath, []byte("zip bytes"), 0o644)
		b, _ := json.Marshal(ToolRunResult{OK: true, Tool: "materialize-tree", Outputs: map[string]string{"root": "/r", "file_count": "2"},
			Files: []ToolRunFile{{Name: "archive", Path: zipPath, Size: 9}}, LogTail: "built it", DurationMs: 12, Workdir: wd})
		return string(b)
	}}
	s := toolsServer(t, fake)
	r := s.tools.handle(ToolsRequest{Req: "q1", Op: "run", Tool: "tool.materialize-tree", Args: map[string]string{"spec": "a: b"}}, false, "test-lr")
	if !r.OK || r.RunID == "" || r.Req != "q1" {
		t.Fatalf("reply %+v", r)
	}
	run := waitRun(t, s.tools, r.RunID)
	if run.Status != "ok" || run.Origin != "tools tab" || !run.FilesKept || run.Result.Outputs["file_count"] != "2" {
		t.Fatalf("run %+v / %+v", run, run.Result)
	}
	if gotInputs["spec"] != "a: b" {
		t.Errorf("inputs %+v", gotInputs)
	}

	r = s.tools.handle(ToolsRequest{Op: "save", Run: run.ID}, false, "test-lr")
	if !r.OK || r.File == nil {
		t.Fatalf("save: %+v", r)
	}
	data, err := os.ReadFile(filepath.Join(s.fileCacheDir, r.File.FileID))
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range z.File {
		names = append(names, f.Name)
		if f.Name == "report.txt" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			if !strings.Contains(string(b), "file_count: 2") || !strings.Contains(string(b), "spec: a: b") {
				t.Errorf("report.txt:\n%s", b)
			}
		}
	}
	if got := strings.Join(names, " "); got != "report.txt result.json log.txt files/tree.zip" {
		t.Errorf("zip holds %s", got)
	}
	if runs := s.tools.tabState().Runs; len(runs[0].Saved) != 1 {
		t.Errorf("the run doesn't list what it was saved as: %+v", runs[0])
	}
}

// TestToolsRunEventsToFile: a run holds LR's and dtt's events in order,
// and "to file" sends them (with the run's details) to the files tab as a
// .txt -- while it's still running, too.
func TestToolsRunEventsToFile(t *testing.T) {
	release := make(chan struct{})
	fake := &fakeDTT{run: func(args []string, in map[string]string) string {
		<-release
		b, _ := json.Marshal(ToolRunResult{OK: true, Tool: "materialize-tree", Outputs: map[string]string{"root": "/r"}, LogTail: "built it", DurationMs: 5,
			Events: []ToolRunEvent{
				{At: time.Now().UnixMilli(), Event: "tool started", Detail: "main.py"},
				{At: time.Now().UnixMilli(), Event: "outputs read", Detail: "1 value(s) (root), 0 file(s)"},
			}})
		return string(b)
	}}
	s := toolsServer(t, fake)
	r := s.tools.handle(ToolsRequest{Op: "run", Tool: "materialize-tree", Args: map[string]string{"spec": "a: b"}}, false, "test-lr")
	if !r.OK {
		t.Fatalf("run: %+v", r)
	}
	readFile := func(id string) string {
		b, err := os.ReadFile(filepath.Join(s.fileCacheDir, id))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// While it runs: the details so far.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if run := s.tools.runCopy(r.RunID); run != nil && len(run.Events) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never noted calling dtt")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f := s.tools.handle(ToolsRequest{Op: "to-file", Run: r.RunID}, false, "test-lr")
	if !f.OK || f.File == nil || !strings.HasSuffix(f.File.Name, "-details.txt") {
		t.Fatalf("to-file while running: %+v", f)
	}
	if txt := readFile(f.File.FileID); !strings.Contains(txt, "still running") || !strings.Contains(txt, "dtt called") || !strings.Contains(txt, "spec: a: b") {
		t.Errorf("details while running:\n%s", txt)
	}

	close(release)
	run := waitRun(t, s.tools, r.RunID)
	var got []string
	for _, ev := range run.Events {
		got = append(got, ev.From+":"+ev.Event)
	}
	want := "lr:run started, lr:dtt called, lr:details sent to file, dtt:tool started, dtt:outputs read, lr:run ok"
	if strings.Join(got, ", ") != want {
		t.Errorf("events %s\nwant   %s", strings.Join(got, ", "), want)
	}
	if run.EventCount != len(run.Events) || run.Result.Events != nil {
		t.Errorf("event_count %d, result's events %+v", run.EventCount, run.Result.Events)
	}

	f = s.tools.handle(ToolsRequest{Op: "to-file", Run: r.RunID}, false, "test-lr")
	if !f.OK {
		t.Fatalf("to-file: %+v", f)
	}
	txt := readFile(f.File.FileID)
	for _, want := range []string{"result: succeeded", "events:", "dtt  tool started: main.py", "lr   run ok", "root: /r", "built it"} {
		if !strings.Contains(txt, want) {
			t.Errorf("details lack %q:\n%s", want, txt)
		}
	}
	if run := s.tools.runCopy(r.RunID); len(run.Saved) != 2 {
		t.Errorf("both files should be listed on the run: %+v", run.Saved)
	}
	if f := s.tools.handle(ToolsRequest{Op: "to-file", Run: "gone"}, false, "test-lr"); f.OK || !strings.Contains(f.Error, "no longer kept") {
		t.Errorf("to-file of an unknown run: %+v", f)
	}

	// Through agent-coordinator only the newest run's events come along.
	other := s.tools.newRun("materialize-tree", nil, "tools tab", true)
	s.tools.finishRun(other.ID, ToolRunResult{OK: true}, nil)
	st := s.tools.tabStateForAC()
	if len(st.Runs[0].Events) == 0 || len(st.Runs[1].Events) != 0 || st.Runs[1].EventCount != len(run.Events)+1 {
		t.Errorf("relayed events: newest %d, older %d (count %d)", len(st.Runs[0].Events), len(st.Runs[1].Events), st.Runs[1].EventCount)
	}
}

// TestToolsRunEventsCapped: past maxToolRunEvents the newest event replaces
// the last kept one.
func TestToolsRunEventsCapped(t *testing.T) {
	s := toolsServer(t, &fakeDTT{})
	r := s.tools.newRun("materialize-tree", nil, "tools tab", true)
	for i := 0; i < maxToolRunEvents+5; i++ {
		s.tools.event(r.ID, "tick", "")
	}
	s.tools.finishRun(r.ID, ToolRunResult{OK: true}, nil)
	run := s.tools.runCopy(r.ID)
	if len(run.Events) != maxToolRunEvents || run.EventsDropped != 7 || run.Events[len(run.Events)-1].Event != "run ok" {
		t.Errorf("%d events, %d dropped, last %+v", len(run.Events), run.EventsDropped, run.Events[len(run.Events)-1])
	}
}

// TestToolControlOp: a control run's tool.<name> saves its outputs under
// save_prefix, copies its files into the files tab, and fails with the
// tool's error.
func TestToolControlOp(t *testing.T) {
	fail := false
	var wd string
	fake := &fakeDTT{run: func(args []string, in map[string]string) string {
		wd = args[len(args)-2]
		if fail {
			b, _ := json.Marshal(ToolRunResult{Tool: "materialize-tree", Error: "materialize-tree exited with status 1: boom", ExitCode: 1})
			return string(b)
		}
		os.MkdirAll(wd, 0o755)
		p := filepath.Join(wd, "tree.zip")
		os.WriteFile(p, []byte("zip"), 0o644)
		b, _ := json.Marshal(ToolRunResult{OK: true, Outputs: map[string]string{"root": "/r", "file_count": "2"}, Files: []ToolRunFile{{Name: "archive", Path: p, Size: 3}}})
		return string(b)
	}}
	s := toolsServer(t, fake)
	r := runningEngine(s)
	spec, _ := findControlOp("tool.materialize-tree", nil)

	note, err := spec.run(r, opArgs{"spec": "a: b", "overwrite": "false", "save_prefix": "tree_", "hint": "h"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r.vars["tree_root"] != "/r" || r.vars["tree_file_count"] != "2" {
		t.Errorf("vars %+v", r.vars)
	}
	if !strings.Contains(note, "copied 1 file(s)") {
		t.Errorf("note %q", note)
	}
	run := s.control.runCopy()
	if len(run.Recordings) != 1 || !run.Recordings[0].File || !strings.HasSuffix(run.Recordings[0].Name, "tree.zip") {
		t.Errorf("recordings %+v", run.Recordings)
	}
	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Errorf("the control run's working directory should be removed: %v", err)
	}
	if runs := s.tools.tabState().Runs; len(runs) != 1 || runs[0].Origin != "control run" || runs[0].FilesKept || len(runs[0].Saved) != 1 {
		t.Errorf("history %+v", runs)
	}
	for _, c := range fake.calls {
		if c[0] == "run" && strings.Contains(strings.Join(c, " "), "save_prefix") {
			t.Errorf("save_prefix reached dtt: %v", c)
		}
	}

	fail = true
	if _, err := spec.run(r, opArgs{"spec": "a: b"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a failing tool: %v", err)
	}

	// tools-allow applies to control runs.
	if err := s.tools.setAllow([]string{"other"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.run(r, opArgs{"spec": "a: b"}); err == nil || !strings.Contains(err.Error(), "tools-allow") {
		t.Errorf("a refused tool: %v", err)
	}
}

func TestToolsAuthorOps(t *testing.T) {
	var lastStdin []byte
	fake := &fakeDTT{other: func(args []string, stdin []byte) string {
		lastStdin = stdin
		switch args[0] {
		case "validate":
			if args[1] == "-" {
				return `{"ok": true, "tool": {"name": "piped", "op": "tool.piped"}}`
			}
			return `{"ok": false, "error": "` + args[1] + `: no tool.yaml"}`
		case "install":
			return `{"ok": true, "tool": {"name": "mine", "path": "/home/x/.ufa/tools/mine"}}`
		case "create":
			return `{"ok": true, "path": "/home/x/.ufa/tools/` + args[1] + `", "tool": {"name": "` + args[1] + `"}}`
		case "describe":
			return `{"ok": true, "tool": {"name": "materialize-tree", "source": "format: dtt-v1\n"}}`
		case "examples":
			return `{"ok": true, "examples": [{"name": "filesystem-json", "title": "files", "tool": "filesystem", "source": "filesystem.items:\n  select:\n    abspath: /tmp/\n"}]}`
		}
		return `{"ok": false, "error": "?"}`
	}}
	s := toolsServer(t, fake)
	w := s.tools

	if r := w.handle(ToolsRequest{Op: "validate", Source: "format: dtt-v1\n"}, false, ""); !r.OK || r.Tool.Name != "piped" || string(lastStdin) != "format: dtt-v1\n" {
		t.Errorf("validate source: %+v", r)
	}
	if r := w.handle(ToolsRequest{Op: "validate", Path: "/srv/x/tool.yaml"}, false, ""); r.OK || r.Error != "/srv/x: no tool.yaml" {
		t.Errorf("validate path: %+v", r)
	}
	if r := w.handle(ToolsRequest{Op: "install", Path: "relative/dir"}, false, ""); r.OK || !strings.Contains(r.Error, "absolute") {
		t.Errorf("install of a relative path: %+v", r)
	}
	if r := w.handle(ToolsRequest{Op: "install", Path: "/srv/mine", Force: true}, false, ""); !r.OK || r.Detail != "installed mine at /home/x/.ufa/tools/mine" {
		t.Errorf("install: %+v", r)
	}
	if last := fake.calls[len(fake.calls)-1]; strings.Join(last, " ") != "install /srv/mine --force --json" {
		t.Errorf("install called dtt %v", last)
	}
	if r := w.handle(ToolsRequest{Op: "create", Name: "Bad"}, false, ""); r.OK {
		t.Errorf("create with a bad name: %+v", r)
	}
	if r := w.handle(ToolsRequest{Op: "create", Name: "fresh"}, false, ""); !r.OK || !strings.Contains(r.Detail, "/home/x/.ufa/tools/fresh") {
		t.Errorf("create: %+v", r)
	}
	if r := w.handle(ToolsRequest{Op: "describe", Tool: "tool.materialize-tree"}, false, ""); !r.OK || r.Tool.Source == "" || !r.Tool.Allowed {
		t.Errorf("describe: %+v", r)
	}
	if r := w.handle(ToolsRequest{Op: "examples"}, true, ""); !r.OK || len(r.Examples) != 1 || r.Examples[0].Tool != "filesystem" || !strings.Contains(r.Examples[0].Source, "abspath") {
		t.Errorf("examples: %+v", r)
	}
	if last := fake.calls[len(fake.calls)-1]; strings.Join(last, " ") != "examples --json" {
		t.Errorf("examples called dtt %v", last)
	}
	if r := w.handle(ToolsRequest{Op: "explode"}, false, ""); r.OK {
		t.Errorf("an unknown op: %+v", r)
	}
}

// TestToolsRecentCalls: recent-calls is dtt calls' list, and call-detail
// dtt calls <path>'s description -- cut to fit the relay to
// agent-coordinator.
func TestToolsRecentCalls(t *testing.T) {
	big := strings.Repeat("statement: x\n", 6<<10)
	fake := &fakeDTT{other: func(args []string, stdin []byte) string {
		if args[0] != "calls" {
			return `{"ok": false, "error": "?"}`
		}
		if len(args) == 2 { // calls --json
			return `{"ok": true, "file": "/home/x/.ufa/dtt/recent-calls.json", "calls": [
				{"dir": "/w/my-call", "kind": "in-place", "name": "my-call", "document": "/w/my-call/it.dtt.yaml", "at": "2026-11-08T10:00:00Z", "mode": "read", "ok": true, "executions": 2},
				{"dir": "/home/x/.ufa/dtt/pathless/document/ab", "kind": "pathless", "name": "document", "document": "/home/x/.ufa/dtt/pathless/document/ab/document.dtt.yaml", "at": "2026-11-08T09:00:00Z", "mode": "assert", "ok": false, "executions": 1}]}`
		}
		if args[1] == "/w/big" {
			images := make([]string, 400)
			for i := range images {
				images[i] = fmt.Sprintf(`{"name": "20261108T1000%02dZ-read.image.json", "path": "/w/big/images/20261108T1000%02dZ-read.image.json", "mode": "read"}`, i%60, i%60)
			}
			src, _ := json.Marshal(big)
			return `{"ok": true, "call": {"dir": "/w/big", "kind": "in-place", "name": "big", "document": "/w/big/big.dtt.yaml", "staged": true, "source": ` + string(src) + `, "images": [` + strings.Join(images, ",") + `]}}`
		}
		return `{"ok": true, "call": {"dir": "` + args[1] + `", "kind": "in-place", "name": "my-call", "document": "/w/my-call/it.dtt.yaml", "staged": true, "source": "timestamp:\n", "images": null}}`
	}}
	s := toolsServer(t, fake)
	w := s.tools

	r := w.handle(ToolsRequest{Op: "recent-calls"}, false, "n")
	if !r.OK || len(r.RecentCalls) != 2 || r.RecentCalls[0].Dir != "/w/my-call" || r.RecentCalls[1].Kind != "pathless" || r.RecentCallsFile == "" {
		t.Fatalf("recent-calls: %+v", r)
	}
	if last := fake.calls[len(fake.calls)-1]; strings.Join(last, " ") != "calls --json" {
		t.Errorf("recent-calls called dtt %v", last)
	}
	d := w.handle(ToolsRequest{Op: "call-detail", Path: "/w/my-call/"}, false, "n")
	if !d.OK || d.CallDetail == nil || d.CallDetail.Name != "my-call" || d.CallDetail.Images == nil {
		t.Fatalf("call-detail: %+v", d)
	}
	if last := fake.calls[len(fake.calls)-1]; strings.Join(last, " ") != "calls /w/my-call --json" {
		t.Errorf("call-detail called dtt %v", last)
	}
	for _, bad := range []string{"", "relative/call"} {
		if r := w.handle(ToolsRequest{Op: "call-detail", Path: bad}, false, "n"); r.OK || !strings.Contains(r.Error, "absolute") {
			t.Errorf("call-detail %q: %+v", bad, r)
		}
	}

	// Through agent-coordinator, a call too big for the relay loses its
	// older images, then the end of its document.
	d = w.handle(ToolsRequest{Op: "call-detail", Path: "/w/big"}, true, "n")
	b, _ := json.Marshal(d)
	if !d.OK || len(b) > maxRelayedTools || d.CallDetail.ImagesOmitted == 0 || len(d.CallDetail.Source) >= len(big) || !strings.Contains(d.CallDetail.Note, "agent-coordinator") {
		t.Errorf("relayed call-detail: ok %v, %d bytes, %d images (%d omitted), note %q", d.OK, len(b), len(d.CallDetail.Images), d.CallDetail.ImagesOmitted, d.CallDetail.Note)
	}
}

// TestToolsExampleCalls: example-calls is dtt examples --specifications'
// list, each call's directory where dtt unpacked it -- its sources left out,
// then its last calls, to fit the relay to agent-coordinator.
func TestToolsExampleCalls(t *testing.T) {
	big, _ := json.Marshal(strings.Repeat("statement: x\n", 6<<10))
	fake := &fakeDTT{other: func(args []string, stdin []byte) string {
		if args[0] != "examples" || len(args) != 3 || args[1] != "--specifications" {
			return `{"ok": false, "error": "?"}`
		}
		return `{"ok": true, "dir": "/home/x/.ufa/dtt/examples/specifications", "specifications": [
			{"name": "hello-files", "title": "files", "description": "d", "commands": [], "path": "examples/specifications/hello-files",
			 "dir": "/home/x/.ufa/dtt/examples/specifications/hello-files", "document": "hello-files.dtt.yaml", "includes": [], "source": ` + string(big) + `},
			{"name": "json-with-note", "title": "json", "description": "d", "commands": [], "path": "examples/specifications/json-with-note",
			 "dir": "/home/x/.ufa/dtt/examples/specifications/json-with-note", "document": "json-with-note.dtt.yaml",
			 "includes": ["compositions/filesystem-json.dtt.yaml"], "source": "timestamp:\n"}]}`
	}}
	s := toolsServer(t, fake)
	w := s.tools

	r := w.handle(ToolsRequest{Op: "example-calls"}, false, "n")
	if !r.OK || len(r.ExampleCalls) != 2 || r.ExampleCalls[1].Dir != "/home/x/.ufa/dtt/examples/specifications/json-with-note" ||
		len(r.ExampleCalls[1].Includes) != 1 || len(r.ExampleCalls[0].Source) < 6<<10 {
		t.Fatalf("example-calls: %+v", r)
	}
	if last := fake.calls[len(fake.calls)-1]; strings.Join(last, " ") != "examples --specifications --json" {
		t.Errorf("example-calls called dtt %v", last)
	}
	r = w.handle(ToolsRequest{Op: "example-calls"}, true, "n")
	b, _ := json.Marshal(r)
	if !r.OK || len(b) > maxRelayedTools || len(r.ExampleCalls) != 2 || r.ExampleCalls[0].Source != "" || r.ExampleCalls[0].Dir == "" {
		t.Errorf("relayed example-calls: ok %v, %d bytes, %+v", r.OK, len(b), r.ExampleCalls)
	}
}

func TestToolsStateForACFits(t *testing.T) {
	s := toolsServer(t, &fakeDTT{})
	w := s.tools
	big := strings.Repeat("x", 6<<10)
	for i := 0; i < maxToolRuns; i++ {
		r := w.newRun("materialize-tree", map[string]string{"spec": big}, "tools tab", true)
		w.finishRun(r.ID, ToolRunResult{OK: true, LogTail: big}, nil)
	}
	st := w.tabStateForAC()
	b, _ := json.Marshal(st)
	if len(b) > maxRelayedTools {
		t.Fatalf("relayed tools-state is %d bytes", len(b))
	}
	if st.RunsOmitted == 0 || len(st.Runs)+st.RunsOmitted != maxToolRuns {
		t.Errorf("runs %d, omitted %d", len(st.Runs), st.RunsOmitted)
	}
	for _, r := range st.Runs {
		if len(r.Result.LogTail) > maxToolLogRelayed+len("…") {
			t.Errorf("a relayed log is %d bytes", len(r.Result.LogTail))
		}
	}
	if full := w.tabState(); len(full.Runs[0].Result.LogTail) != len(big) {
		t.Error("trimming for agent-coordinator changed the tab's own copy")
	}
}

// TestToolsStateForACImages: only the newest run's image is relayed, and
// a too-large one is dropped before any run is.
func TestToolsStateForACImages(t *testing.T) {
	s := toolsServer(t, &fakeDTT{})
	w := s.tools
	run := func(image string) {
		r := w.newRun("filesystem", nil, "tools tab", true)
		w.finishRun(r.ID, ToolRunResult{OK: true, Outputs: map[string]string{"changed": "1", toolImageOutput: image}}, nil)
	}
	run(`{"old": true}`)
	run(`{"new": true}`)
	st := w.tabStateForAC()
	if len(st.Runs) != 2 || st.Runs[0].Result.Outputs[toolImageOutput] != `{"new": true}` {
		t.Fatalf("newest run %+v", st.Runs[0].Result)
	}
	if _, ok := st.Runs[1].Result.Outputs[toolImageOutput]; ok || st.Runs[1].Result.Outputs["changed"] != "1" {
		t.Errorf("older run's outputs %+v", st.Runs[1].Result.Outputs)
	}

	run(strings.Repeat("x", maxRelayedTools))
	st = w.tabStateForAC()
	if len(st.Runs) != 3 || st.RunsOmitted != 0 {
		t.Fatalf("runs %d, omitted %d", len(st.Runs), st.RunsOmitted)
	}
	if _, ok := st.Runs[0].Result.Outputs[toolImageOutput]; ok {
		t.Error("a too-large image was relayed")
	}
	if full := w.tabState(); full.Runs[0].Result.Outputs[toolImageOutput] == "" || full.Runs[1].Result.Outputs[toolImageOutput] == "" {
		t.Error("trimming for agent-coordinator changed the tab's own copy")
	}
}

func TestToolsHistoryIsCapped(t *testing.T) {
	s := toolsServer(t, &fakeDTT{})
	w := s.tools
	var first *ToolRun
	for i := 0; i < maxToolRuns+5; i++ {
		r := w.newRun("materialize-tree", nil, "tools tab", true)
		if first == nil {
			first = r
			os.MkdirAll(r.workdir, 0o755)
		}
		w.finishRun(r.ID, ToolRunResult{OK: true}, nil)
	}
	if n := len(w.tabState().Runs); n != maxToolRuns {
		t.Errorf("%d runs kept, want %d", n, maxToolRuns)
	}
	if _, err := os.Stat(first.workdir); !os.IsNotExist(err) {
		t.Errorf("a dropped run's working directory should be removed: %v", err)
	}
}

// dttCallFixture is a dtt-calls record as clauditable writes it
// (records.Record.FormatWrittenFile): the event line, previews, then the raw
// command and response.
func dttCallFixture(event, command, stdout, stderr string) string {
	var b strings.Builder
	b.WriteString(event + "\n")
	b.WriteString("IN>>" + command + "\n")
	if stdout != "" {
		b.WriteString("OUT>>" + strings.SplitN(stdout, "\n", 2)[0] + "\n")
	}
	b.WriteString("\n----------WRITTEN_RAW----------\n")
	b.WriteString(command)
	b.WriteString("\n\n----------RESPONSE----------\n\n")
	b.WriteString(stdout)
	if stderr != "" {
		if stdout != "" && !strings.HasSuffix(stdout, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("[STDERR]\n" + stderr)
	}
	return b.String()
}

// writeDTTCalls writes two recorded calls (and two files the history should
// skip) into root/dtt-calls: an older dtt run whose image_json output is an
// image, and a newer, failed dtt read whose stdout is an image.
func writeDTTCalls(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "dtt-calls")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	image, _ := json.Marshal(map[string]any{"ok": true, "format": "dtt-image-v1", "mode": "assert", "summary": map[string]int{"unchanged": 2, "changed": 1}})
	result, _ := json.Marshal(map[string]any{"ok": true, "tool": "materialize-tree", "outputs": map[string]string{"image_json": string(image)}, "exit_code": 0, "duration_ms": 900})
	files := map[string]string{
		"1793101234-k7q.txt": dttCallFixture(
			`{"timestamp":"2026-10-27T10:20:34Z","event_type":"dtt_call","host":"h1","agent":"a1","duration_ms":1200,"exit_code":0,"session":"sess-1","session_record":"1793101234-raw.txt"}`,
			"/usr/local/bin/dtt run materialize-tree --input - --workdir /tmp/w --json", string(result)+"\n", ""),
		"1793101300-a1b.txt": dttCallFixture(
			`{"timestamp":"2026-10-27T10:21:40Z","event_type":"dtt_call","host":"h2","duration_ms":300,"exit_code":1,"session":"sess-2","session_record":"1793101300-raw.txt"}`,
			"dtt read /x/doc.dtt.yaml --json",
			`{"ok":false,"format":"dtt-image-v1","mode":"read","summary":{"measured":3},"error":"one failed"}`,
			"dtt: image kept at /home/x/.ufa/dtt/pathless/doc_dtt_yaml/abc/images/20261027T102140Z-read.image.json\n"),
		"1793101200-zzz.txt": "not a record\n",
		"notes.md":           "not a call\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestToolsDTTCallHistory(t *testing.T) {
	root := t.TempDir()
	writeDTTCalls(t, root)
	w := newToolsWatch(nil, nil, nil)
	w.recordsRoot = func() string { return root }

	reply := w.handle(ToolsRequest{Req: "r1", Op: "history"}, false, "n")
	if !reply.OK || reply.Req != "r1" {
		t.Fatalf("history: %+v", reply)
	}
	h := reply.History
	if len(h) != 2 || h[0].ID != "1793101300-a1b.txt" || h[1].ID != "1793101234-k7q.txt" {
		t.Fatalf("history should be the two records, newest first: %+v", h)
	}
	if !strings.Contains(reply.Detail, "1 unreadable") {
		t.Errorf("detail %q should count the skipped record", reply.Detail)
	}

	read := h[0]
	if read.Mode != "read" || read.Document != "/x/doc.dtt.yaml" || read.Tool != "" || read.OK || read.ExitCode != 1 ||
		read.Session != "sess-2" || read.SessionRecord != "1793101300-raw.txt" || read.Host != "h2" || read.DurationMs != 300 {
		t.Errorf("read entry %+v", read)
	}
	if want := time.Date(2026, 10, 27, 10, 21, 40, 0, time.UTC).UnixMilli(); read.At != want {
		t.Errorf("read at %d, want %d", read.At, want)
	}
	if read.ImagePath != "/home/x/.ufa/dtt/pathless/doc_dtt_yaml/abc/images/20261027T102140Z-read.image.json" {
		t.Errorf("read image path %q", read.ImagePath)
	}
	if s := read.Summary; s == nil || s.Kind != "image" || s.OK || s.Mode != "read" || s.Error != "one failed" || s.Image["measured"] != 3 {
		t.Errorf("read summary %+v", read.Summary)
	}

	run := h[1]
	if run.Mode != "run" || run.Tool != "materialize-tree" || run.Document != "" || !run.OK || run.Agent != "a1" || run.Session != "sess-1" ||
		run.Command != "/usr/local/bin/dtt run materialize-tree --input - --workdir /tmp/w --json" {
		t.Errorf("run entry %+v", run)
	}
	if s := run.Summary; s == nil || s.Kind != "result" || !s.OK || s.Mode != "assert" || s.Image["unchanged"] != 2 || s.Image["changed"] != 1 {
		t.Errorf("run summary %+v", run.Summary)
	}
	if run.ImagePath != "" {
		t.Errorf("run image path %q, want none", run.ImagePath)
	}

	d := w.handle(ToolsRequest{Op: "history-detail", Call: "1793101300-a1b.txt"}, false, "n")
	if !d.OK || d.Call == nil {
		t.Fatalf("history-detail: %+v", d)
	}
	if d.Call.RawCommand != "dtt read /x/doc.dtt.yaml --json" || !strings.Contains(d.Call.Stdout, "\n  \"format\": \"dtt-image-v1\"") ||
		!strings.HasPrefix(d.Call.Stderr, "dtt: image kept at ") || d.Call.Mode != "read" {
		t.Errorf("detail %+v", d.Call)
	}
	for _, bad := range []string{"../1793101300-a1b.txt", "", "nope.txt", "notes.md"} {
		if r := w.handle(ToolsRequest{Op: "history-detail", Call: bad}, false, "n"); r.OK {
			t.Errorf("history-detail %q should fail", bad)
		}
	}
}

func TestToolsDTTCallHistoryMissing(t *testing.T) {
	w := newToolsWatch(nil, nil, nil)
	root := t.TempDir()
	w.recordsRoot = func() string { return root }
	reply := w.handle(ToolsRequest{Op: "history"}, false, "n")
	if !reply.OK || len(reply.History) != 0 || !strings.Contains(reply.Detail, "clauditable") {
		t.Errorf("a missing dtt-calls should be no calls, explained: %+v", reply)
	}
	w.recordsRoot = nil
	if reply := w.handle(ToolsRequest{Op: "history"}, false, "n"); !reply.OK || len(reply.History) != 0 {
		t.Errorf("no records root should be no calls: %+v", reply)
	}
}

func TestToolsDTTCallHistoryFitsRelay(t *testing.T) {
	root := t.TempDir()
	dir := writeDTTCalls(t, root)
	big := `{"ok":true,"format":"dtt-image-v1","mode":"read","summary":{},"objects":["` + strings.Repeat("x", 3*maxRelayedTools) + `"]}`
	for i := 0; i < 300; i++ {
		name := fmt.Sprintf("%d-%03d.txt", 1793200000+i, i)
		content := big
		if i < 299 {
			content = `{"ok":true}`
		}
		ev := fmt.Sprintf(`{"timestamp":"2026-10-28T00:00:00Z","event_type":"dtt_call","duration_ms":1,"exit_code":0,"session":"s"}`)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(dttCallFixture(ev, "dtt read /some/rather/long/path/to/a/document.dtt.yaml --json", content, "")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w := newToolsWatch(nil, nil, nil)
	w.recordsRoot = func() string { return root }

	local := w.handle(ToolsRequest{Op: "history"}, false, "n")
	if len(local.History) != maxToolHistory || local.HistoryOmitted != 302-maxToolHistory {
		t.Errorf("local history: %d entries, %d omitted", len(local.History), local.HistoryOmitted)
	}
	if !local.History[0].Partial {
		t.Errorf("the newest (big) record was read in part: %+v", local.History[0])
	}

	relayed := w.handle(ToolsRequest{Op: "history"}, true, "n")
	b, _ := json.Marshal(relayed)
	if !relayed.OK || len(b) > maxRelayedTools || len(relayed.History) == 0 || relayed.HistoryOmitted <= local.HistoryOmitted {
		t.Errorf("relayed history: ok %v, %d bytes, %d entries, %d omitted", relayed.OK, len(b), len(relayed.History), relayed.HistoryOmitted)
	}
	if relayed.History[0].ID != local.History[0].ID {
		t.Errorf("the relay should drop the oldest, not the newest")
	}

	d := w.handle(ToolsRequest{Op: "history-detail", Call: "1793200299-299.txt"}, true, "n")
	b, _ = json.Marshal(d)
	if !d.OK || len(b) > maxRelayedTools || !strings.Contains(d.Call.Note, "stdout cut") || !d.Call.Partial {
		t.Errorf("relayed detail: ok %v, %d bytes, note %q", d.OK, len(b), d.Call.Note)
	}
}

// TestToolsWithRealDTT runs the real dtt binary, built from ../dtt, on its
// reference tool -- skipped without uv (which materialize-tree runs with)
// or a Go toolchain to build dtt.
func TestToolsWithRealDTT(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv isn't installed")
	}
	bin := filepath.Join(t.TempDir(), "dtt")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join("..", "dtt")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("couldn't build dtt: %v\n%s", err, out)
	}
	configDir := t.TempDir()
	os.WriteFile(filepath.Join(configDir, "dtt.yaml"), []byte("tools-dir: "+filepath.Join(t.TempDir(), "tools")+"\n"), 0o644)
	t.Setenv("UFA_CONFIG_DIR", configDir)

	s := newServer("test-lr")
	s.fileCacheDir = t.TempDir()
	s.hostStoreDir = t.TempDir()
	s.tools.resolve = func() (string, error) { return bin, nil }
	s.tools.runDir = t.TempDir()
	t.Cleanup(func() { setToolControlOps(nil, false) })
	s.tools.check()
	if _, ok, _ := s.tools.tool("materialize-tree"); !ok {
		t.Fatalf("dtt didn't list materialize-tree: %+v", s.tools.snapshot())
	}

	r := runningEngine(s)
	spec, _ := findControlOp("tool.materialize-tree", nil)
	if _, err := spec.run(r, opArgs{"spec": "a.txt: hello\nsub/: ~\n", "overwrite": "false"}); err != nil {
		t.Fatalf("tool.materialize-tree: %v", err)
	}
	if r.vars["file_count"] != "1" || r.vars["dir_count"] != "1" {
		t.Errorf("vars %+v", r.vars)
	}
	if files := s.listFiles(); len(files) != 1 || !strings.HasSuffix(files[0].Name, "tree.zip") {
		t.Errorf("files tab %+v", files)
	}
}
