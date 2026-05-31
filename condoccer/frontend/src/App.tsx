import { useCallback, useEffect, useRef, useState } from 'react'
import type { ActionRequest, CondocInfo, CondocMeta, CondocState, Iteration, Phase, StepSummary } from './types'

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
          const msg = JSON.parse(ev.data) as { type: string; payload: unknown }
          if (msg.type === 'list') {
            const p = msg.payload as { condocs: CondocInfo[] }
            setCondocs(p.condocs ?? [])
          } else if (msg.type === 'condoc') {
            setActiveState(msg.payload as CondocState)
          } else if (msg.type === 'error') {
            const p = msg.payload as { message: string }
            setError(p.message)
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

// ---- Navigation ----

type NavLevel = 'condoc-list' | 'condoc' | 'step'

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

// ---- Step section parser ----

interface StepSection {
  id: string
  label: string
  kind: 'prompt' | 'reply' | 'revision' | 'retry'
  content: string
}

const COMMIT_LINK_RE = /^\[`[a-f0-9]+`\]\([^)]+\)\s*$/gm
const PARENT_LINK_RE = /^\[.*?\]\(.*?\)\s*$/gm
const REPLACE_LINE_RE = /^(?:## )?<REPLACE[^>]*>[^\n]*\n?/gm
const DIRECTIVE_RE = /^!(?:HANDOFF|COMPLETED)!\s*$/gm

function parseStepSections(content: string): StepSection[] {
  const sections: StepSection[] = []

  const firstH2 = /^## /m.exec(content)
  const preContent = firstH2 ? content.slice(0, firstH2.index) : content

  const promptText = preContent
    .replace(/^#\s+.+\n?/m, '')
    .replace(PARENT_LINK_RE, '')
    .replace(COMMIT_LINK_RE, '')
    .trim()

  if (promptText) {
    sections.push({ id: 'prompt', label: 'Prompt', kind: 'prompt', content: promptText })
  }

  const headings: Array<{ index: number; kind: string; letter: string; from: string; fullMatch: string }> = []
  const re = /^## (Reply|Revision|Retry|Human-Prompt)(?: ([A-Z]))?(?: \(from (\w+)\))?/gm
  re.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(content)) !== null) {
    headings.push({ index: m.index, kind: m[1], letter: m[2] ?? '', from: m[3] ?? '', fullMatch: m[0] })
  }

  for (let i = 0; i < headings.length; i++) {
    const h = headings[i]
    if (h.kind === 'Human-Prompt') continue

    const contentStart = h.index + h.fullMatch.length
    const contentEnd = i + 1 < headings.length ? headings[i + 1].index : content.length
    const cleaned = content
      .slice(contentStart, contentEnd)
      .replace(COMMIT_LINK_RE, '')
      .replace(REPLACE_LINE_RE, '')
      .replace(DIRECTIVE_RE, '')
      .trim()

    let id: string, label: string, kind: StepSection['kind']
    if (h.kind === 'Reply') {
      id = h.letter ? `reply-${h.letter}` : 'reply-initial'
      label = h.letter ? `Reply ${h.letter}` : 'Reply'
      kind = 'reply'
    } else if (h.kind === 'Revision') {
      id = `revision-${h.letter}`
      label = `Revision ${h.letter}`
      kind = 'revision'
    } else {
      id = `retry-${h.letter}`
      label = h.from ? `Retry ${h.letter} (from ${h.from})` : `Retry ${h.letter}`
      kind = 'retry'
    }

    sections.push({ id, label, kind, content: cleaned })
  }

  return sections
}

function sectionsToIterations(sections: StepSection[]): Iteration[] {
  return sections
    .filter((s) => s.kind !== 'prompt')
    .map((s) => ({ id: s.id, label: s.label, type: s.kind as Iteration['type'] }))
}

// ---- Sidebar ----

interface SidebarProps {
  navLevel: NavLevel
  condocs: CondocInfo[]
  activeState: CondocState | null
  selectedCondocPath: string | null
  selectedStepNum: number | null
  selectedIterId: string | null
  onSelectCondoc: (path: string) => void
  onSelectStep: (num: number) => void
  onSelectIter: (id: string) => void
  onNavUp: () => void
}

function Sidebar({
  navLevel,
  condocs,
  activeState,
  selectedCondocPath,
  selectedStepNum,
  selectedIterId,
  onSelectCondoc,
  onSelectStep,
  onSelectIter,
  onNavUp,
}: SidebarProps) {
  if (navLevel === 'condoc-list') {
    return (
      <div className="sidebar">
        <div className="sidebar-header">
          <h1>Condoccer</h1>
        </div>
        <div className="nav-list">
          {condocs.length === 0 && (
            <div className="nav-empty">No condocs found in this repository.</div>
          )}
          {condocs.map((c) => (
            <div
              key={c.path}
              className={`nav-item${selectedCondocPath === c.path ? ' selected' : ''}`}
              onClick={() => onSelectCondoc(c.path)}
            >
              <span className="nav-item-name">{c.name}</span>
              <div className="nav-item-meta">
                <PhaseBadge phase={c.phase} />
                {c.stepNum > 0 && <span className="step-label">step {c.stepNum}</span>}
              </div>
            </div>
          ))}
        </div>
      </div>
    )
  }

  if (navLevel === 'condoc' && activeState) {
    return (
      <div className="sidebar">
        <div className="sidebar-header">
          <button className="nav-up-btn" onClick={onNavUp}>↑ condocs</button>
          <div className="sidebar-title">{activeState.info.name}</div>
        </div>
        <div className="nav-list">
          {(activeState.steps ?? []).length === 0 && (
            <div className="nav-empty">No steps yet.</div>
          )}
          {(activeState.steps ?? []).map((s) => (
            <div
              key={s.num}
              className={`nav-item${selectedStepNum === s.num ? ' selected' : ''}`}
              onClick={() => onSelectStep(s.num)}
            >
              <span className="nav-item-name">Step {s.num}</span>
              <div className="nav-item-meta">
                <span className="nav-item-subtitle">{s.hasReplace ? '(needs input)' : s.title}</span>
              </div>
            </div>
          ))}
        </div>
      </div>
    )
  }

  if (navLevel === 'step' && activeState) {
    const iterations: Iteration[] = (() => {
      if (selectedStepNum === null) return []
      if (selectedStepNum === activeState.info.stepNum) return activeState.iterations ?? []
      const content = activeState.completedStepContents?.[selectedStepNum]
      return content ? sectionsToIterations(parseStepSections(content)) : []
    })()

    return (
      <div className="sidebar">
        <div className="sidebar-header">
          <button className="nav-up-btn" onClick={onNavUp}>↑ {activeState.info.name}</button>
          <div className="sidebar-title">Step {selectedStepNum}</div>
        </div>
        <div className="nav-list">
          {iterations.length === 0 && (
            <div className="nav-empty">No iterations yet.</div>
          )}
          {iterations.map((iter) => (
            <div
              key={iter.id}
              className={`nav-item${selectedIterId === iter.id ? ' selected' : ''}`}
              onClick={() => onSelectIter(iter.id)}
            >
              <span className={`nav-iter-dot iter-${iter.type}`} />
              <span className="nav-item-name">{iter.label}</span>
            </div>
          ))}
        </div>
      </div>
    )
  }

  return <div className="sidebar"><div className="sidebar-header"><h1>Condoccer</h1></div></div>
}

// ---- Meta fields ----

function MetaField({ label, value }: { label: string; value: string | number | undefined }) {
  if (!value) return null
  return (
    <div className="meta-field">
      <span className="meta-label">{label}</span>
      <span className="meta-value">{value}</span>
    </div>
  )
}

function CondocMetaSection({ meta }: { meta: CondocMeta }) {
  if (!meta.branch && !meta.controlScheme && !meta.callerPath && !meta.startTime) return null
  return (
    <div className="meta-section">
      <MetaField label="Branch" value={meta.branch} />
      <MetaField label="Control Scheme" value={meta.controlScheme} />
      <MetaField label="Caller Path" value={meta.callerPath} />
      {meta.startTime != null && meta.startTime > 0 && (
        <MetaField label="Start Time" value={new Date(meta.startTime * 1000).toLocaleString()} />
      )}
    </div>
  )
}

// ---- Step card (inside condoc view) ----

interface StepCardProps {
  step: StepSummary
  onStartStep: (title: string, prompt: string) => void
  onCompleted: () => void
  isActive: boolean
}

function StepCard({ step, onStartStep, onCompleted, isActive }: StepCardProps) {
  const [title, setTitle] = useState('')
  const [prompt, setPrompt] = useState('')

  if (step.hasReplace) {
    return (
      <div className="step-card step-card-active">
        <div className="step-card-header">Step {step.num}</div>
        <div className="step-form">
          <label className="step-form-label">Title</label>
          <input
            className="step-form-input"
            type="text"
            placeholder="Step title…"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
          />
          <label className="step-form-label">Prompt</label>
          <textarea
            className="step-form-textarea"
            placeholder="Describe what the AI should do…"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            rows={4}
          />
          <div className="action-row" style={{ marginTop: 8 }}>
            <button
              className="btn-primary"
              onClick={() => onStartStep(title.trim(), prompt.trim())}
              disabled={!title.trim() || !prompt.trim()}
            >
              Start Step →
            </button>
            <button className="btn-secondary" onClick={onCompleted}>
              Complete Condoc ✓
            </button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className={`step-card${isActive ? ' step-card-active' : ''}`}>
      <div className="step-card-header">
        Step {step.num} — <span className="step-card-title">{step.title}</span>
      </div>
      {step.prompt && <div className="step-card-prompt">{step.prompt}</div>}
    </div>
  )
}

// ---- Condoc detail view ----

interface CondocDetailViewProps {
  state: CondocState
  onAction: (action: ActionRequest) => void
}

function CondocDetailView({ state, onAction }: CondocDetailViewProps) {
  const { info, meta, description, steps } = state

  const handleStartStep = (title: string, prompt: string) => {
    const newContent = state.mainContent
      .replace('<REPLACE-TITLE>', title)
      .replace('<REPLACE-PROMPT>', prompt)
    onAction({ action: 'start_step', path: info.path, content: newContent })
  }

  return (
    <div className="detail-view">
      <div className="detail-header">
        <h2>{info.name}</h2>
        <PhaseBadge phase={info.phase} />
      </div>

      <div className="detail-body">
        <CondocMetaSection meta={meta} />

        {description && (
          <div className="detail-section">
            <div className="detail-section-label">Description</div>
            <div className="detail-text">{description}</div>
          </div>
        )}

        {info.phase === 'proposed' && (
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
        )}

        {steps && steps.length > 0 && (
          <div className="detail-section">
            <div className="detail-section-label">Steps</div>
            <div className="step-list">
              {steps.map((s) => (
                <StepCard
                  key={s.num}
                  step={s}
                  isActive={s.num === info.stepNum}
                  onStartStep={handleStartStep}
                  onCompleted={() => onAction({ action: 'completed', path: info.path })}
                />
              ))}
            </div>
          </div>
        )}

        {info.phase === 'agent_running' && (
          <div className="action-panel">
            <div className="action-status">
              <span className="spinner" />
              Agent is working on step {info.stepNum}…
            </div>
          </div>
        )}

        {info.phase === 'completed' && (
          <div className="action-panel">
            <div className="action-status">
              <span style={{ color: '#4ec94e' }}>✓</span> Condoc completed.
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

// ---- Action panel (for step view) ----

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

  useEffect(() => {
    setMode(null)
    setPromptText('')
    setFromSel('start')
  }, [info.path, info.stepNum])

  if (info.phase === 'agent_running') {
    return (
      <div className="action-panel">
        <div className="action-status">
          <span className="spinner" />
          Agent is working…
        </div>
      </div>
    )
  }

  if (info.phase !== 'awaiting_action') return null

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
              onClick={() => {
                if (!promptText.trim()) return
                onAction({ action: 'revision', path: info.path, letter: nextLetter, content: promptText.trim() })
                setMode(null)
                setPromptText('')
              }}
              disabled={!promptText.trim()}
            >
              Submit Revision →
            </button>
            <button className="btn-secondary" onClick={() => setMode(null)}>Cancel</button>
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
                <option key={opt} value={opt}>{opt}</option>
              ))}
            </select>
          </div>
          <textarea
            placeholder="Describe what to try differently…"
            value={promptText}
            onChange={(e) => setPromptText(e.target.value)}
            rows={4}
          />
          <div className="action-row">
            <button
              className="btn-secondary"
              onClick={() => {
                if (!promptText.trim()) return
                onAction({ action: 'retry', path: info.path, letter: nextLetter, from: fromSel, content: promptText.trim() })
                setMode(null)
                setPromptText('')
              }}
              disabled={!promptText.trim()}
            >
              Submit Retry →
            </button>
            <button className="btn-secondary" onClick={() => setMode(null)}>Cancel</button>
          </div>
        </div>
      )}
    </div>
  )
}

// ---- Step detail view ----

interface StepDetailViewProps {
  state: CondocState
  stepNum: number
  selectedIterId: string | null
  onAction: (action: ActionRequest) => void
}

function StepDetailView({ state, stepNum, selectedIterId, onAction }: StepDetailViewProps) {
  const sectionRefs = useRef<Record<string, HTMLDivElement | null>>({})
  const stepSummary = (state.steps ?? []).find((s) => s.num === stepNum)
  const isActiveStep = stepNum === state.info.stepNum

  // Scroll to selected iteration
  useEffect(() => {
    if (selectedIterId && sectionRefs.current[selectedIterId]) {
      sectionRefs.current[selectedIterId]?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    }
  }, [selectedIterId])

  if (!isActiveStep) {
    // Completed step: render all iterations read-only (with nav/scroll support)
    const completedContent = state.completedStepContents?.[stepNum]
    const completedSections = completedContent ? parseStepSections(completedContent) : []
    return (
      <div className="detail-view">
        <div className="detail-header">
          <h2>Step {stepNum}</h2>
          {stepSummary && <span className="detail-step-title">{stepSummary.title}</span>}
          <PhaseBadge phase="completed" />
        </div>
        <div className="detail-body">
          {completedSections.length > 0
            ? completedSections.map((sec) => (
                <div
                  key={sec.id}
                  className={`iter-section iter-section-${sec.kind}${selectedIterId === sec.id ? ' iter-section-selected' : ''}`}
                  ref={(el) => { sectionRefs.current[sec.id] = el }}
                >
                  <div className="iter-section-label">{sec.label}</div>
                  <div className="iter-section-content">{sec.content}</div>
                </div>
              ))
            : stepSummary?.prompt && (
                <div className="detail-section">
                  <div className="detail-section-label">Prompt</div>
                  <div className="detail-text">{stepSummary.prompt}</div>
                </div>
              )}
          <div className="action-panel">
            <div className="action-status" style={{ color: '#4ec94e' }}>✓ Step completed.</div>
          </div>
        </div>
      </div>
    )
  }

  const sections = parseStepSections(state.stepContent ?? '')

  return (
    <div className="detail-view">
      <div className="detail-header">
        <h2>Step {stepNum}</h2>
        {stepSummary && <span className="detail-step-title">{stepSummary.title}</span>}
        <PhaseBadge phase={state.info.phase} />
      </div>
      <div className="detail-body">
        {sections.map((sec) => (
          <div
            key={sec.id}
            className={`iter-section iter-section-${sec.kind}${selectedIterId === sec.id ? ' iter-section-selected' : ''}`}
            ref={(el) => { sectionRefs.current[sec.id] = el }}
          >
            <div className="iter-section-label">{sec.label}</div>
            <div className="iter-section-content">{sec.content}</div>
          </div>
        ))}
      </div>
      <ActionPanel state={state} onAction={onAction} />
    </div>
  )
}

// ---- Root app ----

export default function App() {
  const { connected, condocs, activeState, error, subscribe, sendAction, setError } = useCondocWS()

  const [navLevel, setNavLevel] = useState<NavLevel>('condoc-list')
  const [selectedCondocPath, setSelectedCondocPath] = useState<string | null>(null)
  const [selectedStepNum, setSelectedStepNum] = useState<number | null>(null)
  const [selectedIterId, setSelectedIterId] = useState<string | null>(null)

  const handleSelectCondoc = (path: string) => {
    setSelectedCondocPath(path)
    setSelectedStepNum(null)
    setSelectedIterId(null)
    setNavLevel('condoc')
    subscribe(path)
    setError(null)
  }

  const handleSelectStep = (num: number) => {
    setSelectedStepNum(num)
    setSelectedIterId(null)
    setNavLevel('step')
  }

  const handleSelectIter = (id: string) => {
    setSelectedIterId(id)
  }

  const handleNavUp = () => {
    if (navLevel === 'step') {
      setNavLevel('condoc')
      setSelectedStepNum(null)
      setSelectedIterId(null)
    } else if (navLevel === 'condoc') {
      setNavLevel('condoc-list')
      setSelectedCondocPath(null)
      setSelectedIterId(null)
    }
  }

  const handleAction = (action: ActionRequest) => {
    setError(null)
    sendAction(action)
    if (action.action === 'start_step' && activeState !== null) {
      handleSelectStep(activeState.info.stepNum)
    } else if (action.action === 'completed' && navLevel === 'step') {
      setNavLevel('condoc')
      setSelectedStepNum(null)
      setSelectedIterId(null)
    }
  }

  return (
    <div className="app">
      <Sidebar
        navLevel={navLevel}
        condocs={condocs}
        activeState={activeState}
        selectedCondocPath={selectedCondocPath}
        selectedStepNum={selectedStepNum}
        selectedIterId={selectedIterId}
        onSelectCondoc={handleSelectCondoc}
        onSelectStep={handleSelectStep}
        onSelectIter={handleSelectIter}
        onNavUp={handleNavUp}
      />

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

        {navLevel === 'condoc-list' && (
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
        )}

        {navLevel === 'condoc' && activeState && (
          <CondocDetailView state={activeState} onAction={handleAction} />
        )}

        {navLevel === 'step' && activeState && selectedStepNum !== null && (
          <StepDetailView
            state={activeState}
            stepNum={selectedStepNum}
            selectedIterId={selectedIterId}
            onAction={handleAction}
          />
        )}
      </div>
    </div>
  )
}
