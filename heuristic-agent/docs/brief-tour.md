# heuristic-agent Tour

heuristic-agent manages asynchronous AI agent invocations through slopspaces and work signals. It is the spiritual successor to the legacy `agent-worker` and `heuristic-request` implementations.

This tour can be run as a ridealong from federation-command:
```
ridealong heuristic-agent/docs/brief-tour.md
```

## Core Concepts

### Slopspaces
Slopspaces are isolated workspaces that contain read-spaces (immutable context) and write-spaces (agent output). Key design decision: **slopspaces are not tied to an agent type at creation time** - the agent type is specified during deployment.

### Work Signals
Work signals are JSONL files that describe work to be done. They contain the agent configuration, prompt, and status tracking. Work signals are created in `/host-agent-files/work/ongoing/` and moved to `/host-agent-files/work/complete/` when finished.

## Setup

Navigate to the heuristic-agent directory:

```ridealong
cd research/AI-evo1/heuristic-agent
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

```ridealong
./heuristic-agent slopspace create
```

Output:
```
Created slopspace: fbf1df64-6f49-4914-8d55-6ade0c6c64b9
  Path: /host-agent-files/slopspaces/fbf1df64-6f49-4914-8d55-6ade0c6c64b9
  Agent type will be specified at deploy time
```

### List Slopspaces

```ridealong
./heuristic-agent slopspace list
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
./heuristic-agent watch --agent-type agent-worker

# Heuristic-request mode
./heuristic-agent watch --agent-type heuristic-request
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

Subsequent lines are events:

```json
{"event_id": "...", "status_update": "processing", "comment": "Starting work", "timestamp": "..."}
```

## Example: Slopspace Lifecycle (Ridealong)

This example demonstrates slopspace management without the watch loop. For full end-to-end testing with work signals, first start the watch loop in a separate terminal:

```bash
# Run in a separate terminal (not part of ridealong):
./heuristic-agent watch --agent-type agent-worker
```

Then run this ridealong to create and manage a slopspace:

Create a slopspace and capture its ID:

```ridealong
SLOP_ID=$(./heuristic-agent slopspace create | grep "Created" | awk '{print $3}') && echo "Created slopspace: $SLOP_ID"
```

Add a file to the write-space for the agent to work with:

```ridealong
mkdir -p /host-agent-files/slopspaces/$SLOP_ID/write-spaces/files && echo "TODO: implement feature X" > /host-agent-files/slopspaces/$SLOP_ID/write-spaces/files/CAT-TASK.txt
```

Deploy the slopspace to agent-worker location:

```ridealong
./heuristic-agent slopspace deploy $SLOP_ID --agent-type agent-worker
```

Verify deployment (files moved to /agent/agent-worker/):

```ridealong
ls /agent/agent-worker/write-spaces/files/
```

Create a work signal targeting the slopspace (the watch loop will pick this up):

```ridealong
TS=$(date +%s) && printf '{"id":"slop-example-%s","work_type":"slopspace","agent_type":"agent-worker","role":"task","prompt":"Our nice agent should modify the file write-spaces/files/CAT-TASK.txt","agent":"clod","model":"sonnet","status":"pending","created_at":"%s","updated_at":"%s"}\n' "$TS" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > /host-agent-files/work/ongoing/WORKING-slop-example-$TS.jsonl
```

Wait briefly for the watch loop to process, then return the slopspace:

```ridealong
sleep 15 && ./heuristic-agent slopspace return $SLOP_ID
```

Check results in the slopspace:

```ridealong
cat /host-agent-files/slopspaces/$SLOP_ID/write-spaces/files/DONE.txt 2>/dev/null || echo "DONE.txt not created (watch loop may not be running)"
```

Clean up the slopspace:

```ridealong
./heuristic-agent slopspace delete $SLOP_ID
```

Key points:
- **Create** establishes empty read/write spaces (no agent-type binding)
- **Populate** adds context files before deployment
- **Deploy** moves spaces to `/agent/<agent-type>/` for agent access
- **Work** happens via work signals; the agent sees files in its deploy path
- **Return** moves write-spaces back; read-spaces are discarded and recreated
- **Delete** removes the slopspace after completion

## Directory Structure

```
/host-agent-files/
├── slopspaces/
│   └── <slopspace-id>/
│       ├── read-spaces/       # Immutable from agent perspective
│       │   ├── agent-records/
│       │   ├── dtt-images/
│       │   ├── repos/
│       │   └── files/
│       ├── write-spaces/      # Changes reflected outside
│       │   ├── agent-records/
│       │   ├── dtt-canvas/
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
│   ├── read-spaces/
│   └── write-spaces/
└── heuristic-request/         # Deployed heuristic-request slopspace
    ├── SLOPSPACE_ID
    ├── read-spaces/
    └── write-spaces/
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
docker build -f heuristic-agent/Dockerfile -t heuristic-agent .
```

Run with mounted volumes:

```bash
docker run -v /host-agent-files:/host-agent-files \
           -v /agent:/agent \
           heuristic-agent watch --agent-type agent-worker
```

## Back to Main Tour

See [../../docs/tours/brief-tour.md](../../docs/tours/brief-tour.md) for the integration tour.
