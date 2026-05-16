terraform {
  required_version = ">= 1.0"
}

provider "github" {
  token = var.github_pat
  owner = var.github_owner
}
