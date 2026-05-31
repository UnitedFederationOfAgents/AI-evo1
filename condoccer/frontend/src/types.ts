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
}

export interface CondocState {
  info: CondocInfo
  mainContent: string
  stepContent?: string
  nextLetter: string
  fromOptions: string[]
}

export type ServerMsg =
  | { type: 'list'; payload: { condocs: CondocInfo[] } }
  | { type: 'condoc'; payload: CondocState }
  | { type: 'error'; payload: { message: string } }

export interface ActionRequest {
  action: 'handoff' | 'completed' | 'revision' | 'retry' | 'start_step'
  path: string
  content?: string
  letter?: string
  from?: string
}
