export interface Host {
  id: string
  label: string
  status: 'connected' | 'disconnected'
}

export interface HostsMsg {
  hosts: Host[]
}

export interface ServiceStatus {
  name: string
  status: string
}

export interface LRStateMsg {
  host_id: string
  active: boolean
  services?: ServiceStatus[]
}

// fc is the federation-command instance on that host the message is about --
// see local-representative/fcinstances.go. Absent from an older LR.
export interface LRFCStateMsg {
  host_id: string
  fc?: string
  state: string // "remote-control", "local-control", or ""
}

export interface LRFCLogMsg {
  host_id: string
  fc?: string
  line: string
  kind?: string // "cmd" or "output"
}

// FCInstanceInfo is one federation-command instance connected to a host's LR.
export interface FCInstanceInfo {
  key: string
  label: string // "#2", "@fc-ab12"
  instance_id?: string
  head?: string
  state: string
  session?: string
  ridealong?: Omit<LRRidealongMsg, 'host_id'>
  condoc?: Omit<LRCondocMsg, 'host_id'>
}

export interface LRFCInstancesMsg {
  host_id: string
  instances: FCInstanceInfo[]
}

// A host's control tab -- see local-representative/control.go.
export interface ControlStepInfo {
  label: string
  detail?: string
}

export interface ControlSequenceInfo {
  id: string
  name: string
  description?: string
  steps: ControlStepInfo[]
  record?: string[] // whose screens a run records ("robot": the host's own)
}

export interface ControlStepResult {
  label: string
  status: string // "pending" | "running" | "success" | "error" | "skipped"
  message?: string
  duration_ms?: number
}

export interface ControlRunMsg {
  id: string
  sequence: string
  name: string
  status: string // "running" | "success" | "error" | "cancelled"
  error?: string
  started_at: number // unix ms
  duration_ms?: number
  steps: ControlStepResult[]
  values?: { label: string; value: string }[]
}

export interface ControlStateMsg {
  sequences: ControlSequenceInfo[]
  run?: ControlRunMsg
}

export interface LRControlMsg {
  host_id: string
  control: ControlStateMsg | null // null once the host disconnects
}

export interface LRRidealongMsg {
  host_id: string
  active: boolean
  title?: string
  current_index?: number
  total_steps?: number
  current_cmd?: string
  prev_cmd?: string
  prev_exit_code?: number
  next_cmd?: string
  autoplay?: boolean
  countdown?: string
  waypoints?: string[]
}

export interface LRCondocMsg {
  host_id: string
  active: boolean
  name?: string
  phase?: string
  step_num?: number
  status_msg?: string
}

export interface ProcInfo {
  name: string
  instance_id: string // unique key for a managed instance ("" for self)
  instance: number    // per-app ordinal (0 for self / first singleton)
  pid: number
  status: string // "running" | "exited" | "failed"
  managed: boolean
  started_at: number // unix seconds
  exit_code: number  // meaningful once status != "running"
  detail?: string
  dev_mode?: boolean // launched with --dev-mode -- see docs/DevMode.md
  loader_managed?: boolean // self only: launched by ufa-loader, so "restart" comes back up
  version?: string // this process's build version -- see docs/DevMode.md "Versioning"
  update_available?: boolean // the on-disk binary now answers --version differently than this running process (self: docs/DevMode.md "Loader"; managed: Step4Prompt.md Revision D)
  pending_version?: string // the on-disk version update_available refers to; empty whenever update_available is false -- see Step5Prompt.md Revision J
  auto_update?: boolean // self only: restart that host's LR automatically the moment update_available goes true, instead of waiting for the "restart and update" control -- see docs/DevMode.md "Loader"
  session?: string // federation-command only: the session it's currently on (display name if session.yaml has one, otherwise the bare id) -- empty until an instance has connected and reported
}

// SelfInfoMsg discloses this agent-coordinator instance's own dev-mode
// status (see docs/DevMode.md), host identity, and restart-ability -- sent
// when the WebSocket connects and re-sent whenever loader_managed/
// update_available change. host_id is the same ufahostid value a co-located
// local-representative defaults its "-name" to, letting the frontend
// recognize which connected host (if any) is the one agent-coordinator
// itself runs on. loader_managed/update_available mirror ProcInfo's
// same-named fields for a local-representative's own self row -- see the
// global topology view's "restart agent-coordinator" control
// (Step4Prompt.md Revision E).
export interface SelfInfoMsg {
  dev_mode: boolean
  host_id: string
  loader_managed: boolean
  update_available: boolean
  // auto_update: whether AC restarts itself the instant update_available
  // flips true, rather than waiting for an operator to press "restart and
  // update AC" -- mirrors ProcInfo's same-named field for a
  // local-representative's own self row. See the global topology view's
  // "agent-coordinator" section auto-update checkbox (Step1SubstepCPrompt.md
  // Revision D).
  auto_update: boolean
  version: string // this process's own build version -- see BrowserRefreshStrategy.md
  started_at: number // unix seconds this process started -- mirrors ProcInfo's same-named field for AC's own uptime readout
}

// ModeMismatchMsg discloses that a connected local-representative's dev-mode
// status differs from this agent-coordinator's own. mismatched: false clears
// a prior disclosure.
export interface ModeMismatchMsg {
  host_id: string
  mismatched: boolean
  peer_mode?: string
}

// TCAvailabilityMsg is the aggregate answer to "is a the-conversationalist
// instance available on any host" -- see tcavailability.go. Drives the mic
// icon shown beside the camera/screenshot icon in the header (illuminated
// when available) -- see
// condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step2Prompt.md.
export interface TCAvailabilityMsg {
  available: boolean
}

export interface LRSystemStateMsg {
  host_id: string
  active: boolean
  self: ProcInfo
  managed: ProcInfo[]
}

// LRRepoStateMsg mirrors local-representative's dev-repo watcher (--dev-repo,
// see docs/DevMode.md) for one host. watched is false both when that LR isn't
// watching a repo and when it isn't connected.
export interface LRRepoStateMsg {
  host_id: string
  watched: boolean
  root?: string
  dirty: boolean          // uncommitted staged or unstaged changes relative to HEAD
  rebuild_ready: boolean  // the rebuild button is active -- HEAD moved since the last successful rebuild, and the repo isn't dirty
  building: boolean       // 'make deploy-dev-binaries' is running right now
  auto_rebuild: boolean
  // 90s auto-rebuild debounce timer (Step3Prompt.md Revision E): pending is
  // true from the moment the rebuild button first becomes active with
  // auto-rebuild on, until the debounced build actually starts; seconds
  // counts down and is re-armed to 90 whenever a further change lands.
  auto_rebuild_pending?: boolean
  auto_rebuild_seconds?: number
  head?: string
  last_error?: string
  // True while condoccer's '.condoc' lock file sits at the repo root (see
  // condocs/initialDistributedDevelopmentImpls/Step5Prompt.md) -- forces
  // rebuild_ready false regardless of dirty/head, so a condoc mid-transition
  // is never rebuilt out from under.
  condoc_locked?: boolean
}

export interface CondocInfo {
  path: string
  name: string
  phase: string
  stepNum: number
  stepFile?: string
  substepFile?: string
  substepLetter?: string
}

export interface LRCondoccerMsg {
  host_id: string
  available: boolean
  root?: string
  condocs?: CondocInfo[]
}

export interface LRSessionsMsg {
  host_id: string
  available: boolean
}

export interface LRConvoMsg {
  host_id: string
  available: boolean
}

export interface LRRobotMsg {
  host_id: string
  available: boolean
}

export interface FileInfo {
  id: string
  name: string
  size: number
  kind: string // "text" | "image" | "other" -- selects the wireframe icon
  state: string // "cached" | "held" | "persisted" -- selects the icon's color
  uploaded_at: number // unix seconds
  expires_at: number  // unix seconds; meaningless (0) once state is "persisted"
  highlighted: boolean // plain operator-set toggle, independent of state
  marked_up: boolean  // an in-progress markup-dialog session (arrow/rect/text) is open on this file
}

export interface LRFilesMsg {
  host_id: string
  active: boolean
  files?: FileInfo[]
}

// DebugLogEntry mirrors local-representative's same-named type: one captured
// stdout/stderr line from an LR-managed sub-app, shown on the system tab's
// debug view (condocs/initialDistributedSessionsImpls/Step1SubstepBPrompt.md
// Revision E).
export interface DebugLogEntry {
  instance_id: string
  app: string
  stream: string // "stdout" | "stderr"
  line: string
  ts: number // unix seconds
}

export interface LRDebugLogMsg {
  host_id: string
  active: boolean
  entries?: DebugLogEntry[]
}

// ChainCallEntry mirrors local-representative's same-named type: one
// outbound HTTP call made on the SM<->LR<->AC chain -- either that host's
// own "lr->ac" hop or its session-manager's "sm->lr" hop -- shown on the
// system tab's debug view's "network" tab
// (condocs/initialDistributedSessionsImpls/Step1SubstepBPrompt.md Revision
// F). Browser-side fetch capture (see App.tsx's installNetworkCapture)
// covers only this one frontend's own requests; this is the half of the
// chain that happens entirely between backend processes.
export interface ChainCallEntry {
  hop: string // "sm->lr" | "lr->ac"
  method: string
  url: string
  status: number // 0 on a network-level failure
  error?: string
  duration_ms: number
  ts: number // unix seconds
}

export interface LRChainCallMsg {
  host_id: string
  active: boolean
  entries?: ChainCallEntry[]
}

// StateboardEntry mirrors local-representative's same-named type: one
// key/value row, nested two levels deep under the sub-app that owns it
// (app: key: value), on the system tab's debug view's "stateboard" tab
// (condocs/initialDistributedSessionsImpls/Step2Prompt.md Revision E) --
// a generic board any representable-connected sub-app can post custom
// entries to, plus the default "present"/"hosts" rows local-representative
// derives itself from its own connection health. We assume for now that
// keys are only ever this two levels deep (Revision F).
export interface StateboardEntry {
  app: string
  key: string
  value: string
}

export interface LRStateboardMsg {
  host_id: string
  active: boolean
  entries?: StateboardEntry[]
}
