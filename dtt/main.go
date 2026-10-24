// dtt -- the declarative tool tool. A transient CLI: every call starts, does
// one thing and exits; there is no server, no representable connection and
// no restart signal. local-representative is its main caller (its tools tab
// and tool.<name> control ops -- see local-representative/tools.go), so
// with --json, one JSON document on stdout is the contract.
//
// A T3 (declarative-tool-tool-tool) is a directory holding tool.yaml
// (format dtt-v1, see schema.go) and its script, run with uv (the default;
// dependencies declared inline in the script) or directly (exec). See
// README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	ufaconfig "ufa-configurable"
	ufaversion "ufa-version"
)

// Exit codes are fixed: callers tell a tool that failed from a call that
// was wrong.
const (
	exitOK         = 0
	exitToolFailed = 1 // the tool ran (or tried to) and failed, or dtt itself couldn't do what was asked
	exitBadInput   = 2 // usage, an unknown tool, inputs or a tool.yaml that don't fit the schema
)

const usageText = `dtt -- the declarative tool tool (T3s: declarative-tool-tool-tools)

usage: dtt [--config <dir>] <command> [arguments] [--json]

commands:
  list                         list every tool found (built-in, ~/.ufa/tools, tools-dirs)
  describe <tool>              show one tool's schema
  validate <dir|tool.yaml|->   check a tool (- reads tool.yaml from stdin)
                                 --schema-only: don't look for its script
  create <name>                scaffold a new uv Python tool
                                 --dir <parent> (default .) | --install (into the tools dir)
                                 --runtime uv|exec
  install <dir>                validate a tool and copy it into the tools dir (alias: register)
                                 --force: replace an installed copy
  run <tool>                   run a tool once
                                 --arg name=value (repeatable)
                                 --input <file|-> (JSON or flat YAML of name: value)
                                 --workdir <dir> (default: a new temp dir, kept)
                                 --timeout <duration> (default: the tool's own)

--json prints one JSON document on stdout. Exit status: 0 ok, 1 the tool
failed, 2 bad usage or input. --version prints the build version.

settings (~/.ufa/config/dtt.yaml or global.yaml): tools-dir, tools-dirs, uv.
`

func main() {
	if ufaversion.HandleVersionFlag() {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// cli is one invocation's output.
type cli struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	json           bool
}

func (c *cli) emit(v any) {
	enc := json.NewEncoder(c.stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// fail reports err and returns code: as {"ok": false, "error": ...} with
// --json, else on stderr.
func (c *cli) fail(code int, err error) int {
	if c.json {
		c.emit(map[string]any{"ok": false, "error": err.Error()})
	} else {
		fmt.Fprintln(c.stderr, "dtt:", err)
	}
	return code
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := &cli{stdin: stdin, stdout: stdout, stderr: stderr}
	for _, a := range args {
		c.json = c.json || a == "--json" || a == "-json"
	}
	dir, args, err := ufaconfig.ExtractConfigDir(args)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitBadInput
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return exitOK
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "list":
		return c.list(cfg, rest)
	case "describe":
		return c.describe(cfg, rest)
	case "validate":
		return c.validate(rest)
	case "create":
		return c.create(cfg, rest)
	case "install", "register":
		return c.install(cfg, rest)
	case "run", "execute":
		return c.run(ctx, cfg, rest)
	}
	return c.fail(exitBadInput, fmt.Errorf("unknown command %q (dtt help lists them)", cmd))
}

// parseArgs parses fs's flags wherever they fall among args, returning the
// positional arguments.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Bool("json", false, "print JSON")
	return fs
}

// toolName accepts a tool's name or its control op (tool.<name>).
func toolName(s string) string { return strings.TrimPrefix(s, toolOpPrefix) }

// ---- list / describe ----

// ListOutput is "dtt list --json".
type ListOutput struct {
	OK       bool       `json:"ok"`
	Version  string     `json:"version"`
	Tools    []ToolInfo `json:"tools"`
	UserDir  string     `json:"user_dir"` // where install and create --install put tools
	Dirs     []DirInfo  `json:"dirs"`
	Problems []Problem  `json:"problems"`
	Shadowed []Shadowed `json:"shadowed"`
}

func (c *cli) list(cfg config, args []string) int {
	fs := newFlags("list")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	if len(pos) > 0 {
		return c.fail(exitBadInput, fmt.Errorf("list takes no arguments"))
	}
	cat := scanCatalog(cfg)
	out := ListOutput{OK: true, Version: ufaversion.Version, Tools: []ToolInfo{}, UserDir: cfg.toolsDir,
		Dirs: cat.dirs, Problems: append([]Problem{}, cat.problems...), Shadowed: append([]Shadowed{}, cat.shadowed...)}
	for _, t := range cat.tools {
		out.Tools = append(out.Tools, t.info(false))
	}
	if c.json {
		c.emit(out)
		return exitOK
	}
	if len(out.Tools) == 0 {
		fmt.Fprintln(c.stdout, "no tools found")
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	for _, t := range out.Tools {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", t.Name, t.Origin, t.Summary)
	}
	tw.Flush()
	for _, p := range out.Problems {
		fmt.Fprintf(c.stderr, "dtt: skipped %s: %s\n", p.Path, p.Error)
	}
	for _, s := range out.Shadowed {
		fmt.Fprintf(c.stderr, "dtt: %s at %s is hidden by %s\n", s.Name, s.Path, s.By)
	}
	return exitOK
}

func (c *cli) describe(cfg config, args []string) int {
	fs := newFlags("describe")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	if len(pos) != 1 {
		return c.fail(exitBadInput, fmt.Errorf("usage: dtt describe <tool>"))
	}
	t := scanCatalog(cfg).find(toolName(pos[0]))
	if t == nil {
		return c.fail(exitBadInput, fmt.Errorf("no tool %q (dtt list shows them)", pos[0]))
	}
	info := t.info(true)
	if c.json {
		c.emit(map[string]any{"ok": true, "tool": info})
		return exitOK
	}
	fmt.Fprintf(c.stdout, "# %s (%s: %s)\n%s", info.Name, info.Origin, info.Path, info.Source)
	return exitOK
}

// ---- validate ----

func (c *cli) validate(args []string) int {
	fs := newFlags("validate")
	schemaOnly := fs.Bool("schema-only", false, "don't look for the script")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	if len(pos) != 1 {
		return c.fail(exitBadInput, fmt.Errorf("usage: dtt validate <dir|tool.yaml|->"))
	}
	var t *Tool
	switch p := pos[0]; {
	case p == "-":
		src, err := io.ReadAll(c.stdin)
		if err != nil {
			return c.fail(exitToolFailed, err)
		}
		if t, err = parseTool(string(src)); err != nil {
			return c.fail(exitBadInput, err)
		}
		if !*schemaOnly {
			return c.fail(exitBadInput, fmt.Errorf("a tool.yaml read from stdin can only be checked with --schema-only"))
		}
	default:
		dir := p
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			dir = filepath.Dir(p)
			if filepath.Base(p) != toolFile {
				return c.fail(exitBadInput, fmt.Errorf("%s: a tool's schema is a file named %s", p, toolFile))
			}
		}
		if t, err = readToolDir(dir, !*schemaOnly); err != nil {
			return c.fail(exitBadInput, fmt.Errorf("%s: %v", dir, err))
		}
	}
	info := t.info(false)
	if c.json {
		c.emit(map[string]any{"ok": true, "tool": info})
		return exitOK
	}
	fmt.Fprintf(c.stdout, "%s is a valid %s tool (%d input(s), %d output value(s), %d output file(s), runtime %s)\n",
		t.Name, toolFormat, len(t.Inputs), len(t.Values), len(t.Files), t.Runtime)
	return exitOK
}

// ---- create / install ----

func (c *cli) create(cfg config, args []string) int {
	fs := newFlags("create")
	parent := fs.String("dir", ".", "directory to create the tool's directory in")
	inTools := fs.Bool("install", false, "create it in the tools directory, installed")
	runtime := fs.String("runtime", "uv", "uv (a Python script) or exec (a shell script)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	if len(pos) != 1 {
		return c.fail(exitBadInput, fmt.Errorf("usage: dtt create <name> [--dir <parent> | --install] [--runtime uv|exec]"))
	}
	name := pos[0]
	if *inTools {
		*parent = cfg.toolsDir
		if _, err := readBuiltin(name); err == nil {
			return c.fail(exitBadInput, fmt.Errorf("a built-in tool is already called %q -- pick another name", name))
		}
	}
	dir, err := scaffold(*parent, name, *runtime)
	if err != nil {
		if errors.Is(err, errBadInput) {
			return c.fail(exitBadInput, err)
		}
		return c.fail(exitToolFailed, err)
	}
	t, err := readToolDir(dir, true)
	if err != nil {
		return c.fail(exitToolFailed, fmt.Errorf("the scaffold doesn't validate (a dtt bug): %v", err))
	}
	if *inTools {
		t.Origin = "user"
	}
	if c.json {
		c.emit(map[string]any{"ok": true, "path": dir, "tool": t.info(false)})
		return exitOK
	}
	fmt.Fprintf(c.stdout, "created %s in %s -- edit tool.yaml and %s, then dtt validate %s\n", name, dir, t.Script, dir)
	return exitOK
}

func (c *cli) install(cfg config, args []string) int {
	fs := newFlags("install")
	force := fs.Bool("force", false, "replace an installed copy")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	if len(pos) != 1 {
		return c.fail(exitBadInput, fmt.Errorf("usage: dtt install <dir> [--force]"))
	}
	t, err := install(cfg, pos[0], *force)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	if c.json {
		c.emit(map[string]any{"ok": true, "tool": t.info(false)})
		return exitOK
	}
	fmt.Fprintf(c.stdout, "installed %s at %s\n", t.Name, t.Dir)
	return exitOK
}

// ---- run ----

// argList is a repeatable --arg name=value.
type argList map[string]string

func (a argList) String() string { return "" }

func (a argList) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(k) == "" {
		return fmt.Errorf("--arg %q should be name=value", s)
	}
	a[strings.TrimSpace(k)] = v
	return nil
}

func (c *cli) run(ctx context.Context, cfg config, args []string) int {
	fs := newFlags("run")
	given := argList{}
	fs.Var(given, "arg", "an input, name=value")
	inputFile := fs.String("input", "", "a JSON or flat YAML file of inputs (- for stdin)")
	workdir := fs.String("workdir", "", "the run's working directory")
	timeout := fs.Duration("timeout", 0, "override the tool's timeout")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	if len(pos) != 1 {
		return c.fail(exitBadInput, fmt.Errorf("usage: dtt run <tool> [--arg name=value]... [--input <file|->] [--workdir <dir>] [--timeout <d>]"))
	}
	if *timeout < 0 || *timeout > maxToolTimeout {
		return c.fail(exitBadInput, fmt.Errorf("--timeout should be at most %s", maxToolTimeout))
	}
	inputs := map[string]string{}
	if *inputFile != "" {
		if inputs, err = readInputFile(*inputFile, c.stdin); err != nil {
			return c.fail(exitBadInput, err)
		}
	}
	for k, v := range given {
		inputs[k] = v // --arg wins over the input file
	}
	name := toolName(pos[0])
	t := scanCatalog(cfg).find(name)
	if t == nil {
		return c.fail(exitBadInput, fmt.Errorf("no tool %q (dtt list shows them)", pos[0]))
	}

	res, err := runTool(ctx, cfg, t, inputs, runOptions{workdir: *workdir, timeout: *timeout})
	if err != nil {
		code := exitToolFailed
		if errors.Is(err, errBadInput) {
			code = exitBadInput
			err = errors.New(strings.TrimPrefix(err.Error(), errBadInput.Error()+": "))
		}
		res.OK, res.Error = false, err.Error()
		if c.json {
			c.emit(res)
		} else {
			fmt.Fprintln(c.stderr, "dtt:", err)
		}
		return code
	}
	if c.json {
		c.emit(res)
	} else {
		c.printRun(res)
	}
	if !res.OK {
		return exitToolFailed
	}
	return exitOK
}

func (c *cli) printRun(res RunResult) {
	if res.OK {
		fmt.Fprintf(c.stdout, "%s: ok in %s\n", res.Tool, time.Duration(res.DurationMs)*time.Millisecond)
	} else {
		fmt.Fprintf(c.stdout, "%s: FAILED: %s\n", res.Tool, res.Error)
	}
	names := make([]string, 0, len(res.Outputs))
	for k := range res.Outputs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Fprintf(c.stdout, "  %s = %s\n", k, res.Outputs[k])
	}
	for _, f := range res.Files {
		fmt.Fprintf(c.stdout, "  file %s: %s (%d bytes)\n", f.Name, f.Path, f.Size)
	}
	if res.Workdir != "" {
		fmt.Fprintf(c.stdout, "  workdir: %s\n", res.Workdir)
	}
	if !res.OK && res.LogTail != "" {
		fmt.Fprintf(c.stdout, "  log:\n%s\n", indent(res.LogTail, "    "))
	}
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// readInputFile reads inputs as a JSON object (any scalar values) or a flat
// YAML mapping of name: value.
func readInputFile(p string, stdin io.Reader) (map[string]string, error) {
	var data []byte
	var err error
	if p == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(p)
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if trimmed := strings.TrimSpace(string(data)); strings.HasPrefix(trimmed, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("input %s: %v", p, err)
		}
		for k, v := range raw {
			if s := strings.TrimSpace(string(v)); s == "null" {
				continue
			} else if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
				return nil, fmt.Errorf("input %s: %q should be a single value", p, k)
			}
			out[k] = stringifyValue(v)
		}
		return out, nil
	}
	doc, err := parseYAML(string(data))
	if err != nil {
		return nil, fmt.Errorf("input %s: %v", p, err)
	}
	if doc.kind != yMap {
		return nil, fmt.Errorf("input %s should be a mapping of name: value", p)
	}
	for i, k := range doc.keys {
		if doc.vals[i].kind != yScalar {
			return nil, fmt.Errorf("input %s: %q should be a single value (a | block for text)", p, k)
		}
		out[k] = doc.vals[i].str
	}
	return out, nil
}
