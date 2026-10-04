import { useCallback, useEffect, useRef, useState } from 'react'
import type {
  CaptureResultMsg,
  CircleMouseResultMsg,
  ClipFrame,
  ClipResultMsg,
  ModeMismatchMsg,
  ReprStatus,
  ReprStatusMsg,
  SelfInfoMsg,
  SequenceDef,
  SequenceDefsMsg,
  SequenceProgressMsg,
  SequenceResultMsg,
} from './types'

// ---- WebSocket hook ----
//
// Wires up the representable connect/disconnect widget, dev-mode banner, and
// version-mismatch reload check shared by every sub-app in this repo, plus
// IANAR's own capture-native/capture-browser/circle-mouse/clip-native
// channels (see condocs/InitialRobot.md, robot.go and clip.go) and the
// sequence-v1 runner (sequence.go).

type CaptureStatus = { kind: 'idle' | 'pending' | 'error'; message?: string }
type CircleStatus = { kind: 'idle' | 'running' | 'success' | 'error'; message?: string }
type ClipStatus = { kind: 'idle' | 'recording' | 'success' | 'error'; message?: string }

// Preview is whatever a preview pane shows: on the simple tab the latest
// capture or clip, whichever finished last; on the sequence-v1 tab the last
// run's recording. A compositor recording is a video; a frame-sampled one
// (see clip.go's fallback) is played back by FramePlayer.
type Preview =
  | { kind: 'image'; url: string }
  | { kind: 'video'; url: string }
  | { kind: 'frames'; frames: ClipFrame[]; durationMs: number }

// clipPreview turns a successful clip/recording result into a Preview, or
// null if it has nothing to play.
function clipPreview(p: ClipResultMsg): Preview | null {
  if (p.success && p.video_url) return { kind: 'video', url: dataURLToObjectURL(p.video_url) }
  if (p.success && p.frames?.length) return { kind: 'frames', frames: p.frames, durationMs: p.duration_ms ?? 0 }
  return null
}

type StepStatus = 'pending' | 'running' | 'success' | 'error' | 'skipped'

// SeqRun is the sequence-v1 tab's latest run: live per-step status while it
// runs, then the final result and its recording.
type SeqRun = {
  sequenceId: string
  running: boolean
  steps: { status: StepStatus; message?: string; durationMs?: number }[]
  result?: SequenceResultMsg
  recording?: Preview | null
  lostConnection?: boolean
}

// dataURLToObjectURL turns a base64 data: URL into a blob: URL. Video
// elements seek reliably in a blob: URL, and it avoids holding a
// multi-megabyte string in the DOM.
function dataURLToObjectURL(dataURL: string): string {
  const comma = dataURL.indexOf(',')
  const mime = dataURL.slice(0, comma).replace(/^data:/, '').replace(/;base64$/, '')
  const bin = atob(dataURL.slice(comma + 1))
  const bytes = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
  return URL.createObjectURL(new Blob([bytes], { type: mime }))
}

// browserCaptureUnsupportedReason: why the Screen Capture API
// (getDisplayMedia) is unavailable in this tab, named precisely instead of
// assumed -- the previous revision of this check treated "missing API" as
// synonymous with "mobile browser", but a desktop browser hits the exact
// same missing-function symptom when the *connection* isn't a secure
// context (plain http:// on anything but localhost loses
// navigator.mediaDevices.getDisplayMedia regardless of device), e.g. when
// reached remotely through agent-coordinator's reverse proxy today, ahead
// of the Tailscale Funnel HTTPS front door described in
// agent-coordinator/web-exposure-poc/ actually being brought up. Checked
// once at module load since it depends only on the browser/connection, not
// any app state.
const browserCaptureUnsupportedReason: string | null =
  typeof navigator === 'undefined'
    ? 'no browser APIs available'
    : typeof window !== 'undefined' && !window.isSecureContext
      ? "this connection isn't secure (needs HTTPS, or http://localhost) -- a plain http:// remote connection loses screen capture on every browser, desktop included"
      : !navigator.mediaDevices || typeof navigator.mediaDevices.getDisplayMedia !== 'function'
        ? 'this browser has no screen-picker API for web pages to use (expected on mobile browsers)'
        : null

const browserCaptureSupported = browserCaptureUnsupportedReason === null

function useRobotWS() {
  const [connected, setConnected] = useState(false)
  const [reprStatus, setReprStatus] = useState<ReprStatus>('disconnected')
  const [reprHost, setReprHost] = useState('')
  const [reprPort, setReprPort] = useState('')
  const [reprAutoConnect, setReprAutoConnect] = useState(false)
  const [devMode, setDevMode] = useState(false)
  const [version, setVersion] = useState('')
  const [modeMismatch, setModeMismatch] = useState<ModeMismatchMsg | null>(null)
  const [preview, setPreview] = useState<Preview | null>(null)
  const [captureStatus, setCaptureStatus] = useState<CaptureStatus>({ kind: 'idle' })
  const [circleStatus, setCircleStatus] = useState<CircleStatus>({ kind: 'idle' })
  const [clipStatus, setClipStatus] = useState<ClipStatus>({ kind: 'idle' })
  const [sequences, setSequences] = useState<SequenceDef[]>([])
  const [seqRun, setSeqRun] = useState<SeqRun | null>(null)
  const wsRef = useRef<WebSocket | null>(null)

  const send = useCallback((type: string, payload: unknown) => {
    wsRef.current?.send(JSON.stringify({ type, payload }))
  }, [])

  const connectRepr = useCallback(
    (host: string, port: string) => {
      send('connect', { host, port })
    },
    [send],
  )

  const disconnectRepr = useCallback(() => {
    send('disconnect', {})
  }, [send])

  // setAutoConnectRepr toggles the persistent auto-connect state: on, it
  // arms the retry cycle (starting a connect attempt at host/port if none is
  // already underway) and keeps it armed across a successful connection, so
  // a later unintentional disconnect resumes on its own; off, it only stops
  // a retry in progress -- Disconnect above is still the separate, explicit
  // action for dropping an active connection.
  const setAutoConnectRepr = useCallback(
    (enabled: boolean, host?: string, port?: string) => {
      send('set-auto-connect', { enabled, host, port })
    },
    [send],
  )

  useEffect(() => {
    // Derive the WebSocket URL from the path this document was served
    // under, so it also works when reverse-proxied beneath a prefix
    // (/robot/ via local-representative, /host/<id>/robot/ via
    // agent-coordinator).
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const dir = window.location.pathname.replace(/\/[^/]*\.[^/]*$/, '/')
    const base = dir.endsWith('/') ? dir.slice(0, -1) : dir
    const wsUrl = `${proto}://${window.location.host}${base}/ws`

    function connect() {
      const ws = new WebSocket(wsUrl)
      wsRef.current = ws

      ws.onopen = () => setConnected(true)

      ws.onclose = () => {
        setConnected(false)
        // A run in flight reports to the connection that started it, so its
        // result is lost with that connection.
        setSeqRun((run) => (run?.running ? { ...run, running: false, lostConnection: true } : run))
        setTimeout(connect, 2000)
      }

      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data) as { type: string; payload: unknown }
          if (msg.type === 'repr-status') {
            const p = msg.payload as ReprStatusMsg
            setReprStatus(p.status)
            if (p.host) setReprHost(p.host)
            if (p.port) setReprPort(p.port)
            setReprAutoConnect(!!p.auto_connect)
          } else if (msg.type === 'self-info') {
            const p = msg.payload as SelfInfoMsg
            setDevMode(p.dev_mode)
            setVersion(p.version)
            // A rebuild+restart is invisible to an already-open tab -- the
            // reconnect above is the only signal it gets. Compare the
            // server's own reported version against this bundle's
            // build-time version and reload if they differ; skip under the
            // Vite dev server, where HMR already keeps the tab current.
            if (!import.meta.env.DEV && p.version && p.version !== __APP_VERSION__) {
              window.location.reload()
            }
          } else if (msg.type === 'mode-mismatch') {
            const p = msg.payload as ModeMismatchMsg
            setModeMismatch(p.mismatched ? p : null)
          } else if (msg.type === 'capture-result') {
            const p = msg.payload as CaptureResultMsg
            if (p.success) {
              if (p.image_url) setPreview({ kind: 'image', url: p.image_url })
              setCaptureStatus({ kind: 'idle' })
            } else {
              setCaptureStatus({ kind: 'error', message: p.error ?? `${p.source} capture failed` })
            }
          } else if (msg.type === 'circle-mouse-result') {
            const p = msg.payload as CircleMouseResultMsg
            const via = p.via ? ` (via ${p.via})` : ''
            setCircleStatus(
              p.success
                ? { kind: 'success', message: via }
                : { kind: 'error', message: (p.error ?? 'circle-mouse failed') + via },
            )
          } else if (msg.type === 'clip-result') {
            const p = msg.payload as ClipResultMsg
            const via = p.via ? ` (via ${p.via})` : ''
            const clip = clipPreview(p)
            if (clip) {
              setPreview(clip)
              setClipStatus({ kind: 'success', message: via })
            } else {
              setClipStatus({ kind: 'error', message: p.error ?? 'native clip failed' })
            }
          } else if (msg.type === 'sequence-defs') {
            const p = msg.payload as SequenceDefsMsg
            setSequences(p.sequences ?? [])
          } else if (msg.type === 'sequence-progress') {
            const p = msg.payload as SequenceProgressMsg
            setSeqRun((run) => {
              if (!run || run.sequenceId !== p.sequence_id) return run
              const steps = run.steps.slice()
              steps[p.step] = { ...steps[p.step], status: p.status, message: p.message }
              return { ...run, steps }
            })
          } else if (msg.type === 'sequence-result') {
            const p = msg.payload as SequenceResultMsg
            setSeqRun({
              sequenceId: p.sequence_id,
              running: false,
              steps: (p.steps ?? []).map((s) => ({ status: s.status, message: s.message, durationMs: s.duration_ms })),
              result: p,
              recording: p.recording ? clipPreview(p.recording) : null,
            })
          }
        } catch {
          // ignore malformed messages
        }
      }
    }

    connect()
    return () => wsRef.current?.close()
  }, [])

  // Release a recorded clip's blob: URL once it's no longer being previewed.
  useEffect(() => {
    if (preview?.kind !== 'video') return
    const url = preview.url
    return () => URL.revokeObjectURL(url)
  }, [preview])

  // Likewise for the last sequence run's recording.
  const seqRecording = seqRun?.recording
  useEffect(() => {
    if (seqRecording?.kind !== 'video') return
    const url = seqRecording.url
    return () => URL.revokeObjectURL(url)
  }, [seqRecording])

  const captureNative = useCallback(() => {
    setCaptureStatus({ kind: 'pending' })
    send('capture-native', {})
  }, [send])

  // captureBrowser grabs a single frame of the current display via the
  // browser's own screen-capture API (same technique as
  // local-representative's/agent-coordinator's screenshot-upload widget)
  // and hands it to the backend over the "capture-browser" channel, so it
  // renders through the same capture-result path as a native capture.
  const captureBrowser = useCallback(async () => {
    setCaptureStatus({ kind: 'pending' })
    if (!browserCaptureSupported) {
      // Fail fast with a message naming the real cause (insecure connection
      // or no getDisplayMedia support) instead of a generic TypeError.
      setCaptureStatus({
        kind: 'error',
        message:
          `browser capture is not available: ${browserCaptureUnsupportedReason}. Try Capture Native from a machine with ` +
          "direct display access to the robot's host instead.",
      })
      return
    }
    try {
      const stream = await navigator.mediaDevices.getDisplayMedia({ video: true })
      try {
        const track = stream.getVideoTracks()[0]
        const video = document.createElement('video')
        video.srcObject = stream
        await video.play()
        await new Promise((resolve) => requestAnimationFrame(resolve))
        const canvas = document.createElement('canvas')
        canvas.width = video.videoWidth
        canvas.height = video.videoHeight
        canvas.getContext('2d')!.drawImage(video, 0, 0)
        track.stop()
        const dataURL = canvas.toDataURL('image/png')
        send('capture-browser', { data: dataURL })
      } finally {
        stream.getTracks().forEach((t) => t.stop())
      }
    } catch (err) {
      // Past the support check above, a rejection here is a genuine
      // permissions/user-action outcome (e.g. NotAllowedError from
      // dismissing the picker), not a capability gap -- labeled
      // separately so the two causes aren't conflated in the UI.
      setCaptureStatus({ kind: 'error', message: `browser capture failed: ${String(err)}` })
    }
  }, [send])

  const circleMouse = useCallback(() => {
    setCircleStatus({ kind: 'running' })
    send('circle-mouse', {})
  }, [send])

  const clipNative = useCallback(() => {
    setClipStatus({ kind: 'recording' })
    send('clip-native', {})
  }, [send])

  const runSequence = useCallback(
    (seq: SequenceDef) => {
      setSeqRun({
        sequenceId: seq.id,
        running: true,
        steps: seq.steps.map(() => ({ status: 'pending' as const })),
      })
      send('run-sequence', { id: seq.id })
    },
    [send],
  )

  return {
    connected,
    reprStatus,
    reprHost,
    reprPort,
    reprAutoConnect,
    devMode,
    version,
    modeMismatch,
    connectRepr,
    disconnectRepr,
    setAutoConnectRepr,
    preview,
    captureStatus,
    circleStatus,
    clipStatus,
    captureNative,
    captureBrowser,
    circleMouse,
    clipNative,
    sequences,
    seqRun,
    runSequence,
  }
}

// ---- representable connect/disconnect widget ----

const REPR_STATUS_LABELS: Record<ReprStatus, string> = {
  disconnected: 'not connected to local-representative',
  connecting: 'connecting to local-representative…',
  connected: 'connected to local-representative',
}

interface ReprFooterProps {
  status: ReprStatus
  host: string
  port: string
  autoConnect: boolean
  onConnect: (host: string, port: string) => void
  onDisconnect: () => void
  onSetAutoConnect: (enabled: boolean, host?: string, port?: string) => void
}

function ReprFooter({ status, host, port, autoConnect, onConnect, onDisconnect, onSetAutoConnect }: ReprFooterProps) {
  const [hostInput, setHostInput] = useState(host)
  const [portInput, setPortInput] = useState(port)

  // Track the address the server reports (its --lr-host/--lr-port defaults,
  // or whatever it's currently connected/connecting to) until the user edits
  // the fields themselves.
  useEffect(() => setHostInput(host), [host])
  useEffect(() => setPortInput(port), [port])

  const autoConnectToggle = (
    <label
      className="repr-footer-auto-connect"
      title="keep reaching for local-representative: stays armed across a successful connection so an unintentional disconnect resumes the cycle on its own -- an explicit disconnect turns it off"
    >
      <input
        type="checkbox"
        checked={autoConnect}
        onChange={(e) => onSetAutoConnect(e.target.checked, hostInput.trim(), portInput.trim())}
      />
      auto-connect
    </label>
  )

  return (
    <div className="repr-footer">
      <div className="repr-footer-status">
        <span className={`conn-dot ${status === 'connected' ? 'connected' : status === 'connecting' ? 'connecting' : 'disconnected'}`} />
        <span className="repr-footer-label">{REPR_STATUS_LABELS[status]}</span>
      </div>
      {status === 'disconnected' ? (
        <div className="repr-footer-form">
          <input
            className="repr-footer-input"
            type="text"
            placeholder="host"
            value={hostInput}
            onChange={(e) => setHostInput(e.target.value)}
          />
          <input
            className="repr-footer-input repr-footer-input-port"
            type="text"
            placeholder="port"
            value={portInput}
            onChange={(e) => setPortInput(e.target.value)}
          />
          <button className="btn-secondary" onClick={() => onConnect(hostInput.trim(), portInput.trim())}>
            Connect
          </button>
          {autoConnectToggle}
        </div>
      ) : (
        <div className="repr-footer-form">
          <span className="repr-footer-addr">{host}:{port}</span>
          {autoConnectToggle}
          <button className="btn-secondary" onClick={onDisconnect}>
            Disconnect
          </button>
        </div>
      )}
    </div>
  )
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

// PreviewView renders a Preview, or placeholder text when there is none.
function PreviewView({ preview, placeholder }: { preview: Preview | null | undefined; placeholder: string }) {
  return (
    <div className="robot-preview">
      {preview?.kind === 'image' && <img src={preview.url} alt="capture" />}
      {preview?.kind === 'video' && <video src={preview.url} controls autoPlay loop muted playsInline />}
      {preview?.kind === 'frames' && <FramePlayer frames={preview.frames} durationMs={preview.durationMs} />}
      {!preview && <span className="robot-preview-placeholder">{placeholder}</span>}
    </div>
  )
}

// ---- Simple tab: capture native / capture browser / circle mouse / native clip ----

interface RobotPanelProps {
  connected: boolean
  preview: Preview | null
  captureStatus: CaptureStatus
  circleStatus: CircleStatus
  clipStatus: ClipStatus
  onCaptureNative: () => void
  onCaptureBrowser: () => void
  onCircleMouse: () => void
  onClipNative: () => void
}

function RobotPanel({
  connected,
  preview,
  captureStatus,
  circleStatus,
  clipStatus,
  onCaptureNative,
  onCaptureBrowser,
  onCircleMouse,
  onClipNative,
}: RobotPanelProps) {
  return (
    <div className="robot-panel">
      <div className="robot-actions">
        <button className="btn-secondary" disabled={!connected || captureStatus.kind === 'pending'} onClick={onCaptureNative}>
          Capture Native
        </button>
        <button
          className="btn-secondary"
          disabled={!connected || captureStatus.kind === 'pending' || !browserCaptureSupported}
          title={browserCaptureSupported ? undefined : `Not supported: ${browserCaptureUnsupportedReason}`}
          onClick={onCaptureBrowser}
        >
          Capture Browser
        </button>
        <button className="btn-secondary" disabled={!connected || circleStatus.kind === 'running'} onClick={onCircleMouse}>
          Circle Mouse
        </button>
        <button className="btn-secondary" disabled={!connected || clipStatus.kind === 'recording'} onClick={onClipNative}>
          Native Clip
        </button>
      </div>
      {captureStatus.kind === 'pending' && <div className="robot-status">Capturing…</div>}
      {captureStatus.kind === 'error' && <div className="robot-status robot-status-error">{captureStatus.message}</div>}
      {circleStatus.kind === 'running' && <div className="robot-status">Driving the pointer through a circle…</div>}
      {circleStatus.kind === 'success' && <div className="robot-status">Circle complete{circleStatus.message}.</div>}
      {circleStatus.kind === 'error' && <div className="robot-status robot-status-error">{circleStatus.message}</div>}
      {clipStatus.kind === 'recording' && <div className="robot-status">Recording a 4 second clip of the desktop…</div>}
      {clipStatus.kind === 'success' && <div className="robot-status">Clip recorded{clipStatus.message}.</div>}
      {clipStatus.kind === 'error' && <div className="robot-status robot-status-error">{clipStatus.message}</div>}
      <PreviewView
        preview={preview}
        placeholder={'Press "Capture Native", "Capture Browser" or "Native Clip" — the result appears here.'}
      />
    </div>
  )
}

// ---- sequence-v1 tab: run a sequence of high-level actions, recorded ----

const STEP_ICONS: Record<StepStatus, string> = {
  pending: '○',
  running: '◌',
  success: '✓',
  error: '✗',
  skipped: '–',
}

interface SequencePanelProps {
  connected: boolean
  sequences: SequenceDef[]
  run: SeqRun | null
  onRun: (seq: SequenceDef) => void
}

function SequencePanel({ connected, sequences, run, onRun }: SequencePanelProps) {
  const [selectedId, setSelectedId] = useState('')
  const seq = sequences.find((s) => s.id === selectedId) ?? sequences[0]

  if (!seq) {
    return (
      <div className="robot-panel">
        <div className="empty-state">{connected ? 'No sequences defined.' : 'Connecting…'}</div>
      </div>
    )
  }

  const thisRun = run?.sequenceId === seq.id ? run : null
  const running = !!run?.running
  const result = thisRun?.result
  const rec = result?.recording

  return (
    <div className="robot-panel">
      <div className="seq-header">
        {sequences.length > 1 ? (
          <select className="seq-select" value={seq.id} disabled={running} onChange={(e) => setSelectedId(e.target.value)}>
            {sequences.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </select>
        ) : (
          <span className="seq-name">{seq.name}</span>
        )}
        <button className="btn-secondary seq-run" disabled={!connected || running} onClick={() => onRun(seq)}>
          {running ? 'Running…' : 'Run sequence'}
        </button>
      </div>
      <ol className="seq-steps">
        {seq.steps.map((st, i) => {
          const s = thisRun?.steps[i]
          const status: StepStatus = s?.status ?? 'pending'
          return (
            <li key={i} className={`seq-step seq-step-${status}`}>
              <span className="seq-step-icon">{STEP_ICONS[status]}</span>
              <div className="seq-step-body">
                <div className="seq-step-label">
                  {i + 1}. {st.label}
                </div>
                <ul className="seq-step-detail">
                  {st.detail.map((d, j) => (
                    <li key={j}>{d}</li>
                  ))}
                </ul>
                {s?.message && <div className="seq-step-message">{s.message}</div>}
              </div>
              {s?.durationMs !== undefined && status !== 'skipped' && (
                <span className="seq-step-time">{(s.durationMs / 1000).toFixed(1)}s</span>
              )}
            </li>
          )
        })}
      </ol>
      {running && <div className="robot-status">Running and recording the sequence…</div>}
      {thisRun?.lostConnection && (
        <div className="robot-status robot-status-error">
          Lost the connection to IANAR mid-run, so this run's result and recording didn't arrive.
        </div>
      )}
      {result && (
        <div className={`seq-result ${result.success ? 'seq-result-success' : 'seq-result-error'}`}>
          {result.success
            ? `✓ Sequence succeeded in ${(result.duration_ms / 1000).toFixed(1)}s`
            : `✗ Sequence failed: ${result.error ?? 'unknown error'}`}
          {result.keyboard_via && <span className="seq-result-via"> · keyboard via {result.keyboard_via}</span>}
        </div>
      )}
      {rec && (
        <div className={`robot-status${rec.success ? '' : ' robot-status-error'}`}>
          {rec.success ? `Recorded via ${rec.via}.` : `Recording failed: ${rec.error ?? 'unknown error'}`}
        </div>
      )}
      <PreviewView preview={thisRun?.recording} placeholder="Run the sequence — its recording appears here." />
    </div>
  )
}

type Tab = 'simple' | 'sequence-v1'
const TABS: Tab[] = ['simple', 'sequence-v1']

export default function App() {
  const {
    connected,
    reprStatus,
    reprHost,
    reprPort,
    reprAutoConnect,
    devMode,
    version,
    modeMismatch,
    connectRepr,
    disconnectRepr,
    setAutoConnectRepr,
    preview,
    captureStatus,
    circleStatus,
    clipStatus,
    captureNative,
    captureBrowser,
    circleMouse,
    clipNative,
    sequences,
    seqRun,
    runSequence,
  } = useRobotWS()
  const [tab, setTab] = useState<Tab>('simple')

  return (
    <div className={`app${devMode ? ' app-dev-mode' : ''}`}>
      {modeMismatch && (
        <div className="mode-mismatch-banner">
          ⚠ dev/ops mode mismatch with local-representative ({modeMismatch.peer_mode}):
          only health information is exchanged until this is resolved. See docs/DevMode.md.
        </div>
      )}
      <div className="sidebar-wrap">
        <div className="sidebar">
          <div className="sidebar-header">
            <h1>I am Not a Robot</h1>
            {version && <span className="app-version-tag">v{version.replace(/^v/, '')}</span>}
          </div>
          <ReprFooter
            status={reprStatus}
            host={reprHost}
            port={reprPort}
            autoConnect={reprAutoConnect}
            onConnect={connectRepr}
            onDisconnect={disconnectRepr}
            onSetAutoConnect={setAutoConnectRepr}
          />
        </div>
      </div>
      <div className="main-content">
        <div className="tab-bar">
          {TABS.map((t) => (
            <button key={t} className={`tab${tab === t ? ' tab-active' : ''}`} onClick={() => setTab(t)}>
              {t}
            </button>
          ))}
        </div>
        {tab === 'simple' && (
          <RobotPanel
            connected={connected}
            preview={preview}
            captureStatus={captureStatus}
            circleStatus={circleStatus}
            clipStatus={clipStatus}
            onCaptureNative={captureNative}
            onCaptureBrowser={captureBrowser}
            onCircleMouse={circleMouse}
            onClipNative={clipNative}
          />
        )}
        {tab === 'sequence-v1' && (
          <SequencePanel connected={connected} sequences={sequences} run={seqRun} onRun={runSequence} />
        )}
      </div>
    </div>
  )
}
