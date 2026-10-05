import { Fragment, useEffect, useRef, useState } from 'react'
import type { PointerEvent as ReactPointerEvent, ReactNode, RefObject } from 'react'
import type {
  ActionDef,
  Control,
  Instruction,
  OpSpec,
  ReprStatus,
  Seq2ReplyMsg,
  SeqLibraryMsg,
  SequenceStepDef,
  SequenceV2,
  StepRef,
} from './types'
import { PreviewView, SaveToFile, STEP_ICONS } from './shared'
import type { SaveStatus, SeqRun, StepStatus } from './shared'

// The sequence-v2 tab (see seqv2.go): three sub-tabs over the library the
// backend keeps --
//   definer:  actions -- reusable building blocks exposing controls, each a
//             list of primitive instructions (ops)
//   composer: sequences -- actions dragged into order, with values for
//             their controls
//   runner:   run a sequence with chosen control values, recorded
// Both actions and sequences import from and export to YAML.

type Request = (type: string, payload: Record<string, unknown>) => string

// useRequest tracks one library request at a time: the latest sent and its
// reply, once it arrives.
function useRequest(request: Request, replies: Record<string, Seq2ReplyMsg>) {
  const [req, setReq] = useState<string | null>(null)
  const reply = req ? replies[req] : undefined
  return {
    reply,
    pending: req !== null && !reply,
    send: (type: string, payload: Record<string, unknown>) => setReq(request(type, payload)),
    clear: () => setReq(null),
  }
}

type Tracked = ReturnType<typeof useRequest>

function ReplyStatus({ r, working = 'Working…' }: { r: Tracked; working?: string }) {
  if (r.pending) return <div className="robot-status">{working}</div>
  if (!r.reply) return null
  if (!r.reply.success) return <div className="robot-status robot-status-error">{r.reply.error}</div>
  return r.reply.message ? <div className="robot-status seq2-ok">✓ {r.reply.message}</div> : null
}

function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

// ---- Shared editors ----

// ControlsEditor edits a list of controls: name (as used in {{name}}),
// label, default and help.
function ControlsEditor({ controls, onChange }: { controls: Control[]; onChange: (c: Control[]) => void }) {
  const set = (i: number, patch: Partial<Control>) => onChange(controls.map((c, j) => (j === i ? { ...c, ...patch } : c)))
  return (
    <div className="seq2-controls">
      {controls.map((c, i) => (
        <div className="seq2-control" key={i}>
          <input className="seq2-input seq2-input-name" placeholder="name" value={c.name} onChange={(e) => set(i, { name: e.target.value })} />
          <input className="seq2-input" placeholder="label" value={c.label ?? ''} onChange={(e) => set(i, { label: e.target.value })} />
          <input className="seq2-input" placeholder="default" value={c.default ?? ''} onChange={(e) => set(i, { default: e.target.value })} />
          <input className="seq2-input" placeholder="help" value={c.help ?? ''} onChange={(e) => set(i, { help: e.target.value })} />
          <button className="btn-secondary seq2-icon-btn" title="remove this control" onClick={() => onChange(controls.filter((_, j) => j !== i))}>
            ✕
          </button>
        </div>
      ))}
      <button className="btn-secondary seq2-add" onClick={() => onChange([...controls, { name: '' }])}>
        + Add control
      </button>
    </div>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="seq2-field">
      <span className="seq2-field-name">{label}</span>
      {children}
    </label>
  )
}

// YamlTools exports the selected item (or everything) of one kind as YAML,
// to download or copy, and imports YAML pasted or picked from a file.
function YamlTools({
  kind,
  selectedId,
  request,
  replies,
  connected,
}: {
  kind: 'actions' | 'sequences'
  selectedId: string
  request: Request
  replies: Record<string, Seq2ReplyMsg>
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
    a.download = exported.filename || `ianar-${kind}.yaml`
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
    <div className="seq2-yaml">
      <div className="seq2-row">
        <button
          className="btn-secondary"
          disabled={!connected || !selectedId}
          onClick={() => exp.send('seq2-export', { kind, ids: [selectedId] })}
        >
          Export {one} as YAML
        </button>
        <button className="btn-secondary" disabled={!connected} onClick={() => exp.send('seq2-export', { kind, ids: [] })}>
          Export all {kind}
        </button>
        <button className="btn-secondary" onClick={() => setImporting((v) => !v)}>
          {importing ? 'Hide import' : 'Import YAML…'}
        </button>
      </div>
      {exp.reply && !exp.reply.success && <ReplyStatus r={exp} />}
      {exported && (
        <div className="seq2-yaml-box">
          <div className="seq2-row">
            <span className="seq2-hint">{exported.filename}</span>
            <button className="btn-secondary" onClick={download}>
              Download
            </button>
            <button className="btn-secondary" onClick={copy}>
              Copy
            </button>
            <button className="btn-secondary" onClick={exp.clear}>
              Close
            </button>
          </div>
          <textarea ref={textRef} className="seq2-textarea" readOnly value={exported.yaml} rows={12} />
        </div>
      )}
      {importing && (
        <div className="seq2-yaml-box">
          <div className="seq2-hint">
            Paste YAML exported from the definer or composer, or pick a file. Actions and sequences with the same id are
            replaced.
          </div>
          <input
            type="file"
            accept=".yaml,.yml,text/yaml,text/plain"
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (f) f.text().then(setText)
            }}
          />
          <textarea
            className="seq2-textarea"
            value={text}
            rows={10}
            placeholder={'format: ianar-sequence-v2\nactions:\n  - id: …'}
            onChange={(e) => setText(e.target.value)}
          />
          <div className="seq2-row">
            <button
              className="btn-secondary"
              disabled={!connected || !text.trim() || imp.pending}
              onClick={() => imp.send('seq2-import', { yaml: text })}
            >
              Import
            </button>
          </div>
        </div>
      )}
      <ReplyStatus r={imp} working="Importing…" />
    </div>
  )
}

// useLibraryEditor is the select/draft/save cycle the definer and composer
// share: the selected item ('' for a new one), an editable copy of it, and
// whether that copy has unsaved changes. The draft reloads from the library
// whenever it isn't being edited.
function useLibraryEditor<T extends { id: string }>(items: T[], blank: () => T) {
  const [selected, setSelected] = useState(items[0]?.id ?? '')
  const original = items.find((x) => x.id === selected)
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
      if (dirty && !window.confirm('Discard your unsaved changes?')) return false
      setDirty(false)
      setSelected(id)
      return true
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

// ---- Definer ----

const blankAction = (): ActionDef => ({ id: '', name: '', description: '', controls: [], do: [] })

function Definer({
  lib,
  request,
  replies,
  connected,
}: {
  lib: SeqLibraryMsg
  request: Request
  replies: Record<string, Seq2ReplyMsg>
  connected: boolean
}) {
  const actions = lib.actions ?? []
  const ops = lib.ops ?? []
  const ed = useLibraryEditor(actions, blankAction)
  const save = useRequest(request, replies)
  const pendingId = useRef('')
  const [newOp, setNewOp] = useState('key')
  const d = ed.draft

  // A save or delete that went through ends the edit.
  useEffect(() => {
    const r = save.reply
    if (!r?.success) return
    if (r.op === 'seq2-save-action') ed.settle(pendingId.current)
    if (r.op === 'seq2-delete-action') ed.settle(actions.find((a) => a.id !== pendingId.current)?.id ?? '')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [save.reply])

  const opSpec = (op: string): OpSpec | undefined => ops.find((o) => o.op === op)
  const setInstr = (i: number, fn: (x: Instruction) => Instruction) =>
    ed.update((a) => ({ ...a, do: a.do.map((x, j) => (j === i ? fn(x) : x)) }))
  const moveInstr = (i: number, by: number) =>
    ed.update((a) => {
      const list = a.do.slice()
      const [x] = list.splice(i, 1)
      list.splice(i + by, 0, x)
      return { ...a, do: list }
    })
  const changeOp = (i: number, op: string) =>
    setInstr(i, (x) => {
      // Keep the arguments the new op also takes.
      const next: Instruction = { op }
      for (const arg of opSpec(op)?.args ?? []) if (x[arg.name]) next[arg.name] = x[arg.name]
      return next
    })

  const onSave = () => {
    const action: ActionDef = {
      ...d,
      controls: (d.controls ?? []).filter((c) => c.name.trim() !== ''),
      // Empty arguments mean "use the op's default".
      do: d.do.map((x) => {
        const out: Instruction = { op: x.op }
        for (const [k, v] of Object.entries(x)) if (k !== 'op' && v !== '') out[k] = v
        return out
      }),
    }
    pendingId.current = action.id
    save.send('seq2-save-action', { action, previous_id: ed.original?.id ?? '' })
  }
  const onDelete = () => {
    if (!ed.original || !window.confirm(`Delete the action "${ed.original.name}"?`)) return
    pendingId.current = ed.original.id
    save.send('seq2-delete-action', { id: ed.original.id })
  }

  return (
    <div className="seq2-editor">
      <div className="seq2-row">
        <select className="seq-select" value={ed.selected} onChange={(e) => ed.select(e.target.value)}>
          {actions.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
          <option value="">(new action)</option>
        </select>
        <button className="btn-secondary" onClick={() => ed.select('')}>
          New
        </button>
      </div>

      <div className="seq2-section">
        <Field label="id">
          <input className="seq2-input" value={d.id} placeholder="e.g. open-settings" onChange={(e) => ed.update((a) => ({ ...a, id: e.target.value }))} />
        </Field>
        <Field label="name">
          <input className="seq2-input" value={d.name} onChange={(e) => ed.update((a) => ({ ...a, name: e.target.value }))} />
        </Field>
        <Field label="description">
          <input
            className="seq2-input"
            value={d.description ?? ''}
            onChange={(e) => ed.update((a) => ({ ...a, description: e.target.value }))}
          />
        </Field>
      </div>

      <div className="seq2-section">
        <div className="seq2-section-title">Controls</div>
        <div className="seq2-hint">What a composer step can set. Instructions use them as {'{{name}}'}.</div>
        <ControlsEditor controls={d.controls ?? []} onChange={(controls) => ed.update((a) => ({ ...a, controls }))} />
      </div>

      <div className="seq2-section">
        <div className="seq2-section-title">Instructions</div>
        <ol className="seq2-instrs">
          {d.do.map((x, i) => {
            const spec = opSpec(x.op)
            const known = new Set((spec?.args ?? []).map((a) => a.name))
            const unknown = Object.keys(x).filter((k) => k !== 'op' && !known.has(k))
            return (
              <li className="seq2-instr" key={i}>
                <div className="seq2-row">
                  <span className="seq2-num">{i + 1}.</span>
                  <select className="seq-select" value={x.op} onChange={(e) => changeOp(i, e.target.value)}>
                    {!spec && <option value={x.op}>{x.op} (unknown)</option>}
                    {ops.map((o) => (
                      <option key={o.op} value={o.op}>
                        {o.op}
                      </option>
                    ))}
                  </select>
                  <button className="btn-secondary seq2-icon-btn" disabled={i === 0} title="move up" onClick={() => moveInstr(i, -1)}>
                    ↑
                  </button>
                  <button
                    className="btn-secondary seq2-icon-btn"
                    disabled={i === d.do.length - 1}
                    title="move down"
                    onClick={() => moveInstr(i, 1)}
                  >
                    ↓
                  </button>
                  <button
                    className="btn-secondary seq2-icon-btn"
                    title="remove"
                    onClick={() => ed.update((a) => ({ ...a, do: a.do.filter((_, j) => j !== i) }))}
                  >
                    ✕
                  </button>
                </div>
                {spec && <div className="seq2-hint">{spec.summary}</div>}
                {(spec?.args ?? []).map((arg) => (
                  <Field key={arg.name} label={arg.name + (arg.required ? ' *' : '')}>
                    <input
                      className="seq2-input"
                      value={x[arg.name] ?? ''}
                      placeholder={arg.default ? `${arg.default} (default)` : arg.help}
                      title={arg.help}
                      onChange={(e) => setInstr(i, (y) => ({ ...y, [arg.name]: e.target.value }))}
                    />
                  </Field>
                ))}
                {unknown.map((k) => (
                  <Field key={k} label={`${k} (not an argument of ${x.op})`}>
                    <input
                      className="seq2-input seq2-input-bad"
                      value={x[k]}
                      onChange={(e) => setInstr(i, (y) => ({ ...y, [k]: e.target.value }))}
                    />
                  </Field>
                ))}
              </li>
            )
          })}
        </ol>
        <div className="seq2-row">
          <select className="seq-select" value={newOp} onChange={(e) => setNewOp(e.target.value)}>
            {ops.map((o) => (
              <option key={o.op} value={o.op}>
                {o.op}
              </option>
            ))}
          </select>
          <button className="btn-secondary" onClick={() => ed.update((a) => ({ ...a, do: [...a.do, { op: newOp }] }))}>
            + Add instruction
          </button>
        </div>
        <div className="seq2-hint">{opSpec(newOp)?.summary}</div>
      </div>

      <BuiltinsHelp lib={lib} />

      <div className="seq2-row seq2-actions">
        <button className="btn-secondary seq-run" disabled={!connected || !ed.dirty || save.pending} onClick={onSave}>
          {ed.original ? 'Save action' : 'Create action'}
        </button>
        <button className="btn-secondary" disabled={!ed.dirty} onClick={ed.revert}>
          Revert
        </button>
        <button className="btn-secondary" disabled={!connected || !ed.original || save.pending} onClick={onDelete}>
          Delete
        </button>
      </div>
      <ReplyStatus r={save} working="Saving…" />

      <YamlTools kind="actions" selectedId={ed.original?.id ?? ''} request={request} replies={replies} connected={connected} />
    </div>
  )
}

function BuiltinsHelp({ lib }: { lib: SeqLibraryMsg }) {
  return (
    <details className="seq2-help">
      <summary>Values: {'{{name}}'} references and built-ins</summary>
      <p>
        Any value can refer to a control as {'{{name}}'}, URL-encode it as {'{{name|url}}'}, or take a path's file name as{' '}
        {'{{name|base}}'}. Values an instruction saves (save_as) can be used by later instructions and steps. Built-ins:
      </p>
      <ul>
        {(lib.builtins ?? []).map((b) => (
          <li key={b.name}>
            <code>{`{{${b.name}}}`}</code> — {b.help} (now: <code>{b.value}</code>)
          </li>
        ))}
      </ul>
    </details>
  )
}

// ---- Composer ----

const blankSequence = (): SequenceV2 => ({ id: '', name: '', description: '', controls: [], steps: [] })

type DragItem = { kind: 'action'; actionId: string; label: string } | { kind: 'step'; index: number; label: string }
type DragState = { item: DragItem; x: number; y: number; over: number | null }

// useStepDrag is drag-and-drop onto (and within) a step list, built on
// pointer events so it works with touch as well as a mouse -- the HTML5
// drag-and-drop API doesn't fire on mobile browsers. A drag starts on a
// handle; over is the index the item would be dropped at, or null when the
// pointer is away from the list.
function useStepDrag(listRef: RefObject<HTMLOListElement>, onDrop: (item: DragItem, at: number) => void) {
  const [drag, setDrag] = useState<DragState | null>(null)
  const dragRef = useRef<DragState | null>(null)
  const set = (s: DragState | null) => {
    dragRef.current = s
    setDrag(s)
  }

  const overAt = (x: number, y: number): number | null => {
    const list = listRef.current
    if (!list) return null
    const r = list.getBoundingClientRect()
    if (x < r.left - 40 || x > r.right + 40 || y < r.top - 40 || y > r.bottom + 40) return null
    const items = Array.from(list.querySelectorAll<HTMLElement>('[data-step-index]'))
    for (const el of items) {
      const b = el.getBoundingClientRect()
      if (y < b.top + b.height / 2) return Number(el.dataset.stepIndex)
    }
    return items.length
  }

  // Scroll the panel when dragging near its top or bottom edge.
  const autoScroll = (y: number) => {
    const panel = listRef.current?.closest('.robot-panel')
    if (!panel) return
    const r = panel.getBoundingClientRect()
    if (y < r.top + 48) panel.scrollBy(0, -12)
    else if (y > r.bottom - 48) panel.scrollBy(0, 12)
  }

  const handle = (item: DragItem) => ({
    onPointerDown: (e: ReactPointerEvent<HTMLElement>) => {
      if (e.pointerType === 'mouse' && e.button !== 0) return
      e.preventDefault()
      e.currentTarget.setPointerCapture(e.pointerId)
      set({ item, x: e.clientX, y: e.clientY, over: overAt(e.clientX, e.clientY) })
    },
    onPointerMove: (e: ReactPointerEvent<HTMLElement>) => {
      const s = dragRef.current
      if (!s) return
      autoScroll(e.clientY)
      set({ ...s, x: e.clientX, y: e.clientY, over: overAt(e.clientX, e.clientY) })
    },
    onPointerUp: (e: ReactPointerEvent<HTMLElement>) => {
      const s = dragRef.current
      if (!s) return
      set(null)
      const over = overAt(e.clientX, e.clientY)
      if (over !== null) onDrop(s.item, over)
    },
    onPointerCancel: () => set(null),
  })

  return { drag, handle }
}

function Composer({
  lib,
  request,
  replies,
  connected,
}: {
  lib: SeqLibraryMsg
  request: Request
  replies: Record<string, Seq2ReplyMsg>
  connected: boolean
}) {
  const sequences = lib.sequences ?? []
  const actions = lib.actions ?? []
  const actionById = new Map(actions.map((a) => [a.id, a]))
  const ed = useLibraryEditor(sequences, blankSequence)
  const save = useRequest(request, replies)
  const pendingId = useRef('')
  const listRef = useRef<HTMLOListElement>(null)
  const d = ed.draft

  useEffect(() => {
    const r = save.reply
    if (!r?.success) return
    if (r.op === 'seq2-save-sequence') ed.settle(pendingId.current)
    if (r.op === 'seq2-delete-sequence') ed.settle(sequences.find((q) => q.id !== pendingId.current)?.id ?? '')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [save.reply])

  const setSteps = (fn: (steps: StepRef[]) => StepRef[]) => ed.update((q) => ({ ...q, steps: fn(q.steps) }))
  const setStep = (i: number, patch: Partial<StepRef>) => setSteps((s) => s.map((st, j) => (j === i ? { ...st, ...patch } : st)))
  const setWith = (i: number, name: string, value: string) =>
    setSteps((s) =>
      s.map((st, j) => {
        if (j !== i) return st
        const w = { ...(st.with ?? {}) }
        // An empty value means "use the action's default".
        if (value === '') delete w[name]
        else w[name] = value
        return { ...st, with: w }
      }),
    )

  const { drag, handle } = useStepDrag(listRef, (item, at) => {
    if (item.kind === 'action') {
      setSteps((s) => [...s.slice(0, at), { action: item.actionId }, ...s.slice(at)])
      return
    }
    const from = item.index
    if (at === from || at === from + 1) return
    setSteps((s) => {
      const list = s.slice()
      const [st] = list.splice(from, 1)
      list.splice(at > from ? at - 1 : at, 0, st)
      return list
    })
  })

  const onSave = () => {
    const sequence: SequenceV2 = { ...d, controls: (d.controls ?? []).filter((c) => c.name.trim() !== '') }
    pendingId.current = sequence.id
    save.send('seq2-save-sequence', { sequence, previous_id: ed.original?.id ?? '' })
  }
  const onDelete = () => {
    if (!ed.original || !window.confirm(`Delete the sequence "${ed.original.name}"?`)) return
    pendingId.current = ed.original.id
    save.send('seq2-delete-sequence', { id: ed.original.id })
  }

  const marker = (i: number) => drag && drag.over === i && <li className="seq2-drop-marker" key={`marker-${i}`} />

  return (
    <div className="seq2-editor">
      <div className="seq2-row">
        <select className="seq-select" value={ed.selected} onChange={(e) => ed.select(e.target.value)}>
          {sequences.map((q) => (
            <option key={q.id} value={q.id}>
              {q.name}
            </option>
          ))}
          <option value="">(new sequence)</option>
        </select>
        <button className="btn-secondary" onClick={() => ed.select('')}>
          New
        </button>
      </div>

      <div className="seq2-section">
        <Field label="id">
          <input className="seq2-input" value={d.id} placeholder="e.g. morning-check" onChange={(e) => ed.update((q) => ({ ...q, id: e.target.value }))} />
        </Field>
        <Field label="name">
          <input className="seq2-input" value={d.name} onChange={(e) => ed.update((q) => ({ ...q, name: e.target.value }))} />
        </Field>
        <Field label="description">
          <input
            className="seq2-input"
            value={d.description ?? ''}
            onChange={(e) => ed.update((q) => ({ ...q, description: e.target.value }))}
          />
        </Field>
      </div>

      <div className="seq2-section">
        <div className="seq2-section-title">Sequence controls</div>
        <div className="seq2-hint">The runner asks for these; steps use them as {'{{name}}'}.</div>
        <ControlsEditor controls={d.controls ?? []} onChange={(controls) => ed.update((q) => ({ ...q, controls }))} />
      </div>

      <div className="seq2-compose">
        <div className="seq2-section seq2-palette">
          <div className="seq2-section-title">Actions</div>
          <div className="seq2-hint">Drag ⠿ into the steps, or tap + to add at the end.</div>
          {actions.map((a) => (
            <div className="seq2-palette-item" key={a.id} title={a.description}>
              <span className="seq2-handle" {...handle({ kind: 'action', actionId: a.id, label: a.name })}>
                ⠿
              </span>
              <span className="seq2-palette-name">{a.name}</span>
              <button className="btn-secondary seq2-icon-btn" title="add at the end" onClick={() => setSteps((s) => [...s, { action: a.id }])}>
                +
              </button>
            </div>
          ))}
        </div>

        <div className="seq2-section seq2-steps-wrap">
          <div className="seq2-section-title">Steps</div>
          <ol className={`seq2-steps${drag ? ' seq2-steps-dropping' : ''}`} ref={listRef}>
            {d.steps.map((st, i) => {
              const a = actionById.get(st.action)
              return (
                <Fragment key={i}>
                  {marker(i)}
                  <li
                    className={`seq2-step${drag?.item.kind === 'step' && drag.item.index === i ? ' seq2-step-dragging' : ''}`}
                    data-step-index={i}
                  >
                    <span className="seq2-handle" {...handle({ kind: 'step', index: i, label: st.label || a?.name || st.action })}>
                      ⠿
                    </span>
                    <div className="seq2-step-body">
                      <div className="seq2-row">
                        <span className="seq2-num">{i + 1}.</span>
                        <span className="seq2-step-action">{a ? a.name : `missing action "${st.action}"`}</span>
                        <button className="btn-secondary seq2-icon-btn" title="remove this step" onClick={() => setSteps((s) => s.filter((_, j) => j !== i))}>
                          ✕
                        </button>
                      </div>
                      <Field label="label">
                        <input
                          className="seq2-input"
                          value={st.label ?? ''}
                          placeholder={a?.name ?? ''}
                          onChange={(e) => setStep(i, { label: e.target.value || undefined })}
                        />
                      </Field>
                      {(a?.controls ?? []).map((c) => (
                        <Field key={c.name} label={c.label || c.name}>
                          <input
                            className="seq2-input"
                            value={st.with?.[c.name] ?? ''}
                            placeholder={c.default ? `${c.default} (default)` : c.help ?? ''}
                            title={c.help}
                            onChange={(e) => setWith(i, c.name, e.target.value)}
                          />
                        </Field>
                      ))}
                    </div>
                  </li>
                </Fragment>
              )
            })}
            {marker(d.steps.length)}
            {d.steps.length === 0 && !drag && <li className="seq2-steps-empty">Drag actions here.</li>}
          </ol>
        </div>
      </div>

      {drag && (
        <div className="seq2-ghost" style={{ left: drag.x + 12, top: drag.y + 12 }}>
          {drag.item.label}
        </div>
      )}

      <BuiltinsHelp lib={lib} />

      <div className="seq2-row seq2-actions">
        <button className="btn-secondary seq-run" disabled={!connected || !ed.dirty || save.pending} onClick={onSave}>
          {ed.original ? 'Save sequence' : 'Create sequence'}
        </button>
        <button className="btn-secondary" disabled={!ed.dirty} onClick={ed.revert}>
          Revert
        </button>
        <button className="btn-secondary" disabled={!connected || !ed.original || save.pending} onClick={onDelete}>
          Delete
        </button>
      </div>
      <ReplyStatus r={save} working="Saving…" />

      <YamlTools kind="sequences" selectedId={ed.original?.id ?? ''} request={request} replies={replies} connected={connected} />
    </div>
  )
}

// ---- Runner ----

function Runner({
  lib,
  connected,
  reprStatus,
  saves,
  onSave,
  run,
  onRun,
}: {
  lib: SeqLibraryMsg
  connected: boolean
  reprStatus: ReprStatus
  saves: Record<string, SaveStatus>
  onSave: (id: string) => void
  run: SeqRun | null
  onRun: (id: string, controls: Record<string, string>) => void
}) {
  const sequences = lib.sequences ?? []
  const actionById = new Map((lib.actions ?? []).map((a) => [a.id, a]))
  const [selectedId, setSelectedId] = useState('')
  const [values, setValues] = useState<Record<string, Record<string, string>>>({})
  const seq = sequences.find((q) => q.id === selectedId) ?? sequences[0]
  if (!seq) {
    return <div className="empty-state">No sequences yet — build one in the composer.</div>
  }

  const controls = seq.controls ?? []
  const vals = values[seq.id] ?? {}
  const valueOf = (c: Control) => vals[c.name] ?? c.default ?? ''
  const thisRun = run?.sequenceId === seq.id ? run : null
  const running = !!run?.running
  const result = thisRun?.result
  const rec = result?.recording
  const outputs = thisRun?.outputs ?? []
  // Until a run reports its compiled steps, show them from the library.
  const steps: SequenceStepDef[] =
    thisRun?.def?.steps ??
    seq.steps.map((st) => {
      const a = actionById.get(st.action)
      return { label: st.label || a?.name || st.action, detail: [] }
    })

  return (
    <div className="seq2-runner">
      <div className="seq-header">
        <select className="seq-select" value={seq.id} disabled={running} onChange={(e) => setSelectedId(e.target.value)}>
          {sequences.map((q) => (
            <option key={q.id} value={q.id}>
              {q.name}
            </option>
          ))}
        </select>
        <button
          className="btn-secondary seq-run"
          disabled={!connected || running}
          onClick={() => onRun(seq.id, Object.fromEntries(controls.map((c) => [c.name, valueOf(c)])))}
        >
          {running ? 'Running…' : 'Run sequence'}
        </button>
      </div>
      {seq.description && <div className="seq2-hint">{seq.description}</div>}
      {controls.length > 0 && (
        <div className="seq2-section">
          {controls.map((c) => (
            <Field key={c.name} label={c.label || c.name}>
              <input
                className="seq2-input"
                value={valueOf(c)}
                title={c.help}
                disabled={running}
                onChange={(e) => setValues((v) => ({ ...v, [seq.id]: { ...(v[seq.id] ?? {}), [c.name]: e.target.value } }))}
              />
            </Field>
          ))}
        </div>
      )}
      <ol className="seq-steps">
        {steps.map((st, i) => {
          const s = thisRun?.steps[i]
          const status: StepStatus = s?.status ?? 'pending'
          return (
            <li key={i} className={`seq-step seq-step-${status}`}>
              <span className="seq-step-icon">{STEP_ICONS[status]}</span>
              <div className="seq-step-body">
                <div className="seq-step-label">
                  {i + 1}. {st.label}
                </div>
                {st.detail.length > 0 && (
                  <ul className="seq-step-detail">
                    {st.detail.map((x, j) => (
                      <li key={j}>{x}</li>
                    ))}
                  </ul>
                )}
                {s?.message && <div className="seq-step-message">{s.message}</div>}
                {s?.imageUrl && (
                  <a href={s.imageUrl} target="_blank" rel="noreferrer">
                    <img className="seq-step-shot" src={s.imageUrl} alt={`what step ${i + 1} saw`} />
                  </a>
                )}
              </div>
              {s?.durationMs !== undefined && status !== 'skipped' && (
                <span className="seq-step-time">{(s.durationMs / 1000).toFixed(1)}s</span>
              )}
            </li>
          )
        })}
      </ol>
      {outputs.length > 0 && (
        <div className="seq2-outputs">
          {outputs.map((o, i) => (
            <div key={i} className="seq2-output">
              {o.label && <span className="seq2-output-label">{o.label}: </span>}
              <span className="seq2-output-value">{o.value}</span>
            </div>
          ))}
        </div>
      )}
      {running && <div className="robot-status">Running and recording the sequence…</div>}
      {thisRun?.lostConnection && (
        <div className="robot-status robot-status-error">
          Lost the connection to IANAR mid-run, so this run's result and recording didn't arrive.
        </div>
      )}
      {result && (
        <div className={`seq-result ${result.success ? 'seq-result-success' : 'seq-result-error'}`}>
          {result.success
            ? `✓ Sequence succeeded in ${(result.duration_ms / 1000).toFixed(1)}s`
            : `✗ Sequence failed: ${result.error ?? 'unknown error'}`}
          {result.keyboard_via && <span className="seq-result-via"> · keyboard via {result.keyboard_via}</span>}
        </div>
      )}
      {result?.warnings?.map((w, i) => (
        <div key={i} className="robot-status robot-status-error">⚠ {w}</div>
      ))}
      {rec && (
        <div className={`robot-status${rec.success ? '' : ' robot-status-error'}`}>
          {rec.success ? `Recorded via ${rec.via}.` : `Recording failed: ${rec.error ?? 'unknown error'}`}
        </div>
      )}
      {result?.artifact_id && (
        <SaveToFile
          artifactId={result.artifact_id}
          label="Save run to file"
          connected={connected}
          reprStatus={reprStatus}
          status={saves[result.artifact_id]}
          onSave={onSave}
        />
      )}
      <PreviewView preview={thisRun?.recording} placeholder="Run the sequence — its recording appears here." />
    </div>
  )
}

// ---- The tab ----

type SubTab = 'definer' | 'composer' | 'runner'
const SUB_TABS: SubTab[] = ['definer', 'composer', 'runner']

interface SequenceV2PanelProps {
  connected: boolean
  reprStatus: ReprStatus
  saves: Record<string, SaveStatus>
  onSave: (id: string) => void
  lib: SeqLibraryMsg | null
  replies: Record<string, Seq2ReplyMsg>
  request: Request
  run: SeqRun | null
  onRun: (id: string, controls: Record<string, string>) => void
}

export function SequenceV2Panel({ connected, reprStatus, saves, onSave, lib, replies, request, run, onRun }: SequenceV2PanelProps) {
  const [sub, setSub] = useState<SubTab>('runner')
  const restore = useRequest(request, replies)

  return (
    <div className="robot-panel">
      <div className="seq2-subtabs">
        {SUB_TABS.map((t) => (
          <button key={t} className={`seq2-subtab${sub === t ? ' seq2-subtab-active' : ''}`} onClick={() => setSub(t)}>
            {t}
          </button>
        ))}
      </div>
      {lib?.note && <div className="robot-status robot-status-error">{lib.note}</div>}
      {!lib && <div className="empty-state">{connected ? 'Loading the library…' : 'Connecting…'}</div>}
      {lib && sub === 'definer' && <Definer lib={lib} request={request} replies={replies} connected={connected} />}
      {lib && sub === 'composer' && <Composer lib={lib} request={request} replies={replies} connected={connected} />}
      {lib && sub === 'runner' && (
        <Runner lib={lib} connected={connected} reprStatus={reprStatus} saves={saves} onSave={onSave} run={run} onRun={onRun} />
      )}
      {lib && sub !== 'runner' && (
        <div className="seq2-footer">
          <span className="seq2-hint">{lib.path ? `Saved in ${lib.path}` : 'Kept in memory only (not saved to disk).'}</span>
          <button
            className="btn-secondary"
            disabled={!connected || restore.pending}
            title="put the built-in example actions and sequences back, replacing edited copies with the same ids"
            onClick={() => {
              if (window.confirm('Restore the built-in examples? Edited copies with the same ids are replaced.'))
                restore.send('seq2-restore-examples', {})
            }}
          >
            Restore examples
          </button>
          <ReplyStatus r={restore} />
        </div>
      )}
    </div>
  )
}
