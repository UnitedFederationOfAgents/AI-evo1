# dtt Tour

dtt, the declarative tool tool, creates, checks and runs **T3s**: tools
described by a small `tool.yaml` (format `dtt-v1`) and written in any
language. It's a transient CLI. Each call does one thing and exits, and with
`--json` it prints one JSON document. local-representative calls it for its
tools tab and its `tool.<name>` control ops.

This tour can be run as a ridealong from federation-command:
```
ridealong dtt/docs/brief-tour.md
```

The tour uses its own config directory under `/tmp/dtt-tour`, so it doesn't
touch `~/.ufa/tools`. Every step except the `materialize-tree` run works
without `uv`.

## Setup

Navigate to the dtt directory and build it. The built-in tools are embedded
in the binary:

```ridealong
cd dtt
make build
```

Point a tour-only config at a scratch tools directory:

```ridealong
rm -rf /tmp/dtt-tour
mkdir -p /tmp/dtt-tour/config /tmp/dtt-tour/tools
echo "tools-dir: /tmp/dtt-tour/tools" > /tmp/dtt-tour/config/dtt.yaml
```

## Finding Tools

dtt keeps no index. Every call scans the built-in tools, then the user tools
directory, then any `tools-dirs`. With an empty tools directory, only the
built-in tools show up: the reference tool `materialize-tree` and the three
declarative tools covered at the end of the tour.

```ridealong
./dtt --config /tmp/dtt-tour/config list
```

Output (the summary, which is the tool's whole description, is shortened
here and below):
```
aws               builtin  Read or assert AWS objects -- ECS tasks, selected by their tags, ...
filesystem        builtin  Read or assert files on this host -- JSON or text items ...
github            builtin  Read or assert GitHub objects -- a repository and its open pull requests ...
materialize-tree  builtin  Build a directory tree from a flat YAML description ...
```

`describe` prints a tool's schema. A tool can also be named by its control
op, `tool.<name>`:

```ridealong
./dtt --config /tmp/dtt-tour/config describe tool.materialize-tree
```

The `--json` form of `list` is what local-representative reads. Each tool
entry has the `ControlOpSpec` shape (`op`, `summary`, `describe`, `args`):

```ridealong
./dtt --config /tmp/dtt-tour/config list --json | head -20
```

## Writing a Tool

`create` scaffolds a tool that validates and runs as it stands. The `exec`
runtime gives a shell script, so this part of the tour doesn't need `uv`. The
default, `--runtime uv`, gives a Python script instead.

```ridealong
./dtt create tour-echo --dir /tmp/dtt-tour --runtime exec
ls /tmp/dtt-tour/tour-echo
```

Output:
```
created tour-echo in /tmp/dtt-tour/tour-echo -- edit tool.yaml and run.sh, then dtt validate /tmp/dtt-tour/tour-echo
run.sh  tool.yaml
```

The schema declares one required `message` input, a `length` output value
and a `report` output file:

```ridealong
cat /tmp/dtt-tour/tour-echo/tool.yaml
```

The script reads its inputs from `DTT_IN_<NAME>` environment variables (they
also arrive as JSON on stdin) and writes its results to `$DTT_OUTPUT`:

```ridealong
cat /tmp/dtt-tour/tour-echo/run.sh
```

### Validate and Install

```ridealong
./dtt validate /tmp/dtt-tour/tour-echo
```

Output:
```
tour-echo is a valid dtt-v1 tool (1 input(s), 1 output value(s), 1 output file(s), runtime exec)
```

`install` validates the tool again and copies it into the tools directory:

```ridealong
./dtt --config /tmp/dtt-tour/config install /tmp/dtt-tour/tour-echo
./dtt --config /tmp/dtt-tour/config list
```

Output:
```
installed tour-echo at /tmp/dtt-tour/tools/tour-echo
aws               builtin  Read or assert AWS objects -- ECS tasks, selected by their tags, ...
filesystem        builtin  Read or assert files on this host -- JSON or text items ...
github            builtin  Read or assert GitHub objects -- a repository and its open pull requests ...
materialize-tree  builtin  Build a directory tree from a flat YAML description ...
tour-echo         user     What tour-echo does, in a sentence or two. ...
```

## Running a Tool

`run` checks the inputs against the schema, creates a working directory,
runs the script there and collects what it declared:

```ridealong
./dtt --config /tmp/dtt-tour/config run tour-echo --arg message=hello --workdir /tmp/dtt-tour/run1
cat /tmp/dtt-tour/run1/report.txt
```

Output:
```
tour-echo: ok in 4ms
  length = 5
  file report: /tmp/dtt-tour/run1/report.txt (6 bytes)
  workdir: /tmp/dtt-tour/run1
hello
```

With `--json`, the result is the document local-representative consumes:

```ridealong
./dtt --config /tmp/dtt-tour/config run tool.tour-echo --arg message="hello again" --workdir /tmp/dtt-tour/run2 --json
```

### Exit Codes

Inputs that don't fit the schema are rejected before anything runs, with
exit status 2. Exit status 1 means the tool itself failed.

```ridealong
./dtt --config /tmp/dtt-tour/config run tour-echo; echo "exit status: $?"
./dtt --config /tmp/dtt-tour/config run tour-echo --arg message=hi --arg colour=red; echo "exit status: $?"
```

## The Reference Tool: materialize-tree

The built-in `materialize-tree` builds a directory tree from a flat YAML
mapping of path to content, and packs it as `tree.zip`. It uses the `uv`
runtime, which fetches `pyyaml` the first time it runs, so this step is
skipped when `uv` isn't installed:

```ridealong
cat > /tmp/dtt-tour/spec.yaml <<'EOF'
spec: |
  README.md: "# hello"
  src/: ~
  src/main.py: print('hi')
EOF
if command -v uv >/dev/null; then
  ./dtt --config /tmp/dtt-tour/config run materialize-tree --input /tmp/dtt-tour/spec.yaml --workdir /tmp/dtt-tour/tree-run
  find /tmp/dtt-tour/tree-run/tree
else
  echo "uv is not installed - skipping the materialize-tree run"
fi
```

Output (with uv):
```
materialize-tree: ok in 412ms
  dir_count = 1
  file_count = 2
  root = /tmp/dtt-tour/tree-run/tree
  file archive: /tmp/dtt-tour/tree-run/tree.zip (312 bytes)
  workdir: /tmp/dtt-tour/tree-run
/tmp/dtt-tour/tree-run/tree
/tmp/dtt-tour/tree-run/tree/README.md
/tmp/dtt-tour/tree-run/tree/src
/tmp/dtt-tour/tree-run/tree/src/main.py
```

## Declarative Tools: Read and Assert

dtt also reads and asserts **DTT documents**. A document names objects (a
GitHub repository's pull requests, files under a directory, ECS tasks) and
the state they should have. Reading measures them and returns an
**image**, the measured state as JSON. Asserting makes the document true
where the **from rule** allows and also returns an image. The rule: a
field changes only from empty, from a value listed under `from.<field>`,
from what a supplied image saw, or, with force, from anything. The
built-in `github`, `filesystem` and `aws` tools run the same engine through
`dtt run`.

The files example writes a JSON file into `/tmp/`. Point a copy of it at
the tour's directory instead:

```ridealong
cat examples/filesystem-json.dtt.yaml
mkdir -p /tmp/dtt-tour/files
sed 's|"/tmp/"|/tmp/dtt-tour/files/|' examples/filesystem-json.dtt.yaml > /tmp/dtt-tour/files.dtt.yaml
```

### Reading

`dtt read` prints the image on stdout and logs what each asserted field
would do on stderr. Nothing is written yet:

```ridealong
./dtt --config /tmp/dtt-tour/config read /tmp/dtt-tour/files.dtt.yaml > /tmp/dtt-tour/image0.json
ls /tmp/dtt-tour/files
```

Both of the item's fields log as `would-change`, because the file doesn't
exist yet and empty is always a from value. The directory is still empty.

### Asserting Through the Tools Interface

The same document goes through `dtt run filesystem`, the way
local-representative's `tool.filesystem` op runs it:

```ridealong
./dtt --config /tmp/dtt-tour/config run filesystem --arg document_file=/tmp/dtt-tour/files.dtt.yaml --arg mode=assert --workdir /tmp/dtt-tour/assert1
cat /tmp/dtt-tour/files/my-json.json
```

Output (approximately):
```
filesystem: ok in 6ms
  changed = 2
  held = 0
  objects = 2
  unchanged = 0
  would_change = 0
  file image: /tmp/dtt-tour/assert1/image.json (… bytes)
  workdir: /tmp/dtt-tour/assert1
{
  "name": "My Json File.",
  "contents1": "stuff",
  "contents2": {
    "nested": {
      "nested": "stuff"
    },
    "flatter": "stuff"
  }
}
```

### Held Fields, Images and Force

Change the file behind dtt's back. Its content is no longer empty and isn't
a from value, so a plain assert holds it:

```ridealong
echo '{"name": "theirs"}' > /tmp/dtt-tour/files/my-json.json
./dtt --config /tmp/dtt-tour/config run filesystem --arg document_file=/tmp/dtt-tour/files.dtt.yaml --arg mode=assert | grep -E '^  (changed|held) '
```

Output:
```
  changed = 0
  held = 1
```

Read again to see the change, then assert with that image. The content is
still as the image saw it, so the field may change:

```ridealong
./dtt --config /tmp/dtt-tour/config read /tmp/dtt-tour/files.dtt.yaml > /tmp/dtt-tour/image1.json
./dtt --config /tmp/dtt-tour/config assert /tmp/dtt-tour/files.dtt.yaml --image /tmp/dtt-tour/image1.json | grep -A1 '"summary"'
```

Output:
```
  "summary": {
    "changed": 1
```

`--force` (the tools' `force=true`) changes asserted fields whatever they
hold. It is the same as `from.<field>: "*"` on every field.

### GitHub and AWS

The GitHub and AWS examples act on real accounts, so this tour only
**reads** them, and only when credentials are there. A read makes no
changes. It reports what an assert would do:

```ridealong
cat examples/github-prs.dtt.yaml
if [ -n "${GITHUB_TOKEN:-}${GH_TOKEN:-}" ] || gh auth status >/dev/null 2>&1; then
  ./dtt --config /tmp/dtt-tour/config read examples/github-prs.dtt.yaml | grep -A4 '"summary"'
else
  echo "no GitHub token - skipping the github read"
fi
```

```ridealong
cat examples/aws-ecs-tags.dtt.yaml
if command -v aws >/dev/null && aws sts get-caller-identity >/dev/null 2>&1; then
  ./dtt --config /tmp/dtt-tour/config run aws --arg document_file=examples/aws-ecs-tags.dtt.yaml
else
  echo "no AWS CLI credentials - skipping the aws read"
fi
```

## Cleanup

```ridealong
rm -rf /tmp/dtt-tour
```

## Testing

```ridealong
make test
```

## Back to Main Tour

See [../../docs/tours/brief-tour.md](../../docs/tours/brief-tour.md) for the integration tour.
