# Agent Coordinator — Web Exposure POC: Initial Setup (localhost path)

## Purpose

Stand up a **safe, authenticated public path** to the agent-coordinator (AC)
browser UI so that exactly one external identity — a specific Google Account
(`jordan.edsall1@gmail.com`) — can reach it from anywhere, with **no open
inbound ports** on the host and **no unauthenticated request ever reaching AC**.

For this first step **AC and the whole proxy chain run on the local server**
(the existing always-on workstation where development happens). Tailscale brings
public traffic in and hands it to oauth2-proxy, which is the identity gate in
front of AC. The only cloud resource involved is what Google requires to
configure an OAuth client for "Sign in with Google" — no cloud compute, no
cloud networking, no cloud storage.

AC today serves HTTP + WebSocket on `:8083` for browsers and has no auth of its
own (`CheckOrigin` allows every origin). This POC wraps that endpoint rather
than modifying it.

Scope: single host (localhost), single user, essentially zero cloud footprint.
Not production HA.

## Executable version

[RIDEALONG.md](RIDEALONG.md) is the step-by-step, waypointed ridealong that
actually performs this setup. This document is the design: the rationale,
decisions, and open questions the ridealong carries in. Read this for the "why",
run `RIDEALONG.md` for the "how".

## Relationship to the cloud path

A later increment runs AC on cloud compute (`e2-micro`). That plan lives in
[INITIAL-CLOUD.md](INITIAL-CLOUD.md) and is intentionally kept separate so this
document stays small. **Before** that increment we must solve
machine-to-machine auth for the `:8084` representable-TCP port
(local-representative connections), which has no auth today; hosting AC in the
cloud without that would expose an unauthenticated port across the network.

## Non-goals

- Exposing the `:8084` representable TCP port (local-representative
  connections). That stays loopback / tailnet-only; out of scope here.
- Multi-user or group-based authorization. One allowlisted email for now; the
  allowlist file is the extension point.
- Any cloud compute, load balancer, static IP, Cloud NAT, or DNS zone.
  Tailscale supplies the hostname and TLS.

## Design in one picture

```
Browser (any network)
  │  HTTPS + WSS
  ▼
Tailscale Funnel ingress   (*.ts.net, TLS by Tailscale, no inbound ports on the local host)
  │  loopback
  ▼
oauth2-proxy  (127.0.0.1:4180)  ──► Google OIDC login + email allowlist
  │  loopback, only after auth
  ▼
agent-coordinator  (127.0.0.1:8083)   HTTP + WebSocket
```

Everything runs as local processes on the workstation. `tailscaled` on that
machine dials **outbound** to Tailscale; Funnel carries inbound traffic back
over that WireGuard tunnel, so the host needs **zero inbound firewall rules**
and no router/port-forward changes.

### Reaching condoccer through the same path

`condoccer` joins the autolaunch chain as a `representable` client of
local-representative (one instance per box, `--auto-connect`), and AC **reverse-
proxies its browser UI**: `https://<host>.<tailnet>.ts.net/host/<lr-name>/condoccer/`
→ that host's LR `/condoccer/` → condoccer on loopback. So the exact same
Tailscale + oauth2-proxy front door that gates AC also gates condoccer — no
second OAuth client, no second Funnel, no extra port. The only widening of scope
is that the one allowlisted identity can now also drive condocs on any connected
box; the unauthenticated `:8084` representable-TCP port is still **not** exposed.

## Decisions

- **Ingress: Tailscale Funnel** (public HTTPS), not `tailscale serve`
  (tailnet-only). The gate is oauth2-proxy; we do not require the browser user
  to join the tailnet. Revisit only if we later want tailnet membership as an
  explicit second factor.
- **Host: localhost.** The existing always-on workstation runs the stack. No
  `e2-micro` in this step (that is the cloud path).
- **Secrets: local secret store, injected via environment / flags at launch.**
  The OAuth client id/secret and the oauth2-proxy cookie secret live under
  `~/ufa-web-init/secrets/` (mode `0600`, outside the `AI-evo1` repo) and are
  read into the process only at start time — never committed, never in shell
  history. Cloud secret managers are deferred to the cloud path.
- **Tailscale auth**: the local machine is brought onto the tailnet with a
  one-time interactive `tailscale up`; a pre-auth key is optional here and, if
  used, also comes from the local store. No auth key needs to be minted for
  this step.
- **Single user**: `authenticated_emails_file` with one address, not
  `email_domain` (it is a single consumer Gmail account).

## Why both Tailscale and oauth2-proxy

They solve different problems and are layered:

| Layer            | Component                | Job                                                                                          |
|------------------|--------------------------|---------------------------------------------------------------------------------------------|
| Ingress / transport | Tailscale Funnel      | Public HTTPS endpoint, automatic TLS, ingress via Tailscale infra, origin IP hidden, no inbound ports on the host |
| Identity gate    | oauth2-proxy             | Every request must carry a valid session; login is Google OIDC; only allowlisted emails pass |
| Blast radius     | AC bound to `127.0.0.1`  | AC is unreachable except through the proxy chain                                              |

## Components & configuration

### agent-coordinator
- Run as `agent-coordinator -port 8083`, bound to loopback only, so nothing but
  oauth2-proxy can connect.
- `-repr-port 8084` stays on loopback / tailnet only.
- Process supervision: a systemd **user** unit (or a simple `run.sh` / `tmux`
  during the POC). No root required.

### oauth2-proxy
- `provider = "google"`
- `client_id` / `client_secret` — from a Google OAuth 2.0 Web client (manual
  step below); supplied at launch from the `~/ufa-web-init/secrets/` store
- `redirect_url = "https://<host>.<tailnet>.ts.net/oauth2/callback"`
- `upstreams = ["http://127.0.0.1:8083"]`, `reverse_proxy = true` (proxies
  WebSocket upgrades)
- `authenticated_emails_file` = a one-line file with the allowed Gmail address
  (not `email_domain`, since it is a single consumer account)
- `cookie_secret` = 32 random bytes from the local store; `cookie_secure =
  true`; bounded `cookie_expire` (see open questions)
- Listens on `127.0.0.1:4180`

### Tailscale
- One-time: `tailscale up --ssh --advertise-tags=tag:ac-poc` on the local host.
- `tailscale funnel --bg 4180` to publish oauth2-proxy at
  `https://<host>.<tailnet>.ts.net`.
- Tailnet policy: define `tag:ac-poc` and grant it `funnel`.

## Cloud resources (only what auth needs)

For this step the sole cloud dependency is the Google OAuth client used for
"Sign in with Google". There is no first-class Terraform resource for a generic
Google OAuth 2.0 client, so the client itself is created by hand; Terraform is
used for whatever *is* expressible so the setup is reproducible:

- `google_project` / `google_project_service` — a dedicated project with
  `iap.googleapis.com` / People API enabled as needed for the consent screen.
- Consent-screen and OAuth-client creation remain **manual** (see below).

That is the entire `terraform/` surface for this step. Everything else is local.

### Manual steps (not cleanly Terraformable)

1. **Google OAuth client** — in the GCP console, create an OAuth consent screen
   (User type: External; publishing status can stay "Testing" with the single
   allowlisted account added as a test user), then an OAuth 2.0 **Web
   application** client. Scopes: `openid email profile`. Authorized redirect
   URI: `https://<host>.<tailnet>.ts.net/oauth2/callback` (add
   `http://localhost:4180/oauth2/callback` too if we want to test the proxy
   without Funnel). Copy the client id/secret into `~/ufa-web-init/secrets/`.
2. **Cookie secret** — `openssl rand -base64 32`, stored alongside the OAuth
   secret in `~/ufa-web-init/secrets/`.

## Planned folder layout

```
agent-coordinator/web-exposure-poc/
  INITIAL-SETUP.md          # this document (localhost path — design)
  RIDEALONG.md              # the executable ridealong for this document
  INITIAL-CLOUD.md          # cloud-compute path, later increment
  terraform/
    auth/                   # project + API enablement for the Google OAuth client

~/ufa-web-init/             # secrets + generated IDs, outside the AI-evo1 repo
  secrets/                  # oauth-client-id, oauth-client-secret, cookie-secret (0600)
  config/                   # authenticated-emails.txt
  ids/                      # funnel-host.txt, redirect-url.txt, funnel-url.txt
  bin/                      # oauth2-proxy release binary, if not on PATH
  run/                      # *.log / *.pid for start/stop
```

## Bring-up sequence

1. (Optional) `terraform apply` in `terraform/auth/` to create/enable the
   Google project.
2. Manual: create the consent screen + OAuth Web client; generate the cookie
   secret; write client id, client secret, and cookie secret into
   `~/ufa-web-init/secrets/`; write the allowlist file.
3. One-time: `tailscale up` on the local host; add the `tag:ac-poc` + `funnel`
   grant to the tailnet policy.
4. Start AC on `127.0.0.1:8083`; start oauth2-proxy on `127.0.0.1:4180` with
   the secrets read from the local store.
5. `tailscale funnel --bg 4180`; note the `https://<host>.<tailnet>.ts.net`
   URL.
6. Verify: the allowlisted Google account logs in and reaches the AC UI
   (including the WebSocket host list); a non-allowlisted account is rejected at
   oauth2-proxy; AC is not reachable on any non-loopback address.
7. Record the Funnel URL and the teardown steps.

`RIDEALONG.md` performs steps 2–7 as waypointed, confirm-each-step commands.

## Teardown

Stop `tailscale funnel`; stop oauth2-proxy and AC; optionally `tailscale down`.
`terraform destroy` the auth project (or keep it — the OAuth client is reused by
the cloud path). Delete the OAuth client / consent screen only if the cloud path
will not use it.

## Open questions

### AC origin checks

AC's WebSocket handler currently uses a `CheckOrigin` that returns `true` for
every origin. With this proxy chain that is worth tightening, and the details
need to be worked out:

- **Is it actually exploitable here?** oauth2-proxy authenticates the request,
  but a browser on any site can still open a cross-origin WebSocket to the
  Funnel URL and the browser will attach the oauth2-proxy session cookie. If AC
  accepts any origin, that is a cross-site WebSocket hijacking path even with
  the proxy. So the origin check is not merely cosmetic.
- **What origin does AC even see?** Behind oauth2-proxy (which sets
  `reverse_proxy = true`), we need to confirm whether the `Origin` header
  reaching AC is the browser-facing `https://<host>.<tailnet>.ts.net` or
  something rewritten, and likewise for `Host`. The fix depends on that answer.
- **How should the allowed origin be supplied?** Preference is a
  flag/env-configured allowlist (e.g. `AC_ALLOWED_ORIGINS`) rather than a
  hard-coded string, so the same binary serves localhost dev, the Funnel
  hostname here, and a different Funnel hostname in the cloud path.
- **Complementary control:** setting `cookie_samesite = "lax"` (or `strict`) on
  the oauth2-proxy session cookie blunts cross-site use of the session; decide
  whether we rely on that, on the origin check, or on both.

### Session lifetime

oauth2-proxy session behaviour needs concrete values and a policy:

- **`cookie_expire` vs `cookie_refresh`:** pick an absolute session lifetime
  (e.g. 168h) plus a shorter refresh interval (e.g. 15m) at which oauth2-proxy
  re-validates the Google token, versus a single short expiry that forces
  frequent full re-login. What is the acceptable window between Google-side
  revocation and access actually being cut off?
- **Idle timeout:** cookie-session expiry is absolute, not idle-based. Decide
  whether we need an idle timeout at all for a single user, and if so whether
  that justifies a server-side session store (e.g. Redis) instead of pure
  cookie sessions.
- **Immediate kill:** is `/oauth2/sign_out` plus revoking the grant in the
  Google account enough to end a session on demand, or do we want server-side
  invalidation?
- **Long-lived WebSocket:** an established WS connection can outlive the cookie
  expiry because auth is checked at upgrade time. Decide whether AC (or the
  proxy) should periodically re-check auth on live sockets or whether it is
  acceptable that an open socket persists until it drops.
- **UX:** whether to set `skip_provider_button = true` so login goes straight
  to Google instead of showing the oauth2-proxy interstitial.
