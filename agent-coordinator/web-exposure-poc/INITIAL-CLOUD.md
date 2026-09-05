# Agent Coordinator — Web Exposure POC: Cloud-Compute Path

> **Status: later increment, not the first step.** The first step is
> [INITIAL-SETUP.md](INITIAL-SETUP.md), which runs the whole stack on the local
> server and uses cloud resources only for identity-provider (Google) auth
> configuration. This document captures the follow-on increment where AC itself
> runs on cloud compute. It is preserved here so the localhost doc stays small.

## Prerequisite before doing this

Moving AC onto a cloud VM means the `:8084` representable-TCP port (used for
local-representative connections) either moves with it or is reached across the
network. Either way we must **solve machine-to-machine auth for that TCP port
first** — it has no auth today. Do not start this increment until that problem
is solved; otherwise a cloud-hosted AC widens the attack surface on an
unauthenticated port.

## Purpose

Same goal as the localhost path: a **safe, authenticated public path** to the
agent-coordinator (AC) browser UI so that exactly one external identity — a
specific Google Account (`jordan.edsall1@gmail.com`) — can reach it from
anywhere, with **no open inbound ports** on the host and **no unauthenticated
request ever reaching AC**. The difference is only *where AC runs*: here it runs
on a small GCP VM instead of the local workstation.

Scope: single host, single user, minimal cloud footprint. Not production HA.

## Non-goals

- Exposing the `:8084` representable TCP port publicly. It stays tailnet-only or
  local, and it must have machine auth (see prerequisite) before AC runs in the
  cloud at all.
- Multi-user or group-based authorization. One allowlisted email for now; the
  allowlist file is the extension point.
- Autoscaling, HA, blue/green, custom DNS. One small VM, Tailscale-supplied
  hostname and TLS.

## Design in one picture

```
Browser (any network)
  │  HTTPS + WSS
  ▼
Tailscale Funnel ingress   (*.ts.net, TLS by Tailscale, no VM inbound ports)
  │  loopback
  ▼
oauth2-proxy  (127.0.0.1:4180)  ──► Google OIDC login + email allowlist
  │  loopback, only after auth
  ▼
agent-coordinator  (127.0.0.1:8083)   HTTP + WebSocket
```

Everything runs on **one GCP Compute Engine `e2-micro`** (free-tier-eligible
region). `tailscaled` on the VM dials **outbound** to Tailscale; Funnel carries
inbound traffic back over that WireGuard tunnel, so the VM needs **zero inbound
firewall rules**.

## Decisions carried in

- **Ingress: Tailscale Funnel** (public HTTPS), not `tailscale serve`
  (tailnet-only). Gate is oauth2-proxy. Revisit only if we later want tailnet
  membership as an explicit second factor.
- **Host: `e2-micro`.** Free-tier-eligible, Debian 12, Shielded VM.
- **Secrets: GCP Secret Manager + VM service account.** The VM's service
  account gets `roles/secretmanager.secretAccessor` on exactly the secrets it
  needs; nothing is baked into the image or instance metadata. (The localhost
  path instead uses a local secret store fed in via env vars; the cloud path is
  where a cloud secret manager earns its place.)

## Why both Tailscale and oauth2-proxy

They solve different problems and are layered:

| Layer            | Component                | Job                                                                                          |
|------------------|--------------------------|---------------------------------------------------------------------------------------------|
| Ingress / transport | Tailscale Funnel      | Public HTTPS endpoint, automatic TLS, ingress via Tailscale infra, origin IP hidden, no inbound ports on the VM |
| Identity gate    | oauth2-proxy             | Every request must carry a valid session; login is Google OIDC; only allowlisted emails pass |
| Blast radius     | AC bound to `127.0.0.1`  | AC is unreachable except through the proxy chain                                              |
| Host access      | Tailscale SSH            | Admin reaches the box over the tailnet; public port 22 stays closed                          |

## Components & configuration

### agent-coordinator
- Run under systemd as `agent-coordinator -port 8083`, reachable only on
  loopback (front it so nothing but oauth2-proxy connects).
- `-repr-port 8084` stays on loopback / tailnet only, and only after machine
  auth exists for it (prerequisite above).

### oauth2-proxy
- `provider = "google"`
- `client_id` / `client_secret` — from a Google OAuth 2.0 Web client (manual
  step below), delivered via Secret Manager
- `redirect_url = "https://<host>.<tailnet>.ts.net/oauth2/callback"`
- `upstreams = ["http://127.0.0.1:8083"]`, `reverse_proxy = true` (proxies
  WebSocket upgrades)
- `authenticated_emails_file` = a one-line file with the allowed Gmail address
  (not `email_domain`, since it is a single consumer account)
- `cookie_secret` = 32 random bytes; `cookie_secure = true`; bounded
  `cookie_expire`
- Listens on `127.0.0.1:4180`

### Tailscale
- `tailscale up --ssh --advertise-tags=tag:ac-poc --authkey=<key>`
- `tailscale funnel --bg 4180`
- Tailnet policy: define `tag:ac-poc`, grant it `funnel`, and optionally
  restrict who may SSH to the node.

## GCP resources (Terraform)

Kept deliberately small:

- `google_project_service` — enable `compute.googleapis.com`,
  `secretmanager.googleapis.com`
- `google_service_account` — VM identity, no keys; granted only
  `roles/secretmanager.secretAccessor` on the three secrets below
- `google_compute_instance` — `e2-micro`, Debian 12, ~10 GB standard PD,
  ephemeral external IP for **egress only**, Shielded VM enabled
- `google_compute_firewall` — default-deny inbound (no allow rules added);
  egress left at default. No `0.0.0.0/0:22`, no `:8083`, no `:4180`
- `google_secret_manager_secret` (+ versions) — `oauth-client-id`,
  `oauth-client-secret`, `tailscale-authkey`. Secret **values** supplied out of
  band (sensitive TF vars or manual `gcloud`), never committed
- `metadata.startup-script` — installs Tailscale + oauth2-proxy + the AC
  binary, writes systemd units, pulls secrets, runs `tailscale up` and
  `tailscale funnel`

No load balancer, no static IP, no Cloud NAT, no DNS zone — Tailscale provides
the hostname and certificate.

### Manual steps (not cleanly Terraformable)

1. **Google OAuth client** — create an OAuth consent screen (External) plus an
   OAuth 2.0 Web Client in the GCP console; scopes `openid email profile`;
   authorized redirect URI = the Funnel callback URL. There is no first-class
   Terraform resource for a generic OAuth client, so this is done once by hand
   and the id/secret loaded into Secret Manager. This is the **same OAuth
   client** the localhost path creates; only the redirect URI set differs
   (add the `e2-micro` Funnel callback URL alongside the localhost one).
2. **Tailscale auth key** — mint a tagged, reusable, non-ephemeral pre-auth key
   in the Tailscale admin console (or via API); load into Secret Manager. Add
   `tag:ac-poc` and the Funnel grant to the tailnet policy file.

## Planned folder layout

```
agent-coordinator/web-exposure-poc/
  INITIAL-CLOUD.md          # this document
  terraform/
    cloud/                  # versions.tf, variables.tf, main.tf, outputs.tf for the VM stack
  config/                   # oauth2-proxy.cfg.tmpl, *.service units, startup-script.sh
```

## Bring-up sequence

1. Confirm the `:8084` machine-auth prerequisite is met.
2. Create/select the GCP project; `terraform apply` the API enablements and the
   Secret Manager secret containers.
3. Manual: reuse the Google OAuth client (add the Funnel redirect URI); mint the
   Tailscale auth key; generate the cookie secret; write all three into Secret
   Manager.
4. `terraform apply` the VM + service account + firewall + startup script.
5. Startup script converges: services up, `tailscale funnel` live at
   `https://<host>.<tailnet>.ts.net`.
6. Verify: the allowlisted Google account logs in and reaches the AC UI
   (including the WebSocket host list); a non-allowlisted account is rejected at
   oauth2-proxy.
7. Record the Funnel URL and the teardown steps.

## Teardown

`terraform destroy`; delete the Tailscale node and revoke the auth key; if the
OAuth client is no longer used by the localhost path either, delete it and the
consent screen, otherwise just remove the Funnel redirect URI.

## Open questions

- **AC origin checks.** Same question as the localhost path — see
  [INITIAL-SETUP.md](INITIAL-SETUP.md#open-questions). The fixed Funnel hostname
  is different per environment, so whatever mechanism we choose (flag/env for the
  allowed `Origin`) has to take the value per deployment.
- **Session lifetime.** Same question as the localhost path — see
  [INITIAL-SETUP.md](INITIAL-SETUP.md#open-questions). On a shared cloud VM we
  may additionally want a session store rather than pure cookie sessions so that
  logout / revocation is immediate server-side.
- **Machine auth for `:8084`.** Tracked as the prerequisite above; the concrete
  mechanism (mTLS, Tailscale identity headers, a shared bootstrap token) is
  chosen in that separate piece of work, not here.
