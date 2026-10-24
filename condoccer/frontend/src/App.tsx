import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { detectTextFormat } from './textformat'
import { HostFilePicker } from './HostFilePicker'
import type { ActionRequest, BranchCheck, CondocInfo, CondocMeta, CondocState, Iteration, ModeMismatchMsg, Phase, PointerConfig, PointerEntry, PointerStatus, ReprStatus, ReprStatusMsg, RootMsg, SelfInfoMsg, SlopspaceProposal, StepSummary, TCAvailabilityMsg } from './types'

// ---- WebSocket hook ----

interface DiffHunk {
  header: string
  lineIdx: number
}

interface CommitRange {
  from: string
  to: string
}

function useCondocWS() {
  const [connected, setConnected] = useState(false)
  const [condocs, setCondocs] = useState<CondocInfo[]>([])
  const [activeState, setActiveState] = useState<CondocState | null>(null)
  const [error, setError] = useState<string | null>(null)
  // Distinct from `error`: only true when the *server* explicitly rejected a
  // subscribe (path renamed/reverted-away/deleted). `error` also gets set by
  // transport-level hiccups (ws.onerror's generic "retrying" message), which
  // fire routinely during a reconnect -- e.g. every restart triggered by the
  // browser-refresh/auto-update mechanisms, while the server is still coming
  // back up. Only a genuine server rejection should be treated as staleness.
  const [subscribeError, setSubscribeError] = useState(false)
  const [reprStatus, setReprStatus] = useState<ReprStatus>('disconnected')
  const [reprHost, setReprHost] = useState('')
  const [reprPort, setReprPort] = useState('')
  const [reprAutoConnect, setReprAutoConnect] = useState(false)
  // local-representative's own HTTP dashboard port -- see
  // ReprStatusMsg.http_port and tcCaptureURL below.
  const [reprHTTPPort, setReprHTTPPort] = useState('')
  // Aggregate "is a the-conversationalist instance available on any host",
  // relayed down through local-representative -- see tcavailability.go and
  // Step2Prompt.md. Gates the mic icon on every text input.
  const [tcAvailable, setTCAvailable] = useState(false)
  const [devMode, setDevMode] = useState(false)
  const [version, setVersion] = useState('')
  const [modeMismatch, setModeMismatch] = useState<ModeMismatchMsg | null>(null)
  // condoccer's working dir (scan root). rootGen bumps only when it actually
  // changes after the first report, so App can drop back to the condoc list
  // -- every condoc path is relative to the root, so a deep link into the
  // old one means nothing anymore.
  const [root, setRoot] = useState('')
  const [rootGen, setRootGen] = useState(0)
  const rootRef = useRef('')
  // The slopspace tab's pointer repo (see pointer.go); null until reported.
  const [pointer, setPointer] = useState<PointerStatus | null>(null)
  const [diffFiles, setDiffFiles] = useState<string[]>([])
  const [diffFilesLoaded, setDiffFilesLoaded] = useState(false)
  const [fileDiffContent, setFileDiffContent] = useState<string | null>(null)
  const [fileDiffHunks, setFileDiffHunks] = useState<DiffHunk[]>([])
  const wsRef = useRef<WebSocket | null>(null)
  const subscribedRef = useRef<string>('')

  const send = useCallback((type: string, payload: unknown) => {
    wsRef.current?.send(JSON.stringify({ type, payload }))
  }, [])

  const subscribe = useCallback(
    (path: string) => {
      subscribedRef.current = path
      setSubscribeError(false)
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

  const connectRepr = useCallback(
    (host: string, port: string) => {
      send('connect', { host, port })
    },
    [send],
  )

  const disconnectRepr = useCallback(() => {
    send('disconnect', {})
  }, [send])

  // setAutoConnectRepr toggles the persistent auto-connect state: on, it arms
  // the retry cycle (starting a connect attempt at host/port if none is
  // already underway) and keeps it armed across a successful connection, so a
  // later unintentional disconnect resumes on its own; off, it only stops a
  // retry in progress -- Disconnect above is still the separate, explicit
  // action for dropping an active connection.
  const setAutoConnectRepr = useCallback(
    (enabled: boolean, host?: string, port?: string) => {
      send('set-auto-connect', { enabled, host, port })
    },
    [send],
  )

  const setWorkingDir = useCallback(
    (dir: string) => {
      send('set-root', { root: dir })
    },
    [send],
  )

  const setPointerRepo = useCallback(
    (cfg: PointerConfig) => {
      send('set-pointer', cfg)
    },
    [send],
  )

  const clonePointerRepo = useCallback(() => {
    send('clone-pointer', {})
  }, [send])

  // Slopspace condocs (see slopspace.go): propose/edit, then everything
  // else is keyed on the pointer's stem.
  const [branchCheck, setBranchCheck] = useState<{ branch: string; checks: BranchCheck[] } | null>(null)

  const slopPropose = useCallback(
    (p: SlopspaceProposal) => {
      send('slop-propose', p)
    },
    [send],
  )

  const slopCheckBranch = useCallback(
    (repos: string[], branch: string) => {
      setBranchCheck(null)
      send('slop-check-branch', { repos, branch })
    },
    [send],
  )

  const slopSend = useCallback(
    (
      type: 'slop-withdraw' | 'slop-accept' | 'slop-resubmit' | 'slop-merge' | 'slop-save-pr-template' | 'slop-regenerate-prs' | 'slop-accept-prs',
      stem: string,
      extra?: { template?: string; instruction?: string },
    ) => {
      send(type, { stem, ...extra })
    },
    [send],
  )

  // path is the condoc whose repo the diff runs against -- the working dir
  // may sit above several repos (see condocRepoRoot on the server).
  const getDiff = useCallback(
    (path: string, fromCommit: string, toCommit: string) => {
      setDiffFilesLoaded(false)
      send('get-diff', { path, fromCommit, toCommit })
    },
    [send],
  )

  const getFileDiff = useCallback(
    (path: string, fromCommit: string, toCommit: string, file: string) => {
      send('get-file-diff', { path, fromCommit, toCommit, file })
    },
    [send],
  )

  useEffect(() => {
    // Derive the WebSocket URL from the path this document was served under, so
    // it also works when reverse-proxied beneath a prefix (/condoccer/ via
    // local-representative, /host/<id>/condoccer/ via agent-coordinator).
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
            setSubscribeError(false)
          } else if (msg.type === 'error') {
            const p = msg.payload as { message: string }
            setError(p.message)
            setSubscribeError(true)
          } else if (msg.type === 'repr-status') {
            const p = msg.payload as ReprStatusMsg
            setReprStatus(p.status)
            if (p.host) setReprHost(p.host)
            if (p.port) setReprPort(p.port)
            setReprAutoConnect(!!p.auto_connect)
            setReprHTTPPort(p.http_port ?? '')
          } else if (msg.type === 'tc-availability') {
            const p = msg.payload as TCAvailabilityMsg
            setTCAvailable(p.available)
          } else if (msg.type === 'self-info') {
            const p = msg.payload as SelfInfoMsg
            setDevMode(p.dev_mode)
            setVersion(p.version)
            // A rebuild+restart is invisible to an already-open tab -- the
            // reconnect above is the only signal it gets. Compare the
            // server's own reported version against this bundle's
            // build-time version and reload if they differ; skip under the
            // Vite dev server, where HMR already keeps the tab current and
            // the two are never expected to match (see
            // condocs/initialDistributedDevelopmentImpls/BrowserRefreshStrategy.md).
            if (!import.meta.env.DEV && p.version && p.version !== __APP_VERSION__) {
              window.location.reload()
            }
          } else if (msg.type === 'mode-mismatch') {
            const p = msg.payload as ModeMismatchMsg
            setModeMismatch(p.mismatched ? p : null)
          } else if (msg.type === 'root') {
            const p = msg.payload as RootMsg
            if (rootRef.current && rootRef.current !== p.root) {
              // The server already dropped every subscription.
              subscribedRef.current = ''
              setActiveState(null)
              setRootGen((g) => g + 1)
            }
            rootRef.current = p.root
            setRoot(p.root)
          } else if (msg.type === 'pointer') {
            setPointer(msg.payload as PointerStatus)
          } else if (msg.type === 'slop-branch-check') {
            const p = msg.payload as { branch: string; checks: BranchCheck[] | null }
            setBranchCheck({ branch: p.branch, checks: p.checks ?? [] })
          } else if (msg.type === 'diff-list') {
            const p = msg.payload as { fromCommit: string; toCommit: string; files: string[] }
            setDiffFiles(p.files ?? [])
            setDiffFilesLoaded(true)
          } else if (msg.type === 'file-diff') {
            const p = msg.payload as { fromCommit: string; toCommit: string; file: string; content: string; hunks: DiffHunk[] }
            setFileDiffContent(p.content)
            setFileDiffHunks(p.hunks ?? [])
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
    connected,
    condocs,
    activeState,
    error,
    subscribeError,
    subscribe,
    sendAction,
    setError,
    reprStatus,
    reprHost,
    reprPort,
    reprAutoConnect,
    reprHTTPPort,
    tcAvailable,
    devMode,
    version,
    modeMismatch,
    root,
    rootGen,
    setWorkingDir,
    pointer,
    setPointerRepo,
    clonePointerRepo,
    branchCheck,
    slopPropose,
    slopCheckBranch,
    slopSend,
    connectRepr,
    disconnectRepr,
    setAutoConnectRepr,
    getDiff,
    getFileDiff,
    diffFiles,
    setDiffFiles,
    diffFilesLoaded,
    setDiffFilesLoaded,
    fileDiffContent,
    setFileDiffContent,
    fileDiffHunks,
    setFileDiffHunks,
  }
}

// WorkingDirControl ("Set Working Dir") changes the directory condoccer scans
// for condocs. It can sit above several repos: each condoc's .condoc lock,
// diffs and resubmit commits always go to that condoc's own repo root.
function WorkingDirControl({ root, onSet }: { root: string; onSet: (dir: string) => void }) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')

  if (!editing) {
    return (
      <div className="working-dir">
        <span className="working-dir-label">Working dir</span>
        <code className="working-dir-path">{root || '…'}</code>
        <button
          className="btn-secondary working-dir-btn"
          onClick={() => { setDraft(root); setEditing(true) }}
        >
          Set Working Dir
        </button>
      </div>
    )
  }

  const submit = () => {
    const dir = draft.trim()
    if (dir && dir !== root) onSet(dir)
    setEditing(false)
  }

  return (
    <form
      className="working-dir"
      onSubmit={(e) => { e.preventDefault(); submit() }}
    >
      <span className="working-dir-label">Working dir</span>
      <input
        className="working-dir-input"
        value={draft}
        autoFocus
        spellCheck={false}
        placeholder="/path/to/dir (absolute, ~/…, or relative to the current one)"
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => { if (e.key === 'Escape') setEditing(false) }}
      />
      <button type="submit" className="btn-primary working-dir-btn">Set</button>
      <button type="button" className="btn-secondary working-dir-btn" onClick={() => setEditing(false)}>Cancel</button>
    </form>
  )
}

const POINTER_STATE_LABEL: Record<PointerStatus['state'], string> = {
  missing: 'Not cloned',
  cloning: 'Cloning…',
  invalid: 'Not usable',
  ready: 'Ready',
}

// PointerRepoControl ("Set Pointer Repo") is the slopspace tab's counterpart
// to WorkingDirControl: the condoc pointer repo (owner/name), the branch
// condoccer pulls/pushes, and where it is checked out. When nothing is at the
// path it offers to clone (HTTPS + the server's GitHub PAT). There is no
// manual pull: the server pulls it continuously.
function PointerRepoControl({
  pointer,
  onSet,
  onClone,
}: {
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
        {field('path', 'Path', '../sloppo (relative to the repo condoccer started in)')}
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
      {pointer.holder && <div className="pointer-note">This condoccer holds condocs as {pointer.holder}.</div>}
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
// condoccer never touches audio or AWS itself (Step2Prompt.md: "we will keep
// the TC functionality as contained in that sub-app as we can, we only want
// the text to get into other text boxes"). Clicking a MicButton opens
// the-conversationalist's own UI in a small iframe, running in a lightweight
// "capture mode" (see the-conversationalist/frontend/src/App.tsx) that
// auto-starts recording and, on Stop, posts the final transcript back via
// window.postMessage rather than saving it to a file -- the only thing that
// ever crosses the app boundary is that one string.

// MIC_ICON follows the same grey-palette wireframe convention as
// agent-coordinator's and local-representative's header mic-availability
// indicator (Step2Prompt.md) -- kept identical so the affordance reads as
// part of the same icon family everywhere it appears.
const MIC_ICON = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
    <rect x="9" y="3" width="6" height="11" rx="3" />
    <path d="M5 11a7 7 0 0 0 14 0" />
    <path d="M12 18v3" />
    <path d="M8 21h8" />
  </svg>
)

// tcCaptureURL resolves the address of the-conversationalist's own UI,
// running in capture mode, regardless of which of the three ways condoccer
// itself is currently being viewed (see Step1Prompt.md:
// "standalone-UI-with-distinct-port/embed-in-local-representative/
// embed-in-agent-coordinator"):
//   - embedded in local-representative (path ".../condoccer/...") or
//     agent-coordinator (path ".../host/<id>/condoccer/...") -- swap the
//     trailing "condoccer" path segment for "convo" and stay on the same
//     origin, which reaches the right local-representative's reverse proxy
//     (see local-representative/main.go's proxyToConvo) no matter what
//     hostname the browser is actually using.
//   - standalone, on condoccer's own distinct port -- there's no sibling
//     path to swap, so reach local-representative directly using the
//     host/HTTP port it disclosed over representable (see
//     ReprStatusMsg.http_port).
function tcCaptureURL(reprHost: string, reprHTTPPort: string): string | null {
  const path = window.location.pathname
  const idx = path.lastIndexOf('/condoccer/')
  if (idx !== -1) {
    const prefix = path.slice(0, idx)
    return `${window.location.origin}${prefix}/convo/?embed=capture`
  }
  if (!reprHost || !reprHTTPPort) return null
  const proto = window.location.protocol === 'https:' ? 'https' : 'http'
  return `${proto}://${reprHost}:${reprHTTPPort}/convo/?embed=capture`
}

interface TCCaptureContextValue {
  available: boolean
  capture: (onTranscript: (text: string) => void) => void
}

// Default value only ever matters if a MicButton somehow renders outside
// App's own TCCaptureContext.Provider -- available: false keeps it inert
// rather than throwing.
const TCCaptureContext = createContext<TCCaptureContextValue>({ available: false, capture: () => {} })

// useTCCapture owns the one hidden-until-active iframe condoccer ever opens
// into the-conversationalist, and the postMessage listener that receives its
// transcript back. `available` gates every MicButton in the tree; `capture`
// starts a capture, invoking its callback once (and only once) with the
// final text.
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

// MicButton is the per-field affordance (Step2Prompt.md: "Whenever we have a
// text input field for condoccer and it sees TC is available we will see a
// 'mic' icon become visible"). Renders nothing while TC isn't available.
function MicButton({ onTranscript }: { onTranscript: (text: string) => void }) {
  const { available, capture } = useContext(TCCaptureContext)
  if (!available) return null
  return (
    <button
      type="button"
      className="mic-btn"
      title="dictate with The Conversationalist"
      onClick={() => capture(onTranscript)}
    >
      {MIC_ICON}
    </button>
  )
}

// appendTranscript is the shared "insert dictated text" behaviour every
// MicButton call site uses: appended after any existing content, space-
// separated, rather than overwriting it.
function appendTranscript(prev: string, text: string): string {
  const trimmed = text.trim()
  if (!trimmed) return prev
  return prev.trim() ? `${prev.trim()} ${trimmed}` : trimmed
}

// TCCaptureOverlay hosts the actual iframe while a capture is in progress --
// a small floating panel rather than a hidden iframe, since
// the-conversationalist's capture-mode UI (Start is automatic; Stop &
// Cancel are not) needs to be visible and interactive. Unmounted (iframe
// destroyed, mic released) the instant a transcript or cancellation arrives.
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

// ---- Navigation ----

type NavLevel = 'condoc-list' | 'condoc' | 'step' | 'substep' | 'files-changed' | 'file-diff'

// Top-level tab on the condoc list, one per control scheme (see the
// condoc-yaml `controlScheme` field). 'same-repo' is everything condoccer
// has done so far -- condocs whose state lives in the scanned repo itself.
// 'slopspace' is condocs driven through a pointer repo, each in its own
// slopspace -- see docs/SlopspaceControlScheme.md. Their condoc paths are
// "slopspace:<stem>"; past the list they use the same views as same-repo.
type ControlSchemeTab = 'same-repo' | 'slopspace'

const CONTROL_SCHEME_TABS: { id: ControlSchemeTab; label: string }[] = [
  { id: 'same-repo', label: 'Same-Repo' },
  { id: 'slopspace', label: 'Slopspace' },
]

// "Hide Completed" on the same-repo tab -- on by default, remembered across
// reloads.
const HIDE_COMPLETED_KEY = 'condoccer.hideCompleted'

function loadHideCompleted(): boolean {
  try {
    return localStorage.getItem(HIDE_COMPLETED_KEY) !== 'false'
  } catch {
    return true
  }
}

function saveHideCompleted(hide: boolean) {
  try {
    localStorage.setItem(HIDE_COMPLETED_KEY, String(hide))
  } catch {
    // Storage unavailable (e.g. sandboxed iframe) -- just don't persist.
  }
}

// ---- URL-hash resume (browser pickup strategy) ----
//
// The nav state above lives only in useState, so a refresh used to always
// land back on the condoc list. We mirror it into `location.hash` (never a
// real path -- see condocs/initialDistributedDevelopmentImpls/
// BrowserPickupStrategy.md for why: condoccer's `base: './'` asset
// resolution depends on the served directory never changing) so a refresh,
// or an LR/AC iframe remount replaying a captured hash, can resume straight
// to the same condoc/step/iteration/diff pane.
//
// Formats:
//   #/slopspace                    (condoc list, slopspace tab)
//   #/condoc/<enc(path)>
//   #/condoc/<enc(path)>/step/<num>[/iter/<id>]
//   #/condoc/<enc(path)>/step/<num>/substep[/iter/<id>]
//   #/condoc/<enc(path)>/step/<num>[/substep]/files/<enc(from)>..<enc(to)>[/file/<enc(file)>[/hunk/<idx>]]
//
// from/to are commit hashes, or for a slopspace condoc per-repo lists (see
// parseSlopCommitRanges), hence the encoding.

interface NavHashState {
  navLevel: NavLevel
  // Only meaningful at the condoc-list level: deeper levels carry the
  // scheme in the condoc path ("slopspace:" prefix).
  tab: ControlSchemeTab
  condocPath: string | null
  stepNum: number | null
  iterId: string | null
  substepIterId: string | null
  diffReturnLevel: 'step' | 'substep'
  diffFromCommit: string | null
  diffToCommit: string | null
  selectedDiffFile: string | null
  selectedDiffHunkIdx: number | null
}

function hashFromNav(s: NavHashState): string {
  if (s.navLevel === 'condoc-list' || !s.condocPath) return s.tab === 'slopspace' ? '#/slopspace' : ''
  let h = `#/condoc/${encodeURIComponent(s.condocPath)}`
  if (s.navLevel === 'condoc' || s.stepNum === null) return h

  h += `/step/${s.stepNum}`
  if (s.navLevel === 'step') {
    return s.iterId ? `${h}/iter/${s.iterId}` : h
  }
  if (s.navLevel === 'substep') {
    h += '/substep'
    return s.substepIterId ? `${h}/iter/${s.substepIterId}` : h
  }
  if (s.navLevel === 'files-changed' || s.navLevel === 'file-diff') {
    if (s.diffReturnLevel === 'substep') h += '/substep'
    if (s.diffFromCommit === null) return h
    h += `/files/${encodeURIComponent(s.diffFromCommit)}..${encodeURIComponent(s.diffToCommit ?? '')}`
    if (s.navLevel === 'file-diff' && s.selectedDiffFile) {
      h += `/file/${encodeURIComponent(s.selectedDiffFile)}`
      if (s.selectedDiffHunkIdx !== null) h += `/hunk/${s.selectedDiffHunkIdx}`
    }
  }
  return h
}

function emptyNavHashState(): NavHashState {
  return {
    navLevel: 'condoc-list',
    tab: 'same-repo',
    condocPath: null,
    stepNum: null,
    iterId: null,
    substepIterId: null,
    diffReturnLevel: 'step',
    diffFromCommit: null,
    diffToCommit: null,
    selectedDiffFile: null,
    selectedDiffHunkIdx: null,
  }
}

function navFromHash(hash: string): NavHashState {
  const result = emptyNavHashState()
  const trimmed = hash.replace(/^#\/?/, '')
  if (!trimmed) return result
  if (trimmed === 'slopspace') {
    result.tab = 'slopspace'
    return result
  }

  const parts = trimmed.split('/')
  let i = 0
  if (parts[i] !== 'condoc' || !parts[i + 1]) return result
  result.condocPath = decodeURIComponent(parts[i + 1])
  result.navLevel = 'condoc'
  i += 2

  if (parts[i] !== 'step' || !parts[i + 1]) return result
  const stepNum = parseInt(parts[i + 1], 10)
  if (Number.isNaN(stepNum)) return result
  result.stepNum = stepNum
  result.navLevel = 'step'
  i += 2

  let inSubstep = false
  if (parts[i] === 'substep') {
    inSubstep = true
    result.navLevel = 'substep'
    result.diffReturnLevel = 'substep'
    i += 1
  }

  if (parts[i] === 'iter' && parts[i + 1]) {
    if (inSubstep) result.substepIterId = parts[i + 1]
    else result.iterId = parts[i + 1]
    return result
  }

  if (parts[i] === 'files' && parts[i + 1]) {
    const range = parts[i + 1]
    const sep = range.indexOf('..')
    if (sep < 0) return result
    result.diffFromCommit = decodeURIComponent(range.slice(0, sep))
    result.diffToCommit = decodeURIComponent(range.slice(sep + 2))
    result.navLevel = 'files-changed'
    i += 2

    if (parts[i] === 'file' && parts[i + 1]) {
      result.selectedDiffFile = decodeURIComponent(parts[i + 1])
      result.navLevel = 'file-diff'
      i += 2

      if (parts[i] === 'hunk' && parts[i + 1]) {
        const hunkIdx = parseInt(parts[i + 1], 10)
        if (!Number.isNaN(hunkIdx)) result.selectedDiffHunkIdx = hunkIdx
      }
    }
  }

  return result
}

// ---- Phase helpers ----

const PHASE_LABELS: Record<Phase, string> = {
  proposed: 'Proposed',
  awaiting_step: 'Awaiting Step',
  agent_running: 'Running',
  awaiting_action: 'Awaiting Action',
  completed: 'Completed',
  queued: 'Queued',
  pr_proposed: 'Propose PR',
  pr_open: 'PR Open',
  merged: 'Merged',
}

function PhaseBadge({ phase }: { phase: Phase }) {
  return <span className={`badge badge-${phase}`}>{PHASE_LABELS[phase] ?? phase}</span>
}

// ---- Step section parser ----

interface ResourceLink {
  name: string
  filename: string
  // The indented "(...)" line condoccer writes under a zip's link, e.g.
  // "unzipped to 117e9380_run/" (see resources.go's extractZipResource).
  note?: string
}

interface StepSection {
  id: string
  label: string
  kind: 'prompt' | 'reply' | 'revision' | 'retry' | 'substep' | 'resource'
  content: string
  substepLetter?: string
  resourceLinks?: ResourceLink[]
  agent?: boolean // a resource the turn's agent created ("## Resource N (agent)")
}

const COMMIT_LINK_RE = /^\[`[a-f0-9]+`\]\([^)]+\)\s*$/gm
const PARENT_LINK_RE = /^\[.*?\]\(.*?\)\s*$/gm
const REPLACE_LINE_RE = /^(?:## )?<REPLACE[^>]*>[^\n]*\n?/gm
const DIRECTIVE_RE = /^!(?:HANDOFF|COMPLETED|REVERT[^!]*)!\s*$/gm

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

  // Collect all H2 headings with position info.
  interface Heading {
    index: number
    kind: string
    letter: string
    from: string
    substepTitle: string
    resourceNum: string
    resourceName: string
    resourceAgent?: boolean
    fullMatch: string
  }
  const headings: Heading[] = []
  const reMain = /^## (Reply|Revision|Retry|Human-Prompt)(?: ([A-Z]))?(?: \(from (\w+)\))?/gm
  const reSubstep = /^## Substep ([A-Z]) - (.+)/gm
  // "## Resource N" or "## Resource N -- <name>" (Revision B; the original
  // "## Resource (N)" parens were dropped) -- given its own heading here so
  // it gets cut out as its own section below, rather than reading as an
  // unstyled tail of whichever Reply/Revision precedes it. An agent-created
  // one reads "## Resource N (agent)[ -- <name>]" (condoccer/agentresources.go).
  const reResource = /^## Resource (\d+)( \(agent\))?(?: -- (.+))?/gm
  reMain.lastIndex = 0
  reSubstep.lastIndex = 0
  reResource.lastIndex = 0

  let m: RegExpExecArray | null
  while ((m = reMain.exec(content)) !== null) {
    headings.push({ index: m.index, kind: m[1], letter: m[2] ?? '', from: m[3] ?? '', substepTitle: '', resourceNum: '', resourceName: '', fullMatch: m[0] })
  }
  while ((m = reSubstep.exec(content)) !== null) {
    headings.push({ index: m.index, kind: 'Substep', letter: m[1], from: '', substepTitle: m[2].trim(), resourceNum: '', resourceName: '', fullMatch: m[0] })
  }
  while ((m = reResource.exec(content)) !== null) {
    headings.push({ index: m.index, kind: 'Resource', letter: '', from: '', substepTitle: '', resourceNum: m[1], resourceName: (m[3] ?? '').trim(), resourceAgent: m[2] !== undefined, fullMatch: m[0] })
  }
  headings.sort((a, b) => a.index - b.index)

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
    let substepLetter: string | undefined
    let resourceLinks: ResourceLink[] | undefined
    let sectionContent = cleaned
    const agent = h.resourceAgent || undefined

    if (h.kind === 'Reply') {
      id = h.letter ? `reply-${h.letter}` : 'reply-initial'
      label = h.letter ? `Reply ${h.letter}` : 'Reply'
      kind = 'reply'
    } else if (h.kind === 'Revision') {
      id = `revision-${h.letter}`
      label = `Revision ${h.letter}`
      kind = 'revision'
    } else if (h.kind === 'Retry') {
      id = `retry-${h.letter}`
      label = h.from ? `Retry ${h.letter} (from ${h.from})` : `Retry ${h.letter}`
      kind = 'retry'
    } else if (h.kind === 'Resource') {
      id = `resource-${h.resourceNum}`
      label = h.resourceName ? `Resource ${h.resourceNum} | ${h.resourceName}` : `Resource ${h.resourceNum}`
      kind = 'resource'
      resourceLinks = []
      const linkLineRe = /^- \[(.+?)\]\((.+?)\)[ \t]*(?:\n[ \t]+\((.+)\)[ \t]*)?$/gm
      let lm: RegExpExecArray | null
      while ((lm = linkLineRe.exec(cleaned)) !== null) {
        resourceLinks.push({ name: lm[1], filename: lm[2], note: lm[3] })
      }
      sectionContent = cleaned.replace(linkLineRe, '').trim()
    } else {
      // Substep
      id = `substep-${h.letter}`
      label = `Substep ${h.letter} — ${h.substepTitle}`
      kind = 'substep'
      substepLetter = h.letter
    }

    sections.push({ id, label, kind, content: sectionContent, substepLetter, resourceLinks, agent })
  }

  return sections
}

function sectionsToIterations(sections: StepSection[]): Iteration[] {
  return sections
    .filter((s) => s.kind !== 'prompt')
    .map((s) => ({ id: s.id, label: s.label, type: s.kind as Iteration['type'], agent: s.agent }))
}

// ---- Agent-created resources (Revision B of
// slopspaceSlopspaceCondocsImpls/Step1Prompt.md) ----
//
// A resource the turn's agent created is set slightly apart from a human's:
// a hollow sidebar dot, a dashed section border, and an "agent" tag on its
// label. These helpers add the modifier classes to the shared ones.

function iterDotClass(iter: Iteration): string {
  return `nav-iter-dot iter-${iter.type}${iter.agent ? ' iter-agent' : ''}`
}

function sectionClass(sec: StepSection, selected: boolean): string {
  return `iter-section iter-section-${sec.kind}${sec.agent ? ' iter-section-agent' : ''}${selected ? ' iter-section-selected' : ''}`
}

function AgentTag({ agent }: { agent?: boolean }) {
  return agent ? <span className="resource-agent-tag" title="Created by the agent">agent</span> : null
}

// resolveSubstepView picks which substep's content/iterations to show:
// state.substepContent/substepIterations are only ever populated for the
// *currently active* substep (info.substepLetter), so viewing an already-
// completed one (letter !== the active one, or there's no active one at
// all) instead falls back to completedSubstepContents and parses it
// client-side the same way a completed step's raw content is (see
// StepDetailView's non-active branch) -- otherwise the pane would just go
// blank the moment a substep finished. Falls back to the active substep when
// letter is null (e.g. right after entering via the sidebar's "→" button).
function resolveSubstepView(
  state: CondocState,
  letter: string | null,
): { letter: string; isActive: boolean; content: string; iterations: Iteration[] } {
  const activeLetter = state.info.substepLetter ?? ''
  const resolvedLetter = letter ?? activeLetter
  if (resolvedLetter !== '' && resolvedLetter === activeLetter) {
    return { letter: resolvedLetter, isActive: true, content: state.substepContent ?? '', iterations: state.substepIterations ?? [] }
  }
  const content = state.completedSubstepContents?.[resolvedLetter] ?? ''
  return { letter: resolvedLetter, isActive: false, content, iterations: content ? sectionsToIterations(parseStepSections(content)) : [] }
}

// ---- Resource rendering (Revision B of Step5SubstepRPrompt.md) ----
//
// "## Resource N" blocks link to files condoccer copied into the condoc's
// Impls folder (see condoccer/resources.go's addResource). basePath() mirrors
// the WebSocket hook's own path-prefix derivation so /api/resource/... also
// resolves correctly when this UI is reverse-proxied under a path prefix
// (/condoccer/ via local-representative, /host/<id>/condoccer/ via AC).
function basePath(): string {
  const dir = window.location.pathname.replace(/\/[^/]*\.[^/]*$/, '/')
  return dir.endsWith('/') ? dir.slice(0, -1) : dir
}

function resourceUrl(condocPath: string, filename: string, opts?: { download?: boolean }): string {
  const params = new URLSearchParams({ condoc: condocPath })
  if (opts?.download) params.set('download', '1')
  return `${basePath()}/api/resource/${encodeURIComponent(filename)}?${params.toString()}`
}

// RESOURCE_TEXT_EXTENSIONS / RESOURCE_IMAGE_EXTENSIONS mirror
// local-representative's files.go classifyKind -- condoccer has no code
// shared with LR's frontend, so this small classification table is
// duplicated rather than imported.
const RESOURCE_TEXT_EXTENSIONS = new Set([
  '.txt', '.md', '.log', '.csv', '.json', '.yaml', '.yml', '.go', '.py',
  '.js', '.ts', '.tsx', '.jsx', '.html', '.css', '.sh', '.xml', '.ini',
  '.toml', '.conf',
])
const RESOURCE_IMAGE_EXTENSIONS = new Set([
  '.png', '.jpg', '.jpeg', '.gif', '.webp', '.svg', '.bmp', '.ico',
])

function classifyResourceKind(filename: string): 'text' | 'image' | 'other' {
  const dot = filename.lastIndexOf('.')
  const ext = dot >= 0 ? filename.slice(dot).toLowerCase() : ''
  if (RESOURCE_TEXT_EXTENSIONS.has(ext)) return 'text'
  if (RESOURCE_IMAGE_EXTENSIONS.has(ext)) return 'image'
  return 'other'
}

// ResourceTextPreview fetches a text resource's raw content once and renders
// it inline; failures (e.g. the file was since removed from the Impls
// folder) just leave nothing rendered rather than an error block.
function ResourceTextPreview({ url }: { url: string }) {
  const [text, setText] = useState<string | null>(null)
  useEffect(() => {
    let cancelled = false
    setText(null)
    fetch(url)
      .then((r) => (r.ok ? r.text() : Promise.reject(r.status)))
      .then((t) => { if (!cancelled) setText(t) })
      .catch(() => { if (!cancelled) setText(null) })
    return () => { cancelled = true }
  }, [url])
  if (text === null) return null
  return <pre className="resource-text-preview">{text}</pre>
}

// ResourceSectionBody renders a "## Resource N" section's body: its
// description text, then each linked file rendered inline when it's an image
// or text file (anything else falls back to a plain download link). Clicking
// an image "enters" it -- a full-size overlay, since there's nowhere in
// condoccer's hash-based nav model that makes sense as a dedicated page for
// it the way local-representative/agent-coordinator's FileViewer does.
function ResourceSectionBody({ sec, condocPath }: { sec: StepSection; condocPath: string }) {
  const [viewing, setViewing] = useState<{ url: string; name: string } | null>(null)

  return (
    <div className="iter-section-content resource-section-content">
      {sec.content && <div className="resource-description">{sec.content}</div>}
      {(sec.resourceLinks ?? []).map((link) => {
        const kind = classifyResourceKind(link.filename)
        const url = resourceUrl(condocPath, link.filename)
        if (kind === 'image') {
          return (
            <img
              key={link.filename}
              className="resource-image"
              src={url}
              alt={link.name}
              title="Click to view full size"
              onClick={() => setViewing({ url, name: link.name })}
            />
          )
        }
        if (kind === 'text') {
          return <ResourceTextPreview key={link.filename} url={url} />
        }
        return (
          <div key={link.filename}>
            <a className="resource-file-link" href={resourceUrl(condocPath, link.filename, { download: true })}>
              {link.name}
            </a>
            {link.note && <div className="resource-file-note">({link.note})</div>}
          </div>
        )
      })}
      {viewing && (
        <div className="resource-image-overlay" onClick={() => setViewing(null)}>
          <img src={viewing.url} alt={viewing.name} />
          <button className="resource-image-overlay-close" onClick={() => setViewing(null)}>× Close</button>
        </div>
      )}
    </div>
  )
}

// ---- "to files" (Revision E of slopspaceSlopspaceCondocsImpls/Step1Prompt.md) ----
//
// A resource with linked files gets a "to files" button at its label's
// right: it copies those files into this condoccer's local-representative's
// files tab (condoccer/tofiles.go's POST /api/resource-to-files), as
// uploads. A voice note links nothing, so has none.
//
// Revision F: once every linked file is already in the files tab
// (condoccer/tofiles.go's GET /api/resource-in-files), the button is lit
// and pressing it asks the page embedding condoccer -- local-representative
// or agent-coordinator's host view -- to open its files tab with that file
// selected, via an OPEN_FILE_MESSAGE postMessage. Only the files not yet
// there are copied otherwise.

const OPEN_FILE_MESSAGE = 'condoccer-open-file'

function ToFilesButton({ sec, condocPath }: { sec: StepSection; condocPath: string }) {
  const [status, setStatus] = useState<{ kind: 'busy' | 'done' | 'error'; text: string } | null>(null)
  // present maps each linked file already in the files tab to its id there.
  const [present, setPresent] = useState<Record<string, string>>({})
  const filenames = (sec.resourceLinks ?? []).map((l) => l.filename)
  const filenamesKey = filenames.join('\n')

  const checkPresent = useCallback(async () => {
    if (!filenamesKey) return
    const params = new URLSearchParams({ condoc: condocPath })
    for (const f of filenamesKey.split('\n')) params.append('filename', f)
    try {
      const resp = await fetch(`${basePath()}/api/resource-in-files?${params.toString()}`)
      if (resp.ok) setPresent((await resp.json()).present ?? {})
    } catch {
      // unreachable just leaves the button unlit
    }
  }, [condocPath, filenamesKey])

  useEffect(() => { void checkPresent() }, [checkPresent])

  if (filenames.length === 0) return null
  const missing = filenames.filter((f) => !present[f])
  const inFiles = missing.length === 0
  const embedded = window.parent !== window

  const copy = async () => {
    setStatus({ kind: 'busy', text: 'copying…' })
    try {
      const resp = await fetch(`${basePath()}/api/resource-to-files`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ condoc: condocPath, filenames: missing }),
      })
      if (!resp.ok) {
        setStatus({ kind: 'error', text: (await resp.text()).trim() || `failed (${resp.status})` })
        return
      }
      const n = missing.length
      setStatus({ kind: 'done', text: `✓ ${n} file${n === 1 ? '' : 's'} in files` })
      void checkPresent()
    } catch (err) {
      setStatus({ kind: 'error', text: String(err) })
    }
  }

  const open = () => {
    if (!embedded) {
      setStatus({ kind: 'done', text: 'already in files' })
      return
    }
    window.parent.postMessage({ type: OPEN_FILE_MESSAGE, id: present[filenames[0]] }, window.location.origin)
  }

  const n = filenames.length
  const title = inFiles
    ? embedded
      ? `Already in files — go to the files tab with ${n === 1 ? 'it' : 'the first'} selected`
      : `Already in local-representative's files tab`
    : n === 1
      ? `Copy this file to local-representative's files tab`
      : missing.length === n
        ? `Copy these ${n} files to local-representative's files tab`
        : `Copy the ${missing.length} of these ${n} files not yet there to local-representative's files tab`

  return (
    <>
      {status && (
        <span className={`resource-to-files-status resource-to-files-status-${status.kind}`} title={status.text}>
          {status.kind === 'error' ? 'failed' : status.text}
        </span>
      )}
      <button
        className={`btn-secondary resource-to-files-btn${inFiles ? ' resource-to-files-btn-lit' : ''}`}
        disabled={status?.kind === 'busy'}
        title={title}
        onClick={(e) => { e.stopPropagation(); if (inFiles) open(); else void copy() }}
        onDoubleClick={(e) => e.stopPropagation()}
      >
        to files
      </button>
    </>
  )
}

// SectionLabel is a section's label row: its label and agent tag at the
// left, and at the right whatever buttons the caller adds (children) plus a
// resource's "to files".
function SectionLabel({ sec, condocPath, children }: { sec: StepSection; condocPath: string; children?: ReactNode }) {
  return (
    <div className="iter-section-label iter-section-label-row">
      <span>{sec.label}<AgentTag agent={sec.agent} /></span>
      <span className="iter-section-label-actions">
        {children}
        {sec.kind === 'resource' && <ToFilesButton sec={sec} condocPath={condocPath} />}
      </span>
    </div>
  )
}

// sectionBody renders a parsed section's content: the raw-text default for
// every kind but 'resource', which gets ResourceSectionBody's inline
// image/text rendering instead (see Revision B of Step5SubstepRPrompt.md).
function sectionBody(sec: StepSection, condocPath: string) {
  return sec.kind === 'resource'
    ? <ResourceSectionBody sec={sec} condocPath={condocPath} />
    : <div className="iter-section-content">{sec.content}</div>
}

function parseCommitRanges(content: string): Map<string, CommitRange> {
  const rangeRe = /prompt:\s*\[`([a-f0-9]+)`\][^\n]*→\s*reply:\s*\[`([a-f0-9]+)`\]/gm
  const replyRe = /^## Reply(?: ([A-Z]))?(?:\s|$)/gm

  const ranges: Array<{ pos: number; from: string; to: string }> = []
  const replies: Array<{ pos: number; id: string }> = []

  let m: RegExpExecArray | null
  while ((m = rangeRe.exec(content)) !== null) {
    ranges.push({ pos: m.index, from: m[1], to: m[2] })
  }
  while ((m = replyRe.exec(content)) !== null) {
    const letter = m[1] ?? ''
    replies.push({ pos: m.index, id: letter ? `reply-${letter}` : 'reply-initial' })
  }

  // Associate each preamble range with the reply heading that follows it.
  const pairs: Array<{ reply: (typeof replies)[0]; range: (typeof ranges)[0] }> = []
  for (const range of ranges) {
    const nextReply = replies.find((r) => r.pos > range.pos)
    if (nextReply && !pairs.some((p) => p.reply.id === nextReply.id)) {
      pairs.push({ reply: nextReply, range })
    }
  }
  pairs.sort((a, b) => a.reply.pos - b.reply.pos)

  // Implementation commits land IN the reply commit (bundled by git add .) rather than
  // between the preamble hashes. range.to is the "prompt" commit; the implementation is
  // in the very next commit after that, which becomes range.from for the next iteration.
  // So the correct diff range for iteration i is (range.to_i .. range.from_{i+1}).
  // An empty string for `to` tells the backend to use HEAD (last iteration).
  const result = new Map<string, CommitRange>()
  for (let i = 0; i < pairs.length; i++) {
    const nextFrom = pairs[i + 1]?.range.from ?? ''
    result.set(pairs[i].reply.id, { from: pairs[i].range.to, to: nextFrom })
  }
  return result
}

// parseSlopCommitRanges is parseCommitRanges for a slopspace condoc, whose
// reply preamble links each target repo's commit, one line per repo (see
// turnPreamble in condoccer/slopspace.go):
//   - owner/name: [`base`](…/commit/<base>) → [`head`](…/commit/<head>)
//   - owner/name: [`head`](…/commit/<head>)
//   - owner/name: (no changes)
//   - owner/name: [`base`](…/commit/<base>) → this reply
// The last form is the condoc repo and the pointer repo, committed after the
// reply is written. A reply's range spans every repo it changed: from is
// "owner/name@<base>,…" and to "owner/name@<head>,…", which the backend
// diffs repo by repo. A repo with a base and no head is the condoc or
// pointer repo, whose head the backend finds. A reply that changed no repo
// gets no range.
function parseSlopCommitRanges(content: string): Map<string, CommitRange> {
  const link = String.raw`\[\x60[a-f0-9]+\x60\]\(https:\/\/github\.com\/[^)\s]+\/commit\/([a-f0-9]+)\)`
  const changedRe = new RegExp(String.raw`^- ([\w.-]+\/[\w.-]+): ${link}(?: → ${link})?\s*$`)
  const thisReplyRe = new RegExp(String.raw`^- ([\w.-]+\/[\w.-]+): ${link} → this reply\s*$`)
  const unchangedRe = /^- [\w.-]+\/[\w.-]+: \(no changes\)\s*$/
  const replyRe = /^## Reply(?: ([A-Z]))?(?:\s|$)/

  const result = new Map<string, CommitRange>()
  const lines = content.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const rm = replyRe.exec(lines[i])
    if (!rm) continue
    const from: string[] = []
    const to: string[] = []
    let j = i - 1
    while (j >= 0 && lines[j].trim() === '') j--
    for (; j >= 0; j--) {
      if (unchangedRe.test(lines[j])) continue
      const tm = thisReplyRe.exec(lines[j])
      if (tm) {
        from.unshift(`${tm[1]}@${tm[2]}`)
        continue
      }
      const m = changedRe.exec(lines[j])
      if (!m) break
      const [, repo, first, second] = m
      if (second) {
        from.unshift(`${repo}@${first}`)
        to.unshift(`${repo}@${second}`)
      } else {
        to.unshift(`${repo}@${first}`)
      }
    }
    if (from.length > 0 || to.length > 0) {
      result.set(rm[1] ? `reply-${rm[1]}` : 'reply-initial', { from: from.join(','), to: to.join(',') })
    }
  }
  return result
}

// diffFileRepo splits a slopspace diff file ("owner/name/<path>") into its
// repo and path.
function diffFileRepo(file: string): { repo: string; path: string } {
  const parts = file.split('/')
  return { repo: parts.slice(0, 2).join('/'), path: parts.slice(2).join('/') }
}

// ---- Representable connect/disconnect widget ----
//
// condoccer's link to local-representative can come up on its own via
// --auto-connect, but that's not mandatory: this footer lets a condoccer
// started without it (or one whose auto-connect window gave up) connect by
// hand, and lets anyone drop the link with Disconnect.

const REPR_STATUS_LABELS: Record<ReprStatus, string> = {
  disconnected: 'LR: disconnected',
  connecting: 'LR: connecting…',
  connected: 'LR: connected',
}

interface ReprFooterProps {
  status: ReprStatus
  host: string
  port: string
  autoConnect: boolean
  onConnect: (host: string, port: string) => void
  onDisconnect: () => void
  onSetAutoConnect: (enabled: boolean, host?: string, port?: string) => void
}

function ReprFooter({ status, host, port, autoConnect, onConnect, onDisconnect, onSetAutoConnect }: ReprFooterProps) {
  const [hostInput, setHostInput] = useState(host)
  const [portInput, setPortInput] = useState(port)

  // Track the address the server reports (its --lr-host/--lr-port defaults,
  // or whatever it's currently connected/connecting to) until the user edits
  // the fields themselves.
  useEffect(() => setHostInput(host), [host])
  useEffect(() => setPortInput(port), [port])

  // Auto-connect toggle: a first-class state independent of the current
  // connection (see Revision I of Step3Prompt.md) -- checking it arms the
  // retry cycle and keeps it armed across a successful connection, so a later
  // unintentional disconnect resumes on its own; unchecking it only stops a
  // retry in progress. Disconnect above is still the separate, explicit
  // action that drops an active connection and also turns this off.
  const autoConnectToggle = (
    <label className="repr-footer-auto-connect" title="keep reaching for local-representative: stays armed across a successful connection so an unintentional disconnect resumes the cycle on its own -- an explicit disconnect turns it off">
      <input
        type="checkbox"
        checked={autoConnect}
        onChange={(e) => onSetAutoConnect(e.target.checked, hostInput.trim(), portInput.trim())}
      />
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
          <input
            className="repr-footer-input"
            type="text"
            placeholder="host"
            value={hostInput}
            onChange={(e) => setHostInput(e.target.value)}
          />
          <input
            className="repr-footer-input repr-footer-input-port"
            type="text"
            placeholder="port"
            value={portInput}
            onChange={(e) => setPortInput(e.target.value)}
          />
          <button className="btn-secondary" onClick={() => onConnect(hostInput.trim(), portInput.trim())}>
            Connect
          </button>
          {autoConnectToggle}
        </div>
      ) : (
        <div className="repr-footer-form">
          <span className="repr-footer-addr">{host}:{port}</span>
          {autoConnectToggle}
          <button className="btn-secondary" onClick={onDisconnect}>
            Disconnect
          </button>
        </div>
      )}
    </div>
  )
}

// ---- Diff display ----

interface DiffDisplayProps {
  content: string
  selectedHunkIdx: number | null
  hunkRefs: { current: Record<number, HTMLDivElement | null> } | null
}

function DiffDisplay({ content, selectedHunkIdx, hunkRefs }: DiffDisplayProps) {
  interface ProcessedLine {
    line: string
    cls: string
    hunkIdx: number
  }

  const processed: ProcessedLine[] = []
  let hunkCount = 0
  for (const line of content.split('\n')) {
    let cls = 'diff-line'
    let hunkIdx = -1
    if (line.startsWith('@@')) {
      hunkIdx = hunkCount++
      cls += ' diff-hunk-header'
      if (selectedHunkIdx === hunkIdx) cls += ' diff-selected'
    } else if (line.startsWith('+') && !line.startsWith('+++')) {
      cls += ' diff-add'
    } else if (line.startsWith('-') && !line.startsWith('---')) {
      cls += ' diff-remove'
    } else if (/^(diff |index |--- |\+\+\+ )/.test(line)) {
      cls += ' diff-meta'
    }
    processed.push({ line, cls, hunkIdx })
  }

  return (
    <div className="diff-content">
      {processed.map(({ line, cls, hunkIdx }, i) => (
        <div
          key={i}
          className={cls}
          ref={hunkIdx >= 0 && hunkRefs ? (el) => { hunkRefs.current[hunkIdx] = el } : undefined}
        >
          {line || ' '}
        </div>
      ))}
    </div>
  )
}

// ---- Files-changed view ----

interface FilesChangedViewProps {
  files: string[]
  filesLoaded: boolean
  selectedFile: string | null
}

function FilesChangedView({ files, filesLoaded, selectedFile }: FilesChangedViewProps) {
  return (
    <div className="detail-view">
      <div className="detail-header">
        <h2>Files Changed</h2>
        {filesLoaded && (
          <span className="detail-step-title">
            {files.length === 0 ? 'no project files' : `${files.length} file${files.length !== 1 ? 's' : ''}`}
          </span>
        )}
      </div>
      <div className="detail-body">
        {!filesLoaded ? (
          <div className="action-status"><span className="spinner" /> Loading…</div>
        ) : files.length === 0 ? (
          <div className="empty-state"><div>No project files changed in this commit range.</div></div>
        ) : (
          <div className="diff-file-tree">
            {files.map((file) => (
              <div
                key={file}
                className={`diff-file-tree-item${selectedFile === file ? ' diff-file-selected' : ''}`}
              >
                {file}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

// ---- File diff view ----

interface FileDiffViewProps {
  file: string | null
  content: string | null
  selectedHunkIdx: number | null
}

function FileDiffView({ file, content, selectedHunkIdx }: FileDiffViewProps) {
  const hunkRefs = useRef<Record<number, HTMLDivElement | null>>({})

  useEffect(() => {
    if (selectedHunkIdx !== null && hunkRefs.current[selectedHunkIdx]) {
      hunkRefs.current[selectedHunkIdx]?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    }
  }, [selectedHunkIdx])

  if (!file) {
    return (
      <div className="detail-view">
        <div className="empty-state"><div>No file selected.</div></div>
      </div>
    )
  }

  const shortName = file.split('/').pop() ?? file

  return (
    <div className="detail-view">
      <div className="detail-header">
        <h2>{shortName}</h2>
        <span className="detail-step-title">{file}</span>
      </div>
      <div className="detail-body">
        {!content ? (
          <div className="action-status"><span className="spinner" /> Loading diff…</div>
        ) : (
          <DiffDisplay content={content} selectedHunkIdx={selectedHunkIdx} hunkRefs={hunkRefs} />
        )}
      </div>
    </div>
  )
}

// ---- Hunk line number parser ----

function parseHunkLineNumber(header: string): number | null {
  const m = /@@\s+-\d+(?:,\d+)?\s+\+(\d+)/.exec(header)
  return m ? parseInt(m[1], 10) : null
}

// ---- Sidebar ----

interface SidebarProps {
  navLevel: NavLevel
  tab: ControlSchemeTab
  onSelectTab: (tab: ControlSchemeTab) => void
  hideCompleted: boolean
  onSetHideCompleted: (hide: boolean) => void
  pointer: PointerStatus | null
  // The slopspace tab's selection: a stem, '' for the new-proposal form, or
  // null for nothing.
  selectedPointerStem: string | null
  onSelectPointer: (p: PointerEntry | null) => void
  condocs: CondocInfo[]
  activeState: CondocState | null
  selectedCondocPath: string | null
  selectedStepNum: number | null
  selectedIterId: string | null
  selectedSubstepLetter: string | null
  selectedSubstepIterId: string | null
  diffFiles: string[]
  diffFilesLoaded: boolean
  selectedDiffFile: string | null
  fileDiffHunks: DiffHunk[]
  selectedDiffHunkIdx: number | null
  diffReturnLevel: 'step' | 'substep'
  onSelectCondoc: (path: string) => void
  onSelectStep: (num: number) => void
  onSelectIter: (id: string) => void
  onEnterSubstep: (substepLetter: string) => void
  onSelectSubstepIter: (id: string) => void
  onEnterFilesChanged: (fromCommit: string, toCommit: string) => void
  onSelectDiffFile: (file: string) => void
  onSelectDiffHunk: (idx: number) => void
  onNavUp: () => void
  reprStatus: ReprStatus
  reprHost: string
  reprPort: string
  reprAutoConnect: boolean
  onReprConnect: (host: string, port: string) => void
  onReprDisconnect: () => void
  onReprSetAutoConnect: (enabled: boolean, host?: string, port?: string) => void
  version: string
}

function Sidebar({
  navLevel,
  tab,
  onSelectTab,
  hideCompleted,
  onSetHideCompleted,
  pointer,
  selectedPointerStem,
  onSelectPointer,
  condocs,
  activeState,
  selectedCondocPath,
  selectedStepNum,
  selectedIterId,
  selectedSubstepLetter,
  selectedSubstepIterId,
  diffFiles,
  diffFilesLoaded,
  selectedDiffFile,
  fileDiffHunks,
  selectedDiffHunkIdx,
  diffReturnLevel,
  onSelectCondoc,
  onSelectStep,
  onSelectIter,
  onEnterSubstep,
  onSelectSubstepIter,
  onEnterFilesChanged,
  onSelectDiffFile,
  onSelectDiffHunk,
  onNavUp,
  reprStatus,
  reprHost,
  reprPort,
  reprAutoConnect,
  onReprConnect,
  onReprDisconnect,
  onReprSetAutoConnect,
  version,
}: SidebarProps) {
  const reprFooter = (
    <ReprFooter
      status={reprStatus}
      host={reprHost}
      port={reprPort}
      autoConnect={reprAutoConnect}
      onConnect={onReprConnect}
      onDisconnect={onReprDisconnect}
      onSetAutoConnect={onReprSetAutoConnect}
    />
  )

  if (navLevel === 'condoc-list') {
    const visibleCondocs = hideCompleted ? condocs.filter((c) => c.phase !== 'completed') : condocs
    const hiddenCount = condocs.length - visibleCondocs.length

    return (
      <div className="sidebar">
        <div className="sidebar-header">
          <h1>Condoccer{version && <span className="app-version-tag">{version}</span>}</h1>
        </div>
        <div className="scheme-tabs" role="tablist">
          {CONTROL_SCHEME_TABS.map((t) => (
            <button
              key={t.id}
              role="tab"
              aria-selected={tab === t.id}
              className={`scheme-tab${tab === t.id ? ' selected' : ''}`}
              onClick={() => onSelectTab(t.id)}
            >
              {t.label}
            </button>
          ))}
        </div>
        {tab === 'slopspace' ? (
          // The pointers the pointer repo holds, with where each condoc's
          // progression is. A proposal (or a condoc another condoccer
          // holds) opens in the main pane; an accepted one opens in the
          // condoc views, read from its slopspace.
          <div className="nav-list">
            {pointer?.state === 'ready' && (
              <div
                className={`nav-item nav-item-new${selectedPointerStem === '' ? ' selected' : ''}`}
                onClick={() => onSelectPointer(null)}
              >
                <span className="nav-item-name">+ Propose a condoc</span>
              </div>
            )}
            {pointer?.state !== 'ready' && (
              <div className="nav-empty">Set up the pointer repo to see slopspace condocs.</div>
            )}
            {pointer?.state === 'ready' && pointer.pointers.length === 0 && (
              <div className="nav-empty">No pointers in {pointer.repo} yet.</div>
            )}
            {pointer?.state === 'ready' && pointer.pointers.map((p) => (
              <div
                key={p.file}
                className={`nav-item${selectedPointerStem === p.stem ? ' selected' : ''}${p.heldHere ? '' : ' nav-item-readonly'}`}
                title={p.heldHere ? p.file : `${p.file} -- held by ${p.holder || 'nobody'}`}
                onClick={() => onSelectPointer(p)}
              >
                <span className="nav-item-name">{p.name}</span>
                <div className="nav-item-meta">
                  {p.phase && <PhaseBadge phase={p.phase as Phase} />}
                  {p.step && <span className="step-label">step {p.step}{p.substep ? `.${p.substep}` : ''}{p.iteration ? ` ${p.iteration}` : ''}</span>}
                  {p.running && <span className="spinner" title="turn running" />}
                  {!p.running && p.queuePos ? <span className="step-label">#{p.queuePos} in queue</span> : null}
                  {p.error && <span className="badge badge-error" title={p.error}>error</span>}
                </div>
                {p.targets && p.targets.length > 0 && (
                  <span className="nav-item-subtitle">→ {p.targets.join(', ')}</span>
                )}
              </div>
            ))}
          </div>
        ) : (
        <>
        <label className="list-filter">
          <input
            type="checkbox"
            checked={hideCompleted}
            onChange={(e) => onSetHideCompleted(e.target.checked)}
          />
          Hide Completed
          {hideCompleted && hiddenCount > 0 && <span className="list-filter-count">({hiddenCount} hidden)</span>}
        </label>
        <div className="nav-list">
          {condocs.length === 0 && (
            <div className="nav-empty">No condocs found in this repository.</div>
          )}
          {condocs.length > 0 && visibleCondocs.length === 0 && (
            <div className="nav-empty">All condocs are completed.</div>
          )}
          {visibleCondocs.map((c) => (
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
        </>
        )}
        {reprFooter}
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
        {reprFooter}
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

    const stepContent = selectedStepNum === activeState.info.stepNum
      ? (activeState.stepContent ?? '')
      : (activeState.completedStepContents?.[selectedStepNum ?? 0] ?? '')
    const commitRanges = activeState.slopspace ? parseSlopCommitRanges(stepContent) : parseCommitRanges(stepContent)

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
          {iterations.map((iter) => {
            const range = commitRanges.get(iter.id)
            return (
              <div
                key={iter.id}
                className={`nav-item nav-item-iter${selectedIterId === iter.id ? ' selected' : ''}`}
                onClick={() => onSelectIter(iter.id)}
              >
                <span className={iterDotClass(iter)} />
                <span className="nav-item-name">{iter.label}<AgentTag agent={iter.agent} /></span>
                {iter.type === 'substep' && (
                  <button
                    className="nav-enter-btn"
                    title="Enter substep"
                    onClick={(e) => { e.stopPropagation(); onEnterSubstep(iter.id.replace('substep-', '')) }}
                  >
                    →
                  </button>
                )}
                {iter.type === 'reply' && range && (
                  <button
                    className="nav-enter-btn"
                    title="View files changed"
                    onClick={(e) => { e.stopPropagation(); onEnterFilesChanged(range.from, range.to) }}
                  >
                    ⊞
                  </button>
                )}
              </div>
            )
          })}
        </div>
        {reprFooter}
      </div>
    )
  }

  if (navLevel === 'substep' && activeState) {
    const { letter: substepLetter, content: substepContent, iterations: substepIterations } = resolveSubstepView(activeState, selectedSubstepLetter)
    const substepCommitRanges = activeState.slopspace ? parseSlopCommitRanges(substepContent) : parseCommitRanges(substepContent)

    return (
      <div className="sidebar">
        <div className="sidebar-header">
          <button className="nav-up-btn" onClick={onNavUp}>↑ Step {selectedStepNum}</button>
          <div className="sidebar-title">Substep {substepLetter}</div>
        </div>
        <div className="nav-list">
          {substepIterations.length === 0 && (
            <div className="nav-empty">No iterations yet.</div>
          )}
          {substepIterations.map((iter) => {
            const range = substepCommitRanges.get(iter.id)
            return (
              <div
                key={iter.id}
                className={`nav-item nav-item-iter${selectedSubstepIterId === iter.id ? ' selected' : ''}`}
                onClick={() => onSelectSubstepIter(iter.id)}
              >
                <span className={iterDotClass(iter)} />
                <span className="nav-item-name">{iter.label}<AgentTag agent={iter.agent} /></span>
                {iter.type === 'reply' && range && (
                  <button
                    className="nav-enter-btn"
                    title="View files changed"
                    onClick={(e) => { e.stopPropagation(); onEnterFilesChanged(range.from, range.to) }}
                  >
                    ⊞
                  </button>
                )}
              </div>
            )
          })}
        </div>
        {reprFooter}
      </div>
    )
  }

  if (navLevel === 'files-changed') {
    const upLabel = diffReturnLevel === 'substep'
      ? `↑ Substep ${selectedSubstepLetter ?? activeState?.info.substepLetter ?? ''}`
      : `↑ Step ${selectedStepNum ?? ''}`

    return (
      <div className="sidebar">
        <div className="sidebar-header">
          <button className="nav-up-btn" onClick={onNavUp}>{upLabel}</button>
          <div className="sidebar-title">Files Changed</div>
        </div>
        <div className="nav-list">
          {!diffFilesLoaded && (
            <div className="nav-empty">Loading…</div>
          )}
          {diffFilesLoaded && diffFiles.length === 0 && (
            <div className="nav-empty">No project files changed.</div>
          )}
          {diffFiles.map((file, i) => {
            // A slopspace diff spans its repos: head each repo's files.
            const repo = activeState?.slopspace ? diffFileRepo(file).repo : null
            const newRepo = repo !== null && (i === 0 || diffFileRepo(diffFiles[i - 1]).repo !== repo)
            return (
              <div key={file}>
                {newRepo && <div className="nav-repo-header">{repo}</div>}
                <div
                  className={`nav-item${selectedDiffFile === file ? ' selected' : ''}`}
                  onClick={() => onSelectDiffFile(file)}
                >
                  <span className="nav-item-name nav-item-file">{file.split('/').pop() ?? file}</span>
                </div>
              </div>
            )
          })}
        </div>
        {reprFooter}
      </div>
    )
  }

  if (navLevel === 'file-diff') {
    const shortName = selectedDiffFile?.split('/').pop() ?? ''

    return (
      <div className="sidebar">
        <div className="sidebar-header">
          <button className="nav-up-btn" onClick={onNavUp}>↑ Files Changed</button>
          <div className="sidebar-title" title={selectedDiffFile ?? ''}>{shortName}</div>
        </div>
        <div className="nav-list">
          {fileDiffHunks.length === 0 && (
            <div className="nav-empty">No changes.</div>
          )}
          {fileDiffHunks.map((hunk, i) => {
            const lineNum = parseHunkLineNumber(hunk.header)
            return (
              <div
                key={i}
                className={`nav-item${selectedDiffHunkIdx === i ? ' selected' : ''}`}
                onClick={() => onSelectDiffHunk(i)}
              >
                <span className="nav-item-name nav-item-hunk">
                  {lineNum !== null ? `line ${lineNum}` : hunk.header}
                </span>
              </div>
            )
          })}
        </div>
        {reprFooter}
      </div>
    )
  }

  return (
    <div className="sidebar">
      <div className="sidebar-header"><h1>Condoccer{version && <span className="app-version-tag">{version}</span>}</h1></div>
      {reprFooter}
    </div>
  )
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
  completedContent?: string
  onStartStep: (title: string, prompt: string) => void
  onCompleted: () => void
  // Absent for a slopspace condoc, which doesn't support revert.
  onRevert?: (stepNum: number, iterLetter?: string) => void
  isActive: boolean
  isCompleted: boolean
}

function StepCard({ step, completedContent, onStartStep, onCompleted, onRevert, isActive, isCompleted }: StepCardProps) {
  const [title, setTitle] = useState('')
  const [prompt, setPrompt] = useState('')
  const [revertOpen, setRevertOpen] = useState(false)
  const [revertIter, setRevertIter] = useState('')

  const revertIterOptions: string[] = []
  if (completedContent) {
    const sections = parseStepSections(completedContent)
    for (const sec of sections) {
      if (sec.kind === 'revision' || sec.kind === 'retry') {
        const letter = sec.id.split('-').pop() ?? ''
        if (letter) revertIterOptions.push(letter)
      }
    }
  }

  if (step.hasReplace) {
    return (
      <div className="step-card step-card-active">
        <div className="step-card-header">Step {step.num}</div>
        <div className="step-form">
          <label className="step-form-label">Title</label>
          <div className="field-with-mic">
            <input
              className="step-form-input"
              type="text"
              placeholder="Step title…"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
            />
            <MicButton onTranscript={(text) => setTitle(prev => appendTranscript(prev, text))} />
          </div>
          <label className="step-form-label">Prompt</label>
          <div className="field-with-mic">
            <textarea
              className="step-form-textarea"
              placeholder="Describe what the AI should do…"
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              rows={4}
            />
            <MicButton onTranscript={(text) => setPrompt(prev => appendTranscript(prev, text))} />
          </div>
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
      <div className="step-card-header" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span>Step {step.num} — <span className="step-card-title">{step.title}</span></span>
        {isCompleted && onRevert && !revertOpen && (
          <button
            className="btn-danger-sm"
            title={`Revert to step ${step.num}`}
            onClick={() => { setRevertOpen(true); setRevertIter('') }}
          >
            ↩ Revert
          </button>
        )}
      </div>
      {step.prompt && !revertOpen && <div className="step-card-prompt">{step.prompt}</div>}
      {isCompleted && revertOpen && (
        <div className="action-form" style={{ marginTop: 8 }}>
          <div className="action-form-title">Revert to Step {step.num}</div>
          <div className="action-form-row">
            <span className="action-form-label">Before iteration:</span>
            <select value={revertIter} onChange={(e) => setRevertIter(e.target.value)}>
              <option value="">— start (remove all iterations)</option>
              {revertIterOptions.map((l) => (
                <option key={l} value={l}>{l}</option>
              ))}
            </select>
          </div>
          <div className="action-status" style={{ fontSize: 12, color: '#aaa' }}>
            {revertIter
              ? `Keeps content up to just before iteration ${revertIter}. Previous work saved in a diff file.`
              : `Reverts git to the start of step ${step.num}. Previous work saved in a diff file.`}
          </div>
          <div className="action-row">
            <button
              className="btn-danger"
              onClick={() => {
                onRevert?.(step.num, revertIter || undefined)
                setRevertOpen(false)
              }}
            >
              Confirm Revert ↩
            </button>
            <button className="btn-secondary" onClick={() => setRevertOpen(false)}>Cancel</button>
          </div>
        </div>
      )}
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
                  completedContent={s.num < info.stepNum ? state.completedStepContents?.[s.num] : undefined}
                  isActive={s.num === info.stepNum}
                  isCompleted={s.num < info.stepNum}
                  onStartStep={handleStartStep}
                  onCompleted={() => onAction({ action: 'completed', path: info.path })}
                  onRevert={state.slopspace ? undefined : (stepNum, iterLetter) => onAction({ action: 'revert', path: info.path, revertStep: stepNum, revertIter: iterLetter })}
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

        {FORCE_COMPLETABLE_PHASES.includes(info.phase) && info.stepNum > 0 && (
          <ForceCompletePanel slopspace={info.path.startsWith('slopspace:')} onConfirm={() => onAction({ action: 'force_complete', path: info.path })} />
        )}
      </div>
    </div>
  )
}

// FORCE_COMPLETABLE_PHASES are the phases Force Complete is offered in: any
// started condoc not yet completed. Mid-run, federation-command kills the
// agent and completes immediately; a slopspace condoc's worker can't be
// interrupted, so it completes as soon as the turn in flight lands.
const FORCE_COMPLETABLE_PHASES: Phase[] = ['awaiting_step', 'agent_running', 'awaiting_action', 'queued']

// ForceCompletePanel jumps a condoc out of its current state straight to
// completed, where it may be PR'd and merged. Pending work in the open step
// or substep is closed out as-is, so it asks for confirmation first.
function ForceCompletePanel({ slopspace, onConfirm }: { slopspace: boolean; onConfirm: () => void }) {
  const [confirming, setConfirming] = useState(false)
  if (!confirming) {
    return (
      <div className="action-panel">
        <div className="action-row">
          <button className="btn-danger" onClick={() => setConfirming(true)}>
            Force Complete
          </button>
        </div>
      </div>
    )
  }
  return (
    <div className="action-panel">
      <div className="action-form">
        <div className="action-form-title">Force Complete</div>
        <div className="action-status" style={{ fontSize: 12, color: '#aaa' }}>
          Closes the open step and substep as they stand and marks the condoc completed, ready to PR and merge.
          {slopspace
            ? 'If a turn is in flight, its reply is written and then the condoc completes.'
            : 'If an agent is running, it is killed immediately and its work so far is committed as-is.'}
        </div>
        <div className="action-row">
          <button
            className="btn-danger"
            onClick={() => {
              onConfirm()
              setConfirming(false)
            }}
          >
            Confirm Force Complete ✓
          </button>
          <button className="btn-secondary" onClick={() => setConfirming(false)}>Cancel</button>
        </div>
      </div>
    </div>
  )
}

// ---- Clipboard resources (Step4SubstepDPrompt.md Revision G) ----
//
// The "Clipboard" source of Add Resources does what local-representative's
// and agent-coordinator's "new file from clipboard" button does (Revisions
// D/E there): take an image or plain text from the copy buffer, default its
// name to "clipboard-<date>-<time>.<ext>" (the extension sniffed by
// detectTextFormat for text), and let the operator rename it before it goes
// through the same /api/upload-resource route an "Upload" does. The helpers
// below mirror those apps' own copies.

// ClipboardContent is what was found in the copy buffer, already as the
// Blob the upload will carry.
interface ClipboardContent {
  kind: 'image' | 'text'
  blob: Blob
  ext: string
  text?: string // kind 'text' only -- shown as the preview
  format?: string // kind 'text' only -- the detected format's label, e.g. "JSON"
}

// CLIPBOARD_IMAGE_EXT is the image types taken from the copy buffer, in
// preference order. The async clipboard API only ever hands back image/png,
// but a paste event can carry the others as-is.
const CLIPBOARD_IMAGE_EXT: Record<string, string> = {
  'image/png': 'png',
  'image/jpeg': 'jpg',
  'image/gif': 'gif',
  'image/webp': 'webp',
  'image/svg+xml': 'svg',
  'image/bmp': 'bmp',
}

// CLIPBOARD_POLL_MS is how often useClipboardState rechecks the copy buffer
// while the page is focused -- there's no "clipboard changed" event.
const CLIPBOARD_POLL_MS = 3000

function textClipboard(text: string): ClipboardContent {
  const f = detectTextFormat(text)
  return { kind: 'text', blob: new Blob([text], { type: f.mime }), ext: f.ext, text, format: f.label }
}

// clipboardSupported says whether the page may read the copy buffer itself
// at all -- the async clipboard API exists only in a secure context (https
// or localhost), so over plain http only a Ctrl+V paste event can get at it.
function clipboardSupported(): boolean {
  return !!(navigator.clipboard?.read || navigator.clipboard?.readText)
}

// readClipboard reads the copy buffer through the async clipboard API: the
// first image item wins, then plain text. null means it's empty (or holds
// only things we don't take); it throws when the read isn't allowed.
async function readClipboard(): Promise<ClipboardContent | null> {
  const cb = navigator.clipboard
  if (cb?.read) {
    const items = await cb.read()
    for (const item of items) {
      const type = Object.keys(CLIPBOARD_IMAGE_EXT).find(t => item.types.includes(t))
      if (type) return { kind: 'image', blob: await item.getType(type), ext: CLIPBOARD_IMAGE_EXT[type] }
    }
    for (const item of items) {
      if (!item.types.includes('text/plain')) continue
      const text = await (await item.getType('text/plain')).text()
      if (text) return textClipboard(text)
    }
    return null
  }
  if (cb?.readText) {
    const text = await cb.readText()
    return text ? textClipboard(text) : null
  }
  throw new Error('clipboard API unavailable')
}

// clipboardFromPaste is readClipboard's counterpart for a paste event's
// DataTransfer -- no permission needed, so it works even over plain http.
function clipboardFromPaste(data: DataTransfer): ClipboardContent | null {
  for (const item of Array.from(data.items)) {
    if (item.kind !== 'file' || !CLIPBOARD_IMAGE_EXT[item.type]) continue
    const f = item.getAsFile()
    if (f) return { kind: 'image', blob: f, ext: CLIPBOARD_IMAGE_EXT[item.type] }
  }
  const text = data.getData('text/plain')
  return text ? textClipboard(text) : null
}

// ClipboardState is what the "read clipboard" button knows about the copy
// buffer: 'unknown' leaves it enabled (clicking it asks the browser),
// 'empty' disables it, 'has' enables it.
type ClipboardState = 'unknown' | 'empty' | 'has'

// useClipboardState watches the copy buffer while enabled, but only once the
// browser has already granted clipboard-read -- without that grant every
// probe would pop a permission prompt or "Paste" bubble, so the state just
// stays 'unknown'. The probe only lists the item types; it never pulls the
// bytes.
function useClipboardState(enabled: boolean): ClipboardState {
  const [state, setState] = useState<ClipboardState>('unknown')
  useEffect(() => {
    if (!enabled || !navigator.clipboard?.read || !navigator.permissions?.query) return
    let cancelled = false
    let granted = false
    let perm: PermissionStatus | null = null
    const probe = async () => {
      if (!granted || document.visibilityState !== 'visible' || !document.hasFocus()) return
      try {
        const items = await navigator.clipboard.read()
        const has = items.some(item => item.types.some(t => t === 'text/plain' || CLIPBOARD_IMAGE_EXT[t]))
        if (!cancelled) setState(has ? 'has' : 'empty')
      } catch {
        // Focus lost mid-read or access revoked -- keep the last answer.
      }
    }
    const onPerm = () => {
      granted = perm?.state === 'granted'
      if (granted) void probe()
      else setState('unknown')
    }
    // 'clipboard-read' isn't in every TS lib's PermissionName union.
    navigator.permissions.query({ name: 'clipboard-read' } as unknown as PermissionDescriptor)
      .then(p => {
        if (cancelled) return
        perm = p
        p.addEventListener('change', onPerm)
        onPerm()
      })
      .catch(() => { /* Firefox/Safari don't know the name -- stay 'unknown' */ })
    const onWake = () => { void probe() }
    // "copy"/"cut" fire before the copy buffer is written, hence the delay.
    const onCopy = () => { window.setTimeout(onWake, 100) }
    window.addEventListener('focus', onWake)
    document.addEventListener('visibilitychange', onWake)
    document.addEventListener('copy', onCopy)
    document.addEventListener('cut', onCopy)
    const timer = window.setInterval(onWake, CLIPBOARD_POLL_MS)
    return () => {
      cancelled = true
      perm?.removeEventListener('change', onPerm)
      window.removeEventListener('focus', onWake)
      document.removeEventListener('visibilitychange', onWake)
      document.removeEventListener('copy', onCopy)
      document.removeEventListener('cut', onCopy)
      window.clearInterval(timer)
    }
  }, [enabled])
  return state
}

// clipboardDefaultName is a clipboard resource's starting name, e.g.
// "clipboard-20261010-142233.png".
function clipboardDefaultName(ext: string): string {
  const d = new Date()
  const p = (n: number) => String(n).padStart(2, '0')
  return `clipboard-${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}.${ext}`
}

// CLIPBOARD_TEXT_PREVIEW caps how much pasted text the preview shows.
const CLIPBOARD_TEXT_PREVIEW = 2000

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`
}

// isEditableTarget says a paste is meant for a text field, not the form.
function isEditableTarget(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false
  return t.isContentEditable || t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT'
}

// ClipboardResourceFields is the "Clipboard" source's part of the Add
// Resources form: the captured content's file name -- stem (not extension)
// selected on capture, so typing replaces just that -- and a preview.
function ClipboardResourceFields({
  content,
  fileName,
  onFileName,
}: {
  content: ClipboardContent
  fileName: string
  onFileName: (name: string) => void
}) {
  const [imageUrl, setImageUrl] = useState<string | null>(null)
  const nameRef = useRef<HTMLInputElement | null>(null)

  useEffect(() => {
    const el = nameRef.current
    if (!el) return
    el.focus()
    const dot = el.value.lastIndexOf('.')
    el.setSelectionRange(0, dot > 0 ? dot : el.value.length)
  }, [content])

  useEffect(() => {
    if (content.kind !== 'image') return
    const url = URL.createObjectURL(content.blob)
    setImageUrl(url)
    return () => URL.revokeObjectURL(url)
  }, [content])

  const text = content.text ?? ''
  return (
    <div className="clip-resource">
      <div className="action-form-row">
        <span className="action-form-label">File:</span>
        <input
          ref={nameRef}
          className="step-form-input"
          type="text"
          value={fileName}
          onChange={(e) => onFileName(e.target.value)}
        />
      </div>
      {content.kind === 'image' ? (
        imageUrl && <img className="clip-resource-image" src={imageUrl} alt="clipboard image" />
      ) : (
        <pre className="clip-resource-text">
          {text.length > CLIPBOARD_TEXT_PREVIEW
            ? `${text.slice(0, CLIPBOARD_TEXT_PREVIEW)}\n… (${text.length - CLIPBOARD_TEXT_PREVIEW} more characters)`
            : text}
        </pre>
      )}
      <div className="clip-resource-size">
        {content.kind === 'image' ? 'image' : 'text'}
        {content.format && ` · detected ${content.format}`} · {formatBytes(content.blob.size)}
      </div>
    </div>
  )
}

// ---- Action panel (for step or substep view) ----

type ActionMode = null | 'revision' | 'retry' | 'revert' | 'substep' | 'add_resource'

type ResourceType = 'highlighted' | 'upload' | 'clipboard' | 'voice-note'

interface ActionPanelProps {
  state: CondocState
  onAction: (action: ActionRequest) => void
  isSubstep?: boolean
}

function ActionPanel({ state, onAction, isSubstep = false }: ActionPanelProps) {
  const { info, nextLetter, fromOptions } = state
  const [mode, setMode] = useState<ActionMode>(null)
  const [promptText, setPromptText] = useState('')
  const [fromSel, setFromSel] = useState('start')
  const [revertIter, setRevertIter] = useState('')
  const [substepTitle, setSubstepTitle] = useState('')
  const [resourceType, setResourceType] = useState<ResourceType>('highlighted')
  const { available: tcAvailable } = useContext(TCCaptureContext)
  const [resourceName, setResourceName] = useState('')
  const [resourceDescription, setResourceDescription] = useState('')
  // "Upload" source (Revision C): the up-arrow button locks in the file
  // selection the moment a file is chosen, so the operator can't switch back
  // to "Highlighted" mid-flight -- only Cancel or Submit clears it.
  const [uploadFiles, setUploadFiles] = useState<File[]>([])
  const [uploading, setUploading] = useState(false)
  const [uploadError, setUploadError] = useState('')
  const uploadInputRef = useRef<HTMLInputElement | null>(null)
  // "select from host" (Step4Prompt.md Revision F of initialRobotImpls): files
  // on local-representative's host, by path, in place of uploadFiles.
  const [hostPaths, setHostPaths] = useState<string[]>([])
  const [pickingHost, setPickingHost] = useState(false)
  // "Clipboard" source (Step4SubstepDPrompt.md Revision G): what was taken
  // from the copy buffer, and the file name it'll be uploaded under.
  const [clipContent, setClipContent] = useState<ClipboardContent | null>(null)
  const [clipFileName, setClipFileName] = useState('')
  const clipSelected = mode === 'add_resource' && resourceType === 'clipboard'
  const clipState = useClipboardState(clipSelected)

  const takeClipboard = (content: ClipboardContent) => {
    setClipContent(content)
    setClipFileName(clipboardDefaultName(content.ext))
    setUploadError('')
  }

  const resetResourceForm = () => {
    setResourceType('highlighted')
    setUploadFiles([])
    setHostPaths([])
    setUploadError('')
    setClipContent(null)
    setClipFileName('')
  }

  const fromClipboard = async () => {
    setUploadError('')
    if (!clipboardSupported()) {
      setUploadError("this page can't read the clipboard over plain http — press Ctrl+V (⌘V) outside the text boxes instead")
      return
    }
    try {
      const content = await readClipboard()
      if (content) takeClipboard(content)
      else setUploadError('the clipboard has no text or image in it')
    } catch {
      setUploadError("couldn't read the clipboard — allow clipboard access for this page, or press Ctrl+V (⌘V) outside the text boxes")
    }
  }

  // With the "Clipboard" source chosen, Ctrl+V anywhere outside a text field
  // captures the copy buffer -- the one way in over plain http.
  useEffect(() => {
    if (!clipSelected) return
    const onPaste = (e: ClipboardEvent) => {
      if (isEditableTarget(e.target) || !e.clipboardData) return
      const content = clipboardFromPaste(e.clipboardData)
      if (!content) return
      e.preventDefault()
      takeClipboard(content)
    }
    document.addEventListener('paste', onPaste)
    return () => document.removeEventListener('paste', onPaste)
  }, [clipSelected])

  useEffect(() => {
    setMode(null)
    setPromptText('')
    setFromSel('start')
    setRevertIter('')
    setSubstepTitle('')
    setResourceName('')
    setResourceDescription('')
    resetResourceForm()
  }, [info.path, info.stepNum, info.substepLetter])

  // Falls back to "Highlighted" if TC availability drops out from under an
  // open "Voice Note" selection (its <option> disappears from the <select>
  // above the instant tcAvailable does) -- there'd be no way left to dictate
  // anything into it.
  useEffect(() => {
    if (!tcAvailable && resourceType === 'voice-note') setResourceType('highlighted')
  }, [tcAvailable, resourceType])

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

  // Build revert iteration options: letters that exist (Reply A → A is a valid revert point).
  const revertIterOptions: string[] = []
  const iterSource = isSubstep ? (state.substepIterations ?? []) : (state.iterations ?? [])
  for (const iter of iterSource) {
    if (iter.type === 'revision' || iter.type === 'retry' || iter.type === 'substep') {
      const letter = iter.id.split('-').pop() ?? ''
      if (letter) revertIterOptions.push(letter)
    }
  }

  const buildRevertAction = (): ActionRequest => {
    if (isSubstep && info.substepLetter) {
      return {
        action: 'revert',
        path: info.path,
        revertStep: info.stepNum,
        revertIter: info.substepLetter,
        revertSubIter: revertIter || undefined,
      }
    }
    return {
      action: 'revert',
      path: info.path,
      revertStep: info.stepNum,
      revertIter: revertIter || undefined,
    }
  }

  return (
    <div className="action-panel">
      {mode === null && (
        <div className="action-row">
          <button
            className="btn-success"
            onClick={() => onAction({ action: 'completed', path: info.path })}
          >
            Complete {isSubstep ? 'Substep' : 'Step'} ✓
          </button>
          <button className="btn-warning" onClick={() => setMode('revision')}>
            Revise {nextLetter}…
          </button>
          {/* Slopspace condocs don't support retry or revert (D8). */}
          {!state.slopspace && (
            <button className="btn-secondary" onClick={() => setMode('retry')}>
              Retry {nextLetter}…
            </button>
          )}
          {!isSubstep && (
            <button className="btn-primary" onClick={() => setMode('substep')}>
              Substep {nextLetter}…
            </button>
          )}
          {!state.slopspace && (
            <button className="btn-danger" onClick={() => setMode('revert')}>
              Revert↩
            </button>
          )}
          <button className="btn-secondary" onClick={() => setMode('add_resource')}>
            Add Resources…
          </button>
        </div>
      )}

      {mode === 'revision' && (
        <div className="action-form">
          <div className="action-form-title">Revision {nextLetter}</div>
          <div className="field-with-mic">
            <textarea
              placeholder="Describe the revision you want…"
              value={promptText}
              onChange={(e) => setPromptText(e.target.value)}
              rows={4}
            />
            <MicButton onTranscript={(text) => setPromptText(prev => appendTranscript(prev, text))} />
          </div>
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
          <div className="field-with-mic">
            <textarea
              placeholder="Describe what to try differently…"
              value={promptText}
              onChange={(e) => setPromptText(e.target.value)}
              rows={4}
            />
            <MicButton onTranscript={(text) => setPromptText(prev => appendTranscript(prev, text))} />
          </div>
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

      {mode === 'substep' && (
        <div className="action-form">
          <div className="action-form-title">Substep {nextLetter}</div>
          <div className="field-with-mic">
            <input
              className="step-form-input"
              type="text"
              placeholder="Substep title…"
              value={substepTitle}
              onChange={(e) => setSubstepTitle(e.target.value)}
            />
            <MicButton onTranscript={(text) => setSubstepTitle(prev => appendTranscript(prev, text))} />
          </div>
          <div className="field-with-mic">
            <textarea
              placeholder="Describe what the substep should accomplish…"
              value={promptText}
              onChange={(e) => setPromptText(e.target.value)}
              rows={4}
            />
            <MicButton onTranscript={(text) => setPromptText(prev => appendTranscript(prev, text))} />
          </div>
          <div className="action-row">
            <button
              className="btn-primary"
              onClick={() => {
                if (!substepTitle.trim() || !promptText.trim()) return
                onAction({ action: 'substep', path: info.path, letter: nextLetter, substepTitle: substepTitle.trim(), content: promptText.trim() })
                setMode(null)
                setPromptText('')
                setSubstepTitle('')
              }}
              disabled={!substepTitle.trim() || !promptText.trim()}
            >
              Create Substep →
            </button>
            <button className="btn-secondary" onClick={() => setMode(null)}>Cancel</button>
          </div>
        </div>
      )}

      {mode === 'revert' && (
        <div className="action-form">
          <div className="action-form-title">
            Revert {isSubstep ? `Substep ${info.substepLetter}` : `Step ${info.stepNum}`}
          </div>
          {revertIterOptions.length > 0 ? (
            <div className="action-form-row">
              <span className="action-form-label">Before iteration:</span>
              <select value={revertIter} onChange={(e) => setRevertIter(e.target.value)}>
                {!isSubstep
                  ? <option value="">— start (remove all iterations)</option>
                  : <option value="" disabled>— choose iteration —</option>
                }
                {revertIterOptions.map((l) => (
                  <option key={l} value={l}>{l}</option>
                ))}
              </select>
            </div>
          ) : (
            !isSubstep && (
              <div className="action-status" style={{ fontSize: 12, color: '#aaa' }}>
                No iterations yet — will revert to step start.
              </div>
            )
          )}
          {isSubstep && revertIterOptions.length === 0 && (
            <div className="action-status" style={{ fontSize: 12, color: '#666' }}>
              No iterations to revert to within this substep.
            </div>
          )}
          <div className="action-status" style={{ fontSize: 12, color: '#aaa', margin: '4px 0' }}>
            {revertIter
              ? `Keeps content up to just before iteration ${revertIter}. Previous work saved in a diff file.`
              : isSubstep
                ? 'Select an iteration above to revert to.'
                : 'Reverts git to the start of this step. Previous work saved in a diff file.'}
          </div>
          <div className="action-row">
            <button
              className="btn-danger"
              disabled={isSubstep && !revertIter}
              onClick={() => {
                onAction(buildRevertAction())
                setMode(null)
              }}
            >
              Confirm Revert ↩
            </button>
            <button className="btn-secondary" onClick={() => setMode(null)}>Cancel</button>
          </div>
        </div>
      )}

      {mode === 'add_resource' && (
        <div className="action-form">
          <div className="action-form-title">Add Resources</div>
          <div className="action-form-row">
            <span className="action-form-label">Source:</span>
            <select
              value={resourceType}
              disabled={(resourceType === 'upload' && (uploadFiles.length > 0 || hostPaths.length > 0)) || (resourceType === 'clipboard' && clipContent !== null)}
              onChange={(e) => {
                setResourceType(e.target.value as ResourceType)
                setUploadFiles([])
                setHostPaths([])
                setUploadError('')
                setClipContent(null)
                setClipFileName('')
              }}
            >
              <option value="highlighted">Highlighted</option>
              <option value="upload">Upload</option>
              <option value="clipboard">Clipboard</option>
              {tcAvailable && <option value="voice-note">Voice Note</option>}
            </select>
            {resourceType === 'clipboard' && (
              <button
                type="button"
                className="btn-secondary resource-upload-btn"
                title={
                  clipState === 'empty'
                    ? 'the clipboard is empty'
                    : 'take the text or image on the clipboard (or press Ctrl+V outside the text boxes)'
                }
                disabled={clipState === 'empty'}
                onClick={() => void fromClipboard()}
              >
                📋
              </button>
            )}
            {resourceType === 'upload' && (
              <>
                <input
                  ref={uploadInputRef}
                  type="file"
                  multiple
                  style={{ display: 'none' }}
                  onChange={(e) => {
                    if (e.target.files && e.target.files.length > 0) {
                      setUploadFiles(Array.from(e.target.files))
                      setUploadError('')
                    }
                    e.target.value = ''
                  }}
                />
                <button
                  type="button"
                  className="btn-secondary resource-upload-btn"
                  title="Choose file(s) to upload from this computer"
                  disabled={uploadFiles.length > 0 || hostPaths.length > 0}
                  onClick={() => uploadInputRef.current?.click()}
                >
                  ⬆
                </button>
                <button
                  type="button"
                  className="btn-secondary resource-upload-btn"
                  title="Select from host: choose file(s) on local-representative's host"
                  disabled={uploadFiles.length > 0 || hostPaths.length > 0}
                  onClick={() => setPickingHost(true)}
                >
                  🖥
                </button>
                {uploadFiles.length > 0 && (
                  <span className="resource-upload-filenames">
                    {uploadFiles.map((f) => f.name).join(', ')}
                  </span>
                )}
                {hostPaths.length > 0 && (
                  <span className="resource-upload-filenames" title={hostPaths.join('\n')}>
                    {hostPaths.map((p) => p.slice(p.lastIndexOf('/') + 1)).join(', ')} (from host)
                  </span>
                )}
                {pickingHost && (
                  <HostFilePicker
                    api={basePath()}
                    multiple
                    title="select from host"
                    onClose={() => setPickingHost(false)}
                    onPick={(paths) => {
                      setPickingHost(false)
                      setHostPaths(paths)
                      setUploadError('')
                    }}
                  />
                )}
              </>
            )}
          </div>
          {resourceType === 'clipboard' && (
            clipContent ? (
              <ClipboardResourceFields content={clipContent} fileName={clipFileName} onFileName={setClipFileName} />
            ) : (
              <div className="action-status resource-clip-hint">
                Click 📋 or press Ctrl+V (⌘V) outside the text boxes to take the clipboard's text or image.
              </div>
            )
          )}
          <div className="field-with-mic">
            <input
              className="step-form-input"
              type="text"
              placeholder="Name (optional) — e.g. Screenshots…"
              value={resourceName}
              onChange={(e) => setResourceName(e.target.value)}
            />
            <MicButton onTranscript={(text) => setResourceName(prev => appendTranscript(prev, text))} />
          </div>
          <div className="field-with-mic">
            <textarea
              placeholder={
                resourceType === 'voice-note'
                  ? 'Dictate your voice note with the mic below…'
                  : 'Describe why these resources are included…'
              }
              value={resourceDescription}
              onChange={(e) => setResourceDescription(e.target.value)}
              rows={3}
            />
            <MicButton onTranscript={(text) => setResourceDescription(prev => appendTranscript(prev, text))} />
          </div>
          {uploadError && (
            <div className="action-status resource-upload-error">{uploadError}</div>
          )}
          <div className="action-row">
            <button
              className="btn-primary"
              disabled={
                (resourceType === 'upload' && uploadFiles.length === 0 && hostPaths.length === 0) ||
                (resourceType === 'clipboard' && (!clipContent || !clipFileName.trim())) ||
                (resourceType === 'voice-note' && !resourceDescription.trim()) ||
                uploading
              }
              onClick={async () => {
                if (resourceType === 'upload' && hostPaths.length > 0) {
                  setUploading(true)
                  setUploadError('')
                  try {
                    const resp = await fetch(`${basePath()}/api/host-resource`, {
                      method: 'POST',
                      headers: { 'Content-Type': 'application/json' },
                      body: JSON.stringify({
                        path: info.path,
                        name: resourceName.trim(),
                        description: resourceDescription.trim(),
                        host_paths: hostPaths,
                      }),
                    })
                    if (!resp.ok) {
                      setUploadError(await resp.text())
                      return
                    }
                    setMode(null)
                    resetResourceForm()
                    setResourceName('')
                    setResourceDescription('')
                  } catch (err) {
                    setUploadError(String(err))
                  } finally {
                    setUploading(false)
                  }
                  return
                }
                if (resourceType === 'upload' || resourceType === 'clipboard') {
                  let files = uploadFiles
                  if (resourceType === 'clipboard') {
                    const trimmed = clipFileName.trim()
                    if (!clipContent || !trimmed) return
                    // A name typed without any extension gets the content's own one back.
                    const finalName = trimmed.includes('.') ? trimmed : `${trimmed}.${clipContent.ext}`
                    files = [new File([clipContent.blob], finalName, { type: clipContent.blob.type })]
                  }
                  if (files.length === 0) return
                  setUploading(true)
                  setUploadError('')
                  try {
                    const form = new FormData()
                    form.append('path', info.path)
                    if (resourceName.trim()) form.append('name', resourceName.trim())
                    form.append('description', resourceDescription.trim())
                    for (const f of files) form.append('file', f)
                    const resp = await fetch(`${basePath()}/api/upload-resource`, { method: 'POST', body: form })
                    if (!resp.ok) {
                      setUploadError(await resp.text())
                      return
                    }
                    setMode(null)
                    resetResourceForm()
                    setResourceName('')
                    setResourceDescription('')
                  } catch (err) {
                    setUploadError(String(err))
                  } finally {
                    setUploading(false)
                  }
                  return
                }
                onAction({
                  action: 'add_resource',
                  path: info.path,
                  resourceType,
                  resourceName: resourceName.trim() || undefined,
                  content: resourceDescription.trim(),
                })
                setMode(null)
                setResourceType('highlighted')
                setResourceName('')
                setResourceDescription('')
              }}
            >
              {uploading ? 'Uploading…' : 'Add Resources →'}
            </button>
            <button
              className="btn-secondary"
              onClick={() => {
                setMode(null)
                resetResourceForm()
              }}
            >
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

// ---- Substep detail view ----

interface SubstepDetailViewProps {
  state: CondocState
  substepLetter: string
  selectedSubstepIterId: string | null
  onAction: (action: ActionRequest) => void
}

function SubstepDetailView({ state, substepLetter, selectedSubstepIterId, onAction }: SubstepDetailViewProps) {
  const sectionRefs = useRef<Record<string, HTMLDivElement | null>>({})
  const { isActive, content } = resolveSubstepView(state, substepLetter)

  useEffect(() => {
    if (selectedSubstepIterId && sectionRefs.current[selectedSubstepIterId]) {
      sectionRefs.current[selectedSubstepIterId]?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    }
  }, [selectedSubstepIterId])

  const sections = parseStepSections(content)

  // A completed substep is read-only: the action buttons (revision/retry/
  // revert/complete) act on whichever substep condoccer currently considers
  // active, which this one no longer is -- see resolveSubstepView.
  if (!isActive) {
    return (
      <div className="detail-view">
        <div className="detail-header">
          <h2>Substep {substepLetter}</h2>
          <PhaseBadge phase="completed" />
        </div>
        <div className="detail-body">
          {sections.map((sec) => (
            <div
              key={sec.id}
              className={sectionClass(sec, selectedSubstepIterId === sec.id)}
              ref={(el) => { sectionRefs.current[sec.id] = el }}
            >
              <SectionLabel sec={sec} condocPath={state.info.path} />
              {sectionBody(sec, state.info.path)}
            </div>
          ))}
          <div className="action-panel">
            <div className="action-status" style={{ color: '#4ec94e' }}>✓ Substep completed.</div>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="detail-view">
      <div className="detail-header">
        <h2>Substep {substepLetter}</h2>
        <PhaseBadge phase={state.info.phase} />
        {state.info.phase === 'agent_running' && (
          <button
            className="btn-secondary"
            style={{ marginLeft: 'auto', fontSize: 11, padding: '3px 8px' }}
            title="Re-invoke the agent (use when agent has crashed mid-run)"
            onClick={() => onAction({ action: 'resubmit', path: state.info.path })}
          >
            ↺ Resubmit
          </button>
        )}
      </div>
      <div className="detail-body">
        {sections.map((sec) => (
          <div
            key={sec.id}
            className={sectionClass(sec, selectedSubstepIterId === sec.id)}
            ref={(el) => { sectionRefs.current[sec.id] = el }}
          >
            <SectionLabel sec={sec} condocPath={state.info.path} />
            {sectionBody(sec, state.info.path)}
          </div>
        ))}
      </div>
      <ActionPanel state={state} onAction={onAction} isSubstep />
    </div>
  )
}

// ---- Step detail view ----

interface StepDetailViewProps {
  state: CondocState
  stepNum: number
  selectedIterId: string | null
  onAction: (action: ActionRequest) => void
  onEnterSubstep: (substepLetter: string) => void
}

function StepDetailView({ state, stepNum, selectedIterId, onAction, onEnterSubstep }: StepDetailViewProps) {
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
                  className={sectionClass(sec, selectedIterId === sec.id)}
                  ref={(el) => { sectionRefs.current[sec.id] = el }}
                >
                  <SectionLabel sec={sec} condocPath={state.info.path} />
                  {sec.kind === 'substep' ? (
                    <div className="iter-section-content">
                      <span style={{ color: '#aaa', fontSize: 12 }}>{sec.content}</span>
                      <button
                        className="btn-secondary"
                        style={{ marginLeft: 8, fontSize: 11, padding: '2px 8px' }}
                        onClick={() => sec.substepLetter && onEnterSubstep(sec.substepLetter)}
                      >
                        View substep →
                      </button>
                    </div>
                  ) : (
                    sectionBody(sec, state.info.path)
                  )}
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
        {state.info.phase === 'agent_running' && (
          <button
            className="btn-secondary"
            style={{ marginLeft: 'auto', fontSize: 11, padding: '3px 8px' }}
            title="Re-invoke the agent (use when agent has crashed mid-run)"
            onClick={() => onAction({ action: 'resubmit', path: state.info.path })}
          >
            ↺ Resubmit
          </button>
        )}
      </div>
      <div className="detail-body">
        {sections.map((sec) => (
          <div
            key={sec.id}
            className={sectionClass(sec, selectedIterId === sec.id)}
            ref={(el) => { sectionRefs.current[sec.id] = el }}
            onDoubleClick={() => sec.kind === 'substep' && sec.substepLetter && onEnterSubstep(sec.substepLetter)}
          >
            <SectionLabel sec={sec} condocPath={state.info.path}>
              {sec.kind === 'substep' && sec.substepLetter && (
                <button
                  className="btn-secondary"
                  style={{ fontSize: 11, padding: '2px 8px' }}
                  onClick={() => onEnterSubstep(sec.substepLetter!)}
                >
                  Enter →
                </button>
              )}
            </SectionLabel>
            {sectionBody(sec, state.info.path)}
          </div>
        ))}
      </div>
      <ActionPanel state={state} onAction={onAction} />
    </div>
  )
}

// ---- Slopspace condocs (see condoccer/slopspace.go) ----

type SlopSend = (
  type: 'slop-withdraw' | 'slop-accept' | 'slop-resubmit' | 'slop-merge' | 'slop-save-pr-template' | 'slop-regenerate-prs' | 'slop-accept-prs',
  stem: string,
  extra?: { template?: string; instruction?: string },
) => void

function splitRepos(text: string): string[] {
  return text.split(/[\s,]+/).map((r) => r.trim()).filter(Boolean)
}

// ProposalForm proposes a new slopspace condoc, or edits one still proposed:
// its targets (1-N, all different), the repo the condoc itself lives in, the
// branch (must not exist yet in any of them) and the agent/model its turns
// run with.
function ProposalForm({
  pointer,
  entry,
  branchCheck,
  onPropose,
  onCheckBranch,
}: {
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
  // Re-seed when another proposal is selected (or this one is saved).
  const seedKey = `${entry?.stem ?? ''}|${entry?.file ?? ''}|${entry?.branch ?? ''}|${(entry?.targets ?? []).join(',')}`
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
          placeholder={'owner/repo, one per line'}
          onChange={(e) => setDraft({ ...draft, targets: e.target.value })}
        />
      </label>
      {field('condocRepo', 'Condoc repo', 'owner/name (not one of the targets)')}
      {field('branch', 'Branch', entry ? '' : 'blank: condoc/<Name>-<startTime>/main')}
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
  return (
    <div className="slop-details">
      <div className="slop-row">
        <PhaseBadge phase={entry.phase as Phase} />
        {entry.step && <span className="step-label">step {entry.step}{entry.substep ? `.${entry.substep}` : ''}{entry.iteration ? ` ${entry.iteration}` : ''}</span>}
        {entry.running && <span className="step-label"><span className="spinner" /> {entry.turnKind?.replace(/_/g, ' ')} turn {entry.turnStage}</span>}
        {!entry.running && entry.queuePos ? <span className="step-label">#{entry.queuePos} in the turn queue</span> : null}
        {entry.deployed && <span className="step-label">slopspace deployed</span>}
        {entry.forcePending && <span className="step-label">force complete once the turn lands</span>}
      </div>
      <div className="slop-row"><span className="working-dir-label">Branch</span><code>{entry.branch}</code></div>
      <div className="slop-row"><span className="working-dir-label">Targets</span><code>{(entry.targets ?? []).join(', ')}</code></div>
      <div className="slop-row">
        <span className="working-dir-label">Condoc</span>
        {entry.condocUrl ? <a href={entry.condocUrl} target="_blank" rel="noreferrer">{entry.condocRepo}/{entry.condocPath}</a> : <code>{entry.condocRepo}</code>}
      </div>
      {(entry.agent || entry.model) && (
        <div className="slop-row"><span className="working-dir-label">Agent</span><code>{entry.agent}{entry.model ? ` / ${entry.model}` : ''}</code></div>
      )}
      {!entry.heldHere && <div className="pointer-note">Held by {entry.holder || 'nobody'} — shown read-only.</div>}
      {entry.description && <div className="slop-description">{entry.description}</div>}
    </div>
  )
}

// SlopspacePanel is a slopspace condoc's own controls, shown above its
// condoc views (and with its pointer on the list): its error with Resubmit,
// the propose-PR template (edit / regenerate with an instruction / accept),
// and the raised PRs with Merge once every one is mergeable.
function SlopspacePanel({ entry, onSend }: { entry: PointerEntry; onSend: SlopSend }) {
  const [template, setTemplate] = useState(entry.prTemplate ?? '')
  const [dirty, setDirty] = useState(false)
  const [instruction, setInstruction] = useState('')
  useEffect(() => {
    if (!dirty) setTemplate(entry.prTemplate ?? '')
  }, [entry.prTemplate, dirty])

  const prs = entry.prs ?? []
  const allReady = prs.length > 0 && prs.every((p) => p.ready || p.state === 'merged')
  const held = entry.heldHere

  return (
    <div className="slop-panel">
      <PointerDetails entry={entry} />
      {entry.error && (
        <div className="slop-error">
          <div>{entry.error}</div>
          {held && (
            <button className="btn-primary working-dir-btn" onClick={() => onSend('slop-resubmit', entry.stem)}>Resubmit</button>
          )}
        </div>
      )}

      {entry.phase === 'pr_proposed' && (
        <div className="slop-prs">
          <h3>Propose PRs</h3>
          <textarea
            className="working-dir-input slop-textarea slop-template"
            rows={14}
            spellCheck={false}
            value={template}
            disabled={!held}
            onChange={(e) => { setTemplate(e.target.value); setDirty(true) }}
          />
          {held && (
            <>
              <div className="pointer-actions">
                <button
                  className="btn-primary working-dir-btn"
                  onClick={() => { onSend('slop-accept-prs', entry.stem, { template }); setDirty(false) }}
                >
                  Accept &amp; Raise PRs
                </button>
                <button
                  className="btn-secondary working-dir-btn"
                  disabled={!dirty}
                  onClick={() => { onSend('slop-save-pr-template', entry.stem, { template }); setDirty(false) }}
                >
                  Save Edits
                </button>
              </div>
              <div className="pointer-actions">
                <input
                  className="working-dir-input"
                  value={instruction}
                  placeholder="Instruction for regenerating the proposal"
                  onChange={(e) => setInstruction(e.target.value)}
                />
                <button
                  className="btn-secondary working-dir-btn"
                  disabled={!instruction.trim()}
                  onClick={() => {
                    onSend('slop-regenerate-prs', entry.stem, { template: dirty ? template : '', instruction })
                    setInstruction('')
                    setDirty(false)
                  }}
                >
                  Regenerate
                </button>
              </div>
            </>
          )}
        </div>
      )}

      {(entry.phase === 'pr_open' || entry.phase === 'merged') && prs.length > 0 && (
        <div className="slop-prs">
          <h3>Pull Requests</h3>
          <ul className="slop-pr-list">
            {prs.map((p) => (
              <li key={`${p.repo}#${p.number}`}>
                <a href={p.url} target="_blank" rel="noreferrer">{p.repo}#{p.number}</a>
                <span className={`slop-pr-status ${p.state === 'merged' || p.ready ? 'slop-good' : 'slop-bad'}`}>
                  {p.state === 'merged' ? 'merged' : p.status}
                </span>
              </li>
            ))}
          </ul>
          {entry.phase === 'pr_open' && held && (
            <button
              className="btn-primary working-dir-btn"
              disabled={!allReady}
              title={allReady ? 'Merge every PR, targets first and the condoc repo last' : 'Every PR has to be mergeable first'}
              onClick={() => onSend('slop-merge', entry.stem)}
            >
              Merge
            </button>
          )}
        </div>
      )}
    </div>
  )
}

// SlopspacePointerView is the slopspace tab's main pane for a selected
// pointer: a proposal's form with Accept/Withdraw, or the pointer's panel
// with a way into its condoc views.
function SlopspacePointerView({
  pointer,
  entry,
  branchCheck,
  onPropose,
  onCheckBranch,
  onSend,
  onOpen,
}: {
  pointer: PointerStatus
  entry: PointerEntry | null
  branchCheck: { branch: string; checks: BranchCheck[] } | null
  onPropose: (p: SlopspaceProposal) => void
  onCheckBranch: (repos: string[], branch: string) => void
  onSend: SlopSend
  onOpen: (path: string) => void
}) {
  if (!entry) {
    return (
      <div className="slop-view">
        <h2>Propose a condoc</h2>
        <ProposalForm pointer={pointer} entry={null} branchCheck={branchCheck} onPropose={onPropose} onCheckBranch={onCheckBranch} />
      </div>
    )
  }
  if (entry.phase === 'proposed') {
    return (
      <div className="slop-view">
        <h2>{entry.name} <PhaseBadge phase="proposed" /></h2>
        {entry.heldHere ? (
          <>
            <ProposalForm pointer={pointer} entry={entry} branchCheck={branchCheck} onPropose={onPropose} onCheckBranch={onCheckBranch} />
            {entry.error && <div className="slop-error">{entry.error}</div>}
            <div className="pointer-actions">
              <button className="btn-primary working-dir-btn" onClick={() => onSend('slop-accept', entry.stem)}>
                Accept — create the slopspace
              </button>
              <button className="btn-secondary working-dir-btn" onClick={() => onSend('slop-withdraw', entry.stem)}>
                Withdraw
              </button>
            </div>
          </>
        ) : (
          <PointerDetails entry={entry} />
        )}
      </div>
    )
  }
  return (
    <div className="slop-view">
      <h2>{entry.name}</h2>
      <SlopspacePanel entry={entry} onSend={onSend} />
      {entry.slopspaceId && entry.heldHere && entry.phase !== 'merged' && (
        <div className="pointer-actions">
          <button className="btn-primary working-dir-btn" onClick={() => onOpen(entry.path)}>Open Condoc</button>
        </div>
      )}
    </div>
  )
}

// ---- Root app ----

export default function App() {
  const {
    connected,
    condocs,
    activeState,
    error,
    subscribeError,
    subscribe,
    sendAction,
    setError,
    reprStatus,
    reprHost,
    reprPort,
    reprAutoConnect,
    reprHTTPPort,
    tcAvailable,
    devMode,
    version,
    modeMismatch,
    root,
    rootGen,
    setWorkingDir,
    pointer,
    setPointerRepo,
    clonePointerRepo,
    branchCheck,
    slopPropose,
    slopCheckBranch,
    slopSend,
    connectRepr,
    disconnectRepr,
    setAutoConnectRepr,
    getDiff,
    getFileDiff,
    diffFiles,
    setDiffFiles,
    diffFilesLoaded,
    setDiffFilesLoaded,
    fileDiffContent,
    setFileDiffContent,
    fileDiffHunks,
    setFileDiffHunks,
  } = useCondocWS()

  // Seed nav state from the URL hash on mount so a refresh (or an LR/AC
  // iframe remount replaying a captured hash) resumes where it left off --
  // ref so this only ever runs once, not on every render.
  const initialNav = useRef(navFromHash(window.location.hash)).current

  const [navLevel, setNavLevel] = useState<NavLevel>(initialNav.navLevel)
  const [tab, setTab] = useState<ControlSchemeTab>(initialNav.tab)
  const [hideCompleted, setHideCompleted] = useState(loadHideCompleted)
  const [selectedCondocPath, setSelectedCondocPath] = useState<string | null>(initialNav.condocPath)
  const [selectedStepNum, setSelectedStepNum] = useState<number | null>(initialNav.stepNum)
  const [selectedIterId, setSelectedIterId] = useState<string | null>(initialNav.iterId)
  // Which substep letter is being viewed -- distinct from
  // activeState.info.substepLetter (the currently *active* one), since
  // entering an already-completed substep via the step's sidebar/detail view
  // still needs to know which one to look up in completedSubstepContents.
  const [selectedSubstepLetter, setSelectedSubstepLetter] = useState<string | null>(null)
  const [selectedSubstepIterId, setSelectedSubstepIterId] = useState<string | null>(initialNav.substepIterId)
  const [diffFromCommit, setDiffFromCommit] = useState<string | null>(initialNav.diffFromCommit)
  const [diffToCommit, setDiffToCommit] = useState<string | null>(initialNav.diffToCommit)
  const [selectedDiffFile, setSelectedDiffFile] = useState<string | null>(initialNav.selectedDiffFile)
  const [selectedDiffHunkIdx, setSelectedDiffHunkIdx] = useState<number | null>(initialNav.selectedDiffHunkIdx)
  const [diffReturnLevel, setDiffReturnLevel] = useState<'step' | 'substep'>(initialNav.diffReturnLevel)
  // The slopspace tab's list-level selection: a pointer stem, '' for the
  // new-proposal form, null for nothing.
  const [selectedPointerStem, setSelectedPointerStem] = useState<string | null>(null)
  const selectedPointer = pointer?.pointers.find((p) => p.stem === selectedPointerStem) ?? null

  // Mobile nav drawer: the sidebar becomes an off-canvas panel below the
  // `mobile-breakpoint` width (see index.css). Desktop layout is untouched —
  // this state simply has no visible effect above the breakpoint.
  const [mobileNavOpen, setMobileNavOpen] = useState(false)

  const tcCapture = useTCCapture(tcAvailable, reprHost, reprHTTPPort)

  const handleSetHideCompleted = (hide: boolean) => {
    setHideCompleted(hide)
    saveHideCompleted(hide)
  }

  const handleSelectCondoc = (path: string) => {
    setSelectedCondocPath(path)
    setSelectedStepNum(null)
    setSelectedIterId(null)
    setSelectedSubstepLetter(null)
    setSelectedSubstepIterId(null)
    setNavLevel('condoc')
    subscribe(path)
    setError(null)
  }

  // An accepted condoc this condoccer holds opens straight into the condoc
  // views; anything else (a proposal, another holder's, a merged one) opens
  // its pointer in the main pane.
  const handleSelectPointer = (p: PointerEntry | null) => {
    setError(null)
    if (p && p.heldHere && p.slopspaceId && p.phase !== 'proposed' && p.phase !== 'merged') {
      setSelectedPointerStem(p.stem)
      handleSelectCondoc(p.path)
      return
    }
    setSelectedPointerStem(p ? p.stem : '')
  }

  const handleSlopPropose = (p: SlopspaceProposal) => {
    setError(null)
    slopPropose(p)
  }

  const handleSlopSend: SlopSend = (type, stem, extra) => {
    setError(null)
    if (type === 'slop-withdraw') setSelectedPointerStem(null)
    slopSend(type, stem, extra)
  }

  const handleSelectStep = (num: number) => {
    setSelectedStepNum(num)
    setSelectedIterId(null)
    setSelectedSubstepLetter(null)
    setSelectedSubstepIterId(null)
    setNavLevel('step')
  }

  const handleSelectIter = (id: string) => {
    setSelectedIterId(id)
  }

  const handleEnterSubstep = (substepLetter: string) => {
    setSelectedSubstepLetter(substepLetter)
    setSelectedSubstepIterId(null)
    setNavLevel('substep')
  }

  const handleSelectSubstepIter = (id: string) => {
    setSelectedSubstepIterId(id)
  }

  const handleEnterFilesChanged = (fromCommit: string, toCommit: string) => {
    setDiffReturnLevel(navLevel as 'step' | 'substep')
    setDiffFromCommit(fromCommit)
    setDiffToCommit(toCommit)
    setDiffFiles([])
    setDiffFilesLoaded(false)
    setSelectedDiffFile(null)
    setFileDiffContent(null)
    setFileDiffHunks([])
    setSelectedDiffHunkIdx(null)
    setNavLevel('files-changed')
    getDiff(selectedCondocPath ?? '', fromCommit, toCommit)
  }

  const handleSelectDiffFile = (file: string) => {
    setSelectedDiffFile(file)
    setFileDiffContent(null)
    setFileDiffHunks([])
    setSelectedDiffHunkIdx(null)
    if (diffFromCommit !== null) {
      getFileDiff(selectedCondocPath ?? '', diffFromCommit, diffToCommit ?? '', file)
    }
    setNavLevel('file-diff')
  }

  const handleSelectDiffHunk = (idx: number) => {
    setSelectedDiffHunkIdx(idx)
  }

  const handleNavUp = () => {
    if (navLevel === 'file-diff') {
      setNavLevel('files-changed')
      setSelectedDiffHunkIdx(null)
    } else if (navLevel === 'files-changed') {
      setNavLevel(diffReturnLevel)
      setDiffFromCommit(null)
      setDiffToCommit(null)
      setDiffFiles([])
      setDiffFilesLoaded(false)
      setSelectedDiffFile(null)
      setFileDiffContent(null)
      setFileDiffHunks([])
      setSelectedDiffHunkIdx(null)
    } else if (navLevel === 'substep') {
      setNavLevel('step')
      setSelectedSubstepLetter(null)
      setSelectedSubstepIterId(null)
    } else if (navLevel === 'step') {
      setNavLevel('condoc')
      setSelectedStepNum(null)
      setSelectedIterId(null)
      setSelectedSubstepIterId(null)
    } else if (navLevel === 'condoc') {
      setNavLevel('condoc-list')
      setSelectedCondocPath(null)
      setSelectedIterId(null)
      setSelectedSubstepIterId(null)
    }
  }

  const handleAction = (action: ActionRequest) => {
    setError(null)
    sendAction(action)
    if (action.action === 'start_step' && activeState !== null) {
      handleSelectStep(activeState.info.stepNum)
    } else if (action.action === 'completed') {
      if (navLevel === 'substep') {
        // After completing a substep, go back to the step view.
        setNavLevel('step')
        setSelectedSubstepLetter(null)
        setSelectedSubstepIterId(null)
      } else if (navLevel === 'step') {
        setNavLevel('condoc')
        setSelectedStepNum(null)
        setSelectedIterId(null)
      }
    } else if (action.action === 'revert') {
      // After reverting, go up a level — the federation-command will reset state.
      if (navLevel === 'substep') {
        setNavLevel('step')
        setSelectedSubstepLetter(null)
        setSelectedSubstepIterId(null)
      } else if (navLevel === 'step') {
        setNavLevel('condoc')
        setSelectedStepNum(null)
        setSelectedIterId(null)
      }
    }
  }

  // Mirror nav state into the URL hash with replaceState -- not pushState,
  // so this stays a pure resume mechanism and doesn't add a history entry
  // per click (back-button support could be a deliberate follow-up).
  useEffect(() => {
    const hash = hashFromNav({
      navLevel,
      tab,
      condocPath: selectedCondocPath,
      stepNum: selectedStepNum,
      iterId: selectedIterId,
      substepIterId: selectedSubstepIterId,
      diffReturnLevel,
      diffFromCommit,
      diffToCommit,
      selectedDiffFile,
      selectedDiffHunkIdx,
    })
    if (hash === window.location.hash || (hash === '' && window.location.hash === '')) return
    const url = hash || window.location.pathname + window.location.search
    const oldURL = window.location.href
    history.replaceState(null, '', url)
    // history.replaceState never fires a 'hashchange' event (unlike setting
    // location.hash directly), so an embedder that resumes condoccer's nav
    // state by listening for that event on this window -- e.g. agent-
    // coordinator's iframe, see BrowserPickupStrategy.md -- would otherwise
    // only ever see the hash as of the iframe's initial load and never learn
    // about later in-app navigation, sending a subsequent AC-page reload
    // back to that stale spot instead of wherever the user actually was.
    // Dispatch one by hand so same-origin listeners stay in sync.
    window.dispatchEvent(new HashChangeEvent('hashchange', { oldURL, newURL: window.location.href }))
  }, [
    navLevel, tab, selectedCondocPath, selectedStepNum, selectedIterId, selectedSubstepIterId,
    diffReturnLevel, diffFromCommit, diffToCommit, selectedDiffFile, selectedDiffHunkIdx,
  ])

  // Catch up once connected: state seeded from the hash at mount was never
  // triggered by a click, so (re-)issue exactly the requests a click would
  // have made -- keyed off the frozen `initialNav` snapshot, not the live
  // state, so this can't also fire (redundantly, alongside the handlers'
  // own calls) the first time a normal click sets the same state later.
  const hashSubscribeDone = useRef(false)
  useEffect(() => {
    if (connected && initialNav.condocPath && !hashSubscribeDone.current) {
      hashSubscribeDone.current = true
      subscribe(initialNav.condocPath)
    }
  }, [connected, initialNav.condocPath, subscribe])

  const hashDiffCatchupDone = useRef(false)
  useEffect(() => {
    if (connected && initialNav.diffFromCommit !== null && !hashDiffCatchupDone.current) {
      hashDiffCatchupDone.current = true
      getDiff(initialNav.condocPath ?? '', initialNav.diffFromCommit, initialNav.diffToCommit ?? '')
    }
  }, [connected, initialNav.condocPath, initialNav.diffFromCommit, initialNav.diffToCommit, getDiff])

  const hashFileDiffCatchupDone = useRef(false)
  useEffect(() => {
    if (connected && initialNav.diffFromCommit !== null && initialNav.selectedDiffFile && !hashFileDiffCatchupDone.current) {
      hashFileDiffCatchupDone.current = true
      getFileDiff(initialNav.condocPath ?? '', initialNav.diffFromCommit, initialNav.diffToCommit ?? '', initialNav.selectedDiffFile)
    }
  }, [connected, initialNav.condocPath, initialNav.diffFromCommit, initialNav.diffToCommit, initialNav.selectedDiffFile, getFileDiff])

  // Working dir changed (Set Working Dir, from this tab or another): every
  // condoc path is relative to it, so whatever we were looking at is gone --
  // return to the list, which also clears the now-stale hash.
  useEffect(() => {
    if (rootGen === 0) return
    setNavLevel('condoc-list')
    setSelectedCondocPath(null)
    setSelectedStepNum(null)
    setSelectedIterId(null)
    setSelectedSubstepLetter(null)
    setSelectedSubstepIterId(null)
    setDiffFromCommit(null)
    setDiffToCommit(null)
    setSelectedDiffFile(null)
    setSelectedDiffHunkIdx(null)
  }, [rootGen])

  // Staleness: a hash can point at a condoc that's since been renamed,
  // reverted away, or deleted. If we're anywhere but the list and never got
  // a subscribe response before the *server* explicitly rejected it, fall
  // back to the list (which also clears the now-stale hash via the effect
  // above) instead of sitting on a dead deep link.
  //
  // This deliberately checks `subscribeError`, not the generic `error` --
  // `error` also gets set by transport-level ws.onerror hiccups, which fire
  // routinely while reconnecting (e.g. every restart the browser-refresh and
  // auto-update mechanisms trigger, while the server is still coming back
  // up). Falling back on that would kick a perfectly-valid hash-restored
  // condoc back to the list the moment a restart's reconnect flaked, which
  // is exactly the "brought back to the main page" regression this guards
  // against.
  useEffect(() => {
    if (subscribeError && activeState === null && navLevel !== 'condoc-list') {
      setNavLevel('condoc-list')
      setSelectedCondocPath(null)
      setSelectedStepNum(null)
      setSelectedIterId(null)
      setSelectedSubstepIterId(null)
      setDiffFromCommit(null)
      setDiffToCommit(null)
      setSelectedDiffFile(null)
      setSelectedDiffHunkIdx(null)
    }
  }, [subscribeError, activeState, navLevel])

  return (
    <TCCaptureContext.Provider value={tcCapture}>
    <div className={`app${devMode ? ' app-dev-mode' : ''}`}>
      {tcCapture.captureURL && <TCCaptureOverlay url={tcCapture.captureURL} onCancel={tcCapture.cancel} />}
      {modeMismatch && (
        <div className="mode-mismatch-banner">
          ⚠ dev/ops mode mismatch with local-representative ({modeMismatch.peer_mode}):
          only health information is exchanged until this is resolved. See docs/DevMode.md.
        </div>
      )}
      <button
        className="mobile-nav-toggle"
        aria-label="Open navigation"
        onClick={() => setMobileNavOpen(true)}
      >
        ☰
      </button>

      {/* Direct one-tap "go up" for mobile -- without it the only way back is
          opening the full drawer and finding nav-up-btn inside it. Hidden at
          the top level (condoc-list), same as nav-up-btn's own visibility. */}
      {navLevel !== 'condoc-list' && (
        <button
          className="mobile-back-btn"
          aria-label="Back"
          onClick={() => { setMobileNavOpen(false); handleNavUp() }}
        >
          ‹
        </button>
      )}

      {mobileNavOpen && (
        <div className="mobile-nav-backdrop" onClick={() => setMobileNavOpen(false)} />
      )}

      {/* Closes the mobile drawer on nav-item selection without touching the
          individual handlers below — a no-op on desktop widths. Ignores
          clicks on the repr-connect form so typing a host/port doesn't
          dismiss the drawer mid-edit. */}
      <div
        className={`sidebar-wrap${mobileNavOpen ? ' mobile-open' : ''}`}
        onClickCapture={(e) => {
          if ((e.target as HTMLElement).closest('.nav-item, .nav-up-btn')) {
            setMobileNavOpen(false)
          }
        }}
      >
        <Sidebar
          navLevel={navLevel}
          tab={tab}
          onSelectTab={setTab}
          hideCompleted={hideCompleted}
          onSetHideCompleted={handleSetHideCompleted}
          pointer={pointer}
          selectedPointerStem={selectedPointerStem}
          onSelectPointer={handleSelectPointer}
          condocs={condocs}
          activeState={activeState}
          selectedCondocPath={selectedCondocPath}
          selectedStepNum={selectedStepNum}
          selectedIterId={selectedIterId}
          selectedSubstepLetter={selectedSubstepLetter}
          selectedSubstepIterId={selectedSubstepIterId}
          diffFiles={diffFiles}
          diffFilesLoaded={diffFilesLoaded}
          selectedDiffFile={selectedDiffFile}
          fileDiffHunks={fileDiffHunks}
          selectedDiffHunkIdx={selectedDiffHunkIdx}
          diffReturnLevel={diffReturnLevel}
          onSelectCondoc={handleSelectCondoc}
          onSelectStep={handleSelectStep}
          onSelectIter={handleSelectIter}
          onEnterSubstep={handleEnterSubstep}
          onSelectSubstepIter={handleSelectSubstepIter}
          onEnterFilesChanged={handleEnterFilesChanged}
          onSelectDiffFile={handleSelectDiffFile}
          onSelectDiffHunk={handleSelectDiffHunk}
          onNavUp={handleNavUp}
          reprStatus={reprStatus}
          reprHost={reprHost}
          reprPort={reprPort}
          reprAutoConnect={reprAutoConnect}
          onReprConnect={connectRepr}
          onReprDisconnect={disconnectRepr}
          onReprSetAutoConnect={setAutoConnectRepr}
          version={version}
        />
      </div>

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

        {navLevel === 'condoc-list' && tab === 'slopspace' && connected && pointer?.state === 'ready' &&
          selectedPointerStem !== null && (selectedPointerStem === '' || selectedPointer) && (
          <SlopspacePointerView
            pointer={pointer}
            entry={selectedPointer}
            branchCheck={branchCheck}
            onPropose={handleSlopPropose}
            onCheckBranch={slopCheckBranch}
            onSend={handleSlopSend}
            onOpen={handleSelectCondoc}
          />
        )}

        {navLevel === 'condoc-list' && tab === 'slopspace' && (
          <div className="empty-state empty-state-list">
            <div>
              <span className={`conn-dot ${connected ? 'connected' : 'disconnected'}`} />
              {!connected
                ? 'Connecting…'
                : pointer?.state === 'ready'
                  ? 'Select a slopspace condoc, or propose one'
                  : 'Set up the condoc pointer repo to use slopspace condocs'}
            </div>
            {connected && pointer && (
              <PointerRepoControl
                pointer={pointer}
                onSet={(cfg) => { setError(null); setPointerRepo(cfg) }}
                onClone={() => { setError(null); clonePointerRepo() }}
              />
            )}
          </div>
        )}

        {navLevel !== 'condoc-list' && activeState?.slopspace && (
          <SlopspacePanel entry={activeState.slopspace} onSend={handleSlopSend} />
        )}

        {navLevel === 'condoc-list' && tab === 'same-repo' && (
          <div className="empty-state empty-state-list">
            <div>
              <span className={`conn-dot ${connected ? 'connected' : 'disconnected'}`} />
              {connected
                ? condocs.length === 0
                  ? 'No condocs found — set the working dir to a directory containing condoc files'
                  : 'Select a condoc'
                : 'Connecting…'}
            </div>
            {connected && <WorkingDirControl root={root} onSet={(dir) => { setError(null); setWorkingDir(dir) }} />}
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
            onEnterSubstep={handleEnterSubstep}
          />
        )}

        {navLevel === 'substep' && activeState && (
          <SubstepDetailView
            state={activeState}
            substepLetter={selectedSubstepLetter ?? activeState.info.substepLetter ?? ''}
            selectedSubstepIterId={selectedSubstepIterId}
            onAction={handleAction}
          />
        )}

        {navLevel === 'files-changed' && (
          <FilesChangedView
            files={diffFiles}
            filesLoaded={diffFilesLoaded}
            selectedFile={selectedDiffFile}
          />
        )}

        {navLevel === 'file-diff' && (
          <FileDiffView
            file={selectedDiffFile}
            content={fileDiffContent}
            selectedHunkIdx={selectedDiffHunkIdx}
          />
        )}
      </div>
    </div>
    </TCCaptureContext.Provider>
  )
}
