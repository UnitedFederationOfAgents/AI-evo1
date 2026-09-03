# Prompt

[AutolaunchChains](../AutolaunchChains.md)

We will create a subfolder in agent-coordinator called 'web-exposure-poc' where we will create a document INITIAL-SETUP.md explaining how we will use tailscale and oauth2-proxy to create a safe exposure path to agent-coordinator.

Note that we have GCP, AWS, and Azure capabilities available for our use. We will prefer GCP but minimize use of cloud resources generally. The first identity we allow to connect will be a Google Account.

We will be using terraform as much as possible to stand up the resources we need.

Let's concisely record our full plan at a high level in INITIAL-SETUP.md.


prompt: [`d00d209`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/d00d20959c59a9bccd9eea501ac39d15a415765f) → reply: [`902f54f`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/902f54fd95b8b4f2957a344c54433fd0500502df)

## Reply

Created `agent-coordinator/web-exposure-poc/INITIAL-SETUP.md` recording the high-level plan for a safe web-exposure path to agent-coordinator. The plan layers Tailscale Funnel (public HTTPS ingress with no inbound firewall ports) in front of oauth2-proxy (Google OIDC login restricted to a single allowlisted Gmail address), which reverse-proxies to AC bound to loopback, all on one free-tier GCP `e2-micro` stood up via Terraform (compute instance, locked-down firewall, service account, Secret Manager, startup script). It also documents the two unavoidably manual steps (creating the Google OAuth client and the Tailscale auth key), the planned `terraform/` + `config/` folder layout, bring-up/teardown sequences, and open questions (Funnel vs. Serve, secrets delivery, host choice, tightening AC's WebSocket origin check).


## Revision A

Let's refactor our current approach and put it in a document called INITIAL-CLOUD.md -- we want to update INITIAL-SETUP.md so that it hosts on our local server and uses tailscale to bring traffic in through oauth-proxy.

In a later increment we will host agent-coordinator on cloud compute, but we will want to solve our TCP machine auth problem before getting to that stage.

For our very first step we will use cloud resources only for what we need for auth configuration with identity providers like Google.

Let's update our INITIAL-SETUP.md so that it covers this localhost path but let's preserve the cloud-compute path separately.

We can also revise to work in the answers to these questions:
Public (Funnel) vs. tailnet-only (Serve) -- we will use Funnel
Secrets delivery -- We will use cloud secrets managers when we go to the cloud, we will use a local secret store for INITIAL-SETUP and feed the values in through env vars
Host choice -- for INITIAL-SETUP we'll use localhost, for INITIAL-CLOUD we are happy with e2-micro

For 'AC origin checks' and 'Session lifetime' we will elaborate on the questions.


prompt: [`e68a5f5`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/e68a5f5658d915ff3c035b4dc2d70445ee60d496) → reply: [`73b59e0`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/73b59e017d3d68f129021ec55aa6d359224fc360)

## Reply A

Rewrote `INITIAL-SETUP.md` as the **localhost path**: AC and the full oauth2-proxy chain run as local processes on the always-on workstation, with Tailscale Funnel providing public HTTPS ingress and the only cloud dependency being the Google OAuth client needed for "Sign in with Google." I folded in the resolved decisions (Funnel over Serve, localhost host, local secret store injected via env vars, optional one-time `tailscale up` instead of a minted auth key) and expanded the "AC origin checks" and "Session lifetime" open questions into detailed sub-questions rather than one-liners.

Moved the cloud-compute design into a new `INITIAL-CLOUD.md`, reframed as a later increment gated on first solving machine-to-machine auth for the unauthenticated `:8084` representable-TCP port, keeping the `e2-micro` host, GCP Secret Manager + VM service account for secrets, and the shared OAuth client, with its origin-check and session-lifetime questions cross-linked back to `INITIAL-SETUP.md`.


## Revision B

Let's implement our INITIAL-SETUP.md as a ridealong. (We can see existing 'tour' documents in the repo for references)

We will explain the manual steps in the human-readable text of the document.

We will create (if does not exist) and use the ~/usa-web-init folder (and optional subfolders) to hold any secrets and/or generated IDs we need rather than putting them in the scope of AI-evo1.

Let's create this ridealong now -- when we have completed its execution we should have our instance of agent-coordinator web-accessible.
