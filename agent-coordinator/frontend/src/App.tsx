import { useState, useEffect, useCallback, useRef } from 'react'
import type { Host, HostsMsg, LRStateMsg } from './types'

function useCoordinatorWS() {
  const [connected, setConnected] = useState(false)
  const [hosts, setHosts] = useState<Host[]>([])
  const [lrStates, setLrStates] = useState<Record<string, LRStateMsg>>({})
  const wsRef = useRef<WebSocket | null>(null)
  const retryRef = useRef<ReturnType<typeof setTimeout> | null>(null)

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
            const payload = msg.payload as LRStateMsg
            setLrStates(prev => ({ ...prev, [payload.host_id]: payload }))
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

  return { connected, hosts, lrStates, selectHost }
}

function hostDotClass(status: string): string {
  switch (status) {
    case 'connected': return 'host-dot-connected'
    case 'disconnected': return 'host-dot-disconnected'
    default: return 'host-dot-unknown'
  }
}

function HostSidebar({
  hosts,
  selectedHostId,
  onSelect,
}: {
  hosts: Host[]
  selectedHostId: string | null
  onSelect: (id: string) => void
}) {
  return (
    <div className="sidebar">
      <div className="sidebar-header">hosts</div>
      {hosts.length === 0 ? (
        <div className="sidebar-empty">no hosts configured</div>
      ) : (
        hosts.map(host => (
          <div
            key={host.id}
            className={`host-item${selectedHostId === host.id ? ' host-item-active' : ''}`}
            onClick={() => onSelect(host.id)}
          >
            <span className={`host-dot ${hostDotClass(host.status)}`} />
            <div className="host-item-text">
              <span className="host-label">{host.label}</span>
              <span className="host-addr">{host.addr}</span>
            </div>
          </div>
        ))
      )}
    </div>
  )
}

const LR_SERVICES = ['federation-command', 'condoccer', 'worker'] as const
type LRService = typeof LR_SERVICES[number]

function LRView({ host, lrState }: { host: Host; lrState: LRStateMsg | undefined }) {
  const [activeTab, setActiveTab] = useState<LRService>('federation-command')

  const getServiceStatus = (name: string): string => {
    if (!lrState?.active) return 'unknown'
    return lrState.services?.find(s => s.name === name)?.status ?? 'unknown'
  }

  return (
    <div className="lr-view">
      <div className="lr-header">
        <span className="lr-host-label">{host.label}</span>
        <span className="lr-host-addr">{host.addr}</span>
        <span className={`lr-conn-badge${lrState?.active ? ' lr-conn-badge-active' : ' lr-conn-badge-inactive'}`}>
          {lrState?.active ? 'connected' : 'not connected'}
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
          {!lrState?.active && (
            <div className="service-empty">
              local-representative on this host is not connected
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

export default function App() {
  const { connected, hosts, lrStates, selectHost } = useCoordinatorWS()
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
              lrState={lrStates[selectedHost.id]}
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
