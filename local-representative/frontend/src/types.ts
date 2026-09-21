export interface ServiceStatus {
  name: string
  status: string
}

export interface StatusMsg {
  services: ServiceStatus[]
}

export interface FCStateMsg {
  state: string // "remote-control", "local-control", or "" (disconnected)
}

export interface FCLogMsg {
  line: string
  kind?: string // "cmd" or "output"
}

export interface RidealongStateMsg {
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

export interface CondocStateMsg {
  active: boolean
  name?: string
  phase?: string
  step_num?: number
  status_msg?: string
}

export interface ACStateMsg {
  connected: boolean
  host?: string
  port?: string
  connecting?: boolean // background --auto-connect retry loop is still trying
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
  version?: string // this process's build version -- see docs/DevMode.md "Versioning". On self it's LR's own; on a managed instance it's whatever it last reported over representable, empty until it has
  update_available?: boolean // self only: the on-disk binary now answers --version differently than this running process -- see docs/DevMode.md "Loader"
}

export interface SystemStateMsg {
  self: ProcInfo
  managed: ProcInfo[]
}

// ModeMismatchMsg discloses that a connected peer's dev-mode status differs
// from this LR's own (see docs/DevMode.md). peer is "agent-coordinator" for
// the uplink or a representable client name ("federation-command",
// "condoccer") for a downlink; mismatched: false clears a prior disclosure.
export interface ModeMismatchMsg {
  peer: string
  mismatched: boolean
  peer_mode?: string
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

export interface FilesStateMsg {
  files: FileInfo[]
}

// RepoStateMsg mirrors local-representative's dev-repo watcher (--dev-repo,
// see docs/DevMode.md): the git repo LR was launched from, watched for
// rebuild-worthy changes. watched is false when LR wasn't launched with
// --dev-repo, in which case the rest of the fields are meaningless.
export interface RepoStateMsg {
  watched: boolean
  root?: string
  dirty: boolean          // uncommitted staged or unstaged changes relative to HEAD
  rebuild_ready: boolean  // the rebuild button is active -- dirty, or HEAD moved since the last successful rebuild
  building: boolean       // 'make deploy-dev-binaries' is running right now
  auto_rebuild: boolean
  head?: string
  last_error?: string
}
