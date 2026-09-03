# Prompt

[AutolaunchChains](../AutolaunchChains.md)

We will create a subfolder in agent-coordinator called 'web-exposure-poc' where we will create a document INITIAL-SETUP.md explaining how we will use tailscale and oauth2-proxy to create a safe exposure path to agent-coordinator.

Note that we have GCP, AWS, and Azure capabilities available for our use. We will prefer GCP but minimize use of cloud resources generally. The first identity we allow to connect will be a Google Account.

We will be using terraform as much as possible to stand up the resources we need.

Let's concisely record our full plan at a high level in INITIAL-SETUP.md.


prompt: [`d00d209`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/d00d20959c59a9bccd9eea501ac39d15a415765f) → reply: [`902f54f`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/902f54fd95b8b4f2957a344c54433fd0500502df)

## Reply

Created `agent-coordinator/web-exposure-poc/INITIAL-SETUP.md` recording the high-level plan for a safe web-exposure path to agent-coordinator. The plan layers Tailscale Funnel (public HTTPS ingress with no inbound firewall ports) in front of oauth2-proxy (Google OIDC login restricted to a single allowlisted Gmail address), which reverse-proxies to AC bound to loopback, all on one free-tier GCP `e2-micro` stood up via Terraform (compute instance, locked-down firewall, service account, Secret Manager, startup script). It also documents the two unavoidably manual steps (creating the Google OAuth client and the Tailscale auth key), the planned `terraform/` + `config/` folder layout, bring-up/teardown sequences, and open questions (Funnel vs. Serve, secrets delivery, host choice, tightening AC's WebSocket origin check).


## <REPLACE-Revision|Retry> A

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
