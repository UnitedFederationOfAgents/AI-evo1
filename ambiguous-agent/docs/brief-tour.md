# ambiguous-agent Tour

ambiguous-agent provides a generic interface for invoking AI coding agents without knowing which agent/model will fulfill the request.

This tour can be run as a ridealong from federation-command:
```
ridealong ambiguous-agent/docs/brief-tour.md
```

## Setup

Navigate to the ambiguous-agent directory:

```ridealong
cd research/AI-evo1/ambiguous-agent
```

## Listing Agents

```ridealong
./ambiguous-agent --list-agents
```

Available agents include: copilot, gemini, claude, opencode, codex, grok, clod

## Listing Models

For agents that support model selection:

```ridealong
./ambiguous-agent --list-models -a claude
```

```ridealong
./ambiguous-agent --list-models -a grok
```

## Permission Modes

| Flag | Mode | Description |
|------|------|-------------|
| `-p` | Prompt | Chat only, no file access |
| `-r` | Read | Read files only (default) |
| `-w` | Write | Read and write files |
| `-x` | Execute | Full access including commands |

## Basic Invocation with clod

Using clod (the test agent) so this tour works without API keys:

```ridealong
./ambiguous-agent -p -a clod "Hello, are you conscious?"
```

```ridealong
./ambiguous-agent -w -a clod "Our nice agent should create the file /tmp/ambiguous-agent-tour.txt"
```

Verify the file was created:

```ridealong
cat /tmp/ambiguous-agent-tour.txt
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

## Cleanup

```ridealong
rm -f /tmp/ambiguous-agent-tour.txt
```

## Testing

```ridealong
make test
```

## Back to Main Tour

See [../../docs/tours/brief-tour.md](../../docs/tours/brief-tour.md) for the integration tour.
