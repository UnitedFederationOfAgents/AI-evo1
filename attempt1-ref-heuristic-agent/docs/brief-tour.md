# heuristic-agent Tour (Reference Implementation)

heuristic-agent manages asynchronous AI agent invocations through two core abstractions: **slopspaces** (isolated working directories) and **work signals** (JSONL task files).

This is the reference implementation (`attempt1-ref-heuristic-agent`). For the current implementation, see [../../heuristic-agent/docs/brief-tour.md](../../heuristic-agent/docs/brief-tour.md).

This tour can be run as a ridealong from federation-command:
```
ridealong attempt1-ref-heuristic-agent/docs/brief-tour.md
```

## Concepts

**Slopspaces** are managed working directories that get created, deployed to a live path, used by an agent, then returned. This lets multiple tasks share a workspace across iterations without conflicts.

**Work signals** are JSONL files in `/host-agent-files/work/ongoing/`. Each file starts with a header record describing the current state of the task, followed by appended event records as the worker processes it. Completed signals move to `/host-agent-files/work/complete/`.

**Agent types** determine which worker picks up a signal:
- `agent-worker` — general-purpose coding agent tasks
- `heuristic-request` — tasks routed to a heuristic/planning agent

## Setup

Navigate to the heuristic-agent directory:

```ridealong
cd research/AI-evo1/attempt1-ref-heuristic-agent
```

## Building

```ridealong
make build
```

## Managing Slopspaces

Create a slopspace (returns an ID):

```ridealong
./heuristic-agent slopspace create
```

List all slopspaces and their deployment status:

```ridealong
./heuristic-agent slopspace list
```

Check what's currently deployed:

```ridealong
./heuristic-agent slopspace status
```

## Watching for Work

**Note:** The watch command runs as a continuous loop. For this ridealong, we demonstrate slopspace management only. To test the full watch loop, run this command in a separate terminal:

```bash
# Run in a separate terminal (not part of ridealong):
./heuristic-agent watch
```

For the heuristic-request role:

```bash
# Run in a separate terminal (not part of ridealong):
./heuristic-agent watch --agent-type heuristic-request
```

The worker logs its ID and directories on startup. When idle it backs off progressively (30s → 5m → 1h → 24h) to reduce log noise.

## Work Signal Flow

When the worker picks up a signal it:

1. Takes ownership (sets `holder` to its worker ID, status → `processing`)
2. Reads the signal to determine work type and agent config
3. For **slopspace** work: creates/reuses a deployed slopspace, invokes the agent, returns the slopspace
4. For **in-place** work: invokes the agent directly in the `work_location` path
5. Marks the signal complete (status → `completed` or `failed`) and moves the file to `complete/`

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SLOPSPACES_DIR` | `/host-agent-files/slopspaces` | Where slopspaces are stored |
| `WORK_SIGNALS_DIR` | `/host-agent-files/work` | Root for `ongoing/` and `complete/` |
| `AGENT_SLOPSPACE_ROOT` | `/agent` | Base deploy path (appends `/<agent-type>`) |
| `AGENT_RECORDS_PATH` | `/host-agent-files/agent-records` | Agent session records |

## Testing

```ridealong
make test
```

## Back to Main Tour

See [../../docs/tours/brief-tour.md](../../docs/tours/brief-tour.md) for the integration tour.
