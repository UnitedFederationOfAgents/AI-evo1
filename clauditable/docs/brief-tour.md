# clauditable Tour

clauditable wraps any command and records its execution, capturing stdout/stderr with timestamps and metadata.

This tour can be run as a ridealong from federation-command:
```
ridealong clauditable/docs/brief-tour.md
```

## Setup

Navigate to the clauditable directory and set up environment:

```ridealong
cd research/AI-evo1/clauditable
```

```ridealong
export AGENT_RECORDS_PATH=/tmp/clauditable-tour
```

```ridealong
export AGENT_SESSION=tour-session
```

## Basic Usage

Wrap any command for recording:

```ridealong
./clauditable echo "Hello, recorded world"
```

## Record Structure

After execution, the session directory contains:

- `session.jsonl` - Consolidated JSON lines with metadata
- `*-raw.txt` - Full untruncated command outputs

View the session directory:

```ridealong
ls -la /tmp/clauditable-tour/tour-session/
```

View the session.jsonl entries:

```ridealong
cat /tmp/clauditable-tour/tour-session/session.jsonl
```

Example session.jsonl entry:
```json
{"timestamp":"2026-04-29T10:00:00Z","event_type":"command_execution","agent":"","duration_ms":5,"exit_code":0,"command":"echo Hello, recorded world"}
```

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `AGENT_RECORDS_PATH` | Base directory for records | `/host-agent-files/agent-records` |
| `AGENT_SESSION` | Session identifier | Current date (YYYY-MM-DD) |
| `UFA_AGENT` | Agent name to record | Empty |
| `UFA_MODEL` | Model name to record | Empty |
| `AGENT_CONSOLIDATE_RECORDS` | Consolidate to session.jsonl | `true` |

## Double-Wrap Prevention

clauditable sets `IS_CLAUDITABLE=true` in the environment. If this is already set, it passes through without recording to prevent duplicate entries.

## Testing

```ridealong
make test
```

## Cleanup

When finished with this tour, clean up the environment:

```ridealong
unset AGENT_RECORDS_PATH
```

```ridealong
unset AGENT_SESSION
```

```ridealong
rm -rf /tmp/clauditable-tour
```

## Back to Main Tour

See [../../docs/tours/brief-tour.md](../../docs/tours/brief-tour.md) for the integration tour.
