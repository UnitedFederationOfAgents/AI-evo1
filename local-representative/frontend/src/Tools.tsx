import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { HostFilePicker } from './HostFilePicker'

// The tools tab (local-representative/tools.go): LR's own tab over T3s, the
// declarative tools dtt runs (dtt/README.md). Three sub-tabs, like the
// worker tab's --
//   catalog: the tools dtt found, each one's schema and where it came from,
//            and whether tools-allow lets control runs use it
//   runner:  a form made from a tool's inputs, a run button, and the recent
//            runs with their outputs and logs ("save results to files")
//   author:  check a pasted tool.yaml, validate or install a tool picked on
//            the host ("select from host…"), scaffold a new one
//
// This file is shared, word for word, by local-representative's frontend
// and agent-coordinator's (which drives a chosen host's tools through its
// own relay, "__tools:<op>") -- keep the two copies in step. Everything
// node-specific comes in through the props, and the types live here.

export interface ToolArg {
  name: string
  help: string
  required?: boolean
  default?: string
  type: string // string, text, int, number, bool, path
  choices?: string[]
}

export interface ToolOutput {
  name: string
  help?: string
}

export interface ToolInfo {
  op: string
  summary: string
  describe: string
  args: ToolArg[]
  name: string
  description: string
  values: ToolOutput[]
  files: ToolOutput[]
  runtime: string
  script: string
  timeout: string
  origin: string // builtin, user, tools-dirs
  path: string
  source?: string // tool.yaml, from describe only
  allowed: boolean
}

export interface ToolRunResult {
  ok: boolean
  tool: string
  outputs: Record<string, string>
  files: { name: string; path: string; size: number }[]
  log_tail: string
  exit_code: number
  error?: string
  timed_out?: boolean
  workdir?: string
  duration_ms: number
}

export interface ToolSavedFile {
  file_id: string
  name: string
}

export interface ToolRun {
  id: string
  tool: string
  args: Record<string, string> | null
  origin: string
  status: 'running' | 'ok' | 'failed'
  started_at: number
  finished_at?: number
  result?: ToolRunResult
  files_kept: boolean
  saved?: ToolSavedFile[]
}

export interface ToolsStateMsg {
  status: string // checking, ok, failed
  detail?: string
  version?: string
  tools: ToolInfo[]
  user_dir?: string
  dirs?: { origin: string; path: string; status: string; detail?: string }[]
  problems?: { path: string; error: string }[]
  shadowed?: { name: string; path: string; by: string }[]
  checked_at?: number
  allow: string[] | null
  allow_from?: string
  runs: ToolRun[]
  runs_omitted?: number
}

export interface ToolsRequest {
  op: 'reinspect' | 'run' | 'save' | 'describe' | 'validate' | 'install' | 'create'
  tool?: string
  args?: Record<string, string>
  run?: string
  source?: string
  path?: string
  force?: boolean
  name?: string
  runtime?: string
}

export interface ToolsReply {
  req?: string
  op: string
  ok: boolean
  error?: string
  detail?: string
  run_id?: string
  tool?: ToolInfo
  file?: ToolSavedFile
}

// Sends a request and returns the id its reply will carry.
export type ToolsRequestFn = (req: ToolsRequest) => string

export interface ToolsPanelProps {
  connected: boolean
  state: ToolsStateMsg | null
  replies: Record<string, ToolsReply> // by request id
  request: ToolsRequestFn
  fileUrl: (id: string) => string // a saved file's URL
  hostApi: string // where the node's /api/host-files is: '' on LR, '/host/<id>' through AC
  node: string // how to refer to the node: "this node", "this host"
}

const SUBTABS = ['catalog', 'runner', 'author'] as const
type SubTab = typeof SUBTABS[number]

function useRequest(request: ToolsRequestFn, replies: Record<string, ToolsReply>) {
  const [req, setReq] = useState<string | null>(null)
  const reply = req ? replies[req] : undefined
  return {
    reply,
    pending: req !== null && !reply,
    send: (r: ToolsRequest) => setReq(request(r)),
    clear: () => setReq(null),
  }
}

type Tracked = ReturnType<typeof useRequest>

function ReplyStatus({ r, working = 'working…' }: { r: Tracked; working?: string }) {
  if (r.pending) return <div className="ctl-hint">{working}</div>
  if (!r.reply) return null
  if (!r.reply.ok) return <div className="ctl-run-error-msg">{r.reply.error}</div>
  return r.reply.detail ? <div className="ctl-ok">✓ {r.reply.detail}</div> : null
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="ctl-field">
      <span className="ctl-field-name">{label}</span>
      {children}
    </label>
  )
}

function formatMs(ms?: number): string {
  if (!ms) return ''
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

function AllowedBadge({ tool }: { tool: ToolInfo }) {
  return tool.allowed
    ? <span className="tools-badge tools-badge-ok" title="control runs (and agent-coordinator) may run it">allowed</span>
    : <span className="tools-badge tools-badge-no" title="tools-allow doesn't list it: only this node's own runner may run it">not allowed</span>
}

export function ToolsPanel(props: ToolsPanelProps) {
  const [sub, setSub] = useState<SubTab>('catalog')
  const [runnerTool, setRunnerTool] = useState('')
  const { state } = props
  const reinspect = useRequest(props.request, props.replies)

  const openRunner = (name: string) => {
    setRunnerTool(name)
    setSub('runner')
  }

  return (
    <div className="ctl-panel">
      <div className="tools-status">
        {!state ? (
          <span className="ctl-hint">waiting for {props.node}'s tools…</span>
        ) : (
          <>
            <span className={`tools-dot tools-dot-${state.status}`} />
            <span>
              {state.status === 'checking' ? 'listing tools through dtt…'
                : state.status === 'failed' ? `dtt failed: ${state.detail ?? ''}`
                : `${state.tools.length} tool${state.tools.length === 1 ? '' : 's'} via dtt ${state.version ?? ''}`}
            </span>
            {state.checked_at ? <span className="ctl-hint">checked {new Date(state.checked_at * 1000).toLocaleTimeString()}</span> : null}
            <button
              className="ctl-btn"
              disabled={!props.connected || state.status === 'checking'}
              title="list the tools again now (otherwise every 10 minutes)"
              onClick={() => reinspect.send({ op: 'reinspect' })}
            >
              re-inspect
            </button>
          </>
        )}
      </div>
      <div className="ctl-subtabs">
        {SUBTABS.map(t => (
          <button key={t} className={`ctl-subtab${sub === t ? ' ctl-subtab-active' : ''}`} onClick={() => setSub(t)}>
            {t}
          </button>
        ))}
      </div>
      {sub === 'catalog' && <Catalog {...props} onRun={openRunner} />}
      {sub === 'runner' && <Runner {...props} selected={runnerTool} onSelect={setRunnerTool} />}
      {sub === 'author' && <Author {...props} />}
    </div>
  )
}

// ---- Catalog ----

function Catalog({ state, request, replies, connected, onRun }: ToolsPanelProps & { onRun: (name: string) => void }) {
  const [open, setOpen] = useState<string | null>(null)
  if (!state) return null
  const allow = state.allow ?? []
  return (
    <div className="ctl-v1-view">
      <div className="ctl-hint">
        tools-allow: {allow.length === 0 ? 'every tool' : allow.join(', ')}
        {state.allow_from ? ` (from ${state.allow_from})` : ''} — which tools control runs' tool.&lt;name&gt; ops and
        agent-coordinator may run. This node's own runner may run any tool.
      </div>
      {state.tools.length === 0 && <div className="ctl-empty">no tools found</div>}
      {state.tools.map(t => (
        <div key={t.name} className="ctl-seq">
          <div className="ctl-seq-head tools-head" onClick={() => setOpen(open === t.name ? null : t.name)}>
            <span className="ctl-seq-name">{t.name}</span>
            <span className="tools-badge">{t.origin}</span>
            <AllowedBadge tool={t} />
            <span className="tools-grow" />
            <button className="ctl-btn" onClick={e => { e.stopPropagation(); onRun(t.name) }}>run…</button>
          </div>
          <div className="ctl-seq-desc">{t.description}</div>
          {open === t.name && <ToolDetail tool={t} request={request} replies={replies} connected={connected} />}
        </div>
      ))}
      {(state.dirs?.length ?? 0) > 0 && (
        <div className="ctl-section">
          <div className="ctl-section-title">where dtt looked</div>
          {state.dirs!.map(d => (
            <div key={d.origin + d.path} className="tools-line">
              <span className="tools-badge">{d.origin}</span> {d.path} — {d.status}{d.detail ? `: ${d.detail}` : ''}
            </div>
          ))}
          {state.user_dir && <div className="ctl-hint">install and scaffold put tools in {state.user_dir}</div>}
        </div>
      )}
      {((state.problems?.length ?? 0) > 0 || (state.shadowed?.length ?? 0) > 0) && (
        <div className="ctl-section">
          <div className="ctl-section-title">skipped</div>
          {state.problems?.map(p => <div key={p.path} className="tools-line tools-problem">{p.path}: {p.error}</div>)}
          {state.shadowed?.map(s => <div key={s.path} className="tools-line">{s.name} at {s.path} is hidden by {s.by}</div>)}
        </div>
      )}
    </div>
  )
}

function ToolDetail({ tool: t, request, replies, connected }: { tool: ToolInfo; request: ToolsRequestFn; replies: Record<string, ToolsReply>; connected: boolean }) {
  const describe = useRequest(request, replies)
  const source = describe.reply?.ok ? describe.reply.tool?.source : undefined
  return (
    <div className="tools-detail">
      <div className="tools-line"><b>op</b> {t.op} — {t.describe}</div>
      <div className="tools-line"><b>runs</b> {t.runtime} {t.script}, at most {t.timeout} · {t.path}</div>
      <div className="ctl-section-title">inputs</div>
      {t.args.length === 0 ? <div className="ctl-hint">none</div> : (
        <table className="tools-table">
          <tbody>
            {t.args.map(a => (
              <tr key={a.name}>
                <td>{a.name}{a.required ? ' *' : ''}</td>
                <td>{a.type}{a.choices ? `: ${a.choices.join(' | ')}` : ''}</td>
                <td>{a.default ? `default ${a.default}` : ''}</td>
                <td>{a.help}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <div className="ctl-section-title">outputs</div>
      {t.values.length + t.files.length === 0 ? <div className="ctl-hint">none</div> : (
        <table className="tools-table">
          <tbody>
            {t.values.map(o => <tr key={o.name}><td>{`{{${o.name}}}`}</td><td>value</td><td>{o.help}</td></tr>)}
            {t.files.map(o => <tr key={o.name}><td>{o.name}</td><td>file</td><td>{o.help}</td></tr>)}
          </tbody>
        </table>
      )}
      <div className="ctl-row">
        <button className="ctl-btn" disabled={!connected || describe.pending} onClick={() => describe.send({ op: 'describe', tool: t.name })}>
          {source ? 'reload tool.yaml' : 'show tool.yaml'}
        </button>
      </div>
      {describe.reply && !describe.reply.ok && <div className="ctl-run-error-msg">{describe.reply.error}</div>}
      {source && <pre className="tools-pre">{source}</pre>}
    </div>
  )
}

// ---- Runner ----

function Runner({ state, request, replies, connected, fileUrl, selected, onSelect }: ToolsPanelProps & { selected: string; onSelect: (name: string) => void }) {
  const tools = state?.tools ?? []
  const tool = tools.find(t => t.name === selected) ?? tools[0]
  const [values, setValues] = useState<Record<string, Record<string, string>>>({})
  const run = useRequest(request, replies)
  const [open, setOpen] = useState<string | null>(null)

  // A run that started opens in the list.
  useEffect(() => {
    if (run.reply?.ok && run.reply.run_id) setOpen(run.reply.run_id)
  }, [run.reply])

  const vals = tool ? values[tool.name] ?? {} : {}
  const setVal = (k: string, v: string) => tool && setValues(prev => ({ ...prev, [tool.name]: { ...(prev[tool.name] ?? {}), [k]: v } }))
  const missing = tool ? tool.args.filter(a => a.required && !(vals[a.name] ?? '').trim()) : []

  return (
    <div className="ctl-v1-view">
      {tools.length === 0 ? <div className="ctl-empty">no tools to run</div> : tool && (
        <div className="ctl-section">
          <div className="ctl-row">
            <select className="ctl-select" value={tool.name} onChange={e => onSelect(e.target.value)}>
              {tools.map(t => <option key={t.name} value={t.name}>{t.name}</option>)}
            </select>
            <AllowedBadge tool={tool} />
          </div>
          <div className="ctl-hint">{tool.description}</div>
          {tool.args.map(a => (
            <Field key={a.name} label={a.name + (a.required ? ' *' : '')}>
              <ArgInput arg={a} value={vals[a.name] ?? ''} onChange={v => setVal(a.name, v)} />
            </Field>
          ))}
          <div className="ctl-row">
            <button
              className="sys-btn sys-btn-launch"
              disabled={!connected || run.pending || missing.length > 0}
              title={missing.length > 0 ? `fill in ${missing.map(a => a.name).join(', ')}` : `run ${tool.name} through dtt`}
              onClick={() => run.send({ op: 'run', tool: tool.name, args: vals })}
            >
              run
            </button>
            <span className="ctl-hint">empty inputs take their defaults</span>
          </div>
          <ReplyStatus r={run} working="starting…" />
        </div>
      )}
      <RunList state={state} request={request} replies={replies} connected={connected} fileUrl={fileUrl} open={open} onOpen={setOpen} />
    </div>
  )
}

function ArgInput({ arg, value, onChange }: { arg: ToolArg; value: string; onChange: (v: string) => void }) {
  const placeholder = arg.default ? `${arg.default} (default)` : arg.help
  if (arg.choices && arg.choices.length > 0) {
    return (
      <select className="ctl-select" value={value} title={arg.help} onChange={e => onChange(e.target.value)}>
        <option value="">{arg.default ? `${arg.default} (default)` : '—'}</option>
        {arg.choices.map(c => <option key={c} value={c}>{c}</option>)}
      </select>
    )
  }
  if (arg.type === 'bool') {
    return (
      <select className="ctl-select" value={value} title={arg.help} onChange={e => onChange(e.target.value)}>
        <option value="">{arg.default ? `${arg.default} (default)` : '—'}</option>
        <option value="true">true</option>
        <option value="false">false</option>
      </select>
    )
  }
  if (arg.type === 'text') {
    return <textarea className="ctl-textarea" rows={6} value={value} placeholder={placeholder} title={arg.help} spellCheck={false} onChange={e => onChange(e.target.value)} />
  }
  return <input className="ctl-input" value={value} placeholder={placeholder} title={arg.help} onChange={e => onChange(e.target.value)} />
}

function RunList({ state, request, replies, connected, fileUrl, open, onOpen }: {
  state: ToolsStateMsg | null
  request: ToolsRequestFn
  replies: Record<string, ToolsReply>
  connected: boolean
  fileUrl: (id: string) => string
  open: string | null
  onOpen: (id: string | null) => void
}) {
  const runs = state?.runs ?? []
  return (
    <div className="ctl-section">
      <div className="ctl-section-title">recent runs</div>
      {runs.length === 0 && <div className="ctl-hint">no runs yet</div>}
      {runs.map(r => (
        <RunRow key={r.id} run={r} open={open === r.id} onToggle={() => onOpen(open === r.id ? null : r.id)}
          request={request} replies={replies} connected={connected} fileUrl={fileUrl} />
      ))}
      {!!state?.runs_omitted && <div className="ctl-hint">…and {state.runs_omitted} older not relayed — see the host's own tools tab</div>}
    </div>
  )
}

function RunRow({ run: r, open, onToggle, request, replies, connected, fileUrl }: {
  run: ToolRun
  open: boolean
  onToggle: () => void
  request: ToolsRequestFn
  replies: Record<string, ToolsReply>
  connected: boolean
  fileUrl: (id: string) => string
}) {
  const save = useRequest(request, replies)
  const res = r.result
  const icon = r.status === 'running' ? '…' : r.status === 'ok' ? '✓' : '✗'
  const statusClass = r.status === 'running' ? 'running' : r.status === 'ok' ? 'success' : 'error'
  return (
    <div className={`ctl-step ctl-step-${statusClass} tools-run`}>
      <span className="ctl-step-icon">{icon}</span>
      <div className="ctl-step-body">
        <div className="tools-head" onClick={onToggle}>
          <span className="ctl-step-label">{r.tool}</span>
          <span className="tools-badge">{r.origin}</span>
          <span className="ctl-hint">{new Date(r.started_at).toLocaleTimeString()}</span>
          <span className="tools-grow" />
          {res && <span className="ctl-step-time">{formatMs(res.duration_ms)}</span>}
        </div>
        {res?.error && <div className="ctl-step-msg">{res.error}</div>}
        {open && (
          <div className="tools-detail">
            {r.args && Object.keys(r.args).length > 0 && (
              <div className="ctl-values">
                {Object.entries(r.args).map(([k, v]) => (
                  <div key={k}><span className="ctl-value-label">{k}:</span> <span className="tools-value">{v}</span></div>
                ))}
              </div>
            )}
            {res && Object.keys(res.outputs ?? {}).length > 0 && (
              <div className="ctl-output">
                <div className="ctl-output-head">outputs</div>
                {Object.entries(res.outputs).map(([k, v]) => (
                  <div key={k}><span className="ctl-value-label">{`{{${k}}}`}</span> <span className="ctl-output-value">{v}</span></div>
                ))}
              </div>
            )}
            {res && (res.files ?? []).length > 0 && (
              <div className="ctl-values">
                {res.files.map(f => (
                  <div key={f.name}><span className="ctl-value-label">{f.name}:</span> {f.path} ({formatSize(f.size)})</div>
                ))}
                {!r.files_kept && <div className="ctl-hint">copied into the files tab by the control run; not kept here</div>}
              </div>
            )}
            {res?.log_tail && <pre className="tools-pre">{res.log_tail}</pre>}
            {r.status !== 'running' && (
              <div className="ctl-save">
                <button className="ctl-btn" disabled={!connected || save.pending} onClick={() => save.send({ op: 'save', run: r.id })}>
                  save results to files
                </button>
                {(r.saved ?? []).map(f => (
                  <a key={f.file_id} className="ctl-save-link" href={fileUrl(f.file_id)} target="_blank" rel="noreferrer">{f.name}</a>
                ))}
              </div>
            )}
            {save.reply && !save.reply.ok && <div className="ctl-run-error-msg">{save.reply.error}</div>}
          </div>
        )}
      </div>
    </div>
  )
}

// ---- Author ----

const SAMPLE_TOOL = `format: dtt-v1
name: my-tool
description: What my-tool does.
inputs:
  - name: message
    type: string
    required: true
outputs:
  values:
    - name: length
entrypoint:
  runtime: uv
  script: main.py
  timeout: 60s
`

function Author({ state, request, replies, connected, hostApi }: ToolsPanelProps) {
  const [source, setSource] = useState(SAMPLE_TOOL)
  const [path, setPath] = useState('')
  const [force, setForce] = useState(false)
  const [picking, setPicking] = useState(false)
  const [name, setName] = useState('')
  const [runtime, setRuntime] = useState('uv')
  const check = useRequest(request, replies)
  const fromHost = useRequest(request, replies)
  const create = useRequest(request, replies)

  return (
    <div className="ctl-v1-view">
      <div className="ctl-section">
        <div className="ctl-section-title">check a tool.yaml</div>
        <div className="ctl-hint">Checks the schema (format dtt-v1) only — the script isn't looked for.</div>
        <textarea className="ctl-textarea" rows={14} value={source} spellCheck={false} onChange={e => setSource(e.target.value)} />
        <div className="ctl-row">
          <button className="ctl-btn" disabled={!connected || check.pending || !source.trim()} onClick={() => check.send({ op: 'validate', source })}>validate</button>
        </div>
        <ReplyStatus r={check} working="checking…" />
      </div>

      <div className="ctl-section">
        <div className="ctl-section-title">a tool on the host</div>
        <div className="ctl-hint">
          A tool is a directory named after it, holding tool.yaml and its script. Pick its tool.yaml to validate it
          (script included) or install it into {state?.user_dir || 'the tools directory'}.
        </div>
        <div className="ctl-row">
          <input className="ctl-input" value={path} placeholder="/path/to/my-tool" onChange={e => setPath(e.target.value)} />
          <button className="ctl-btn" onClick={() => setPicking(true)}>select from host…</button>
        </div>
        <div className="ctl-row">
          <button className="ctl-btn" disabled={!connected || fromHost.pending || !path.trim()} onClick={() => fromHost.send({ op: 'validate', path })}>validate</button>
          <button className="sys-btn sys-btn-launch" disabled={!connected || fromHost.pending || !path.trim()} onClick={() => fromHost.send({ op: 'install', path, force })}>install</button>
          <label className="ctl-check">
            <input type="checkbox" checked={force} onChange={e => setForce(e.target.checked)} />
            replace an installed copy
          </label>
        </div>
        <ReplyStatus r={fromHost} />
        {picking && (
          <HostFilePicker
            api={hostApi}
            title="select a tool's tool.yaml"
            onPick={paths => { if (paths[0]) setPath(paths[0]); setPicking(false) }}
            onClose={() => setPicking(false)}
          />
        )}
      </div>

      <div className="ctl-section">
        <div className="ctl-section-title">scaffold a new tool</div>
        <div className="ctl-hint">
          Creates a tool that runs as it stands — tool.yaml plus a uv Python script (dependencies declared inline) or a
          shell script — in {state?.user_dir || 'the tools directory'}, for you to edit there.
        </div>
        <div className="ctl-row">
          <input className="ctl-input ctl-input-name" value={name} placeholder="my-tool" onChange={e => setName(e.target.value)} />
          <select className="ctl-select" value={runtime} onChange={e => setRuntime(e.target.value)}>
            <option value="uv">uv (Python)</option>
            <option value="exec">exec (shell)</option>
          </select>
          <button className="sys-btn sys-btn-launch" disabled={!connected || create.pending || !name.trim()} onClick={() => create.send({ op: 'create', name: name.trim(), runtime })}>create</button>
        </div>
        <ReplyStatus r={create} working="creating…" />
      </div>
    </div>
  )
}
