# dungeon-keeper Tour

dungeon-keeper manages asynchronous AI agent invocations through slopspaces and work signals. It is the spiritual successor to the legacy `agent-worker` and `heuristic-request` implementations.

This tour can be run as a ridealong from federation-command:
```
ridealong dungeon-keeper/docs/brief-tour.md
```

## Core Concepts

### Slopspaces
Slopspaces are isolated workspaces that contain readspaces (immutable context) and writespaces (agent output). Key design decision: **slopspaces are not tied to an agent type at creation time** - the agent type is specified during deployment.

### Work Signals
Work signals are JSONL files that describe work to be done. They contain the agent configuration, prompt, and status tracking. Work signals are created in `/host-agent-files/work/ongoing/` and moved to `/host-agent-files/work/complete/` when finished.

## Setup

Navigate to the dungeon-keeper directory:

```ridealong
cd dungeon-keeper
```

### Building

```ridealong
make build
```

### Local Development Setup

Deploy the required dependencies (ambiguous-agent, clauditable, clod) locally:

```ridealong
make deploy-dependencies-local
```

## Slopspace Management

### Create a Slopspace

The `export` builtin persists variables in the federation-command environment,
so `$SLOP_ID` will be available in all subsequent ridealong steps.

```ridealong
export SLOP_ID=$(./dungeon-keeper slopspace create | awk '/Created slopspace:/ {print $3; exit}')
echo "Created slopspace: $SLOP_ID"
```

Output:
```
Created slopspace: fbf1df64-6f49-4914-8d55-6ade0c6c64b9
  Path: /host-agent-files/slopspaces/fbf1df64-6f49-4914-8d55-6ade0c6c64b9
  Agent type will be specified at deploy time
```

### List Slopspaces

```ridealong
./dungeon-keeper slopspace list
```

Output:
```
ID                                    DEPLOYED AGENT      DEPLOYED  ITER
------------------------------------------------------------------------
fbf1df64-6f49-4914-8d55-6ade0c6c64b9  agent-worker        yes       1
```

### Running the Watch Loop

**Note:** The watch loop runs continuously. For this ridealong, run the watch command in a separate terminal:

```bash
# Run in a separate terminal (not part of ridealong):
# Agent-worker mode (default)
./dungeon-keeper watch --agent-type agent-worker

# Heuristic-request mode
./dungeon-keeper watch --agent-type heuristic-request
```

## Work Signal Format

Work signals follow this JSONL format (first line is the header):

```json
{
  "id": "unique-signal-id",
  "work_location": "",
  "work_type": "slopspace",
  "agent_type": "agent-worker",
  "role": "code-implementer",
  "prompt": "Implement the feature described in FEATURE.md",
  "agent": "claude",
  "model": "opus",
  "holder": "",
  "status": "pending",
  "created_at": "2026-05-02T19:29:38Z",
  "updated_at": "2026-05-02T19:29:38Z"
}
```

`agent` is an ambiguous-agent name. `pi` is supported (hello-pi invocation: `pi --print --approve --no-session`). Use a `provider/id` model such as `anthropic/sonnet`, or a bare pattern (`sonnet`, `gemini`, `grok`) when that provider's key is exported. A slopspace `command` may set the same `agent` and `model` fields.

Subsequent lines are events:

```json
{"event_id": "...", "status_update": "processing", "comment": "Starting work", "timestamp": "..."}
```

### Stopping a run

Every path ends with the signal in `complete/`, since that file is all a submitter (condocv2, say) waits on:

- **Cancel.** Append `{"status_update": "cancel_requested", "comment": "<reason>"}` (`worksignal.RequestCancel`). The worker checks the running signal every 2 seconds. When it finds the event, it sends the agent's process group SIGTERM, then SIGKILL 10 seconds later, and fails the signal with the reason. If the signal was cancelled before the worker picked it up, it's failed without running.
- **Timeout.** An optional `timeout_seconds` in the header is a hard ceiling on the agent run. When it passes, the agent is stopped the same way.
- **Shutdown.** On SIGHUP (restart), SIGINT or SIGTERM, the worker stops the running agent and fails its signal before exiting. The agent runs in its own process group, so it doesn't get the terminal's Ctrl-C directly.
- **Crash.** Each worker holds an flock on `work/workers/<worker-id>.lock` for as long as its process is alive. On start, a worker fails any signal of its agent type that is still `processing` under a holder whose lock nobody holds ("worker … stopped mid-run"). An agent that outlived its crashed worker isn't killed; its process group isn't this worker's.

## Example: Slopspace Lifecycle (Ridealong)

This example demonstrates slopspace management without the watch loop. For full end-to-end testing with work signals, first start the watch loop in a separate terminal:

```bash
# Run in a separate terminal (not part of ridealong):
./dungeon-keeper watch --agent-type agent-worker
```

Then run this ridealong to manage the slopspace created above (`$SLOP_ID` is
already set from the "Create a Slopspace" step).

Add a file to its write-space and deploy it to the agent-worker location:

```ridealong
mkdir -p "/host-agent-files/slopspaces/$SLOP_ID/writespaces/files"
echo "TODO: implement feature X" > "/host-agent-files/slopspaces/$SLOP_ID/writespaces/files/CAT-TASK.txt"
./dungeon-keeper slopspace deploy "$SLOP_ID" --agent-type agent-worker
```

Verify deployment (files moved to /agent/agent-worker/):

```ridealong
ls /agent/agent-worker/writespaces/files/
```

Create a work signal targeting the slopspace (the watch loop will pick this up):

```ridealong
TS=$(date +%s)
cat > /host-agent-files/work/ongoing/WORKING-slop-example-$TS.jsonl << EOF
{"id":"slop-example-${TS}","work_type":"slopspace","agent_type":"agent-worker","role":"task","prompt":"Our nice agent should modify the file writespaces/files/CAT-TASK.txt","agent":"clod","model":"sonnet","status":"pending","created_at":"$(date -u +%Y-%m-%dT%H:%M:%SZ)","updated_at":"$(date -u +%Y-%m-%dT%H:%M:%SZ)"}
EOF
```

Wait briefly for the watch loop to process, then return the slopspace if the watch loop hasn't already done so:

```ridealong
sleep 15 && (./dungeon-keeper slopspace return "$SLOP_ID" || echo "Slopspace already returned by watch loop - OK")
```

Check results in the slopspace:

```ridealong
cat "/host-agent-files/slopspaces/$SLOP_ID/writespaces/files/DONE.txt" 2>/dev/null || echo "DONE.txt not created (watch loop may not be running)"
```

Clean up the slopspace:

```ridealong
./dungeon-keeper slopspace delete "$SLOP_ID"
```

Key points:
- **Create** establishes empty read/write spaces (no agent-type binding)
- **Populate** adds context files before deployment
- **Deploy** moves spaces to `/agent/<agent-type>/` for agent access
- **Work** happens via work signals; the agent sees files in its deploy path
- **Return** moves writespaces back; readspaces are discarded and recreated (the DTT canvas and images are kept)
- **Read-only repos** (`slopspace add-readspace repo`) are registered in `SLOPSPACE.json` and exported, without `.git` and read-only, at their ref's latest commit on every deploy and every return, so the agent's changes to them are discarded; each new commit is recorded in the repo's `resolutions` (`pkg/slopspace/readrepos.go`)
- **Review** answers any DTT proposals the agent left in `writespaces/dtt-requests` (`slopspace review <id> --decision accept|reject|modify`, with `--proposal <path>` to accept or reject just one); until then the slopspace can't be deployed again
- **Delete** removes the slopspace after completion

## Directory Structure

```
/host-agent-files/
├── slopspaces/
│   └── <slopspace-id>/
│       ├── readspaces/       # Immutable from agent perspective
│       │   ├── agent-records/
│       │   ├── dtt-canvas/   # read/ and write/: DTT calls the human supplied
│       │   ├── dtt-images/
│       │   ├── repos/
│       │   └── files/
│       ├── writespaces/      # Changes reflected outside
│       │   ├── agent-records/
│       │   ├── dtt-requests/ # read/ and write/: DTT calls the agent proposes
│       │   ├── repos/
│       │   └── files/
│       └── SLOPSPACE.json     # Metadata
├── work/
│   ├── ongoing/               # In-progress work signals
│   └── complete/              # Completed work signals
└── agent-records/             # Execution records

/agent/
├── agent-worker/              # Deployed agent-worker slopspace
│   ├── SLOPSPACE_ID           # Marker file with slopspace ID
│   ├── readspaces/
│   └── writespaces/
└── heuristic-request/         # Deployed heuristic-request slopspace
    ├── SLOPSPACE_ID
    ├── readspaces/
    └── writespaces/
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SLOPSPACES_DIR` | `/host-agent-files/slopspaces` | Slopspace storage |
| `WORK_SIGNALS_DIR` | `/host-agent-files/work` | Work signals directory |
| `AGENT_SLOPSPACE_ROOT` | `/agent` | Where slopspaces are deployed |
| `AGENT_RECORDS_PATH` | `/host-agent-files/agent-records` | Session records |

## Testing

```ridealong
make test
```

## Docker

Build the Docker image from the AI-evo1 directory (not part of ridealong - requires full docker setup):

```bash
cd research/AI-evo1
docker build -f dungeon-keeper/Dockerfile -t dungeon-keeper .
```

Run with mounted volumes:

```bash
docker run -v /host-agent-files:/host-agent-files \
           -v /agent:/agent \
           dungeon-keeper watch --agent-type agent-worker
```

## Back to Main Tour

See [../../docs/tours/brief-tour.md](../../docs/tours/brief-tour.md) for the integration tour.
