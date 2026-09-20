# Prompt

[InitialDistributedDevelopment](../InitialDistributedDevelopment.md)

Next we are implementing the ufa-loader (see 'docs/DevMode.md' for some extra guidance).

The loader will work by being a wrapping executable which launches the inner sub-application. The syntax of launching it will be: 'ufa-loader [optional-loader-flags] <binary> [binary-args]' -- for example 'ufa-loader local-representative --dev-mode'

The sub-application will gain a behaviour where it prints a structured section after an identifying banner as the final act before termination. The loader will recognize this and will trigger a restart. We expect that the binary it calls will have been replaced with a more recent version.

We will implement this ufa-loader sub-application to target only local-representative at first, but it will later be expanded to other sub-applications and so we may think ahead and use a common code approach.

We will introduce a 'make run-loader' command to the makefile of all supporting sub-applications.


prompt: [`f545c1c`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/f545c1ce99ffadf3f1bfa254957737f5e2c8c9ab) → reply: [`4f593ce`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/4f593cea56298f8f6123f2047bb2a228de235a5f)

## Reply

This all looks correct and complete. I was not able to run `go build`/`go test` in this sandbox (Bash denied any `go`/`make`-toolchain invocation without approval), so I did a careful line-by-line manual review of every new/changed Go file for import correctness, types, and control flow instead.

## Summary

Implemented Step 2: added the `ufa-loader` module (`main.go` + `restartsignal` sub-package) — a generic wrapping executable (`ufa-loader [flags] <binary> [args...]`) that launches a sub-application, watches its stdout for a `restartsignal.Announce` banner/JSON/footer sequence, and relaunches on that signal while propagating a plain exit's code untouched. Wired `local-representative` to speak the protocol (SIGHUP → announce-and-exit via `watchRestartSignal`), added `make run-loader` to its Makefile, added `ufa-loader` to the root Makefile's `SUBPROJECTS`, and updated `docs/DevMode.md` / `local-representative/README.md` / `ufa-loader/README.md` accordingly. Added unit tests for the `restartsignal` protocol and the loader's restart loop (using real `sh` subprocesses), but couldn't execute `go build`/`go test` in this session since the sandbox denies Go-toolchain invocations without interactive approval.


## Revision A

Let's concisely add a 'restart' control to local-representative under the system tab - it allows us to terminate the application such that the loader re-launches it with the identical config.

When launching applications ufa-loader exports a UFA_LOADER_INIT environment variable that the launched sub-applications detect during startup. This is what makes the ufa-loaded sub-applications detect that they are loader-managed.

When local-representative is not loader-managed the 'restart' control is greyed out (not usable).
