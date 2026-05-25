# Ridealong: Claudomation Sync Example

This ridealong walks through the sync flow via claudomation: repos are added to a slopspace,
terraform deploys the agent, and the agent's changes are **automatically committed and pushed**
by dungeon-keeper's auto-sync — no manual `slopspace write` step required.

## Prerequisites

- `dungeon-keeper` is compiled and in PATH
- `dungeon-keeper watch` is running in a separate terminal
- `terraform` is installed
- `TF_VAR_github_pat` is set with a valid GitHub PAT

## Step 1: Navigate to This Example's Directory

```ridealong
cd "$(git rev-parse --show-toplevel)/claudomation/examples/sync"
echo "In: $PWD"
```

## Step 2: Initialize Terraform

```ridealong
terraform init
```

## Step 3: Clone Repositories into Canonical Locations

These clones are the persistent originals that dungeon-keeper syncs from. Run from anywhere:

```ridealong
dungeon-keeper readspace repo clone UnitedFederationOfAgents/AI-evo1
dungeon-keeper writespace repo clone UnitedFederationOfAgents/AI-evo1
```

## Step 4: Create a Slopspace and Add Repos

Choose a branch name, create the slopspace (auto-sync is the default), and add repos:

```ridealong
export BRANCH="claudomation/sync-demo-$(date +%Y%m%d)-$(cat /dev/urandom | tr -dc 'a-z0-9' | head -c 2)"
echo "Branch: $BRANCH"
```

```ridealong
export SLOPSPACE_ID=$(dungeon-keeper slopspace create | grep "^Created slopspace:" | sed 's/Created slopspace: //')
echo "[ridealong] Slopspace created: $SLOPSPACE_ID"
[ -n "$SLOPSPACE_ID" ] || { echo "ERROR: Could not create slopspace" >&2; exit 1; }
```

```ridealong
dungeon-keeper slopspace add-readspace repo "$SLOPSPACE_ID" UnitedFederationOfAgents/AI-evo1
dungeon-keeper slopspace add-writespace repo "$SLOPSPACE_ID" UnitedFederationOfAgents/AI-evo1 --ref "$BRANCH"
echo "[ridealong] Repos added to slopspace"
```

## Step 5: Apply Terraform

Terraform deploys the slopspace (triggering pre-deploy sync) and runs the agent.
When the agent finishes, dungeon-keeper calls Return and triggers post-return sync
(auto-commit + push to `$BRANCH`):

```ridealong
export AGENT=clod
export MODEL=opus
echo "[ridealong] agent=$AGENT, model=$MODEL"
terraform apply -auto-approve -var="slopspace_id=$SLOPSPACE_ID" -var="agent=$AGENT" -var="model=$MODEL"
echo "[ridealong] terraform apply complete"
```

## Step 6: Validate the Result

Check that the slopspace was returned and that the file was written to the writespace:

```ridealong
export SLOPSPACE_JSON="${SLOPSPACES_DIR:-/host-agent-files/slopspaces}/$SLOPSPACE_ID/SLOPSPACE.json"
python3 -c "import json,sys; d=json.load(open('$SLOPSPACE_JSON')); d.get('deployed', False) and sys.exit('ERROR: Slopspace is still deployed — work may not have completed')"
echo "[ridealong] Validation passed: slopspace $SLOPSPACE_ID was returned"
```

```ridealong
cat "${SLOPSPACES_DIR:-/host-agent-files/slopspaces}/$SLOPSPACE_ID/writespaces/repos/UnitedFederationOfAgents/AI-evo1/dungeon-keeper/docs/claudomation-sync-result.md" 2>/dev/null || echo "File not found — check watch loop log"
```

## Step 7: Verify Auto-Sync Pushed to Remote

The agent's commit should already be on `$BRANCH` with no manual `slopspace write` required:

```ridealong
git -C /host-agent-files/readspaces/repos/UnitedFederationOfAgents/AI-evo1 fetch --prune
git -C /host-agent-files/readspaces/repos/UnitedFederationOfAgents/AI-evo1 log --oneline "origin/$BRANCH" 2>/dev/null | head -5 || echo "Branch not visible — check watch log for push errors"
```

## Step 8: Clean Up (Optional)

```ridealong
dungeon-keeper slopspace delete "$SLOPSPACE_ID"
dungeon-keeper readspace repo delete UnitedFederationOfAgents/AI-evo1
dungeon-keeper writespace repo delete UnitedFederationOfAgents/AI-evo1
echo "[ridealong] Cleanup complete"
```

---

## Running Without the Ridealong

1. Clone repos into canonical locations:
   ```bash
   dungeon-keeper readspace repo clone UnitedFederationOfAgents/AI-evo1
   dungeon-keeper writespace repo clone UnitedFederationOfAgents/AI-evo1
   ```

2. Create a slopspace and add repos:
   ```bash
   SLOPSPACE_ID=$(dungeon-keeper slopspace create | grep "^Created slopspace:" | sed 's/Created slopspace: //')
   dungeon-keeper slopspace add-readspace repo "$SLOPSPACE_ID" UnitedFederationOfAgents/AI-evo1
   dungeon-keeper slopspace add-writespace repo "$SLOPSPACE_ID" UnitedFederationOfAgents/AI-evo1 --ref my-branch
   ```

3. Initialize terraform (first time only):
   ```bash
   terraform init
   ```

4. Apply:
   ```bash
   terraform apply \
     -var="slopspace_id=$SLOPSPACE_ID" \
     -var="agent=clod" \
     -var="model=opus"
   ```

All variables have defaults except `slopspace_id`. See `variables.tf` for the full list.
