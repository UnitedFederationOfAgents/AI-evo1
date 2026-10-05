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
}

// SelfInfoMsg discloses this instance's own dev-mode status and build
// version (see docs/DevMode.md) -- sent once when the WebSocket connects.
export interface SelfInfoMsg {
  dev_mode: boolean
  version: string
}

// ModeMismatchMsg discloses that the connected local-representative's
// dev-mode status differs from this instance's own. mismatched: false clears
// a prior disclosure.
export interface ModeMismatchMsg {
  mismatched: boolean
  peer_mode?: string
}

// CaptureResultMsg is the "capture-result" payload reporting a completed
// native or browser capture (see robot.go).
export interface CaptureResultMsg {
  source: 'native' | 'browser'
  success: boolean
  image_url?: string
  error?: string
  artifact_id?: string // names a successful capture for "save-artifact" (see artifacts.go)
}

// CircleMouseResultMsg is the "circle-mouse-result" payload reporting
// whether the circle-mouse action completed (see robot.go).
export interface CircleMouseResultMsg {
  success: boolean
  via?: string // which input path drove the pointer
  error?: string
}

// ClipFrame is one sampled frame of a fallback native clip, at_ms after the
// clip started (see clip.go).
export interface ClipFrame {
  image_url: string
  at_ms: number
}

// ClipResultMsg is the "clip-result" payload reporting a completed native
// clip (see clip.go). On success exactly one of video_url (compositor
// recording) or frames (sampled fallback) is set.
export interface ClipResultMsg {
  success: boolean
  via?: string // which capture path recorded the clip
  video_url?: string
  frames?: ClipFrame[]
  duration_ms?: number
  error?: string
  artifact_id?: string // names a successful Native Clip for "save-artifact" (see artifacts.go)
}

// SequenceStepDef is one high-level sequence step and the lower-level
// instructions it is carried out as (see sequence.go).
export interface SequenceStepDef {
  label: string
  detail: string[]
}

export interface SequenceDef {
  id: string
  name: string
  steps: SequenceStepDef[]
}

// SequenceDefsMsg is the "sequence-defs" payload listing the sequences this
// instance can run -- sent once when the WebSocket connects.
export interface SequenceDefsMsg {
  sequences: SequenceDef[]
}

// SequenceOutput is a value a run printed (sequence-v2's print and read-text
// ops).
export interface SequenceOutput {
  label?: string
  value: string
}

// SequenceProgressMsg is the "sequence-progress" (v1) / "seq2-progress" (v2)
// payload reporting one step starting or finishing.
export interface SequenceProgressMsg {
  sequence_id: string
  step: number
  status: 'running' | 'success' | 'error'
  message?: string
  image_url?: string // what the step saw, if it looked at the screen
  outputs?: SequenceOutput[] // everything the run has printed so far
}

export interface SequenceStepResult {
  status: 'success' | 'error' | 'skipped'
  message?: string
  duration_ms: number
  image_url?: string // what the step saw, if it looked at the screen
}

// SequenceResultMsg is the "sequence-result" payload reporting a finished
// run, with its recording. success is the run's own outcome; the recording
// reports its own success separately.
export interface SequenceResultMsg {
  sequence_id: string
  success: boolean
  failed_step: number // -1 if no step failed
  error?: string
  steps: SequenceStepResult[] | null
  duration_ms: number
  keyboard_via?: string
  recording?: ClipResultMsg
  outputs?: SequenceOutput[] // what the run printed
  warnings?: string[] // e.g. a step used an example action older than this build's
  artifact_id?: string // names the run for "save-artifact" (see artifacts.go)
}

// ---- sequence-v2 (see seqv2.go) ----

// Control is a parameter a definer action or composer sequence exposes.
export interface Control {
  name: string
  label?: string
  default?: string
  help?: string
}

// Instruction is one primitive operation: op names it, the other keys are
// its arguments.
export type Instruction = Record<string, string> & { op: string }

export interface ActionDef {
  id: string
  name: string
  description?: string
  controls: Control[] | null
  do: Instruction[]
}

export interface StepRef {
  action: string
  label?: string
  with?: Record<string, string>
}

export interface SequenceV2 {
  id: string
  name: string
  description?: string
  controls: Control[] | null
  steps: StepRef[]
}

export interface OpArg {
  name: string
  help: string
  required?: boolean
  default?: string
}

// OpSpec is a primitive operation the definer's instructions can use.
export interface OpSpec {
  op: string
  summary: string
  describe: string
  args: OpArg[] | null
}

export interface BuiltinVar {
  name: string
  help: string
  value: string
}

// SeqLibraryMsg is the "seq2-library" payload: the whole library, sent on
// connect and after every change.
export interface SeqLibraryMsg {
  actions: ActionDef[] | null
  sequences: SequenceV2[] | null
  ops: OpSpec[] | null
  builtins: BuiltinVar[] | null
  path?: string
  note?: string
}

// Seq2ReplyMsg answers a library edit, import or export; req echoes the
// request's.
export interface Seq2ReplyMsg {
  req?: string
  op: string
  success: boolean
  error?: string
  message?: string
  yaml?: string
  filename?: string
}

// Seq2StartedMsg: a v2 run has begun, with its steps as compiled.
export interface Seq2StartedMsg {
  sequence_id: string
  def: SequenceDef
}

// OCRLine is a line of text read off the screen, boxed in capture pixels
// (see vision.go).
export interface OCRLine {
  text: string
  x: number
  y: number
  w: number
  h: number
}

// InspectResultMsg is the "inspect-result" payload: a capture, the text read
// off it, and the lines matching the requested text, if any. Boxes are in
// the capture's pixels (width x height), not image_url's.
export interface InspectResultMsg {
  success: boolean
  error?: string
  image_url?: string
  width: number
  height: number
  capture_via?: string
  lines: OCRLine[] | null
  find?: string
  matches: OCRLine[] | null
  partial: OCRLine[] | null
  duration_ms: number
  artifact_id?: string // names a successful inspection for "save-artifact" (see artifacts.go)
}

// SaveResultMsg is the "save-result" payload reporting whether a
// "save-artifact" request made it into local-representative's files area
// (see artifacts.go).
export interface SaveResultMsg {
  artifact_id: string
  success: boolean
  name?: string // name as uploaded
  file_id?: string // local-representative's id for it
  error?: string
}

export type ServerMsg =
  | { type: 'repr-status'; payload: ReprStatusMsg }
  | { type: 'self-info'; payload: SelfInfoMsg }
  | { type: 'mode-mismatch'; payload: ModeMismatchMsg }
  | { type: 'capture-result'; payload: CaptureResultMsg }
  | { type: 'circle-mouse-result'; payload: CircleMouseResultMsg }
  | { type: 'clip-result'; payload: ClipResultMsg }
  | { type: 'sequence-defs'; payload: SequenceDefsMsg }
  | { type: 'sequence-progress'; payload: SequenceProgressMsg }
  | { type: 'sequence-result'; payload: SequenceResultMsg }
  | { type: 'inspect-result'; payload: InspectResultMsg }
  | { type: 'save-result'; payload: SaveResultMsg }
  | { type: 'seq2-library'; payload: SeqLibraryMsg }
  | { type: 'seq2-reply'; payload: Seq2ReplyMsg }
  | { type: 'seq2-started'; payload: Seq2StartedMsg }
  | { type: 'seq2-progress'; payload: SequenceProgressMsg }
  | { type: 'seq2-result'; payload: SequenceResultMsg }
