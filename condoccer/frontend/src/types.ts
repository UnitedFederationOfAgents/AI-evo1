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
  type: 'reply' | 'revision' | 'retry' | 'substep'
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
}

export type ReprStatus = 'disconnected' | 'connecting' | 'connected'

export interface ReprStatusMsg {
  status: ReprStatus
  host?: string
  port?: string
}

export type ServerMsg =
  | { type: 'list'; payload: { condocs: CondocInfo[] } }
  | { type: 'condoc'; payload: CondocState }
  | { type: 'error'; payload: { message: string } }
  | { type: 'repr-status'; payload: ReprStatusMsg }

export interface ActionRequest {
  action: 'handoff' | 'completed' | 'revision' | 'retry' | 'substep' | 'start_step' | 'revert'
  path: string
  content?: string
  letter?: string
  from?: string
  substepTitle?: string
  revertStep?: number
  revertIter?: string
  revertSubIter?: string
}
