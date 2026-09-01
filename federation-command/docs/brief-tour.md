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
blue "accent" pulse on top of its normal mode. If the window elapses first, FC
prints that it gave up.

On success, the control mode FC adopts depends on what you were doing when the
connection landed:

- If the blinker dot is **selected** (or you already started a manual connect),
  FC takes **remote control** — you were angling for it.
- Otherwise you were mid-entry at the prompt, so FC takes **local control** and
  leaves your foreground session intact. Press `→` to hand control to LR.

```bash
./federation-command --auto-connect            # default representable port (8082)
./federation-command --auto-connect --lr-port 9090
```

`--lr-host` / `--lr-port` override the target for both the background auto-connect
and the manual blinker connect flow; they default to `localhost:8082`.

## Configuration files

`--auto-connect`, `--lr-host` and `--lr-port` can also be set from YAML via the
shared `ufa-configurable` loader, read on startup from
`~/.ufa/config/global.yaml` and `~/.ufa/config/federation-command.yaml`
(override the directory with `--config <dir>`). Command-line flags win over the
per-app file, which wins over `global.yaml`, which wins over the built-in
defaults. See [../README.md](../README.md) for an example config.

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
