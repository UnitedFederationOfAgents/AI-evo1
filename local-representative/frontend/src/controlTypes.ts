// The control tab's types (local-representative/control.go, controllib.go
// and controlops.go). This file and ControlV1.tsx are shared, word for word,
// by local-representative's and agent-coordinator's frontends -- keep the
// two copies in step.

export interface ControlStepInfo {
  label: string
  detail?: string // the action's description
  do?: string[] // what each instruction does, as far as known before the run
  action?: string // the definer action it runs
}

export interface ControlSequenceInfo {
  id: string
  name: string
  description?: string
  controls?: ControlParam[]
  steps: ControlStepInfo[]
  record?: string[] // whose screens a run records ("robot": the node's own)
  error?: string // why it can't be compiled, if it can't
}

export interface ControlStepResult extends ControlStepInfo {
  status: string // "pending" | "running" | "success" | "error" | "skipped"
  message?: string
  duration_ms?: number
}

export interface ControlRunMsg {
  id: string
  sequence: string
  name: string
  controls?: Record<string, string> // the values it ran with
  status: string // "running" | "success" | "error" | "cancelled"
  error?: string
  started_at: number // unix ms
  duration_ms?: number
  steps: ControlStepResult[]
  values?: { label: string; value: string }[]
  recordings?: ControlRecording[]
}

// A screen recording, or a screenshot, a run saved into the node's files tab.
export interface ControlRecording {
  who: string // whose screen ("robot": the node's own; else a node's name)
  file_id: string // <files api>/<file_id>
  name: string
  video: boolean // playable in the browser, rather than a .zip of frames
  image?: boolean // a screenshot (node-capture), rather than a recording
  duration_ms?: number
  via?: string
}

export interface ControlStateMsg {
  sequences: ControlSequenceInfo[]
  run?: ControlRunMsg
  node?: string // the node's own name
  nodes?: string[] // the nodes connected to agent-coordinator: what a "node" control can choose
}

// ---- The library: definer actions and composer sequences ----

export interface ControlParam {
  name: string
  label?: string
  type?: string // "" for text, "node" for a node connected to agent-coordinator
  default?: string
  help?: string
}

// One op and its arguments; op names it.
export type ControlInstruction = Record<string, string> & { op: string }

export interface ControlActionDef {
  id: string
  name: string
  description?: string
  controls: ControlParam[] | null
  do: ControlInstruction[]
}

export interface ControlStepRef {
  action: string
  label?: string
  with?: Record<string, string>
  before_recording?: boolean
}

export interface ControlSequenceDef {
  id: string
  name: string
  description?: string
  controls: ControlParam[] | null
  record?: string[]
  steps: ControlStepRef[]
}

export interface ControlOpArg {
  name: string
  help: string
  required?: boolean
  default?: string
}

export interface ControlOpSpec {
  op: string // robot.<op> for the robot's own
  summary: string
  describe: string
  args: ControlOpArg[] | null
}

export interface ControlBuiltin {
  name: string
  help: string
  value: string
}

export interface ControlLibraryMsg {
  actions: ControlActionDef[]
  sequences: ControlSequenceDef[]
  ops: ControlOpSpec[]
  robot_ops: boolean // false until the robot has said which ops it has
  builtins: ControlBuiltin[]
  path?: string // where it's saved; absent if only in memory
  note?: string
}

// A definer/composer request; req is filled in by whoever sends it.
export interface ControlLibRequest {
  op: 'save-action' | 'delete-action' | 'save-sequence' | 'delete-sequence' | 'import' | 'export' | 'restore-examples'
  id?: string
  previous_id?: string
  action?: ControlActionDef
  sequence?: ControlSequenceDef
  yaml?: string
  kind?: 'actions' | 'sequences'
  ids?: string[]
}

export interface ControlLibReply {
  req?: string
  op: string
  success: boolean
  error?: string
  message?: string
  yaml?: string
  filename?: string
}
