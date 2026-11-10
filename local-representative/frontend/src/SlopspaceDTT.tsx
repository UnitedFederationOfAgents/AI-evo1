import { useEffect, useState } from 'react'
import type { ProposalDecision, SlopspaceFileListing, SlopspaceInfo, WorkerSlopspaceReply, WorkerSlopspaceRequest } from './types'

// The worker tab's slopspace sub-tab additions for DTTs (dTT-IntegrationImpls
// Step 1, Revision H): reviewing the DTT proposals a returned slopspace left
// in writespaces/dtt-requests, and viewing a slopspace's files. Both go
// through the manual control ops ("review", "files") that this host's
// dungeon-keeper runs -- see dungeon-keeper/slopctl.go and
// pkg/slopspace/proposals.go.
//
// Mirrored in agent-coordinator/frontend/src/SlopspaceDTT.tsx -- keep the
// two in step.

// ProposalReview answers a returned slopspace's pending DTT proposals. Until
// they're answered it can't be deployed again. accept moves them onto its
// DTT canvas (a readspace); reject sets them aside; request modification
// deploys it again for this worker's agent with the note as its task.
// Clicking a proposal enters it in place of the list (Revision L); ↑ comes
// back out, and the decision buttons stay below either way.
//
// Each proposal can also be read, accepted or rejected on its own (Revision
// M): its row has ✓ / ✗, and so does the proposal once entered. The rest
// stay pending -- and the slopspace held -- until they're answered too.
export function ProposalReview({
  slopspace: s,
  disabled,
  workerAgentType,
  healthy,
  reply,
  onSend,
  onControl,
}: {
  slopspace: SlopspaceInfo
  disabled: boolean
  workerAgentType: string
  healthy: boolean
  reply: WorkerSlopspaceReply | null
  onSend: (req: WorkerSlopspaceRequest) => void
  onControl: (req: WorkerSlopspaceRequest) => void
}) {
  const [note, setNote] = useState('')
  const [viewing, setViewing] = useState<string | null>(null)
  useEffect(() => setNote(''), [s.id])
  const proposals = s.pending_proposals ?? []
  useEffect(() => setViewing(null), [s.id, proposals.join('\n')])
  if (proposals.length === 0) return null
  const total = proposals.length + (s.proposals_omitted ?? 0)
  const all = total === 1 ? '' : ' all'
  const decide = (decision: ProposalDecision) => {
    if (decision === 'reject' && !window.confirm(`Reject ${total} DTT proposal${total === 1 ? '' : 's'} from slopspace ${s.id.slice(0, 8)}?`)) return
    onSend({ op: 'review', id: s.id, decision, note: note.trim() || undefined })
  }
  // One proposal on its own; the list catches up with the next worker-state.
  const decideOne = (p: string, decision: 'accept' | 'reject') => {
    if (decision === 'reject' && !window.confirm(`Reject the DTT proposal ${p}?`)) return
    onSend({ op: 'review', id: s.id, decision, note: note.trim() || undefined, proposals: [p] })
  }
  const oneButtons = (p: string, words: boolean) => (
    <span className="worker-dtt-one">
      <button className="worker-submit-btn worker-dtt-accept" disabled={disabled} title={`accept ${p} onto the DTT canvas`} onClick={() => decideOne(p, 'accept')}>✓{words ? ' accept this → canvas' : ''}</button>
      <button className="worker-submit-btn worker-slopctl-danger" disabled={disabled} title={`reject ${p}`} onClick={() => decideOne(p, 'reject')}>✗{words ? ' reject this' : ''}</button>
    </span>
  )
  return (
    <div className="worker-dtt-review">
      <div className="worker-slopctl-title worker-dtt-title">
        {total} DTT proposal{total === 1 ? '' : 's'} awaiting your review — accept, reject or request modification before deploying it again
      </div>
      {viewing
        ? (
          <>
            <SlopspaceFiles
              slopspace={s}
              start={`writespaces/dtt-requests/${viewing}`}
              floor={`writespaces/dtt-requests/${viewing}`}
              upLabel="proposals"
              healthy={healthy}
              reply={reply}
              onControl={onControl}
              onUp={() => setViewing(null)}
            />
            <div className="worker-slopctl-line">{oneButtons(viewing, true)}</div>
          </>
        )
        : (
          <div className="worker-dtt-list">
            {proposals.map(p => (
              <div key={p} className="worker-dtt-row">
                <button className="wv-link worker-dtt-item" title="enter it" onClick={() => setViewing(p)}>
                  <span className={`worker-dtt-kind worker-dtt-${p.split('/')[0]}`}>{p.split('/')[0]}</span> {p.split('/').slice(1).join('/')}
                </button>
                {oneButtons(p, false)}
              </div>
            ))}
            {!!s.proposals_omitted && <div className="worker-signal-detail">…and {s.proposals_omitted} more</div>}
          </div>
        )}
      <textarea
        className="worker-slopctl-input worker-slopctl-grow worker-slopctl-command"
        rows={2}
        placeholder={`note — what to change, for request modification (it deploys the slopspace for ${workerAgentType} and asks the agent); optional otherwise`}
        value={note}
        onChange={e => setNote(e.target.value)}
        disabled={disabled}
      />
      <div className="worker-slopctl-line">
        <button className="worker-submit-btn worker-dtt-accept" disabled={disabled} onClick={() => decide('accept')}>accept{all} → canvas</button>
        <button className="worker-submit-btn worker-slopctl-danger" disabled={disabled} onClick={() => decide('reject')}>reject{all}</button>
        <button
          className="worker-submit-btn"
          disabled={disabled || !note.trim()}
          title={note.trim() ? '' : 'write what to change first'}
          onClick={() => decide('modify')}
        >request modification</button>
      </div>
    </div>
  )
}

function fmtSize(n?: number) {
  if (n === undefined) return ''
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`
  return `${(n / 1024 / 1024).toFixed(1)} MiB`
}

// SlopspaceFiles browses a slopspace's readspaces and writespaces, wherever
// they are right now (the deploy location while deployed), one directory or
// file at a time. Read-only; it works while an agent is working in it.
//
// Like the condoc sidebar's levels (Revision L), each directory or file is a
// scope you enter: clicking an entry zooms into it, and ↑ goes back to the
// directory holding it. At floor (the slopspace's root unless given) ↑ leaves
// the viewer, labelled upLabel.
export function SlopspaceFiles({
  slopspace: s,
  start,
  floor = '',
  upLabel,
  healthy,
  reply,
  onControl,
  onUp,
}: {
  slopspace: SlopspaceInfo
  start: string
  floor?: string
  upLabel: string
  healthy: boolean
  reply: WorkerSlopspaceReply | null
  onControl: (req: WorkerSlopspaceRequest) => void
  onUp: () => void
}) {
  const [path, setPath] = useState(start)
  const [listing, setListing] = useState<SlopspaceFileListing | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const open = (p: string) => {
    setPath(p)
    setLoading(true)
    setError(null)
    onControl({ op: 'files', id: s.id, path: p })
  }
  // (Re)load when opened on a slopspace or a starting path.
  useEffect(() => { open(start) }, [s.id, start]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!reply || reply.op !== 'files' || reply.id !== s.id) return
    if ((reply.path ?? '') !== path && (reply.files?.path ?? reply.path ?? '') !== path) return
    setLoading(false)
    if (reply.ok && reply.files) {
      setListing(reply.files)
      setError(null)
    } else {
      setError(reply.error || 'no answer')
    }
  }, [reply]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!loading) return
    const t = setTimeout(() => { setLoading(false); setError('no answer from dungeon-keeper — is it up to date?') }, 20_000)
    return () => clearTimeout(t)
  }, [loading])

  const parts = path ? path.split('/') : []
  const nameOf = (p: string) => p ? p.split('/').pop() : s.id.slice(0, 8)
  const parent = parts.slice(0, -1).join('/')
  const atFloor = path === floor
  const shown = listing && listing.path === path ? listing : null

  return (
    <div className="worker-slopctl worker-files">
      <button className="worker-files-up" onClick={() => atFloor ? onUp() : open(parent)} disabled={!atFloor && !healthy}>
        ↑ {atFloor ? upLabel : nameOf(parent)}
      </button>
      <div className="worker-slopctl-line">
        <span className={`worker-files-scope${shown?.dir ? ' worker-files-dir' : ''}`}>{nameOf(path)}{shown?.dir ? '/' : ''}</span>
        <span className="wv-spacer" />
        <button className="worker-submit-btn" onClick={() => open(path)} disabled={!healthy || loading}>reload</button>
      </div>
      {shown && <div className="worker-meta">{shown.root}{path ? `/${path}` : ''}{s.deployed ? ' (deployed)' : ''}</div>}
      {loading && <div className="worker-meta">loading…</div>}
      {error && <div className="worker-error">{error}</div>}
      {shown && shown.dir && (
        <div className="worker-files-list">
          {(shown.entries ?? []).length === 0 && <div className="worker-signal-detail">empty</div>}
          {(shown.entries ?? []).map(e => (
            <button key={e.name} className="wv-link worker-files-entry" onClick={() => open(path ? `${path}/${e.name}` : e.name)}>
              <span className={e.dir ? 'worker-files-dir' : ''}>{e.name}{e.dir ? '/' : ''}</span>
              {!e.dir && <span className="worker-files-size">{fmtSize(e.size)}</span>}
            </button>
          ))}
          {!!shown.omitted && <div className="worker-signal-detail">…and {shown.omitted} more not listed</div>}
        </div>
      )}
      {shown && !shown.dir && (
        <>
          <div className="worker-meta">
            {fmtSize(shown.size)}
            {shown.truncated ? ' — showing only the start' : ''}
          </div>
          {shown.binary
            ? <div className="worker-signal-detail">not a text file</div>
            : <pre className="worker-signal-text worker-files-content">{shown.content}</pre>}
        </>
      )}
    </div>
  )
}
