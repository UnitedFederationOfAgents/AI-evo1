import { useCallback, useEffect, useRef, useState } from 'react'
import type {
  ModeMismatchMsg,
  NotepadMsg,
  ReprStatus,
  ReprStatusMsg,
  SaveResultMsg,
  SelfInfoMsg,
  TranscribeStatusMsg,
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
  const [transcribeStatus, setTranscribeStatus] = useState<TranscribeStatusMsg>({
    state: 'checking',
    engine: 'aws-transcribe',
  })
  const [notepad, setNotepadText] = useState('')
  // notepadRevRef is the newest server revision this tab has applied, so a
  // stale "notepad" message (e.g. the connect-time snapshot racing a newer
  // broadcast) can't roll the text back; notepadSeqRef numbers this tab's
  // own edits so the server can drop any that arrive out of order (see
  // notepad.go).
  const notepadRevRef = useRef(0)
  const notepadSeqRef = useRef(0)
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

      ws.onopen = () => {
        // A reconnect may be to a restarted server whose notepad revision
        // counter started over -- accept whatever it sends next.
        notepadRevRef.current = 0
        setConnected(true)
      }

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
          } else if (msg.type === 'notepad') {
            const p = msg.payload as NotepadMsg
            if (p.rev >= notepadRevRef.current) {
              notepadRevRef.current = p.rev
              setNotepadText(p.text)
            }
          } else if (msg.type === 'transcribe-status') {
            setTranscribeStatus(msg.payload as TranscribeStatusMsg)
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

  const recheckTranscribe = useCallback(() => {
    send('recheck-transcribe', {})
  }, [send])

  // setNotepad applies a local edit immediately and pushes it to the server,
  // which relays it to every other connected client.
  const setNotepad = useCallback(
    (text: string) => {
      setNotepadText(text)
      notepadSeqRef.current += 1
      send('set-notepad', { text, seq: notepadSeqRef.current })
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
    recording,
    transcript,
    partialTranscript,
    saveStatus,
    startRecording,
    stopRecording,
    saveTranscript,
    transcribeStatus,
    recheckTranscribe,
    notepad,
    setNotepad,
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
  transcribeStatus: TranscribeStatusMsg
  onStart: () => void
  onStop: () => void
  onSave: () => void
  onRecheck: () => void
}

const TRANSCRIBE_ENGINE_LABELS: Record<string, string> = {
  'aws-transcribe': 'AWS Transcribe',
}

// TranscribeAvailability shows whether the server can currently reach its
// transcription engine (see transcribe_status.go): a quiet one-liner while
// available or checking, and a prominent "unavailable" notice with the
// server's reason and a Recheck button otherwise.
function TranscribeAvailability({ status, connected, onRecheck }: {
  status: TranscribeStatusMsg
  connected: boolean
  onRecheck: () => void
}) {
  const engine = TRANSCRIBE_ENGINE_LABELS[status.engine] ?? status.engine
  if (status.state === 'unavailable') {
    return (
      <div className="transcribe-availability transcribe-availability-unavailable">
        <div>
          <strong>Transcription unavailable</strong> — {engine} can't be reached
          {status.reason && <div className="transcribe-availability-reason">{status.reason}</div>}
        </div>
        <button className="btn-secondary" disabled={!connected} onClick={onRecheck}>
          Recheck
        </button>
      </div>
    )
  }
  return (
    <div className={`transcribe-availability transcribe-availability-${status.state}`}>
      <span className={`conn-dot ${status.state === 'available' ? 'connected' : 'connecting'}`} />
      {status.state === 'available'
        ? `${engine} available${status.region ? ` (${status.region})` : ''}`
        : `checking ${engine} access…`}
    </div>
  )
}

function TranscriptionPanel({
  connected,
  recording,
  transcript,
  partialTranscript,
  saveStatus,
  transcribeStatus,
  onStart,
  onStop,
  onSave,
  onRecheck,
}: TranscriptionPanelProps) {
  const hasTranscript = Boolean(transcript.trim() || partialTranscript.trim())
  const unavailable = transcribeStatus.state === 'unavailable'

  return (
    <div className="transcribe-panel">
      <TranscribeAvailability status={transcribeStatus} connected={connected} onRecheck={onRecheck} />
      <div className="transcribe-controls">
        <button
          className="btn-secondary"
          disabled={!connected || recording || unavailable}
          title={unavailable ? 'transcription is unavailable -- see the notice above' : undefined}
          onClick={onStart}
        >
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
        ) : unavailable ? (
          <span className="transcribe-placeholder">Transcription is unavailable on this instance.</span>
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

// ---- Notepad panel ----
//
// A quick-notes area shared by every client connected to this instance
// (condocs/initialRobotImpls/Step5Prompt.md): each keystroke goes straight to
// the server and on to every other open tab, last writer wins. Read-only
// while the WebSocket is down so edits can't silently go nowhere.

interface NotepadPanelProps {
  connected: boolean
  text: string
  onChange: (text: string) => void
}

function NotepadPanel({ connected, text, onChange }: NotepadPanelProps) {
  const inputRef = useRef<HTMLTextAreaElement>(null)

  // speakText reads the textarea's current selection if there is one, else
  // the whole notepad -- read at click time rather than from `text` so a
  // selection made just before clicking is what gets spoken.
  const speakText = useCallback(() => {
    const el = inputRef.current
    if (el && el.selectionStart !== el.selectionEnd) {
      return el.value.slice(el.selectionStart, el.selectionEnd)
    }
    return el?.value ?? text
  }, [text])

  return (
    <div className="notepad-panel">
      <div className="notepad-header">
        <span className="notepad-title">Notepad</span>
        <span className="notepad-hint">{connected ? 'shared with all clients' : 'disconnected — read-only'}</span>
      </div>
      <div className="notepad-body">
        <textarea
          ref={inputRef}
          className="notepad-input"
          value={text}
          readOnly={!connected}
          placeholder="Quick notes — visible to everyone connected."
          spellCheck={false}
          onChange={(e) => onChange(e.target.value)}
        />
        <SpeakButton getText={speakText} disabled={!text.trim()} />
      </div>
    </div>
  )
}

// ---- Speech output (Step5Prompt.md Revision C) ----
//
// The first step of docs/ConversationalistSpeechOutput.md's recommendation:
// the browser's own speechSynthesis, spoken locally in the tab that clicked
// -- nothing goes through the server or to other clients, unlike the notepad
// text itself.

// SPEAK_ICON follows the grey-palette wireframe convention of
// local-representative's/agent-coordinator's MIC_ICON and CAMERA_ICON
// (Step1SubstepCPrompt.md): a speech balloon with a sound wave in it.
const SPEAK_ICON = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
    <path d="M4 4h16a1 1 0 0 1 1 1v11a1 1 0 0 1-1 1h-9l-4 3.5V17H4a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1z" />
    <path d="M7 9.5v2M9.5 8v5M12 6.5v8M14.5 8.5v4M17 9.5v2" />
  </svg>
)

const speechSupported = typeof window !== 'undefined' && 'speechSynthesis' in window

// SpeakButton toggles speaking getText()'s result: click to speak, click
// again to stop. Renders nothing in a browser without speechSynthesis.
function SpeakButton({ getText, disabled }: { getText: () => string; disabled: boolean }) {
  const [speaking, setSpeaking] = useState(false)

  // Don't keep talking after the panel goes away.
  useEffect(() => () => { if (speechSupported) window.speechSynthesis.cancel() }, [])

  const toggle = useCallback(() => {
    const synth = window.speechSynthesis
    if (speaking) {
      synth.cancel()
      setSpeaking(false)
      return
    }
    const text = getText().trim()
    if (!text) return
    // Clear anything already queued so this utterance starts immediately.
    synth.cancel()
    const utterance = new SpeechSynthesisUtterance(text)
    utterance.onend = () => setSpeaking(false)
    utterance.onerror = () => setSpeaking(false)
    synth.speak(utterance)
    setSpeaking(true)
  }, [speaking, getText])

  if (!speechSupported) return null
  return (
    <button
      type="button"
      className={`speak-btn${speaking ? ' speak-btn-active' : ''}`}
      disabled={disabled && !speaking}
      title={speaking ? 'stop speaking' : 'speak the notepad (or its selected text) aloud'}
      aria-pressed={speaking}
      onClick={toggle}
    >
      {SPEAK_ICON}
    </button>
  )
}

// ---- Capture mode (Step2Prompt.md) ----
//
// condoccer embeds this app in an iframe with "?embed=capture" (see
// condoccer/frontend/src/App.tsx's tcCaptureURL/TCCaptureOverlay) to dictate
// straight into one of its own text fields, rather than the full
// TranscriptionPanel/Save-to-file flow above. This is the only thing about
// The Conversationalist condoccer ever sees: recording starts itself the
// instant the WebSocket connects, and the one "Insert Transcript" action
// posts the accumulated text back to the parent window instead of saving it
// anywhere (Step2Prompt.md: "we only want the text to get into other text
// boxes"). The embedding parent is trusted with '*' as postMessage's target
// origin -- same-origin already for the two proxied cases (see
// tcCaptureURL), and the standalone case's parent is whatever page the
// operator chose to iframe this into.

function isCaptureMode(): boolean {
  return new URLSearchParams(window.location.search).get('embed') === 'capture'
}

function CaptureApp() {
  const {
    connected,
    recording,
    transcript,
    partialTranscript,
    saveStatus,
    transcribeStatus,
    startRecording,
    stopRecording,
    recheckTranscribe,
  } = useConversationalistWS()
  const startedRef = useRef(false)
  const unavailable = transcribeStatus.state === 'unavailable'

  // Auto-start once the server has confirmed its transcription engine is
  // reachable, rather than opening the mic only to have Start fail.
  useEffect(() => {
    if (connected && transcribeStatus.state === 'available' && !startedRef.current) {
      startedRef.current = true
      startRecording()
    }
  }, [connected, transcribeStatus.state, startRecording])

  const insertTranscript = useCallback(() => {
    stopRecording()
    const text = `${transcript} ${partialTranscript}`.trim()
    window.parent.postMessage({ type: 'tc-transcript', text }, '*')
  }, [stopRecording, transcript, partialTranscript])

  const cancel = useCallback(() => {
    stopRecording()
    window.parent.postMessage({ type: 'tc-transcript-cancel' }, '*')
  }, [stopRecording])

  const hasTranscript = Boolean(transcript.trim() || partialTranscript.trim())

  return (
    <div className="tc-capture-app">
      <div className="tc-capture-status">
        {recording ? (
          <>
            <span className="conn-dot connecting transcribe-recording-dot" title="recording" />
            Listening…
          </>
        ) : saveStatus.kind === 'error' ? (
          <span className="tc-capture-error">{saveStatus.message}</span>
        ) : unavailable ? (
          <span className="tc-capture-error">
            Transcription unavailable{transcribeStatus.reason ? `: ${transcribeStatus.reason}` : ''}
          </span>
        ) : (
          <span>Connecting…</span>
        )}
      </div>
      <div className="tc-capture-output">
        {hasTranscript ? (
          <>
            {transcript}
            {partialTranscript && <span className="transcribe-partial"> {partialTranscript}</span>}
          </>
        ) : (
          <span className="transcribe-placeholder">Speak now…</span>
        )}
      </div>
      <div className="tc-capture-actions">
        <button className="btn-primary" disabled={!recording && !hasTranscript} onClick={insertTranscript}>
          Insert Transcript
        </button>
        <button className="btn-secondary" onClick={cancel}>Cancel</button>
        {unavailable ? (
          <button className="btn-secondary" disabled={!connected} onClick={recheckTranscribe}>Recheck</button>
        ) : saveStatus.kind === 'error' && (
          <button className="btn-secondary" onClick={startRecording}>Retry</button>
        )}
      </div>
    </div>
  )
}

export default function App() {
  if (isCaptureMode()) return <CaptureApp />

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
    transcribeStatus,
    recheckTranscribe,
    notepad,
    setNotepad,
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
          transcribeStatus={transcribeStatus}
          onStart={startRecording}
          onStop={stopRecording}
          onSave={saveTranscript}
          onRecheck={recheckTranscribe}
        />
        <NotepadPanel connected={connected} text={notepad} onChange={setNotepad} />
      </div>
    </div>
  )
}
