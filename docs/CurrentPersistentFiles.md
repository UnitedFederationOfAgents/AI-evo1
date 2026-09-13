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
    │   ├── dtt-images/
    │   ├── repos/
    │   └── files/
    ├── writespaces/           # Agent output; reflected outside after return
    │   ├── agent-records/
    │   ├── dtt-canvas/
    │   ├── repos/
    │   └── files/
    └── writespaces-secure/    # .git directories kept out of agent reach during deployment
```

Slopspaces are created agent-type-agnostic; the agent type is bound at deploy time.

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
