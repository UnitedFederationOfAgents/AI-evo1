variable "github_pat" {
  description = "GitHub personal access token with repo write permissions."
  type        = string
  sensitive   = true
}

variable "github_owner" {
  description = "GitHub owner (user or org) where the assignment repo will be created."
  type        = string
}

variable "assignment_name" {
  description = "Short URL-safe name for this assignment."
  type        = string
  default     = "claudomation-test"
}

variable "instruction" {
  description = "Instruction to pass to the AI agent."
  type        = string
  default     = "Add a HELLO.md file containing the text 'Hello from the branch-isolation flow.'."
}

variable "agent_type" {
  description = "dungeon-keeper agent type."
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
  description = "Append-only JSONL ledger path."
  type        = string
  default     = "/host-agent-files/ledger.jsonl"
}

variable "simulate_execution" {
  description = "Skip real agent execution and make a simulated commit instead (for tests)."
  type        = bool
  default     = false
}
