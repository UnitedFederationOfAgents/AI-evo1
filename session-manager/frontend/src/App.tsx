import { useCallback, useEffect, useRef, useState } from 'react'
import type {
  ArchiveResultMsg,
  ModeMismatchMsg,
  ProcessedView,
  ReprStatus,
  ReprStatusMsg,
  SelfInfoMsg,
  SessionInfo,
  SessionsMsg,
  SessionSummary,
  SessionView,
} from './types'

// ---- WebSocket hook ----
//
// Wires up the representable connect/disconnect widget, dev-mode banner,
// and version-mismatch reload check shared by every sub-app in this repo
// (see condocs/InitialShellsSessionManagerAndTheConversationalist.md), plus
// (Revision I) Session Manager's first domain functionality: parity with
// federation-command's "ufa session" sub-menu (list/new/set/describe/
// rename/archive, see ../sessions.go) and a "view" mode that renders a
// session's session.jsonl as a readable transcript.

type Notice = { kind: 'error' | 'success'; message: string } | null

function useSessionManagerWS() {
  const [connected, setConnected] = useState(false)
  const [reprStatus, setReprStatus] = useState<ReprStatus>('disconnected')
  const [reprHost, setReprHost] = useState('')
  const [reprPort, setReprPort] = useState('')
  const [reprAutoConnect, setReprAutoConnect] = useState(false)
  const [devMode, setDevMode] = useState(false)
  const [version, setVersion] = useState('')
  const [modeMismatch, setModeMismatch] = useState<ModeMismatchMsg | null>(null)

  const [sessions, setSessions] = useState<SessionSummary[]>([])
  const [currentSessionId, setCurrentSessionId] = useState('')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [sessionInfo, setSessionInfo] = useState<SessionInfo | null>(null)
  const [sessionView, setSessionView] = useState<SessionView | null>(null)
  const [notice, setNotice] = useState<Notice>(null)

  // The transcript turn currently expanded into its full -processed file,
  // or null at the transcript level. expandedRef lets the message handler
  // drop a "processed-view" reply for a turn that's no longer expanded.
  const [expanded, setExpanded] = useState<{ id: string; record: string } | null>(null)
  const [processedView, setProcessedView] = useState<ProcessedView | null>(null)
  const expandedRef = useRef<{ id: string; record: string } | null>(null)

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

  // expandEntry scopes the transcript down into one turn's full -processed
  // file; collapseEntry goes back "up" to the transcript, the way
  // condoccer's ↑ buttons step back out of a step or substep.
  const expandEntry = useCallback(
    (id: string, record: string) => {
      const target = { id, record }
      expandedRef.current = target
      setExpanded(target)
      setProcessedView(null)
      send('view-processed', target)
    },
    [send],
  )

  const collapseEntry = useCallback(() => {
    expandedRef.current = null
    setExpanded(null)
    setProcessedView(null)
  }, [])

  // selectSession picks a session for the detail pane and asks the server
  // for both its describe-parity info and its readable transcript.
  const selectSession = useCallback(
    (id: string) => {
      setSelectedId(id)
      setSessionInfo(null)
      setSessionView(null)
      collapseEntry()
      send('describe-session', { id })
      send('view-session', { id })
    },
    [send, collapseEntry],
  )

  const newSession = useCallback(
    (name: string) => {
      send('new-session', { name })
    },
    [send],
  )

  const setCurrentSession = useCallback(
    (id: string) => {
      send('set-session', { id })
    },
    [send],
  )

  const renameSession = useCallback(
    (id: string, name: string) => {
      send('rename-session', { id, name })
    },
    [send],
  )

  const archiveSessions = useCallback(() => {
    send('archive-sessions', {})
  }, [send])

  // Auto-select whichever session becomes current (e.g. right after "New")
  // as long as nothing is already selected -- never hijacks an in-progress
  // selection just because another tab changed the current session.
  useEffect(() => {
    if (currentSessionId && selectedId === null) {
      selectSession(currentSessionId)
    }
  }, [currentSessionId, selectedId, selectSession])

  useEffect(() => {
    // Derive the WebSocket URL from the path this document was served
    // under, so it also works when reverse-proxied beneath a prefix
    // (/sessions/ via local-representative, /host/<id>/sessions/ via
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
          } else if (msg.type === 'sessions') {
            const p = msg.payload as SessionsMsg
            setSessions(p.sessions ?? [])
            setCurrentSessionId(p.current ?? '')
          } else if (msg.type === 'session-info') {
            setSessionInfo(msg.payload as SessionInfo)
          } else if (msg.type === 'session-view') {
            setSessionView(msg.payload as SessionView)
          } else if (msg.type === 'processed-view') {
            const p = msg.payload as ProcessedView
            const cur = expandedRef.current
            if (cur && cur.id === p.id && cur.record === p.record) {
              setProcessedView(p)
            }
          } else if (msg.type === 'archive-result') {
            const p = msg.payload as ArchiveResultMsg
            if (p.error) {
              setNotice({ kind: 'error', message: `archive: ${p.error}` })
            } else if (p.count > 0) {
              setNotice({ kind: 'success', message: `archived ${p.count} session(s) to ${p.path}` })
              setSelectedId(null)
              setSessionInfo(null)
              setSessionView(null)
              expandedRef.current = null
              setExpanded(null)
              setProcessedView(null)
            } else {
              setNotice({ kind: 'success', message: 'no sessions to archive' })
            }
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
    sessions,
    currentSessionId,
    selectedId,
    sessionInfo,
    sessionView,
    expanded,
    processedView,
    notice,
    selectSession,
    expandEntry,
    collapseEntry,
    newSession,
    setCurrentSession,
    renameSession,
    archiveSessions,
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

// ---- Session list ("ufa session list" parity, plus new/archive) ----

interface SessionListProps {
  sessions: SessionSummary[]
  currentId: string
  selectedId: string | null
  onSelect: (id: string) => void
  onNew: (name: string) => void
  onArchive: () => void
}

function SessionList({ sessions, currentId, selectedId, onSelect, onNew, onArchive }: SessionListProps) {
  const [newName, setNewName] = useState('')
  const [confirmArchive, setConfirmArchive] = useState(false)

  const submitNew = () => {
    onNew(newName.trim())
    setNewName('')
  }

  return (
    <div className="session-list-wrap">
      <div className="session-list-toolbar">
        <input
          className="repr-footer-input"
          type="text"
          placeholder="new session name…"
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && submitNew()}
        />
        <button className="btn-secondary" onClick={submitNew} title="ufa session new">
          New
        </button>
      </div>
      <div className="session-list">
        {sessions.length === 0 ? (
          <div className="session-list-empty">no sessions yet</div>
        ) : (
          sessions.map((s) => (
            <button
              key={s.id}
              className={`session-row${s.id === selectedId ? ' selected' : ''}${s.remote ? ' session-row-remote' : ''}`}
              onClick={() => onSelect(s.id)}
              title={s.remote ? `${s.id} — discovered on ${s.host}, not yet pulled locally; selecting it will pull it` : s.id}
            >
              <span className={`session-row-current-dot${s.id === currentId ? ' is-current' : ''}`} title={s.id === currentId ? 'current session' : ''} />
              <span className="session-row-name">{s.name || s.id}</span>
              {s.remote ? (
                <span className="session-row-meta session-row-remote-badge">remote · {s.host}</span>
              ) : (
                <span className="session-row-meta">{s.file_count}</span>
              )}
            </button>
          ))
        )}
      </div>
      <div className="session-list-toolbar session-list-toolbar-archive">
        {confirmArchive ? (
          <>
            <span className="session-archive-confirm-label">archive all sessions?</span>
            <button
              className="btn-secondary btn-danger"
              onClick={() => {
                onArchive()
                setConfirmArchive(false)
              }}
            >
              Confirm
            </button>
            <button className="btn-secondary" onClick={() => setConfirmArchive(false)}>
              Cancel
            </button>
          </>
        ) : (
          <button
            className="btn-secondary"
            disabled={sessions.length === 0}
            onClick={() => setConfirmArchive(true)}
            title="ufa session archive"
          >
            Archive All
          </button>
        )}
      </div>
    </div>
  )
}

// ---- Session detail pane (describe + rename + set-current + view) ----

type DetailTab = 'details' | 'transcript'

interface SessionDetailProps {
  id: string
  currentId: string
  info: SessionInfo | null
  view: SessionView | null
  expanded: { id: string; record: string } | null
  processedView: ProcessedView | null
  onSetCurrent: (id: string) => void
  onRename: (id: string, name: string) => void
  onExpand: (id: string, record: string) => void
  onCollapse: () => void
}

function SessionDetail({
  id,
  currentId,
  info,
  view,
  expanded,
  processedView,
  onSetCurrent,
  onRename,
  onExpand,
  onCollapse,
}: SessionDetailProps) {
  const [tab, setTab] = useState<DetailTab>('details')
  const [renameInput, setRenameInput] = useState('')
  const [renaming, setRenaming] = useState(false)

  const nameField = info?.fields?.find(([k]) => k === 'name')?.[1] ?? ''
  const isDefault = id.endsWith('-default')
  const isCurrent = id === currentId

  return (
    <div className="session-detail">
      <div className="session-detail-header">
        <div className="session-detail-title">
          <span className={`conn-dot ${isCurrent ? 'connected' : 'disconnected'}`} title={isCurrent ? 'current session' : 'not current'} />
          {renaming ? (
            <input
              className="repr-footer-input session-rename-input"
              autoFocus
              value={renameInput}
              onChange={(e) => setRenameInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && renameInput.trim()) {
                  onRename(id, renameInput.trim())
                  setRenaming(false)
                } else if (e.key === 'Escape') {
                  setRenaming(false)
                }
              }}
            />
          ) : (
            <h2>{nameField || id}</h2>
          )}
        </div>
        <div className="session-detail-actions">
          {!isCurrent && (
            <button className="btn-secondary" onClick={() => onSetCurrent(id)} title="ufa session set">
              Set as Current
            </button>
          )}
          {!isDefault && !renaming && (
            <button
              className="btn-secondary"
              onClick={() => {
                setRenameInput(nameField)
                setRenaming(true)
              }}
              title="ufa session rename"
            >
              Rename
            </button>
          )}
        </div>
      </div>

      <div className="session-detail-tabs">
        <button className={`session-tab${tab === 'details' ? ' active' : ''}`} onClick={() => setTab('details')}>
          Details
        </button>
        <button className={`session-tab${tab === 'transcript' ? ' active' : ''}`} onClick={() => setTab('transcript')}>
          View
        </button>
      </div>

      {tab === 'details' ? (
        <div className="session-detail-body">
          {info ? (
            <table className="session-fields">
              <tbody>
                <tr>
                  <td className="session-field-key">id</td>
                  <td className="session-field-val">{info.id}</td>
                </tr>
                <tr>
                  <td className="session-field-key">location</td>
                  <td className="session-field-val">{info.location}</td>
                </tr>
                {(info.fields ?? []).map(([k, v]) => (
                  <tr key={k}>
                    <td className="session-field-key">{k}</td>
                    <td className="session-field-val">{v}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <div className="session-detail-loading">loading…</div>
          )}
        </div>
      ) : (
        <>
          {/* Hidden rather than unmounted while a turn is expanded, so going
              back up lands at the same scroll position. */}
          <div className={`session-transcript-scope${expanded ? ' is-hidden' : ''}`}>
            <SessionTranscript view={view} onExpand={(record) => onExpand(id, record)} />
          </div>
          {expanded && <ProcessedPane record={expanded.record} view={processedView} onUp={onCollapse} />}
        </>
      )}
    </div>
  )
}

// ProcessedPane is one transcript turn expanded into its full -processed
// file -- everything the transcript's capped preview leaves out. "↑
// transcript" (or Escape) goes back up, mirroring condoccer's scoping.
function ProcessedPane({ record, view, onUp }: { record: string; view: ProcessedView | null; onUp: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      if (e.key === 'Escape' && !(t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA'))) {
        onUp()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onUp])

  return (
    <div className="processed-pane">
      <div className="processed-pane-header">
        <button className="nav-up-btn" onClick={onUp} title="back to the transcript (Esc)">
          ↑ transcript
        </button>
        <span className="processed-pane-file">{view?.file ?? `${record}-processed.txt`}</span>
      </div>
      {!view ? (
        <div className="session-detail-loading">loading…</div>
      ) : view.error ? (
        <div className="session-transcript-warning">{view.error}</div>
      ) : (
        <ProcessedContent content={view.content} />
      )}
    </div>
  )
}

// Separators/prefixes of a -processed file, mirroring clauditable/pkg/
// records (WrittenFileSeparator, ResponseSeparator, Input/Output/
// ErrorPrefix).
const WRITTEN_RAW_SEPARATOR = '\n----------WRITTEN_RAW----------\n'
const RESPONSE_SEPARATOR = '\n\n----------RESPONSE----------\n\n'
const STDERR_MARKER = '[STDERR]\n'
const BLOCK_PREFIXES: [string, 'in' | 'out' | 'err'][] = [
  ['IN>> ', 'in'],
  ['OUT>> ', 'out'],
  ['ERR>> ', 'err'],
]

type ProcessedBlock = { kind: 'in' | 'out' | 'err'; text: string }

interface ParsedProcessed {
  event: Record<string, unknown> | null
  processing: Record<string, unknown>[]
  blocks: ProcessedBlock[]
}

// parseProcessed splits a -processed file (see clauditable's
// FormatProcessedFile) into its event header, its auto-maintenance
// processing headers, and command/response blocks with the IN>>/OUT>>/ERR>>
// prefixes stripped. The written-raw section after WRITTEN_RAW is the full
// command and response; the prefixed lines before it are only a capped
// preview of the same, so they're used only when there is no raw section.
function parseProcessed(content: string): ParsedProcessed {
  const sep = content.indexOf(WRITTEN_RAW_SEPARATOR)
  const log = sep >= 0 ? content.slice(0, sep) : content
  const raw = sep >= 0 ? content.slice(sep + WRITTEN_RAW_SEPARATOR.length) : null

  let event: Record<string, unknown> | null = null
  const processing: Record<string, unknown>[] = []
  const preview: ProcessedBlock[] = []
  for (const line of log.split('\n')) {
    if (line.startsWith('{')) {
      try {
        const obj = JSON.parse(line) as Record<string, unknown>
        if (event === null && ('timestamp' in obj || 'event_type' in obj)) event = obj
        else processing.push(obj)
        continue
      } catch {
        // not JSON after all -- fall through to the prefix checks
      }
    }
    const match = BLOCK_PREFIXES.find(([p]) => line.startsWith(p))
    if (!match) continue
    const [prefix, kind] = match
    const text = line.slice(prefix.length)
    const last = preview[preview.length - 1]
    if (last && last.kind === kind) last.text += '\n' + text
    else preview.push({ kind, text })
  }

  let blocks = preview
  if (raw !== null) {
    const r = raw.indexOf(RESPONSE_SEPARATOR)
    const command = r >= 0 ? raw.slice(0, r) : raw
    let response = r >= 0 ? raw.slice(r + RESPONSE_SEPARATOR.length) : ''
    // FormatRawFile appends stderr after a "[STDERR]" line.
    let stderr = ''
    const e = response.startsWith(STDERR_MARKER) ? 0 : response.indexOf('\n' + STDERR_MARKER)
    if (e >= 0) {
      stderr = response.slice(e === 0 ? STDERR_MARKER.length : e + 1 + STDERR_MARKER.length)
      response = response.slice(0, e)
    }
    blocks = [{ kind: 'in', text: command.trimEnd() }]
    if (response.trim()) blocks.push({ kind: 'out', text: response.trimEnd() })
    if (stderr.trim()) blocks.push({ kind: 'err', text: stderr.trimEnd() })
  }
  return { event, processing, blocks }
}

function formatHeaderValue(key: string, value: unknown): string {
  if (key === 'duration_ms' && typeof value === 'number') {
    return value >= 1000 ? `${(value / 1000).toFixed(1)}s (${value}ms)` : `${value}ms`
  }
  return typeof value === 'string' ? value : JSON.stringify(value)
}

const BLOCK_LABELS = { in: 'command', out: 'response', err: 'error' }

// ProcessedContent renders a -processed file: its JSON event header as a
// key/value table (processing headers beneath it), then the command and
// response as colour-coded blocks instead of IN>>/OUT>> prefixed text.
function ProcessedContent({ content }: { content: string }) {
  const { event, processing, blocks } = parseProcessed(content)
  return (
    <div className="processed-pane-content">
      {(event || processing.length > 0) && (
        <table className="processed-header-table">
          <tbody>
            {event &&
              Object.entries(event).map(([k, v]) => (
                <tr key={k}>
                  <th>{k}</th>
                  <td
                    className={
                      k === 'exit_code' ? (v === 0 ? 'transcript-exit-ok' : 'transcript-exit-err') : undefined
                    }
                  >
                    {formatHeaderValue(k, v)}
                  </td>
                </tr>
              ))}
            {processing.map((p, i) => {
              const { processing_type, ...rest } = p
              return (
                <tr key={`p${i}`} className="processed-header-processing">
                  <th>processing</th>
                  <td>
                    <span className="transcript-entry-tag">{String(processing_type ?? '?')}</span>
                    {Object.entries(rest).map(([k, v]) => (
                      <span key={k} className="processed-header-sub">
                        {k} {formatHeaderValue(k, v)}
                      </span>
                    ))}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
      {blocks.map((b, i) => (
        <div key={i} className={`transcript-block transcript-block-${b.kind}`}>
          <div className="transcript-block-label">{BLOCK_LABELS[b.kind]}</div>
          <pre>{b.text}</pre>
        </div>
      ))}
    </div>
  )
}

// SessionTranscript presents a session's session.jsonl in the "ice readable"
// way Revision I asks for: one card per turn, timestamp/agent/model/
// duration/exit-code header, then its input/output/error blocks -- instead
// of the raw IN>>/OUT>>/ERR>> prefixed log text. A turn with a record ID gets
// an "expand" control that opens its full -processed file (ProcessedPane).
function SessionTranscript({ view, onExpand }: { view: SessionView | null; onExpand: (record: string) => void }) {
  if (!view) {
    return <div className="session-detail-body session-detail-loading">loading…</div>
  }
  const entries = view.entries ?? []
  // sync_incomplete means local-representative couldn't reach one of the
  // other LR-active hosts while refreshing this session just now -- the
  // transcript below (even if non-empty) may be missing turns a reachable
  // peer would have had (see types.ts's SessionView doc comment).
  const warning = view.sync_incomplete && (
    <div className="session-transcript-warning">
      could not sync from one or more remote hosts just now -- this transcript may be stale or incomplete
    </div>
  )
  if (entries.length === 0) {
    return (
      <div className="session-detail-body">
        {warning}
        <span className="session-transcript-empty">no recorded turns in this session yet</span>
      </div>
    )
  }
  return (
    <div className="session-transcript">
      {warning}
      {entries.map((e, i) => (
        <div className="transcript-entry" key={i}>
          <div className="transcript-entry-header">
            <span className="transcript-entry-time">{e.timestamp}</span>
            {e.agent && <span className="transcript-entry-tag">{e.agent}</span>}
            {e.model && <span className="transcript-entry-tag">{e.model}</span>}
            <span className="transcript-entry-tag">{e.duration_ms}ms</span>
            <span className={`transcript-entry-tag ${e.exit_code === 0 ? 'transcript-exit-ok' : 'transcript-exit-err'}`}>
              exit {e.exit_code}
            </span>
            {e.record && (
              <button
                className="transcript-expand-btn"
                onClick={() => onExpand(e.record!)}
                title={`open ${e.record}-processed.txt -- the full turn, not just this preview`}
              >
                ⤢ expand
              </button>
            )}
          </div>
          {e.input && (
            <div className="transcript-block transcript-block-in">
              <div className="transcript-block-label">input</div>
              <pre>{e.input}</pre>
            </div>
          )}
          {e.output && (
            <div className="transcript-block transcript-block-out">
              <div className="transcript-block-label">output</div>
              <pre>{e.output}</pre>
            </div>
          )}
          {e.error && (
            <div className="transcript-block transcript-block-err">
              <div className="transcript-block-label">error</div>
              <pre>{e.error}</pre>
            </div>
          )}
        </div>
      ))}
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
    sessions,
    currentSessionId,
    selectedId,
    sessionInfo,
    sessionView,
    expanded,
    processedView,
    notice,
    selectSession,
    expandEntry,
    collapseEntry,
    newSession,
    setCurrentSession,
    renameSession,
    archiveSessions,
  } = useSessionManagerWS()

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
            <h1>Session Manager</h1>
            {version && <span className="app-version-tag">v{version}</span>}
          </div>
          <SessionList
            sessions={sessions}
            currentId={currentSessionId}
            selectedId={selectedId}
            onSelect={selectSession}
            onNew={newSession}
            onArchive={archiveSessions}
          />
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
        {notice && (
          <div className={`session-notice session-notice-${notice.kind}`}>{notice.message}</div>
        )}
        {selectedId ? (
          <SessionDetail
            id={selectedId}
            currentId={currentSessionId}
            info={sessionInfo}
            view={sessionView}
            expanded={expanded}
            processedView={processedView}
            onSetCurrent={setCurrentSession}
            onRename={renameSession}
            onExpand={expandEntry}
            onCollapse={collapseEntry}
          />
        ) : (
          <div className="empty-state">
            <div>
              <span className={`conn-dot ${connected ? 'connected' : 'disconnected'}`} />
              Select a session on the left, or create a new one, to get started.
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
