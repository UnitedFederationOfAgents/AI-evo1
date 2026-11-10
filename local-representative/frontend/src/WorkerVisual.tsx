import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import type { DeploymentInfo, SlopspaceInfo, WorkerSignalInfo, WorkerStateMsg } from './types'

// WorkerVisual is the worker tab's visual sub-tab (CondoccerV2 Step 1,
// Revision E): one horizontal swimlane per agent type, time running left to
// right in proportion and zoomable. Each lane has a blue track of work
// submissions (work signals), and below it a red track per slopspace -- one
// line per writespace repo -- from its creation until now.
//
// While a slopspace is deployed to the lane's agent type (Revision F), its
// lines merge into a red box spanning the deployment, from deploy to return.
// The box sits just below the blue track, so work done while it was deployed
// but not in it runs ABOVE it; work that ran in it is WITHIN it -- the box
// rises around the part of the submission that ran. Only the deployed
// slopspace's lines touch its box; any other slopspace's lines it reaches past
// stop short of it, with a gap (Revision I). Clicking a submission, a
// slopspace or a deployment shows it in the details panel on the right, and
// clicking the blue track itself shows its agent type (Revision G).
//
// It only draws what dungeon-keeper reports in worker-state: ongoing signals
// and the most recent completed ones, and the slopspaces still on the host
// with their last few deployments. Slopspace work is taken to have run in the
// slopspace it recorded, or else in the one deployed to its agent type when it
// started (there is at most one). Where work ran in a slopspace with no
// recorded deployment covering it -- from before deployments were recorded,
// or a deleted slopspace -- the box is dashed and spans only that work. A
// deleted slopspace shows as a dashed track spanning the work that ran in it.
//
// The human is green (dTT-IntegrationImpls Step 1, Revision H, and its
// "Slopspaces DTT Proposals" sketch): a green lane at the bottom is the human
// track. A deployment that returned with DTT proposals (in its writespaces'
// dtt-requests) has a green bar inside its red box at the return end. A
// dotted green line runs from the bar down to a dashed box on the human track
// -- the proposals waiting on the human, from the return until they're
// answered (or now). Once answered, a solid green line runs from the bar
// along time and down to a solid box with the decision: ✓ accept, ✗ reject,
// ↻ modify. The slopspace can't be deployed again until then, so the next
// red box starts after it.
//
// condocv2's agent boxes link here (PrivateRepoOps Step 4): clicking one
// opens this sub-tab with its submission selected (WorkFocus).
//
// Mirrored in agent-coordinator/frontend/src/WorkerVisual.tsx -- keep the
// two in step.

const BASE_AGENT_TYPES = ['agent-worker', 'heuristic-request']
const UNDEPLOYED = 'undeployed'
const HUMAN = 'human'

const AXIS_H = 24      // the time axis above the lanes
const LANE_HEAD = 16   // above a lane's blue track
const BOX_H = 18       // a submission box
const TRACK_GAP = 18   // between the blue track and the first slopspace
const REPO_GAP = 4     // between one slopspace's repo lines
const SLOP_GAP = 12    // between slopspaces
const LANE_PAD = 14    // below a lane's last track
const BOX_PAD = 6      // a red box's margin around what it encloses
const DEPLOY_GAP = 4   // between the blue track's boxes and a deployment box below them
const LINE_GAP = 5     // between another slopspace's deployment box and the lines it cuts
const MIN_BOX_W = 8
const MAX_ZOOM = 2000

const BLUE = '#5bb8ff'
const RED = '#e05555'
const GREEN = '#4ec94e'
const DEPLOY_FILL = '#2a1518'
const REVIEW_FILL = '#0f1a10'
const BAR_INSET = 4    // the green bar's distance inside a red box's return end

const DECISION_MARK: Record<string, string> = { accept: '✓', reject: '✗', modify: '↻', mixed: '±' }

type Selection =
  | { kind: 'signal'; key: string }
  | { kind: 'slopspace'; id: string }
  | { kind: 'agent-type'; type: string }
  | { kind: 'review'; id: string; at: number } // a slopspace's deployment (by deployed_at) that returned DTT proposals
  | { kind: 'human' }
  | null

interface SlopTrack {
  id: string
  info?: SlopspaceInfo
  y: number     // its first repo line
  lines: number // one per repo, at least one
}

interface Lane {
  type: string
  y: number
  height: number
  blueY: number
  signals: WorkerSignalInfo[]
  tracks: SlopTrack[]
}

const signalKey = (s: WorkerSignalInfo) => s.id || s.file

// WorkFocus asks for one work submission to be selected -- condocv2's agent
// box, clicked (its Swimlanes.tsx openWork). The condoc keeps no on-box id
// for it, so it is found in what the worker reports: the signal file, if it
// is still listed, else the role's submission in that slopspace nearest at.
// seq tells one request from the next.
export interface WorkFocus {
  slopspace?: string
  role?: string
  signal?: string
  at: number // unix ms
  seq: number
}

function focusedSignal(f: WorkFocus, signals: WorkerSignalInfo[], ran: Map<string, string>): WorkerSignalInfo | null {
  const byFile = f.signal ? signals.find(s => s.file === f.signal) : undefined
  if (byFile) return byFile
  let best: WorkerSignalInfo | null = null
  for (const s of signals) {
    if (f.role && s.role !== f.role) continue
    if (f.slopspace && ran.get(signalKey(s)) !== f.slopspace) continue
    if (!best || Math.abs(s.created_at - f.at) < Math.abs(best.created_at - f.at)) best = s
  }
  return best
}

function signalStart(s: WorkerSignalInfo) {
  return s.started_at || s.created_at
}

function signalEnd(s: WorkerSignalInfo, now: number) {
  if (s.completed_at) return s.completed_at
  return s.status === 'pending' || s.status === 'processing' ? now : signalStart(s)
}

const signalType = (s: WorkerSignalInfo, fallbackType: string) => s.agent_type || fallbackType || BASE_AGENT_TYPES[0]

// ranIn maps each slopspace submission to the slopspace it ran in: the one it
// recorded, or else the one deployed to its agent type when it started.
function ranIn(signals: WorkerSignalInfo[], slopspaces: SlopspaceInfo[], fallbackType: string, now: number) {
  const out = new Map<string, string>()
  for (const s of signals) {
    if (s.slopspace_id) {
      out.set(signalKey(s), s.slopspace_id)
      continue
    }
    if (s.work_type !== 'slopspace' || !s.started_at) continue
    const type = signalType(s, fallbackType)
    const t = signalStart(s)
    const deployed = slopspaces.find(sp => sp.deployments?.some(d => d.agent_type === type && d.deployed_at <= t && t <= (d.returned_at || now)))
    if (deployed) out.set(signalKey(s), deployed.id)
  }
  return out
}

// A deployment box: one of a slopspace's deployments, with a raised part
// around each submission that ran in it (clamped to the deployment).
interface DeployBox {
  key: string
  from: number
  to: number
  open: boolean     // still deployed
  inferred: boolean // no recorded deployment: spans the work that ran in it
  work: { from: number; to: number }[]
  review?: DeploymentInfo // returned with DTT proposals: the human's review of them
}

// deployBoxes lists a track's deployments to the lane's agent type, plus
// inferred ones for work in it that no recorded deployment covers.
function deployBoxes(l: Lane, tr: SlopTrack, ran: Map<string, string>, now: number): DeployBox[] {
  const boxes: DeployBox[] = (tr.info?.deployments ?? [])
    .filter(d => d.agent_type === l.type)
    .map((d): DeployBox => ({
      key: `d-${d.deployed_at}`, from: d.deployed_at, to: d.returned_at || now, open: !d.returned_at, inferred: false, work: [],
      review: d.returned_at && d.proposals ? d : undefined,
    }))
  for (const s of l.signals) {
    if (ran.get(signalKey(s)) !== tr.id) continue
    const from = signalStart(s)
    const to = signalEnd(s, now)
    let box = boxes.find(b => !b.inferred && b.from <= from && from <= b.to)
    if (!box) {
      box = { key: `w-${signalKey(s)}`, from, to, open: false, inferred: true, work: [] }
      boxes.push(box)
    }
    box.work.push({ from: Math.max(from, box.from), to: Math.min(to, box.to) })
  }
  return boxes
}

// boxShape places a deployment box in pixels: its body runs from just below
// the blue track to past its slopspace's last line, and each run in it raises
// a part around the submission.
function boxShape(l: Lane, tr: SlopTrack, b: DeployBox, x: (t: number) => number) {
  const x0 = x(b.from)
  const x1 = Math.max(x0 + MIN_BOX_W, x(b.to))
  const top = l.blueY + BOX_H / 2 + DEPLOY_GAP
  const bottom = tr.y + (tr.lines - 1) * REPO_GAP + BOX_PAD
  const rises = b.work.map(w => {
    const wx0 = Math.max(x0, x(w.from) - BOX_PAD)
    const wx1 = Math.min(x1, Math.max(x(w.from) + MIN_BOX_W, x(w.to)) + BOX_PAD)
    return { x: wx0, w: Math.max(0, wx1 - wx0), y: l.blueY - BOX_H / 2 - BOX_PAD, h: top + BOX_PAD - (l.blueY - BOX_H / 2 - BOX_PAD) }
  })
  const parts = [{ x: x0, w: x1 - x0, y: top, h: bottom - top }, ...rises.filter(r => r.w > 0)]
  return { ...b, x0, x1, top, bottom, parts }
}

// cutGaps splits the span a..b around the gaps, leaving what's outside them.
function cutGaps(a: number, b: number, gaps: [number, number][]) {
  const segs: [number, number][] = []
  let at = a
  for (const [g0, g1] of [...gaps].sort((p, q) => p[0] - q[0])) {
    if (g1 <= at) continue
    if (g0 >= b) break
    if (g0 > at) segs.push([at, g0])
    at = g1
  }
  if (at < b) segs.push([at, b])
  return segs
}

// layoutLanes stacks the lanes: the base agent types always, then any other
// type something reports, then one for slopspaces never deployed. A lane's
// slopspaces are those deployed to its type (now or before) plus any its work
// ran in.
function layoutLanes(signals: WorkerSignalInfo[], slopspaces: SlopspaceInfo[], fallbackType: string, ran: Map<string, string>): Lane[] {
  const typeOf = (s: WorkerSignalInfo) => signalType(s, fallbackType)
  const types = [...BASE_AGENT_TYPES]
  const add = (t: string) => { if (t && !types.includes(t)) types.push(t) }
  signals.forEach(s => add(typeOf(s)))
  slopspaces.forEach(s => {
    add(s.agent_type ?? '')
    s.deployments?.forEach(d => add(d.agent_type))
  })
  if (slopspaces.some(s => !s.agent_type)) types.push(UNDEPLOYED)

  const byId = new Map(slopspaces.map(s => [s.id, s]))
  let y = AXIS_H
  const lanes = types.map(type => {
    const laneSignals = signals.filter(s => typeOf(s) === type)
    const ids = slopspaces
      .filter(s => (s.agent_type || UNDEPLOYED) === type || s.deployments?.some(d => d.agent_type === type))
      .map(s => s.id)
    laneSignals.forEach(s => {
      const id = ran.get(signalKey(s))
      if (id && !ids.includes(id)) ids.push(id)
    })
    ids.sort((a, b) => (byId.get(a)?.created_at ?? Infinity) - (byId.get(b)?.created_at ?? Infinity))

    const blueY = y + LANE_HEAD + BOX_H / 2
    let ty = type === UNDEPLOYED ? y + LANE_HEAD : blueY + BOX_H / 2 + TRACK_GAP
    const tracks = ids.map(id => {
      const info = byId.get(id)
      const lines = Math.max(1, info?.repos?.length ?? 1)
      const t: SlopTrack = { id, info, y: ty, lines }
      ty += (lines - 1) * REPO_GAP + SLOP_GAP
      return t
    })
    const bottom = tracks.length ? ty - SLOP_GAP : blueY + BOX_H / 2
    const lane: Lane = { type, y, height: bottom - y + LANE_PAD, blueY, signals: laneSignals, tracks }
    y += lane.height
    return lane
  })
  // The human's green track, last: what's been submitted to them for review.
  // Its blueY is the green track's.
  lanes.push({ type: HUMAN, y, height: LANE_HEAD + BOX_H + LANE_PAD, blueY: y + LANE_HEAD + BOX_H / 2, signals: [], tracks: [] })
  return lanes
}

// A deployment's DTT proposals and the human's review of them, placed in
// pixels: the green bar inside its red box, and the boxes on the human track.
interface ReviewMark {
  id: string     // the slopspace
  at: number     // its deployment's deployed_at
  d: DeploymentInfo
  barX: number
  barTop: number
  barBottom: number
  waitX0: number // the dashed box: waiting on the human, from the return...
  waitX1: number // ...until answered, or now
  decX?: number  // the decision box's left edge, once answered
}

function reviewMarks(lanes: Lane[], shapes: ReturnType<typeof boxShape>[][][], x: (t: number) => number, now: number): ReviewMark[] {
  const out: ReviewMark[] = []
  lanes.forEach((l, li) => l.tracks.forEach((tr, ti) => shapes[li][ti].forEach(b => {
    const d = b.review
    if (!d || !d.returned_at) return
    const barX = Math.max(b.x0 + 2, b.x1 - BAR_INSET)
    const decided = d.decision && d.decided_at ? d.decided_at : 0
    const waitX0 = barX - 3
    const waitX1 = Math.max(waitX0 + MIN_BOX_W, x(decided || now))
    out.push({
      id: tr.id, at: d.deployed_at, d, barX, barTop: b.top + 3, barBottom: b.bottom - 3,
      waitX0, waitX1, decX: decided ? waitX1 : undefined,
    })
  })))
  return out
}

const TICK_STEPS = [
  1e3, 5e3, 15e3, 30e3,
  60e3, 5 * 60e3, 15 * 60e3, 30 * 60e3,
  3600e3, 3 * 3600e3, 6 * 3600e3, 12 * 3600e3,
  86400e3, 7 * 86400e3, 30 * 86400e3,
]

function tickLabel(t: number, step: number) {
  const d = new Date(t)
  if (step < 60e3) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  if (step < 86400e3) {
    const midnight = d.getHours() === 0 && d.getMinutes() === 0
    return midnight ? d.toLocaleDateString([], { month: 'short', day: 'numeric' }) : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

function fmtDuration(ms: number) {
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`
}

const fmtTime = (t?: number) => (t ? new Date(t).toLocaleString() : '—')

export function WorkerVisual({ state, focus }: { state: WorkerStateMsg | null; focus?: WorkFocus | null }) {
  const [now, setNow] = useState(() => Date.now())
  const [zoom, setZoom] = useState(1)
  const [viewW, setViewW] = useState(800)
  const [sel, setSel] = useState<Selection>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  // Where to keep still while zooming: a fraction of the width and its
  // offset in the viewport, applied once the new width has rendered.
  const anchor = useRef<{ frac: number; offset: number } | null>(null)

  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 5000)
    return () => clearInterval(t)
  }, [])

  // The scroll pane only exists once the worker has reported.
  const ready = !!state
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    const ro = new ResizeObserver(() => setViewW(Math.max(200, el.clientWidth)))
    ro.observe(el)
    return () => ro.disconnect()
  }, [ready])

  const signals = state?.signals ?? []
  const slopspaces = state?.slopspaces ?? []
  const ran = useMemo(() => ranIn(signals, slopspaces, state?.agent_type ?? '', now), [signals, slopspaces, state?.agent_type, now])
  const lanes = useMemo(() => layoutLanes(signals, slopspaces, state?.agent_type ?? '', ran), [signals, slopspaces, state?.agent_type, ran])

  // The time range: everything reported, up to now, with a little margin.
  const starts = [...signals.map(s => s.created_at), ...slopspaces.map(s => s.created_at)].filter(t => t > 0)
  const first = starts.length ? Math.min(...starts) : now - 3600e3
  const span = Math.max(now - first, 60e3)
  const t0 = first - span * 0.02
  const t1 = now + span * 0.03
  const width = viewW * zoom
  const x = (t: number) => ((t - t0) / (t1 - t0)) * width

  const zoomTo = (z: number, offset = viewW / 2) => {
    const el = scrollRef.current
    const next = Math.min(MAX_ZOOM, Math.max(1, z))
    if (el) anchor.current = { frac: (el.scrollLeft + offset) / width, offset }
    setZoom(next)
  }

  useLayoutEffect(() => {
    const el = scrollRef.current
    if (!el || !anchor.current) return
    el.scrollLeft = anchor.current.frac * width - anchor.current.offset
    anchor.current = null
  }, [width])

  // A focus request selects its submission and scrolls it into the middle,
  // once: as soon as the worker has reported one that matches.
  const focusDone = useRef(0)
  useEffect(() => {
    if (!focus || focus.seq === focusDone.current) return
    const s = focusedSignal(focus, signals, ran)
    if (!s) return
    focusDone.current = focus.seq
    setSel({ kind: 'signal', key: signalKey(s) })
    const el = scrollRef.current
    if (el) el.scrollLeft = x(signalStart(s)) - el.clientWidth / 2
  })

  // Ctrl/Cmd + wheel zooms around the pointer; a plain wheel scrolls. React's
  // wheel listener is passive, so this one is attached by hand.
  const zoomRef = useRef(zoomTo)
  zoomRef.current = zoomTo
  const zoomVal = useRef(zoom)
  zoomVal.current = zoom
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return
      e.preventDefault()
      const offset = e.clientX - el.getBoundingClientRect().left
      zoomRef.current(zoomVal.current * Math.exp(-e.deltaY * 0.002), offset)
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [ready])

  if (!state) return <div className="service-empty">the worker hasn't reported yet</div>

  const height = lanes.length ? lanes[lanes.length - 1].y + lanes[lanes.length - 1].height : AXIS_H
  const step = TICK_STEPS.find(s => (s / (t1 - t0)) * width >= 90) ?? TICK_STEPS[TICK_STEPS.length - 1]
  const ticks: number[] = []
  for (let t = Math.ceil(t0 / step) * step; t <= t1; t += step) ticks.push(t)

  const selSignal = sel?.kind === 'signal' ? signals.find(s => signalKey(s) === sel.key) ?? null : null
  const selSlopspaceId = sel?.kind === 'slopspace' ? sel.id : null
  const isSel = (s: WorkerSignalInfo) => sel?.kind === 'signal' && sel.key === signalKey(s)
  const selType = sel?.kind === 'agent-type' ? sel.type : null
  const toggle = (next: Selection) => setSel(cur => (JSON.stringify(cur) === JSON.stringify(next) ? null : next))

  const shapesByLane = lanes.map(l => l.tracks.map(tr => deployBoxes(l, tr, ran, now).map(b => boxShape(l, tr, b, x))))
  const marks = reviewMarks(lanes, shapesByLane, x, now)
  const human = lanes[lanes.length - 1]
  const humanTop = human.blueY - BOX_H / 2
  const selReview = sel?.kind === 'review' ? marks.find(m => m.id === sel.id && m.at === sel.at) ?? null : null
  const humanPicked = sel?.kind === 'human'

  return (
    <div className="wv">
      <div className="wv-toolbar">
        <span className="wv-legend"><span className="wv-swatch" style={{ borderColor: BLUE }} /> work submission</span>
        <span className="wv-legend"><span className="wv-swatch wv-swatch-line" style={{ borderColor: RED }} /> slopspace (a line per repo)</span>
        <span className="wv-legend"><span className="wv-swatch" style={{ borderColor: RED, background: DEPLOY_FILL }} /> deployed (work within it ran in it)</span>
        <span className="wv-legend"><span className="wv-swatch" style={{ borderColor: GREEN, background: REVIEW_FILL }} /> human: DTT proposals to review (dashed: waiting)</span>
        <span className="wv-spacer" />
        <button className="worker-submit-btn" onClick={() => zoomTo(zoom / 2)} disabled={zoom <= 1}>−</button>
        <span className="wv-zoom">{zoom < 10 ? zoom.toFixed(1) : Math.round(zoom)}×</span>
        <button className="worker-submit-btn" onClick={() => zoomTo(zoom * 2)} disabled={zoom >= MAX_ZOOM}>+</button>
        <button className="worker-submit-btn" onClick={() => setZoom(1)} disabled={zoom === 1}>fit</button>
      </div>
      <div className="wv-main">
        <div className="wv-labels" style={{ height }}>
          {lanes.map(l => (
            <div key={l.type} className="wv-label" style={{ top: l.y, height: l.height }}>{l.type}</div>
          ))}
        </div>
        <div className="wv-scroll" ref={scrollRef}>
          <svg className="wv-svg" width={width} height={height}>
            {ticks.map(t => (
              <g key={t}>
                <line x1={x(t)} x2={x(t)} y1={AXIS_H - 4} y2={height} className="wv-grid" />
                <text x={x(t) + 3} y={AXIS_H - 9} className="wv-tick">{tickLabel(t, step)}</text>
              </g>
            ))}
            {lanes.map((l, i) => (
              <line key={l.type} x1={0} x2={width} y1={l.y} y2={l.y} className={i === 0 ? 'wv-lane-edge' : 'wv-lane-edge wv-lane-sep'} />
            ))}
            <line x1={x(now)} x2={x(now)} y1={AXIS_H} y2={height} className="wv-now"><title>now</title></line>

            {lanes.map((l, li) => {
              const shapes = shapesByLane[li]
              if (l.type === HUMAN) return (
                <g key={l.type}>
                  {/* the human's green track: clicking it shows what awaits review */}
                  <rect
                    className="wv-click"
                    x={0} y={l.blueY - BOX_H / 2} width={width} height={BOX_H}
                    fill="transparent"
                    onClick={() => toggle({ kind: 'human' })}
                  >
                    <title>the human: DTT proposals submitted for review</title>
                  </rect>
                  <line
                    x1={0} x2={width} y1={l.blueY} y2={l.blueY}
                    stroke={humanPicked ? '#fff' : GREEN} strokeOpacity={humanPicked ? 0.9 : 0.6} strokeWidth={humanPicked ? 2 : 1}
                    pointerEvents="none"
                  />
                </g>
              )
              return (
              <g key={l.type}>
                {/* the blue track's hit area, under everything else in the
                    lane: clicking the track selects its agent type */}
                {l.type !== UNDEPLOYED && (
                  <rect
                    className="wv-click"
                    x={0} y={l.blueY - BOX_H / 2} width={width} height={BOX_H}
                    fill="transparent"
                    onClick={() => toggle({ kind: 'agent-type', type: l.type })}
                  >
                    <title>{`agent type ${l.type}`}</title>
                  </rect>
                )}

                {/* slopspace lines. Only a slopspace's own deployment box
                    touches its lines; another's box, reaching down past
                    them, cuts them with a gap either side. */}
                {l.tracks.map((tr, i) => {
                  const used = l.signals.filter(s => ran.get(signalKey(s)) === tr.id)
                  const from = tr.info ? tr.info.created_at : Math.min(...used.map(signalStart))
                  const to = tr.info ? now : Math.max(...used.map(s => signalEnd(s, now)))
                  const last = tr.y + (tr.lines - 1) * REPO_GAP
                  const gaps = shapes.flatMap((bs, j) => j === i ? [] : bs
                    .filter(s => s.top <= last && tr.y <= s.bottom)
                    .map((s): [number, number] => [s.x0 - LINE_GAP, s.x1 + LINE_GAP]))
                  const segs = cutGaps(x(from), x(to), gaps)
                  const picked = selSlopspaceId === tr.id
                  return (
                    <g key={tr.id} className="wv-click" onClick={() => toggle({ kind: 'slopspace', id: tr.id })}>
                      <title>{`slopspace ${tr.id}${tr.info?.repos?.length ? `\n${tr.info.repos.join('\n')}` : ''}${tr.info ? '' : '\n(deleted)'}`}</title>
                      <rect x={x(from)} y={tr.y - 5} width={Math.max(2, x(to) - x(from))} height={(tr.lines - 1) * REPO_GAP + 10} fill="transparent" />
                      {Array.from({ length: tr.lines }, (_, k) => segs.map(([a, b]) => (
                        <line
                          key={`${k}-${a}`}
                          x1={a} x2={b}
                          y1={tr.y + k * REPO_GAP} y2={tr.y + k * REPO_GAP}
                          stroke={picked ? '#fff' : RED}
                          strokeWidth={picked ? 1.5 : 1}
                          strokeDasharray={tr.info ? undefined : '4 3'}
                          opacity={tr.info?.in_use || tr.info?.deployed || picked ? 1 : 0.7}
                        />
                      )))}
                    </g>
                  )
                })}

                {/* red boxes: a slopspace deployed, below the blue track,
                    rising around the work that ran in it. Each is drawn as
                    its outlines, then its fills over them, so the body and
                    its raised parts read as one shape. */}
                {l.tracks.map((tr, i) => shapes[i].map(b => {
                  const parts = b.parts
                  const picked = selSlopspaceId === tr.id
                  const stroke = picked ? '#fff' : RED
                  const sw = picked ? 2 : 1.25
                  const dash = b.inferred ? '4 3' : undefined
                  return (
                    <g key={`${tr.id}-${b.key}`} className="wv-click" onClick={() => toggle({ kind: 'slopspace', id: tr.id })}>
                      <title>{`slopspace ${tr.id}\n${b.inferred ? 'ran work (deployment not recorded)' : `deployed ${fmtTime(b.from)} – ${b.open ? 'now' : fmtTime(b.to)}`}\n${b.work.length} submission${b.work.length === 1 ? '' : 's'} ran in it`}</title>
                      {parts.map((p, k) => (
                        <rect key={`s${k}`} x={p.x} y={p.y} width={p.w} height={p.h} rx={3} fill="none" stroke={stroke} strokeWidth={sw} strokeDasharray={dash} />
                      ))}
                      {/* inset by half the stroke: hides the outlines inside the shape, keeps its edge */}
                      {parts.map((p, k) => (
                        <rect key={`f${k}`} x={p.x + sw / 2} y={p.y + sw / 2} width={Math.max(0, p.w - sw)} height={Math.max(0, p.h - sw)} rx={2} fill={DEPLOY_FILL} />
                      ))}
                    </g>
                  )
                }))}

                {/* the blue track and its submissions */}
                {l.type !== UNDEPLOYED && (
                  <line
                    x1={0} x2={width} y1={l.blueY} y2={l.blueY}
                    stroke={selType === l.type ? '#fff' : BLUE} strokeOpacity={selType === l.type ? 0.9 : 0.55} strokeWidth={selType === l.type ? 2 : 1}
                    pointerEvents="none"
                  />
                )}
                {l.signals.map(s => {
                  const x0 = x(s.created_at)
                  const w = Math.max(MIN_BOX_W, x(signalEnd(s, now)) - x0)
                  const picked = isSel(s)
                  return (
                    <rect
                      key={signalKey(s)}
                      className={`wv-click wv-sub wv-sub-${s.status}`}
                      x={x0} y={l.blueY - BOX_H / 2} width={w} height={BOX_H}
                      rx={2}
                      stroke={picked ? '#fff' : BLUE} strokeWidth={picked ? 2 : 1.25}
                      onClick={() => toggle({ kind: 'signal', key: signalKey(s) })}
                    >
                      <title>{`${s.status} · ${s.role}\n${s.prompt.split('\n')[0].slice(0, 120)}`}</title>
                    </rect>
                  )
                })}
              </g>
              )
            })}

            {/* DTT proposals submitted to the human, over everything else:
                the green bar in the red box, the dotted line to the dashed
                waiting box, and -- once answered -- the solid line along
                time to the decision box. */}
            {marks.map(m => {
              const picked = selReview === m
              const stroke = picked ? '#fff' : GREEN
              const pick = () => toggle({ kind: 'review', id: m.id, at: m.at })
              const mark = m.d.decision ? DECISION_MARK[m.d.decision] ?? '?' : ''
              const title = `slopspace ${m.id}\n${m.d.proposals} DTT proposal${m.d.proposals === 1 ? '' : 's'} returned ${fmtTime(m.d.returned_at)}\n${m.d.decision ? `${m.d.decision} ${fmtTime(m.d.decided_at)}` : 'awaiting review'}${m.d.note ? `\n${m.d.note}` : ''}`
              const runY = m.barBottom + 3 + BAR_INSET
              return (
                <g key={`${m.id}-${m.at}`} className="wv-click" onClick={pick}>
                  <title>{title}</title>
                  <line x1={m.barX} x2={m.barX} y1={m.barTop} y2={m.barBottom} stroke={stroke} strokeWidth={3} strokeLinecap="round" />
                  <line x1={m.barX} x2={m.barX} y1={m.barBottom} y2={humanTop} stroke={stroke} strokeWidth={1.5} strokeDasharray="2 4" strokeLinecap="round" />
                  <rect
                    x={m.waitX0} y={humanTop} width={m.waitX1 - m.waitX0} height={BOX_H} rx={3}
                    fill={REVIEW_FILL} fillOpacity={0.6} stroke={stroke} strokeWidth={1.25} strokeDasharray="3 3"
                    className={m.decX === undefined ? 'wv-review-waiting' : undefined}
                  />
                  {m.decX !== undefined && (
                    <>
                      <path d={`M ${m.barX} ${m.barBottom} V ${runY} H ${m.decX + BOX_H / 2} V ${humanTop}`} fill="none" stroke={stroke} strokeWidth={1.25} />
                      <rect x={m.decX} y={humanTop} width={BOX_H} height={BOX_H} rx={2} fill={REVIEW_FILL} stroke={stroke} strokeWidth={picked ? 2 : 1.5} />
                      <text x={m.decX + BOX_H / 2} y={human.blueY + 4} textAnchor="middle" className="wv-review-mark" fill={stroke}>{mark}</text>
                    </>
                  )}
                </g>
              )
            })}
          </svg>
        </div>
        <div className="wv-details">
          <div className="wv-details-title">details</div>
          {selReview
            ? (
              <ReviewDetails
                mark={selReview}
                info={slopspaces.find(s => s.id === selReview.id)}
                now={now}
                onSlopspace={id => setSel({ kind: 'slopspace', id })}
              />
            )
            : humanPicked
            ? <HumanDetails marks={marks} now={now} onReview={m => setSel({ kind: 'review', id: m.id, at: m.at })} />
            : selSignal
            ? <SignalDetails signal={selSignal} slopspaceId={ran.get(signalKey(selSignal))} now={now} onSlopspace={id => setSel({ kind: 'slopspace', id })} />
            : selSlopspaceId
              ? (
                <SlopspaceDetails
                  id={selSlopspaceId}
                  info={slopspaces.find(s => s.id === selSlopspaceId)}
                  work={signals.filter(s => ran.get(signalKey(s)) === selSlopspaceId)}
                  onSignal={key => setSel({ kind: 'signal', key })}
                />
              )
              : selType
                ? (
                  <AgentTypeDetails
                    type={selType}
                    state={state}
                    work={lanes.find(l => l.type === selType)?.signals ?? []}
                    slopspaces={slopspaces}
                    now={now}
                    onSignal={key => setSel({ kind: 'signal', key })}
                    onSlopspace={id => setSel({ kind: 'slopspace', id })}
                  />
                )
                : <div className="worker-signal-detail">select a work submission, a slopspace, a blue track (its agent type), or the green human track or a review on it</div>}
        </div>
      </div>
      <div className="worker-signal-detail">
        Ctrl/⌘ + scroll to zoom around the pointer. Shows ongoing work, the {signals.filter(s => s.completed_at).length} most recent finished submissions and this host's current slopspaces
        {state.slopspaces_omitted ? ` (${state.slopspaces_omitted} older slopspaces not reported)` : ''}.
      </div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="wv-field">
      <span className="wv-field-label">{label}</span>
      <span className="wv-field-value">{children}</span>
    </div>
  )
}

function SignalDetails({ signal: s, slopspaceId, now, onSlopspace }: { signal: WorkerSignalInfo; slopspaceId?: string; now: number; onSlopspace: (id: string) => void }) {
  const end = s.completed_at || (s.status === 'pending' || s.status === 'processing' ? now : 0)
  return (
    <div className={`wv-detail-body worker-status-${s.status}`}>
      <div className="wv-detail-kind" style={{ color: BLUE }}>work submission</div>
      <Field label="status"><span className="worker-signal-status">{s.status}</span></Field>
      <Field label="agent type">{s.agent_type || '—'}</Field>
      <Field label="role">{s.role}</Field>
      <Field label="work">{s.work_type}{s.work_location ? ` in ${s.work_location}` : ''}</Field>
      {slopspaceId && (
        <Field label="ran in">
          <button className="wv-link" onClick={() => onSlopspace(slopspaceId)} title={slopspaceId}>{slopspaceId.slice(0, 8)}</button>
          {s.slopspace_id ? '' : ' (deployed then)'}
        </Field>
      )}
      {(s.agent || s.model || s.mode) && (
        <Field label="agent">{[s.agent, s.model && `(${s.model})`, s.mode].filter(Boolean).join(' ')}</Field>
      )}
      <Field label="created">{fmtTime(s.created_at)}</Field>
      {s.started_at && <Field label="started">{fmtTime(s.started_at)}</Field>}
      <Field label="finished">{fmtTime(s.completed_at)}</Field>
      {end > 0 && <Field label="duration">{fmtDuration(end - signalStart(s))}{s.completed_at ? '' : ' so far'}</Field>}
      <Field label="file">{s.file}</Field>
      <div className="wv-field-label">prompt</div>
      <pre className="worker-signal-text">{s.prompt}</pre>
      {s.comment && <div className="worker-signal-comment">{s.comment}</div>}
      {s.output && (
        <>
          <div className="wv-field-label">output{s.output_trimmed ? ' (the end only)' : ''}</div>
          <pre className="worker-signal-text worker-signal-output">{s.output}</pre>
        </>
      )}
    </div>
  )
}

function SlopspaceDetails({ id, info, work, onSignal }: { id: string; info?: SlopspaceInfo; work: WorkerSignalInfo[]; onSignal: (key: string) => void }) {
  const status = !info ? 'deleted' : info.in_use ? 'in use' : info.deployed ? 'deployed' : 'stored'
  return (
    <div className="wv-detail-body">
      <div className="wv-detail-kind" style={{ color: RED }}>slopspace</div>
      <Field label="id">{id}</Field>
      <Field label="status">{status}</Field>
      {info && (
        <>
          <Field label="agent type">{info.agent_type || '—'}</Field>
          <Field label="deploys">{info.iteration}</Field>
          <Field label="created">{fmtTime(info.created_at)}</Field>
          <Field label="repos">{info.repos?.length ? info.repos.join(', ') : 'none'}</Field>
          {info.path && <Field label="path">{info.path}</Field>}
          {info.deployed && info.deploy_path && <Field label="deployed at">{info.deploy_path}</Field>}
          {!!info.deployments?.length && (
            <>
              <div className="wv-field-label">deployments (last {info.deployments.length})</div>
              <div className="wv-work-list">
                {[...info.deployments].reverse().map(d => (
                  <div key={d.deployed_at} className="worker-signal-detail">
                    {d.agent_type} · {fmtTime(d.deployed_at)} – {d.returned_at ? fmtTime(d.returned_at) : 'now'}
                  </div>
                ))}
              </div>
            </>
          )}
        </>
      )}
      <div className="wv-field-label">work run in it ({work.length})</div>
      {work.length === 0
        ? <div className="worker-signal-detail">none among the reported submissions</div>
        : (
          <div className="wv-work-list">
            {work.map(s => (
              <button key={signalKey(s)} className="wv-link wv-work-item" onClick={() => onSignal(signalKey(s))}>
                {fmtTime(signalStart(s))} · {s.status} · {s.prompt.split('\n')[0].slice(0, 60)}
              </button>
            ))}
          </div>
        )}
    </div>
  )
}

const DECISION_LABEL: Record<string, string> = { accept: 'accepted onto the DTT canvas', reject: 'rejected', modify: 'modification requested', mixed: 'answered one at a time' }

// ReviewDetails is one deployment's DTT proposals and the human's answer.
function ReviewDetails({ mark: m, info, now, onSlopspace }: { mark: ReviewMark; info?: SlopspaceInfo; now: number; onSlopspace: (id: string) => void }) {
  const d = m.d
  const returned = d.returned_at ?? 0
  const pending = !d.decision
  const last = info?.deployments?.[info.deployments.length - 1]
  const listed = pending && last?.deployed_at === d.deployed_at ? info?.pending_proposals ?? [] : []
  return (
    <div className="wv-detail-body">
      <div className="wv-detail-kind" style={{ color: GREEN }}>DTT proposals for the human</div>
      <Field label="slopspace">
        <button className="wv-link" onClick={() => onSlopspace(m.id)} title={m.id}>{m.id.slice(0, 8)}</button>
      </Field>
      <Field label="proposals">{d.proposals ?? 0}</Field>
      <Field label="from">{d.agent_type}, deployed {fmtTime(d.deployed_at)}</Field>
      <Field label="returned">{fmtTime(returned)}</Field>
      <Field label="decision">{pending ? 'awaiting review — it can\'t be deployed again until answered' : `${DECISION_MARK[d.decision!] ?? ''} ${DECISION_LABEL[d.decision!] ?? d.decision}`}</Field>
      {!!(d.accepted || d.rejected) && <Field label="one at a time">✓ {d.accepted ?? 0} accepted · ✗ {d.rejected ?? 0} rejected</Field>}
      {!pending && <Field label="decided">{fmtTime(d.decided_at)}</Field>}
      <Field label={pending ? 'waiting' : 'waited'}>{fmtDuration((d.decided_at || now) - returned)}</Field>
      {d.note && (
        <>
          <div className="wv-field-label">note</div>
          <pre className="worker-signal-text">{d.note}</pre>
        </>
      )}
      {listed.length > 0 && (
        <>
          <div className="wv-field-label">proposals (writespaces/dtt-requests)</div>
          <div className="wv-work-list">
            {listed.map(p => <div key={p} className="worker-signal-detail">{p}</div>)}
            {!!info?.proposals_omitted && <div className="worker-signal-detail">…and {info.proposals_omitted} more</div>}
          </div>
        </>
      )}
      {pending && <div className="worker-signal-detail">answer them on the slopspace sub-tab</div>}
    </div>
  )
}

// HumanDetails summarises the human track: what awaits review, and the
// answers given so far.
function HumanDetails({ marks, now, onReview }: { marks: ReviewMark[]; now: number; onReview: (m: ReviewMark) => void }) {
  const waiting = marks.filter(m => !m.d.decision)
  const answered = marks.filter(m => m.d.decision).sort((a, b) => (b.d.decided_at ?? 0) - (a.d.decided_at ?? 0))
  return (
    <div className="wv-detail-body">
      <div className="wv-detail-kind" style={{ color: GREEN }}>the human</div>
      <Field label="awaiting review">{waiting.length ? `${waiting.length} slopspace${waiting.length === 1 ? '' : 's'}` : 'nothing'}</Field>
      <Field label="answered">{answered.length}</Field>
      {waiting.length > 0 && (
        <>
          <div className="wv-field-label">waiting</div>
          <div className="wv-work-list">
            {waiting.map(m => (
              <button key={`${m.id}-${m.at}`} className="wv-link wv-work-item" onClick={() => onReview(m)}>
                {m.id.slice(0, 8)} · {m.d.proposals} proposal{m.d.proposals === 1 ? '' : 's'} · {fmtDuration(now - (m.d.returned_at ?? now))}
              </button>
            ))}
          </div>
        </>
      )}
      {answered.length > 0 && (
        <>
          <div className="wv-field-label">answered (newest first)</div>
          <div className="wv-work-list">
            {answered.map(m => (
              <button key={`${m.id}-${m.at}`} className="wv-link wv-work-item" onClick={() => onReview(m)}>
                {fmtTime(m.d.decided_at)} · {DECISION_MARK[m.d.decision!]} {m.d.decision} · {m.id.slice(0, 8)}
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  )
}

// AgentTypeDetails summarises a lane's agent type from what the worker
// reports: whether this worker runs it, the slopspace deployed to it, its
// reported submissions and the agents that ran them.
function AgentTypeDetails({ type, state, work, slopspaces, now, onSignal, onSlopspace }: {
  type: string
  state: WorkerStateMsg
  work: WorkerSignalInfo[]
  slopspaces: SlopspaceInfo[]
  now: number
  onSignal: (key: string) => void
  onSlopspace: (id: string) => void
}) {
  const ours = type === state.agent_type
  const deployed = slopspaces.find(s => s.deployed && s.agent_type === type)
  const deployedTo = slopspaces.filter(s => s.deployments?.some(d => d.agent_type === type))
  const deployments = deployedTo.reduce((n, s) => n + (s.deployments?.filter(d => d.agent_type === type).length ?? 0), 0)
  const byStatus = new Map<string, number>()
  work.forEach(s => byStatus.set(s.status, (byStatus.get(s.status) ?? 0) + 1))
  const ran = work.filter(s => s.started_at)
  const busy = ran.reduce((ms, s) => ms + Math.max(0, signalEnd(s, now) - signalStart(s)), 0)
  const agents = [...new Set(work.map(s => [s.agent, s.model && `(${s.model})`].filter(Boolean).join(' ')).filter(Boolean))]
  const newest = [...work].sort((a, b) => b.created_at - a.created_at)
  return (
    <div className="wv-detail-body">
      <div className="wv-detail-kind" style={{ color: BLUE }}>agent type</div>
      <Field label="name">{type}</Field>
      <Field label="this worker">{ours ? `runs it (${state.worker_id}, ${state.busy ? 'busy' : 'idle'})` : `runs ${state.agent_type || '—'}, not this`}</Field>
      <Field label="deployed now">
        {deployed
          ? <button className="wv-link" onClick={() => onSlopspace(deployed.id)} title={deployed.id}>{deployed.id.slice(0, 8)}</button>
          : 'no slopspace'}
      </Field>
      <Field label="deployments">{deployments} recorded, across {deployedTo.length} slopspace{deployedTo.length === 1 ? '' : 's'}</Field>
      <Field label="submissions">{work.length ? [...byStatus].map(([st, n]) => `${n} ${st}`).join(', ') : 'none reported'}</Field>
      {ran.length > 0 && <Field label="time working">{fmtDuration(busy)} over {ran.length} run{ran.length === 1 ? '' : 's'}</Field>}
      {agents.length > 0 && <Field label="agents">{agents.join(', ')}</Field>}
      {newest.length > 0 && (
        <>
          <div className="wv-field-label">work (newest first)</div>
          <div className="wv-work-list">
            {newest.map(s => (
              <button key={signalKey(s)} className="wv-link wv-work-item" onClick={() => onSignal(signalKey(s))}>
                {fmtTime(s.created_at)} · {s.status} · {s.prompt.split('\n')[0].slice(0, 60)}
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  )
}
