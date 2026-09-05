# federation-command

Interactive CLI shell for orchestrating AI agents. Wraps every command with
`clauditable` for record-keeping, provides a bubbletea-based multi-line prompt,
agent/model selection, mode-based invocation (`-p`/`-r`/`-w`/`-x`), and session
management. See [`docs/brief-tour.md`](docs/brief-tour.md) for a guided tour.

## Running

```bash
make build
./federation-command
```

## Flags

| Flag | Config key | Default | Purpose |
| --- | --- | --- | --- |
| `--config <dir>` | — | `~/.ufa/config` | directory holding the `ufa-configurable` YAML files |
| `--auto-connect` | `auto-connect` | `false` | on startup, dial `local-representative` in the background (retry every 10s for up to 10m) **and adopt remote control** once connected — for fully machine-driven auto-launch/auto-connect chains |
| `--lr-host <host>` | `lr-host` | `localhost` | `local-representative` host for auto-connect and the manual blinker connect |
| `--lr-port <n>` | `lr-port` | `8082` | `local-representative` `representable` port (same two flows) |
| `--version`, `-v` | — | — | print version and exit |

Single-dash spellings (`-auto-connect`, `-lr-port`, ...) also work. Unknown
flags are ignored for backward compatibility.

### Environment variables

`local-representative` auto-launches FC through a terminal emulator / multiplexer,
which can swallow or re-quote trailing argv. To make the machine-driven chain
robust it also passes the connection as environment variables, honoured below CLI
flags and above the config file:

| Variable | Equivalent | Notes |
| --- | --- | --- |
| `FC_AUTO_CONNECT` | `--auto-connect` | truthy = any value except `0`/`false`/`no`/`off`/empty; also selects remote control |
| `FC_LR_HOST` | `--lr-host` | non-empty value overrides the host |
| `FC_LR_PORT` | `--lr-port` | non-empty value overrides the port (validated 1–65535) |

## Configuration files

Every flag except `--config` and `--version` can also be set from YAML, via the
shared [`ufa-configurable`](../ufa-configurable) loader. On startup FC reads:

    ~/.ufa/config/global.yaml            # shared across all sub-applications
    ~/.ufa/config/federation-command.yaml # federation-command overrides

Pass `--config <dir>` (or set `$UFA_CONFIG_DIR`) to look elsewhere. Precedence,
highest first: **command-line flag → `FC_*` environment variable →
`federation-command.yaml` → `global.yaml` → built-in default** (resolved per key,
so a single value in `global.yaml` still applies even when the app file sets
other keys).

The format is a flat `key: value` mapping; `#` comments and blank lines are
ignored.

```yaml
# ~/.ufa/config/global.yaml — applies to every sub-application
auto-connect: true

# ~/.ufa/config/federation-command.yaml — federation-command only
auto-connect: true
lr-host: 10.0.0.5
lr-port: 8082
```

With the above, `./federation-command` behaves like
`./federation-command --auto-connect --lr-host 10.0.0.5`, and
`./federation-command --lr-host localhost` overrides just the host.

## Remote-by-default in an auto-launch chain

Auto-connect *is* the remote-control switch — there is no separate `--remote`
flag. When `local-representative` launches `federation-command` itself (its
**system** tab, an `auto-launch` config entry, or a command from
`agent-coordinator`), it starts it with `--auto-connect` *and*
`FC_AUTO_CONNECT=1` (from a visible terminal it hosts for you — see
`local-representative`'s README). The completed background connect then lands in
**remote control** — the terminal shows the shell but LR drives it — so a chain
that begins with a single `./local-representative` comes up fully ready to
operate from the dashboard.

While an auto-connect FC waits for that first connection its **local input is
suspended** (the prompt does not accept keystrokes), closing the window in which
the FC terminal would otherwise be locally controllable. Input is handed back if
auto-connect gives up after 10 minutes, or stays with LR once it connects. An FC
started *without* auto-connect is always locally controlled.
