export type Phase =
  | 'proposed'
  | 'awaiting_step'
  | 'agent_running'
  | 'awaiting_action'
  | 'completed'
  // Slopspace condocs only (see condoccer/slopspace.go): a turn waiting in
  // the queue, then propose-PR, PRs open and merged after completion.
  | 'queued'
  | 'pr_proposed'
  | 'pr_open'
  | 'merged'

export interface CondocInfo {
  path: string
  name: string
  phase: Phase
  stepNum: number
  stepFile?: string
  substepFile?: string
  substepLetter?: string
}

export interface CondocMeta {
  startTime?: number
  controlScheme?: string
  branch?: string
  callerPath?: string
}

export interface StepSummary {
  num: number
  title: string
  prompt: string
  hasReplace?: boolean
}

export interface Iteration {
  id: string
  label: string
  type: 'reply' | 'revision' | 'retry' | 'substep' | 'resource'
  from?: string
  agent?: boolean // a resource the turn's agent created ("## Resource N (agent)")
}

export interface CondocState {
  info: CondocInfo
  mainContent: string
  stepContent?: string
  substepContent?: string
  substepIterations?: Iteration[]
  nextLetter: string
  fromOptions: string[]
  meta: CondocMeta
  description: string
  steps: StepSummary[]
  iterations: Iteration[]
  completedStepContents?: Record<number, string>
  // completedSubstepContents holds the full raw content of every substep of
  // the active step other than the one currently active (keyed by substep
  // letter), so a substep's whole history stays viewable once it completes
  // and control returns to the step -- see substepContent/substepIterations,
  // which are only ever populated for a *currently active* substep.
  completedSubstepContents?: Record<string, string>
  // slopspace is the condoc's pointer -- set for slopspace condocs only.
  slopspace?: PointerEntry
}

export type ReprStatus = 'disconnected' | 'connecting' | 'connected'

export interface ReprStatusMsg {
  status: ReprStatus
  host?: string
  port?: string
  // auto_connect is the persistent auto-connect toggle: true whenever the
  // cycle is armed, whether or not it's currently connected/connecting -- it
  // stays true across a successful connection, and only an explicit
  // disconnect turns it off.
  auto_connect?: boolean
  // http_port is local-representative's own HTTP dashboard port, disclosed
  // once connected; empty while disconnected. Used to reach LR's /convo/
  // reverse proxy directly for TC's mic-capture iframe -- see App.tsx's
  // tcCaptureURL and
  // condocs/initialShellsSessionManagerAndTheConversationalistImpls/
  // Step2Prompt.md.
  http_port?: string
}

// TCAvailabilityMsg is the aggregate answer to "is a the-conversationalist
// instance available on any host", relayed down through local-representative
// from agent-coordinator's own aggregate -- see tcavailability.go. Gates the
// mic icon shown on condoccer's text input fields.
export interface TCAvailabilityMsg {
  available: boolean
}

// SelfInfoMsg discloses this condoccer instance's own dev-mode status and
// build version (see docs/DevMode.md) -- sent once when the WebSocket
// connects.
export interface SelfInfoMsg {
  dev_mode: boolean
  version: string
}

// RootMsg is condoccer's current working dir (the scan root every condoc
// path is relative to) -- sent on connect and whenever "Set Working Dir"
// changes it.
export interface RootMsg {
  root: string
}

// PRLive is a raised PR as last seen on GitHub -- see slopspace.go.
export interface PRLive {
  repo: string
  number: number
  url: string
  state: string // open, closed, merged
  status: string // mergeability, e.g. "mergeable", "checks pending", "conflicts"
  ready: boolean
}

// PointerEntry summarizes one pointer file (pointers/*.yaml) in the slopspace
// pointer repo -- see condoccer/pointer.go and slopspace.go's entryFromSpec.
export interface PointerEntry {
  file: string
  stem: string
  path: string // "slopspace:<stem>", the condoc path to subscribe to
  name: string
  phase: string
  step?: string
  substep?: string
  iteration?: string
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
  prTemplate?: string
  turnKind?: string
  turnStage?: string
  prs?: PRLive[]
  heldHere: boolean
  queuePos?: number
  running?: boolean
  deployed?: boolean
  forcePending?: boolean
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

// PointerConfig is the configurable part of the pointer repo setting ("Set
// Pointer Repo"). A relative path resolves server-side against the git repo
// condoccer was started in.
export interface PointerConfig {
  repo: string
  branch: string
  path: string
}

export type PointerRepoState ='missing' | 'cloning' | 'invalid' | 'ready'

// PointerStatus is the slopspace tab's pointer repo setting and its state on
// disk -- sent on connect and whenever either changes.
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

// ModeMismatchMsg discloses that the connected local-representative's
// dev-mode status differs from this condoccer's own. mismatched: false
// clears a prior disclosure.
export interface ModeMismatchMsg {
  mismatched: boolean
  peer_mode?: string
}

export type ServerMsg =
  | { type: 'list'; payload: { condocs: CondocInfo[] } }
  | { type: 'condoc'; payload: CondocState }
  | { type: 'error'; payload: { message: string } }
  | { type: 'repr-status'; payload: ReprStatusMsg }
  | { type: 'self-info'; payload: SelfInfoMsg }
  | { type: 'mode-mismatch'; payload: ModeMismatchMsg }
  | { type: 'root'; payload: RootMsg }
  | { type: 'pointer'; payload: PointerStatus }

export interface ActionRequest {
  action: 'handoff' | 'completed' | 'force_complete' | 'revision' | 'retry' | 'substep' | 'start_step' | 'revert' | 'resubmit' | 'add_resource'
  path: string
  content?: string
  letter?: string
  from?: string
  substepTitle?: string
  revertStep?: number
  revertIter?: string
  revertSubIter?: string
  resourceType?: 'highlighted' | 'voice-note' // for add_resource action; 'upload' and 'clipboard' go through /api/upload-resource instead
  resourceName?: string // for add_resource action: optional display name -> "## Resource N -- <name>"
}
