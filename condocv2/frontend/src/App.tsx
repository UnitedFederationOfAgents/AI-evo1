import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { Swimlanes } from './Swimlanes'
import type {
  BranchCheck, CondocState, LockStage, ModeMismatchMsg, Phase, PointerConfig, PointerEntry, PointerStatus,
  ReprStatus, ReprStatusMsg, SelfInfoMsg, SlopspaceProposal, TCAvailabilityMsg,
} from './types'

// condocv2's UI: slopspace condocs only, incompatible with condoccer's. The
// sidebar lists the v2 pointers in the pointer repo; a proposal opens its
// form (Accept begins it), a begun condoc opens with two tabs -- Text, the
// linear Prompt/Response/Record flow with the prompt box, and Visual, the
// swimlanes of the commits each interaction makes (see Swimlanes.tsx).

// ---- WebSocket hook ----

type StemAction =
  | 'withdraw' | 'accept' | 'resubmit' | 'prompt' | 'submit'
  | 'complete' | 'redraft' | 'raise-prs' | 'merge'

type ActionExtra = { text?: string; autoSubmit?: boolean; template?: string }
type OnAction = (type: StemAction, stem: string, extra?: ActionExtra) => void

function useCondocWS() {
  const [connected, setConnected] = useState(false)
  const [activeState, setActiveState] = useState<CondocState | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [reprStatus, setReprStatus] = useState<ReprStatus>('disconnected')
  const [reprHost, setReprHost] = useState('')
  const [reprPort, setReprPort] = useState('')
  const [reprAutoConnect, setReprAutoConnect] = useState(false)
  // local-representative's own HTTP dashboard port -- see tcCaptureURL.
  const [reprHTTPPort, setReprHTTPPort] = useState('')
  const [tcAvailable, setTCAvailable] = useState(false)
  const [devMode, setDevMode] = useState(false)
  const [version, setVersion] = useState('')
  const [modeMismatch, setModeMismatch] = useState<ModeMismatchMsg | null>(null)
  const [pointer, setPointer] = useState<PointerStatus | null>(null)
  const [branchCheck, setBranchCheck] = useState<{ branch: string; checks: BranchCheck[] } | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const subscribedRef = useRef<string>('')

  const send = useCallback((type: string, payload: unknown) => {
    wsRef.current?.send(JSON.stringify({ type, payload }))
  }, [])

  const subscribe = useCallback(
    (stem: string) => {
      if (subscribedRef.current !== stem) setActiveState(null)
      subscribedRef.current = stem
      send('subscribe', { stem })
    },
    [send],
  )

  const stemAction = useCallback(
    (type: StemAction, stem: string, extra?: ActionExtra) => {
      send(type, { stem, ...extra })
    },
    [send],
  )

  const propose = useCallback((p: SlopspaceProposal) => send('propose', p), [send])

  const checkBranch = useCallback(
    (repos: string[], branch: string) => {
      setBranchCheck(null)
      send('check-branch', { repos, branch })
    },
    [send],
  )

  const setPointerRepo = useCallback((cfg: PointerConfig) => send('set-pointer', cfg), [send])
  const clonePointerRepo = useCallback(() => send('clone-pointer', {}), [send])
  const connectRepr = useCallback((host: string, port: string) => send('connect', { host, port }), [send])
  const disconnectRepr = useCallback(() => send('disconnect', {}), [send])
  const setAutoConnectRepr = useCallback(
    (enabled: boolean, host?: string, port?: string) => send('set-auto-connect', { enabled, host, port }),
    [send],
  )

  useEffect(() => {
    // Derive the WebSocket URL from the path this document was served under,
    // so it also works reverse-proxied beneath a prefix (/condocv2/ via
    // local-representative, /host/<id>/condocv2/ via agent-coordinator).
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const dir = window.location.pathname.replace(/\/[^/]*\.[^/]*$/, '/')
    const base = dir.endsWith('/') ? dir.slice(0, -1) : dir
    const wsUrl = `${proto}://${window.location.host}${base}/ws`

    function connect() {
      const ws = new WebSocket(wsUrl)
      wsRef.current = ws

      ws.onopen = () => {
        setConnected(true)
        setError(null)
        if (subscribedRef.current) send('subscribe', { stem: subscribedRef.current })
      }
      ws.onclose = () => {
        setConnected(false)
        setTimeout(connect, 2000)
      }
      ws.onerror = () => setError('WebSocket error — retrying…')
      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data) as { type: string; payload: unknown }
          switch (msg.type) {
            case 'condoc': {
              const st = msg.payload as CondocState
              if (st.entry.stem === subscribedRef.current) setActiveState(st)
              break
            }
            case 'error':
              setError((msg.payload as { message: string }).message)
              break
            case 'pointer':
              setPointer(msg.payload as PointerStatus)
              break
            case 'branch-check': {
              const p = msg.payload as { branch: string; checks: BranchCheck[] | null }
              setBranchCheck({ branch: p.branch, checks: p.checks ?? [] })
              break
            }
            case 'repr-status': {
              const p = msg.payload as ReprStatusMsg
              setReprStatus(p.status)
              if (p.host) setReprHost(p.host)
              if (p.port) setReprPort(p.port)
              setReprAutoConnect(!!p.auto_connect)
              setReprHTTPPort(p.http_port ?? '')
              break
            }
            case 'tc-availability':
              setTCAvailable((msg.payload as TCAvailabilityMsg).available)
              break
            case 'self-info': {
              const p = msg.payload as SelfInfoMsg
              setDevMode(p.dev_mode)
              setVersion(p.version)
              // A rebuild+restart is invisible to an open tab -- reload when
              // the server's version differs from this bundle's (not under
              // the Vite dev server, where HMR keeps the tab current).
              if (!import.meta.env.DEV && p.version && p.version !== __APP_VERSION__) {
                window.location.reload()
              }
              break
            }
            case 'mode-mismatch': {
              const p = msg.payload as ModeMismatchMsg
              setModeMismatch(p.mismatched ? p : null)
              break
            }
          }
        } catch {
          // ignore malformed messages
        }
      }
    }

    connect()
    return () => wsRef.current?.close()
  }, [send])

  return {
    connected, activeState, error, setError, pointer, branchCheck,
    reprStatus, reprHost, reprPort, reprAutoConnect, reprHTTPPort, tcAvailable, devMode, version, modeMismatch,
    subscribe, stemAction, propose, checkBranch, setPointerRepo, clonePointerRepo,
    connectRepr, disconnectRepr, setAutoConnectRepr,
  }
}

// ---- Pointer repo ----

const POINTER_STATE_LABEL: Record<PointerStatus['state'], string> = {
  missing: 'Not cloned',
  cloning: 'Cloning…',
  invalid: 'Not usable',
  ready: 'Ready',
}

// PointerRepoControl ("Set Pointer Repo"): the pointer repo (owner/name), the
// branch condocv2 pulls/pushes, and where it is checked out. When nothing is
// at the path it offers to clone. There is no manual pull: the server pulls
// it continuously.
function PointerRepoControl({ pointer, onSet, onClone }: {
  pointer: PointerStatus
  onSet: (cfg: PointerConfig) => void
  onClone: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<PointerConfig>({ repo: '', branch: '', path: '' })

  if (editing) {
    const submit = () => {
      const cfg = { repo: draft.repo.trim(), branch: draft.branch.trim(), path: draft.path.trim() }
      if (cfg.repo !== pointer.repo || cfg.branch !== pointer.branch || cfg.path !== pointer.path) onSet(cfg)
      setEditing(false)
    }
    const field = (key: keyof PointerConfig, label: string, placeholder: string) => (
      <label className="pointer-field">
        <span className="working-dir-label">{label}</span>
        <input
          className="working-dir-input"
          value={draft[key]}
          spellCheck={false}
          placeholder={placeholder}
          onChange={(e) => setDraft({ ...draft, [key]: e.target.value })}
          onKeyDown={(e) => { if (e.key === 'Escape') setEditing(false) }}
        />
      </label>
    )
    return (
      <form className="working-dir pointer-repo" onSubmit={(e) => { e.preventDefault(); submit() }}>
        {field('repo', 'Pointer repo', 'owner/name')}
        {field('branch', 'Branch', 'main')}
        {field('path', 'Path', '../sloppo (relative to the repo condocv2 started in)')}
        <div className="pointer-actions">
          <button type="submit" className="btn-primary working-dir-btn">Set</button>
          <button type="button" className="btn-secondary working-dir-btn" onClick={() => setEditing(false)}>Cancel</button>
        </div>
      </form>
    )
  }

  return (
    <div className="working-dir pointer-repo">
      <div className="pointer-field">
        <span className="working-dir-label">Pointer repo</span>
        <code className="working-dir-path">{pointer.repo}@{pointer.branch}</code>
      </div>
      <div className="pointer-field">
        <span className="working-dir-label">Path</span>
        <code className="working-dir-path" title={pointer.resolvedPath}>{pointer.resolvedPath}</code>
      </div>
      <div className="pointer-field">
        <span className="working-dir-label">State</span>
        <span className={`pointer-state pointer-state-${pointer.state}`}>{POINTER_STATE_LABEL[pointer.state]}</span>
        {pointer.state === 'ready' && pointer.lastPull && (
          <span className="pointer-note">pulled {new Date(pointer.lastPull).toLocaleTimeString()}</span>
        )}
      </div>
      {pointer.message && <div className="pointer-message">{pointer.message}</div>}
      {pointer.state === 'ready' && pointer.pullError && <div className="pointer-message">{pointer.pullError}</div>}
      {pointer.state === 'ready' && pointer.pushError && <div className="pointer-message">{pointer.pushError} (retried after the next pull)</div>}
      {pointer.holder && <div className="pointer-note">This condocv2 holds condocs as {pointer.holder}. condoccer's (v1) pointers are not listed.</div>}
      {!pointer.tokenAvailable && (
        <div className="pointer-note">No GitHub token on the server (TF_VAR_github_pat) — only public repos can be cloned or pulled.</div>
      )}
      <div className="pointer-actions">
        {pointer.state === 'missing' && (
          <button className="btn-primary working-dir-btn" onClick={onClone}>Clone {pointer.repo}</button>
        )}
        <button
          className="btn-secondary working-dir-btn"
          disabled={pointer.state === 'cloning'}
          onClick={() => { setDraft({ repo: pointer.repo, branch: pointer.branch, path: pointer.path }); setEditing(true) }}
        >
          Set Pointer Repo
        </button>
      </div>
    </div>
  )
}

// ---- The Conversationalist mic-capture ----
//
// condocv2 never touches audio itself: a MicButton opens
// the-conversationalist's own UI in capture mode in a small iframe, which
// posts the final transcript back via window.postMessage.

const MIC_ICON = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
    <rect x="9" y="3" width="6" height="11" rx="3" />
    <path d="M5 11a7 7 0 0 0 14 0" />
    <path d="M12 18v3" />
    <path d="M8 21h8" />
  </svg>
)

// tcCaptureURL: embedded in local-representative (".../condocv2/...") or
// agent-coordinator (".../host/<id>/condocv2/..."), swap the trailing
// "condocv2" segment for "convo" on the same origin; standalone, reach
// local-representative directly through the host/HTTP port it disclosed.
function tcCaptureURL(reprHost: string, reprHTTPPort: string): string | null {
  const path = window.location.pathname
  const idx = path.lastIndexOf('/condocv2/')
  if (idx !== -1) {
    return `${window.location.origin}${path.slice(0, idx)}/convo/?embed=capture`
  }
  if (!reprHost || !reprHTTPPort) return null
  const proto = window.location.protocol === 'https:' ? 'https' : 'http'
  return `${proto}://${reprHost}:${reprHTTPPort}/convo/?embed=capture`
}

interface TCCaptureContextValue {
  available: boolean
  capture: (onTranscript: (text: string) => void) => void
}

const TCCaptureContext = createContext<TCCaptureContextValue>({ available: false, capture: () => {} })

function useTCCapture(tcAvailable: boolean, reprHost: string, reprHTTPPort: string) {
  const [captureURL, setCaptureURL] = useState<string | null>(null)
  const onTranscriptRef = useRef<((text: string) => void) | null>(null)

  const capture = useCallback(
    (onTranscript: (text: string) => void) => {
      const url = tcCaptureURL(reprHost, reprHTTPPort)
      if (!url) return
      onTranscriptRef.current = onTranscript
      setCaptureURL(url)
    },
    [reprHost, reprHTTPPort],
  )

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

function MicButton({ onTranscript }: { onTranscript: (text: string) => void }) {
  const { available, capture } = useContext(TCCaptureContext)
  if (!available) return null
  return (
    <button type="button" className="mic-btn" title="dictate with The Conversationalist" onClick={() => capture(onTranscript)}>
      {MIC_ICON}
    </button>
  )
}

function appendTranscript(prev: string, text: string): string {
  const trimmed = text.trim()
  if (!trimmed) return prev
  return prev.trim() ? `${prev.trim()} ${trimmed}` : trimmed
}

function TCCaptureOverlay({ url, onCancel }: { url: string; onCancel: () => void }) {
  return (
    <div className="tc-capture-backdrop" onClick={onCancel}>
      <div className="tc-capture-panel" onClick={(e) => e.stopPropagation()}>
        <div className="tc-capture-panel-header">
          <span>The Conversationalist</span>
          <button type="button" className="tc-capture-close" onClick={onCancel} aria-label="Cancel dictation">×</button>
        </div>
        <iframe className="tc-capture-frame" src={url} title="the-conversationalist capture" allow="microphone" />
      </div>
    </div>
  )
}

// ---- local-representative link ----

const REPR_STATUS_LABELS: Record<ReprStatus, string> = {
  disconnected: 'LR: disconnected',
  connecting: 'LR: connecting…',
  connected: 'LR: connected',
}

function ReprFooter({ status, host, port, autoConnect, onConnect, onDisconnect, onSetAutoConnect }: {
  status: ReprStatus
  host: string
  port: string
  autoConnect: boolean
  onConnect: (host: string, port: string) => void
  onDisconnect: () => void
  onSetAutoConnect: (enabled: boolean, host?: string, port?: string) => void
}) {
  const [hostInput, setHostInput] = useState(host)
  const [portInput, setPortInput] = useState(port)
  useEffect(() => setHostInput(host), [host])
  useEffect(() => setPortInput(port), [port])

  const autoConnectToggle = (
    <label className="repr-footer-auto-connect" title="keep reaching for local-representative: stays armed across a successful connection -- an explicit disconnect turns it off">
      <input type="checkbox" checked={autoConnect} onChange={(e) => onSetAutoConnect(e.target.checked, hostInput.trim(), portInput.trim())} />
      auto-connect
    </label>
  )

  return (
    <div className="repr-footer">
      <div className="repr-footer-status">
        <span className={`conn-dot ${status === 'connected' ? 'connected' : status === 'connecting' ? 'connecting' : 'disconnected'}`} />
        <span className="repr-footer-label">{REPR_STATUS_LABELS[status]}</span>
      </div>
      {status === 'disconnected' ? (
        <div className="repr-footer-form">
          <input className="repr-footer-input" type="text" placeholder="host" value={hostInput} onChange={(e) => setHostInput(e.target.value)} />
          <input className="repr-footer-input repr-footer-input-port" type="text" placeholder="port" value={portInput} onChange={(e) => setPortInput(e.target.value)} />
          <button className="btn-secondary" onClick={() => onConnect(hostInput.trim(), portInput.trim())}>Connect</button>
          {autoConnectToggle}
        </div>
      ) : (
        <div className="repr-footer-form">
          <span className="repr-footer-addr">{host}:{port}</span>
          {autoConnectToggle}
          <button className="btn-secondary" onClick={onDisconnect}>Disconnect</button>
        </div>
      )}
    </div>
  )
}

// ---- Phase and stage labels ----

const PHASE_LABELS: Record<Phase, string> = {
  proposed: 'Proposed',
  awaiting_prompt: 'Ready',
  locked: 'Locked',
  pr_drafting: 'Drafting PRs',
  pr_proposed: 'PRs drafted',
  pr_open: 'PRs open',
  merged: 'Merged',
}

function PhaseBadge({ phase }: { phase: Phase }) {
  return <span className={`badge badge-v2-${phase}`}>{PHASE_LABELS[phase] ?? phase}</span>
}

const STAGE_LABELS: Record<LockStage, string> = {
  locked: 'inserting the prompt',
  prompted: 'prompt committed — waiting for Submit',
  queued: 'queued for the agent',
  deployed: 'slopspace deployed',
  signalled: 'agent running',
  returned: 'collecting the response',
  responded: 'committing the target repos',
  mapped: 'recording the commits',
  recorded: 'unlocking',
}

function lockSummary(e: PointerEntry): string {
  if (!e.lockInteraction || !e.lockStage) return ''
  let s = `interaction ${e.lockInteraction}: ${STAGE_LABELS[e.lockStage] ?? e.lockStage}`
  if (e.lockStage === 'queued' && !e.running && e.queuePos) s += ` (#${e.queuePos})`
  return s
}

// ---- Proposal ----

function splitRepos(text: string): string[] {
  return text.split(/[\s,]+/).map((r) => r.trim()).filter(Boolean)
}

// ProposalForm proposes a new condoc, or edits one still proposed: its
// targets (1-N, all different), the repo the condoc itself lives in, the
// branch (under condocv2/, and not existing yet in any of them) and the
// agent/model its interactions run with.
function ProposalForm({ pointer, entry, branchCheck, onPropose, onCheckBranch }: {
  pointer: PointerStatus
  entry: PointerEntry | null
  branchCheck: { branch: string; checks: BranchCheck[] } | null
  onPropose: (p: SlopspaceProposal) => void
  onCheckBranch: (repos: string[], branch: string) => void
}) {
  const initial = () => ({
    name: entry?.name ?? '',
    description: entry?.description ?? '',
    targets: (entry?.targets ?? []).join('\n'),
    condocRepo: entry?.condocRepo ?? pointer.repo,
    branch: entry?.branch ?? '',
    agent: entry?.agent ?? pointer.defaultAgent ?? '',
    model: entry?.model ?? pointer.defaultModel ?? '',
  })
  const [draft, setDraft] = useState(initial)
  const seedKey = `${entry?.stem ?? ''}|${entry?.branch ?? ''}|${(entry?.targets ?? []).join(',')}`
  useEffect(() => { setDraft(initial()) }, [seedKey]) // eslint-disable-line react-hooks/exhaustive-deps

  const repos = [...splitRepos(draft.targets), draft.condocRepo.trim()].filter(Boolean)
  const field = (key: keyof typeof draft, label: string, placeholder: string, disabled = false) => (
    <label className="pointer-field">
      <span className="working-dir-label">{label}</span>
      <input
        className="working-dir-input"
        value={draft[key]}
        spellCheck={false}
        disabled={disabled}
        placeholder={placeholder}
        onChange={(e) => setDraft({ ...draft, [key]: e.target.value })}
      />
    </label>
  )
  const check = branchCheck && branchCheck.branch === draft.branch.trim() ? branchCheck.checks : null

  return (
    <form
      className="working-dir pointer-repo slop-form"
      onSubmit={(e) => {
        e.preventDefault()
        onPropose({
          stem: entry?.stem,
          name: draft.name.trim(),
          description: draft.description,
          targets: splitRepos(draft.targets),
          condocRepo: draft.condocRepo.trim(),
          branch: draft.branch.trim(),
          agent: draft.agent.trim(),
          model: draft.model.trim(),
        })
      }}
    >
      {field('name', 'Name', 'MyCondoc (letters, digits, - and _)', !!entry)}
      <label className="pointer-field slop-field-block">
        <span className="working-dir-label">Description</span>
        <textarea
          className="working-dir-input slop-textarea"
          rows={3}
          value={draft.description}
          placeholder="What the condoc is for"
          onChange={(e) => setDraft({ ...draft, description: e.target.value })}
        />
      </label>
      <label className="pointer-field slop-field-block">
        <span className="working-dir-label">Targets</span>
        <textarea
          className="working-dir-input slop-textarea"
          rows={3}
          spellCheck={false}
          value={draft.targets}
          placeholder="owner/repo, one per line"
          onChange={(e) => setDraft({ ...draft, targets: e.target.value })}
        />
      </label>
      {field('condocRepo', 'Condoc repo', 'owner/name (not one of the targets)')}
      {field('branch', 'Branch', entry ? '' : 'blank: condocv2/<Name>-<startTime>/main')}
      {field('agent', 'Agent', 'claude')}
      {field('model', 'Model', "blank: the agent's default")}
      {check && (
        <ul className="slop-branch-check">
          {check.map((c) => (
            <li key={c.repo} className={c.error || c.exists ? 'slop-bad' : 'slop-good'}>
              {c.repo}: {c.error ? c.error : c.exists ? 'branch already exists' : 'free'}
            </li>
          ))}
        </ul>
      )}
      <div className="pointer-actions">
        <button type="submit" className="btn-primary working-dir-btn">{entry ? 'Save Proposal' : 'Propose'}</button>
        <button
          type="button"
          className="btn-secondary working-dir-btn"
          disabled={!draft.branch.trim() || repos.length === 0}
          title={draft.branch.trim() ? '' : 'The default branch is picked when proposing'}
          onClick={() => onCheckBranch(repos, draft.branch.trim())}
        >
          Check Branch
        </button>
      </div>
    </form>
  )
}

// PointerDetails is the read-only summary of a pointer.
function PointerDetails({ entry }: { entry: PointerEntry }) {
  const lock = lockSummary(entry)
  return (
    <div className="slop-details">
      <div className="slop-row">
        <PhaseBadge phase={entry.phase} />
        {entry.phase !== 'proposed' && <span className="step-label">{entry.interactions} interaction{entry.interactions === 1 ? '' : 's'}</span>}
        {lock && <span className="step-label">{entry.running && <span className="spinner" />} {lock}</span>}
        {entry.deployed && <span className="step-label">slopspace deployed</span>}
      </div>
      <div className="slop-row"><span className="working-dir-label">Branch</span><code>{entry.branch}</code></div>
      <div className="slop-row"><span className="working-dir-label">Targets</span><code>{(entry.targets ?? []).join(', ')}</code></div>
      <div className="slop-row">
        <span className="working-dir-label">Condoc</span>
        {entry.condocUrl && entry.phase !== 'proposed'
          ? <a href={entry.condocUrl} target="_blank" rel="noreferrer">{entry.condocRepo}/{entry.condocPath}</a>
          : <code>{entry.condocRepo}/{entry.condocPath}</code>}
      </div>
      {(entry.agent || entry.model) && (
        <div className="slop-row"><span className="working-dir-label">Agent</span><code>{entry.agent}{entry.model ? ` / ${entry.model}` : ''}</code></div>
      )}
      {!entry.heldHere && <div className="pointer-note">Held by {entry.holder || 'nobody'} — shown read-only.</div>}
      {entry.description && <div className="slop-description">{entry.description}</div>}
    </div>
  )
}

// ProposalView is a proposal's main pane: its form with Accept (which begins
// the condoc) and Withdraw, or the read-only details for another holder's.
function ProposalView({ pointer, entry, branchCheck, onPropose, onCheckBranch, onAction }: {
  pointer: PointerStatus
  entry: PointerEntry | null
  branchCheck: { branch: string; checks: BranchCheck[] } | null
  onPropose: (p: SlopspaceProposal) => void
  onCheckBranch: (repos: string[], branch: string) => void
  onAction: (type: StemAction, stem: string) => void
}) {
  if (!entry) {
    return (
      <div className="slop-view">
        <h2>Propose a condoc</h2>
        <ProposalForm pointer={pointer} entry={null} branchCheck={branchCheck} onPropose={onPropose} onCheckBranch={onCheckBranch} />
      </div>
    )
  }
  return (
    <div className="slop-view">
      <h2>{entry.name} <PhaseBadge phase="proposed" /></h2>
      {entry.heldHere ? (
        <>
          <ProposalForm pointer={pointer} entry={entry} branchCheck={branchCheck} onPropose={onPropose} onCheckBranch={onCheckBranch} />
          {entry.error && <div className="slop-error">{entry.error}</div>}
          <div className="pointer-actions">
            <button className="btn-primary working-dir-btn" onClick={() => onAction('accept', entry.stem)}>
              Accept — begin the condoc
            </button>
            <button className="btn-secondary working-dir-btn" onClick={() => onAction('withdraw', entry.stem)}>Withdraw</button>
          </div>
        </>
      ) : (
        <PointerDetails entry={entry} />
      )}
    </div>
  )
}

// ---- The condoc: Text and Visual ----

// InlineLinks renders a record line's markdown links ([`abc1234`](url))
// as anchors and leaves the rest as text.
function InlineLinks({ text }: { text: string }) {
  const out: ReactNode[] = []
  const re = /\[([^\]]+)\]\(([^)\s]+)\)/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) out.push(text.slice(last, m.index))
    const label = m[1].replace(/^`|`$/g, '')
    out.push(<a key={m.index} href={m[2]} target="_blank" rel="noreferrer"><code>{label}</code></a>)
    last = m.index + m[0].length
  }
  if (last < text.length) out.push(text.slice(last))
  return <>{out}</>
}

function RecordBlock({ text }: { text: string }) {
  return (
    <ul className="v2-record">
      {text.split('\n').filter((l) => l.trim()).map((l, i) => (
        <li key={i}><InlineLinks text={l.replace(/^\s*-\s*/, '')} /></li>
      ))}
    </ul>
  )
}

// PromptBox starts the next interaction: the text is locked into the pointer
// and inserted into the condoc as the prompt commit; with auto-submit once
// (the default) it goes straight to the agent.
function PromptBox({ entry, onPrompt }: { entry: PointerEntry; onPrompt: (text: string, autoSubmit: boolean) => void }) {
  const [text, setText] = useState('')
  const [autoSubmit, setAutoSubmit] = useState(true)
  const n = entry.interactions + 1
  return (
    <form
      className="v2-prompt-box"
      onSubmit={(e) => {
        e.preventDefault()
        if (!text.trim()) return
        onPrompt(text, autoSubmit)
        setText('')
      }}
    >
      <div className="v2-section-head v2-section-prompt">Prompt {n}</div>
      <div className="v2-prompt-input">
        <textarea
          className="working-dir-input slop-textarea v2-textarea"
          rows={6}
          value={text}
          placeholder="What should the agent do next?"
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && text.trim()) {
              e.preventDefault()
              onPrompt(text, autoSubmit)
              setText('')
            }
          }}
        />
        <MicButton onTranscript={(t) => setText((prev) => appendTranscript(prev, t))} />
      </div>
      <div className="pointer-actions v2-prompt-actions">
        <label className="list-filter v2-auto-submit" title="submit this prompt to the agent as soon as it is committed -- for this interaction only">
          <input type="checkbox" checked={autoSubmit} onChange={(e) => setAutoSubmit(e.target.checked)} />
          auto-submit once
        </label>
        <button type="submit" className="btn-primary working-dir-btn" disabled={!text.trim()}>
          Lock &amp; Insert Prompt
        </button>
      </div>
    </form>
  )
}

// Completion, after the interactions: Complete (with the PR guidance, which
// defaults to extremely concise) -> the agent drafts the PRs -> edit and
// Raise PRs (or Redraft) -> Merge once every PR is mergeable.
function Completion({ state, onAction }: { state: CondocState; onAction: OnAction }) {
  const e = state.entry
  const [open, setOpen] = useState(false)
  const [guidance, setGuidance] = useState(state.prGuidance || state.defaultPrGuidance)
  const [template, setTemplate] = useState(state.prTemplate ?? '')
  useEffect(() => { setTemplate(state.prTemplate ?? '') }, [state.prTemplate])
  useEffect(() => { setGuidance(state.prGuidance || state.defaultPrGuidance) }, [state.prGuidance, state.defaultPrGuidance])
  const prs = state.prs ?? []
  const held = e.heldHere

  const guidanceBox = (
    <label className="pointer-field slop-field-block">
      <span className="working-dir-label">PR guidance</span>
      <textarea className="working-dir-input slop-textarea" rows={3} value={guidance} onChange={(ev) => setGuidance(ev.target.value)} />
    </label>
  )
  const prList = (
    <ul className="v2-record">
      {prs.map((pr) => (
        <li key={pr.repo}>
          {pr.repo}: <a href={pr.url} target="_blank" rel="noreferrer">#{pr.number}</a> — {pr.state === 'merged' ? 'merged' : pr.status}
        </li>
      ))}
    </ul>
  )

  switch (e.phase) {
    case 'awaiting_prompt':
      if (!held || e.error || e.interactions === 0) return null
      return open ? (
        <div className="v2-interaction">
          <div className="v2-section-head v2-section-record">Complete</div>
          {guidanceBox}
          <div className="pointer-actions">
            <button className="btn-primary working-dir-btn" onClick={() => onAction('complete', e.stem, { text: guidance })}>Complete &amp; Draft PRs</button>
            <button className="btn-secondary working-dir-btn" onClick={() => setOpen(false)}>Cancel</button>
          </div>
        </div>
      ) : (
        <div className="pointer-actions">
          <button className="btn-secondary working-dir-btn" onClick={() => setOpen(true)}>Complete the condoc…</button>
        </div>
      )
    case 'pr_drafting':
      return (
        <div className="v2-interaction">
          <div className="v2-section-head v2-section-record">Pull Requests</div>
          <div className="v2-status">
            {e.running && <span className="spinner" />} {e.running ? 'agent drafting the PRs' : e.queuePos ? `queued to draft the PRs (#${e.queuePos})` : 'drafting the PRs'}
          </div>
        </div>
      )
    case 'pr_proposed':
      return (
        <div className="v2-interaction">
          <div className="v2-section-head v2-section-record">Pull Requests (drafts)</div>
          <textarea
            className="working-dir-input slop-textarea v2-textarea"
            rows={12}
            spellCheck={false}
            value={template}
            readOnly={!held}
            onChange={(ev) => setTemplate(ev.target.value)}
          />
          {held && (
            <>
              {guidanceBox}
              <div className="pointer-actions">
                <button className="btn-primary working-dir-btn" onClick={() => onAction('raise-prs', e.stem, { template })}>Raise PRs</button>
                <button className="btn-secondary working-dir-btn" onClick={() => onAction('redraft', e.stem, { text: guidance, template })}>Redraft</button>
              </div>
            </>
          )}
        </div>
      )
    case 'pr_open':
      return (
        <div className="v2-interaction">
          <div className="v2-section-head v2-section-record">Pull Requests</div>
          {prList}
          {held && (
            <div className="pointer-actions">
              <button
                className="btn-primary working-dir-btn"
                disabled={!prs.every((pr) => pr.ready || pr.state === 'merged')}
                title="merges the targets first and the condoc repo last, then removes the branches and the slopspace"
                onClick={() => onAction('merge', e.stem)}
              >
                Merge
              </button>
            </div>
          )}
        </div>
      )
    case 'merged':
      return (
        <div className="v2-interaction">
          <div className="v2-section-head v2-section-record">Merged</div>
          {prList}
          {e.condocUrl && <div className="pointer-note">The condoc: <a href={e.condocUrl} target="_blank" rel="noreferrer">{e.condocRepo}/{e.condocPath}</a></div>}
        </div>
      )
  }
  return null
}

function TextFlow({ state, onAction }: { state: CondocState; onAction: OnAction }) {
  const e = state.entry
  const bottomRef = useRef<HTMLDivElement | null>(null)
  const count = state.interactions.length
  const lastResponse = state.interactions[count - 1]?.response ?? ''
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: 'end' })
  }, [count, lastResponse.length, e.lockStage])

  const inFlight = !!e.lockInteraction
  return (
    <div className="v2-text">
      {state.interactions.length === 0 && !state.pendingPrompt && e.phase !== 'merged' && (
        <div className="nav-empty">No interactions yet{e.heldHere ? ' — enter the first prompt below.' : '.'}</div>
      )}
      {state.interactions.map((it) => (
        <div key={it.n} className="v2-interaction">
          <div className="v2-section-head v2-section-prompt">Prompt {it.n}</div>
          <pre className="v2-body">{it.prompt}</pre>
          {it.response !== undefined && (
            <>
              <div className="v2-section-head v2-section-response">Response {it.n}</div>
              <pre className="v2-body">{it.response}</pre>
            </>
          )}
          {it.record !== undefined && (
            <>
              <div className="v2-section-head v2-section-record">Record {it.n}</div>
              <RecordBlock text={it.record} />
            </>
          )}
          {inFlight && e.lockInteraction === it.n && (
            <div className="v2-status">
              {e.running && <span className="spinner" />} {lockSummary(e)}
              {e.lockStage === 'prompted' && e.heldHere && (
                <button className="btn-primary working-dir-btn v2-submit" onClick={() => onAction('submit', e.stem)}>
                  Submit to Agent
                </button>
              )}
            </div>
          )}
        </div>
      ))}
      {state.pendingPrompt && (
        <div className="v2-interaction v2-interaction-pending">
          <div className="v2-section-head v2-section-prompt">Prompt {e.lockInteraction} (locked, not yet inserted)</div>
          <pre className="v2-body">{state.pendingPrompt}</pre>
          <div className="v2-status">{lockSummary(e)}</div>
        </div>
      )}
      {e.heldHere && e.phase === 'awaiting_prompt' && !e.error && (
        <PromptBox entry={e} onPrompt={(text, autoSubmit) => onAction('prompt', e.stem, { text, autoSubmit })} />
      )}
      <Completion state={state} onAction={onAction} />
      <div ref={bottomRef} />
    </div>
  )
}

type CondocTab = 'text' | 'visual'

function CondocView({ state, tab, onTab, onAction }: {
  state: CondocState
  tab: CondocTab
  onTab: (t: CondocTab) => void
  onAction: OnAction
}) {
  const e = state.entry
  return (
    <div className="v2-condoc">
      <div className="v2-condoc-head">
        <h2>{e.name}</h2>
        <PointerDetails entry={e} />
        {e.error && (
          <div className="slop-error">
            <div>{e.error}</div>
            {e.heldHere && (
              <button className="btn-primary working-dir-btn" onClick={() => onAction('resubmit', e.stem)}>Resubmit</button>
            )}
          </div>
        )}
        <div className="scheme-tabs v2-tabs" role="tablist">
          {(['text', 'visual'] as const).map((t) => (
            <button
              key={t}
              role="tab"
              aria-selected={tab === t}
              className={`scheme-tab${tab === t ? ' selected' : ''}`}
              onClick={() => onTab(t)}
            >
              {t === 'text' ? 'Text' : 'Visual'}
            </button>
          ))}
        </div>
      </div>
      <div className="v2-condoc-body">
        {tab === 'text' ? (
          <TextFlow state={state} onAction={onAction} />
        ) : (
          <Swimlanes
            timeline={state.timeline}
            interactions={state.interactions}
            pendingPrompt={state.pendingPrompt}
            lockInteraction={e.lockInteraction}
          />
        )}
      </div>
    </div>
  )
}

// ---- URL-hash resume ----
//
// #/condoc/<stem>/<text|visual>, #/propose, or nothing -- so a refresh (or
// an LR/AC iframe remount replaying a captured hash) resumes in place.

interface NavState {
  stem: string | null // a pointer stem, '' for the new-proposal form, null for nothing
  tab: CondocTab
}

function navFromHash(hash: string): NavState {
  const parts = hash.replace(/^#\/?/, '').split('/')
  if (parts[0] === 'propose') return { stem: '', tab: 'text' }
  if (parts[0] === 'condoc' && parts[1]) {
    return { stem: decodeURIComponent(parts[1]), tab: parts[2] === 'visual' ? 'visual' : 'text' }
  }
  return { stem: null, tab: 'text' }
}

function hashFromNav(n: NavState): string {
  if (n.stem === '') return '#/propose'
  if (n.stem) return `#/condoc/${encodeURIComponent(n.stem)}/${n.tab}`
  return ''
}

// ---- Root app ----

export default function App() {
  const ws = useCondocWS()
  const { connected, activeState, error, setError, pointer, branchCheck } = ws
  const initialNav = useRef(navFromHash(window.location.hash)).current
  const [selectedStem, setSelectedStem] = useState<string | null>(initialNav.stem)
  const [tab, setTab] = useState<CondocTab>(initialNav.tab)
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const tcCapture = useTCCapture(ws.tcAvailable, ws.reprHost, ws.reprHTTPPort)

  const selectedEntry = pointer?.pointers.find((p) => p.stem === selectedStem) ?? null
  const isCondoc = !!selectedEntry && selectedEntry.phase !== 'proposed'

  const { subscribe } = ws
  useEffect(() => {
    if (connected) subscribe(isCondoc && selectedStem ? selectedStem : '')
  }, [connected, isCondoc, selectedStem, subscribe])

  useEffect(() => {
    const hash = hashFromNav({ stem: selectedStem, tab })
    if (hash === window.location.hash) return
    const oldURL = window.location.href
    history.replaceState(null, '', hash || window.location.pathname + window.location.search)
    // replaceState never fires 'hashchange'; dispatch one so an embedder
    // tracking this frame's hash (LR/AC) stays in sync.
    window.dispatchEvent(new HashChangeEvent('hashchange', { oldURL, newURL: window.location.href }))
  }, [selectedStem, tab])

  const handleAction: OnAction = (type, stem, extra) => {
    setError(null)
    if (type === 'withdraw') setSelectedStem(null)
    ws.stemAction(type, stem, extra)
  }

  const select = (stem: string | null) => {
    setError(null)
    setSelectedStem(stem)
    setMobileNavOpen(false)
  }

  const footer = (
    <ReprFooter
      status={ws.reprStatus}
      host={ws.reprHost}
      port={ws.reprPort}
      autoConnect={ws.reprAutoConnect}
      onConnect={ws.connectRepr}
      onDisconnect={ws.disconnectRepr}
      onSetAutoConnect={ws.setAutoConnectRepr}
    />
  )

  const ready = connected && pointer !== null && pointer.state === 'ready'

  return (
    <TCCaptureContext.Provider value={tcCapture}>
      <div className={`app${ws.devMode ? ' app-dev-mode' : ''}`}>
        {tcCapture.captureURL && <TCCaptureOverlay url={tcCapture.captureURL} onCancel={tcCapture.cancel} />}
        {ws.modeMismatch && (
          <div className="mode-mismatch-banner">
            ⚠ dev/ops mode mismatch with local-representative ({ws.modeMismatch.peer_mode}):
            only health information is exchanged until this is resolved. See docs/DevMode.md.
          </div>
        )}
        <button className="mobile-nav-toggle" aria-label="Open navigation" onClick={() => setMobileNavOpen(true)}>☰</button>
        {selectedStem !== null && (
          <button className="mobile-back-btn" aria-label="Back" onClick={() => select(null)}>‹</button>
        )}
        {mobileNavOpen && <div className="mobile-nav-backdrop" onClick={() => setMobileNavOpen(false)} />}

        <div className={`sidebar-wrap${mobileNavOpen ? ' mobile-open' : ''}`}>
          <div className="sidebar">
            <div className="sidebar-header">
              <h1>Condoc v2{ws.version && <span className="app-version-tag">{ws.version}</span>}</h1>
            </div>
            <div className="nav-list">
              {pointer?.state === 'ready' && (
                <div className={`nav-item nav-item-new${selectedStem === '' ? ' selected' : ''}`} onClick={() => select('')}>
                  <span className="nav-item-name">+ Propose a condoc</span>
                </div>
              )}
              {pointer?.state !== 'ready' && <div className="nav-empty">Set up the pointer repo to see condocs.</div>}
              {pointer?.state === 'ready' && pointer.pointers.length === 0 && (
                <div className="nav-empty">No v2 condocs in {pointer.repo} yet.</div>
              )}
              {pointer?.state === 'ready' && pointer.pointers.map((p) => (
                <div
                  key={p.file}
                  className={`nav-item${selectedStem === p.stem ? ' selected' : ''}${p.heldHere ? '' : ' nav-item-readonly'}`}
                  title={p.heldHere ? p.file : `${p.file} -- held by ${p.holder || 'nobody'}`}
                  onClick={() => select(p.stem)}
                >
                  <span className="nav-item-name">{p.name}</span>
                  <div className="nav-item-meta">
                    <PhaseBadge phase={p.phase} />
                    {p.phase !== 'proposed' && <span className="step-label">{p.interactions} done</span>}
                    {p.running && <span className="spinner" title="agent turn running" />}
                    {!p.running && p.queuePos ? <span className="step-label">#{p.queuePos} in queue</span> : null}
                    {p.error && <span className="badge badge-error" title={p.error}>error</span>}
                  </div>
                  {p.targets && p.targets.length > 0 && <span className="nav-item-subtitle">→ {p.targets.join(', ')}</span>}
                </div>
              ))}
            </div>
            {footer}
          </div>
        </div>

        <div className="main-content">
          {error && (
            <div className="error-bar">
              {error}
              <button className="btn-secondary" style={{ marginLeft: 12, fontSize: 11, padding: '2px 8px' }} onClick={() => setError(null)}>×</button>
            </div>
          )}

          {ready && pointer && selectedStem !== null && !isCondoc && (selectedStem === '' || selectedEntry) && (
            <ProposalView
              pointer={pointer}
              entry={selectedEntry}
              branchCheck={branchCheck}
              onPropose={(p) => { setError(null); ws.propose(p) }}
              onCheckBranch={ws.checkBranch}
              onAction={handleAction}
            />
          )}

          {ready && isCondoc && activeState && activeState.entry.stem === selectedStem && (
            <CondocView state={activeState} tab={tab} onTab={setTab} onAction={handleAction} />
          )}

          {ready && isCondoc && (!activeState || activeState.entry.stem !== selectedStem) && (
            <div className="empty-state">Loading {selectedEntry?.name}…</div>
          )}

          {(!ready || selectedStem === null || (selectedStem !== '' && !selectedEntry)) && (
            <div className="empty-state empty-state-list">
              <div>
                <span className={`conn-dot ${connected ? 'connected' : 'disconnected'}`} />
                {!connected
                  ? 'Connecting…'
                  : pointer?.state === 'ready'
                    ? 'Select a condoc, or propose one'
                    : 'Set up the condoc pointer repo to use condocv2'}
              </div>
              {connected && pointer && (
                <PointerRepoControl
                  pointer={pointer}
                  onSet={(cfg) => { setError(null); ws.setPointerRepo(cfg) }}
                  onClone={() => { setError(null); ws.clonePointerRepo() }}
                />
              )}
            </div>
          )}
        </div>
      </div>
    </TCCaptureContext.Provider>
  )
}
