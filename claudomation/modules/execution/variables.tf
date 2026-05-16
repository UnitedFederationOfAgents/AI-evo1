# ---------------------------------------------------------------------------------------------------------------------
# REQUIRED PARAMETERS
# These parameters must be supplied when consuming this module.
# ---------------------------------------------------------------------------------------------------------------------

variable "execution_details" {
  description = "Details for the execution to be performed via dungeon-keeper."
  type = object({
    slopspaces_dir  = string
    work_signal_dir = string
    slopspace_id    = string
    prompt          = string
    agent           = string
    model           = string
  })
}
