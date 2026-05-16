output "assignment_id" {
  description = "The unique ID for this assignment."
  value       = local.assignment_id
}

output "repo_full_name" {
  description = "The full name (owner/repo) of the created assignment repository."
  value       = local.repo_full_name
}

output "branch_name" {
  description = "The working branch name in the assignment repo."
  value       = local.branch_name
}

output "pr_url" {
  description = "The URL of the review PR submitted after execution."
  value       = github_repository_pull_request.review_pr.html_url
}

output "pr_number" {
  description = "The PR number for review."
  value       = github_repository_pull_request.review_pr.number
}
