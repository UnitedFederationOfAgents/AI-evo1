# heuristic-agent Tour

heuristic-agent manages asynchronous AI agent invocations through slopspaces and work signals. It is the spiritual successor to the legacy `agent-worker` and `heuristic-request` implementations.

## Core Concepts

### Slopspaces
Slopspaces are isolated workspaces that contain read-spaces (immutable context) and write-spaces (agent output). Key design decision: **slopspaces are not tied to an agent type at creation time** - the agent type is specified during deployment.

### Work Signals
Work signals are JSONL files that describe work to be done. They contain the agent configuration, prompt, and status tracking. Work signals are created in `/host-agent-files/work/ongoing/` and moved to `/host-agent-files/work/complete/` when finished.

## Quick Start

### Building

```bash
cd research/AI-evo1/heuristic-agent
make build
```

### Local Development Setup

Deploy the required dependencies (ambiguous-agent, clauditable, clod) locally:

```bash
make deploy-dependencies-local
```

### Running the Watch Loop

```bash
# Agent-worker mode (default)
./heuristic-agent watch --agent-type agent-worker

# Heuristic-request mode
./heuristic-agent watch --agent-type heuristic-request
```

## Slopspace Management

### Create a Slopspace

```bash
./heuristic-agent slopspace create
```

Output:
```
Created slopspace: fbf1df64-6f49-4914-8d55-6ade0c6c64b9
  Path: /host-agent-files/slopspaces/fbf1df64-6f49-4914-8d55-6ade0c6c64b9
  Agent type will be specified at deploy time
```

### Deploy a Slopspace

Agent type is specified at deploy time, not creation time:

```bash
# Deploy to agent-worker location
./heuristic-agent slopspace deploy fbf1df64-... --agent-type agent-worker

# Deploy to heuristic-request location
./heuristic-agent slopspace deploy fbf1df64-... --agent-type heuristic-request
```

### List Slopspaces

```bash
./heuristic-agent slopspace list
```

Output:
```
ID                                    DEPLOYED AGENT      DEPLOYED  ITER
------------------------------------------------------------------------
fbf1df64-6f49-4914-8d55-6ade0c6c64b9  agent-worker        yes       1
```

### Return and Delete

```bash
./heuristic-agent slopspace return fbf1df64-...
./heuristic-agent slopspace delete fbf1df64-...
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

### Creating a Work Signal Manually

```bash
printf '{"id":"%s","work_type":"in-place","work_location":"%s","agent_type":"agent-worker","role":"task","prompt":"Our nice agent should create the file /tmp/heuristic-test.txt","agent":"clod","model":"sonnet","status":"pending","created_at":"%s","updated_at":"%s"}\n' \
  "$(cat /proc/sys/kernel/random/uuid)" \
  "$(pwd)" \
  "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  > /host-agent-files/work/ongoing/WORKING-task-$(date +%s).jsonl
```

## Example: Agent-Worker with Slopspace

This example demonstrates the full slopspace lifecycle: create, populate, deploy, work, and return.

```bash
# 1. Create a slopspace
SLOP_ID=$(./heuristic-agent slopspace create | grep "Created" | awk '{print $3}')
echo "Created slopspace: $SLOP_ID"

# 2. Add a file to the write-space for the agent to work with
mkdir -p /host-agent-files/slopspaces/$SLOP_ID/write-spaces/files
echo "TODO: implement feature X" > /host-agent-files/slopspaces/$SLOP_ID/write-spaces/files/CAT-TASK.txt

# 3. Deploy the slopspace to agent-worker location
./heuristic-agent slopspace deploy $SLOP_ID --agent-type agent-worker

# 4. Verify deployment (files moved to /agent/agent-worker/)
ls /agent/agent-worker/write-spaces/files/

# 5. Create a work signal targeting the slopspace
TS=$(date +%s)
cat > /host-agent-files/work/ongoing/WORKING-slop-example-$TS.jsonl <<EOF
{"id":"slop-example-$TS","work_type":"slopspace","agent_type":"agent-worker","role":"task","prompt":"Our nice agent should modify the file write-spaces/files/CAT-TASK.txt","agent":"clod","model":"sonnet","status":"pending","created_at":"$(date -u +%Y-%m-%dT%H:%M:%SZ)","updated_at":"$(date -u +%Y-%m-%dT%H:%M:%SZ)"}
EOF

# 6. Start the watch loop (or run it in another terminal beforehand)
./heuristic-agent watch --agent-type agent-worker

# 7. After work completes, return the slopspace
./heuristic-agent slopspace return $SLOP_ID

# 8. Check results in the slopspace
cat /host-agent-files/slopspaces/$SLOP_ID/write-spaces/files/DONE.txt

# 9. Clean up
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

```bash
make test
```

## Docker

Build the Docker image from the AI-evo1 directory:

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
