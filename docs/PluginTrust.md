# Plug-in trust

How local-representative (LR) and agent-coordinator (AC) decide what an
external plug-in's UI may do. This was introduced in
condocs/privateRepoOpsImpls/Step3Prompt.md Revision C, which took options 2
(approve in the UI) and 3 (a sandboxed tier) from Reply B. The contract a
plug-in implements is in [PluginSubApps.md](PluginSubApps.md);
[cats-ufa-plugin](../cats-ufa-plugin/README.md) is the worked example.

## What is being protected

A plug-in that can say hello is already a process on the host: LR only
accepts a hello over loopback. The sandbox doesn't constrain that process,
which has whatever access its user account has. It constrains the plug-in's
**UI**, which runs in the operator's browser. A same-origin frame there could
call every LR API (launch apps, control runs, files) and, through AC's
`/host/<id>/` proxy, every host's LR. That reach, from one host to the whole
fleet, is what the sandbox removes.

## Today: two tiers, fixed

| tier | who | approval | how it is framed |
|------|-----|----------|------------------|
| `first-party` | the six sub-apps in `firstPartyPlugins` | always approved | same-origin, as before plug-ins |
| `sandboxed` | every other app id | per host, by an operator on that host's LR | opaque origin |

`pluginTrust(appID)` in `local-representative/plugins.go` decides the tier.
It is the only place that does, and today it only checks the first-party
list.

### Approval

- A hello from an app id that isn't first-party and isn't approved on this
  host is registered as `pending`. It shows in `/api/plugins`, the debug
  view and the plugins tab, but it is never routed, and its manifest and
  icon are never fetched.
- **Approve** and **revoke** are buttons in LR's own plugins tab. They call
  `POST /api/plugins/approval` with `{"app_id", "approved"}`. LR refuses that
  request if:
  - agent-coordinator relayed it (`X-UFA-Proxied-By`), so a decision made for
    one host can't spread fleet-wide from AC;
  - the body isn't JSON, so a cross-origin page can't send it without a CORS
    preflight that nothing answers;
  - it comes from a sandboxed page (see the guard below).
- Revision E adds two more ways to approve, both in the plugins tab's
  **registration** sub-tab and both with the same refusals, except as noted:
  - **register example** (`POST /api/plugins/example`) approves
    cats-ufa-plugin and launches it. Since Revision F this one *is* accepted
    relayed through agent-coordinator, so AC's registration sub-tab can press
    it for one host. It can only ever approve that one compiled-in app id,
    never one the caller names, and the relay targets a single host, so the
    decision still doesn't spread fleet-wide. LR notes "(via
    agent-coordinator)" in the outcome.
  - **manual registration** (`POST /api/plugins/manual`) approves the app id
    it registers. A manual registration can only point at a port on this
    host's loopback, never at LR's own ports. It is served in the same
    sandboxed tier as any external plug-in.

  See [PluginSubApps.md](PluginSubApps.md#registration).
- AC's plugins tab shows every host's decision, but it can only describe a
  pending plug-in, not approve it (register example aside).
- Approvals are kept per host in `--plugin-approvals`. The default is
  `<user config dir>/local-representative/plugin-approvals.json`; empty keeps
  them in memory only. They survive restarts, and the file is easy to read
  or delete by hand:

  ```json
  {"version": 1, "approved": [{"app_id": "cats-ufa-plugin", "approved_at": 1793280040}]}
  ```

- Approving a connected pending plug-in brings it online at once and fetches
  its manifest. Revoking an online one returns it to `pending` and drops its
  manifest. Revoking an offline one removes it from the registry.

### The sandboxed tier

Four enforcement points have to agree:

1. **Response header.** `proxyToApp` adds `Content-Security-Policy: sandbox
   allow-scripts allow-forms` (`representable.SandboxCSP`) to every response
   from a sandboxed plug-in. It adds the header rather than replacing it, so
   a policy the plug-in sets itself still applies. The header, not the
   iframe attribute, is what protects a page opened directly in a tab.
2. **Iframe attribute.** Both frontends frame the plug-in with
   `sandbox="allow-scripts allow-forms"` (`PLUGIN_IFRAME_SANDBOX`, which
   mirrors `representable.IframeSandbox`). Neither flag set includes
   `allow-same-origin`, `allow-popups` or `allow-top-navigation`.
3. **Credentials.** `proxyToApp` strips `Cookie`, `Authorization` and
   `X-UFA-Proxied-By` from requests to a sandboxed plug-in, and `Set-Cookie`
   from its responses.
4. **Origin guard.** `representable.RefuseSandboxedCallers` wraps both
   hosts' whole mux. It returns 403 for a request that looks like it came
   from an opaque-origin page, unless the path is inside a plug-in's own tree
   (LR's `/apps/`, AC's `/host/<id>/apps/`). A request looks that way when:
   - it carries `Origin: null` (a WebSocket, a CORS fetch or a POST), or
   - it carries `Sec-Fetch-Site: cross-site` and isn't a top-level document
     navigation (this covers an `<img>`, a plain GET, or the frame
     navigating itself).

   Points 1 and 2 alone wouldn't stop a sandboxed page opening LR's `/ws` and
   sending `launch-app`. The WebSocket upgrader accepts any origin, and
   nothing here uses CORS.

From inside, a sandboxed plug-in has no origin of its own and no
`localStorage` or `sessionStorage`. Its own API is cross-origin to it, so it
must send `Access-Control-Allow-Origin`, and its own WebSocket arrives with
`Origin: null`. cats-ufa-plugin's page checks all of this and shows the
results.

Its fetches also carry no cookies: to the browser they are cross-origin
requests from an opaque origin. Where agent-coordinator sits behind a cookie
login (web-exposure-poc's oauth2-proxy), such a fetch never gets past the
login, and the browser reports it only as a network error -- the "no cat:
TypeError: NetworkError" of Step3Prompt.md Revision F. The frame's own
navigation (its first load, a reload, a form GET) is not affected: it carries
the cookies. So a sandboxed plug-in that has to work through a remotely
reached AC should render what it shows server-side and move between views by
navigating, as cats-ufa-plugin now does, rather than depend on fetch or a
WebSocket. Giving sandboxed pages a credentialed way back to their own API is
part of the future `postMessage` bridge.

### What it does not cover

- **The plug-in's process.** As above, a local process is trusted as much as
  its user account. Approval is the only control over that.
- **Older browsers without `Sec-Fetch-*` headers.** On those, the guard
  only catches requests carrying `Origin: null`. A GET with no Origin, such
  as an `<img>`, would still reach a host API, without being able to read
  the answer. So LR's and AC's APIs must not change anything on a plain GET.
  Their existing GET handlers haven't been audited for this yet.
- **What the frame shows.** A plug-in can draw anything inside its frame,
  including a fake login prompt. The tab bar always shows which plug-in a
  frame belongs to.
- **Resource use.** A plug-in can spin the CPU or flood its own API.
  Revoking it, or stopping its process, ends that.

## Making it toggleable (go-forward)

The aim is to let an operator move one external plug-in between tiers, and
to keep the default as it is today. The plan, in the order it should be
built:

1. **Store the tier with the approval.** Bump the approvals file to version
   2, with `"trust": "sandboxed" | "same-origin"` on each record. A version 1
   file, or a record without `trust`, reads as `sandboxed`. `pluginTrust`
   then reads the record instead of only checking the first-party list.
   First-party ids keep a fixed `first-party` tier. Allowing them to be
   *lowered* to `sandboxed` would be useful for testing that the sub-apps
   survive the sandbox, and lowering is always safe.
2. **Gate promotion per host.** Add an LR flag, `--plugins-allow-same-origin`
   (default off). Without it, LR refuses any request to set an external
   plug-in to `same-origin`, so a host has to opt in before the button does
   anything. Lowering a tier is always allowed.
3. **Use the same endpoint and rules.** Extend `POST /api/plugins/approval`
   with an optional `"trust"` field, with the same refusals as approval
   (direct to LR only, JSON only, not from a sandboxed page). AC shows the
   tier and can't change it. LR's plugins tab gets a tier selector next to
   **revoke**. Promoting asks for confirmation, and the dialog says what
   same-origin access means.
4. **Keep the enforcement points tied to `trust`.** All four points already
   key off the single `PluginInfo.Trust` value: `proxyToApp` reads it, and
   the frontends frame by it. A toggle must keep it that way:
   - The frontends must take the iframe's `sandbox` attribute from
     `p.trust` in the snapshot, never decide it locally.
   - The guard needs no change: a same-origin frame isn't caught by it, which
     is exactly what "same-origin" means.
5. **Apply a change immediately.** A frame's sandbox is fixed when it loads.
   The frontends should key the plug-in iframe on `app_id + trust`, so a tier
   change remounts the frame. `proxyToApp` should send `Cache-Control:
   no-cache` on an external plug-in's HTML. Otherwise, after a change to
   `sandboxed`, the browser could reuse a page it cached as same-origin,
   without the CSP header.
6. **Make every change visible.** Log each tier change with who and when,
   and keep the last few in the snapshot, the way refused hellos are kept,
   so the debug view's plugins tab can show the history.
7. **Test each step.** The tests must cover:
   - a version 1 file reads as sandboxed;
   - promotion is refused without the flag;
   - promotion and demotion are refused through AC;
   - the CSP header is present or absent to match the tier on the next
     response;
   - the frontends' iframe attribute follows `trust`.

Later, the same `trust` field can take a third value, `separate-origin`
(Reply B's option 4: its own port per plug-in), once AC can expose a port or
subdomain for each host and plug-in pair. The `postMessage` bridge, for
sandboxed plug-ins that need something from the host, should be designed
when the first real plug-in needs it. It would sit in front of a small,
allow-listed `{app, action, args}` vocabulary, never a general API pass-through.
