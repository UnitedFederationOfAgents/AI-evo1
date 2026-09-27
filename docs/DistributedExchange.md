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
- **Step5SubstepRPrompt.md added `POST /api/files/<id>/highlight`**: a plain
  toggle on a file's `highlighted` marking (independent of `state`), shown in
  the same file-details dialog alongside enter/hold/persist/download and
  rendered as a yellow ring around a highlighted file's grid box. It's the
  same style of action as Revision C's — ungated on `proxiedHeader`, passes
  through AC's transparent proxy unmodified. This increment only implements
  the marking itself: a first step toward flagging files at the LR/AC level
  for cross-system functionality condoccer will build on later.
- **Revision A wires up that cross-system functionality for condoccer**:
  condoccer's "Add Resources" action (available on any step/substep while
  it's `awaiting_action`) pulls every currently-highlighted file straight
  from local-representative and copies it into the condoc's `Impls` folder,
  inserting a `## Resource N` block that links to each copy. condoccer
  already maintains a `representable.Client` connection to LR (see
  `condoccer/repr.go`) purely for status/commands, which knows LR's dial
  host but not its separate HTTP dashboard port — so `representable.Server`
  gained an opt-in `SetHTTPPort`, disclosed to every connecting client in its
  existing "hello" message (`representable.Client.PeerHTTPPort`).
  local-representative sets it to its own `-port`; condoccer resolves
  `http://<lr-host>:<lr-http-port>/api/files` (and `/api/files/<id>?download=1`
  per highlighted file) directly against it — the same ungated endpoints AC's
  transparent proxy already passes through, so nothing new was needed on the
  AC side. The `.condoc` lock is asserted for the duration of the copy+edit
  (see `condoccer/resources.go`), since — unlike every other condoccer
  action — inserting a resource block doesn't itself change the condoc's
  phase, so nothing else would otherwise stop local-representative's
  dev-repo watcher from rebuilding mid-operation.
- **Revision B of Step5SubstepRPrompt.md makes a resource block a first-class
  citizen of the condoc viewer**, rather than unstyled text tacked onto
  whichever Reply/Revision preceded it. The heading dropped its parens
  (`## Resource N`, not `## Resource (N)`) and gained an optional
  `-- <name>` suffix from a new "name" field on the "Add Resources" dialog;
  condoccer's markdown parser (`parseIterations` server-side,
  `parseStepSections` client-side) now cuts a `## Resource N` heading out as
  its own `Iteration`/section rather than folding its body into the
  preceding one, so it gets its own sidebar entry (`Resource N` or
  `Resource N | <name>`) instead of appearing as a tail of the Reply. A new
  `GET /api/resource/<filename>?condoc=<path>[&download=1]` route — the only
  HTTP route condoccer serves besides its own embedded frontend and `/ws` —
  lets the scroll pane render an image or text resource inline instead of
  showing its raw markdown link text; clicking an image opens a full-size
  overlay. Anything else falls back to a plain download link.
- **Revision C of Step5SubstepRPrompt.md adds a second "Add Resources"
  source: "Upload".** Unlike "Highlighted" (pulls files off
  local-representative over the representable connection), "Upload" is a
  plain multipart `POST /api/upload-resource` straight from the browser — the
  same shape as local-representative's own files-dialog upload — except the
  bytes land directly in the condoc's `Impls` folder instead of LR's
  host-cache; no highlighting, no representable connection, and no
  host-cache TTL sweep are involved. The dialog's "Source" dropdown gained
  the option, revealing an "up arrow" button that opens the browser's file
  picker; choosing a file locks the dropdown (an operator can't switch
  sources mid-upload) until Cancel or a successful submission clears it. The
  same `.condoc`-lock discipline as "Highlighted" applies, asserted before
  the first uploaded byte lands in the Impls folder.

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
