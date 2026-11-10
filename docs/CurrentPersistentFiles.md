# Current Persistent Files

All files and directories used by UFA and its sub-applications that store state across sessions or runs.

---

## `/host-agent-files/agent-records/`

**Owner:** clauditable, federation-command  
**Env var:** `AGENT_RECORDS_PATH` (default: `/host-agent-files/agent-records`)

Each named session gets a subdirectory:

```
agent-records/
└── <session-id>/
    ├── session.yaml          # Session name, id, created timestamp
    ├── session.jsonl         # Consolidated execution log (JSON metadata + plaintext previews)
    └── <timestamp>-raw.txt   # Full untruncated command + response (permanent, never consolidated)
```

`session.yaml` schema:
```yaml
id: 2026-09-13_14-17-00_unnamed-2026-09-13-14-17-00
name: "My best session"
created: 2026-09-13T14:17:00Z
```

`session.jsonl` format: one JSON metadata line followed by `IN>>` / `OUT>>` / `ERR>>` prefixed plaintext previews (up to 20 lines each). See [`clauditable/RECORD_SCHEMAS.md`](../clauditable/RECORD_SCHEMAS.md) for the full schema.

Session IDs default to the current date (`YYYY-MM-DD`), auto-incrementing daily. Custom IDs are set with `ufa session new` or `ufa session set`.

---

## `/host-agent-files/agent-records-archive/`

**Owner:** clauditable (`archive` subcommand), federation-command (`ufa session archive`)  
**Env var:** `AGENT_RECORDS_ARCHIVE_PATH` (default: `<AGENT_RECORDS_PATH>-archive`)

Sessions moved here by `clauditable archive` (or `ufa session archive` inside federation-command). Each archive run creates a timestamped subdirectory:

```
agent-records-archive/
└── <YYYY-MM-DD_HH-MM-SS>/
    └── <session-id>/         # Moved verbatim from agent-records
```

---

## `/host-agent-files/exchange/host-cache/`

**Owner:** local-representative  
**Flag:** `-file-cache-dir` (default: `/host-agent-files/exchange/host-cache`)

Files dropped on LR's "files" tab, either directly or relayed through
agent-coordinator's per-host files tab (see
[`docs/DistributedExchange.md`](DistributedExchange.md)). Flat directory:

```
host-cache/
├── <8-hex>_<original-filename>              # e.g. 3f9a2b1c_report.pdf
└── .manifest_<8-hex>_<original-filename>.yaml   # hidden sidecar, e.g.
                                                  # .manifest_3f9a2b1c_report.pdf.yaml
```

The 8-hex prefix makes concurrent uploads collision-free; LR strips it back
off to show the original filename. Entries are swept once they are more than
one hour old (checked every minute) — nothing here is meant to persist,
unless the file details dialog's "hold" button has extended that to 72 hours
(see below) or its "persist" button has moved the entry to the host-store.

Every upload also gets a `.manifest_<id>.yaml` sidecar (flat `key: value`
YAML — id, name, kind, size, uploaded_at, held, expires_at, creator,
highlighted) written alongside it. It's hidden from the "files" tab;
name/kind/size/uploaded_at are for an operator to read by hand, but
`held`/`expires_at`/`highlighted` ARE read back by LR — the files tab's
"hold" button rewrites the former pair (`held: true`, `expires_at` pushed
out to 72 hours from the press) so the extended TTL survives an LR restart,
since the data file's own mtime only ever reflects its original upload time.
`creator` records the identity of the local-representative that uploaded the
file (the same host/head id it uses to identify itself to agent-coordinator
— see `~/.ufa/host.yaml` below), kept for future correlation once file
exchange spans more than one host. `highlighted` is a plain operator-set
toggle rewritten by the files tab's "highlight" button — independent of
held/expires_at, it marks a file at the LR/AC level (yellow ring around its
grid box) for cross-system functionality; session-manager's "Add Resource"
menu ("Highlighted") pulls every currently-highlighted file in (the removed
condoccer v1 was the first consumer; see
[`condocs/initialDistributedDevelopmentImpls/Step5SubstepRPrompt.md`](../condocs/initialDistributedDevelopmentImpls/Step5SubstepRPrompt.md)
and [`docs/DistributedExchange.md`](DistributedExchange.md)).
Uploads whose claimed filename starts with `.manifest_` are refused; the
sweep removes a manifest alongside its data file (and cleans up an orphaned
manifest whose data file is already gone). A "persist" press moves the
manifest into the host-store along with its data file rather than dropping
it (see below).

An image file being edited in the files tab's "markup" dialog gets a second
hidden sidecar while that's in progress: `.markup_<id>.jpg`, a flat JPEG
holding the running composite (original image plus whatever arrows,
rectangles, or text have been drawn on it so far), written alongside `<id>`
wherever it currently lives. Its mere presence is what marks a file "marked
up" (bright orange border in the files tab) and lets re-opening the dialog
pick the session back up instead of starting over. The dialog's "commit"
button writes those bytes over `<id>` directly and removes the sidecar;
"copy" spins them off into a brand-new host-cache entry instead (`<id>`
untouched) and also removes the sidecar; "cancel" just removes it. Like
`.manifest_`, uploads whose claimed filename starts with `.markup_` are
refused, it's invisible to the files tab listing, and it's swept/moved/
deleted alongside its data file the same way a manifest is.

Two sub-apps also upload here like any other client: The
Conversationalist's Polly button, with its "save" box ticked, keeps each
clip as `polly-<voice>-<first words>-<stamp>.mp3`, and composer saves
each render as `composer-<composition>-<stamp>.webm` (see
[`composer/README.md`](../composer/README.md)). A clip asked for over
The Conversationalist's `POST /api/polly` with a name (dtt's `localrep`
tool does) is `polly-<name>.mp3` instead. All are swept like any
other entry unless held or persisted.

LR keeps nothing new on disk for runs started over its `/api/control/*`
API: the last 20 finished runs are held in memory only, so a dtt run key
is forgotten when LR restarts.

---

## `/host-agent-files/exchange/host-store/`

**Owner:** local-representative  
**Flag:** `-host-store-dir` (default: `/host-agent-files/exchange/host-store`)

Where the files tab's "persist" button (file details dialog, shown in place
of "hold" once a file is already held) moves a host-cache entry to. Flat
directory, same `<8-hex>_<original-filename>` naming as the host-cache —
nothing here is ever swept. Its `.manifest_<id>.yaml` sidecar moves along
with it (a persisted entry has no expiry left to track, but its `creator`
and `highlighted` fields are kept — see above). The "highlight" button still
works on a persisted entry, rewriting the manifest in place in the
host-store.

---

## `/host-agent-files/slopspaces/`

**Owner:** dungeon-keeper  
**Env var:** `SLOPSPACES_DIR` (default: `/host-agent-files/slopspaces`)

Isolated workspaces for async agent tasks. Each slopspace has a UUID directory:

```
slopspaces/
└── <uuid>/
    ├── SLOPSPACE.json         # Metadata (id, agent-type, deploy state, iteration count)
    ├── readspaces/            # Immutable context provided to the agent
    │   ├── agent-records/
    │   ├── dtt-canvas/        # DTT tool calls the human supplied (or accepted)
    │   │   ├── read/
    │   │   └── write/
    │   ├── dtt-images/        # The images those tool calls returned; read/<name>.image.json is written by dtt read when a read is accepted (or at deploy, if missing), <name>.error.txt if it failed
    │   ├── repos/             # Read-only repos: <owner>/<name> at one commit, no .git, read-only; listed (with every commit pulled) in SLOPSPACE.json's readspace_repos and exported again on each deploy and return
    │   └── files/
    ├── writespaces/           # Agent output; reflected outside after return
    │   ├── agent-records/
    │   ├── dtt-requests/      # DTT tool calls the agent proposes, reviewed by the human after return
    │   │   ├── read/
    │   │   └── write/
    │   ├── repos/
    │   └── files/
    ├── writespaces-secure/    # .git directories kept out of agent reach during deployment
    ├── readspaces-kept/       # While deployed: the human's copy of dtt-canvas and dtt-images, put back on return
    └── dtt-reviews/           # Accepted and rejected proposals, one dir per review
```

Slopspaces are created agent-type-agnostic; the agent type is bound at deploy time.

When a slopspace is returned with files in `writespaces/dtt-requests`, they are
recorded in `SLOPSPACE.json` as that deployment's proposals, and the slopspace
can't be deployed again until the human answers them: **accept** (moved onto
`readspaces/dtt-canvas`), **reject** (set aside), or **modify** (deployed again
with the human's note queued for the agent). See
`dungeon-keeper/pkg/slopspace/proposals.go`, the worker tab's slopspace
sub-tab, or `dungeon-keeper slopspace review`. condocv2 holds the condoc
until they're answered and offers the same answers on its Text tab.
Accept and reject can also be given one proposal at a time (the sub-tab's
per-proposal ✓ / ✗, or `slopspace review --proposal <path>`): each answer is
kept in the deployment's `decided` list, the rest stay pending, and once none
are left its `decision` is `accept`, `reject`, or `mixed` if they differed.

A slopspace with `dtt_requests_disabled` set in `SLOPSPACE.json`
(`Manager.SetDTTRequests`) is deployed without `writespaces/dtt-requests`, and
its returns record no proposals. condocv2 clears it; the worker tab's manual control sets it per deployment
with its "DTT proposals" checkbox.

Each deploy also writes `readspaces/DTT-GUIDE.md` into the deployed
readspaces: a short primer on DTT documents, with examples, for the agent
(`dungeon-keeper/pkg/slopspace/dttguide.go`). Agent prompts carry only a
pointer to it. It goes with the readspaces on return.

---

## `/host-agent-files/work/`

**Owner:** dungeon-keeper  
**Env var:** `WORK_SIGNALS_DIR` (default: `/host-agent-files/work`)

JSONL files describing work to be done. The watch loop moves them from `ongoing/` to `complete/` when finished.

```
work/
├── ongoing/
│   └── <name>.jsonl    # Pending or in-progress work signals
└── complete/
    └── <name>.jsonl    # Finished work signals
```

Work signal format (first line header, subsequent lines are status events):
```json
{"id":"...","work_type":"slopspace","agent_type":"agent-worker","role":"...","prompt":"...","agent":"claude","model":"opus","status":"pending","created_at":"...","updated_at":"..."}
{"event_id":"...","status_update":"processing","comment":"...","timestamp":"..."}
```

---

## `/agent/`

**Owner:** dungeon-keeper  
**Env var:** `AGENT_SLOPSPACE_ROOT` (default: `/agent`)

The live deployment point for slopspaces. At most one slopspace is deployed per agent type at a time.

```
/agent/
├── agent-worker/
│   ├── SLOPSPACE_ID    # Plain-text file containing the active slopspace UUID
│   ├── readspaces/
│   └── writespaces/
└── heuristic-request/
    ├── SLOPSPACE_ID
    ├── readspaces/
    └── writespaces/
```

---

## `~/.federation_records`

**Owner:** federation-command

Plain-text shell history file, one command per line. Loaded on startup and appended to on every command entry. No env var override; path is always `$HOME/.federation_records`.

---

## `~/.ufa/config/`

**Owner:** ufa-configurable (used by federation-command, local-representative, and any future sub-app that imports it)  
**Env var:** `UFA_CONFIG_DIR` (default: `~/.ufa/config`); overridable per-launch with `--config <dir>`

```
~/.ufa/config/
├── global.yaml                  # Shared defaults for every sub-application
├── federation-command.yaml      # federation-command overrides
└── local-representative.yaml    # local-representative overrides
```

Flat `key: value` YAML. Precedence (highest first): CLI flag → app-specific file → global.yaml → built-in default. See [`ufa-configurable/README.md`](../ufa-configurable/README.md) for the full loader spec.

---

## `~/.ufa/host.yaml`

**Owner:** ufa-hostid (used by clauditable, local-representative, and any future sub-app that imports it)

```yaml
id: myhostname-a3b9
first_configured: 2026-09-13T14:00:00Z
```

Stable per-host identifier in the form `<hostname>-<4-char-random-alphanumeric>`. Created automatically on first use by `ufahostid.GetHostID()`. The `id` field is used as the `host` attribute in `session.jsonl` events and as the default `--name` for local-representative. If the file is absent it is created; if it is inaccessible, `hostname` is used as a fallback.

---

## `~/.ufa/composer/`

**Owner:** composer  
**Flag:** `--data-dir` (default: `~/.ufa/composer`)

```
~/.ufa/composer/
└── compositions/
    └── <id>.json    # one saved composition, e.g. hello-world.json
```

Each file is one composition: its name, size, video and audio tracks, and
the renders made from it so far (file id and name in local-representative's
files area -- the render itself lives there, not here). Written atomically
(temp file, then rename) on every save and render; deleting a composition
in the UI removes its file. See [`composer/README.md`](../composer/README.md).

---

## `~/.ufa/condocv2/asides/`

**Owner:** condocv2  
**Flag:** `--asides-dir` (default: `~/.ufa/condocv2/asides`; blank turns asides off)

```
~/.ufa/condocv2/asides/
└── <pointer stem>/      # <Name>-<startTime>, the condoc the asides belong to
    └── aside-<A>.json   # one aside: question, answer, scope, times, agent
```

The condoc's asides, the read-only questions to its agent that are kept off
the condoc. They live only on this host and never reach a repo. See
[`condocv2/README.md`](../condocv2/README.md#asides).

---

## `~/.ufa/convo-polly.json`

**Owner:** the-conversationalist  
**Flag:** `--polly-config-file` (default: `~/.ufa/convo-polly.json`; empty keeps it in memory only)

```json
{
  "active": "neural",
  "configs": [
    {"name": "british", "voice": "Emma", "engine": "neural", "region": "us-east-1", "preset": true, "edited": true},
    {"name": "slow-kendra", "voice": "Kendra", "engine": "neural", "region": "eu-west-1", "description": "mine"}
  ]
}
```

A configuration's `region` is the AWS region Polly is asked in; without
one, the server's own AWS region is used. An edited preset saved before
presets had regions has none, so reset it in the menu to pick up its
`us-east-1`.

The Polly configuration menu's state (the gear beside the notepad's Polly
button; see `the-conversationalist/pollyconfig.go`). It holds the active
configuration, the configurations added in the menu and the presets edited
there. Unedited presets aren't written, so their built-in values (and the
default preset's `--polly-voice`/`--polly-engine`) always apply. The file is
rewritten on every change and read at startup. The menu's recent clips
aren't kept here: they're held in memory only, so a restart forgets them.

---

## `~/.ufa/convo-settings.json`

**Owner:** the-conversationalist  
**Flag:** `--settings-file` (default: `~/.ufa/convo-settings.json`; empty keeps it in memory only)

```json
{
  "announce_start": true
}
```

The sidebar's **announce start** toggle (see
`the-conversationalist/announce.go`). It's off by default. While it's on, TC
reports its start time, and its own page, LR's and AC's say "application
running" when it starts or restarts. It's kept here so the setting survives
the restarts it announces. The file is rewritten whenever the toggle changes
and read at startup.

---

## `~/.claude/`

**Owner:** Claude Code (used by developers working in this repo)

Claude Code's per-user state directory. Not part of UFA's runtime, but relevant to any agent session running inside this devcontainer.

```
~/.claude/
├── settings.json                               # User-level Claude Code settings and permissions
└── projects/<project-hash>/
    └── memory/
        ├── MEMORY.md                           # Memory index (loaded into every conversation)
        └── <topic>.md                          # Individual memory files (user, feedback, project, reference)
```

The `<project-hash>` is a slug derived from the project working directory path (e.g., `-workspaces-research-AI-evo1`).

---

## `claudomation/examples/execution/terraform.tfstate`

**Owner:** claudomation (Terraform)

Terraform state for the `execution` example module. Tracks provisioned cloud infrastructure. Also has a `.backup` copy of the previous apply.

```
claudomation/examples/execution/
├── terraform.tfstate         # Current infrastructure state
├── terraform.tfstate.backup  # Previous state (written before each apply)
└── .terraform.lock.hcl       # Provider version lock file (checked into git)
```
