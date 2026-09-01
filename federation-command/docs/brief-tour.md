# federation-command Tour

federation-command is an interactive CLI shell for orchestrating AI agents. It provides a readline-based interface with session management and record-keeping.

This tour includes both:
- **ridealong blocks**: For setup and testing (executable via `ridealong federation-command/docs/brief-tour.md`)
- **federation-command blocks**: Commands to run once inside the shell

## Setup

Navigate to the federation-command directory:

```ridealong
cd federation-command
```

Build federation-command:

```ridealong
make build
```

## Starting the Shell

Launch federation-command (this tour continues inside the shell):

```bash
export AGENT_SESSION=my-session
./federation-command
```

## Auto-Connecting to local-representative

Passing `--auto-connect` makes federation-command dial local-representative in the
background on startup, retrying every 10 seconds for up to 10 minutes. The attempt
runs without blocking the shell; while it is in progress the blinker adds a brief
blue "accent" pulse on top of its normal mode. On success FC enters remote-control
mode automatically; if the window elapses first, FC prints that it gave up.

```bash
./federation-command --auto-connect            # default representable port (8082)
./federation-command --auto-connect --lr-port 9090
```

`--lr-port` overrides the port for both the background auto-connect and the manual
blinker connect flow; it defaults to 8082.

## Session Management

Once inside federation-command, each shell instance creates a session directory for record-keeping:

```federation-command
list-sessions
```

## Agent Commands

```federation-command
list-agents                    # Show available agents
set-agent clod                 # Switch to test agent
list-models                    # Show models for current agent
```

## Invoking Agents

With clod as the active agent:

```federation-command
agent -p "Hello, are you conscious?"
```

```federation-command
agent -w "Our nice agent should create the file /tmp/fc-tour-test.txt"
```

## Providing Records Context

Add `-provide-records <id>` to any agent command:

```federation-command
agent -provide-records default -r "What did we do?"
```

Session IDs can include `default` for the current session.

## Multi-Line Input

- Backslash continuation: `\` at end of line
- Unclosed quotes: Continue until quote is closed
- Heredoc: `<<<DELIMITER` ... `DELIMITER`

## Visual Log (Scrollback Log)

Capture terminal output as it would appear when scrolling back — without graphics like dynapanes or ridealong panels:

```federation-command
scrollback-log /tmp/session.log   # Start logging to file (off by default)
```

```federation-command
ls                                # Commands run while logging is active
```

```federation-command
clear-scrollback-log              # Stop and clear the log file
```

Only one log file is active at a time. Starting a new `scrollback-log` replaces the previous one.

## Shell Features

Standard shell conveniences work inside federation-command:

```federation-command
ls                             # Regular commands (wrapped with clauditable)
cat /tmp/fc-tour-test.txt      # View file created earlier
exit                           # End session
```

## Cleanup

After exiting federation-command:

```ridealong
rm -f /tmp/fc-tour-test.txt
```

## Testing

```ridealong
make test
```

## Back to Main Tour

See [../../docs/tours/brief-tour.md](../../docs/tours/brief-tour.md) for the integration tour.
