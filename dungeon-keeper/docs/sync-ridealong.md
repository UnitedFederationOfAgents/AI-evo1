# Sync Ridealong

This ridealong demonstrates the **auto-sync** feature. With auto-sync (the default), dungeon-keeper automatically:

- **Pre-deploy**: Pulls the latest changes from remote into all readspace and writespace repos, so the agent always has fresh data.
- **Post-return**: Commits and pushes any changes the agent made to writespace repos, without a manual `slopspace write` call.

**Target Repository:** `UnitedFederationOfAgents/AI-evo1` (this repo)
**Agent:** clod (claude-code)

## Overview

The sync flow has three main phases:

1. **Setup**: Clone the repo and create a slopspace with the default auto-sync mode
2. **Async Work**: Deploy, submit a work signal; the watch loop picks it up, agent runs, Return + auto-push happen automatically
3. **Verify**: Confirm the agent's commit landed on the remote branch without any manual write step

## Prerequisites

Before running this ridealong, ensure:
- `TF_VAR_github_pat` environment variable is set with a valid GitHub PAT
- You have push access to `UnitedFederationOfAgents/AI-evo1`
- dungeon-keeper is built: `make build`
- An **agent-worker watch loop is already running** — the normal steady state. If not:

```bash
./dungeon-keeper watch --agent-type agent-worker > /tmp/dungeon-keeper-watch.log 2>&1 &
```

```ridealong
cd dungeon-keeper
make build
```

## Phase 1: Repository Setup

### Clone Repositories

Clone into readspaces. The `.git` directory will be moved to `readspaces-secure` (not deleted)
so sync can pull from remote before each deploy:

```ridealong
./dungeon-keeper readspace repo clone UnitedFederationOfAgents/AI-evo1
```

Clone into writespaces (`.git` will be moved to `writespaces-secure` when added to slopspace):

```ridealong
./dungeon-keeper writespace repo clone UnitedFederationOfAgents/AI-evo1
```

### Choose a Branch Name

```ridealong
export BRANCH="ridealong/sync-demo-$(date +%Y%m%d)-$(cat /dev/urandom | tr -dc 'a-z0-9' | head -c 2)"
echo "Branch will be created: $BRANCH"
```

## Phase 2: Slopspace Setup

### Create a Slopspace

Auto-sync is the default; the flag is shown here explicitly for clarity:

```ridealong
export SLOP_ID=$(./dungeon-keeper slopspace create --sync-mode auto-sync | awk '/Created slopspace:/ {print $3; exit}')
echo "Created slopspace: $SLOP_ID"
```

### Add Repositories to Slopspace

Add the repo to readspaces. Note: `.git` is now moved to `readspaces-secure` instead of
being deleted, so pre-deploy sync can pull from remote:

```ridealong
./dungeon-keeper slopspace add-readspace repo "$SLOP_ID" UnitedFederationOfAgents/AI-evo1
```

Add the repo to writespaces, creating the new branch:

```ridealong
./dungeon-keeper slopspace add-writespace repo "$SLOP_ID" UnitedFederationOfAgents/AI-evo1 --ref "$BRANCH"
```

### Deploy the Slopspace

Deploying triggers **pre-deploy sync**: dungeon-keeper pulls the latest commits into both
the readspace and writespace repos before moving them to `/agent/agent-worker/`:

```ridealong
./dungeon-keeper slopspace deploy "$SLOP_ID" --agent-type agent-worker
```

Verify repos are accessible at the deploy path:

```ridealong
ls -la /agent/agent-worker/writespaces/repos/UnitedFederationOfAgents/AI-evo1/
```

## Phase 3: Async Work

### Submit a Work Signal

The agent will create a file in the writespace repo. The watch loop will:
1. Detect this signal
2. Invoke the agent
3. Call `Return` automatically — which triggers **post-return sync** (commit + push)

```ridealong
export TS=$(date +%s)
export SIGNAL_FILE="WORKING-sync-ridealong-${TS}.jsonl"
cat > "/host-agent-files/work/ongoing/$SIGNAL_FILE" << EOF
{"id":"sync-demo-${TS}","work_type":"slopspace","agent_type":"agent-worker","role":"sync-ridealong","prompt":"Our nice agent should create the file writespaces/repos/UnitedFederationOfAgents/AI-evo1/dungeon-keeper/docs/sync-ridealong-result.md containing a short description of the auto-sync feature.","agent":"clod","model":"sonnet","status":"pending","created_at":"$(date -u +%Y-%m-%dT%H:%M:%SZ)","updated_at":"$(date -u +%Y-%m-%dT%H:%M:%SZ)"}
EOF
echo "Submitted: $SIGNAL_FILE"
```

Verify the signal file exists:

```ridealong
ls -la "/host-agent-files/work/ongoing/$SIGNAL_FILE" && echo "Signal file confirmed." || echo "ERROR: signal file not found!"
```

### Poll for Completion

```ridealong
cat > /tmp/wait-sync-${TS}.sh << 'EOF'
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
chmod +x /tmp/wait-sync-${TS}.sh
```

```ridealong
/tmp/wait-sync-${TS}.sh
```

### Inspect Results

Check watch loop output (should include "post-return sync" log lines and the git push):

```ridealong
cat /tmp/dungeon-keeper-watch.log
```

Verify the agent created the expected file:

```ridealong
cat "/host-agent-files/slopspaces/$SLOP_ID/writespaces/repos/UnitedFederationOfAgents/AI-evo1/dungeon-keeper/docs/sync-ridealong-result.md" 2>/dev/null || echo "File not found - check watch log above"
```

## Phase 4: Verify Auto-Sync Pushed to Remote

The key difference from the branch-isolation ridealong: **no `slopspace write` call needed**.
Auto-sync committed and pushed the agent's work during Return. Confirm the commit is on the
remote branch:

```ridealong
git -C /host-agent-files/readspaces/repos/UnitedFederationOfAgents/AI-evo1 fetch --prune
git -C /host-agent-files/readspaces/repos/UnitedFederationOfAgents/AI-evo1 log --oneline "origin/$BRANCH" 2>/dev/null | head -5 || echo "Branch not yet visible — check watch log for push errors"
```

## Cleanup

```ridealong
./dungeon-keeper slopspace delete "$SLOP_ID"
```

```ridealong
./dungeon-keeper readspace repo delete UnitedFederationOfAgents/AI-evo1
./dungeon-keeper writespace repo delete UnitedFederationOfAgents/AI-evo1
```

## Summary

Auto-sync introduces two automatic lifecycle hooks:

| Hook | When | What it does |
|------|------|--------------|
| Pre-deploy sync | During `slopspace deploy` | Pulls latest commits into readspace and writespace repos |
| Post-return sync | During `slopspace return` | Commits and pushes all writespace repo changes |

### Directory Structure with readspaces-secure

```
/host-agent-files/
└── slopspaces/
    └── <slopspace-id>/
        ├── readspaces/
        │   └── repos/
        │       └── UnitedFederationOfAgents/
        │           └── AI-evo1/   # Content WITHOUT .git (moved to secure)
        ├── readspaces-secure/     # NEW: mirrors writespaces-secure
        │   └── repos/
        │       └── UnitedFederationOfAgents/
        │           └── AI-evo1/   # The .git directory is HERE for sync pulls
        ├── writespaces/
        │   └── repos/
        │       └── UnitedFederationOfAgents/
        │           └── AI-evo1/   # Content WITHOUT .git (moved to secure)
        ├── writespaces-secure/
        │   └── repos/
        │       └── UnitedFederationOfAgents/
        │           └── AI-evo1/   # The .git directory is HERE
        └── SLOPSPACE.json         # Contains: "sync_mode": "auto-sync"
```

### Key Commands Reference

| Command | Description |
|---------|-------------|
| `slopspace create [--sync-mode auto-sync]` | Create slopspace with specified sync mode |
| `slopspace deploy <id>` | Deploy and trigger pre-deploy sync |
| `slopspace return <id>` | Return and trigger post-return sync (commit + push) |
| `slopspace write <id> all` | Manual write (still available; auto-sync makes it optional) |
