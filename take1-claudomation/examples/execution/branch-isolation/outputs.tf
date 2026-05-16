output "assignment_id" {
  description = "Unique identifier for this assignment."
  value       = module.branch_isolation.assignment_id
}

output "repo_full_name" {
  description = "Full name (owner/repo) of the created assignment repository."
  value       = module.branch_isolation.repo_full_name
}

output "branch_name" {
  description = "Working branch name."
  value       = module.branch_isolation.branch_name
}

output "pr_url" {
  description = "URL of the review PR."
  value       = module.branch_isolation.pr_url
}

output "pr_number" {
  description = "PR number."
  value       = module.branch_isolation.pr_number
}

output "pr_conclusion_state" {
  description = "Current PR conclusion state (active / merged / closed / not_found)."
  value       = module.pr_collector.conclusion_state
}

output "pr_comments_json" {
  description = "JSON-encoded map of comments on the review PR."
  value       = module.pr_collector.comments_json
}
