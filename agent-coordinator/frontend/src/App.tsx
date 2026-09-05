import { useState, useEffect, useCallback, useRef } from 'react'
import type {
  Host, HostsMsg, LRStateMsg, LRFCStateMsg, LRFCLogMsg,
  LRRidealongMsg, LRCondocMsg, LRSystemStateMsg, LRCondoccerMsg, ProcInfo, ServiceStatus,
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
  condoccer?: LRCondoccerMsg
}

function emptyHostState(): HostClientState {
  return { fcState: '', fcLog: [] }
}

function useCoordinatorWS() {
  const [connected, setConnected] = useState(false)
  const [hosts, setHosts] = useState<Host[]>([])
  const [hostData, setHostData] = useState<Record<string, HostClientState>>({})
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

  const selectHost = useCallback((hostId: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'select-host', payload: { host_id: hostId } }))
  }, [])

  const connect = useCallback(() => {
    const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${wsProto}//${window.location.host}/ws`)
    wsRef.current = ws

    ws.onopen = () => setConnected(true)

    ws.onclose = () => {
      setConnected(false)
      retryRef.current = setTimeout(connect, 2000)
    }

    ws.onerror = () => ws.close()

    ws.onmessage = (ev: MessageEvent) => {
      try {
        const msg = JSON.parse(ev.data as string) as { type: string; payload: unknown }
        switch (msg.type) {
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
          case 'lr-condoccer-state': {
            const p = msg.payload as LRCondoccerMsg
            setHostData(prev => ({
              ...prev,
              [p.host_id]: { ...(prev[p.host_id] ?? emptyHostState()), condoccer: p.available ? p : undefined },
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
    connected, hosts, hostData, selectHost,
    sendLRCommand, sendLRRidealongCommand, sendLRLaunchApp, sendLRTerminateApp,
  }
}

function hostDotClass(status: string): string {
  return status === 'connected' ? 'host-dot-connected' : 'host-dot-disconnected'
}

function HostSidebar({
  hosts, selectedHostId, onSelect,
}: {
  hosts: Host[]
  selectedHostId: string | null
  onSelect: (id: string) => void
}) {
  return (
    <div className="sidebar">
      <div className="sidebar-header">hosts</div>
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
// "system" sits to the right of the service tabs, mirroring local-representative's
// own dashboard: it drives that LR's process management from the coordinator.
const LR_TABS = [...LR_SERVICES, 'system'] as const
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
  proc, nowSec, onTerminate,
}: {
  proc: ProcInfo
  nowSec: number
  onTerminate?: (id: string) => void
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
        {label}
        {!proc.managed && <span className="sys-self-tag">this LR</span>}
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
      </span>
    </div>
  )
}

function SystemPanel({
  hostId, state, active, fcState, onLaunch, onTerminate,
}: {
  hostId: string
  state: LRSystemStateMsg | undefined
  active: boolean
  fcState: string
  onLaunch: (hostId: string, name: string) => void
  onTerminate: (hostId: string, id: string) => void
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
        <SystemProcRow proc={state.self} nowSec={nowSec} />
        {managed.map(p => (
          <SystemProcRow
            key={p.instance_id}
            proc={p}
            nowSec={nowSec}
            onTerminate={id => onTerminate(hostId, id)}
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

function LRView({
  host, data, sendLRCommand, sendLRRidealongCommand, sendLRLaunchApp, sendLRTerminateApp,
}: {
  host: Host
  data: HostClientState
  sendLRCommand: (hostId: string, cmd: string) => void
  sendLRRidealongCommand: (hostId: string, action: string) => void
  sendLRLaunchApp: (hostId: string, name: string) => void
  sendLRTerminateApp: (hostId: string, id: string) => void
}) {
  const [activeTab, setActiveTab] = useState<LRTab>('federation-command')
  const lrState = data.lrState
  const active = lrState?.active ?? false

  const getServiceStatus = (name: string): string => {
    if (!active) return 'unknown'
    return lrState?.services?.find((s: ServiceStatus) => s.name === name)?.status ?? 'unknown'
  }

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
              onClick={() => setActiveTab(svc)}
            >
              {svc}
            </button>
          ))}
        </div>
      </div>
      <div className="main-pane">
        <div className="service-view">
          <div className="service-name">{activeTab}</div>
          {activeTab !== 'system' && (
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
              onLaunch={sendLRLaunchApp}
              onTerminate={sendLRTerminateApp}
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
          {activeTab !== 'federation-command' && activeTab !== 'system' && !active && (
            <div className="service-empty">local-representative on this host is not connected</div>
          )}
        </div>
      </div>
    </div>
  )
}

export default function App() {
  const {
    connected, hosts, hostData, selectHost,
    sendLRCommand, sendLRRidealongCommand, sendLRLaunchApp, sendLRTerminateApp,
  } = useCoordinatorWS()
  const [selectedHostId, setSelectedHostId] = useState<string | null>(null)

  const handleSelectHost = (id: string) => {
    setSelectedHostId(id)
    selectHost(id)
  }

  const selectedHost = hosts.find(h => h.id === selectedHostId) ?? null

  return (
    <div className="app">
      <div className="app-header">
        <span className="app-title">agent-coordinator</span>
        <span
          className={`conn-dot${connected ? ' conn-dot-ok' : ' conn-dot-err'}`}
          title={connected ? 'connected' : 'disconnected'}
        />
      </div>
      <div className="app-body">
        <HostSidebar
          hosts={hosts}
          selectedHostId={selectedHostId}
          onSelect={handleSelectHost}
        />
        <div className="content">
          {selectedHost ? (
            <LRView
              host={selectedHost}
              data={hostData[selectedHost.id] ?? emptyHostState()}
              sendLRCommand={sendLRCommand}
              sendLRRidealongCommand={sendLRRidealongCommand}
              sendLRLaunchApp={sendLRLaunchApp}
              sendLRTerminateApp={sendLRTerminateApp}
            />
          ) : (
            <div className="no-selection">
              <span className="no-selection-text">select a host</span>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
