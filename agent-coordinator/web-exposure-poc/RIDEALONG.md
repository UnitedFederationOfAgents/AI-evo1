# Agent Coordinator — Web Exposure POC: Ridealong (localhost path)

## What you get

Run this document as a **ridealong** and, at the end, exactly one external
identity — the Google Account `jordan.edsall1@gmail.com` — can reach the
agent-coordinator (AC) browser UI from anywhere over HTTPS, with **no inbound
ports opened on the host** and **no unauthenticated request ever reaching AC**.

For this first step **AC and the whole proxy chain run on the local server**
(the always-on workstation where development happens). Tailscale Funnel brings
public traffic in and hands it to oauth2-proxy, which is the identity gate in
front of AC. The only cloud dependency is the Google OAuth client used for
"Sign in with Google" — no cloud compute, no cloud networking, no cloud storage.

The design rationale, decisions, and open questions behind this ridealong are
recorded in [INITIAL-SETUP.md](INITIAL-SETUP.md); this file is the executable
version of that plan. A later increment runs AC on cloud compute; that plan is
kept separate in [INITIAL-CLOUD.md](INITIAL-CLOUD.md) and is out of scope here.

## How to run this ridealong

From the repo root:

```
ridealong agent-coordinator/web-exposure-poc/RIDEALONG.md
```

Jump straight to a chapter with a waypoint, e.g.:

```
ridealong --waypoint oauth2-proxy agent-coordinator/web-exposure-poc/RIDEALONG.md
```

Waypoints, in order: `prereqs`, `secrets`, `tailscale`, `google`, `start-ac`,
`oauth2-proxy`, `funnel`, `verify`, `teardown`.

Each ` ```ridealong ` step runs one at a time; you confirm each before it runs.
Steps that need a human decision (creating the Google OAuth client, approving the
Tailscale login) are called out in the prose right before the step that consumes
their output, and the step pauses for you to paste values in.

## What this builds

```
Browser (any network)
  │  HTTPS + WSS
  ▼
Tailscale Funnel ingress   (*.ts.net, TLS by Tailscale, no inbound ports on the local host)
  │  loopback
  ▼
oauth2-proxy  (127.0.0.1:4180)  ──► Google OIDC login + single-email allowlist
  │  loopback, only after auth
  ▼
agent-coordinator  (127.0.0.1:8083)   HTTP + WebSocket
```

`tailscaled` on the workstation dials **outbound** to Tailscale; Funnel carries
inbound traffic back over that WireGuard tunnel, so the host needs **zero inbound
firewall rules** and no router/port-forward changes.

| Layer               | Component               | Job                                                                                   |
|---------------------|-------------------------|--------------------------------------------------------------------------------------|
| Ingress / transport | Tailscale Funnel        | Public HTTPS endpoint, automatic TLS, ingress via Tailscale infra, no inbound ports  |
| Identity gate       | oauth2-proxy            | Every request needs a valid session; login is Google OIDC; only allowlisted emails pass |
| Blast radius        | AC on `127.0.0.1:8083`  | AC is reached only through the proxy chain                                            |

## Decisions (carried in from the design phase)

- **Ingress: Tailscale Funnel** (public HTTPS), not `tailscale serve`
  (tailnet-only). The gate is oauth2-proxy; the browser user does not need to
  join the tailnet.
- **Host: localhost.** The existing always-on workstation runs the whole stack.
  No `e2-micro` in this step — that is the [cloud path](INITIAL-CLOUD.md).
- **Secrets: a local secret store, injected via environment / flags at launch.**
  The OAuth client id/secret and the oauth2-proxy cookie secret live under
  `~/ufa-web-init/secrets/` with `0600` perms and are read into the process only
  at start time. Cloud secret managers are deferred to the cloud path.
- **Tailscale auth**: bring the machine onto the tailnet with a one-time
  interactive `tailscale up`. No pre-auth key needs to be minted for this step.
- **Single user**: `authenticated_emails_file` with one address, not
  `email_domain` (it is a single consumer Gmail account).

## The `~/ufa-web-init` secret / ID store

Nothing sensitive or machine-generated goes into the `AI-evo1` working tree.
Everything this ridealong creates lives under `~/ufa-web-init/`, outside the
repo:

```
~/ufa-web-init/
  secrets/                 # 0600, git-free, never leaves the box
    oauth-client-id
    oauth-client-secret
    cookie-secret
  config/
    authenticated-emails.txt
  ids/                     # generated identifiers, safe-ish but still out of the repo
    funnel-host.txt        # e.g. workstation.tailnet-1234.ts.net
    redirect-url.txt       # https://<funnel-host>/oauth2/callback
    funnel-url.txt         # https://<funnel-host>/
  bin/
    oauth2-proxy           # downloaded release binary (if not already on PATH)
  run/
    *.log  *.pid           # process logs and pids for start/stop
```

The first ridealong chapter creates this tree with `chmod 700` on the root and
on `secrets/`.

## Optional: Terraform / GCP project

There is no first-class Terraform resource for a generic Google OAuth 2.0
client, and the OAuth consent screen is console-only, so the auth setup below is
done by hand. If you want the *project + API enablement* reproducible, a small
`terraform/auth/` stack (`google_project` / `google_project_service` for the
People API + OAuth) can own that — but if you already have a GCP project with
those APIs on, it is not needed for this step and the ridealong does not depend
on it.

---

## Chapter 1 — Prerequisites

This checks for `curl`, `openssl`, `tailscale`, `go`, and a JSON reader, installs
Tailscale if missing, drops the `oauth2-proxy` release binary into
`~/ufa-web-init/bin/`, and builds the AC binary from this repo.

<!-- ridealong waypoint prereqs -->

```ridealong
command -v curl >/dev/null && command -v openssl >/dev/null && command -v go >/dev/null && echo "core tools present" || { echo "MISSING one of: curl openssl go — install them and re-run this step"; exit 1; }
```

If Tailscale is not installed this step installs it (Linux/macOS; needs sudo). On
an unsupported platform, install it by hand from https://tailscale.com/download
and re-run.

```ridealong
command -v tailscale >/dev/null && tailscale version || curl -fsSL https://tailscale.com/install.sh | sh
```

Download a pinned `oauth2-proxy` release into the secret store's `bin/` (skips if
one is already there or on `PATH`).

```ridealong
mkdir -p "$HOME/ufa-web-init/bin" && { command -v oauth2-proxy >/dev/null || test -x "$HOME/ufa-web-init/bin/oauth2-proxy"; } && echo "oauth2-proxy already available" || { cd /tmp && curl -fsSL -o o2p.tgz https://github.com/oauth2-proxy/oauth2-proxy/releases/download/v7.6.0/oauth2-proxy-v7.6.0.$(uname -s | tr '[:upper:]' '[:lower:]')-amd64.tar.gz && tar -xzf o2p.tgz && cp oauth2-proxy-v7.6.0.*-amd64/oauth2-proxy "$HOME/ufa-web-init/bin/oauth2-proxy" && chmod +x "$HOME/ufa-web-init/bin/oauth2-proxy" && "$HOME/ufa-web-init/bin/oauth2-proxy" --version; }
```

Build the agent-coordinator binary (frontend + Go):

```ridealong
cd "$(git rev-parse --show-toplevel)/agent-coordinator" && make build && ./agent-coordinator -h 2>&1 | head -5 || true
```

---

## Chapter 2 — Create the local secret / ID store

Creates `~/ufa-web-init/` with locked-down perms and writes the one-line email
allowlist.

<!-- ridealong waypoint secrets -->

```ridealong
mkdir -p "$HOME/ufa-web-init/secrets" "$HOME/ufa-web-init/config" "$HOME/ufa-web-init/ids" "$HOME/ufa-web-init/bin" "$HOME/ufa-web-init/run" && chmod 700 "$HOME/ufa-web-init" "$HOME/ufa-web-init/secrets" && echo "store at $HOME/ufa-web-init (outside the AI-evo1 repo)"
```

```ridealong
printf '%s\n' 'jordan.edsall1@gmail.com' > "$HOME/ufa-web-init/config/authenticated-emails.txt" && cat "$HOME/ufa-web-init/config/authenticated-emails.txt"
```

Generate the oauth2-proxy cookie secret (32 random bytes, base64) once:

```ridealong
test -s "$HOME/ufa-web-init/secrets/cookie-secret" && echo "cookie secret already present" || { openssl rand -base64 32 | tr -d '\n' > "$HOME/ufa-web-init/secrets/cookie-secret" && chmod 600 "$HOME/ufa-web-init/secrets/cookie-secret" && echo "cookie secret written"; }
```

---

## Chapter 3 — Join the tailnet and derive the public hostname

`tailscale up` opens a browser (or prints a URL) for you to authenticate the
**machine** to your tailnet — this is a manual approval, done once. `--ssh` lets
you administer the box over the tailnet later; public port 22 stays closed.

After the machine is up, the next steps read your node's stable DNS name
(`<host>.<tailnet>.ts.net`) and write it, plus the derived OAuth redirect URL,
into `~/ufa-web-init/ids/`. You need the redirect URL for the Google console step
in Chapter 4, so run this chapter first.

<!-- ridealong waypoint tailscale -->

```ridealong
tailscale status >/dev/null 2>&1 && echo "already on the tailnet" || sudo tailscale up --ssh
```

```ridealong
tailscale status --json | { jq -r '.Self.DNSName' 2>/dev/null || python3 -c 'import sys,json;print(json.load(sys.stdin)["Self"]["DNSName"])'; } | sed 's/\.$//' | tee "$HOME/ufa-web-init/ids/funnel-host.txt"
```

```ridealong
printf 'https://%s/oauth2/callback\n' "$(cat "$HOME/ufa-web-init/ids/funnel-host.txt")" | tee "$HOME/ufa-web-init/ids/redirect-url.txt"
```

```ridealong
printf 'https://%s/\n' "$(cat "$HOME/ufa-web-init/ids/funnel-host.txt")" | tee "$HOME/ufa-web-init/ids/funnel-url.txt"
```

---

## Chapter 4 — Create the Google OAuth client (manual, then paste in)

This is the one genuinely manual, non-Terraformable piece. Do it once in the
GCP console:

1. **Pick / create a project.** Any GCP project works. If it is new, no APIs need
   enabling for a plain OAuth web client (People API is only needed if you later
   read profile data server-side).
2. **OAuth consent screen** → *APIs & Services → OAuth consent screen*:
   - User type: **External**.
   - App name, support email, developer contact: fill in anything reasonable.
   - Publishing status can stay **Testing**. Under *Test users* add
     `jordan.edsall1@gmail.com` — in Testing mode only listed test users can log
     in, which is a second belt alongside the oauth2-proxy allowlist.
   - Scopes: the defaults (`openid`, `.../auth/userinfo.email`,
     `.../auth/userinfo.profile`) are enough; no sensitive scopes.
3. **Create the client** → *APIs & Services → Credentials → Create credentials →
   OAuth client ID*:
   - Application type: **Web application**.
   - Name: e.g. `ac-web-exposure-poc`.
   - **Authorized redirect URIs**: paste the exact contents of
     `~/ufa-web-init/ids/redirect-url.txt` (the
     `https://<host>.<tailnet>.ts.net/oauth2/callback` value from Chapter 3). If
     you also want to test the proxy locally without Funnel, add
     `http://localhost:4180/oauth2/callback` too.
   - No "Authorized JavaScript origins" are needed (oauth2-proxy does the
     redirect server-side).
4. Click **Create** and keep the **Client ID** and **Client secret** dialog open
   for the next two steps.

The next two steps read those values straight into `~/ufa-web-init/secrets/`
(they never touch the repo, your shell history, or a config file). The secret
prompt is hidden as you type.

<!-- ridealong waypoint google -->

```ridealong
read -rp 'Paste the Google OAuth Client ID: ' ID && printf '%s' "$ID" > "$HOME/ufa-web-init/secrets/oauth-client-id" && chmod 600 "$HOME/ufa-web-init/secrets/oauth-client-id" && echo "client id saved (${#ID} chars)"
```

```ridealong
read -rsp 'Paste the Google OAuth Client Secret: ' SEC && printf '%s' "$SEC" > "$HOME/ufa-web-init/secrets/oauth-client-secret" && chmod 600 "$HOME/ufa-web-init/secrets/oauth-client-secret" && echo && echo "client secret saved (${#SEC} chars)"
```

```ridealong
test -s "$HOME/ufa-web-init/secrets/oauth-client-id" && test -s "$HOME/ufa-web-init/secrets/oauth-client-secret" && test -s "$HOME/ufa-web-init/secrets/cookie-secret" && echo "all three secrets present" || { echo "one or more secrets missing under ~/ufa-web-init/secrets"; exit 1; }
```

---

## Chapter 5 — Start agent-coordinator on loopback

Starts AC in the background, logging to `~/ufa-web-init/run/`. AC listens on
`:8083`; it currently has **no loopback-bind flag**, so on a workstation that
shares a LAN with untrusted hosts add a local deny rule for TCP 8083
(`sudo ufw deny 8083` or equivalent). Tailscale Funnel itself opens **no**
inbound port — only the proxy chain is ever reachable from outside.

<!-- ridealong waypoint start-ac -->

```ridealong
cd "$(git rev-parse --show-toplevel)/agent-coordinator" && nohup ./agent-coordinator -port 8083 -repr-port 8084 > "$HOME/ufa-web-init/run/agent-coordinator.log" 2>&1 & echo $! > "$HOME/ufa-web-init/run/agent-coordinator.pid" && sleep 2 && echo "AC pid $(cat "$HOME/ufa-web-init/run/agent-coordinator.pid")"
```

```ridealong
curl -fsS -o /dev/null -w 'AC on 127.0.0.1:8083 -> HTTP %{http_code}\n' http://127.0.0.1:8083/ || { echo "AC not responding — check ~/ufa-web-init/run/agent-coordinator.log"; exit 1; }
```

---

## Chapter 6 — Start oauth2-proxy (the identity gate)

Starts oauth2-proxy on `127.0.0.1:4180`, reading the three secrets and the
redirect URL from `~/ufa-web-init/` at launch. `--reverse-proxy=true` makes it
proxy the AC WebSocket upgrade. `--cookie-secure=true` because the browser-facing
side is HTTPS via Funnel. Session lifetime here is a 168h absolute expiry with a
15m refresh against Google — see **Open questions** for the policy discussion.

<!-- ridealong waypoint oauth2-proxy -->

```ridealong
O2P="$(command -v oauth2-proxy || echo "$HOME/ufa-web-init/bin/oauth2-proxy")"; nohup "$O2P" --provider=google --client-id="$(cat "$HOME/ufa-web-init/secrets/oauth-client-id")" --client-secret="$(cat "$HOME/ufa-web-init/secrets/oauth-client-secret")" --cookie-secret="$(cat "$HOME/ufa-web-init/secrets/cookie-secret")" --redirect-url="$(cat "$HOME/ufa-web-init/ids/redirect-url.txt")" --authenticated-emails-file="$HOME/ufa-web-init/config/authenticated-emails.txt" --upstream="http://127.0.0.1:8083" --http-address="127.0.0.1:4180" --reverse-proxy=true --cookie-secure=true --cookie-expire=168h --cookie-refresh=15m --cookie-samesite=lax --skip-provider-button=true > "$HOME/ufa-web-init/run/oauth2-proxy.log" 2>&1 & echo $! > "$HOME/ufa-web-init/run/oauth2-proxy.pid" && sleep 2 && echo "oauth2-proxy pid $(cat "$HOME/ufa-web-init/run/oauth2-proxy.pid")"
```

```ridealong
curl -fsS -o /dev/null -w 'oauth2-proxy on 127.0.0.1:4180 -> HTTP %{http_code}\n' http://127.0.0.1:4180/ping || { echo "oauth2-proxy not responding — check ~/ufa-web-init/run/oauth2-proxy.log"; exit 1; }
```

```ridealong
curl -fsS -o /dev/null -w 'unauthenticated request to a protected path -> HTTP %{http_code} (302 = redirected to Google, good)\n' http://127.0.0.1:4180/
```

---

## Chapter 7 — Publish with Tailscale Funnel

`tailscale funnel` exposes `127.0.0.1:4180` (oauth2-proxy) at
`https://<host>.<tailnet>.ts.net` with a Tailscale-issued certificate. Your
tailnet ACL policy must permit Funnel for this node — if the first step errors
about Funnel not being enabled, add `"funnel"` to the node's attributes (or the
`tag:` it carries) in the tailnet policy file and re-run.

<!-- ridealong waypoint funnel -->

```ridealong
sudo tailscale funnel --bg 4180 && tailscale funnel status
```

```ridealong
echo "Public URL: $(cat "$HOME/ufa-web-init/ids/funnel-url.txt")"
```

---

## Chapter 8 — Verify end to end

<!-- ridealong waypoint verify -->

The edge should now answer and bounce an unauthenticated request to Google:

```ridealong
curl -fsS -o /dev/null -w 'Funnel edge -> HTTP %{http_code} (200 or 302 both mean it is live)\n' "$(cat "$HOME/ufa-web-init/ids/funnel-url.txt")"
```

Now the human check — this is what "web-accessible" means for this POC:

```ridealong
echo "Open $(cat "$HOME/ufa-web-init/ids/funnel-url.txt") in a browser. Sign in as jordan.edsall1@gmail.com. Confirm the agent-coordinator UI loads and the WebSocket host list populates."
```

Negative check: sign in (in another profile / incognito) with a **different**
Google account and confirm oauth2-proxy refuses it with a 403 before AC is ever
reached.

```ridealong
echo "Recorded: Funnel URL $(cat "$HOME/ufa-web-init/ids/funnel-url.txt") ; logs in ~/ufa-web-init/run/ ; secrets in ~/ufa-web-init/secrets/"
```

At this point the ridealong is complete and AC is reachable on the public
internet only for the one allowlisted identity.

---

## Chapter 9 — Teardown

Stops the Funnel first (closes public access), then the two local processes.
Secrets and IDs under `~/ufa-web-init/` are left in place for a fast restart; the
Google OAuth client can be reused by the [cloud path](INITIAL-CLOUD.md), so it is
not deleted here.

<!-- ridealong waypoint teardown -->

```ridealong
sudo tailscale funnel --bg off 4180 2>/dev/null || sudo tailscale serve reset; tailscale funnel status || true
```

```ridealong
kill "$(cat "$HOME/ufa-web-init/run/oauth2-proxy.pid" 2>/dev/null)" 2>/dev/null; kill "$(cat "$HOME/ufa-web-init/run/agent-coordinator.pid" 2>/dev/null)" 2>/dev/null; echo "oauth2-proxy and agent-coordinator stopped"
```

To fully leave the tailnet as well: `sudo tailscale down`. To wipe the local
store: `rm -rf ~/ufa-web-init`. To revoke external access permanently, delete the
OAuth client and consent screen in the GCP console.

---

## Open questions

### AC origin checks

AC's WebSocket handler currently uses a `CheckOrigin` that returns `true` for
every origin (`agent-coordinator/main.go`). With this proxy chain that is worth
tightening:

- **Is it exploitable here?** oauth2-proxy authenticates the request, but a
  browser on any site can still open a cross-origin WebSocket to the Funnel URL
  and the browser will attach the oauth2-proxy session cookie. If AC accepts any
  origin, that is a cross-site WebSocket hijacking path even with the proxy.
  `--cookie-samesite=lax` (set above) blunts it but does not fully close it for
  top-level navigations.
- **What origin does AC see?** Behind oauth2-proxy with `--reverse-proxy=true`,
  confirm whether the `Origin` / `Host` headers reaching AC are the
  browser-facing `https://<host>.<tailnet>.ts.net` or something rewritten; the
  fix depends on the answer.
- **How should the allowed origin be supplied?** Preference is a flag/env
  allowlist (e.g. `AC_ALLOWED_ORIGINS`) so the same binary serves localhost dev,
  this Funnel hostname, and a different Funnel hostname in the cloud path.

### Session lifetime

The values baked into Chapter 6 (`--cookie-expire=168h`,
`--cookie-refresh=15m`) are a starting point, not a settled policy:

- **Absolute vs refresh:** 168h absolute with a 15m re-validation against Google
  vs. a single short expiry that forces frequent full re-login. What is the
  acceptable window between Google-side revocation and access actually being cut
  off?
- **Idle timeout:** cookie-session expiry is absolute, not idle-based. Decide
  whether a single user needs an idle timeout at all, and if so whether that
  justifies a server-side session store (e.g. Redis) instead of pure cookie
  sessions.
- **Immediate kill:** is `/oauth2/sign_out` plus revoking the grant in the
  Google account enough, or do we want server-side invalidation?
- **Long-lived WebSocket:** an established WS connection can outlive the cookie
  expiry because auth is checked only at upgrade time. Decide whether AC (or the
  proxy) should periodically re-check auth on live sockets.

## Relationship to the cloud path

[INITIAL-CLOUD.md](INITIAL-CLOUD.md) runs AC on an `e2-micro` instead of the
workstation, with GCP Secret Manager + a VM service account in place of
`~/ufa-web-init/secrets/`, reusing the same Google OAuth client (just adding the
VM's Funnel callback URL). **Before** that increment we must solve
machine-to-machine auth for the `:8084` representable-TCP port, which has no auth
today; hosting AC in the cloud without that would expose an unauthenticated port
across the network.
