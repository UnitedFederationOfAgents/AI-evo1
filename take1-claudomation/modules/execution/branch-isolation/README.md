# branch-isolation

A Terraform module that executes a full branch-isolation flow for an AI assignment.

## What it does

1. Creates a fresh GitHub repository for the assignment (keeps the evo1 repo clean)
2. Creates a working branch in that repo
3. Runs `slopspace_flow.py`, which wraps `dungeon-keeper` to:
   - Clone the repo into the writespaces directory
   - Create a slopspace and populate its writespace with the repo at the working branch
   - Deploy the slopspace to the specified agent type
   - Signal execution (real work signal or simulated commit for tests)
   - Return the slopspace and push committed changes back to GitHub
   - Append each step to an irreversible JSONL ledger
4. Opens a PR for review (open-loop — no reintegration)

## Usage

```hcl
module "branch_isolation" {
  source = "../../modules/execution/branch-isolation"

  assignment_name = "my-task"
  github_owner    = "my-org"
  github_pat      = var.github_pat
  instruction     = "Add a HELLO.md file with a greeting."
}
```

## Inputs

| Name | Description | Required | Default |
|------|-------------|----------|---------|
| `assignment_name` | Short URL-safe name for this assignment | yes | — |
| `github_owner` | GitHub owner where the assignment repo is created | yes | — |
| `github_pat` | GitHub PAT with repo write permissions | yes | — |
| `instruction` | Instruction passed to the AI agent | yes | — |
| `agent_type` | dungeon-keeper agent type | no | `agent-worker` |
| `dungeon_keeper_binary` | Path to dungeon-keeper binary | no | `dungeon-keeper` |
| `slopspaces_dir` | Slopspace storage directory | no | `/host-agent-files/slopspaces` |
| `work_signals_dir` | Work signal directory | no | `/host-agent-files/work` |
| `ledger_path` | Append-only JSONL ledger path | no | `/host-agent-files/ledger.jsonl` |
| `execution_timeout_seconds` | Seconds to wait for agent completion | no | `3600` |
| `simulate_execution` | Skip real agent; make a simulated commit (for tests) | no | `false` |

## Outputs

| Name | Description |
|------|-------------|
| `assignment_id` | Unique assignment identifier |
| `repo_full_name` | `owner/repo` of the created repository |
| `branch_name` | Working branch name |
| `pr_url` | URL of the review PR |
| `pr_number` | PR number |
