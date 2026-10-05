import { useEffect, useState } from 'react'
import type {
  ClipFrame,
  ClipResultMsg,
  InspectResultMsg,
  OCRLine,
  ReprStatus,
  SequenceDef,
  SequenceOutput,
  SequenceResultMsg,
} from './types'

// Pieces shared by the simple, sequence-v1 and sequence-v2 tabs (App.tsx,
// SequenceV2.tsx): result previews, Save to file, and run state.

export type SaveStatus = { kind: 'saving' | 'success' | 'error'; message?: string }

// Preview is whatever a preview pane shows: on the simple tab the latest
// capture, clip or screen inspection, whichever finished last; on the
// sequence tabs the last run's recording. A compositor recording is a
// video; a frame-sampled one (see clip.go's fallback) is played back by
// FramePlayer; an inspection is drawn by InspectView. artifactId, when set,
// is what "Save to file" asks the backend to upload (see artifacts.go).
export type Preview = (
  | { kind: 'image'; url: string }
  | { kind: 'video'; url: string }
  | { kind: 'frames'; frames: ClipFrame[]; durationMs: number }
  | { kind: 'inspect'; result: InspectResultMsg }
) & { artifactId?: string }

// clipPreview turns a successful clip/recording result into a Preview, or
// null if it has nothing to play.
export function clipPreview(p: ClipResultMsg): Preview | null {
  const artifactId = p.artifact_id
  if (p.success && p.video_url) return { kind: 'video', url: dataURLToObjectURL(p.video_url), artifactId }
  if (p.success && p.frames?.length) return { kind: 'frames', frames: p.frames, durationMs: p.duration_ms ?? 0, artifactId }
  return null
}

export type StepStatus = 'pending' | 'running' | 'success' | 'error' | 'skipped'

export const STEP_ICONS: Record<StepStatus, string> = {
  pending: '○',
  running: '◌',
  success: '✓',
  error: '✗',
  skipped: '–',
}

// SeqRun is a sequence tab's latest run: live per-step status while it
// runs, then the final result and its recording. A sequence-v2 run also
// carries its steps as compiled (def) and what it has printed (outputs).
export type SeqRun = {
  sequenceId: string
  running: boolean
  steps: { status: StepStatus; message?: string; durationMs?: number; imageUrl?: string }[]
  result?: SequenceResultMsg
  recording?: Preview | null
  lostConnection?: boolean
  def?: SequenceDef
  outputs?: SequenceOutput[]
}

// dataURLToObjectURL turns a base64 data: URL into a blob: URL. Video
// elements seek reliably in a blob: URL, and it avoids holding a
// multi-megabyte string in the DOM.
export function dataURLToObjectURL(dataURL: string): string {
  const comma = dataURL.indexOf(',')
  const mime = dataURL.slice(0, comma).replace(/^data:/, '').replace(/;base64$/, '')
  const bin = atob(dataURL.slice(comma + 1))
  const bytes = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
  return URL.createObjectURL(new Blob([bytes], { type: mime }))
}

// ---- Frame player: playback for a frame-sampled native clip ----

interface FramePlayerProps {
  frames: ClipFrame[]
  durationMs: number
}

// FramePlayer plays a frame-sampled clip back at the pace it was captured,
// looping, with play/pause and a scrubber.
function FramePlayer({ frames, durationMs }: FramePlayerProps) {
  const [index, setIndex] = useState(0)
  const [playing, setPlaying] = useState(true)

  useEffect(() => {
    setIndex(0)
    setPlaying(true)
  }, [frames])

  const current = Math.min(index, frames.length - 1)

  useEffect(() => {
    if (!playing || frames.length < 2) return
    const next = current + 1 < frames.length ? current + 1 : 0
    const delay =
      next === 0 ? Math.max(0, durationMs - frames[current].at_ms) : frames[next].at_ms - frames[current].at_ms
    const t = setTimeout(() => setIndex(next), delay)
    return () => clearTimeout(t)
  }, [playing, current, frames, durationMs])

  return (
    <div className="robot-clip-player">
      <img src={frames[current].image_url} alt={`clip frame ${current + 1}`} />
      <div className="robot-clip-controls">
        <button className="btn-secondary" disabled={frames.length < 2} onClick={() => setPlaying((p) => !p)}>
          {playing ? 'Pause' : 'Play'}
        </button>
        <input
          type="range"
          min={0}
          max={frames.length - 1}
          value={current}
          onChange={(e) => {
            setPlaying(false)
            setIndex(Number(e.target.value))
          }}
        />
        <span className="robot-clip-time">
          {(frames[current].at_ms / 1000).toFixed(1)}s / {(durationMs / 1000).toFixed(1)}s
        </span>
      </div>
    </div>
  )
}

// ---- Inspect view: a capture with the text read off it boxed ----

// InspectView draws an inspection's capture with every line of text it read
// outlined: lines matching the requested text in green, lines merely
// containing it in amber, the rest faintly. Boxes are placed in percentages
// of the capture so they track the image at any display size. Tapping a box
// shows its text; the full reading is listed underneath.
function InspectView({ result }: { result: InspectResultMsg }) {
  const [picked, setPicked] = useState<OCRLine | null>(null)
  useEffect(() => setPicked(null), [result])

  const box = (l: OCRLine, cls: string, key: string) => (
    <div
      key={key}
      className={`inspect-box ${cls}${picked === l ? ' inspect-box-picked' : ''}`}
      style={{
        left: `${(l.x / result.width) * 100}%`,
        top: `${(l.y / result.height) * 100}%`,
        width: `${(l.w / result.width) * 100}%`,
        height: `${(l.h / result.height) * 100}%`,
      }}
      title={l.text}
      onClick={() => setPicked(l)}
    />
  )
  const matches = result.matches ?? []
  const partial = result.partial ?? []
  const flagged = new Set([...matches, ...partial].map((l) => `${l.x},${l.y}`))
  const lines = result.lines ?? []

  return (
    <div className="inspect-view">
      <div className="inspect-image">
        <img src={result.image_url} alt="screen inspection" />
        {lines.filter((l) => !flagged.has(`${l.x},${l.y}`)).map((l, i) => box(l, 'inspect-box-line', `l${i}`))}
        {partial.map((l, i) => box(l, 'inspect-box-partial', `p${i}`))}
        {matches.map((l, i) => box(l, 'inspect-box-match', `m${i}`))}
      </div>
      {picked && (
        <div className="inspect-picked">
          "{picked.text}" at ({picked.x}, {picked.y}) · {picked.w}×{picked.h}
        </div>
      )}
      <details className="inspect-lines">
        <summary>
          {lines.length} lines read from a {result.width}×{result.height} capture
        </summary>
        <ol>
          {lines.map((l, i) => (
            <li key={i}>
              <span className="inspect-line-at">
                ({l.x}, {l.y})
              </span>{' '}
              {l.text}
            </li>
          ))}
        </ol>
      </details>
    </div>
  )
}

// PreviewView renders a Preview, or placeholder text when there is none.
export function PreviewView({ preview, placeholder }: { preview: Preview | null | undefined; placeholder: string }) {
  return (
    <div className="robot-preview">
      {preview?.kind === 'image' && <img src={preview.url} alt="capture" />}
      {preview?.kind === 'video' && <video src={preview.url} controls autoPlay loop muted playsInline />}
      {preview?.kind === 'frames' && <FramePlayer frames={preview.frames} durationMs={preview.durationMs} />}
      {preview?.kind === 'inspect' && <InspectView result={preview.result} />}
      {!preview && <span className="robot-preview-placeholder">{placeholder}</span>}
    </div>
  )
}

// ---- Save to file ----

interface SaveToFileProps {
  artifactId: string
  label: string
  connected: boolean
  reprStatus: ReprStatus
  status: SaveStatus | undefined
  onSave: (id: string) => void
}

// SaveToFile uploads a kept result into local-representative's files area --
// its host-cache, also listed for this host in agent-coordinator's files tab
// -- like the other sub-apps' "Save to File"/"to file" buttons. The backend
// does the upload (see artifacts.go), so it needs IANAR's link to
// local-representative.
export function SaveToFile({ artifactId, label, connected, reprStatus, status, onSave }: SaveToFileProps) {
  const linked = reprStatus === 'connected'
  return (
    <div className="robot-save">
      <button
        className="btn-secondary"
        disabled={!connected || !linked || status?.kind === 'saving'}
        title={linked ? "upload into local-representative's files area" : 'connect to local-representative first'}
        onClick={() => onSave(artifactId)}
      >
        {status?.kind === 'saving' ? 'Saving…' : label}
      </button>
      {status && status.kind !== 'saving' && (
        <span className={`robot-save-status robot-save-${status.kind}`}>{status.message}</span>
      )}
      {!status && !linked && <span className="robot-save-status">connect to local-representative to save</span>}
    </div>
  )
}
