import { useCallback, useEffect, useRef, useState } from 'react'
import type { CaptureResultMsg, CircleMouseResultMsg, ModeMismatchMsg, ReprStatus, ReprStatusMsg, SelfInfoMsg } from './types'

// ---- WebSocket hook ----
//
// Wires up the representable connect/disconnect widget, dev-mode banner, and
// version-mismatch reload check shared by every sub-app in this repo, plus
// IANAR's own capture-native/capture-browser/circle-mouse channels (see
// condocs/InitialRobot.md and robot.go).

type CaptureStatus = { kind: 'idle' | 'pending' | 'error'; message?: string }
type CircleStatus = { kind: 'idle' | 'running' | 'success' | 'error'; message?: string }

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
  const [captureImage, setCaptureImage] = useState<string | null>(null)
  const [captureStatus, setCaptureStatus] = useState<CaptureStatus>({ kind: 'idle' })
  const [circleStatus, setCircleStatus] = useState<CircleStatus>({ kind: 'idle' })
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
              setCaptureImage(p.image_url ?? null)
              setCaptureStatus({ kind: 'idle' })
            } else {
              setCaptureStatus({ kind: 'error', message: p.error ?? `${p.source} capture failed` })
            }
          } else if (msg.type === 'circle-mouse-result') {
            const p = msg.payload as CircleMouseResultMsg
            setCircleStatus(p.success ? { kind: 'success' } : { kind: 'error', message: p.error ?? 'circle-mouse failed' })
          }
        } catch {
          // ignore malformed messages
        }
      }
    }

    connect()
    return () => wsRef.current?.close()
  }, [])

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
    captureImage,
    captureStatus,
    circleStatus,
    captureNative,
    captureBrowser,
    circleMouse,
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

// ---- Robot panel: capture native / capture browser / circle mouse ----

interface RobotPanelProps {
  connected: boolean
  captureImage: string | null
  captureStatus: CaptureStatus
  circleStatus: CircleStatus
  onCaptureNative: () => void
  onCaptureBrowser: () => void
  onCircleMouse: () => void
}

function RobotPanel({
  connected,
  captureImage,
  captureStatus,
  circleStatus,
  onCaptureNative,
  onCaptureBrowser,
  onCircleMouse,
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
      </div>
      {captureStatus.kind === 'pending' && <div className="robot-status">Capturing…</div>}
      {captureStatus.kind === 'error' && <div className="robot-status robot-status-error">{captureStatus.message}</div>}
      {circleStatus.kind === 'running' && <div className="robot-status">Driving the pointer through a circle…</div>}
      {circleStatus.kind === 'success' && <div className="robot-status">Circle complete.</div>}
      {circleStatus.kind === 'error' && <div className="robot-status robot-status-error">{circleStatus.message}</div>}
      <div className="robot-preview">
        {captureImage ? (
          <img src={captureImage} alt="capture" />
        ) : (
          <span className="robot-preview-placeholder">
            Press "Capture Native" or "Capture Browser" — the result appears here.
          </span>
        )}
      </div>
    </div>
  )
}

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
    captureImage,
    captureStatus,
    circleStatus,
    captureNative,
    captureBrowser,
    circleMouse,
  } = useRobotWS()

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
            {version && <span className="app-version-tag">v{version}</span>}
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
        <RobotPanel
          connected={connected}
          captureImage={captureImage}
          captureStatus={captureStatus}
          circleStatus={circleStatus}
          onCaptureNative={captureNative}
          onCaptureBrowser={captureBrowser}
          onCircleMouse={circleMouse}
        />
      </div>
    </div>
  )
}
