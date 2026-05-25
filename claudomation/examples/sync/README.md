# sync

A Terraform-driven claudomation example that demonstrates **auto-sync**: dungeon-keeper
automatically commits and pushes the agent's changes to a remote branch after Return,
with no manual `slopspace write` call required.

## Usage

```hcl
module "execution" {
  source = "../../modules/execution"
}
```

## Requirements

| Name      | Version |
| --------- | ------- |
| terraform | >= 1.0  |

## Setup (before terraform apply)

Repo setup is done outside terraform so the slopspace can be pre-populated:

```bash
# 1. Clone canonical copies
dungeon-keeper readspace repo clone UnitedFederationOfAgents/AI-evo1
dungeon-keeper writespace repo clone UnitedFederationOfAgents/AI-evo1

# 2. Create slopspace, add repos
SLOPSPACE_ID=$(dungeon-keeper slopspace create | grep "^Created slopspace:" | sed 's/Created slopspace: //')
dungeon-keeper slopspace add-readspace repo "$SLOPSPACE_ID" UnitedFederationOfAgents/AI-evo1
dungeon-keeper slopspace add-writespace repo "$SLOPSPACE_ID" UnitedFederationOfAgents/AI-evo1 --ref my-branch
```

## Inputs

| Name           | Description                                              | Required |
| -------------- | -------------------------------------------------------- | -------- |
| slopspace\_id  | Pre-created slopspace ID (with repos already added)      | yes      |
| slopspaces\_dir | Path to slopspaces directory                            | no       |
| work\_signal\_dir | Path to work signals directory                        | no       |
| prompt         | Prompt for the agent                                     | no       |
| agent          | Agent binary (`clod` or `claude`)                        | no       |
| model          | Model name                                               | no       |

## Outputs

| Name   | Description                  |
| ------ | ---------------------------- |
| result | Output from execution module |
