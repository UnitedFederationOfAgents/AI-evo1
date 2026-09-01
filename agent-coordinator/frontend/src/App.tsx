import { useState, useEffect, useCallback, useRef } from 'react'
import type {
  Host, HostsMsg, LRStateMsg, LRFCStateMsg, LRFCLogMsg,
  LRRidealongMsg, LRCondocMsg, ServiceStatus,
} from './types'

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

  const selectHost = useCallback((hostId: string) => {
    wsRef.current?.send(JSON.stringify({ type: 'select-host', payload: { host_id: hostId } }))
  }, [])

  const connect = useCallback(() => {
    const ws = new WebSocket(`ws://${window.location.host}/ws`)
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

  return { connected, hosts, hostData, selectHost, sendLRCommand, sendLRRidealongCommand }
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
type LRService = typeof LR_SERVICES[number]

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

function LRView({
  host, data, sendLRCommand, sendLRRidealongCommand,
}: {
  host: Host
  data: HostClientState
  sendLRCommand: (hostId: string, cmd: string) => void
  sendLRRidealongCommand: (hostId: string, action: string) => void
}) {
  const [activeTab, setActiveTab] = useState<LRService>('federation-command')
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
          {LR_SERVICES.map(svc => (
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
          <div className={`health-indicator health-${getServiceStatus(activeTab)}`}>
            <span className="health-dot" />
            <span className="health-label">{getServiceStatus(activeTab)}</span>
          </div>
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
          {activeTab !== 'federation-command' && !active && (
            <div className="service-empty">local-representative on this host is not connected</div>
          )}
        </div>
      </div>
    </div>
  )
}

export default function App() {
  const { connected, hosts, hostData, selectHost, sendLRCommand, sendLRRidealongCommand } = useCoordinatorWS()
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
