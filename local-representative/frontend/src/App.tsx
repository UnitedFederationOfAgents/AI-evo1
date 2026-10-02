import { useState, useEffect, useCallback, useRef } from 'react'
import type { ServiceStatus, StatusMsg, FCStateMsg, FCLogMsg, RidealongStateMsg, CondocStateMsg, ACStateMsg, ProcInfo, SystemStateMsg, FileInfo, FilesStateMsg, ModeMismatchMsg, RepoStateMsg, TCAvailabilityMsg } from './types'

const TABS = ['federation-command', 'condoccer', 'convo', 'sessions', 'worker', 'system', 'files'] as const
type Tab = typeof TABS[number]

// Tabs whose content is another app's own UI, embedded via same-origin
// iframe (condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step1Prompt.md Revision A: "nested UI" tabs). These skip the service-name
// heading and health-indicator every other tab gets and instead get a full
// tab pane for the iframe plus a slim status bar under the tab bar, so the
// embedded app's own UI (including its own "DEV MODE" border, when that
// sub-app runs in dev mode) fills the space instead of floating in a
// padded, header-topped box.
const EMBED_TABS: ReadonlySet<Tab> = new Set(['condoccer', 'sessions', 'convo'])

// Screen-history nav arrows (condocs/initialDistributedDevelopmentImpls/
// Step5Prompt.md Revision M): how many recently-visited tabs we keep around
// for back/forward. A pragmatic starting value -- see useScreenHistory below.
const NAV_HISTORY_MAX = 20

// Applications the system tab offers a launch button for. `multi` apps are
// N-per-host (launch stays enabled while instances run); others are singletons.
const LAUNCHABLE_APPS: { name: string; multi: boolean }[] = [
  { name: 'federation-command', multi: true },
  { name: 'condoccer', multi: false },
  { name: 'sessions', multi: false },
  { name: 'convo', multi: false },
]

interface LogEntry {
  kind: 'cmd' | 'output' | 'state'
  text: string
}

function useStatusWS() {
  const [connected, setConnected] = useState(false)
  const [services, setServices] = useState<ServiceStatus[]>([])
  const [fcState, setFcState] = useState<string>('')
  const [fcLog, setFcLog] = useState<LogEntry[]>([])
  const [ridealongState, setRidealongState] = useState<RidealongStateMsg | null>(null)
  const [condocState, setCondocState] = useState<CondocStateMsg | null>(null)
  const [acState, setAcState] = useState<ACStateMsg>({ connected: false })
  const [systemState, setSystemState] = useState<SystemStateMsg | null>(null)
  const [repoState, setRepoState] = useState<RepoStateMsg>({ watched: false, dirty: false, rebuild_ready: false, building: false, auto_rebuild: false })
  const [filesState, setFilesState] = useState<FilesStateMsg | null>(null)
  // Peer name -> current mismatch disclosure -- see docs/DevMode.md. A
  // mismatched peer only ever exchanges health information with this LR.
  const [modeMismatches, setModeMismatches] = useState<Record<string, ModeMismatchMsg>>({})
  // Aggregate "is a the-conversationalist instance available on any host",
  // relayed down from agent-coordinator -- see tcavailability.go and
  // Step2Prompt.md. Drives the mic icon beside the camera/screenshot icon.
  const [tcAvailable, setTCAvailable] = useState(false)
  const wsRef = useRef<WebSocket | null>(null)
  const retryRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const fcStateRef = useRef<string>('')

  const sendCommand = useCallback((cmd: string) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({
        type: 'command',
        payload: { cmd },
      }))
    }
  }, [])

  const sendRidealongCommand = useCallback((action: string) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({
        type: 'ridealong-command',
        payload: { action },
      }))
    }
  }, [])

  const connectToAC = useCallback((host: string, port: string) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({
        type: 'connect-ac',
        payload: { host, port },
      }))
    }
  }, [])

  const disconnectFromAC = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: 'disconnect-ac', payload: {} }))
    }
  }, [])

  // setAutoConnectAC toggles the persistent auto-connect state: on, it arms
  // the background retry loop at host/port (falling back to the last-used
  // target server-side when omitted) and keeps it armed across a successful
  // connection, so a later unintentional disconnect resumes the cycle on its
  // own; off, it only cancels a retry in progress -- disconnecting an active
  // connection is still a separate, explicit action (see disconnectFromAC).
  const setAutoConnectAC = useCallback((enabled: boolean, host?: string, port?: string) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: 'set-auto-connect-ac', payload: { enabled, host, port } }))
    }
  }, [])

  const launchApp = useCallback((name: string) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: 'launch-app', payload: { name } }))
    }
  }, [])

  const terminateApp = useCallback((id: string) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: 'terminate-app', payload: { id } }))
    }
  }, [])

  // Restarts this local-representative process itself — only expected to
  // come back up when it is loader-managed (see ProcInfo.loader_managed);
  // the system tab greys the control out otherwise.
  const restartApp = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: 'restart-app', payload: {} }))
    }
  }, [])

  // Dev-repo watcher controls (--dev-repo, see docs/DevMode.md): rebuildRepo
  // runs 'make deploy-dev-binaries' at the watched repo's root; setAutoRebuild
  // toggles whether that happens automatically whenever it becomes possible.
  const rebuildRepo = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: 'rebuild-app', payload: {} }))
    }
  }, [])

  const setAutoRebuild = useCallback((enabled: boolean) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: 'set-auto-rebuild', payload: { enabled } }))
    }
  }, [])

  // Toggles whether this LR restarts itself the instant an update becomes
  // available, instead of waiting for the "update and restart" button --
  // see docs/DevMode.md "Loader".
  const setAutoUpdate = useCallback((enabled: boolean) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: 'set-auto-update', payload: { enabled } }))
    }
  }, [])

  // File upload is a plain HTTP POST (not a websocket command) so the browser
  // can stream the multipart body directly to /api/files.
  const uploadFiles = useCallback(async (fileList: FileList | File[]) => {
    const files = Array.from(fileList)
    if (files.length === 0) return
    const form = new FormData()
    for (const f of files) form.append('file', f)
    try {
      const resp = await fetch('/api/files', { method: 'POST', body: form })
      if (!resp.ok) {
        console.error('file upload failed:', resp.status, await resp.text())
      }
    } catch (err) {
      console.error('file upload failed:', err)
    }
  }, [])

  const connect = useCallback(() => {
    const ws = new WebSocket(`ws://${window.location.host}/ws`)
    wsRef.current = ws

    ws.onopen = () => {
      setConnected(true)
      // The server resends a full mode-mismatch snapshot on connect; drop any
      // stale entries from a mismatch that cleared while we were offline.
      setModeMismatches({})
    }

    ws.onclose = () => {
      setConnected(false)
      retryRef.current = setTimeout(connect, 2000)
    }

    ws.onerror = () => ws.close()

    ws.onmessage = (ev: MessageEvent) => {
      try {
        const msg = JSON.parse(ev.data as string) as { type: string; payload: unknown }
        switch (msg.type) {
          case 'status':
            setServices((msg.payload as StatusMsg).services)
            break
          case 'fc-state': {
            const raw = (msg.payload as FCStateMsg).state
            const newSt = raw === 'disconnected' ? '' : raw
            const prevSt = fcStateRef.current
            fcStateRef.current = newSt
            setFcState(newSt)
            if (prevSt !== newSt) {
              const label =
                newSt === '' ? '-- disconnected --'
                : newSt === 'remote-control' ? '-- remote control --'
                : newSt === 'local-control' ? '-- local control --'
                : `-- ${newSt} --`
              setFcLog(prev => [...prev, { kind: 'state', text: label }])
            }
            break
          }
          case 'fc-log': {
            const logPayload = msg.payload as FCLogMsg
            setFcLog(prev => [
              ...prev.slice(-199),
              {
                kind: logPayload.kind === 'output' ? 'output' : 'cmd',
                text: logPayload.line,
              },
            ])
            break
          }
          case 'ridealong-state': {
            const payload = msg.payload as RidealongStateMsg
            setRidealongState(payload.active ? payload : null)
            break
          }
          case 'condoc-state': {
            const payload = msg.payload as CondocStateMsg
            setCondocState(payload.active ? payload : null)
            break
          }
          case 'ac-state':
            setAcState(msg.payload as ACStateMsg)
            break
          case 'system-state': {
            const payload = msg.payload as SystemStateMsg
            setSystemState(payload)
            // A rebuild+restart (see docs/DevMode.md, --dev-repo) is
            // invisible to an already-open tab -- the reconnect above is the
            // only signal it gets. Compare the server's own reported
            // version against this bundle's build-time version and reload
            // if they differ; skip under the Vite dev server, where HMR
            // already keeps the tab current and the two are never expected
            // to match (see BrowserRefreshStrategy.md).
            if (!import.meta.env.DEV && payload.self.version && payload.self.version !== __APP_VERSION__) {
              window.location.reload()
            }
            break
          }
          case 'repo-state':
            setRepoState(msg.payload as RepoStateMsg)
            break
          case 'files-state':
            setFilesState(msg.payload as FilesStateMsg)
            break
          case 'mode-mismatch': {
            const payload = msg.payload as ModeMismatchMsg
            setModeMismatches(prev => {
              const next = { ...prev }
              if (payload.mismatched) next[payload.peer] = payload
              else delete next[payload.peer]
              return next
            })
            break
          }
          case 'tc-availability': {
            const payload = msg.payload as TCAvailabilityMsg
            setTCAvailable(payload.available)
            break
          }
        }
      } catch {
        // ignore malformed messages
      }
    }
  }, [])

  useEffect(() => {
    connect()
    return () => {
      if (retryRef.current) clearTimeout(retryRef.current)
      wsRef.current?.close()
    }
  }, [connect])

  return {
    connected, services, fcState, fcLog, ridealongState, condocState, acState, systemState, repoState, filesState, modeMismatches, tcAvailable,
    sendCommand, sendRidealongCommand, connectToAC, disconnectFromAC, setAutoConnectAC, launchApp, terminateApp, restartApp, uploadFiles,
    rebuildRepo, setAutoRebuild, setAutoUpdate,
  }
}

function FCCommandPanel({
  fcState,
  fcLog,
  sendCommand,
}: {
  fcState: string
  fcLog: LogEntry[]
  sendCommand: (cmd: string) => void
}) {
  const [input, setInput] = useState('')
  const logEndRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    logEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [fcLog])

  const submit = () => {
    const cmd = input.trim()
    if (!cmd) return
    sendCommand(cmd)
    setInput('')
  }

  if (fcState === '') {
    return <div className="fc-status fc-status-disconnected">not connected</div>
  }

  return (
    <div className="fc-panel">
      <div className="fc-output">
        {fcLog.length === 0
          ? <span className="fc-log-empty">waiting for activity…</span>
          : fcLog.map((entry, i) =>
              entry.kind === 'state'
                ? <div key={i} className="fc-log-state">{entry.text}</div>
                : entry.kind === 'output'
                ? <div key={i} className="fc-log-output">{entry.text}</div>
                : <div key={i} className="fc-log-line">
                    <span className="fc-log-prompt">$</span> {entry.text}
                  </div>
            )
        }
        <div ref={logEndRef} />
      </div>
      {fcState === 'remote-control' && (
        <div className="fc-command-area">
          <span className="fc-cmd-prompt">$</span>
          <input
            className="fc-cmd-input"
            type="text"
            value={input}
            placeholder="enter command to run on federation-command…"
            onChange={e => setInput(e.target.value)}
            onKeyDown={e => {
              if (e.key === 'Enter') submit()
            }}
            autoFocus
          />
          <button className="fc-cmd-send" onClick={submit}>run</button>
        </div>
      )}
    </div>
  )
}

function RidealongPanel({
  state,
  fcState,
  sendRidealongCommand,
}: {
  state: RidealongStateMsg
  fcState: string
  sendRidealongCommand: (action: string) => void
}) {
  const [customCmd, setCustomCmd] = useState('')
  const canControl = fcState === 'remote-control'

  const totalSteps = state.total_steps ?? 0
  const currentIndex = state.current_index ?? 0
  const stepLabel = totalSteps > 0 ? `${currentIndex + 1} / ${totalSteps}` : ''

  const submitCustom = () => {
    const text = customCmd.trim()
    if (!text) return
    sendRidealongCommand(`custom:${text}`)
    setCustomCmd('')
  }

  return (
    <div className="ra-panel">
      <div className="ra-header">
        <span className="ra-icon">🚔</span>
        <span className="ra-title">ridealong</span>
        <span className="ra-file">{state.title}</span>
        {stepLabel && <span className="ra-progress">{stepLabel}</span>}
      </div>

      <div className="ra-steps">
        {state.prev_cmd ? (
          <div className="ra-step ra-step-prev">
            <span className="ra-step-marker ra-step-marker-prev">▸</span>
            <span className="ra-step-text">{state.prev_cmd}</span>
            {(state.prev_exit_code ?? 0) !== 0 && (
              <span className="ra-exit-code">[{state.prev_exit_code}]</span>
            )}
          </div>
        ) : (
          <div className="ra-step ra-step-prev">
            <span className="ra-step-text ra-step-empty">(no previous step)</span>
          </div>
        )}
        <div className="ra-step ra-step-current">
          <span className="ra-step-marker ra-step-marker-current">✦</span>
          <span className="ra-step-text">{state.current_cmd}</span>
          {state.autoplay && state.countdown && (
            <span className="ra-countdown">{state.countdown}</span>
          )}
        </div>
        <div className="ra-step ra-step-next">
          <span className="ra-step-marker ra-step-marker-next">▸</span>
          <span className="ra-step-text">{state.next_cmd === '<end>' ? '<end>' : state.next_cmd}</span>
        </div>
      </div>

      {canControl && (
        <div className="ra-controls">
          <div className="ra-actions">
            <button
              className="ra-btn ra-btn-primary"
              onClick={() => sendRidealongCommand('execute')}
              title="Execute current step"
            >
              execute
            </button>
            <button
              className={`ra-btn ${state.autoplay ? 'ra-btn-active' : ''}`}
              onClick={() => sendRidealongCommand('autoplay')}
              title="Toggle autoplay"
            >
              {state.autoplay ? 'stop autoplay' : 'autoplay'}
            </button>
            <button
              className="ra-btn ra-btn-danger"
              onClick={() => sendRidealongCommand('exit')}
              title="Exit ridealong"
            >
              exit
            </button>
          </div>

          {state.waypoints && state.waypoints.length > 0 && (
            <div className="ra-waypoints">
              <span className="ra-waypoints-label">waypoints:</span>
              {state.waypoints.map(wp => (
                <button
                  key={wp}
                  className="ra-btn ra-btn-waypoint"
                  onClick={() => sendRidealongCommand(`waypoint:${wp}`)}
                >
                  {wp}
                </button>
              ))}
            </div>
          )}

          <div className="ra-custom">
            <span className="ra-cmd-prompt">$</span>
            <input
              className="ra-cmd-input"
              type="text"
              value={customCmd}
              placeholder="insert custom command…"
              onChange={e => setCustomCmd(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') submitCustom() }}
            />
            <button className="ra-btn" onClick={submitCustom}>run</button>
          </div>
        </div>
      )}

      {!canControl && (
        <div className="ra-observe-hint">observing — switch to remote control to drive</div>
      )}
    </div>
  )
}

function CondocPanel({
  state,
  fcState,
}: {
  state: CondocStateMsg
  fcState: string
}) {
  const canControl = fcState === 'remote-control'
  const stepLabel = state.step_num ? `step ${state.step_num}` : ''

  return (
    <div className="condoc-panel">
      <div className="condoc-header">
        <span className="condoc-icon">📄</span>
        <span className="condoc-title">condoc</span>
        {state.name && <span className="condoc-name">{state.name}</span>}
      </div>
      <div className="condoc-status">
        <div className="condoc-phase">
          {stepLabel && <span className="condoc-step">{stepLabel}</span>}
          <span className="condoc-phase-label">{state.phase}</span>
        </div>
        {state.status_msg && (
          <div className="condoc-msg">{state.status_msg}</div>
        )}
      </div>
      {!canControl && (
        <div className="ra-observe-hint">observing — switch to remote control to drive</div>
      )}
    </div>
  )
}

function ACConnectionPanel({
  acState,
  onConnect,
  onDisconnect,
  onSetAutoConnect,
}: {
  acState: { connected: boolean; host?: string; port?: string; connecting?: boolean; auto_connect?: boolean }
  onConnect: (host: string, port: string) => void
  onDisconnect: () => void
  onSetAutoConnect: (enabled: boolean, host?: string, port?: string) => void
}) {
  const [host, setHost] = useState(acState.host ?? 'localhost')
  const [port, setPort] = useState(acState.port ?? '8084')

  // auto-connect toggle: a first-class state independent of the current
  // connection, so it's rendered alongside every panel variant (see Revision
  // I of Step3Prompt.md) -- checking it arms the background retry loop and
  // keeps it armed across a successful connection; unchecking it only stops a
  // retry in progress, not an already-live connection (use disconnect below
  // for that, which also unchecks this).
  const autoConnectToggle = (
    <label className="ac-auto-connect" title="keep reaching for agent-coordinator: stays armed across a successful connection so an unintentional disconnect resumes the cycle on its own -- an explicit disconnect turns it off">
      <input
        type="checkbox"
        checked={acState.auto_connect ?? false}
        onChange={e => onSetAutoConnect(e.target.checked, host, port)}
      />
      auto-connect
    </label>
  )

  if (acState.connected) {
    return (
      <div className="ac-panel ac-panel-connected">
        <span className="ac-label">agent-coordinator</span>
        <span className="ac-addr">{acState.host}:{acState.port}</span>
        {autoConnectToggle}
        <button className="ac-btn ac-btn-disconnect" onClick={onDisconnect}>disconnect</button>
      </div>
    )
  }

  if (acState.connecting) {
    return (
      <div className="ac-panel ac-panel-connecting">
        <span className="ac-label">agent-coordinator</span>
        <span className="ac-connecting">auto-connecting… {acState.host}:{acState.port}</span>
        {autoConnectToggle}
        <button className="ac-btn ac-btn-disconnect" onClick={onDisconnect}>cancel</button>
      </div>
    )
  }

  return (
    <div className="ac-panel">
      <span className="ac-label">agent-coordinator</span>
      <input
        className="ac-input"
        type="text"
        value={host}
        placeholder="host"
        onChange={e => setHost(e.target.value)}
        onKeyDown={e => { if (e.key === 'Enter') onConnect(host, port) }}
      />
      <span className="ac-sep">:</span>
      <input
        className="ac-input ac-input-port"
        type="text"
        value={port}
        placeholder="port"
        onChange={e => setPort(e.target.value)}
        onKeyDown={e => { if (e.key === 'Enter') onConnect(host, port) }}
      />
      <button className="ac-btn ac-btn-connect" onClick={() => onConnect(host, port)}>connect</button>
      {autoConnectToggle}
    </div>
  )
}

function formatUptime(startedAt: number, nowSec: number): string {
  if (!startedAt) return '—'
  const secs = Math.max(0, nowSec - startedAt)
  if (secs < 60) return `${secs}s`
  if (secs < 3600) return `${Math.floor(secs / 60)}m ${secs % 60}s`
  return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`
}

function SystemProcRow({
  proc,
  nowSec,
  onTerminate,
  onRestart,
  onSetAutoUpdate,
}: {
  proc: ProcInfo
  nowSec: number
  onTerminate?: (id: string) => void
  onRestart?: () => void
  onSetAutoUpdate?: (enabled: boolean) => void
}) {
  const detail = proc.status === 'running'
    ? formatUptime(proc.started_at, nowSec)
    : `exit ${proc.exit_code}`

  const label = proc.managed && proc.instance > 0
    ? `${proc.name} #${proc.instance}`
    : proc.name

  return (
    <div className={`sys-row sys-row-${proc.status}`}>
      <span className="sys-col sys-col-name">
        <span className="sys-col-name-main">
          {label}
          {!proc.managed && <span className="sys-self-tag">this process</span>}
          {proc.dev_mode && <span className="sys-dev-tag" title="launched with --dev-mode">dev</span>}
        </span>
        {proc.version && (
          <span className="sys-version-tag" title="build version">{proc.version}</span>
        )}
        {proc.session && (
          <span className="sys-session-tag" title="active session">{proc.session}</span>
        )}
      </span>
      <span className="sys-col sys-col-pid">{proc.pid > 0 ? proc.pid : '—'}</span>
      <span className={`sys-col sys-col-status sys-status-${proc.status}`}>{proc.status}</span>
      <span className="sys-col sys-col-detail" title={proc.detail}>{detail}</span>
      <span className="sys-col sys-col-actions">
        {proc.managed && onTerminate && (
          <button
            className="sys-btn sys-btn-terminate"
            onClick={() => onTerminate(proc.instance_id)}
          >
            {proc.status === 'running' ? 'terminate' : 'dismiss'}
          </button>
        )}
        {!proc.managed && onRestart && (
          <button
            className={`sys-btn sys-btn-restart${proc.update_available ? ' sys-btn-restart-update' : ''}`}
            disabled={!proc.loader_managed}
            title={proc.loader_managed
              ? (proc.update_available
                ? 'a newer build has landed on disk — terminate this process so ufa-loader relaunches it with the new binary'
                : 'terminate this process so ufa-loader relaunches it with the identical config')
              : 'not loader-managed — run under ufa-loader (see make run-loader) to enable'}
            onClick={onRestart}
          >
            {proc.update_available ? 'update and restart' : 'restart'}
          </button>
        )}
        {!proc.managed && onSetAutoUpdate && (
          <label
            className="sys-auto-rebuild"
            title={proc.loader_managed
              ? 'restart automatically the instant an update becomes available, instead of waiting for the button above'
              : 'not loader-managed — run under ufa-loader (see make run-loader) to enable'}
          >
            <input
              type="checkbox"
              disabled={!proc.loader_managed}
              checked={!!proc.auto_update}
              onChange={e => onSetAutoUpdate(e.target.checked)}
            />
            auto-update
          </label>
        )}
      </span>
    </div>
  )
}

// TroughEntry is one line in the system tab's "trough" (see Trough below) --
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision L.
interface TroughEntry {
  id: number
  ts: number // Date.now(), when the notification was recorded
  text: string
}

// TROUGH_MAX_ENTRIES caps how much history the trough keeps -- it's a live,
// session-scoped notification log (not persisted; a refresh starts it fresh),
// not an audit trail, so old entries are simply dropped off the front.
const TROUGH_MAX_ENTRIES = 50

let troughIdSeq = 0

// useTrough appends a new trough entry each time `error` changes to a new,
// non-empty value -- right now that's only ever RepoStateMsg.last_error after
// a failed rebuild, per the prompt's "print errors or notifications when
// things happen like a failure during rebuild", but the trough itself is
// generic (any future error/notification source can feed it the same way).
function useTrough(error: string | undefined): TroughEntry[] {
  const [entries, setEntries] = useState<TroughEntry[]>([])
  const lastSeen = useRef<string | undefined>(undefined)

  useEffect(() => {
    if (error && error !== lastSeen.current) {
      setEntries(prev => [
        ...prev.slice(-(TROUGH_MAX_ENTRIES - 1)),
        { id: ++troughIdSeq, ts: Date.now(), text: `rebuild failed — ${error}` },
      ])
    }
    lastSeen.current = error
  }, [error])

  return entries
}

// Trough is "an expandable-and-then-scrollable single line at the bottom of
// the main pane where we can print errors or notifications when things
// happen" (Step5Prompt.md Revision L). Collapsed, it's just the most recent
// entry on one line; clicking it expands into a scrollable list of
// everything recorded this session, newest first.
function Trough({ entries }: { entries: TroughEntry[] }) {
  const [expanded, setExpanded] = useState(false)
  const latest = entries[entries.length - 1]

  return (
    <div className={`sys-trough${expanded ? ' sys-trough-expanded' : ''}`}>
      <button
        className="sys-trough-line"
        onClick={() => setExpanded(e => !e)}
        disabled={entries.length === 0}
        title={entries.length === 0 ? 'no notifications yet' : expanded ? 'collapse' : 'expand for full history'}
      >
        <span className="sys-trough-chevron">{expanded ? '▾' : '▸'}</span>
        {latest ? (
          <>
            <span className="sys-trough-ts">{new Date(latest.ts).toLocaleTimeString()}</span>
            <span className="sys-trough-text">{latest.text}</span>
          </>
        ) : (
          <span className="sys-trough-empty">no notifications</span>
        )}
        {entries.length > 1 && <span className="sys-trough-count">{entries.length}</span>}
      </button>
      {expanded && (
        <div className="sys-trough-list">
          {entries.slice().reverse().map(e => (
            <div key={e.id} className="sys-trough-entry">
              <span className="sys-trough-ts">{new Date(e.ts).toLocaleTimeString()}</span>
              <span className="sys-trough-text">{e.text}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// RepoWatchPanel is the system tab's dev-repo watcher widget (--dev-repo, see
// docs/DevMode.md): the rebuild button turns orange and reads "dirty" while
// the watched repo has uncommitted changes -- but stays disabled, since
// rebuilding a dirty tree would silently bake in unreviewed changes. It's
// selectable, plain, and reads "rebuild" only once HEAD has moved since the
// last build with the repo clean; otherwise it's disabled. Rendered only
// when LR was actually launched with --dev-repo.
function RepoWatchPanel({
  repoState,
  onRebuild,
  onSetAutoRebuild,
}: {
  repoState: RepoStateMsg
  onRebuild: () => void
  onSetAutoRebuild: (enabled: boolean) => void
}) {
  if (!repoState.watched) return null

  const locked = repoState.condoc_locked && !repoState.dirty
  const label = repoState.building ? 'building…' : repoState.dirty ? 'dirty' : locked ? 'condoc' : 'rebuild'

  return (
    <div className="sys-repo-panel">
      <div className="sys-repo-info">
        <span className="sys-repo-label">dev-repo</span>
        <span className="sys-repo-root" title={repoState.root}>{repoState.root}</span>
        {repoState.head && <span className="sys-repo-head">{repoState.head}</span>}
      </div>
      <div className="sys-repo-controls">
        <button
          className={`sys-btn sys-btn-rebuild${repoState.dirty ? ' sys-btn-rebuild-dirty' : ''}`}
          disabled={repoState.building || !repoState.rebuild_ready}
          onClick={onRebuild}
          title={
            repoState.dirty
              ? 'uncommitted changes — commit or revert to enable rebuilding'
              : locked
              ? 'a condoc is mid-transition (.condoc lock file present) — rebuilding is held off until it settles'
              : repoState.rebuild_ready
              ? 'HEAD has moved since the last rebuild — runs make deploy-dev-binaries at the repo root'
              : 'nothing to rebuild since the last successful build'
          }
        >
          {label}
        </button>
        <label className="sys-auto-rebuild" title="rebuild automatically whenever it becomes possible -- waits 90s after the last change to avoid rebuilding on every commit in a burst">
          <input
            type="checkbox"
            checked={repoState.auto_rebuild}
            onChange={e => onSetAutoRebuild(e.target.checked)}
          />
          auto-rebuild
        </label>
        {repoState.auto_rebuild_pending && (
          <span className="sys-repo-auto-pending" title="auto-rebuild is waiting for changes to settle -- bumped back to 90s each time HEAD moves again">
            rebuilding in {repoState.auto_rebuild_seconds ?? 0}s
          </span>
        )}
      </div>
      {repoState.last_error && (
        <div className="sys-repo-error" title={repoState.last_error}>last rebuild failed — see LR's log</div>
      )}
    </div>
  )
}

function SystemPanel({
  state,
  fcState,
  repoState,
  onLaunch,
  onTerminate,
  onRestart,
  onRebuild,
  onSetAutoRebuild,
  onSetAutoUpdate,
}: {
  state: SystemStateMsg | null
  fcState: string
  repoState: RepoStateMsg
  onLaunch: (name: string) => void
  onTerminate: (id: string) => void
  onRestart: () => void
  onRebuild: () => void
  onSetAutoRebuild: (enabled: boolean) => void
  onSetAutoUpdate: (enabled: boolean) => void
}) {
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000))
  const troughEntries = useTrough(repoState.last_error)

  useEffect(() => {
    const id = setInterval(() => setNowSec(Math.floor(Date.now() / 1000)), 1000)
    return () => clearInterval(id)
  }, [])

  if (!state) {
    return <div className="sys-panel sys-panel-empty">waiting for system state…</div>
  }

  const runningCount = (name: string) =>
    state.managed.filter(p => p.name === name && p.status === 'running').length

  const fcRunning = runningCount('federation-command') > 0
  const fcControl =
    fcState === 'remote-control' ? 'remote'
    : fcState === 'local-control' ? 'local'
    : 'not connected'

  return (
    <div className="sys-panel">
      <RepoWatchPanel repoState={repoState} onRebuild={onRebuild} onSetAutoRebuild={onSetAutoRebuild} />
      {fcRunning && (
        <div className={`sys-fc-control sys-fc-control-${fcState || 'none'}`}>
          federation-command control: <strong>{fcControl}</strong>
          {fcControl !== 'remote' && ' — expected remote in a machine-driven chain'}
        </div>
      )}
      <div className="sys-table">
        <div className="sys-row sys-row-head">
          <span className="sys-col sys-col-name">process</span>
          <span className="sys-col sys-col-pid">pid</span>
          <span className="sys-col sys-col-status">status</span>
          <span className="sys-col sys-col-detail">uptime</span>
          <span className="sys-col sys-col-actions" />
        </div>
        <SystemProcRow proc={state.self} nowSec={nowSec} onRestart={onRestart} onSetAutoUpdate={onSetAutoUpdate} />
        {state.managed.map(p => (
          <SystemProcRow key={p.instance_id} proc={p} nowSec={nowSec} onTerminate={onTerminate} />
        ))}
        {state.managed.length === 0 && (
          <div className="sys-row sys-row-none">no managed applications</div>
        )}
      </div>

      <div className="sys-launch">
        <span className="sys-launch-label">launch</span>
        {LAUNCHABLE_APPS.map(({ name, multi }) => {
          const n = runningCount(name)
          return (
            <button
              key={name}
              className="sys-btn sys-btn-launch"
              disabled={!multi && n > 0}
              onClick={() => onLaunch(name)}
            >
              {multi
                ? (n > 0 ? `${name} (+1 · ${n} running)` : name)
                : (n > 0 ? `${name} (running)` : name)}
            </button>
          )
        })}
      </div>
      <Trough entries={troughEntries} />
    </div>
  )
}

/* ---- Files panel ---- */

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`
}

function formatAgo(tsSec: number, nowSec: number): string {
  const secs = Math.max(0, nowSec - tsSec)
  if (secs < 60) return `${secs}s ago`
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`
  return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m ago`
}

function formatCountdown(expiresAtSec: number, nowSec: number): string {
  const secs = expiresAtSec - nowSec
  if (secs <= 0) return 'expiring…'
  if (secs < 60) return `${secs}s left`
  if (secs < 3600) return `${Math.floor(secs / 60)}m left`
  return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m left`
}

// FILE_ICON_PATH is the very small wireframe (outline, not filled) icon set
// this increment supports: text, image, and a catch-all for everything else.
// Each renders in `currentColor`, so FileIcon's state-driven CSS class is
// what actually colors it (orange/yellow/green -- see FILE_STATE_CLASS).
const FILE_ICON_PATH: Record<string, JSX.Element> = {
  text: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round">
      <path d="M6 2h9l5 5v15a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" />
      <path d="M14 2v6h6" strokeLinecap="round" />
      <path d="M8 13h8M8 17h8M8 9h3" strokeLinecap="round" />
    </svg>
  ),
  image: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round">
      <rect x="3" y="4" width="18" height="16" rx="1.5" />
      <circle cx="8.5" cy="9.5" r="1.5" />
      <path d="M21 16.5 15.6 11a1 1 0 0 0-1.4 0L4 21" strokeLinecap="round" />
    </svg>
  ),
  other: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round">
      <path d="M12 2 3 7v10l9 5 9-5V7z" />
      <path d="M3 7l9 5 9-5M12 12v10" strokeLinecap="round" />
    </svg>
  ),
}

// FILE_STATE_CLASS drives the icon color the file-details dialog's
// hold/persist actions describe: orange while short-term cached, yellow once
// held, green once persisted to the host-store.
const FILE_STATE_CLASS: Record<string, string> = {
  cached: 'file-state-cached',
  held: 'file-state-held',
  persisted: 'file-state-persisted',
}

function FileIcon({ kind, state }: { kind: string; state?: string }) {
  const stateClass = FILE_STATE_CLASS[state ?? ''] ?? FILE_STATE_CLASS.cached
  return (
    <span className={`file-icon file-icon-${kind} ${stateClass}`}>
      {FILE_ICON_PATH[kind] ?? FILE_ICON_PATH.other}
    </span>
  )
}

// fileRawUrl/fileDownloadUrl address a host-cache file's bytes directly
// (GET /api/files/<id>, added alongside the viewer/download widgets — see
// local-representative/files.go's handleFileRaw). Inline is what the viewer
// embeds/fetches; ?download=1 gets an attachment Content-Disposition so the
// browser saves it instead of navigating to it.
function fileRawUrl(id: string): string {
  return `/api/files/${encodeURIComponent(id)}`
}

function fileDownloadUrl(id: string): string {
  return `/api/files/${encodeURIComponent(id)}?download=1`
}

// fileHoldUrl/filePersistUrl back the file-details dialog's "hold"/"persist"
// buttons (POST, no body -- see local-representative/files.go's
// handleFileHold/handleFilePersist). fileDeleteUrl backs its "delete" button
// (DELETE, same address as fileRawUrl's GET).
function fileHoldUrl(id: string): string {
  return `/api/files/${encodeURIComponent(id)}/hold`
}

function filePersistUrl(id: string): string {
  return `/api/files/${encodeURIComponent(id)}/persist`
}

// fileHighlightUrl backs the file-details dialog's "highlight" toggle (POST,
// no body -- see local-representative/files.go's handleFileHighlight). A
// plain flip, independent of hold/persist state -- see FileInfo.highlighted.
function fileHighlightUrl(id: string): string {
  return `/api/files/${encodeURIComponent(id)}/highlight`
}

function fileDeleteUrl(id: string): string {
  return `/api/files/${encodeURIComponent(id)}`
}

// markup*Url back the markup dialog (Step1SubstepCPrompt.md Revision F) --
// GET markupUrl fetches the in-progress composite if a session is already
// open (404 otherwise, meaning "start from the plain original"), POST
// markupUrl saves the composite after every completed stroke, and the three
// action routes below back its "commit"/"cancel"/"copy" buttons -- see
// local-representative/files.go's handleMarkupGet and friends.
function markupUrl(id: string): string {
  return `/api/files/${encodeURIComponent(id)}/markup`
}

function markupCommitUrl(id: string): string {
  return `/api/files/${encodeURIComponent(id)}/markup/commit`
}

function markupCancelUrl(id: string): string {
  return `/api/files/${encodeURIComponent(id)}/markup/cancel`
}

function markupCopyUrl(id: string): string {
  return `/api/files/${encodeURIComponent(id)}/markup/copy`
}

function FilesPanel({
  state,
  selectedId,
  onSelect,
  onEnter,
  onUpload,
}: {
  state: FilesStateMsg | null
  selectedId: string | null
  onSelect: (id: string) => void
  onEnter: (id: string) => void
  onUpload?: (files: FileList) => void
}) {
  const [dragging, setDragging] = useState(false)
  const inputRef = useRef<HTMLInputElement | null>(null)
  const files = state?.files ?? []

  return (
    <div className="files-panel">
      {onUpload && (
        <div
          className={`files-dropzone${dragging ? ' files-dropzone-active' : ''}`}
          onDragOver={e => { e.preventDefault(); setDragging(true) }}
          onDragLeave={() => setDragging(false)}
          onDrop={e => {
            e.preventDefault()
            setDragging(false)
            if (e.dataTransfer.files.length > 0) onUpload(e.dataTransfer.files)
          }}
          onClick={() => inputRef.current?.click()}
        >
          <span className="files-dropzone-text">drag files here, or click to browse</span>
          <span className="files-dropzone-hint">uploaded files are removed after 1 hour</span>
          <input
            ref={inputRef}
            type="file"
            multiple
            className="files-dropzone-input"
            onChange={e => {
              if (e.target.files && e.target.files.length > 0) onUpload(e.target.files)
              e.target.value = ''
            }}
          />
        </div>
      )}
      {files.length === 0 ? (
        <div className="files-empty">no files in the host-cache</div>
      ) : (
        <div className="files-grid">
          {files.map(f => (
            <button
              key={f.id}
              className={`files-item${selectedId === f.id ? ' files-item-active' : ''}${f.highlighted ? ' files-item-highlighted' : ''}${f.marked_up ? ' files-item-markedup' : ''}`}
              onClick={() => onSelect(f.id)}
              onDoubleClick={() => onEnter(f.id)}
              title={f.name}
            >
              <FileIcon kind={f.kind} state={f.state} />
              <span className="files-item-name">{f.name}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

function FileDetailPane({
  file,
  onClose,
  onEnter,
  onMarkup,
}: {
  file: FileInfo
  onClose: () => void
  onEnter: (id: string) => void
  onMarkup: (id: string) => void
}) {
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000))
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    const id = setInterval(() => setNowSec(Math.floor(Date.now() / 1000)), 1000)
    return () => clearInterval(id)
  }, [])

  // runAction hits one of the state-changing routes (hold/persist/delete):
  // the files-tab listing itself refreshes via the next "files-state"
  // broadcast, not from this response -- this just reports whether the
  // request was accepted.
  const runAction = useCallback(async (url: string, method: string): Promise<boolean> => {
    setBusy(true)
    try {
      const resp = await fetch(url, { method })
      if (!resp.ok) {
        console.error('file action failed:', method, url, resp.status, await resp.text())
        return false
      }
      return true
    } catch (err) {
      console.error('file action failed:', method, url, err)
      return false
    } finally {
      setBusy(false)
    }
  }, [])

  const handleHoldOrPersist = () => {
    const url = file.state === 'held' ? filePersistUrl(file.id) : fileHoldUrl(file.id)
    void runAction(url, 'POST')
  }

  const handleHighlight = () => {
    void runAction(fileHighlightUrl(file.id), 'POST')
  }

  const handleDelete = () => {
    if (!window.confirm(`Delete "${file.name}"? This can't be undone.`)) return
    void runAction(fileDeleteUrl(file.id), 'DELETE').then(ok => { if (ok) onClose() })
  }

  return (
    <div className="file-detail-pane">
      <div className="file-detail-header">
        <span className="file-detail-title">file details</span>
        <button className="file-detail-close" onClick={onClose}>×</button>
      </div>
      <div className="file-detail-icon"><FileIcon kind={file.kind} state={file.state} /></div>
      <div className="file-detail-rows">
        <div className="file-detail-row">
          <span className="file-detail-label">name</span>
          <span className="file-detail-value" title={file.name}>{file.name}</span>
        </div>
        <div className="file-detail-row">
          <span className="file-detail-label">type</span>
          <span className="file-detail-value">{file.kind}</span>
        </div>
        <div className="file-detail-row">
          <span className="file-detail-label">size</span>
          <span className="file-detail-value">{formatBytes(file.size)}</span>
        </div>
        <div className="file-detail-row">
          <span className="file-detail-label">uploaded</span>
          <span className="file-detail-value">{formatAgo(file.uploaded_at, nowSec)}</span>
        </div>
        <div className="file-detail-row">
          <span className="file-detail-label">{file.state === 'persisted' ? 'status' : 'expires'}</span>
          <span className="file-detail-value">
            {file.state === 'persisted' ? 'persisted — never expires' : formatCountdown(file.expires_at, nowSec)}
          </span>
        </div>
      </div>
      <div className="file-detail-actions">
        <button className="file-detail-enter" onClick={() => onEnter(file.id)} title="Open the viewer">
          view
        </button>
        <a
          className="file-detail-download"
          href={fileDownloadUrl(file.id)}
          download={file.name}
        >
          download
        </a>
      </div>
      {file.kind === 'image' && (
        <div className="file-detail-actions">
          <button
            className={`file-detail-markup${file.marked_up ? ' file-detail-markup-active' : ''}`}
            onClick={() => onMarkup(file.id)}
            title={file.marked_up ? 'keep editing the in-progress markup' : 'draw arrows, rectangles, or text on this image'}
          >
            markup
          </button>
        </div>
      )}
      <div className="file-detail-actions">
        <button
          className={`file-detail-highlight${file.highlighted ? ' file-detail-highlight-active' : ''}`}
          onClick={handleHighlight}
          disabled={busy}
          title="Mark this file at the LR/AC level"
        >
          {file.highlighted ? 'unhighlight' : 'highlight'}
        </button>
      </div>
      <div className="file-detail-actions">
        {file.state !== 'persisted' && (
          <button className="file-detail-hold" onClick={handleHoldOrPersist} disabled={busy}>
            {file.state === 'held' ? 'persist' : 'hold'}
          </button>
        )}
        <button className="file-detail-delete" onClick={handleDelete} disabled={busy}>
          delete
        </button>
      </div>
    </div>
  )
}

/* ---- File viewer ---- */

// FileViewer is the files tab's "drill-down" page, reached by double-clicking
// a grid item or the detail pane's "view" widget (reminiscent of
// condoccer's substep entry). Images render inline; text is fetched and shown
// as plain text; anything else falls back to a "use download" notice — this
// increment's wireframe icon set is deliberately small (text/image/other),
// and so is its preview set.
function FileViewer({
  fileId,
  file,
  onBack,
}: {
  fileId: string
  file: FileInfo | null
  onBack: () => void
}) {
  const [textContent, setTextContent] = useState<string | null>(null)
  const [textError, setTextError] = useState<string | null>(null)
  const rawUrl = fileRawUrl(fileId)

  useEffect(() => {
    setTextContent(null)
    setTextError(null)
    if (!file || file.kind !== 'text') return
    let cancelled = false
    fetch(rawUrl)
      .then(resp => {
        if (!resp.ok) throw new Error(`status ${resp.status}`)
        return resp.text()
      })
      .then(text => { if (!cancelled) setTextContent(text) })
      .catch(err => { if (!cancelled) setTextError(String(err)) })
    return () => { cancelled = true }
  }, [rawUrl, file?.kind])

  return (
    <div className="file-viewer">
      <div className="file-viewer-header">
        <button className="file-viewer-back" onClick={onBack}>← back</button>
        <span className="file-viewer-name" title={file?.name ?? fileId}>{file?.name ?? fileId}</span>
        {file && (
          <a className="file-detail-download" href={fileDownloadUrl(fileId)} download={file.name}>
            download
          </a>
        )}
      </div>
      <div className="file-viewer-body">
        {!file ? (
          <div className="file-viewer-empty">this file is no longer in the host-cache</div>
        ) : file.kind === 'image' ? (
          <img className="file-viewer-image" src={rawUrl} alt={file.name} />
        ) : file.kind === 'text' ? (
          textError ? (
            <div className="file-viewer-empty">couldn't load preview: {textError}</div>
          ) : (
            <pre className="file-viewer-text">{textContent ?? 'loading…'}</pre>
          )
        ) : (
          <div className="file-viewer-empty">no preview available for this file type — use download above</div>
        )}
      </div>
    </div>
  )
}

/* ---- Markup dialog ---- */

// MarkupTool is the markup dialog's small tool set (Step1SubstepCPrompt.md
// Revision F): a click-drag arrow, a click-drag rectangle, and a click-to-place
// text label. Each renders permanently into the canvas the instant its
// stroke finishes (see MarkupDialog's pointer handlers) -- there's no
// per-shape undo, just the flattened result, same MVP spirit as the rest of
// the files tab.
type MarkupTool = 'arrow' | 'rect' | 'text'

// MARKUP_COLORS is the markup dialog's colour palette -- unlike the rest of
// this file's grey-wireframe icon set, these render as-is (actual colour
// swatches), since the whole point is to draw in a colour that stands out
// against the screenshot underneath.
const MARKUP_COLORS = ['#ff3b30', '#ff9500', '#ffcc00', '#34c759', '#0a84ff', '#af52de', '#ffffff', '#111111']

const MARKUP_TOOL_ICON: Record<MarkupTool, JSX.Element> = {
  arrow: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
      <path d="M4 20 18 6" />
      <path d="M9 6h9v9" />
    </svg>
  ),
  rect: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinejoin="round">
      <rect x="4" y="6" width="16" height="12" rx="1" />
    </svg>
  ),
  text: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
      <path d="M5 6h14M12 6v13" />
    </svg>
  ),
}

// canvasPoint converts a mouse event into canvas-backing-store coordinates,
// accounting for the canvas being displayed smaller (via CSS) than its
// natural-resolution backing store -- see MarkupDialog's `.markup-canvas`.
function canvasPoint(e: React.MouseEvent<HTMLCanvasElement>, canvas: HTMLCanvasElement): { x: number; y: number } {
  const rect = canvas.getBoundingClientRect()
  const scaleX = canvas.width / rect.width
  const scaleY = canvas.height / rect.height
  return { x: (e.clientX - rect.left) * scaleX, y: (e.clientY - rect.top) * scaleY }
}

function drawMarkupArrow(ctx: CanvasRenderingContext2D, x0: number, y0: number, x1: number, y1: number, color: string) {
  const headLen = Math.max(14, Math.hypot(x1 - x0, y1 - y0) * 0.18)
  const angle = Math.atan2(y1 - y0, x1 - x0)
  ctx.save()
  ctx.strokeStyle = color
  ctx.fillStyle = color
  ctx.lineWidth = Math.max(3, ctx.canvas.width * 0.005)
  ctx.lineCap = 'round'
  ctx.beginPath()
  ctx.moveTo(x0, y0)
  ctx.lineTo(x1, y1)
  ctx.stroke()
  ctx.beginPath()
  ctx.moveTo(x1, y1)
  ctx.lineTo(x1 - headLen * Math.cos(angle - Math.PI / 6), y1 - headLen * Math.sin(angle - Math.PI / 6))
  ctx.lineTo(x1 - headLen * Math.cos(angle + Math.PI / 6), y1 - headLen * Math.sin(angle + Math.PI / 6))
  ctx.closePath()
  ctx.fill()
  ctx.restore()
}

function drawMarkupRect(ctx: CanvasRenderingContext2D, x0: number, y0: number, x1: number, y1: number, color: string) {
  ctx.save()
  ctx.strokeStyle = color
  ctx.lineWidth = Math.max(3, ctx.canvas.width * 0.005)
  ctx.strokeRect(Math.min(x0, x1), Math.min(y0, y1), Math.abs(x1 - x0), Math.abs(y1 - y0))
  ctx.restore()
}

// MarkupDialog is the "convenient markup system" opened from the file
// details pane's "markup" button on any image (Step1SubstepCPrompt.md
// Revision F). It loads whichever composite is furthest along -- the
// in-progress sidecar if a session is already open (GET markupUrl), else the
// plain original (rawUrl) -- draws directly onto a full-resolution canvas,
// and autosaves the flattened result (POST markupUrl) after every completed
// arrow/rectangle/text stroke, which is what leaves the file "marked up"
// (see FileInfo.marked_up). "cancel"/"commit"/"copy" all close the dialog on
// success; which of the three is used decides whether that in-progress work
// is discarded, baked into the original file, or spun off into a duplicate.
function MarkupDialog({
  file,
  rawUrl,
  onClose,
}: {
  file: FileInfo
  rawUrl: string
  onClose: () => void
}) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const dragStartRef = useRef<{ x: number; y: number } | null>(null)
  const snapshotRef = useRef<ImageData | null>(null)
  const [ready, setReady] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [tool, setTool] = useState<MarkupTool>('arrow')
  const [color, setColor] = useState(MARKUP_COLORS[0])
  const [hasMarkup, setHasMarkup] = useState(file.marked_up)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)

  // Seed the canvas: continue the in-progress composite if one exists,
  // otherwise start fresh from the plain original.
  useEffect(() => {
    let cancelled = false
    let objectUrl: string | null = null
    const canvas = canvasRef.current
    if (!canvas) return
    setReady(false)
    setLoadError(null)

    const draw = (src: string) => {
      const img = new Image()
      img.onload = () => {
        if (cancelled) return
        canvas.width = img.naturalWidth
        canvas.height = img.naturalHeight
        canvas.getContext('2d')!.drawImage(img, 0, 0)
        setReady(true)
      }
      img.onerror = () => { if (!cancelled) setLoadError("couldn't load the image") }
      img.src = src
    }

    fetch(markupUrl(file.id))
      .then(resp => {
        if (cancelled) return
        if (resp.ok) {
          setHasMarkup(true)
          return resp.blob().then(b => {
            if (cancelled) return
            objectUrl = URL.createObjectURL(b)
            draw(objectUrl)
          })
        }
        draw(rawUrl)
      })
      .catch(() => { if (!cancelled) draw(rawUrl) })

    return () => {
      cancelled = true
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [file.id, rawUrl])

  const saveComposite = useCallback(async () => {
    const canvas = canvasRef.current
    if (!canvas) return
    const blob = await new Promise<Blob | null>(resolve => canvas.toBlob(resolve, 'image/jpeg', 0.92))
    if (!blob) return
    const form = new FormData()
    form.append('file', blob, 'markup.jpg')
    try {
      const resp = await fetch(markupUrl(file.id), { method: 'POST', body: form })
      if (resp.ok) setHasMarkup(true)
      else setActionError('failed to save markup')
    } catch {
      setActionError('failed to save markup')
    }
  }, [file.id])

  const handlePointerDown = (e: React.MouseEvent<HTMLCanvasElement>) => {
    if (!ready || busy) return
    const canvas = canvasRef.current!
    const pt = canvasPoint(e, canvas)
    if (tool === 'text') {
      const text = window.prompt('markup text:')
      if (text) {
        const ctx = canvas.getContext('2d')!
        ctx.save()
        ctx.fillStyle = color
        ctx.font = `${Math.max(20, Math.round(canvas.width * 0.028))}px sans-serif`
        ctx.textBaseline = 'top'
        ctx.fillText(text, pt.x, pt.y)
        ctx.restore()
        void saveComposite()
      }
      return
    }
    snapshotRef.current = canvas.getContext('2d')!.getImageData(0, 0, canvas.width, canvas.height)
    dragStartRef.current = pt
  }

  const handlePointerMove = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const start = dragStartRef.current
    const snapshot = snapshotRef.current
    if (!start || !snapshot) return
    const canvas = canvasRef.current!
    const ctx = canvas.getContext('2d')!
    const pt = canvasPoint(e, canvas)
    ctx.putImageData(snapshot, 0, 0)
    if (tool === 'arrow') drawMarkupArrow(ctx, start.x, start.y, pt.x, pt.y, color)
    else if (tool === 'rect') drawMarkupRect(ctx, start.x, start.y, pt.x, pt.y, color)
  }

  const handlePointerUp = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const start = dragStartRef.current
    const snapshot = snapshotRef.current
    if (!start || !snapshot) return
    const canvas = canvasRef.current!
    const ctx = canvas.getContext('2d')!
    const pt = canvasPoint(e, canvas)
    ctx.putImageData(snapshot, 0, 0)
    const moved = Math.hypot(pt.x - start.x, pt.y - start.y) > 2
    if (moved) {
      if (tool === 'arrow') drawMarkupArrow(ctx, start.x, start.y, pt.x, pt.y, color)
      else if (tool === 'rect') drawMarkupRect(ctx, start.x, start.y, pt.x, pt.y, color)
    }
    dragStartRef.current = null
    snapshotRef.current = null
    if (moved) void saveComposite()
  }

  const runAction = async (url: string) => {
    setBusy(true)
    setActionError(null)
    try {
      const resp = await fetch(url, { method: 'POST' })
      if (resp.ok) onClose()
      else setActionError('action failed')
    } catch {
      setActionError('action failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="markup-overlay">
      <div className="markup-dialog">
        <div className="markup-header">
          <span className="markup-title">markup</span>
          <span className="markup-filename" title={file.name}>{file.name}</span>
        </div>
        <div className="markup-canvas-wrap">
          {!ready && !loadError && <div className="markup-status">loading…</div>}
          {loadError && <div className="markup-status markup-status-error">{loadError}</div>}
          <canvas
            ref={canvasRef}
            className="markup-canvas"
            style={{ visibility: ready ? 'visible' : 'hidden' }}
            onMouseDown={handlePointerDown}
            onMouseMove={handlePointerMove}
            onMouseUp={handlePointerUp}
            onMouseLeave={handlePointerUp}
          />
        </div>
        <div className="markup-toolbar">
          <div className="markup-tools">
            {(Object.keys(MARKUP_TOOL_ICON) as MarkupTool[]).map(t => (
              <button
                key={t}
                className={`markup-tool-btn${tool === t ? ' markup-tool-active' : ''}`}
                onClick={() => setTool(t)}
                title={t}
              >
                {MARKUP_TOOL_ICON[t]}
              </button>
            ))}
          </div>
          <div className="markup-palette">
            {MARKUP_COLORS.map(c => (
              <button
                key={c}
                className={`markup-swatch${color === c ? ' markup-swatch-active' : ''}`}
                style={{ background: c }}
                onClick={() => setColor(c)}
                title={c}
              />
            ))}
          </div>
        </div>
        {actionError && <div className="markup-status markup-status-error">{actionError}</div>}
        <div className="markup-actions">
          <button className="markup-btn markup-btn-cancel" onClick={() => runAction(markupCancelUrl(file.id))} disabled={busy}>
            cancel
          </button>
          <button
            className="markup-btn markup-btn-copy"
            onClick={() => runAction(markupCopyUrl(file.id))}
            disabled={busy || !hasMarkup}
            title={hasMarkup ? 'create a file duplicate with the markup included' : 'draw something first'}
          >
            copy
          </button>
          <button
            className="markup-btn markup-btn-commit"
            onClick={() => runAction(markupCommitUrl(file.id))}
            disabled={busy || !hasMarkup}
            title={hasMarkup ? 'edit the markup into the image file directly' : 'draw something first'}
          >
            commit
          </button>
        </div>
      </div>
    </div>
  )
}

// Browser pickup strategy (condocs/initialDistributedDevelopmentImpls/
// BrowserPickupStrategy.md), Layer 2: sessionStorage survives a refresh,
// stays scoped per-tab (so two LR tabs on different condocs don't clobber
// each other), and clears when the tab actually closes rather than pinning
// stale state forever -- the right lifetime for "resume where I was".
function initialTab(): Tab {
  const stored = sessionStorage.getItem('lr-active-tab')
  return (TABS as readonly string[]).includes(stored ?? '') ? (stored as Tab) : 'federation-command'
}

// Small forward/back stack over the app's top-level navigation state
// (Step5Prompt.md Revision M). Deliberately independent of the real browser
// history -- this app never calls pushState (see BrowserPickupStrategy.md on
// why nav already relies on replaceState/sessionStorage instead), so this is
// purely an in-app "last N screens" stack, not a wrapper around back/forward
// button clicks. `max` caps how many screens are remembered.
function useScreenHistory<T>(
  screen: T,
  isEqual: (a: T, b: T) => boolean,
  applyScreen: (screen: T) => void,
  max: number,
) {
  const [hist, setHist] = useState(() => ({ stack: [screen], index: 0 }))

  useEffect(() => {
    setHist(prev => {
      if (isEqual(prev.stack[prev.index], screen)) return prev // e.g. applyScreen just navigated us here
      let stack = [...prev.stack.slice(0, prev.index + 1), screen]
      let index = stack.length - 1
      if (stack.length > max) {
        const drop = stack.length - max
        stack = stack.slice(drop)
        index -= drop
      }
      return { stack, index }
    })
  }, [screen])

  const canBack = hist.index > 0
  const canForward = hist.index < hist.stack.length - 1

  const back = () => {
    if (!canBack) return
    applyScreen(hist.stack[hist.index - 1])
    setHist(prev => (prev.index <= 0 ? prev : { ...prev, index: prev.index - 1 }))
  }

  const forward = () => {
    if (!canForward) return
    applyScreen(hist.stack[hist.index + 1])
    setHist(prev => (prev.index >= prev.stack.length - 1 ? prev : { ...prev, index: prev.index + 1 }))
  }

  return { canBack, canForward, back, forward }
}

function NavArrows({ canBack, canForward, onBack, onForward }: {
  canBack: boolean
  canForward: boolean
  onBack: () => void
  onForward: () => void
}) {
  return (
    <span className="nav-arrows">
      <button
        className={`nav-arrow-btn${canBack ? ' nav-arrow-active' : ''}`}
        onClick={onBack}
        disabled={!canBack}
        title={canBack ? 'back' : 'no earlier screen'}
      >
        ←
      </button>
      <button
        className={`nav-arrow-btn${canForward ? ' nav-arrow-active' : ''}`}
        onClick={onForward}
        disabled={!canForward}
        title={canForward ? 'forward' : 'no later screen'}
      >
        →
      </button>
    </span>
  )
}

// CAMERA_ICON is a grey-palette wireframe icon (Step1SubstepCPrompt.md),
// matching FILE_ICON_PATH's outline style so the quick-feedback screenshot
// button reads as part of the same icon family as the files tab.
const CAMERA_ICON = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round">
    <path d="M9 4 7.5 6H4a1 1 0 0 0-1 1v12a1 1 0 0 0 1 1h16a1 1 0 0 0 1-1V7a1 1 0 0 0-1-1h-3.5L15 4z" strokeLinecap="round" />
    <circle cx="12" cy="13" r="3.5" />
  </svg>
)

// captureScreenshot grabs a single frame of "the current display"
// (Step1SubstepCPrompt.md) via the browser's screen-capture API rather than
// rasterizing the DOM, so it genuinely captures whatever's on screen
// (including, say, an embedded tab's own iframe content) without pulling in
// a DOM-to-canvas dependency. The capture stream is stopped immediately
// after the one frame is drawn -- this is a screenshot, not a recording.
async function captureScreenshot(): Promise<File> {
  const stream = await navigator.mediaDevices.getDisplayMedia({ video: true })
  try {
    const track = stream.getVideoTracks()[0]
    const video = document.createElement('video')
    video.srcObject = stream
    await video.play()
    // Give the first frame a tick to actually land before drawing it.
    await new Promise(resolve => requestAnimationFrame(resolve))
    const canvas = document.createElement('canvas')
    canvas.width = video.videoWidth
    canvas.height = video.videoHeight
    canvas.getContext('2d')!.drawImage(video, 0, 0)
    track.stop()
    const blob = await new Promise<Blob>((resolve, reject) =>
      canvas.toBlob(b => (b ? resolve(b) : reject(new Error('canvas.toBlob returned null'))), 'image/png'),
    )
    return new File([blob], `screenshot-${Date.now()}.png`, { type: 'image/png' })
  } finally {
    stream.getTracks().forEach(t => t.stop())
  }
}

// MIC_ICON follows the same grey-palette wireframe convention as CAMERA_ICON
// (Step1SubstepCPrompt.md) so the mic-availability indicator reads as part of
// the same icon family. Kept identical to agent-coordinator's copy -- see
// App.tsx there -- and to condoccer's per-input mic button, which reuses this
// same path. See
// condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step2Prompt.md.
const MIC_ICON = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
    <rect x="9" y="3" width="6" height="11" rx="3" />
    <path d="M5 11a7 7 0 0 0 14 0" />
    <path d="M12 18v3" />
    <path d="M8 21h8" />
  </svg>
)

// TCAvailabilityIndicator sits beside the camera/screenshot icon in the
// header: illuminated (mic-indicator-active) once agent-coordinator reports
// at least one the-conversationalist instance available on any connected
// host. Mirrors agent-coordinator's own copy -- a passive indicator, not a
// control; the actual mic-to-transcribe action lives on condoccer's own text
// inputs (Step2Prompt.md: "we will keep the TC functionality as contained in
// that sub-app as we can").
function TCAvailabilityIndicator({ available }: { available: boolean }) {
  return (
    <span
      className={`mic-indicator${available ? ' mic-indicator-active' : ''}`}
      title={available ? 'The Conversationalist is available' : 'The Conversationalist is not available on any connected host'}
    >
      {MIC_ICON}
    </span>
  )
}

// ScreenshotButton sits immediately left of the nav arrows. `enabled`
// reflects whether there's at least one file store to save into -- see
// callers for what that means in each app -- independent of `busy`, which
// just covers the capture/upload round-trip so a slow save can't be
// double-fired.
function ScreenshotButton({ enabled, busy, onClick }: { enabled: boolean; busy: boolean; onClick: () => void }) {
  const active = enabled && !busy
  return (
    <button
      className={`screenshot-btn${active ? ' screenshot-btn-active' : ''}`}
      onClick={onClick}
      disabled={!active}
      title={enabled ? (busy ? 'saving screenshot…' : 'save a screenshot to the most-preferred file store') : 'no file store available'}
    >
      {CAMERA_ICON}
    </button>
  )
}

export default function App() {
  const [activeTab, setActiveTab] = useState<Tab>(initialTab)
  const [selectedFileId, setSelectedFileId] = useState<string | null>(null)
  const [viewerFileId, setViewerFileId] = useState<string | null>(null)
  // markupFile is snapshotted at the moment the dialog opens (rather than
  // re-derived from the live filesState listing, like selectedFile/viewerFile
  // below) so an in-flight files-state broadcast can't yank the dialog's
  // target out from under an open editing session.
  const [markupFile, setMarkupFile] = useState<FileInfo | null>(null)
  const [screenshotBusy, setScreenshotBusy] = useState(false)
  // Condoccer is embedded via a same-origin iframe with a hardcoded `src`,
  // so condoccer's own hash-based resume (Layer 1) never survives a refresh
  // of this outer page on its own -- the iframe just remounts at the bare
  // `/condoccer/`. Capture the iframe's hash as it navigates and bake it
  // back into `src` so a refresh here hands condoccer back its resume point.
  const [condoccerHash, setCondoccerHash] = useState(() => sessionStorage.getItem('lr-condoccer-hash') ?? '')
  const condoccerFrameRef = useRef<HTMLIFrameElement>(null)

  const handleCondoccerLoad = () => {
    const win = condoccerFrameRef.current?.contentWindow
    if (!win) return
    const capture = () => {
      setCondoccerHash(win.location.hash)
      sessionStorage.setItem('lr-condoccer-hash', win.location.hash)
    }
    win.addEventListener('hashchange', capture)
    capture() // in case condoccer already restored a hash before this attached
  }
  const {
    connected, services, fcState, fcLog,
    ridealongState, condocState, acState, systemState, repoState, filesState, modeMismatches, tcAvailable,
    sendCommand, sendRidealongCommand, connectToAC, disconnectFromAC, setAutoConnectAC,
    launchApp, terminateApp, restartApp, uploadFiles, rebuildRepo, setAutoRebuild, setAutoUpdate,
  } = useStatusWS()

  const devMode = systemState?.self.dev_mode ?? false
  const mismatches = Object.values(modeMismatches)
  const isEmbedTab = EMBED_TABS.has(activeTab)

  const getStatus = (name: string): string => {
    return services.find(s => s.name === name)?.status ?? 'healthy'
  }

  const selectedFile = activeTab === 'files' && !viewerFileId
    ? filesState?.files.find(f => f.id === selectedFileId) ?? null
    : null
  const viewerFile = activeTab === 'files' && viewerFileId
    ? filesState?.files.find(f => f.id === viewerFileId) ?? null
    : null

  // Files tab picker gets a yellow dot (Step5SubstepR Revision E) whenever a
  // file is highlighted, so highlighting something is discoverable without
  // having to keep the files tab open. Double-clicking the dot jumps
  // straight to whichever file was highlighted first.
  const firstHighlightedFile = filesState?.files.find(f => f.highlighted) ?? null

  useEffect(() => {
    sessionStorage.setItem('lr-active-tab', activeTab)
  }, [activeTab])

  const goToTab = (tab: Tab) => {
    setActiveTab(tab)
    setSelectedFileId(null)
    setViewerFileId(null)
  }

  const goToFirstHighlighted = () => {
    if (!firstHighlightedFile) return
    setActiveTab('files')
    setSelectedFileId(null)
    setViewerFileId(firstHighlightedFile.id)
  }

  // "At least one file store available" (Step1SubstepCPrompt.md): LR always
  // owns its own host-cache (defaultFileCacheDir in files.go), unlike AC
  // which only ever reaches one by relaying through a selected host -- so
  // here that's unconditionally true. uploadFiles below already prefers a
  // cloud cache over the host-cache wherever one exists; today that's
  // nowhere, so every screenshot lands in the host-cache.
  const hasFileStore = true

  const handleScreenshot = async () => {
    if (screenshotBusy) return
    setScreenshotBusy(true)
    try {
      const file = await captureScreenshot()
      await uploadFiles([file])
    } catch (err) {
      console.error('screenshot failed:', err)
    } finally {
      setScreenshotBusy(false)
    }
  }

  const nav = useScreenHistory(activeTab, (a, b) => a === b, goToTab, NAV_HISTORY_MAX)

  return (
    <div className={`app${devMode ? ' app-dev-mode' : ''}`}>
      {mismatches.length > 0 && (
        <div className="mode-mismatch-banner">
          ⚠ dev/ops mode mismatch — {mismatches.map(m => `${m.peer} (${m.peer_mode})`).join(', ')}:
          only health information is exchanged until this is resolved. See docs/DevMode.md.
        </div>
      )}
      <ACConnectionPanel
        acState={acState}
        onConnect={connectToAC}
        onDisconnect={disconnectFromAC}
        onSetAutoConnect={setAutoConnectAC}
      />
      <div className="tab-bar">
        <span className="app-title">local-representative</span>
        <div className="tabs">
          {TABS.map(tab => (
            <button
              key={tab}
              className={`tab${activeTab === tab ? ' tab-active' : ''}`}
              onClick={() => goToTab(tab)}
            >
              {tab}
              {tab === 'files' && firstHighlightedFile && (
                <span
                  className="tab-highlight-dot"
                  title="a file is highlighted — double-click to go to it"
                  onClick={e => e.stopPropagation()}
                  onDoubleClick={e => { e.stopPropagation(); goToFirstHighlighted() }}
                />
              )}
            </button>
          ))}
        </div>
        <span className="header-version-tag" title="build version">{__APP_VERSION__}</span>
        <TCAvailabilityIndicator available={tcAvailable} />
        <ScreenshotButton enabled={hasFileStore} busy={screenshotBusy} onClick={handleScreenshot} />
        <NavArrows canBack={nav.canBack} canForward={nav.canForward} onBack={nav.back} onForward={nav.forward} />
        <span
          className={`conn-dot${connected ? ' conn-dot-ok' : ' conn-dot-err'}`}
          title={connected ? 'connected' : 'disconnected'}
        />
      </div>
      {isEmbedTab && (
        <div className={`embed-status-bar health-${getStatus(activeTab)}`}>
          <span className="health-dot" />
          <span className="health-label">{activeTab} — {getStatus(activeTab)}</span>
        </div>
      )}
      <div className={`main-pane${isEmbedTab ? ' main-pane-embed' : ''}`}>
        {isEmbedTab ? (
          getStatus(activeTab) === 'healthy' ? (
            activeTab === 'condoccer' ? (
              <iframe
                ref={condoccerFrameRef}
                className="embed-frame"
                src={`/condoccer/${condoccerHash}`}
                title="condoccer"
                onLoad={handleCondoccerLoad}
              />
            ) : activeTab === 'sessions' ? (
              <iframe className="embed-frame" src="/sessions/" title="sessions" />
            ) : (
              <iframe className="embed-frame" src="/convo/" title="convo" />
            )
          ) : (
            <div className="service-empty service-empty-embed">
              {activeTab} is not running on this host — launch it from the system tab
            </div>
          )
        ) : (
          <div className={`main-pane-inner${selectedFile ? ' with-detail' : ''}`}>
            <div className="service-view">
              <div className="service-name">{activeTab}</div>
              {activeTab === 'system' ? (
                <SystemPanel
                  state={systemState}
                  fcState={fcState}
                  repoState={repoState}
                  onLaunch={launchApp}
                  onTerminate={terminateApp}
                  onRestart={restartApp}
                  onRebuild={rebuildRepo}
                  onSetAutoRebuild={setAutoRebuild}
                  onSetAutoUpdate={setAutoUpdate}
                />
              ) : activeTab === 'files' && viewerFileId ? (
                <FileViewer
                  fileId={viewerFileId}
                  file={viewerFile}
                  onBack={() => setViewerFileId(null)}
                />
              ) : activeTab === 'files' ? (
                <FilesPanel
                  state={filesState}
                  selectedId={selectedFileId}
                  onSelect={setSelectedFileId}
                  onEnter={setViewerFileId}
                  onUpload={uploadFiles}
                />
              ) : (
                <>
                  <div className={`health-indicator health-${getStatus(activeTab)}`}>
                    <span className="health-dot" />
                    <span className="health-label">{getStatus(activeTab)}</span>
                  </div>
                  {activeTab === 'federation-command' && (
                    <>
                      {ridealongState && (
                        <RidealongPanel
                          state={ridealongState}
                          fcState={fcState}
                          sendRidealongCommand={sendRidealongCommand}
                        />
                      )}
                      {condocState && !ridealongState && (
                        <CondocPanel
                          state={condocState}
                          fcState={fcState}
                        />
                      )}
                      <FCCommandPanel
                        fcState={fcState}
                        fcLog={fcLog}
                        sendCommand={sendCommand}
                      />
                    </>
                  )}
                </>
              )}
            </div>
            {selectedFile && (
              <FileDetailPane
                file={selectedFile}
                onClose={() => setSelectedFileId(null)}
                onEnter={setViewerFileId}
                onMarkup={id => setMarkupFile(filesState?.files.find(f => f.id === id) ?? null)}
              />
            )}
          </div>
        )}
      </div>
      {markupFile && (
        <MarkupDialog
          file={markupFile}
          rawUrl={fileRawUrl(markupFile.id)}
          onClose={() => setMarkupFile(null)}
        />
      )}
    </div>
  )
}
