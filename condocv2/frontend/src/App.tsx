import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { Swimlanes } from './Swimlanes'
import { DTTLinkedText, DTTViewer, dttTarget } from './DTTViewer'
import type { DTTTarget, OnOpenDTT } from './DTTViewer'
import { DiffViewer, diffRows, diffRowTitle, externalRow } from './DiffViewer'
import type { OnOpenDiff } from './DiffViewer'
import type {
  AgentOverride, Aside, BranchCheck, CondocState, HighlightedFile, HighlightedFiles, HostFile, LockStage, ModeMismatchMsg, Phase, PointerConfig, PointerEntry, PointerStatus,
  ReprStatus, ReprStatusMsg, ResourceEntry, ResourceSource, ReviewDecision, SelfInfoMsg, SlopspaceProposal, StepEntry, TCAvailabilityMsg,
} from './types'
import { isFinalPhase, isLocalURL, resourceFileURL, scopeTitle, stepAncestors, stepParent, wasMerged } from './types'
import { RepoLink } from './RepoLink'

// condocv2's UI: slopspace condocs only. The
// sidebar lists the v2 pointers in the pointer repo; a proposal opens its
// form (Accept begins it), a begun condoc opens with two tabs -- Text, the
// linear Prompt/Response/Record flow with the prompt box, and Visual, the
// swimlanes of the commits each interaction makes (see Swimlanes.tsx).

// ---- WebSocket hook ----

type StemAction =
  | 'withdraw' | 'accept' | 'resubmit' | 'cancel-turn' | 'prompt' | 'submit' | 'review'
  | 'complete' | 'redraft' | 'raise-prs' | 'merge' | 'archive' | 'abandon' | 'unabandon' | 'configure-repo' | 'add-repo' | 'refresh-remotes'
  | 'enter-step' | 'complete-step' | 'aside' | 'cancel-aside'

type ActionExtra = { text?: string; autoSubmit?: boolean; template?: string; decision?: ReviewDecision; proposal?: string; repo?: string; ref?: string; number?: number }
type OnAction = (type: StemAction, stem: string, extra?: ActionExtra) => void

// ResourceRequest is an "add-resource" message (condocv2/resources.go): the
// resource's title and its files from one source -- uploaded (each file's
// content, base64), the host files tab's entries by id, the highlighted files
// by key (none: all of them), or one text file, typed or dictated.
interface ResourceRequest {
  stem: string
  title: string
  source: ResourceSource
  uploads?: { name: string; data: string }[]
  hostFiles?: string[]
  highlighted?: string[]
  name?: string
  text?: string
}

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
  const [hostFiles, setHostFiles] = useState<HostFile[] | null>(null) // null until listed
  const [highlighted, setHighlighted] = useState<HighlightedFiles | null>(null) // null until listed
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

  const listHostFiles = useCallback(() => {
    setHostFiles(null)
    send('list-host-files', {})
  }, [send])
  const listHighlighted = useCallback(() => {
    setHighlighted(null)
    send('list-highlighted', {})
  }, [send])
  const addResource = useCallback((r: ResourceRequest) => send('add-resource', r), [send])

  const setPointerRepo = useCallback((cfg: PointerConfig) => send('set-pointer', cfg), [send])
  const clonePointerRepo = useCallback(() => send('clone-pointer', {}), [send])
  const setAgentOverride = useCallback((o: AgentOverride) => send('set-agent-override', o), [send])
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
            case 'host-files':
              setHostFiles((msg.payload as { files: HostFile[] | null }).files ?? [])
              break
            case 'highlighted-files': {
              const p = msg.payload as { files: HighlightedFile[] | null; error?: string }
              setHighlighted({ files: p.files ?? [], error: p.error })
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
    subscribe, stemAction, propose, checkBranch, setPointerRepo, clonePointerRepo, setAgentOverride,
    connectRepr, disconnectRepr, setAutoConnectRepr, hostFiles, listHostFiles, highlighted, listHighlighted, addResource,
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
      {pointer.holder && <div className="pointer-note">This condocv2 holds condocs as {pointer.holder}.</div>}
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

// ---- Agent override ----
//
// The agent/model this condocv2 runs every condoc's agent turns (and PR
// drafting) with, in place of each condoc's own -- an application setting,
// kept on this host and never written into a condoc (see override.go).

// The agents ambiguous-agent knows (ambiguous-agent/main.go availableAgents).
const KNOWN_AGENTS = ['claude', 'pi', 'opencode', 'codex', 'gemini', 'copilot', 'grok', 'clod']

const AgentOverrideContext = createContext<AgentOverride | null>(null)

function agentPairLabel(o: AgentOverride): string {
  return o.model ? `${o.agent} / ${o.model}` : o.agent
}

function AgentOverrideControl({ override, onSet }: {
  override: AgentOverride | null
  onSet: (o: AgentOverride) => void
}) {
  const [editing, setEditing] = useState(false)
  const [agent, setAgent] = useState('')
  const [model, setModel] = useState('')

  const open = () => {
    setAgent(override?.agent ?? '')
    setModel(override?.model ?? '')
    setEditing(true)
  }
  const submit = () => {
    onSet({ agent: agent.trim(), model: model.trim() })
    setEditing(false)
  }

  return (
    <div className="repr-footer agent-override">
      <div className="repr-footer-status" title="used for every condoc's agent runs while set, instead of the agent/model each condoc was proposed with; the condocs never see it">
        <span className={`conn-dot ${override ? 'connecting' : 'connected'}`} />
        <span className="repr-footer-label">
          {override ? <>Agent override: <code>{agentPairLabel(override)}</code></> : "Agent: each condoc's own"}
        </span>
      </div>
      {editing ? (
        <form className="agent-override-form" onSubmit={(e) => { e.preventDefault(); submit() }}>
          <input
            className="repr-footer-input"
            list="agent-override-agents"
            placeholder="agent (claude, pi, opencode…)"
            value={agent}
            spellCheck={false}
            autoFocus
            onChange={(e) => setAgent(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Escape') setEditing(false) }}
          />
          <datalist id="agent-override-agents">
            {KNOWN_AGENTS.map((a) => <option key={a} value={a} />)}
          </datalist>
          <input
            className="repr-footer-input"
            placeholder="model (blank: the agent's default)"
            value={model}
            spellCheck={false}
            onChange={(e) => setModel(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Escape') setEditing(false) }}
          />
          <div className="repr-footer-form">
            <button type="submit" className="btn-primary" disabled={!agent.trim()}>Set</button>
            <button type="button" className="btn-secondary" onClick={() => setEditing(false)}>Cancel</button>
          </div>
        </form>
      ) : (
        <div className="repr-footer-form">
          <span className="repr-footer-addr">applies from the next agent run</span>
          <button className="btn-secondary" onClick={open}>{override ? 'Change' : 'Override agent'}</button>
          {override && <button className="btn-secondary" onClick={() => onSet({ agent: '' })}>Clear</button>}
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
  reviewing: 'Review DTT',
  pr_drafting: 'Drafting PRs',
  pr_proposed: 'PRs drafted',
  pr_open: 'PRs open',
  merged: 'Merged',
  archived: 'Archived',
  abandoned: 'Abandoned',
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
// targets (1-N, all different), any read-only repos (read, never changed:
// pulled afresh on every deploy and return, see condocv2/readonly.go), the
// repo the condoc itself lives in, the branch (under condocv2/, and not
// existing yet in any of them) and the agent/model its interactions run with.
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
    readOnly: (entry?.readOnly ?? []).join('\n'),
    condocRepo: entry?.condocRepo ?? pointer.repo,
    branch: entry?.branch ?? '',
    agent: entry?.agent ?? pointer.defaultAgent ?? '',
    model: entry?.model ?? pointer.defaultModel ?? '',
  })
  const [draft, setDraft] = useState(initial)
  const seedKey = `${entry?.stem ?? ''}|${entry?.branch ?? ''}|${(entry?.targets ?? []).join(',')}|${(entry?.readOnly ?? []).join(',')}`
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
          readOnly: splitRepos(draft.readOnly),
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
      <label className="pointer-field slop-field-block">
        <span className="working-dir-label">Read-only</span>
        <textarea
          className="working-dir-input slop-textarea"
          rows={2}
          spellCheck={false}
          value={draft.readOnly}
          placeholder="owner/repo or owner/repo@ref, one per line (optional): read by the agent, never changed — pulled afresh on every deploy and return"
          onChange={(e) => setDraft({ ...draft, readOnly: e.target.value })}
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
  const override = useContext(AgentOverrideContext)
  return (
    <div className="slop-details">
      <div className="slop-row">
        <PhaseBadge phase={entry.phase} />
        {entry.phase !== 'proposed' && <span className="step-label">{entry.interactions} interaction{entry.interactions === 1 ? '' : 's'}</span>}
        {entry.step && <span className="step-label" title="the scope interactions go into now">in {scopeTitle(entry.step)}</span>}
        {lock && <span className="step-label">{entry.running && <span className="spinner" />} {lock}</span>}
        {entry.deployed && <span className="step-label">slopspace deployed</span>}
        {entry.finalFrom && (
          <span className="step-label">
            from {PHASE_LABELS[entry.finalFrom] ?? entry.finalFrom}{entry.finalAt && `, ${new Date(entry.finalAt).toLocaleString()}`}
          </span>
        )}
      </div>
      <div className="slop-row"><span className="working-dir-label">Branch</span><code>{entry.branch}</code></div>
      <div className="slop-row"><span className="working-dir-label">Targets</span><code>{(entry.targets ?? []).join(', ')}</code></div>
      {(entry.readOnly ?? []).length > 0 && (
        <div className="slop-row" title="read by the agent, never changed: pulled afresh on every deploy and return">
          <span className="working-dir-label">Read-only</span><code>{(entry.readOnly ?? []).join(', ')}</code>
        </div>
      )}
      <div className="slop-row">
        <span className="working-dir-label">Condoc</span>
        {entry.condocUrl && entry.phase !== 'proposed'
          ? <RepoLink href={entry.condocUrl}>{entry.condocRepo}/{entry.condocPath}</RepoLink>
          : <code>{entry.condocRepo}/{entry.condocPath}</code>}
      </div>
      {(entry.agent || entry.model || override) && (
        <div className="slop-row" title={override ? "this condocv2's agent override runs this condoc's agent turns; the condoc keeps its own pair" : undefined}>
          <span className="working-dir-label">Agent</span>
          <code className={override ? 'agent-overridden' : undefined}>{entry.agent}{entry.model ? ` / ${entry.model}` : ''}</code>
          {override && <span className="agent-override-note">overridden here: <code>{agentPairLabel(override)}</code></span>}
        </div>
      )}
      {!entry.heldHere && <div className="pointer-note">Held by {entry.holder || 'nobody'} — shown read-only.</div>}
      {entry.description && <div className="slop-description">{entry.description}</div>}
    </div>
  )
}

// CancelTurnButton stops the agent a held condoc's turn is waiting on, after
// a warning: the worker kills it and fails the run, and the turn finishes as
// for any failed run (see condocv2/slopspace.go's slopCancelTurn).
function CancelTurnButton({ entry, onAction }: { entry: PointerEntry; onAction: (type: StemAction, stem: string) => void }) {
  if (!entry.heldHere || !entry.awaitingAgent) return null
  if (entry.cancelRequested) {
    return <button className="btn-secondary working-dir-btn" disabled>Cancelling…</button>
  }
  const warn = `Cancel the agent run for ${entry.name}?\n\n` +
    'The worker kills the agent and fails the run. The turn then finishes on its own: ' +
    'whatever the agent changed before it stopped is committed as this turn\'s work, so review it before prompting again.'
  return (
    <button
      className="btn-secondary working-dir-btn"
      onClick={() => { if (window.confirm(warn)) onAction('cancel-turn', entry.stem) }}
    >
      Cancel turn…
    </button>
  )
}

// AbandonButton forces a held condoc that hasn't merged yet to its end, after
// a warning (a merged one is archived instead). Like Archive it only marks
// the pointer (see condocv2/final.go); Unabandon undoes it.
function AbandonButton({ entry, onAction }: { entry: PointerEntry; onAction: (type: StemAction, stem: string) => void }) {
  if (!entry.heldHere || isFinalPhase(entry.phase) || entry.phase === 'merged') return null
  const warn = `Abandon ${entry.name}?\n\n` +
    'It moves to a final state: condocv2 stops driving it and hides it unless "show final" is on, until you Unabandon it. ' +
    'Nothing else is touched -- its slopspace, branches and any open PRs stay as they are, for you to clean up.'
  return (
    <button
      className="btn-secondary working-dir-btn btn-abandon"
      onClick={() => { if (window.confirm(warn)) onAction('abandon', entry.stem) }}
    >
      Abandon…
    </button>
  )
}

// splitReadOnly splits owner/name[@ref] (see condocv2/readonly.go).
function splitReadOnly(s: string): { repo: string; ref: string } {
  const i = s.lastIndexOf('@')
  return i > 0 ? { repo: s.slice(0, i), ref: s.slice(i + 1) } : { repo: s, ref: '' }
}

// ConfigureCondoc is the "Configure condoc" control of a held condoc (see
// condocv2/configure.go), shown whether or not it has read-only repos. For
// now it has one subsection, Repos: rename the branch (or tag, or commit) a
// read-only repo follows, or add a new read-only repo mid-condoc. Each change
// is done within a momentary lock in the pointer repo -- configure-lock N.c,
// the slopspace pulls the new ref (or the new repo), configure-unlock N.c --
// so the repos and refs stay in the version-controlled record.
//
// Beside it, Refresh remotes (condocv2/external.go) pulls in whatever was
// pushed to the condoc's branches from outside condocv2. Finding nothing shows
// nothing; finding something is an "external" section on the Visual tab. A
// prompt does the same by itself just before it takes the lock.
//
// And Add resource (condocv2/resources.go) adds files to the condoc between
// interactions -- see AddResource.
function ConfigureCondoc({ entry, onAction }: { entry: PointerEntry; onAction: OnAction }) {
  const [open, setOpen] = useState(false)
  const [adding, setAdding] = useState(false)
  const [refs, setRefs] = useState<Record<string, string>>({})
  const [added, setAdded] = useState('')
  if (!entry.heldHere || isFinalPhase(entry.phase)) return null
  const repos = (entry.readOnly ?? []).map(splitReadOnly)
  const blocked = entry.configureBlocked
  const refreshBlocked = entry.refreshBlocked
  return (
    <div className="slop-details">
      <div className="slop-row">
        <button className="btn-secondary working-dir-btn" onClick={() => setOpen(!open)}>
          {open ? 'Close condoc configuration' : 'Configure condoc'}
        </button>
        <button
          className="btn-secondary working-dir-btn"
          disabled={!!refreshBlocked}
          title={refreshBlocked
            ? `Not now: ${refreshBlocked}`
            : "pull in commits pushed to the condoc's branches from outside condocv2 -- shown as an external change on the Visual tab"}
          onClick={() => onAction('refresh-remotes', entry.stem)}
        >
          Refresh remotes
        </button>
        <button
          className="btn-secondary working-dir-btn"
          title="add files to the condoc for the agent -- uploaded, from this host's files, the highlighted files, entered as text, or dictated -- shown as a resource on the Visual tab"
          onClick={() => setAdding(!adding)}
        >
          {adding ? 'Close resource' : 'Add resource'}
        </button>
        {entry.configuring && <span className="step-label"><span className="spinner" /> configuring {entry.configuring}</span>}
      </div>
      {adding && <AddResource entry={entry} onDone={() => setAdding(false)} />}
      {open && (
        <>
          {blocked && <div className="pointer-note">Not now: {blocked}.</div>}
          <div className="slop-row"><span className="working-dir-label">Repos</span></div>
          <div className="pointer-note">
            Rename the branch (or tag, or commit) a read-only repo follows -- empty follows its default branch -- or add a
            read-only repo. Each change is made within a momentary lock in the pointer repo, and the slopspace pulls the
            new ref or repo straight away; from the next interaction on the agent is told about it.
          </div>
          {repos.length === 0 && <div className="pointer-note">No read-only repos yet.</div>}
          {repos.map(({ repo, ref }) => {
            const draft = refs[repo] ?? ref
            return (
              <div className="slop-row" key={repo}>
                <span className="working-dir-label">{repo}</span>
                <input
                  className="working-dir-input"
                  value={draft}
                  placeholder="default branch"
                  onChange={(ev) => setRefs({ ...refs, [repo]: ev.target.value })}
                />
                <button
                  className="btn-primary working-dir-btn"
                  disabled={!!blocked || draft.trim() === ref}
                  title={`now follows ${ref || 'the default branch'}`}
                  onClick={() => onAction('configure-repo', entry.stem, { repo, ref: draft.trim() })}
                >
                  Set
                </button>
              </div>
            )
          })}
          <div className="slop-row">
            <span className="working-dir-label">Add</span>
            <input
              className="working-dir-input"
              value={added}
              placeholder="owner/name[@ref] (read-only)"
              onChange={(ev) => setAdded(ev.target.value)}
            />
            <button
              className="btn-primary working-dir-btn"
              disabled={!!blocked || added.trim() === ''}
              title="add a read-only repo: cloned and added to the slopspace now, kept on the pointer"
              onClick={() => { onAction('add-repo', entry.stem, { repo: added.trim() }); setAdded('') }}
            >
              Add
            </button>
          </div>
          {(entry.configurations ?? []).length > 0 && (
            <div className="slop-row">
              <span className="working-dir-label">History</span>
              <code style={{ whiteSpace: 'pre-line' }}>{(entry.configurations ?? []).join('\n')}</code>
            </div>
          )}
          {(entry.externals ?? []).length > 0 && (
            <div className="slop-row">
              <span className="working-dir-label">External</span>
              <code style={{ whiteSpace: 'pre-line' }}>{(entry.externals ?? []).join('\n')}</code>
            </div>
          )}
        </>
      )}
    </div>
  )
}

// ResourceContext is how AddResource lists this host's files and sends a
// resource (see useCondocWS).
interface ResourceContextValue {
  hostFiles: HostFile[] | null
  listHostFiles: () => void
  highlighted: HighlightedFiles | null
  listHighlighted: () => void
  addResource: (r: ResourceRequest) => void
}
const ResourceContext = createContext<ResourceContextValue>({
  hostFiles: null, listHostFiles: () => {}, highlighted: null, listHighlighted: () => {}, addResource: () => {},
})

const RESOURCE_SOURCES: { source: ResourceSource; label: string; hint: string }[] = [
  { source: 'upload', label: 'Upload', hint: 'files from this machine, through the browser' },
  { source: 'files', label: 'Host files', hint: "this host's files tab: its host-cache and host-store" },
  { source: 'highlighted', label: 'Highlighted', hint: "the files highlighted in local-representative's files tab, and in those of the other hosts connected to agent-coordinator" },
  { source: 'text', label: 'Text', hint: 'text entered here, kept as one file' },
  { source: 'voice', label: 'Voice', hint: 'text dictated with The Conversationalist, kept as one file' },
]

const MAX_RESOURCE_BYTES = 50 << 20 // condocv2/resources.go's maxResourceBytes

function byteSize(n: number): string {
  return n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : n >= 1 << 10 ? `${(n / (1 << 10)).toFixed(1)} KB` : `${n} bytes`
}

// readBase64 reads a picked file's content as base64.
function readBase64(f: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => {
      const url = String(r.result)
      resolve(url.slice(url.indexOf(',') + 1))
    }
    r.onerror = () => reject(r.error)
    r.readAsDataURL(f)
  })
}

// resourceSourceWord says where a resource's files came from.
const RESOURCE_SOURCE_WORDS: Record<ResourceSource, string> = {
  upload: 'uploaded',
  files: "from the host's files",
  highlighted: 'highlighted in the files tab',
  text: 'entered as text',
  voice: 'dictated by voice',
}
const resourceSourceWord = (s: ResourceSource) => RESOURCE_SOURCE_WORDS[s] ?? s

// AddResource is Add resource's form (condocv2/resources.go): a title, and
// the files from one source -- uploaded, picked from this host's files tab,
// picked from the highlighted files (condocv2/highlighted.go), entered as
// text, or dictated. Adding one is made between interactions only: the
// files go into the condoc under resources/R/ with a "Resource R" section in
// the scope it is in now, then the pointer -- a section of its own on the
// Visual tab.
function AddResource({ entry, onDone }: { entry: PointerEntry; onDone: () => void }) {
  const { hostFiles, listHostFiles, highlighted, listHighlighted, addResource } = useContext(ResourceContext)
  const { available: voiceAvailable, capture } = useContext(TCCaptureContext)
  const [source, setSource] = useState<ResourceSource>('upload')
  const [title, setTitle] = useState('')
  const [uploads, setUploads] = useState<File[]>([])
  const [picked, setPicked] = useState<Set<string>>(new Set())
  // The highlighted files left out: all of them are taken unless unticked.
  const [unpicked, setUnpicked] = useState<Set<string>>(new Set())
  const [name, setName] = useState('resource.md')
  const [text, setText] = useState('')
  const [voiceName, setVoiceName] = useState('dictation.md')
  const [voiceText, setVoiceText] = useState('')
  const [sending, setSending] = useState(false)
  const [problem, setProblem] = useState('')
  // The host's files and the highlighted ones, listed afresh each time they
  // are picked from.
  useEffect(() => {
    if (source === 'files') listHostFiles()
    if (source === 'highlighted') listHighlighted()
  }, [source, listHostFiles, listHighlighted])
  const blocked = entry.resourceBlocked
  const next = (entry.resources?.length ?? 0) + 1
  const uploadBytes = uploads.reduce((sum, f) => sum + f.size, 0)
  const highlightedPicked = (highlighted?.files ?? []).filter((f) => !unpicked.has(f.key))
  const highlightedBytes = highlightedPicked.reduce((sum, f) => sum + f.size, 0)
  const ready = source === 'upload' ? uploads.length > 0 && uploadBytes <= MAX_RESOURCE_BYTES
    : source === 'files' ? picked.size > 0
      : source === 'highlighted' ? highlightedPicked.length > 0 && highlightedBytes <= MAX_RESOURCE_BYTES
        : source === 'voice' ? voiceText.trim() !== ''
          : text.trim() !== ''
  const dictate = () => capture((t) => setVoiceText((prev) => appendTranscript(prev, t)))
  const add = async () => {
    setProblem('')
    const req: ResourceRequest = { stem: entry.stem, title: title.trim(), source }
    if (source === 'upload') {
      setSending(true)
      try {
        req.uploads = await Promise.all(uploads.map(async (f) => ({ name: f.name, data: await readBase64(f) })))
      } catch (err) {
        setProblem(`couldn't read the files: ${String(err)}`)
        setSending(false)
        return
      }
      setSending(false)
    } else if (source === 'files') {
      req.hostFiles = Array.from(picked)
    } else if (source === 'highlighted') {
      req.highlighted = highlightedPicked.map((f) => f.key)
    } else if (source === 'voice') {
      req.name = voiceName.trim() || 'dictation.md'
      req.text = voiceText
    } else {
      req.name = name.trim() || 'resource.md'
      req.text = text
    }
    addResource(req)
    onDone()
  }
  const toggle = (id: string) => {
    const s = new Set(picked)
    if (s.has(id)) s.delete(id)
    else s.add(id)
    setPicked(s)
  }
  const toggleHighlighted = (key: string) => {
    const s = new Set(unpicked)
    if (s.has(key)) s.delete(key)
    else s.add(key)
    setUnpicked(s)
  }
  return (
    <div className="v2-resource-form">
      <div className="pointer-note">
        Resource {next}: its files go into the condoc beside the step files, under <code>resources/{next}/</code>, with a
        "Resource {next}" section in {scopeTitle(entry.step ?? '')} — the agent reads them from the next interaction on.
      </div>
      {blocked && <div className="pointer-note">Not now: {blocked}.</div>}
      <div className="slop-row">
        <span className="working-dir-label">Title</span>
        <div className="field-with-mic">
          <input className="working-dir-input" value={title} placeholder="what it is (optional)" onChange={(ev) => setTitle(ev.target.value)} />
          <MicButton onTranscript={(t) => setTitle((prev) => appendTranscript(prev, t))} />
        </div>
      </div>
      <div className="slop-row v2-resource-sources" role="tablist">
        <span className="working-dir-label">From</span>
        {RESOURCE_SOURCES.map((s) => (
          <button
            key={s.source}
            role="tab"
            aria-selected={source === s.source}
            className={`scheme-tab${source === s.source ? ' selected' : ''}`}
            title={s.source === 'voice' && !voiceAvailable ? `${s.hint} -- The Conversationalist isn't available now` : s.hint}
            onClick={() => setSource(s.source)}
          >
            {s.label}
          </button>
        ))}
      </div>
      {source === 'upload' && (
        <>
          <div className="slop-row">
            <input type="file" multiple onChange={(ev) => setUploads(Array.from(ev.target.files ?? []))} />
          </div>
          {uploads.length > 0 && (
            <ul className="v2-resource-files">
              {uploads.map((f) => <li key={f.name}>{f.name} <span className="step-label">{byteSize(f.size)}</span></li>)}
            </ul>
          )}
          {uploadBytes > MAX_RESOURCE_BYTES && (
            <div className="pointer-note">{byteSize(uploadBytes)}: a resource takes at most {byteSize(MAX_RESOURCE_BYTES)}.</div>
          )}
        </>
      )}
      {source === 'files' && (
        <>
          <div className="slop-row">
            <span className="pointer-note">Pick from this host's files tab (host-cache and host-store).</span>
            <button className="btn-secondary working-dir-btn" onClick={listHostFiles}>Refresh list</button>
          </div>
          {hostFiles === null && <div className="pointer-note"><span className="spinner" /> listing…</div>}
          {hostFiles !== null && hostFiles.length === 0 && <div className="pointer-note">The host's files tab is empty.</div>}
          {hostFiles !== null && hostFiles.length > 0 && (
            <ul className="v2-resource-files v2-resource-pick">
              {hostFiles.map((f) => (
                <li key={f.id}>
                  <label>
                    <input type="checkbox" checked={picked.has(f.id)} onChange={() => toggle(f.id)} />
                    {f.name} <span className="step-label">{byteSize(f.size)} · {f.state}</span>
                  </label>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      {source === 'highlighted' && (
        <>
          <div className="slop-row">
            <span className="pointer-note">The files highlighted in local-representative's files tab, and on the other hosts connected to agent-coordinator. Untick any to leave out.</span>
            <button className="btn-secondary working-dir-btn" onClick={listHighlighted}>Refresh list</button>
          </div>
          {highlighted === null && <div className="pointer-note"><span className="spinner" /> listing…</div>}
          {highlighted?.error && <div className="slop-error">{highlighted.error}</div>}
          {highlighted !== null && !highlighted.error && highlighted.files.length === 0 && (
            <div className="pointer-note">No files are highlighted. Highlight some in local-representative's files tab (a file's details, "highlight"), then refresh.</div>
          )}
          {highlighted !== null && highlighted.files.length > 0 && (
            <ul className="v2-resource-files v2-resource-pick">
              {highlighted.files.map((f) => (
                <li key={f.key}>
                  <label>
                    <input type="checkbox" checked={!unpicked.has(f.key)} onChange={() => toggleHighlighted(f.key)} />
                    <span className="v2-highlighted-mark" aria-hidden="true" />
                    {f.name} <span className="step-label">{byteSize(f.size)} · {f.node || 'this host'}</span>
                  </label>
                </li>
              ))}
            </ul>
          )}
          {highlightedBytes > MAX_RESOURCE_BYTES && (
            <div className="pointer-note">{byteSize(highlightedBytes)}: a resource takes at most {byteSize(MAX_RESOURCE_BYTES)}.</div>
          )}
        </>
      )}
      {source === 'text' && (
        <>
          <div className="slop-row">
            <span className="working-dir-label">File</span>
            <input className="working-dir-input" value={name} placeholder="resource.md" onChange={(ev) => setName(ev.target.value)} />
          </div>
          <div className="field-with-mic">
            <textarea className="v2-resource-text" value={text} rows={8} placeholder="the resource's text" onChange={(ev) => setText(ev.target.value)} />
            <MicButton onTranscript={(t) => setText((prev) => appendTranscript(prev, t))} />
          </div>
        </>
      )}
      {source === 'voice' && (
        <>
          {!voiceAvailable && (
            <div className="pointer-note">
              The Conversationalist isn't available now, so there's nothing to dictate with. Start it and connect it to
              local-representative; this tab picks it up as soon as it is reachable.
            </div>
          )}
          <div className="slop-row">
            <span className="working-dir-label">File</span>
            <input className="working-dir-input" value={voiceName} placeholder="dictation.md" onChange={(ev) => setVoiceName(ev.target.value)} />
          </div>
          <div className="slop-row">
            <button
              type="button"
              className="btn-secondary working-dir-btn v2-dictate-btn"
              disabled={!voiceAvailable}
              title={voiceAvailable ? 'dictate with The Conversationalist; each dictation is added to the end of the text' : "The Conversationalist isn't available"}
              onClick={dictate}
            >
              {MIC_ICON} {voiceText.trim() ? 'Dictate more' : 'Dictate'}
            </button>
            {voiceText.trim() && <button type="button" className="btn-secondary working-dir-btn" onClick={() => setVoiceText('')}>Clear</button>}
          </div>
          <textarea
            className="v2-resource-text"
            value={voiceText}
            rows={6}
            placeholder="what you dictate appears here, and can be corrected before it is added"
            onChange={(ev) => setVoiceText(ev.target.value)}
          />
        </>
      )}
      {problem && <div className="slop-error">{problem}</div>}
      <div className="slop-row">
        <button
          className="btn-primary working-dir-btn"
          disabled={!!blocked || !ready || sending}
          title={blocked ? `Not now: ${blocked}` : `add resource ${next} to the condoc`}
          onClick={() => { void add() }}
        >
          {sending ? 'Reading…' : `Add resource ${next}`}
        </button>
      </div>
      {(entry.resources ?? []).length > 0 && (
        <div className="slop-row">
          <span className="working-dir-label">Added</span>
          <code style={{ whiteSpace: 'pre-line' }}>
            {(entry.resources ?? []).map((r) => `${r.number} after ${r.interaction}${r.step ? ` (${scopeTitle(r.step)})` : ''}: ${r.title || r.files.join(', ')}`).join('\n')}
          </code>
        </div>
      )}
    </div>
  )
}

// ResourceMarker is a resource added, in the text of the scope it was added
// in: its title and its files, each opening in a new tab.
function ResourceMarker({ stem, resource: r }: { stem: string; resource: ResourceEntry }) {
  return (
    <div className="v2-interaction v2-step-marker">
      <div className="v2-section-head v2-section-step">🗎 Resource {r.number}{r.title ? ` — ${r.title}` : ''}</div>
      <div className="pointer-note">Added after interaction {r.interaction}, {resourceSourceWord(r.source)}:</div>
      <ul className="v2-resource-files">
        {r.files.map((f) => (
          <li key={f}><a href={resourceFileURL(stem, r.number, f)} target="_blank" rel="noreferrer"><code>{f}</code></a></li>
        ))}
      </ul>
    </div>
  )
}

// canAside is whether the condoc can take an aside (condocv2/aside.go's
// asideBlocked): it has a slopspace, and its interactions are going on --
// not while it is held for DTT proposals, when its slopspace can't be deployed.
function canAside(e: PointerEntry): boolean {
  return !!e.slopspaceId && (e.phase === 'awaiting_prompt' || e.phase === 'locked')
}

const ASIDE_STATUS_WORDS: Record<Aside['status'], string> = {
  queued: 'queued — waits its turn behind any interaction',
  running: 'the agent is answering (read-only)',
  completed: 'answered',
  failed: 'failed',
  cancelled: 'cancelled',
}

// AsideMarker is an aside (condocv2/aside.go), in the text of the scope it
// ran in, after the interaction before it: the question and the agent's
// answer. It is this host's alone -- never in the step file or a repo.
function AsideMarker({ aside: a, onCancel }: { aside: Aside; onCancel?: () => void }) {
  const live = a.status === 'queued' || a.status === 'running'
  return (
    <div className="v2-interaction v2-aside">
      <div className="v2-section-head v2-section-aside">
        Aside {a.number} · after interaction {a.interaction}
        {a.agent ? ` · ${[a.agent, a.model].filter(Boolean).join(' ')}` : ''}
      </div>
      <pre className="v2-body">{a.prompt}</pre>
      {a.reply !== undefined && a.reply !== '' && (
        <>
          <div className="v2-section-head v2-section-aside">Answer {a.number}</div>
          <pre className="v2-body">{a.reply}</pre>
        </>
      )}
      <div className="v2-status">
        {live && <span className="spinner" />} {ASIDE_STATUS_WORDS[a.status] ?? a.status}
        {a.error ? `: ${a.error}` : ''}
        {live && onCancel && (
          <button type="button" className="btn-secondary working-dir-btn" onClick={onCancel}>Cancel aside</button>
        )}
      </div>
      {(a.changed ?? []).length > 0 && (
        <div className="pointer-note">
          The agent changed {(a.changed ?? []).join(', ')} despite running read-only; those changes are still there, and the next interaction will commit them unless you undo them.
        </div>
      )}
      <div className="pointer-note">Kept in this host's history only: not in the condoc, though later agent runs can read it.</div>
    </div>
  )
}

// AsideBox asks an aside: a question the agent answers read-only, which
// never goes into the condoc (condocv2/aside.go). It can be asked while an
// interaction runs; it waits its turn behind it.
function AsideBox({ entry, next, onAside }: { entry: PointerEntry; next: number; onAside: (text: string) => void }) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const ask = () => {
    if (!text.trim()) return
    onAside(text)
    setText('')
    setOpen(false)
  }
  if (!open) {
    return (
      <div className="pointer-actions v2-aside-actions">
        <button type="button" className="btn-secondary working-dir-btn" onClick={() => setOpen(true)}
          title="ask the agent something about the condoc, read-only: its answer is kept on this host, never in the condoc">
          Aside…
        </button>
      </div>
    )
  }
  return (
    <form className="v2-prompt-box v2-aside-box" onSubmit={(ev) => { ev.preventDefault(); ask() }}>
      <div className="v2-section-head v2-section-aside">Aside {next}{entry.step ? ` · ${scopeTitle(entry.step)}` : ''}</div>
      <div className="pointer-note">
        The agent sees the condoc but runs read-only, and neither the question nor its answer goes into the condoc: they are
        kept in this host's history, shown here and as a red bubble on the Visual tab, and later agent runs can read them.
      </div>
      <div className="v2-prompt-input">
        <textarea
          className="working-dir-input slop-textarea v2-textarea"
          rows={4}
          value={text}
          placeholder="What do you want to ask, off the record?"
          onChange={(ev) => setText(ev.target.value)}
          onKeyDown={(ev) => {
            if (ev.key === 'Enter' && (ev.metaKey || ev.ctrlKey)) {
              ev.preventDefault()
              ask()
            }
          }}
        />
        <MicButton onTranscript={(t) => setText((prev) => appendTranscript(prev, t))} />
      </div>
      <div className="pointer-actions v2-prompt-actions">
        <button type="button" className="btn-secondary working-dir-btn" onClick={() => setOpen(false)}>Close</button>
        <button type="submit" className="btn-primary working-dir-btn" disabled={!text.trim()}>Ask aside</button>
      </div>
    </form>
  )
}

// nextStepPath is the path the next step entered from scope gets
// (condocv2/steps.go's nextStepPath): '1', '2', ... from the main scope,
// '1-1', '1-2', ... from step 1.
function nextStepPath(steps: StepEntry[], scope: string): string {
  const k = steps.filter((s) => stepParent(s.path) === scope).length + 1
  return scope ? `${scope}-${k}` : `${k}`
}

// ScopeBar is where the condoc view is among its steps (condocv2/steps.go):
// the scope shown -- the main scope, or a step, which shows only what was
// made in it -- with ↑ Up back to the scope it was entered from, and the way
// to the scope interactions go into now when another is shown. There, a held
// condoc can indent its scope into a new step (a substep, from a step), or
// complete the step it is in and come back up. Both are made between
// interactions only; neither resets the interaction count.
function ScopeBar({ entry: e, scope, onScope, onAction }: { entry: PointerEntry; scope: string; onScope: (s: string) => void; onAction: OnAction }) {
  const [title, setTitle] = useState('')
  const steps = e.steps ?? []
  const active = e.step ?? ''
  const step = steps.find((s) => s.path === scope)
  const canChange = e.heldHere && !isFinalPhase(e.phase) && !wasMerged(e) && e.phase !== 'proposed' && scope === active
  const blocked = e.stepBlocked
  if (steps.length === 0 && !canChange) return null
  const next = nextStepPath(steps, active)
  return (
    <div className="v2-scope-bar">
      {scope && (
        <button className="btn-secondary working-dir-btn" title={`back up to the ${scopeTitle(stepParent(scope))}`} onClick={() => onScope(stepParent(scope))}>
          ↑ Up
        </button>
      )}
      <span className="v2-scope-crumbs">
        {stepAncestors(scope).map((s, i) => (
          <span key={s || 'main'}>
            {i > 0 && ' › '}
            {s === scope
              ? <b>{scopeTitle(s)}</b>
              : <button type="button" className="dtt-link" onClick={() => onScope(s)}>{scopeTitle(s)}</button>}
          </span>
        ))}
      </span>
      {step?.title && <span className="step-label">{step.title}</span>}
      {step && (
        <span className="step-label">
          {step.completed ? `completed after interaction ${step.to}` : scope === active ? 'current' : 'in progress'}
        </span>
      )}
      {scope !== active && (
        <button
          className="btn-secondary working-dir-btn"
          title="the scope the next prompt goes into"
          onClick={() => onScope(active)}
        >
          Go to {scopeTitle(active)} →
        </button>
      )}
      {canChange && (
        <>
          <input
            className="working-dir-input v2-scope-title"
            value={title}
            placeholder={`step-${next} title (optional)`}
            onChange={(ev) => setTitle(ev.target.value)}
          />
          <button
            className="btn-primary working-dir-btn"
            disabled={!!blocked}
            title={blocked
              ? `Not now: ${blocked}`
              : `indent the scope into a new ${active ? 'substep' : 'step'}, step-${next}: the next interactions go into its own step file, and the visual shows only them until it is completed`}
            onClick={() => { onAction('enter-step', e.stem, { text: title }); setTitle('') }}
          >
            → New {active ? 'substep' : 'step'}
          </button>
          {active && (
            <button
              className="btn-secondary working-dir-btn"
              disabled={!!blocked}
              title={blocked ? `Not now: ${blocked}` : `complete ${scopeTitle(active)} and come back up to the ${scopeTitle(stepParent(active))}`}
              onClick={() => onAction('complete-step', e.stem)}
            >
              ← Complete {scopeTitle(active)}
            </button>
          )}
        </>
      )}
    </div>
  )
}

// StepMarker is a step entered or left, in the text of a scope: in the scope
// it was entered from, with the way into it; in its own, with the way back up.
function StepMarker({ step, done, scope, onScope }: { step: StepEntry; done: boolean; scope: string; onScope: (s: string) => void }) {
  const own = step.path === scope
  const up = stepParent(step.path)
  return (
    <div className="v2-interaction v2-step-marker">
      <div className="v2-section-head v2-section-step">
        {done ? '← ' : '→ '}{scopeTitle(step.path)}{done ? ' completed' : ''}{!done && step.title ? ` — ${step.title}` : ''}
      </div>
      <div className="pointer-note">
        {done ? `Completed after interaction ${step.to}, back up to the ${scopeTitle(up)}. ` : `Entered from the ${scopeTitle(up)} after interaction ${step.from}. `}
        {own
          ? done && <button type="button" className="dtt-link" onClick={() => onScope(up)}>↑ back up to the {scopeTitle(up)}</button>
          : <button type="button" className="dtt-link" onClick={() => onScope(step.path)}>descend into {scopeTitle(step.path)} →</button>}
      </div>
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
            <AbandonButton entry={entry} onAction={onAction} />
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
    out.push(<RepoLink key={m.index} href={m[2]}><code>{label}</code></RepoLink>)
    last = m.index + m[0].length
  }
  if (last < text.length) out.push(text.slice(last))
  return <>{out}</>
}

// RecordBlock is a record's lines; each external change it lists (see
// condocv2/external.go's externalRecord) has a "+/-" for its diff.
function RecordBlock({ text, hasDiff, onOpenDiff }: { text: string; hasDiff: Set<string>; onOpenDiff: OnOpenDiff }) {
  return (
    <ul className="v2-record">
      {text.split('\n').filter((l) => l.trim()).map((l, i) => {
        const x = l.match(/\(external (\d+)\.(\d+)\)\s*$/)
        const row = x ? externalRow(Number(x[1]), Number(x[2])) : ''
        return (
          <li key={i}>
            <InlineLinks text={l.replace(/^\s*-\s*/, '')} />
            {row && hasDiff.has(row) && <DiffLink row={row} onOpen={onOpenDiff} />}
          </li>
        )
      })}
    </ul>
  )
}

// DiffLink opens a row's diff (DiffViewer.tsx), like the visual's "+/-".
function DiffLink({ row, label = '+/-', onOpen }: { row: string; label?: string; onOpen: OnOpenDiff }) {
  return (
    <button type="button" className="v2-diff-btn" title={`${diffRowTitle(row)} -- click for the changes, repo by repo`} onClick={() => onOpen(row)}>
      {label}
    </button>
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
      <div className="v2-section-head v2-section-prompt">Prompt {n}{entry.step ? ` · ${scopeTitle(entry.step)}` : ''}</div>
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

const TOOL_STAGE_LABELS: Record<string, string> = {
  locked: 'committing the proposal',
  proposed: 'running dtt',
  ran: 'committing the image',
  imaged: 'unlocking',
}

// ReviewPanel is the hold on a condoc whose last interaction came back with
// DTT proposals (condocv2/review.go): nothing proceeds -- no prompt, no
// complete -- until the human has accepted or rejected each one, which runs
// as a sub-iteration of its own (an accepted one runs with dtt), or asked
// for changes to the rest, which becomes the next prompt to the agent. A
// write proposal can be read first (dtt read, its own sub-iteration too),
// and each file name opens the tool call and its images (DTTViewer).
function ReviewPanel({ entry: e, onAction, onOpenDTT }: { entry: PointerEntry; onAction: OnAction; onOpenDTT: OnOpenDTT }) {
  const [note, setNote] = useState('')
  const proposals = e.holdProposals ?? []
  const preReads = e.holdPreReads ?? []
  const waiting = e.holdStage === 'waiting'
  const n = e.holdInteraction ?? 0
  const next = `${n}.${(e.holdDone ?? 0) + 1}`
  const decide = (decision: ReviewDecision, proposal?: string) => onAction('review', e.stem, { decision, proposal })
  const doing = e.holdDecision === 'reject' ? 'rejecting' : e.holdDecision === 'read' ? 'reading before deciding:' : `dtt ${e.holdMode ?? ''}`
  return (
    <div className="v2-interaction v2-review">
      <div className="v2-section-head v2-section-review">
        {`Blocked — answer the DTT proposals from interaction ${n}`}
        {e.holdDone ? ` (${e.holdDone} sub-iteration${e.holdDone === 1 ? '' : 's'} done)` : ''}
      </div>
      {!waiting && e.holdSub && (
        <div className="v2-status">
          <span className="spinner" />{' '}
          {`${n}.${e.holdSub}: ${doing} ${e.holdCall ?? ''} — ${TOOL_STAGE_LABELS[e.holdStage ?? ''] ?? e.holdStage}`}
        </div>
      )}
      <ul className="v2-record">
        {proposals.map((p) => {
          const reads = preReads.filter((r) => r === p).length
          return (
            <li key={p}>
              <button type="button" className="dtt-link" title="show the tool call (and any images)" onClick={() => onOpenDTT(n, p)}>
                <code>{p}</code>
              </button>
              {reads > 0 && (
                <button type="button" className="dtt-link dtt-read-badge" title="read before deciding -- show its image" onClick={() => onOpenDTT(n, p)}>
                  👁 read{reads > 1 ? ` ×${reads}` : ''}
                </button>
              )}
              {e.heldHere && waiting && (
                <>
                  {' '}
                  <button className="btn-secondary working-dir-btn" title={`sub-iteration ${next}: committed as rejected, never run`} onClick={() => decide('reject', p)}>✗ Reject</button>{' '}
                  {!p.startsWith('read/') && (
                    <>
                      <button className="btn-secondary working-dir-btn" title={`sub-iteration ${next}: committed, then run with dtt read to see what it would change -- it still awaits accept or reject`} onClick={() => decide('read', p)}>👁 Read</button>{' '}
                    </>
                  )}
                  <button className="btn-primary working-dir-btn" title={`sub-iteration ${next}: committed, then run with dtt`} onClick={() => decide('accept', p)}>✓ Accept</button>
                </>
              )}
            </li>
          )
        })}
      </ul>
      <div className="pointer-note">
        The agent left {proposals.length === 1 ? 'this proposal' : `these ${proposals.length} proposals`} in the slopspace's
        writespaces/dtt-requests (they are in the condoc beside the step file, dtt/{n}/). Each one you accept or reject is a
        sub-iteration of its own: the decision is committed, an accepted one runs with dtt as soon as that is pushed, and its
        image is committed. 👁 Read reads a write proposal with dtt read the same way, before you decide it. An adjustment
        request instead goes back to the agent as Prompt {e.interactions + 1}.
      </div>
      {e.heldHere && waiting && (
        <>
          <textarea
            className="working-dir-input slop-textarea v2-textarea"
            rows={3}
            value={note}
            placeholder="Adjustment request: what should the agent change in its proposals?"
            onChange={(ev) => setNote(ev.target.value)}
          />
          <div className="pointer-actions">
            {proposals.length > 1 && (
              <>
                <button className="btn-secondary working-dir-btn" title="one sub-iteration each, in order" onClick={() => decide('accept')}>✓ Accept all</button>
                <button className="btn-secondary working-dir-btn" title="one sub-iteration each, in order" onClick={() => decide('reject')}>✗ Reject all</button>
              </>
            )}
            <button
              className="btn-secondary working-dir-btn"
              disabled={!note.trim()}
              title="sends the request to the agent as the next prompt, auto-submitted"
              onClick={() => {
                onAction('review', e.stem, { decision: 'modify', text: note })
                setNote('')
              }}
            >
              ↻ Request changes
            </button>
          </div>
        </>
      )}
    </div>
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
          {pr.repo}: {isLocalURL(pr.url)
            ? <RepoLink href={pr.url}>local merge</RepoLink>
            : <RepoLink href={pr.url}>#{pr.number}</RepoLink>} — {pr.state === 'merged' ? 'merged' : pr.status}
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
                title="merges the targets first and the condoc repo last, awaits each merge, commits the final state to the pointer, then removes the slopspace (the branches stay)"
                onClick={() => onAction('merge', e.stem)}
              >
                Merge
              </button>
            </div>
          )}
        </div>
      )
    case 'merged':
    case 'archived':
      return (
        <div className="v2-interaction">
          <div className="v2-section-head v2-section-record">{e.phase === 'archived' ? 'Merged and archived' : 'Merged'}</div>
          {prList}
          {e.condocUrl && <div className="pointer-note">The condoc: <RepoLink href={e.condocUrl}>{e.condocRepo}/{e.condocPath}</RepoLink></div>}
          {held && e.phase === 'merged' && (
            <div className="pointer-actions">
              <button
                className="btn-primary working-dir-btn"
                title="marks the pointer archived -- nothing else changes -- and hides the condoc unless 'show final' is on"
                onClick={() => onAction('archive', e.stem)}
              >
                Archive
              </button>
            </div>
          )}
        </div>
      )
    case 'abandoned':
      return (
        <div className="v2-interaction">
          <div className="v2-section-head v2-section-record">Abandoned</div>
          {prs.length > 0 && prList}
          <div className="pointer-note">
            Abandoned while {PHASE_LABELS[e.finalFrom ?? 'proposed'] ?? e.finalFrom}. Its slopspace, branches and any PRs were left as they were.
          </div>
          {held && (
            <div className="pointer-actions">
              <button
                className="btn-secondary working-dir-btn"
                title="puts the condoc back where it was abandoned -- condocv2 drives it again from there"
                onClick={() => onAction('unabandon', e.stem)}
              >
                Unabandon
              </button>
            </div>
          )}
        </div>
      )
  }
  return null
}

// TextFlow is one scope's text (condocv2/steps.go): its interactions, from
// its own step file, with each step entered from it or left back to it in
// place -- and, for a step, its entry first and its completion last. The
// interaction in flight, the DTT proposals held and the prompt box are the
// current scope's.
function TextFlow({ state, scope, onScope, onAction, onOpenDTT, onOpenDiff }: {
  state: CondocState
  scope: string
  onScope: (s: string) => void
  onAction: OnAction
  onOpenDTT: OnOpenDTT
  onOpenDiff: OnOpenDiff
}) {
  const e = state.entry
  const here = (e.step ?? '') === scope
  const interactions = state.interactions.filter((it) => (it.step ?? '') === scope)
  const bottomRef = useRef<HTMLDivElement | null>(null)
  const count = interactions.length
  const lastResponse = interactions[count - 1]?.response ?? ''
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: 'end' })
  }, [count, lastResponse.length, e.lockStage])

  // Interactions in order, each followed by the steps entered from or left
  // back to this scope after it, and the resources added in it, in the order
  // that happened.
  type Item = { n: number; at: number; it?: (typeof interactions)[number]; step?: StepEntry; done?: boolean; resource?: ResourceEntry; aside?: Aside }
  const items: Item[] = interactions.map((it) => ({ n: it.n, at: -1, it }))
  for (const s of e.steps ?? []) {
    if (stepParent(s.path) === scope || s.path === scope) items.push({ n: s.from, at: s.enteredAt ?? 0, step: s })
    if (s.completed && (stepParent(s.path) === scope || s.path === scope)) items.push({ n: s.to ?? 0, at: s.completedAt ?? 0, step: s, done: true })
  }
  for (const r of e.resources ?? []) {
    if ((r.step ?? '') === scope) items.push({ n: r.interaction, at: r.time ?? 0, resource: r })
  }
  // The asides (condocv2/aside.go), after the interactions completed when
  // each ran -- kept in this host's history, never in the step file.
  for (const a of state.asides ?? []) {
    if ((a.step ?? '') === scope) items.push({ n: a.interaction, at: a.startedAt || a.askedAt, aside: a })
  }
  items.sort((a, b) => a.n - b.n || a.at - b.at)

  const inFlight = !!e.lockInteraction && here
  const tl = state.timeline
  const hasDiff = useMemo(() => diffRows(tl), [tl])
  return (
    <div className="v2-text">
      {wasMerged(e) && (
        <div className="pointer-note">
          {tl.source === 'record'
            ? <>The slopspace is gone; this is the text saved in the {tl.recordUrl ? <RepoLink href={tl.recordUrl}>timeline record</RepoLink> : 'timeline record'}.</>
            : 'The slopspace is gone and there is no timeline record to read the text from.'}
        </div>
      )}
      {interactions.length === 0 && !(here && state.pendingPrompt) && !wasMerged(e) && (
        <div className="nav-empty">
          No interactions {scope ? `in ${scopeTitle(scope)} ` : ''}yet{here && e.heldHere && !isFinalPhase(e.phase) ? ' — enter the first prompt below.' : '.'}
        </div>
      )}
      {items.map(({ it, step, done, resource, aside }) => aside ? (
        <AsideMarker key={`aside-${aside.number}`} aside={aside} onCancel={e.heldHere ? () => onAction('cancel-aside', e.stem, { number: aside.number }) : undefined} />
      ) : resource ? (
        <ResourceMarker key={`resource-${resource.number}`} stem={e.stem} resource={resource} />
      ) : step ? (
        <StepMarker key={`${step.path}-${done ? 'done' : 'in'}`} step={step} done={!!done} scope={scope} onScope={onScope} />
      ) : it && (
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
              <div className="v2-section-head v2-section-record">
                Record {it.n}
                {hasDiff.has(`${it.n}`) && <DiffLink row={`${it.n}`} label="+/- changes" onOpen={onOpenDiff} />}
              </div>
              <RecordBlock text={it.record} hasDiff={hasDiff} onOpenDiff={onOpenDiff} />
            </>
          )}
          {it.proposals !== undefined && (
            <>
              <div className="v2-section-head v2-section-review">Proposals {it.n}</div>
              <pre className="v2-body"><DTTLinkedText text={it.proposals} onOpen={onOpenDTT} /></pre>
            </>
          )}
          {it.review !== undefined && (
            <>
              <div className="v2-section-head v2-section-review">Review {it.n}</div>
              <pre className="v2-body">{it.review}</pre>
            </>
          )}
          {(it.tools ?? []).map((t) => (
            <div key={t.k} className="v2-tool-call">
              {t.dtt !== undefined && (
                <>
                  <div className="v2-section-head v2-section-review">DTT {it.n}.{t.k}</div>
                  <pre className="v2-body"><DTTLinkedText text={t.dtt} onOpen={onOpenDTT} /></pre>
                </>
              )}
              {t.result !== undefined && (
                <>
                  <div className="v2-section-head v2-section-record">Result {it.n}.{t.k}</div>
                  <pre className="v2-body"><DTTLinkedText text={t.result} onOpen={onOpenDTT} /></pre>
                </>
              )}
            </div>
          ))}
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
      {here && state.pendingPrompt && (
        <div className="v2-interaction v2-interaction-pending">
          <div className="v2-section-head v2-section-prompt">Prompt {e.lockInteraction} (locked, not yet inserted)</div>
          <pre className="v2-body">{state.pendingPrompt}</pre>
          <div className="v2-status">{lockSummary(e)}</div>
        </div>
      )}
      {here && e.phase === 'reviewing' && e.holdInteraction && <ReviewPanel entry={e} onAction={onAction} onOpenDTT={onOpenDTT} />}
      {here && e.heldHere && e.phase === 'awaiting_prompt' && !e.error && (
        <PromptBox entry={e} onPrompt={(text, autoSubmit) => onAction('prompt', e.stem, { text, autoSubmit })} />
      )}
      {here && e.heldHere && canAside(e) && (
        <AsideBox entry={e} next={(state.asides ?? []).reduce((m, a) => Math.max(m, a.number), 0) + 1} onAside={(text) => onAction('aside', e.stem, { text })} />
      )}
      {!here && e.heldHere && !isFinalPhase(e.phase) && !wasMerged(e) && (
        <div className="pointer-note">
          The condoc is in {scopeTitle(e.step ?? '')}: the next prompt goes there.{' '}
          <button type="button" className="dtt-link" onClick={() => onScope(e.step ?? '')}>Go to {scopeTitle(e.step ?? '')} →</button>
        </div>
      )}
      {(here || scope === '') && <Completion state={state} onAction={onAction} />}
      <div ref={bottomRef} />
    </div>
  )
}

type CondocTab = 'text' | 'visual'

// useScrollMemory keeps the condoc body's scroll position -- and the
// visual's own sideways scroll -- per condoc and tab in sessionStorage, so
// coming back to it (from the worker tab, say, which remounts this frame in
// local-representative or agent-coordinator) lands where you were browsing.
function useScrollMemory(key: string) {
  const ref = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const storeKey = `condocv2.scroll:${key}`
    const inner = () => el.querySelector<HTMLElement>('.swim-scroll')
    try {
      const saved = JSON.parse(sessionStorage.getItem(storeKey) ?? 'null') as { top: number; left: number; inner?: number } | null
      if (saved) {
        el.scrollTop = saved.top
        el.scrollLeft = saved.left
        const i = inner()
        if (i && saved.inner) i.scrollLeft = saved.inner
      }
    } catch {
      // a bad saved position: start where the view puts us
    }
    const save = () => sessionStorage.setItem(storeKey, JSON.stringify({ top: el.scrollTop, left: el.scrollLeft, inner: inner()?.scrollLeft ?? 0 }))
    // capture: the visual's inner scroller's scrolls don't bubble.
    el.addEventListener('scroll', save, { capture: true, passive: true })
    return () => el.removeEventListener('scroll', save, { capture: true })
  }, [key])
  return ref
}

// useHeadOpen is whether the condoc head is expanded on a narrow (mobile)
// screen. Collapsed, the head is cut down to the name, a one-line summary and
// the tabs, so the step content below gets most of the screen; expanded, it
// shows the details, Configure condoc and the scope bar as on desktop, where
// it is always expanded (see index.css). Kept in sessionStorage, like the
// scroll positions, so a reload keeps it.
function useHeadOpen(): [boolean, (open: boolean) => void] {
  const storeKey = 'condocv2.headOpen'
  const [open, setOpen] = useState(() => sessionStorage.getItem(storeKey) === '1')
  const set = useCallback((o: boolean) => {
    sessionStorage.setItem(storeKey, o ? '1' : '0')
    setOpen(o)
  }, [])
  return [open, set]
}

// OpenDTT is the DTT viewer's place: interaction n, at the entry rel ('' for
// its first). It lives in App's hash, so a reload -- condocv2's own after a
// rebuild-and-restart, say -- opens it again.
interface OpenDTT {
  n: number
  rel: string
}

// OpenDiff is the diff viewer's place, kept in the hash like OpenDTT: the
// row ("N" or "external-N.e") at the file "<owner>/<name>/<path>" ('' for its
// first).
interface OpenDiff {
  row: string
  file: string
}

function CondocView({ state, tab, onTab, scope, onScope, onAction, openDTT: opened, onDTT, openDiff, onDiff }: {
  state: CondocState
  tab: CondocTab
  onTab: (t: CondocTab) => void
  scope: string // the scope shown (see ScopeBar)
  onScope: (s: string) => void
  onAction: OnAction
  openDTT: OpenDTT | null
  onDTT: (o: OpenDTT | null) => void
  openDiff: OpenDiff | null
  onDiff: (o: OpenDiff | null) => void
}) {
  const e = state.entry
  const here = (e.step ?? '') === scope // the interaction in flight and any hold are the current scope's
  const interactions = state.recordPrompts ?? state.interactions
  const held = opened && opened.n === e.holdInteraction ? e.holdProposals : undefined
  const dtt = useMemo<DTTTarget | null>(
    () => (opened ? dttTarget(interactions, opened.n, opened.rel || undefined, held) : null),
    [interactions, opened, held],
  )
  const openDTT: OnOpenDTT = (n, rel) => onDTT({ n, rel: rel ?? '' })
  const closeDTT = useCallback(() => onDTT(null), [onDTT])
  const selectDTT = useCallback((rel: string) => { if (opened) onDTT({ n: opened.n, rel }) }, [opened, onDTT])
  const openDiffRow: OnOpenDiff = (row) => onDiff({ row, file: '' })
  const closeDiff = useCallback(() => onDiff(null), [onDiff])
  const selectDiff = useCallback((file: string) => { if (openDiff) onDiff({ row: openDiff.row, file }) }, [openDiff, onDiff])
  const bodyRef = useScrollMemory(`${e.stem}/${tab}/${scope}`)
  const [headOpen, setHeadOpen] = useHeadOpen()
  const lock = lockSummary(e)
  return (
    <div className="v2-condoc">
      {dtt && <DTTViewer stem={e.stem} target={dtt} onClose={closeDTT} onSelect={selectDTT} />}
      {openDiff && <DiffViewer stem={e.stem} row={openDiff.row} file={openDiff.file} onClose={closeDiff} onSelect={selectDiff} />}
      <div className={`v2-condoc-head${headOpen ? '' : ' head-collapsed'}`}>
        <h2>{e.name} <CancelTurnButton entry={e} onAction={onAction} /> <AbandonButton entry={e} onAction={onAction} /></h2>
        <div className="v2-head-summary">
          <PhaseBadge phase={e.phase} />
          {e.phase !== 'proposed' && <span className="step-label">{e.interactions} done</span>}
          <span className="step-label" title="the scope shown">{scopeTitle(scope)}</span>
          {lock && <span className="step-label">{e.running && <span className="spinner" />} {lock}</span>}
          {!e.heldHere && <span className="step-label">read-only</span>}
          <button
            type="button"
            className="btn-secondary working-dir-btn v2-head-toggle"
            aria-expanded={headOpen}
            onClick={() => setHeadOpen(!headOpen)}
          >
            {headOpen ? '▴ Less' : '▾ More'}
          </button>
        </div>
        <div className="v2-condoc-head-more">
          <PointerDetails entry={e} />
          <ConfigureCondoc entry={e} onAction={onAction} />
          <ScopeBar entry={e} scope={scope} onScope={onScope} onAction={onAction} />
        </div>
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
      <div className="v2-condoc-body" ref={bodyRef}>
        {tab === 'text' ? (
          <TextFlow state={state} scope={scope} onScope={onScope} onAction={onAction} onOpenDTT={openDTT} onOpenDiff={openDiffRow} />
        ) : (
          <Swimlanes
            onOpenDTT={openDTT}
            onOpenDiff={openDiffRow}
            timeline={state.timeline}
            interactions={state.recordPrompts ?? state.interactions}
            scope={scope}
            steps={e.steps}
            resources={e.resources}
            onScope={onScope}
            pendingPrompt={here ? state.pendingPrompt : undefined}
            lockInteraction={here ? e.lockInteraction : undefined}
            holdInteraction={here ? e.holdInteraction : undefined}
            holdProposals={here ? e.holdProposals : undefined}
            holdWaiting={e.holdStage === 'waiting'}
            operations={state.operations}
          />
        )}
      </div>
    </div>
  )
}

// ---- URL-hash resume ----
//
// #/condoc/<stem>/<text|visual>[/step/<path|main>][/dtt/<n>[/<rel>] |
// /diff/<row>[/<file>]], #/propose, or nothing -- so a refresh (or an LR/AC
// iframe remount replaying a captured hash, or condocv2's own reload after a
// rebuild-and-restart) resumes in place: in the step it was showing, as
// condoccer v1 did, with the DTT viewer open on the interaction it was showing
// (Step6Prompt.md Revision H), or the diff viewer on the row and file. Without
// a step it opens on the scope the condoc is in now.

interface NavState {
  stem: string | null // a pointer stem, '' for the new-proposal form, null for nothing
  tab: CondocTab
  scope: string | null // the scope shown: '' the main scope, a step's path, null the condoc's current one
  dtt: OpenDTT | null
  diff: OpenDiff | null
}

function navFromHash(hash: string): NavState {
  const parts = hash.replace(/^#\/?/, '').split('/')
  if (parts[0] === 'propose') return { stem: '', tab: 'text', scope: null, dtt: null, diff: null }
  if (parts[0] === 'condoc' && parts[1]) {
    let i = 3
    let scope: string | null = null
    if (parts[i] === 'step' && parts[i + 1]) {
      scope = parts[i + 1] === 'main' ? '' : decodeURIComponent(parts[i + 1])
      i += 2
    }
    const n = parts[i] === 'dtt' && parts[i + 1] ? Number(parts[i + 1]) : NaN
    const row = parts[i] === 'diff' ? decodeURIComponent(parts[i + 1] ?? '') : ''
    return {
      stem: decodeURIComponent(parts[1]),
      tab: parts[2] === 'visual' ? 'visual' : 'text',
      scope: scope !== null && /^(\d+(-\d+)*)?$/.test(scope) ? scope : null,
      dtt: Number.isInteger(n) && n >= 0 ? { n, rel: decodeURIComponent(parts[i + 2] ?? '') } : null,
      diff: /^(external-\d+\.\d+|\d+)$/.test(row) ? { row, file: decodeURIComponent(parts[i + 2] ?? '') } : null,
    }
  }
  return { stem: null, tab: 'text', scope: null, dtt: null, diff: null }
}

function hashFromNav(n: NavState): string {
  if (n.stem === '') return '#/propose'
  if (!n.stem) return ''
  let condoc = `#/condoc/${encodeURIComponent(n.stem)}/${n.tab}`
  if (n.scope !== null) condoc += `/step/${n.scope || 'main'}`
  if (n.diff) return `${condoc}/diff/${encodeURIComponent(n.diff.row)}${n.diff.file ? `/${encodeURIComponent(n.diff.file)}` : ''}`
  if (!n.dtt) return condoc
  return `${condoc}/dtt/${n.dtt.n}${n.dtt.rel ? `/${encodeURIComponent(n.dtt.rel)}` : ''}`
}

// ---- Root app ----

export default function App() {
  const ws = useCondocWS()
  const { connected, activeState, error, setError, pointer, branchCheck } = ws
  const initialNav = useRef(navFromHash(window.location.hash)).current
  const [selectedStem, setSelectedStem] = useState<string | null>(initialNav.stem)
  const [tab, setTab] = useState<CondocTab>(initialNav.tab)
  const [scope, setScope] = useState<string | null>(initialNav.scope)
  const [openDTT, setOpenDTT] = useState<OpenDTT | null>(initialNav.dtt)
  const [openDiff, setOpenDiff] = useState<OpenDiff | null>(initialNav.diff)
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  // "show final": archived and abandoned condocs are hidden unless it's on
  // (the open one stays listed).
  const [showFinal, setShowFinal] = useState(() => localStorage.getItem('condocv2.showFinal') === '1')
  useEffect(() => { localStorage.setItem('condocv2.showFinal', showFinal ? '1' : '0') }, [showFinal])
  const tcCapture = useTCCapture(ws.tcAvailable, ws.reprHost, ws.reprHTTPPort)

  const selectedEntry = pointer?.pointers.find((p) => p.stem === selectedStem) ?? null
  const isCondoc = !!selectedEntry && selectedEntry.phase !== 'proposed'

  // A new proposal opens itself once it is listed, so Accept is right there
  // (and the robot needn't find it in the sidebar) -- unless something else
  // has been opened meanwhile.
  const [proposedName, setProposedName] = useState<string | null>(null)
  useEffect(() => {
    if (proposedName === null) return
    if (selectedStem !== '') {
      setProposedName(null)
      return
    }
    const made = pointer?.pointers.find((p) => p.name === proposedName && p.phase === 'proposed' && p.heldHere)
    if (made) {
      setProposedName(null)
      setSelectedStem(made.stem)
    }
  }, [pointer, proposedName, selectedStem])

  const { subscribe } = ws
  useEffect(() => {
    if (connected) subscribe(isCondoc && selectedStem ? selectedStem : '')
  }, [connected, isCondoc, selectedStem, subscribe])

  useEffect(() => {
    const hash = hashFromNav({ stem: selectedStem, tab, scope, dtt: openDTT, diff: openDiff })
    if (hash === window.location.hash) return
    const oldURL = window.location.href
    history.replaceState(null, '', hash || window.location.pathname + window.location.search)
    // replaceState never fires 'hashchange'; dispatch one so an embedder
    // tracking this frame's hash (LR/AC) stays in sync.
    window.dispatchEvent(new HashChangeEvent('hashchange', { oldURL, newURL: window.location.href }))
  }, [selectedStem, tab, scope, openDTT, openDiff])

  // The DTT and diff viewers and the scope shown belong to the condoc they
  // were opened on: another opens on the scope it is in now.
  const shownStem = useRef(selectedStem)
  useEffect(() => {
    if (shownStem.current !== selectedStem) {
      setOpenDTT(null)
      setOpenDiff(null)
      setScope(null)
    }
    shownStem.current = selectedStem
  }, [selectedStem])

  // The scope shown (condocv2/steps.go): pinned to the condoc's current one
  // once it is known, so the hash -- and so a restart -- keeps it; and when
  // the condoc enters or completes a step while its current scope is shown,
  // the view follows it in or back up.
  const shownState = activeState && activeState.entry.stem === selectedStem ? activeState : null
  const currentScope = shownState ? shownState.entry.step ?? '' : null
  const lastScope = useRef<{ stem: string; step: string } | null>(null)
  useEffect(() => {
    if (currentScope === null || !selectedStem) return
    const prev = lastScope.current
    lastScope.current = { stem: selectedStem, step: currentScope }
    if (scope === null) setScope(currentScope)
    else if (prev && prev.stem === selectedStem && prev.step !== currentScope && scope === prev.step) setScope(currentScope)
  }, [currentScope, selectedStem, scope])
  // A scope the condoc doesn't have (a stale hash) shows the main scope.
  const knownScope = (s: string) => s === '' || !!shownState?.entry.steps?.some((st) => st.path === s)
  const viewScope = scope !== null && knownScope(scope) ? scope : currentScope ?? ''

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

  const agentOverride: AgentOverride | null = pointer?.agentOverride?.agent ? pointer.agentOverride ?? null : null
  const { hostFiles, listHostFiles, highlighted, listHighlighted, addResource } = ws
  const resourceApi = useMemo<ResourceContextValue>(
    () => ({ hostFiles, listHostFiles, highlighted, listHighlighted, addResource: (r) => { setError(null); addResource(r) } }),
    [hostFiles, listHostFiles, highlighted, listHighlighted, addResource, setError],
  )

  const ready = connected && pointer !== null && pointer.state === 'ready'
  const finalCount = pointer?.pointers.filter((p) => isFinalPhase(p.phase)).length ?? 0
  const listed = (pointer?.pointers ?? []).filter((p) => showFinal || !isFinalPhase(p.phase) || p.stem === selectedStem)

  return (
    <TCCaptureContext.Provider value={tcCapture}><AgentOverrideContext.Provider value={agentOverride}><ResourceContext.Provider value={resourceApi}>
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
            {pointer?.state === 'ready' && (
              <label className="list-filter" title="list archived and abandoned condocs too">
                <input type="checkbox" checked={showFinal} onChange={(e) => setShowFinal(e.target.checked)} />
                show final <span className="list-filter-count">({finalCount})</span>
              </label>
            )}
            <div className="nav-list">
              {pointer?.state === 'ready' && (
                <div className={`nav-item nav-item-new${selectedStem === '' ? ' selected' : ''}`} onClick={() => select('')}>
                  <span className="nav-item-name">+ Propose a condoc</span>
                </div>
              )}
              {pointer?.state !== 'ready' && <div className="nav-empty">Set up the pointer repo to see condocs.</div>}
              {pointer?.state === 'ready' && listed.length === 0 && (
                <div className="nav-empty">
                  {pointer.pointers.length === 0 ? `No v2 condocs in ${pointer.repo} yet.` : 'Every condoc is final — tick "show final" to list them.'}
                </div>
              )}
              {pointer?.state === 'ready' && listed.map((p) => (
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
                    {p.step && <span className="step-label">{scopeTitle(p.step)}</span>}
                    {p.running && <span className="spinner" title="agent turn running" />}
                    {!p.running && p.queuePos ? <span className="step-label">#{p.queuePos} in queue</span> : null}
                    {p.error && <span className="badge badge-error" title={p.error}>error</span>}
                  </div>
                  {p.targets && p.targets.length > 0 && <span className="nav-item-subtitle">→ {p.targets.join(', ')}</span>}
                </div>
              ))}
            </div>
            {connected && pointer && (
              <AgentOverrideControl override={agentOverride} onSet={(o) => { setError(null); ws.setAgentOverride(o) }} />
            )}
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
              onPropose={(p) => { setError(null); if (!p.stem) setProposedName(p.name); ws.propose(p) }}
              onCheckBranch={ws.checkBranch}
              onAction={handleAction}
            />
          )}

          {ready && isCondoc && activeState && activeState.entry.stem === selectedStem && (
            <CondocView
              state={activeState}
              tab={tab}
              onTab={setTab}
              scope={viewScope}
              onScope={setScope}
              onAction={handleAction}
              openDTT={openDTT}
              onDTT={setOpenDTT}
              openDiff={openDiff}
              onDiff={setOpenDiff}
            />
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
    </ResourceContext.Provider></AgentOverrideContext.Provider></TCCaptureContext.Provider>
  )
}
