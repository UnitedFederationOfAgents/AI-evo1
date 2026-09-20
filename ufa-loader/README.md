# ufa-loader

A wrapping executable that launches an inner sub-application and restarts it
whenever that sub-application asks to be restarted — the foundation for
restart/version-switching described in
[`docs/DevMode.md`](../docs/DevMode.md) and implemented per
[`condocs/InitialDistributedDevelopment.md`](../condocs/InitialDistributedDevelopment.md)
Step 2.

`ufa-loader` knows nothing about which sub-application it's wrapping — only
how to launch a binary and watch its stdout for the `restartsignal` protocol
below. Today only [`local-representative`](../local-representative) speaks
it (see its `--dev-mode` / `docs/DevMode.md`), but any future sub-application
that adopts `restartsignal` gets restart/version-switching for free.

## Usage

```bash
ufa-loader [flags] <binary> [binary-args...]

# example
ufa-loader local-representative --dev-mode
```

`<binary>` and everything after it are passed straight to `exec.Command`
(so a bare name is resolved on `$PATH`, same as a shell would; a path with a
slash, relative or absolute, is used as-is). Flag parsing stops at the first
non-flag argument, so `ufa-loader`'s own flags must come before `<binary>`.

| Flag | Default | Purpose |
| --- | --- | --- |
| `-max-restarts` | `0` (unlimited) | stop relaunching after this many restarts |
| `-restart-delay` | `500ms` | pause before relaunching, giving an in-flight binary replacement time to land on disk |
| `-version` | — | print `ufa-loader`'s own version and exit instead of launching `<binary>`. Since flag parsing stops at the first non-flag argument, `ufa-loader --version` reports the loader's version; `ufa-loader <binary> --version` runs `<binary>` with `--version` instead, which reports *its* version (see "Versioning" in `docs/DevMode.md`) |

`ufa-loader` forwards `SIGINT`/`SIGTERM` it receives to the running child and
then exits once the child does — the restart trigger itself is sent directly
to the child's own pid (e.g. `kill -HUP <local-representative pid>`), not
through `ufa-loader`.

`ufa-loader` also sets `UFA_LOADER_INIT` (a non-empty value) in the child's
environment on every launch. A sub-application checks it at startup
(`restartsignal.IsLoaderManaged`) to tell whether it's loader-managed — i.e.
whether asking for a restart can be expected to actually come back up —
without needing to know anything else about how it was invoked.
`local-representative` uses this to grey out its system tab's **restart**
control when it isn't running under `ufa-loader`.

## The restart protocol

Defined in [`restartsignal`](restartsignal/restartsignal.go), this is the
"structured section after an identifying banner" the condoc calls for: as
its final act before exiting, a sub-application that wants restarting writes

```
=== UFA-LOADER-RESTART ===
{"app":"local-representative","reason":"sighup","pid":1234,"time":"2026-09-20T12:00:00Z"}
=== END-UFA-LOADER-RESTART ===
```

to its own stdout (`restartsignal.Announce(os.Stdout, app, reason)`), then
exits. `ufa-loader` streams the child's stdout straight through to its own
(so nothing about the child's normal output changes) while watching for that
exact sequence with `restartsignal.Scanner`. Seeing it changes how the
following exit is treated:

- **No announcement before exit** — an ordinary stop or crash. `ufa-loader`
  propagates the child's exit code as its own and does not restart it.
- **Announcement before exit** — `ufa-loader` waits `-restart-delay`, then
  relaunches `<binary>` with the same arguments, expecting the file on disk
  to have been replaced with a newer build in the meantime (that replacement
  step — the "dev branch follower" — is a later piece of this condoc, not
  `ufa-loader` itself).

The sub-application never restarts itself; `Announce` only discloses that a
restart is wanted and why. Actually relaunching is `ufa-loader`'s job, kept
in one shared place so the mechanics can't drift between sub-applications as
more of them adopt this protocol.

## Development

```bash
make build   # go build
make test    # go test ./...
make run BIN=../local-representative/local-representative ARGS="--dev-mode"
```

See [`local-representative`](../local-representative)'s `make run-loader`
for the wired-up version of the `make run` example above.
