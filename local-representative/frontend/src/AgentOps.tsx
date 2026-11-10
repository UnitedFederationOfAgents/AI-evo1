import { useState } from 'react'
import type { AgentOp } from './types'

// Agent operations (condocv2 Step1 Revisions J, K): the system tab's section
// below "agents & models" -- a host's running agent calls, plus those
// finished in the last 5 minutes (local-representative/agentops.go).
// onOpen drills into the full-page view (onUp goes back to the system tab):
// a text sub-tab, the calls on the left and the selected one's details
// (command, response once finished) on the right, and a visual sub-tab, not
// yet populated. Kept identical in local-representative and
// agent-coordinator.

function dur(secs: number): string {
  secs = Math.max(0, secs)
  if (secs < 60) return `${secs}s`
  if (secs < 3600) return `${Math.floor(secs / 60)}m ${secs % 60}s`
  return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`
}

function timing(o: AgentOp, nowSec: number): string {
  return o.ended_at
    ? `took ${dur(o.ended_at - o.started_at)} · ended ${dur(nowSec - o.ended_at)} ago`
    : `running ${dur(nowSec - o.started_at)}`
}

const SUBTABS = ['text', 'visual'] as const

export function AgentOps({ ops, nowSec, onOpen, onUp }: {
  ops?: AgentOp[] | null
  nowSec: number
  onOpen?: () => void
  onUp?: () => void
}) {
  const [sub, setSub] = useState<typeof SUBTABS[number]>('text')
  const [selId, setSelId] = useState<string | null>(null)
  const full = onUp !== undefined
  const running = ops?.filter(o => !o.ended_at).length ?? 0
  const sel = ops?.find(o => o.id === selId)
  const rows = !ops ? (
    <div className="sys-providers-none">not reported by this host</div>
  ) : ops.length === 0 ? (
    <div className="sys-providers-none">no agent calls in the last 5 minutes</div>
  ) : ops.map(o => (
    <div key={o.id}
      className={`agent-op ${o.ended_at ? 'agent-op-done' : 'agent-op-running'}${full ? ' agent-op-pick' : ''}${o.id === selId ? ' agent-op-sel' : ''}`}
      onClick={full ? () => setSelId(o.id === selId ? null : o.id) : undefined}>
      <span className="agent-op-dot" />
      <span className="agent-op-agent">{o.agent || 'agent?'}{o.model ? ` · ${o.model}` : ''}</span>
      <span className="agent-op-session" title={o.session}>{o.session_name || o.session}</span>
      <span className="agent-op-time">{timing(o, nowSec)}</span>
    </div>
  ))
  return (
    <div className={`sys-providers agent-ops${full ? ' agent-ops-full' : ''}`}>
      <div className="sys-providers-heading">
        {full && <button className="sys-btn sys-btn-neutral agent-ops-up" onClick={onUp}>↑ system</button>}
        <span className="sys-providers-header">agent operations</span>
        {ops && <span className="agent-ops-count">{running} running</span>}
        {onOpen && <button className="sys-btn sys-btn-neutral" onClick={onOpen} title="open the full-page view">open ⤢</button>}
      </div>
      {!full ? rows : (
        <>
          <div className="ctl-subtabs">
            {SUBTABS.map(t => (
              <button key={t} className={`ctl-subtab${sub === t ? ' ctl-subtab-active' : ''}`} onClick={() => setSub(t)}>{t}</button>
            ))}
          </div>
          {sub === 'visual' ? (
            <div className="sys-providers-none">not yet populated</div>
          ) : (
            <div className="agent-ops-split">
              <div className="agent-ops-list">{rows}</div>
              <div className="agent-ops-details">
                {!sel ? (
                  <div className="sys-providers-none">{selId ? 'that call has been dropped' : 'select a call'}</div>
                ) : (
                  <>
                    <div className="agent-op-agent">{sel.agent || 'agent?'}{sel.model ? ` · ${sel.model}` : ''}</div>
                    <div className="agent-op-session">session {sel.session_name ? `${sel.session_name} (${sel.session})` : sel.session}</div>
                    <div className="agent-op-session">
                      started {new Date(sel.started_at * 1000).toLocaleTimeString()} · {timing(sel, nowSec)}
                    </div>
                    <div className="agent-ops-label">command</div>
                    <pre className="agent-ops-pre">{sel.command}</pre>
                    <div className="agent-ops-label">response</div>
                    {sel.ended_at
                      ? <pre className="agent-ops-pre">{sel.response || '(none recorded)'}</pre>
                      : <div className="sys-providers-none">available once the call finishes</div>}
                  </>
                )}
              </div>
            </div>
          )}
        </>
      )}
    </div>
  )
}
