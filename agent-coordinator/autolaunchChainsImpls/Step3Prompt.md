# Prompt

[AutolaunchChains](../AutolaunchChains.md)

We will create a subfolder in agent-coordinator called 'web-exposure-poc' where we will create a document INITIAL-SETUP.md explaining how we will use tailscale and oauth2-proxy to create a safe exposure path to agent-coordinator.

Note that we have GCP, AWS, and Azure capabilities available for our use. We will prefer GCP but minimize use of cloud resources generally. The first identity we allow to connect will be a Google Account.

We will be using terraform as much as possible to stand up the resources we need.

Let's concisely record our full plan at a high level in INITIAL-SETUP.md.
