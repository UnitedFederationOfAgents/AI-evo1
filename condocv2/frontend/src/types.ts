// Pointer phases -- see condocv2/spec.go.
export type Phase =
  | 'proposed' | 'awaiting_prompt' | 'locked' | 'reviewing' | 'pr_drafting' | 'pr_proposed' | 'pr_open' | 'merged'
  | 'archived' | 'abandoned' // the final states -- see condocv2/final.go

export const isFinalPhase = (p: Phase) => p === 'archived' || p === 'abandoned'

// wasMerged: merged, whatever final state it has moved to since -- its views
// come from the timeline record (condocv2/record.go).
export const wasMerged = (e: PointerEntry) => e.phase === 'merged' || e.finalFrom === 'merged'

// isLocalURL: a link into a local repo with no remote (ufa-local:<name>/...,
// condocv2/localrepo.go) -- there's no page to open, so it is shown as text.
export const isLocalURL = (url?: string) => !!url && url.startsWith('ufa-local:')

// Interaction stages, in order -- see condocv2/spec.go.
export type LockStage =
  | 'locked'
  | 'prompted'
  | 'queued'
  | 'deployed'
  | 'signalled'
  | 'returned'
  | 'responded'
  | 'mapped'
  | 'recorded'

// A held condoc's stage: waiting for the human, or the stage of the
// sub-iteration in flight -- see condocv2/spec.go's ToolCall.
export type HoldStage = 'waiting' | 'locked' | 'proposed' | 'ran' | 'imaged'

// read isn't a decision: it reads a write proposal (assert or delete) with
// dtt read before deciding it, in a sub-iteration of its own.
export type ReviewDecision = 'accept' | 'reject' | 'modify' | 'read'

// PointerEntry summarizes one v2 pointer file (pointers/*.yaml) in the
// pointer repo -- see condocv2/slopspace.go's entryFromSpec. Legacy v1
// pointers are never listed.
export interface PointerEntry {
  file: string
  stem: string
  name: string
  phase: Phase
  interactions: number
  branch?: string
  condocRepo?: string
  targets?: string[]
  readOnly?: string[] // owner/name[@ref]: read but never changed -- see condocv2/readonly.go
  condocPath?: string
  condocUrl?: string
  description?: string
  agent?: string
  model?: string
  startTime?: number
  slopspaceId?: string
  holder?: string
  finalFrom?: Phase // archived/abandoned: the phase it left, and when
  finalAt?: string
  error?: string
  lockInteraction?: number
  lockStage?: LockStage
  autoSubmit?: boolean
  // The DTT proposals holding the condoc, if any -- see condocv2/review.go:
  // those still unanswered, how many sub-iterations are done, and the one in
  // flight.
  holdInteraction?: number
  holdStage?: HoldStage
  holdProposals?: string[]
  holdDone?: number // sub-iterations done, pre-reads included
  holdPreReads?: string[] // the proposals read before deciding, one per such sub-iteration
  holdSub?: number
  holdCall?: string
  holdDecision?: 'accept' | 'reject' | 'read'
  holdMode?: string
  // Configure condoc -- see condocv2/configure.go: why it can't be
  // configured now (absent when it can), the configuration in flight, and
  // the ones done ("N.c owner/name: from -> to", or "N.c owner/name: added,
  // following ref").
  configureBlocked?: string
  configuring?: string
  configurations?: string[]
  // Refresh remotes -- see condocv2/external.go: why the remotes can't be
  // refreshed now (absent when they can), and the external changes found
  // ("N.e owner/name: k commits (how)").
  refreshBlocked?: string
  externals?: string[]
  // Steps -- see condocv2/steps.go: the scope interactions go into now
  // (absent for the main scope), every step entered, and why the scope can't
  // change now (absent when it can).
  step?: string
  steps?: StepEntry[]
  stepBlocked?: string
  // Resources -- see condocv2/resources.go: every resource added, and why
  // one can't be added now (absent when it can).
  resources?: ResourceEntry[]
  resourceBlocked?: string
  heldHere: boolean
  queuePos?: number
  running?: boolean
  deployed?: boolean
  // Its turn is waiting on the agent, which Cancel turn stops, and whether
  // that has been asked already -- see condocv2/slopspace.go's slopCancelTurn.
  awaitingAgent?: boolean
  cancelRequested?: boolean
}

// StepEntry is one step the condoc entered -- see condocv2/steps.go. A step
// is a scope of its own, with a step file of its own: the main scope is '',
// the first step entered from it '1', the first entered from that '1-1', and
// so on. from/to are the interactions completed when it was entered and left.
export interface StepEntry {
  path: string
  title?: string
  from: number
  enteredAt?: number
  completed?: boolean
  to?: number
  completedAt?: number
}

// ResourceEntry is one resource added to the condoc between interactions --
// see condocv2/resources.go: resource `number` (1, 2, ... across the whole
// condoc), added after `interaction` in scope `step`, its files kept in the
// condoc under resources/<number>/.
export interface ResourceEntry {
  number: number
  interaction: number
  step?: string
  title?: string
  source: ResourceSource
  files: string[]
  time?: number
}

// Where a resource's files come from: the browser, this host's files tab
// (its host-cache and host-store), the files highlighted in
// local-representative's files tab and its peers', text entered in the form,
// or text dictated with The Conversationalist.
export type ResourceSource = 'upload' | 'files' | 'highlighted' | 'text' | 'voice'

// HighlightedFile is one file highlighted in a files tab
// (condocv2/highlighted.go): on this host's local-representative, or on the
// peer `node`.
export interface HighlightedFile {
  key: string
  id: string
  name: string
  node?: string
  size: number
}

// HighlightedFiles is the "highlighted-files" answer: null until listed.
export interface HighlightedFiles {
  files: HighlightedFile[]
  error?: string
}

// HostFile is one entry of this host's files tab, which a resource can take.
export interface HostFile {
  id: string
  name: string
  size: number
  state: 'cached' | 'persisted'
  time: number
}

// resourceFileURL is where condocv2 serves one of a resource's files --
// relative to the path the UI was served under, as DTTViewer's dttFileURL.
export function resourceFileURL(stem: string, number: number, file: string): string {
  const dir = window.location.pathname.replace(/\/[^/]*\.[^/]*$/, '/')
  const base = dir.endsWith('/') ? dir.slice(0, -1) : dir
  return `${base}/api/resource/${encodeURIComponent(stem)}/${number}/${encodeURIComponent(file)}`
}

// stepParent is the scope a step was entered from: '1-2' -> '1', '1' -> ''.
export const stepParent = (path: string) => (path.includes('-') ? path.slice(0, path.lastIndexOf('-')) : '')

// stepAncestors are the scopes around a step, outermost first, the step
// itself last: '1-2' -> ['', '1', '1-2'].
export function stepAncestors(path: string): string[] {
  const out = [path]
  for (let p = path; p !== ''; ) {
    p = stepParent(p)
    out.unshift(p)
  }
  return out
}

// scopeTitle names a scope as the condoc files do.
export const scopeTitle = (path: string) => (path ? `step-${path}` : 'main scope')

// Interaction is one Prompt/Response/Record round of a step file.
export interface Interaction {
  n: number
  prompt: string
  response?: string
  proposals?: string // the DTT tool calls its turn proposed
  record?: string
  review?: string // an older condoc's whole-set decision on them
  tools?: ToolText[] // its sub-iterations, one per proposal
  step?: string // the scope it was made in (absent for the main scope)
}

// ToolText is sub-iteration n.k's text: the decision on one proposal, and
// what dtt returned for it.
export interface ToolText {
  k: number
  dtt?: string
  result?: string
}

export interface TimelineLane {
  id: string // "pointer", "condoc", or a target's or read-only repo's owner/name
  repo: string
  role: 'pointer' | 'condoc' | 'target' | 'readonly'
}

export type TimelineKind =
  | 'propose' | 'begin' | 'lock' | 'prompt' | 'response' | 'commit' | 'record' | 'unlock'
  | 'dtt' | 'result' // a sub-iteration's proposal (note: read, assert, delete, reject, or preread -- read before deciding) and image (note: ok or failed)
  | 'hold' | 'review' | 'release' // an older condoc's whole-set review (review's note is the decision)
  | 'resolve' // a read-only repo pulled at a new commit (note: begin, deploy or return) -- see condocv2/readonly.go
  | 'external' // a branch moved from outside condocv2, pulled in between interactions (note: refresh or prompt) -- see condocv2/external.go
  | 'step' | 'step-complete' // a step entered or left (path: the step) -- see condocv2/steps.go
  | 'resource' // resource seq added after interaction n (note: its title, else its files) -- see condocv2/resources.go
  | 'complete' | 'redraft' | 'drafted' | 'prs' | 'merged' // completion -- see condocv2/complete.go

export interface TimelineEvent {
  lane: string
  kind: TimelineKind
  interaction?: number
  sub?: number // k of sub-iteration n.k
  seq?: number // e of external change n.e, R of resource n.R
  commits?: number // external: how many commits came in
  sha?: string
  subject?: string
  time?: number
  url?: string
  pending?: boolean
  note?: string
  path?: string // step, step-complete: the step entered or left
  step?: string // the scope it belongs to (absent for the main scope)
}

export interface Timeline {
  lanes: TimelineLane[]
  events: TimelineEvent[]
  errors?: string[]
  // "record": the slopspace is gone, so this is the timeline saved on
  // completion (condocv2/record.go), at recordUrl.
  source?: 'record'
  recordUrl?: string
}

// CondocState is the selected condoc: its pointer, the text flow and the
// visual timeline -- see condocv2/slopspace.go's condocState.
export interface CondocState {
  entry: PointerEntry
  mainContent?: string
  stepContent?: string // the main scope's step file
  interactions: Interaction[] // every scope's, each marked with its own
  pendingPrompt?: string
  timeline: Timeline
  // The visual's prompt column when the timeline comes from the record.
  recordPrompts?: Interaction[]
  // The visual's right-hand column, read from this host's records and never
  // kept in the condoc's model -- see condocv2/ops.go.
  operations?: LocalOperation[]
  // The read-only questions asked of the agent, from this host's local
  // history and never in the condoc -- see condocv2/aside.go.
  asides?: Aside[]
  // Completion -- see condocv2/complete.go.
  defaultPrGuidance: string
  prGuidance?: string
  prTemplate?: string
  prs?: PRLive[]
}

// Aside is one read-only question asked of the agent about the condoc --
// see condocv2/aside.go: aside `number` (1, 2, ... per condoc), placed after
// `interaction` in the scope `step`, kept only in this host's history.
export interface Aside {
  number: number
  stem: string
  interaction: number
  step?: string
  prompt: string
  status: 'queued' | 'running' | 'completed' | 'failed' | 'cancelled'
  askedAt: number
  startedAt?: number
  completedAt?: number
  agent?: string
  model?: string
  reply?: string
  changed?: string[] // repos the agent changed despite read mode
  error?: string
}

// LocalOperation is an agent invocation, DTT execution or aside behind the
// condoc, hung off the timeline node it followed.
export interface LocalOperation {
  kind: 'agent' | 'dtt' | 'aside'
  title: string
  detail?: string
  status?: string
  start: number
  end?: number
  path?: string
  anchorLane?: string
  anchorKind?: TimelineKind
  interaction?: number
  sub?: number
  // aside: its number, and the scope it ran in
  number?: number
  step?: string
  // agent: what the worker tab's visual finds its work submission by
  slopspace?: string
  role?: string
  signal?: string
}

// PRLive is a raised PR as last seen on GitHub.
export interface PRLive {
  repo: string
  number: number
  url: string
  state: string // "open" or "merged"
  status: string // mergeability, in words
  ready: boolean
}

export type ReprStatus = 'disconnected' | 'connecting' | 'connected'

export interface ReprStatusMsg {
  status: ReprStatus
  host?: string
  port?: string
  // auto_connect is the persistent auto-connect toggle: true whenever the
  // cycle is armed -- it stays true across a successful connection, and only
  // an explicit disconnect turns it off.
  auto_connect?: boolean
  // http_port is local-representative's own HTTP dashboard port, disclosed
  // once connected; used to reach LR's /convo/ reverse proxy for TC's
  // mic-capture iframe -- see App.tsx's tcCaptureURL.
  http_port?: string
}

// TCAvailabilityMsg: is a the-conversationalist instance available on any
// host -- gates the mic icon on the prompt box.
export interface TCAvailabilityMsg {
  available: boolean
}

// SelfInfoMsg discloses this condocv2's own dev-mode status and build
// version (see docs/DevMode.md) -- sent once when the WebSocket connects.
export interface SelfInfoMsg {
  dev_mode: boolean
  version: string
}

// SlopspaceProposal is the propose form: new when stem is empty, an edit of a
// still-proposed condoc otherwise.
export interface SlopspaceProposal {
  stem?: string
  name: string
  description: string
  targets: string[]
  readOnly: string[] // owner/name[@ref]
  condocRepo: string
  branch: string
  agent: string
  model: string
}

export interface BranchCheck {
  repo: string
  exists: boolean
  error?: string
}

// PointerConfig is the configurable part of the pointer repo setting.
export interface PointerConfig {
  repo: string
  branch: string
  path: string
}

export type PointerRepoState = 'missing' | 'cloning' | 'invalid' | 'ready'

export interface PointerStatus extends PointerConfig {
  resolvedPath: string
  state: PointerRepoState
  message?: string
  lastPull?: string
  pullError?: string
  pushError?: string
  tokenAvailable: boolean
  holder: string
  defaultAgent?: string
  defaultModel?: string
  // agentOverride: this condocv2's own agent/model, used for every condoc's
  // agent runs while set; never written into a condoc (see override.go).
  agentOverride?: AgentOverride
  pointers: PointerEntry[]
}

export interface AgentOverride {
  agent: string
  model?: string
}

export interface ModeMismatchMsg {
  mismatched: boolean
  peer_mode?: string
}
