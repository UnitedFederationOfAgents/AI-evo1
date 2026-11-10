import { useEffect, useRef, useState } from 'react'
import type { DemoProposal, ToolsReply, ToolsRequest, ToolsRequestFn } from './Tools'

// The demo tab's proposals sub-tab (PrivateRepoOps Step 5, Revision E; far
// left; agent-only since Revision H): a demo the worker's agent builds
// (local-representative's demoproposals.go). A name, a title and a
// description of the whole demo go in, and "agent create" hands them to
// the worker, whose agent writes the full demo -- a DTT document at the
// top level of the proposal's directory, and anything new it needs beside
// it -- in write mode, in that directory only.
//
// Each proposal is a tile below the inputs, with its icon if the agent
// made one. Once its demo is written the host copies it into the library,
// and selecting the tile shows its details -- the description and the
// files the agent wrote -- and picks the demo's declared coverage out on
// the protosurf map, as a demo does. Kept identical in local-representative
// and agent-coordinator.

// How often the listing is re-read while a demo is being built.
const CREATING_POLL_MS = 4000

function when(ms?: number): string {
  return ms ? new Date(ms).toLocaleString() : ''
}

function statusLabel(p: DemoProposal): string {
  if (p.status === 'creating') return 'the agent is building it…'
  if (p.status === 'failed') return 'failed'
  return p.demo ? `demo ${p.demo}` : 'created'
}

export function ProposalsView({ request, replies, connected, proposals, onChanged, onPick, isPicked, onClearPick, onOpenDemo }: {
  request: ToolsRequestFn
  replies: Record<string, ToolsReply>
  connected: boolean
  proposals: DemoProposal[]
  // onChanged re-reads the demo tab's listing (a proposal created, built
  // or deleted); onPick picks a proposal's demo's coverage out on the map;
  // onOpenDemo shows the demo built from one.
  onChanged: () => void
  onPick: (name: string) => void
  isPicked: (name: string) => boolean
  onClearPick: () => void
  onOpenDemo: (name: string) => void
}) {
  // One request at a time, and its reply.
  const [req, setReq] = useState<{ id: string; op: ToolsRequest['op'] } | null>(null)
  const reply = req ? replies[req.id] : undefined
  const pending = req !== null && !reply
  const send = (r: ToolsRequest) => setReq({ id: request(r), op: r.op })

  // The selected tile, and its proposal as the host last answered it in
  // full (demo-proposal: its files -- a listing relayed through
  // agent-coordinator may have been cut).
  const [open, setOpen] = useState<string | null>(null)
  const [shown, setShown] = useState<DemoProposal | null>(null)
  const [newName, setNewName] = useState('')
  const [newTitle, setNewTitle] = useState('')
  const [newDescription, setNewDescription] = useState('')
  const [agent, setAgent] = useState('')

  useEffect(() => {
    if (!reply || !req || !reply.ok) return
    if (req.op === 'demo-proposal-delete') {
      setOpen(null); setShown(null)
      onChanged()
      return
    }
    const p = reply.demo_proposal
    if (!p) return
    if (req.op === 'demo-proposal-agent-create') {
      setOpen(p.name)
      setNewName(''); setNewTitle(''); setNewDescription('')
      onChanged()
    } else if (open !== p.name) {
      return // the tile selected before this one
    }
    setShown(p)
  }, [reply]) // eslint-disable-line react-hooks/exhaustive-deps

  // The selected proposal is read in full when it's selected, and again
  // when the listing says it has changed (built, or failed).
  const listed = proposals.find(p => p.name === open)
  const listedKey = listed ? `${listed.status}|${listed.demo ?? ''}|${listed.updated_at ?? ''}` : ''
  const read = useRef<() => void>(() => {})
  read.current = () => { if (open) send({ op: 'demo-proposal', name: open }) }
  useEffect(() => { if (open && connected) read.current() }, [open, connected, listedKey])

  const creating = proposals.some(p => p.status === 'creating')
  const changed = useRef(onChanged)
  changed.current = onChanged
  useEffect(() => {
    if (!creating) return
    const t = setInterval(() => changed.current(), CREATING_POLL_MS)
    return () => clearInterval(t)
  }, [creating])

  const toggle = (p: DemoProposal) => {
    setShown(null)
    if (open === p.name) { setOpen(null); return }
    setOpen(p.name)
    if ((p.coverage?.focal.length ?? 0) + (p.coverage?.supporting.length ?? 0) > 0) onPick(p.name)
  }
  const agentCreate = () => send({
    op: 'demo-proposal-agent-create',
    proposal: { name: newName.trim(), title: newTitle.trim(), description: newDescription, created_at: 0 },
    agent: agent.trim() || undefined,
  })
  const remove = () => {
    if (!open || !window.confirm(`Delete proposal ${open}, and everything the agent wrote in its directory? A demo it put into the library is kept.`)) return
    send({ op: 'demo-proposal-delete', name: open })
  }

  const note = pending ? 'working…' : reply && !reply.ok ? reply.error : reply && req?.op !== 'demo-proposal' ? reply.detail : undefined
  const d = shown?.name === open ? shown : listed
  const files = shown?.name === open ? shown?.files ?? [] : []

  return (
    <>
      <div className="ctl-hint">
        Describe a demo and the worker’s agent builds the whole of it: a DTT document at the top level of the proposal’s directory, and anything new it needs beside it, written there and nowhere else. Once it’s done the demo goes into this host’s library, and its tile below shows what it wrote.
      </div>
      <div className="demo-card">
        <div className="demo-actions">
          <span className="demo-var-name">new demo</span>
          <input className="demo-dup-name" value={newName} placeholder="name (e.g. robot-tour)" onChange={e => setNewName(e.target.value)} />
          <input className="demo-prop-title" value={newTitle} placeholder="title" onChange={e => setNewTitle(e.target.value)} />
          <input className="demo-dup-name" value={agent} placeholder="agent (default: the worker’s)" onChange={e => setAgent(e.target.value)} />
          <button
            className="sys-btn" disabled={!connected || !newName.trim() || (!newTitle.trim() && !newDescription.trim()) || pending}
            title={!newTitle.trim() && !newDescription.trim() ? 'give the agent a title or a description to go on' : 'have the worker’s agent build the whole demo from the name, title and description'}
            onClick={agentCreate}
          >✦ agent create</button>
        </div>
        <textarea className="demo-question demo-prop-long" value={newDescription} placeholder="the description: the full demo -- what it shows, how it unfolds, what it should leave a viewer with" onChange={e => setNewDescription(e.target.value)} />
      </div>
      {note && <div className={reply && !reply.ok ? 'ctl-run-error-msg' : 'ctl-hint'}>{note}</div>}
      {proposals.length === 0 ? (
        <div className="sys-providers-none">no proposals yet</div>
      ) : (
        <div className="demo-prop-tiles">
          {proposals.map(p => (
            <div
              key={p.name} title={p.description}
              className={`demo-prop-tile demo-prop-tile-${p.status ?? 'failed'}${open === p.name ? ' demo-prop-tile-open' : ''}`}
              onClick={() => toggle(p)}
            >
              <div className="demo-prop-icon">{p.icon ? <img src={p.icon} alt="" /> : <span>{p.status === 'creating' ? '…' : p.status === 'failed' ? '✗' : '▶'}</span>}</div>
              <div className="demo-prop-tile-title">{p.title}</div>
              <div className="demo-count">{p.name}</div>
              <div className="demo-count">{statusLabel(p)}</div>
              {isPicked(p.name) && <span className="demo-map-mark" title="picked out on the protosurf map -- click to let it go" onClick={e => { e.stopPropagation(); onClearPick() }}>◎ on the map</span>}
            </div>
          ))}
        </div>
      )}
      {open && d && (
        <div className="demo-card demo-card-open">
          <div className="demo-card-head">
            <span className="demo-app-name">{d.title}</span>
            <span className="demo-count">details · {d.name} · {statusLabel(d)} · {when(d.updated_at || d.created_at)}</span>
          </div>
          <div className="demo-card-body">
            <div className="demo-prop-part">
              <span className="demo-var-name">description</span>
              <div className="demo-desc">{d.description || '(none -- built from its title)'}</div>
            </div>
            <div className="demo-prop-part">
              <span className="demo-var-name">files</span>
              {d.dir && <div className="demo-count">in {d.dir}</div>}
              {files.length === 0 ? (
                <div className="demo-count">{shown?.name === open ? 'none yet' : 'reading…'}</div>
              ) : (
                <ul className="demo-prop-files">
                  {files.map(f => <li key={f}>{f}{f === d.demo_file ? ' -- the demo' : ''}</li>)}
                </ul>
              )}
            </div>
            {d.error && <div className="ctl-run-error-msg">{d.error}</div>}
            <div className="demo-actions">
              {d.demo && <button className="sys-btn sys-btn-neutral" onClick={() => onOpenDemo(d.demo!)}>open demo {d.demo}</button>}
              <button className="sys-btn sys-btn-neutral" disabled={!connected || pending} onClick={remove}>delete</button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
