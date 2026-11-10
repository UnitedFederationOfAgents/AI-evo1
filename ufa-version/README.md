# ufa-version

Shared build-version plumbing for UFA sub-application binaries. Import path
`ufa-version`, package `ufaversion`. See
[`docs/DevMode.md`](../docs/DevMode.md) "Versioning" for the full picture.

## API

```go
import ufaversion "ufa-version"

fmt.Println(ufaversion.Version) // "dev" unless overridden at link time

if ufaversion.HandleVersionFlag() {
    return // "--version" was in os.Args[1:]; it's already been printed
}
```

`Version` defaults to `"dev"`. Every sub-application's Makefile overrides it
at link time from [`scripts/compute-version.sh`](../scripts/compute-version.sh):

```make
VERSION := $(shell bash ../scripts/compute-version.sh)

build:
	go build -ldflags "-X ufa-version.Version=$(VERSION)" -o <binary> .
```

`HandleVersionFlag` scans `os.Args[1:]` directly for `--version` rather than
going through any particular flag-parsing library, so the same one-liner
works whether the caller uses the stdlib `flag` package, a raw `os.Args`
switch, or something else. It's meant to be called first thing in `main()`,
before any other flag parsing.

`ufa-loader` doesn't use `HandleVersionFlag`: its own argv is followed by a
wrapped binary's, so a bare scan of `os.Args` would swallow that binary's own
`--version` — it declares `-version` as a proper flag instead, which stdlib
`flag.Parse()` naturally scopes to ufa-loader's own leading flags.
