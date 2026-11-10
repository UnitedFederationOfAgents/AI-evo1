# Not Yet Implemented

This document tracks functionality from the legacy `sandbox/AI-sandboxing/ambiguous-agent/ambiguous-shell` that has not yet been brought over to `federation-command`.

## Pending Features

### From Legacy Shell

1. **Capability-based model selection (`-c` flag)**
   - Legacy supported `agent -c <capability> <prompt>` for automatic agent/model selection based on task type
   - Capabilities like `image`, `cheap` mapped to specific agent/model combinations
   - Status: Not implemented

2. **Direct output tee to log file**
   - Legacy shell copied all stdout/stderr directly to the session log file
   - federation-command relies on clauditable for record-keeping instead
   - Status: Different approach taken

3. **invoke-agent.sh script**
   - Legacy used a bash script for agent invocation
   - federation-command uses ambiguous-agent binary directly
   - Status: Superseded by ambiguous-agent

### Interactive Mode Testing

The interactive REPL mode is not easily testable in CI. The current test suite covers:
- `--version` flag
- Agent validation
- Variable name validation
- Path abbreviation
- Argument parsing with quotes
- Multi-line continuation detection
- Mode descriptions

#### Testing Interactive Mode

Options for testing the interactive REPL in the future:

1. **PTY-based testing**: Use the `os/exec` package with a pseudo-terminal to simulate interactive input
   - Pros: Most accurate simulation
   - Cons: Platform-dependent, complex setup

2. **Expect-style testing**: Use a Go expect library like `goexpect` or `go-expect`
   - Pros: Readable test scenarios
   - Cons: Additional dependency

3. **Input injection**: Refactor readline handling to accept an io.Reader interface for testing
   - Pros: No external dependencies, testable in unit tests
   - Cons: Requires code refactoring

4. **Integration test with docker**: Run the shell in a container with scripted input
   - Pros: End-to-end testing
   - Cons: Slow, requires Docker

5. **Snapshot testing**: Record terminal sessions and replay them
   - Pros: Easy to create tests from real usage
   - Cons: Brittle to output changes

Recommended approach: Refactor to use interfaces (option 3) for unit testing, combined with occasional manual testing or PTY-based integration tests for the full interactive experience.

## Environment Variables

The following environment variables are recognized:

| Variable | Description | Default |
|----------|-------------|---------|
| `AGENT_RECORDS_PATH` | Base directory for agent records | `/host-agent-files/agent-records` |
| `AGENT_NAME` | Default agent to use | `claude` |
| `AGENT_MODEL` | Default model override | (none) |
| `AGENT_SESSION` | Session identifier | Auto-generated |
| `IS_CLAUDITABLE` | Double-wrap prevention flag | (set by clauditable) |

## Dependencies

- `clauditable`: Required for command recording
- `ambiguous-agent`: Required for AI agent invocation

Both must be available in PATH or in the same directory as the `federation-command` binary.

## Migration Notes

When migrating from the legacy shell:

1. Replace `agent -p <prompt>` with `agent -p <prompt>` (same syntax)
2. Replace `agent -e <prompt>` with `agent -x <prompt>` (execute mode)
3. Add `-r` for read mode (new, default)
4. Add `-w` for write mode (new)
5. Capability flags (`-c`) are not yet supported
