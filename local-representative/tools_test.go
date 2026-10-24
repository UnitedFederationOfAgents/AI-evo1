package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	if r := w.handle(ToolsRequest{Op: "explode"}, false, ""); r.OK {
		t.Errorf("an unknown op: %+v", r)
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
