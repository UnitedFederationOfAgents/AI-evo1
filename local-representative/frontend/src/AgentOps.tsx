import type { AgentOp } from './types'

// Agent operations (condocv2 Step1 Revision J): the system tab's section
// below "agents & models" -- a host's running agent calls, plus those
// finished in the last 5 minutes (local-representative/agentops.go).
// onOpen drills into the full-page view, which shows the same list (with
// each call's command) and an onUp back to the system tab. Kept identical
// in local-representative and agent-coordinator.

function dur(secs: number): string {
  secs = Math.max(0, secs)
  if (secs < 60) return `${secs}s`
  if (secs < 3600) return `${Math.floor(secs / 60)}m ${secs % 60}s`
  return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`
}

export function AgentOps({ ops, nowSec, onOpen, onUp }: {
  ops?: AgentOp[] | null
  nowSec: number
  onOpen?: () => void
  onUp?: () => void
}) {
  const full = onUp !== undefined
  const running = ops?.filter(o => !o.ended_at).length ?? 0
  return (
    <div className={`sys-providers agent-ops${full ? ' agent-ops-full' : ''}`}>
      <div className="sys-providers-heading">
        {full && <button className="sys-btn sys-btn-neutral agent-ops-up" onClick={onUp}>↑ system</button>}
        <span className="sys-providers-header">agent operations</span>
        {ops && <span className="agent-ops-count">{running} running</span>}
        {onOpen && <button className="sys-btn sys-btn-neutral" onClick={onOpen} title="open the full-page view">open ⤢</button>}
      </div>
      {!ops ? (
        <div className="sys-providers-none">not reported by this host</div>
      ) : ops.length === 0 ? (
        <div className="sys-providers-none">no agent calls in the last 5 minutes</div>
      ) : ops.map(o => (
        <div key={o.id} className={`agent-op ${o.ended_at ? 'agent-op-done' : 'agent-op-running'}`}>
          <span className="agent-op-dot" />
          <span className="agent-op-agent">{o.agent || 'agent?'}{o.model ? ` · ${o.model}` : ''}</span>
          <span className="agent-op-session" title={o.session}>{o.session_name || o.session}</span>
          <span className="agent-op-time">
            {o.ended_at
              ? `took ${dur(o.ended_at - o.started_at)} · ended ${dur(nowSec - o.ended_at)} ago`
              : `running ${dur(nowSec - o.started_at)}`}
          </span>
          {full && <span className="agent-op-cmd">{o.command}</span>}
        </div>
      ))}
    </div>
  )
}
