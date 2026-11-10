# cats-ufa-plugin

The hello-world **external** plug-in (condocs/privateRepoOpsImpls/Step3Prompt.md
Revision C). It is the smallest program that meets the plug-in contract in
[docs/PluginSubApps.md](../docs/PluginSubApps.md) and works in the sandboxed
tier described in [docs/PluginTrust.md](../docs/PluginTrust.md). Copy it to
start a new plug-in.

## Try it

The quickest way is local-representative's **plugins** tab →
**registration** → **register example** (Step3Prompt.md Revision E). That
approves `cats-ufa-plugin` on that host and launches it from the dev
binaries (`make deploy-dev-binaries` at the repo root builds it) on
`--example-plugin-port` (default 8090). It shows up on the system tab like
any managed app, so it can be terminated there. agent-coordinator's plugins
tab has the same button for each host (Revision F).

To do the same by hand:

```sh
make build
./cats-ufa-plugin --port 8090 --lr-port 8082   # beside a running local-representative
```

1. Open local-representative's **plugins** tab. A "Cats" tab appears as
   `pending`: LR has never seen this app id, so it isn't routed yet.
2. Click **approve cats-ufa-plugin**. LR remembers the approval in
   `plugin-approvals.json` (see `--plugin-approvals`) for this host only. It
   fetches the manifest and frames the page at `/apps/cats-ufa-plugin/`,
   sandboxed.
3. The page shows a cat and a **sandbox check** table. Inside the plugins
   tab every row should be green: origin `null`, localStorage blocked, host
   page blocked, own API answering (or unreachable behind a cookie login, see
   below), host API refused.
   Below the ASCII cat is one from **cats-as-a-service** (Revision G, see
   below), with two more rows in the table for it.
4. agent-coordinator shows the same tab, under the host's plugins tab,
   framed through `/host/<id>/apps/cats-ufa-plugin/`. It can't approve or
   revoke: that is only done on the host itself. The global plugins tab lists
   "Cats" once and says where it is registered, but has no global view of it
   yet. When the hosts run different builds of it, that tab and the
   top-level plugins tab show an orange **!**: restart the example on every
   host but one to see it.
5. The debug view's **plugins → manifests** shows the manifest beside its icon.

Stop the process and the tab goes `offline`. Its last manifest is kept and
it comes back online when the plug-in reconnects. **revoke** in LR's plugins
tab takes it back to `pending`.

## What it does

| piece | where |
|-------|-------|
| hello over representable, after every (re)connect | `connectLoop` |
| `/ui-manifest.json` with ETag/304 | `representable.ServeUIManifest` |
| icon at `/plugin-icon.svg` | `representable.ServeSVG` |
| one page, relative URLs only, served with its cat | `static/index.html`, `withCat` |
| one JSON API, readable from an opaque origin | `handleCat` (`Access-Control-Allow-Origin: *`) |
| calls to an outside API, made server-side | `cataas.go` |
| its own version in the manifest (`"2"`) | `pluginVersion` |

## Cats-as-a-service

[cataas.com](https://cataas.com) is the example of a plug-in using an
outside API (Revision G). Each time it serves the page, the plug-in's server
makes two calls:

- `GET /cat?json=true` picks a random cat and returns its id and tags.
- `GET /api/count` returns how many cats there are. It is cached for 10
  minutes.

The page is served with the results, the same way as its ASCII cat. The
browser then shows the picture with a plain `<img>` from
`https://cataas.com/cat/<id>`. A sandboxed page can load an image from
anywhere, but it shouldn't rely on fetch (see below).

If cats-as-a-service can't be reached, the page says why and still shows its
ASCII cat. Two **sandbox check** rows report each side separately:
`cataas (plug-in server)` covers the host's outbound access, and
`cataas (browser picture)` covers the browser's. `--cataas-url` points at
another instance, and `--cataas-url ""` turns the calls off.

Writing a plug-in for the sandboxed tier comes down to a few rules:

- The page has no origin of its own. Even its own API is cross-origin to it,
  so anything it fetches needs `Access-Control-Allow-Origin`. Its own
  WebSocket would arrive with `Origin: null`, so the server has to accept that.
- No cookies and no `localStorage`/`sessionStorage`. Keep state server-side,
  or in the URL.
- Don't depend on fetch for what the page shows. Its fetches carry no
  cookies, so behind a cookie login in front of agent-coordinator they never
  arrive. The page gets its cat server-side instead, and "another cat" reloads
  the frame (Revision F, docs/PluginTrust.md).
- No popups and no top-level navigation.
- It can't reach the host page or the host's APIs. Anything it needs from the
  host will go through a future `postMessage` bridge (docs/PluginTrust.md).

Apart from "register example" launching this one, local-representative
doesn't launch, restart or update external plug-ins: lifecycle is out of
scope for version 1, so run your own yourself.
