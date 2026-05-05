# Brief Tour of AI-evo1

This document walks through the AI-evo1 suite component by component, building from primitives to the full integrated shell. Running this as a ridealong executes each sub-project tour in sequence:

```
ridealong docs/tours/brief-tour.md
```

For a more comprehensive and detailed tour, see [full-tour.md](full-tour.md).

## Prerequisites

Navigate to the AI-evo1 directory:

```ridealong
cd research/AI-evo1
```

Ensure the suite is built:

```ridealong
make build-all
```

## Chapter 1: clod – The Test Agent

clod mimics real AI coding agents without making API calls. It is the foundation for exercising the rest of the suite during development and CI, with no API keys or costs required.

```ridealong
ridealong clod/docs/brief-tour.md
```

## Chapter 2: clauditable – Transparent Recording

clauditable wraps any command and records its execution, capturing stdout/stderr with timestamps and metadata. Every agent invocation in the suite flows through clauditable, giving you a complete audit trail of what ran, when, and what it produced.

```ridealong
ridealong clauditable/docs/brief-tour.md
```

## Chapter 3: ambiguous-agent – Unified Agent Interface

ambiguous-agent provides a single invocation interface across all supported AI coding agents. Switching from clod to claude, gemini, or copilot is a one-flag change — the rest of the invocation stays the same.

```ridealong
ridealong ambiguous-agent/docs/brief-tour.md
```

## Chapter 4: federation-command – Interactive Shell

federation-command brings the previous three components together in an interactive, readline-based shell with session management and multi-line input. The sub-tour below covers setup and all rideable steps; the interactive shell session itself must be launched manually:

```ridealong
ridealong federation-command/docs/brief-tour.md
```

### Integration Demo (manual)

With the shell launched, the following demonstrates how the components integrate end-to-end. Launch it:

```bash
export AGENT_SESSION=tour-session
./federation-command/federation-command
```

Inside the shell, list agents (backed by ambiguous-agent), switch to clod, run prompts with different permission modes (recorded by clauditable), then review session records:

```federation-command
list-agents
```

```federation-command
set-agent clod
```

```federation-command
agent -p Hello, are you conscious?
```

```federation-command
agent -r What files are in this directory?
```

```federation-command
agent -w Our nice agent should create the file /tmp/tour-test.txt
```

```federation-command
list-sessions
```

```federation-command
exit
```

Inspect the records after exiting:

```ridealong
ls -la /host-agent-files/agent-records/tour-session/ 2>/dev/null || echo "Session directory will be created when you run the tour"
```

Start a second session and provide the first session's records as context:

```bash
export AGENT_SESSION=tour-session-2
./federation-command/federation-command
```

```federation-command
agent -provide-records tour-session -r What happened in our last session?
```

```federation-command
exit
```

Cleanup:

```ridealong
rm -f /tmp/tour-test.txt
```

## Chapter 5: heuristic-agent – Async Orchestration

heuristic-agent manages long-running, background AI work through slopspaces and work signals. Where federation-command is interactive, heuristic-agent handles tasks that run unattended.

```ridealong
ridealong heuristic-agent/docs/brief-tour.md
```

## Summary

The AI-evo1 suite layers cleanly:

1. **clod** – Drop-in test agent; no API calls, no cost.
2. **clauditable** – Wraps any command with transparent recording.
3. **ambiguous-agent** – Single interface across all supported agents.
4. **federation-command** – Interactive shell tying the three together with session management.
5. **heuristic-agent** – Async orchestration for background agent work via slopspaces and work signals.

## Next Steps

Run the full test suite:

```ridealong
make test-all
```

Explore `.github/workflows/` for CI patterns, or open any sub-project tour directly to go deeper on a single component.
