import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { apiUrl } from './api'
import { DiagramSVG, svgSize } from './Diagram'
import type { Diagram } from './types'

// The export controls. Each one renders the diagram with the same
// DiagramSVG the canvas uses, so an export looks like the canvas:
//
//   PNG    the step on the canvas, drawn at 2x
//   GIF    every step (or the one frame of a static diagram), put together
//          by the server (POST /api/export/gif, ../../gif.go)
//   video  every step played as the canvas plays it, dots moving along the
//          lit edges, recorded to WebM with MediaRecorder
//   web    a standalone HTML page: the SVGs and the canvas's step controls
//   D2     the server's D2 rendering (../../d2.go)

// FRAME_MS is how long each step shows in the GIF, the video and the web
// page's play button -- and on the canvas's.
export const FRAME_MS = 1500

const STATIC_VIDEO_MS = 3000

// VIDEO_FPS is the video's frame rate. Each step is FRAME_MS of frames, each
// drawn with the canvas's progress for its moment in the step.
const VIDEO_FPS = 30

export function svgMarkup(d: Diagram, step: number, progress?: number): string {
  return renderToStaticMarkup(createElement(DiagramSVG, { diagram: d, step, progress }))
}

// animationSteps are the frames a GIF or video plays: every step, or the
// whole diagram once when it's static.
function animationSteps(d: Diagram): number[] {
  return d.steps.length > 0 ? d.steps.map((_, i) => i) : [-1]
}

function download(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
}

// loadSVG loads SVG markup as an image, ready to draw onto a canvas.
async function loadSVG(markup: string): Promise<HTMLImageElement> {
  const url = URL.createObjectURL(new Blob([markup], { type: 'image/svg+xml' }))
  try {
    const img = new Image()
    await new Promise<void>((resolve, reject) => {
      img.onload = () => resolve()
      img.onerror = () => reject(new Error('the browser could not draw the diagram'))
      img.src = url
    })
    return img
  } finally {
    URL.revokeObjectURL(url)
  }
}

// renderCanvas draws one step of the diagram onto a canvas.
async function renderCanvas(d: Diagram, step: number, scale: number): Promise<HTMLCanvasElement> {
  const { width, height } = svgSize(d)
  const img = await loadSVG(svgMarkup(d, step))
  const c = document.createElement('canvas')
  c.width = Math.round(width * scale)
  c.height = Math.round(height * scale)
  c.getContext('2d')!.drawImage(img, 0, 0, c.width, c.height)
  return c
}

export async function exportPNG(d: Diagram, step: number) {
  const c = await renderCanvas(d, step, 2)
  const blob = await new Promise<Blob | null>(resolve => c.toBlob(resolve, 'image/png'))
  if (!blob) throw new Error('the browser could not make a PNG')
  download(blob, step >= 0 ? `${d.name}-step${step + 1}.png` : `${d.name}.png`)
}

export async function exportGIF(d: Diagram) {
  const frames = await Promise.all(animationSteps(d).map(async s => (await renderCanvas(d, s, 1)).toDataURL('image/png')))
  const res = await fetch(apiUrl('/api/export/gif'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ frames, delay_ms: FRAME_MS }),
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error || `GIF export: ${res.status} ${res.statusText}`)
  }
  download(await res.blob(), `${d.name}.gif`)
}

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms))

// videoFrames are the video's frames, one per 1/VIDEO_FPS s: each step
// FRAME_MS long, its dots moving as on the canvas while it plays, or the
// static diagram held for STATIC_VIDEO_MS. Frames that come out the same
// (once a step's dots have arrived) share one image.
async function videoFrames(d: Diagram): Promise<HTMLImageElement[]> {
  const markups: string[] = []
  if (d.steps.length > 0) {
    const perStep = Math.round((FRAME_MS / 1000) * VIDEO_FPS)
    d.steps.forEach((_, s) => {
      for (let i = 0; i < perStep; i++) markups.push(svgMarkup(d, s, i / perStep))
    })
  } else {
    const still = svgMarkup(d, -1)
    for (let i = 0; i < (STATIC_VIDEO_MS / 1000) * VIDEO_FPS; i++) markups.push(still)
  }
  const loaded = new Map<string, Promise<HTMLImageElement>>()
  for (const m of markups) if (!loaded.has(m)) loaded.set(m, loadSVG(m))
  return Promise.all(markups.map(m => loaded.get(m)!))
}

export async function exportVideo(d: Diagram) {
  if (typeof MediaRecorder === 'undefined') throw new Error('this browser cannot record video')
  const frames = await videoFrames(d)
  const { width, height } = svgSize(d)
  const c = document.createElement('canvas')
  // Video encoders want even dimensions.
  c.width = width + (width % 2)
  c.height = height + (height % 2)
  const ctx = c.getContext('2d')!
  ctx.fillStyle = '#1e1e1e'
  ctx.fillRect(0, 0, c.width, c.height)

  const mimeType = ['video/webm;codecs=vp9', 'video/webm;codecs=vp8', 'video/webm'].find(m => MediaRecorder.isTypeSupported(m))
  const rec = new MediaRecorder(c.captureStream(30), mimeType ? { mimeType } : undefined)
  const chunks: Blob[] = []
  rec.ondataavailable = e => {
    if (e.data.size > 0) chunks.push(e.data)
  }
  const stopped = new Promise<void>(resolve => {
    rec.onstop = () => resolve()
  })
  rec.start()
  // Play the frames against the clock, so the video runs in real time even
  // if the browser falls behind, and keep repainting: the stream only
  // carries frames the canvas was drawn in.
  const frameMs = 1000 / VIDEO_FPS
  const start = performance.now()
  for (let elapsed = 0; elapsed < frames.length * frameMs; elapsed = performance.now() - start) {
    ctx.drawImage(frames[Math.floor(elapsed / frameMs)], 0, 0, width, height)
    await sleep(frameMs / 2)
  }
  rec.stop()
  await stopped
  download(new Blob(chunks, { type: 'video/webm' }), `${d.name}.webm`)
}

const HTML_ESCAPES: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }

function escapeHTML(s: string): string {
  return s.replace(/[&<>"]/g, c => HTML_ESCAPES[c])
}

// webPage is the web export: the canvas's own presentation -- the diagram,
// and for a dynamic one the overview plus a frame per step with the same
// prev/next/play controls -- as one self-contained HTML file.
export function webPage(d: Diagram): string {
  const steps = d.steps.length > 0 ? [-1, ...d.steps.map((_, i) => i)] : [-1]
  // Each frame gets its own arrowhead marker ids: a url(#id) that resolves
  // into a hidden (display: none) frame draws no arrowheads.
  const frames = steps
    .map((s, i) => `<div class="dg-frame${i === 0 ? ' on' : ''}">${svgMarkup(d, s).replace(/dg-arrow/g, `dg${i}-arrow`)}</div>`)
    .join('\n')
  const controls =
    d.steps.length > 0
      ? `<div class="dg-controls"><button id="dg-prev">&#9664;</button><button id="dg-play">play</button><button id="dg-next">&#9654;</button></div>
<script>
(function () {
  var f = document.querySelectorAll('.dg-frame'), i = 0, t = null, play = document.getElementById('dg-play')
  function show(n) { f[i].classList.remove('on'); i = (n + f.length) % f.length; f[i].classList.add('on') }
  document.getElementById('dg-prev').onclick = function () { show(i - 1) }
  document.getElementById('dg-next').onclick = function () { show(i + 1) }
  play.onclick = function () {
    if (t) { clearInterval(t); t = null; play.textContent = 'play'; return }
    t = setInterval(function () { show(i + 1) }, ${FRAME_MS}); play.textContent = 'pause'
  }
})()
</script>`
      : ''
  return `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${escapeHTML(d.title)}</title>
<style>
body { margin: 0; padding: 16px; background: #1a1a1a; color: #d4d4d4; font-family: Menlo, Monaco, Consolas, monospace; display: flex; flex-direction: column; align-items: center; gap: 10px; }
.dg-frame { display: none; } .dg-frame.on { display: block; }
.dg-frame svg { max-width: 100%; height: auto; }
.dg-controls { display: flex; gap: 6px; }
button { font-family: inherit; font-size: 12px; background: #3c3c3c; color: #ccc; border: none; border-radius: 3px; padding: 5px 12px; cursor: pointer; }
</style>
</head>
<body>
${frames}
${controls}
</body>
</html>
`
}

export function exportWeb(d: Diagram) {
  download(new Blob([webPage(d)], { type: 'text/html' }), `${d.name}.html`)
}

export function exportD2(d: Diagram, d2: string) {
  download(new Blob([d2], { type: 'text/plain' }), `${d.name}.d2`)
}

export function exportDTT(d: Diagram, dtt: string) {
  download(new Blob([dtt], { type: 'text/yaml' }), `${d.name}.dtt.yaml`)
}
