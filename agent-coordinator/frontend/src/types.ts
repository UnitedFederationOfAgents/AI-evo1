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

export interface LRFCStateMsg {
  host_id: string
  state: string // "remote-control", "local-control", or ""
}

export interface LRFCLogMsg {
  host_id: string
  line: string
  kind?: string // "cmd" or "output"
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
}

// SelfInfoMsg discloses this agent-coordinator instance's own dev-mode status
// (see docs/DevMode.md) and host identity -- sent once when the WebSocket
// connects. host_id is the same ufahostid value a co-located
// local-representative defaults its "-name" to, letting the frontend
// recognize which connected host (if any) is the one agent-coordinator
// itself runs on.
export interface SelfInfoMsg {
  dev_mode: boolean
  host_id: string
}

// ModeMismatchMsg discloses that a connected local-representative's dev-mode
// status differs from this agent-coordinator's own. mismatched: false clears
// a prior disclosure.
export interface ModeMismatchMsg {
  host_id: string
  mismatched: boolean
  peer_mode?: string
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

export interface FileInfo {
  id: string
  name: string
  size: number
  kind: string // "text" | "image" | "other" -- selects the wireframe icon
  state: string // "cached" | "held" | "persisted" -- selects the icon's color
  uploaded_at: number // unix seconds
  expires_at: number  // unix seconds; meaningless (0) once state is "persisted"
}

export interface LRFilesMsg {
  host_id: string
  active: boolean
  files?: FileInfo[]
}
