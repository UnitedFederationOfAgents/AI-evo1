# ---------------------------------------------------------------------------------------------------------------------
# REQUIRED PARAMETERS
# ---------------------------------------------------------------------------------------------------------------------

variable "assignment_name" {
  description = "Name of this assignment (used for repo and branch naming). Must be short and URL-safe."
  type        = string
}

variable "github_owner" {
  description = "GitHub owner (user or org) where the assignment repo will be created. Must be known a priori."
  type        = string
}

variable "github_pat" {
  description = "GitHub personal access token with repo write permissions."
  type        = string
  sensitive   = true
}

variable "instruction" {
  description = "The instruction to pass to the AI agent for this assignment."
  type        = string
}

# ---------------------------------------------------------------------------------------------------------------------
# OPTIONAL PARAMETERS
# ---------------------------------------------------------------------------------------------------------------------

variable "agent_type" {
  description = "Agent type for dungeon-keeper deployment (agent-worker or heuristic-request)."
  type        = string
  default     = "agent-worker"
}

variable "dungeon_keeper_binary" {
  description = "Path to the dungeon-keeper binary."
  type        = string
  default     = "dungeon-keeper"
}

variable "slopspaces_dir" {
  description = "Directory where slopspaces are stored."
  type        = string
  default     = "/host-agent-files/slopspaces"
}

variable "work_signals_dir" {
  description = "Directory where work signals are stored."
  type        = string
  default     = "/host-agent-files/work"
}

variable "ledger_path" {
  description = "Path to the append-only execution ledger file."
  type        = string
  default     = "/host-agent-files/ledger.jsonl"
}

variable "execution_timeout_seconds" {
  description = "Timeout in seconds to wait for agent execution to complete."
  type        = number
  default     = 3600
}

variable "simulate_execution" {
  description = <<-EOT
    When true, skip the real agent work signal and instead make a simulated
    commit to the branch. Useful for testing the Terraform infrastructure
    without requiring a running dungeon-keeper worker.
  EOT
  type        = bool
  default     = false
}
