# ufa-configurable

Shared config-file loading for UFA sub-applications (`federation-command`,
`local-representative`, ...). Import path `ufa-configurable`, package
`ufaconfig`.

## Model

Each sub-application reads two YAML files from a config directory:

| File | Purpose |
| --- | --- |
| `<dir>/global.yaml` | shared defaults for every sub-application |
| `<dir>/<app>.yaml` | per-application overrides (e.g. `federation-command.yaml`) |

`<dir>` defaults to `~/.ufa/config` (or `$UFA_CONFIG_DIR`) and can be overridden
at launch with `--config <dir>`.

Resolution order, highest priority first:

1. command-line flags
2. `<dir>/<app>.yaml`
3. `<dir>/global.yaml` (per key — the app file wins for any key it sets)
4. the application's built-in default

## YAML subset

A flat mapping of `key: value` scalar pairs. Blank lines and `#` comments (whole
line or trailing after an unquoted value) are ignored. Values may be single- or
double-quoted. Nested mappings, sequences and anchors are rejected so typos
surface as errors rather than being silently dropped.

```yaml
# ~/.ufa/config/global.yaml
auto-connect: true
lr-host: "10.0.0.5"
```

## API

```go
conf, err := ufaconfig.Load("federation-command", dir) // dir "" -> DefaultDir()
host := conf.String("lr-host", "localhost")
port, err := conf.Int("lr-port", 8082)
auto, err := conf.Bool("auto-connect", false)

dir, rest, err := ufaconfig.ExtractConfigDir(os.Args[1:]) // strips --config <dir>
```

A nil `*Config` is valid and behaves as an empty config.
