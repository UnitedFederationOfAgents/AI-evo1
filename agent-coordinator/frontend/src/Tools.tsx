import { Fragment, useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { HostFilePicker } from './HostFilePicker'

// The tools tab (local-representative/tools.go): LR's own tab over T3s, the
// declarative tools dtt runs (dtt/README.md). Its words -- tool, tool call
// (definition, specification, execution), read, image -- are dtt's
// (dtt/docs/terms.md). Three sub-tabs, like the worker tab's --
//   calls:     tool calls, made here, keyed by their directories (the call
//              on the right shown as text -- all that follows -- or as its
//              visual: an execution's steps, each its tool's icon, laid out
//              in the order of their dependency graph, grey until begun,
//              yellow while running, green or red as each ends; dtt run
//              reports them as it goes, and LR passes them on in the run's
//              progress): a left-hand
//              bar lists dtt's recent calls (an in-place call's directory, or
//              a pathless call's staging area, most recently executed first)
//              to pick one from, takes an unlisted one's path, lists dtt's
//              example specifications under examples (folded by default;
//              each an in-place call, unpacked on the host), and starts a
//              new call: a new document, or an imported example. A picked
//              call's definition is its document, and its specification the
//              declare tool's inputs with the call as document_file; a new
//              call's definition is a document of its own -- naming whatever
//              tools it uses, so none is chosen first -- executed by declare
//              (or an imported example's instance of its tool), and its
//              specification the form made from the tool's inputs. Either
//              has an execute button once every required
//              input is given; and its executions are listed below
//              with their outputs and logs ("save results to files"). Each
//              execution holds its coarse events (LR's and dtt's) in memory;
//              its "to file" sends its details, events first, to the files
//              tab as a .txt -- even while it's still going. A declarative
//              tool's execution returns its image (the image_json output):
//              when it finishes it fills that form's from_image box, the
//              image dialog, which a clear button empties
//   history:   the dtt calls clauditable recorded on this host, from anywhere
//              (an agent, a shell, this tab), newest first, each with its
//              session -- read from <records>/dtt-calls on request
//   configure: what dtt has to call with, in three sub-tabs --
//                catalog:  the tools dtt found, each one's schema and where
//                          it came from, and whether tools-allow lets
//                          control runs use it
//                examples: the examples built into dtt ("dtt examples"),
//                          to leaf through and import: an import is a
//                          runnable instance in calls -- the example's
//                          tool with its document filled in (and its
//                          declared variables, at their defaults, in the
//                          variables input), in read mode until you switch
//                          it to assert, or (a tool-call example) with the
//                          call's inputs filled in -- kept, edits included,
//                          until this page is reloaded
//                author:   check a pasted tool.yaml, validate or install a
//                          tool picked on the host ("select from host…"),
//                          scaffold a new one
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

// One of a run's coarse events: at is unix ms, from 'lr' or 'dtt'.
export interface ToolRunEvent {
  at: number
  from: string
  event: string
  detail?: string
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
  // Through agent-coordinator only the newest run's events come along;
  // event_count still says how many the host holds.
  events: ToolRunEvent[] | null
  event_count?: number
  events_dropped?: number
  progress?: ToolRunProgress // a declarative tool's, step by step
}

// One step of a run's plan (tools.go's ToolRunStep; dtt/progress.go): a
// statement of its document, the steps it depends on (after) and its
// level in that graph, and how it stands -- pending until begun, running,
// then ok or failed. stage remeasure is an assert's measuring again.
export interface ToolRunStep {
  id: string
  key: string
  path: string
  provider: string // github, git, filesystem, aws, localrep, timestamp, dtt, delay, variables
  alias?: string
  line: number
  phase?: string
  asserts?: boolean
  after: string[]
  level: number
  status: 'pending' | 'running' | 'ok' | 'failed'
  stage?: 'measure' | 'remeasure'
  started_at?: number
  finished_at?: number
  duration_ms?: number
  error?: string
  objects?: string[] // left out through agent-coordinator
  objects_omitted?: number
  summary?: Record<string, number>
}

// A run's progress, as dtt reported it while the run went (tools.go's
// ToolRunProgress).
export interface ToolRunProgress {
  mode?: string
  steps: ToolRunStep[]
  steps_omitted?: number
  updated: number
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

// An example built into dtt (dtt/examples.go): a DTT document, or a tool
// call's inputs.
export interface ToolExample {
  name: string
  kind?: 'document' | 'call'
  title: string
  description: string
  tool: string // the built-in tool that runs it; '' for none
  providers: string[]
  inputs?: Record<string, string> // a call's
  commands: string[]
  path: string
  source: string
  error?: string
}

// A runnable instance in calls: an example imported for its tool. Its
// inputs are the calls sub-tab's values under instanceKey(id).
export interface ToolInstance {
  id: string
  tool: string
  example: string
  title: string
  call: boolean // imported from a tool-call example
}

// What a recorded call's stdout said (tools.go's ToolHistorySummary): dtt
// run's result, a read/assert/delete's image, or another answer. image is
// the image's summary: outcome -> how many fields had it.
export interface ToolHistorySummary {
  kind: 'result' | 'image' | 'answer'
  ok: boolean
  error?: string
  mode?: string
  image?: Record<string, number>
}

// One dtt call clauditable recorded in <records>/dtt-calls: id is its file
// name there, at unix ms. mode is run, read, assert or delete; tool is a
// run's, document a read/assert/delete's.
export interface ToolHistoryEntry {
  id: string
  at: number
  session?: string
  session_record?: string
  host?: string
  agent?: string
  command: string
  mode?: string
  tool?: string
  document?: string
  ok: boolean
  exit_code: number
  duration_ms: number
  summary?: ToolHistorySummary
  image_path?: string
  partial?: boolean
}

// One recorded call in full (history-detail).
export interface ToolHistoryDetail extends ToolHistoryEntry {
  raw_command: string
  stdout: string
  stderr: string
  note?: string
}

// One of dtt's recent calls (tools.go's ToolCall): a call's directory -- an
// in-place call's own, or a pathless call's staging area -- and its latest
// execution (at, mode, ok and the image it kept).
export interface ToolCall {
  dir: string
  kind: 'in-place' | 'pathless'
  name: string
  document: string
  from?: string // pathless: the file it was called from
  at: string
  mode: string
  ok: boolean
  image?: string
  executions: number
}

// An image kept for a call, from its file's name.
export interface ToolCallImage {
  name: string
  path: string
  at?: string
  mode?: string
}

// One call described (call-detail): its document (source) and its images,
// newest first. staged is whether dir exists yet (a pathless call's does
// once it's executed).
export interface ToolCallDetail {
  dir: string
  kind: 'in-place' | 'pathless'
  name: string
  document: string
  staged: boolean
  source: string
  images: ToolCallImage[]
  images_omitted?: number
  recent?: ToolCall
  includes?: string[] // in-place: the dtt examples its .dtttc includes
  note?: string
}

// One of dtt's example tool call specifications (example-calls; dtt's
// examples/specifications/): an in-place call, unpacked on the host at dir,
// which the calls sub-tab opens it by. source may be left out through
// agent-coordinator (the call's detail has its document).
export interface ToolExampleCall {
  name: string
  title: string
  description: string
  commands: string[]
  path: string
  dir: string
  document: string
  includes: string[]
  source: string
  error?: string
}

// The demo tab's (Demo.tsx; local-representative's demos.go): a demo is a
// DTT composition run as one declare tool call, and each run is kept on the
// host as a DemoRecord.
export interface DemoVariable { name: string; default?: string; help?: string }
export interface DemoPhase { name: string; role: 'lead-in' | 'subject' | 'tear-down' | 'compose'; calls?: string[] }
// DemoOrigin is where a demo or suite comes from: embedded, in-tree in
// AI-evo1-spiral and built into LR, or configured, on the host's own
// filesystem (its library -- duplicates, proposals' demos -- in no repo).
export type DemoOrigin = 'embedded' | 'configured'
export interface DemoInfo {
  name: string
  title: string
  description: string
  origin: DemoOrigin
  path?: string
  variables: DemoVariable[]
  phases: DemoPhase[]
  source?: string
  error?: string
  coverage?: DemoCoverage
  // sut is the kind of system under test: local (LSUT, a node picked
  // through its node variable) or same (SSUT, the node running it).
  sut?: 'local' | 'same'
}
// DemoCoverage is what of the project a demo or suite exercises, declared
// (local-representative's democoverage.go): project paths, those ending
// "/" directories -- focal what it's about, supporting what it needs on
// the way. The protosurf map picks them out.
export interface DemoCoverage { focal: string[]; supporting: string[] }
// DemoRender is a video a demo run rendered: the demo's own, or one inside
// a document it called (call is the alias down to it). kept is its file in
// the record's videos/, keep_error why it isn't there.
export interface DemoRender {
  composition: string
  phase?: string
  call?: string
  file_id: string
  name: string
  kept?: string
  size?: number
  keep_error?: string
  primary?: boolean // the record's one primary video: its subject, without lead-in or tear-down
}
// DemoArtifact is a file in a record's artifacts/ (file, relative to it),
// served at <hostApi>/api/demo-records/<id>/artifacts/<file>.
export interface DemoArtifact {
  kind: 'summary' | 'image' | 'outputs' | 'log' | 'screenshot'
  file?: string
  size?: number
  phase?: string
  call?: string
  file_id?: string
  keep_error?: string
}
// DemoAnalysis is what a record's analysis/ holds: frames pulled from its
// videos (Step 5, Revision A). contact_sheets are paths in the record,
// served at <hostApi>/api/demo-records/<id>/<path>, as is MAP.md.
export interface DemoAnalysis {
  video?: string
  step_frames: number
  scene_frames: number
  failure_frames?: number
  contact_sheets?: string[]
  notes?: string[]
}
export interface DemoRecord {
  id: string
  demo: string
  title: string
  mode: 'assert' | 'read' | 'delete'
  undo_of?: string
  variables: Record<string, string>
  node: string
  origin: string
  tool: string
  run_id?: string
  status: 'running' | 'ok' | 'failed' | 'interrupted'
  started_at: number
  finished_at?: number
  error?: string
  summary?: Record<string, number>
  phases?: { name: string; role: DemoPhase['role']; status: string; duration_ms?: number; error?: string }[]
  renders?: DemoRender[]
  has_image: boolean
  log_tail?: string
  dir: string
  artifacts?: DemoArtifact[]
  suite?: string
  suite_record?: string
  analysis?: DemoAnalysis
}

// A suite (local-representative's suites.go) runs demos one after another,
// its variables -- every variable its demos declare -- asked for once.
export interface SuiteVariable { name: string; default?: string; help?: string; demos: string[] }
export interface SuiteInfo {
  name: string
  title: string
  description: string
  origin: DemoOrigin
  path?: string
  demos: string[]
  on_failure: 'continue' | 'stop'
  variables: SuiteVariable[]
  source?: string
  error?: string
  coverage?: DemoCoverage
}
export interface SuiteEntry {
  demo: string
  record?: string
  status: 'pending' | 'running' | 'ok' | 'failed' | 'skipped' | 'interrupted'
  error?: string
  variables: Record<string, string>
  started_at?: number
  finished_at?: number
}
export interface SuiteRecord {
  id: string
  suite: string
  title: string
  variables: Record<string, string>
  node: string
  origin: string
  on_failure: string
  status: 'running' | 'ok' | 'failed' | 'interrupted'
  started_at: number
  finished_at?: number
  entries: SuiteEntry[]
  dir: string
}

// DemoReview is the worker's read-only review of a demo record
// (local-representative's demoreview.go).
export interface DemoReview {
  id: string
  record: string
  question?: string
  agent?: string
  model?: string
  status: 'submitted' | 'pending' | 'processing' | 'completed' | 'failed'
  signal_id?: string
  signal_file?: string
  requested_at: number
  started_at?: number
  finished_at?: number
  comment?: string
  output?: string
  output_trimmed?: boolean
  error?: string
}

// DemoProposal is a demo the worker's agent builds (local-representative's
// demoproposals.go): its name, title and description, where it stands, and
// once built, the demo put into the library from it, its icon (a data:
// URL), and its demo's declared coverage. files come only with the
// demo-proposal-* ops' replies (a listing relayed through agent-coordinator
// may leave them out).
export interface DemoProposal {
  name: string
  title: string
  description: string
  job?: string // the worker's creation job
  demo?: string // the demo put into the library from it
  created_at: number
  updated_at?: number
  dir?: string
  status?: 'creating' | 'created' | 'failed'
  error?: string
  demo_file?: string // the demo document, relative to dir
  files?: string[] // relative to dir
  icon?: string
  coverage?: DemoCoverage
}

// ProtosurfFile is one file on the demo tab's protosurf map
// (local-representative's protosurf.go), as the array [dir, name, lang,
// role, lines, code, comment, folded?]: dir, lang and role index the map's
// dirs, langs and roles; code counts the lines neither blank nor comments.
// folded, when there, counts the small files folded into this one entry to
// fit the relay.
export type ProtosurfFile = [number, string, number, number, number, number, number, number?]

export interface ProtosurfMap {
  root: string
  source: 'git' | 'walk'
  scanned_at: number
  dirs: string[]
  langs: string[]
  roles: string[]
  files: ProtosurfFile[]
  skipped?: Record<string, number>
  folded?: number
}

export interface ToolsRequest {
  op: 'reinspect' | 'run' | 'save' | 'to-file' | 'describe' | 'validate' | 'install' | 'create' | 'examples' | 'history' | 'history-detail'
    | 'recent-calls' | 'call-detail' | 'example-calls'
    | 'demos' | 'demo-run' | 'demo-record' | 'demo-duplicate' | 'demo-keep-videos'
    | 'suite-run' | 'demo-review' | 'demo-reviews' | 'protosurf'
    | 'demo-proposal' | 'demo-proposal-agent-create' | 'demo-proposal-delete'
  demo?: string
  proposal?: DemoProposal // demo-proposal-agent-create's: its name, title and description
  mode?: 'assert' | 'read' | 'delete'
  variables?: Record<string, string>
  record?: string
  suite?: string
  question?: string
  agent?: string
  model?: string
  tool?: string
  call?: string // history-detail's: a ToolHistoryEntry's id
  args?: Record<string, string>
  run?: string
  source?: string
  path?: string // also call-detail's: a call's directory or document
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
  examples?: ToolExample[]
  history?: ToolHistoryEntry[] // newest first
  history_omitted?: number
  call?: ToolHistoryDetail
  recent_calls?: ToolCall[] // most recently executed first
  recent_calls_file?: string
  call_detail?: ToolCallDetail
  example_calls?: ToolExampleCall[]
  demos?: DemoInfo[]
  demo_records?: DemoRecord[] // newest first
  demo_record?: DemoRecord
  demo_image?: string
  demo_dir?: string
  suites?: SuiteInfo[]
  suite_records?: SuiteRecord[] // newest first
  suite_record?: SuiteRecord
  demo_last_values?: Record<string, string>
  demo_reviews?: DemoReview[] // newest first
  demo_proposals?: DemoProposal[] // most recently changed first
  demo_proposal?: DemoProposal
  protosurf?: ProtosurfMap
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

const SUBTABS = ['calls', 'history', 'configure'] as const
type SubTab = typeof SUBTABS[number]
const CONFIGURE_SUBTABS = ['catalog', 'examples', 'author'] as const
type ConfigureTab = typeof CONFIGURE_SUBTABS[number]
// The call pane's two views of a call: its text (definition,
// specification, executions) and its visual (an execution's steps).
const CALL_VIEWS = ['text', 'visual'] as const
type CallView = typeof CALL_VIEWS[number]

// The calls sub-tab's values are keyed by tool name, by instanceKey for an
// imported example's instance (tool names can't start with '@'), or by
// callKey for a call's directory.
const instanceKey = (id: string) => '@' + id

type CallValues = Record<string, Record<string, string>>

// A declarative tool's output holding its image, and the input an image
// goes back in (dtt/README.md "Declarative tools").
const IMAGE_OUTPUT = 'image_json'
const IMAGE_INPUT = 'from_image'

// A built-in declarative tool's input giving the document's variables, as
// name=value lines (dtt/README.md "Variables").
const VARIABLES_INPUT = 'variables'

// A variable a document's variables statement declares; def is undefined
// when it has no default (the caller has to give it).
interface DocVariable {
  name: string
  def?: string
  help?: string
}

// yamlScalar reads one plain or quoted YAML scalar as dtt's yamlite would,
// a trailing # comment dropped.
function yamlScalar(raw: string): string {
  const s = raw.trim()
  if (s.startsWith('"')) {
    const m = s.match(/^"((?:[^"\\]|\\.)*)"/)
    if (m) {
      try {
        return JSON.parse(`"${m[1]}"`)
      } catch {
        return m[1]
      }
    }
  }
  if (s.startsWith("'")) {
    const m = s.match(/^'((?:[^']|'')*)'/)
    if (m) return m[1].replace(/''/g, "'")
  }
  return s.replace(/(^|\s+)#.*$/, '').trim()
}

// documentVariables finds the variables a DTT document declares:
//
//   variables:
//     node:
//       default: ""
//       help: the node to run on
//     take: "1"          (a single value is the default)
//
// It reads only that statement's simple shape -- enough to fill a call's
// variables input; dtt itself checks the document when it runs.
function documentVariables(src: string): DocVariable[] {
  const lines = src.replace(/\r\n/g, '\n').split('\n').map(l => l.replace(/\t/g, '    '))
  const indent = (l: string) => l.length - l.trimStart().length
  const blank = (l: string) => l.trim() === '' || l.trim().startsWith('#')
  const start = lines.findIndex(l => /^variables:\s*(#.*)?$/.test(l))
  if (start < 0) return []
  const out: DocVariable[] = []
  let nameIndent = -1
  let cur: DocVariable | null = null
  for (const l of lines.slice(start + 1)) {
    if (blank(l)) continue
    const ind = indent(l)
    if (ind === 0) break // the next statement
    const m = l.trim().match(/^([A-Za-z][A-Za-z0-9_-]*):(?:\s+(.*))?$/)
    if (!m) continue
    const value = m[2] === undefined || m[2].trim().startsWith('#') ? undefined : m[2]
    if (nameIndent < 0) nameIndent = ind
    if (ind === nameIndent) {
      cur = { name: m[1] }
      if (value !== undefined) cur.def = yamlScalar(value) // name: default
      out.push(cur)
    } else if (cur && ind > nameIndent) {
      if (m[1] === 'default') cur.def = value === undefined ? '' : yamlScalar(value)
      else if (m[1] === 'help' && value !== undefined) cur.help = yamlScalar(value)
    }
  }
  return out
}

// variablesText is the variables input for an imported document: each
// variable as name=default, its help as a # comment above it (dtt skips
// # lines). One with no default is left commented out, to be filled in.
function variablesText(vars: DocVariable[]): string {
  return vars.map(v => {
    const help = `# ${v.name}${v.def === undefined ? ' (required)' : ''}${v.help ? `: ${v.help}` : ''}`
    return v.def === undefined ? `${help}\n# ${v.name}=` : `${help}\n${v.name}=${v.def}`
  }).join('\n')
}

function prettyJSON(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2)
  } catch {
    return s
  }
}

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
    : <span className="tools-badge tools-badge-no" title="tools-allow doesn't list it: only this node's own calls sub-tab may call it">not allowed</span>
}

export function ToolsPanel(props: ToolsPanelProps) {
  const [sub, setSub] = useState<SubTab>('calls')
  const [configure, setConfigure] = useState<ConfigureTab>('catalog')
  const [callTool, setCallTool] = useState('') // a tool's name or an instance's key
  const [values, setValues] = useState<CallValues>({})
  const [instances, setInstances] = useState<ToolInstance[]>([])
  const [seq, setSeq] = useState(0)
  // The run each call's form (by its values' key) started last, and the
  // runs whose image has been put in their form already.
  const [runOf, setRunOf] = useState<Record<string, string>>({})
  const [imaged, setImaged] = useState<Record<string, boolean>>({})
  // How the call in calls is shown: as text or as its visual.
  const [callView, setCallView] = useState<CallView>('text')
  const { state } = props
  const reinspect = useRequest(props.request, props.replies)

  // A form's run that finished with an image fills the form's from_image --
  // what it read, for the next run (an assert may then change what is
  // still as it saw it). Once per run, so clear stays cleared.
  useEffect(() => {
    for (const [key, id] of Object.entries(runOf)) {
      const r = state?.runs.find(x => x.id === id)
      if (!r || r.status === 'running' || imaged[id]) continue
      setImaged(prev => ({ ...prev, [id]: true }))
      const img = r.result?.outputs?.[IMAGE_OUTPUT]
      if (img) setValues(prev => ({ ...prev, [key]: { ...(prev[key] ?? {}), [IMAGE_INPUT]: prettyJSON(img) } }))
    }
  }, [state?.runs, runOf]) // eslint-disable-line react-hooks/exhaustive-deps

  const openCall = (name: string) => {
    setCallTool(name)
    setSub('calls')
  }

  // Importing an example makes a new instance of its tool -- a document
  // example's document in the document input, its variables (with their
  // defaults, to edit) in the variables input, and mode read; a tool-call
  // example's inputs as they are -- and opens it in calls.
  const importExample = (ex: ToolExample) => {
    const id = `${ex.name}-${seq + 1}`
    setSeq(seq + 1)
    const sameName = instances.filter(i => i.example === ex.name).length
    const call = ex.kind === 'call'
    const doc: Record<string, string> = { document: ex.source, mode: 'read' }
    const vars = call ? [] : documentVariables(ex.source)
    const takesVars = state?.tools.find(t => t.name === ex.tool)?.args.some(a => a.name === VARIABLES_INPUT) ?? false
    if (vars.length > 0 && takesVars) doc[VARIABLES_INPUT] = variablesText(vars)
    setInstances(prev => [...prev, { id, tool: ex.tool, example: ex.name, title: sameName ? `${ex.name} (${sameName + 1})` : ex.name, call }])
    setValues(prev => ({ ...prev, [instanceKey(id)]: call ? { ...(ex.inputs ?? {}) } : doc }))
    openCall(instanceKey(id))
  }

  const removeInstance = (id: string) => {
    setInstances(prev => prev.filter(i => i.id !== id))
    setValues(prev => {
      const next = { ...prev }
      delete next[instanceKey(id)]
      return next
    })
    if (callTool === instanceKey(id)) setCallTool('')
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
      {sub === 'calls' && (
        <Calls {...props} selected={callTool} onSelect={setCallTool} values={values} setValues={setValues}
          instances={instances} onRemove={removeInstance} runOf={runOf} view={callView} onView={setCallView}
          onStarted={(key, id) => setRunOf(prev => ({ ...prev, [key]: id }))} />
      )}
      {sub === 'history' && <CallHistory {...props} />}
      {sub === 'configure' && (
        <>
          <div className="ctl-subtabs">
            {CONFIGURE_SUBTABS.map(t => (
              <button key={t} className={`ctl-subtab${configure === t ? ' ctl-subtab-active' : ''}`} onClick={() => setConfigure(t)}>
                {t}
              </button>
            ))}
          </div>
          {configure === 'catalog' && <Catalog {...props} onCall={openCall} />}
          {configure === 'examples' && <Examples {...props} instances={instances} onImport={importExample} onOpen={openCall} />}
          {configure === 'author' && <Author {...props} />}
        </>
      )}
    </div>
  )
}

// ---- Catalog ----

function Catalog({ state, request, replies, connected, onCall }: ToolsPanelProps & { onCall: (name: string) => void }) {
  const [open, setOpen] = useState<string | null>(null)
  if (!state) return null
  const allow = state.allow ?? []
  return (
    <div className="ctl-v1-view">
      <div className="ctl-hint">
        tools-allow: {allow.length === 0 ? 'every tool' : allow.join(', ')}
        {state.allow_from ? ` (from ${state.allow_from})` : ''} — which tools control runs' tool.&lt;name&gt; ops and
        agent-coordinator may run. This node's own calls sub-tab may call any tool.
      </div>
      {state.tools.length === 0 && <div className="ctl-empty">no tools found</div>}
      {state.tools.map(t => (
        <div key={t.name} className="ctl-seq">
          <div className="ctl-seq-head tools-head" onClick={() => setOpen(open === t.name ? null : t.name)}>
            <span className="tools-icon">{toolIcon(t.name)}</span>
            <span className="ctl-seq-name">{t.name}</span>
            <span className="tools-badge">{t.origin}</span>
            <AllowedBadge tool={t} />
            <span className="tools-grow" />
            <button className="ctl-btn" title="open a call of it in calls" onClick={e => { e.stopPropagation(); onCall(t.name) }}>call…</button>
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

// ---- Calls ----

// The calls sub-tab is keyed by calls' directories (dtt/docs/terms.md, "In-
// place and pathless calls"): an in-place call's own, or a pathless call's
// staging area. Its left-hand bar lists dtt's recent calls (dtt calls, from
// ~/.ufa/dtt/recent-calls.json) to pick one from, takes an unlisted one's
// directory or document, lists dtt's example specifications (examples,
// folded until opened; each an in-place call unpacked on the host), and
// starts a new call -- an empty document, whose statements name whatever
// tools it uses, so there's no tool to choose first; or an imported
// example's instance. A new call of a document is pathless, and joins the
// recent calls once it's executed.

// A call's directory as a key of the sub-tab's values: '#' and the
// directory (tool names can't start with '#', nor instanceKey's '@').
const CALL_KEY = '#'
const callKey = (dir: string) => CALL_KEY + dir

// The new call's key (tool names can't start with '+' either), and the
// document it starts with.
const NEW_CALL = '+new'
const NEW_DOCUMENT = `# A new tool call definition. Its statements name the objects to read (and
# assert) with whichever providers they need -- github, git, filesystem, aws,
# localrep, dtt.calls -- so the tools it uses are in the document itself
# (dtt/README.md "Documents"). Executed, it's a pathless call.

timestamp:
  select: now
`

// The built-in tool a call's directory is executed with, as its
// document_file -- declare reads every provider's statements -- and the
// inputs the directory stands for.
const CALL_TOOL = 'declare'
const DOCUMENT_INPUTS = ['document', 'document_file']

const MAX_CALL_IMAGES_SHOWN = 10

// callLabel names a recent call in the bar: a pathless one's staging name
// with the start of its document's hash (many inline documents share the
// name document).
function callLabel(c: ToolCall): string {
  if (c.kind !== 'pathless') return c.name
  const hash = c.dir.split('/').filter(Boolean).pop() ?? ''
  return `${c.name} ${hash.slice(0, 8)}`
}

function BarItem({ label, sub, title, active, onClick }: { label: string; sub?: string; title?: string; active: boolean; onClick: () => void }) {
  return (
    <button className={`tools-calls-item${active ? ' tools-calls-item-active' : ''}`} title={title} onClick={onClick}>
      <span className="tools-calls-item-label">{label}</span>
      {sub && <span className="tools-calls-item-sub">{sub}</span>}
    </button>
  )
}

// Calls is a tool call in its three parts (dtt/docs/terms.md): the
// definition (a call's directory and its document; or, for a new call, a
// document of its own, or an imported example's instance of its tool), the
// specification (its inputs, until every required one is given) and its
// executions (the recent runs, this one's and everyone's here).
function Calls({ state, request, replies, connected, fileUrl, selected, onSelect, values, setValues, instances, onRemove, runOf, view, onView, onStarted }: ToolsPanelProps & {
  selected: string
  onSelect: (name: string) => void
  values: CallValues
  setValues: (f: (prev: CallValues) => CallValues) => void
  instances: ToolInstance[]
  onRemove: (id: string) => void
  runOf: Record<string, string>
  view: CallView
  onView: (v: CallView) => void
  onStarted: (key: string, runId: string) => void
}) {
  const tools = state?.tools ?? []
  const recents = useRequest(request, replies)
  const examples = useRequest(request, replies)
  const run = useRequest(request, replies)
  const [open, setOpen] = useState<string | null>(null)
  const [sentKey, setSentKey] = useState('')
  const [unlisted, setUnlisted] = useState('')
  // The examples are listed (and dtt unpacks them on the host) each time
  // they're unfolded.
  const [examplesOpen, setExamplesOpen] = useState(false)
  useEffect(() => {
    if (connected && examplesOpen) examples.send({ op: 'example-calls' })
  }, [connected, examplesOpen]) // eslint-disable-line react-hooks/exhaustive-deps
  const exampleCalls = examples.reply?.ok ? examples.reply.example_calls ?? [] : []
  // A tool opened from configure → catalog is a new call of it.
  const pickedTool = tools.find(t => t.name === selected)
  // Changes as each execution finishes -- when the recent calls (and the
  // call shown) are asked for again.
  const finished = (state?.runs ?? []).filter(r => r.status !== 'running').map(r => r.id).join(' ')

  // The list stays as it was while it's asked for again.
  const [calls, setCalls] = useState<ToolCall[]>([])
  useEffect(() => {
    if (connected) recents.send({ op: 'recent-calls' })
  }, [connected, finished]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (recents.reply?.ok) setCalls(recents.reply.recent_calls ?? [])
  }, [recents.reply])

  // With nothing picked yet, the most recent call is.
  useEffect(() => {
    if (!selected && calls.length > 0) onSelect(callKey(calls[0].dir))
  }, [calls]) // eslint-disable-line react-hooks/exhaustive-deps

  // An execution that started opens in the list, and its form takes its
  // image.
  useEffect(() => {
    if (run.reply?.ok && run.reply.run_id) {
      setOpen(run.reply.run_id)
      onStarted(sentKey, run.reply.run_id)
    }
  }, [run.reply]) // eslint-disable-line react-hooks/exhaustive-deps

  const dir = selected.startsWith(CALL_KEY) ? selected.slice(CALL_KEY.length) : ''
  const key = dir ? selected : ''
  const vals = values[key] ?? {}
  const setVal = (k: string, v: string) => key && setValues(prev => ({ ...prev, [key]: { ...(prev[key] ?? {}), [k]: v } }))
  const execute = (k: string, tool: string, args: Record<string, string>) => {
    setSentKey(k)
    run.send({ op: 'run', tool, args })
  }
  const openUnlisted = () => unlisted.trim() && onSelect(callKey(unlisted.trim()))
  // The key the pane's form is under (as ToolPane works it out), whose
  // last execution the visual shows.
  const paneKey = dir ? key
    : instances.some(i => instanceKey(i.id) === selected) ? selected
    : pickedTool ? pickedTool.name : NEW_CALL

  return (
    <div className="tools-calls">
      <div className="tools-calls-bar">
        <div className="ctl-section-title">recent calls</div>
        {recents.pending && calls.length === 0 && <div className="ctl-hint">listing…</div>}
        {recents.reply && !recents.reply.ok && <div className="ctl-run-error-msg">{recents.reply.error}</div>}
        {recents.reply?.ok && calls.length === 0 && <div className="ctl-hint">none yet — executing a DTT document puts its call here</div>}
        {calls.map(c => (
          <BarItem key={c.dir} label={callLabel(c)} active={selected === callKey(c.dir)} onClick={() => onSelect(callKey(c.dir))}
            title={`${c.dir}\nlast executed ${new Date(c.at).toLocaleString()} (${c.mode}${c.ok ? '' : ', failed'}), ${c.executions} execution${c.executions === 1 ? '' : 's'} while listed`}
            sub={`${c.kind} · ${c.mode} ${c.ok ? '✓' : '✗'} · ${new Date(c.at).toLocaleString()}`} />
        ))}
        <div className="ctl-section-title">unlisted</div>
        <input className="ctl-input" value={unlisted} placeholder="a call's directory or document" spellCheck={false}
          title="an absolute path on the host: a directory holding .dtttc, a pathless call's staging directory, or a *.dtt.yaml"
          onChange={e => setUnlisted(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') openUnlisted() }} />
        <button className="ctl-btn" disabled={!unlisted.trim()} onClick={openUnlisted}>open</button>
        <details className="tools-calls-examples" onToggle={e => setExamplesOpen(e.currentTarget.open)}>
          <summary className="ctl-section-title" title="dtt's example tool call specifications (dtt/examples/specifications), each an in-place call unpacked on the host">
            examples
          </summary>
          <div className="tools-calls-examples-list">
            {examples.pending && exampleCalls.length === 0 && <div className="ctl-hint">listing…</div>}
            {examples.reply && !examples.reply.ok && <div className="ctl-run-error-msg">{examples.reply.error}</div>}
            {examples.reply?.ok && exampleCalls.length === 0 && <div className="ctl-hint">dtt has none</div>}
            {exampleCalls.map(ex => ex.dir && !ex.error ? (
              <BarItem key={ex.name} label={ex.name} active={selected === callKey(ex.dir)} onClick={() => onSelect(callKey(ex.dir))}
                title={`${ex.title}\n${ex.dir}${ex.description ? `\n\n${ex.description}` : ''}`}
                sub={ex.includes.length > 0 ? `includes ${ex.includes.join(', ')}` : 'self-contained'} />
            ) : (
              <div key={ex.name} className="ctl-run-error-msg" title={ex.path}>{ex.name}: {ex.error || 'not unpacked on the host'}</div>
            ))}
          </div>
        </details>
        <div className="ctl-section-title">new call</div>
        <BarItem label="new document" sub={`a definition, executed by ${CALL_TOOL}`} active={selected === NEW_CALL}
          title="start a new tool call definition: a DTT document naming whatever tools it uses" onClick={() => onSelect(NEW_CALL)} />
        {pickedTool && <BarItem label={pickedTool.name} sub={`tool · ${pickedTool.origin}`} title={pickedTool.description} active onClick={() => onSelect(pickedTool.name)} />}
        {instances.map(i => (
          <BarItem key={i.id} label={i.title} sub={`example · ${i.tool}`} title={`imported from dtt's example ${i.example}`}
            active={selected === instanceKey(i.id)} onClick={() => onSelect(instanceKey(i.id))} />
        ))}
      </div>
      <div className="ctl-v1-view tools-calls-main">
        <div className="ctl-subtabs tools-call-views">
          {CALL_VIEWS.map(v => (
            <button key={v} className={`ctl-subtab${view === v ? ' ctl-subtab-active' : ''}`} onClick={() => onView(v)}
              title={v === 'text' ? "the call's definition, specification and executions" : "an execution's steps, in the order of their dependencies, as they go"}>
              {v}
            </button>
          ))}
        </div>
        {view === 'text' && (
          <>
            {dir ? (
              <CallDirPane dir={dir} state={state} request={request} replies={replies} connected={connected} refresh={finished}
                vals={vals} setVal={setVal} busy={run.pending} onSelect={onSelect}
                onExecute={args => execute(key, CALL_TOOL, args)} />
            ) : (
              <ToolPane state={state} selected={selected} values={values} setValues={setValues} instances={instances} onRemove={onRemove}
                connected={connected} busy={run.pending} onExecute={execute} />
            )}
            <ReplyStatus r={run} working="starting…" />
            <RunList state={state} request={request} replies={replies} connected={connected} fileUrl={fileUrl} open={open} onOpen={setOpen} />
          </>
        )}
        {view === 'visual' && <CallVisual state={state} lastRun={runOf[paneKey]} />}
      </div>
    </div>
  )
}

// CallDirPane is the call in dir (dtt calls <dir>): its document, the
// specification of executing it again (the declare tool's inputs, its
// document_file the call), and the images kept for it.
function CallDirPane({ dir, state, request, replies, connected, refresh, vals, setVal, busy, onSelect, onExecute }: {
  dir: string
  state: ToolsStateMsg | null
  request: ToolsRequestFn
  replies: Record<string, ToolsReply>
  connected: boolean
  refresh: string
  vals: Record<string, string>
  setVal: (k: string, v: string) => void
  busy: boolean
  onSelect: (key: string) => void
  onExecute: (args: Record<string, string>) => void
}) {
  const detail = useRequest(request, replies)
  useEffect(() => {
    if (connected) detail.send({ op: 'call-detail', path: dir })
  }, [dir, connected, refresh]) // eslint-disable-line react-hooks/exhaustive-deps
  // The call stays shown while it's asked for again (after an execution).
  const [shown, setShown] = useState<{ dir: string; d: ToolCallDetail } | null>(null)
  useEffect(() => {
    if (detail.reply?.ok && detail.reply.call_detail) setShown({ dir, d: detail.reply.call_detail })
  }, [detail.reply]) // eslint-disable-line react-hooks/exhaustive-deps
  const d = shown?.dir === dir ? shown.d : undefined

  // A call entered by its document, or its directory written another way,
  // is keyed by its directory once dtt has said which it is (a pathless
  // document not yet executed has no directory to key it by).
  useEffect(() => {
    if (d && d.dir && d.dir !== dir && (d.kind === 'in-place' || d.staged)) onSelect(callKey(d.dir))
  }, [d]) // eslint-disable-line react-hooks/exhaustive-deps
  // The variables the document declares fill the variables input, at their
  // defaults, until it's been given.
  useEffect(() => {
    if (!d || vals[VARIABLES_INPUT] !== undefined) return
    const vars = documentVariables(d.source)
    if (vars.length > 0) setVal(VARIABLES_INPUT, variablesText(vars))
  }, [d]) // eslint-disable-line react-hooks/exhaustive-deps

  const tool = state?.tools.find(t => t.name === CALL_TOOL)
  // What document_file is: the call's directory, or -- a pathless document
  // not executed yet -- the document itself.
  const target = d ? (d.kind === 'pathless' && !d.staged ? d.document : d.dir) : dir
  const args = tool?.args.filter(a => !DOCUMENT_INPUTS.includes(a.name)) ?? []
  const missing = args.filter(a => a.required && !(vals[a.name] ?? '').trim())
  const moreImages = d ? Math.max(0, d.images.length - MAX_CALL_IMAGES_SHOWN) + (d.images_omitted ?? 0) : 0

  return (
    <div className="ctl-section">
      <div className="ctl-section-title">call</div>
      {detail.pending && !d && <div className="ctl-hint">reading {dir}…</div>}
      {detail.reply && !detail.reply.ok && <div className="ctl-run-error-msg">{detail.reply.error}</div>}
      {d && (
        <>
          <div className="ctl-row">
            <span className="ctl-seq-name">{d.name || dir}</span>
            <span className="tools-badge" title={d.kind === 'in-place' ? 'a directory holding .dtttc' : `staged under dtt's pathless-dir`}>{d.kind}</span>
            {d.recent && <span className="tools-badge">{d.recent.executions} execution{d.recent.executions === 1 ? '' : 's'}</span>}
            {d.kind === 'pathless' && !d.staged && <span className="tools-badge">not executed yet</span>}
          </div>
          <div className="tools-line"><b>directory</b> {d.dir || '—'}</div>
          <div className="tools-line"><b>document</b> {d.document}</div>
          {d.recent?.from && <div className="tools-line"><b>called from</b> {d.recent.from}</div>}
          {(d.includes ?? []).length > 0 && (
            <div className="tools-line" title="dtt examples its .dtttc includes: its document may read them though they aren't in its directory">
              <b>includes</b> {(d.includes ?? []).map(i => `dtt's examples/${i}`).join(', ')}
            </div>
          )}
          {d.note && <div className="ctl-hint">{d.note}</div>}
          <div className="ctl-section-title">definition</div>
          <details open>
            <summary className="ctl-hint">the document ({formatSize(d.source.length)})</summary>
            <pre className="tools-pre">{d.source}</pre>
          </details>
          <div className="ctl-section-title">specification</div>
          {!tool ? (
            <div className="ctl-run-error-msg">dtt doesn't list the {CALL_TOOL} tool here — re-inspect, or check where dtt looked in configure → catalog</div>
          ) : (
            <>
              <div className="ctl-row">
                <span className="ctl-hint">executed by {tool.name} with document_file {target}</span>
                <AllowedBadge tool={tool} />
              </div>
              <SpecFields args={args} vals={vals} setVal={setVal} />
              <div className="ctl-row">
                <button
                  className="sys-btn sys-btn-launch"
                  disabled={!connected || busy || missing.length > 0}
                  title={missing.length > 0 ? `fill in ${missing.map(a => a.name).join(', ')}` : `execute this call through dtt's ${tool.name} tool`}
                  onClick={() => onExecute({ ...Object.fromEntries(Object.entries(vals).filter(([k]) => !DOCUMENT_INPUTS.includes(k))), document_file: target })}
                >
                  execute
                </button>
                <span className="ctl-hint">
                  {missing.length > 0 ? `not yet specified: ${missing.map(a => a.name).join(', ')}` : 'specified — empty inputs take their defaults'}
                </span>
              </div>
            </>
          )}
          <div className="ctl-section-title">images</div>
          {d.images.length === 0 && <div className="ctl-hint">none kept yet</div>}
          {d.images.slice(0, MAX_CALL_IMAGES_SHOWN).map(im => (
            <div key={im.path} className="tools-line" title={im.path}>
              {im.at ? new Date(im.at).toLocaleString() : im.name} {im.mode && <span className="tools-badge">{im.mode}</span>} {im.path}
            </div>
          ))}
          {moreImages > 0 && <div className="ctl-hint">…and {moreImages} older</div>}
        </>
      )}
    </div>
  )
}

// SpecFields is a specification's form: an input for each of args, and the
// image input's clear button.
function SpecFields({ args, vals, setVal }: { args: ToolArg[]; vals: Record<string, string>; setVal: (k: string, v: string) => void }) {
  return (
    <>
      {args.map(a => (
        <Fragment key={a.name}>
          <Field label={a.name + (a.required ? ' *' : '') + (a.name === IMAGE_INPUT ? ' (image)' : '')}>
            <ArgInput arg={a} value={vals[a.name] ?? ''} onChange={v => setVal(a.name, v)} />
          </Field>
          {a.name === IMAGE_INPUT && (
            <div className="ctl-row">
              <button className="ctl-btn" disabled={!(vals[a.name] ?? '').trim()} title="empty the image, so the next run uses none"
                onClick={() => setVal(a.name, '')}>
                clear
              </button>
              <span className="ctl-hint">
                An execution here fills this with the image it returned: the reads of every field of every object it
                selected, as it left them. An assert or delete given it may change fields still as it saw them; clear
                it to execute without.
              </span>
            </div>
          )}
        </Fragment>
      ))}
    </>
  )
}

// ToolPane is a new call: a new document (NEW_CALL, and anything not a tool
// or an instance), executed by the declare tool; an imported example's
// instance of its tool; or a tool opened from configure → catalog.
function ToolPane({ state, selected, values, setValues, instances, onRemove, connected, busy, onExecute }: {
  state: ToolsStateMsg | null
  selected: string
  values: CallValues
  setValues: (f: (prev: CallValues) => CallValues) => void
  instances: ToolInstance[]
  onRemove: (id: string) => void
  connected: boolean
  busy: boolean
  onExecute: (key: string, tool: string, args: Record<string, string>) => void
}) {
  const tools = state?.tools ?? []
  const inst = instances.find(i => instanceKey(i.id) === selected)
  const picked = inst ? undefined : tools.find(t => t.name === selected)
  const isNew = !inst && !picked
  const tool = inst ? tools.find(t => t.name === inst.tool) : picked ?? tools.find(t => t.name === CALL_TOOL)
  const key = inst ? instanceKey(inst.id) : isNew ? NEW_CALL : picked?.name ?? ''

  const vals = values[key] ?? {}
  const setVal = (k: string, v: string) => key && setValues(prev => ({ ...prev, [key]: { ...(prev[key] ?? {}), [k]: v } }))
  // A new call's document is written here, never a file's.
  const args = (tool?.args ?? []).filter(a => !(isNew && a.name === 'document_file'))
  const missing = args.filter(a => a.required && !(vals[a.name] ?? '').trim())

  // A new call starts as an empty document, in read mode.
  useEffect(() => {
    if (isNew && tool && vals.document === undefined) {
      setValues(prev => ({ ...prev, [NEW_CALL]: { document: NEW_DOCUMENT, mode: 'read', ...(prev[NEW_CALL] ?? {}) } }))
    }
  }, [isNew, tool]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <>
      {isNew && !tool ? (
        <div className="ctl-run-error-msg">
          A new call is a document executed by dtt's {CALL_TOOL} tool, which dtt doesn't list here — re-inspect, or check where
          dtt looked in configure → catalog
        </div>
      ) : (
        <div className="ctl-section">
          <div className="ctl-section-title">definition</div>
          <div className="ctl-row">
            <span className="ctl-seq-name">{inst ? inst.title : isNew ? 'new call' : tool?.name}</span>
            {inst && <span className="ctl-hint">({inst.tool})</span>}
            {isNew && <span className="ctl-hint">(a document, executed by {CALL_TOOL})</span>}
            {tool && <AllowedBadge tool={tool} />}
            {inst && <span className="tools-badge" title={`imported from dtt's example ${inst.example}`}>example</span>}
            {inst && <button className="ctl-btn" title="drop this imported instance" onClick={() => onRemove(inst.id)}>remove</button>}
          </div>
          {inst && !tool && (
            <div className="ctl-run-error-msg">
              {inst.title} calls tool {inst.tool}, which dtt doesn't list here — re-inspect, or check where dtt looked in configure → catalog
            </div>
          )}
          {inst && tool && (inst.call ? (
            <div className="ctl-hint">
              Example {inst.example}, a call of {tool.name}: its inputs are filled in as the example gives them — edit them as
              you like (the edits stay with this instance) and execute.
            </div>
          ) : (
            <div className="ctl-hint">
              Example {inst.example}, imported for {tool.name}: edit its document as you like (the edits stay with this instance).
              {tool.args.some(a => a.name === VARIABLES_INPUT) && (
                <> Set the document's variables in {VARIABLES_INPUT}, name=value on each line — an import fills in each one the
                  document declares with its default (# lines are skipped; one with no default has to be given).</>
              )}
              {' '}It came in as mode read, which only reads — set mode to assert to make the changes, and later to delete
              to remove what the assert made.
            </div>
          ))}
          {isNew && tool && (
            <div className="ctl-hint">
              Write the call's definition in document: its statements name the tools it uses (the providers — github, git,
              filesystem, aws, localrep, dtt.calls), so there's no tool to choose first. It starts in mode read, which only
              reads; set mode to assert to make the changes. Executed, it's a pathless call and joins the recent calls.
            </div>
          )}
          {tool && (
            <>
              {!isNew && <div className="ctl-hint">{tool.description}</div>}
              <div className="ctl-section-title">specification</div>
              <SpecFields args={args} vals={vals} setVal={setVal} />
              <div className="ctl-row">
                <button
                  className="sys-btn sys-btn-launch"
                  disabled={!connected || busy || missing.length > 0}
                  title={missing.length > 0 ? `fill in ${missing.map(a => a.name).join(', ')}` : `execute this call of ${tool.name} through dtt`}
                  onClick={() => onExecute(key, tool.name, vals)}
                >
                  execute
                </button>
                <span className="ctl-hint">
                  {missing.length > 0 ? `not yet specified: ${missing.map(a => a.name).join(', ')}` : 'specified — empty inputs take their defaults'}
                </span>
              </div>
            </>
          )}
        </div>
      )}
    </>
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
    // An imported example's document shows whole, up to 30 lines.
    const rows = Math.min(30, Math.max(6, value.split('\n').length))
    return <textarea className="ctl-textarea" rows={rows} value={value} placeholder={placeholder} title={arg.help} spellCheck={false} onChange={e => onChange(e.target.value)} />
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
      <div className="ctl-section-title">executions</div>
      <div className="ctl-hint">the recent executions through this node's tools tab, control runs and agent-coordinator — every recorded dtt call on the host is in history</div>
      {runs.length === 0 && <div className="ctl-hint">no executions yet</div>}
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
  const toFile = useRequest(request, replies)
  const res = r.result
  const events = r.events ?? []
  const eventCount = r.event_count ?? events.length
  const icon = r.status === 'running' ? '…' : r.status === 'ok' ? '✓' : '✗'
  const statusClass = r.status === 'running' ? 'running' : r.status === 'ok' ? 'success' : 'error'
  return (
    <div className={`ctl-step ctl-step-${statusClass} tools-run`}>
      <span className="ctl-step-icon">{icon}</span>
      <div className="ctl-step-body">
        <div className="tools-head" onClick={onToggle}>
          <span className="tools-icon">{toolIcon(r.tool)}</span>
          <span className="ctl-step-label">{r.tool}</span>
          <span className="tools-badge">{r.origin}</span>
          <span className="ctl-hint">{new Date(r.started_at).toLocaleTimeString()}</span>
          <span className="tools-grow" />
          {res && <span className="ctl-step-time">{formatMs(res.duration_ms)}</span>}
          <button
            className="ctl-btn"
            disabled={!connected || toFile.pending}
            title={`send this execution's details — its ${eventCount} event${eventCount === 1 ? '' : 's'}, inputs, outputs and log — to the files tab as a .txt${r.status === 'running' ? ' (so far: it is still going)' : ''}`}
            onClick={e => { e.stopPropagation(); toFile.send({ op: 'to-file', run: r.id }) }}
          >
            {toFile.pending ? 'sending…' : 'to file'}
          </button>
        </div>
        {res?.error && <div className="ctl-step-msg">{res.error}</div>}
        {toFile.reply && (toFile.reply.ok && toFile.reply.file ? (
          <div className="ctl-ok">
            ✓ details sent to the files tab:{' '}
            <a className="ctl-save-link" href={fileUrl(toFile.reply.file.file_id)} target="_blank" rel="noreferrer">{toFile.reply.file.name}</a>
          </div>
        ) : !toFile.reply.ok && <div className="ctl-run-error-msg">{toFile.reply.error}</div>)}
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
                {Object.entries(res.outputs).map(([k, v]) => k === IMAGE_OUTPUT ? (
                  <details key={k}>
                    <summary><span className="ctl-value-label">{`{{${k}}}`}</span> <span className="ctl-hint">the image ({formatSize(v.length)})</span></summary>
                    <pre className="tools-pre">{prettyJSON(v)}</pre>
                  </details>
                ) : (
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
            {events.length > 0 ? (
              <details>
                <summary><span className="ctl-value-label">events</span> <span className="ctl-hint">{events.length}{r.events_dropped ? ` (+${r.events_dropped} not kept)` : ''}</span></summary>
                <div className="ctl-values">
                  {events.map((ev, i) => (
                    <div key={i}>
                      <span className="ctl-step-time">+{formatMs(ev.at - r.started_at) || '0ms'}</span>{' '}
                      <span className="tools-badge">{ev.from}</span>{' '}
                      <span className="ctl-value-label">{ev.event}</span>
                      {ev.detail && <span className="tools-value"> {ev.detail}</span>}
                    </div>
                  ))}
                </div>
              </details>
            ) : eventCount > 0 && (
              <div className="ctl-hint">{eventCount} event{eventCount === 1 ? '' : 's'} held on the host — "to file" sends them</div>
            )}
            {res?.log_tail && <pre className="tools-pre">{res.log_tail}</pre>}
            {(r.status !== 'running' || (r.saved ?? []).length > 0) && (
              <div className="ctl-save">
                {r.status !== 'running' && (
                  <button className="ctl-btn" disabled={!connected || save.pending} onClick={() => save.send({ op: 'save', run: r.id })}>
                    save results to files
                  </button>
                )}
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

// ---- History ----

// imageSummary is an image's summary, outcome -> fields, on one line.
function imageSummary(image?: Record<string, number>): string {
  return Object.entries(image ?? {}).map(([k, n]) => `${k} ${n}`).join(' · ')
}

// CallHistory lists the dtt calls clauditable recorded on this node
// (tools.go's "History"), asked for when the sub-tab opens and on reload.
// An entry's whole record is asked for when it's opened.
function CallHistory({ request, replies, connected, node }: ToolsPanelProps) {
  const list = useRequest(request, replies)
  const [open, setOpen] = useState<string | null>(null)
  const entries = list.reply?.ok ? list.reply.history ?? [] : []

  useEffect(() => {
    if (connected && !list.reply && !list.pending) list.send({ op: 'history' })
  }, [connected]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="ctl-v1-view">
      <div className="ctl-row">
        <span className="ctl-hint">
          Every dtt call (run, read, assert, delete) recorded on {node} — by an agent, a shell or this tab — as
          clauditable copies it into the records' dtt-calls, each naming the session it was made in.
        </span>
        <span className="tools-grow" />
        <button className="ctl-btn" disabled={!connected || list.pending} onClick={() => list.send({ op: 'history' })}>reload</button>
      </div>
      {list.pending && <div className="ctl-hint">reading the recorded calls…</div>}
      {list.reply && !list.reply.ok && <div className="ctl-run-error-msg">{list.reply.error}</div>}
      {list.reply?.ok && list.reply.detail && <div className="ctl-hint">{list.reply.detail}</div>}
      {list.reply?.ok && entries.length === 0 && <div className="ctl-empty">no recorded dtt calls</div>}
      {entries.map(e => (
        <HistoryRow key={e.id} entry={e} open={open === e.id} onToggle={() => setOpen(open === e.id ? null : e.id)}
          request={request} replies={replies} connected={connected} />
      ))}
      {!!list.reply?.history_omitted && <div className="ctl-hint">…and {list.reply.history_omitted} older not listed</div>}
    </div>
  )
}

function HistoryRow({ entry: e, open, onToggle, request, replies, connected }: {
  entry: ToolHistoryEntry
  open: boolean
  onToggle: () => void
  request: ToolsRequestFn
  replies: Record<string, ToolsReply>
  connected: boolean
}) {
  const detail = useRequest(request, replies)
  const d = detail.reply?.ok ? detail.reply.call : undefined

  // Opening an entry reads its whole record, once.
  useEffect(() => {
    if (open && connected && !detail.reply && !detail.pending) detail.send({ op: 'history-detail', call: e.id })
  }, [open, connected]) // eslint-disable-line react-hooks/exhaustive-deps

  const what = e.tool || e.document || e.command
  const image = imageSummary(e.summary?.image)
  const mode = e.summary?.mode && e.summary.mode !== e.mode ? `${e.mode} (${e.summary.mode})` : e.mode
  return (
    <div className={`ctl-step ctl-step-${e.ok ? 'success' : 'error'} tools-run`}>
      <span className="ctl-step-icon">{e.ok ? '✓' : '✗'}</span>
      <div className="ctl-step-body">
        <div className="tools-head" onClick={onToggle}>
          {mode && <span className="tools-badge">{mode}</span>}
          <span className="ctl-step-label">{what}</span>
          <span className="ctl-hint">{new Date(e.at).toLocaleString()}</span>
          {e.agent && <span className="tools-badge" title="the agent the call was recorded for">{e.agent}</span>}
          <span className="tools-grow" />
          <span className="ctl-step-time">{formatMs(e.duration_ms)}</span>
        </div>
        {image && <div className="ctl-hint">image: {image}</div>}
        {e.summary?.error && <div className="ctl-step-msg">{e.summary.error}</div>}
        {open && (
          <div className="tools-detail">
            <div className="ctl-values">
              <div><span className="ctl-value-label">call:</span> <span className="tools-value">{e.id}</span></div>
              {e.session && (
                <div>
                  <span className="ctl-value-label">session:</span> <span className="tools-value">{e.session}</span>
                  {e.session_record && <span className="ctl-hint"> ({e.session_record})</span>}
                </div>
              )}
              {e.host && <div><span className="ctl-value-label">host:</span> <span className="tools-value">{e.host}</span></div>}
              <div><span className="ctl-value-label">exit status:</span> <span className="tools-value">{e.exit_code}</span></div>
              {e.image_path && <div><span className="ctl-value-label">image kept at:</span> <span className="tools-value">{e.image_path}</span></div>}
            </div>
            <ReplyStatus r={detail} working="reading the record…" />
            {d && (
              <>
                {d.note && <div className="ctl-hint">{d.note}</div>}
                <div className="ctl-section-title">command</div>
                <pre className="tools-pre">{d.raw_command}</pre>
                {d.stdout && (
                  <>
                    <div className="ctl-section-title">stdout</div>
                    <pre className="tools-pre">{d.stdout}</pre>
                  </>
                )}
                {d.stderr && (
                  <>
                    <div className="ctl-section-title">stderr</div>
                    <pre className="tools-pre">{d.stderr}</pre>
                  </>
                )}
              </>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

// ---- Visual ----

// Each type of tool's default icon: a provider's (a document's statement
// names one; its built-in tool is named after it), the variables
// statement's, and the other built-in tools'. Any other tool has
// DEFAULT_TOOL_ICON.
const PROVIDER_ICONS: Record<string, string> = {
  github: '🐙',
  git: '🌿',
  filesystem: '📁',
  aws: '☁️',
  localrep: '🛰️',
  timestamp: '🕒',
  dtt: '🧩', // dtt.calls: another document called
  delay: '⏳',
  slopspace: '🧫',
  variables: '🔣',
}
// Paths with an icon of their own, over their provider's: the kinds of
// agent work a slopspace runs, a file in the preferred file cache, and a
// repository's files at a ref, a filter of them, and a commit of them
// pushed to a branch or tag.
const PATH_ICONS: Record<string, string> = {
  'slopspace.files': '📄',
  'slopspace.work.executions': '🤖',
  'slopspace.work.submissions': '📨',
  'slopspace.work.completions': '📬',
  'localrep.files': '🗂️',
  'git.trees': '🌲',
  'git.files': '🔎',
  'git.commits': '📌',
}
const TOOL_ICONS: Record<string, string> = {
  declare: '📜',
  composer: '🎬',
  'materialize-tree': '🌳',
}
const DEFAULT_TOOL_ICON = '🔧'

const toolIcon = (tool: string) => TOOL_ICONS[tool] ?? PROVIDER_ICONS[tool] ?? DEFAULT_TOOL_ICON
const stepIcon = (s: { provider: string; path: string }) => PATH_ICONS[s.path] ?? PROVIDER_ICONS[s.provider] ?? DEFAULT_TOOL_ICON

const STEP_STATUSES = ['pending', 'running', 'ok', 'failed'] as const
const STEP_STATUS_WORDS: Record<ToolRunStep['status'], string> = {
  pending: 'not begun', running: 'running', ok: 'succeeded', failed: 'failed',
}

// The visual's layout: a column for each level of the dependency graph
// (a step's level is one more than its deepest dependency's), its steps in
// the order they run; NODE is an icon's box.
const NODE = 48
const COL_W = 150
const ROW_H = 92
const PAD = 16

// runSteps is what the visual draws for run: its steps, or -- a tool that
// reported none -- the tool itself, as one step going as the run goes.
function runSteps(run: ToolRun): { steps: ToolRunStep[]; reported: boolean } {
  const steps = run.progress?.steps ?? []
  if (steps.length > 0) return { steps, reported: true }
  return {
    reported: false,
    steps: [{
      id: run.id, key: run.tool, path: run.tool, provider: run.tool, line: 0, after: [], level: 0,
      status: run.status === 'running' ? 'running' : run.status, error: run.result?.error,
      duration_ms: run.result?.duration_ms, started_at: run.started_at, finished_at: run.finished_at,
    }],
  }
}

// The height of the phase names above the icons, when a run has phases.
const PHASE_LABEL_H = 22

// phaseBands are the phases of steps, in the order they run: the columns
// each one's steps span (a phase begins only after the one before it, so
// they don't overlap) and how it's going, as its steps are.
function phaseBands(steps: ToolRunStep[]): { name: string; from: number; to: number; status: ToolRunStep['status'] }[] {
  const bands: { name: string; from: number; to: number; status: ToolRunStep['status'] }[] = []
  for (const s of steps) {
    if (!s.phase) continue
    const b = bands.find(x => x.name === s.phase)
    if (!b) {
      bands.push({ name: s.phase, from: s.level, to: s.level, status: 'pending' })
      continue
    }
    b.from = Math.min(b.from, s.level)
    b.to = Math.max(b.to, s.level)
  }
  for (const b of bands) {
    const of = steps.filter(s => s.phase === b.name)
    b.status = of.some(s => s.status === 'failed') ? 'failed'
      : of.every(s => s.status === 'ok') ? 'ok'
      : of.some(s => s.status !== 'pending') ? 'running' : 'pending'
  }
  return bands
}

function clip(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + '…' : s
}

// CallVisual is an execution of the call as its steps' icons, laid out in
// the order of their dependency graph: grey until begun, yellow while
// running, then green or red. It's this call's last execution from here
// (its form's), or one picked from the recent executions; it changes as
// tools-state brings the run's progress.
function CallVisual({ state, lastRun }: { state: ToolsStateMsg | null; lastRun?: string }) {
  const runs = state?.runs ?? []
  const [picked, setPicked] = useState('')
  const [sel, setSel] = useState<string | null>(null)
  useEffect(() => setPicked(''), [lastRun])
  const shownId = picked || lastRun || ''
  const run = runs.find(r => r.id === shownId)
  useEffect(() => setSel(null), [shownId])

  const picker = (
    <div className="ctl-row">
      <span className="ctl-hint">execution</span>
      <select className="ctl-select" value={picked} onChange={e => setPicked(e.target.value)}>
        <option value="">{lastRun ? "this call's last, from here" : '— this call has none here yet —'}</option>
        {runs.map(r => (
          <option key={r.id} value={r.id}>
            {toolIcon(r.tool)} {r.tool} · {new Date(r.started_at).toLocaleTimeString()} · {r.status} · {r.origin}
          </option>
        ))}
      </select>
    </div>
  )
  if (!run) {
    return (
      <div className="tools-visual">
        {picker}
        <div className="ctl-empty">
          {shownId ? "that execution isn't among the recent ones any more"
            : 'Execute this call (under text) and its steps show here as they go — or pick one of the recent executions.'}
        </div>
        <VisualLegend />
      </div>
    )
  }

  const { steps, reported } = runSteps(run)
  const cols: ToolRunStep[][] = []
  for (const s of steps) (cols[s.level] ??= []).push(s)
  const bands = phaseBands(steps)
  const top = PAD + (bands.length > 0 ? PHASE_LABEL_H : 0)
  const pos: Record<string, { x: number; y: number }> = {}
  cols.forEach((col, level) => (col ?? []).forEach((s, row) => {
    pos[s.id] = { x: PAD + level * COL_W + (COL_W - NODE) / 2, y: top + row * ROW_H }
  }))
  const width = PAD * 2 + Math.max(1, cols.length) * COL_W
  const height = top + PAD + Math.max(1, ...Array.from(cols, c => c?.length ?? 0)) * ROW_H - (ROW_H - NODE - 30)
  const byId = Object.fromEntries(steps.map(s => [s.id, s]))
  const counts = Object.fromEntries(STEP_STATUSES.map(st => [st, steps.filter(s => s.status === st).length]))
  const chosen = sel ? byId[sel] : undefined

  return (
    <div className="tools-visual">
      {picker}
      <div className="ctl-row">
        <span className="tools-icon">{toolIcon(run.tool)}</span>
        <span className="ctl-seq-name">{run.tool}</span>
        {run.progress?.mode && <span className="tools-badge">{run.progress.mode}</span>}
        <span className={`tools-badge${run.status === 'ok' ? ' tools-badge-ok' : run.status === 'failed' ? ' tools-badge-no' : ''}`}>{run.status}</span>
        <span className="ctl-hint">
          {new Date(run.started_at).toLocaleTimeString()}
          {reported && ` · ${steps.length} step${steps.length === 1 ? '' : 's'}: ${STEP_STATUSES.filter(st => counts[st] > 0).map(st => `${counts[st]} ${st}`).join(', ')}`}
        </span>
      </div>
      {!reported && (
        <div className="ctl-hint">
          {run.status === 'running'
            ? 'No steps reported yet — a declarative tool states its plan once it has checked its document.'
            : "This execution reported no steps (a tool that isn't declarative, its document refused before it began, or a dtt from before progress): it's shown as its tool."}
        </div>
      )}
      {!!run.progress?.steps_omitted && <div className="ctl-hint">…and {run.progress.steps_omitted} more steps not kept</div>}
      <div className="tools-visual-graph">
        <svg width={width} height={height} role="img" aria-label={`${run.tool}'s steps`}>
          {bands.map(b => (
            <g key={b.name} className={`tools-visual-phase tools-visual-phase-${b.status}`}>
              <title>{`phase ${b.name}: ${STEP_STATUS_WORDS[b.status]}`}</title>
              <rect x={PAD + b.from * COL_W + 4} y={PAD - 6} width={(b.to - b.from + 1) * COL_W - 8} height={height - PAD} rx={12} />
              <text x={PAD + b.from * COL_W + 14} y={PAD + 8}>{clip(b.name, 18 * (b.to - b.from + 1))}</text>
            </g>
          ))}
          {steps.flatMap(s => s.after.filter(d => pos[d]).map(d => {
            const a = pos[d], b = pos[s.id]
            const x1 = a.x + NODE, y1 = a.y + NODE / 2, x2 = b.x, y2 = b.y + NODE / 2, mx = (x1 + x2) / 2
            return (
              <path key={`${d}-${s.id}`} className={`tools-visual-edge${byId[d]?.status === 'ok' ? ' tools-visual-edge-done' : ''}`}
                d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`} />
            )
          }))}
          {steps.map(s => {
            const p = pos[s.id]
            const sub = s.status === 'running' ? (s.stage === 'remeasure' ? 'measuring again…' : 'running…')
              : s.status === 'pending' ? (s.line ? `line ${s.line}` : '')
              : `${s.status === 'ok' ? '✓' : '✗'} ${formatMs(s.duration_ms) || ''}${s.stage === 'remeasure' ? ' ↻' : ''}`
            return (
              <g key={s.id} className={`tools-visual-node tools-visual-${s.status}${sel === s.id ? ' tools-visual-selected' : ''}`}
                onClick={() => setSel(sel === s.id ? null : s.id)}>
                <title>{`${s.key}${s.line ? ` (line ${s.line})` : ''} — ${STEP_STATUS_WORDS[s.status]}${s.error ? `\n${s.error}` : ''}`}</title>
                <rect x={p.x} y={p.y} width={NODE} height={NODE} rx={10} />
                <text className="tools-visual-icon" x={p.x + NODE / 2} y={p.y + NODE / 2 + 8} textAnchor="middle">{stepIcon(s)}</text>
                <text className="tools-visual-label" x={p.x + NODE / 2} y={p.y + NODE + 14} textAnchor="middle">{clip(s.alias || s.key, 22)}</text>
                <text className="tools-visual-sub" x={p.x + NODE / 2} y={p.y + NODE + 26} textAnchor="middle">{sub}</text>
              </g>
            )
          })}
        </svg>
      </div>
      <VisualLegend />
      {chosen ? <StepDetail step={chosen} /> : <div className="ctl-hint">pick a step to see what it did</div>}
    </div>
  )
}

function VisualLegend() {
  return (
    <div className="tools-visual-legend">
      {STEP_STATUSES.map(st => (
        <span key={st}><span className={`tools-visual-swatch tools-visual-${st}`} />{STEP_STATUS_WORDS[st]}</span>
      ))}
      <span>
        {Object.entries(PROVIDER_ICONS).map(([p, icon]) => `${icon} ${p}`).join('  ')}
      </span>
    </div>
  )
}

function StepDetail({ step: s }: { step: ToolRunStep }) {
  const summary = imageSummary(s.summary)
  const objects = s.objects ?? []
  return (
    <div className="tools-detail">
      <div className="ctl-row">
        <span className="tools-icon">{stepIcon(s)}</span>
        <span className="ctl-seq-name">{s.key}</span>
        <span className="tools-badge">{s.provider}</span>
        {s.asserts && <span className="tools-badge" title="the statement asserts a state">asserts</span>}
        <span className="tools-badge">{STEP_STATUS_WORDS[s.status]}{s.stage === 'remeasure' ? ' (measured again)' : ''}</span>
      </div>
      <div className="ctl-values">
        {s.path !== s.key && <div><span className="ctl-value-label">path:</span> <span className="tools-value">{s.path}</span></div>}
        {s.alias && <div><span className="ctl-value-label">alias:</span> <span className="tools-value">{s.alias}</span></div>}
        {s.line > 0 && <div><span className="ctl-value-label">line:</span> <span className="tools-value">{s.line}</span></div>}
        {s.phase && <div><span className="ctl-value-label">phase:</span> <span className="tools-value">{s.phase}</span></div>}
        {s.after.length > 0 && <div><span className="ctl-value-label">after:</span> <span className="tools-value">{s.after.join(', ')}</span></div>}
        {s.duration_ms !== undefined && s.status !== 'running' && s.status !== 'pending' && (
          <div><span className="ctl-value-label">took:</span> <span className="tools-value">{formatMs(s.duration_ms) || '0ms'}</span></div>
        )}
        {summary && <div><span className="ctl-value-label">fields:</span> <span className="tools-value">{summary}</span></div>}
        {(objects.length > 0 || !!s.objects_omitted) && (
          <div>
            <span className="ctl-value-label">objects:</span>{' '}
            <span className="tools-value">{objects.join(', ')}{s.objects_omitted ? `${objects.length ? ', ' : ''}+${s.objects_omitted} more` : ''}</span>
          </div>
        )}
      </div>
      {s.error && <div className="ctl-run-error-msg">{s.error}</div>}
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
  runtime: python
  script: main.py
  timeout: 60s
`

function Author({ state, request, replies, connected, hostApi }: ToolsPanelProps) {
  const [source, setSource] = useState(SAMPLE_TOOL)
  const [path, setPath] = useState('')
  const [force, setForce] = useState(false)
  const [picking, setPicking] = useState(false)
  const [name, setName] = useState('')
  const [runtime, setRuntime] = useState('python')
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
          Creates a tool that runs as it stands — tool.yaml plus a Python script (dependencies declared inline, for uv)
          or a Go main package (standard library only; dtt builds it) — in {state?.user_dir || 'the tools directory'}, for you to edit there.
        </div>
        <div className="ctl-row">
          <input className="ctl-input ctl-input-name" value={name} placeholder="my-tool" onChange={e => setName(e.target.value)} />
          <select className="ctl-select" value={runtime} onChange={e => setRuntime(e.target.value)}>
            <option value="python">python</option>
            <option value="go">go</option>
          </select>
          <button className="sys-btn sys-btn-launch" disabled={!connected || create.pending || !name.trim()} onClick={() => create.send({ op: 'create', name: name.trim(), runtime })}>create</button>
        </div>
        <ReplyStatus r={create} working="creating…" />
      </div>
    </div>
  )
}

// ---- Examples ----

function Examples({ state, request, replies, connected, node, instances, onImport, onOpen }: ToolsPanelProps & {
  instances: ToolInstance[]
  onImport: (ex: ToolExample) => void
  onOpen: (key: string) => void
}) {
  const list = useRequest(request, replies)
  const [open, setOpen] = useState<string | null>(null)
  const examples = list.reply?.ok ? list.reply.examples ?? [] : []

  // Ask dtt for its examples when the sub-tab opens (and again on reload).
  useEffect(() => {
    if (connected && !list.reply && !list.pending) list.send({ op: 'examples' })
  }, [connected]) // eslint-disable-line react-hooks/exhaustive-deps

  const tools = state?.tools ?? []
  return (
    <div className="ctl-v1-view">
      <div className="ctl-row">
        <span className="ctl-hint">
          The examples built into {node}'s dtt — tool call definitions. Import one to get a runnable instance of its tool
          in calls: a DTT document comes in with mode read and its variables, if it declares any, in the variables input
          with their defaults (name=value lines, to edit there) — switch mode to assert there to make the changes — and a
          tool call (marked call) with its inputs filled in.
        </span>
        <span className="tools-grow" />
        <button className="ctl-btn" disabled={!connected || list.pending} onClick={() => list.send({ op: 'examples' })}>reload</button>
      </div>
      {list.pending && <div className="ctl-hint">asking dtt for its examples…</div>}
      {list.reply && !list.reply.ok && <div className="ctl-run-error-msg">{list.reply.error}</div>}
      {list.reply?.ok && examples.length === 0 && <div className="ctl-empty">dtt has no examples</div>}
      {examples.map(ex => {
        const tool = tools.find(t => t.name === ex.tool)
        const imported = instances.filter(i => i.example === ex.name)
        const why = ex.error ? `it doesn't parse: ${ex.error}`
          : !ex.tool ? `it uses ${ex.providers.join(', ')}, which no one built-in tool runs (dtt read/assert runs it)`
          : !tool ? `its tool, ${ex.tool}, isn't in ${node}'s catalog`
          : ''
        return (
          <div key={ex.name} className="ctl-seq">
            <div className="ctl-seq-head tools-head" onClick={() => setOpen(open === ex.name ? null : ex.name)}>
              <span className="ctl-seq-name">{ex.name}</span>
              {ex.tool && <span className="tools-badge" title="the tool that runs it">{ex.tool}</span>}
              {ex.kind === 'call' && <span className="tools-badge" title="a tool call: the tool's inputs, not a DTT document">call</span>}
              {tool && <AllowedBadge tool={tool} />}
              <span className="tools-grow" />
              <button
                className="sys-btn sys-btn-launch"
                disabled={!!why}
                title={why ? `can't import: ${why}` : `make a new runnable instance of ${ex.tool} from this example`}
                onClick={e => { e.stopPropagation(); onImport(ex) }}
              >
                import
              </button>
            </div>
            <div className="ctl-seq-desc">{ex.title}</div>
            {imported.length > 0 && (
              <div className="ctl-row">
                <span className="ctl-hint">imported as</span>
                {imported.map(i => (
                  <button key={i.id} className="ctl-btn" title="open it in calls" onClick={() => onOpen(instanceKey(i.id))}>{i.title}</button>
                ))}
              </div>
            )}
            {open === ex.name && (
              <div className="tools-detail">
                {ex.description.split('\n\n').map((p, i) => <div key={i} className="tools-line">{p}</div>)}
                {why && <div className="ctl-run-error-msg">can't import: {why}</div>}
                {ex.commands.length > 0 && (
                  <>
                    <div className="ctl-section-title">from a shell (in dtt's source directory)</div>
                    <pre className="tools-pre">{ex.commands.join('\n')}</pre>
                  </>
                )}
                <div className="ctl-section-title">{ex.path}</div>
                <pre className="tools-pre">{ex.source}</pre>
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
