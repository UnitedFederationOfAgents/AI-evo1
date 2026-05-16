# Ridealong: Claudomation Execution Example

This ridealong walks through the full flow: creating a slopspace, running the execution example via terraform, and validating that the work completed. It assumes dungeon-keeper is already running in watch mode.

## Prerequisites

- `dungeon-keeper` is compiled and in PATH
- `dungeon-keeper watch` is running in a separate terminal (watching for work signals)
- `terraform` is installed

## Configuration

Set `USE_REAL_CLAUDE=true` to dispatch to real Claude. Omit it (or set it to anything else) to use `clod` (mock agent).

## Step 1: Navigate to This Example's Directory

```ridealong
cd "$(git rev-parse --show-toplevel)/claudomation/examples/execution"
echo "In: $PWD"
```

## Step 2: Initialize Terraform

```ridealong
terraform init
```

## Step 3: Create a Slopspace

```ridealong
export SLOPSPACE_ID=$(dungeon-keeper slopspace create | grep "^Created slopspace:" | sed 's/Created slopspace: //')
echo "[ridealong] Slopspace created: $SLOPSPACE_ID"
[ -n "$SLOPSPACE_ID" ] || { echo "ERROR: Could not create slopspace" >&2; exit 1; }
```

## Step 4: Apply Terraform

```ridealong
export AGENT=clod
export MODEL=opus
echo "[ridealong] agent=$AGENT, model=$MODEL"
terraform apply -auto-approve -var="slopspace_id=$SLOPSPACE_ID" -var="agent=$AGENT" -var="model=$MODEL"
echo "[ridealong] terraform apply complete"
```

## Step 5: Validate the Result

Verify the slopspace was returned (no longer deployed) after the work completed.

```ridealong
export SLOPSPACE_JSON="${SLOPSPACES_DIR:-/host-agent-files/slopspaces}/$SLOPSPACE_ID/SLOPSPACE.json"
python3 -c "import json,sys; d=json.load(open('$SLOPSPACE_JSON')); d.get('deployed', False) and sys.exit('ERROR: Slopspace is still deployed — work may not have completed')"
echo "[ridealong] Validation passed: slopspace $SLOPSPACE_ID was returned after work completed"
echo "[ridealong] Writespaces available at: ${SLOPSPACES_DIR:-/host-agent-files/slopspaces}/$SLOPSPACE_ID/writespaces/"
```

## Step 6: Clean Up (Optional)

```ridealong
dungeon-keeper slopspace delete "$SLOPSPACE_ID"
echo "[ridealong] Cleanup complete"
```

---

## Running Without the Ridealong

To run the example manually with a pre-existing slopspace:

1. Create a slopspace:
   ```bash
   dungeon-keeper slopspace create
   ```
   Note the slopspace ID printed.

2. Initialize terraform (first time only):
   ```bash
   terraform init
   ```

3. Apply with your slopspace ID:
   ```bash
   terraform apply \
     -var="slopspace_id=<your-slopspace-id>" \
     -var="agent=clod" \
     -var="model=opus"
   ```
   Use `-var="agent=claude"` for real Claude.

4. To destroy the terraform state afterward:
   ```bash
   terraform destroy -var="slopspace_id=<your-slopspace-id>"
   ```

All variables have defaults except `slopspace_id`. See `variables.tf` for the full list.
