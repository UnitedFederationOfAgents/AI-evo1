# ---------------------------------------------------------------------------------------------------------------------
# REQUIRED PARAMETERS
# ---------------------------------------------------------------------------------------------------------------------

variable "slopspace_id" {
  description = "The ID of the pre-created slopspace to use. Create one with: dungeon-keeper slopspace create"
  type        = string
}

# ---------------------------------------------------------------------------------------------------------------------
# OPTIONAL PARAMETERS
# ---------------------------------------------------------------------------------------------------------------------

variable "slopspaces_dir" {
  description = "Path to the slopspaces directory."
  type        = string
  default     = "/host-agent-files/slopspaces"
}

variable "work_signal_dir" {
  description = "Path to the work signals directory."
  type        = string
  default     = "/host-agent-files/work"
}

variable "prompt" {
  description = "The prompt to send to the agent."
  type        = string
  default     = "Our nice agent should create the file writespaces/repos/UnitedFederationOfAgents/AI-evo1/dungeon-keeper/docs/claudomation-sync-result.md containing a one-line message: 'Auto-synced by claudomation sync example.'"
}

variable "agent" {
  description = "The agent binary to use ('claude' for real Claude, 'clod' for mock)."
  type        = string
  default     = "clod"
}

variable "model" {
  description = "The model to use with the agent."
  type        = string
  default     = "opus"
}
