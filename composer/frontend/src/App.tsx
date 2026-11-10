import { useCallback, useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { mediaKind, play, sourceUrl, trackDuration, uploadRender } from './render'
import type { Playback } from './render'
import type {
  Clip,
  ClipKind,
  Composition,
  CompositionsMsg,
  CompositionSummary,
  ExampleSummary,
  ModeMismatchMsg,
  RenderCapabilityMsg,
  RenderJob,
  RenderJobMsg,
  RenderJobsMsg,
  RenderResultMsg,
  ReprStatus,
  ReprStatusMsg,
  SavedMsg,
  SelfInfoMsg,
  Source,
  SourcesMsg,
  Track,
} from './types'

// ---- WebSocket hook ----
//
// Wires up the representable connect/disconnect widget, dev-mode banner and
// version-mismatch reload check shared by every sub-app in this repo, plus
// composer's own messages: the saved compositions (../compositions.go) and
// the media in local-representative's files area they're built from
// (../sources.go).

type Notice = { kind: 'error' | 'success'; message: string } | null

function useComposerWS() {
  const [connected, setConnected] = useState(false)
  const [reprStatus, setReprStatus] = useState<ReprStatus>('disconnected')
  const [reprHost, setReprHost] = useState('')
  const [reprPort, setReprPort] = useState('')
  const [reprAutoConnect, setReprAutoConnect] = useState(false)
  const [devMode, setDevMode] = useState(false)
  const [version, setVersion] = useState('')
  const [modeMismatch, setModeMismatch] = useState<ModeMismatchMsg | null>(null)

  const [compositions, setCompositions] = useState<CompositionSummary[]>([])
  const [examples, setExamples] = useState<ExampleSummary[]>([])
  const [sources, setSources] = useState<SourcesMsg>({ sources: [] })
  // The composition open in the editor, as last saved; null when none.
  const [open, setOpen] = useState<Composition | null>(null)
  const [notice, setNotice] = useState<Notice>(null)
  // Render jobs (../renderjobs.go): the browser jobs no tab has claimed
  // yet, and the one this tab won and is rendering.
  const [waitingJobs, setWaitingJobs] = useState<RenderJob[]>([])
  const [assignedJob, setAssignedJob] = useState<RenderJobMsg | null>(null)
  // Whether this node has ffmpeg (../ffmpeg.go); null until the server says.
  const [renderCapability, setRenderCapability] = useState<RenderCapabilityMsg | null>(null)
  // openIdRef lets the message handler drop a "composition" for one that's
  // no longer open.
  const openIdRef = useRef<string | null>(null)

  const wsRef = useRef<WebSocket | null>(null)

  const send = useCallback((type: string, payload: unknown) => {
    wsRef.current?.send(JSON.stringify({ type, payload }))
  }, [])

  const connectRepr = useCallback((host: string, port: string) => send('connect', { host, port }), [send])
  const disconnectRepr = useCallback(() => send('disconnect', {}), [send])
  const setAutoConnectRepr = useCallback(
    (enabled: boolean, host?: string, port?: string) => send('set-auto-connect', { enabled, host, port }),
    [send],
  )

  const openComposition = useCallback(
    (id: string) => {
      openIdRef.current = id
      send('get-composition', { id })
    },
    [send],
  )

  // newDraft opens a blank, not-yet-saved composition (id "").
  const newDraft = useCallback(() => {
    openIdRef.current = ''
    setOpen({ id: '', name: 'Untitled', width: 1280, height: 720, video: [], audio: [] })
  }, [])

  const closeComposition = useCallback(() => {
    openIdRef.current = null
    setOpen(null)
  }, [])

  const saveComposition = useCallback((c: Composition) => send('save-composition', c), [send])
  const newFromExample = useCallback((id: string) => send('new-from-example', { id }), [send])
  const deleteComposition = useCallback((id: string) => send('delete-composition', { id }), [send])
  const deleteAllCompositions = useCallback(() => send('delete-all-compositions', {}), [send])
  const refreshSources = useCallback(() => send('list-sources', {}), [send])
  const claimRenderJob = useCallback((id: string) => send('claim-render-job', { id }), [send])
  const failRenderJob = useCallback(
    (id: string, error: string) => {
      send('render-job-failed', { id, error })
      setAssignedJob(null)
    },
    [send],
  )
  const finishRenderJob = useCallback(() => setAssignedJob(null), [])
  const checkRenderCapability = useCallback(() => send('check-render-capability', {}), [send])

  useEffect(() => {
    // Derive the WebSocket URL from the path this document was served
    // under, so it also works when reverse-proxied beneath a prefix
    // (/compose/ via local-representative, /host/<id>/compose/ via
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
            // reconnect above is the only signal it gets. Reload if the
            // server's version differs from this bundle's; skip under the
            // Vite dev server, where HMR already keeps the tab current.
            if (!import.meta.env.DEV && p.version && p.version !== __APP_VERSION__) {
              window.location.reload()
            }
          } else if (msg.type === 'mode-mismatch') {
            const p = msg.payload as ModeMismatchMsg
            setModeMismatch(p.mismatched ? p : null)
          } else if (msg.type === 'compositions') {
            const p = msg.payload as CompositionsMsg
            setCompositions(p.compositions ?? [])
            setExamples(p.examples ?? [])
            if (p.error) setNotice({ kind: 'error', message: `compositions: ${p.error}` })
          } else if (msg.type === 'composition') {
            const p = msg.payload as Composition
            if (p.id === openIdRef.current) setOpen(p)
          } else if (msg.type === 'saved') {
            const p = msg.payload as SavedMsg
            if (p.action === 'delete-all') {
              // Even a failed delete-all may have removed some, so the
              // open one goes if it was saved (a new draft has no id).
              if (openIdRef.current) {
                openIdRef.current = null
                setOpen(null)
              }
              const deleted = `deleted ${p.count ?? 0} composition${p.count === 1 ? '' : 's'}`
              setNotice(p.error ? { kind: 'error', message: `delete all: ${p.error} (${deleted})` } : { kind: 'success', message: deleted })
            } else if (p.error) {
              setNotice({ kind: 'error', message: `${p.action}: ${p.error}` })
            } else if (p.action === 'delete') {
              setNotice({ kind: 'success', message: `deleted ${p.id}` })
              if (p.id === openIdRef.current) {
                openIdRef.current = null
                setOpen(null)
              }
            } else if (p.composition) {
              setNotice({ kind: 'success', message: p.action === 'example' ? `created ${p.composition.name}` : 'saved' })
              openIdRef.current = p.composition.id
              setOpen(p.composition)
            }
          } else if (msg.type === 'sources') {
            setSources(msg.payload as SourcesMsg)
          } else if (msg.type === 'render-result') {
            const p = msg.payload as RenderResultMsg
            if (p.error) setNotice({ kind: 'error', message: `render: ${p.error}` })
            else
              setNotice({
                kind: 'success',
                message: `rendered ${p.name}${p.renderer ? ` (${RENDERER_LABELS[p.renderer]})` : ''} into local-representative's files`,
              })
          } else if (msg.type === 'render-jobs') {
            setWaitingJobs((msg.payload as RenderJobsMsg).waiting ?? [])
          } else if (msg.type === 'render-job') {
            setAssignedJob(msg.payload as RenderJobMsg)
          } else if (msg.type === 'render-capability') {
            setRenderCapability(msg.payload as RenderCapabilityMsg)
          } else if (msg.type === 'error') {
            setNotice({ kind: 'error', message: String(msg.payload) })
          }
        } catch {
          // ignore malformed messages
        }
      }
    }

    connect()
    return () => wsRef.current?.close()
  }, [])

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
    compositions,
    examples,
    sources,
    open,
    notice,
    setNotice,
    openComposition,
    newDraft,
    closeComposition,
    saveComposition,
    newFromExample,
    deleteComposition,
    deleteAllCompositions,
    refreshSources,
    waitingJobs,
    assignedJob,
    claimRenderJob,
    failRenderJob,
    finishRenderJob,
    renderCapability,
    checkRenderCapability,
  }
}

// ---- Render jobs ----
//
// A render job (../renderjobs.go) with renderer "browser" is rendered by
// whichever open compose tab claims it first, exactly as Render would. A
// tab claims on its own only while visible and once the page has been
// clicked -- browsers won't start sound before that, and a recording's
// clock is its sound -- so the banner's "render now" button is the other
// way in.

type UserActivationNavigator = Navigator & { userActivation?: { hasBeenActive: boolean } }

function pageActivated(): boolean {
  const ua = (navigator as UserActivationNavigator).userActivation
  return ua ? ua.hasBeenActive : false
}

function RenderJobs({
  waiting,
  assigned,
  onClaim,
  onFail,
  onDone,
  onNotice,
}: {
  waiting: RenderJob[]
  assigned: RenderJobMsg | null
  onClaim: (id: string) => void
  onFail: (id: string, error: string) => void
  onDone: () => void
  onNotice: (n: Notice) => void
}) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const [visible, setVisible] = useState(document.visibilityState === 'visible')
  const [progress, setProgress] = useState<{ t: number; d: number } | null>(null)

  const [activated, setActivated] = useState(pageActivated)

  useEffect(() => {
    const onChange = () => setVisible(document.visibilityState === 'visible')
    document.addEventListener('visibilitychange', onChange)
    return () => document.removeEventListener('visibilitychange', onChange)
  }, [])

  // The first click or key press in the page lets it play sound, so a
  // waiting job can be claimed from then on.
  useEffect(() => {
    if (activated) return
    const onActivate = () => setActivated(true)
    document.addEventListener('pointerdown', onActivate, true)
    document.addEventListener('keydown', onActivate, true)
    return () => {
      document.removeEventListener('pointerdown', onActivate, true)
      document.removeEventListener('keydown', onActivate, true)
    }
  }, [activated])

  // Claim the oldest waiting job on our own when we can render it.
  const next = waiting[0]?.id
  useEffect(() => {
    if (next && !assigned && visible && activated) onClaim(next)
  }, [next, assigned, visible, activated, onClaim])

  // Render the job this tab won, then upload it against the job.
  const jobId = assigned?.job.id
  useEffect(() => {
    if (!assigned || !canvasRef.current) return
    const { job, composition } = assigned
    let cancelled = false
    let stop: (() => void) | null = null
    ;(async () => {
      try {
        onNotice({ kind: 'success', message: `rendering ${job.name || job.composition} for a render job -- keep this tab visible` })
        const pb = await play(composition, {
          canvas: canvasRef.current!,
          record: true,
          onProgress: (t, d) => setProgress({ t, d }),
        })
        stop = pb.stop
        const blob = await pb.done
        if (cancelled) return
        if (!blob || blob.size === 0) throw new Error('the recording came out empty')
        await uploadRender(composition.id, blob, job.id)
        onDone()
      } catch (err) {
        if (cancelled) return
        const message = err instanceof Error ? err.message : String(err)
        onNotice({ kind: 'error', message: `render job: ${message}` })
        onFail(job.id, message)
      } finally {
        setProgress(null)
      }
    })()
    return () => {
      cancelled = true
      stop?.()
    }
    // Only a new job starts a new render.
  }, [jobId])

  return (
    <>
      <canvas ref={canvasRef} className="job-canvas" aria-hidden="true" />
      {assigned ? (
        <div className="job-banner">
          rendering {assigned.job.name || assigned.job.composition} for a render job
          {progress && ` -- ${formatSeconds(progress.t)} / ${formatSeconds(progress.d)}`} · keep this tab visible
        </div>
      ) : (
        waiting.length > 0 && (
          <div className="job-banner">
            {waiting.length} render job{waiting.length === 1 ? '' : 's'} waiting for a compose tab ({waiting.map((j) => j.name || j.composition).join(', ')})
            <button className="btn-primary" onClick={() => onClaim(waiting[0].id)}>
              render now
            </button>
          </div>
        )
      )}
    </>
  )
}

// ---- ffmpeg on this node ----
//
// With ffmpeg installed, composer renders jobs itself (../ffmpeg.go) and
// nothing needs to be open; without it every job waits for a compose tab.
// The Render button always records in this tab, whichever is installed.

const RENDERER_LABELS: Record<'ffmpeg' | 'browser', string> = {
  ffmpeg: 'ffmpeg',
  browser: 'browser tab',
}

function RenderCapability({ cap, onCheck }: { cap: RenderCapabilityMsg | null; onCheck: () => void }) {
  if (!cap) return null
  const ok = !!cap.ffmpeg
  const title = ok
    ? `ffmpeg: ${cap.ffmpeg}\nffprobe: ${cap.ffprobe || 'not found -- clip lengths are read by decoding them'}\nRender jobs (the dtt composer tool, demos) are rendered here with ffmpeg; no tab needs to be open.`
    : `${cap.error || "ffmpeg isn't installed"}\nRender jobs wait for an open, visible compose tab to record them.`
  return (
    <div className="render-capability" title={title}>
      <span className={`conn-dot ${ok ? 'connected' : 'connecting'}`} />
      <span className="render-capability-label">{ok ? 'ffmpeg: render jobs need no tab' : 'no ffmpeg: render jobs need this tab'}</span>
      <button className="btn-icon" title="check again" onClick={onCheck}>
        ↻
      </button>
    </div>
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

  // Track the address the server reports until the user edits the fields.
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
          <input className="repr-footer-input" type="text" placeholder="host" value={hostInput} onChange={(e) => setHostInput(e.target.value)} />
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

// ---- Confirmation dialog ----

// ConfirmDialog is a modal asking before something destructive. Escape or a
// click outside it cancels; the cancel button has focus, so Enter doesn't
// confirm by accident.
function ConfirmDialog({
  title,
  confirmLabel,
  onConfirm,
  onCancel,
  children,
}: {
  title: string
  confirmLabel: string
  onConfirm: () => void
  onCancel: () => void
  children: ReactNode
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onCancel()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onCancel])

  return (
    <div className="dialog-backdrop" onClick={onCancel}>
      <div className="dialog" role="alertdialog" aria-modal="true" aria-label={title} onClick={(e) => e.stopPropagation()}>
        <div className="dialog-title">{title}</div>
        <div className="dialog-body">{children}</div>
        <div className="dialog-actions">
          <button className="btn-secondary" autoFocus onClick={onCancel}>
            Cancel
          </button>
          <button className="btn-danger" onClick={onConfirm}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}

// ---- Sidebar: compositions and examples ----

function CompositionList({
  compositions,
  examples,
  openId,
  onOpen,
  onNew,
  onFromExample,
  onDeleteAll,
}: {
  compositions: CompositionSummary[]
  examples: ExampleSummary[]
  openId: string | null
  onOpen: (id: string) => void
  onNew: () => void
  onFromExample: (id: string) => void
  onDeleteAll: () => void
}) {
  const [confirmDeleteAll, setConfirmDeleteAll] = useState(false)

  return (
    <div className="comp-list-wrap">
      <div className="comp-list-toolbar">
        <button className="btn-secondary" onClick={onNew}>
          New
        </button>
        <button
          className="btn-secondary"
          disabled={compositions.length === 0}
          title="delete every saved composition"
          onClick={() => setConfirmDeleteAll(true)}
        >
          Delete all
        </button>
      </div>
      {confirmDeleteAll && (
        <ConfirmDialog
          title="Delete all compositions?"
          confirmLabel={`Delete ${compositions.length}`}
          onConfirm={() => {
            setConfirmDeleteAll(false)
            onDeleteAll()
          }}
          onCancel={() => setConfirmDeleteAll(false)}
        >
          <p>
            This deletes all {compositions.length} saved composition{compositions.length === 1 ? '' : 's'} and can't be undone. Media
            already rendered into local-representative's files stays.
          </p>
          <ul className="dialog-list">
            {compositions.map((c) => (
              <li key={c.id}>{c.name}</li>
            ))}
          </ul>
        </ConfirmDialog>
      )}
      <div className="comp-list">
        {compositions.length === 0 && <div className="comp-list-empty">No compositions yet -- start from an example below.</div>}
        {compositions.map((c) => (
          <div key={c.id} className={`comp-row${c.id === openId ? ' selected' : ''}`} onClick={() => onOpen(c.id)}>
            <div className="comp-row-name">{c.name}</div>
            <div className="comp-row-meta">
              {c.clips} clip{c.clips === 1 ? '' : 's'}
              {c.renders > 0 && ` · ${c.renders} render${c.renders === 1 ? '' : 's'}`}
            </div>
          </div>
        ))}
      </div>
      {examples.length > 0 && (
        <div className="comp-examples">
          <div className="section-label">examples</div>
          {examples.map((e) => (
            <div key={e.id} className="comp-example">
              <div className="comp-example-text">
                <div className="comp-row-name">{e.name}</div>
                <div className="comp-row-meta">{e.description}</div>
              </div>
              <button className="btn-secondary" title={`save a copy of ${e.id} to edit`} onClick={() => onFromExample(e.id)}>
                + use
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// ---- Editor ----

const VIDEO_KINDS: ClipKind[] = ['title', 'media']
const AUDIO_KINDS: ClipKind[] = ['media', 'tone', 'silence']

// newClip is what an "add" button appends.
function newClip(kind: ClipKind): Clip {
  switch (kind) {
    case 'title':
      return { kind, duration: 2, text: 'Title', color: '#ffffff', background: '#000000' }
    case 'tone':
      return { kind, duration: 0.5, frequency: 440 }
    case 'silence':
      return { kind, duration: 1 }
    default:
      return { kind }
  }
}

// usableOn says whether a source can go on a track (see ../compositions.go's
// Clip.validate).
function usableOn(track: Track, s: Source): boolean {
  return track === 'video' ? s.media !== 'audio' : s.media !== 'image'
}

const ORIGIN_LABELS: Record<string, string> = { ianar: 'IANAR', polly: 'Polly', composer: 'composer' }

function formatSeconds(s: number): string {
  return `${Math.round(s * 10) / 10}s`
}

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1 << 20) return `${(n / 1024).toFixed(1)} KiB`
  return `${(n / (1 << 20)).toFixed(1)} MiB`
}

function ClipRow({
  track,
  clip,
  index,
  count,
  sources,
  onChange,
  onMove,
  onRemove,
}: {
  track: Track
  clip: Clip
  index: number
  count: number
  sources: Source[]
  onChange: (c: Clip) => void
  onMove: (delta: number) => void
  onRemove: () => void
}) {
  const kinds = track === 'video' ? VIDEO_KINDS : AUDIO_KINDS
  const usable = sources.filter((s) => usableOn(track, s))
  const isImage = clip.kind === 'media' && mediaKind(clip.file_name) === 'image'
  const durationOptional = clip.kind === 'media' && !isImage
  return (
    <div className={`clip-row clip-${clip.kind}`}>
      <span className="clip-index">{index + 1}</span>
      <select value={clip.kind} onChange={(e) => onChange(newClip(e.target.value as ClipKind))}>
        {kinds.map((k) => (
          <option key={k} value={k}>
            {k}
          </option>
        ))}
      </select>
      {clip.kind === 'title' && (
        <>
          <input
            className="clip-text"
            type="text"
            value={clip.text ?? ''}
            placeholder="text"
            onChange={(e) => onChange({ ...clip, text: e.target.value })}
          />
          <input type="color" title="text color" value={clip.color || '#ffffff'} onChange={(e) => onChange({ ...clip, color: e.target.value })} />
          <input
            type="color"
            title="background"
            value={clip.background || '#000000'}
            onChange={(e) => onChange({ ...clip, background: e.target.value })}
          />
        </>
      )}
      {clip.kind === 'media' && (
        <select
          className="clip-source"
          value={clip.file_id ?? ''}
          onChange={(e) => {
            const s = usable.find((u) => u.id === e.target.value)
            onChange({ ...clip, file_id: s?.id, file_name: s?.name })
          }}
        >
          <option value="">choose a file…</option>
          {clip.file_id && !usable.some((u) => u.id === clip.file_id) && (
            <option value={clip.file_id}>{clip.file_name ?? clip.file_id} (not in files)</option>
          )}
          {usable.map((s) => (
            <option key={s.id} value={s.id}>
              {s.origin ? `[${ORIGIN_LABELS[s.origin]}] ` : ''}
              {s.name}
            </option>
          ))}
        </select>
      )}
      {clip.kind === 'tone' && (
        <label className="clip-field">
          <input
            type="number"
            min={20}
            max={20000}
            step={10}
            value={clip.frequency ?? 440}
            onChange={(e) => onChange({ ...clip, frequency: Number(e.target.value) })}
          />
          Hz
        </label>
      )}
      <label className="clip-field" title={durationOptional ? 'leave empty to use the whole file' : undefined}>
        <input
          type="number"
          min={0}
          max={600}
          step={0.1}
          value={clip.duration ?? ''}
          placeholder={durationOptional ? 'full' : ''}
          onChange={(e) => onChange({ ...clip, duration: e.target.value === '' ? undefined : Number(e.target.value) })}
        />
        s
      </label>
      <span className="clip-actions">
        <button className="btn-icon" title="move earlier" disabled={index === 0} onClick={() => onMove(-1)}>
          ↑
        </button>
        <button className="btn-icon" title="move later" disabled={index === count - 1} onClick={() => onMove(1)}>
          ↓
        </button>
        <button className="btn-icon" title="remove" onClick={onRemove}>
          ✕
        </button>
      </span>
    </div>
  )
}

// TimelineStrip draws a track's clips as blocks proportional to their known
// durations (a media clip at its own length shows as a narrow block).
function TimelineStrip({ clips, total }: { clips: Clip[]; total: number }) {
  return (
    <div className="timeline-strip">
      {clips.map((c, i) => (
        <span
          key={i}
          className={`timeline-block timeline-${c.kind}`}
          style={{ flexGrow: c.duration || total * 0.05 || 1, background: c.kind === 'title' ? c.background : undefined }}
          title={`${c.kind}${c.duration ? ` · ${formatSeconds(c.duration)}` : ' · full length'}`}
        >
          {c.kind === 'title' ? c.text : c.kind === 'media' ? c.file_name : c.kind === 'tone' ? `${c.frequency ?? 440}Hz` : ''}
        </span>
      ))}
      {clips.length === 0 && <span className="timeline-empty">empty</span>}
    </div>
  )
}

function TrackEditor({
  track,
  clips,
  sources,
  onChange,
}: {
  track: Track
  clips: Clip[]
  sources: Source[]
  onChange: (clips: Clip[]) => void
}) {
  const kinds = track === 'video' ? VIDEO_KINDS : AUDIO_KINDS
  const update = (i: number, c: Clip) => onChange(clips.map((old, j) => (j === i ? c : old)))
  const move = (i: number, delta: number) => {
    const next = [...clips]
    const [c] = next.splice(i, 1)
    next.splice(i + delta, 0, c)
    onChange(next)
  }
  return (
    <div className="track">
      <div className="track-header">
        <span className="section-label">{track} track</span>
        <span className="track-length">{formatSeconds(trackDuration(clips))}</span>
        <span className="track-add">
          {kinds.map((k) => (
            <button key={k} className="btn-secondary" onClick={() => onChange([...clips, newClip(k)])}>
              + {k}
            </button>
          ))}
        </span>
      </div>
      <TimelineStrip clips={clips} total={trackDuration(clips)} />
      {clips.map((c, i) => (
        <ClipRow
          key={i}
          track={track}
          clip={c}
          index={i}
          count={clips.length}
          sources={sources}
          onChange={(nc) => update(i, nc)}
          onMove={(d) => move(i, d)}
          onRemove={() => onChange(clips.filter((_, j) => j !== i))}
        />
      ))}
    </div>
  )
}

// PlayerPanel previews the draft on a canvas, or renders it -- recording it
// in the browser and handing the WebM to the server -- once it's saved.
function PlayerPanel({
  draft,
  saved,
  dirty,
  reprConnected,
  onNotice,
}: {
  draft: Composition
  saved: boolean
  dirty: boolean
  reprConnected: boolean
  onNotice: (n: Notice) => void
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const playbackRef = useRef<Playback | null>(null)
  const [mode, setMode] = useState<'idle' | 'loading' | 'preview' | 'render' | 'upload'>('idle')
  const [progress, setProgress] = useState<{ t: number; d: number } | null>(null)

  // Stop playing when the editor goes away.
  useEffect(() => () => playbackRef.current?.stop(), [])

  const run = useCallback(
    async (record: boolean) => {
      const canvas = canvasRef.current
      if (!canvas) return
      onNotice(null)
      setMode('loading')
      setProgress(null)
      try {
        const pb = await play(draft, { canvas, record, onProgress: (t, d) => setProgress({ t, d }) })
        playbackRef.current = pb
        setMode(record ? 'render' : 'preview')
        const blob = await pb.done
        playbackRef.current = null
        if (record && blob) {
          setMode('upload')
          await uploadRender(draft.id, blob)
        }
        setMode('idle')
      } catch (err) {
        playbackRef.current = null
        setMode('idle')
        onNotice({ kind: 'error', message: `${record ? 'render' : 'preview'}: ${err instanceof Error ? err.message : String(err)}` })
      }
    },
    [draft, onNotice],
  )

  const busy = mode !== 'idle'
  const status =
    mode === 'loading'
      ? 'loading sources…'
      : mode === 'upload'
        ? 'saving the render to local-representative…'
        : progress && busy
          ? `${mode === 'render' ? 'recording' : 'playing'} ${formatSeconds(progress.t)} / ${formatSeconds(progress.d)}`
          : ''
  const renderBlocked = !saved
    ? 'save the composition first'
    : dirty
      ? 'save your changes first -- the render is of the saved composition'
      : !reprConnected
        ? 'not connected to local-representative, where the render is saved'
        : ''

  return (
    <div className="player">
      <canvas ref={canvasRef} className="player-canvas" width={draft.width} height={draft.height} />
      <div className="player-controls">
        <button className="btn-secondary" disabled={busy} onClick={() => run(false)}>
          ▶ Preview
        </button>
        <button className="btn-primary" disabled={busy || !!renderBlocked} title={renderBlocked || 'record this composition and save it as a WebM in local-representative\'s files'} onClick={() => run(true)}>
          ● Render
        </button>
        <button className="btn-secondary" disabled={mode !== 'preview' && mode !== 'render'} onClick={() => playbackRef.current?.stop()}>
          ■ Stop
        </button>
        <span className="player-status">{status}</span>
      </div>
      {mode === 'render' && <div className="player-hint">Keep this tab visible while it records -- a hidden tab stops drawing frames.</div>}
    </div>
  )
}

function RenderList({ comp }: { comp: Composition }) {
  const [playing, setPlaying] = useState<string | null>(null)
  if (!comp.renders?.length) return null
  return (
    <div className="renders">
      <div className="section-label">renders (in local-representative's files)</div>
      {comp.renders.map((r) => (
        <div key={r.file_id} className="render-row">
          <button className="btn-icon" title="play here" onClick={() => setPlaying(playing === r.file_id ? null : r.file_id)}>
            {playing === r.file_id ? '■' : '▶'}
          </button>
          <span className="render-name">{r.name}</span>
          {r.renderer && (
            <span className={`render-by render-by-${r.renderer}`} title={`rendered by ${RENDERER_LABELS[r.renderer]}`}>
              {RENDERER_LABELS[r.renderer]}
            </span>
          )}
          <span className="render-meta">
            {formatSize(r.size)} · {new Date(r.at * 1000).toLocaleString()}
          </span>
        </div>
      ))}
      {playing && <video className="render-video" src={sourceUrl(playing)} controls autoPlay />}
    </div>
  )
}

function SourcesPanel({ sources, onRefresh }: { sources: SourcesMsg; onRefresh: () => void }) {
  return (
    <div className="sources">
      <div className="track-header">
        <span className="section-label">sources (local-representative's files)</span>
        <button className="btn-secondary" onClick={onRefresh}>
          Refresh
        </button>
      </div>
      {sources.error ? (
        <div className="sources-empty">{sources.error}</div>
      ) : sources.sources.length === 0 ? (
        <div className="sources-empty">
          No audio, video or image files yet. Save an IANAR recording, tick "save" beside The Conversationalist's Polly button, or upload
          one in local-representative's files tab.
        </div>
      ) : (
        sources.sources.map((s) => (
          <div key={s.id} className="source-row">
            <span className={`source-media source-media-${s.media}`}>{s.media}</span>
            {s.origin && <span className={`source-origin source-origin-${s.origin}`}>{ORIGIN_LABELS[s.origin]}</span>}
            <span className="source-name">{s.name}</span>
            <span className="render-meta">{formatSize(s.size)}</span>
          </div>
        ))
      )}
    </div>
  )
}

function Editor({
  comp,
  sources,
  reprConnected,
  onSave,
  onDelete,
  onClose,
  onRefreshSources,
  onNotice,
}: {
  comp: Composition
  sources: SourcesMsg
  reprConnected: boolean
  onSave: (c: Composition) => void
  onDelete: (id: string) => void
  onClose: () => void
  onRefreshSources: () => void
  onNotice: (n: Notice) => void
}) {
  const [draft, setDraft] = useState<Composition>(comp)
  const [confirmDelete, setConfirmDelete] = useState(false)

  // Take the saved copy whenever the server sends a new one (a save, a
  // render being recorded, or another tab's edit).
  useEffect(() => {
    setDraft(comp)
    setConfirmDelete(false)
  }, [comp])

  const dirty = JSON.stringify({ ...draft, renders: undefined, updated_at: undefined }) !== JSON.stringify({ ...comp, renders: undefined, updated_at: undefined })

  return (
    <div className="editor">
      <div className="editor-header">
        <input className="editor-name" value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
        <label className="clip-field">
          <input type="number" min={16} max={3840} value={draft.width} onChange={(e) => setDraft({ ...draft, width: Number(e.target.value) })} />×
          <input type="number" min={16} max={3840} value={draft.height} onChange={(e) => setDraft({ ...draft, height: Number(e.target.value) })} />
        </label>
        <label className="clip-field" title="audio: render only the audio track, into an audio-only WebM (.weba) -- an audio composition has no video clips">
          <select
            value={draft.output === 'audio' ? 'audio' : 'video'}
            onChange={(e) => setDraft({ ...draft, output: e.target.value === 'audio' ? 'audio' : undefined })}
          >
            <option value="video">video</option>
            <option value="audio">audio only</option>
          </select>
        </label>
        <span className="editor-actions">
          <button className="btn-primary" disabled={!dirty && !!comp.id} onClick={() => onSave(draft)}>
            {dirty || !comp.id ? 'Save' : 'Saved'}
          </button>
          {comp.id &&
            (confirmDelete ? (
              <>
                <button className="btn-danger" onClick={() => onDelete(comp.id)}>
                  Delete {comp.name}?
                </button>
                <button className="btn-secondary" onClick={() => setConfirmDelete(false)}>
                  Keep
                </button>
              </>
            ) : (
              <button className="btn-secondary" onClick={() => setConfirmDelete(true)}>
                Delete
              </button>
            ))}
          <button className="btn-secondary" title="close" onClick={onClose}>
            ✕
          </button>
        </span>
      </div>
      <div className="editor-body">
        <PlayerPanel draft={draft} saved={!!comp.id} dirty={dirty} reprConnected={reprConnected} onNotice={onNotice} />
        <TrackEditor track="video" clips={draft.video} sources={sources.sources} onChange={(video) => setDraft({ ...draft, video })} />
        <TrackEditor track="audio" clips={draft.audio} sources={sources.sources} onChange={(audio) => setDraft({ ...draft, audio })} />
        <RenderList comp={comp} />
        <SourcesPanel sources={sources} onRefresh={onRefreshSources} />
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
    compositions,
    examples,
    sources,
    open,
    notice,
    setNotice,
    openComposition,
    newDraft,
    closeComposition,
    saveComposition,
    newFromExample,
    deleteComposition,
    deleteAllCompositions,
    refreshSources,
    waitingJobs,
    assignedJob,
    claimRenderJob,
    failRenderJob,
    finishRenderJob,
    renderCapability,
    checkRenderCapability,
  } = useComposerWS()

  return (
    <div className={`app${devMode ? ' app-dev-mode' : ''}`}>
      {modeMismatch && (
        <div className="mode-mismatch-banner">
          ⚠ dev/ops mode mismatch with local-representative ({modeMismatch.peer_mode}): only health information is exchanged until this is
          resolved. See docs/DevMode.md.
        </div>
      )}
      <div className="sidebar-wrap">
        <div className="sidebar">
          <div className="sidebar-header">
            <h1>Composer</h1>
            {version && <span className="app-version-tag">v{version.replace(/^v/, '')}</span>}
          </div>
          <CompositionList
            compositions={compositions}
            examples={examples}
            openId={open ? open.id : null}
            onOpen={openComposition}
            onNew={newDraft}
            onFromExample={newFromExample}
            onDeleteAll={deleteAllCompositions}
          />
          <RenderCapability cap={renderCapability} onCheck={checkRenderCapability} />
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
        <RenderJobs
          waiting={waitingJobs}
          assigned={assignedJob}
          onClaim={claimRenderJob}
          onFail={failRenderJob}
          onDone={finishRenderJob}
          onNotice={setNotice}
        />
        {notice && (
          <div className={`notice notice-${notice.kind}`} onClick={() => setNotice(null)}>
            {notice.message}
          </div>
        )}
        {open ? (
          <Editor
            comp={open}
            sources={sources}
            reprConnected={reprStatus === 'connected'}
            onSave={saveComposition}
            onDelete={deleteComposition}
            onClose={closeComposition}
            onRefreshSources={refreshSources}
            onNotice={setNotice}
          />
        ) : (
          <div className="empty-state">
            <div>
              <span className={`conn-dot ${connected ? 'connected' : 'disconnected'}`} />
              Open a composition on the left, or start one from the hello-world example, to compose IANAR recordings and Polly clips into
              new media.
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
