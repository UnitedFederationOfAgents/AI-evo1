package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// "dtt create": a new tool directory that validates and runs as it stands,
// for its author to edit -- tool.yaml plus a uv Python script (dependencies
// declared inline, PEP 723) or, with --runtime exec, a shell script.

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
uv installs them on first run.
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

const scaffoldShell = `#!/bin/sh
# {{name}} -- a T3 run by dtt (see AI-evo1/dtt/README.md), runtime exec.
#
# Inputs arrive as DTT_IN_<NAME> environment variables (and as JSON on
# stdin); results are written as JSON to the file $DTT_OUTPUT names.
# stderr (and stdout) is the run's log.
set -eu
echo "writing a report of ${#DTT_IN_MESSAGE} characters" >&2
printf '%s\n' "$DTT_IN_MESSAGE" > report.txt
printf '{"values": {"length": %d}, "files": {"report": "report.txt"}}\n' "${#DTT_IN_MESSAGE}" > "$DTT_OUTPUT"
`

// scaffold creates parent/name and returns its path.
func scaffold(parent, name, runtime string) (string, error) {
	if !toolNameRe.MatchString(name) {
		return "", fmt.Errorf("%w: %q: tool names are lowercase letters, digits and '-', starting with a letter", errBadInput, name)
	}
	script, body, mode := "main.py", scaffoldPython, os.FileMode(0o755)
	switch runtime {
	case "uv":
	case "exec":
		script, body = "run.sh", scaffoldShell
	default:
		return "", fmt.Errorf("%w: --runtime %q should be uv or exec", errBadInput, runtime)
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
