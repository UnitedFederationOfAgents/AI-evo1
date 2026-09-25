import { useState, useEffect, useCallback, useRef } from 'react'
import type { ServiceStatus, StatusMsg, FCStateMsg, FCLogMsg, RidealongStateMsg, CondocStateMsg, ACStateMsg, ProcInfo, SystemStateMsg, FileInfo, FilesStateMsg, ModeMismatchMsg, RepoStateMsg } from './types'

const TABS = ['federation-command', 'condoccer', 'worker', 'system', 'files'] as const
type Tab = typeof TABS[number]

// Applications the system tab offers a launch button for. `multi` apps are
// N-per-host (launch stays enabled while instances run); others are singletons.
const LAUNCHABLE_APPS: { name: string; multi: boolean }[] = [
  { name: 'federation-command', multi: true },
  { name: 'condoccer', multi: false },
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
    connected, services, fcState, fcLog, ridealongState, condocState, acState, systemState, repoState, filesState, modeMismatches,
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

function fileDeleteUrl(id: string): string {
  return `/api/files/${encodeURIComponent(id)}`
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
              className={`files-item${selectedId === f.id ? ' files-item-active' : ''}`}
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
}: {
  file: FileInfo
  onClose: () => void
  onEnter: (id: string) => void
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
          enter →
        </button>
        <a
          className="file-detail-download"
          href={fileDownloadUrl(file.id)}
          download={file.name}
        >
          download
        </a>
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
// a grid item or the detail pane's "enter →" widget (reminiscent of
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

// Browser pickup strategy (condocs/initialDistributedDevelopmentImpls/
// BrowserPickupStrategy.md), Layer 2: sessionStorage survives a refresh,
// stays scoped per-tab (so two LR tabs on different condocs don't clobber
// each other), and clears when the tab actually closes rather than pinning
// stale state forever -- the right lifetime for "resume where I was".
function initialTab(): Tab {
  const stored = sessionStorage.getItem('lr-active-tab')
  return (TABS as readonly string[]).includes(stored ?? '') ? (stored as Tab) : 'federation-command'
}

export default function App() {
  const [activeTab, setActiveTab] = useState<Tab>(initialTab)
  const [selectedFileId, setSelectedFileId] = useState<string | null>(null)
  const [viewerFileId, setViewerFileId] = useState<string | null>(null)
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
    ridealongState, condocState, acState, systemState, repoState, filesState, modeMismatches,
    sendCommand, sendRidealongCommand, connectToAC, disconnectFromAC, setAutoConnectAC,
    launchApp, terminateApp, restartApp, uploadFiles, rebuildRepo, setAutoRebuild, setAutoUpdate,
  } = useStatusWS()

  const devMode = systemState?.self.dev_mode ?? false
  const mismatches = Object.values(modeMismatches)

  const getStatus = (name: string): string => {
    return services.find(s => s.name === name)?.status ?? 'healthy'
  }

  const selectedFile = activeTab === 'files' && !viewerFileId
    ? filesState?.files.find(f => f.id === selectedFileId) ?? null
    : null
  const viewerFile = activeTab === 'files' && viewerFileId
    ? filesState?.files.find(f => f.id === viewerFileId) ?? null
    : null

  useEffect(() => {
    sessionStorage.setItem('lr-active-tab', activeTab)
  }, [activeTab])

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
              onClick={() => { setActiveTab(tab); setSelectedFileId(null); setViewerFileId(null) }}
            >
              {tab}
            </button>
          ))}
        </div>
        <span
          className={`conn-dot${connected ? ' conn-dot-ok' : ' conn-dot-err'}`}
          title={connected ? 'connected' : 'disconnected'}
        />
      </div>
      <div className="main-pane">
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
                {activeTab === 'condoccer' && (
                  getStatus('condoccer') === 'healthy' ? (
                    <iframe
                      ref={condoccerFrameRef}
                      className="condoccer-frame"
                      src={`/condoccer/${condoccerHash}`}
                      title="condoccer"
                      onLoad={handleCondoccerLoad}
                    />
                  ) : (
                    <div className="service-empty">
                      condoccer is not running on this host — launch it from the system tab
                    </div>
                  )
                )}
              </>
            )}
          </div>
          {selectedFile && (
            <FileDetailPane
              file={selectedFile}
              onClose={() => setSelectedFileId(null)}
              onEnter={setViewerFileId}
            />
          )}
        </div>
      </div>
    </div>
  )
}
