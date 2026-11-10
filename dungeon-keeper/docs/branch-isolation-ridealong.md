# Branch-Isolation Flow Ridealong

This ridealong demonstrates the complete branch-isolation workflow using dungeon-keeper. The flow allows an agent to work on a repository branch without having direct git access - the `.git` directory is kept secure and only merged back when the agent's work is complete.

**Target Repository:** `UnitedFederationOfAgents/AI-evo1` (this repo)
**Agent:** clod (claude-code)

## Overview

The branch-isolation flow has four main phases:

1. **Setup**: Clone the repo and create a new branch in the writespace
2. **Slopspace**: Create a slopspace, add repos, deploy
3. **Async Work**: Submit a work signal; the agent-worker watch loop picks it up and runs asynchronously
4. **Commit**: Write changes back to the new branch on the remote

## Prerequisites

Before running this ridealong, ensure:
- `TF_VAR_github_pat` environment variable is set with a valid GitHub PAT
- You have push access to `UnitedFederationOfAgents/AI-evo1`
- dungeon-keeper is built: `make build`
- An **agent-worker watch loop is already running** — this is the normal steady state of any system running this ridealong. The watch loop must be up before a work signal is submitted. If it is not already running, start it:

```bash
./dungeon-keeper watch --agent-type agent-worker > /tmp/dungeon-keeper-watch.log 2>&1 &
```

```ridealong
cd dungeon-keeper
make build
```

## Phase 1: Repository Setup

### Clone Repositories

Clone into readspaces (read-only reference, `.git` will be deleted when added to slopspace):

```ridealong
./dungeon-keeper readspace repo clone UnitedFederationOfAgents/AI-evo1
```

Clone into writespaces (for modifications, `.git` will be moved to secure storage):

```ridealong
./dungeon-keeper writespace repo clone UnitedFederationOfAgents/AI-evo1
```

### Choose a Branch Name

Set the target branch name. The `add-writespace --ref` command will create this branch
in the slopspace copy automatically (required — prevents accidental use of main).

```ridealong
export BRANCH="ridealong/branch-isolation-demo-$(date +%Y%m%d)-$(cat /dev/urandom | tr -dc 'a-z0-9' | head -c 2)"
echo "Branch will be created: $BRANCH"
```

## Phase 2: Slopspace Setup

### Create a Slopspace

```ridealong
export SLOP_ID=$(./dungeon-keeper slopspace create | awk '/Created slopspace:/ {print $3; exit}')
echo "Created slopspace: $SLOP_ID"
```

### Add Repositories to Slopspace

Add the repo to readspaces (reference copy, `.git` deleted):

```ridealong
./dungeon-keeper slopspace add-readspace repo "$SLOP_ID" UnitedFederationOfAgents/AI-evo1
```

Add the repo to writespaces, creating the new branch (`.git` moved to secure storage):

```ridealong
./dungeon-keeper slopspace add-writespace repo "$SLOP_ID" UnitedFederationOfAgents/AI-evo1 --ref "$BRANCH"
```

### Deploy the Slopspace

Deploy for the agent-worker to access at `/agent/agent-worker/`:

```ridealong
./dungeon-keeper slopspace deploy "$SLOP_ID" --agent-type agent-worker
```

Verify the repo is accessible at the deploy path:

```ridealong
ls -la /agent/agent-worker/writespaces/repos/UnitedFederationOfAgents/AI-evo1/
```

## Phase 3: Async Work

### Submit a Work Signal

Write a work signal JSONL file to the ongoing work directory. The watch loop will:
1. Detect this signal (it targets the already-deployed slopspace)
2. Invoke the agent with the given prompt
3. Return the slopspace automatically when the agent finishes

```ridealong
export TS=$(date +%s)
export SIGNAL_FILE="WORKING-branch-isolation-ridealong-${TS}.jsonl"
cat > "/host-agent-files/work/ongoing/$SIGNAL_FILE" << EOF
{"id":"branch-iso-${TS}","work_type":"slopspace","agent_type":"agent-worker","role":"branch-isolation-ridealong","prompt":"Our nice agent should create the file writespaces/repos/UnitedFederationOfAgents/AI-evo1/dungeon-keeper/docs/ridealong-result.md","agent":"clod","model":"sonnet","status":"pending","created_at":"$(date -u +%Y-%m-%dT%H:%M:%SZ)","updated_at":"$(date -u +%Y-%m-%dT%H:%M:%SZ)"}
EOF
echo "Submitted: $SIGNAL_FILE"
```

Verify the signal file was created with the correct name before polling:

```ridealong
ls -la "/host-agent-files/work/ongoing/$SIGNAL_FILE" && echo "Signal file confirmed." || echo "ERROR: signal file not found!"
```

### Poll for Completion

The signal file moves out of the `ongoing/` directory when the work is done (complete
or failed). Create and run a polling script in `/tmp/` to avoid inline multi-line shell issues:

```ridealong
cat > /tmp/wait-check-${TS}.sh << 'EOF'
#!/bin/bash
echo "Waiting for work signal to be processed..."
while [ -f "/host-agent-files/work/ongoing/$SIGNAL_FILE" ]; do
  sleep 10
  echo "  still waiting..."
done
echo "Work signal processed."
EOF
```

```ridealong
chmod +x /tmp/wait-check-${TS}.sh
```

```ridealong
/tmp/wait-check-${TS}.sh
```

### Inspect Results

Check watch loop output:

```ridealong
cat /tmp/dungeon-keeper-watch.log
```

Verify the agent created the expected file (the slopspace is returned automatically
by the watch loop, so files are back in the slopspace directory):

```ridealong
cat "/host-agent-files/slopspaces/$SLOP_ID/writespaces/repos/UnitedFederationOfAgents/AI-evo1/dungeon-keeper/docs/ridealong-result.md" 2>/dev/null || echo "File not found - check watch log above"
```

## Phase 4: Write Changes to New Branch

Push the agent's changes from the writespace back to the remote. This will:
1. Restore the `.git` directory from writespaces-secure
2. Stage all changes
3. Commit with the provided message
4. Push to `$BRANCH` on the remote

```ridealong
./dungeon-keeper slopspace write "$SLOP_ID" all --message "feat: add ridealong result from branch-isolation demo"
```

## Cleanup

Delete the slopspace:

```ridealong
./dungeon-keeper slopspace delete "$SLOP_ID"
```

Clean up the base readspace and writespace repos:

```ridealong
./dungeon-keeper readspace repo delete UnitedFederationOfAgents/AI-evo1
./dungeon-keeper writespace repo delete UnitedFederationOfAgents/AI-evo1
```

## Summary

The branch-isolation flow provides:

1. **Security**: Agents never have direct git access; the `.git` directory is stored separately
2. **Isolation**: Each slopspace is independent; changes are only committed when explicitly written
3. **Async execution**: The watch loop picks up work signals and runs the agent asynchronously; callers only need to drop a JSONL file and poll for completion
4. **New-branch workflow**: By creating the target branch in the writespace clone before adding it, agents work on a fresh branch and push only when `slopspace write` is called

### How the Watch Loop Interacts with a Pre-Deployed Slopspace

When a work signal arrives, the watch loop checks `GetDeployedID` for an existing
deployed slopspace. If found (as in this ridealong), it skips creation/deployment
and goes straight to invoking the agent, then calls `Return` automatically on completion.
This lets you pre-populate the slopspace with repos and context before submitting work.

### Directory Structure After Setup

```
/host-agent-files/
├── readspaces/
│   └── repos/
│       └── UnitedFederationOfAgents/
│           └── AI-evo1/          # Full clone with .git
├── writespaces/
│   └── repos/
│       └── UnitedFederationOfAgents/
│           └── AI-evo1/          # Full clone with .git (new branch checked out)
└── slopspaces/
    └── <slopspace-id>/
        ├── readspaces/
        │   └── repos/
        │       └── UnitedFederationOfAgents/
        │           └── AI-evo1/  # Copy WITHOUT .git (deleted)
        ├── writespaces/
        │   └── repos/
        │       └── UnitedFederationOfAgents/
        │           └── AI-evo1/  # Copy WITHOUT .git (moved to secure)
        ├── writespaces-secure/
        │   └── repos/
        │       └── UnitedFederationOfAgents/
        │           └── AI-evo1/  # The .git directory is HERE
        └── SLOPSPACE.json
```

### Key Commands Reference

| Command | Description |
|---------|-------------|
| `readspace repo clone <owner/repo>` | Clone a repo into readspaces |
| `writespace repo clone <owner/repo>` | Clone a repo into writespaces |
| `slopspace add-readspace repo <id> <owner/repo> [--ref]` | Add repo to slopspace readspaces |
| `slopspace add-writespace repo <id> <owner/repo> --ref <branch>` | Add repo to slopspace writespaces, creating the branch (--ref required) |
| `slopspace deploy <id> --agent-type <type>` | Deploy slopspace for agent access |
| `watch --agent-type <type>` | Start the watch loop (run in background for async work) |
| `slopspace write <id> all [--message <msg>]` | Commit and push all writespace repo changes |
| `slopspace write repo <id> <owner/repo> [--message <msg>]` | Commit and push a specific repo's changes |
