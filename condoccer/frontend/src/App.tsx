import { useCallback, useEffect, useRef, useState } from 'react'
import type { ActionRequest, CondocInfo, CondocMeta, CondocState, Iteration, ModeMismatchMsg, Phase, ReprStatus, ReprStatusMsg, SelfInfoMsg, StepSummary } from './types'

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
  const [reprStatus, setReprStatus] = useState<ReprStatus>('disconnected')
  const [reprHost, setReprHost] = useState('')
  const [reprPort, setReprPort] = useState('')
  const [reprAutoConnect, setReprAutoConnect] = useState(false)
  const [devMode, setDevMode] = useState(false)
  const [version, setVersion] = useState('')
  const [modeMismatch, setModeMismatch] = useState<ModeMismatchMsg | null>(null)
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

  const getDiff = useCallback(
    (fromCommit: string, toCommit: string) => {
      setDiffFilesLoaded(false)
      send('get-diff', { fromCommit, toCommit })
    },
    [send],
  )

  const getFileDiff = useCallback(
    (fromCommit: string, toCommit: string, file: string) => {
      send('get-file-diff', { fromCommit, toCommit, file })
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
          } else if (msg.type === 'error') {
            const p = msg.payload as { message: string }
            setError(p.message)
          } else if (msg.type === 'repr-status') {
            const p = msg.payload as ReprStatusMsg
            setReprStatus(p.status)
            if (p.host) setReprHost(p.host)
            if (p.port) setReprPort(p.port)
            setReprAutoConnect(!!p.auto_connect)
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
    subscribe,
    sendAction,
    setError,
    reprStatus,
    reprHost,
    reprPort,
    reprAutoConnect,
    devMode,
    version,
    modeMismatch,
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

// ---- Navigation ----

type NavLevel = 'condoc-list' | 'condoc' | 'step' | 'substep' | 'files-changed' | 'file-diff'

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
//   #/condoc/<enc(path)>
//   #/condoc/<enc(path)>/step/<num>[/iter/<id>]
//   #/condoc/<enc(path)>/step/<num>/substep[/iter/<id>]
//   #/condoc/<enc(path)>/step/<num>[/substep]/files/<from>..<to>[/file/<enc(file)>[/hunk/<idx>]]

interface NavHashState {
  navLevel: NavLevel
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
  if (s.navLevel === 'condoc-list' || !s.condocPath) return ''
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
    h += `/files/${s.diffFromCommit}..${s.diffToCommit ?? ''}`
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
    result.diffFromCommit = range.slice(0, sep)
    result.diffToCommit = range.slice(sep + 2)
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
}

function PhaseBadge({ phase }: { phase: Phase }) {
  return <span className={`badge badge-${phase}`}>{PHASE_LABELS[phase]}</span>
}

// ---- Step section parser ----

interface StepSection {
  id: string
  label: string
  kind: 'prompt' | 'reply' | 'revision' | 'retry' | 'substep'
  content: string
  substepLetter?: string
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
    fullMatch: string
  }
  const headings: Heading[] = []
  const reMain = /^## (Reply|Revision|Retry|Human-Prompt)(?: ([A-Z]))?(?: \(from (\w+)\))?/gm
  const reSubstep = /^## Substep ([A-Z]) - (.+)/gm
  reMain.lastIndex = 0
  reSubstep.lastIndex = 0

  let m: RegExpExecArray | null
  while ((m = reMain.exec(content)) !== null) {
    headings.push({ index: m.index, kind: m[1], letter: m[2] ?? '', from: m[3] ?? '', substepTitle: '', fullMatch: m[0] })
  }
  while ((m = reSubstep.exec(content)) !== null) {
    headings.push({ index: m.index, kind: 'Substep', letter: m[1], from: '', substepTitle: m[2].trim(), fullMatch: m[0] })
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
    } else {
      // Substep
      id = `substep-${h.letter}`
      label = `Substep ${h.letter} — ${h.substepTitle}`
      kind = 'substep'
      substepLetter = h.letter
    }

    sections.push({ id, label, kind, content: cleaned, substepLetter })
  }

  return sections
}

function sectionsToIterations(sections: StepSection[]): Iteration[] {
  return sections
    .filter((s) => s.kind !== 'prompt')
    .map((s) => ({ id: s.id, label: s.label, type: s.kind as Iteration['type'] }))
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
  condocs: CondocInfo[]
  activeState: CondocState | null
  selectedCondocPath: string | null
  selectedStepNum: number | null
  selectedIterId: string | null
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
  condocs,
  activeState,
  selectedCondocPath,
  selectedStepNum,
  selectedIterId,
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
    return (
      <div className="sidebar">
        <div className="sidebar-header">
          <h1>Condoccer{version && <span className="app-version-tag">{version}</span>}</h1>
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
    const commitRanges = parseCommitRanges(stepContent)

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
                <span className={`nav-iter-dot iter-${iter.type}`} />
                <span className="nav-item-name">{iter.label}</span>
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
    const substepIterations: Iteration[] = activeState.substepIterations ?? []
    const substepLetter = activeState.info.substepLetter ?? ''
    const substepCommitRanges = parseCommitRanges(activeState.substepContent ?? '')

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
                <span className={`nav-iter-dot iter-${iter.type}`} />
                <span className="nav-item-name">{iter.label}</span>
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
      ? `↑ Substep ${activeState?.info.substepLetter ?? ''}`
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
          {diffFiles.map((file) => (
            <div
              key={file}
              className={`nav-item${selectedDiffFile === file ? ' selected' : ''}`}
              onClick={() => onSelectDiffFile(file)}
            >
              <span className="nav-item-name nav-item-file">{file.split('/').pop() ?? file}</span>
            </div>
          ))}
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
  onRevert: (stepNum: number, iterLetter?: string) => void
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
      <div className="step-card-header" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span>Step {step.num} — <span className="step-card-title">{step.title}</span></span>
        {isCompleted && !revertOpen && (
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
                onRevert(step.num, revertIter || undefined)
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
                  onRevert={(stepNum, iterLetter) => onAction({ action: 'revert', path: info.path, revertStep: stepNum, revertIter: iterLetter })}
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

// ---- Action panel (for step or substep view) ----

type ActionMode = null | 'revision' | 'retry' | 'revert' | 'substep'

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

  useEffect(() => {
    setMode(null)
    setPromptText('')
    setFromSel('start')
    setRevertIter('')
    setSubstepTitle('')
  }, [info.path, info.stepNum, info.substepLetter])

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
          <button className="btn-secondary" onClick={() => setMode('retry')}>
            Retry {nextLetter}…
          </button>
          {!isSubstep && (
            <button className="btn-primary" onClick={() => setMode('substep')}>
              Substep {nextLetter}…
            </button>
          )}
          <button className="btn-danger" onClick={() => setMode('revert')}>
            Revert↩
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

      {mode === 'substep' && (
        <div className="action-form">
          <div className="action-form-title">Substep {nextLetter}</div>
          <input
            className="step-form-input"
            type="text"
            placeholder="Substep title…"
            value={substepTitle}
            onChange={(e) => setSubstepTitle(e.target.value)}
          />
          <textarea
            placeholder="Describe what the substep should accomplish…"
            value={promptText}
            onChange={(e) => setPromptText(e.target.value)}
            rows={4}
          />
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
    </div>
  )
}

// ---- Substep detail view ----

interface SubstepDetailViewProps {
  state: CondocState
  selectedSubstepIterId: string | null
  onAction: (action: ActionRequest) => void
}

function SubstepDetailView({ state, selectedSubstepIterId, onAction }: SubstepDetailViewProps) {
  const sectionRefs = useRef<Record<string, HTMLDivElement | null>>({})
  const substepLetter = state.info.substepLetter ?? ''
  const content = state.substepContent ?? ''

  useEffect(() => {
    if (selectedSubstepIterId && sectionRefs.current[selectedSubstepIterId]) {
      sectionRefs.current[selectedSubstepIterId]?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    }
  }, [selectedSubstepIterId])

  const sections = parseStepSections(content)

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
            className={`iter-section iter-section-${sec.kind}${selectedSubstepIterId === sec.id ? ' iter-section-selected' : ''}`}
            ref={(el) => { sectionRefs.current[sec.id] = el }}
          >
            <div className="iter-section-label">{sec.label}</div>
            <div className="iter-section-content">{sec.content}</div>
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
                  className={`iter-section iter-section-${sec.kind}${selectedIterId === sec.id ? ' iter-section-selected' : ''}`}
                  ref={(el) => { sectionRefs.current[sec.id] = el }}
                >
                  <div className="iter-section-label">{sec.label}</div>
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
                    <div className="iter-section-content">{sec.content}</div>
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
            className={`iter-section iter-section-${sec.kind}${selectedIterId === sec.id ? ' iter-section-selected' : ''}`}
            ref={(el) => { sectionRefs.current[sec.id] = el }}
            onDoubleClick={() => sec.kind === 'substep' && sec.substepLetter && onEnterSubstep(sec.substepLetter)}
          >
            <div className="iter-section-label" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <span>{sec.label}</span>
              {sec.kind === 'substep' && sec.substepLetter && (
                <button
                  className="btn-secondary"
                  style={{ fontSize: 11, padding: '2px 8px' }}
                  onClick={() => onEnterSubstep(sec.substepLetter!)}
                >
                  Enter →
                </button>
              )}
            </div>
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
  const {
    connected,
    condocs,
    activeState,
    error,
    subscribe,
    sendAction,
    setError,
    reprStatus,
    reprHost,
    reprPort,
    reprAutoConnect,
    devMode,
    version,
    modeMismatch,
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
  const [selectedCondocPath, setSelectedCondocPath] = useState<string | null>(initialNav.condocPath)
  const [selectedStepNum, setSelectedStepNum] = useState<number | null>(initialNav.stepNum)
  const [selectedIterId, setSelectedIterId] = useState<string | null>(initialNav.iterId)
  const [selectedSubstepIterId, setSelectedSubstepIterId] = useState<string | null>(initialNav.substepIterId)
  const [diffFromCommit, setDiffFromCommit] = useState<string | null>(initialNav.diffFromCommit)
  const [diffToCommit, setDiffToCommit] = useState<string | null>(initialNav.diffToCommit)
  const [selectedDiffFile, setSelectedDiffFile] = useState<string | null>(initialNav.selectedDiffFile)
  const [selectedDiffHunkIdx, setSelectedDiffHunkIdx] = useState<number | null>(initialNav.selectedDiffHunkIdx)
  const [diffReturnLevel, setDiffReturnLevel] = useState<'step' | 'substep'>(initialNav.diffReturnLevel)

  // Mobile nav drawer: the sidebar becomes an off-canvas panel below the
  // `mobile-breakpoint` width (see index.css). Desktop layout is untouched —
  // this state simply has no visible effect above the breakpoint.
  const [mobileNavOpen, setMobileNavOpen] = useState(false)

  const handleSelectCondoc = (path: string) => {
    setSelectedCondocPath(path)
    setSelectedStepNum(null)
    setSelectedIterId(null)
    setSelectedSubstepIterId(null)
    setNavLevel('condoc')
    subscribe(path)
    setError(null)
  }

  const handleSelectStep = (num: number) => {
    setSelectedStepNum(num)
    setSelectedIterId(null)
    setSelectedSubstepIterId(null)
    setNavLevel('step')
  }

  const handleSelectIter = (id: string) => {
    setSelectedIterId(id)
  }

  const handleEnterSubstep = (_substepLetter: string) => {
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
    getDiff(fromCommit, toCommit)
  }

  const handleSelectDiffFile = (file: string) => {
    setSelectedDiffFile(file)
    setFileDiffContent(null)
    setFileDiffHunks([])
    setSelectedDiffHunkIdx(null)
    if (diffFromCommit !== null) {
      getFileDiff(diffFromCommit, diffToCommit ?? '', file)
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
    history.replaceState(null, '', url)
  }, [
    navLevel, selectedCondocPath, selectedStepNum, selectedIterId, selectedSubstepIterId,
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
      getDiff(initialNav.diffFromCommit, initialNav.diffToCommit ?? '')
    }
  }, [connected, initialNav.diffFromCommit, initialNav.diffToCommit, getDiff])

  const hashFileDiffCatchupDone = useRef(false)
  useEffect(() => {
    if (connected && initialNav.diffFromCommit !== null && initialNav.selectedDiffFile && !hashFileDiffCatchupDone.current) {
      hashFileDiffCatchupDone.current = true
      getFileDiff(initialNav.diffFromCommit, initialNav.diffToCommit ?? '', initialNav.selectedDiffFile)
    }
  }, [connected, initialNav.diffFromCommit, initialNav.diffToCommit, initialNav.selectedDiffFile, getFileDiff])

  // Staleness: a hash can point at a condoc that's since been renamed,
  // reverted away, or deleted. If we're anywhere but the list and never got
  // a subscribe response before an error came in, fall back to the list
  // (which also clears the now-stale hash via the effect above) instead of
  // sitting on a dead deep link.
  useEffect(() => {
    if (error && activeState === null && navLevel !== 'condoc-list') {
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
  }, [error, activeState, navLevel])

  return (
    <div className={`app${devMode ? ' app-dev-mode' : ''}`}>
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
          condocs={condocs}
          activeState={activeState}
          selectedCondocPath={selectedCondocPath}
          selectedStepNum={selectedStepNum}
          selectedIterId={selectedIterId}
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
            onEnterSubstep={handleEnterSubstep}
          />
        )}

        {navLevel === 'substep' && activeState && (
          <SubstepDetailView
            state={activeState}
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
  )
}
