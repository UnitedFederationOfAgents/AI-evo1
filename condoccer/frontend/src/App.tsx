import { useCallback, useEffect, useRef, useState } from 'react'
import type { ActionRequest, CondocInfo, CondocState, Phase, ServerMsg } from './types'

// ---- WebSocket hook ----

function useCondocWS() {
  const [connected, setConnected] = useState(false)
  const [condocs, setCondocs] = useState<CondocInfo[]>([])
  const [activeState, setActiveState] = useState<CondocState | null>(null)
  const [error, setError] = useState<string | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const subscribedRef = useRef<string>('')

  const send = useCallback((type: string, payload: unknown) => {
    wsRef.current?.send(JSON.stringify({ type, payload }))
  }, [])

  const subscribe = useCallback(
    (path: string) => {
      subscribedRef.current = path
      send('subscribe', { path })
    },
    [send],
  )

  const sendAction = useCallback(
    (action: ActionRequest) => {
      send('action', action)
    },
    [send],
  )

  useEffect(() => {
    const wsUrl = `${window.location.protocol === 'https:' ? 'wss' : 'ws'}://${window.location.host}/ws`

    function connect() {
      const ws = new WebSocket(wsUrl)
      wsRef.current = ws

      ws.onopen = () => {
        setConnected(true)
        setError(null)
        send('list', {})
        if (subscribedRef.current) {
          send('subscribe', { path: subscribedRef.current })
        }
      }

      ws.onclose = () => {
        setConnected(false)
        setTimeout(connect, 2000)
      }

      ws.onerror = () => {
        setError('WebSocket error — retrying…')
      }

      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data) as ServerMsg
          if (msg.type === 'list') {
            setCondocs(msg.payload.condocs ?? [])
          } else if (msg.type === 'condoc') {
            setActiveState(msg.payload)
          } else if (msg.type === 'error') {
            setError(msg.payload.message)
          }
        } catch {
          // ignore malformed messages
        }
      }
    }

    connect()
    return () => wsRef.current?.close()
  }, [send])

  return { connected, condocs, activeState, error, subscribe, sendAction, setError }
}

// ---- Phase helpers ----

const PHASE_LABELS: Record<Phase, string> = {
  proposed: 'Proposed',
  awaiting_step: 'Awaiting Step',
  agent_running: 'Running',
  awaiting_action: 'Awaiting Action',
  completed: 'Completed',
}

function PhaseBadge({ phase }: { phase: Phase }) {
  return <span className={`badge badge-${phase}`}>{PHASE_LABELS[phase]}</span>
}

// ---- Sidebar ----

interface SidebarProps {
  condocs: CondocInfo[]
  selected: string | null
  onSelect: (path: string) => void
}

function Sidebar({ condocs, selected, onSelect }: SidebarProps) {
  return (
    <div className="sidebar">
      <div className="sidebar-header">
        <h1>Condoccer</h1>
      </div>
      <div className="condoc-list">
        {condocs.length === 0 && (
          <div style={{ padding: '16px', color: '#555', fontSize: 12 }}>
            No condocs found in this repository.
          </div>
        )}
        {condocs.map((c) => (
          <div
            key={c.path}
            className={`condoc-item${selected === c.path ? ' selected' : ''}`}
            onClick={() => onSelect(c.path)}
          >
            <div className="condoc-item-name">{c.name}</div>
            <div className="condoc-item-meta">
              <PhaseBadge phase={c.phase} />
              {c.stepNum > 0 && <span className="step-label">step {c.stepNum}</span>}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

// ---- Action panel ----
// Receives the condoc state and a callback that supplies the current edited main
// file content (used only for the start_step action).

type ActionMode = null | 'revision' | 'retry'

interface ActionPanelProps {
  state: CondocState
  onAction: (action: ActionRequest) => void
}

function ActionPanel({ state, onAction }: ActionPanelProps) {
  const { info, nextLetter, fromOptions } = state
  const [mode, setMode] = useState<ActionMode>(null)
  const [promptText, setPromptText] = useState('')
  const [fromSel, setFromSel] = useState('start')

  // Reset form state when condoc path changes.
  useEffect(() => {
    setMode(null)
    setPromptText('')
    setFromSel('start')
  }, [info.path])

  const submitRevision = () => {
    if (!promptText.trim()) return
    onAction({ action: 'revision', path: info.path, letter: nextLetter, content: promptText.trim() })
    setMode(null)
    setPromptText('')
  }

  const submitRetry = () => {
    if (!promptText.trim()) return
    onAction({ action: 'retry', path: info.path, letter: nextLetter, from: fromSel, content: promptText.trim() })
    setMode(null)
    setPromptText('')
  }

  if (info.phase === 'completed') {
    return (
      <div className="action-panel">
        <div className="action-status">
          <span style={{ color: '#4ec94e' }}>✓</span> Condoc completed.
        </div>
      </div>
    )
  }

  if (info.phase === 'agent_running') {
    return (
      <div className="action-panel">
        <div className="action-status">
          <span className="spinner" />
          Agent is working on step {info.stepNum}…
        </div>
      </div>
    )
  }

  if (info.phase === 'proposed') {
    return (
      <div className="action-panel">
        <div className="action-row">
          <button
            className="btn-primary"
            onClick={() => onAction({ action: 'handoff', path: info.path })}
          >
            Accept Proposal →
          </button>
        </div>
      </div>
    )
  }

  if (info.phase === 'awaiting_step') {
    return (
      <div className="action-panel">
        <div style={{ fontSize: 11, color: '#888', marginBottom: 8 }}>
          Click <strong style={{ color: '#ccc' }}>Edit</strong> in the header to fill in the step
          template, then click <strong style={{ color: '#ccc' }}>Start Step</strong>.
        </div>
        <div className="action-row">
          {/* start_step content is injected by CondocView.handleAction */}
          <button
            className="btn-primary"
            onClick={() => onAction({ action: 'start_step', path: info.path })}
          >
            Start Step →
          </button>
          <button
            className="btn-secondary"
            onClick={() => onAction({ action: 'completed', path: info.path })}
          >
            Complete Condoc ✓
          </button>
        </div>
      </div>
    )
  }

  // awaiting_action
  return (
    <div className="action-panel">
      {mode === null && (
        <div className="action-row">
          <button
            className="btn-success"
            onClick={() => onAction({ action: 'completed', path: info.path })}
          >
            Complete Step ✓
          </button>
          <button className="btn-warning" onClick={() => setMode('revision')}>
            Revise {nextLetter}…
          </button>
          <button className="btn-secondary" onClick={() => setMode('retry')}>
            Retry {nextLetter}…
          </button>
        </div>
      )}

      {mode === 'revision' && (
        <div className="action-form">
          <div className="action-form-title">Revision {nextLetter}</div>
          <textarea
            placeholder="Describe the revision you want…"
            value={promptText}
            onChange={(e) => setPromptText(e.target.value)}
            rows={4}
          />
          <div className="action-row">
            <button
              className="btn-warning"
              onClick={submitRevision}
              disabled={!promptText.trim()}
            >
              Submit Revision →
            </button>
            <button className="btn-secondary" onClick={() => setMode(null)}>
              Cancel
            </button>
          </div>
        </div>
      )}

      {mode === 'retry' && (
        <div className="action-form">
          <div className="action-form-title">Retry {nextLetter}</div>
          <div className="action-form-row">
            <span className="action-form-label">From:</span>
            <select value={fromSel} onChange={(e) => setFromSel(e.target.value)}>
              {fromOptions.map((opt) => (
                <option key={opt} value={opt}>
                  {opt}
                </option>
              ))}
            </select>
          </div>
          <textarea
            placeholder="Describe what you want the agent to try differently…"
            value={promptText}
            onChange={(e) => setPromptText(e.target.value)}
            rows={4}
          />
          <div className="action-row">
            <button
              className="btn-secondary"
              onClick={submitRetry}
              disabled={!promptText.trim()}
            >
              Submit Retry →
            </button>
            <button className="btn-secondary" onClick={() => setMode(null)}>
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

// ---- Condoc view ----

interface CondocViewProps {
  state: CondocState
  onAction: (action: ActionRequest) => void
}

function CondocView({ state, onAction }: CondocViewProps) {
  const { info, mainContent, stepContent } = state
  const [editing, setEditing] = useState(false)
  const [editText, setEditText] = useState(mainContent)

  // Reset edit state when condoc or content changes.
  useEffect(() => {
    setEditing(false)
    setEditText(mainContent)
  }, [info.path, mainContent])

  // Intercept start_step to inject the current edit buffer as content.
  const handleAction = (action: ActionRequest) => {
    if (action.action === 'start_step') {
      onAction({ ...action, content: editText })
      setEditing(false)
    } else {
      onAction(action)
    }
  }

  return (
    <div className="condoc-view">
      <div className="condoc-header">
        <h2>{info.name}</h2>
        <PhaseBadge phase={info.phase} />
        {info.stepNum > 0 && (
          <span className="step-label" style={{ color: '#777' }}>
            step {info.stepNum}
          </span>
        )}
        {info.phase === 'awaiting_step' && (
          <button
            className="btn-secondary"
            style={{ marginLeft: 'auto', fontSize: 11 }}
            onClick={() => setEditing((e) => !e)}
          >
            {editing ? 'View' : 'Edit'}
          </button>
        )}
      </div>

      <div className="condoc-panels">
        <div className="file-panel">
          <div className="file-panel-header">Main File</div>
          {editing ? (
            <textarea
              className="file-edit-area"
              value={editText}
              onChange={(e) => setEditText(e.target.value)}
              spellCheck={false}
            />
          ) : (
            <pre className="file-content">{mainContent}</pre>
          )}
        </div>

        {stepContent && (
          <div className="file-panel">
            <div className="file-panel-header">Step {info.stepNum} File</div>
            <pre className="file-content">{stepContent}</pre>
          </div>
        )}
      </div>

      <ActionPanel state={state} onAction={handleAction} />
    </div>
  )
}

// ---- Root app ----

export default function App() {
  const { connected, condocs, activeState, error, subscribe, sendAction, setError } =
    useCondocWS()
  const [selectedPath, setSelectedPath] = useState<string | null>(null)

  const handleSelect = (path: string) => {
    setSelectedPath(path)
    subscribe(path)
    setError(null)
  }

  return (
    <div className="app">
      <Sidebar condocs={condocs} selected={selectedPath} onSelect={handleSelect} />

      <div className="main-content">
        {error && (
          <div className="error-bar">
            {error}
            <button
              className="btn-secondary"
              style={{ marginLeft: 12, fontSize: 11, padding: '2px 8px' }}
              onClick={() => setError(null)}
            >
              ×
            </button>
          </div>
        )}

        {!selectedPath || !activeState ? (
          <div className="empty-state">
            <div>
              <span className={`conn-dot ${connected ? 'connected' : 'disconnected'}`} />
              {connected
                ? condocs.length === 0
                  ? 'No condocs found — point condoccer at a repository containing condoc files'
                  : 'Select a condoc'
                : 'Connecting…'}
            </div>
          </div>
        ) : (
          <CondocView
            state={activeState}
            onAction={(action) => {
              setError(null)
              sendAction(action)
            }}
          />
        )}
      </div>
    </div>
  )
}
