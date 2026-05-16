module "execution" {
  source = "../../modules/execution"

  execution_details = {
    slopspaces_dir  = var.slopspaces_dir
    work_signal_dir = var.work_signal_dir
    slopspace_id    = var.slopspace_id
    prompt          = var.prompt
    agent           = var.agent
    model           = var.model
  }
}
