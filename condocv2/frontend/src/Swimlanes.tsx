import { useState } from 'react'
import type { ReactNode } from 'react'
import type { Interaction, Timeline, TimelineEvent } from './types'

// Swimlanes is the visual tab: the condoc's commits drawn as the swimlane
// dialog of the condocv2 design sketch (sloppo
// condocs/condoccerV2Impls/Step1Prompt.md, Resource 1). One lane per repo --
// the pointer repo, the condoc repo, then each target -- and rows in time,
// newest at the top as in the sketch (switchable):
//
//   propose        the pointer's first commit
//   begin          a double circle in every lane, fanned out from the pointer
//   per interaction, three rows:
//     lock -> prompt          the prompt arrives from the left into the lock,
//                             which locks the condoc for its prompt commit
//     response, commits       the response commit, and the commit it made in
//                             each target
//     unlock -> record        the record commit maps the target commits
//                             (dotted) and the unlock releases the condoc
//
// Blue is structure (propose, begin, response, target commits); green is the
// interaction itself (lock, prompt, record, unlock). A dashed grey node is
// where the interaction in flight goes next.

const BLUE = '#4da6ff'
const GREEN = '#4ec94e'
const PENDING = '#777'
const LANE_LINE = '#2f4f6f'

const GUTTER = 210 // the prompt column on the left
const COL_W = 150
const ROW_H = 54
const HEADER_H = 44
const PAD_BOTTOM = 24
const R = 9

// rowOf places an event: propose, begin, then three rows per interaction.
function rowOf(e: TimelineEvent): number {
  if (e.kind === 'propose') return 0
  if (e.kind === 'begin') return 1
  const base = 2 + ((e.interaction ?? 1) - 1) * 3
  switch (e.kind) {
    case 'lock':
    case 'prompt':
      return base
    case 'response':
    case 'commit':
      return base + 1
    default: // record, unlock
      return base + 2
  }
}

function isGreen(e: TimelineEvent): boolean {
  return e.kind === 'lock' || e.kind === 'prompt' || e.kind === 'record' || e.kind === 'unlock'
}

function shortRepo(repo: string): string {
  return repo.split('/').pop() ?? repo
}

function snippet(text: string, n: number): string {
  const one = text.replace(/\s+/g, ' ').trim()
  return one.length > n ? `${one.slice(0, n - 1)}…` : one
}

function eventTitle(e: TimelineEvent): string {
  const parts = [`${e.kind}${e.interaction ? ` ${e.interaction}` : ''}`]
  if (e.pending) parts.push(`(pending${e.note ? `: ${e.note}` : ''})`)
  else if (e.note) parts.push(`(${e.note})`)
  if (e.subject) parts.push(e.subject)
  if (e.sha) parts.push(e.sha.slice(0, 12))
  if (e.time) parts.push(new Date(e.time * 1000).toLocaleString())
  return parts.join('\n')
}

function Node({ e, x, y }: { e: TimelineEvent; x: number; y: number }) {
  const color = e.pending ? PENDING : isGreen(e) ? GREEN : BLUE
  const dash = e.pending ? '3 3' : undefined
  const small = e.kind === 'lock' || e.kind === 'unlock'
  let shape: ReactNode
  if (e.kind === 'begin') {
    shape = (
      <>
        <circle cx={x} cy={y} r={R + 3} fill="none" stroke={color} strokeWidth={1.5} strokeDasharray={dash} />
        <circle cx={x} cy={y} r={R - 2} fill="none" stroke={color} strokeWidth={1.5} strokeDasharray={dash} />
      </>
    )
  } else if (e.kind === 'record') {
    shape = (
      <>
        <circle cx={x} cy={y} r={R} fill="#1a1a1a" stroke={color} strokeWidth={2} strokeDasharray={dash} />
        <circle cx={x} cy={y} r={2.5} fill={color} />
      </>
    )
  } else {
    shape = (
      <circle
        cx={x}
        cy={y}
        r={small ? R - 4 : R}
        fill={e.pending ? 'none' : '#1a1a1a'}
        stroke={color}
        strokeWidth={2}
        strokeDasharray={dash}
      />
    )
  }
  const label = e.kind === 'lock' || e.kind === 'unlock' ? e.kind : e.pending ? e.note ?? '' : ''
  const body = (
    <g className={`swim-node${e.pending ? ' swim-node-pending' : ''}`}>
      <title>{eventTitle(e)}</title>
      {shape}
      {label && (
        <text x={x + R + 4} y={y + 4} className="swim-node-label" fill={color}>
          {label}
        </text>
      )}
    </g>
  )
  return e.url ? (
    <a href={e.url} target="_blank" rel="noreferrer">
      {body}
    </a>
  ) : (
    body
  )
}

function Arrow({ x1, y1, x2, y2, color, dashed }: { x1: number; y1: number; x2: number; y2: number; color: string; dashed?: boolean }) {
  return (
    <line
      x1={x1}
      y1={y1}
      x2={x2}
      y2={y2}
      stroke={color}
      strokeWidth={1.5}
      strokeDasharray={dashed ? '2 4' : undefined}
      markerEnd={`url(#swim-arrow-${color === GREEN ? 'green' : color === PENDING ? 'grey' : 'blue'})`}
    />
  )
}

export function Swimlanes({ timeline, interactions, pendingPrompt, lockInteraction }: {
  timeline: Timeline
  interactions: Interaction[]
  pendingPrompt?: string
  lockInteraction?: number
}) {
  const [newestOnTop, setNewestOnTop] = useState(true)
  const lanes = timeline.lanes ?? []
  const events = timeline.events ?? []

  const laneIndex = new Map(lanes.map((l, i) => [l.id, i]))
  let maxRow = 1
  for (const e of events) maxRow = Math.max(maxRow, rowOf(e))
  // Prompts with no lock yet still get their row (the pending prompt).
  for (const it of interactions) maxRow = Math.max(maxRow, rowOf({ lane: 'pointer', kind: 'lock', interaction: it.n }))
  if (lockInteraction) maxRow = Math.max(maxRow, rowOf({ lane: 'pointer', kind: 'lock', interaction: lockInteraction }) + 2)

  const width = GUTTER + lanes.length * COL_W + 40
  const height = HEADER_H + (maxRow + 1) * ROW_H + PAD_BOTTOM
  const colX = (lane: string) => GUTTER + (laneIndex.get(lane) ?? 0) * COL_W + COL_W / 2
  const rowY = (row: number) =>
    HEADER_H + ROW_H / 2 + (newestOnTop ? maxRow - row : row) * ROW_H

  // Several events can land in one cell (a stage retried); fan them out.
  const cellCount = new Map<string, number>()
  const placed = events.map((e) => {
    const key = `${e.lane}|${rowOf(e)}`
    const k = cellCount.get(key) ?? 0
    cellCount.set(key, k + 1)
    return { e, x: colX(e.lane) + k * (2 * R + 6), y: rowY(rowOf(e)) }
  })
  const find = (lane: string, kind: string, n?: number) =>
    placed.find((p) => p.e.lane === lane && p.e.kind === kind && (n === undefined || p.e.interaction === n))

  // Connectors.
  const connectors: ReactNode[] = []
  const pointerBegin = find('pointer', 'begin')
  if (pointerBegin) {
    for (const p of placed) {
      if (p.e.kind === 'begin' && p.e.lane !== 'pointer') {
        connectors.push(<Arrow key={`begin-${p.e.lane}`} x1={pointerBegin.x + R + 3} y1={pointerBegin.y} x2={p.x - R - 5} y2={p.y} color={BLUE} />)
      }
    }
  }
  const ns = new Set<number>()
  for (const p of placed) if (p.e.interaction) ns.add(p.e.interaction)
  for (const n of ns) {
    const lock = find('pointer', 'lock', n)
    const prompt = find('condoc', 'prompt', n)
    if (lock && prompt) {
      connectors.push(<Arrow key={`lock-${n}`} x1={lock.x + R} y1={lock.y} x2={prompt.x - R - 3} y2={prompt.y} color={prompt.e.pending ? PENDING : GREEN} />)
    }
    const response = find('condoc', 'response', n)
    const record = find('condoc', 'record', n)
    for (const c of placed.filter((p) => p.e.kind === 'commit' && p.e.interaction === n)) {
      if (response) {
        connectors.push(<Arrow key={`resp-${n}-${c.e.lane}-${c.x}`} x1={response.x + R} y1={response.y} x2={c.x - R - 3} y2={c.y} color={c.e.pending ? PENDING : BLUE} />)
      }
      if (record && !c.e.pending) {
        connectors.push(<Arrow key={`rec-${n}-${c.e.lane}-${c.x}`} x1={record.x + R} y1={record.y} x2={c.x - R - 3} y2={c.y} color={GREEN} dashed />)
      }
    }
    const unlock = find('pointer', 'unlock', n)
    if (unlock && record) {
      connectors.push(<Arrow key={`unlock-${n}`} x1={unlock.x + R} y1={unlock.y} x2={record.x - R - 3} y2={record.y} color={unlock.e.pending ? PENDING : GREEN} />)
    }
  }

  // The prompt gutter: each interaction's prompt arrives from the left.
  const prompts: { n: number; text: string; pending: boolean }[] = interactions.map((it) => ({ n: it.n, text: it.prompt, pending: false }))
  if (pendingPrompt && lockInteraction && !interactions.some((it) => it.n === lockInteraction)) {
    prompts.push({ n: lockInteraction, text: pendingPrompt, pending: true })
  }
  const pointerX = colX('pointer')

  // Each lane's line runs from its first node to its last.
  const laneLines = lanes.map((l) => {
    const ys = placed.filter((p) => p.e.lane === l.id).map((p) => p.y)
    if (ys.length < 2) return null
    return <line key={`lane-${l.id}`} x1={colX(l.id)} y1={Math.min(...ys)} x2={colX(l.id)} y2={Math.max(...ys)} stroke={LANE_LINE} strokeWidth={2} />
  })

  return (
    <div className="swim">
      <div className="swim-toolbar">
        <label className="list-filter swim-toggle">
          <input type="checkbox" checked={newestOnTop} onChange={(e) => setNewestOnTop(e.target.checked)} />
          Newest on top
        </label>
        <span className="swim-legend">
          <span className="swim-key" style={{ color: BLUE }}>● structure</span>
          <span className="swim-key" style={{ color: GREEN }}>● interaction</span>
          <span className="swim-key" style={{ color: PENDING }}>◌ pending</span>
          <span className="swim-key">click a node for its commit</span>
        </span>
      </div>
      {(timeline.errors ?? []).map((err) => (
        <div key={err} className="pointer-message">{err}</div>
      ))}
      <div className="swim-scroll">
        <svg className="swim-svg" width={width} height={height} viewBox={`0 0 ${width} ${height}`}>
          <defs>
            {([['blue', BLUE], ['green', GREEN], ['grey', PENDING]] as const).map(([id, color]) => (
              <marker key={id} id={`swim-arrow-${id}`} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
                <path d="M 0 0 L 10 5 L 0 10 z" fill={color} />
              </marker>
            ))}
          </defs>

          {/* Lane bands and headings. */}
          {lanes.map((l, i) => (
            <g key={l.id}>
              <rect x={GUTTER + i * COL_W} y={0} width={COL_W} height={height} className={i % 2 ? 'swim-band swim-band-odd' : 'swim-band'} />
              <text x={colX(l.id)} y={18} textAnchor="middle" className="swim-lane-title">
                {l.role === 'pointer' ? 'Pointer' : l.role === 'condoc' ? 'Condoc' : shortRepo(l.repo)}
              </text>
              <text x={colX(l.id)} y={33} textAnchor="middle" className="swim-lane-repo">
                <title>{l.repo}</title>
                {l.role === 'target' ? 'target' : shortRepo(l.repo)}
              </text>
            </g>
          ))}
          <text x={12} y={18} className="swim-lane-title">Prompts</text>

          {laneLines}

          {prompts.map((p) => {
            const y = rowY(rowOf({ lane: 'pointer', kind: 'lock', interaction: p.n }))
            const color = p.pending ? PENDING : GREEN
            return (
              <g key={`prompt-${p.n}`}>
                <title>{`Prompt ${p.n}\n\n${p.text}`}</title>
                <text x={12} y={y - 4} className="swim-prompt-n" fill={color}>prompt {p.n}</text>
                <text x={12} y={y + 11} className="swim-prompt-text">{snippet(p.text, 26)}</text>
                <Arrow x1={GUTTER - 12} y1={y} x2={pointerX - R - 3} y2={y} color={color} />
              </g>
            )
          })}

          {connectors}

          {placed.map((p, i) => (
            <Node key={`${p.e.lane}-${p.e.kind}-${p.e.interaction ?? 0}-${p.e.sha ?? i}-${i}`} e={p.e} x={p.x} y={p.y} />
          ))}
        </svg>
      </div>
      <ol className="swim-sequence">
        <li><b>propose</b> — the proposal's pointer commit</li>
        <li><b>begin</b> — the slopspace, a branch in every repo, the condoc's first commit</li>
        <li><b>lock</b> — the pointer locks the condoc for one interaction</li>
        <li><b>prompt</b> — the prompt commit (auto-submits once if asked)</li>
        <li><b>response</b> — the agent's reply, committed to the condoc, with a commit in each target it changed</li>
        <li><b>record</b> — those target commits mapped into the condoc</li>
        <li><b>unlock</b> — the pointer releases the condoc</li>
      </ol>
    </div>
  )
}
