# clauditable Tour

clauditable wraps any command and records its execution, capturing stdout/stderr with timestamps and metadata.

## Basic Usage

Wrap any command for recording. For this tour we'll set a custom records path:

```bash
export AGENT_RECORDS_PATH=/tmp/clauditable-tour
export AGENT_SESSION=tour-session
./clauditable echo "Hello, recorded world"
```

## Record Structure

After execution, the session directory contains:

- `session.jsonl` - Consolidated JSON lines with metadata
- `*-raw.txt` - Full untruncated command outputs

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

```bash
make test
```

## Cleanup

When finished with this tour, clean up the environment:

```bash
unset AGENT_RECORDS_PATH
unset AGENT_SESSION
rm -rf /tmp/clauditable-tour
```

## Back to Main Tour

See [../../docs/tours/brief-tour.md](../../docs/tours/brief-tour.md) for the integration tour.
