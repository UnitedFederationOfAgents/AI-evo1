import { useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { ProposalsView } from './DemoProposals'
import { ProtosurfMapView, type MapHighlight } from './Protosurf'
import type { DemoArtifact, DemoInfo, DemoOrigin, DemoPhase, DemoRecord, DemoRender, DemoReview, DemoVariable, SuiteEntry, SuiteInfo, SuiteRecord, ToolsReply, ToolsRequest, ToolsRequestFn } from './Tools'

// The demo tab (PrivateRepoOps Step 4, Revisions E and F). It is no sub-app
// of its own -- a demo is put together from the standard ones (DEMO_APPS),
// so the tab opens on which of them are up here. Below that, a demo is a
// DTT composition (local-representative's demos.go): a document whose
// phases each call one of dtt's examples -- a general lead-in, a lead-in
// for the subject area, the subject, the area's tear-down, the general
// tear-down -- and then compose the renders into one video. Running one is
// a single dtt tool call (declare, in assert mode), and each run is kept
// on the host as a record: the document as it ran, the image it returned
// and how each phase went. The demos and records sub-tabs list both,
// through the tools tab's own requests. Kept identical in
// local-representative and agent-coordinator.
//
// Revision G: a record keeps its videos itself (videos/ in its directory,
// served at <hostApi>/api/demo-records/<id>/videos/<file>), and a demo's
// node variables are a pick-list of the nodes connected to
// agent-coordinator, empty being this node -- the local system under test.
//
// PrivateRepoOps Step 5: the suites sub-tab (far left) runs a suite -- demos
// one after another (local-representative's suites.go) -- asking for all
// their variables up front, once each; a free-text variable starts from the
// value it was last run with (kept on the host's filesystem), a node one
// stays a pick-list. A record marks its primary video (its subject, without
// lead-in or tear-down) and lists its artifacts. The review sub-tab (far
// right) has the worker's agent review a record, read-only, in place
// (demoreview.go), and shows what it said. Revision A: a record's analysis/
// -- frames from its videos, tiled into contact sheets -- and its MAP.md,
// which the reviewing agent reads first (demoanalysis.go).
//
// Revision B: the tab opens on the protosurf area -- the prototype surface
// -- in place of the sub-apps, which become one of its children beside the
// map: the project's content as a treemap, coloured by language, system
// role or density (Protosurf.tsx), where demo and test coverage are to be
// drawn next.
//
// Revision C: selecting a demo or a suite -- its row on the demos or
// suites sub-tab, a demo of a suite, a record or a suite run -- picks its
// declared coverage out on the map (local-representative's
// democoverage.go): focal in one colour, supporting in another. The row
// picked shows ◎; the map's clear, or ◎ again, lets it go.
//
// Revision E: the proposals sub-tab (far left, DemoProposals.tsx); since
// Revision H the worker's agent builds the whole demo from a name, title
// and description, and each proposal is a tile whose details are its
// description and files. A built proposal picks its demo's coverage out on
// the map as a demo does.
//
// Revision N: the demos and suites sub-tabs each list two groups --
// configured (on the host's own filesystem, in no repo: duplicates, ones
// put there by hand, and every demo a proposal builds) and embedded
// (in-tree in AI-evo1-spiral, built into local-representative).

// DEMO_APPS are the parts a demo draws on, each with the role it plays.
// `own` ones are local-representative's own tabs rather than sub-apps, so
// they're up whenever local-representative is.
export const DEMO_APPS: { name: string; role: string; own?: boolean }[] = [
  { name: 'control', role: 'runs the demo’s steps across the sub-apps', own: true },
  { name: 'robot', role: 'drives and records the screens' },
  { name: 'convo', role: 'narrates, with Polly clips' },
  { name: 'compose', role: 'cuts recordings and clips into one video' },
  { name: 'condocv2', role: 'condoc timelines (Visual, Record.html)' },
  { name: 'worker', role: 'agent work submissions' },
  { name: 'tools', role: 'dtt tool calls -- each demo run is one', own: true },
  { name: 'files', role: 'where recordings and renders land', own: true },
]

// DemoAppStatus is one DEMO_APPS entry's state where the tab is shown: kind
// picks its dot's colour, label says it in words ("healthy", "not launched",
// "on 2 of 3 hosts").
export type DemoAppStatus = { kind: 'up' | 'down' | 'unknown'; label: string }

// The protosurf area's children: the map of the project's content, and the
// sub-apps a demo draws on.
const PROTOSURF_AREAS = ['map', 'sub-apps'] as const

const SUBTABS = ['proposals', 'suites', 'demos', 'records', 'review'] as const

const SUBTAB_HEADER: Record<typeof SUBTABS[number], string> = {
  proposals: 'demo proposals',
  suites: 'demo suites',
  demos: 'demo compositions',
  records: 'demo records',
  review: 'review a record',
}

// ORIGIN_GROUPS are the demos and suites sub-tabs' groups, in order: what
// each holds, and what to say when it holds nothing.
const ORIGIN_GROUPS: { origin: DemoOrigin; label: string; hint: string; none: string }[] = [
  {
    origin: 'configured', label: 'configured',
    hint: 'on this host’s filesystem, in its demo library -- in no repo',
    none: 'none configured on this host -- duplicate an embedded one, or build one from a proposal',
  },
  {
    origin: 'embedded', label: 'embedded',
    hint: 'in-tree in AI-evo1-spiral, built into local-representative',
    none: 'none embedded',
  },
]

// OriginGroups lists items in ORIGIN_GROUPS' groups, each under a heading
// with its count, card drawing each item.
function OriginGroups<T extends { name: string; origin: DemoOrigin }>({ items, card }: { items: T[]; card: (x: T) => ReactNode }) {
  return (
    <>
      {ORIGIN_GROUPS.map(g => {
        const of = items.filter(x => x.origin === g.origin)
        return (
          <div key={g.origin} className="demo-origin-group">
            <div className="demo-heading" title={g.hint}>
              <span className={`demo-origin demo-origin-${g.origin}`}>{g.label}</span>
              <span className="demo-count">{of.length} · {g.hint}</span>
            </div>
            {of.length === 0 ? <div className="sys-providers-none">{g.none}</div> : <div className="demo-list">{of.map(card)}</div>}
          </div>
        )
      })}
    </>
  )
}

// How often the records are re-read while one is running.
const RUNNING_POLL_MS = 4000

const ROLE_LABEL: Record<DemoPhase['role'], string> = {
  'lead-in': 'lead-in',
  subject: 'subject',
  'tear-down': 'tear-down',
  compose: 'compose',
}

// useDemoRequest tracks one request's reply, as the tools tab's useRequest
// does.
function useDemoRequest(request: ToolsRequestFn | undefined, replies: Record<string, ToolsReply>) {
  const [req, setReq] = useState<string | null>(null)
  const reply = req ? replies[req] : undefined
  return {
    reply,
    pending: req !== null && !reply,
    send: (r: ToolsRequest) => { if (request) setReq(request(r)) },
    clear: () => setReq(null),
  }
}

function when(ms?: number): string {
  return ms ? new Date(ms).toLocaleString() : ''
}

function took(r: DemoRecord): string {
  if (!r.finished_at) return ''
  return `${((r.finished_at - r.started_at) / 1000).toFixed(0)}s`
}

// isNodeVariable: a demo variable naming the node to give the demo on
// ("node", or "<something>_node"), offered as a pick-list.
function isNodeVariable(v: DemoVariable): boolean {
  return v.name === 'node' || v.name.endsWith('_node')
}

// varValue is the value variable v shows: what was entered here, else --
// for a free-text one -- the value it was last run with on this host.
function varValue(entered: Record<string, string>, v: DemoVariable, last: Record<string, string>): string {
  return entered[v.name] ?? (isNodeVariable(v) ? '' : last[v.name] ?? '')
}

// runValues are the values to run with: every declared variable's, empty
// ones included, so the host forgets a free-text one emptied here.
function runValues(vars: DemoVariable[], entered: Record<string, string>, last: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const v of vars) out[v.name] = varValue(entered, v, last)
  return out
}

// VarInputs edits a demo's or a suite's variables: a node one is a
// pick-list of the nodes connected to agent-coordinator, the rest free
// text. forDemos says which of a suite's demos each goes to.
function VarInputs({ vars, entered, last, onChange, nodes, thisNode, scope, forDemos }: {
  vars: DemoVariable[]
  entered: Record<string, string>
  last: Record<string, string>
  onChange: (name: string, value: string) => void
  nodes: string[]
  thisNode: string
  scope: string
  forDemos?: (name: string) => string[]
}) {
  if (vars.length === 0) return null
  return (
    <div className="demo-vars">
      {vars.map(v => {
        const value = varValue(entered, v, last)
        const demos = forDemos?.(v.name)
        const title = [v.help, demos && `for ${demos.join(', ')}`, !isNodeVariable(v) && last[v.name] !== undefined && `last run with: ${last[v.name]}`].filter(Boolean).join('\n')
        return (
          <label key={v.name} className="demo-var" title={title}>
            <span className="demo-var-name">{v.name}{demos && <span className="demo-var-for"> ×{demos.length}</span>}</span>
            {isNodeVariable(v) ? (
              // The nodes connected to agent-coordinator; empty is
              // this node, the local system under test.
              <select className="ctl-select" value={value} onChange={e => onChange(v.name, e.target.value)}>
                <option value="">{v.default ? `default: ${v.default}` : `this node${thisNode ? ` (${thisNode})` : ''} -- local system under test`}</option>
                {value !== '' && !nodes.includes(value) && (
                  <option value={value} disabled>{value} (not connected)</option>
                )}
                {nodes.map(n => (
                  <option key={n} value={n}>{n}{n === thisNode ? ' (this node)' : ''}</option>
                ))}
              </select>
            ) : (
              <input
                value={value}
                placeholder={v.default ? `default: ${v.default}` : v.help || '(empty)'}
                onChange={e => onChange(v.name, e.target.value)}
              />
            )}
          </label>
        )
      })}
      {vars.some(isNodeVariable) && nodes.length === 0 && (
        <div className="ctl-hint">{scope} isn’t connected to agent-coordinator, so it can only give the demo on itself</div>
      )}
    </div>
  )
}

// MapMark is the ◎ on the row picked out on the map; clicking it lets the
// row go.
function MapMark({ on, onClear }: { on: boolean; onClear: () => void }) {
  if (!on) return null
  return (
    <span className="demo-map-mark" title="picked out on the protosurf map -- click to let it go" onClick={e => { e.stopPropagation(); onClear() }}>◎ on the map</span>
  )
}

// ENTRY_CLASS shows a suite entry's status with the phase chips' colours.
const ENTRY_CLASS: Record<SuiteEntry['status'], string> = {
  pending: 'not-run', running: 'running', ok: 'done', failed: 'failed', skipped: 'not-run', interrupted: 'failed',
}

// SuiteRun is one run of a suite: a chip per demo, coloured by how it
// went, each opening the demo's own record.
function SuiteRun({ sr, open, onToggle, demoTitle, onRecord }: {
  sr: SuiteRecord
  open: boolean
  onToggle: () => void
  demoTitle: (name: string) => string
  onRecord: (id: string) => void
}) {
  const ok = sr.entries.filter(e => e.status === 'ok').length
  const finished = sr.finished_at ? ` · ${((sr.finished_at - sr.started_at) / 1000).toFixed(0)}s` : ''
  return (
    <div className={`demo-card demo-record-${sr.status}`}>
      <div className="demo-card-head" onClick={onToggle}>
        <span className="demo-app-dot" />
        <span className="demo-app-name">{sr.title || sr.suite}</span>
        <span className="demo-count">{sr.status} · {ok} of {sr.entries.length} ok · {when(sr.started_at)}{finished}</span>
      </div>
      <div className="demo-phases">
        {sr.entries.map((e, i) => (
          <span
            key={e.demo + i}
            className={`demo-phase demo-phase-subject demo-phase-${ENTRY_CLASS[e.status] ?? 'not-run'}${e.record ? ' demo-entry-link' : ''}`}
            title={[demoTitle(e.demo), e.status, e.record && `record ${e.record} -- click to open it`, e.error].filter(Boolean).join('\n')}
            onClick={e.record ? () => onRecord(e.record!) : undefined}
          >
            {i + 1}. {e.demo}
          </span>
        ))}
      </div>
      {open && (
        <div className="demo-card-body">
          <div className="demo-count">kept at {sr.dir} · by {sr.origin} on {sr.node || 'this node'} · {sr.on_failure === 'stop' ? 'stops at a failure' : 'carries on past a failure'}</div>
          {Object.keys(sr.variables ?? {}).length > 0 && (
            <div className="demo-count">variables: {Object.entries(sr.variables).map(([k, v]) => `${k}=${v}`).join(', ')}</div>
          )}
          {sr.entries.filter(e => e.error).map((e, i) => (
            <div key={i} className="ctl-run-error-msg">{e.demo}: {e.error}</div>
          ))}
        </div>
      )}
    </div>
  )
}

// AnalysisLinks shows a record's analysis/: its map, how many frames were
// pulled from its videos, and the contact sheets as thumbnails.
function AnalysisLinks({ r, url }: { r: DemoRecord; url: (r: DemoRecord, path: string) => string }) {
  const a = r.analysis
  if (!a) return null
  const counts = [
    `${a.step_frames} at step boundaries`,
    `${a.scene_frames} at scene changes`,
    a.failure_frames ? `${a.failure_frames} more since it failed` : '',
  ].filter(Boolean).join(', ')
  return (
    <>
      <div className="demo-renders">
        <span className="demo-count">analysis/</span>
        <a href={url(r, 'MAP.md')} target="_blank" rel="noreferrer" title="the record's map: every file, lightest first, and each frame's time and step -- what a reviewing agent reads first">MAP.md</a>
        <span className="demo-count" title={(a.notes ?? []).join('\n') || undefined}>
          frames: {counts}{a.notes?.length ? ` · ${a.notes.length} note(s)` : ''}
        </span>
      </div>
      {(a.contact_sheets ?? []).length > 0 && (
        <div className="demo-shots">
          {(a.contact_sheets ?? []).map(p => (
            <a key={p} href={url(r, p)} target="_blank" rel="noreferrer" title={p}><img src={url(r, p)} alt={p} /></a>
          ))}
        </div>
      )}
    </>
  )
}

// ArtifactLinks lists what a record keeps in artifacts/: its screenshots
// as thumbnails, the rest as links.
function ArtifactLinks({ r, url }: { r: DemoRecord; url: (r: DemoRecord, a: DemoArtifact) => string | undefined }) {
  const all = r.artifacts ?? []
  if (all.length === 0) return null
  const shots = all.filter(a => a.kind === 'screenshot')
  const files = all.filter(a => a.kind !== 'screenshot')
  return (
    <>
      <div className="demo-renders">
        <span className="demo-count">artifacts/</span>
        {files.map(a => (
          <a key={a.kind + a.file} href={url(r, a)} target="_blank" rel="noreferrer" title={`${a.kind}${a.size ? `, ${sizeOf(a.size)}` : ''}`}>{a.file}</a>
        ))}
      </div>
      {shots.length > 0 && (
        <div className="demo-shots">
          {shots.map((a, i) => {
            const src = url(r, a)
            const title = [a.phase && `phase ${a.phase}`, a.call && `inside call ${a.call}`, a.file ?? `not kept: ${a.keep_error ?? ''}`].filter(Boolean).join('\n')
            return src
              ? <a key={a.file ?? i} href={src} target="_blank" rel="noreferrer" title={title}><img src={src} alt={a.file} /></a>
              : <span key={a.file_id ?? i} className="demo-render-unkept" title={title}>⚠ screenshot from {a.phase || 'the run'}</span>
          })}
        </div>
      )}
    </>
  )
}

function sizeOf(bytes?: number): string {
  if (!bytes) return ''
  return bytes >= 1 << 20 ? `${(bytes / (1 << 20)).toFixed(1)} MB` : `${Math.max(1, Math.round(bytes / 1024))} KB`
}

function PhaseChain({ phases }: { phases: { name: string; role: DemoPhase['role']; status?: string; calls?: string[]; error?: string }[] }) {
  if (phases.length === 0) return <span className="demo-count">no phases</span>
  return (
    <div className="demo-phases">
      {phases.map((p, i) => (
        <span
          key={p.name + i}
          className={`demo-phase demo-phase-${p.role}${p.status ? ` demo-phase-${p.status}` : ''}`}
          title={[`${ROLE_LABEL[p.role]}: ${p.name}`, p.status, ...(p.calls ?? []).map(c => `calls ${c}`), p.error].filter(Boolean).join('\n')}
        >
          {p.name}
        </span>
      ))}
    </div>
  )
}

export function DemoPanel({ scope, appStatus, onOpenApp, request, replies = {}, connected = true, fileUrl, hostApi = '', nodes = [], thisNode = '' }: {
  scope: string
  appStatus: (app: string, own: boolean) => DemoAppStatus
  onOpenApp?: (app: string) => void
  // request and replies are the host's tools requests; without them (AC's
  // global view) the demos can't be listed or run here.
  request?: ToolsRequestFn
  replies?: Record<string, ToolsReply>
  connected?: boolean
  fileUrl?: (id: string) => string
  // hostApi prefixes the host's own URLs ("" on local-representative,
  // "/host/<id>" through agent-coordinator), for a record's kept videos.
  hostApi?: string
  // nodes are those connected to agent-coordinator (the control state's),
  // thisNode the host's own name among them: a node variable's pick-list.
  nodes?: string[]
  thisNode?: string
}) {
  const [sub, setSub] = useState<typeof SUBTABS[number]>('demos')
  // The protosurf area opens on the map where there's a host to map, else
  // on the sub-apps.
  const [surf, setSurf] = useState<typeof PROTOSURF_AREAS[number]>(request ? 'map' : 'sub-apps')
  const statuses = DEMO_APPS.map(a => ({ ...a, st: appStatus(a.name, !!a.own) }))
  const up = statuses.filter(a => a.st.kind === 'up').length

  const list = useDemoRequest(request, replies)
  const action = useDemoRequest(request, replies)
  const detail = useDemoRequest(request, replies)
  // The last good listing stays shown while the next is asked for.
  const [listing, setListing] = useState<ToolsReply | null>(null)
  const [open, setOpen] = useState<string | null>(null)
  const [vars, setVars] = useState<Record<string, Record<string, string>>>({})
  const [showSource, setShowSource] = useState(false)
  const [dupName, setDupName] = useState('')
  const [openRecord, setOpenRecord] = useState<string | null>(null)
  const [openSuite, setOpenSuite] = useState<string | null>(null)
  const [suiteVars, setSuiteVars] = useState<Record<string, Record<string, string>>>({})
  const [openSuiteRecord, setOpenSuiteRecord] = useState<string | null>(null)
  // The review sub-tab: the record picked, and what to ask about it.
  const reviews = useDemoRequest(request, replies)
  const [reviewRecord, setReviewRecord] = useState('')
  const [question, setQuestion] = useState('')
  const [reviewAgent, setReviewAgent] = useState('')
  // The demo, suite or proposal picked out on the protosurf map.
  const [mapPick, setMapPick] = useState<{ kind: MapHighlight['kind']; name: string } | null>(null)

  const refresh = useRef<() => void>(() => {})
  refresh.current = () => list.send({ op: 'demos' })
  // request is a fresh function each render, so only whether there is one
  // counts; a host's tab is keyed by host, so another host remounts it.
  const canRequest = !!request
  useEffect(() => { if (canRequest && connected) refresh.current() }, [canRequest, connected])
  useEffect(() => { if (list.reply?.ok) setListing(list.reply) }, [list.reply])
  // A run or a duplicate changes the lists.
  useEffect(() => { if (action.reply?.ok) refresh.current() }, [action.reply])
  const records = listing?.demo_records ?? []
  const suiteRecords = listing?.suite_records ?? []
  // One demo runs at a time on a host, a suite's included.
  const running = records.some(r => r.status === 'running') || suiteRecords.some(r => r.status === 'running')
  useEffect(() => {
    if (!running) return
    const t = setInterval(() => refresh.current(), RUNNING_POLL_MS)
    return () => clearInterval(t)
  }, [running])

  const demos = listing?.demos ?? []
  const suites = listing?.suites ?? []
  const lastValues = listing?.demo_last_values ?? {}
  const proposals = listing?.demo_proposals ?? []
  const openDemo = demos.find(d => d.name === open)
  const varsOf = (d: DemoInfo) => vars[d.name] ?? {}
  const setVar = (d: DemoInfo, k: string, v: string) => setVars(prev => ({ ...prev, [d.name]: { ...(prev[d.name] ?? {}), [k]: v } }))
  const run = (d: DemoInfo, mode: 'assert' | 'read') => {
    action.send({ op: 'demo-run', demo: d.name, mode, variables: runValues(d.variables, varsOf(d), lastValues) })
    setSub('records')
  }
  const suiteVarsOf = (s: SuiteInfo) => suiteVars[s.name] ?? {}
  const setSuiteVar = (s: SuiteInfo, k: string, v: string) => setSuiteVars(prev => ({ ...prev, [s.name]: { ...(prev[s.name] ?? {}), [k]: v } }))
  const runSuite = (s: SuiteInfo) => action.send({ op: 'suite-run', suite: s.name, variables: runValues(s.variables, suiteVarsOf(s), lastValues) })
  const demoTitle = (name: string) => demos.find(d => d.name === name)?.title ?? name
  // pickOnMap picks a demo, suite or proposal out on the map, and shows the
  // map.
  const pickOnMap = (kind: MapHighlight['kind'], name: string) => {
    setMapPick({ kind, name })
    if (request) setSurf('map')
  }
  const clearPick = () => setMapPick(null)
  const highlight: MapHighlight | null = (() => {
    if (!mapPick) return null
    const x = mapPick.kind === 'demo' ? demos.find(d => d.name === mapPick.name)
      : mapPick.kind === 'suite' ? suites.find(s => s.name === mapPick.name)
      : proposals.find(p => p.name === mapPick.name)
    return x?.coverage ? { kind: mapPick.kind, name: x.name, title: x.title, coverage: x.coverage } : null
  })()
  // A row is marked picked only when there's coverage to show for it.
  const isPicked = (kind: MapHighlight['kind'], name: string) => highlight?.kind === kind && highlight.name === name
  // goToRecord opens record id on the records sub-tab.
  const goToRecord = (id: string) => {
    setSub('records')
    setOpenRecord(id)
    detail.send({ op: 'demo-record', record: id })
  }
  const artifactUrl = (r: DemoRecord, a: DemoArtifact) => a.file
    ? `${hostApi}/api/demo-records/${encodeURIComponent(r.id)}/artifacts/${a.file.split('/').map(encodeURIComponent).join('/')}`
    : undefined
  const recordFileUrl = (r: DemoRecord, path: string) =>
    `${hostApi}/api/demo-records/${encodeURIComponent(r.id)}/${path.split('/').map(encodeURIComponent).join('/')}`

  // The review sub-tab's record: the one picked, else the newest finished
  // assert.
  const reviewable = records.filter(r => r.status !== 'running')
  const reviewId = reviewRecord || reviewable.find(r => r.mode === 'assert')?.id || reviewable[0]?.id || ''
  const reviewRec = records.find(r => r.id === reviewId)
  // The last good list stays shown while the next is asked for.
  const [shownReviews, setShownReviews] = useState<{ id: string; list: DemoReview[] } | null>(null)
  useEffect(() => {
    const r = reviews.reply
    if (r?.ok && r.demo_record) setShownReviews({ id: r.demo_record.id, list: r.demo_reviews ?? [] })
  }, [reviews.reply])
  const reviewList = shownReviews?.id === reviewId ? shownReviews.list : []
  const reviewWaiting = reviewList.some(v => v.status !== 'completed' && v.status !== 'failed')
  const readReviews = useRef<() => void>(() => {})
  readReviews.current = () => { if (reviewId) reviews.send({ op: 'demo-reviews', record: reviewId }) }
  useEffect(() => { if (sub === 'review' && connected) readReviews.current() }, [sub, reviewId, connected])
  useEffect(() => {
    if (sub !== 'review' || !reviewWaiting) return
    const t = setInterval(() => readReviews.current(), RUNNING_POLL_MS)
    return () => clearInterval(t)
  }, [sub, reviewWaiting])
  const askReview = () => {
    if (!reviewId) return
    reviews.send({ op: 'demo-review', record: reviewId, question: question.trim() || undefined, agent: reviewAgent.trim() || undefined })
  }
  const reviewThis = (r: DemoRecord) => {
    setReviewRecord(r.id)
    setSub('review')
  }
  const undo = (r: DemoRecord) => {
    if (!window.confirm(`Delete what ${r.id} made (its sequences and compositions)? Its runs and renders are kept.`)) return
    action.send({ op: 'demo-run', mode: 'delete', record: r.id })
  }
  const showRecord = (r: DemoRecord) => {
    if (openRecord === r.id) { setOpenRecord(null); return }
    setOpenRecord(r.id)
    if (demos.some(d => d.name === r.demo)) pickOnMap('demo', r.demo)
    detail.send({ op: 'demo-record', record: r.id })
  }
  const keepVideos = (r: DemoRecord) => action.send({ op: 'demo-keep-videos', record: r.id })
  const videoUrl = (r: DemoRecord, v: DemoRender) => v.kept
    ? `${hostApi}/api/demo-records/${encodeURIComponent(r.id)}/videos/${encodeURIComponent(v.kept)}`
    : fileUrl?.(v.file_id)
  const shownDetail = detail.reply?.demo_record?.id === openRecord ? detail.reply : undefined
  // openDemoRow shows demo name open on the demos sub-tab -- one started
  // from a proposal.
  const openDemoRow = (name: string) => {
    setSub('demos')
    setOpen(name)
    setShowSource(false)
    pickOnMap('demo', name)
  }

  const busyNote = action.pending ? 'working…' : action.reply && !action.reply.ok ? action.reply.error : action.reply?.detail

  // The suites sub-tab: each suite, its demos and its variables, asked for
  // once; below, its runs, each demo linking its record.
  const suitesView = () => (
    <>
      <OriginGroups
        items={suites}
        card={s => (
            <div key={s.name} className={`demo-card${openSuite === s.name ? ' demo-card-open' : ''}`}>
              <div className="demo-card-head" onClick={() => { setOpenSuite(openSuite === s.name ? null : s.name); pickOnMap('suite', s.name) }}>
                <span className="demo-app-name">{s.title}</span>
                <span className="demo-count">{s.name} · {s.demos.length} demo(s) · {s.on_failure === 'stop' ? 'stops at a failure' : 'carries on past a failure'}</span>
                <MapMark on={isPicked('suite', s.name)} onClear={clearPick} />
                <span className={`demo-origin demo-origin-${s.origin}`}>{s.origin}</span>
              </div>
              <div className="demo-phases">
                {s.demos.map((d, i) => (
                  <span
                    key={d + i}
                    className={`demo-phase demo-phase-subject demo-entry-link${isPicked('demo', d) ? ' demo-phase-mapped' : ''}`}
                    title={`${d} -- click to pick this demo alone out on the map`}
                    onClick={() => pickOnMap('demo', d)}
                  >
                    {i + 1}. {demoTitle(d)}
                  </span>
                ))}
              </div>
              {s.error && <div className="ctl-run-error-msg">{s.error}</div>}
              {openSuite === s.name && (
                <div className="demo-card-body">
                  {s.description && <div className="demo-desc">{s.description}</div>}
                  {s.path && <div className="demo-count">at {s.path}</div>}
                  <VarInputs
                    vars={s.variables} entered={suiteVarsOf(s)} last={lastValues} onChange={(k, v) => setSuiteVar(s, k, v)}
                    nodes={nodes} thisNode={thisNode} scope={scope}
                    forDemos={name => s.variables.find(v => v.name === name)?.demos ?? []}
                  />
                  <div className="demo-actions">
                    <button className="sys-btn" disabled={!connected || !!s.error || action.pending || running} title={running ? 'a demo is running -- one runs at a time' : 'run each demo in turn, each with its own record'} onClick={() => runSuite(s)}>▶ run suite</button>
                    {s.source && <button className="sys-btn sys-btn-neutral" onClick={() => setShowSource(!showSource)}>{showSource ? 'hide file' : 'show file'}</button>}
                  </div>
                  {showSource && s.source && <pre className="demo-source">{s.source}</pre>}
                </div>
              )}
            </div>
        )}
      />
      <div className="demo-heading">
        <span className="demo-header">suite runs</span>
      </div>
      {suiteRecords.length === 0 ? (
        <div className="sys-providers-none">no suite runs yet</div>
      ) : (
        <div className="demo-list">
          {suiteRecords.map(sr => (
            <SuiteRun
              key={sr.id} sr={sr} open={openSuiteRecord === sr.id} demoTitle={demoTitle} onRecord={goToRecord}
              onToggle={() => {
                setOpenSuiteRecord(openSuiteRecord === sr.id ? null : sr.id)
                if (suites.some(s => s.name === sr.suite)) pickOnMap('suite', sr.suite)
              }}
            />
          ))}
        </div>
      )}
    </>
  )

  // The review sub-tab: pick a record, ask the worker's agent to review it
  // read-only, and read what it said.
  const reviewView = () => (
    <>
      <div className="demo-actions">
        <select className="ctl-select" value={reviewId} onChange={e => setReviewRecord(e.target.value)}>
          {reviewable.length === 0 && <option value="">no finished records</option>}
          {reviewable.map(r => (
            <option key={r.id} value={r.id}>{r.id} -- {r.mode} · {r.status}{r.suite ? ` · suite ${r.suite}` : ''}</option>
          ))}
        </select>
        <button className="sys-btn sys-btn-neutral" disabled={!reviewId} onClick={() => goToRecord(reviewId)}>open record</button>
      </div>
      {reviewRec && (
        <div className="demo-card">
          <div className="demo-card-head">
            <span className="demo-app-name">{reviewRec.title || reviewRec.demo}</span>
            <span className="demo-count">{reviewRec.status} · {when(reviewRec.started_at)} · kept at {reviewRec.dir}</span>
          </div>
          <PhaseChain phases={reviewRec.phases ?? []} />
          {(() => {
            const p = (reviewRec.renders ?? []).find(v => v.primary)
            const url = p && videoUrl(reviewRec, p)
            return p
              ? <div className="demo-renders"><span className="demo-count">primary video</span>{url ? <a className="demo-render-primary" href={url} target="_blank" rel="noreferrer">★ {p.kept ?? p.name}</a> : <span>★ {p.name}</span>}</div>
              : <div className="demo-count">no primary video{reviewRec.mode !== 'assert' ? ` -- a ${reviewRec.mode} renders none` : ''}</div>
          })()}
          <ArtifactLinks r={reviewRec} url={artifactUrl} />
          <AnalysisLinks r={reviewRec} url={recordFileUrl} />
        </div>
      )}
      <div className="ctl-hint">
        The worker’s agent reads the record’s directory in place, read-only, starting from MAP.md -- the record’s map -- then what it needs of record.json, the document, artifacts/ (summary, image, outputs, log, screenshots) and analysis/ (frames from the videos at each step boundary and scene change, and more of them when the demo failed), and replies with its analysis.
      </div>
      <textarea className="demo-question" value={question} placeholder="anything in particular to look at? (optional)" onChange={e => setQuestion(e.target.value)} />
      <div className="demo-actions">
        <input className="demo-dup-name" value={reviewAgent} placeholder="agent (default: the worker’s)" onChange={e => setReviewAgent(e.target.value)} />
        <button className="sys-btn" disabled={!connected || !reviewId || reviews.pending} onClick={askReview}>▶ review with the worker</button>
        <button className="sys-btn sys-btn-neutral" disabled={!reviewId || reviews.pending} onClick={() => readReviews.current()}>refresh</button>
        {reviews.pending && <span className="demo-count">working…</span>}
      </div>
      {reviews.reply && !reviews.reply.ok && <div className="ctl-run-error-msg">{reviews.reply.error}</div>}
      {reviews.reply?.ok && reviews.reply.op === 'demo-review' && <div className="ctl-hint">{reviews.reply.detail}</div>}
      {reviewList.length === 0 ? (
        <div className="sys-providers-none">{reviewId ? 'no reviews of this record yet' : 'pick a record to review'}</div>
      ) : (
        <div className="demo-list">
          {reviewList.map(v => (
            <div key={v.id} className={`demo-card demo-record-${v.status === 'completed' ? 'ok' : v.status === 'failed' ? 'failed' : 'running'}`}>
              <div className="demo-card-head">
                <span className="demo-app-dot" />
                <span className="demo-app-name">{v.id}</span>
                <span className="demo-count">{v.status === 'submitted' ? 'handed to the worker' : v.status} · asked {when(v.requested_at)}{v.finished_at ? ` · done ${when(v.finished_at)}` : ''}{v.agent ? ` · ${v.agent}` : ''}</span>
              </div>
              {v.question && <div className="demo-desc">asked: {v.question}</div>}
              {v.error && <div className="ctl-run-error-msg">{v.error}</div>}
              {v.comment && <div className="demo-count">{v.comment}</div>}
              {v.output && <pre className="demo-source demo-review-output">{v.output}</pre>}
              {v.output_trimmed && <div className="demo-count">trimmed -- the work signal{v.signal_file ? ` ${v.signal_file}` : ''} has all of it</div>}
            </div>
          ))}
        </div>
      )}
    </>
  )

  return (
    <div className="demo-panel">
      <div className="demo-section">
        <div className="demo-heading">
          <span className="demo-header" title="the prototype surface: what the project is made of, and what's up to give a demo with">protosurf</span>
          <div className="ctl-subtabs ps-areas">
            {PROTOSURF_AREAS.map(t => (
              <button key={t} className={`ctl-subtab${surf === t ? ' ctl-subtab-active' : ''}`} onClick={() => setSurf(t)}>{t}</button>
            ))}
          </div>
          <span className="demo-count">{surf === 'map' ? 'the project’s content, a tile per file sized by its lines' : `${up} of ${statuses.length} up on ${scope}`}</span>
        </div>
        {surf === 'map' ? <ProtosurfMapView request={request} replies={replies} connected={connected} highlight={highlight} onClearHighlight={clearPick} /> : (
        <div className="demo-apps">
          {statuses.map(a => (
            <div
              key={a.name}
              className={`demo-app demo-app-${a.st.kind}${onOpenApp ? ' demo-app-open' : ''}`}
              title={onOpenApp ? `open the ${a.name} tab` : undefined}
              onClick={onOpenApp ? () => onOpenApp(a.name) : undefined}
            >
              <span className="demo-app-dot" />
              <span className="demo-app-name">{a.name}</span>
              <span className="demo-app-status">{a.st.label}</span>
              <span className="demo-app-role">{a.role}</span>
            </div>
          ))}
        </div>
        )}
      </div>
      <div className="demo-section">
        <div className="ctl-subtabs">
          {SUBTABS.map(t => (
            <button key={t} className={`ctl-subtab${sub === t ? ' ctl-subtab-active' : ''}`} onClick={() => setSub(t)}>
              {t}{((t === 'records' || t === 'suites') && running) || (t === 'proposals' && proposals.some(p => p.status === 'creating')) ? ' ●' : ''}
            </button>
          ))}
        </div>
        {!request ? (
          <div className="sys-providers-none">demos run on one host -- open a host’s demo tab to run them and see its records</div>
        ) : !listing ? (
          <div className="sys-providers-none">{list.reply && !list.reply.ok ? list.reply.error : connected ? 'reading the demos…' : 'not connected'}</div>
        ) : (
          <>
            <div className="demo-heading">
              <span className="demo-header">{SUBTAB_HEADER[sub]}</span>
              <span className="demo-count" title="where this host keeps its demo library and records">{listing.demo_dir || 'no directory for records on this host'}</span>
              <button className="sys-btn sys-btn-neutral" onClick={() => refresh.current()} disabled={list.pending}>refresh</button>
            </div>
            {busyNote && <div className="ctl-hint">{busyNote}</div>}
            {sub === 'proposals' ? (
              <ProposalsView
                request={request} replies={replies} connected={connected}
                proposals={proposals}
                onChanged={() => refresh.current()}
                onPick={name => pickOnMap('proposal', name)} isPicked={name => isPicked('proposal', name)} onClearPick={clearPick}
                onOpenDemo={openDemoRow}
              />
            ) : sub === 'suites' ? suitesView() : sub === 'review' ? reviewView() : sub === 'demos' ? (
              <OriginGroups
                items={demos}
                card={d => (
                    <div key={d.name} className={`demo-card${open === d.name ? ' demo-card-open' : ''}`}>
                      <div className="demo-card-head" onClick={() => { setOpen(open === d.name ? null : d.name); setShowSource(false); setDupName(''); pickOnMap('demo', d.name) }}>
                        <span className="demo-app-name">{d.title}</span>
                        <span className="demo-count">{d.name}</span>
                        <MapMark on={isPicked('demo', d.name)} onClear={clearPick} />
                        {d.sut === 'same' && <span className="demo-origin demo-sut-same" title="same system under test: it runs on the node running it, with no node to pick">SSUT</span>}
                        {proposals.some(p => p.demo === d.name) && <span className="demo-origin" title="built by the worker's agent from the proposal of this name">from proposal</span>}
                        <span className={`demo-origin demo-origin-${d.origin}`}>{d.origin}</span>
                      </div>
                      <PhaseChain phases={d.phases} />
                      {d.error && <div className="ctl-run-error-msg">{d.error}</div>}
                      {open === d.name && openDemo && (
                        <div className="demo-card-body">
                          {d.description && <div className="demo-desc">{d.description}</div>}
                          {d.path && <div className="demo-count">at {d.path}</div>}
                          <VarInputs vars={d.variables} entered={varsOf(d)} last={lastValues} onChange={(k, v) => setVar(d, k, v)} nodes={nodes} thisNode={thisNode} scope={scope} />
                          <div className="demo-actions">
                            <button className="sys-btn" disabled={!connected || !!d.error || action.pending || running} title={running ? 'a demo is running -- one runs at a time' : 'assert the composition: lead in, run the subject, tear down, compose'} onClick={() => run(d, 'assert')}>▶ run demo</button>
                            <button className="sys-btn sys-btn-neutral" disabled={!connected || !!d.error || action.pending || running} title="read the composition: measure only, nothing runs" onClick={() => run(d, 'read')}>read only</button>
                            <input className="demo-dup-name" value={dupName} placeholder="new demo name" onChange={e => setDupName(e.target.value)} />
                            <button className="sys-btn sys-btn-neutral" disabled={!connected || !dupName.trim() || action.pending} title="copy this demo into the host’s library, to repurpose it there" onClick={() => action.send({ op: 'demo-duplicate', demo: d.name, name: dupName.trim() })}>duplicate</button>
                            {d.source && <button className="sys-btn sys-btn-neutral" onClick={() => setShowSource(!showSource)}>{showSource ? 'hide document' : 'show document'}</button>}
                          </div>
                          {showSource && d.source && <pre className="demo-source">{d.source}</pre>}
                        </div>
                      )}
                    </div>
                )}
              />
            ) : records.length === 0 ? (
              <div className="sys-providers-none">no demo records yet — each demo run is kept here</div>
            ) : (
              <div className="demo-list">
                {records.map(r => (
                  <div key={r.id} className={`demo-card demo-record-${r.status}`}>
                    <div className="demo-card-head" onClick={() => showRecord(r)}>
                      <span className="demo-app-dot" />
                      <span className="demo-app-name">{r.title || r.demo}</span>
                      <span className="demo-count">{r.mode}{r.undo_of ? ` of ${r.undo_of}` : ''} · {r.status} · {when(r.started_at)}{took(r) ? ` · ${took(r)}` : ''}</span>
                      {r.suite && <span className="demo-origin" title={`one demo of suite run ${r.suite_record ?? ''}`}>suite {r.suite}</span>}
                      <MapMark on={openRecord === r.id && isPicked('demo', r.demo)} onClear={clearPick} />
                    </div>
                    <PhaseChain phases={r.phases ?? []} />
                    {r.error && <div className="ctl-run-error-msg">{r.error}</div>}
                    {(r.renders ?? []).length > 0 && (
                      <div className="demo-renders">
                        {(r.renders ?? []).map(v => {
                          const url = videoUrl(r, v)
                          const label = `${v.kept ? '🎬' : '⚠'} ${v.primary ? '★ ' : ''}${v.call ? `${v.phase ?? v.call}: ` : ''}${v.name || v.file_id}${v.kept && v.size ? ` (${sizeOf(v.size)})` : ''}`
                          const title = [v.primary && 'the primary video: the demo’s subject, without lead-in or tear-down', v.composition, v.call && `inside call ${v.call}`, v.kept ? `kept with the record as videos/${v.kept}` : `not kept with the record${v.keep_error ? `: ${v.keep_error}` : ''} -- this links the files tab`].filter(Boolean).join('\n')
                          const cls = [v.kept ? '' : 'demo-render-unkept', v.primary ? 'demo-render-primary' : ''].filter(Boolean).join(' ') || undefined
                          return url
                            ? <a key={v.file_id} className={cls} href={url} target="_blank" rel="noreferrer" title={title}>{label}</a>
                            : <span key={v.file_id} className={cls} title={title}>{label}</span>
                        })}
                      </div>
                    )}
                    {openRecord === r.id && (
                      <div className="demo-card-body">
                        <div className="demo-count">kept at {r.dir} · by {r.origin} on {r.node || 'this node'} · tool {r.tool}{r.run_id ? ` (run ${r.run_id})` : ''}</div>
                        {Object.keys(r.variables ?? {}).length > 0 && (
                          <div className="demo-count">variables: {Object.entries(r.variables).map(([k, v]) => `${k}=${v}`).join(', ')}</div>
                        )}
                        {r.summary && <div className="demo-count">fields: {Object.entries(r.summary).map(([k, v]) => `${v} ${k}`).join(', ')}</div>}
                        <ArtifactLinks r={r} url={artifactUrl} />
                        <AnalysisLinks r={r} url={recordFileUrl} />
                        <div className="demo-actions">
                          {r.status !== 'running' && (
                            <button className="sys-btn sys-btn-neutral" title="have the worker’s agent review this record, read-only" onClick={() => reviewThis(r)}>review…</button>
                          )}
                          {r.mode === 'assert' && r.has_image && r.status !== 'running' && (
                            <button className="sys-btn sys-btn-neutral" disabled={!connected || action.pending || running} onClick={() => undo(r)}>delete what it made</button>
                          )}
                          {r.mode === 'assert' && r.has_image && r.status !== 'running' && (r.renders === undefined || r.renders.some(v => !v.kept)) && (
                            <button className="sys-btn sys-btn-neutral" disabled={!connected || action.pending} title="fetch the videos this run rendered from the files tab into the record, those it hasn’t kept yet" onClick={() => keepVideos(r)}>keep videos</button>
                          )}
                        </div>
                        {detail.pending ? <div className="ctl-hint">reading the record…</div>
                          : detail.reply && !detail.reply.ok ? <div className="ctl-run-error-msg">{detail.reply.error}</div>
                          : shownDetail && (
                            <>
                              {shownDetail.detail && <div className="ctl-hint">{shownDetail.detail}</div>}
                              {shownDetail.demo_record?.log_tail && <pre className="demo-source">{shownDetail.demo_record.log_tail}</pre>}
                              {shownDetail.demo_image && <pre className="demo-source">{shownDetail.demo_image}</pre>}
                            </>
                          )}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
