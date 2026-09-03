# Agent Coordinator — Web Exposure POC: Initial Setup

## Purpose

Stand up a **safe, authenticated public path** to the agent-coordinator (AC)
browser UI so that exactly one external identity — a specific Google Account
(`jordan.edsall1@gmail.com`) — can reach it from anywhere, with **no open
inbound ports** on the host and **no unauthenticated request ever reaching AC**.

AC today serves HTTP + WebSocket on `:8083` for browsers and has no auth of its
own (`CheckOrigin` allows every origin). This POC wraps that endpoint rather
than modifying it.

Scope: single host, single user, minimal cloud footprint. Not production HA.

## Non-goals

- Exposing the `:8084` representable TCP port (local-representative
  connections). That stays tailnet-only or local; out of scope here.
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
- `-repr-port 8084` stays on loopback / tailnet only.

### oauth2-proxy
- `provider = "google"`
- `client_id` / `client_secret` — from a Google OAuth 2.0 Web client (manual
  step below)
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
- `tailscale funnel --bg 4180` (switch to `tailscale serve` if we decide
  tailnet-only instead of public)
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
   and the id/secret loaded into Secret Manager.
2. **Tailscale auth key** — mint a tagged, reusable, non-ephemeral pre-auth key
   in the Tailscale admin console (or via API); load into Secret Manager. Add
   `tag:ac-poc` and the Funnel grant to the tailnet policy file.

## Planned folder layout

```
agent-coordinator/web-exposure-poc/
  INITIAL-SETUP.md          # this document
  terraform/                # versions.tf, variables.tf, main.tf, outputs.tf
  config/                   # oauth2-proxy.cfg.tmpl, *.service units, startup-script.sh
```

## Bring-up sequence

1. Create/select the GCP project; `terraform apply` the API enablements and the
   Secret Manager secret containers.
2. Manual: create the Google OAuth client; mint the Tailscale auth key;
   generate the cookie secret; write all three into Secret Manager.
3. `terraform apply` the VM + service account + firewall + startup script.
4. Startup script converges: services up, `tailscale funnel` live at
   `https://<host>.<tailnet>.ts.net`.
5. Verify: the allowlisted Google account logs in and reaches the AC UI
   (including the WebSocket host list); a non-allowlisted account is rejected at
   oauth2-proxy.
6. Record the Funnel URL and the teardown steps.

## Teardown

`terraform destroy`; delete the Tailscale node and revoke the auth key; delete
the Google OAuth client and consent screen.

## Open questions

- **Public (Funnel) vs. tailnet-only (Serve).** Funnel is reachable from any
  browser, gated only by oauth2-proxy. Serve additionally requires the user to
  join the tailnet. This POC assumes Funnel; revisit if we want tailnet
  membership as a second factor.
- **Secrets delivery.** Secret Manager + VM service account (assumed here) vs.
  sensitive Terraform vars injected via instance metadata (simpler, weaker,
  readable by anyone with `instances.get`).
- **Host choice.** The reference is one `e2-micro`. If an always-on non-cloud
  host is available, the same Tailscale + oauth2-proxy + AC stack runs there
  with zero GCP resources — preferred if such a host exists.
- **AC origin checks.** AC's WebSocket `CheckOrigin` currently allows all
  origins; with a fixed Funnel hostname we can tighten it to that origin.
- **Session lifetime** and whether to set `skip_provider_button`.
