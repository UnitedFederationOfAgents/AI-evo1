package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// env is a dtt configuration rooted in a temp dir: its own config dir and
// tools dir, so tests never see ~/.ufa.
type env struct {
	t         *testing.T
	root      string
	configDir string
	toolsDir  string
}

func newEnv(t *testing.T, extra ...string) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{t: t, root: root, configDir: filepath.Join(root, "config"), toolsDir: filepath.Join(root, "tools")}
	if err := os.MkdirAll(e.configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "tools-dir: " + e.toolsDir + "\n"
	if len(extra) > 0 {
		conf += "tools-dirs: " + strings.Join(extra, ",") + "\n"
	}
	if err := os.WriteFile(filepath.Join(e.configDir, "dtt.yaml"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	return e
}

// dtt runs the CLI, returning its exit code and stdout.
func (e *env) dtt(stdin string, args ...string) (int, string) {
	e.t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), append([]string{"--config", e.configDir}, args...), strings.NewReader(stdin), &out, &errb)
	if errb.Len() > 0 {
		e.t.Logf("dtt %s stderr: %s", strings.Join(args, " "), errb.String())
	}
	return code, out.String()
}

func (e *env) config() config {
	cfg, err := loadConfig(e.configDir)
	if err != nil {
		e.t.Fatal(err)
	}
	return cfg
}

// writeTool writes a tool directory under parent.
func writeTool(t *testing.T, parent, dirName, yaml string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(parent, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, toolFile), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const minimalTool = `format: dtt-v1
name: NAME
description: a test tool
entrypoint:
  runtime: exec
  script: run.sh
`

func TestBuiltinMaterializeTree(t *testing.T) {
	e := newEnv(t)
	code, out := e.dtt("", "list", "--json")
	if code != exitOK {
		t.Fatalf("list exited %d: %s", code, out)
	}
	var list ListOutput
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Problems) > 0 {
		t.Fatalf("problems: %+v", list.Problems)
	}
	var mt *ToolInfo
	for i := range list.Tools {
		if list.Tools[i].Name == "materialize-tree" {
			mt = &list.Tools[i]
		}
	}
	if mt == nil {
		t.Fatalf("materialize-tree not listed: %+v", list.Tools)
	}
	if mt.Op != "tool.materialize-tree" || mt.Origin != "builtin" || mt.Runtime != "uv" || mt.Path != "builtin/materialize-tree" {
		t.Errorf("unexpected info %+v", mt)
	}
	if len(mt.Args) != 3 || mt.Args[0].Name != "spec" || !mt.Args[0].Required || mt.Args[0].Type != "text" {
		t.Errorf("unexpected args %+v", mt.Args)
	}
	if mt.Args[2].Name != "overwrite" || mt.Args[2].Default != "false" || mt.Args[2].Type != "bool" {
		t.Errorf("unexpected overwrite arg %+v", mt.Args[2])
	}
	if len(mt.Values) != 3 || len(mt.Files) != 1 || mt.Files[0].Name != "archive" {
		t.Errorf("unexpected outputs %+v / %+v", mt.Values, mt.Files)
	}
	if list.UserDir != e.toolsDir {
		t.Errorf("user_dir %q, want %q", list.UserDir, e.toolsDir)
	}

	code, out = e.dtt("", "describe", "tool.materialize-tree", "--json")
	if code != exitOK || !strings.Contains(out, `"source"`) || !strings.Contains(out, "format: dtt-v1") {
		t.Errorf("describe exited %d: %s", code, out)
	}
}

func TestParseToolErrors(t *testing.T) {
	base := func(extra string) string {
		return "format: dtt-v1\nname: t\ndescription: d\nentrypoint:\n  script: main.py\n" + extra
	}
	for _, tc := range []struct{ name, src, want string }{
		{"format", strings.Replace(base(""), "dtt-v1", "dtt-v0", 1), "format is"},
		{"name", strings.Replace(base(""), "name: t", "name: Bad_Name", 1), "tool names"},
		{"description", strings.Replace(base(""), "description: d", "description: ''", 1), "needs a description"},
		{"unknown key", base("colour: red\n"), `takes no "colour"`},
		{"input type", base("inputs:\n  - name: a\n    type: blob\n"), "type \"blob\""},
		{"reserved input", base("inputs:\n  - name: hint\n"), "reserved"},
		{"dup names", base("inputs:\n  - name: a\noutputs:\n  values:\n    - name: a\n"), "same name"},
		{"required default", base("inputs:\n  - name: a\n    required: true\n    default: x\n"), "takes no default"},
		{"bad default", base("inputs:\n  - name: a\n    type: int\n    default: x\n"), "whole number"},
		{"choice default", base("inputs:\n  - name: a\n    choices: [x, y]\n    default: z\n"), "isn't one of"},
		{"runtime", strings.Replace(base(""), "  script", "  runtime: node\n  script", 1), "uv or exec"},
		{"script escapes", strings.Replace(base(""), "main.py", "../main.py", 1), "relative path"},
		{"timeout", base("") + "  timeout: forever\n", "entrypoint.timeout"},
		{"no entrypoint", "format: dtt-v1\nname: t\ndescription: d\n", "needs an entrypoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTool(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
	if _, err := parseTool(base("")); err != nil {
		t.Fatalf("the base tool should parse: %v", err)
	}
}

func TestCheckInputs(t *testing.T) {
	tool, err := parseTool(`format: dtt-v1
name: t
description: d
inputs:
  - name: s
    required: true
  - name: n
    type: int
    default: "3"
  - name: f
    type: bool
  - name: mode
    choices: [fast, slow]
    default: fast
  - name: body
    type: text
entrypoint:
  script: main.py
`)
	if err != nil {
		t.Fatal(err)
	}
	typed, strs, err := tool.checkInputs(map[string]string{"s": "hi", "body": "a\nb"})
	if err != nil {
		t.Fatal(err)
	}
	if typed["s"] != "hi" || typed["n"] != int64(3) || typed["f"] != nil || typed["mode"] != "fast" || typed["body"] != "a\nb" {
		t.Errorf("typed %+v", typed)
	}
	if strs["n"] != "3" || strs["mode"] != "fast" {
		t.Errorf("strs %+v", strs)
	}
	if _, has := strs["f"]; has {
		t.Errorf("an unset optional input with no default shouldn't be in the env: %+v", strs)
	}
	for _, tc := range []struct {
		given map[string]string
		want  string
	}{
		{map[string]string{}, `needs input "s"`},
		{map[string]string{"s": "  "}, `needs input "s"`},
		{map[string]string{"s": "x", "zz": "1"}, `takes no input "zz"`},
		{map[string]string{"s": "x", "n": "three"}, "whole number"},
		{map[string]string{"s": "x", "f": "maybe"}, "true or false"},
		{map[string]string{"s": "x", "mode": "medium"}, "isn't one of"},
		{map[string]string{"s": "two\nlines"}, "one line"},
	} {
		if _, _, err := tool.checkInputs(tc.given); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: got %v, want %q", tc.given, err, tc.want)
		}
	}
}

func TestCatalogScan(t *testing.T) {
	extra := t.TempDir()
	e := newEnv(t, extra)
	// A user tool named like the built-in is shadowed by it.
	writeTool(t, e.toolsDir, "materialize-tree", strings.Replace(minimalTool, "NAME", "materialize-tree", 1), map[string]string{"run.sh": "#!/bin/sh\n"})
	writeTool(t, e.toolsDir, "good", strings.Replace(minimalTool, "NAME", "good", 1), map[string]string{"run.sh": "#!/bin/sh\n"})
	writeTool(t, e.toolsDir, "misnamed", strings.Replace(minimalTool, "NAME", "other", 1), map[string]string{"run.sh": "#!/bin/sh\n"})
	writeTool(t, e.toolsDir, "noscript", strings.Replace(minimalTool, "NAME", "noscript", 1), nil)
	// The same name in tools-dirs is shadowed by the user dir's.
	writeTool(t, extra, "good", strings.Replace(minimalTool, "NAME", "good", 1), map[string]string{"run.sh": "#!/bin/sh\n"})
	writeTool(t, extra, "extra-one", strings.Replace(minimalTool, "NAME", "extra-one", 1), map[string]string{"run.sh": "#!/bin/sh\n"})

	cat := scanCatalog(e.config())
	names := []string{}
	for _, tl := range cat.tools {
		names = append(names, tl.Name+"@"+tl.Origin)
	}
	if got := strings.Join(names, " "); got != "aws@builtin extra-one@tools-dirs filesystem@builtin github@builtin good@user materialize-tree@builtin" {
		t.Errorf("tools: %s", got)
	}
	if len(cat.shadowed) != 2 {
		t.Errorf("shadowed: %+v", cat.shadowed)
	}
	if len(cat.problems) != 2 {
		t.Errorf("problems: %+v", cat.problems)
	}
	if len(cat.dirs) != 3 || cat.dirs[1].Status != "ok" || cat.dirs[2].Status != "ok" {
		t.Errorf("dirs: %+v", cat.dirs)
	}
}

// TestExecLifecycle walks scaffold -> validate -> install -> list -> run
// with the exec runtime, which needs no uv.
func TestExecLifecycle(t *testing.T) {
	e := newEnv(t)
	work := t.TempDir()
	code, out := e.dtt("", "create", "shout", "--dir", work, "--runtime", "exec", "--json")
	if code != exitOK {
		t.Fatalf("create exited %d: %s", code, out)
	}
	src := filepath.Join(work, "shout")
	if code, out = e.dtt("", "validate", src, "--json"); code != exitOK {
		t.Fatalf("validate exited %d: %s", code, out)
	}
	if code, out = e.dtt("", "run", "shout", "--json"); code != exitBadInput {
		t.Fatalf("running an uninstalled tool exited %d, want %d: %s", code, exitBadInput, out)
	}
	if code, out = e.dtt("", "install", src, "--json"); code != exitOK {
		t.Fatalf("install exited %d: %s", code, out)
	}
	if code, out = e.dtt("", "install", src, "--json"); code != exitBadInput || !strings.Contains(out, "--force") {
		t.Fatalf("a second install exited %d: %s", code, out)
	}
	if code, out = e.dtt("", "install", src, "--force", "--json"); code != exitOK {
		t.Fatalf("install --force exited %d: %s", code, out)
	}
	if code, out = e.dtt("", "list", "--json"); code != exitOK || !strings.Contains(out, `"tool.shout"`) {
		t.Fatalf("list exited %d: %s", code, out)
	}

	if code, out = e.dtt("", "run", "shout", "--json"); code != exitBadInput || !strings.Contains(out, `needs input \"message\"`) {
		t.Fatalf("a run missing its input exited %d: %s", code, out)
	}
	wd := filepath.Join(t.TempDir(), "wd")
	code, out = e.dtt("", "run", "tool.shout", "--arg", "message=hello", "--workdir", wd, "--json")
	if code != exitOK {
		t.Fatalf("run exited %d: %s", code, out)
	}
	var res RunResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Outputs["length"] != "5" || res.ExitCode != 0 || res.Workdir != wd {
		t.Errorf("result %+v", res)
	}
	if len(res.Files) != 1 || res.Files[0].Name != "report" || res.Files[0].Path != filepath.Join(wd, "report.txt") {
		t.Fatalf("files %+v", res.Files)
	}
	if b, _ := os.ReadFile(res.Files[0].Path); string(b) != "hello\n" {
		t.Errorf("report.txt holds %q", b)
	}
	if !strings.Contains(res.LogTail, "writing a report of 5 characters") {
		t.Errorf("log tail %q", res.LogTail)
	}

	// Inputs from a file (stdin here), with --arg winning.
	code, out = e.dtt("message: from file\n", "run", "shout", "--input", "-", "--json")
	if code != exitOK || !strings.Contains(out, `"length": "9"`) {
		t.Fatalf("run --input exited %d: %s", code, out)
	}
	code, out = e.dtt(`{"message": "from json"}`, "run", "shout", "--input", "-", "--arg", "message=x", "--json")
	if code != exitOK || !strings.Contains(out, `"length": "1"`) {
		t.Fatalf("run --input with --arg exited %d: %s", code, out)
	}
}

func TestRunFailures(t *testing.T) {
	e := newEnv(t)
	tool := func(name, script, extra string) {
		yaml := strings.Replace(minimalTool, "NAME", name, 1) + extra
		writeTool(t, e.toolsDir, name, yaml, map[string]string{"run.sh": "#!/bin/sh\n" + script})
	}
	tool("slow", "sleep 5\n", "")
	tool("broken", "echo 'it went wrong' >&2\nexit 3\n", "")
	tool("liar", `echo '{"values": {"surprise": 1}}' > "$DTT_OUTPUT"`+"\n", "")
	tool("bad-json", `echo 'not json' > "$DTT_OUTPUT"`+"\n", "")
	tool("lost-file", `echo '{"files": {"out": "nowhere.txt"}}' > "$DTT_OUTPUT"`+"\n", "outputs:\n  files:\n    - name: out\n")

	for _, tc := range []struct {
		tool string
		args []string
		want string
	}{
		{"slow", []string{"--timeout", "200ms"}, "didn't finish within 200ms"},
		{"broken", nil, "exited with status 3: it went wrong"},
		{"liar", nil, `doesn't declare`},
		{"bad-json", nil, "isn't"},
		{"lost-file", nil, "nowhere.txt"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			code, out := e.dtt("", append([]string{"run", tc.tool, "--json"}, tc.args...)...)
			if code != exitToolFailed {
				t.Fatalf("exited %d, want %d: %s", code, exitToolFailed, out)
			}
			var res RunResult
			if err := json.Unmarshal([]byte(out), &res); err != nil {
				t.Fatal(err)
			}
			if res.OK || !strings.Contains(res.Error, tc.want) {
				t.Fatalf("error %q, want it to contain %q", res.Error, tc.want)
			}
		})
	}
}

func TestValidateStdinAndCreateInstall(t *testing.T) {
	e := newEnv(t)
	code, out := e.dtt(strings.Replace(minimalTool, "NAME", "piped", 1), "validate", "-", "--schema-only", "--json")
	if code != exitOK || !strings.Contains(out, `"tool.piped"`) {
		t.Fatalf("validate - exited %d: %s", code, out)
	}
	if code, out = e.dtt("format: dtt-v1\n", "validate", "-", "--schema-only", "--json"); code != exitBadInput {
		t.Fatalf("validating a bad schema exited %d: %s", code, out)
	}
	// create --install puts it straight into the tools dir; installing it
	// from there again is a no-op.
	if code, out = e.dtt("", "create", "fresh", "--install", "--json"); code != exitOK {
		t.Fatalf("create --install exited %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(e.toolsDir, "fresh", "main.py")); err != nil {
		t.Fatal(err)
	}
	if code, out = e.dtt("", "install", filepath.Join(e.toolsDir, "fresh"), "--json"); code != exitOK {
		t.Fatalf("installing in place exited %d: %s", code, out)
	}
	if code, out = e.dtt("", "create", "materialize-tree", "--install", "--json"); code != exitBadInput {
		t.Fatalf("creating a built-in's name exited %d: %s", code, out)
	}
}

// TestMaterializeTreeWithUV runs the real reference tool. uv fetches pyyaml
// on its first run, so this needs uv and (once) the network.
func TestMaterializeTreeWithUV(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv isn't installed")
	}
	e := newEnv(t)
	wd := t.TempDir()
	input := "spec: |\n  README.md: \"# hello\"\n  src/: ~\n  src/main.py: print('hi')\n"
	code, out := e.dtt(input, "run", "materialize-tree", "--input", "-", "--workdir", wd, "--json")
	if code != exitOK {
		t.Fatalf("run exited %d: %s", code, out)
	}
	var res RunResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Outputs["file_count"] != "2" || res.Outputs["dir_count"] != "1" || res.Outputs["root"] != filepath.Join(wd, "tree") {
		t.Errorf("outputs %+v", res.Outputs)
	}
	if b, _ := os.ReadFile(filepath.Join(wd, "tree", "src", "main.py")); string(b) != "print('hi')" {
		t.Errorf("main.py holds %q", b)
	}
	if len(res.Files) != 1 {
		t.Fatalf("files %+v", res.Files)
	}
	z, err := zip.OpenReader(res.Files[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	var names []string
	for _, f := range z.File {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, " "); got != "README.md src/ src/main.py" {
		t.Errorf("tree.zip holds %s", got)
	}
	// A second run refuses to overwrite without overwrite: true.
	code, out = e.dtt(input, "run", "materialize-tree", "--input", "-", "--workdir", wd, "--json")
	if code != exitToolFailed || !strings.Contains(out, "already exists") {
		t.Fatalf("a second run exited %d: %s", code, out)
	}
}
