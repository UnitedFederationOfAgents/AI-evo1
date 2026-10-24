# dtt — the declarative tool tool

`dtt` creates, checks and runs **T3s** (declarative-tool-tool-tools). A T3 is
a tool described by a small YAML schema and written in any language, with
Python (run through [`uv`](https://docs.astral.sh/uv/)) as the default.

`dtt` is a **transient CLI**. Each call starts, does one thing and exits.
There is no server, no representable connection and no restart signal.
Its main caller is local-representative, which uses it for its **tools** tab
and the `tool.<name>` control ops (see
[`local-representative/README.md`](../local-representative/README.md#tools-tab)).
With `--json`, one JSON document on stdout is the contract.

It modernizes the stub at `ignored-scratch/AI-sandboxing/declarative-tool-tool`
(`tool-tool create/register/execute`). The registry file is gone: the tools
on disk are the only record of what's installed.

## Commands

```
dtt list                       [--json]
dtt describe <tool>            [--json]
dtt validate <dir|tool.yaml|-> [--schema-only] [--json]
dtt create <name>              [--dir <parent> | --install] [--runtime uv|exec] [--json]
dtt install <dir>              [--force] [--json]     (alias: register)
dtt run <tool>                 [--arg name=value]... [--input <file|->]
                               [--workdir <dir>] [--timeout <d>] [--json]
dtt --version
```

`<tool>` may be written as its control op, `tool.<name>`. `--config <dir>`
works as it does for every UFA app.

**Exit status** is fixed:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | the tool failed: it exited non-zero, timed out, wrote bad output, or its runtime (`uv`) is missing |
| 2 | bad input: usage, an unknown tool, inputs that don't fit the schema, or an invalid `tool.yaml` |

## Where tools come from

There's no index. Every call scans these places, in this order:

1. **built-in**: tools embedded in the binary (`builtin/`), currently
   `materialize-tree`.
2. **user**: the `tools-dir` setting, `~/.ufa/tools` by default. `install`
   and `create --install` put tools here.
3. **tools-dirs**: each directory in the `tools-dirs` setting (a comma- or
   space-separated list).

Each tool is a directory **named after the tool** that holds `tool.yaml` and
its script. If two tools share a name, the first one found wins. The others
are listed as `shadowed` in `dtt list --json`. Directories that look like
tools but don't validate are listed as `problems`.

`install <dir>` validates the tool and copies it into the tools directory,
leaving out hidden files such as a `.venv`. It replaces an installed copy only
with `--force`, and it refuses a built-in tool's name.

Settings go in `~/.ufa/config/dtt.yaml` (or `global.yaml`):

```yaml
tools-dir: ~/.ufa/tools
tools-dirs: /srv/shared-tools, ~/work/tools
uv: /usr/local/bin/uv     # the uv binary, if it isn't on PATH
```

## The schema: `format: dtt-v1`

```yaml
format: dtt-v1                 # required; versioned like lr-control-v1
name: materialize-tree         # lowercase letters, digits, '-'; matches its directory
description: Build a directory tree from a flat YAML description ...
describe: materialize a directory tree from YAML   # optional: how a control step lists a call ({input} is replaced)
inputs:
  - name: spec                 # lowercase letters, digits, '_'
    type: text                 # string (default), text, int, number, bool, path
    help: flat YAML mapping each relative path to its content
    required: true             # a required input takes no default
  - name: overwrite
    type: bool
    default: "false"
  - name: mode
    choices: [fast, slow]      # optional: the only values allowed
    default: fast
outputs:
  values:                      # named values; a control run saves each one under its name
    - name: root
      help: the tree's absolute path
  files:                       # named files; a control run copies each one into the files tab
    - name: archive
      help: the tree as tree.zip
entrypoint:
  runtime: uv                  # uv (default) or exec
  script: main.py              # relative to the tool's directory
  timeout: 120s                # default 60s, at most 1h
```

Inputs carry the same fields as a control op's arguments
(`ControlOpArg`: `name`, `help`, `required`, `default`), plus `type` and
`choices`. That makes a T3 a control op with no translation layer: each
entry in `dtt list --json` starts with `op` (`tool.<name>`), `summary`,
`describe` and `args`, which is the `ControlOpSpec` shape.

Unknown keys are errors, so typos show up rather than being ignored. The
names `op`, `save_prefix` and `hint` are reserved for the arguments
local-representative adds to every `tool.<name>` op. Input names and output
names share one namespace.

## Runtimes

- **`uv`** (the default) runs `uv run --quiet --script <script>`. Declare the
  script's dependencies inline with a [PEP 723](https://peps.python.org/pep-0723/)
  block, and uv installs them on first use:

  ```python
  #!/usr/bin/env -S uv run --script
  # /// script
  # requires-python = ">=3.10"
  # dependencies = ["pyyaml>=6"]
  # ///
  ```

  If `uv` isn't installed, `dtt run` fails with exit 1 and a message saying
  so. `uv` isn't part of `make check-dev-deps`. Install it on any host that
  runs uv tools.
- **`exec`** runs the script directly, so it must be executable and start
  with a `#!` line.

## The run contract

`dtt run` checks the inputs against the schema. Unknown names, missing
required values, wrong types and values outside `choices` exit with code 2
before anything runs. Defaults are filled in. Then dtt:

- **creates the working directory**, or uses `--workdir`. The tool runs with
  this as its current directory. Without `--workdir`, dtt creates a new
  directory under `$TMPDIR` and leaves it in place afterwards. A built-in
  tool is unpacked into `.dtt-tool/` inside it.
- **passes the inputs** two ways:
  - as **JSON on stdin**, typed: an `int` is a number, a `bool` is
    `true`/`false`, a `path` has `~` expanded, and an unset optional input
    with no default is `null`;
  - as **`DTT_IN_<NAME>`** environment variables, which are strings, with
    unset inputs left out. Alongside them are `DTT_TOOL`, `DTT_TOOL_DIR`,
    `DTT_WORKDIR` and `DTT_OUTPUT`.
- **reads the tool's results** from the file named by `$DTT_OUTPUT`:

  ```json
  {"values": {"root": "/tmp/x/tree", "file_count": 2}, "files": {"archive": "tree.zip"}}
  ```

  A file's path is relative to the working directory, or absolute. Values
  that aren't strings are kept as JSON text (`2` becomes `"2"`). An output
  that isn't declared in `tool.yaml` makes the run fail. A declared output
  can be left out.
- **keeps the log**: stderr is the log, and stdout is folded into it too.
  The result carries the last 40 lines.
- **enforces the timeout** by killing the tool's whole process group,
  including uv's Python child.

The run ends with one result:

```json
{
  "ok": true,
  "tool": "materialize-tree",
  "outputs": {"root": "/tmp/dtt-run-…/tree", "file_count": "2", "dir_count": "1"},
  "files": [{"name": "archive", "path": "/tmp/dtt-run-…/tree.zip", "size": 312}],
  "log_tail": "file  README.md (7 bytes)\n…",
  "exit_code": 0,
  "workdir": "/tmp/dtt-run-…",
  "duration_ms": 412
}
```

On failure, `ok` is false and `error` says why. `timed_out` is set when the
tool ran out of time.

## The reference T3: `materialize-tree`

This is the worked example the original README planned. Its `spec` input is
a flat YAML mapping. Each key is a relative path and its value is that
file's content. A key ending in `/` is a directory. The tool builds the tree
under `root`, which defaults to `tree/` in the working directory. It refuses
to replace existing files unless `overwrite` is true, and it packs the
entries into `tree.zip`.

```sh
cat > spec.yaml <<'EOF'
spec: |
  README.md: "# hello"
  src/: ~
  src/main.py: print('hi')
EOF
dtt run materialize-tree --input spec.yaml
```

## Writing a tool

```sh
dtt create my-tool --install     # ~/.ufa/tools/my-tool: tool.yaml + main.py, ready to run
$EDITOR ~/.ufa/tools/my-tool/tool.yaml ~/.ufa/tools/my-tool/main.py
dtt validate ~/.ufa/tools/my-tool
dtt run my-tool --arg message=hello
```

Without `--install`, `create` scaffolds the tool in the current directory.
Use `dtt install <dir>` once it validates.

## Build and test

```sh
make build    # ./dtt, built-ins embedded
make test     # the materialize-tree run test is skipped without uv (and needs the network once, for pyyaml)
```

`make deploy-dev-binaries` at the repo root puts `dtt` in `/AI-evo1-dev/bin`,
where local-representative finds it.
