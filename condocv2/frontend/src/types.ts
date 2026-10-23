// Pointer phases -- see condocv2/spec.go.
export type Phase = 'proposed' | 'awaiting_prompt' | 'locked' | 'pr_drafting' | 'pr_proposed' | 'pr_open' | 'merged'

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

// PointerEntry summarizes one v2 pointer file (pointers/*.yaml) in the
// pointer repo -- see condocv2/slopspace.go's entryFromSpec. condoccer's v1
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
  condocPath?: string
  condocUrl?: string
  description?: string
  agent?: string
  model?: string
  startTime?: number
  slopspaceId?: string
  holder?: string
  error?: string
  lockInteraction?: number
  lockStage?: LockStage
  autoSubmit?: boolean
  heldHere: boolean
  queuePos?: number
  running?: boolean
  deployed?: boolean
}

// Interaction is one Prompt/Response/Record round of the step file.
export interface Interaction {
  n: number
  prompt: string
  response?: string
  record?: string
}

export interface TimelineLane {
  id: string // "pointer", "condoc", or a target's owner/name
  repo: string
  role: 'pointer' | 'condoc' | 'target'
}

export type TimelineKind = 'propose' | 'begin' | 'lock' | 'prompt' | 'response' | 'commit' | 'record' | 'unlock'

export interface TimelineEvent {
  lane: string
  kind: TimelineKind
  interaction?: number
  sha?: string
  subject?: string
  time?: number
  url?: string
  pending?: boolean
  note?: string
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
  stepContent?: string
  interactions: Interaction[]
  pendingPrompt?: string
  timeline: Timeline
  // The visual's prompt column when the timeline comes from the record.
  recordPrompts?: Interaction[]
  // Completion -- see condocv2/complete.go.
  defaultPrGuidance: string
  prGuidance?: string
  prTemplate?: string
  prs?: PRLive[]
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
  pointers: PointerEntry[]
}

export interface ModeMismatchMsg {
  mismatched: boolean
  peer_mode?: string
}
