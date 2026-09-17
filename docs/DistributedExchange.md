# Distributed Exchange

How file exchange (see [`condocs/InitialFileExchange.md`](../condocs/InitialFileExchange.md))
extends past its Step 1 shape — a single local-representative's host-cache,
reachable only by a browser connected directly to that LR — into
`local-representative <--> agent-coordinator <--> local-representative` and
`agent-coordinator <--> local-representative` chains.

**Revision B landed Path 1** (AC-mediated upload to one host, below); Path 2
(LR-to-LR transfer brokered through AC) remains a sketch, not yet built.

## Where Step 1 leaves things

- Each LR has its own host-cache directory (`/host-agent-files/exchange/host-cache`
  by default) and a `files` tab: drag a file onto it, it lands in the
  host-cache, is listed with a wireframe icon by type (text/image/other), and
  is swept an hour after upload.
- LR pushes the listing up to agent-coordinator (`data` / `"files-state"`) so
  the files tab is **visible** through AC too, host-scoped like `system`.
- Upload was **not** accepted through AC in Step 1. AC's `proxyToHost`
  reverse proxy stamps every request it forwards with
  `X-UFA-Proxied-By: agent-coordinator`; LR's upload handler refused any
  request carrying that header. This was enforced server-side on LR, not just
  by AC's UI omitting a dropzone — a `POST` aimed straight at
  `/host/<id>/api/files` through AC was refused too. **Revision B relaxes
  this** — see Path 1 below.
- **Revision A added a content endpoint**: `GET /api/files/<id>` streams a
  host-cache file's raw bytes (`?download=1` for an attachment
  Content-Disposition), backing the files tab's viewer page and download
  button. It is deliberately **not** gated on `proxiedHeader` — a read isn't
  the write-to-an-arbitrary-filesystem operation upload is — so it already
  works unmodified through AC's `/host/<id>/*` proxy, and AC's own files tab
  reuses it directly for its viewer/download widgets rather than needing a
  proxy-owned route of its own.
- There is still no cross-host transfer primitive: an LR only ever reads and
  writes its own host-cache. Path 2 below now has less to add, since the
  content endpoint it originally proposed already exists.
- **Revision C added the file-details dialog's state-changing actions**:
  `DELETE /api/files/<id>`, `POST /api/files/<id>/hold`, and
  `POST /api/files/<id>/persist`. Like the content endpoint above (and unlike
  upload), none of these are gated on `proxiedHeader` — they act on a file
  already listed in a host's files tab, which AC only shows once an operator
  has explicitly selected that host, so the "ambiguity of target" concern
  that justifies upload's blunt refusal doesn't apply. They pass through AC's
  transparent `/host/<id>/*` proxy unmodified, same as a direct LR client —
  no dedicated AC-owned relay route was needed for them, unlike Path 1's
  upload relay.

The rest of this doc sketches what closing that remaining gap — LR-to-LR
transfer brokered through AC — would look like, without committing to it yet.
(Path 1, AC-mediated *upload* to a single LR, landed in Revision B; see
below.)

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

Revision B relaxes this boundary on purpose, per Path 1 below — the ambiguity
and consent-boundary points still applied at Step 1 time, but with a host
always selected explicitly before its files tab is even visible, and a
distinct header AC can only set from its own owned route, deliberately
carving out the AC-mediated case rather than removing the guard.

## Path 1: `agent-coordinator <--> local-representative` — AC-mediated upload to one host (landed, Revision B)

The smallest useful extension: let an operator sitting at AC's dashboard drop
a file onto *the selected host's* files tab, same as if they'd connected to
that LR directly. AC acts purely as a relay for this — it never keeps its own
copy of the file, or looks at LR's host-cache directly; it just streams the
multipart body through.

**What's built:**

1. **An explicit relaxation, not a removal, of `proxiedHeader`** — the first
   of the two shapes this section originally sketched: a `POST` to
   `/host/<id>/api/files` no longer takes AC's transparent `proxyToHost`
   passthrough. `proxyToHost` recognizes that one path+method combination and
   routes it to `handleFileUploadRelay` instead, a route AC *owns*. That
   handler builds a fresh outbound request to the target LR (rather than
   forwarding the browser's request verbatim) and stamps it with both
   `X-UFA-Proxied-By: agent-coordinator` (it did arrive via AC) and
   `X-UFA-Relayed-Upload-By: agent-coordinator` (the deliberate exception).
   LR's upload handler accepts a request only when *both* are present with
   the expected value — a request that reaches it through the transparent
   passthrough instead can never carry the second header, since
   `proxyToHost`'s `Director` strips any client-supplied copy before
   forwarding, so a browser can't spoof its way past the distinction. The
   transparent path stays blocked for every other write.
2. **UI**: AC's `FilesPanel` gained the same dropzone LR already has, gated on
   `selectedHost` and rendered only when the host is `active`.
3. **Host disambiguation in the UI** — the host label is already always
   visible in `LRView`'s header, which covers most of the ambiguity concern
   below.

No representable protocol changes were needed — the upload still goes over
HTTP, just through a route AC owns rather than the transparent
`/host/<id>/*` reverse proxy.

(The LR config-flag alternative sketched originally — `-allow-ac-uploads`,
off by default — was not built; the header-based relaxation covers the same
need without adding a second flag-driven trust knob.)

## Path 2: `local-representative <--> agent-coordinator <--> local-representative` — cross-host transfer

The harder case: get a file that's already sitting in host A's host-cache
into host B's host-cache, with AC as the only thing both hosts can reach.

**Broker, not protocol change.** representable stays a small control-plane
protocol; it should keep describing *intent* ("host B, fetch id X from host
A"), not carry the bytes. The transfer itself can reuse the HTTP surface that
already exists:

1. **Already have this.** `GET /api/files/<id>` (added in Revision A for the
   files tab's own viewer/download widgets) streams the raw bytes and is
   already open through AC's proxy by default — exactly the "reads are far
   less obviously in need of the guard than writes" shape this section
   originally predicted. Nothing left to add here; Path 2 can reuse it as-is.
   The guard that *does* still matter is on the write side: writing the
   pulled bytes into host B's host-cache.
2. AC gains a command, e.g. `__files:pull <src-host> <file-id>`, sent to host
   B over the existing representable command channel (mirrors `__system:` /
   `__ridealong:`).
3. Host B's LR, on receiving that command, issues a plain HTTP `GET` against
   `http://<ac-host>:<ac-http-port>/host/<src-host>/api/files/<id>`
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
  <id>_report.pdf ──HTTP GET──> /host/A/api/files/<id>
                                        │
                                        └──relayed to──> __files:pull command
                                                          issued to host B
                                                                │
                                                                ▼
                                                    GET /host/A/api/files/<id>
                                                    (host B calling back through AC)
                                                                │
                                                                ▼
                                                    written into host B's host-cache
```

`agent-coordinator <--> local-representative` chains (Path 1) are the
degenerate one-hop case of this — AC talking to a single LR instead of
brokering between two.

## Open questions before Path 2 gets built

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
