# dtt Tour

dtt, the declarative tool tool, creates, checks and runs **T3s**: tools
described by a small `tool.yaml` (format `dtt-v1`) and written in Python or
Go. It's a transient CLI. Each call does one thing and exits, and with
`--json` it prints one JSON document. local-representative calls it for its
tools tab and its `tool.<name>` control ops.

This tour can be run as a ridealong from federation-command:
```
ridealong dtt/docs/brief-tour.md
```

The tour uses its own config directory under `/tmp/dtt-tour`, so it doesn't
touch `~/.ufa/tools`. Every step except the `materialize-tree` run works
without `uv`. The tour's own tool is written in Go, so it uses the go
toolchain that building dtt already needs.

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
built-in tools show up: the reference tool `materialize-tree`, the three
declarative tools covered later in the tour, and `composer`, covered at the
end.

```ridealong
./dtt --config /tmp/dtt-tour/config list
```

Output (the summary, which is the tool's whole description, is shortened
here and below):
```
aws               builtin  Read or assert AWS objects -- ECS tasks, selected by their tags, ...
composer          builtin  Save a composition in composer -- title cards, images and videos ...
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

Tools are written in Python or Go. `create` scaffolds either, ready to
validate and run. `--runtime go` gives a Go main package that uses only the
standard library, so this part of the tour doesn't need `uv`. The default,
`--runtime python`, gives a Python script instead.

```ridealong
./dtt create tour-echo --dir /tmp/dtt-tour --runtime go
ls /tmp/dtt-tour/tour-echo
```

Output:
```
created tour-echo in /tmp/dtt-tour/tour-echo -- edit tool.yaml and main.go, then dtt validate /tmp/dtt-tour/tour-echo
main.go  tool.yaml
```

The schema declares one required `message` input, a `length` output value
and a `report` output file:

```ridealong
cat /tmp/dtt-tour/tour-echo/tool.yaml
```

The program reads its inputs as JSON on stdin (they also arrive as
`DTT_IN_<NAME>` environment variables) and writes its results to
`$DTT_OUTPUT`:

```ridealong
cat /tmp/dtt-tour/tour-echo/main.go
```

### Validate and Install

```ridealong
./dtt validate /tmp/dtt-tour/tour-echo
```

Output:
```
tour-echo is a valid dtt-v1 tool (1 input(s), 1 output value(s), 1 output file(s), runtime go)
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
composer          builtin  Save a composition in composer -- title cards, images and videos ...
filesystem        builtin  Read or assert files on this host -- JSON or text items ...
github            builtin  Read or assert GitHub objects -- a repository and its open pull requests ...
materialize-tree  builtin  Build a directory tree from a flat YAML description ...
tour-echo         user     What tour-echo does, in a sentence or two. ...
```

## Running a Tool

`run` checks the inputs against the schema, creates a working directory,
builds the go tool into it, runs it there and collects what it declared. The
first build takes a few seconds; Go's build cache makes later ones quick:

```ridealong
./dtt --config /tmp/dtt-tour/config run tour-echo --arg message=hello --workdir /tmp/dtt-tour/run1
cat /tmp/dtt-tour/run1/report.txt
```

Output:
```
tour-echo: ok in 1.2s
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
mapping of path to content, and packs it as `tree.zip`. It's a python tool,
and uv fetches its `pyyaml` the first time it runs, so this step is skipped
when `uv` isn't installed:

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

dtt also reads, asserts and deletes **DTT documents**. A document names objects (a
GitHub repository's pull requests, files under a directory, ECS tasks) and
the state they should have. Reading measures them and returns an
**image**, the measured state as JSON. Asserting makes the document true
where the **from rule** allows and also returns an image. The rule: a
field changes only from empty, from a value listed under `from.<field>`,
from what a supplied image saw, or, with force, from anything. The
built-in `github`, `filesystem`, `aws` and `localrep` tools run the same
engine through `dtt run`.

The files example writes a JSON file into `/tmp/`. Point a copy of it at
the tour's directory instead:

```ridealong
cat examples/compositions/filesystem-json.dtt.yaml
mkdir -p /tmp/dtt-tour/files
sed 's|"/tmp/"|/tmp/dtt-tour/files/|' examples/compositions/filesystem-json.dtt.yaml > /tmp/dtt-tour/files.dtt.yaml
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
  image_json = {"ok":true,"format":"dtt-image-v1","mode":"assert",…}
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

`image_json` above is the image itself, returned as an output value. In
local-representative's tools tab, a run's image fills its calls form's
`from_image` box when the run finishes, ready for the next assert, and
**clear** empties it. The image holds every field of every selected object
(the directory's `mode`, `owner`, `group` and `items` included), measured
again after the asserts; an object the run changed also keeps `measured`,
its state before.

### Mode, Owner and Group

The text-template example writes under `/etc/ufa/`, which needs root. Point
it at the tour's directory instead. `motd.txt` asserts its `mode` beside
its content, and `old-settings.ini` is to be removed:

```ridealong
sed 's|/etc/ufa/|/tmp/dtt-tour/etc/|' examples/compositions/filesystem-text-template.dtt.yaml > /tmp/dtt-tour/etc.dtt.yaml
mkdir -p /tmp/dtt-tour/etc && echo 'x=1' > /tmp/dtt-tour/etc/old-settings.ini
./dtt --config /tmp/dtt-tour/config assert /tmp/dtt-tour/etc.dtt.yaml > /tmp/dtt-tour/etc-image.json
./dtt --config /tmp/dtt-tour/config assert /tmp/dtt-tour/etc.dtt.yaml --image /tmp/dtt-tour/etc-image.json | grep -A3 '"summary"'
ls -l /tmp/dtt-tour/etc
```

The first assert creates `motd.txt` (a new file had no mode, so its mode
applies) and holds the removal. The second, given the first's image, sees
`old-settings.ini` still as that image saw it, so removes it.

### Deleting

`dtt delete` (the tools' `mode=delete`) removes what asserting the same
document would make, here the JSON file. Removing it goes by the from rule,
so a plain delete is held. A delete given the image an assert returned
removes it:

```ridealong
./dtt --config /tmp/dtt-tour/config assert /tmp/dtt-tour/files.dtt.yaml > /tmp/dtt-tour/image2.json
./dtt --config /tmp/dtt-tour/config delete /tmp/dtt-tour/files.dtt.yaml | grep -A1 '"summary"'
./dtt --config /tmp/dtt-tour/config delete /tmp/dtt-tour/files.dtt.yaml --image /tmp/dtt-tour/image2.json | grep -A1 '"summary"'
ls /tmp/dtt-tour/files
```

Output:
```
  "summary": {
    "held": 1
  "summary": {
    "changed": 1
```

The directory is empty again. The selected directory itself is only
measured, so it stays.

### GitHub and AWS

The GitHub and AWS examples act on real accounts, so this tour only
**reads** them, and only when credentials are there. A read makes no
changes. It reports what an assert would do:

```ridealong
cat examples/compositions/github-prs.dtt.yaml
if [ -n "${GITHUB_TOKEN:-}${GH_TOKEN:-}" ] || gh auth status >/dev/null 2>&1; then
  ./dtt --config /tmp/dtt-tour/config read examples/compositions/github-prs.dtt.yaml | grep -A4 '"summary"'
else
  echo "no GitHub token - skipping the github read"
fi
```

`examples/compositions/github-condoc-labels.dtt.yaml` selects PRs by a pattern
(`condoc/*/main`) and adds labels and a reviewer member by member; a read
of it lists what an assert would add to each matching PR, and matching
none is fine.

`examples/compositions/github-issue.dtt.yaml` opens a dated issue on a repository,
labelled and assigned, and finds it again by its title; delete mode closes
it.

Two examples create things. `examples/compositions/github-repo-dated.dtt.yaml` makes
today's `scratch-<date>` repository, and `examples/compositions/aws-vpc-sandbox.dtt.yaml`
makes a VPC with one subnet. A read of either only reports that it would
make them. If you assert one, delete it afterwards with the image the assert
returned, e.g. `dtt delete examples/compositions/github-repo-dated.dtt.yaml --image
made.json`. The repository example carries the `approve_delete` that deleting
a repository needs, and with the image its timestamp is the day the image
saw.

```ridealong
cat examples/compositions/aws-ecs-tags.dtt.yaml
if python3 -c 'import boto3; boto3.client("sts").get_caller_identity()' >/dev/null 2>&1; then
  ./dtt --config /tmp/dtt-tour/config run aws --arg document_file=examples/compositions/aws-ecs-tags.dtt.yaml
else
  echo "no boto3 with AWS credentials - skipping the aws read"
fi
```

### Built-in Examples

These examples are also built into the binary. `dtt examples` lists each
one with the tool that runs it. local-representative's tools tab uses the
JSON form for its **configure → examples** sub-tab, where **import** loads
an example into **calls** as an instance of that tool, ready to execute:

```ridealong
./dtt --config /tmp/dtt-tour/config examples
./dtt --config /tmp/dtt-tour/config examples --json | grep -E '"(name|kind|tool|title)"'
```

### Variables and localrep (optional)

A document can take **variables**, as a terraform module does: a
`variables` statement declares them with defaults, `--var name=value` (or a
built-in tool's `variables` input, `name=value` on each line) sets them,
and `${{var.<name>}}` uses them. `examples/compositions/localrep-robot-hello.dtt.yaml`
takes `node` and `take`. It imports a control sequence from
`examples/compositions/resources/`, runs it on that node while its screen records, has
Polly narrate, and composes the lot into a WebM (see the README's "The
localrep tool"). Asserting it needs local-representative, the robot, The
Conversationalist and composer, so the tour only **reads** it, and only
when local-representative and composer answer. Skip this if they're not
running:

```ridealong
if curl -fs http://localhost:8081/compose/api/status >/dev/null; then
  ./dtt --config /tmp/dtt-tour/config read examples/compositions/localrep-robot-hello.dtt.yaml --var node= --var take=tour | grep -A6 '"summary"'
else
  echo "local-representative or composer is not answering on :8081 - skipping the localrep read"
fi
```

The read lists what an assert would do: import the sequence, start the run,
make the clip, save and render the composition. A variable the document
doesn't declare stops the run before anything is measured:

```ridealong
./dtt --config /tmp/dtt-tour/config read examples/compositions/localrep-robot-hello.dtt.yaml --var nod=x; echo "exit $?"
```

Output (approximately):
```
dtt: variable nod was given, but the document only declares node, take
exit 2
```

## A Go Tool: composer

The built-in `composer` tool is written in Go, so dtt builds it before it
runs. It drives [composer](../../composer/README.md), the sub-app behind the
**compose** tab: it saves a composition and renders it into a WebM in
local-representative's files area. Its example, `composer-hello`, is a
**call** example rather than a document. It holds the tool's inputs, which
`--input` reads as they are:

```ridealong
./dtt --config /tmp/dtt-tour/config examples composer-hello
```

The run needs composer to be up and connected to local-representative. With
ffmpeg installed, composer renders on its own; otherwise an open compose tab
renders it, so this step uses a short `wait` and is skipped when composer
isn't answering:

```ridealong
if curl -fs http://localhost:8089/api/status >/dev/null; then
  ./dtt --config /tmp/dtt-tour/config run composer --input examples/compositions/composer-hello.call.yaml --arg wait=60 --workdir /tmp/dtt-tour/compose-run
else
  echo "composer is not running on :8089 - skipping the composer run"
fi
```

Output (with composer and ffmpeg):
```
composer: ok in 9.8s
  composition_id = dtt-hello
  composition_json = {"id":"dtt-hello","name":"Hello from dtt",…}
  created = true
  file_id = …_composer-dtt-hello-2026-10-25T15-04-05.webm
  file_name = composer-dtt-hello-2026-10-25T15-04-05.webm
  rendered_by = ffmpeg
  size = 81234
  workdir: /tmp/dtt-tour/compose-run
```

The file is now in local-representative's files tab, and in the `dtt-hello`
composition's renders in the compose tab. The tools tab's **examples**
sub-tab can import `composer-hello` too, and runs it the same way.

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
