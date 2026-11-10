# composer

composer takes simple resources, such as IANAR's screen recordings and the
Polly clips The Conversationalist makes, and puts them together into new
media. It's a Go server with an embedded React frontend, like
session-manager. local-representative launches it as the **compose** tab.

```
 IANAR ──recording──▶ ┌──────────────────────────┐
                      │ local-representative's   │ ◀──── composer-<id>-<stamp>.webm
 TC Polly ──clip────▶ │ files area (host-cache)  │                 ▲
                      └────────────┬─────────────┘                 │ POST /api/render
                                   │ GET /api/files[/<id>]         │
                                   ▼                               │
                      ┌──────────────────────────┐   recording   ┌─┴──────────────┐
                      │ composer (:8089)         │ ────────────▶ │ browser:       │
                      │ compositions/<id>.json   │ ◀──────────── │ canvas + Web   │
                      └──────────────────────────┘   sources     │ Audio + Media- │
                                                                 │ Recorder       │
                                                                 └────────────────┘
```

## Compositions

A composition has two tracks. Each track is a list of clips that play back
to back from time zero:

| Track | Clip | What it is |
| --- | --- | --- |
| video | `title` | text on a solid background, for `duration` seconds |
| video | `media` | an image (needs `duration`) or a video (its own length, unless `duration` cuts it short), drawn without sound |
| audio | `media` | an audio file, or the sound of a video file (if the browser can decode it), at its own length or cut to `duration` |
| audio | `tone` | a sine beep at `frequency` Hz |
| audio | `silence` | a gap |

The render lasts as long as the longer track. Black fills the video track
once it runs out.

Compositions are saved as `~/.ufa/composer/compositions/<id>.json` (see
`--data-dir` and [docs/CurrentPersistentFiles.md](../docs/CurrentPersistentFiles.md)).

## Sources

composer stores no media itself. Its sources are the audio, video and image
files in local-representative's files area. The UI tags where each came
from, based on its name:

- **IANAR:** `ianar-recording-*`, `ianar-clip-*` and `ianar-capture-*`. Save
  a recording in IANAR, or with a control sequence's `record-stop`.
- **Polly:** `polly-*.mp3`. Tick **save** under the Polly button in The
  Conversationalist's notepad, and each clip it speaks is also saved here.
- **composer:** earlier renders, which can be used again as sources.

Anything else uploaded in the files tab works too, if the browser can play
it. Files in the host-cache are swept after an hour unless they're held or
persisted, so hold the sources (and renders) you want to keep.

## Rendering

The browser does the rendering:

1. It draws the video track onto a canvas.
2. It plays the audio track through Web Audio.
3. MediaRecorder records both into a WebM.
4. composer uploads the WebM to local-representative's files area as
   `composer-<id>-<stamp>.webm` and lists it under the composition's
   renders.

**Preview** plays the current draft without recording it. **Render** works
on the saved composition, so save your changes first.

A composition's **output** (beside its size in the editor, `"output"` in its
JSON) is `video` by default. Set to `audio`, the composition has no video
clips and a render records only the audio track: a pure audio track in an
audio-only WebM (Opus), saved as `composer-<id>-<stamp>.weba`. dtt's
`localrep-polly-configs` example puts Polly clips together this way.

### Render jobs, and dtt

A program can save and render a composition over HTTP without anyone
pressing Render. This is how dtt's built-in `composer` tool works (see
[dtt's README](../dtt/README.md#the-composer-tool)):

1. `PUT /api/compositions/<id>` saves it.
2. `POST /api/render-jobs` `{composition, renderer}` starts a job.
3. `GET /api/render-jobs/<job>?wait=<seconds>` waits for it to finish
   (at most 10 minutes per call) and answers with its `state` (`queued`,
   `waiting`, `rendering`, `done` or `failed`) and, once done, its `result`
   (`file_id`, `name`, `size`).

A job's `renderer` is one of these:

- **`ffmpeg`**: composer renders it itself (`ffmpeg.go`), with nothing open.
  It fetches the media sources into a scratch directory, measures with
  ffprobe any that have no `duration` (reading through a WebM whose header
  doesn't say, as MediaRecorder's often don't), and writes VP8/Opus WebM (Opus alone for an audio composition). Title cards
  use a font found on the host (`--font-file` to choose one), so the
  lettering differs a little from a browser render.
- **`browser`**: an open compose tab renders it as its Render button would.
  A banner lists waiting jobs. A tab takes one on its own once you've
  clicked in the page (browsers won't play sound before that), or when you
  press **render now**. The tab must stay visible while it records. A job
  that no tab takes within 10 minutes fails.
- **`auto`**, the default: ffmpeg when composer finds it, otherwise a tab.

Both renderers save through the same path as a Render button, so the file
lands in local-representative's files area and in the composition's renders.
Jobs are kept in memory only, for an hour after they finish.

**Does this node have ffmpeg?** The sidebar shows it above the
local-representative widget: green "ffmpeg: render jobs need no tab", or
amber "no ffmpeg: render jobs need this tab". Hover for ffmpeg's and
ffprobe's paths, or why ffmpeg wasn't found, and press **↻** to check again
after installing it (no restart needed). `GET /api/status` reports the same
as `ffmpeg`, `ffprobe` and `ffmpeg_error`.

**Which renderer made a render?** Each render in a composition's
**renders** list has an `ffmpeg` or `browser tab` badge (renders from before
this was recorded have none), kept as `renderer` in the composition's JSON.
The "rendered …" notice says it too, and composer's log has a
`render job <id> started for <composition> (renderer …)` line when a job
starts and a `render saved: <file> (…, renderer …, render job <id>)` line
when any render is saved. A Render button press is always `browser`.

Neither renderer saves a render that has lost clips. The browser one fails
the render if a video clip's picture never moved while it recorded, and the
ffmpeg one fails it if a video clip's file has no picture, or if the output
comes out more than a second shorter than the composition. Composer's own
renders, used as clips in another composition (as a demo's compose phase
does), are MediaRecorder WebMs with no index. So the browser renderer keeps
its source videos in the page, off screen, and rewinds each one to a
decoded first frame before the recording starts.

```sh
dtt run composer --input dtt/examples/compositions/composer-hello.call.yaml   # from the repo root
```

Rendering runs in real time, so a 30-second composition takes 30 seconds.
Keep the tab visible while it records: a hidden tab stops drawing frames.
A render can be at most 63 MiB, which is local-representative's upload
limit.

## Hello world

The `hello-world` example has two title cards over two beeps. It needs no
source files, so it's the quickest way to check the whole path:

1. Run `make deploy-dev-binaries` (or `make build` here), then launch
   **compose** from local-representative's system tab, or from the launch
   button in the empty compose tab.
2. In the sidebar's **examples**, click **+ use** beside "Hello, world".
   This saves a copy called `hello-world` and opens it.
3. Click **▶ Preview**. You should see "Hello, world!" for 2s and then
   "composed by composer" for 2s, with a 440 Hz beep at the start and a
   660 Hz beep at the second card.
4. Click **● Render**. After about 4 seconds the composition's **renders**
   list shows `composer-hello-world-<stamp>.webm`, and the same file
   appears in local-representative's files tab. **▶** plays it back in
   place.

To use a real source next, save an IANAR recording or a Polly clip. Click
**Refresh** under **sources**, add a `media` clip to the right track and
pick the file.

## Wiring

| | |
| --- | --- |
| Port | `--port` 8089; local-representative's `--compose-port` |
| Name to local-representative | `compose` (`--name`) |
| Proxied at | `/compose/` on local-representative, `/host/<id>/compose/` on agent-coordinator |
| State message | `compose-state` `{http_port, compositions}` → local-representative → agent-coordinator (`lr-compose-state`) |
| Commands | `__compose:refresh` |
| HTTP | `GET /api/source/<file id>` (passes Range through), `POST /api/render?composition=<id>` |
| HTTP API (`api.go`, `renderjobs.go`) | `GET /api/status` `{repr_status, ffmpeg, ffprobe, ffmpeg_error, tabs}`; `GET`/`PUT`/`DELETE /api/compositions[/<id>]` (`DELETE /api/compositions` deletes them all, as the sidebar's "Delete all" does); `POST /api/render-jobs`, `GET /api/render-jobs/<id>?wait=` |
| Rendering without a tab | ffmpeg, from `--ffmpeg` (default `ffmpeg` on PATH) |

## Building and testing

```
make build        # frontend, then the Go binary
make test         # go test ./...
make dev          # Go server without the embedded frontend
make dev-frontend # Vite dev server, proxying /ws and /api to :8089
```

As with session-manager, `go test` needs `frontend/dist` to exist (it's
embedded). Run `make build` once, or `make clean`, which writes a
placeholder.
