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
| `--auto-connect` | `auto-connect` | `false` | on startup, dial `local-representative` in the background (retry every 10s for up to 10m) |
| `--lr-host <host>` | `lr-host` | `localhost` | `local-representative` host for auto-connect and the manual blinker connect |
| `--lr-port <n>` | `lr-port` | `8082` | `local-representative` `representable` port (same two flows) |
| `--version`, `-v` | — | — | print version and exit |

Single-dash spellings (`-auto-connect`, `-lr-port`, ...) also work. Unknown
flags are ignored for backward compatibility.

## Configuration files

Every flag except `--config` and `--version` can also be set from YAML, via the
shared [`ufa-configurable`](../ufa-configurable) loader. On startup FC reads:

    ~/.ufa/config/global.yaml            # shared across all sub-applications
    ~/.ufa/config/federation-command.yaml # federation-command overrides

Pass `--config <dir>` (or set `$UFA_CONFIG_DIR`) to look elsewhere. Precedence,
highest first: **command-line flag → `federation-command.yaml` → `global.yaml`
→ built-in default** (resolved per key, so a single value in `global.yaml`
still applies even when the app file sets other keys).

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
