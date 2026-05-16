resource "terraform_data" "execute" {
  triggers_replace = [var.execution_details.slopspace_id]

  provisioner "local-exec" {
    command = "python3 ${path.module}/execute.py"

    environment = {
      SLOPSPACES_DIR   = var.execution_details.slopspaces_dir
      WORK_SIGNALS_DIR = var.execution_details.work_signal_dir
      SLOPSPACE_ID     = var.execution_details.slopspace_id
      PROMPT           = var.execution_details.prompt
      AGENT            = var.execution_details.agent
      MODEL            = var.execution_details.model
    }
  }
}
