import type { Clip, Composition, MediaKind } from './types'

// ---- Rendering ----
//
// composer renders in the browser (see ../compositions.go): the video track
// is drawn onto a canvas frame by frame, the audio track is scheduled on a
// Web Audio graph, and -- for a render rather than a preview -- the
// canvas's captured stream and the graph's output are recorded together by
// MediaRecorder into one WebM. An audio composition records the graph's
// output alone, into an audio-only WebM.
//
// Video-track media are drawn silently; to hear a video's sound, add the
// same file to the audio track too.

// serverBase is the path this document was served under, so requests also
// work when reverse-proxied beneath a prefix (/compose/ via
// local-representative, /host/<id>/compose/ via agent-coordinator).
export function serverBase(): string {
  const dir = window.location.pathname.replace(/\/[^/]*\.[^/]*$/, '/')
  return dir.endsWith('/') ? dir.slice(0, -1) : dir
}

// sourceUrl is where a source file's bytes are served (../sources.go's
// handleSource).
export function sourceUrl(fileId: string): string {
  return `${serverBase()}/api/source/${encodeURIComponent(fileId)}`
}

// MEDIA_EXTENSIONS mirrors ../sources.go's mediaExtensions.
const MEDIA_EXTENSIONS: Record<string, MediaKind> = {
  mp3: 'audio', wav: 'audio', ogg: 'audio', oga: 'audio', m4a: 'audio', aac: 'audio', flac: 'audio', opus: 'audio', weba: 'audio',
  webm: 'video', mp4: 'video', m4v: 'video', mov: 'video', ogv: 'video',
  png: 'image', jpg: 'image', jpeg: 'image', gif: 'image', webp: 'image',
}

export function mediaKind(name: string | undefined): MediaKind | undefined {
  const m = /\.([^./]+)$/.exec(name ?? '')
  return m ? MEDIA_EXTENSIONS[m[1].toLowerCase()] : undefined
}

// A clip placed on the timeline once every duration is known.
interface Placed {
  clip: Clip
  index: number
  start: number
  duration: number
  image?: HTMLImageElement
  video?: HTMLVideoElement
  buffer?: AudioBuffer
  // While playing: how many times a drawn video frame moved on from the
  // one before, the media time the last was at, and when play() was last
  // asked for.
  frames: number
  lastTime: number
  lastPlay: number
}

export interface Timeline {
  video: Placed[]
  audio: Placed[]
  duration: number
  // holder keeps the video clips' elements in the document, off screen,
  // until the timeline is released.
  holder: HTMLDivElement
}

// release lets go of a timeline's video elements.
function release(tl: { holder: HTMLDivElement; video: Placed[] }) {
  for (const p of tl.video) {
    if (p.video) {
      p.video.pause()
      p.video.removeAttribute('src')
      p.video.load()
    }
  }
  tl.holder.remove()
}

// newHolder is an off-screen box for video elements. A video that's in the
// document keeps decoding frames for drawImage the way an on-page one does;
// one that never joined the document can be left without a current frame
// (or paused) by the browser's power saving, and a render then records only
// its title cards.
function newHolder(): HTMLDivElement {
  const holder = document.createElement('div')
  holder.setAttribute('aria-hidden', 'true')
  holder.style.cssText = 'position:fixed;left:-10000px;top:0;width:2px;height:2px;overflow:hidden;opacity:0.01;pointer-events:none'
  document.body.appendChild(holder)
  return holder
}

function clipLabel(track: string, i: number, clip: Clip): string {
  return `${track} clip ${i + 1}${clip.file_name ? ` (${clip.file_name})` : ''}`
}

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => resolve(img)
    img.onerror = () => reject(new Error('could not load the image'))
    img.src = url
  })
}

function loadVideo(url: string, holder: HTMLDivElement): Promise<HTMLVideoElement> {
  return new Promise((resolve, reject) => {
    const v = document.createElement('video')
    v.muted = true
    v.playsInline = true
    v.preload = 'auto'
    v.width = 2
    v.height = 2
    v.onloadedmetadata = () => resolve(v)
    v.onerror = () => reject(new Error('could not load the video'))
    holder.appendChild(v)
    v.src = url
  })
}

// waitFor resolves once ok() holds -- checked now and on each of events --
// or after ms, saying whether it held.
function waitFor(v: HTMLVideoElement, events: string[], ok: () => boolean, ms: number): Promise<boolean> {
  if (ok()) return Promise.resolve(true)
  return new Promise((resolve) => {
    const check = () => {
      if (!ok()) return
      done(true)
    }
    const timer = setTimeout(() => done(ok()), ms)
    const done = (held: boolean) => {
      clearTimeout(timer)
      events.forEach((e) => v.removeEventListener(e, check))
      resolve(held)
    }
    events.forEach((e) => v.addEventListener(e, check))
  })
}

// rewind puts a loaded video back at its start with a frame ready to draw.
// videoDuration's seek to the end leaves it there, and the seek back to 0
// is asynchronous: started playing before it lands, a WebM recorded by
// MediaRecorder (which has no index to seek with -- composer's own renders
// are such files) can sit on its last frame, or none, for its whole clip.
async function rewind(v: HTMLVideoElement): Promise<void> {
  if (v.currentTime !== 0) v.currentTime = 0
  if (!(await waitFor(v, ['seeked'], () => !v.seeking, 10000))) throw new Error('could not seek back to its start')
  if (!(await waitFor(v, ['loadeddata', 'canplay', 'seeked'], () => v.readyState >= HTMLMediaElement.HAVE_CURRENT_DATA, 10000))) {
    throw new Error('no picture could be decoded from it')
  }
  if (!v.videoWidth || !v.videoHeight) throw new Error('it has no picture (an audio-only file? put it on the audio track)')
}

async function loadAudio(ctx: BaseAudioContext, url: string): Promise<AudioBuffer> {
  const resp = await fetch(url)
  if (!resp.ok) throw new Error(`fetching it failed: ${resp.status} ${(await resp.text()).trim()}`)
  return ctx.decodeAudioData(await resp.arrayBuffer())
}

// videoDuration is a loaded video's length, or NaN when the file doesn't say
// (a MediaRecorder WebM often reports Infinity until it's been read through).
async function videoDuration(v: HTMLVideoElement): Promise<number> {
  if (Number.isFinite(v.duration)) return v.duration
  await new Promise<void>((resolve) => {
    v.ondurationchange = () => {
      if (Number.isFinite(v.duration)) resolve()
    }
    v.currentTime = 1e9
    setTimeout(resolve, 5000)
  })
  const d = v.duration
  v.currentTime = 0
  return Number.isFinite(d) ? d : NaN
}

// prepare loads every media clip's source and lays both tracks out back to
// back. An error names the clip it's about.
export async function prepare(comp: Composition, ctx: BaseAudioContext): Promise<Timeline> {
  const video: Placed[] = []
  const holder = newHolder()
  try {
    let t = 0
    for (const [i, clip] of comp.video.entries()) {
      const p: Placed = { clip, index: i, start: t, duration: clip.duration ?? 0, frames: 0, lastTime: -1, lastPlay: 0 }
      video.push(p)
      try {
        if (clip.kind === 'media') {
          if (!clip.file_id) throw new Error('no file chosen')
          const url = sourceUrl(clip.file_id)
          if (mediaKind(clip.file_name) === 'image') {
            p.image = await loadImage(url)
          } else {
            p.video = await loadVideo(url, holder)
            if (!p.duration) p.duration = await videoDuration(p.video)
            await rewind(p.video)
          }
        }
      } catch (err) {
        throw new Error(`${clipLabel('video', i, clip)}: ${err instanceof Error ? err.message : String(err)}`)
      }
      if (!(p.duration > 0)) throw new Error(`${clipLabel('video', i, clip)}: unknown length -- give it a duration`)
      t += p.duration
    }
    const audio = await prepareAudio(comp, ctx)
    const audioEnd = audio.reduce((end, p) => Math.max(end, p.start + p.duration), 0)
    return { video, audio, duration: Math.max(t, audioEnd), holder }
  } catch (err) {
    release({ holder, video })
    throw err
  }
}

async function prepareAudio(comp: Composition, ctx: BaseAudioContext): Promise<Placed[]> {
  const audio: Placed[] = []
  let t = 0
  for (const [i, clip] of comp.audio.entries()) {
    const p: Placed = { clip, index: i, start: t, duration: clip.duration ?? 0, frames: 0, lastTime: -1, lastPlay: 0 }
    if (clip.kind === 'media') {
      try {
        if (!clip.file_id) throw new Error('no file chosen')
        p.buffer = await loadAudio(ctx, sourceUrl(clip.file_id))
      } catch (err) {
        throw new Error(`${clipLabel('audio', i, clip)}: ${err instanceof Error ? err.message : String(err)}`)
      }
      if (!p.duration) p.duration = p.buffer.duration
    }
    if (!(p.duration > 0)) throw new Error(`${clipLabel('audio', i, clip)}: needs a duration`)
    audio.push(p)
    t += p.duration
  }
  return audio
}

// ---- Drawing ----

function wrapLines(g: CanvasRenderingContext2D, text: string, maxWidth: number): string[] {
  const lines: string[] = []
  for (const para of text.split('\n')) {
    let line = ''
    for (const word of para.split(/\s+/).filter(Boolean)) {
      const next = line ? `${line} ${word}` : word
      if (line && g.measureText(next).width > maxWidth) {
        lines.push(line)
        line = word
      } else {
        line = next
      }
    }
    lines.push(line)
  }
  return lines
}

function drawTitle(g: CanvasRenderingContext2D, w: number, h: number, clip: Clip) {
  g.fillStyle = clip.background || '#000000'
  g.fillRect(0, 0, w, h)
  const size = Math.max(12, Math.round(h * 0.08))
  g.font = `600 ${size}px system-ui, sans-serif`
  g.fillStyle = clip.color || '#ffffff'
  g.textAlign = 'center'
  g.textBaseline = 'middle'
  const lines = wrapLines(g, clip.text ?? '', w * 0.85)
  const lineHeight = size * 1.25
  const top = h / 2 - ((lines.length - 1) * lineHeight) / 2
  lines.forEach((line, i) => g.fillText(line, w / 2, top + i * lineHeight))
}

// drawFitted draws src centered and scaled to fit, over black.
function drawFitted(g: CanvasRenderingContext2D, w: number, h: number, src: CanvasImageSource, sw: number, sh: number) {
  g.fillStyle = '#000000'
  g.fillRect(0, 0, w, h)
  if (!sw || !sh) return
  const scale = Math.min(w / sw, h / sh)
  const dw = sw * scale
  const dh = sh * scale
  g.drawImage(src, (w - dw) / 2, (h - dh) / 2, dw, dh)
}

// ---- Playing ----

// scheduleAudio queues the audio track on ctx, t0 being timeline zero.
function scheduleAudio(ctx: AudioContext, out: AudioNode, tl: Timeline, t0: number): AudioScheduledSourceNode[] {
  const nodes: AudioScheduledSourceNode[] = []
  for (const p of tl.audio) {
    const at = t0 + p.start
    if (p.clip.kind === 'tone') {
      const osc = ctx.createOscillator()
      osc.frequency.value = p.clip.frequency || 440
      const gain = ctx.createGain()
      // A short fade in and out, so the beep doesn't click.
      const fade = Math.min(0.01, p.duration / 4)
      gain.gain.setValueAtTime(0, at)
      gain.gain.linearRampToValueAtTime(0.4, at + fade)
      gain.gain.setValueAtTime(0.4, at + p.duration - fade)
      gain.gain.linearRampToValueAtTime(0, at + p.duration)
      osc.connect(gain).connect(out)
      osc.start(at)
      osc.stop(at + p.duration)
      nodes.push(osc)
    } else if (p.clip.kind === 'media' && p.buffer) {
      const src = ctx.createBufferSource()
      src.buffer = p.buffer
      src.connect(out)
      src.start(at, 0, Math.min(p.duration, p.buffer.duration))
      nodes.push(src)
    }
  }
  return nodes
}

export interface PlayOptions {
  canvas: HTMLCanvasElement
  // record: also capture what's played into a WebM, returned by done.
  record?: boolean
  onProgress?: (t: number, duration: number) => void
}

export interface Playback {
  // done settles once the composition has played through (or stop was
  // called): with the recording when asked for one.
  done: Promise<Blob | null>
  stop: () => void
}

// RECORDER_TYPES are tried in order; the first the browser supports wins.
// AUDIO_RECORDER_TYPES are an audio composition's (../compositions.go).
const RECORDER_TYPES = ['video/webm;codecs=vp9,opus', 'video/webm;codecs=vp8,opus', 'video/webm']
const AUDIO_RECORDER_TYPES = ['audio/webm;codecs=opus', 'audio/webm']

// play prepares comp and plays it onto opts.canvas, with its sound to the
// speakers -- or, when recording, to the recording only.
export async function play(comp: Composition, opts: PlayOptions): Promise<Playback> {
  const ctx = new AudioContext()
  const tl = await prepare(comp, ctx).catch((err) => {
    ctx.close()
    throw err
  })
  const { canvas } = opts
  canvas.width = comp.width
  canvas.height = comp.height
  const g = canvas.getContext('2d')
  if (!g) {
    release(tl)
    ctx.close()
    throw new Error('this browser has no 2D canvas')
  }

  let recorder: MediaRecorder | null = null
  const chunks: Blob[] = []
  let out: AudioNode = ctx.destination
  if (opts.record) {
    if (typeof MediaRecorder === 'undefined') {
      release(tl)
      ctx.close()
      throw new Error('this browser cannot record (no MediaRecorder)')
    }
    const dest = ctx.createMediaStreamDestination()
    out = dest
    // An audio composition records its sound alone: a pure audio track.
    const audioOnly = comp.output === 'audio'
    const stream = audioOnly
      ? new MediaStream(dest.stream.getAudioTracks())
      : new MediaStream([...canvas.captureStream(30).getVideoTracks(), ...dest.stream.getAudioTracks()])
    const mimeType = (audioOnly ? AUDIO_RECORDER_TYPES : RECORDER_TYPES).find((t) => MediaRecorder.isTypeSupported(t))
    recorder = new MediaRecorder(stream, mimeType ? { mimeType } : undefined)
    recorder.ondataavailable = (e) => {
      if (e.data.size > 0) chunks.push(e.data)
    }
  }

  await ctx.resume()
  // A little lead-in so everything scheduled starts on time.
  const t0 = ctx.currentTime + 0.15
  const nodes = scheduleAudio(ctx, out, tl, t0)
  recorder?.start(250)

  let stopped = false
  let current: Placed | null = null
  let finish: (b: Blob | null) => void = () => {}
  let fail: (err: Error) => void = () => {}
  const done = new Promise<Blob | null>((resolve, reject) => {
    finish = resolve
    fail = reject
  })

  // end stops playing. played: it reached its end rather than being
  // stopped, so a recording is checked before it's handed back -- one in
  // which a video clip never showed a frame would hold only the rest.
  const end = (played = false) => {
    if (stopped) return
    stopped = true
    nodes.forEach((n) => {
      try {
        n.stop()
      } catch {
        // already stopped
      }
    })
    current?.video?.pause()
    const unplayed = played && recorder ? tl.video.filter((p) => p.video && p.frames === 0) : []
    release(tl)
    const settle = (blob: Blob | null) => {
      ctx.close()
      if (unplayed.length) {
        const names = unplayed.map((p) => clipLabel('video', p.index, p.clip)).join(', ')
        fail(new Error(`${names} showed no picture while recording, so the render would hold only the other clips -- it wasn't saved. Keep the compose tab visible while it records, and check the file plays in the files tab`))
      } else {
        finish(blob)
      }
    }
    if (recorder && recorder.state !== 'inactive') {
      const type = recorder.mimeType || (comp.output === 'audio' ? 'audio/webm' : 'video/webm')
      recorder.onstop = () => settle(new Blob(chunks, { type }))
      recorder.stop()
    } else {
      settle(null)
    }
  }

  const frame = () => {
    if (stopped) return
    const t = ctx.currentTime - t0
    if (t >= tl.duration) {
      opts.onProgress?.(tl.duration, tl.duration)
      end(true)
      return
    }
    const p = t < 0 ? null : tl.video.find((v) => t >= v.start && t < v.start + v.duration) ?? null
    if (p !== current) {
      current?.video?.pause()
      current = p
      if (p?.video) {
        // prepare left it at its start; only a late frame needs a seek.
        const at = t - p.start
        if (at > 0.25) p.video.currentTime = at
        p.lastPlay = performance.now()
        p.video.play().catch(() => {})
      }
    }
    if (!p) {
      g.fillStyle = '#000000'
      g.fillRect(0, 0, comp.width, comp.height)
    } else if (p.clip.kind === 'title') {
      drawTitle(g, comp.width, comp.height, p.clip)
    } else if (p.image) {
      drawFitted(g, comp.width, comp.height, p.image, p.image.naturalWidth, p.image.naturalHeight)
    } else if (p.video) {
      const v = p.video
      // A video the browser paused, or never started, is asked again -- at
      // most twice a second -- while its slot lasts.
      if (v.paused && !v.ended && performance.now() - p.lastPlay > 500) {
        p.lastPlay = performance.now()
        v.play().catch(() => {})
      }
      if (v.readyState >= HTMLMediaElement.HAVE_CURRENT_DATA && v.videoWidth) {
        drawFitted(g, comp.width, comp.height, v, v.videoWidth, v.videoHeight)
        // Counted only once it moves: one frozen frame isn't the clip.
        if (p.lastTime < 0) p.lastTime = v.currentTime
        else if (v.currentTime !== p.lastTime) {
          p.lastTime = v.currentTime
          p.frames++
        }
      } else if (p.frames === 0) {
        g.fillStyle = '#000000'
        g.fillRect(0, 0, comp.width, comp.height)
      }
    }
    opts.onProgress?.(Math.max(0, t), tl.duration)
    requestAnimationFrame(frame)
  }
  requestAnimationFrame(frame)

  return { done, stop: () => end() }
}

// uploadRender hands a finished recording to the server, which saves it to
// local-representative's files area (../sources.go's handleRender). jobId is
// set when it was rendered for a render job (../renderjobs.go).
export async function uploadRender(compositionId: string, blob: Blob, jobId?: string): Promise<void> {
  const job = jobId ? `&job=${encodeURIComponent(jobId)}` : ''
  const resp = await fetch(`${serverBase()}/api/render?composition=${encodeURIComponent(compositionId)}${job}`, {
    method: 'POST',
    headers: { 'Content-Type': blob.type || 'video/webm' },
    body: blob,
  })
  if (!resp.ok) {
    let message = `${resp.status}`
    try {
      const body = (await resp.json()) as { error?: string }
      if (body.error) message = body.error
    } catch {
      // not JSON
    }
    throw new Error(message)
  }
}

// trackDuration is a track's known length: clips without a duration (media
// played at their own length) count as 0 until they're loaded.
export function trackDuration(clips: Clip[]): number {
  return clips.reduce((sum, c) => sum + (c.duration ?? 0), 0)
}
