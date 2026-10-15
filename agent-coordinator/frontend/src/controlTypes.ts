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
  recordings?: ControlRecordBlock[] // whose screens a run records, across which steps (nodes as the defaults fill them in)
  error?: string // why it can't be compiled, if it can't
  rev?: number // which import of it this is: a new one each time it's (re-)imported or added
}

// A recording block (local-representative/controlrecord.go): node's screen
// recorded from step from to step to (1-based, inclusive). Up to three side
// by side, never the same node twice at a step. In a sequence's definition
// node may be a {{reference}} ({{this_node}}, a node control).
export interface ControlRecordBlock {
  node: string
  from: number
  to: number
  label?: string
}

// A recording block's progress in a run.
export interface ControlRecordState extends ControlRecordBlock {
  status: string // "pending" | "starting" | "recording" | "saving" | "saved" | "error" | "skipped"
  message?: string
}

export interface ControlStepResult extends ControlStepInfo {
  status: string // "pending" | "running" | "success" | "error" | "skipped"
  message?: string
  duration_ms?: number
}

export interface ControlRunMsg {
  id: string
  sequence: string
  sequence_rev?: number // the rev of the sequence it ran: only that import's run, not a re-import's
  name: string
  controls?: Record<string, string> // the values it ran with
  record_override?: string // the runner's override recording: a node recorded across the whole run, or "none"
  status: string // "running" | "success" | "error" | "cancelled"
  error?: string
  started_at: number // unix ms
  duration_ms?: number
  steps: ControlStepResult[]
  values?: { label: string; value: string }[]
  records?: ControlRecordState[] // the sequence's recording blocks, as they go
  recordings?: ControlRecording[]
  output?: { label: string; value: string }[] // what the run brings back (the output op)
  prompt?: ControlPrompt // set while a step waits for continue
  saved?: ControlSavedRun[] // where the run was saved to the files tab ("save-run")
}

// A .zip of a finished run (report, run.json and its files) saved into the
// node's files tab.
export interface ControlSavedRun {
  file_id: string // <files api>/<file_id>
  name: string
}

// A step waiting for the person following the run (the ask-user op): shown
// with a continue button.
export interface ControlPrompt {
  step: number
  message: string
}

// A screen recording, a screenshot, or a file fetched from a node, that a
// run saved into the node's files tab.
export interface ControlRecording {
  who: string // whose screen: a node's name ("robot", from older runs: the node's own)
  file_id: string // <files api>/<file_id>
  name: string
  video: boolean // playable in the browser, rather than a .zip of frames
  image?: boolean // a screenshot (node-capture), rather than a recording
  file?: boolean // a file fetched from who (node-fetch-file), rather than a recording
  path?: string // where on who a fetched file came from
  duration_ms?: number
  via?: string
  from?: number // the steps a recording block recorded (1-based)
  to?: number
  label?: string // the block's label
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
}

export interface ControlSequenceDef {
  id: string
  name: string
  description?: string
  controls: ControlParam[] | null
  steps: ControlStepRef[]
  recordings?: ControlRecordBlock[]
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
  op: 'save-action' | 'delete-action' | 'save-sequence' | 'delete-sequence' | 'import' | 'export' | 'restore-examples' | 'save-run'
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
