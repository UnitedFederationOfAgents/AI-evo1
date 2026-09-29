export type Phase =
  | 'proposed'
  | 'awaiting_step'
  | 'agent_running'
  | 'awaiting_action'
  | 'completed'

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

export interface ActionRequest {
  action: 'handoff' | 'completed' | 'revision' | 'retry' | 'substep' | 'start_step' | 'revert' | 'resubmit' | 'add_resource'
  path: string
  content?: string
  letter?: string
  from?: string
  substepTitle?: string
  revertStep?: number
  revertIter?: string
  revertSubIter?: string
  resourceType?: 'highlighted' // for add_resource action; only option so far
  resourceName?: string // for add_resource action: optional display name -> "## Resource N -- <name>"
}
