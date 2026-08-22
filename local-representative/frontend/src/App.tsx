import { useState, useEffect, useCallback, useRef } from 'react'
import type { ServiceStatus, StatusMsg } from './types'

const TABS = ['federation-command', 'condoccer', 'worker'] as const
type Tab = typeof TABS[number]

function useStatusWS() {
  const [connected, setConnected] = useState(false)
  const [services, setServices] = useState<ServiceStatus[]>([])
  const wsRef = useRef<WebSocket | null>(null)
  const retryRef = useRef<ReturnType<typeof setTimeout> | null>(null)

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
        if (msg.type === 'status') {
          setServices((msg.payload as StatusMsg).services)
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

  return { connected, services }
}

export default function App() {
  const [activeTab, setActiveTab] = useState<Tab>('federation-command')
  const { connected, services } = useStatusWS()

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
        </div>
      </div>
    </div>
  )
}
