# federation-command Tour

federation-command is an interactive CLI shell for orchestrating AI agents. It provides a readline-based interface with session management and record-keeping.

## Starting the Shell

```bash
export AGENT_SESSION=my-session
./federation-command
```

## Session Management

Each shell instance creates a session directory for record-keeping:

```
list-sessions
```

## Agent Commands

```
list-agents                    # Show available agents
set-agent <name>               # Switch active agent
list-models                    # Show models for current agent
set-model <name>               # Set model override
clear-model                    # Use agent's default model
```

## Invoking Agents

```
agent -p <prompt>              # Prompt mode (chat only)
agent -r <prompt>              # Read mode (default)
agent -w <prompt>              # Write mode
agent -x <prompt>              # Execute mode
```

## Providing Records Context

Add `-provide-records <id>` to any agent command:

```
agent -provide-records default -r "What did we do?"
agent -provide-records "session1" -provide-records "session2" -r "Review sessions"
```

Session IDs can include `default` for the current session.

## Multi-Line Input

- Backslash continuation: `\` at end of line
- Unclosed quotes: Continue until quote is closed
- Heredoc: `<<<DELIMITER` ... `DELIMITER`

## Shell Features

Standard shell conveniences:

```
cd <path>                      # Change directory
export VAR=value               # Set environment variable
ls, cat, etc                   # Regular commands (wrapped with clauditable)
exit                           # End session
```

## Testing

```bash
make test
```

## Back to Main Tour

See [../../docs/tours/brief-tour.md](../../docs/tours/brief-tour.md) for the integration tour.
