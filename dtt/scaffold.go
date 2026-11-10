package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// "dtt create": a new tool directory that validates and runs as it stands,
// for its author to edit -- tool.yaml plus a Python script (dependencies
// declared inline, PEP 723, for uv) or, with --runtime go, a Go main
// package using only the standard library (see runtimes.go).

const scaffoldYAML = `format: dtt-v1
name: {{name}}
description: What {{name}} does, in a sentence or two. Control runs and the tools tab show this.
# How a control step lists a call; {input} is replaced by that input's value.
describe: "{{name}}: {message}"
inputs:
  - name: message
    type: string
    help: the text to work on
    required: true
outputs:
  values:
    - name: length
      help: how many characters the message has
  files:
    - name: report
      help: a text file holding the message
entrypoint:
  runtime: {{runtime}}
  script: {{script}}
  timeout: 60s
`

const scaffoldPython = `#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""{{name}} -- a T3 run by dtt (see AI-evo1/dtt/README.md).

Inputs arrive as JSON on stdin (and as DTT_IN_<NAME> environment
variables); results are written as JSON to the file $DTT_OUTPUT names.
stderr (and stdout) is the run's log. Add dependencies to the list above;
uv installs them on first run. (Where uv isn't installed dtt runs this with
python3, and they must be installed already.)
"""
import json
import os
import sys


def main() -> int:
    inputs = json.load(sys.stdin)
    message = inputs["message"]
    print(f"writing a report of {len(message)} characters", file=sys.stderr)
    with open("report.txt", "w") as f:  # relative paths land in the run's working directory
        f.write(message + "\n")
    with open(os.environ["DTT_OUTPUT"], "w") as f:
        json.dump({"values": {"length": len(message)}, "files": {"report": "report.txt"}}, f)
    return 0


if __name__ == "__main__":
    sys.exit(main())
`

const scaffoldGo = `// {{name}} -- a T3 run by dtt (see AI-evo1/dtt/README.md), runtime go.
//
// dtt builds this package before each run (the go build cache keeps that
// quick). Inputs arrive as JSON on stdin (and as DTT_IN_<NAME> environment
// variables); results are written as JSON to the file $DTT_OUTPUT names.
// stderr (and stdout) is the run's log. With no go.mod here, dtt builds the
// package as a module of its own, so it can use the standard library only;
// add a go.mod (and go.sum) to use other modules.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"unicode/utf8"
)

func main() {
	var inputs struct {
		Message string ` + "`json:\"message\"`" + `
	}
	if err := json.NewDecoder(os.Stdin).Decode(&inputs); err != nil {
		fmt.Fprintln(os.Stderr, "{{name}}: reading inputs:", err)
		os.Exit(1)
	}
	n := utf8.RuneCountInString(inputs.Message)
	fmt.Fprintf(os.Stderr, "writing a report of %d characters\n", n)
	// Relative paths land in the run's working directory.
	if err := os.WriteFile("report.txt", []byte(inputs.Message+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "{{name}}:", err)
		os.Exit(1)
	}
	out, _ := json.Marshal(map[string]any{
		"values": map[string]any{"length": n},
		"files":  map[string]string{"report": "report.txt"},
	})
	if err := os.WriteFile(os.Getenv("DTT_OUTPUT"), out, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "{{name}}:", err)
		os.Exit(1)
	}
}
`

// scaffold creates parent/name and returns its path.
func scaffold(parent, name, runtime string) (string, error) {
	if !toolNameRe.MatchString(name) {
		return "", fmt.Errorf("%w: %q: tool names are lowercase letters, digits and '-', starting with a letter", errBadInput, name)
	}
	script, body, mode := "main.py", scaffoldPython, os.FileMode(0o755)
	switch runtime {
	case "python", "uv":
		runtime = "python"
	case "go":
		script, body, mode = "main.go", scaffoldGo, 0o644
	default:
		return "", fmt.Errorf("%w: --runtime %q should be python or go", errBadInput, runtime)
	}
	dir, err := filepath.Abs(filepath.Join(parent, name))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("%w: %s already exists", errBadInput, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	fill := strings.NewReplacer("{{name}}", name, "{{runtime}}", runtime, "{{script}}", script)
	if err := os.WriteFile(filepath.Join(dir, toolFile), []byte(fill.Replace(scaffoldYAML)), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, script), []byte(fill.Replace(body)), mode); err != nil {
		return "", err
	}
	return dir, nil
}
