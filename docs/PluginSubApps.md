# Plug-in sub-apps

First step of the plug-in break-out (condocs/privateRepoOpsImpls/Step3Prompt.md).
session-manager was the first plug-in. As of Revision B every first-party
sub-app with a presented UI is one:

| sub-app               | app id (`--name`) | route             |
|-----------------------|-------------------|-------------------|
| condocv2              | `condocv2`        | `/apps/condocv2/` |
| the-conversationalist | `convo`           | `/apps/convo/`    |
| session-manager       | `sessions`        | `/apps/sessions/` |
| ianar                 | `robot`           | `/apps/robot/`    |
| composer              | `compose`         | `/apps/compose/`  |
| diagrammographer      | `dg`              | `/apps/dg/`       |

Each keeps its own top-level tab in local-representative (LR) and
agent-coordinator (AC); what changed is where the tab's iframe points. Both
frontends frame the app at `/apps/<appId>/` plus the manifest's `entry` once
the app is `online` in that LR's registry, and fall back to the legacy
hand-written `/<appId>/` proxy otherwise -- an older sub-app build that
doesn't say hello. The legacy proxies stay in `local-representative/main.go`
only for that fallback; they can go once every host has been updated.
diagrammographer came after the break-out, so it has no legacy proxy: both
frontends frame its tab only once it is online in the registry, and say they
are waiting for its hello while it is connected but not yet online.
The-conversationalist's mic-capture frame follows the convo tab's path the
same way. condocv2 finds convo by swapping its own path segment for
`convo`, so under `/apps/condocv2/` it reaches `/apps/convo/`.

## The contract

A plug-in must do three things:

1. **Say hello over representable.** After `representable.Connect`, call
   `client.SendPluginHello(PluginHello{AppID, DisplayName, HTTPPort, DevMode})`.
   The hello is an ordinary `data` message with `data_type: "plugin-hello"`:

   ```json
   {"hello_version":1,"app_id":"sessions","display_name":"Sessions",
    "http_port":"8085","manifest_version":1,"dev_mode":false}
   ```

   `app_id` must match `[a-z][a-z0-9-]{0,31}` and equal the connection's client
   name. The connection is the liveness signal: when it drops, the plug-in goes
   offline.
2. **Serve `/ui-manifest.json`** with an `ETag`, and answer `If-None-Match` with
   304:

   ```json
   {"manifest_version":1,"app_id":"sessions","display_name":"Sessions",
    "short_name":"SM","entry":"./","icon":"./plugin-icon.svg","order":30}
   ```

   Every URL in it is relative to the plug-in's own root. Readers ignore fields
   they don't know. An optional `version` (Revision G) is the plug-in's own
   build or release; see [Version skew](#version-skew-revision-g).

   **Version 2** (Revision E) adds two optional fields: `icon_mask` (the icon
   is monochrome, so a host draws it as a mask in its own text colour rather
   than on a light chip) and `description` (a tab tooltip). A plug-in serving
   version 2 says so in its hello's `manifest_version`. session-manager and
   (since Revision H) composer are on version 2, and composer also sends a
   `version` of `"2"`; the others stay on 1, so a fleet whose hosts run
   different builds shows both, and the compose tab gets the version-skew
   mark until every host runs the new composer. A host whose LR reads only version
   1 skips fetching a version 2 manifest and records why in
   `manifest_error`. It still routes the plug-in, so the tab works there,
   just without a manifest: no icon, and the default entry page.
   `representable.UIManifestReadVersion` is the newest version a host reads. When the manifest changes, send
   `client.SendManifestChanged(etag)` (`data_type: "manifest-changed"`) and the
   host fetches it again. `representable.ServeUIManifest(func() UIManifest)`
   does the ETag/304 handling, and `representable.ServeSVG` serves an icon at
   `representable.PluginIconPath`; every sub-app's `plugin.go` uses both.
3. **Work under a base path it doesn't control.** The plug-in is served at
   `/apps/<appId>/` on local-representative and at
   `/host/<id>/apps/<appId>/` through agent-coordinator. Use relative asset URLs
   (Vite `base: './'`).

The schema types live in `representable/plugin.go`.

## local-representative

The registry and route live in `local-representative/plugins.go`.

- **`/apps/<appId>/*`** reverse-proxies to `127.0.0.1:<http_port>` with the
  prefix stripped. A bare `/apps/<appId>` redirects to the trailing-slash form.
- **`GET /api/plugins`** returns every registry entry: its status, hello, last
  manifest, ETag and base path.
- **Admission** (Revision C; the details are in [PluginTrust.md](PluginTrust.md)):
  - A hello is accepted only from a loopback connection whose name matches
    `app_id`.
  - App ids in `firstPartyPlugins` are always approved and served
    same-origin (tier `first-party`).
  - Any other app id is `pending` until an operator approves it in this
    LR's plugins tab (`POST /api/plugins/approval`, refused when relayed by
    AC). A pending app shows up in `/api/plugins` but is never routed, and
    its manifest is never fetched.
  - Approvals are kept per host in `--plugin-approvals`. An approved
    external plug-in is served in the `sandboxed` tier.
  - Every entry's `trust` field says which tier it is in, or would be once
    approved.
  - Both hosts refuse requests from a sandboxed page anywhere outside a
    plug-in's own tree (`representable.RefuseSandboxedCallers`).
- **Offline plug-ins:** when an online plug-in disconnects it becomes `offline`.
  Its last manifest is kept, and its route returns 502.
- **Lifecycle is out of scope for version 1.** local-representative only
  displays plug-ins; it doesn't launch, restart or update them.
- **Debug view:** every registry change broadcasts a `plugins-state` snapshot
  to browser clients and agent-coordinator. The snapshot holds the
  configuration (allow-list, admission rules, manifest path and versions,
  fetch timeout), every registry entry with its peer host and hello, manifest
  and disconnect times, and the last 20 refused hellos with the reason.
  agent-coordinator relays it per host as `lr-plugins-state`. The system tab's
  debug view shows it under a "plugins" sub-tab, per host, or in the global
  view as one host-labelled list. That sub-tab has two modes:
  - **state:** the snapshot as text lines, with "to file".
  - **manifests:** pick a plug-in (per host in the global view) to see the
    manifest LR holds beside its icon, its ETag and fetch time, any fetch
    error, and its hello. Each plug-in in the list has its icon too. Icons
    load through `/host/<id>/apps/<appId>/` plus the manifest's `icon`, so
    only online plug-ins have one; the others show their initials. "fetch"
    loads the manifest file as the plug-in serves it now,
    through `/host/<id>/apps/<appId>/ui-manifest.json`, and says whether its
    ETag matches the held one. Only online plug-ins are routed, so it is
    disabled for the rest.

## Tab bars (Revision E)

- **Icons:** each hosted-UI sub-app's top-level tab (condocv2,
  convo, sessions, robot, compose, dg) shows its manifest icon once the app is
  online in the registry (`AppTabIcon`). AC's host view uses that host's
  registry. AC's global view uses the first host where the app is online.
  A version 2 manifest's `description` is the tab's tooltip.
- **Labels:** a tab reads as its app id, except dg's, which reads
  "diagram" (`TAB_LABELS` in LR, `LR_TAB_LABELS` in AC).
- **Scrolling:** every tab row that can outgrow its bar (the top-level bars,
  and the plugins tab's own) is a `ScrollTabs`. The mouse wheel scrolls it
  sideways, a fade on an edge shows there are more tabs that way, and a tab
  chosen some other way (nav arrows, a link) is scrolled into view.

## The plugins tab

LR and AC (per host, and in the global view) have a top-level **plugins** tab
for *external* plug-ins: every registry entry whose `trust` isn't
`first-party`. It is a tab bar like the top-level sub-app bar over a full pane
(`PluginsShell` in each `App.tsx`). At its far left, set off by a divider, is
the **registration** sub-tab. After it comes one tab per plug-in, each with
its manifest icon.

AC's global view keeps the registration sub-tab (one section per host). Since
Revision G its bar lists each external plug-in registered on any host once
(`globalPluginTabs`), rather than once per host. Selecting one shows that it
is registered, which hosts have it with their status, trust and version, and
that its global view is **not yet implemented** (`GlobalPluginPane`). A host's
plug-ins are still opened from that host's own plugins tab. The registration
sub-tab marks a registered example the same way.

### Version skew (Revision G)

A manifest may carry `version`, the plug-in's own free-form build or release
(cats-ufa-plugin sends `"2"`). It isn't a schema change, so any manifest
version may carry it. In AC's global view, `pluginVersionSkew` compares each
plug-in across the hosts that hold a manifest for it, using manifest version,
`version` and manifest ETag. When they aren't all the same, the plug-in's tab
gets a bright orange **!** (the dirty/out-of-date orange) whose tooltip lists
each host's version. This applies to the hosted-UI sub-apps' top-level tabs,
to each plug-in tab inside global → plugins, and to the top-level plugins tab
when any external plug-in there differs. Hosts without the plug-in, or where
it is still pending and so has no manifest, don't count.

### registration

On LR, `PluginRegistration` has two controls. Both are plug-in decisions with
the same refusals as approval (JSON only, not from a sandboxed page, and
direct to LR only -- except register example, which AC may relay, see
below):

- **register example** (`POST /api/plugins/example`): approves
  `cats-ufa-plugin` on this host and, unless it is already connected or
  running, launches it. It is a managed app in `procman.go`, started from the
  dev binaries (the root `deploy-dev-binaries` builds it) on
  `--example-plugin-port`, default 8090. It then says hello like any plug-in.
  The last outcome is kept in the snapshot's `example`. AC's registration
  sub-tab has the same button per host (Revision F): it POSTs through
  `/host/<id>/api/plugins/example`, and LR accepts that relay for this
  endpoint only.
- **manual registration** (`POST /api/plugins/manual` with `{"app_id",
  "http_port", "display_name", "registered"}`): for a plug-in that serves a
  manifest on this host's loopback but doesn't say hello.
  - Registering approves the app id, so it is served sandboxed like any
    external plug-in. First-party ids and LR's own ports are refused, and so
    is an app id that is already connected.
  - With no connection to say it is alive, LR fetches its manifest every
    `manualPollInterval` (10s). An answer means `online`; none means
    `offline`, with the last manifest kept.
  - If the plug-in later says hello, its connection takes over the entry
    (`source: "hello"`). When that connection drops, polling takes the entry
    back.
  - Registrations are kept beside the approvals, under `manual` in
    `--plugin-approvals`. Unregistering removes the registration but keeps
    the approval; revoke that separately.

The sub-tab lists each manual registration with its status and last check.
AC's copy shows the same per host, read-only apart from register example.

- **online:** framed at its base path plus the manifest's `entry`, sandboxed
  (see [PluginTrust.md](PluginTrust.md)). LR's bar has a **revoke** button
  for it.
- **pending:** LR shows an **approve** button. AC only says it is waiting
  for approval on that host's own LR.
- **offline:** says since when.

[cats-ufa-plugin](../cats-ufa-plugin/README.md) is the hello-world external
plug-in to try this with.

## Not yet done

- Using the registry for per-host availability in AC beyond the frame paths
  and tab icons.
- Moving the other sub-apps to manifest version 2 (only session-manager and
  composer are on it, on purpose, while hosts on different builds are
  compared).
- Making the trust tier toggleable per plug-in (the plan is in
  [PluginTrust.md](PluginTrust.md)).
- A separate-origin tier, and a postMessage bridge for sandboxed plug-ins
  that need something from the host.
- Removing the legacy `/<appId>/` proxies once no host needs the fallback.
