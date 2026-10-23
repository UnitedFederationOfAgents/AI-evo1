package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This file is "dtt run": one call of one T3. dtt checks the inputs, makes
// (or takes) a scratch working directory, runs the entrypoint there under
// its timeout, and reports one RunResult. The tool gets its inputs as JSON
// on stdin and as DTT_IN_<NAME> environment variables, writes its results
// as JSON to the file $DTT_OUTPUT names, and logs to stderr (stdout is
// folded into the log too, so a stray print can't corrupt anything).
//
// $DTT_OUTPUT holds {"values": {name: value}, "files": {name: path}} --
// names from the tool's outputs, a file's path relative to the working
// directory or absolute. Values that aren't strings are kept as JSON text.

const (
	outputFileName = ".dtt-output.json"
	toolCopyDir    = ".dtt-tool" // where a built-in is extracted, in the working directory

	logKeepBytes = 256 << 10
	logTailLines = 40
	logTailBytes = 8 << 10
)

// RunResult is "dtt run --json"'s output.
type RunResult struct {
	OK         bool              `json:"ok"`
	Tool       string            `json:"tool"`
	Outputs    map[string]string `json:"outputs"`
	Files      []RunFile         `json:"files"`
	LogTail    string            `json:"log_tail"`
	ExitCode   int               `json:"exit_code"`
	Error      string            `json:"error,omitempty"`
	TimedOut   bool              `json:"timed_out,omitempty"`
	Workdir    string            `json:"workdir,omitempty"`
	DurationMs int64             `json:"duration_ms"`
}

// RunFile is one output file the tool wrote.
type RunFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// errBadInput marks a run refused before the tool started: an unknown tool
// or inputs that don't fit its schema. dtt exits exitBadInput for it.
var errBadInput = errors.New("bad input")

type runOptions struct {
	workdir string        // "" makes a fresh one under the temp dir, kept afterwards
	timeout time.Duration // 0: the tool's own
}

// runTool runs t with the given inputs (name -> value, as strings). The
// error is only for a run refused before it started (wrapping errBadInput)
// or that couldn't be set up; a tool that ran and failed comes back as a
// RunResult with OK false.
func runTool(ctx context.Context, cfg config, t *Tool, given map[string]string, opt runOptions) (RunResult, error) {
	res := RunResult{Tool: t.Name, Outputs: map[string]string{}, Files: []RunFile{}, ExitCode: -1}
	typed, strs, err := t.checkInputs(given)
	if err != nil {
		return res, fmt.Errorf("%w: %v", errBadInput, err)
	}
	timeout := t.Timeout
	if opt.timeout > 0 {
		timeout = opt.timeout
	}

	workdir := opt.workdir
	if workdir == "" {
		if workdir, err = os.MkdirTemp("", "dtt-run-"+t.Name+"-"); err != nil {
			return res, err
		}
	} else if err := os.MkdirAll(workdir, 0o755); err != nil {
		return res, err
	}
	if workdir, err = filepath.Abs(workdir); err != nil {
		return res, err
	}
	res.Workdir = workdir

	toolDir := t.Dir
	if toolDir == "" {
		toolDir = filepath.Join(workdir, toolCopyDir)
		os.RemoveAll(toolDir)
		if err := extractBuiltin(t, toolDir); err != nil {
			return res, fmt.Errorf("unpacking built-in %s: %v", t.Name, err)
		}
	}
	script := filepath.Join(toolDir, t.Script)

	var argv []string
	switch t.Runtime {
	case "uv":
		uv, err := exec.LookPath(cfg.uvBin)
		if err != nil {
			res.Error = fmt.Sprintf("%s runs with uv, which isn't installed here (looked for %q) -- install it (https://docs.astral.sh/uv/) or point dtt's \"uv\" setting at it", t.Name, cfg.uvBin)
			return res, nil
		}
		argv = []string{uv, "run", "--quiet", "--script", script}
	case "exec":
		argv = []string{script}
	}

	outPath := filepath.Join(workdir, outputFileName)
	os.Remove(outPath)
	stdin, err := json.Marshal(typed)
	if err != nil {
		return res, err
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = workdir
	cmd.Stdin = bytes.NewReader(stdin)
	log := &tailBuffer{max: logKeepBytes}
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = append(os.Environ(),
		"DTT_TOOL="+t.Name,
		"DTT_TOOL_DIR="+toolDir,
		"DTT_WORKDIR="+workdir,
		"DTT_OUTPUT="+outPath,
	)
	for name, v := range strs {
		cmd.Env = append(cmd.Env, "DTT_IN_"+strings.ToUpper(name)+"="+v)
	}
	// The tool runs in its own process group, which is killed whole on a
	// timeout -- uv's python child included.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second

	start := time.Now()
	runErr := cmd.Run()
	res.DurationMs = time.Since(start).Milliseconds()
	res.LogTail = log.tail(logTailLines, logTailBytes)
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}

	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		res.Error = fmt.Sprintf("%s didn't finish within %s", t.Name, timeout)
		return res, nil
	case ctx.Err() != nil:
		res.Error = fmt.Sprintf("%s was cancelled", t.Name)
		return res, nil
	case runErr != nil && res.ExitCode <= 0:
		res.Error = fmt.Sprintf("running %s: %v", t.Name, runErr)
		return res, nil
	case res.ExitCode != 0:
		res.Error = fmt.Sprintf("%s exited with status %d", t.Name, res.ExitCode)
		if last := lastLine(res.LogTail); last != "" {
			res.Error += ": " + last
		}
		return res, nil
	}

	if err := readOutputs(t, outPath, workdir, &res); err != nil {
		res.Error = err.Error()
		return res, nil
	}
	res.OK = true
	return res, nil
}

// readOutputs reads $DTT_OUTPUT into res. A missing file means no outputs.
func readOutputs(t *Tool, outPath, workdir string, res *RunResult) error {
	data, err := os.ReadFile(outPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var out struct {
		Values map[string]json.RawMessage `json:"values"`
		Files  map[string]string          `json:"files"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("%s wrote $DTT_OUTPUT, but it isn't {\"values\": {...}, \"files\": {...}} JSON: %v", t.Name, err)
	}
	declared := func(list []Output, name string) bool {
		for _, o := range list {
			if o.Name == name {
				return true
			}
		}
		return false
	}
	for name, raw := range out.Values {
		if !declared(t.Values, name) {
			return fmt.Errorf("%s output a value %q its tool.yaml doesn't declare (outputs.values)", t.Name, name)
		}
		res.Outputs[name] = stringifyValue(raw)
	}
	names := make([]string, 0, len(out.Files))
	for name := range out.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !declared(t.Files, name) {
			return fmt.Errorf("%s output a file %q its tool.yaml doesn't declare (outputs.files)", t.Name, name)
		}
		p := out.Files[name]
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(workdir, p)
		}
		st, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("%s's output file %q: %v", t.Name, name, err)
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%s's output file %q (%s) isn't a regular file", t.Name, name, p)
		}
		res.Files = append(res.Files, RunFile{Name: name, Path: filepath.Clean(p), Size: st.Size()})
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu      sync.Mutex
	max     int
	buf     []byte
	dropped bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = append([]byte(nil), b.buf[over:]...)
		b.dropped = true
	}
	return len(p), nil
}

// tail returns at most the last lines lines and maxBytes bytes.
func (b *tailBuffer) tail(lines, maxBytes int) string {
	b.mu.Lock()
	s := string(b.buf)
	b.mu.Unlock()
	cut := len(s) > maxBytes
	if cut {
		s = s[len(s)-maxBytes:]
	}
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > lines {
		parts, cut = parts[len(parts)-lines:], true
	}
	if cut || b.dropped {
		// The first kept line may be partial; mark that lines went before it.
		return "…\n" + strings.Join(parts, "\n")
	}
	return strings.Join(parts, "\n")
}
