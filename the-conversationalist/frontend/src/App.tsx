import { useCallback, useEffect, useRef, useState } from 'react'
import type {
  ModeMismatchMsg,
  NotepadMsg,
  NotesResultMsg,
  NotesSource,
  PollyAudioMsg,
  PollyClipView,
  PollyConfig,
  PollyPlayMsg,
  PollyStateMsg,
  ReprStatus,
  ReprStatusMsg,
  SaveResultMsg,
  SelfInfoMsg,
  TranscribeStatusMsg,
  TranscriptMsg,
} from './types'
import { noteTCStart } from './tcannounce'

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
  // announceStart is the server's "announce start" toggle (see
  // ../../announce.go): off by default.
  const [announceStart, setAnnounceStartState] = useState(false)
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
  // notes holds each panel's "To Notes" state (see notes.go);
  // notesInFlightRef remembers the text each pending request sent, so a
  // successful "notes-result" can record it as that panel's sent text.
  const [notes, setNotes] = useState<Record<NotesSource, NotesStatus>>(INITIAL_NOTES)
  const notesInFlightRef = useRef<Partial<Record<NotesSource, string>>>({})
  // pollyResult is the latest "polly-audio" answer; PollyButton plays it if
  // its id is the request it's waiting for (see polly.go).
  const [pollyResult, setPollyResult] = useState<PollyAudioMsg | null>(null)
  const pollySeqRef = useRef(0)
  // pollyState is the Polly configurations and recent clips (see
  // pollyconfig.go), for the notepad's configuration menu.
  const [pollyState, setPollyState] = useState<PollyStateMsg | null>(null)
  // hostPlayback plays the clips the HTTP API made with play, one after
  // another, when the server picks this tab to (see ../../pollyplay.go).
  const hostPlayback = useHostPlayback()
  const enqueueHostClip = hostPlayback.enqueue
  const wsRef = useRef<WebSocket | null>(null)

  const send = useCallback((type: string, payload: unknown) => {
    wsRef.current?.send(JSON.stringify({ type, payload }))
  }, [])

  // A tab tells the server it plays clips for this host, and whether it's
  // visible, so the server can pick one tab -- a visible one first -- to
  // play each (pollyplay.go). Capture-mode tabs stay out of it.
  const sendPresence = useCallback(() => {
    if (isCaptureMode()) return
    send('tab-presence', { visible: document.visibilityState === 'visible' })
  }, [send])

  useEffect(() => {
    document.addEventListener('visibilitychange', sendPresence)
    return () => document.removeEventListener('visibilitychange', sendPresence)
  }, [sendPresence])

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
        sendPresence()
      }

      ws.onclose = () => {
        setConnected(false)
        // A request in flight on the dropped socket will never be answered;
        // free its button rather than leaving it stuck.
        notesInFlightRef.current = {}
        setNotes((n) => ({
          notepad: { ...n.notepad, pending: false },
          transcript: { ...n.transcript, pending: false },
        }))
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
            setAnnounceStartState(!!p.announce_start)
            // A rebuild+restart is invisible to an already-open tab -- the
            // reconnect above is the only signal it gets. Compare the
            // server's own reported version against this bundle's
            // build-time version and reload if they differ; skip under the
            // Vite dev server, where HMR already keeps the tab current.
            if (!import.meta.env.DEV && p.version && p.version !== __APP_VERSION__) {
              window.location.reload()
              return
            }
            // Announce a start/restart (Step5Prompt.md Revision D) -- but not
            // when embedded (LR's/AC's convo tab, condocv2's capture
            // overlay): the page around the iframe announces it itself.
            // started_at only comes while announce start is on.
            if (window.self === window.top) noteTCStart('local', p.started_at)
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
          } else if (msg.type === 'notes-result') {
            const p = msg.payload as NotesResultMsg
            if (p.source !== 'notepad' && p.source !== 'transcript') return
            const sentText = notesInFlightRef.current[p.source]
            delete notesInFlightRef.current[p.source]
            setNotes((n) => ({
              ...n,
              [p.source]: p.success
                ? { pending: false, sent: sentText, message: `appended to ${p.path ?? 'notes file'}` }
                : { pending: false, sent: n[p.source].sent, error: p.error ?? 'append failed' },
            }))
          } else if (msg.type === 'polly-audio') {
            setPollyResult(msg.payload as PollyAudioMsg)
          } else if (msg.type === 'polly-state') {
            setPollyState(msg.payload as PollyStateMsg)
          } else if (msg.type === 'polly-play') {
            enqueueHostClip(msg.payload as PollyPlayMsg)
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

  const setAnnounceStart = useCallback(
    (enabled: boolean) => {
      setAnnounceStartState(enabled)
      send('set-announce-start', { enabled })
    },
    [send],
  )

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

  // clearTranscript empties the transcription window (Revision G) and the
  // server's accumulated copy, so a later Save can't write the cleared text.
  // Recording, if running, carries on into the now-empty window. The To
  // Notes "last sent" marker resets too, so new text can always be appended.
  const clearTranscript = useCallback(() => {
    send('clear-transcript', {})
    setTranscript('')
    setPartialTranscript('')
    setSaveStatus({ kind: 'idle' })
    setNotes((n) => ({ ...n, transcript: { pending: n.transcript.pending } }))
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

  // sendToNotes asks the server to append text to its notes file. The panel
  // keeps its text; its button stays disabled until the text differs from
  // what was last appended (see ToNotesButton).
  const sendToNotes = useCallback(
    (source: NotesSource, text: string) => {
      notesInFlightRef.current[source] = text
      setNotes((n) => ({ ...n, [source]: { ...n[source], pending: true, error: undefined, message: undefined } }))
      send('append-notes', { source, text })
    },
    [send],
  )

  // requestPolly asks the server to synthesize text with AWS Polly and
  // returns the request's id; the answer arrives as pollyResult.
  // With save, the server also keeps the clip in local-representative's
  // files area, where composer can use it (see ../polly.go).
  const requestPolly = useCallback(
    (text: string, save: boolean) => {
      pollySeqRef.current += 1
      send('polly-speak', { id: pollySeqRef.current, text, save })
      return pollySeqRef.current
    },
    [send],
  )

  // The configuration menu's requests (see pollyconfig.go): replayPolly
  // asks for a recent clip's audio, answered as pollyResult like
  // requestPolly; the rest answer with a new pollyState.
  const replayPolly = useCallback(
    (clip: number) => {
      pollySeqRef.current += 1
      send('polly-replay', { id: pollySeqRef.current, clip })
      return pollySeqRef.current
    },
    [send],
  )
  const pollyActions: PollyActions = {
    setActive: useCallback((name: string) => send('polly-set-active', { name }), [send]),
    save: useCallback((config: PollyConfig, previous?: string) => send('polly-save-config', { config, previous }), [send]),
    remove: useCallback((name: string) => send('polly-remove-config', { name }), [send]),
    replay: replayPolly,
  }

  return {
    connected,
    reprStatus,
    reprHost,
    reprPort,
    reprAutoConnect,
    devMode,
    version,
    announceStart,
    setAnnounceStart,
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
    clearTranscript,
    transcribeStatus,
    recheckTranscribe,
    notepad,
    setNotepad,
    notes,
    sendToNotes,
    pollyResult,
    requestPolly,
    pollyState,
    pollyActions,
    hostPlayback,
  }
}

// ---- Clips played on this host (dTT-IntegrationImpls/Step3Prompt.md Revision B) ----
//
// A clip made through the HTTP API with play -- a dtt making clips, say --
// is sent to one TC tab on this host as "polly-play" (../../pollyplay.go).
// useHostPlayback queues them and plays them one after another, in the
// order they came. A hidden tab plays them too: browsers don't stop a
// background tab's audio. What can stop it is the autoplay policy -- a tab
// nobody has clicked in since it loaded may not be allowed to start sound
// -- so a clip refused that way waits at the head of the queue, and the
// next click or key press in the tab plays it and the rest.

interface HostPlayback {
  current: PollyPlayMsg | null // the clip playing, or waiting to be allowed
  queued: number // clips after it
  blocked: boolean // the browser refused to start sound until the tab is used
  enqueue: (clip: PollyPlayMsg) => void
  resume: () => void
  stop: () => void
}

function useHostPlayback(): HostPlayback {
  const queueRef = useRef<PollyPlayMsg[]>([]) // the first is current
  const audioRef = useRef<{ audio: HTMLAudioElement; url: string } | null>(null)
  const [current, setCurrent] = useState<PollyPlayMsg | null>(null)
  const [queued, setQueued] = useState(0)
  const [blocked, setBlocked] = useState(false)

  const release = useCallback(() => {
    const cur = audioRef.current
    audioRef.current = null
    if (cur) {
      cur.audio.pause()
      URL.revokeObjectURL(cur.url)
    }
  }, [])

  // next plays the clip at the head of the queue, unless one is playing.
  const next: () => void = useCallback(() => {
    if (audioRef.current) return
    const clip = queueRef.current[0]
    setCurrent(clip ?? null)
    setQueued(Math.max(0, queueRef.current.length - 1))
    if (!clip) return
    const bytes = Uint8Array.from(atob(clip.audio), (ch) => ch.charCodeAt(0))
    const url = URL.createObjectURL(new Blob([bytes], { type: clip.content_type || 'audio/mpeg' }))
    const audio = new Audio(url)
    const finished = () => {
      if (audioRef.current?.audio !== audio) return
      release()
      queueRef.current.shift()
      next()
    }
    audio.onended = finished
    audio.onerror = finished
    audioRef.current = { audio, url }
    setBlocked(false)
    audio.play().catch((err) => {
      if (audioRef.current?.audio !== audio) return
      if (err instanceof DOMException && err.name === 'NotAllowedError') {
        release() // it stays queued, for resume
        setBlocked(true)
        return
      }
      finished()
    })
  }, [release])

  const enqueue = useCallback((clip: PollyPlayMsg) => {
    queueRef.current.push(clip)
    next()
  }, [next])

  const resume = useCallback(() => {
    setBlocked(false)
    next()
  }, [next])

  const stop = useCallback(() => {
    queueRef.current = []
    release()
    setBlocked(false)
    next()
  }, [release, next])

  // Blocked, the tab's next use is the gesture the browser wants.
  useEffect(() => {
    if (!blocked) return
    document.addEventListener('pointerdown', resume)
    document.addEventListener('keydown', resume)
    return () => {
      document.removeEventListener('pointerdown', resume)
      document.removeEventListener('keydown', resume)
    }
  }, [blocked, resume])

  // Don't keep talking after the page goes away.
  useEffect(() => release, [release])

  return { current, queued, blocked, enqueue, resume, stop }
}

// HostPlaybackStatus shows, in the sidebar, the clip this tab is playing for
// this host and how many wait behind it; nothing while there's none.
function HostPlaybackStatus({ playback }: { playback: HostPlayback }) {
  const { current, queued, blocked, resume, stop } = playback
  if (!current) return null
  const how = [current.config, current.cost && `about ${current.cost}`, current.reused && 'reused'].filter(Boolean).join(', ')
  return (
    <div className="host-playback">
      <div className="host-playback-line" title={current.saved_as}>
        {blocked ? '⏸ waiting to play ' : '▶ playing '}
        {current.saved_as ?? `clip ${current.clip}`}
        {how && <span className="host-playback-how"> ({how})</span>}
      </div>
      {blocked && (
        <div className="host-playback-blocked">
          the browser wants this tab used before it plays sound -- click anywhere here
        </div>
      )}
      <div className="host-playback-controls">
        {queued > 0 && <span>{queued} more queued</span>}
        {blocked && <button className="btn-secondary" onClick={resume}>play</button>}
        <button className="btn-secondary" onClick={stop}>stop{queued > 0 ? ' all' : ''}</button>
      </div>
    </div>
  )
}

// ---- Announcements (dTT-IntegrationImpls/Step3Prompt.md Revision A) ----
//
// "announce self" says hello in this tab; "announce start" is the server's
// toggle for saying "application running" on a start or restart -- off by
// default, and shared by every tab, LR's and AC's included (see
// ../../announce.go and tcannounce.ts).

function AnnounceControls({ connected, announceStart, onSetAnnounceStart }: {
  connected: boolean
  announceStart: boolean
  onSetAnnounceStart: (enabled: boolean) => void
}) {
  const announceSelf = () => {
    if (!speechSupported) return
    window.speechSynthesis.cancel()
    window.speechSynthesis.speak(new SpeechSynthesisUtterance('hello'))
  }
  return (
    <div className="announce-controls">
      <button
        className="btn-secondary"
        disabled={!speechSupported}
        title={speechSupported ? 'say "hello" in this tab' : 'this browser has no speech synthesis'}
        onClick={announceSelf}
      >
        announce self
      </button>
      <label
        className="repr-footer-auto-connect"
        title='say "application running" when The Conversationalist starts or restarts -- here, and in local-representative and agent-coordinator'
      >
        <input
          type="checkbox"
          checked={announceStart}
          disabled={!connected}
          onChange={(e) => onSetAnnounceStart(e.target.checked)}
        />
        announce start
      </label>
    </div>
  )
}

// PollyActions are the configuration menu's requests to the server.
interface PollyActions {
  setActive: (name: string) => void
  save: (config: PollyConfig, previous?: string) => void // previous: the name it renames
  remove: (name: string) => void // an added one; a preset is reset
  replay: (clip: number) => number // the request id its audio answers
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

// NotesStatus is one panel's "To Notes" state: sent is the text it last
// appended successfully, message/error describe the latest attempt.
type NotesStatus = { pending: boolean; sent?: string; message?: string; error?: string }

const INITIAL_NOTES: Record<NotesSource, NotesStatus> = {
  notepad: { pending: false },
  transcript: { pending: false },
}

// ---- To Notes (Step5SubstepGPrompt.md Revision C) ----
//
// ToNotesButton appends text to the server's notes file (~/notes-input.txt
// by default -- see notes.go) without clearing the panel it sits in. It's
// only enabled while there's text to send that differs from what this panel
// last appended, so the same content can't be appended twice in a row.

function ToNotesButton({ connected, text, status, onSend }: {
  connected: boolean
  text: string
  status: NotesStatus
  onSend: (text: string) => void
}) {
  const alreadySent = status.sent !== undefined && text === status.sent
  const disabled = !connected || status.pending || !text.trim() || alreadySent
  const title = !text.trim()
    ? 'nothing to append'
    : alreadySent
      ? 'already appended -- edit the text to append again'
      : 'append this text to the notes file'
  return (
    <span className="to-notes">
      <button className="btn-secondary" disabled={disabled} title={title} onClick={() => onSend(text)}>
        {status.pending ? 'Appending…' : 'To Notes'}
      </button>
      {status.error ? (
        <span className="to-notes-status to-notes-error">{status.error}</span>
      ) : alreadySent && status.message ? (
        <span className="to-notes-status">{status.message}</span>
      ) : null}
    </span>
  )
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
  onClear: () => void
  onRecheck: () => void
  notes: NotesStatus
  onToNotes: (text: string) => void
  onToNotepad: (text: string) => void
}

const TRANSCRIBE_ENGINE_LABELS: Record<string, string> = {
  'aws-transcribe': 'AWS Transcribe',
}

// TranscribeAvailability shows whether the server can currently reach its
// transcription engine (see transcribe_status.go): a quiet one-liner while
// available or checking, and a prominent "unavailable" notice with the
// server's reason and a Recheck button otherwise. A server warning (e.g. a
// defaulted region) shows as a yellow notice above either.
function TranscribeAvailability({ status, connected, onRecheck }: {
  status: TranscribeStatusMsg
  connected: boolean
  onRecheck: () => void
}) {
  return (
    <>
      {status.warning && (
        <div className="transcribe-availability transcribe-availability-warning">{status.warning}</div>
      )}
      <TranscribeAvailabilityState status={status} connected={connected} onRecheck={onRecheck} />
    </>
  )
}

function TranscribeAvailabilityState({ status, connected, onRecheck }: {
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
  const who = status.identity
    ? `as ${status.identity}${status.credential_source ? ` (${status.credential_source})` : ''}`
    : undefined
  return (
    <div className={`transcribe-availability transcribe-availability-${status.state}`} title={who}>
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
  onClear,
  onRecheck,
  notes,
  onToNotes,
  onToNotepad,
}: TranscriptionPanelProps) {
  const hasTranscript = Boolean(transcript.trim() || partialTranscript.trim())
  // What the window shows, partial result included.
  const shownTranscript = `${transcript} ${partialTranscript}`.trim()
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
        <ToNotesButton connected={connected} text={shownTranscript} status={notes} onSend={onToNotes} />
        <button
          className="btn-secondary"
          disabled={!connected || !hasTranscript}
          title="add the transcript to the end of the shared notepad"
          onClick={() => onToNotepad(shownTranscript)}
        >
          To Notepad
        </button>
        <button className="btn-secondary" disabled={!connected || !hasTranscript} onClick={onClear}>
          Clear
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
  notes: NotesStatus
  onToNotes: (text: string) => void
  pollyResult: PollyAudioMsg | null
  onPolly: (text: string, save: boolean) => number
  pollyState: PollyStateMsg | null
  pollyActions: PollyActions
}

function NotepadPanel({ connected, text, onChange, notes, onToNotes, pollyResult, onPolly, pollyState, pollyActions }: NotepadPanelProps) {
  const inputRef = useRef<HTMLTextAreaElement>(null)
  // menuOpen shows the Polly configuration menu in place of the notepad
  // (Step3Prompt.md); its "up" button closes it.
  const [menuOpen, setMenuOpen] = useState(false)
  const [pollyError, setPollyError] = useState<string | undefined>()
  // savePolly keeps each Polly clip in local-representative's files area as
  // well as playing it -- composer's source for Polly audio.
  const [savePolly, setSavePolly] = useState(false)
  const [pollySaved, setPollySaved] = useState<string | undefined>()
  const requestPolly = useCallback(
    (t: string) => {
      setPollySaved(undefined)
      return onPolly(t, savePolly)
    },
    [onPolly, savePolly],
  )

  // replayIdsRef holds the menu's replay requests, whose answers are the
  // menu's to report.
  const replayIdsRef = useRef(new Set<number>())
  const replayPolly = pollyActions.replay
  const replay = useCallback(
    (clip: number) => {
      const id = replayPolly(clip)
      replayIdsRef.current.add(id)
      return id
    },
    [replayPolly],
  )

  // Report what a clip cost (or that a recent one was reused) and where a
  // saved clip went (or why it wasn't saved). A replay from the menu says
  // nothing here.
  useEffect(() => {
    if (!pollyResult || pollyResult.error || !pollyResult.config || replayIdsRef.current.has(pollyResult.id)) return
    const made = pollyResult.reused
      ? `reused a recent clip (${pollyResult.config}) -- no Polly call`
      : `${pollyResult.config}: ${pollyResult.characters} characters, about ${pollyResult.cost}`
    const saved = pollyResult.saved_as
      ? `; saved to files as ${pollyResult.saved_as}`
      : pollyResult.save_error ? `; clip not saved: ${pollyResult.save_error}` : ''
    setPollySaved(made + saved)
  }, [pollyResult])

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

  if (menuOpen) {
    return (
      <div className="notepad-panel">
        <PollyConfigMenu
          connected={connected}
          state={pollyState}
          actions={pollyActions}
          result={pollyResult}
          onReplay={replay}
          sampleText={text}
          onUp={() => setMenuOpen(false)}
        />
      </div>
    )
  }

  return (
    <div className="notepad-panel">
      <div className="notepad-header">
        <span className="notepad-title">Notepad</span>
        <span className="notepad-hint">{connected ? 'shared with all clients' : 'disconnected — read-only'}</span>
        <ToNotesButton connected={connected} text={text} status={notes} onSend={onToNotes} />
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
        <div className="notepad-speak-buttons">
          <SpeakButton getText={speakText} disabled={!text.trim()} />
          <div className="polly-buttons">
            <PollyButton
              connected={connected}
              getText={speakText}
              disabled={!text.trim()}
              result={pollyResult}
              onRequest={requestPolly}
              onError={setPollyError}
            />
            <button
              type="button"
              className="polly-gear-btn"
              title={`Polly configuration${pollyState ? ` (using ${pollyState.active})` : ''} and recent clips`}
              onClick={() => setMenuOpen(true)}
            >
              {GEAR_ICON}
            </button>
          </div>
          <label className="polly-save-toggle" title="also keep each Polly clip in local-representative's files area, for composer">
            <input type="checkbox" checked={savePolly} onChange={(e) => setSavePolly(e.target.checked)} />
            save
          </label>
        </div>
      </div>
      {pollyError && <div className="polly-error">{pollyError}</div>}
      {pollySaved && <div className="polly-saved">{pollySaved}</div>}
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

// ---- AWS Polly speech (Step5SubstepGPrompt.md Revision I) ----
//
// Option B of docs/ConversationalistSpeechOutput.md, as one isolated button
// under SpeakButton: the server synthesizes the text with Polly (polly.go)
// and only this tab plays the returned audio.

// POLLY_ICON is SPEAK_ICON's balloon with a "P" in it.
const POLLY_ICON = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
    <path d="M4 4h16a1 1 0 0 1 1 1v11a1 1 0 0 1-1 1h-9l-4 3.5V17H4a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1z" />
    <path d="M10 14V7.5h2.5a2 2 0 0 1 0 4H10" />
  </svg>
)

// POLLY_MAX_CHARS matches polly.go's pollyMaxChars.
const POLLY_MAX_CHARS = 3000

// usePollyPlayer plays the "polly-audio" answer to one request at a time:
// wait(id) names the request whose answer to play, stop() cancels the wait
// or the playback. A failure is reported through onError.
function usePollyPlayer(connected: boolean, result: PollyAudioMsg | null, onError: (message?: string) => void) {
  // waitingId is the request whose answer should be played; 0 when none.
  const [waitingId, setWaitingId] = useState(0)
  const [playing, setPlaying] = useState(false)
  const audioRef = useRef<{ audio: HTMLAudioElement; url: string } | null>(null)

  const stopAudio = useCallback(() => {
    const cur = audioRef.current
    audioRef.current = null
    if (cur) {
      cur.audio.pause()
      URL.revokeObjectURL(cur.url)
    }
    setPlaying(false)
  }, [])

  // Don't keep talking after the panel goes away.
  useEffect(() => stopAudio, [stopAudio])

  // A request in flight on a dropped socket will never be answered.
  useEffect(() => {
    if (!connected) setWaitingId(0)
  }, [connected])

  useEffect(() => {
    if (!result || waitingId === 0 || result.id !== waitingId) return
    setWaitingId(0)
    if (result.error || !result.audio) {
      onError(`Polly: ${result.error ?? 'no audio returned'}`)
      return
    }
    const bytes = Uint8Array.from(atob(result.audio), (ch) => ch.charCodeAt(0))
    const url = URL.createObjectURL(new Blob([bytes], { type: result.content_type || 'audio/mpeg' }))
    const audio = new Audio(url)
    audio.onended = stopAudio
    audio.onerror = () => {
      stopAudio()
      onError('Polly: the browser could not play the audio')
    }
    stopAudio()
    audioRef.current = { audio, url }
    setPlaying(true)
    audio.play().catch((err) => {
      stopAudio()
      onError(`Polly: playback failed: ${String(err)}`)
    })
  }, [result, waitingId, stopAudio, onError])

  const stop = useCallback(() => {
    setWaitingId(0)
    stopAudio()
  }, [stopAudio])

  return { waitingId, playing, busy: waitingId !== 0 || playing, wait: setWaitingId, stop }
}

// PollyButton toggles Polly speech of getText()'s result: click to request
// and play it, click again to cancel the wait or stop playback. A failure
// is reported through onError and cleared on the next click.
function PollyButton({ connected, getText, disabled, result, onRequest, onError }: {
  connected: boolean
  getText: () => string
  disabled: boolean
  result: PollyAudioMsg | null
  onRequest: (text: string) => number
  onError: (message?: string) => void
}) {
  const { waitingId, playing, busy, wait: setWaitingId, stop } = usePollyPlayer(connected, result, onError)

  const toggle = useCallback(() => {
    onError(undefined)
    if (busy) {
      stop()
      return
    }
    const text = getText().trim()
    if (!text) return
    const chars = [...text].length
    if (chars > POLLY_MAX_CHARS) {
      onError(`Polly: ${chars} characters is over its ${POLLY_MAX_CHARS}-character limit -- select a shorter part`)
      return
    }
    setWaitingId(onRequest(text))
  }, [busy, getText, onRequest, onError, stop, setWaitingId])

  return (
    <button
      type="button"
      className={`speak-btn${busy ? ' speak-btn-active' : ''}${waitingId !== 0 ? ' speak-btn-waiting' : ''}`}
      disabled={!busy && (disabled || !connected)}
      title={
        waitingId !== 0
          ? 'waiting for AWS Polly -- click to cancel'
          : playing
            ? 'stop speaking'
            : 'speak the notepad (or its selected text) aloud with AWS Polly'
      }
      aria-pressed={busy}
      onClick={toggle}
    >
      {POLLY_ICON}
    </button>
  )
}

// ---- Polly configuration menu (dTT-IntegrationImpls/Step3Prompt.md) ----
//
// The gear beside the Polly button opens this in place of the notepad;
// "up" goes back. It holds the configurations (a default and a few other
// presets, each editable, plus any added), which one the Polly button uses,
// and the recent clips, which replay from the server's memory without
// asking Polly again (see ../../pollyconfig.go). It's all the server's, so
// every tab sees the same.

// GEAR_ICON is a small cog, in the same wireframe style.
const GEAR_ICON = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
    <circle cx="12" cy="12" r="3" />
    <path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5.3 5.3l2.1 2.1M16.6 16.6l2.1 2.1M5.3 18.7l2.1-2.1M16.6 7.4l2.1-2.1" />
    <circle cx="12" cy="12" r="6.5" />
  </svg>
)

const PLAY_ICON = (
  <svg viewBox="0 0 24 24" fill="currentColor" stroke="none">
    <path d="M8 5.5v13l10.5-6.5z" />
  </svg>
)

const STOP_ICON = (
  <svg viewBox="0 0 24 24" fill="currentColor" stroke="none">
    <rect x="7" y="7" width="10" height="10" rx="1" />
  </svg>
)

// POLLY_VOICES are suggestions for the voice field; any Polly voice id goes.
const POLLY_VOICES = [
  'Joanna', 'Matthew', 'Ruth', 'Stephen', 'Danielle', 'Gregory', 'Ivy', 'Justin', 'Kendra', 'Kimberly',
  'Salli', 'Joey', 'Kevin', 'Patrick', 'Amy', 'Emma', 'Brian', 'Arthur', 'Olivia', 'Aria', 'Ayanda',
  'Niamh', 'Geraint', 'Raveena', 'Kajal',
]

// POLLY_REGIONS are suggestions for the region field; us-east-1 offers every
// engine (see pollyconfig.go's pollyPresetRegion).
const POLLY_REGIONS = ['us-east-1', 'us-west-2', 'us-east-2', 'ca-central-1', 'eu-west-1', 'eu-west-2', 'eu-central-1', 'ap-southeast-2', 'ap-northeast-1']

// pollyRegionLabel says where a configuration asks Polly.
function pollyRegionLabel(region?: string): string {
  return region || 'default region'
}

// pollyCostWords matches pollyconfig.go's: "0.32 cents", "1.50 dollars".
function pollyCostWords(usd: number): string {
  return usd >= 1 ? `${usd.toFixed(2)} dollars` : `${(usd * 100).toFixed(2)} cents`
}

function PollyConfigMenu({ connected, state, actions, result, onReplay, sampleText, onUp }: {
  connected: boolean
  state: PollyStateMsg | null
  actions: PollyActions
  result: PollyAudioMsg | null
  onReplay: (clip: number) => number
  sampleText: string
  onUp: () => void
}) {
  // selected is the configuration being looked at and edited; draft is its
  // fields as edited, until saved.
  const [selected, setSelected] = useState<string | null>(null)
  const shownName = selected ?? state?.active
  const current = state?.configs.find((c) => c.name === shownName) ?? null
  const [draft, setDraft] = useState<PollyConfig | null>(current)
  const currentKey = current ? JSON.stringify(current) : ''
  useEffect(() => setDraft(current), [currentKey]) // eslint-disable-line react-hooks/exhaustive-deps

  const [playError, setPlayError] = useState<string | undefined>()
  const player = usePollyPlayer(connected, result, setPlayError)
  const [replaying, setReplaying] = useState(0) // the clip the player is for

  const toggleReplay = (clip: PollyClipView) => {
    setPlayError(undefined)
    const mine = replaying === clip.id && player.busy
    player.stop()
    if (mine) return
    setReplaying(clip.id)
    player.wait(onReplay(clip.id))
  }

  if (!state) {
    return (
      <div className="polly-menu">
        <PollyMenuHeader connected={connected} onUp={onUp} />
        <div className="polly-menu-hint">{connected ? 'waiting for the server…' : 'disconnected'}</div>
      </div>
    )
  }

  const price = (engine: string) => state.prices[engine] ?? 0
  const changed = !!(draft && current && JSON.stringify(draft) !== JSON.stringify(current))
  const nameTaken = !!draft && state.configs.some((c) => c.name === draft.name)
  const sampleChars = [...sampleText.trim()].length
  const totalCost = state.clips.reduce((sum, c) => sum + c.cost_usd, 0)
  const savedCost = state.clips.reduce((sum, c) => sum + c.cost_usd * Math.max(0, c.uses - 1), 0)

  const save = () => {
    if (!draft || !current) return
    actions.save(draft, current.preset ? undefined : current.name)
    setSelected(draft.name)
  }
  const saveAsNew = () => {
    if (!draft) return
    actions.save({ ...draft, preset: false, edited: false })
    setSelected(draft.name)
  }

  return (
    <div className="polly-menu">
      <PollyMenuHeader connected={connected} onUp={onUp} />
      {state.error && <div className="polly-error">{state.error}</div>}
      <div className="polly-menu-body">
        <div className="polly-menu-section">
          <div className="polly-menu-section-title">configurations</div>
          <div className="polly-menu-hint">
            The Polly button speaks with the one ticked. Prices are Polly's list prices per million characters.
            Each asks Polly in the region shown -- "default region" is this server's AWS region -- because
            Polly offers its neural, generative and long-form engines in only some regions.
          </div>
          {state.configs.map((c) => (
            <div
              key={c.name}
              className={`polly-config-row${c.name === current?.name ? ' polly-config-row-selected' : ''}`}
              onClick={() => setSelected(c.name)}
            >
              <input
                type="radio"
                name="polly-active"
                checked={c.name === state.active}
                disabled={!connected}
                title="use this configuration for the Polly button"
                onClick={(e) => e.stopPropagation()}
                onChange={() => actions.setActive(c.name)}
              />
              <span className="polly-config-name">{c.name}</span>
              <span className="polly-config-voice">{c.voice} · {c.engine} · {pollyRegionLabel(c.region)}</span>
              <span className="polly-config-price">${price(c.engine)}/M</span>
              {c.preset && <span className="polly-badge">{c.edited ? 'preset, edited' : 'preset'}</span>}
              <span className="polly-config-desc">{c.description}</span>
            </div>
          ))}
        </div>

        {draft && current && (
          <div className="polly-menu-section">
            <div className="polly-menu-section-title">edit {current.name}</div>
            <div className="polly-edit-grid">
              <label>name</label>
              <input
                className="repr-footer-input"
                value={draft.name}
                disabled={current.preset}
                title={current.preset ? 'a preset keeps its name -- change it and "save as new" for a copy' : undefined}
                onChange={(e) => setDraft({ ...draft, name: e.target.value.toLowerCase() })}
              />
              <label>voice</label>
              <input
                className="repr-footer-input"
                list="polly-voices"
                value={draft.voice}
                onChange={(e) => setDraft({ ...draft, voice: e.target.value })}
              />
              <label>engine</label>
              <select
                className="repr-footer-input"
                value={draft.engine}
                onChange={(e) => setDraft({ ...draft, engine: e.target.value })}
              >
                {state.engines.map((en) => (
                  <option key={en} value={en}>{en} (${price(en)} per million characters)</option>
                ))}
              </select>
              <label>region</label>
              <input
                className="repr-footer-input"
                list="polly-regions"
                value={draft.region ?? ''}
                placeholder="default region (this server's AWS region)"
                title="the AWS region to ask Polly in -- one that offers the engine; empty for this server's AWS region"
                onChange={(e) => setDraft({ ...draft, region: e.target.value })}
              />
              <label>about</label>
              <input
                className="repr-footer-input"
                value={draft.description ?? ''}
                onChange={(e) => setDraft({ ...draft, description: e.target.value })}
              />
            </div>
            <datalist id="polly-voices">
              {POLLY_VOICES.map((v) => <option key={v} value={v} />)}
            </datalist>
            <datalist id="polly-regions">
              {POLLY_REGIONS.map((r) => <option key={r} value={r} />)}
            </datalist>
            {sampleChars > 0 && (
              <div className="polly-menu-hint">
                The notepad's {sampleChars} characters would cost about {pollyCostWords((price(draft.engine) * sampleChars) / 1e6)} with this.
              </div>
            )}
            <div className="polly-edit-actions">
              <button className="btn-secondary" disabled={!connected || !changed} onClick={save}>save</button>
              <button
                className="btn-secondary"
                disabled={!connected || nameTaken}
                title={nameTaken ? 'give it a name of its own first' : 'add these settings as a new configuration'}
                onClick={saveAsNew}
              >
                save as new
              </button>
              {changed && <button className="btn-secondary" onClick={() => setDraft(current)}>undo changes</button>}
              {current.preset ? (
                current.edited && (
                  <button className="btn-secondary" disabled={!connected} onClick={() => actions.remove(current.name)}>
                    reset to preset
                  </button>
                )
              ) : (
                <button
                  className="btn-secondary"
                  disabled={!connected}
                  onClick={() => {
                    actions.remove(current.name)
                    setSelected(null)
                  }}
                >
                  remove
                </button>
              )}
              {current.name !== state.active && (
                <button className="btn-secondary" disabled={!connected} onClick={() => actions.setActive(current.name)}>use</button>
              )}
            </div>
          </div>
        )}

        <div className="polly-menu-section">
          <div className="polly-menu-section-title">recent clips</div>
          <div className="polly-menu-hint">
            {state.clips.length === 0
              ? "None yet. Clips made here -- from the notepad or through /api/polly (dtt's localrep tool) -- are kept in memory for replaying."
              : `${state.clips.length} held in memory, newest first: about ${pollyCostWords(totalCost)} to make` +
                (savedCost > 0 ? `, and about ${pollyCostWords(savedCost)} saved by reusing them` : '') +
                '. Replaying, or asking again for the same text, voice and engine, reuses a clip without a Polly call.'}
          </div>
          {playError && <div className="polly-error">{playError}</div>}
          {state.clips.map((c) => {
            const mine = replaying === c.id && player.busy
            return (
              <div key={c.id} className="polly-clip-row">
                <button
                  type="button"
                  className={`speak-btn${mine ? ' speak-btn-active' : ''}${mine && player.waitingId !== 0 ? ' speak-btn-waiting' : ''}`}
                  disabled={!connected && !mine}
                  title={mine ? 'stop' : 'replay this clip (no Polly call)'}
                  onClick={() => toggleReplay(c)}
                >
                  {mine ? STOP_ICON : PLAY_ICON}
                </button>
                <div className="polly-clip-main">
                  <div className="polly-clip-text" title={c.text}>{c.text}</div>
                  <div className="polly-clip-meta">
                    {new Date(c.at).toLocaleTimeString()} · {c.config} ({c.voice}, {c.engine}{c.region ? `, ${c.region}` : ''}) · {c.characters} characters · {c.cost}
                    {c.uses > 1 ? ` · reused ${c.uses - 1}×` : ''} · from {c.from}
                    {c.saved_as ? ` · ${c.saved_as}` : ''}
                  </div>
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}

function PollyMenuHeader({ connected, onUp }: { connected: boolean; onUp: () => void }) {
  return (
    <div className="notepad-header polly-menu-header">
      <button className="btn-secondary polly-menu-up" title="back to the notepad" onClick={onUp}>↑ up</button>
      <span className="notepad-title">Polly configuration</span>
      <span className="notepad-hint">{connected ? 'shared with all clients' : 'disconnected — read-only'}</span>
    </div>
  )
}

// ---- Capture mode (Step2Prompt.md) ----
//
// condocv2 embeds this app in an iframe with "?embed=capture" (see
// condocv2/frontend/src/App.tsx's tcCaptureURL/TCCaptureOverlay) to dictate
// straight into one of its own text fields, rather than the full
// TranscriptionPanel/Save-to-file flow above. This is the only thing about
// The Conversationalist condocv2 ever sees: recording starts itself the
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
    announceStart,
    setAnnounceStart,
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
    clearTranscript,
    transcribeStatus,
    recheckTranscribe,
    notepad,
    setNotepad,
    notes,
    sendToNotes,
    pollyResult,
    requestPolly,
    pollyState,
    pollyActions,
    hostPlayback,
  } = useConversationalistWS()

  // appendToNotepad adds text to the end of the shared notepad on a line of
  // its own -- the transcription window's "To Notepad" button (Revision I).
  const appendToNotepad = (text: string) => {
    if (!notepad) setNotepad(text)
    else setNotepad(`${notepad}${notepad.endsWith('\n') ? '' : '\n'}${text}`)
  }

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
          <AnnounceControls connected={connected} announceStart={announceStart} onSetAnnounceStart={setAnnounceStart} />
          <HostPlaybackStatus playback={hostPlayback} />
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
          onClear={clearTranscript}
          onRecheck={recheckTranscribe}
          notes={notes.transcript}
          onToNotes={(text) => sendToNotes('transcript', text)}
          onToNotepad={appendToNotepad}
        />
        <NotepadPanel
          connected={connected}
          text={notepad}
          onChange={setNotepad}
          notes={notes.notepad}
          onToNotes={(text) => sendToNotes('notepad', text)}
          pollyResult={pollyResult}
          onPolly={requestPolly}
          pollyState={pollyState}
          pollyActions={pollyActions}
        />
      </div>
    </div>
  )
}
