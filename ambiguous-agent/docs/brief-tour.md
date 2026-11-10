# ambiguous-agent Tour

ambiguous-agent provides a generic interface for invoking AI coding agents without knowing which agent/model will fulfill the request.

## Listing Agents

```bash
./ambiguous-agent --list-agents
```

Available agents include: copilot, gemini, claude, opencode, codex, grok, clod

## Listing Models

For agents that support model selection:

```bash
./ambiguous-agent --list-models -a claude
./ambiguous-agent --list-models -a grok
```

## Permission Modes

| Flag | Mode | Description |
|------|------|-------------|
| `-p` | Prompt | Chat only, no file access |
| `-r` | Read | Read files only (default) |
| `-w` | Write | Read and write files |
| `-x` | Execute | Full access including commands |

## Basic Invocation

```bash
./ambiguous-agent -r -a claude "What files are here?"
./ambiguous-agent -w -a gemini "Update the README"
./ambiguous-agent -x -a clod "Run the tests"
```

## Providing Session Context

Copy previous session records for agent context:

```bash
./ambiguous-agent -provide-records default -r "Continue our work"
./ambiguous-agent -provide-records session1 -provide-records session2 -r "Review both sessions"
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `AGENT_NAME` | Default agent selection |
| `AGENT_MODEL` | Default model selection |
| `AGENT_SESSION` | Session identifier |
| `AGENT_RECORDS_PATH` | Records directory |
| `AGENT_ADD_DIRS` | Additional directories to add |
| `NO_CLAUDITABLE` | Skip clauditable wrapping |

## Testing

```bash
make test
```

## Back to Main Tour

See [../../docs/tours/brief-tour.md](../../docs/tours/brief-tour.md) for the integration tour.
