import { useState, useEffect, useCallback, useRef } from 'react'
import type { ServiceStatus, StatusMsg, FCStateMsg, FCLogMsg, RidealongStateMsg, CondocStateMsg, ACStateMsg, ProcInfo, SystemStateMsg } from './types'

const TABS = ['federation-command', 'condoccer', 'worker', 'system'] as const
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
          case 'system-state':
            setSystemState(msg.payload as SystemStateMsg)
            break
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

  return { connected, services, fcState, fcLog, ridealongState, condocState, acState, systemState, sendCommand, sendRidealongCommand, connectToAC, disconnectFromAC, launchApp, terminateApp }
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
}: {
  acState: { connected: boolean; host?: string; port?: string; connecting?: boolean }
  onConnect: (host: string, port: string) => void
  onDisconnect: () => void
}) {
  const [host, setHost] = useState(acState.host ?? 'localhost')
  const [port, setPort] = useState(acState.port ?? '8084')

  if (acState.connected) {
    return (
      <div className="ac-panel ac-panel-connected">
        <span className="ac-label">agent-coordinator</span>
        <span className="ac-addr">{acState.host}:{acState.port}</span>
        <button className="ac-btn ac-btn-disconnect" onClick={onDisconnect}>disconnect</button>
      </div>
    )
  }

  if (acState.connecting) {
    return (
      <div className="ac-panel ac-panel-connecting">
        <span className="ac-label">agent-coordinator</span>
        <span className="ac-connecting">auto-connecting… {acState.host}:{acState.port}</span>
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
        {!proc.managed && <span className="sys-self-tag">this process</span>}
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
  state,
  fcState,
  onLaunch,
  onTerminate,
}: {
  state: SystemStateMsg | null
  fcState: string
  onLaunch: (name: string) => void
  onTerminate: (id: string) => void
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

export default function App() {
  const [activeTab, setActiveTab] = useState<Tab>('federation-command')
  const {
    connected, services, fcState, fcLog,
    ridealongState, condocState, acState, systemState,
    sendCommand, sendRidealongCommand, connectToAC, disconnectFromAC,
    launchApp, terminateApp,
  } = useStatusWS()

  const getStatus = (name: string): string => {
    return services.find(s => s.name === name)?.status ?? 'healthy'
  }

  return (
    <div className="app">
      <ACConnectionPanel
        acState={acState}
        onConnect={connectToAC}
        onDisconnect={disconnectFromAC}
      />
      <div className="tab-bar">
        <span className="app-title">local-representative</span>
        <div className="tabs">
          {TABS.map(tab => (
            <button
              key={tab}
              className={`tab${activeTab === tab ? ' tab-active' : ''}`}
              onClick={() => setActiveTab(tab)}
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
        <div className="service-view">
          <div className="service-name">{activeTab}</div>
          {activeTab === 'system' ? (
            <SystemPanel
              state={systemState}
              fcState={fcState}
              onLaunch={launchApp}
              onTerminate={terminateApp}
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
                    className="condoccer-frame"
                    src="/condoccer/"
                    title="condoccer"
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
      </div>
    </div>
  )
}
