import { useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import type {
  ControlActionDef,
  ControlInstruction,
  ControlLibReply,
  ControlLibRequest,
  ControlLibraryMsg,
  ControlOpSpec,
  ControlParam,
  ControlRecordBlock,
  ControlRecordState,
  ControlRecording,
  ControlSequenceDef,
  ControlStateMsg,
  ControlStepInfo,
  ControlStepRef,
} from './controlTypes'

// The control tab's v1 sub-tab (local-representative/control.go and
// controllib.go, condocs/initialRobotImpls/Step3Prompt.md Revision C): three
// views over the node's control library, the way the robot's sequence-v2
// tab works (ianar/frontend/src/SequenceV2.tsx) --
//   runner:   run a sequence with chosen control values, following its steps
//             and playing back its recording
//   definer:  actions -- reusable building blocks exposing controls, each a
//             list of instructions: LR's own ops, or robot.<op> for the
//             robot's
//   composer: sequences -- actions in order, with values for their controls,
//             and slim recording blocks beside them: each records a node's
//             screen across a span of steps, up to three side by side
//             (Step4Prompt.md, local-representative/controlrecord.go)
// Both actions and sequences import from and export to YAML.
//
// This file and controlTypes.ts are shared, word for word, by
// local-representative's frontend and agent-coordinator's (which drives a
// chosen host's library through its own relay) -- keep the two copies in
// step. Everything node-specific comes in through the props.

// Request sends a library request and returns the id its reply will carry.
export type ControlRequest = (req: ControlLibRequest) => string

export interface ControlV1Props {
  connected: boolean
  state: ControlStateMsg | null
  lib: ControlLibraryMsg | null
  // Replies by request id; a run that couldn't start is under "run".
  replies: Record<string, ControlLibReply>
  request: ControlRequest
  robotHealthy: boolean
  // recordOverride, if set, replaces the sequence's recordings for the run:
  // a node recorded across the whole run, or "none" (controlrecord.go).
  onRun: (sequence: string, controls: Record<string, string>, recordOverride?: string) => void
  onCancel: () => void
  onContinue: (run: string) => void // answers a step waiting for continue
  fileUrl: (id: string) => string // a saved recording's URL
  node: string // how to refer to the node: "this node", "this host"
}

const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T

function formatMs(ms?: number): string {
  if (!ms) return ''
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}

function stepIcon(status: string): string {
  return status === 'success' ? '✓'
    : status === 'error' ? '✗'
    : status === 'running' ? '▸'
    : status === 'skipped' ? '–'
    : '·'
}

// useRequest tracks one library request at a time: the latest sent and its
// reply, once it arrives.
function useRequest(request: ControlRequest, replies: Record<string, ControlLibReply>) {
  const [req, setReq] = useState<string | null>(null)
  const reply = req ? replies[req] : undefined
  return {
    reply,
    pending: req !== null && !reply,
    send: (r: ControlLibRequest) => setReq(request(r)),
    clear: () => setReq(null),
  }
}

type Tracked = ReturnType<typeof useRequest>

function ReplyStatus({ r, working = 'working…' }: { r: Tracked; working?: string }) {
  if (r.pending) return <div className="ctl-hint">{working}</div>
  if (!r.reply) return null
  if (!r.reply.success) return <div className="ctl-run-error-msg">{r.reply.error}</div>
  return r.reply.message ? <div className="ctl-ok">✓ {r.reply.message}</div> : null
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="ctl-field">
      <span className="ctl-field-name">{label}</span>
      {children}
    </label>
  )
}

// ---- Recording blocks ----

// At most this many recordings side by side at any step (controlrecord.go).
const MAX_PARALLEL_RECORDINGS = 3
const REC_COLORS = ['#c586c0', '#4fc1ff', '#dcdcaa', '#4ec9b0', '#ce9178', '#b5cea8']
const recColor = (k: number) => REC_COLORS[k % REC_COLORS.length]

const covers = (b: ControlRecordBlock, step: number) => b.from <= step && step <= b.to
const recSteps = (b: { from?: number; to?: number }) => (b.from === b.to ? `step ${b.from}` : `steps ${b.from}–${b.to}`)

// recordLanes puts each block in a lane (column) beside the steps, earliest
// first into the first lane free by then -- never more lanes than blocks
// side by side at a step.
function recordLanes(blocks: ControlRecordBlock[]): { lane: number[]; lanes: number } {
  const order = blocks.map((_, k) => k).sort((a, b) => blocks[a].from - blocks[b].from || blocks[a].to - blocks[b].to)
  const ends: number[] = []
  const lane = blocks.map(() => 0)
  for (const k of order) {
    let l = ends.findIndex(end => end < blocks[k].from)
    if (l < 0) {
      l = ends.length
      ends.push(0)
    }
    ends[l] = blocks[k].to
    lane[k] = l
  }
  return { lane, lanes: ends.length }
}

// recordProblems is what saving would refuse (controlrecord.go's
// checkRecordBlocks), to show while editing.
function recordProblems(blocks: ControlRecordBlock[], nSteps: number): string[] {
  const out = new Set<string>()
  blocks.forEach((b, k) => {
    if (!b.node.trim()) out.add(`recording ${k + 1} has no node`)
    if (b.from < 1 || b.to < b.from || b.to > nSteps) out.add(`recording ${k + 1} has to be within steps 1 to ${nSteps}, first to last`)
  })
  for (let i = 1; i <= nSteps; i++) {
    const at = blocks.map((_, k) => k).filter(k => covers(blocks[k], i))
    if (at.length > MAX_PARALLEL_RECORDINGS) out.add(`step ${i} has ${at.length} recordings; at most ${MAX_PARALLEL_RECORDINGS} can run side by side`)
    at.forEach((x, j) =>
      at.slice(j + 1).forEach(y => {
        const node = blocks[x].node.trim()
        if (node && node === blocks[y].node.trim()) out.add(`recordings ${x + 1} and ${y + 1} both record ${node} at step ${i}`)
      }),
    )
  }
  return [...out]
}

function recTitle(b: ControlRecordBlock, k: number, st?: ControlRecordState): string {
  let t = `recording ${k + 1}: ${b.node || '(no node)'}'s screen, ${recSteps(b)}`
  if (b.label) t += ` — ${b.label}`
  if (st) t += `\n${st.status}${st.message ? `: ${st.message}` : ''}`
  return t
}

// A step list with recordings is a grid (Step4Prompt.md Revision A): each
// step one row in the first column, then a slim column per recording lane,
// then (in the composer) a column of ⏺ buttons. A recording is then one
// element spanning the rows of its first step to its last -- a single bar
// across the steps and the gaps between them.
const recGridColumns = (lanes: number, addColumn: boolean) =>
  ['minmax(0, 1fr)', ...Array.from({ length: lanes }, () => '9px'), ...(addColumn ? ['auto'] : [])].join(' ')
const stepCell = (i: number) => ({ gridRow: i + 1, gridColumn: 1 })

type RecEnd = 'from' | 'to'

// RecordBars draws each recording block as one bar in its lane, from its
// first step's row to its last's (clamped to the nSteps there are, as an
// edit may leave it past them). onGrab, in the composer, gives each bar
// handles to drag its first and last step.
function RecordBars({ blocks, layout, nSteps, states, selected, onSelect, onGrab }: {
  blocks: ControlRecordBlock[]
  layout: { lane: number[]; lanes: number }
  nSteps: number
  states?: ControlRecordState[]
  selected?: number | null
  onSelect?: (k: number) => void
  onGrab?: (k: number, end: RecEnd) => void
}) {
  return (
    <>
      {blocks.map((b, k) => {
        const from = Math.max(1, b.from)
        const to = Math.min(nSteps, b.to)
        if (from > to) return null
        const st = states?.[k]
        const cls = [
          'ctl-rec-bar',
          st && `ctl-rec-st-${st.status}`,
          selected === k && 'ctl-rec-bar-selected',
          onSelect && 'ctl-rec-bar-click',
        ].filter(Boolean).join(' ')
        const grip = (end: RecEnd) => (
          <span
            className={`ctl-rec-grip ctl-rec-grip-${end}`}
            title={end === 'from' ? 'drag to change the first step it records' : 'drag to change the last step it records'}
            onPointerDown={e => {
              e.preventDefault()
              e.stopPropagation()
              onGrab?.(k, end)
            }}
          />
        )
        return (
          <li
            key={`rec-${k}`}
            className={cls}
            style={{ gridRow: `${from} / ${to + 1}`, gridColumn: 2 + layout.lane[k], background: recColor(k) }}
            title={recTitle(b, k, st)}
            onClick={onSelect ? () => onSelect(k) : undefined}
          >
            {onGrab && grip('from')}
            {onGrab && grip('to')}
          </li>
        )
      })}
    </>
  )
}

// RecordLegend lists a sequence's recording blocks (and, in a run, how each
// is going) under its steps.
function RecordLegend({ blocks, states, node, thisNode }: { blocks: ControlRecordBlock[]; states?: ControlRecordState[]; node: string; thisNode?: string }) {
  return (
    <div className="ctl-rec-legend">
      {blocks.map((b, k) => {
        const st = states?.[k]
        return (
          <div key={k} className="ctl-rec-legend-row">
            <span className="ctl-rec-swatch" style={{ background: recColor(k) }} />
            <span>
              records {b.node ? `${b.node}${b.node === thisNode ? ` (${node})` : ''}` : '(a node chosen when it runs)'}'s screen, {recSteps(b)}
              {b.label ? ` — ${b.label}` : ''}
            </span>
            {st && <span className={`ctl-rec-status ctl-rec-st-${st.status}`}>{st.status}</span>}
            {st?.message && <span className="ctl-rec-msg">{st.message}</span>}
          </div>
        )
      })}
    </div>
  )
}

// ---- Runner ----

function Runner({ connected, state, lib, replies, request, robotHealthy, onRun, onCancel, onContinue, fileUrl, node }: ControlV1Props) {
  const sequences = state?.sequences ?? []
  const nodes = state?.nodes ?? []
  const [selectedId, setSelectedId] = useState('')
  const [values, setValues] = useState<Record<string, Record<string, string>>>({})
  // The override recording (Step4Prompt.md Revision D): when on, the run
  // records overrideNode's screen from first step to last (or none), in
  // place of the sequence's recording blocks. The node defaults to the one
  // running the sequence.
  const [overrideOn, setOverrideOn] = useState(false)
  const [overrideNode, setOverrideNode] = useState('')
  const saveRun = useRequest(request, replies)
  const run = state?.run
  const running = run?.status === 'running'
  // Follow a run started elsewhere (agent-coordinator, another tab).
  useEffect(() => {
    if (running && run) setSelectedId(run.sequence)
  }, [running, run?.sequence]) // eslint-disable-line react-hooks/exhaustive-deps
  // A save's failure belongs to the run it was for.
  useEffect(() => {
    saveRun.clear()
  }, [run?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const seq = sequences.find(q => q.id === selectedId) ?? sequences[0]
  if (!seq) {
    return <div className="ctl-empty">{lib ? 'No sequences yet — build one in the composer.' : 'waiting for control state…'}</div>
  }
  const controls = seq.controls ?? []
  const vals = values[seq.id] ?? {}
  const valueOf = (c: ControlParam) => vals[c.name] ?? c.default ?? ''
  const thisRun = run && run.sequence === seq.id ? run : undefined
  // A run shows the steps as compiled with its values; otherwise the defaults.
  const steps: (ControlStepInfo & { status?: string; message?: string; duration_ms?: number })[] = thisRun?.steps ?? seq.steps ?? []
  const runError = replies.run && !replies.run.success ? replies.run.error : undefined
  // The recording blocks beside the steps: as the run fills them in and
  // follows them, else with the defaults.
  const primary = state?.node ?? ''
  const overrideTo = overrideNode || primary
  const override = overrideOn ? overrideTo : undefined
  const recBlocks: ControlRecordBlock[] = thisRun?.records
    ?? (override === undefined ? seq.recordings ?? []
      : override === 'none' || steps.length === 0 ? []
      : [{ node: override, from: 1, to: steps.length, label: 'override: the whole run' }])
  const recLayout = recordLanes(recBlocks)

  return (
    <div className="ctl-v1-view">
      {!robotHealthy && (
        <div className="ctl-hint ctl-hint-warn">the robot isn't running on {node} — robot steps will fail; launch it from the system tab first</div>
      )}
      <div className="ctl-row">
        <select className="ctl-select" value={seq.id} disabled={running} onChange={e => setSelectedId(e.target.value)}>
          {sequences.map(q => (
            <option key={q.id} value={q.id}>{q.name}</option>
          ))}
        </select>
        {thisRun && (
          <span className={`ctl-run-status ctl-run-${thisRun.status}`}>
            {thisRun.status}{thisRun.duration_ms ? ` · ${formatMs(thisRun.duration_ms)}` : ''}
          </span>
        )}
        {running ? (
          <button className="sys-btn sys-btn-terminate" onClick={onCancel}>cancel</button>
        ) : (
          <button
            className="sys-btn sys-btn-launch"
            disabled={!connected || !!seq.error}
            onClick={() => onRun(seq.id, Object.fromEntries(controls.map(c => [c.name, valueOf(c)])), override)}
          >
            run
          </button>
        )}
      </div>
      <div className="ctl-row">
        <label className="ctl-check" title="record one node's screen across the whole run (or until it stops on a failure), or nothing, in place of the sequence's own recordings">
          <input type="checkbox" checked={overrideOn} disabled={running} onChange={e => setOverrideOn(e.target.checked)} />
          override recording
        </label>
        {overrideOn && (
          <select className="ctl-select" value={overrideTo} disabled={running} onChange={e => setOverrideNode(e.target.value)}>
            {primary && !nodes.includes(primary) && <option value={primary}>{primary} ({node})</option>}
            {nodes.map(n => (
              <option key={n} value={n}>{n}{n === primary ? ` (${node})` : ''}</option>
            ))}
            <option value="none">none — no recordings</option>
          </select>
        )}
        {thisRun?.record_override && (
          <span className="ctl-hint">this run's recording was overridden: {thisRun.record_override === 'none' ? 'none' : `${thisRun.record_override}'s screen, the whole run`}</span>
        )}
      </div>
      {running && !thisRun && run && <div className="ctl-hint">running: {run.name}</div>}
      {seq.description && <div className="ctl-seq-desc">{seq.description}</div>}
      {seq.error && <div className="ctl-run-error-msg">can't run: {seq.error}</div>}
      {runError && !running && <div className="ctl-run-error-msg">{runError}</div>}
      {controls.length > 0 && (
        <div className="ctl-section">
          {controls.some(c => c.type === 'node') && nodes.length === 0 && (
            <div className="ctl-hint ctl-hint-warn">{node} isn't connected to agent-coordinator, so there are no nodes to choose</div>
          )}
          {controls.map(c => {
            const set = (value: string) => setValues(v => ({ ...v, [seq.id]: { ...(v[seq.id] ?? {}), [c.name]: value } }))
            return (
              <Field key={c.name} label={c.label || c.name}>
                {c.type === 'node' ? (
                  // Only nodes connected to agent-coordinator (Step3Prompt.md Revision F).
                  <select className="ctl-select" value={valueOf(c)} title={c.help} disabled={running} onChange={e => set(e.target.value)}>
                    <option value="">— choose a node —</option>
                    {valueOf(c) !== '' && !nodes.includes(valueOf(c)) && (
                      <option value={valueOf(c)} disabled>{valueOf(c)} (not connected)</option>
                    )}
                    {nodes.map(n => (
                      <option key={n} value={n}>{n}{n === state?.node ? ` (${node})` : ''}</option>
                    ))}
                  </select>
                ) : (
                  <input className="ctl-input" value={valueOf(c)} title={c.help} disabled={running} onChange={e => set(e.target.value)} />
                )}
              </Field>
            )
          })}
        </div>
      )}
      <ol className="ctl-steps ctl-rec-grid" style={{ gridTemplateColumns: recGridColumns(recLayout.lanes, false) }}>
        {steps.map((st, i) => {
          const status = st.status ?? 'pending'
          return (
            <li key={i} className={`ctl-step ctl-step-${status}`} style={stepCell(i)}>
              <span className="ctl-step-icon">{stepIcon(status)}</span>
              <div className="ctl-step-body">
                <div className="ctl-step-label">{i + 1}. {st.label}</div>
                {st.detail && <div className="ctl-step-detail">{st.detail}</div>}
                {st.do && st.do.length > 0 && (
                  <ul className="ctl-step-do">
                    {st.do.map((x, j) => <li key={j}>{x}</li>)}
                  </ul>
                )}
                {st.message && <div className="ctl-step-msg">{st.message}</div>}
                {running && thisRun?.prompt?.step === i && (
                  // A step waiting for the user (ask-user, Step3Prompt.md Revision G).
                  <div className="ctl-prompt">
                    <div className="ctl-prompt-msg">{thisRun.prompt.message}</div>
                    <button className="sys-btn sys-btn-launch" disabled={!connected} onClick={() => onContinue(thisRun.id)}>continue</button>
                  </div>
                )}
              </div>
              <span className="ctl-step-time">{status !== 'skipped' ? formatMs(st.duration_ms) : ''}</span>
            </li>
          )
        })}
        <RecordBars blocks={recBlocks} layout={recLayout} nSteps={steps.length} states={thisRun?.records} />
      </ol>
      {recBlocks.length > 0 && (
        <>
          <RecordLegend blocks={recBlocks} states={thisRun?.records} node={node} thisNode={state?.node} />
          {!thisRun && <div className="ctl-hint">recordings are saved to the files tab and played back below once they stop</div>}
        </>
      )}
      {thisRun?.output && thisRun.output.length > 0 && (
        <div className="ctl-output">
          <div className="ctl-output-head">output</div>
          {thisRun.output.map((v, i) => (
            <div key={i} className="ctl-value">
              <span className="ctl-value-label">{v.label}:</span> <span className="ctl-output-value">{v.value}</span>
            </div>
          ))}
        </div>
      )}
      {thisRun?.values && thisRun.values.length > 0 && (
        <div className="ctl-values">
          {thisRun.values.map((v, i) => (
            <div key={i} className="ctl-value">
              <span className="ctl-value-label">{v.label}:</span> {v.value}
            </div>
          ))}
        </div>
      )}
      {thisRun?.recordings && thisRun.recordings.length > 0 && (
        <Recordings recordings={thisRun.recordings} fileUrl={fileUrl} node={node} />
      )}
      {thisRun?.error && <div className="ctl-run-error-msg">{thisRun.error}</div>}
      {thisRun && !running && (
        // Save the finished run into the files tab, like the robot's
        // sequence runs (Step3Prompt.md Revision J; controlsave.go).
        <div className="ctl-save">
          <div className="ctl-row">
            <button
              className="sys-btn"
              disabled={!connected || saveRun.pending}
              title={`upload a .zip of this run (report, steps, values, output and its files) into ${node}'s files tab`}
              onClick={() => saveRun.send({ op: 'save-run', id: thisRun.id })}
            >
              {saveRun.pending ? 'saving…' : 'save results to files'}
            </button>
            {(thisRun.saved ?? []).map(f => (
              <a key={f.file_id} className="ctl-save-link" href={`${fileUrl(f.file_id)}?download=1`}>{f.name}</a>
            ))}
          </div>
          {saveRun.reply && !saveRun.reply.success && <div className="ctl-run-error-msg">{saveRun.reply.error}</div>}
        </div>
      )}
    </div>
  )
}

// Recordings plays back a run's screen recordings beside its steps
// (Step3Prompt.md Revision B), and shows its screenshots (Revision F).
// Recordings saved as a .zip of frames (no compositor video), and files
// fetched from nodes, only get a link.
function Recordings({ recordings, fileUrl, node }: { recordings: ControlRecording[]; fileUrl: (id: string) => string; node: string }) {
  return (
    <div className="ctl-recordings">
      {recordings.map(rec => (
        <div key={rec.file_id} className="ctl-recording">
          <div className="ctl-recording-head">
            {rec.file
              ? `${rec.who === 'robot' ? node : rec.who}: ${rec.path ?? 'file'}`
              : rec.who === 'robot' ? `${node}'s screen` : `${rec.who}'s screen`}
            {rec.from ? ` · ${recSteps(rec)}` : ''}
            {rec.label ? ` · ${rec.label}` : ''}
            {rec.image ? ' · screenshot' : ''}
            {rec.duration_ms ? ` · ${formatMs(rec.duration_ms)}` : ''}
            {rec.via ? ` · via ${rec.via}` : ''}
            {' · '}
            <a href={`${fileUrl(rec.file_id)}?download=1`}>{rec.name}</a>
            {!rec.video && !rec.image && !rec.file && ' (sampled frames, not a video — download to view)'}
          </div>
          {rec.video && <video className="ctl-recording-video" src={fileUrl(rec.file_id)} controls preload="metadata" />}
          {rec.image && (
            <a href={fileUrl(rec.file_id)} target="_blank" rel="noreferrer">
              <img className="ctl-recording-video" src={fileUrl(rec.file_id)} alt={rec.name} />
            </a>
          )}
        </div>
      ))}
    </div>
  )
}

// ---- Shared editors ----

// ControlsEditor edits a list of controls: name (as used in {{name}}),
// label, type, default and help. A "node" control is chosen in the runner
// from the nodes connected to agent-coordinator.
function ControlsEditor({ controls, onChange }: { controls: ControlParam[]; onChange: (c: ControlParam[]) => void }) {
  const set = (i: number, patch: Partial<ControlParam>) => onChange(controls.map((c, j) => (j === i ? { ...c, ...patch } : c)))
  return (
    <div className="ctl-controls">
      {controls.map((c, i) => (
        <div className="ctl-row" key={i}>
          <input className="ctl-input ctl-input-name" placeholder="name" value={c.name} onChange={e => set(i, { name: e.target.value })} />
          <input className="ctl-input" placeholder="label" value={c.label ?? ''} onChange={e => set(i, { label: e.target.value })} />
          <select
            className="ctl-select"
            value={c.type ?? ''}
            title="text, or a node connected to agent-coordinator (the runner offers only those)"
            onChange={e => set(i, { type: e.target.value || undefined })}
          >
            <option value="">text</option>
            <option value="node">node</option>
          </select>
          <input className="ctl-input" placeholder="default" value={c.default ?? ''} onChange={e => set(i, { default: e.target.value })} />
          <input className="ctl-input" placeholder="help" value={c.help ?? ''} onChange={e => set(i, { help: e.target.value })} />
          <button className="ctl-btn ctl-icon-btn" title="remove this control" onClick={() => onChange(controls.filter((_, j) => j !== i))}>✕</button>
        </div>
      ))}
      <button className="ctl-btn" onClick={() => onChange([...controls, { name: '' }])}>+ add control</button>
    </div>
  )
}

// YamlTools exports the selected item (or everything) of one kind as YAML,
// to download or copy, and imports YAML pasted or picked from a file.
function YamlTools({ kind, selectedId, request, replies, connected }: {
  kind: 'actions' | 'sequences'
  selectedId: string
  request: ControlRequest
  replies: Record<string, ControlLibReply>
  connected: boolean
}) {
  const exp = useRequest(request, replies)
  const imp = useRequest(request, replies)
  const [importing, setImporting] = useState(false)
  const [text, setText] = useState('')
  const textRef = useRef<HTMLTextAreaElement>(null)
  const exported = exp.reply?.success ? exp.reply : undefined
  const one = kind === 'actions' ? 'action' : 'sequence'

  const download = () => {
    if (!exported?.yaml) return
    const url = URL.createObjectURL(new Blob([exported.yaml], { type: 'text/yaml' }))
    const a = document.createElement('a')
    a.href = url
    a.download = exported.filename || `lr-control-${kind}.yaml`
    a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
  const copy = () => {
    if (!exported?.yaml) return
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(exported.yaml)
    } else {
      // No clipboard API over plain http: select the text and copy that.
      textRef.current?.select()
      document.execCommand('copy')
    }
  }

  return (
    <div className="ctl-section">
      <div className="ctl-row">
        <button className="ctl-btn" disabled={!connected || !selectedId} onClick={() => exp.send({ op: 'export', kind, ids: [selectedId] })}>
          export {one} as YAML
        </button>
        <button className="ctl-btn" disabled={!connected} onClick={() => exp.send({ op: 'export', kind, ids: [] })}>
          export all {kind}
        </button>
        <button className="ctl-btn" onClick={() => setImporting(v => !v)}>{importing ? 'hide import' : 'import YAML…'}</button>
      </div>
      {exp.reply && !exp.reply.success && <ReplyStatus r={exp} />}
      {exported && (
        <div className="ctl-yaml-box">
          <div className="ctl-row">
            <span className="ctl-hint">{exported.filename}</span>
            <button className="ctl-btn" onClick={download}>download</button>
            <button className="ctl-btn" onClick={copy}>copy</button>
            <button className="ctl-btn" onClick={exp.clear}>close</button>
          </div>
          <textarea ref={textRef} className="ctl-textarea" readOnly value={exported.yaml} rows={12} />
        </div>
      )}
      {importing && (
        <div className="ctl-yaml-box">
          <div className="ctl-hint">
            Paste YAML exported from a definer or composer, or pick a file. Actions and sequences with the same id are replaced.
          </div>
          <input
            type="file"
            accept=".yaml,.yml,text/yaml,text/plain"
            onChange={e => {
              const f = e.target.files?.[0]
              if (f) f.text().then(setText)
            }}
          />
          <textarea
            className="ctl-textarea"
            value={text}
            rows={10}
            placeholder={'format: lr-control-v1\nactions:\n  - id: …'}
            onChange={e => setText(e.target.value)}
          />
          <div className="ctl-row">
            <button className="ctl-btn" disabled={!connected || !text.trim() || imp.pending} onClick={() => imp.send({ op: 'import', yaml: text })}>
              import
            </button>
          </div>
        </div>
      )}
      <ReplyStatus r={imp} working="importing…" />
    </div>
  )
}

// useLibraryEditor is the select/draft/save cycle the definer and composer
// share: the selected item ('' for a new one), an editable copy of it, and
// whether that copy has unsaved changes. The draft reloads from the library
// whenever it isn't being edited.
function useLibraryEditor<T extends { id: string }>(items: T[], blank: () => T) {
  const [selected, setSelected] = useState(items[0]?.id ?? '')
  const original = items.find(x => x.id === selected)
  const [draft, setDraft] = useState<T>(() => (original ? clone(original) : blank()))
  const [dirty, setDirty] = useState(false)

  useEffect(() => {
    if (!dirty) setDraft(original ? clone(original) : blank())
    // blank is a fresh-object factory; only the selection and library matter.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [original, dirty])

  return {
    selected,
    original,
    draft,
    dirty,
    select: (id: string) => {
      if (dirty && !window.confirm('Discard your unsaved changes?')) return
      setDirty(false)
      setSelected(id)
    },
    update: (fn: (d: T) => T) => {
      setDraft(fn)
      setDirty(true)
    },
    // settle ends an edit after a save or delete, moving the selection.
    settle: (id: string) => {
      setDirty(false)
      setSelected(id)
    },
    revert: () => setDirty(false),
  }
}

function BuiltinsHelp({ lib }: { lib: ControlLibraryMsg }) {
  return (
    <details className="ctl-help">
      <summary>values: {'{{name}}'} references and built-ins</summary>
      <p>
        Any value can refer to a control as {'{{name}}'}. Values an instruction saves (save_as — launch-fc saves the
        new federation-command as {'{{fc}}'} by default) can be used by later instructions and steps. Built-ins:
      </p>
      <ul>
        {(lib.builtins ?? []).map(b => (
          <li key={b.name}>
            <code>{`{{${b.name}}}`}</code> — {b.help} (now: <code>{b.value}</code>)
          </li>
        ))}
      </ul>
    </details>
  )
}

// ---- Definer ----

const blankAction = (): ControlActionDef => ({ id: '', name: '', description: '', controls: [], do: [] })

function Definer({ lib, request, replies, connected }: {
  lib: ControlLibraryMsg
  request: ControlRequest
  replies: Record<string, ControlLibReply>
  connected: boolean
}) {
  const actions = lib.actions ?? []
  const ops = lib.ops ?? []
  const lrOps = ops.filter(o => !o.op.startsWith('robot.'))
  const robotOps = ops.filter(o => o.op.startsWith('robot.'))
  const ed = useLibraryEditor(actions, blankAction)
  const save = useRequest(request, replies)
  const pendingId = useRef('')
  const [newOp, setNewOp] = useState('fc-send')
  const [newArg, setNewArg] = useState<Record<number, string>>({})
  const d = ed.draft

  // A save or delete that went through ends the edit.
  useEffect(() => {
    const r = save.reply
    if (!r?.success) return
    if (r.op === 'save-action') ed.settle(pendingId.current)
    if (r.op === 'delete-action') ed.settle(actions.find(a => a.id !== pendingId.current)?.id ?? '')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [save.reply])

  const opSpec = (op: string): ControlOpSpec | undefined => ops.find(o => o.op === op)
  const setInstr = (i: number, fn: (x: ControlInstruction) => ControlInstruction) =>
    ed.update(a => ({ ...a, do: a.do.map((x, j) => (j === i ? fn(x) : x)) }))
  const moveInstr = (i: number, by: number) =>
    ed.update(a => {
      const list = a.do.slice()
      const [x] = list.splice(i, 1)
      list.splice(i + by, 0, x)
      return { ...a, do: list }
    })
  const changeOp = (i: number, op: string) =>
    setInstr(i, x => {
      // Keep the arguments the new op also takes (all of them, if it's one
      // whose arguments aren't known).
      const spec = opSpec(op)
      const next: ControlInstruction = { op }
      for (const [k, v] of Object.entries(x)) {
        if (k !== 'op' && (!spec || (spec.args ?? []).some(a => a.name === k))) next[k] = v
      }
      return next
    })

  const onSave = () => {
    const action: ControlActionDef = {
      ...d,
      controls: (d.controls ?? []).filter(c => c.name.trim() !== ''),
      // Empty arguments mean "use the op's default".
      do: d.do.map(x => {
        const out: ControlInstruction = { op: x.op.trim() }
        for (const [k, v] of Object.entries(x)) if (k !== 'op' && v !== '') out[k] = v
        return out
      }),
    }
    pendingId.current = action.id
    save.send({ op: 'save-action', action, previous_id: ed.original?.id ?? '' })
  }
  const onDelete = () => {
    if (!ed.original || !window.confirm(`Delete the action "${ed.original.name}"?`)) return
    pendingId.current = ed.original.id
    save.send({ op: 'delete-action', id: ed.original.id })
  }

  const opOptions = (
    <>
      <optgroup label="local-representative">
        {lrOps.map(o => <option key={o.op} value={o.op}>{o.op}</option>)}
      </optgroup>
      {robotOps.length > 0 && (
        <optgroup label="robot">
          {robotOps.map(o => <option key={o.op} value={o.op}>{o.op}</option>)}
        </optgroup>
      )}
    </>
  )

  return (
    <div className="ctl-v1-view">
      <div className="ctl-row">
        <select className="ctl-select" value={ed.selected} onChange={e => ed.select(e.target.value)}>
          {actions.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}
          <option value="">(new action)</option>
        </select>
        <button className="ctl-btn" onClick={() => ed.select('')}>new</button>
      </div>

      <div className="ctl-section">
        <Field label="id">
          <input className="ctl-input" value={d.id} placeholder="e.g. launch-fc" onChange={e => ed.update(a => ({ ...a, id: e.target.value }))} />
        </Field>
        <Field label="name">
          <input className="ctl-input" value={d.name} onChange={e => ed.update(a => ({ ...a, name: e.target.value }))} />
        </Field>
        <Field label="description">
          <input className="ctl-input" value={d.description ?? ''} onChange={e => ed.update(a => ({ ...a, description: e.target.value }))} />
        </Field>
      </div>

      <div className="ctl-section">
        <div className="ctl-section-title">controls</div>
        <div className="ctl-hint">What a composer step can set. Instructions use them as {'{{name}}'}.</div>
        <ControlsEditor controls={d.controls ?? []} onChange={controls => ed.update(a => ({ ...a, controls }))} />
      </div>

      <div className="ctl-section">
        <div className="ctl-section-title">instructions</div>
        {!lib.robot_ops && (
          <div className="ctl-hint ctl-hint-warn">
            The robot hasn't said which ops it has yet (it isn't connected), so robot.&lt;op&gt; instructions are listed
            with whatever arguments they have and checked when they run.
          </div>
        )}
        <ol className="ctl-instrs">
          {d.do.map((x, i) => {
            const spec = opSpec(x.op)
            const known = new Set((spec?.args ?? []).map(a => a.name))
            const extra = Object.keys(x).filter(k => k !== 'op' && !known.has(k))
            return (
              <li className="ctl-instr" key={i}>
                <div className="ctl-row">
                  <span className="ctl-num">{i + 1}.</span>
                  <select className="ctl-select" value={x.op} onChange={e => changeOp(i, e.target.value)}>
                    {!spec && <option value={x.op}>{x.op} (not known)</option>}
                    {opOptions}
                  </select>
                  <button className="ctl-btn ctl-icon-btn" disabled={i === 0} title="move up" onClick={() => moveInstr(i, -1)}>↑</button>
                  <button className="ctl-btn ctl-icon-btn" disabled={i === d.do.length - 1} title="move down" onClick={() => moveInstr(i, 1)}>↓</button>
                  <button className="ctl-btn ctl-icon-btn" title="remove" onClick={() => ed.update(a => ({ ...a, do: a.do.filter((_, j) => j !== i) }))}>✕</button>
                </div>
                {spec && <div className="ctl-hint">{spec.summary}</div>}
                {(spec?.args ?? []).map(arg => (
                  <Field key={arg.name} label={arg.name + (arg.required ? ' *' : '')}>
                    <input
                      className="ctl-input"
                      value={x[arg.name] ?? ''}
                      placeholder={arg.default ? `${arg.default} (default)` : arg.help}
                      title={arg.help}
                      onChange={e => setInstr(i, y => ({ ...y, [arg.name]: e.target.value }))}
                    />
                  </Field>
                ))}
                {extra.map(k => (
                  <Field key={k} label={spec ? `${k} (not an argument of ${x.op})` : k}>
                    <div className="ctl-row">
                      <input
                        className={`ctl-input${spec ? ' ctl-input-bad' : ''}`}
                        value={x[k]}
                        onChange={e => setInstr(i, y => ({ ...y, [k]: e.target.value }))}
                      />
                      <button
                        className="ctl-btn ctl-icon-btn"
                        title="remove this argument"
                        onClick={() => setInstr(i, y => {
                          const next = { ...y }
                          delete next[k]
                          return next
                        })}
                      >
                        ✕
                      </button>
                    </div>
                  </Field>
                ))}
                {!spec && (
                  <div className="ctl-row">
                    <input
                      className="ctl-input ctl-input-name"
                      placeholder="argument"
                      value={newArg[i] ?? ''}
                      onChange={e => setNewArg(v => ({ ...v, [i]: e.target.value }))}
                    />
                    <button
                      className="ctl-btn"
                      disabled={!(newArg[i] ?? '').trim() || (newArg[i] ?? '').trim() === 'op'}
                      onClick={() => {
                        setInstr(i, y => ({ ...y, [(newArg[i] ?? '').trim()]: '' }))
                        setNewArg(v => ({ ...v, [i]: '' }))
                      }}
                    >
                      + add argument
                    </button>
                  </div>
                )}
              </li>
            )
          })}
        </ol>
        <div className="ctl-row">
          <select className="ctl-select" value={newOp} onChange={e => setNewOp(e.target.value)}>{opOptions}</select>
          <button className="ctl-btn" onClick={() => ed.update(a => ({ ...a, do: [...a.do, { op: newOp }] }))}>+ add instruction</button>
          {!lib.robot_ops && (
            <button
              className="ctl-btn"
              title="a robot op by name, while the robot isn't connected"
              onClick={() => {
                const name = window.prompt('Robot op (as on the robot tab\'s definer), e.g. key, type, click-text:')
                if (name?.trim()) ed.update(a => ({ ...a, do: [...a.do, { op: `robot.${name.trim()}` }] }))
              }}
            >
              + add robot op…
            </button>
          )}
        </div>
        <div className="ctl-hint">{opSpec(newOp)?.summary}</div>
      </div>

      <BuiltinsHelp lib={lib} />

      <div className="ctl-row">
        <button className="sys-btn sys-btn-launch" disabled={!connected || !ed.dirty || save.pending} onClick={onSave}>
          {ed.original ? 'save action' : 'create action'}
        </button>
        <button className="ctl-btn" disabled={!ed.dirty} onClick={ed.revert}>revert</button>
        <button className="sys-btn sys-btn-terminate" disabled={!connected || !ed.original || save.pending} onClick={onDelete}>delete</button>
      </div>
      <ReplyStatus r={save} working="saving…" />

      <YamlTools kind="actions" selectedId={ed.original?.id ?? ''} request={request} replies={replies} connected={connected} />
    </div>
  )
}

// ---- Composer ----

const blankSequence = (): ControlSequenceDef => ({ id: '', name: '', description: '', controls: [], steps: [], recordings: [] })

function Composer({ lib, request, replies, connected, node, nodes }: {
  lib: ControlLibraryMsg
  request: ControlRequest
  replies: Record<string, ControlLibReply>
  connected: boolean
  node: string
  nodes: string[] // connected to agent-coordinator
}) {
  const sequences = lib.sequences ?? []
  const actions = lib.actions ?? []
  const actionById = new Map(actions.map(a => [a.id, a]))
  const ed = useLibraryEditor(sequences, blankSequence)
  const save = useRequest(request, replies)
  const pendingId = useRef('')
  const [addAction, setAddAction] = useState('')
  const [selRec, setSelRec] = useState<number | null>(null)
  const [drag, setDrag] = useState<{ k: number; end: RecEnd } | null>(null)
  const stepEls = useRef<(HTMLLIElement | null)[]>([])
  const d = ed.draft
  const recs = d.recordings ?? []
  const recsRef = useRef(recs)
  recsRef.current = recs
  const recLayout = recordLanes(recs)
  const problems = recordProblems(recs, d.steps.length)
  const nodeControls = (d.controls ?? []).filter(c => c.type === 'node' && c.name)

  useEffect(() => {
    const r = save.reply
    if (!r?.success) return
    if (r.op === 'save-sequence') ed.settle(pendingId.current)
    if (r.op === 'delete-sequence') ed.settle(sequences.find(q => q.id !== pendingId.current)?.id ?? '')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [save.reply])
  useEffect(() => {
    setSelRec(null)
  }, [ed.selected])
  // Dragging a bar's end moves its first or last step to the step under
  // the pointer, never past its other end.
  useEffect(() => {
    if (!drag) return
    const move = (e: PointerEvent) => {
      const tops = stepEls.current.slice(0, d.steps.length).map(el => el?.getBoundingClientRect().top ?? Infinity)
      const step = Math.max(1, tops.filter(top => top <= e.clientY).length)
      const b = recsRef.current[drag.k]
      if (!b) return
      // Only on a change: any update marks the sequence edited.
      if (drag.end === 'from' && b.from !== Math.min(step, b.to)) setRec(drag.k, { from: Math.min(step, b.to) })
      if (drag.end === 'to' && b.to !== Math.max(step, b.from)) setRec(drag.k, { to: Math.max(step, b.from) })
    }
    const up = () => setDrag(null)
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
    return () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
  }, [drag, d.steps.length]) // eslint-disable-line react-hooks/exhaustive-deps

  const setRecs = (fn: (r: ControlRecordBlock[]) => ControlRecordBlock[]) => ed.update(q => ({ ...q, recordings: fn(q.recordings ?? []) }))
  const setRec = (k: number, patch: Partial<ControlRecordBlock>) => setRecs(r => r.map((b, j) => (j === k ? { ...b, ...patch } : b)))
  // addRec starts a one-step recording at step (1-based) of the first node
  // not already recorded there: this node, a node control, a connected node.
  const addRec = (step: number) => {
    const taken = new Set(recs.filter(b => covers(b, step)).map(b => b.node.trim()))
    const who = ['{{this_node}}', ...nodeControls.map(c => `{{${c.name}}}`), ...nodes].find(n => !taken.has(n)) ?? ''
    setSelRec(recs.length)
    setRecs(r => [...r, { node: who, from: step, to: step }])
  }
  const removeRec = (k: number) => {
    setSelRec(null)
    setRecs(r => r.filter((_, j) => j !== k))
  }
  // removeStep takes step i (0-based) out, pulling the recordings after it
  // up a step; one that recorded only that step goes with it.
  const removeStep = (i: number) => {
    const n = i + 1
    setSelRec(null)
    ed.update(q => ({
      ...q,
      steps: q.steps.filter((_, j) => j !== i),
      recordings: (q.recordings ?? [])
        .filter(b => !(b.from === n && b.to === n))
        .map(b => ({ ...b, from: b.from > n ? b.from - 1 : b.from, to: b.to >= n ? b.to - 1 : b.to })),
    }))
  }

  const setSteps = (fn: (steps: ControlStepRef[]) => ControlStepRef[]) => ed.update(q => ({ ...q, steps: fn(q.steps) }))
  const setStep = (i: number, patch: Partial<ControlStepRef>) => setSteps(s => s.map((st, j) => (j === i ? { ...st, ...patch } : st)))
  const setWith = (i: number, name: string, value: string) =>
    setSteps(s =>
      s.map((st, j) => {
        if (j !== i) return st
        const w = { ...(st.with ?? {}) }
        // An empty value means "use the action's default".
        if (value === '') delete w[name]
        else w[name] = value
        return { ...st, with: w }
      }),
    )
  const moveStep = (i: number, by: number) =>
    setSteps(s => {
      const list = s.slice()
      const [st] = list.splice(i, 1)
      list.splice(i + by, 0, st)
      return list
    })
  const chosen = addAction || actions[0]?.id || ''

  const onSave = () => {
    const sequence: ControlSequenceDef = {
      ...d,
      controls: (d.controls ?? []).filter(c => c.name.trim() !== ''),
      recordings: recs.map(b => ({ ...b, node: b.node.trim(), label: b.label?.trim() || undefined })),
    }
    pendingId.current = sequence.id
    save.send({ op: 'save-sequence', sequence, previous_id: ed.original?.id ?? '' })
  }
  const onDelete = () => {
    if (!ed.original || !window.confirm(`Delete the sequence "${ed.original.name}"?`)) return
    pendingId.current = ed.original.id
    save.send({ op: 'delete-sequence', id: ed.original.id })
  }

  return (
    <div className="ctl-v1-view">
      <div className="ctl-row">
        <select className="ctl-select" value={ed.selected} onChange={e => ed.select(e.target.value)}>
          {sequences.map(q => <option key={q.id} value={q.id}>{q.name}</option>)}
          <option value="">(new sequence)</option>
        </select>
        <button className="ctl-btn" onClick={() => ed.select('')}>new</button>
      </div>

      <div className="ctl-section">
        <Field label="id">
          <input className="ctl-input" value={d.id} placeholder="e.g. fc-robot-handoff" onChange={e => ed.update(q => ({ ...q, id: e.target.value }))} />
        </Field>
        <Field label="name">
          <input className="ctl-input" value={d.name} onChange={e => ed.update(q => ({ ...q, name: e.target.value }))} />
        </Field>
        <Field label="description">
          <input className="ctl-input" value={d.description ?? ''} onChange={e => ed.update(q => ({ ...q, description: e.target.value }))} />
        </Field>
      </div>

      <div className="ctl-section">
        <div className="ctl-section-title">sequence controls</div>
        <div className="ctl-hint">The runner asks for these; steps use them as {'{{name}}'}.</div>
        <ControlsEditor controls={d.controls ?? []} onChange={controls => ed.update(q => ({ ...q, controls }))} />
      </div>

      <div className="ctl-section">
        <div className="ctl-section-title">steps</div>
        <div className="ctl-hint">
          The slim bars to the right of the steps are recordings: each records one node's screen from its first step to its
          last, up to {MAX_PARALLEL_RECORDINGS} side by side, never the same node twice at a step. ⏺ starts one at that step;
          drag a bar's top or bottom end to change the steps it spans.
        </div>
        <ol className="ctl-instrs ctl-comp-steps ctl-rec-grid" style={{ gridTemplateColumns: recGridColumns(recLayout.lanes, d.steps.length > 0) }}>
          {d.steps.map((st, i) => {
            const a = actionById.get(st.action)
            const atStep = recs.filter(b => covers(b, i + 1)).length
            return [
              <li className="ctl-comp-step" key={i} style={stepCell(i)} ref={el => { stepEls.current[i] = el }}>
                <div className="ctl-instr">
                  <div className="ctl-row">
                    <span className="ctl-num">{i + 1}.</span>
                    <span className="ctl-step-action" title={a?.description}>{a ? a.name : `missing action "${st.action}"`}</span>
                    <button className="ctl-btn ctl-icon-btn" disabled={i === 0} title="move up" onClick={() => moveStep(i, -1)}>↑</button>
                    <button className="ctl-btn ctl-icon-btn" disabled={i === d.steps.length - 1} title="move down" onClick={() => moveStep(i, 1)}>↓</button>
                    <button className="ctl-btn ctl-icon-btn" title="remove this step" onClick={() => removeStep(i)}>✕</button>
                  </div>
                  <Field label="label">
                    <input className="ctl-input" value={st.label ?? ''} placeholder={a?.name ?? ''} onChange={e => setStep(i, { label: e.target.value || undefined })} />
                  </Field>
                  {(a?.controls ?? []).map(c => (
                    <Field key={c.name} label={c.label || c.name}>
                      <input
                        className="ctl-input"
                        // A node control's value is usually a sequence node control's {{name}}; the
                        // connected nodes are offered too.
                        list={c.type === 'node' ? 'ctl-node-list' : undefined}
                        value={st.with?.[c.name] ?? ''}
                        placeholder={c.default ? `${c.default} (default)` : c.help ?? ''}
                        title={c.help}
                        onChange={e => setWith(i, c.name, e.target.value)}
                      />
                    </Field>
                  ))}
                </div>
              </li>,
              <li className="ctl-rec-add-cell" key={`add-${i}`} style={{ gridRow: i + 1, gridColumn: 2 + recLayout.lanes }}>
                <button
                  className="ctl-rec-add"
                  disabled={atStep >= MAX_PARALLEL_RECORDINGS}
                  title={atStep >= MAX_PARALLEL_RECORDINGS ? `${MAX_PARALLEL_RECORDINGS} recordings already run at this step` : `start a recording at step ${i + 1}`}
                  onClick={() => addRec(i + 1)}
                >
                  ⏺
                </button>
              </li>,
            ]
          })}
          <RecordBars
            blocks={recs}
            layout={recLayout}
            nSteps={d.steps.length}
            selected={selRec}
            onSelect={setSelRec}
            onGrab={(k, end) => {
              setSelRec(k)
              setDrag({ k, end })
            }}
          />
          {d.steps.length === 0 && <li className="ctl-empty" style={stepCell(0)}>No steps yet — add actions below.</li>}
        </ol>
        <datalist id="ctl-node-list">
          <option value="{{this_node}}">{node}</option>
          {nodeControls.map(c => <option key={c.name} value={`{{${c.name}}}`} />)}
          {nodes.map(n => <option key={n} value={n} />)}
        </datalist>
        <div className="ctl-row">
          <select className="ctl-select" value={chosen} onChange={e => setAddAction(e.target.value)}>
            {actions.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}
          </select>
          <button className="ctl-btn" disabled={!chosen} onClick={() => setSteps(s => [...s, { action: chosen }])}>+ add step</button>
        </div>
        <div className="ctl-hint">{actionById.get(chosen)?.description}</div>
      </div>

      <div className="ctl-section">
        <div className="ctl-section-title">recordings</div>
        {recs.length === 0 && <div className="ctl-hint">None — the run records no screens. Press ⏺ beside a step to start one there.</div>}
        {recs.map((b, k) => {
          const stepOptions = d.steps.map((_, j) => <option key={j} value={j + 1}>{j + 1}</option>)
          return (
            <div key={k} className={`ctl-row ctl-rec-edit${selRec === k ? ' ctl-rec-edit-selected' : ''}`} onClick={() => setSelRec(k)}>
              <span className="ctl-rec-swatch" style={{ background: recColor(k) }} />
              <input
                className="ctl-input"
                list="ctl-node-list"
                value={b.node}
                placeholder="node"
                title={`whose screen: a node connected to agent-coordinator, {{this_node}} (${node}) or a node control's {{name}}`}
                onChange={e => setRec(k, { node: e.target.value })}
              />
              <span className="ctl-hint">steps</span>
              <select className="ctl-select" value={b.from} title="the first step it records" onChange={e => {
                const from = Number(e.target.value)
                setRec(k, { from, to: Math.max(from, b.to) })
              }}>
                {stepOptions}
              </select>
              <span className="ctl-hint">to</span>
              <select className="ctl-select" value={b.to} title="the last step it records" onChange={e => {
                const to = Number(e.target.value)
                setRec(k, { to, from: Math.min(to, b.from) })
              }}>
                {stepOptions}
              </select>
              <input className="ctl-input" value={b.label ?? ''} placeholder="label (optional)" onChange={e => setRec(k, { label: e.target.value || undefined })} />
              <button className="ctl-btn ctl-icon-btn" title="remove this recording" onClick={e => {
                e.stopPropagation()
                removeRec(k)
              }}>✕</button>
            </div>
          )
        })}
        {problems.map(p => <div key={p} className="ctl-run-error-msg">{p}</div>)}
        {recs.length > 0 && (
          <div className="ctl-hint">
            Each is saved to {node}'s files tab once it stops — another node's is recorded by that node's robot and copied here
            through agent-coordinator — and played back on the runner. Recordings keep their step numbers when steps move.
          </div>
        )}
      </div>

      <BuiltinsHelp lib={lib} />

      <div className="ctl-row">
        <button
          className="sys-btn sys-btn-launch"
          disabled={!connected || !ed.dirty || save.pending || problems.length > 0}
          title={problems.length > 0 ? 'fix the recordings first' : undefined}
          onClick={onSave}
        >
          {ed.original ? 'save sequence' : 'create sequence'}
        </button>
        <button className="ctl-btn" disabled={!ed.dirty} onClick={ed.revert}>revert</button>
        <button className="sys-btn sys-btn-terminate" disabled={!connected || !ed.original || save.pending} onClick={onDelete}>delete</button>
      </div>
      <ReplyStatus r={save} working="saving…" />

      <YamlTools kind="sequences" selectedId={ed.original?.id ?? ''} request={request} replies={replies} connected={connected} />
    </div>
  )
}

// ---- The v1 sub-tab ----

type View = 'runner' | 'definer' | 'composer'
const VIEWS: View[] = ['runner', 'definer', 'composer']

export function ControlV1(props: ControlV1Props) {
  const { connected, lib, request, replies, node } = props
  const [view, setView] = useState<View>('runner')
  const restore = useRequest(request, replies)

  return (
    <div className="ctl-v1">
      <div className="ctl-subtabs ctl-v1-views">
        {VIEWS.map(v => (
          <button key={v} className={`ctl-subtab${view === v ? ' ctl-subtab-active' : ''}`} onClick={() => setView(v)}>
            {v}
          </button>
        ))}
      </div>
      {lib?.note && <div className="ctl-hint ctl-hint-warn">{lib.note}</div>}
      {view === 'runner' && <Runner {...props} />}
      {view !== 'runner' && !lib && <div className="ctl-empty">{connected ? 'loading the library…' : 'connecting…'}</div>}
      {lib && view === 'definer' && <Definer lib={lib} request={request} replies={replies} connected={connected} />}
      {lib && view === 'composer' && (
        <Composer lib={lib} request={request} replies={replies} connected={connected} node={node} nodes={props.state?.nodes ?? []} />
      )}
      {lib && view !== 'runner' && (
        <div className="ctl-footer">
          <span className="ctl-hint">{lib.path ? `saved in ${lib.path} on ${node}` : 'kept in memory only (not saved to disk)'}</span>
          <button
            className="ctl-btn"
            disabled={!connected || restore.pending}
            title="put the built-in example actions and sequences back, replacing edited copies with the same ids"
            onClick={() => {
              if (window.confirm('Restore the built-in examples? Edited copies with the same ids are replaced.')) restore.send({ op: 'restore-examples' })
            }}
          >
            restore examples
          </button>
          <ReplyStatus r={restore} />
        </div>
      )}
    </div>
  )
}
