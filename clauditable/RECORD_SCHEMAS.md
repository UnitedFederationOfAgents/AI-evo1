# Record Schemas

This document describes the record and event schemas used by `clauditable` and related tools.

## Terminology

- **Records**: Directly collected signals from command executions or agent interactions
- **Reports**: Significantly processed, composed, or refined records (not covered here)

## File Types

### Session Log Entry (`session.log`)

Each entry in the session log consists of:

1. **JSON metadata line** - A single line of JSON containing the `Event` structure
2. **Input preview** - Command input with `IN>> ` prefix (up to 20 lines)
3. **Output preview** - Command output with `OUT>> ` prefix for stdout, `ERR>> ` prefix for stderr (up to 20 lines each)

```
{"timestamp":"2026-04-24T10:30:00Z","event_type":"command_execution","agent":"claude","model":"opus-4","duration_ms":50,"exit_code":0,"record_path":"/path/to/1234567890"}
IN>> echo hello
OUT>> hello

```

If input or output exceeds 20 lines, a truncation marker is added:
```
IN>> line 1
IN>> line 2
... (18 more lines)
IN>> line 20
IN>> ...
```

### Raw File (`<timestamp>-raw.txt`)

The raw file contains the complete, untruncated command and response. This file is **not consolidated** and remains as a permanent record.

Format:
```
<full command text>

----------RESPONSE----------

<full stdout>
[STDERR]
<full stderr if present>
```

### Temporary Record File (`<timestamp>`)

A temporary file containing the formatted session log entry. This file is consolidated into `session.log` and then deleted when `AGENT_CONSOLIDATE_RECORDS` is true.

## Event Schema

The `Event` structure contains metadata about a command execution. The command itself is stored as plaintext in the `IN>>` prefixed lines, not in the JSON.

| Field | Type | Description |
|-------|------|-------------|
| `timestamp` | string | RFC3339 formatted timestamp of when the command started |
| `event_type` | string | Type of event, typically `"command_execution"` |
| `agent` | string | Agent identifier from `UFA_AGENT` (optional) |
| `model` | string | Model identifier from `UFA_MODEL` (optional) |
| `duration_ms` | int64 | How long the command took in milliseconds |
| `exit_code` | int | The exit code returned by the command |
| `record_path` | string | Path to the record file (optional) |
| `metadata` | object | Unstructured key-value pairs from `UFA_METADATA` (optional) |

## Directory Structure

```
<AGENT_RECORDS_PATH>/
└── <session>/
    ├── session.log           # Consolidated JSONL with plaintext previews
    ├── 1234567890-raw.txt    # Raw file (permanent, not consolidated)
    ├── 1234567891-raw.txt    # Another raw file
    └── ...
```

When `AGENT_CONSOLIDATE_RECORDS=true` (default), temporary `<timestamp>` files are appended to `session.log` and deleted. The `-raw.txt` files are always preserved.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENT_RECORDS_PATH` | `/host-agent-files/agent-records` | Root directory for all records |
| `AGENT_SESSION` | Current date (YYYY-MM-DD) | Session identifier, auto-updates daily |
| `AGENT_CONSOLIDATE_RECORDS` | `true` | Whether to consolidate temporary files into session.log |
| `UFA_AGENT` | (none) | Agent identifier to include in events |
| `UFA_MODEL` | (none) | Model identifier to include in events |
| `UFA_METADATA` | (none) | Key-value metadata, format: `key1=value1,key2=value2` |

## Design Rationale

### JSON + Plaintext Format

The session log uses a hybrid format:
- **JSON line first**: Enables programmatic parsing of metadata
- **Plaintext preview after**: Enables human readability and quick scanning

This design supports both machine processing and human review without requiring a separate tool to render the logs.

### Command Stored as Plaintext

The command is stored in the `IN>>` prefixed lines rather than in the JSON blob. This:
- Avoids redundancy between JSON and plaintext
- Keeps the JSON focused on metadata about the execution
- Makes the session log more human-readable

### Separate Raw Files

Raw files (`-raw.txt`) are kept separate because:
- They may be large and would bloat the session log
- They serve as an immutable reference when the session log is summarized
- They enable future processing without re-running commands

### Line Prefixes

The `IN>> `, `OUT>> `, and `ERR>> ` prefixes:
- Make it clear which stream each line belongs to
- Enable regex-based filtering (e.g., `grep "^ERR>>"`)
- Prevent confusion when command output contains similar patterns

### Single Record Path

The `record_path` field serves as a reference for both the consolidated entry location and the raw file (by appending `-raw.txt`). This simplifies the schema while maintaining full traceability.

## Metadata

The `metadata` field allows arbitrary key-value pairs to be attached to events. This is useful for:
- Recording environment context (pwd, git branch, etc.)
- Tagging events for filtering
- Extending the schema without modifying the core structure

Metadata is passed via the `UFA_METADATA` environment variable in the format:
```
UFA_METADATA="key1=value1,key2=value2"
# or
UFA_METADATA="key1=value1;key2=value2"
```

## Extending the Schema

When adding new event types:

1. Add a new `event_type` value (e.g., `"agent_interaction"`)
2. Use the same `Event` structure with appropriate fields
3. Consider whether new fields are needed; if so, add them as optional fields
4. Update this documentation

The schema is designed to be forward-compatible: parsers should ignore unknown fields.
