import { useState, useEffect, useCallback, useRef } from 'react'
import type { ServiceStatus, StatusMsg, FCStateMsg, FCLogMsg } from './types'

const TABS = ['federation-command', 'condoccer', 'worker'] as const
type Tab = typeof TABS[number]

interface LogEntry {
  kind: 'cmd' | 'state'
  text: string
}

function useStatusWS() {
  const [connected, setConnected] = useState(false)
  const [services, setServices] = useState<ServiceStatus[]>([])
  const [fcState, setFcState] = useState<string>('')
  const [fcLog, setFcLog] = useState<LogEntry[]>([])
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
          case 'fc-log':
            setFcLog(prev => [
              ...prev.slice(-199),
              { kind: 'cmd', text: (msg.payload as FCLogMsg).line },
            ])
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

  return { connected, services, fcState, fcLog, sendCommand }
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

export default function App() {
  const [activeTab, setActiveTab] = useState<Tab>('federation-command')
  const { connected, services, fcState, fcLog, sendCommand } = useStatusWS()

  const getStatus = (name: string): string => {
    return services.find(s => s.name === name)?.status ?? 'healthy'
  }

  return (
    <div className="app">
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
          <div className={`health-indicator health-${getStatus(activeTab)}`}>
            <span className="health-dot" />
            <span className="health-label">{getStatus(activeTab)}</span>
          </div>
          {activeTab === 'federation-command' && (
            <FCCommandPanel
              fcState={fcState}
              fcLog={fcLog}
              sendCommand={sendCommand}
            />
          )}
        </div>
      </div>
    </div>
  )
}
