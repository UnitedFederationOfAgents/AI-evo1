# Example: branch-isolation flow
#
# Creates a fresh GitHub repo for the assignment, runs the full slopspace
# branch-isolation flow (create → populate → deploy → execute → return → push),
# and opens a PR for review.
#
# A separate pr-collector module is instantiated to demonstrate scanning
# the resulting PR for comments.  On first apply the PR has just been created
# so comments will be empty; subsequent applies will pick up any new comments.

module "branch_isolation" {
  source = "../../../modules/execution/branch-isolation"

  assignment_name = var.assignment_name
  github_owner    = var.github_owner
  github_pat      = var.github_pat
  instruction     = var.instruction

  agent_type            = var.agent_type
  dungeon_keeper_binary = var.dungeon_keeper_binary
  slopspaces_dir        = var.slopspaces_dir
  work_signals_dir      = var.work_signals_dir
  ledger_path           = var.ledger_path
  simulate_execution    = var.simulate_execution
}

# Collect PR state and comments from the review PR.
# On first apply conclusion_state will be "active" (PR just opened).
module "pr_collector" {
  source = "../../../modules/pr-collector"

  github_owner         = var.github_owner
  github_pat           = var.github_pat
  repo_name            = split("/", module.branch_isolation.repo_full_name)[1]
  pr_number            = module.branch_isolation.pr_number
  skip_existence_check = true
}
