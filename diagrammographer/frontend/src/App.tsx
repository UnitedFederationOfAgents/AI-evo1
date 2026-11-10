import { useCallback, useEffect, useRef, useState } from 'react'
import { convert, getDiagram, listDiagrams } from './api'
import { DiagramSVG } from './Diagram'
import { exportD2, exportDTT, exportGIF, exportPNG, exportVideo, exportWeb, FRAME_MS } from './exports'
import type { DiagramSummary, DiagramView, ModeMismatchMsg, ReprStatus, ReprStatusMsg, SelfInfoMsg } from './types'

// ---- WebSocket hook ----
//
// Wires up the representable connect/disconnect widget, dev-mode banner and
// version-mismatch reload check shared by every sub-app in this repo. The
// diagrams go over the HTTP API instead (see api.ts).

function useDiagrammographerWS() {
  const [connected, setConnected] = useState(false)
  const [reprStatus, setReprStatus] = useState<ReprStatus>('disconnected')
  const [reprHost, setReprHost] = useState('')
  const [reprPort, setReprPort] = useState('')
  const [reprAutoConnect, setReprAutoConnect] = useState(false)
  const [devMode, setDevMode] = useState(false)
  const [version, setVersion] = useState('')
  const [modeMismatch, setModeMismatch] = useState<ModeMismatchMsg | null>(null)
  const [error, setError] = useState<string | null>(null)

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

  useEffect(() => {
    // Derive the WebSocket URL from the path this document was served
    // under, so it also works beneath the plug-in route (/apps/dg/ via
    // local-representative, /host/<id>/apps/dg/ via agent-coordinator).
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
          } else if (msg.type === 'error') {
            setError(String(msg.payload))
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
    error,
    setError,
    connectRepr,
    disconnectRepr,
    setAutoConnectRepr,
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
    error,
    setError,
    connectRepr,
    disconnectRepr,
    setAutoConnectRepr,
  } = useDiagrammographerWS()
  const { list, selected, setSelected, open, load, updateOpen } = useDiagrams(setError)

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
            <h1>Diagrammographer</h1>
            {version && <span className="app-version-tag">v{version.replace(/^v/, '')}</span>}
          </div>
          <div className="sidebar-body">
            <div className="section-label">diagrams</div>
            {list.length === 0 && <div className="sidebar-empty">none yet</div>}
            {list.map(item => {
              const od = open[item.name]
              const steps = od ? od.view.diagram.steps.length : item.steps
              return (
                <button
                  key={item.name}
                  className={`diagram-item${selected === item.name ? ' diagram-item-active' : ''}`}
                  onClick={() => setSelected(item.name)}
                >
                  <span className="diagram-item-title">
                    {od?.view.diagram.title ?? item.title}
                    {od?.edited && <span className="diagram-item-edited" title="edited in this browser">•</span>}
                  </span>
                  <span className="diagram-item-meta">
                    {item.name} · {steps > 0 ? `dynamic, ${steps} steps` : 'static'}
                  </span>
                </button>
              )
            })}
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
        {error && (
          <div className="notice notice-error" onClick={() => setError(null)}>
            {error}
          </div>
        )}
        {selected && open[selected] ? (
          <DiagramPane
            key={selected}
            od={open[selected]}
            onChange={update => updateOpen(selected, update)}
            onReset={() => load(selected)}
            setError={setError}
          />
        ) : (
          <div className="empty-state">
            <div>
              <span className={`conn-dot ${connected ? 'connected' : 'disconnected'}`} />
              {list.length === 0 ? 'Loading diagrams…' : 'Pick a diagram on the left.'}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

// ---- Diagrams ----

// OpenDiagram is a diagram as this browser has it: the server's view with
// any edits folded in, and the DTT view's text. Edits stay in the browser.
interface OpenDiagram {
  view: DiagramView
  draft: string // the DTT view's text: differs from view.dtt until applied
  draftError: string | null
  edited: boolean // changed since it was loaded
}

type OpenUpdate = (od: OpenDiagram) => OpenDiagram

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

// useDiagrams loads the sidebar's list and each diagram the first time it's
// picked.
function useDiagrams(setError: (e: string | null) => void) {
  const [list, setList] = useState<DiagramSummary[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [open, setOpen] = useState<Record<string, OpenDiagram>>({})
  const requested = useRef(new Set<string>())

  useEffect(() => {
    listDiagrams()
      .then(l => {
        setList(l)
        setSelected(s => s ?? l[0]?.name ?? null)
      })
      .catch(e => setError(errText(e)))
  }, [setError])

  const load = useCallback(
    (name: string) => {
      requested.current.add(name)
      getDiagram(name)
        .then(view => setOpen(o => ({ ...o, [name]: { view, draft: view.dtt, draftError: null, edited: false } })))
        .catch(e => {
          requested.current.delete(name)
          setError(errText(e))
        })
    },
    [setError],
  )

  useEffect(() => {
    if (selected && !requested.current.has(selected)) load(selected)
  }, [selected, load])

  const updateOpen = useCallback((name: string, update: OpenUpdate) => {
    setOpen(o => (o[name] ? { ...o, [name]: update(o[name]) } : o))
  }, [])

  return { list, selected, setSelected, open, load, updateOpen }
}

type PaneTab = 'canvas' | 'dtt'
type ExportKind = 'png' | 'gif' | 'video' | 'web' | 'd2' | 'dtt'

const EXPORTS: { kind: ExportKind; label: string; title: string }[] = [
  { kind: 'png', label: 'PNG', title: 'the step on the canvas, as an image' },
  { kind: 'gif', label: 'GIF', title: 'every step, as an animated image' },
  { kind: 'video', label: 'video', title: 'every step as the canvas plays it, moving dots and all, recorded to a WebM video (takes as long as it plays)' },
  { kind: 'web', label: 'web', title: 'a standalone HTML page, presented as on the canvas' },
  { kind: 'd2', label: 'D2', title: 'diagram-as-code (d2lang.com); D2 lays it out itself' },
  { kind: 'dtt', label: 'DTT', title: "the diagram's DTT document" },
]

interface DiagramPaneProps {
  od: OpenDiagram
  onChange: (update: OpenUpdate) => void
  onReset: () => void
  setError: (e: string | null) => void
}

// DiagramPane is the right-hand pane: the canvas (the diagram as the web
// export presents it, with nodes to drag) or its DTT document, with the step
// and export controls above.
function DiagramPane({ od, onChange, onReset, setError }: DiagramPaneProps) {
  const { diagram } = od.view
  // latest is the diagram with every drag so far, including any not yet
  // rendered, for moveEnd to send.
  const latest = useRef(diagram)
  latest.current = diagram
  const [tab, setTab] = useState<PaneTab>('canvas')
  const [step, setStep] = useState(-1) // -1: the overview, every node and edge lit
  const [playing, setPlaying] = useState(false)
  const [busy, setBusy] = useState<ExportKind | null>(null)
  const n = diagram.steps.length
  const dirty = od.draft !== od.view.dtt

  // Step through the overview and then each step, wrapping round.
  const advance = useCallback((by: number) => setStep(s => ((s + 1 + by + n + 1) % (n + 1)) - 1), [n])

  useEffect(() => {
    if (step >= n) setStep(-1)
    if (n === 0) setPlaying(false)
  }, [n, step])

  // Playing loops through the steps, as the GIF and video do, each held for
  // FRAME_MS. progress (0 to 1) is how far through the step the canvas is,
  // for the dots travelling along the lit edges.
  const [progress, setProgress] = useState<number | undefined>(undefined)
  useEffect(() => {
    if (!playing || n === 0) {
      setProgress(undefined)
      return
    }
    let start = performance.now()
    let frame = 0
    const tick = (now: number) => {
      if (now - start >= FRAME_MS) {
        setStep(s => (s + 1) % n)
        start = now
      }
      setProgress((now - start) / FRAME_MS)
      frame = requestAnimationFrame(tick)
    }
    frame = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(frame)
  }, [playing, n])

  // Play from the overview starts at the first step.
  const togglePlay = () => {
    if (!playing && step < 0) setStep(0)
    setPlaying(p => !p)
  }

  const moveNode = (id: string, x: number, y: number) => {
    const d = latest.current
    const moved = { ...d, nodes: d.nodes.map(nd => (nd.id === id ? { ...nd, x, y } : nd)) }
    latest.current = moved
    onChange(o => ({ ...o, edited: true, view: { ...o.view, diagram: moved } }))
  }

  // Once a node is let go, have the server write the moved diagram's DTT
  // document (and D2).
  const moveEnd = () => {
    convert({ diagram: latest.current })
      .then(view => onChange(o => ({ ...o, view, draft: view.dtt, draftError: null })))
      .catch(e => setError(errText(e)))
  }

  const applyDraft = () => {
    convert({ dtt: od.draft })
      .then(view => onChange(o => ({ ...o, view, draft: view.dtt, draftError: null, edited: true })))
      .catch(e => onChange(o => ({ ...o, draftError: errText(e) })))
  }

  const runExport = async (kind: ExportKind) => {
    setBusy(kind)
    try {
      switch (kind) {
        case 'png':
          await exportPNG(diagram, step)
          break
        case 'gif':
          await exportGIF(diagram)
          break
        case 'video':
          await exportVideo(diagram)
          break
        case 'web':
          exportWeb(diagram)
          break
        case 'd2':
          exportD2(diagram, od.view.d2)
          break
        case 'dtt':
          exportDTT(diagram, od.view.dtt)
          break
      }
    } catch (e) {
      setError(`${kind} export: ${errText(e)}`)
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="pane">
      <div className="pane-toolbar">
        <div className="pane-tabs">
          <button className={`pane-tab${tab === 'canvas' ? ' pane-tab-active' : ''}`} onClick={() => setTab('canvas')}>
            canvas
          </button>
          <button className={`pane-tab${tab === 'dtt' ? ' pane-tab-active' : ''}`} onClick={() => setTab('dtt')}>
            DTT{dirty && ' •'}
          </button>
        </div>
        {n > 0 && (
          <div className="pane-group">
            <button className="btn-secondary" onClick={() => advance(-1)} title="previous step">
              ◀
            </button>
            <span className="step-label">{step < 0 ? `overview · ${n} steps` : `step ${step + 1}/${n}`}</span>
            <button className="btn-secondary" onClick={() => advance(1)} title="next step">
              ▶
            </button>
            <button className="btn-secondary" onClick={togglePlay}>
              {playing ? 'pause' : 'play'}
            </button>
          </div>
        )}
        <div className="pane-spacer" />
        <div className="pane-group">
          <span className="pane-group-label">export</span>
          {EXPORTS.map(x => (
            <button key={x.kind} className="btn-secondary" title={x.title} disabled={busy !== null || dirty} onClick={() => runExport(x.kind)}>
              {busy === x.kind ? '…' : x.label}
            </button>
          ))}
        </div>
        {od.edited && (
          <button className="btn-secondary" title="drop this browser's edits and reload the diagram" onClick={onReset}>
            reset
          </button>
        )}
      </div>

      {tab === 'canvas' ? (
        <div className="canvas-area">
          {dirty && <div className="canvas-note">The DTT view has changes that aren't applied: apply or revert them to edit here.</div>}
          <div className="canvas-kind">
            {n > 0 ? (
              <>
                <span className="kind-badge kind-badge-dynamic">dynamic</span>
                <span className="canvas-kind-text">{playing && step >= 0 ? `playing step ${step + 1} of ${n}` : `${n} steps`}</span>
                <button className="btn-play" onClick={togglePlay}>
                  {playing ? '❚❚ pause' : '▶ play'}
                </button>
              </>
            ) : (
              <>
                <span className="kind-badge">static</span>
                <span className="canvas-kind-text">no steps to play</span>
              </>
            )}
          </div>
          <div className="canvas-frame">
            <DiagramSVG
              diagram={diagram}
              step={step}
              progress={progress}
              onNodeMove={dirty ? undefined : moveNode}
              onNodeMoveEnd={dirty ? undefined : moveEnd}
            />
          </div>
          {diagram.description && <div className="canvas-description">{diagram.description}</div>}
        </div>
      ) : (
        <div className="dtt-area">
          <div className="dtt-toolbar">
            <button className="btn-secondary" disabled={!dirty} onClick={applyDraft}>
              apply
            </button>
            <button className="btn-secondary" disabled={!dirty} onClick={() => onChange(o => ({ ...o, draft: o.view.dtt, draftError: null }))}>
              revert
            </button>
            <span className="dtt-hint">The diagram as a DTT document (diagrammographer.diagrams). Apply redraws the canvas.</span>
          </div>
          {od.draftError && <div className="notice notice-error">{od.draftError}</div>}
          <textarea
            className="dtt-text"
            spellCheck={false}
            value={od.draft}
            onChange={e => {
              const draft = e.target.value
              onChange(o => ({ ...o, draft }))
            }}
          />
        </div>
      )}
    </div>
  )
}
