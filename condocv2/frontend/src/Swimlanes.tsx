import type { ReactNode } from 'react'
import type { Interaction, LocalOperation, ResourceEntry, StepEntry, Timeline, TimelineEvent, TimelineKind } from './types'
import { isLocalURL, scopeTitle, stepParent } from './types'
import { imageOf, proposalOf } from './DTTViewer'
import { RepoLink } from './RepoLink'
import type { OnOpenDTT } from './DTTViewer'
import { diffRows, diffRowTitle, externalRow } from './DiffViewer'
import type { OnOpenDiff } from './DiffViewer'

// Swimlanes is the visual tab: the condoc's commits drawn as the swimlane
// dialog of the condocv2 design sketches (sloppo
// condocs/condoccerV2Impls/Step1Prompt.md, Resource 1;
// condocs/dTT-IntegrationImpls/Step1Prompt.md, Resource 2 "Condocv2"; and
// condocs/privateRepoOpsImpls/Step4Prompt.md, Resource 2 "How DTT
// Interactions SHOULD Look"). One lane per repo -- the pointer repo, the
// condoc repo, then each target -- and rows in time, flowing from the top
// down, split into numbered sections by dividers:
//
//   start          propose (the pointer's first commit), then begin: a
//                  double circle in every lane, fanned out from the pointer
//   ─ N ─          each interaction, three rows:
//     lock -> prompt          the prompt arrives from the left into the lock,
//                             which locks the condoc for its prompt commit
//     response, commits       the response commit (a wrench when it proposed
//                             DTT tool calls), and its commit in each target
//     unlock -> record        the record commit maps the target commits
//                             (dotted) and the unlock releases the condoc --
//                             a "?" when its proposals still await answers
//   - N.k -        each of its sub-iterations, one per proposal, two rows:
//     lock -> dtt             the proposal commit: an eye (read) or wrench
//                             (assert) dashed until it has run, ✗ rejected
//     unlock -> result        the image commit, ✓ (✗ when dtt failed)
//   ─ ─            completion: complete ◆, drafted / redraft (a dashed PR),
//                  prs (PR), merged (⑂) -- the pointer's, then each
//                  repo's merge commit
//
// Each read-only repo (condocv2/readonly.go) has a column after the targets:
// a blue dotted circle for every new commit it was pulled at -- at begin, on
// an interaction's response row for its deploy, record row for its return.
//
// An external change (condocv2/external.go) -- commits pushed to a condoc or
// target branch from outside condocv2, pulled in between interactions by
// Refresh remotes or as a prompt took the lock -- is a section of its own
// after the interaction it followed, labelled "external" instead of a number:
// a blue dotted circle in each lane that moved, recorded by the pointer's
// external commit.
//
// A step (condocv2/steps.go) is a scope of its own, and the visual draws one
// scope at a time: only the events made in it. Entering a step is a section
// of its own in the scope it was entered from -- a dot in the pointer lane
// beside a green circle with a right-arrow in the condoc lane; click the arrow
// to descend into the step. Completing it is another, a green left-arrow in
// the condoc lane; in the step's own view, which starts with its entry and
// ends with its completion, click that to come back up. Interaction numbers
// run on through every scope, so a scope's numbers needn't be consecutive.
//
// A resource (condocv2/resources.go) -- files added between interactions,
// uploaded, from the host's files or entered as text -- is a section of its
// own too, "resource R", after the interaction it followed: a dot in the
// pointer lane beside a green circle with a file in the condoc lane.
//
// Blue is structure (propose, begin, response, target commits); green is the
// interaction itself (lock, prompt, record, unlock, and the sub-iterations);
// amber is the human held by the proposals. A dashed grey node is where the
// condoc goes next. Condocs reviewed before the sub-iterations have a hold,
// review and release instead, two more rows of their interaction.
//
// The column on the right is the local operations behind the condoc -- each
// agent invocation and DTT execution, read from this host's records (never
// from the condoc's model, see condocv2/ops.go) -- with red arrows out to it
// and back, level, just below the node it followed: an agent's prompt, a DTT
// run's proposal. An aside (condocv2/aside.go), a read-only question kept
// off the condoc, is a red bubble there saying "aside", hung off the last
// node of its own scope before it ran. Clicking an agent or an aside opens its work submission in the
// embedding page's worker tab (openWork); clicking a DTT run's box, a
// sub-iteration's proposal or ✓ node, an answer or the wait opens the DTT
// viewer on the tool call or its image (DTTViewer.tsx).
//
// A "+/-" in the left margin of each row that moved repos -- an
// interaction's response and commits row, the agent's work, and each
// external change's -- opens the diff viewer on it (DiffViewer.tsx), repo by
// repo.
//
// condocv2/record.go draws the same picture, without the operations, into
// the timeline record's HTML page; keep the two in step.

const BLUE = '#4da6ff'
const GREEN = '#4ec94e'
const AMBER = '#e0b050'
const RED = '#e05a5a'
const PURPLE = '#b48ce0'
const PENDING = '#777'
const LANE_LINE = '#2f4f6f'

const GUTTER = 210 // the human's column on the left: prompts and decisions
const COL_W = 150
const ROW_H = 54
const HEADER_H = 44
const PAD_BOTTOM = 24
const R = 9
const OPS_GAP = 36
const OPS_W = 210
const OP_H = 34
const OP_DROP = R + 2 // an operation box's top below its anchor node's centre
const OP_ARROW_IN = 9 // its red arrows' distance inside the box's top and bottom

const COMPLETION_ROW: Partial<Record<TimelineKind, number>> = { complete: 0, redraft: 1, drafted: 1, prs: 2, merged: 3 }

const isCompletion = (k: TimelineKind) => COMPLETION_ROW[k] !== undefined
const isLegacyReview = (k: TimelineKind) => k === 'hold' || k === 'review' || k === 'release'
const isStepKind = (k: TimelineKind) => k === 'step' || k === 'step-complete'

// stepKey is a step event's section; stepLabel names it.
const stepKey = (e: { kind: TimelineKind; path?: string }) => `${e.kind} ${e.path ?? ''}`
const stepLabel = (e: { kind: TimelineKind; path?: string }) => `step ${e.path ?? ''}${e.kind === 'step-complete' ? ' done' : ''}`

// resourceKey is a resource's section; resourceLabel names it.
const resourceKey = (seq?: number) => `resource ${seq ?? 0}`
const resourceLabel = resourceKey

// A divider above a section: solid for start, each interaction and the
// completion, dashed for a sub-iteration.
type Section = { label: string; row: number; solid: boolean }

// A section between interactions: an external change, a step entered or
// left, or a resource added, after interaction n.
type Between = { n: number; key: string; label: string; time: number; seq: number }

// layoutRows places one scope's events in rows: start (propose, begin -- the
// main scope's only), then, in the order they happened, each interaction
// (three rows, five when it was reviewed the older way) with its
// sub-iterations (two rows each) and a row for the human held by its
// proposals, and a row for each external change found, each step entered or
// left and each resource added between interactions; then completion.
function layoutRows(events: TimelineEvent[], interactions: Interaction[], scope: string, lockInteraction?: number, waitingOn?: number) {
  const ns = new Set<number>()
  if (lockInteraction) ns.add(lockInteraction)
  if (waitingOn) ns.add(waitingOn)
  const legacy = new Set<number>()
  const subs = new Map<number, number>()
  const addSub = (n: number, k: number) => subs.set(n, Math.max(subs.get(n) ?? 0, k))
  const between = new Map<string, Between>()
  const addBetween = (b: Between) => {
    const had = between.get(b.key)
    if (!had) between.set(b.key, b)
    else if (b.time && (!had.time || b.time < had.time)) had.time = b.time
  }
  for (const e of events) {
    const n = e.interaction ?? 0
    if (e.kind === 'external') {
      addBetween({ n, key: `external ${n}.${e.seq ?? 0}`, label: 'external', time: e.time ?? 0, seq: e.seq ?? 0 })
      continue
    }
    if (isStepKind(e.kind)) {
      addBetween({ n, key: stepKey(e), label: stepLabel(e), time: e.time ?? 0, seq: 0 })
      continue
    }
    if (e.kind === 'resource') {
      addBetween({ n, key: resourceKey(e.seq), label: resourceLabel(e.seq), time: e.time ?? 0, seq: e.seq ?? 0 })
      continue
    }
    // A configuration's resolve follows interaction n, which may be another
    // scope's: it doesn't make n one of this scope's.
    if (!n || (e.kind === 'resolve' && e.note === 'configure')) continue
    ns.add(n)
    if (isLegacyReview(e.kind)) legacy.add(n)
    if (e.sub) addSub(n, e.sub)
  }
  for (const it of interactions) {
    ns.add(it.n)
    for (const t of it.tools ?? []) addSub(it.n, t.k)
  }
  // Each interaction before whatever followed it, which goes in the order it
  // happened.
  const blocks: ({ n: number; b?: Between })[] = [...ns].map((n) => ({ n }))
  for (const b of between.values()) blocks.push({ n: b.n, b })
  blocks.sort((x, y) =>
    x.n !== y.n ? x.n - y.n
      : !x.b || !y.b ? (x.b ? 1 : 0) - (y.b ? 1 : 0)
        : x.b.time !== y.b.time ? x.b.time - y.b.time
          : x.b.seq - y.b.seq)

  const base = new Map<number, number>()
  const sections: Section[] = scope ? [] : [{ label: 'start', row: 0, solid: true }]
  const firstRow = scope ? 0 : 1 // begin's, or the step's entry
  let waitRow: number | undefined
  let row = scope ? 0 : 2
  const betweenRow = new Map<string, number>()
  for (const { n, b } of blocks) {
    if (b) {
      betweenRow.set(b.key, row)
      sections.push({ label: b.label, row, solid: true })
      row += 1
      continue
    }
    base.set(n, row)
    sections.push({ label: `${n}`, row, solid: true })
    row += legacy.has(n) ? 5 : 3
    const k = subs.get(n) ?? 0
    for (let s = 1; s <= k; s++) sections.push({ label: `${n}.${s}`, row: row + (s - 1) * 2, solid: false })
    row += 2 * k
    if (waitingOn === n) {
      waitRow = row
      row += 1
    }
  }
  const completionBase = row
  if (events.some((e) => isCompletion(e.kind))) sections.push({ label: '', row: completionBase, solid: true })
  const rowOf = (e: { kind: TimelineKind; interaction?: number; sub?: number; seq?: number; note?: string; path?: string }): number => {
    if (e.kind === 'external') return betweenRow.get(`external ${e.interaction ?? 0}.${e.seq ?? 0}`) ?? firstRow
    if (isStepKind(e.kind)) return betweenRow.get(stepKey(e)) ?? firstRow
    if (e.kind === 'resource') return betweenRow.get(resourceKey(e.seq)) ?? firstRow
    if (e.kind === 'propose') return 0
    if (e.kind === 'begin') return 1
    if (e.kind === 'resolve' && !e.interaction) return firstRow
    const c = COMPLETION_ROW[e.kind]
    if (c !== undefined) return completionBase + c
    const n = e.interaction ?? 1
    const b = base.get(n)
    if (b === undefined) return firstRow // a configuration after another scope's interaction
    if (e.sub) {
      const sb = b + (legacy.has(n) ? 5 : 3) + (e.sub - 1) * 2
      return e.kind === 'lock' || e.kind === 'dtt' ? sb : sb + 1
    }
    switch (e.kind) {
      case 'lock':
      case 'prompt':
        return b
      case 'response':
      case 'commit':
        return b + 1
      case 'resolve': // a deploy's commit is what the response saw; a return's (or a configure's) comes after
        return e.note === 'return' || e.note === 'configure' ? b + 2 : b + 1
      case 'hold':
        return b + 3
      case 'review':
      case 'release':
        return b + 4
      default: // record, unlock
        return b + 2
    }
  }
  let maxRow = Math.max(1, row - 1)
  for (const e of events) maxRow = Math.max(maxRow, rowOf(e))
  return { rowOf, maxRow, sections, waitRow, betweenRow }
}

function isGreen(e: TimelineEvent): boolean {
  return ['lock', 'prompt', 'record', 'unlock', 'dtt', 'result', 'hold', 'review', 'release', 'step', 'step-complete', 'resource', 'complete', 'prs'].includes(e.kind)
}

function shortRepo(repo: string): string {
  return repo.split('/').pop() ?? repo
}

function snippet(text: string, n: number): string {
  const one = text.replace(/\s+/g, ' ').trim()
  return one.length > n ? `${one.slice(0, n - 1)}…` : one
}

const label = (n?: number, k?: number) => (n ? (k ? `${n}.${k}` : `${n}`) : '')

function eventTitle(e: TimelineEvent): string {
  const parts = [
    e.kind === 'external' ? `external ${e.interaction ?? 0}.${e.seq ?? 0}`
      : isStepKind(e.kind) ? `${e.kind} ${e.path ?? ''} (after interaction ${e.interaction ?? 0})`
        : e.kind === 'resource' ? `${resourceLabel(e.seq)} (after interaction ${e.interaction ?? 0})`
        : `${e.kind}${e.interaction ? ` ${label(e.interaction, e.sub)}` : ''}`,
  ]
  if (e.pending) parts.push(`(pending${e.note ? `: ${e.note}` : ''})`)
  else if (e.note) parts.push(`(${e.note})`)
  if (e.subject) parts.push(e.subject)
  if (e.sha) parts.push(e.sha.slice(0, 12))
  if (e.time) parts.push(new Date(e.time * 1000).toLocaleString())
  return parts.join('\n')
}

const DECISION_GLYPH: Record<string, string> = { accept: '✓', reject: '✗', modify: '↻', mixed: '±' }
const DECISION_WORD: Record<string, string> = { accept: 'accepted', reject: 'rejected', modify: 'changes requested', mixed: 'some accepted, some rejected' }

const SMALL = new Set<TimelineKind>(['lock', 'unlock', 'hold', 'release'])

function Glyph({ x, y, text, color }: { x: number; y: number; text: string; color: string }) {
  return (
    <text x={x} y={y + 4} textAnchor="middle" className="swim-glyph" fill={color}>
      {text}
    </text>
  )
}

// wrenchGlyph is a DTT change: a proposal that asserts, or a response that
// proposed tool calls.
function wrenchGlyph(x: number, y: number, color: string) {
  return (
    <>
      <path d={`M ${x - 4.5} ${y + 4.5} L ${x + 1} ${y - 1}`} stroke={color} strokeWidth={2.2} strokeLinecap="round" />
      <path d={`M ${x + 0.5} ${y - 4.5} A 3 3 0 1 0 ${x + 4.5} ${y - 0.5}`} fill="none" stroke={color} strokeWidth={1.8} />
    </>
  )
}

// eyeGlyph is a DTT read.
function eyeGlyph(x: number, y: number, color: string) {
  return (
    <>
      <path d={`M ${x - 6} ${y} Q ${x} ${y - 6} ${x + 6} ${y} Q ${x} ${y + 6} ${x - 6} ${y} Z`} fill="none" stroke={color} strokeWidth={1.5} />
      <circle cx={x} cy={y} r={1.8} fill={color} />
    </>
  )
}

// prBox is the PR icon: a rounded box reading "PR", dashed while a draft.
function prBox(x: number, y: number, color: string, draft: boolean) {
  return (
    <>
      <rect x={x - 12} y={y - 8} width={24} height={16} rx={4} fill="#1a1a1a" stroke={color} strokeWidth={2} strokeDasharray={draft ? '3 2' : undefined} />
      <text x={x} y={y + 4} textAnchor="middle" className="swim-pr-text" fill={color}>PR</text>
    </>
  )
}

// fileGlyph is a resource's file: a page with its corner folded.
function fileGlyph(x: number, y: number, color: string) {
  return (
    <path
      d={`M ${x - 3.5} ${y - 5} L ${x + 1.5} ${y - 5} L ${x + 3.5} ${y - 3} L ${x + 3.5} ${y + 5} L ${x - 3.5} ${y + 5} Z M ${x + 1.5} ${y - 5} L ${x + 1.5} ${y - 3} L ${x + 3.5} ${y - 3}`}
      fill="none"
      stroke={color}
      strokeWidth={1.4}
      strokeLinejoin="round"
    />
  )
}

// mergeGlyph is the merge icon: two branches joining into one.
function mergeGlyph(x: number, y: number, color: string) {
  return (
    <>
      <circle cx={x} cy={y} r={R + 2} fill="#1a1a1a" stroke={color} strokeWidth={2} />
      <path d={`M ${x - 4} ${y - 5} L ${x} ${y} L ${x + 4} ${y - 5} M ${x} ${y} L ${x} ${y + 6}`} fill="none" stroke={color} strokeWidth={1.8} />
      <circle cx={x - 4} cy={y - 5} r={1.6} fill={color} />
      <circle cx={x + 4} cy={y - 5} r={1.6} fill={color} />
      <circle cx={x} cy={y + 6} r={1.6} fill={color} />
    </>
  )
}

// Node draws one event. asked is a record's "?": its interaction proposed
// DTT tool calls -- amber while the human is held by them. onOpen, when set,
// is what a click does instead of opening the commit (a proposal or image
// node opens the DTT viewer, which links the commit; a step's arrow goes
// into the step, or back out of it), and hint says so.
function Node({ e, x, y, asked, onOpen, hint }: { e: TimelineEvent; x: number; y: number; asked?: 'waiting' | 'answered'; onOpen?: () => void; hint?: string }) {
  const color = e.pending ? PENDING : e.kind === 'merged' ? PURPLE : asked === 'waiting' ? AMBER : isGreen(e) ? GREEN : BLUE
  const dash = e.pending ? '3 3' : undefined
  const circle = (r: number, fill: string, dashArray = dash) => (
    <circle cx={x} cy={y} r={r} fill={fill} stroke={color} strokeWidth={2} strokeDasharray={dashArray} />
  )
  let shape: ReactNode
  if (e.kind === 'resolve' || e.kind === 'external') {
    // A read-only repo pulled at a new commit, or a branch moved from outside
    // condocv2: a blue dotted circle.
    shape = circle(R, '#1a1a1a', '2 3')
  } else if (e.kind === 'begin') {
    shape = (
      <>
        <circle cx={x} cy={y} r={R + 3} fill="none" stroke={color} strokeWidth={1.5} strokeDasharray={dash} />
        <circle cx={x} cy={y} r={R - 2} fill="none" stroke={color} strokeWidth={1.5} strokeDasharray={dash} />
      </>
    )
  } else if (e.kind === 'record' && asked && !e.pending) {
    shape = (
      <>
        {circle(R + 1, '#1a1a1a')}
        <Glyph x={x} y={y} text="?" color={color} />
      </>
    )
  } else if (e.kind === 'record') {
    shape = (
      <>
        {circle(R, '#1a1a1a')}
        <circle cx={x} cy={y} r={2.5} fill={color} />
      </>
    )
  } else if (e.kind === 'response' && e.note === 'dtt' && !e.pending) {
    shape = (
      <>
        {circle(R + 1, '#1a1a1a')}
        {wrenchGlyph(x, y, color)}
      </>
    )
  } else if (e.kind === 'dtt' && !e.pending) {
    // The proposal, dashed: what it asks dtt to do.
    shape = (
      <>
        {circle(R + 2, '#1a1a1a', '3 2')}
        {e.note === 'read' || e.note === 'preread' ? eyeGlyph(x, y, color) : e.note === 'reject' ? <Glyph x={x} y={y} text="✗" color={color} /> : e.note === 'delete' ? <Glyph x={x} y={y} text="⊘" color={color} /> : wrenchGlyph(x, y, color)}
      </>
    )
  } else if (e.kind === 'result' && !e.pending) {
    shape = (
      <>
        {circle(R + 1, '#1a1a1a')}
        <Glyph x={x} y={y} text={e.note === 'failed' ? '✗' : '✓'} color={color} />
      </>
    )
  } else if (e.kind === 'review') {
    shape = (
      <>
        {circle(R + 1, e.pending ? 'none' : '#1a1a1a')}
        <Glyph x={x} y={y} text={e.pending ? '?' : DECISION_GLYPH[e.note ?? ''] ?? '•'} color={color} />
      </>
    )
  } else if (e.kind === 'complete') {
    shape = <path d={`M ${x} ${y - R - 1} L ${x + R + 1} ${y} L ${x} ${y + R + 1} L ${x - R - 1} ${y} Z`} fill="#1a1a1a" stroke={color} strokeWidth={2} strokeDasharray={dash} />
  } else if (e.kind === 'drafted' || e.kind === 'redraft') {
    shape = prBox(x, y, color, true)
  } else if (e.kind === 'prs') {
    shape = prBox(x, y, color, false)
  } else if (e.kind === 'merged') {
    shape = mergeGlyph(x, y, color)
  } else if (isStepKind(e.kind) && e.lane === 'condoc') {
    // A step entered (a right-arrow) or left (a left-arrow).
    shape = (
      <>
        {circle(R + 1, '#1a1a1a')}
        <Glyph x={x} y={y} text={e.kind === 'step' ? '→' : '←'} color={color} />
      </>
    )
  } else if (e.kind === 'resource' && e.lane === 'condoc') {
    // A resource added: a circle with a file.
    shape = (
      <>
        {circle(R + 1, '#1a1a1a')}
        {fileGlyph(x, y, color)}
      </>
    )
  } else {
    // (A step's or a resource's pointer commit is a dot.)
    shape = circle(SMALL.has(e.kind) || isStepKind(e.kind) || e.kind === 'resource' ? R - 4 : R, e.pending ? 'none' : '#1a1a1a')
  }
  const text = isStepKind(e.kind) ? (e.lane === 'condoc' ? stepLabel(e) : '')
    : e.kind === 'resource' ? (e.lane === 'condoc' ? resourceLabel(e.seq) : '')
    : SMALL.has(e.kind) || ((isCompletion(e.kind) || e.kind === 'external') && e.lane === 'pointer') ? e.kind : e.pending ? e.note ?? '' : ''
  const body = (
    <g className={`swim-node${e.pending ? ' swim-node-pending' : ''}${onOpen ? ' swim-node-link' : ''}`} onClick={onOpen}>
      <title>
        {eventTitle(e)}
        {asked === 'waiting' ? '\n-- its DTT proposals await your answer' : ''}
        {onOpen ? `\n-- ${hint ?? `click for the ${e.kind === 'result' ? 'image' : 'tool call'}`}` : ''}
      </title>
      {shape}
      {text && (
        <text x={x + R + 6} y={y + 4} className="swim-node-label" fill={color}>
          {text}
        </text>
      )}
    </g>
  )
  return e.url && !isLocalURL(e.url) && !onOpen ? (
    <a href={e.url} target="_blank" rel="noreferrer">
      {body}
    </a>
  ) : (
    body
  )
}

// Blocked is the "?" in the condoc lane on an older hold's row.
function Blocked({ hold, x, y }: { hold: TimelineEvent; x: number; y: number }) {
  const body = (
    <g className="swim-node">
      <title>{`held for DTT proposals after interaction ${hold.interaction}`}</title>
      <circle cx={x} cy={y} r={R + 1} fill="#1a1a1a" stroke={GREEN} strokeWidth={2} />
      <Glyph x={x} y={y} text="?" color={GREEN} />
    </g>
  )
  return hold.url && !isLocalURL(hold.url) ? <a href={hold.url} target="_blank" rel="noreferrer">{body}</a> : body
}

const MARKERS: [string, string][] = [['blue', BLUE], ['green', GREEN], ['grey', PENDING], ['amber', AMBER], ['red', RED], ['purple', PURPLE]]
const markerOf = (color: string) => MARKERS.find(([, c]) => c === color)?.[0] ?? 'blue'

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
      markerEnd={`url(#swim-arrow-${markerOf(color)})`}
    />
  )
}

// DiffButton is the "+/-" in a row's left margin that opens its diff.
function DiffButton({ y, row, onOpen }: { y: number; row: string; onOpen: OnOpenDiff }) {
  return (
    <g className="swim-diff-btn" onClick={() => onOpen(row)}>
      <title>{`${diffRowTitle(row)}\n-- click for the changes, repo by repo`}</title>
      <rect x={2} y={y - 7} width={19} height={14} rx={3} />
      <text x={11.5} y={y + 3.5} textAnchor="middle">+/-</text>
    </g>
  )
}

// countProposals reads how many proposals a section lists.
function countProposals(text?: string): number {
  return text ? (text.match(/^- (\[)?`/gm) ?? []).length : 0
}

// openWork asks the page embedding condocv2 -- local-representative, or
// agent-coordinator's host view -- to open its worker tab's visual on an
// agent's work submission, via an OPEN_WORK_MESSAGE postMessage. Nothing
// identifying it is kept in the condoc: the page finds the submission in its
// worker's slopspace history, by the slopspace, role and file read from this
// host's records (condocv2/ops.go), else the role's submission nearest the
// start.
const OPEN_WORK_MESSAGE = 'condocv2-open-work'

const embedded = window.parent !== window

function canOpenWork(op: LocalOperation): boolean {
  return embedded && (op.kind === 'agent' || op.kind === 'aside') && !!(op.signal || op.role)
}

function openWork(op: LocalOperation) {
  window.parent.postMessage(
    { type: OPEN_WORK_MESSAGE, slopspace: op.slopspace, role: op.role, signal: op.signal, at: op.start * 1000 },
    window.location.origin,
  )
}

function opTime(op: LocalOperation): string {
  const t = new Date(op.start * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  if (!op.end) return op.kind !== 'dtt' ? `${t} · ${op.status ?? 'running'}` : t
  const mins = Math.max(1, Math.round((op.end - op.start) / 60))
  return `${t} · ${mins} min${op.status && op.status !== 'completed' ? ` · ${op.status}` : ''}`
}

export function Swimlanes({ timeline, interactions: allInteractions, scope, steps, resources, onScope, pendingPrompt, lockInteraction, holdInteraction, holdProposals, holdWaiting, operations, onOpenDTT, onOpenDiff }: {
  timeline: Timeline
  interactions: Interaction[] // every scope's
  scope: string // the scope drawn ('' the main scope)
  steps?: StepEntry[]
  resources?: ResourceEntry[] // every resource added (see condocv2/resources.go)
  onScope?: (scope: string) => void // descends into a step, or back up out of one
  // The interaction in flight, and the one held for its DTT proposals: only
  // when they are in the scope drawn.
  pendingPrompt?: string
  lockInteraction?: number
  holdInteraction?: number
  holdProposals?: string[] // those still unanswered
  holdWaiting?: boolean // no sub-iteration in flight: the human's turn
  operations?: LocalOperation[]
  onOpenDTT?: OnOpenDTT // opens the DTT viewer (see DTTViewer.tsx)
  onOpenDiff?: OnOpenDiff // opens the diff viewer (see DiffViewer.tsx)
}) {
  // The scope's own events, with its entry and completion when it is a step.
  const inScope = (e: TimelineEvent) => (e.step ?? '') === scope || (isStepKind(e.kind) && e.path === scope && scope !== '')
  const interactions = allInteractions.filter((it) => (it.step ?? '') === scope)
  const stepOf = (path?: string) => steps?.find((s) => s.path === path)
  // A sub-iteration's text: what its proposal and image nodes open.
  const toolText = (n?: number, k?: number) => interactions.find((it) => it.n === n)?.tools?.find((t) => t.k === k)
  const openNode = (e: TimelineEvent): (() => void) | undefined => {
    if (!onOpenDTT || e.pending || !e.interaction || !e.sub || (e.kind !== 'dtt' && e.kind !== 'result')) return undefined
    const t = toolText(e.interaction, e.sub)
    const rel = (e.kind === 'result' && imageOf(t?.result)) || proposalOf(t?.dtt)
    const n = e.interaction
    return rel ? () => onOpenDTT(n, rel) : undefined
  }
  // A step's arrow: in the scope it was entered from, the right-arrow goes
  // into it; in its own view, the left-arrow comes back out.
  const openStep = (e: TimelineEvent): { open: () => void; hint: string } | undefined => {
    if (!onScope || !e.path || e.lane !== 'condoc') return undefined
    const path = e.path
    if (e.kind === 'step' && path !== scope) return { open: () => onScope(path), hint: `click to descend into ${scopeTitle(path)}` }
    if (e.kind === 'step-complete' && path === scope) {
      const up = stepParent(path)
      return { open: () => onScope(up), hint: `click to come back up to the ${scopeTitle(up)}` }
    }
    return undefined
  }
  const lanes = timeline.lanes ?? []
  const events = (timeline.events ?? []).filter(inScope)
  const unanswered = holdProposals?.length ?? 0
  const waitingOn = holdInteraction && holdWaiting && unanswered > 0 ? holdInteraction : undefined

  const laneIndex = new Map(lanes.map((l, i) => [l.id, i]))
  const { rowOf, maxRow, sections, waitRow, betweenRow } = layoutRows(events, interactions, scope, lockInteraction, waitingOn)

  const lanesRight = GUTTER + lanes.length * COL_W
  const opsX = lanesRight + OPS_GAP
  const colX = (lane: string) => GUTTER + (laneIndex.get(lane) ?? 0) * COL_W + COL_W / 2
  const rowY = (row: number) => HEADER_H + ROW_H / 2 + row * ROW_H

  // Several events can land in one cell (a stage retried); fan them out.
  const cellCount = new Map<string, number>()
  const placed = events.map((e) => {
    const key = `${e.lane}|${rowOf(e)}`
    const k = cellCount.get(key) ?? 0
    cellCount.set(key, k + 1)
    return { e, x: colX(e.lane) + k * (2 * R + 6), y: rowY(rowOf(e)) }
  })
  const find = (lane: string, kind: string, n?: number, sub = 0) =>
    placed.find((p) => p.e.lane === lane && p.e.kind === kind && (n === undefined || ((p.e.interaction ?? 0) === n && (p.e.sub ?? 0) === sub)))

  // Which interactions proposed DTT tool calls: their record is a "?".
  const proposed = new Set(placed.filter((p) => p.e.kind === 'response' && p.e.note === 'dtt').map((p) => p.e.interaction ?? 0))
  const askedOf = (e: TimelineEvent): 'waiting' | 'answered' | undefined =>
    e.kind === 'record' && proposed.has(e.interaction ?? 0) ? (e.interaction === holdInteraction && unanswered > 0 ? 'waiting' : 'answered') : undefined

  // The "?" of each older hold, in the condoc lane.
  const blocked = placed.filter((p) => p.e.kind === 'hold').map((p) => ({ hold: p.e, x: colX('condoc'), y: p.y }))

  // The operations column: each box just below its anchor, pushed down below
  // the one before. Its red arrows run level, out to it and back. Only this
  // scope's: those hung off its nodes (and, in the main scope, any hung off
  // none) -- and the asides asked in it, whose own scope places them, with
  // no arrows when the node before them is another scope's.
  const anchorOf = (op: LocalOperation) => (op.anchorLane && op.anchorKind ? find(op.anchorLane, op.anchorKind, op.interaction ?? 0, op.sub ?? 0) : undefined)
  const ops = (operations ?? []).filter((op) =>
    op.kind === 'aside' ? (op.step ?? '') === scope : op.anchorLane && op.anchorKind ? !!anchorOf(op) : scope === '')
  let nextOpY = HEADER_H
  const opBoxes = ops.map((op) => {
    const anchor = anchorOf(op)
    const want = anchor ? anchor.y + OP_DROP : nextOpY
    const y = Math.max(want, nextOpY)
    nextOpY = y + OP_H + 8
    return { op, anchor, y }
  })

  const width = opsX + OPS_W + 24
  const height = Math.max(HEADER_H + (maxRow + 1) * ROW_H, nextOpY) + PAD_BOTTOM

  // Connectors.
  const connectors: ReactNode[] = []
  const pointerBegin = find('pointer', 'begin')
  if (pointerBegin) {
    for (const p of placed) {
      if (p.e.kind === 'begin' && p.e.lane !== 'pointer') {
        connectors.push(<Arrow key={`begin-${p.e.lane}`} x1={pointerBegin.x + R + 3} y1={pointerBegin.y} x2={p.x - R - 5} y2={p.y} color={BLUE} />)
      }
      // Begin also adds each read-only repo, at its first commit.
      if (p.e.kind === 'resolve' && !p.e.interaction) {
        connectors.push(<Arrow key={`begin-ro-${p.e.lane}-${p.x}`} x1={pointerBegin.x + R + 3} y1={pointerBegin.y} x2={p.x - R - 3} y2={p.y} color={BLUE} dashed />)
      }
    }
  }
  // The pointer's external commit records each lane's external change.
  const externalNodes = placed.filter((p) => p.e.kind === 'external' && p.e.lane !== 'pointer')
  const sameExternal = (a: TimelineEvent, b: TimelineEvent) => (a.interaction ?? 0) === (b.interaction ?? 0) && (a.seq ?? 0) === (b.seq ?? 0)
  for (const from of placed.filter((p) => p.e.kind === 'external' && p.e.lane === 'pointer')) {
    for (const to of externalNodes.filter((p) => sameExternal(p.e, from.e))) {
      connectors.push(<Arrow key={`ext-${from.e.interaction ?? 0}.${from.e.seq ?? 0}-${to.e.lane}`} x1={from.x + R + 3} y1={from.y} x2={to.x - R - 3} y2={to.y} color={BLUE} dashed />)
    }
  }
  // The pointer's step commit records the condoc's.
  const stepNodes = placed.filter((p) => isStepKind(p.e.kind))
  for (const from of stepNodes.filter((p) => p.e.lane === 'pointer')) {
    const to = stepNodes.find((p) => p.e.lane === 'condoc' && stepKey(p.e) === stepKey(from.e))
    if (to) connectors.push(<Arrow key={`step-${stepKey(from.e)}`} x1={from.x + R - 2} y1={from.y} x2={to.x - R - 4} y2={to.y} color={GREEN} />)
  }
  // The pointer's resource commit records the condoc's.
  const resourceNodes = placed.filter((p) => p.e.kind === 'resource')
  for (const from of resourceNodes.filter((p) => p.e.lane === 'pointer')) {
    const to = resourceNodes.find((p) => p.e.lane === 'condoc' && p.e.seq === from.e.seq)
    if (to) connectors.push(<Arrow key={`resource-${from.e.seq ?? 0}`} x1={from.x + R - 2} y1={from.y} x2={to.x - R - 4} y2={to.y} color={GREEN} />)
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
      const color = unlock.e.pending ? PENDING : askedOf(record.e) === 'waiting' ? AMBER : GREEN
      connectors.push(<Arrow key={`unlock-${n}`} x1={unlock.x + R} y1={unlock.y} x2={record.x - R - 3} y2={record.y} color={color} />)
    }
    // Each sub-iteration: its lock brings in the proposal commit, its unlock
    // the image commit.
    const ks = new Set(placed.filter((p) => p.e.interaction === n && p.e.sub).map((p) => p.e.sub ?? 0))
    for (const k of ks) {
      const subLock = find('pointer', 'lock', n, k)
      const dtt = find('condoc', 'dtt', n, k)
      if (subLock && dtt) {
        connectors.push(<Arrow key={`lock-${n}.${k}`} x1={subLock.x + R} y1={subLock.y} x2={dtt.x - R - 4} y2={dtt.y} color={dtt.e.pending ? PENDING : GREEN} />)
      }
      const subUnlock = find('pointer', 'unlock', n, k)
      const result = find('condoc', 'result', n, k)
      if (subUnlock && result) {
        connectors.push(<Arrow key={`unlock-${n}.${k}`} x1={subUnlock.x + R} y1={subUnlock.y} x2={result.x - R - 3} y2={result.y} color={subUnlock.e.pending ? PENDING : GREEN} />)
      }
    }
    // An older condoc's hold, review and release.
    const hold = find('pointer', 'hold', n)
    const q = blocked.find((b) => b.hold.interaction === n)
    if (hold && q) {
      connectors.push(<Arrow key={`hold-${n}`} x1={hold.x + R} y1={hold.y} x2={q.x - R - 3} y2={q.y} color={GREEN} />)
      if (record) connectors.push(<Arrow key={`held-${n}`} x1={record.x} y1={record.y + R + 2} x2={q.x} y2={q.y - R - 4} color={GREEN} />)
    }
    const review = find('condoc', 'review', n)
    if (q && review) {
      connectors.push(<Arrow key={`decide-${n}`} x1={q.x} y1={q.y + R + 2} x2={review.x} y2={review.y - R - 4} color={review.e.pending ? PENDING : GREEN} />)
    }
    const release = find('pointer', 'release', n)
    if (release && review) {
      connectors.push(<Arrow key={`release-${n}`} x1={release.x + R} y1={release.y} x2={review.x - R - 4} y2={review.y} color={release.e.pending ? PENDING : GREEN} />)
    }
  }
  for (const kind of ['complete', 'prs'] as const) {
    const from = find('pointer', kind)
    const to = find('condoc', kind)
    if (from && to) connectors.push(<Arrow key={`c-${kind}`} x1={from.x + R + 2} y1={from.y} x2={to.x - R - 6} y2={to.y} color={GREEN} />)
  }
  // The merge: the pointer's merged commit across each repo's merge commit
  // (kept by its finalization, see condocv2/timeline.go withFinalization).
  const merges = lanes.flatMap((l) => {
    const p = find(l.id, 'merged')
    return p ? [p] : []
  })
  merges.slice(1).forEach((to, i) => {
    const from = merges[i]
    connectors.push(<Arrow key={`merged-${to.e.lane}`} x1={from.x + R + 4} y1={from.y} x2={to.x - R - 6} y2={to.y} color={PURPLE} />)
  })

  // The human's column: each interaction's prompt arrives from the left, and
  // so does each answer to a proposal (a sub-iteration), or the wait for one.
  const prompts: { n: number; text: string; pending: boolean }[] = interactions.map((it) => ({ n: it.n, text: it.prompt, pending: false }))
  if (pendingPrompt && lockInteraction && !interactions.some((it) => it.n === lockInteraction)) {
    prompts.push({ n: lockInteraction, text: pendingPrompt, pending: true })
  }
  const answers = placed
    .filter((p) => p.e.kind === 'lock' && p.e.sub)
    .map((p) => {
      const n = p.e.interaction ?? 0
      const k = p.e.sub ?? 0
      const dtt = find('condoc', 'dtt', n, k)
      const text = interactions.find((it) => it.n === n)?.tools?.find((t) => t.k === k)?.dtt
      const note = dtt && !dtt.e.pending ? dtt.e.note : undefined
      return { n, k, y: p.y, note, proposal: proposalOf(text) }
    })
  const reviews = blocked.map((b) => {
    const n = b.hold.interaction ?? 0
    const review = placed.find((p) => p.e.kind === 'review' && p.e.interaction === n && !p.e.pending)
    return { n, y: b.y, decision: review?.e.note, count: countProposals(interactions.find((it) => it.n === n)?.review) }
  })
  const pointerX = colX('pointer')
  // ...and each external change comes in from outside, dotted.
  const externalChanges = [...betweenRow.entries()].filter(([key]) => key.startsWith('external ')).map(([k, row]) => {
    const key = k.slice('external '.length)
    const nodes = externalNodes.filter((p) => `${p.e.interaction ?? 0}.${p.e.seq ?? 0}` === key)
    const commits = nodes.reduce((sum, p) => sum + (p.e.commits ?? 0), 0)
    return { key, y: rowY(row), commits, repos: nodes.map((p) => shortRepo(p.e.lane)), how: nodes[0]?.e.note }
  })
  const commitWord = (c: number) => (c === 0 ? 'commits' : c === 1 ? '1 commit' : `${c} commits`)
  // ...and each step entered or left, with its arrow's way in or out.
  const stepChanges = [...betweenRow.entries()].filter(([key]) => key.startsWith('step')).map(([key, row]) => {
    const node = stepNodes.find((p) => stepKey(p.e) === key && p.e.lane === 'condoc') ?? stepNodes.find((p) => stepKey(p.e) === key)
    const e = node?.e
    const step = stepOf(e?.path)
    return { key, y: rowY(row), e, step, nav: e ? openStep({ ...e, lane: 'condoc' }) : undefined }
  })
  // ...and each resource added, with its files.
  const resourceAdds = [...betweenRow.entries()].filter(([key]) => key.startsWith('resource ')).map(([key, row]) => {
    const e = (resourceNodes.find((p) => resourceKey(p.e.seq) === key && p.e.lane === 'condoc') ?? resourceNodes.find((p) => resourceKey(p.e.seq) === key))?.e
    const r = resources?.find((x) => resourceKey(x.number) === key)
    return { key, y: rowY(row), e, r }
  })

  // Each lane's line runs from its first node to its last -- a read-only
  // repo's dotted blue, since it is followed but never written.
  const laneLines = lanes.map((l) => {
    const ys = placed.filter((p) => p.e.lane === l.id).map((p) => p.y)
    if (ys.length < 2) return null
    const readOnly = l.role === 'readonly'
    return (
      <line
        key={`lane-${l.id}`}
        x1={colX(l.id)}
        y1={Math.min(...ys)}
        x2={colX(l.id)}
        y2={Math.max(...ys)}
        stroke={readOnly ? BLUE : LANE_LINE}
        strokeWidth={2}
        strokeDasharray={readOnly ? '2 4' : undefined}
      />
    )
  })

  // The rows with a diff: each of this scope's interactions whose agent's
  // work has landed, on its response and commits row, and each external
  // change.
  const hasDiff = onOpenDiff ? diffRows(timeline) : new Set<string>()
  const diffButtons: { row: string; y: number }[] = []
  for (const n of new Set(placed.filter((p) => !p.e.sub && (p.e.kind === 'response' || p.e.kind === 'commit')).map((p) => p.e.interaction ?? 0))) {
    if (hasDiff.has(`${n}`)) diffButtons.push({ row: `${n}`, y: rowY(rowOf({ kind: 'response', interaction: n })) })
  }
  for (const x of externalChanges) {
    const [n, e] = x.key.split('.').map(Number)
    if (hasDiff.has(externalRow(n, e))) diffButtons.push({ row: externalRow(n, e), y: x.y })
  }

  const dividerY = (row: number) => rowY(row) - ROW_H / 2

  return (
    <div className="swim">
      <div className="swim-toolbar">
        <span className="swim-legend">
          <span className="swim-key" style={{ color: BLUE }}>● structure</span>
          <span className="swim-key" style={{ color: GREEN }}>● interaction · N.k a DTT tool call</span>
          <span className="swim-key" style={{ color: AMBER }}>? awaiting your answer</span>
          <span className="swim-key" style={{ color: BLUE }}>◌ read-only repo pulled at a new commit · external change</span>
          <span className="swim-key" style={{ color: GREEN }}>→ step entered (click to descend) · ← step completed</span>
          <span className="swim-key" style={{ color: GREEN }}>🗎 resource added</span>
          <span className="swim-key" style={{ color: PURPLE }}>⑂ merged</span>
          <span className="swim-key" style={{ color: RED }}>▭ local operation</span>
          <span className="swim-key" style={{ color: PENDING }}>◌ pending</span>
          <span className="swim-key">
            click a node for its commit — a DTT tool call or ✓ for its tool call and image{embedded ? ', an agent for its work submission' : ''}
          </span>
        </span>
      </div>
      {timeline.source === 'record' && (
        <div className="pointer-note">
          Drawn from the condoc's{' '}
          {timeline.recordUrl ? <RepoLink href={timeline.recordUrl}>timeline record</RepoLink> : 'timeline record'}
          , saved when it was completed — its slopspace is gone. The completion after it (the PRs and each repo's merge commit) is from the pointer's final merged commit.
        </div>
      )}
      {(timeline.errors ?? []).map((err) => (
        <div key={err} className="pointer-message">{err}</div>
      ))}
      <div className="swim-scroll">
        <svg className="swim-svg" width={width} height={height} viewBox={`0 0 ${width} ${height}`}>
          <defs>
            {MARKERS.map(([id, color]) => (
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
                {l.role === 'target' ? 'target' : l.role === 'readonly' ? 'read-only' : shortRepo(l.repo)}
              </text>
            </g>
          ))}
          <text x={12} y={18} className="swim-lane-title">Human</text>
          <text x={12} y={33} className="swim-lane-repo">prompts and decisions</text>
          <text x={opsX} y={18} className="swim-lane-title">Local operations</text>
          <text x={opsX} y={33} className="swim-lane-repo">agent invocations · DTT executions · asides</text>

          {/* The numbered dividers: solid between iterations, dashed before each sub-iteration. */}
          {sections.map((s) => (
            <g key={`section-${s.label}-${s.row}`}>
              <line x1={4} y1={dividerY(s.row)} x2={width - 8} y2={dividerY(s.row)} className="swim-divider" strokeDasharray={s.solid ? undefined : '8 6'} />
              {s.label && <text x={4} y={dividerY(s.row) + 11} className="swim-section-label">{s.label}</text>}
            </g>
          ))}
          <line x1={4} y1={dividerY(maxRow + 1)} x2={width - 8} y2={dividerY(maxRow + 1)} className="swim-divider" />

          {laneLines}

          {prompts.map((p) => {
            const y = rowY(rowOf({ kind: 'lock', interaction: p.n }))
            const color = p.pending ? PENDING : GREEN
            return (
              <g key={`prompt-${p.n}`}>
                <title>{`Prompt ${p.n}\n\n${p.text}`}</title>
                <text x={24} y={y - 4} className="swim-prompt-n" fill={color}>prompt {p.n}</text>
                <text x={24} y={y + 11} className="swim-prompt-text">{snippet(p.text, 24)}</text>
                <Arrow x1={GUTTER - 12} y1={y} x2={pointerX - R - 3} y2={y} color={color} />
              </g>
            )
          })}

          {answers.map((a) => {
            const color = a.note ? GREEN : PENDING
            const word = a.note === 'reject' ? '✗ rejected' : a.note === 'preread' ? '👁 read before deciding' : a.note ? `✓ accepted · dtt ${a.note}` : 'answered'
            const open = onOpenDTT && a.proposal ? () => onOpenDTT(a.n, a.proposal) : undefined
            return (
              <g key={`answer-${a.n}.${a.k}`} className={open ? 'swim-answer-link' : undefined} onClick={open}>
                <title>{`DTT tool call ${a.n}.${a.k}: ${a.proposal || 'a proposal'} ${word}${open ? '\n-- click for the tool call' : ''}`}</title>
                <text x={24} y={a.y - 4} className="swim-prompt-n" fill={color}>{word}</text>
                <text x={24} y={a.y + 11} className="swim-prompt-text">{snippet(a.proposal, 24)}</text>
                <Arrow x1={GUTTER - 12} y1={a.y} x2={pointerX - R - 3} y2={a.y} color={color} />
              </g>
            )
          })}

          {externalChanges.map((x) => (
            <g key={`external-${x.key}`}>
              <title>{`External change ${x.key}: ${commitWord(x.commits)} pushed to ${x.repos.join(', ') || 'the condoc'} from outside condocv2, pulled in ${x.how === 'prompt' ? 'as the next prompt took the lock' : 'by Refresh remotes'}`}</title>
              <text x={24} y={x.y - 4} className="swim-prompt-n" fill={BLUE}>external</text>
              <text x={24} y={x.y + 11} className="swim-prompt-text">{snippet(`${commitWord(x.commits)} · ${x.repos.join(', ')}`, 24)}</text>
              <Arrow x1={GUTTER - 12} y1={x.y} x2={pointerX - R - 3} y2={x.y} color={BLUE} dashed />
            </g>
          ))}

          {stepChanges.map(({ key, y, e, step, nav }) => {
            const done = e?.kind === 'step-complete'
            const what = done
              ? `completed after interaction ${e?.interaction ?? 0}`
              : step?.title || `entered after interaction ${e?.interaction ?? 0}`
            return (
              <g key={`stepnote-${key}`} className={nav ? 'swim-answer-link' : undefined} onClick={nav?.open}>
                <title>{`${scopeTitle(e?.path ?? '')} ${done ? 'completed' : 'entered'}${step?.title ? `: ${step.title}` : ''}\n${what}${nav ? `\n-- ${nav.hint}` : ''}`}</title>
                <text x={24} y={y - 4} className="swim-prompt-n" fill={GREEN}>{`${done ? '←' : '→'} ${e ? stepLabel(e) : key}`}</text>
                <text x={24} y={y + 11} className="swim-prompt-text">{snippet(what, 24)}</text>
                <Arrow x1={GUTTER - 12} y1={y} x2={pointerX - R - 3} y2={y} color={GREEN} />
              </g>
            )
          })}

          {resourceAdds.map(({ key, y, e, r }) => {
            const what = r?.title || e?.note || (r ? r.files.join(', ') : `added after interaction ${e?.interaction ?? 0}`)
            const files = r ? `\n${r.files.length} file${r.files.length === 1 ? '' : 's'}: ${r.files.join(', ')}` : ''
            return (
              <g key={`resourcenote-${key}`}>
                <title>{`${key[0].toUpperCase()}${key.slice(1)}, added after interaction ${e?.interaction ?? r?.interaction ?? 0}: ${what}${files}`}</title>
                <text x={24} y={y - 4} className="swim-prompt-n" fill={GREEN}>{key}</text>
                <text x={24} y={y + 11} className="swim-prompt-text">{snippet(what, 24)}</text>
                <Arrow x1={GUTTER - 12} y1={y} x2={pointerX - R - 3} y2={y} color={GREEN} />
              </g>
            )
          })}

          {waitRow !== undefined && (
            <g className={onOpenDTT ? 'swim-answer-link' : undefined} onClick={onOpenDTT && waitingOn ? () => onOpenDTT(waitingOn) : undefined}>
              <title>{`Interaction ${waitingOn} proposed DTT tool calls: accept or reject each one (it runs as sub-iteration ${waitingOn}.k), or request changes, to carry on${onOpenDTT ? '\n-- click for the tool calls' : ''}`}</title>
              <text x={24} y={rowY(waitRow) - 4} className="swim-prompt-n" fill={AMBER}>blocked — answer {unanswered}</text>
              <text x={24} y={rowY(waitRow) + 11} className="swim-prompt-text">DTT proposal{unanswered === 1 ? '' : 's'} to decide</text>
              <Arrow x1={GUTTER - 12} y1={rowY(waitRow)} x2={pointerX - R - 3} y2={rowY(waitRow)} color={AMBER} dashed />
            </g>
          )}

          {reviews.map((r) => (
            <g key={`review-${r.n}`}>
              <title>{`Review ${r.n}: ${DECISION_WORD[r.decision ?? ''] ?? 'decided'}`}</title>
              <text x={24} y={r.y - 4} className="swim-prompt-n" fill={GREEN}>review {r.n}</text>
              <text x={24} y={r.y + 11} className="swim-prompt-text">
                {`${DECISION_GLYPH[r.decision ?? ''] ?? ''} ${DECISION_WORD[r.decision ?? ''] ?? 'decided'} · ${r.count || 'the'} DTT proposal${r.count === 1 ? '' : 's'}`}
              </text>
              <Arrow x1={GUTTER - 12} y1={r.y} x2={pointerX - R - 3} y2={r.y} color={GREEN} />
            </g>
          ))}

          {onOpenDiff && diffButtons.map((d) => <DiffButton key={`diff-${d.row}`} y={d.y} row={d.row} onOpen={onOpenDiff} />)}

          {connectors}

          {placed.map((p, i) => {
            const nav = openStep(p.e)
            return (
              <Node
                key={`${p.e.lane}-${p.e.kind}-${p.e.path ?? label(p.e.interaction, p.e.sub)}-${p.e.sha ?? i}-${i}`}
                e={p.e}
                x={p.x}
                y={p.y}
                asked={askedOf(p.e)}
                onOpen={nav?.open ?? openNode(p.e)}
                hint={nav?.hint}
              />
            )
          })}
          {blocked.map((b) => (
            <Blocked key={`blocked-${b.hold.interaction}`} hold={b.hold} x={b.x} y={b.y} />
          ))}

          {opBoxes.map(({ op, anchor, y }, i) => {
            const running = op.kind !== 'dtt' && !op.end
            // An aside is a bubble rather than a box (condocv2/aside.go).
            const bubble = op.kind === 'aside'
            // A DTT run's image is copied into the condoc beside its
            // proposal, under the same name: dtt/<N>/<rel>.
            const image = op.kind === 'dtt' && op.interaction && onOpenDTT ? op.title.replace(/^dtt /, '') : ''
            const click = canOpenWork(op) ? () => openWork(op) : image && op.interaction ? () => onOpenDTT?.(op.interaction ?? 0, image) : undefined
            const hint = canOpenWork(op) ? 'click for its work submission (worker › visual)' : image ? 'click for its image' : ''
            const out = y + OP_ARROW_IN
            const back = y + OP_H - OP_ARROW_IN
            return (
              <g key={`op-${i}`} className={click ? 'swim-op swim-op-link' : 'swim-op'} onClick={click}>
                <title>{[op.title, opTime(op), op.detail, op.path, hint].filter(Boolean).join('\n')}</title>
                {anchor && (
                  <>
                    <Arrow x1={anchor.x + R + 4} y1={out} x2={opsX - 4} y2={out} color={RED} />
                    <Arrow x1={opsX - 4} y1={back} x2={anchor.x + R + 4} y2={back} color={RED} dashed={running} />
                  </>
                )}
                <rect x={opsX} y={y} width={OPS_W} height={OP_H} rx={bubble ? OP_H / 2 : 4} fill={bubble ? '#2a1414' : '#1a1a1a'} stroke={RED} strokeWidth={1.5} strokeDasharray={running ? '4 3' : undefined} />
                <text x={opsX + (bubble ? 16 : 8)} y={y + 14} className="swim-op-title" fill={RED}>{snippet(op.title, 30)}</text>
                <text x={opsX + (bubble ? 16 : 8)} y={y + 27} className="swim-op-detail">{opTime(op)}</text>
              </g>
            )
          })}
        </svg>
      </div>
      <ol className="swim-sequence">
        <li><b>start</b> — <b>propose</b>, the proposal's pointer commit; <b>begin</b>, the slopspace, a branch in every repo, the condoc's first commit</li>
        <li><b>N</b> — an interaction: <b>lock</b> the condoc, the <b>prompt</b> commit (auto-submits once if asked), the agent's <b>response</b> with a commit in each target it changed (a wrench when it proposed DTT tool calls), the <b>record</b> mapping those commits, <b>unlock</b></li>
        <li><b>?</b> — the response proposed DTT tool calls: no prompt and no completion until each is accepted or rejected (or changes are requested, which is the next interaction)</li>
        <li><b>N.k</b> — one DTT tool call: <b>lock</b>, the proposal commit (eye a read — or a write proposal read before deciding it — wrench an assert, ✗ rejected), dtt runs it once that is pushed, the image commit <b>✓</b>, <b>unlock</b></li>
        <li><b>◌ read-only</b> — a read-only repo (its own column, never changed) pulled at a new commit, a blue dotted circle: at begin, or by an interaction's deploy (its response row) or return (its record row); each is also a <code>readspace N</code> commit on the pointer</li>
        <li><b>external</b> — commits pushed to a condoc or target branch from outside condocv2, pulled in between interactions (blue dotted): by <b>Refresh remotes</b>, or as the next prompt took the lock; the pointer's <b>external</b> commit records them, and the next interaction's record lists them</li>
        {onOpenDiff && <li><b>+/-</b> — in the left margin of an interaction's response row and of each external change: click for its changes, file by file in every repo it moved</li>}
        <li><b>step →</b> — indent the scope into a step (step-1, a substep step-1-1, ...): its interactions go into a step file of their own and are drawn in its own view — click the arrow to descend into it; <b>step done ←</b> — the step completed, back up to the scope it was entered from (in the step's view, click it to come back up). Interaction numbers run on through every scope</li>
        <li><b>resource R</b> — files added to the condoc between interactions (<b>Add resource</b>: uploaded, from this host's files, the highlighted files, entered as text, or dictated): a dot on the pointer, a green circle with a file in the condoc, which keeps them under <code>resources/R/</code> with a <b>Resource R</b> section the agent reads from the next interaction on</li>
        <li><b>aside</b> — a red bubble among the local operations: a read-only question asked of the agent (<b>Aside</b> on the Text tab), kept in this host's history and never in the condoc; later agent runs can read it</li>
        <li><b>complete ◆ · PR · merged ⑂</b> — completion, the drafted and raised PRs, and the merge</li>
      </ol>
    </div>
  )
}
