import { useCallback, useEffect, useRef, useState } from 'react'
import type {
  ModeMismatchMsg,
  ReprStatus,
  ReprStatusMsg,
  SaveResultMsg,
  SelfInfoMsg,
  TranscriptMsg,
} from './types'

// ---- WebSocket hook ----
//
// This is a minimal application shell (see
// condocs/InitialShellsSessionManagerAndTheConversationalist.md) -- it wires
// up the representable connect/disconnect widget, dev-mode banner, and
// version-mismatch reload check shared by every sub-app in this repo, plus
// (Revision D) The Conversationalist's first bit of domain functionality:
// live speech transcription via AWS Transcribe, replicating
// ignored-scratch/AI-sandboxing/agent-scribe's "start transcription"/"save
// to file" buttons with this app's own Go backend and typed WebSocket
// protocol instead of agent-scribe's Node/socket.io server.

function useConversationalistWS() {
  const [connected, setConnected] = useState(false)
  const [reprStatus, setReprStatus] = useState<ReprStatus>('disconnected')
  const [reprHost, setReprHost] = useState('')
  const [reprPort, setReprPort] = useState('')
  const [reprAutoConnect, setReprAutoConnect] = useState(false)
  const [devMode, setDevMode] = useState(false)
  const [version, setVersion] = useState('')
  const [modeMismatch, setModeMismatch] = useState<ModeMismatchMsg | null>(null)
  const [recording, setRecording] = useState(false)
  const [transcript, setTranscript] = useState('')
  const [partialTranscript, setPartialTranscript] = useState('')
  const [saveStatus, setSaveStatus] = useState<SaveStatus>({ kind: 'idle' })
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
    // (/convo/ via local-representative, /host/<id>/convo/ via
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
          } else if (msg.type === 'transcript') {
            const p = msg.payload as TranscriptMsg
            if (p.is_final) {
              setTranscript((t) => (t ? `${t} ${p.text}` : p.text))
              setPartialTranscript('')
            } else {
              setPartialTranscript(p.text)
            }
          } else if (msg.type === 'save-result') {
            const p = msg.payload as SaveResultMsg
            if (p.success) {
              setSaveStatus({ kind: 'success', message: `saved as ${p.name ?? p.file_id}` })
              setTranscript('')
              setPartialTranscript('')
            } else {
              setSaveStatus({ kind: 'error', message: p.error ?? 'save failed' })
            }
          } else if (msg.type === 'error') {
            setSaveStatus({ kind: 'error', message: String(msg.payload) })
          }
        } catch {
          // ignore malformed messages
        }
      }
    }

    connect()
    return () => wsRef.current?.close()
  }, [])

  // Microphone capture: ScriptProcessorNode -> Int16 PCM -> base64'd
  // "audio-chunk" messages, the same capture technique agent-scribe's
  // index.html uses, just handed to our own WebSocket send() instead of a
  // socket.io emit.
  const captureRef = useRef<{ ctx: AudioContext; stream: MediaStream; processor: ScriptProcessorNode } | null>(null)

  const startRecording = useCallback(async () => {
    if (captureRef.current) return
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true })
      const ctx = new AudioContext()
      const source = ctx.createMediaStreamSource(stream)
      const processor = ctx.createScriptProcessor(1024, 1, 1)
      source.connect(processor)
      processor.connect(ctx.destination)

      processor.onaudioprocess = (e) => {
        const float32 = e.inputBuffer.getChannelData(0)
        const int16 = new Int16Array(float32.length)
        for (let i = 0; i < float32.length; i++) {
          int16[i] = Math.max(-32768, Math.min(32767, Math.floor(float32[i] * 32768)))
        }
        send('audio-chunk', { data: int16ToBase64(int16) })
      }

      captureRef.current = { ctx, stream, processor }
      send('start-transcription', {})
      setSaveStatus({ kind: 'idle' })
      setRecording(true)
    } catch (err) {
      setSaveStatus({ kind: 'error', message: `microphone access failed: ${String(err)}` })
    }
  }, [send])

  const stopRecording = useCallback(() => {
    const capture = captureRef.current
    captureRef.current = null
    capture?.processor.disconnect()
    capture?.stream.getTracks().forEach((t) => t.stop())
    capture?.ctx.close()
    send('stop-transcription', {})
    setRecording(false)
  }, [send])

  // Stop any in-progress capture if the component unmounts mid-recording.
  useEffect(() => () => { captureRef.current?.processor.disconnect() }, [])

  const saveTranscript = useCallback(() => {
    setSaveStatus({ kind: 'pending' })
    send('save-transcript', {})
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
    recording,
    transcript,
    partialTranscript,
    saveStatus,
    startRecording,
    stopRecording,
    saveTranscript,
  }
}

// int16ToBase64 encodes a 16-bit PCM sample buffer as base64, chunked to
// avoid blowing String.fromCharCode's argument-count limit on large buffers.
function int16ToBase64(samples: Int16Array): string {
  const bytes = new Uint8Array(samples.buffer)
  let binary = ''
  const chunkSize = 0x8000
  for (let i = 0; i < bytes.length; i += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunkSize))
  }
  return btoa(binary)
}

type SaveStatus = { kind: 'idle' | 'pending' | 'success' | 'error'; message?: string }

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

// ---- Transcription panel ----
//
// Replicates agent-scribe's "Start Transcription"/"Save to File" buttons
// (ignored-scratch/AI-sandboxing/agent-scribe/index.html): press Start,
// speak, press Stop, then Save writes the accumulated transcript into
// local-representative's files area exactly as an upload would (see
// transcribe.go's saveTranscript) instead of agent-scribe's HEURISTIC.md
// intake.

interface TranscriptionPanelProps {
  connected: boolean
  recording: boolean
  transcript: string
  partialTranscript: string
  saveStatus: SaveStatus
  onStart: () => void
  onStop: () => void
  onSave: () => void
}

function TranscriptionPanel({
  connected,
  recording,
  transcript,
  partialTranscript,
  saveStatus,
  onStart,
  onStop,
  onSave,
}: TranscriptionPanelProps) {
  const hasTranscript = Boolean(transcript.trim() || partialTranscript.trim())

  return (
    <div className="transcribe-panel">
      <div className="transcribe-controls">
        <button className="btn-secondary" disabled={!connected || recording} onClick={onStart}>
          Start Transcription
        </button>
        <button className="btn-secondary" disabled={!recording} onClick={onStop}>
          Stop Transcription
        </button>
        <button className="btn-secondary" disabled={!connected || !hasTranscript} onClick={onSave}>
          Save to File
        </button>
        {recording && <span className="conn-dot connecting transcribe-recording-dot" title="recording" />}
      </div>
      <div className="transcribe-output">
        {hasTranscript ? (
          <>
            {transcript}
            {partialTranscript && <span className="transcribe-partial"> {partialTranscript}</span>}
          </>
        ) : (
          <span className="transcribe-placeholder">Press "Start Transcription" and speak — the transcript appears here.</span>
        )}
      </div>
      {saveStatus.kind !== 'idle' && (
        <div className={`transcribe-save-status transcribe-save-${saveStatus.kind}`}>
          {saveStatus.kind === 'pending' ? 'Saving…' : saveStatus.message}
        </div>
      )}
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
    recording,
    transcript,
    partialTranscript,
    saveStatus,
    startRecording,
    stopRecording,
    saveTranscript,
  } = useConversationalistWS()

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
            <h1>The Conversationalist</h1>
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
        <TranscriptionPanel
          connected={connected}
          recording={recording}
          transcript={transcript}
          partialTranscript={partialTranscript}
          saveStatus={saveStatus}
          onStart={startRecording}
          onStop={stopRecording}
          onSave={saveTranscript}
        />
      </div>
    </div>
  )
}
