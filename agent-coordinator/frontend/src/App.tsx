import { useState, useEffect, useCallback, useRef, useMemo, useContext, createContext } from 'react'
import type {
  Host, HostsMsg, LRStateMsg, LRFCStateMsg, LRFCLogMsg, LRFCInstancesMsg, FCInstanceInfo,
  LRControlMsg, ControlStateMsg, ControlSequenceInfo, ControlRunMsg,
  LRRidealongMsg, LRCondocMsg, LRSystemStateMsg, LRRepoStateMsg, LRCondoccerMsg, LRSessionsMsg, LRConvoMsg, LRRobotMsg, LRFilesMsg, LRDebugLogMsg, DebugLogEntry, LRChainCallMsg, ChainCallEntry, LRStateboardMsg, StateboardEntry, FileInfo, ProcInfo, ServiceStatus,
  SelfInfoMsg, ModeMismatchMsg, TCAvailabilityMsg,
} from './types'

// Applications the system tab offers a launch button for. `multi` apps are
// N-per-host (launch stays enabled while instances run); others are singletons.
const LAUNCHABLE_APPS: { name: string; multi: boolean }[] = [
  { name: 'federation-command', multi: true },
  { name: 'condoccer', multi: false },
  { name: 'sessions', multi: false },
  { name: 'convo', multi: false },
  { name: 'robot', multi: false },
]

interface LogEntry {
  kind: 'cmd' | 'output' | 'state'
  text: string
}

interface HostClientState {
  lrState?: LRStateMsg
  // fcState/fcLog are for an older LR that doesn't tell its FC instances
  // apart; fcInstances/fcLogs (keyed by instance) for a current one -- see
  // local-representative/fcinstances.go.
  fcState: string
  fcLog: LogEntry[]
  fcInstances: FCInstanceInfo[]
  fcLogs: Record<string, LogEntry[]>
  control?: ControlStateMsg
  ridealong?: LRRidealongMsg
  condoc?: LRCondocMsg
  system?: LRSystemStateMsg
  repo?: LRRepoStateMsg
  condoccer?: LRCondoccerMsg
  sessions?: LRSessionsMsg
  convo?: LRConvoMsg
  robot?: LRRobotMsg
  files?: LRFilesMsg
  debugLog?: LRDebugLogMsg
  chainCall?: LRChainCallMsg
  stateboard?: LRStateboardMsg
}

function emptyHostState(): HostClientState {
  return { fcState: '', fcLog: [], fcInstances: [], fcLogs: {} }
}

function fcStateLabel(state: string): string {
  return state === '' ? '-- disconnected --'
    : state === 'remote-control' ? '-- remote control --'
    : state === 'local-control' ? '-- local control --'
    : `-- ${state} --`
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
  const [acAutoUpdate, setACAutoUpdate] = useState(false)
  const [acStartedAt, setACStartedAt] = useState(0)
  // LR host id -> current mismatch disclosure -- see docs/DevMode.md.
  const [modeMismatches, setModeMismatches] = useState<Record<string, ModeMismatchMsg>>({})
  // Aggregate "is a the-conversationalist instance available on any host" --
  // see tcavailability.go and Step2Prompt.md. Drives the mic icon beside the
  // camera/screenshot icon in the header.
  const [tcAvailable, setTCAvailable] = useState(false)
  const wsRef = useRef<WebSocket | null>(null)
  const retryRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const fcStateRefs = useRef<Record<string, string>>({})

  // fc names the FC instance on that host; without it the host's LR picks.
  const sendLRCommand = useCallback((hostId: string, cmd: string, fc?: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-command', payload: { host_id: hostId, cmd, fc } }))
  }, [])

  const sendLRRidealongCommand = useCallback((hostId: string, action: string, fc?: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-ridealong-command', payload: { host_id: hostId, action, fc } }))
  }, [])

  // A host's control tab -- see local-representative/control.go.
  const sendLRControlRun = useCallback((hostId: string, sequence: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-control-run', payload: { host_id: hostId, sequence } }))
  }, [])

  const sendLRControlCancel = useCallback((hostId: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-control-cancel', payload: { host_id: hostId } }))
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

  // Toggles whether the given host's LR restarts itself the instant an
  // update becomes available, instead of waiting for the "restart and
  // update LR" control -- see docs/DevMode.md "Loader".
  const sendLRSetAutoUpdate = useCallback((hostId: string, enabled: boolean) => {
    wsRef.current?.send(JSON.stringify({ type: 'lr-set-auto-update', payload: { host_id: hostId, enabled } }))
  }, [])

  // Restarts agent-coordinator itself (not any host's LR) -- only expected to
  // come back up when it's loader-managed; mirrors sendLRRestartApp but for
  // AC's own process, with no host to target (see Step4Prompt.md Revision E).
  const sendACRestartApp = useCallback(() => {
    wsRef.current?.send(JSON.stringify({ type: 'ac-restart-app', payload: {} }))
  }, [])

  // Toggles whether agent-coordinator restarts itself the instant an update
  // becomes available, instead of waiting for the "restart and update AC"
  // control -- mirrors sendLRSetAutoUpdate but for AC's own process, with no
  // host to target (see Step1SubstepCPrompt.md Revision D).
  const sendACSetAutoUpdate = useCallback((enabled: boolean) => {
    wsRef.current?.send(JSON.stringify({ type: 'ac-set-auto-update', payload: { enabled } }))
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
            setACAutoUpdate(p.auto_update)
            setACStartedAt(p.started_at)
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
            const refKey = p.fc ? `${p.host_id}\n${p.fc}` : p.host_id
            const prevSt = fcStateRefs.current[refKey] ?? ''
            fcStateRefs.current[refKey] = newSt
            const stateEntry: LogEntry | null = prevSt !== newSt ? { kind: 'state', text: fcStateLabel(newSt) } : null
            setHostData(prev => {
              const cur = prev[p.host_id] ?? emptyHostState()
              if (p.fc) {
                // One instance's transition -- its current state comes with
                // lr-fc-instances.
                const fc = p.fc
                if (!stateEntry) return prev
                return {
                  ...prev,
                  [p.host_id]: { ...cur, fcLogs: { ...cur.fcLogs, [fc]: [...(cur.fcLogs[fc] ?? []), stateEntry] } },
                }
              }
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
              if (p.fc) {
                const fc = p.fc
                return {
                  ...prev,
                  [p.host_id]: { ...cur, fcLogs: { ...cur.fcLogs, [fc]: [...(cur.fcLogs[fc] ?? []).slice(-199), entry] } },
                }
              }
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
          case 'lr-fc-instances': {
            const p = msg.payload as LRFCInstancesMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), fcInstances: p.instances ?? [] },
            }))
            break
          }
          case 'lr-control-state': {
            const p = msg.payload as LRControlMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), control: p.control ?? undefined },
            }))
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
          case 'lr-sessions-state': {
            const p = msg.payload as LRSessionsMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), sessions: p.available ? p : undefined },
            }))
            break
          }
          case 'lr-convo-state': {
            const p = msg.payload as LRConvoMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), convo: p.available ? p : undefined },
            }))
            break
          }
          case 'lr-robot-state': {
            const p = msg.payload as LRRobotMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), robot: p.available ? p : undefined },
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
          case 'lr-debug-log-state': {
            const p = msg.payload as LRDebugLogMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), debugLog: p.active ? p : undefined },
            }))
            break
          }
          case 'lr-chain-call-state': {
            const p = msg.payload as LRChainCallMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), chainCall: p.active ? p : undefined },
            }))
            break
          }
          case 'lr-stateboard-state': {
            const p = msg.payload as LRStateboardMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), stateboard: p.active ? p : undefined },
            }))
            break
          }
          case 'tc-availability': {
            const p = msg.payload as TCAvailabilityMsg
            setTCAvailable(p.available)
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
    connected, hosts, hostData, selectHost, devMode, selfHostId, modeMismatches, tcAvailable,
    acLoaderManaged, acUpdateAvailable, acAutoUpdate, acStartedAt,
    sendLRCommand, sendLRRidealongCommand, sendLRControlRun, sendLRControlCancel, sendLRLaunchApp, sendLRTerminateApp, uploadFiles,
    sendLRRestartApp, sendLRRestartManagedApp, sendLRRebuildApp, sendLRSetAutoRebuild, sendLRSetAutoUpdate, sendACRestartApp, sendACSetAutoUpdate,
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

const LR_SERVICES = ['federation-command', 'condoccer', 'convo', 'sessions', 'robot', 'worker'] as const
// "system" and "files" sit to the right of the service tabs, mirroring
// local-representative's own dashboard: they drive/view that LR's process
// management and host-cache from the coordinator. Upload on this files tab is
// relayed through AC's dedicated upload-relay route rather than AC keeping
// its own copy of the file (see docs/DistributedExchange.md, Path 1).
// "control" sequences actions across a host's sub-apps (see
// local-representative/control.go); it sits between the services and system.
const LR_TABS = [...LR_SERVICES, 'control', 'system', 'files'] as const
type LRTab = typeof LR_TABS[number]

// Tabs whose content is another app's own UI, embedded via same-origin
// iframe (condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step1Prompt.md Revision A: "nested UI" tabs; Revision B: bring this host
// view up to the same full-pane treatment local-representative already got).
// These skip the service-name heading and health-indicator every other tab
// gets and instead get a full tab pane for the iframe plus a slim status bar
// under the tab bar, so the embedded app's own UI (including its own
// "DEV MODE" border, when that sub-app runs in dev mode) fills the space
// instead of floating in a padded, header-topped box.
const EMBED_TABS: ReadonlySet<LRTab> = new Set(['condoccer', 'sessions', 'convo', 'robot'])

// Browser pickup strategy (condocs/initialDistributedDevelopmentImpls/
// BrowserPickupStrategy.md), Layer 2: sessionStorage survives a refresh,
// stays scoped per-tab, and clears when the tab actually closes rather than
// pinning stale state forever -- the right lifetime for "resume where I was".
function initialACTab(): LRTab {
  const stored = sessionStorage.getItem('ac-active-tab')
  return (LR_TABS as readonly string[]).includes(stored ?? '') ? (stored as LRTab) : 'system'
}

// Screen-history nav arrows (condocs/initialDistributedDevelopmentImpls/
// Step5Prompt.md Revision M): how many recently-visited (host, tab) screens
// we keep around for back/forward. A pragmatic starting value -- see
// useScreenHistory below.
const NAV_HISTORY_MAX = 20

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

// FCInstancePicker lists every federation-command instance connected to the
// host's LR; the federation-command tab's panels follow the selected one.
// Mirrors local-representative/frontend/src/App.tsx's own copy.
function FCInstancePicker({
  instances, selected, onSelect,
}: {
  instances: FCInstanceInfo[]
  selected: string | null
  onSelect: (key: string) => void
}) {
  if (instances.length === 0) return null
  return (
    <div className="fc-instances">
      <span className="fc-instances-label">
        {instances.length} instance{instances.length === 1 ? '' : 's'}
      </span>
      {instances.map(inst => (
        <button
          key={inst.key}
          className={`fc-instance fc-instance-${inst.state || 'none'}${inst.key === selected ? ' fc-instance-active' : ''}`}
          onClick={() => onSelect(inst.key)}
          title={`${inst.key}${inst.head ? ` (head ${inst.head})` : ''} — ${inst.state || 'no state yet'}`}
        >
          <span className="fc-instance-dot" />
          {inst.label}
          {inst.session && <span className="fc-instance-session">{inst.session}</span>}
        </button>
      ))}
    </div>
  )
}

// ---- Control tab (local-representative/control.go) ----
// Runs on the selected host's LR; mirrors local-representative's own copy.

const CONTROL_SUBTABS = ['v1'] as const
type ControlSubTab = typeof CONTROL_SUBTABS[number]

function controlStepIcon(status: string): string {
  return status === 'success' ? '✓'
    : status === 'error' ? '✗'
    : status === 'running' ? '▸'
    : status === 'skipped' ? '–'
    : '·'
}

function formatMs(ms?: number): string {
  if (!ms) return ''
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}

function ControlPanel({
  state, robotHealthy, onRun, onCancel,
}: {
  state: ControlStateMsg | null
  robotHealthy: boolean
  onRun: (sequence: string) => void
  onCancel: () => void
}) {
  const [sub, setSub] = useState<ControlSubTab>('v1')
  return (
    <div className="ctl-panel">
      <div className="ctl-subtabs">
        {CONTROL_SUBTABS.map(t => (
          <button key={t} className={`ctl-subtab${sub === t ? ' ctl-subtab-active' : ''}`} onClick={() => setSub(t)}>
            {t}
          </button>
        ))}
      </div>
      {sub === 'v1' && (
        !state ? (
          <div className="ctl-empty">waiting for control state…</div>
        ) : (
          <>
            {!robotHealthy && (
              <div className="ctl-hint ctl-hint-warn">the robot isn't running on this host — launch it from the system tab first</div>
            )}
            <div className="ctl-hint">
              The robot finds the new terminal by reading that host's screen: its window has to be visible,
              not behind another, and no other view of federation-command's output (its tab here or in
              local-representative) should be showing on that desktop.
            </div>
            {state.sequences.map(q => (
              <ControlSequenceCard
                key={q.id}
                seq={q}
                run={state.run && state.run.sequence === q.id ? state.run : undefined}
                busy={state.run?.status === 'running'}
                onRun={() => onRun(q.id)}
                onCancel={onCancel}
              />
            ))}
          </>
        )
      )}
    </div>
  )
}

function ControlSequenceCard({
  seq, run, busy, onRun, onCancel,
}: {
  seq: ControlSequenceInfo
  run?: ControlRunMsg
  busy: boolean
  onRun: () => void
  onCancel: () => void
}) {
  const running = run?.status === 'running'
  return (
    <div className="ctl-seq">
      <div className="ctl-seq-head">
        <span className="ctl-seq-name">{seq.name}</span>
        {run && (
          <span className={`ctl-run-status ctl-run-${run.status}`}>
            {run.status}{run.duration_ms ? ` · ${formatMs(run.duration_ms)}` : ''}
          </span>
        )}
        {running ? (
          <button className="sys-btn sys-btn-terminate" onClick={onCancel}>cancel</button>
        ) : (
          <button className="sys-btn sys-btn-launch" disabled={busy} onClick={onRun}>run</button>
        )}
      </div>
      {seq.description && <div className="ctl-seq-desc">{seq.description}</div>}
      <ol className="ctl-steps">
        {seq.steps.map((st, i) => {
          const r = run?.steps[i]
          const status = r?.status ?? 'pending'
          return (
            <li key={i} className={`ctl-step ctl-step-${status}`}>
              <span className="ctl-step-icon">{controlStepIcon(status)}</span>
              <div className="ctl-step-body">
                <div className="ctl-step-label">{st.label}</div>
                {st.detail && <div className="ctl-step-detail">{st.detail}</div>}
                {r?.message && <div className="ctl-step-msg">{r.message}</div>}
              </div>
              <span className="ctl-step-time">{formatMs(r?.duration_ms)}</span>
            </li>
          )
        })}
      </ol>
      {run?.values && run.values.length > 0 && (
        <div className="ctl-values">
          {run.values.map((v, i) => (
            <div key={i} className="ctl-value">
              <span className="ctl-value-label">{v.label}:</span> {v.value}
            </div>
          ))}
        </div>
      )}
      {run?.error && <div className="ctl-run-error-msg">{run.error}</div>}
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

// procRowKey identifies a system-tab row for selection/drill-down purposes:
// managed instances are unique by instance_id, but self has none (it's a
// singleton row), so it gets a fixed sentinel instead.
function procRowKey(proc: ProcInfo): string {
  return proc.managed ? proc.instance_id : '__self__'
}

function SystemProcRow({
  proc, nowSec, selected, onSelect, onTerminate, onRestart, onRestartManaged, onSetAutoUpdate,
}: {
  proc: ProcInfo
  nowSec: number
  selected?: boolean
  onSelect?: () => void
  onTerminate?: (id: string) => void
  onRestart?: () => void
  onRestartManaged?: (id: string) => void
  onSetAutoUpdate?: (enabled: boolean) => void
}) {
  const detail = proc.status === 'running'
    ? formatUptime(proc.started_at, nowSec)
    : `exit ${proc.exit_code}`

  const label = proc.managed && proc.instance > 0
    ? `${proc.name} #${proc.instance}`
    : proc.name

  return (
    <div
      className={`sys-row sys-row-${proc.status}${onSelect ? ' sys-row-clickable' : ''}${selected ? ' sys-row-selected' : ''}`}
      onClick={onSelect}
      title={onSelect ? 'click for version details' : undefined}
    >
      <span className="sys-col sys-col-name">
        <span className="sys-col-name-main">
          {label}
          {!proc.managed && <span className="sys-self-tag">this LR</span>}
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
        {proc.managed && onRestartManaged && (
          <button
            className={`sys-btn sys-btn-restart${proc.update_available ? ' sys-btn-restart-update' : ''}`}
            title={proc.update_available
              ? 'a newer build has landed on disk — terminate this instance and launch a new one with it'
              : 'terminate this instance and launch a fresh one of the same application'}
            onClick={e => { e.stopPropagation(); onRestartManaged(proc.instance_id) }}
          >
            {proc.update_available ? 'restart and update' : 'restart'}
          </button>
        )}
        {proc.managed && onTerminate && (
          <button
            className="sys-btn sys-btn-terminate"
            onClick={e => { e.stopPropagation(); onTerminate(proc.instance_id) }}
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
            onClick={e => { e.stopPropagation(); onRestart() }}
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
            onClick={e => e.stopPropagation()}
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

// SystemProcDetails is the drill-down shown below the table when a process
// row is clicked (see procRowKey/SystemPanel) -- Step5Prompt.md Revision J.
// Mirrors the topology view's .topo-details readout-row convention
// (App.tsx's per-host "Details & Control" pane).
function SystemProcDetails({ proc, nowSec }: { proc: ProcInfo; nowSec: number }) {
  const label = proc.managed && proc.instance > 0 ? `${proc.name} #${proc.instance}` : proc.name
  const detail = proc.status === 'running'
    ? formatUptime(proc.started_at, nowSec)
    : `exit ${proc.exit_code}`

  return (
    <div className="sys-details">
      <div className="sys-details-header">{label}</div>
      <div className="sys-readout-row">
        <span className="sys-readout-label">status</span>
        <span className="sys-readout-value">
          {proc.status}{proc.status === 'running' ? ` (${detail} uptime)` : ` (${detail})`}
          {proc.detail && ` — ${proc.detail}`}
        </span>
      </div>
      <div className="sys-readout-row">
        <span className="sys-readout-label">current version</span>
        <span className="sys-readout-value">
          {proc.version || <span className="sys-readout-placeholder">not yet reported</span>}
        </span>
      </div>
      <div className="sys-readout-row">
        <span className="sys-readout-label">pending version</span>
        <span className="sys-readout-value">
          {!proc.update_available
            ? <span className="sys-readout-placeholder">up to date</span>
            : proc.pending_version || <span className="sys-readout-placeholder">update available (version unknown)</span>}
        </span>
      </div>
      {proc.name === 'federation-command' && (
        <div className="sys-readout-row">
          <span className="sys-readout-label">session</span>
          <span className="sys-readout-value">
            {proc.session || <span className="sys-readout-placeholder">not yet reported</span>}
          </span>
        </div>
      )}
    </div>
  )
}

// TroughEntry is one line in a system tab's "trough" (see Trough below) --
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision L.
interface TroughEntry {
  id: number
  ts: number // Date.now(), when the notification was recorded
  text: string
}

// TROUGH_MAX_ENTRIES caps how much history a trough keeps -- it's a live,
// session-scoped notification log (not persisted; a refresh starts it fresh),
// not an audit trail, so old entries are simply dropped off the front.
const TROUGH_MAX_ENTRIES = 50

let troughIdSeq = 0

// useTrough appends a new trough entry each time `error` changes to a new,
// non-empty value -- right now that's only ever LRRepoStateMsg.last_error
// after a failed rebuild, per the prompt's "print errors or notifications
// when things happen like a failure during rebuild", but the trough itself
// is generic (any future error/notification source can feed it the same
// way). Used by the per-host system tab; see useAggregateTrough for the
// global system tab's multi-host equivalent.
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

// useAggregateTrough is useTrough generalized across every connected host, for
// the global system tab: each host's own rebuild failure is recorded once,
// tagged with that host's label, keyed independently so one host's repeated
// failures don't mask another's.
function useAggregateTrough(hosts: Host[], hostData: Record<string, HostClientState>): TroughEntry[] {
  const [entries, setEntries] = useState<TroughEntry[]>([])
  const lastSeen = useRef<Record<string, string | undefined>>({})

  useEffect(() => {
    setEntries(prev => {
      let next = prev
      for (const h of hosts) {
        const error = hostData[h.id]?.repo?.last_error
        if (error && error !== lastSeen.current[h.id]) {
          next = [
            ...next.slice(-(TROUGH_MAX_ENTRIES - 1)),
            { id: ++troughIdSeq, ts: Date.now(), text: `${h.label}: rebuild failed — ${error}` },
          ]
        }
        lastSeen.current[h.id] = error
      }
      return next
    })
  }, [hosts, hostData])

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

/* ---- Debug view (system tab, Step1SubstepBPrompt.md Revision E) ----
 *
 * A first, deliberately simple pass: a "debug" button in the system tab
 * (both per-host and global) swaps that tab's main pane for a two-tab
 * viewer -- "network" and "logs" -- each just a plain scrolling list of
 * captured lines, plus a "to file" button that drops the current contents
 * into the preferred file store the same way ScreenshotButton already does.
 */

// NetworkLogEntry is one captured request this agent-coordinator frontend
// itself made -- method/URL/status/duration. Since every interaction with
// every sub-app (uploads, hold/persist/delete, markup, screenshots, launch/
// terminate commands that ride the WebSocket, etc.) is issued as a fetch
// from this one page, wrapping window.fetch once (see installNetworkCapture)
// covers all of them without needing each sub-app's own embedded iframe to
// cooperate. It doesn't yet see network activity that happens entirely
// inside an embedded iframe's own document (condoccer/sessions/convo/robot's
// own UI) -- a later increment could thread that through postMessage if needed.
interface NetworkLogEntry {
  id: number
  time: number // unix ms
  method: string
  url: string
  status: number | null // null while in flight or on a network-level failure
  duration_ms: number | null
  error?: string
}

const MAX_NETWORK_LOG_ENTRIES = 300
let networkLog: NetworkLogEntry[] = []
let networkLogSeq = 0
const networkLogListeners = new Set<() => void>()

function pushNetworkLogEntry(entry: NetworkLogEntry) {
  networkLog = [...networkLog.slice(-(MAX_NETWORK_LOG_ENTRIES - 1)), entry]
  networkLogListeners.forEach(fn => fn())
}

// installNetworkCapture wraps window.fetch exactly once per page load.
let networkCaptureInstalled = false
function installNetworkCapture() {
  if (networkCaptureInstalled) return
  networkCaptureInstalled = true
  const realFetch = window.fetch.bind(window)
  window.fetch = (...args: Parameters<typeof fetch>) => {
    const method = (args[1]?.method ?? 'GET').toUpperCase()
    const first = args[0]
    const url = typeof first === 'string' ? first : first instanceof Request ? first.url : String(first)
    const id = ++networkLogSeq
    const start = Date.now()
    return realFetch(...args).then(
      resp => {
        pushNetworkLogEntry({ id, time: start, method, url, status: resp.status, duration_ms: Date.now() - start })
        return resp
      },
      err => {
        pushNetworkLogEntry({ id, time: start, method, url, status: null, duration_ms: Date.now() - start, error: String(err) })
        throw err
      },
    )
  }
}

// useNetworkLog subscribes this component to the shared capture buffer above.
function useNetworkLog(): NetworkLogEntry[] {
  const [, forceRender] = useState(0)
  useEffect(() => {
    installNetworkCapture()
    const listener = () => forceRender(n => n + 1)
    networkLogListeners.add(listener)
    return () => { networkLogListeners.delete(listener) }
  }, [])
  return networkLog
}

function formatNetworkLogLine(e: NetworkLogEntry): string {
  const status = e.status != null ? String(e.status) : e.error ? 'error' : '…'
  const duration = e.duration_ms != null ? `${e.duration_ms}ms` : ''
  return `${new Date(e.time).toLocaleTimeString()}  ${e.method}  ${status}  ${duration}  ${e.url}`
}

// TimedLine is a pre-formatted debug-pane line plus its original timestamp
// (ms since epoch), letting DebugView merge chain-call lines (ts in unix
// seconds, formatted by the caller -- see formatChainCallLine) into the
// same chronological list as this frontend's own browser-fetch captures
// (ts in unix ms, formatted by formatNetworkLogLine) without losing the
// ordering either source needs its raw timestamp for.
interface TimedLine {
  ts: number // ms since epoch
  line: string
}

// formatChainCallLine renders one captured backend-to-backend HTTP call
// from the SM<->LR<->AC chain (Step1SubstepBPrompt.md Revision F) -- the
// half of the "network" tab a browser fetch wrapper can't see, since it
// never touches session-manager's or local-representative's own HTTP
// clients. hostLabel is only passed in the global perspective, same as
// formatDebugLogLine.
function formatChainCallLine(e: ChainCallEntry, hostLabel?: string): string {
  const host = hostLabel ? `${hostLabel} ` : ''
  const status = e.status ? String(e.status) : e.error ? 'error' : '…'
  return `${new Date(e.ts * 1000).toLocaleTimeString()}  ${host}[${e.hop}] ${e.method}  ${status}  ${e.duration_ms}ms  ${e.url}`
}

// formatDebugLogLine renders one captured LR-managed-sub-app stdout/stderr
// line. hostLabel is only passed in the global perspective, where lines from
// every host are merged into one list and need tagging to tell them apart.
function formatDebugLogLine(e: DebugLogEntry, hostLabel?: string): string {
  const host = hostLabel ? `${hostLabel} ` : ''
  const marker = e.stream === 'stderr' ? '!' : ' '
  return `${new Date(e.ts * 1000).toLocaleTimeString()}  ${host}[${e.app}]${marker} ${e.line}`
}

// formatStateboardLine renders one stateboard key/value row (see
// StateboardEntry) for the debug view's "stateboard" tab
// (condocs/initialDistributedSessionsImpls/Step2Prompt.md Revision E).
// hostLabel is only passed in the global perspective, same as
// formatDebugLogLine/formatChainCallLine -- a stateboard entry has no
// timestamp of its own (it's a live snapshot, not a log), so unlike those
// two this is plain text with no time prefix. Entries nest two levels deep
// under their owning sub-app (Revision F), so this renders "app: key =
// value" rather than a single flattened key.
function formatStateboardLine(e: StateboardEntry, hostLabel?: string): string {
  const host = hostLabel ? `${hostLabel}  ` : ''
  return `${host}${e.app}: ${e.key} = ${e.value || '(empty)'}`
}

// mergeGlobalStateboard combines every host's locally-reported stateboard
// into one process-wide view (Revision G): each LR only *presents* its own
// local view (what's connected to it), but the global perspective needs to
// show what's reachable across every host -- the same way an LR recognizes
// a capability like transcription is usable from another node once *any*
// host reports it present, rather than only its own. Flat-mapping per-host
// entries with a host label (the old behaviour, still used for logs/chain
// calls where per-host attribution is the point) just produced a repeated
// per-host breakdown here instead of a merged summary. "present" rows OR
// together across hosts; every other row (the "hosts"/"instances" list rows
// and any custom key a sub-app posts, e.g. "current-session") unions the
// distinct non-empty values reported by each host, sorted, so e.g. a
// "hosts" row ends up listing every host where that app is present instead
// of just whichever single host happened to report it.
function mergeGlobalStateboard(hosts: Host[], hostData: Record<string, HostClientState>): StateboardEntry[] {
  const present = new Map<string, boolean>()
  const values = new Map<string, Set<string>>()
  const idOf = (app: string, key: string) => `${app}\x00${key}`
  for (const h of hosts) {
    for (const e of hostData[h.id]?.stateboard?.entries ?? []) {
      const id = idOf(e.app, e.key)
      if (e.key === 'present') {
        present.set(id, (present.get(id) ?? false) || e.value === 'true')
      } else {
        const set = values.get(id) ?? new Set<string>()
        for (const part of e.value.split(',').map(v => v.trim())) {
          if (part) set.add(part)
        }
        values.set(id, set)
      }
    }
  }
  const entries: StateboardEntry[] = []
  for (const [id, isPresent] of present) {
    const [app, key] = id.split('\x00')
    entries.push({ app, key, value: String(isPresent) })
  }
  for (const [id, set] of values) {
    const [app, key] = id.split('\x00')
    entries.push({ app, key, value: [...set].sort().join(', ') })
  }
  entries.sort((a, b) => (a.app !== b.app ? a.app.localeCompare(b.app) : a.key.localeCompare(b.key)))
  return entries
}

// resolveGlobalDebugUploadHost picks the "preferred file store" to save a
// global-perspective debug capture to, where there's no single selected host
// to fall back on the way ScreenshotButton's per-host case does: this
// agent-coordinator's own co-located host if it's connected, else the first
// connected host, else null (disables the "to file" button).
function resolveGlobalDebugUploadHost(hosts: Host[], selfHostId: string | null): string | null {
  if (selfHostId && hosts.some(h => h.id === selfHostId && h.status === 'connected')) return selfHostId
  return hosts.find(h => h.status === 'connected')?.id ?? null
}

// DebugLogPane is the "very simple viewer" shared by both the network and
// logs tabs: a plain scrolling list of pre-formatted lines, plus a "to file"
// button that uploads them as one text file through the normal upload path
// (so it lands in the files tab cache like any other upload).
function DebugLogPane({
  lines, toFileName, uploadHostId, uploadFiles, emptyMessage,
}: {
  lines: string[]
  toFileName: string
  uploadHostId: string | null
  uploadFiles: (hostId: string, files: File[]) => void
  emptyMessage: string
}) {
  const [busy, setBusy] = useState(false)

  const handleToFile = async () => {
    if (!uploadHostId || lines.length === 0 || busy) return
    setBusy(true)
    try {
      await uploadFiles(uploadHostId, [new File([lines.join('\n') + '\n'], toFileName, { type: 'text/plain' })])
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="debug-pane">
      <div className="debug-pane-toolbar">
        <span className="debug-pane-count">{lines.length} line(s) captured this session</span>
        <button
          className="sys-btn sys-btn-neutral"
          disabled={!uploadHostId || lines.length === 0 || busy}
          onClick={handleToFile}
          title={uploadHostId ? 'save these lines to the preferred file store' : 'no file store available -- select a connected host first'}
        >
          {busy ? 'saving…' : 'to file'}
        </button>
      </div>
      <div className="debug-pane-lines">
        {lines.length === 0 ? (
          <div className="debug-pane-empty">{emptyMessage}</div>
        ) : (
          lines.map((line, i) => <div className="debug-pane-line" key={i}>{line}</div>)
        )}
      </div>
    </div>
  )
}

const DEBUG_TABS = ['network', 'logs', 'stateboard'] as const
type DebugTab = typeof DEBUG_TABS[number]

// DebugView is the system tab's "debug" button destination: network capture
// (see useNetworkLog, app-wide and identical in both perspectives since it's
// this one frontend's own request log -- merged chronologically with
// chainLines, the backend-to-backend SM<->LR<->AC calls the caller already
// formatted via formatChainCallLine, same reasoning as logLines below),
// logs (logLines, already formatted by the caller -- see
// formatDebugLogLine -- since per-host and global differ in whether a host
// tag is needed), and stateboard (stateLines, formatted the same way via
// formatStateboardLine -- condocs/initialDistributedSessionsImpls/
// Step2Prompt.md Revision E).
function DebugView({
  logLines, chainLines, stateLines, uploadHostId, uploadFiles,
}: {
  logLines: string[]
  chainLines: TimedLine[]
  stateLines: string[]
  uploadHostId: string | null
  uploadFiles: (hostId: string, files: File[]) => void
}) {
  const [tab, setTab] = useState<DebugTab>('network')
  const networkEntries = useNetworkLog()

  const networkLines = useMemo(() => {
    const fetchLines: TimedLine[] = networkEntries.map(e => ({ ts: e.time, line: formatNetworkLogLine(e) }))
    return [...fetchLines, ...chainLines].sort((a, b) => a.ts - b.ts).map(l => l.line)
  }, [networkEntries, chainLines])

  return (
    <div className="debug-view">
      <div className="tab-bar tab-bar-nested">
        <div className="tabs">
          {DEBUG_TABS.map(t => (
            <button key={t} className={`tab${tab === t ? ' tab-active' : ''}`} onClick={() => setTab(t)}>
              {t}
            </button>
          ))}
        </div>
      </div>
      {tab === 'network' ? (
        <DebugLogPane
          lines={networkLines}
          toFileName={`network-debug-${Date.now()}.log`}
          uploadHostId={uploadHostId}
          uploadFiles={uploadFiles}
          emptyMessage="no network activity captured yet"
        />
      ) : tab === 'logs' ? (
        <DebugLogPane
          lines={logLines}
          toFileName={`logs-debug-${Date.now()}.log`}
          uploadHostId={uploadHostId}
          uploadFiles={uploadFiles}
          emptyMessage="no LR-managed sub-app log lines captured yet"
        />
      ) : (
        <DebugLogPane
          lines={stateLines}
          toFileName={`stateboard-${Date.now()}.log`}
          uploadHostId={uploadHostId}
          uploadFiles={uploadFiles}
          emptyMessage="no stateboard entries reported yet"
        />
      )}
    </div>
  )
}

// DebugToggleButton sits at the right edge of the system tab's own tab bar
// (both per-host and global -- Step1SubstepBPrompt.md Revision E: "a 'debug'
// button in the system tab in the top right corner"), only rendered while
// that tab is active.
function DebugToggleButton({ open, onClick }: { open: boolean; onClick: () => void }) {
  return (
    <div className="tab-bar-right">
      <button className={`sys-btn sys-btn-neutral${open ? ' debug-toggle-btn-active' : ''}`} onClick={onClick}>
        debug
      </button>
    </div>
  )
}

function SystemPanel({
  hostId, state, active, fcState, fcInstances, repoState, onLaunch, onTerminate, onRestart, onRestartManaged, onRebuild, onSetAutoRebuild, onSetAutoUpdate,
}: {
  hostId: string
  state: LRSystemStateMsg | undefined
  active: boolean
  fcState: string // an older LR's single FC state -- see HostClientState
  fcInstances: FCInstanceInfo[]
  repoState: LRRepoStateMsg | undefined
  onLaunch: (hostId: string, name: string) => void
  onTerminate: (hostId: string, id: string) => void
  onRestart: (hostId: string) => void
  onRestartManaged: (hostId: string, id: string) => void
  onRebuild: (hostId: string) => void
  onSetAutoRebuild: (hostId: string, enabled: boolean) => void
  onSetAutoUpdate: (hostId: string, enabled: boolean) => void
}) {
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000))
  // Which process row's drill-down is open, keyed by procRowKey -- cleared
  // whenever that row disappears (e.g. a terminated instance is dismissed)
  // rather than left pointing at a stale selection.
  const [selectedKey, setSelectedKey] = useState<string | null>(null)
  const troughEntries = useTrough(repoState?.last_error)

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
  const fcControl = (st: string) =>
    st === 'remote-control' ? 'remote'
    : st === 'local-control' ? 'local'
    : 'not connected'
  // One line per connected FC instance (see FCInstancePicker); a single line
  // for an older LR, or while launched instances have yet to connect.
  const fcRows = fcInstances.length > 0
    ? fcInstances.map(inst => ({ key: inst.key, label: `federation-command ${inst.label}`, state: inst.state }))
    : [{ key: '', label: 'federation-command', state: fcState }]

  const selectedProc = selectedKey === null
    ? undefined
    : [state.self, ...managed].find(p => procRowKey(p) === selectedKey)

  return (
    <div className="sys-panel">
      <RepoWatchPanel
        repoState={repoState}
        onRebuild={() => onRebuild(hostId)}
        onSetAutoRebuild={enabled => onSetAutoRebuild(hostId, enabled)}
      />
      {fcRunning && fcRows.map(row => (
        <div key={row.key} className={`sys-fc-control sys-fc-control-${row.state || 'none'}`}>
          {row.label} control: <strong>{fcControl(row.state)}</strong>
          {fcControl(row.state) !== 'remote' && ' — expected remote in a machine-driven chain'}
        </div>
      ))}
      <div className="sys-table">
        <div className="sys-row sys-row-head">
          <span className="sys-col sys-col-name">process</span>
          <span className="sys-col sys-col-pid">pid</span>
          <span className="sys-col sys-col-status">status</span>
          <span className="sys-col sys-col-detail">uptime</span>
          <span className="sys-col sys-col-actions" />
        </div>
        <SystemProcRow
          proc={state.self}
          nowSec={nowSec}
          selected={selectedKey === procRowKey(state.self)}
          onSelect={() => setSelectedKey(k => k === procRowKey(state.self) ? null : procRowKey(state.self))}
          onRestart={() => onRestart(hostId)}
          onSetAutoUpdate={enabled => onSetAutoUpdate(hostId, enabled)}
        />
        {managed.map(p => (
          <SystemProcRow
            key={p.instance_id}
            proc={p}
            nowSec={nowSec}
            selected={selectedKey === procRowKey(p)}
            onSelect={() => setSelectedKey(k => k === procRowKey(p) ? null : procRowKey(p))}
            onTerminate={id => onTerminate(hostId, id)}
            onRestartManaged={id => onRestartManaged(hostId, id)}
          />
        ))}
        {managed.length === 0 && (
          <div className="sys-row sys-row-none">no managed applications</div>
        )}
      </div>
      {selectedProc && <SystemProcDetails proc={selectedProc} nowSec={nowSec} />}

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
      <Trough entries={troughEntries} />
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

// fileHighlightUrl backs the file-details dialog's "highlight" toggle,
// through the same transparent proxy as hold/persist/delete above -- see
// local-representative/files.go's handleFileHighlight.
function fileHighlightUrl(hostId: string, id: string): string {
  return `/host/${encodeURIComponent(hostId)}/api/files/${encodeURIComponent(id)}/highlight`
}

function fileDeleteUrl(hostId: string, id: string): string {
  return `/host/${encodeURIComponent(hostId)}/api/files/${encodeURIComponent(id)}`
}

// markup*Url back the markup dialog (Step1SubstepCPrompt.md Revision F),
// through the same transparent proxy as hold/persist/highlight above -- see
// local-representative/files.go's handleMarkupGet and friends.
function markupUrl(hostId: string, id: string): string {
  return `/host/${encodeURIComponent(hostId)}/api/files/${encodeURIComponent(id)}/markup`
}

function markupCommitUrl(hostId: string, id: string): string {
  return `/host/${encodeURIComponent(hostId)}/api/files/${encodeURIComponent(id)}/markup/commit`
}

function markupCancelUrl(hostId: string, id: string): string {
  return `/host/${encodeURIComponent(hostId)}/api/files/${encodeURIComponent(id)}/markup/cancel`
}

function markupCopyUrl(hostId: string, id: string): string {
  return `/host/${encodeURIComponent(hostId)}/api/files/${encodeURIComponent(id)}/markup/copy`
}

// NewTextFileDialog is the files tab's "new text file" button (Step2Prompt.md
// Revision J: "add a 'new text file' button to the files tab ... accepts the
// name and text (with optional voice input with mic icon when available)").
// There's no dedicated "create file" API on the server -- this reuses the
// very same multipart upload endpoint every drag-and-drop/browse upload
// already goes through (relayed via handleFileUploadRelay to the target
// host's own handleFileUpload, in files.go) by synthesizing a File from the
// typed name and body client-side, the same trick the debug-log panel's
// "save to file" button already uses. Mirrors local-representative's own
// copy of this dialog.
function NewTextFileDialog({
  onCreate,
  onClose,
}: {
  onCreate: (files: File[]) => void | Promise<void>
  onClose: () => void
}) {
  const [name, setName] = useState('')
  const [body, setBody] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const handleCreate = async () => {
    const trimmed = name.trim()
    if (!trimmed) {
      setError('name is required')
      return
    }
    // Mirrors files.go's saveUploadedFile reserved-prefix checks -- a
    // friendlier client-side echo of a rejection the server would otherwise
    // give silently (a rejected file is just dropped from the response).
    if (trimmed.startsWith('.manifest_') || trimmed.startsWith('.markup_')) {
      setError("that name is reserved for the host-cache's own bookkeeping files")
      return
    }
    const finalName = trimmed.includes('.') ? trimmed : `${trimmed}.txt`
    setBusy(true)
    setError(null)
    try {
      await onCreate([new File([body], finalName, { type: 'text/plain' })])
      onClose()
    } catch {
      setError('failed to create file')
      setBusy(false)
    }
  }

  return (
    <div className="new-file-overlay" onClick={onClose}>
      <div className="new-file-dialog" onClick={e => e.stopPropagation()}>
        <div className="file-detail-header">
          <span className="file-detail-title">new text file</span>
          <button className="file-detail-close" onClick={onClose}>×</button>
        </div>
        <input
          className="new-file-name-input"
          placeholder="file name (e.g. notes.txt)"
          value={name}
          onChange={e => setName(e.target.value)}
          autoFocus
        />
        <div className="field-with-mic">
          <textarea
            className="new-file-body-input"
            placeholder="file contents…"
            value={body}
            onChange={e => setBody(e.target.value)}
            rows={10}
          />
          <MicButton onTranscript={text => setBody(prev => appendTranscript(prev, text))} />
        </div>
        {error && <div className="new-file-error">{error}</div>}
        <div className="new-file-actions">
          <button type="button" className="new-file-btn new-file-btn-cancel" onClick={onClose} disabled={busy}>
            cancel
          </button>
          <button type="button" className="new-file-btn new-file-btn-create" onClick={() => void handleCreate()} disabled={busy}>
            {busy ? 'creating…' : 'create'}
          </button>
        </div>
      </div>
    </div>
  )
}

function FilesPanel({
  files, active, selectedId, onSelect, onEnter, onUpload,
}: {
  files: FileInfo[]
  active: boolean
  selectedId: string | null
  onSelect: (id: string) => void
  onEnter: (id: string) => void
  onUpload: (files: FileList | File[]) => void
}) {
  const [dragging, setDragging] = useState(false)
  const [newFileOpen, setNewFileOpen] = useState(false)
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
      <button type="button" className="files-new-btn" onClick={() => setNewFileOpen(true)}>
        + new text file
      </button>
      {newFileOpen && (
        <NewTextFileDialog onCreate={onUpload} onClose={() => setNewFileOpen(false)} />
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
  file, hostId, onClose, onEnter, onMarkup,
}: {
  file: FileInfo
  hostId: string
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

  const handleHighlight = () => {
    void runAction(fileHighlightUrl(hostId, file.id), 'POST')
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
          view
        </button>
        <a className="file-detail-download" href={fileDownloadUrl(hostId, file.id)} download={file.name}>
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

// FileViewer mirrors local-representative's own files-tab viewer, reached the
// same way (double-click a grid item, or the detail pane's "view" button), just
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

/* ---- Markup dialog (Step1SubstepCPrompt.md Revision F) ---- */

// MarkupTool/MARKUP_COLORS/MARKUP_TOOL_ICON/canvasPoint/drawMarkupArrow/
// drawMarkupRect all mirror local-representative's own copies verbatim (see
// local-representative/frontend/src/App.tsx) -- this file has no shared
// module to hang them off, same duplication as the rest of the files-tab UI
// (FileIcon, FilesPanel, FileDetailPane, FileViewer above).
type MarkupTool = 'arrow' | 'rect' | 'text'

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

// MarkupDialog mirrors local-representative's own copy, fetching/saving
// through this host's /host/<id>/* proxy (markupUrl et al. above) instead of
// same-origin -- see that file's MarkupDialog for the full rationale.
function MarkupDialog({
  file,
  hostId,
  rawUrl,
  onClose,
}: {
  file: FileInfo
  hostId: string
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

    fetch(markupUrl(hostId, file.id))
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
  }, [hostId, file.id, rawUrl])

  const saveComposite = useCallback(async () => {
    const canvas = canvasRef.current
    if (!canvas) return
    const blob = await new Promise<Blob | null>(resolve => canvas.toBlob(resolve, 'image/jpeg', 0.92))
    if (!blob) return
    const form = new FormData()
    form.append('file', blob, 'markup.jpg')
    try {
      const resp = await fetch(markupUrl(hostId, file.id), { method: 'POST', body: form })
      if (resp.ok) setHasMarkup(true)
      else setActionError('failed to save markup')
    } catch {
      setActionError('failed to save markup')
    }
  }, [hostId, file.id])

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
          <button className="markup-btn markup-btn-cancel" onClick={() => runAction(markupCancelUrl(hostId, file.id))} disabled={busy}>
            cancel
          </button>
          <button
            className="markup-btn markup-btn-copy"
            onClick={() => runAction(markupCopyUrl(hostId, file.id))}
            disabled={busy || !hasMarkup}
            title={hasMarkup ? 'create a file duplicate with the markup included' : 'draw something first'}
          >
            copy
          </button>
          <button
            className="markup-btn markup-btn-commit"
            onClick={() => runAction(markupCommitUrl(hostId, file.id))}
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
  return ['federation-command', 'condoccer', 'convo', 'sessions', 'robot', 'worker'].some(
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
  const tcHealthy = serviceHealthy(services, 'convo')
  const smHealthy = serviceHealthy(services, 'sessions')
  const rbHealthy = serviceHealthy(services, 'robot')
  const wHealthy = serviceHealthy(services, 'worker')
  const fcManaged = subAppManaged(managed, 'federation-command')
  const coManaged = subAppManaged(managed, 'condoccer')
  const tcManaged = subAppManaged(managed, 'convo')
  const smManaged = subAppManaged(managed, 'sessions')
  const rbManaged = subAppManaged(managed, 'robot')
  const wManaged = subAppManaged(managed, 'worker')
  const fcOutdated = devMode && fcHealthy && subAppOutOfDate(managed, 'federation-command')
  const coOutdated = devMode && coHealthy && subAppOutOfDate(managed, 'condoccer')
  const tcOutdated = devMode && tcHealthy && subAppOutOfDate(managed, 'convo')
  const smOutdated = devMode && smHealthy && subAppOutOfDate(managed, 'sessions')
  const rbOutdated = devMode && rbHealthy && subAppOutOfDate(managed, 'robot')
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
            className={subAppBoxClass(tcHealthy, tcManaged, !!tcOutdated)}
            title={tcOutdated ? "convo is connected but running an older build than what's on disk" : undefined}
          >TC</span>
          <span
            className={subAppBoxClass(smHealthy, smManaged, !!smOutdated)}
            title={smOutdated ? "sessions is connected but running an older build than what's on disk" : undefined}
          >SM</span>
          <span
            className={subAppBoxClass(rbHealthy, rbManaged, !!rbOutdated)}
            title={rbOutdated ? "robot is connected but running an older build than what's on disk" : undefined}
          >RB</span>
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
  hosts, hostData, selfHostId, devMode, sendLRRestartApp, sendLRRebuildApp, sendLRSetAutoRebuild, sendLRSetAutoUpdate,
  acLoaderManaged, acUpdateAvailable, acAutoUpdate, acStartedAt, sendACRestartApp, sendACSetAutoUpdate,
}: {
  hosts: Host[]
  hostData: Record<string, HostClientState>
  selfHostId: string | null
  devMode: boolean
  sendLRRestartApp: (hostId: string) => void
  sendLRRebuildApp: (hostId: string) => void
  sendLRSetAutoRebuild: (hostId: string, enabled: boolean) => void
  sendLRSetAutoUpdate: (hostId: string, enabled: boolean) => void
  acLoaderManaged: boolean
  acUpdateAvailable: boolean
  acAutoUpdate: boolean
  acStartedAt: number
  sendACRestartApp: () => void
  sendACSetAutoUpdate: (enabled: boolean) => void
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

  // Accompanying auto-update toggle-selector, mirroring auto-rebuild's above:
  // applies to every connected, loader-managed host at once (not just ones
  // currently stale, since like auto-rebuild this is a standing setting, not
  // a one-shot action) -- Step5Prompt.md Revision E. Also arms AC's own
  // auto-update alongside every host's, so agent-coordinator restarts itself
  // the moment it notices an update too, rather than only ever coming back
  // up via a manual "restart AC" -- see Step1SubstepCPrompt.md Revision D.
  const updatableHosts = devMode ? allHosts.filter(h => !!hostData[h.id]?.system?.self?.loader_managed) : []
  const allAutoUpdateOn = (updatableHosts.length > 0 || acLoaderManaged) &&
    updatableHosts.every(h => !!hostData[h.id]?.system?.self?.auto_update) &&
    (!acLoaderManaged || acAutoUpdate)
  const handleSetAutoUpdateAll = (enabled: boolean) => {
    updatableHosts.forEach(h => sendLRSetAutoUpdate(h.id, enabled))
    if (acLoaderManaged) sendACSetAutoUpdate(enabled)
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
          <span className="topo-readout-label">LR version</span>
          <span className={`topo-readout-value${selfProc?.version ? '' : ' topo-readout-placeholder'}`}>
            {selfProc?.version ?? '—'}
          </span>
        </div>
        <div className="topo-readout-row">
          <span className="topo-readout-label">LR uptime</span>
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
            <label
              className="sys-auto-rebuild"
              title={canRestart
                ? 'restart that host\'s LR automatically the instant an update becomes available, instead of waiting for the button above'
                : 'not loader-managed — run under ufa-loader (see make run-loader) to enable'}
            >
              <input
                type="checkbox"
                disabled={!canRestart}
                checked={!!selfProc?.auto_update}
                onChange={e => selectedHostId && sendLRSetAutoUpdate(selectedHostId, e.target.checked)}
              />
              auto-update
            </label>
          </div>
          {!selectedHostId && (
            <div className="topo-controls-hint">select a host to enable rebuild/restart controls</div>
          )}
        </div>
        {isSelfSelected && (
          <div className="topo-ac-controls">
            <div className="topo-controls-label">agent-coordinator</div>
            <div className="topo-readout-row">
              <span className="topo-readout-label">AC uptime</span>
              <span className={`topo-readout-value${acStartedAt ? '' : ' topo-readout-placeholder'}`}>
                {acStartedAt ? formatUptime(acStartedAt, nowSec) : '—'}
              </span>
            </div>
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
              <label
                className="sys-auto-rebuild"
                title="restart automatically, the instant an update becomes available -- toggles auto-update for every connected, loader-managed host's LR at once, plus agent-coordinator's own restart above"
              >
                <input
                  type="checkbox"
                  disabled={updatableHosts.length === 0 && !acLoaderManaged}
                  checked={allAutoUpdateOn}
                  onChange={e => handleSetAutoUpdateAll(e.target.checked)}
                />
                auto-update-all
              </label>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function GlobalSystemPanel({
  hosts, hostData, selfHostId, devMode, sendLRRestartApp, sendLRRebuildApp, sendLRSetAutoRebuild, sendLRSetAutoUpdate,
  acLoaderManaged, acUpdateAvailable, acAutoUpdate, acStartedAt, sendACRestartApp, sendACSetAutoUpdate,
}: {
  hosts: Host[]
  hostData: Record<string, HostClientState>
  selfHostId: string | null
  devMode: boolean
  sendLRRestartApp: (hostId: string) => void
  sendLRRebuildApp: (hostId: string) => void
  sendLRSetAutoRebuild: (hostId: string, enabled: boolean) => void
  sendLRSetAutoUpdate: (hostId: string, enabled: boolean) => void
  acLoaderManaged: boolean
  acUpdateAvailable: boolean
  acAutoUpdate: boolean
  acStartedAt: number
  sendACRestartApp: () => void
  sendACSetAutoUpdate: (enabled: boolean) => void
}) {
  const [subTab, setSubTab] = useState<GlobalSystemTab>('topology')
  const troughEntries = useAggregateTrough(hosts, hostData)
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
          sendLRSetAutoUpdate={sendLRSetAutoUpdate}
          acLoaderManaged={acLoaderManaged}
          acUpdateAvailable={acUpdateAvailable}
          acAutoUpdate={acAutoUpdate}
          acStartedAt={acStartedAt}
          sendACRestartApp={sendACRestartApp}
          sendACSetAutoUpdate={sendACSetAutoUpdate}
        />
      ) : (
        <div className="service-empty">not yet implemented</div>
      )}
      <Trough entries={troughEntries} />
    </div>
  )
}

function GlobalView({
  hosts, hostData, selfHostId, devMode, sendLRRestartApp, sendLRRebuildApp, sendLRSetAutoRebuild, sendLRSetAutoUpdate, activeTab, setActiveTab,
  acLoaderManaged, acUpdateAvailable, acAutoUpdate, acStartedAt, sendACRestartApp, sendACSetAutoUpdate, hasHighlighted, onGoToHighlighted, uploadFiles,
}: {
  hosts: Host[]
  hostData: Record<string, HostClientState>
  selfHostId: string | null
  devMode: boolean
  sendLRRestartApp: (hostId: string) => void
  sendLRRebuildApp: (hostId: string) => void
  sendLRSetAutoRebuild: (hostId: string, enabled: boolean) => void
  sendLRSetAutoUpdate: (hostId: string, enabled: boolean) => void
  activeTab: LRTab
  setActiveTab: (tab: LRTab) => void
  acLoaderManaged: boolean
  acUpdateAvailable: boolean
  acAutoUpdate: boolean
  acStartedAt: number
  sendACRestartApp: () => void
  sendACSetAutoUpdate: (enabled: boolean) => void
  hasHighlighted: boolean
  onGoToHighlighted: () => void
  uploadFiles: (hostId: string, files: FileList | File[]) => void
}) {
  const [debugOpen, setDebugOpen] = useState(false)

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
              {t === 'files' && hasHighlighted && (
                <span
                  className="tab-highlight-dot"
                  title="a file is highlighted — double-click to go to it"
                  onClick={e => e.stopPropagation()}
                  onDoubleClick={e => { e.stopPropagation(); onGoToHighlighted() }}
                />
              )}
            </button>
          ))}
        </div>
        {activeTab === 'system' && (
          <DebugToggleButton open={debugOpen} onClick={() => setDebugOpen(o => !o)} />
        )}
      </div>
      <div className="main-pane">
        {activeTab === 'system' ? (
          debugOpen ? (
            <DebugView
              logLines={hosts.flatMap(h =>
                (hostData[h.id]?.debugLog?.entries ?? []).map(e => formatDebugLogLine(e, h.label)),
              )}
              chainLines={hosts.flatMap(h =>
                (hostData[h.id]?.chainCall?.entries ?? []).map(e => ({ ts: e.ts * 1000, line: formatChainCallLine(e, h.label) })),
              )}
              stateLines={mergeGlobalStateboard(hosts, hostData).map(e => formatStateboardLine(e))}
              uploadHostId={resolveGlobalDebugUploadHost(hosts, selfHostId)}
              uploadFiles={uploadFiles}
            />
          ) : (
            <GlobalSystemPanel
              hosts={hosts}
              hostData={hostData}
              selfHostId={selfHostId}
              devMode={devMode}
              sendLRRestartApp={sendLRRestartApp}
              sendLRRebuildApp={sendLRRebuildApp}
              sendLRSetAutoRebuild={sendLRSetAutoRebuild}
              sendLRSetAutoUpdate={sendLRSetAutoUpdate}
              acLoaderManaged={acLoaderManaged}
              acUpdateAvailable={acUpdateAvailable}
              acAutoUpdate={acAutoUpdate}
              acStartedAt={acStartedAt}
              sendACRestartApp={sendACRestartApp}
              sendACSetAutoUpdate={sendACSetAutoUpdate}
            />
          )
        ) : (
          <div className="service-empty">not yet implemented</div>
        )}
      </div>
    </div>
  )
}

function LRView({
  host, data, sendLRCommand, sendLRRidealongCommand, sendLRControlRun, sendLRControlCancel, sendLRLaunchApp, sendLRTerminateApp,
  sendLRRestartApp, sendLRRestartManagedApp, sendLRRebuildApp, sendLRSetAutoRebuild, sendLRSetAutoUpdate, uploadFiles, activeTab, setActiveTab,
  hasHighlighted, onGoToHighlighted, pendingFileTarget, onConsumePendingFileTarget,
}: {
  host: Host
  data: HostClientState
  sendLRCommand: (hostId: string, cmd: string, fc?: string) => void
  sendLRRidealongCommand: (hostId: string, action: string, fc?: string) => void
  sendLRControlRun: (hostId: string, sequence: string) => void
  sendLRControlCancel: (hostId: string) => void
  sendLRLaunchApp: (hostId: string, name: string) => void
  sendLRTerminateApp: (hostId: string, id: string) => void
  sendLRRestartApp: (hostId: string) => void
  sendLRRestartManagedApp: (hostId: string, id: string) => void
  sendLRRebuildApp: (hostId: string) => void
  sendLRSetAutoRebuild: (hostId: string, enabled: boolean) => void
  sendLRSetAutoUpdate: (hostId: string, enabled: boolean) => void
  uploadFiles: (hostId: string, files: FileList | File[]) => void
  activeTab: LRTab
  setActiveTab: (tab: LRTab) => void
  hasHighlighted: boolean
  onGoToHighlighted: () => void
  pendingFileTarget: { hostId: string; fileId: string } | null
  onConsumePendingFileTarget: () => void
}) {
  const [selectedFileId, setSelectedFileId] = useState<string | null>(null)
  const [viewerFileId, setViewerFileId] = useState<string | null>(null)
  // debugOpen is reset whenever the viewed host changes -- this component
  // isn't remounted on host switch (see condoccerHash below), so without
  // this a debug view left open on one host would otherwise still be open
  // after picking a different one.
  const [debugOpen, setDebugOpen] = useState(false)
  useEffect(() => setDebugOpen(false), [host.id])
  // markupFile is snapshotted at the moment the dialog opens (rather than
  // re-derived from `files` below) so an in-flight files-state broadcast
  // can't yank the dialog's target out from under an open editing session --
  // see local-representative/frontend/src/App.tsx's own copy of this.
  const [markupFile, setMarkupFile] = useState<FileInfo | null>(null)
  const lrState = data.lrState
  const active = lrState?.active ?? false
  // Mirrors local-representative/frontend/src/App.tsx's own fcSession: the
  // session this host's federation-command is on, relayed down through
  // lr-system-state (see condocs/initialDistributedSessionsImpls/
  // Step2Prompt.md Revision C). Surfaced on the federation-command tab
  // itself rather than only the system tab's per-row tag.
  // The federation-command tab follows one FC instance at a time (see
  // FCInstancePicker): the one picked on this host, else the first.
  const [selectedFC, setSelectedFC] = useState<Record<string, string>>({})
  const fc = data.fcInstances.find(i => i.key === selectedFC[host.id]) ?? data.fcInstances[0] ?? null
  const fcSession = fc?.session ?? data.system?.managed.find(p => p.name === 'federation-command' && p.session)?.session

  // Consume the global "go to first highlighted file" handoff (Step5SubstepR
  // Revision E) once this is the host it was aimed at -- App() has already
  // selected this host and switched to the files tab by the time this fires.
  useEffect(() => {
    if (pendingFileTarget && pendingFileTarget.hostId === host.id) {
      setSelectedFileId(null)
      setViewerFileId(pendingFileTarget.fileId)
      onConsumePendingFileTarget()
    }
  }, [pendingFileTarget, host.id, onConsumePendingFileTarget])

  // Condoccer is embedded via a same-origin iframe with a hardcoded `src`,
  // so condoccer's own hash-based resume (Layer 1 of the browser pickup
  // strategy) never survives a refresh of this outer page on its own -- the
  // iframe just remounts at the bare `/host/<id>/condoccer/`. Capture the
  // iframe's hash as it navigates and bake it back into `src` so a refresh
  // here hands condoccer back its resume point. Keyed per host so switching
  // between hosts (this component isn't remounted on host change) doesn't
  // leak one host's condoc position into another's iframe.
  const [condoccerHash, setCondoccerHash] = useState(() => sessionStorage.getItem(`ac-condoccer-hash:${host.id}`) ?? '')
  const condoccerFrameRef = useRef<HTMLIFrameElement>(null)

  useEffect(() => {
    setCondoccerHash(sessionStorage.getItem(`ac-condoccer-hash:${host.id}`) ?? '')
  }, [host.id])

  const handleCondoccerLoad = () => {
    const win = condoccerFrameRef.current?.contentWindow
    if (!win) return
    const capture = () => {
      setCondoccerHash(win.location.hash)
      sessionStorage.setItem(`ac-condoccer-hash:${host.id}`, win.location.hash)
    }
    win.addEventListener('hashchange', capture)
    capture() // in case condoccer already restored a hash before this attached
  }

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
  const isEmbedTab = EMBED_TABS.has(activeTab)

  // Drives every MicButton under this host's view (Step2Prompt.md Revision
  // J) via TCCaptureContext, below -- gated on this host's own data.convo
  // (whether a the-conversationalist instance is actually running there),
  // not the header's global tcAvailable, since dictation has to reach this
  // specific host's convo instance -- see useTCCapture's doc comment.
  const tcCapture = useTCCapture(Boolean(data.convo), host.id)

  return (
    <TCCaptureContext.Provider value={{ available: tcCapture.available, capture: tcCapture.capture }}>
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
              {svc === 'files' && hasHighlighted && (
                <span
                  className="tab-highlight-dot"
                  title="a file is highlighted — double-click to go to it"
                  onClick={e => e.stopPropagation()}
                  onDoubleClick={e => { e.stopPropagation(); onGoToHighlighted() }}
                />
              )}
            </button>
          ))}
        </div>
        {activeTab === 'system' && (
          <DebugToggleButton open={debugOpen} onClick={() => setDebugOpen(o => !o)} />
        )}
      </div>
      {isEmbedTab && (
        <div className={`embed-status-bar health-${getServiceStatus(activeTab)}`}>
          <span className="health-dot" />
          <span className="health-label">{activeTab} — {getServiceStatus(activeTab)}</span>
        </div>
      )}
      <div className={`main-pane${isEmbedTab ? ' main-pane-embed' : ''}`}>
        {isEmbedTab ? (
          !active ? (
            <div className="service-empty service-empty-embed">local-representative on this host is not connected</div>
          ) : activeTab === 'condoccer' ? (
            data.condoccer ? (
              <iframe
                ref={condoccerFrameRef}
                className="embed-frame"
                src={`/host/${host.id}/condoccer/${condoccerHash}`}
                title={`condoccer on ${host.label}`}
                onLoad={handleCondoccerLoad}
              />
            ) : (
              <div className="service-empty service-empty-embed">
                condoccer is not running on this host — launch it from the system tab
              </div>
            )
          ) : activeTab === 'sessions' ? (
            data.sessions ? (
              <iframe className="embed-frame" src={`/host/${host.id}/sessions/`} title={`sessions on ${host.label}`} />
            ) : (
              <div className="service-empty service-empty-embed">
                sessions is not running on this host — launch it from the system tab
              </div>
            )
          ) : activeTab === 'robot' ? (
            data.robot ? (
              <iframe className="embed-frame" src={`/host/${host.id}/robot/`} title={`robot on ${host.label}`} />
            ) : (
              <div className="service-empty service-empty-embed">
                robot is not running on this host — launch it from the system tab
              </div>
            )
          ) : (
            data.convo ? (
              <iframe className="embed-frame" src={`/host/${host.id}/convo/`} title={`convo on ${host.label}`} />
            ) : (
              <div className="service-empty service-empty-embed">
                convo is not running on this host — launch it from the system tab
              </div>
            )
          )
        ) : (
          <div className={`main-pane-inner${selectedFile ? ' with-detail' : ''}`}>
            <div className="service-view">
              <div className="service-name">{activeTab}</div>
              {activeTab === 'federation-command' && (
                <div className={`health-indicator health-${getServiceStatus(activeTab)}`}>
                  <span className="health-dot" />
                  <span className="health-label">{getServiceStatus(activeTab)}</span>
                  {fcSession && (
                    <span className="fc-session-tag" title="active session">{fcSession}</span>
                  )}
                </div>
              )}
              {activeTab === 'system' && (
                debugOpen ? (
                  <DebugView
                    logLines={(data.debugLog?.entries ?? []).map(e => formatDebugLogLine(e))}
                    chainLines={(data.chainCall?.entries ?? []).map(e => ({ ts: e.ts * 1000, line: formatChainCallLine(e) }))}
                    stateLines={(data.stateboard?.entries ?? []).map(e => formatStateboardLine(e))}
                    uploadHostId={active ? host.id : null}
                    uploadFiles={uploadFiles}
                  />
                ) : (
                  <SystemPanel
                    hostId={host.id}
                    state={data.system}
                    active={active}
                    fcState={data.fcState}
                    fcInstances={data.fcInstances}
                    repoState={data.repo}
                    onLaunch={sendLRLaunchApp}
                    onTerminate={sendLRTerminateApp}
                    onRestart={sendLRRestartApp}
                    onRestartManaged={sendLRRestartManagedApp}
                    onRebuild={sendLRRebuildApp}
                    onSetAutoRebuild={sendLRSetAutoRebuild}
                    onSetAutoUpdate={sendLRSetAutoUpdate}
                  />
                )
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
              {activeTab === 'control' && (
                !active ? (
                  <div className="service-empty">local-representative on this host is not connected</div>
                ) : (
                  <ControlPanel
                    state={data.control ?? null}
                    robotHealthy={getServiceStatus('robot') === 'healthy'}
                    onRun={seq => sendLRControlRun(host.id, seq)}
                    onCancel={() => sendLRControlCancel(host.id)}
                  />
                )
              )}
              {activeTab === 'federation-command' && (fc ? (
                // A current LR: one instance at a time, picked above.
                <>
                  <FCInstancePicker
                    instances={data.fcInstances}
                    selected={fc.key}
                    onSelect={key => setSelectedFC(prev => ({ ...prev, [host.id]: key }))}
                  />
                  {fc.ridealong && (
                    <RidealongPanel
                      hostId={host.id}
                      state={{ ...fc.ridealong, host_id: host.id }}
                      fcState={fc.state}
                      sendLRRidealongCommand={(hostId, action) => sendLRRidealongCommand(hostId, action, fc.key)}
                    />
                  )}
                  {fc.condoc && !fc.ridealong && (
                    <CondocPanel state={{ ...fc.condoc, host_id: host.id }} fcState={fc.state} />
                  )}
                  <FCCommandPanel
                    key={fc.key}
                    hostId={host.id}
                    fcState={fc.state}
                    fcLog={data.fcLogs[fc.key] ?? []}
                    sendLRCommand={(hostId, cmd) => sendLRCommand(hostId, cmd, fc.key)}
                  />
                </>
              ) : (
                // An older LR that doesn't tell its instances apart (or none
                // connected yet).
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
              ))}
            </div>
            {selectedFile && (
              <FileDetailPane
                file={selectedFile}
                hostId={host.id}
                onClose={() => setSelectedFileId(null)}
                onEnter={setViewerFileId}
                onMarkup={id => setMarkupFile(files.find(f => f.id === id) ?? null)}
              />
            )}
          </div>
        )}
      </div>
      {markupFile && (
        <MarkupDialog
          file={markupFile}
          hostId={host.id}
          rawUrl={fileRawUrl(host.id, markupFile.id)}
          onClose={() => setMarkupFile(null)}
        />
      )}
      {tcCapture.captureURL && (
        <TCCaptureOverlay url={tcCapture.captureURL} onCancel={tcCapture.cancel} />
      )}
    </div>
    </TCCaptureContext.Provider>
  )
}

// CAMERA_ICON is a grey-palette wireframe icon (Step1SubstepCPrompt.md),
// matching the files tab's own outline icon set so the quick-feedback
// screenshot button reads as part of the same icon family. Kept identical to
// local-representative's copy -- see App.tsx there for the file-icon
// convention this follows.
const CAMERA_ICON = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round">
    <path d="M9 4 7.5 6H4a1 1 0 0 0-1 1v12a1 1 0 0 0 1 1h16a1 1 0 0 0 1-1V7a1 1 0 0 0-1-1h-3.5L15 4z" strokeLinecap="round" />
    <circle cx="12" cy="13" r="3.5" />
  </svg>
)

// captureScreenshot grabs a single frame of "the current display"
// (Step1SubstepCPrompt.md) via the browser's screen-capture API rather than
// rasterizing the DOM, so it genuinely captures whatever's on screen
// (including an embedded tab's own iframe content) without pulling in a
// DOM-to-canvas dependency. The capture stream is stopped immediately after
// the one frame is drawn -- this is a screenshot, not a recording.
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
// the same icon family. Kept identical to local-representative's copy -- see
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

// ---- The Conversationalist mic-capture, for per-field dictation ----
//
// Step2Prompt.md Revision J adds the files tab's "new text file" dialog, whose
// body field gets the same mic-dictation affordance condoccer's inputs
// already have. Mirrors condoccer's and local-representative's own copies of
// this block almost exactly, with one difference: agent-coordinator only
// ever reaches a the-conversationalist instance *through* a specific host's
// local-representative (/host/<id>/convo/, reverse-proxied by proxyToHost in
// main.go), so both the capture URL and the "is TC available" gate are
// host-scoped rather than global -- see LRView's own per-host
// TCCaptureContext.Provider, below, which already knows which host (and
// whether that host's own data.convo says TC is running there) it's for.
function tcCaptureURL(hostId: string): string {
  return `${window.location.origin}/host/${encodeURIComponent(hostId)}/convo/?embed=capture`
}

interface TCCaptureContextValue {
  available: boolean
  capture: (onTranscript: (text: string) => void) => void
}

// Default value only matters if a MicButton somehow renders outside a
// TCCaptureContext.Provider -- available: false keeps it inert.
const TCCaptureContext = createContext<TCCaptureContextValue>({ available: false, capture: () => {} })

// useTCCapture owns the one hidden-until-active iframe a given host's
// TCCaptureContext.Provider ever opens into that host's the-conversationalist,
// and the postMessage listener that receives its transcript back. `available`
// gates every MicButton under that provider; `capture` starts a capture,
// invoking its callback once (and only once) with the final text.
function useTCCapture(tcAvailable: boolean, hostId: string) {
  const [captureURL, setCaptureURL] = useState<string | null>(null)
  const onTranscriptRef = useRef<((text: string) => void) | null>(null)

  const capture = useCallback((onTranscript: (text: string) => void) => {
    onTranscriptRef.current = onTranscript
    setCaptureURL(tcCaptureURL(hostId))
  }, [hostId])

  const cancel = useCallback(() => {
    onTranscriptRef.current = null
    setCaptureURL(null)
  }, [])

  useEffect(() => {
    if (!captureURL) return
    let expectedOrigin: string
    try {
      expectedOrigin = new URL(captureURL).origin
    } catch {
      return
    }
    const onMessage = (ev: MessageEvent) => {
      if (ev.origin !== expectedOrigin) return
      const data = ev.data as { type?: string; text?: string } | undefined
      if (data?.type === 'tc-transcript') {
        onTranscriptRef.current?.(data.text ?? '')
        onTranscriptRef.current = null
        setCaptureURL(null)
      } else if (data?.type === 'tc-transcript-cancel') {
        onTranscriptRef.current = null
        setCaptureURL(null)
      }
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [captureURL])

  return { available: tcAvailable, capture, captureURL, cancel }
}

// MicButton is the per-field affordance -- renders nothing while the
// enclosing TCCaptureContext.Provider says TC isn't available for that host.
// See the new-text-file dialog's body field, below, for its one call site in
// this app so far.
function MicButton({ onTranscript }: { onTranscript: (text: string) => void }) {
  const { available, capture } = useContext(TCCaptureContext)
  if (!available) return null
  return (
    <button
      type="button"
      className="mic-btn"
      title="dictate with The Conversationalist"
      onClick={() => capture(onTranscript)}
    >
      {MIC_ICON}
    </button>
  )
}

// appendTranscript is the shared "insert dictated text" behaviour every
// MicButton call site uses: appended after any existing content, space-
// separated, rather than overwriting it. Mirrors condoccer's own copy.
function appendTranscript(prev: string, text: string): string {
  const trimmed = text.trim()
  if (!trimmed) return prev
  return prev.trim() ? `${prev.trim()} ${trimmed}` : trimmed
}

// TCCaptureOverlay hosts the actual iframe while a capture is in progress --
// mirrors condoccer's own copy.
function TCCaptureOverlay({ url, onCancel }: { url: string; onCancel: () => void }) {
  return (
    <div className="tc-capture-backdrop" onClick={onCancel}>
      <div className="tc-capture-panel" onClick={e => e.stopPropagation()}>
        <div className="tc-capture-panel-header">
          <span>The Conversationalist</span>
          <button type="button" className="tc-capture-close" onClick={onCancel} aria-label="Cancel dictation">×</button>
        </div>
        <iframe className="tc-capture-frame" src={url} title="the-conversationalist capture" allow="microphone" />
      </div>
    </div>
  )
}

// TCAvailabilityIndicator sits beside the camera/screenshot icon in the
// header: illuminated (mic-btn-active) once at least one
// the-conversationalist instance is available on any connected host (the
// aggregate agent-coordinator itself computes -- see tcavailability.go).
// Unlike ScreenshotButton this is a passive indicator, not a control -- the
// actual mic-to-transcribe action lives on condoccer's own text inputs (see
// Step2Prompt.md: "we will keep the TC functionality as contained in that
// sub-app as we can").
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
      title={enabled ? (busy ? 'saving screenshot…' : 'save a screenshot to the most-preferred file store') : 'no file store available -- select a connected host first'}
    >
      {CAMERA_ICON}
    </button>
  )
}

export default function App() {
  const {
    connected, hosts, hostData, selectHost, devMode, selfHostId, modeMismatches, tcAvailable,
    acLoaderManaged, acUpdateAvailable, acAutoUpdate, acStartedAt,
    sendLRCommand, sendLRRidealongCommand, sendLRControlRun, sendLRControlCancel, sendLRLaunchApp, sendLRTerminateApp, uploadFiles,
    sendLRRestartApp, sendLRRestartManagedApp, sendLRRebuildApp, sendLRSetAutoRebuild, sendLRSetAutoUpdate, sendACRestartApp, sendACSetAutoUpdate,
  } = useCoordinatorWS()
  const mismatches = Object.values(modeMismatches)
  // Seeded from sessionStorage (browser pickup strategy, Layer 2) so a
  // refresh lands back on the same host -- if that host id no longer
  // exists, `selectedHost` below just comes back null and we fall through
  // to the global view, same as picking an unknown host any other way.
  const [selectedHostId, setSelectedHostId] = useState<string | null>(() => sessionStorage.getItem('ac-selected-host'))
  // Shared across the global view and any host's view, so switching between
  // them (selecting/deselecting a host) keeps whichever tab was active
  // instead of resetting it.
  const [activeTab, setActiveTab] = useState<LRTab>(initialACTab)
  // Mobile nav drawer: the host sidebar becomes an off-canvas panel below the
  // `mobile-breakpoint` width (see index.css), same treatment as condoccer's
  // sidebar. Desktop layout is untouched -- this state has no visible effect
  // above the breakpoint.
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const [screenshotBusy, setScreenshotBusy] = useState(false)

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

  // "At least one file store available" (Step1SubstepCPrompt.md): AC has no
  // file store of its own -- a screenshot can only ever land somewhere by
  // relaying through the selected host's local-representative (same route
  // uploadFiles already uses), which only works once a host is both picked
  // and actually connected. uploadFiles below already prefers a cloud cache
  // over the host-cache wherever the target LR has one; today that's
  // nowhere, so every screenshot lands in that host's host-cache.
  const hasFileStore = !!selectedHost && selectedHost.status === 'connected'

  const handleScreenshot = async () => {
    if (screenshotBusy || !selectedHostId) return
    setScreenshotBusy(true)
    try {
      const file = await captureScreenshot()
      await uploadFiles(selectedHostId, [file])
    } catch (err) {
      console.error('screenshot failed:', err)
    } finally {
      setScreenshotBusy(false)
    }
  }

  // Files tab picker's highlighted-file indicator (Step5SubstepR Revision E).
  // Computed across every known host -- not just whichever one is currently
  // selected -- so the dot lights up on the global view, or while looking at
  // an unrelated host, and double-clicking it can jump to the right host
  // *and* the right file. "First" follows the host sidebar's own order, then
  // each host's own file order.
  const firstHighlighted = useMemo(() => {
    for (const host of hosts) {
      const hit = hostData[host.id]?.files?.files?.find(f => f.highlighted)
      if (hit) return { hostId: host.id, fileId: hit.id }
    }
    return null
  }, [hosts, hostData])
  // One-shot handoff to whichever LRView ends up rendered for the target
  // host, telling it to open that file's viewer -- cleared once consumed.
  const [pendingFileTarget, setPendingFileTarget] = useState<{ hostId: string; fileId: string } | null>(null)

  const goToFirstHighlighted = () => {
    if (!firstHighlighted) return
    setPendingFileTarget(firstHighlighted)
    if (firstHighlighted.hostId !== selectedHostId) handleSelectHost(firstHighlighted.hostId)
    setActiveTab('files')
  }

  useEffect(() => {
    if (selectedHostId) sessionStorage.setItem('ac-selected-host', selectedHostId)
    else sessionStorage.removeItem('ac-selected-host')
  }, [selectedHostId])

  useEffect(() => {
    sessionStorage.setItem('ac-active-tab', activeTab)
  }, [activeTab])

  // Forward/back nav arrows (Step5Prompt.md Revision M) track the pair of
  // top-level navigation choices -- which host (or global) and which tab --
  // as a single "screen".
  const navScreen = useMemo(() => ({ hostId: selectedHostId, tab: activeTab }), [selectedHostId, activeTab])
  const applyNavScreen = (s: { hostId: string | null; tab: LRTab }) => {
    // Only re-issue the host (de)selection -- with its selectHost() resubscribe
    // -- when the host actually changes, so navigating between tabs on the
    // same host doesn't needlessly resubscribe on every back/forward step.
    if (s.hostId !== selectedHostId) {
      if (s.hostId) handleSelectHost(s.hostId)
      else handleSelectGlobal()
    }
    setActiveTab(s.tab)
  }
  const nav = useScreenHistory(
    navScreen,
    (a, b) => a.hostId === b.hostId && a.tab === b.tab,
    applyNavScreen,
    NAV_HISTORY_MAX,
  )

  // A host id restored from sessionStorage was never sent via
  // handleSelectHost's own selectHost() call -- issue it here exactly once,
  // now that the websocket is actually up.
  const hashSelectHostDone = useRef(false)
  useEffect(() => {
    if (connected && selectedHostId && !hashSelectHostDone.current) {
      hashSelectHostDone.current = true
      selectHost(selectedHostId)
    }
  }, [connected, selectedHostId, selectHost])

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
        <span className="header-version-tag" title="build version">{__APP_VERSION__}</span>
        <TCAvailabilityIndicator available={tcAvailable} />
        <ScreenshotButton enabled={hasFileStore} busy={screenshotBusy} onClick={handleScreenshot} />
        <NavArrows canBack={nav.canBack} canForward={nav.canForward} onBack={nav.back} onForward={nav.forward} />
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
              sendLRControlRun={sendLRControlRun}
              sendLRControlCancel={sendLRControlCancel}
              sendLRLaunchApp={sendLRLaunchApp}
              sendLRTerminateApp={sendLRTerminateApp}
              sendLRRestartApp={sendLRRestartApp}
              sendLRRestartManagedApp={sendLRRestartManagedApp}
              sendLRRebuildApp={sendLRRebuildApp}
              sendLRSetAutoRebuild={sendLRSetAutoRebuild}
              sendLRSetAutoUpdate={sendLRSetAutoUpdate}
              uploadFiles={uploadFiles}
              activeTab={activeTab}
              setActiveTab={setActiveTab}
              hasHighlighted={!!firstHighlighted}
              onGoToHighlighted={goToFirstHighlighted}
              pendingFileTarget={pendingFileTarget}
              onConsumePendingFileTarget={() => setPendingFileTarget(null)}
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
              sendLRSetAutoUpdate={sendLRSetAutoUpdate}
              activeTab={activeTab}
              setActiveTab={setActiveTab}
              acLoaderManaged={acLoaderManaged}
              acUpdateAvailable={acUpdateAvailable}
              acAutoUpdate={acAutoUpdate}
              acStartedAt={acStartedAt}
              sendACRestartApp={sendACRestartApp}
              sendACSetAutoUpdate={sendACSetAutoUpdate}
              hasHighlighted={!!firstHighlighted}
              onGoToHighlighted={goToFirstHighlighted}
              uploadFiles={uploadFiles}
            />
          )}
        </div>
      </div>
    </div>
  )
}
