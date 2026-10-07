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
//             whose screens to record, and which leading steps run first
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
  onRun: (sequence: string, controls: Record<string, string>) => void
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

// ---- Runner ----

function Runner({ connected, state, lib, replies, request, robotHealthy, onRun, onCancel, onContinue, fileUrl, node }: ControlV1Props) {
  const sequences = state?.sequences ?? []
  const nodes = state?.nodes ?? []
  const [selectedId, setSelectedId] = useState('')
  const [values, setValues] = useState<Record<string, Record<string, string>>>({})
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
            onClick={() => onRun(seq.id, Object.fromEntries(controls.map(c => [c.name, valueOf(c)])))}
          >
            run
          </button>
        )}
      </div>
      {running && !thisRun && run && <div className="ctl-hint">running: {run.name}</div>}
      {seq.description && <div className="ctl-seq-desc">{seq.description}</div>}
      {seq.record && seq.record.length > 0 && (
        <div className="ctl-seq-desc">
          records the screen of: {seq.record.map(w => (w === 'robot' ? `${node} (its robot)` : w)).join(', ')} — saved to the files tab and played back below once the run ends
        </div>
      )}
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
      <ol className="ctl-steps">
        {steps.map((st, i) => {
          const status = st.status ?? 'pending'
          return (
            <li key={i} className={`ctl-step ctl-step-${status}`}>
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
      </ol>
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

const blankSequence = (): ControlSequenceDef => ({ id: '', name: '', description: '', controls: [], record: [], steps: [] })

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
  const d = ed.draft

  useEffect(() => {
    const r = save.reply
    if (!r?.success) return
    if (r.op === 'save-sequence') ed.settle(pendingId.current)
    if (r.op === 'delete-sequence') ed.settle(sequences.find(q => q.id !== pendingId.current)?.id ?? '')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [save.reply])

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
  const recordsRobot = (d.record ?? []).includes('robot')
  const chosen = addAction || actions[0]?.id || ''

  const onSave = () => {
    const sequence: ControlSequenceDef = { ...d, controls: (d.controls ?? []).filter(c => c.name.trim() !== '') }
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
        <label className="ctl-check">
          <input
            type="checkbox"
            checked={recordsRobot}
            onChange={e => ed.update(q => ({ ...q, record: e.target.checked ? ['robot'] : [] }))}
          />
          record {node}'s screen across the run (saved to the files tab)
        </label>
      </div>

      <div className="ctl-section">
        <div className="ctl-section-title">sequence controls</div>
        <div className="ctl-hint">The runner asks for these; steps use them as {'{{name}}'}.</div>
        <ControlsEditor controls={d.controls ?? []} onChange={controls => ed.update(q => ({ ...q, controls }))} />
      </div>

      <div className="ctl-section">
        <div className="ctl-section-title">steps</div>
        <ol className="ctl-instrs">
          {d.steps.map((st, i) => {
            const a = actionById.get(st.action)
            const canRunFirst = d.steps.slice(0, i).every(p => p.before_recording)
            return (
              <li className="ctl-instr" key={i}>
                <div className="ctl-row">
                  <span className="ctl-num">{i + 1}.</span>
                  <span className="ctl-step-action" title={a?.description}>{a ? a.name : `missing action "${st.action}"`}</span>
                  <button className="ctl-btn ctl-icon-btn" disabled={i === 0} title="move up" onClick={() => moveStep(i, -1)}>↑</button>
                  <button className="ctl-btn ctl-icon-btn" disabled={i === d.steps.length - 1} title="move down" onClick={() => moveStep(i, 1)}>↓</button>
                  <button className="ctl-btn ctl-icon-btn" title="remove this step" onClick={() => setSteps(s => s.filter((_, j) => j !== i))}>✕</button>
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
                {recordsRobot && (canRunFirst || st.before_recording) && (
                  <label className="ctl-check">
                    <input type="checkbox" checked={!!st.before_recording} onChange={e => setStep(i, { before_recording: e.target.checked || undefined })} />
                    run before the recording starts (leading steps only)
                  </label>
                )}
              </li>
            )
          })}
          {d.steps.length === 0 && <li className="ctl-empty">No steps yet — add actions below.</li>}
        </ol>
        <datalist id="ctl-node-list">
          {(d.controls ?? []).filter(c => c.type === 'node' && c.name).map(c => <option key={c.name} value={`{{${c.name}}}`} />)}
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

      <BuiltinsHelp lib={lib} />

      <div className="ctl-row">
        <button className="sys-btn sys-btn-launch" disabled={!connected || !ed.dirty || save.pending} onClick={onSave}>
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
