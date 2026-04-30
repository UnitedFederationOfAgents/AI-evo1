# Brief Tour of AI-evo1

This document provides a walkthrough of the AI-evo1 suite from the perspective of federation-command, demonstrating how the components integrate. Each code block can be executed in sequence to tell a story about how the system works.

For a more comprehensive and detailed tour, see [full-tour.md](full-tour.md).

For component-specific documentation, see the tour documents in each sub-project:
- [clod/docs/brief-tour.md](../../clod/docs/brief-tour.md) - Test agent details
- [clauditable/docs/brief-tour.md](../../clauditable/docs/brief-tour.md) - Record-keeping internals
- [ambiguous-agent/docs/brief-tour.md](../../ambiguous-agent/docs/brief-tour.md) - Agent abstraction layer
- [federation-command/docs/brief-tour.md](../../federation-command/docs/brief-tour.md) - Interactive shell features

## Prerequisites

Start from the repository root:

```bash
cd "$(git rev-parse --show-toplevel)"
```

Ensure the suite is built:

```bash
make build-all
```

## Chapter 1: Starting a Session

Launch federation-command. This creates a session directory where all interactions will be recorded. We set a custom session name so we can reference it later:

```bash
export AGENT_SESSION=tour-session
./federation-command/federation-command
```

You'll see the session indicator showing where records are stored. The shell is now ready for commands.

## Chapter 2: Exploring Available Agents

Within the federation-command shell, list the available agents:

```
list-agents
```

You'll see agents like claude, gemini, copilot, and clod (our test agent). Notice which ones support model selection - use `list-models` to see options.

## Chapter 3: Working with the Test Agent

Set clod as the active agent for testing without API calls:

```
set-agent clod
```

Try a simple prompt-only interaction:

```
agent -p Hello, are you conscious?
```

Clod responds conversationally. Now try asking it to read:

```
agent -r What files are in this directory?
```

## Chapter 4: Understanding Permission Modes

Try to make clod write a file without write permissions:

```
agent -r Our nice agent should create the file /tmp/tour-test.txt
```

Clod explains it can't write without permission. Now enable write mode:

```
agent -w Our nice agent should create the file /tmp/tour-test.txt
```

The file is created. You can verify outside the shell with `cat /tmp/tour-test.txt`.

## Chapter 5: Session Records

Every command has been recorded. List the sessions:

```
list-sessions
```

The current session shows file counts. Exit and examine the records:

```
exit
```

Now look at the session directory (default location):

```bash
ls -la /host-agent-files/agent-records/tour-session/
```

You'll find:
- `session.jsonl` - Consolidated JSON lines with metadata
- `*-raw.txt` - Full untruncated command outputs

## Chapter 6: Continuing Work with Records

Start a new session and provide previous session context:

```bash
export AGENT_SESSION=tour-session-2
./federation-command/federation-command
```

In the shell:

```
list-sessions
agent -provide-records tour-session -r What happened in our last session?
```

The agent receives the session records as context. Multiple sessions can be provided:

```
agent -provide-records tour-session -provide-records tour-session-2 -r Summarize both sessions
```

## Summary

The AI-evo1 suite provides:
1. **clod** - A test agent for CI/development without API costs
2. **clauditable** - Transparent recording of any command execution
3. **ambiguous-agent** - Uniform interface across different AI coding agents
4. **federation-command** - Interactive orchestration with session management

All components work together, with federation-command providing the primary user interface that leverages the others for agent abstraction and record-keeping.

## Next Steps

- Run `make test-all` to verify the full test suite passes
- Explore the sub-project tour documents linked above
- Check `.github/workflows/` for CI patterns
