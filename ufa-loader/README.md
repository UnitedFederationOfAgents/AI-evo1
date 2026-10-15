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
| `-no-aws-principal` | `false` | skip the temporary AWS principal (below) and pass whatever AWS credentials ufa-loader started with straight through |
| `-aws-principal-idle-days` | `2` | delete temporary AWS principals from previous days once they've gone unused this many days |
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

## Temporary AWS principals

So that sub-applications such as `the-conversationalist` don't run on the
host's long-lived AWS credentials, ufa-loader gives each launch its own
temporary IAM user ([`awsprincipal`](awsprincipal/principal.go); see
[`condocs/initialRobotImpls/Step5SubstepGPrompt.md`](../condocs/initialRobotImpls/Step5SubstepGPrompt.md)
Revision D). Before the first launch of `<binary>`:

1. **Starting credentials.** It looks for AWS credentials:
   `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` (plus `AWS_SESSION_TOKEN`), or
   static keys for the selected profile (`AWS_PROFILE`, else `default`) in
   `~/.aws/credentials` or `~/.aws/config`. If there aren't any, it does
   nothing and logs nothing. SSO, `role_arn`, `credential_process` and
   instance or container roles aren't supported yet, so they count as "no
   credentials".
2. **Create.** Using those credentials, it creates IAM user
   `ufa-tmp-<host-id>-<YYYYMMDD>-<6 random chars>` under path `/ufa-temp/`,
   tagged `ufa-host=<host-id>`, `date=<YYYY-MM-DD>` (UTC) and
   `app=<binary's base name>`. It attaches `AdministratorAccess` (high
   privilege for now; this will be scaled back) and creates an access key,
   then waits up to 30s for STS to accept the new key. The random suffix
   lets several loaders on one host on the same day each get their own
   user. If any step fails, the half-built user is deleted, a line says
   why, and the starting credentials are passed through unchanged.
3. **Clean up.** Still using the starting credentials, it deletes users under
   `/ufa-temp/` that have a `ufa-host` tag and a `date` tag before today,
   and that haven't been used for `-aws-principal-idle-days` days. "Used"
   means the most recent access-key use IAM reports, or the user's creation
   if no key was ever used. Their access keys and attached policies are
   removed first. Note that IAM's last-used time can lag by a few hours, and
   a long-running sub-app that makes no AWS calls for that long will lose
   its principal.
4. **Hand over.** It removes every credential-bearing `AWS_*` variable from
   its own environment (`AWS_SESSION_TOKEN`, `AWS_PROFILE`, role and
   container variables, and so on) and sets `AWS_ACCESS_KEY_ID` and
   `AWS_SECRET_ACCESS_KEY` to the new key. Every launch and restart of
   `<binary>` inherits only those. `AWS_REGION`, `AWS_DEFAULT_REGION` and the
   shared-file paths are left alone. If neither region variable is set,
   the selected profile's `region` from `~/.aws/config` is carried over as
   `AWS_REGION`, because once `AWS_PROFILE` is cleared the sub-app's SDK
   can't see that profile any more. The new key exists only in ufa-loader's
   memory and its
   children's environments; nothing is written to disk.

It logs exactly one line when this happens:

```
ufa-loader: created temporary AWS principal ufa-tmp-devbox-ab12-20261012-x7k2qp for the-conversationalist (removed 1 stale)
```

Only the `aws` partition's global IAM and STS endpoints are used. Requests
are signed by a small built-in SigV4 implementation instead of
aws-sdk-go-v2, because every module that imports `ufa-loader/restartsignal`
inherits ufa-loader's module requirements.

## Carrying state across a restart

An announcement may attach opaque `state` (see the JSON shape below).
`ufa-loader` doesn't look at what's inside — it just hands it back to the
*next* launch of the same binary as the `UFA_LOADER_STATE` environment
variable, mirroring `UFA_LOADER_INIT`. A sub-application that wants to bring
some of its own live state forward across a restart writes it via
`restartsignal.AnnounceState(w, app, reason, state)` before exiting, and
reads its predecessor's via `restartsignal.PreviousState()` on the next
launch (`ok` is false on the very first launch, or whenever the previous
announcement carried no state). `local-representative` uses this for its own
`auto-rebuild`/`auto-connect` toggles — see
[`docs/DevMode.md`](../docs/DevMode.md) "Dev-repo watcher" and
[`condocs/initialDistributedDevelopmentImpls/Step3Prompt.md`](../condocs/initialDistributedDevelopmentImpls/Step3Prompt.md)
Revision D.

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
  `ufa-loader` itself). If the announcement carried `state`, it's handed to
  the relaunched process as `UFA_LOADER_STATE` — see "Carrying state across a
  restart" above.

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
