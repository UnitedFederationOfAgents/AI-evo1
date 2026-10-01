# Prompt

[InitialDistributedSessions](../InitialDistributedSessions.md)

In this step we will improve some session behaviour.

In this first increment we will fix the issue where 'new-session "My New Session"' does not strip the quotes.

We should end up with a name 'My New Sessions' -- not '"My New Session"'


prompt: [`e657c57`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/e657c57b1d94b496d697d449627a363cbb5fbfa1) → reply: [`0f1dfa3`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/0f1dfa3f82aa390d06895a5c97de623c031cc9c7)

## Reply

I fixed the quote-stripping bug for `new-session` in `federation-command/main.go`: added a `stripSurroundingQuotes` helper and applied it where the REPL extracts the session name from the raw input line (both the TUI-mode and script-mode `new-session` handlers), so `new-session "My New Session"` now produces `My New Session` instead of keeping the literal quotes. I also added `TestStripSurroundingQuotes` in `main_test.go` covering quoted, unquoted, and malformed-quote cases. Note: `go build`/`go test` were blocked by the sandbox's permission policy, so I verified correctness via careful manual code review rather than a compiled/test run.

## Resource 1 -- Debug Quotes

Quotes stripping failed

- [Screenshot_20261001-195912.png](5d31d14a_Screenshot_20261001-195912.png)

## Revision A

See the resource "Debug Quotes" for context - let's fix stripping quotes.

Let's also fix up the mobile view up a bit for SM.
