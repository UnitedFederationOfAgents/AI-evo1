# execution
# A Terraform module

resource "null_resource" "example" {
  triggers = {
    always_run = timestamp()
  }
}
