locals {
  unix_timestamp = time_static.assignment_time.unix

  # All names derive from assignment_name + timestamp so they are a-priori-known
  # (no dependency on resource-computed attributes) and unique across runs.
  assignment_id  = "assign_${var.assignment_name}_${local.unix_timestamp}"
  branch_name    = "assign-${var.assignment_name}-${local.unix_timestamp}"
  repo_name      = "${var.assignment_name}-${local.unix_timestamp}"
  repo_full_name = "${var.github_owner}/${local.repo_name}"

  # Authenticated clone URL (PAT embedded — never logged)
  clone_url = "https://${var.github_pat}@github.com/${local.repo_full_name}.git"
}

# Capture creation time once so all derived names stay stable across applies.
resource "time_static" "assignment_time" {}

# Create a new repository for this assignment.
# Using a fresh repo keeps the evo1 repo free of assignment branches.
resource "github_repository" "assignment_repo" {
  name        = local.repo_name
  visibility  = "private"
  auto_init   = true
  description = "Assignment: ${var.assignment_name} (${local.unix_timestamp})"
}

# =============================================================================
# EXISTENCE CHECK: Use a-priori-known strings to detect whether the repo has
# been created, avoiding the "count cannot be determined until apply" error.
# =============================================================================

data "github_repositories" "assignment" {
  query = "user:${var.github_owner} ${local.repo_name} in:name"
}

locals {
  repo_match  = [for n in data.github_repositories.assignment.names : n if n == local.repo_name]
  repo_exists = length(local.repo_match) > 0
}

# Create the working branch where the agent will commit its changes.
resource "github_branch" "working_branch" {
  repository = github_repository.assignment_repo.name
  branch     = local.branch_name

  depends_on = [github_repository.assignment_repo]
}

# Run the full slopspace branch-isolation flow via Python (which wraps dungeon-keeper).
#
# The script handles the complete lifecycle:
#   1. Clone repo to writespaces (dungeon-keeper writespace repo clone)
#   2. Create slopspace            (dungeon-keeper slopspace create)
#   3. Add writespace repo         (dungeon-keeper slopspace add-writespace repo)
#   4. Deploy slopspace            (dungeon-keeper slopspace deploy)
#   5. Signal / await execution    (work signal JSONL or simulated commit)
#   6. Return slopspace            (dungeon-keeper slopspace return)
#   7. Push repo changes           (dungeon-keeper slopspace write)
#   8. Append each step to ledger
resource "terraform_data" "slopspace_flow" {
  triggers_replace = {
    assignment_id = local.assignment_id
  }

  provisioner "local-exec" {
    command = "python3 ${path.module}/scripts/slopspace_flow.py > /tmp/slopspace_flow_${local.unix_timestamp}.log 2>&1"

    environment = {
      ASSIGNMENT_ID      = local.assignment_id
      ASSIGNMENT_NAME    = var.assignment_name
      GITHUB_OWNER       = var.github_owner
      GITHUB_PAT         = var.github_pat
      REPO_FULL_NAME     = local.repo_full_name
      REPO_CLONE_URL     = local.clone_url
      BRANCH_NAME        = local.branch_name
      INSTRUCTION        = var.instruction
      AGENT_TYPE         = var.agent_type
      DK_BINARY          = var.dungeon_keeper_binary
      LEDGER_PATH        = var.ledger_path
      SLOPSPACES_DIR     = var.slopspaces_dir
      WORK_SIGNALS_DIR   = var.work_signals_dir
      EXECUTION_TIMEOUT  = tostring(var.execution_timeout_seconds)
      SIMULATE_EXECUTION = tostring(var.simulate_execution)
    }
  }

  depends_on = [github_branch.working_branch]
}

# Open a PR for review once execution has completed.
# This is open-loop: no automated feedback or reintegration is triggered.
resource "github_repository_pull_request" "review_pr" {
  title           = "Assignment: ${var.assignment_name} (${local.unix_timestamp})"
  body            = <<-EOT
    Branch-isolation flow completed for assignment **${var.assignment_name}**.

    **Assignment ID:** `${local.assignment_id}`
    **Branch:** `${local.branch_name}`
    **Repo:** `${local.repo_full_name}`

    The AI agent has completed execution. Changes are ready for review.
    This PR is open-loop — no automated reintegration will occur.
  EOT
  head_ref        = local.branch_name
  base_ref        = "main"
  base_repository = github_repository.assignment_repo.name

  depends_on = [terraform_data.slopspace_flow]
}
