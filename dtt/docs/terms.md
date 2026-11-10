# DTT terms

The words dtt, its documents, its built-in tools and local-representative's
tools tab use, and what in the implementation each one is. When the code and
this page disagree, one of them is wrong.

## Tool (tool-tool-tool, T3)

A **tool** is an implementation giving an interface to one coherent system,
much as a terraform provider does. In dtt it's a directory holding
`tool.yaml` (format `dtt-v1`) and its script — a T3, a
declarative-tool-tool-tool. The built-in `github`, `filesystem`, `aws` and
`localrep` tools each give one system's objects (a GitHub account, files on
this host, an AWS account, local-representative); `declare` gives all of
them together, and `materialize-tree` and `composer` are tools of the plain,
non-declarative kind.

`dtt list` shows the tools found; the tools tab shows them under
**configure → catalog**. See the [README](../README.md#where-tools-come-from).

## Tool call

A **tool call** is everything around one running of a tool. It has three
parts:

- **definition** — the abstract arrangement of tool actions that will occur
  when it's called. A DTT document (`*.dtt.yaml`) is a definition: it names
  objects, the reads to make of them and the asserts to make true. A tool
  call example (`*.call.yaml`, a tool's inputs) is one too. Every example in
  [`examples/compositions/`](../examples/compositions) is a definition; the
  tools tab lists them under **configure → examples**. A definition names
  whatever tools it uses (the providers its statements use), so a new call
  in the tools tab starts as an empty document, not as a choice of tool.
- **specification** — completing the input a definition needs, and checking
  it, so it can execute: a document's variables (`--var`, the `variables`
  input), the mode (read, assert, delete), an earlier image (`--image`,
  `from_image`), `--force`. dtt checks the whole of it — the document, every
  variable, every input against `tool.yaml` — before anything executes; a
  call that doesn't check exits 2 having changed nothing. In the tools tab's
  **calls** sub-tab, the form is the specification, and **execute** is
  enabled once every required input is given.
- **execution** — running the call: its side effects (what an assert or
  delete changes), its reads, and the image they make. `dtt run`, `dtt
  read`, `dtt assert` and `dtt delete` execute; the calls sub-tab lists
  its recent executions, and **history** lists every one recorded on the host.

## Composition

A **composition** is a tool call looked at as an object — one that may then
be inside another tool call. A composition may be used as a tool call
definition directly, but the word implies recursive inclusion: a call made
of calls. (localrep's `compositions`, composer's documents, are the first
objects of this kind dtt reads and asserts.) A document calls another
document with a `dtt.calls` statement: the called document runs in the
caller's mode, and its image is the call object's state (see
[Calls and phases](../README.md#calls-and-phases)).

## Phase

A **phase** is a named part of a document's work. YAML document
delimiters (`---`) split a document into phases, each part between them
one (named by a `phase: <name>` in it, or `phase-<n>`); in a document
without them, `phase: <name>` on a statement starts one, and the
statements after it are in it until the next phase starts. Phases run in
document order, each beginning only once everything in the one before it
is done; one that fails stops the rest. A call is often a phase of its
own, and a **delay** (`delay: {select: 30s}`) is a wait within one, with
when it `began` and `completed`. See
[`examples/specifications/understanding-phases`](../examples/specifications/understanding-phases).

## Work execution, submission and completion

Agent work a document asks for always runs in a slopspace (the `slopspace`
provider). A **work execution** is that work in band: dispatched and waited
for, so it's done once the agent is. A **work submission** is the same
work out of band: done once submitted. A **work completion**, given a
submission's id, waits for that work to complete rather than reporting it
unfinished, often to begin a later phase, and can grab a file the agent
left in the slopspace. See
[Slopspaces and agent work](../README.md#slopspaces-and-agent-work),
[`examples/specifications/slopspace-dog-to-cat`](../examples/specifications/slopspace-dog-to-cat)
and [`examples/specifications/slopspace-cat-story`](../examples/specifications/slopspace-cat-story).

## Preferred file cache

Where a document's output files are uploaded to be picked up: a
`localrep.files` statement with `cache: preferred` (or no `cache`). So far
it is always this host's own, its local-representative's files tab, where
an upload is swept after an hour unless it's held or persisted. Both
slopspace example specifications upload the story the agent wrote there,
and `git-md-lines` uploads its count of a repository's Markdown lines.
See [The localrep tool](../README.md#the-localrep-tool).

## Git tree and file node tree

A **git tree** (`git.trees`) is a repository's files at a ref: the git
tool fetches the ref from the repository's URL into dtt's git cache and
lists what's there. It is read, never asserted. A **file node tree**
(`git.files`) is a filter of a git tree: the files matching its patterns
(`**/*.md`), each a node with its lines and bytes, nested by directory.
The github tool finds the URL (`github.repos`' `clone_url`), so the two
tools work together: GitHub knows the repository and git knows its files.
See [Git trees](../README.md#git-trees) and
[`examples/specifications/git-md-lines`](../examples/specifications/git-md-lines).

A **git commit** (`git.commits`) is the one git object asserted on a
remote. It is a branch or tag of a repository by its URL, and asserting it
pushes one commit holding a git tree's files, or a directory's, to it. Its
`tree` names what it holds, as a git tree's does, so a later statement can
filter it, commit it again elsewhere, or copy it into a slopspace
(`slopspace.files`' `copy_from`). See [Git commits](../README.md#git-commits)
and
[`examples/specifications/publish-scrubbed`](../examples/specifications/publish-scrubbed).

## Read

DTT has no state, only **reads**: timestamped records of the data that came
back from its read-only actions. A read is always the first step before any
write — an assert reads every object it selects before it changes anything,
and reads them all again afterwards.

In the image, an object's `state` is its latest read and `measured` (on an
object the call changed) its read from before; the image's `taken` is when.
dtt never keeps an object's state between calls: the next call reads again.

## Image

An **image** is the collection of reads that came back for a tool call:
format `dtt-image-v1`, every selected object's reads plus each asserted
field's outcome (`unchanged`, `changed`, `held`, `would-change`, `kept`).
A document with phases also gets an image of each phase, under `phases`:
the objects it first measured, its outcomes' summary, and whether it was
`done`, `failed` or `not-run`. Each object and outcome names its phase too.
See [The image](../README.md#the-image).

An image is printed (or returned as the `image_json` output and `image`
file) so its caller can capture it, and is also kept as a timestamped file
in the call's staging area — see [In-place and pathless calls](#in-place-and-pathless-calls).
Given back as `--image`/`from_image`, it's what the next call's from rule
compares against.

## Select

A **select** identifies the objects to put handles on: which objects under a
DTT path a statement means (`select:` under the path, or under an assert's
list, such as a PR by its branches). What's selected is what gets read
for the image, what an assert decides whether to write, and what it writes.

## Assert

An **assert** checks whether an object is in the state wanted, and puts it
in that state if it isn't (`assert:` in a statement, `dtt assert`, mode
`assert`).

- **from** — the states from which the assert is willing to make its
  changes. A field changes only when what it holds now is empty, one of its
  `from.<field>` values, what the given image saw, or anything with
  `--force`; otherwise it's **held**. See [The from rule](../README.md#the-from-rule).

Deleting (`dtt delete`) is asserting that what an assert would make doesn't
exist, by the same from rule.

## In-place and pathless calls

A tool call of a document is either **in-place** or **pathless**.

- **In-place**: a directory holding a `.dtttc` file is an in-place tool
  call. Its document is in the directory (named in `.dtttc` as
  `document: <file>`, or else the directory's only `*.dtt.yaml`), and the
  call may only take from-file content (localrep's `source_file`, say, or a
  document it calls) that is in that directory or nested under it, or that
  `.dtttc` includes — `include:`, a list of dtt's example files
  (`compositions/<file>`) the call brings in though they aren't in the
  directory. Any other path is a mistake in the document (exit 2). `dtt
  read <dir>` calls it, as does naming a document file whose directory
  holds `.dtttc`. Its images are kept in `<dir>/images/`.
  [`examples/specifications/`](../examples/specifications) holds example
  in-place calls, each with a `.gitignore` for its `images/`: one
  self-contained, one including a composition. `dtt examples
  --specifications` unpacks them under `~/.ufa/dtt/examples/` (the
  `examples-dir` setting), so they can be called on any host.
- **Pathless**: any other call — a document file elsewhere, a directory
  without `.dtttc` (its only `*.dtt.yaml`), one read from
  stdin (named `stdin`), or a built-in tool's inline `document` input (named
  `document`). It's staged under
  `~/.ufa/dtt/pathless/<file name, dots as underscores>/<sha256 of the
  document>/` (the `pathless-dir` setting moves the root), with the
  document copied in as `document.dtt.yaml`, and its images kept in
  `images/` there. The staging directory (or its `document.dtt.yaml`) may
  be called again: unchanged, that is the same pathless call, kept in the
  same place.

Either way, each execution's image comes back in a timestamped file,
`images/<UTC time>-<mode>.image.json`, as well as on stdout so the caller
can capture it; dtt names the file on stderr (`dtt: image kept at ...`).
See [`calls.go`](../calls.go).

A call's **directory** — the in-place directory, or the pathless staging
area — is what names it. Each execution puts its call at the top of the
**recent calls**, `~/.ufa/dtt/recent-calls.json` (the `recent-calls`
setting moves it): each call once, by its directory, the most recently
executed first, with its latest execution's time, mode and image. `dtt
calls` lists them; `dtt calls <dir|document>` describes one, listed or
not — its document and the images kept for it. The tools tab's **calls**
sub-tab is keyed by these directories: its left-hand bar lists the recent
calls, and one is picked from it (or an unlisted one entered, or one of the
example specifications, under **examples**) to call again; **new call**
starts an empty document, a pathless call once it's executed.

## Recording

Every execution (`dtt run`, `read`, `assert`, `delete`) is recorded through
clauditable when clauditable can be found, and runs unrecorded when it
can't (or with `record: off`, or `DTT_RECORD=off`). A recorded call goes into
the session it was made in, as any command does, and is also copied to
`<agent-records>/dtt-calls/<unix time>-<3 random [a-z0-9]>.txt`, a record
naming its session (and the session's own record of it) for linkage. Agent
invocations are copied the same way to `<agent-records>/invocations/`; plain
shell commands aren't copied. Sessions themselves live in
`<agent-records>/sessions/`. See [`record.go`](../record.go) and
clauditable's [`layout.go`](../../clauditable/layout.go).
