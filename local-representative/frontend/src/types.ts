export interface ServiceStatus {
  name: string
  status: string
}

export interface StatusMsg {
  services: ServiceStatus[]
}

// fc on the FC messages below is the federation-command instance they're
// about -- the name it connected to LR under ("federation-command#2" for one
// LR launched). See local-representative/fcinstances.go.
export interface FCStateMsg {
  fc?: string
  state: string // "remote-control", "local-control", or "" (disconnected)
}

export interface FCLogMsg {
  fc?: string
  line: string
  kind?: string // "cmd" or "output"
}

// FCInstanceInfo is one connected federation-command instance.
export interface FCInstanceInfo {
  key: string
  label: string // "#2", "@fc-ab12"
  instance_id?: string
  head?: string
  state: string
  session?: string
  ridealong?: RidealongStateMsg
  condoc?: CondocStateMsg
}

export interface FCInstancesMsg {
  instances: FCInstanceInfo[]
}

// Control tab -- see local-representative/control.go and controlTypes.ts.
export type {
  ControlStepInfo, ControlSequenceInfo, ControlStepResult, ControlRunMsg, ControlRecording, ControlStateMsg,
  ControlLibraryMsg, ControlLibReply, ControlLibRequest,
} from './controlTypes'

export interface RidealongStateMsg {
  fc?: string
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
  fc?: string
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
  connecting?: boolean // background --auto-connect retry loop is currently trying
  // auto_connect is the persistent auto-connect toggle: true whenever the
  // cycle is armed, whether or not it's currently connected/connecting -- it
  // stays true across a successful connection, and only an explicit
  // disconnect turns it off.
  auto_connect?: boolean
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
  auto_update?: boolean // self only: restart automatically the moment update_available goes true, instead of waiting for the "update and restart" button -- see docs/DevMode.md "Loader"
  session?: string // federation-command only: the session it's currently on (display name if session.yaml has one, otherwise the bare id) -- empty until an instance has connected and reported
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

// TCAvailabilityMsg is the aggregate answer to "is a the-conversationalist
// instance available on any host", relayed down from agent-coordinator's own
// aggregate -- see tcavailability.go. Drives the mic icon shown beside the
// camera/screenshot icon in the header (illuminated when available) -- see
// condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step2Prompt.md.
export interface TCAvailabilityMsg {
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
