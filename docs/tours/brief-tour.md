# Brief Tour of AI-evo1

This document provides a quick walkthrough of the AI-evo1 suite. Each code block can be executed in sequence to demonstrate the core functionality.

For a more comprehensive and detailed tour, see [full-tour.md](full-tour.md).

## Prerequisites

Ensure you have the following installed:
- Go 1.25 or later
- A configured AI agent CLI (claude, gemini, copilot, etc.)

## Building the Suite

Build all sub-projects from the repository root:

```bash
cd /workspaces/workspace/research/AI-evo1
make build-all
```

The suite consists of four components:
- **clod**: A test agent for CI/development purposes
- **clauditable**: Record-keeping wrapper for command execution
- **ambiguous-agent**: Generic interface for invoking AI coding agents
- **federation-command**: Interactive CLI shell for agent orchestration

## Component 1: clod (Test Agent)

The clod agent interprets prompts and demonstrates file operations. It's useful for testing the pipeline without calling real AI APIs.

```bash
cd /workspaces/workspace/research/AI-evo1/clod
./clod -p "Our nice agent should create the file /tmp/clod-test.txt"
```

Verify the file was created:

```bash
cat /tmp/clod-test.txt
```

## Component 2: clauditable (Record-Keeping)

Clauditable wraps any command and records its execution, capturing stdout/stderr with timestamps and metadata.

```bash
cd /workspaces/workspace/research/AI-evo1/clauditable
export AGENT_RECORDS_PATH=/tmp/agent-records
export AGENT_SESSION=tour-session
./clauditable echo "Hello from clauditable"
```

Check the session records:

```bash
cat /tmp/agent-records/tour-session/session.log
```

The session.log contains JSON metadata followed by prefixed input/output lines.

## Component 3: ambiguous-agent (Agent Abstraction)

Ambiguous-agent provides a uniform interface across different AI coding agents. It handles mode selection and wraps calls with clauditable.

List available agents:

```bash
cd /workspaces/workspace/research/AI-evo1/ambiguous-agent
./ambiguous-agent --list-agents
```

List models for a specific agent:

```bash
./ambiguous-agent --list-models -a claude
```

Invoke an agent in read mode (using clod for testing):

```bash
export NO_CLAUDITABLE=true
./ambiguous-agent -r -a clod "What files are here?"
```

## Component 4: federation-command (Interactive Shell)

Federation-command is the primary interface for orchestrating agents interactively.

Start the shell:

```bash
cd /workspaces/workspace/research/AI-evo1/federation-command
export AGENT_RECORDS_PATH=/tmp/agent-records
./federation-command
```

Within the shell, try these commands:

```
list-agents
set-agent clod
agent -r What files are in this directory?
exit!
```

## Running Tests

Verify everything works by running the full test suite:

```bash
cd /workspaces/workspace/research/AI-evo1
make test-all
```

## Session Records Structure

After running commands, examine the records directory:

```bash
ls -la /tmp/agent-records/tour-session/
```

You'll find:
- `session.log`: Consolidated JSONL with plaintext previews
- `*-raw.txt`: Full untruncated command output files

## Next Steps

- See [full-tour.md](full-tour.md) for detailed examples and edge cases
- Review individual component READMEs for API documentation
- Check `.github/workflows/` for CI patterns
