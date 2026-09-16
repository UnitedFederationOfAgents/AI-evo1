# Distributed Exchange

How file exchange (see [`condocs/InitialFileExchange.md`](../condocs/InitialFileExchange.md))
might extend past its Step 1 shape — a single local-representative's host-cache,
reachable only by a browser connected directly to that LR — into
`local-representative <--> agent-coordinator <--> local-representative` and
`agent-coordinator <--> local-representative` chains.

## Where Step 1 leaves things

- Each LR has its own host-cache directory (`/host-agent-files/exchange/host-cache`
  by default) and a `files` tab: drag a file onto it, it lands in the
  host-cache, is listed with a wireframe icon by type (text/image/other), and
  is swept an hour after upload.
- LR pushes the listing up to agent-coordinator (`data` / `"files-state"`) so
  the files tab is **visible** through AC too, host-scoped like `system`.
- Upload is **not** accepted through AC. AC's `proxyToHost` reverse proxy
  stamps every request it forwards with `X-UFA-Proxied-By: agent-coordinator`;
  LR's upload handler refuses any request carrying that header. This is
  enforced server-side on LR, not just by AC's UI omitting a dropzone — a
  `POST` aimed straight at `/host/<id>/api/files` through AC is refused too.
- There is no cross-host transfer primitive at all yet: an LR only ever reads
  and writes its own host-cache. There's also no "download bytes" endpoint —
  the files tab only ever needed id/name/size/kind/timestamps, not content.

The rest of this doc sketches what closing those two gaps — AC-mediated
upload to a single LR, and LR-to-LR transfer brokered through AC — would look
like, without committing to either yet.

## Why direct-client-only was the right Step 1 boundary

- **Ambiguity of target.** A browser on AC's aggregated dashboard is looking
  at *a* host's files tab, but "drop a file here" doesn't yet have a story for
  confirming *which* host, or for an operator watching multiple hosts to avoid
  a habitual drop into the wrong one. A direct LR client has no such
  ambiguity — there is exactly one host in view.
- **Consent boundary.** `proxiedHeader` is a deliberately blunt instrument:
  anything arriving through AC is refused, full stop. That's the correct
  default for a feature whose write path touches an arbitrary host's
  filesystem — it should be relaxed on purpose, not by omission.
- **No transfer primitive yet.** representable (the LR↔AC TCP protocol) only
  carries small JSON `data`/`command`/`log` messages today. Moving file bytes
  through it as-is would mean base64-inflated JSON frames with no
  backpressure or resumability — fine for a status blob, not for a file.

## Path 1: `agent-coordinator <--> local-representative` — AC-mediated upload to one host

The smallest useful extension: let an operator sitting at AC's dashboard drop
a file onto *the selected host's* files tab, same as if they'd connected to
that LR directly.

**What's needed:**

1. **An explicit relaxation, not a removal, of `proxiedHeader`.** Two
   reasonable shapes:
   - AC's `/host/<id>/api/files` POST goes through a route AC *owns* (not the
     transparent `proxyToHost` passthrough), which re-stamps the request with
     a distinct header (e.g. `X-UFA-Relayed-Upload-By: agent-coordinator`)
     that LR is willing to accept — the transparent proxy path stays blocked
     for everything else, closing off any other AC-forwarded write.
   - Or: an LR config flag (`-allow-ac-uploads`, off by default) that an
     operator opts a given host into, so the server-side default stays
     "refuse" unless the box owner has said otherwise.
2. **UI**: AC's (currently read-only) `FilesPanel` gains the same dropzone LR
   already has, gated on `selectedHost` and rendered only when the host is
   `active`.
3. **Host disambiguation in the UI** — the host label is already always
   visible in `LRView`'s header, which covers most of the ambiguity concern
   above; a confirming toast/highlight on drop is a cheap extra.

No representable protocol changes needed — the upload still goes over HTTP
through the existing `/host/<id>/*` reverse proxy, just no longer refused.

## Path 2: `local-representative <--> agent-coordinator <--> local-representative` — cross-host transfer

The harder case: get a file that's already sitting in host A's host-cache
into host B's host-cache, with AC as the only thing both hosts can reach.

**Broker, not protocol change.** representable stays a small control-plane
protocol; it should keep describing *intent* ("host B, fetch id X from host
A"), not carry the bytes. The transfer itself can reuse the HTTP surface that
already exists:

1. Add a content endpoint to LR: `GET /api/files/<id>/content` (streams the
   raw bytes; not present in Step 1 since only metadata was needed). Subject
   to the same `proxiedHeader` question as Path 1 — for a *read*, refusing
   AC-proxied requests is far less obviously correct than for a write, so
   this one plausibly stays open by default (an operator viewing a thumbnail
   through AC is a normal case; downloading arbitrary bytes cross-host and
   writing them to another box's filesystem is the operation that needs the
   guard).
2. AC gains a command, e.g. `__files:pull <src-host> <file-id>`, sent to host
   B over the existing representable command channel (mirrors `__system:` /
   `__ridealong:`).
3. Host B's LR, on receiving that command, issues a plain HTTP `GET` against
   `http://<ac-host>:<ac-http-port>/host/<src-host>/api/files/<id>/content`
   — i.e. it uses AC's own reverse proxy as the relay, the same path a
   browser would use to preview the file. AC never needs to buffer or
   understand the file; it's just the reachability bridge, same role it
   already plays for the condoccer iframe forward.
4. Host B streams the response straight into a new host-cache entry (fresh
   8-hex-prefixed filename, fresh hour-long TTL — a copy, not a move; host A's
   copy is untouched and expires on its own original schedule).

**Chain topology this enables:**

```
LR (host A)              agent-coordinator              LR (host B)
  host-cache                                               host-cache
  <id>_report.pdf ──HTTP GET──> /host/A/api/files/.../content
                                        │
                                        └──relayed to──> __files:pull command
                                                          issued to host B
                                                                │
                                                                ▼
                                                    GET /host/A/api/files/.../content
                                                    (host B calling back through AC)
                                                                │
                                                                ▼
                                                    written into host B's host-cache
```

`agent-coordinator <--> local-representative` chains (Path 1) are the
degenerate one-hop case of this — AC talking to a single LR instead of
brokering between two.

## Open questions before either path gets built

- **Consent model for Path 2**: does the pull need an operator to confirm on
  *both* ends, or does AC-level access already imply authorization? Given
  agent-coordinator/docs/architecture.md's web-exposure path (Tailscale +
  oauth2-proxy), the operator reaching AC is already the trust boundary for
  Path 1; Path 2 additionally moves bytes onto a *third party's* filesystem
  (host B), which argues for requiring an explicit destination confirmation
  even if the source read is unguarded.
- **Size/rate limits**: Step 1's 64MiB-per-request cap on upload is a
  reasonable starting point for Path 1; Path 2 adds a second hop's worth of
  bandwidth and should probably cap concurrent pulls per host too.
- **Naming collisions on pull**: always mint a fresh 8-hex prefix on the
  destination host (never trust the source's id/name pair to be free of
  collision) — already true of local uploads, carries over unchanged.
- **Does representable ever need a real stream frame?** Not for file bytes
  (HTTP handles that fine per above) — but if a future use case wants
  server-initiated push notifications of large payloads without an HTTP
  round trip, that's a representable feature, not a files-tab one, and
  should stay out of scope here.
