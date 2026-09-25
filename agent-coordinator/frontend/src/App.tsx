import { useState, useEffect, useCallback, useRef } from 'react'
import type {
  Host, HostsMsg, LRStateMsg, LRFCStateMsg, LRFCLogMsg,
  LRRidealongMsg, LRCondocMsg, LRSystemStateMsg, LRRepoStateMsg, LRCondoccerMsg, LRFilesMsg, FileInfo, ProcInfo, ServiceStatus,
  SelfInfoMsg, ModeMismatchMsg,
} from './types'

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

interface HostClientState {
  lrState?: LRStateMsg
  fcState: string
  fcLog: LogEntry[]
  ridealong?: LRRidealongMsg
  condoc?: LRCondocMsg
  system?: LRSystemStateMsg
  repo?: LRRepoStateMsg
  condoccer?: LRCondoccerMsg
  files?: LRFilesMsg
}

function emptyHostState(): HostClientState {
  return { fcState: '', fcLog: [] }
}

function useCoordinatorWS() {
  const [connected, setConnected] = useState(false)
  const [hosts, setHosts] = useState<Host[]>([])
  const [hostData, setHostData] = useState<Record<string, HostClientState>>({})
  const [devMode, setDevMode] = useState(false)
  // The connected host (if any) that agent-coordinator itself runs on -- see
  // GlobalTopologyPanel's self-card collapsing. null until self-info arrives
  // or when it discloses no id.
  const [selfHostId, setSelfHostId] = useState<string | null>(null)
  // agent-coordinator's own restart-ability -- mirrors a local-representative
  // self row's loader_managed/update_available, but for AC itself (see
  // types.ts SelfInfoMsg and Step4Prompt.md Revision E).
  const [acLoaderManaged, setACLoaderManaged] = useState(false)
  const [acUpdateAvailable, setACUpdateAvailable] = useState(false)
  // LR host id -> current mismatch disclosure -- see docs/DevMode.md.
  const [modeMismatches, setModeMismatches] = useState<Record<string, ModeMismatchMsg>>({})
  const wsRef = useRef<WebSocket | null>(null)
  const retryRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const fcStateRefs = useRef<Record<string, string>>({})

  const sendLRCommand = useCallback((hostId: string, cmd: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-command', payload: { host_id: hostId, cmd } }))
  }, [])

  const sendLRRidealongCommand = useCallback((hostId: string, action: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-ridealong-command', payload: { host_id: hostId, action } }))
  }, [])

  const sendLRLaunchApp = useCallback((hostId: string, name: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-launch-app', payload: { host_id: hostId, name } }))
  }, [])

  const sendLRTerminateApp = useCallback((hostId: string, id: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-terminate-app', payload: { host_id: hostId, id } }))
  }, [])

  // Restarts one managed sub-application instance on the given host's LR
  // (terminate the running instance, then launch a fresh one of the same
  // app) -- distinct from sendLRRestartApp, which restarts LR itself. See
  // Step4Prompt.md Revision H.
  const sendLRRestartManagedApp = useCallback((hostId: string, id: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-restart-managed-app', payload: { host_id: hostId, id } }))
  }, [])

  // Restarts the selected host's local-representative itself (not
  // agent-coordinator) -- only expected to come back up when it's
  // loader-managed; see docs/DevMode.md "Loader".
  const sendLRRestartApp = useCallback((hostId: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-restart-app', payload: { host_id: hostId } }))
  }, [])

  // Dev-repo watcher controls for the selected host's LR (--dev-repo, see
  // docs/DevMode.md): sendLRRebuildApp runs 'make deploy-dev-binaries' at the
  // watched repo's root; sendLRSetAutoRebuild toggles the auto-rebuild flag.
  const sendLRRebuildApp = useCallback((hostId: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-rebuild-app', payload: { host_id: hostId } }))
  }, [])

  const sendLRSetAutoRebuild = useCallback((hostId: string, enabled: boolean) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-set-auto-rebuild', payload: { host_id: hostId, enabled } }))
  }, [])

  // Restarts agent-coordinator itself (not any host's LR) -- only expected to
  // come back up when it's loader-managed; mirrors sendLRRestartApp but for
  // AC's own process, with no host to target (see Step4Prompt.md Revision E).
  const sendACRestartApp = useCallback(() => {
    wsRef.current?.send(JSON.stringify({ type: 'ac-restart-app', payload: {} }))
  }, [])

  const selectHost = useCallback((hostId: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'select-host', payload: { host_id: hostId } }))
  }, [])

  // File upload is a plain HTTP POST to AC's dedicated upload-relay route
  // (not a websocket command), which streams the multipart body straight
  // through to the selected host's local-representative -- AC never keeps its
  // own copy. See handleFileUploadRelay / docs/DistributedExchange.md, Path 1.
  const uploadFiles = useCallback(async (hostId: string, fileList: FileList | File[]) => {
    const files = Array.from(fileList)
    if (files.length === 0) return
    const form = new FormData()
    for (const f of files) form.append('file', f)
    try {
      const resp = await fetch(`/host/${encodeURIComponent(hostId)}/api/files`, { method: 'POST', body: form })
      if (!resp.ok) {
        console.error('file upload failed:', resp.status, await resp.text())
      }
    } catch (err) {
      console.error('file upload failed:', err)
    }
  }, [])

  const connect = useCallback(() => {
    const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${wsProto}//${window.location.host}/ws`)
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
          case 'self-info': {
            const p = msg.payload as SelfInfoMsg
            setDevMode(p.dev_mode)
            setSelfHostId(p.host_id || null)
            setACLoaderManaged(p.loader_managed)
            setACUpdateAvailable(p.update_available)
            // A rebuild+restart is invisible to an already-open tab -- the
            // reconnect above is the only signal it gets. Compare the
            // server's own reported version against this bundle's
            // build-time version and reload if they differ; skip under the
            // Vite dev server, where HMR already keeps the tab current and
            // the two are never expected to match (see
            // condocs/initialDistributedDevelopmentImpls/BrowserRefreshStrategy.md).
            if (!import.meta.env.DEV && p.version && p.version !== __APP_VERSION__) {
              window.location.reload()
            }
            break
          }
          case 'mode-mismatch': {
            const payload = msg.payload as ModeMismatchMsg
            setModeMismatches(prev => {
              const next = { ...prev }
              if (payload.mismatched) next[payload.host_id] = payload
              else delete next[payload.host_id]
              return next
            })
            break
          }
          case 'hosts':
            setHosts((msg.payload as HostsMsg).hosts)
            break
          case 'lr-state': {
            const p = msg.payload as LRStateMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), lrState: p },
            }))
            break
          }
          case 'lr-fc-state': {
            const p = msg.payload as LRFCStateMsg
            const newSt = p.state === 'disconnected' ? '' : p.state
            const prevSt = fcStateRefs.current[p.host_id] ?? ''
            fcStateRefs.current[p.host_id] = newSt
            const stateEntry: LogEntry | null = prevSt !== newSt ? {
              kind: 'state',
              text: newSt === '' ? '-- disconnected --'
                : newSt === 'remote-control' ? '-- remote control --'
                : newSt === 'local-control' ? '-- local control --'
                : `-- ${newSt} --`,
            } : null
            setHostData(prev => {
              const cur = prev[p.host_id] ?? emptyHostState()
              return {
                ...prev,
                [p.host_id]: {
                  ...cur,
                  fcState: newSt,
                  fcLog: stateEntry ? [...cur.fcLog, stateEntry] : cur.fcLog,
                },
              }
            })
            break
          }
          case 'lr-fc-log': {
            const p = msg.payload as LRFCLogMsg
            const entry: LogEntry = {
              kind: p.kind === 'output' ? 'output' : 'cmd',
              text: p.line,
            }
            setHostData(prev => {
              const cur = prev[p.host_id] ?? emptyHostState()
              return {
                ...prev,
                [p.host_id]: {
                  ...cur,
                  fcLog: [...cur.fcLog.slice(-199), entry],
                },
              }
            })
            break
          }
          case 'lr-ridealong-state': {
            const p = msg.payload as LRRidealongMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), ridealong: p.active ? p : undefined },
            }))
            break
          }
          case 'lr-condoc-state': {
            const p = msg.payload as LRCondocMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), condoc: p.active ? p : undefined },
            }))
            break
          }
          case 'lr-system-state': {
            const p = msg.payload as LRSystemStateMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), system: p.active ? p : undefined },
            }))
            break
          }
          case 'lr-repo-state': {
            const p = msg.payload as LRRepoStateMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), repo: p.watched ? p : undefined },
            }))
            break
          }
          case 'lr-condoccer-state': {
            const p = msg.payload as LRCondoccerMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), condoccer: p.available ? p : undefined },
            }))
            break
          }
          case 'lr-files-state': {
            const p = msg.payload as LRFilesMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), files: p.active ? p : undefined },
            }))
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
    connected, hosts, hostData, selectHost, devMode, selfHostId, modeMismatches,
    acLoaderManaged, acUpdateAvailable,
    sendLRCommand, sendLRRidealongCommand, sendLRLaunchApp, sendLRTerminateApp, uploadFiles,
    sendLRRestartApp, sendLRRestartManagedApp, sendLRRebuildApp, sendLRSetAutoRebuild, sendACRestartApp,
  }
}

function hostDotClass(status: string): string {
  return status === 'connected' ? 'host-dot-connected' : 'host-dot-disconnected'
}

function HostSidebar({
  hosts, selectedHostId, onSelect, onSelectGlobal,
}: {
  hosts: Host[]
  selectedHostId: string | null
  onSelect: (id: string) => void
  onSelectGlobal: () => void
}) {
  return (
    <div className="sidebar">
      <div className="sidebar-header">hosts</div>
      {/* The global selection sits above the per-host list and is mutually
          exclusive with picking a particular host -- selectedHostId === null
          means global, which is also the default on first load. */}
      <div
        className={`host-item global-item${selectedHostId === null ? ' host-item-active' : ''}`}
        onClick={onSelectGlobal}
      >
        <span className="global-icon">◎</span>
        <span className="host-label">global</span>
      </div>
      {hosts.length === 0 ? (
        <div className="sidebar-empty">no hosts connected</div>
      ) : (
        hosts.map(host => (
          <div
            key={host.id}
            className={`host-item${selectedHostId === host.id ? ' host-item-active' : ''}`}
            onClick={() => onSelect(host.id)}
          >
            <span className={`host-dot ${hostDotClass(host.status)}`} />
            <span className="host-label">{host.label}</span>
          </div>
        ))
      )}
    </div>
  )
}

const LR_SERVICES = ['federation-command', 'condoccer', 'worker'] as const
// "system" and "files" sit to the right of the service tabs, mirroring
// local-representative's own dashboard: they drive/view that LR's process
// management and host-cache from the coordinator. Upload on this files tab is
// relayed through AC's dedicated upload-relay route rather than AC keeping
// its own copy of the file (see docs/DistributedExchange.md, Path 1).
const LR_TABS = [...LR_SERVICES, 'system', 'files'] as const
type LRTab = typeof LR_TABS[number]

function FCCommandPanel({
  hostId, fcState, fcLog, sendLRCommand,
}: {
  hostId: string
  fcState: string
  fcLog: LogEntry[]
  sendLRCommand: (hostId: string, cmd: string) => void
}) {
  const [input, setInput] = useState('')
  const logEndRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    logEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [fcLog])

  const submit = () => {
    const cmd = input.trim()
    if (!cmd) return
    sendLRCommand(hostId, cmd)
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
            onKeyDown={e => { if (e.key === 'Enter') submit() }}
            autoFocus
          />
          <button className="fc-cmd-send" onClick={submit}>run</button>
        </div>
      )}
    </div>
  )
}

function RidealongPanel({
  hostId, state, fcState, sendLRRidealongCommand,
}: {
  hostId: string
  state: LRRidealongMsg
  fcState: string
  sendLRRidealongCommand: (hostId: string, action: string) => void
}) {
  const [customCmd, setCustomCmd] = useState('')
  const canControl = fcState === 'remote-control'

  const totalSteps = state.total_steps ?? 0
  const currentIndex = state.current_index ?? 0
  const stepLabel = totalSteps > 0 ? `${currentIndex + 1} / ${totalSteps}` : ''

  const submitCustom = () => {
    const text = customCmd.trim()
    if (!text) return
    sendLRRidealongCommand(hostId, `custom:${text}`)
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
          <span className="ra-step-text">{state.next_cmd}</span>
        </div>
      </div>
      {canControl && (
        <div className="ra-controls">
          <div className="ra-actions">
            <button className="ra-btn ra-btn-primary" onClick={() => sendLRRidealongCommand(hostId, 'execute')}>
              execute
            </button>
            <button
              className={`ra-btn ${state.autoplay ? 'ra-btn-active' : ''}`}
              onClick={() => sendLRRidealongCommand(hostId, 'autoplay')}
            >
              {state.autoplay ? 'stop autoplay' : 'autoplay'}
            </button>
            <button className="ra-btn ra-btn-danger" onClick={() => sendLRRidealongCommand(hostId, 'exit')}>
              exit
            </button>
          </div>
          {state.waypoints && state.waypoints.length > 0 && (
            <div className="ra-waypoints">
              <span className="ra-waypoints-label">waypoints:</span>
              {state.waypoints.map(wp => (
                <button key={wp} className="ra-btn ra-btn-waypoint" onClick={() => sendLRRidealongCommand(hostId, `waypoint:${wp}`)}>
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

function CondocPanel({ state, fcState }: { state: LRCondocMsg; fcState: string }) {
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
        {state.status_msg && <div className="condoc-msg">{state.status_msg}</div>}
      </div>
      {!canControl && (
        <div className="ra-observe-hint">observing — switch to remote control to drive</div>
      )}
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
  proc, nowSec, onTerminate, onRestart, onRestartManaged,
}: {
  proc: ProcInfo
  nowSec: number
  onTerminate?: (id: string) => void
  onRestart?: () => void
  onRestartManaged?: (id: string) => void
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
          {!proc.managed && <span className="sys-self-tag">this LR</span>}
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
        {proc.managed && onRestartManaged && (
          <button
            className={`sys-btn sys-btn-restart${proc.update_available ? ' sys-btn-restart-update' : ''}`}
            title={proc.update_available
              ? 'a newer build has landed on disk — terminate this instance and launch a new one with it'
              : 'terminate this instance and launch a fresh one of the same application'}
            onClick={() => onRestartManaged(proc.instance_id)}
          >
            {proc.update_available ? 'restart and update' : 'restart'}
          </button>
        )}
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
                ? 'a newer build has landed on disk — terminate this LR so ufa-loader relaunches it with the new binary'
                : 'terminate this LR so ufa-loader relaunches it with the identical config')
              : 'not loader-managed — run under ufa-loader (see make run-loader) to enable'}
            onClick={onRestart}
          >
            {proc.update_available ? 'update and restart' : 'restart'}
          </button>
        )}
      </span>
    </div>
  )
}

// RepoWatchPanel mirrors local-representative's own dev-repo watcher widget
// (--dev-repo, see docs/DevMode.md): the rebuild button turns orange and
// reads "dirty" while the watched repo has uncommitted changes -- but stays
// disabled, since rebuilding a dirty tree would silently bake in unreviewed
// changes. It's selectable, plain, and reads "rebuild" only once HEAD has
// moved since the last build with the repo clean; otherwise it's disabled.
// Rendered only when that host's LR was actually launched with --dev-repo.
function RepoWatchPanel({
  repoState, onRebuild, onSetAutoRebuild,
}: {
  repoState: LRRepoStateMsg | undefined
  onRebuild: () => void
  onSetAutoRebuild: (enabled: boolean) => void
}) {
  if (!repoState?.watched) return null

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
        <div className="sys-repo-error" title={repoState.last_error}>last rebuild failed — see that host's LR log</div>
      )}
    </div>
  )
}

function SystemPanel({
  hostId, state, active, fcState, repoState, onLaunch, onTerminate, onRestart, onRestartManaged, onRebuild, onSetAutoRebuild,
}: {
  hostId: string
  state: LRSystemStateMsg | undefined
  active: boolean
  fcState: string
  repoState: LRRepoStateMsg | undefined
  onLaunch: (hostId: string, name: string) => void
  onTerminate: (hostId: string, id: string) => void
  onRestart: (hostId: string) => void
  onRestartManaged: (hostId: string, id: string) => void
  onRebuild: (hostId: string) => void
  onSetAutoRebuild: (hostId: string, enabled: boolean) => void
}) {
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000))

  useEffect(() => {
    const id = setInterval(() => setNowSec(Math.floor(Date.now() / 1000)), 1000)
    return () => clearInterval(id)
  }, [])

  if (!active) {
    return <div className="service-empty">local-representative on this host is not connected</div>
  }
  if (!state) {
    return <div className="sys-panel sys-panel-empty">waiting for system state…</div>
  }

  const managed = state.managed ?? []
  const runningCount = (name: string) =>
    managed.filter(p => p.name === name && p.status === 'running').length

  const fcRunning = runningCount('federation-command') > 0
  const fcControl =
    fcState === 'remote-control' ? 'remote'
    : fcState === 'local-control' ? 'local'
    : 'not connected'

  return (
    <div className="sys-panel">
      <RepoWatchPanel
        repoState={repoState}
        onRebuild={() => onRebuild(hostId)}
        onSetAutoRebuild={enabled => onSetAutoRebuild(hostId, enabled)}
      />
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
        <SystemProcRow proc={state.self} nowSec={nowSec} onRestart={() => onRestart(hostId)} />
        {managed.map(p => (
          <SystemProcRow
            key={p.instance_id}
            proc={p}
            nowSec={nowSec}
            onTerminate={id => onTerminate(hostId, id)}
            onRestartManaged={id => onRestartManaged(hostId, id)}
          />
        ))}
        {managed.length === 0 && (
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
              onClick={() => onLaunch(hostId, name)}
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

/* ---- Files panel (upload relayed through AC -- see docs/DistributedExchange.md) ---- */

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

// fileRawUrl/fileDownloadUrl address a file's bytes through AC's existing
// /host/<id>/* transparent reverse proxy — local-representative's
// raw-serving route (GET /api/files/<id>) isn't gated on the proxied-request
// header the way uploads are, so viewing/downloading works the same here as
// connected directly to that LR. Upload (a POST to the same /api/files path)
// takes a different route, AC's dedicated upload-relay handler — see
// docs/DistributedExchange.md. The file-details dialog's hold/persist/delete
// actions (fileHoldUrl/filePersistUrl/fileDeleteUrl below) aren't gated
// either -- see local-representative/files.go's relayedUploadHeader comment
// for why that's a deliberate difference from upload -- so they also go
// through this same transparent proxy unmodified.
function fileRawUrl(hostId: string, id: string): string {
  return `/host/${encodeURIComponent(hostId)}/api/files/${encodeURIComponent(id)}`
}

function fileDownloadUrl(hostId: string, id: string): string {
  return `/host/${encodeURIComponent(hostId)}/api/files/${encodeURIComponent(id)}?download=1`
}

function fileHoldUrl(hostId: string, id: string): string {
  return `/host/${encodeURIComponent(hostId)}/api/files/${encodeURIComponent(id)}/hold`
}

function filePersistUrl(hostId: string, id: string): string {
  return `/host/${encodeURIComponent(hostId)}/api/files/${encodeURIComponent(id)}/persist`
}

function fileDeleteUrl(hostId: string, id: string): string {
  return `/host/${encodeURIComponent(hostId)}/api/files/${encodeURIComponent(id)}`
}

function FilesPanel({
  files, active, selectedId, onSelect, onEnter, onUpload,
}: {
  files: FileInfo[]
  active: boolean
  selectedId: string | null
  onSelect: (id: string) => void
  onEnter: (id: string) => void
  onUpload: (files: FileList) => void
}) {
  const [dragging, setDragging] = useState(false)
  const inputRef = useRef<HTMLInputElement | null>(null)

  if (!active) {
    return <div className="service-empty">local-representative on this host is not connected</div>
  }
  return (
    <div className="files-panel">
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
        <span className="files-dropzone-hint">relayed through agent-coordinator — removed after 1 hour</span>
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
  file, hostId, onClose, onEnter,
}: {
  file: FileInfo
  hostId: string
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
  // the files-tab listing itself refreshes via the next "lr-files-state"
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
    const url = file.state === 'held' ? filePersistUrl(hostId, file.id) : fileHoldUrl(hostId, file.id)
    void runAction(url, 'POST')
  }

  const handleDelete = () => {
    if (!window.confirm(`Delete "${file.name}"? This can't be undone.`)) return
    void runAction(fileDeleteUrl(hostId, file.id), 'DELETE').then(ok => { if (ok) onClose() })
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
        <a className="file-detail-download" href={fileDownloadUrl(hostId, file.id)} download={file.name}>
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

// FileViewer mirrors local-representative's own files-tab viewer, reached the
// same way (double-click a grid item, or the detail pane's "enter →"), just
// fetching through this host's /host/<id>/* proxy instead of same-origin.
function FileViewer({
  hostId, fileId, file, onBack,
}: {
  hostId: string
  fileId: string
  file: FileInfo | null
  onBack: () => void
}) {
  const [textContent, setTextContent] = useState<string | null>(null)
  const [textError, setTextError] = useState<string | null>(null)
  const rawUrl = fileRawUrl(hostId, fileId)

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
          <a className="file-detail-download" href={fileDownloadUrl(hostId, fileId)} download={file.name}>
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

/* ---- Global view ----
 *
 * The global selection sits above the per-host list (App.tsx's
 * selectedHostId === null) and shows a net-centric alternative to a single
 * host's dashboard. Every one of a host's tabs (LR_TABS) has a global
 * counterpart in principle, but only 'system' has one implemented so far --
 * the rest render the same "not yet implemented" placeholder until a later
 * increment gives them a real view (Step4Prompt.md).
 */

// GLOBAL_SYSTEM_TABS: nested tabs within the global system tab's main-view
// selector. Only 'topology' is populated this increment -- it's not
// interactive yet, just visible. 'timeline' is a placeholder.
const GLOBAL_SYSTEM_TABS = ['topology', 'timeline'] as const
type GlobalSystemTab = typeof GLOBAL_SYSTEM_TABS[number]

// serviceHealthy reports whether `services` (a host's lrState.services, as
// also used by LRView's getServiceStatus) lists `name` as healthy -- drives
// whether that sub-application's box in the topology diagram renders green.
function serviceHealthy(services: ServiceStatus[] | undefined, name: string): boolean {
  return services?.find(s => s.name === name)?.status === 'healthy'
}

// subAppOutOfDate reports whether `managed` (a host's system.managed, keyed
// by app name the same way ServiceStatus.name is -- representable only
// tracks one connection identity per app name today) lists `name` with
// update_available set: that application's on-disk binary now differs from
// the version its connected instance last reported (see
// local-representative/procman.go's pollManagedVersions). Drives the orange
// halo drawn around that sub-application's green "connected" box in the
// topology diagram (Step4Prompt.md Revision D) -- LR itself is excluded
// because hostOutOfDate already covers it at the whole-card level.
function subAppOutOfDate(managed: ProcInfo[] | undefined, name: string): boolean {
  return !!managed?.find(p => p.name === name)?.update_available
}

// subAppManaged reports whether `managed` lists `name` at all -- a
// sub-application can be connected (see serviceHealthy) without LR having
// launched it (e.g. run manually and pointed at LR's address), in which case
// it never appears here. Drives the topology diagram's distinction (Step4Prompt.md
// Revision K) between a managed connected box (green border, matching LR)
// and a merely-connected one (green abbreviation, but the border stays the
// same grey as an unlit box) -- LR itself is always "managed" by definition
// so this only applies to FC/CO/W.
function subAppManaged(managed: ProcInfo[] | undefined, name: string): boolean {
  return !!managed?.find(p => p.name === name)
}

// subAppBoxClass builds an FC/CO/W box's className: connected-and-managed
// gets the full green treatment (border + text, matching the LR box);
// connected-but-unmanaged (see subAppManaged) gets only the green
// abbreviation, leaving the border the same grey as an unlit box
// (Step4Prompt.md Revision K). outdated layers its halo on top of either, and
// never applies unless healthy is also true (see subAppOutOfDate).
function subAppBoxClass(healthy: boolean, managed: boolean, outdated: boolean): string {
  const healthClass = healthy ? (managed ? ' topo-node-box-healthy' : ' topo-node-box-unmanaged') : ''
  return `topo-node-box${healthClass}${outdated ? ' topo-node-box-outdated' : ''}`
}

// hostRebuildReady reports whether this host's LR is watching a --dev-repo
// whose "rebuild" control would be enabled right now (HEAD has moved past
// the last successful build and the repo is clean) -- the same condition
// RepoWatchPanel uses to un-disable its own rebuild button. Factored out of
// hostOutOfDate so the global "rebuild all" control (Step4Prompt.md
// Revision J) can ask the same question across every connected host at once.
function hostRebuildReady(data: HostClientState | undefined): boolean {
  return !!data?.repo?.watched && !data.repo.building && !!data.repo.rebuild_ready
}

// hostOutOfDate reports whether a host's LR is behind the dev branch it's
// tracking -- true while either the "rebuild" control would be enabled (see
// hostRebuildReady) or the "restart" control would read "update and restart"
// (a newer build has already landed on disk but isn't running yet). Drives
// the topology card's orange halo (Step4Prompt.md Revision C); dev-mode-only,
// so callers gate this on the viewing agent-coordinator's own devMode.
function hostOutOfDate(data: HostClientState | undefined): boolean {
  const updateAvailable = !!data?.system?.self?.update_available
  return hostRebuildReady(data) || updateAvailable
}

// hostAnySubAppUpdateAvailable reports whether any of this host's connected
// FC/CO/W sub-applications is running an older build than what's on disk --
// the same per-app check that drives an individual box's own halo (see
// subAppOutOfDate), OR'd across the three. Factored out so both the
// self-host-only "host update all" button and the network-wide "network
// update all" button (Step4Prompt.md Revisions F/G and J) share one
// definition of "this host has a pending sub-app update".
function hostAnySubAppUpdateAvailable(data: HostClientState | undefined): boolean {
  const services = data?.lrState?.services
  const managed = data?.system?.managed
  return ['federation-command', 'condoccer', 'worker'].some(
    name => serviceHealthy(services, name) && subAppOutOfDate(managed, name)
  )
}

// TopologyNodeCard is one node in the topology main pane: the "self" card
// (agent-coordinator collapsed together with its own LR box -- there's no
// separate self-only panel any more) is pinned to its own row above the
// per-host cards, which are selectable and color their sub-application boxes
// green once that host's LR reports them healthy -- fully green (border and
// abbreviation) once LR also launched it (see subAppManaged), or just the
// abbreviation, with the border left the same grey as an unlit box, when it's
// merely connected without LR managing it (Step4Prompt.md Revision K).
// outOfDate draws a faint orange halo around the whole card (dev mode only)
// without displacing the grey-vs-blue selected indication -- see
// hostOutOfDate. devMode additionally draws that same halo around an
// individual FC/CO/W box's own green border once it's connected but out of
// date (see subAppOutOfDate) -- LR/AC never get one; the card-level halo
// already implies it for LR.
function TopologyNodeCard({
  label, status, isSelf, services, managed, devMode, selected, outOfDate, onClick,
}: {
  label: string
  status?: string
  isSelf?: boolean
  services?: ServiceStatus[]
  managed?: ProcInfo[]
  devMode?: boolean
  selected?: boolean
  outOfDate?: boolean
  onClick?: () => void
}) {
  const fcHealthy = serviceHealthy(services, 'federation-command')
  const coHealthy = serviceHealthy(services, 'condoccer')
  const wHealthy = serviceHealthy(services, 'worker')
  const fcManaged = subAppManaged(managed, 'federation-command')
  const coManaged = subAppManaged(managed, 'condoccer')
  const wManaged = subAppManaged(managed, 'worker')
  const fcOutdated = devMode && fcHealthy && subAppOutOfDate(managed, 'federation-command')
  const coOutdated = devMode && coHealthy && subAppOutOfDate(managed, 'condoccer')
  const wOutdated = devMode && wHealthy && subAppOutOfDate(managed, 'worker')

  return (
    <div
      className={`topo-node${isSelf ? ' topo-node-self' : ''}${selected ? ' topo-node-selected' : ''}${onClick ? ' topo-node-clickable' : ''}${outOfDate ? ' topo-node-outdated' : ''}`}
      title={outOfDate ? "this host's LR is behind the dev branch it's tracking — see details & control" : undefined}
      onClick={onClick}
    >
      <div className="topo-node-header">
        {status && <span className={`host-dot ${hostDotClass(status)}`} />}
        <span className="topo-node-label">{label}</span>
        {isSelf && <span className="topo-node-self-tag">self</span>}
      </div>
      <div className="topo-node-diagram">
        <div className="topo-node-row topo-node-row-top">
          {isSelf && <span className="topo-node-box topo-node-box-ac">AC</span>}
          <span className="topo-node-box topo-node-box-lr">LR</span>
        </div>
        <div className="topo-node-row topo-node-row-bottom">
          <span
            className={subAppBoxClass(fcHealthy, fcManaged, !!fcOutdated)}
            title={fcOutdated ? "federation-command is connected but running an older build than what's on disk" : undefined}
          >FC</span>
          <span
            className={subAppBoxClass(coHealthy, coManaged, !!coOutdated)}
            title={coOutdated ? "condoccer is connected but running an older build than what's on disk" : undefined}
          >CO</span>
          <span
            className={subAppBoxClass(wHealthy, wManaged, !!wOutdated)}
            title={wOutdated ? "worker is connected but running an older build than what's on disk" : undefined}
          >W</span>
        </div>
      </div>
    </div>
  )
}

// GlobalTopologyPanel: main pane of host cards (left, agent-coordinator's own
// card always on top, a faint divider below it) plus a details-and-control
// pane (right) -- see
// condocs/initialDistributedDevelopmentImpls/global_topology_panel.jpg.
// Selecting a host card drives the details pane's readouts and its restart
// control (which sends that host's LR a restart, same as the per-host system
// tab's self-restart button). When selfHostId names a currently-connected
// host, that's the host agent-coordinator itself runs on -- its card
// collapses the AC box together with that host's own LR/FC/CO/W card (one
// panel, keeping the host's real name) and it's selectable like any other
// host. Without a match (no co-located LR connected), a static,
// unselectable "agent-coordinator" placeholder card is shown instead, same
// as before. Everything else here (live per-app data beyond health) is still
// a placeholder. devMode gates the out-of-date halo and the rebuild/restart
// controls below (Step4Prompt.md Revision C) -- both are meaningless outside
// a dev workflow. When the selected card is the self host, the details pane
// also grows a small "agent-coordinator" section below the rebuild/restart
// controls with its own restart button, since that selection's restart
// control above only ever targets that host's LR -- restarting AC itself is
// a separate action (Step4Prompt.md Revision E) -- its rebuild & restart
// controls sibling above is labeled "restart LR"/"restart and update LR" to
// keep the two unambiguous now that they sit side by side. That same
// agent-coordinator section also grows a "host update all" button, enabled
// once any connected sub-application on this host is out of date (see
// anySubAppUpdateAvailable) -- but pressing it only actually restarts
// whichever of this host's LR / AC itself is the one running a stale
// binary (each checked independently, same willUpdate/acWillUpdate flags
// the controls above already use), rather than always restarting both
// (Step4Prompt.md Revision F, narrowed by Revision G). Two more controls
// (Revision J) round out that section: a green "rebuild all" button plus its
// accompanying auto-rebuild toggle at the top, sweeping every connected
// host's dev-repo watcher instead of just the selected one; and a
// blue-or-orange "network update all" button at the bottom, the same
// selective-restart effect as "host update all" but generalized to every
// connected host's LR plus AC, rather than just the AC host.
function GlobalTopologyPanel({
  hosts, hostData, selfHostId, devMode, sendLRRestartApp, sendLRRebuildApp, sendLRSetAutoRebuild,
  acLoaderManaged, acUpdateAvailable, sendACRestartApp,
}: {
  hosts: Host[]
  hostData: Record<string, HostClientState>
  selfHostId: string | null
  devMode: boolean
  sendLRRestartApp: (hostId: string) => void
  sendLRRebuildApp: (hostId: string) => void
  sendLRSetAutoRebuild: (hostId: string, enabled: boolean) => void
  acLoaderManaged: boolean
  acUpdateAvailable: boolean
  sendACRestartApp: () => void
}) {
  const [selectedHostId, setSelectedHostId] = useState<string | null>(null)
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000))

  useEffect(() => {
    const id = setInterval(() => setNowSec(Math.floor(Date.now() / 1000)), 1000)
    return () => clearInterval(id)
  }, [])

  const selectedHost = hosts.find(h => h.id === selectedHostId) ?? null
  const selectedData = selectedHostId ? hostData[selectedHostId] : undefined
  const selfProc = selectedData?.system?.self
  const canRestart = !!selectedHostId && !!selfProc?.loader_managed
  const willUpdate = devMode && !!selfProc?.update_available
  // The selected card is the one agent-coordinator itself runs on -- see
  // GlobalTopologyPanel's self-card collapsing above.
  const isSelfSelected = !!selectedHostId && selectedHostId === selfHostId
  const acWillUpdate = devMode && acUpdateAvailable

  const selfHost = selfHostId ? hosts.find(h => h.id === selfHostId) ?? null : null
  const otherHosts = selfHost ? hosts.filter(h => h.id !== selfHost.id) : hosts

  // Drives the "host update all" button below: true once any connected
  // sub-application on the AC host itself is running an older build than
  // what's on disk (same per-app check TopologyNodeCard uses for that box's
  // own halo -- see hostAnySubAppUpdateAvailable) -- LR/AC's own staleness
  // isn't counted here since they're what the button updates, not what it's
  // watching for.
  const selfHostData = selfHost ? hostData[selfHost.id] : undefined
  const anySubAppUpdateAvailable = devMode && hostAnySubAppUpdateAvailable(selfHostData)
  const canUpdateAll = anySubAppUpdateAvailable && !!selfHostData?.system?.self?.loader_managed && acLoaderManaged

  // allHosts: every connected host, self first when known -- the set both new
  // agent-coordinator-section controls below (Step4Prompt.md Revision J)
  // sweep across, since unlike the controls above them these two aren't
  // scoped to whichever single host card is selected.
  const allHosts = selfHost ? [selfHost, ...otherHosts] : otherHosts

  // "rebuild all": green, enabled once any connected host's LR has a
  // rebuild ready (see hostRebuildReady); clicking rebuilds only those hosts,
  // leaving already-up-to-date ones untouched.
  const rebuildableHosts = devMode ? allHosts.filter(h => hostRebuildReady(hostData[h.id])) : []
  const anyRebuildReady = rebuildableHosts.length > 0
  const handleRebuildAll = () => rebuildableHosts.forEach(h => sendLRRebuildApp(h.id))

  // Accompanying auto-rebuild toggle-selector: applies to every connected
  // host with a watched dev-repo (not just ones currently rebuild-ready,
  // since auto-rebuild is a standing setting, not a one-shot action).
  // Reads as checked only when every watched repo already has it on.
  const watchedHosts = devMode ? allHosts.filter(h => hostData[h.id]?.repo?.watched) : []
  const allAutoRebuildOn = watchedHosts.length > 0 && watchedHosts.every(h => !!hostData[h.id]?.repo?.auto_rebuild)
  const handleSetAutoRebuildAll = (enabled: boolean) =>
    watchedHosts.forEach(h => sendLRSetAutoRebuild(h.id, enabled))

  // "network update all": the same selective-restart effect as "host update
  // all" above, generalized to every connected host's LR plus AC -- each
  // host's own `update_available` (not its sub-apps') decides whether that
  // host's LR gets restarted, same as the "restart LR"/"restart and update
  // LR" control above already does for whichever single host is selected.
  const staleHosts = allHosts.filter(h => devMode && !!hostData[h.id]?.system?.self?.update_available)
  const restartableStaleHosts = staleHosts.filter(h => !!hostData[h.id]?.system?.self?.loader_managed)
  const anyNetworkUpdateAvailable = staleHosts.length > 0 || acWillUpdate
  const canNetworkUpdateAll = restartableStaleHosts.length > 0 || (acWillUpdate && acLoaderManaged)
  const handleNetworkUpdateAll = () => {
    restartableStaleHosts.forEach(h => sendLRRestartApp(h.id))
    if (acWillUpdate && acLoaderManaged) sendACRestartApp()
  }

  return (
    <div className="topo-panel">
      <div className="topo-main">
        {selfHost ? (
          <TopologyNodeCard
            label={selfHost.label}
            status={selfHost.status}
            isSelf
            services={hostData[selfHost.id]?.lrState?.services}
            managed={hostData[selfHost.id]?.system?.managed}
            devMode={devMode}
            selected={selectedHostId === selfHost.id}
            outOfDate={devMode && hostOutOfDate(hostData[selfHost.id])}
            onClick={() => setSelectedHostId(prev => prev === selfHost.id ? null : selfHost.id)}
          />
        ) : (
          <TopologyNodeCard label="agent-coordinator" isSelf />
        )}
        {otherHosts.length > 0 && <div className="topo-divider" />}
        {otherHosts.map(h => (
          <TopologyNodeCard
            key={h.id}
            label={h.label}
            status={h.status}
            services={hostData[h.id]?.lrState?.services}
            managed={hostData[h.id]?.system?.managed}
            devMode={devMode}
            selected={selectedHostId === h.id}
            outOfDate={devMode && hostOutOfDate(hostData[h.id])}
            onClick={() => setSelectedHostId(prev => prev === h.id ? null : h.id)}
          />
        ))}
      </div>
      <div className="topo-details">
        <div className="topo-details-header">details &amp; control</div>
        <div className="topo-readout-row">
          <span className="topo-readout-label">Host</span>
          <span className={`topo-readout-value${selectedHost ? '' : ' topo-readout-placeholder'}`}>
            {selectedHost?.label ?? '—'}
          </span>
        </div>
        <div className="topo-readout-row">
          <span className="topo-readout-label">status</span>
          <span className={`topo-readout-value${selectedHost ? '' : ' topo-readout-placeholder'}`}>
            {selectedHost?.status ?? '—'}
          </span>
        </div>
        <div className="topo-readout-row">
          <span className="topo-readout-label">version</span>
          <span className={`topo-readout-value${selfProc?.version ? '' : ' topo-readout-placeholder'}`}>
            {selfProc?.version ?? '—'}
          </span>
        </div>
        <div className="topo-readout-row">
          <span className="topo-readout-label">uptime</span>
          <span className={`topo-readout-value${selfProc ? '' : ' topo-readout-placeholder'}`}>
            {selfProc ? formatUptime(selfProc.started_at, nowSec) : '—'}
          </span>
        </div>
        <div className="topo-controls">
          <div className="topo-controls-label">rebuild &amp; restart controls</div>
          {devMode && (
            <RepoWatchPanel
              repoState={selectedData?.repo}
              onRebuild={() => selectedHostId && sendLRRebuildApp(selectedHostId)}
              onSetAutoRebuild={enabled => selectedHostId && sendLRSetAutoRebuild(selectedHostId, enabled)}
            />
          )}
          <div className="topo-controls-buttons">
            <button
              className={`sys-btn sys-btn-restart${willUpdate ? ' sys-btn-restart-update' : ''}`}
              disabled={!canRestart}
              title={
                !selectedHostId
                  ? 'select a host to enable'
                  : !canRestart
                  ? 'not loader-managed — run under ufa-loader (see make run-loader) to enable'
                  : willUpdate
                  ? 'a newer build has landed on disk — terminate this LR so ufa-loader relaunches it with the new binary'
                  : "terminate that host's LR so ufa-loader relaunches it with the identical config"
              }
              onClick={() => selectedHostId && sendLRRestartApp(selectedHostId)}
            >
              {willUpdate ? 'restart and update LR' : 'restart LR'}
            </button>
          </div>
          {!selectedHostId && (
            <div className="topo-controls-hint">select a host to enable rebuild/restart controls</div>
          )}
        </div>
        {isSelfSelected && (
          <div className="topo-ac-controls">
            <div className="topo-controls-label">agent-coordinator</div>
            {devMode && (
              <div className="topo-controls-buttons">
                <button
                  className="sys-btn sys-btn-rebuild"
                  disabled={!anyRebuildReady}
                  title={
                    anyRebuildReady
                      ? 'runs make deploy-dev-binaries on every connected host whose dev-repo has moved past its last rebuild'
                      : 'no connected host has a rebuild ready'
                  }
                  onClick={handleRebuildAll}
                >
                  rebuild all
                </button>
                <label
                  className="sys-auto-rebuild"
                  title="rebuild automatically, per host, whenever it becomes possible -- toggles auto-rebuild for every connected host's dev-repo watcher at once"
                >
                  <input
                    type="checkbox"
                    disabled={watchedHosts.length === 0}
                    checked={allAutoRebuildOn}
                    onChange={e => handleSetAutoRebuildAll(e.target.checked)}
                  />
                  auto-rebuild
                </label>
              </div>
            )}
            <div className="topo-controls-buttons">
              <button
                className={`sys-btn sys-btn-restart${acWillUpdate ? ' sys-btn-restart-update' : ''}`}
                disabled={!acLoaderManaged}
                title={
                  !acLoaderManaged
                    ? 'not loader-managed — run under ufa-loader (see make run-loader) to enable'
                    : acWillUpdate
                    ? 'a newer build has landed on disk — terminate agent-coordinator so ufa-loader relaunches it with the new binary'
                    : 'terminate agent-coordinator so ufa-loader relaunches it with the identical config'
                }
                onClick={sendACRestartApp}
              >
                {acWillUpdate ? 'restart and update AC' : 'restart AC'}
              </button>
              <button
                className={`sys-btn sys-btn-restart${anySubAppUpdateAvailable ? ' sys-btn-restart-update' : ''}`}
                disabled={!canUpdateAll}
                title={
                  !anySubAppUpdateAvailable
                    ? 'no sub-application on this host has a pending update'
                    : !canUpdateAll
                    ? 'not loader-managed — run under ufa-loader (see make run-loader) to enable'
                    : willUpdate && acWillUpdate
                    ? "terminate this host's LR, then agent-coordinator, so ufa-loader relaunches both with their new binaries"
                    : willUpdate
                    ? "terminate this host's LR so ufa-loader relaunches it with its new binary — agent-coordinator is already up to date"
                    : acWillUpdate
                    ? "terminate agent-coordinator so ufa-loader relaunches it with its new binary — this host's LR is already up to date"
                    : "this host's LR and agent-coordinator are both already up to date"
                }
                onClick={() => {
                  if (selfHost && willUpdate) sendLRRestartApp(selfHost.id)
                  if (acWillUpdate) sendACRestartApp()
                }}
              >
                host update all
              </button>
            </div>
            <div className="topo-controls-buttons">
              <button
                className={`sys-btn sys-btn-restart${anyNetworkUpdateAvailable ? ' sys-btn-restart-update' : ''}`}
                disabled={!canNetworkUpdateAll}
                title={
                  !anyNetworkUpdateAvailable
                    ? 'every connected host and agent-coordinator are already up to date'
                    : !canNetworkUpdateAll
                    ? 'not loader-managed — run under ufa-loader (see make run-loader) to enable'
                    : "terminate every connected host's LR that has a newer build waiting, plus agent-coordinator itself if it does, so ufa-loader relaunches each with its new binary"
                }
                onClick={handleNetworkUpdateAll}
              >
                network update all
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function GlobalSystemPanel({
  hosts, hostData, selfHostId, devMode, sendLRRestartApp, sendLRRebuildApp, sendLRSetAutoRebuild,
  acLoaderManaged, acUpdateAvailable, sendACRestartApp,
}: {
  hosts: Host[]
  hostData: Record<string, HostClientState>
  selfHostId: string | null
  devMode: boolean
  sendLRRestartApp: (hostId: string) => void
  sendLRRebuildApp: (hostId: string) => void
  sendLRSetAutoRebuild: (hostId: string, enabled: boolean) => void
  acLoaderManaged: boolean
  acUpdateAvailable: boolean
  sendACRestartApp: () => void
}) {
  const [subTab, setSubTab] = useState<GlobalSystemTab>('topology')
  return (
    <div className="global-sys-panel">
      <div className="tab-bar tab-bar-nested">
        <div className="tabs">
          {GLOBAL_SYSTEM_TABS.map(t => (
            <button
              key={t}
              className={`tab${subTab === t ? ' tab-active' : ''}`}
              onClick={() => setSubTab(t)}
            >
              {t}
            </button>
          ))}
        </div>
      </div>
      {subTab === 'topology' ? (
        <GlobalTopologyPanel
          hosts={hosts}
          hostData={hostData}
          selfHostId={selfHostId}
          devMode={devMode}
          sendLRRestartApp={sendLRRestartApp}
          sendLRRebuildApp={sendLRRebuildApp}
          sendLRSetAutoRebuild={sendLRSetAutoRebuild}
          acLoaderManaged={acLoaderManaged}
          acUpdateAvailable={acUpdateAvailable}
          sendACRestartApp={sendACRestartApp}
        />
      ) : (
        <div className="service-empty">not yet implemented</div>
      )}
    </div>
  )
}

function GlobalView({
  hosts, hostData, selfHostId, devMode, sendLRRestartApp, sendLRRebuildApp, sendLRSetAutoRebuild, activeTab, setActiveTab,
  acLoaderManaged, acUpdateAvailable, sendACRestartApp,
}: {
  hosts: Host[]
  hostData: Record<string, HostClientState>
  selfHostId: string | null
  devMode: boolean
  sendLRRestartApp: (hostId: string) => void
  sendLRRebuildApp: (hostId: string) => void
  sendLRSetAutoRebuild: (hostId: string, enabled: boolean) => void
  activeTab: LRTab
  setActiveTab: (tab: LRTab) => void
  acLoaderManaged: boolean
  acUpdateAvailable: boolean
  sendACRestartApp: () => void
}) {
  return (
    <div className="lr-view">
      <div className="lr-header">
        <span className="lr-host-label">global</span>
      </div>
      <div className="tab-bar">
        <div className="tabs">
          {LR_TABS.map(t => (
            <button
              key={t}
              className={`tab${activeTab === t ? ' tab-active' : ''}`}
              onClick={() => setActiveTab(t)}
            >
              {t}
            </button>
          ))}
        </div>
      </div>
      <div className="main-pane">
        {activeTab === 'system' ? (
          <GlobalSystemPanel
            hosts={hosts}
            hostData={hostData}
            selfHostId={selfHostId}
            devMode={devMode}
            sendLRRestartApp={sendLRRestartApp}
            sendLRRebuildApp={sendLRRebuildApp}
            sendLRSetAutoRebuild={sendLRSetAutoRebuild}
            acLoaderManaged={acLoaderManaged}
            acUpdateAvailable={acUpdateAvailable}
            sendACRestartApp={sendACRestartApp}
          />
        ) : (
          <div className="service-empty">not yet implemented</div>
        )}
      </div>
    </div>
  )
}

function LRView({
  host, data, sendLRCommand, sendLRRidealongCommand, sendLRLaunchApp, sendLRTerminateApp,
  sendLRRestartApp, sendLRRestartManagedApp, sendLRRebuildApp, sendLRSetAutoRebuild, uploadFiles, activeTab, setActiveTab,
}: {
  host: Host
  data: HostClientState
  sendLRCommand: (hostId: string, cmd: string) => void
  sendLRRidealongCommand: (hostId: string, action: string) => void
  sendLRLaunchApp: (hostId: string, name: string) => void
  sendLRTerminateApp: (hostId: string, id: string) => void
  sendLRRestartApp: (hostId: string) => void
  sendLRRestartManagedApp: (hostId: string, id: string) => void
  sendLRRebuildApp: (hostId: string) => void
  sendLRSetAutoRebuild: (hostId: string, enabled: boolean) => void
  uploadFiles: (hostId: string, files: FileList) => void
  activeTab: LRTab
  setActiveTab: (tab: LRTab) => void
}) {
  const [selectedFileId, setSelectedFileId] = useState<string | null>(null)
  const [viewerFileId, setViewerFileId] = useState<string | null>(null)
  const lrState = data.lrState
  const active = lrState?.active ?? false

  const getServiceStatus = (name: string): string => {
    if (!active) return 'unknown'
    return lrState?.services?.find((s: ServiceStatus) => s.name === name)?.status ?? 'unknown'
  }

  const files = data.files?.files ?? []
  const selectedFile = activeTab === 'files' && !viewerFileId
    ? files.find(f => f.id === selectedFileId) ?? null
    : null
  const viewerFile = activeTab === 'files' && viewerFileId
    ? files.find(f => f.id === viewerFileId) ?? null
    : null

  return (
    <div className="lr-view">
      <div className="lr-header">
        <span className="lr-host-label">{host.label}</span>
        <span className={`lr-conn-badge${active ? ' lr-conn-badge-active' : ' lr-conn-badge-inactive'}`}>
          {active ? 'connected' : 'not connected'}
        </span>
      </div>
      <div className="tab-bar">
        <div className="tabs">
          {LR_TABS.map(svc => (
            <button
              key={svc}
              className={`tab${activeTab === svc ? ' tab-active' : ''}`}
              onClick={() => { setActiveTab(svc); setSelectedFileId(null); setViewerFileId(null) }}
            >
              {svc}
            </button>
          ))}
        </div>
      </div>
      <div className="main-pane">
        <div className={`main-pane-inner${selectedFile ? ' with-detail' : ''}`}>
          <div className="service-view">
            <div className="service-name">{activeTab}</div>
            {activeTab !== 'system' && activeTab !== 'files' && (
              <div className={`health-indicator health-${getServiceStatus(activeTab)}`}>
                <span className="health-dot" />
                <span className="health-label">{getServiceStatus(activeTab)}</span>
              </div>
            )}
            {activeTab === 'system' && (
              <SystemPanel
                hostId={host.id}
                state={data.system}
                active={active}
                fcState={data.fcState}
                repoState={data.repo}
                onLaunch={sendLRLaunchApp}
                onTerminate={sendLRTerminateApp}
                onRestart={sendLRRestartApp}
                onRestartManaged={sendLRRestartManagedApp}
                onRebuild={sendLRRebuildApp}
                onSetAutoRebuild={sendLRSetAutoRebuild}
              />
            )}
            {activeTab === 'files' && viewerFileId ? (
              <FileViewer
                hostId={host.id}
                fileId={viewerFileId}
                file={viewerFile}
                onBack={() => setViewerFileId(null)}
              />
            ) : activeTab === 'files' && (
              <FilesPanel
                files={files}
                active={active}
                selectedId={selectedFileId}
                onSelect={setSelectedFileId}
                onEnter={setViewerFileId}
                onUpload={f => uploadFiles(host.id, f)}
              />
            )}
            {activeTab === 'federation-command' && (
              <>
                {data.ridealong && (
                  <RidealongPanel
                    hostId={host.id}
                    state={data.ridealong}
                    fcState={data.fcState}
                    sendLRRidealongCommand={sendLRRidealongCommand}
                  />
                )}
                {data.condoc && !data.ridealong && (
                  <CondocPanel state={data.condoc} fcState={data.fcState} />
                )}
                <FCCommandPanel
                  hostId={host.id}
                  fcState={data.fcState}
                  fcLog={data.fcLog}
                  sendLRCommand={sendLRCommand}
                />
              </>
            )}
            {activeTab === 'condoccer' && active && (
              data.condoccer ? (
                <iframe
                  className="condoccer-frame"
                  src={`/host/${host.id}/condoccer/`}
                  title={`condoccer on ${host.label}`}
                />
              ) : (
                <div className="service-empty">
                  condoccer is not running on this host — launch it from the system tab
                </div>
              )
            )}
            {activeTab !== 'federation-command' && activeTab !== 'system' && activeTab !== 'files' && !active && (
              <div className="service-empty">local-representative on this host is not connected</div>
            )}
          </div>
          {selectedFile && (
            <FileDetailPane
              file={selectedFile}
              hostId={host.id}
              onClose={() => setSelectedFileId(null)}
              onEnter={setViewerFileId}
            />
          )}
        </div>
      </div>
    </div>
  )
}

export default function App() {
  const {
    connected, hosts, hostData, selectHost, devMode, selfHostId, modeMismatches,
    acLoaderManaged, acUpdateAvailable,
    sendLRCommand, sendLRRidealongCommand, sendLRLaunchApp, sendLRTerminateApp, uploadFiles,
    sendLRRestartApp, sendLRRestartManagedApp, sendLRRebuildApp, sendLRSetAutoRebuild, sendACRestartApp,
  } = useCoordinatorWS()
  const mismatches = Object.values(modeMismatches)
  const [selectedHostId, setSelectedHostId] = useState<string | null>(null)
  // Shared across the global view and any host's view, so switching between
  // them (selecting/deselecting a host) keeps whichever tab was active
  // instead of resetting it.
  const [activeTab, setActiveTab] = useState<LRTab>('system')
  // Mobile nav drawer: the host sidebar becomes an off-canvas panel below the
  // `mobile-breakpoint` width (see index.css), same treatment as condoccer's
  // sidebar. Desktop layout is untouched -- this state has no visible effect
  // above the breakpoint.
  const [mobileNavOpen, setMobileNavOpen] = useState(false)

  const handleSelectHost = (id: string) => {
    setSelectedHostId(id)
    selectHost(id)
    setMobileNavOpen(false)
  }

  // Global is the default landing view (selectedHostId === null) and is
  // mutually exclusive with a particular host selection.
  const handleSelectGlobal = () => {
    setSelectedHostId(null)
    setMobileNavOpen(false)
  }

  const selectedHost = hosts.find(h => h.id === selectedHostId) ?? null

  return (
    <div className={`app${devMode ? ' app-dev-mode' : ''}`}>
      {mismatches.length > 0 && (
        <div className="mode-mismatch-banner">
          ⚠ dev/ops mode mismatch — {mismatches.map(m => `${m.host_id} (${m.peer_mode})`).join(', ')}:
          only health information is exchanged until this is resolved. See docs/DevMode.md.
        </div>
      )}
      <div className="app-header">
        <span className="app-title">agent-coordinator</span>
        <span
          className={`conn-dot${connected ? ' conn-dot-ok' : ' conn-dot-err'}`}
          title={connected ? 'connected' : 'disconnected'}
        />
      </div>
      <div className="app-body">
        <button
          className="mobile-nav-toggle"
          aria-label="Open hosts"
          onClick={() => setMobileNavOpen(true)}
        >
          ☰
        </button>

        {/* Direct one-tap "go up" to the host list, without opening the
            drawer first -- mirrors condoccer's mobile-back-btn. */}
        {selectedHost && (
          <button
            className="mobile-back-btn"
            aria-label="Back to hosts"
            onClick={() => setSelectedHostId(null)}
          >
            ‹
          </button>
        )}

        {mobileNavOpen && (
          <div className="mobile-nav-backdrop" onClick={() => setMobileNavOpen(false)} />
        )}

        <div className={`sidebar-wrap${mobileNavOpen ? ' mobile-open' : ''}`}>
          <HostSidebar
            hosts={hosts}
            selectedHostId={selectedHostId}
            onSelect={handleSelectHost}
            onSelectGlobal={handleSelectGlobal}
          />
        </div>
        <div className="content">
          {selectedHost ? (
            <LRView
              host={selectedHost}
              data={hostData[selectedHost.id] ?? emptyHostState()}
              sendLRCommand={sendLRCommand}
              sendLRRidealongCommand={sendLRRidealongCommand}
              sendLRLaunchApp={sendLRLaunchApp}
              sendLRTerminateApp={sendLRTerminateApp}
              sendLRRestartApp={sendLRRestartApp}
              sendLRRestartManagedApp={sendLRRestartManagedApp}
              sendLRRebuildApp={sendLRRebuildApp}
              sendLRSetAutoRebuild={sendLRSetAutoRebuild}
              uploadFiles={uploadFiles}
              activeTab={activeTab}
              setActiveTab={setActiveTab}
            />
          ) : (
            <GlobalView
              hosts={hosts}
              hostData={hostData}
              selfHostId={selfHostId}
              devMode={devMode}
              sendLRRestartApp={sendLRRestartApp}
              sendLRRebuildApp={sendLRRebuildApp}
              sendLRSetAutoRebuild={sendLRSetAutoRebuild}
              activeTab={activeTab}
              setActiveTab={setActiveTab}
              acLoaderManaged={acLoaderManaged}
              acUpdateAvailable={acUpdateAvailable}
              sendACRestartApp={sendACRestartApp}
            />
          )}
        </div>
      </div>
    </div>
  )
}
