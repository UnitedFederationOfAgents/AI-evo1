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

// ConnectionInfo is one provider this host holds credentials for -- see
// connectivity.go. Only present providers are listed; status is "checking"
// until the first verification, then "ok" or "failed".
export interface ConnectionInfo {
  provider: string // "aws" | "github"
  status: string   // "checking" | "ok" | "failed"
  principal?: string // AWS caller ARN / GitHub login, once verified
  source?: string    // where the credentials came from, e.g. "TF_VAR_github_pat"
  detail?: string    // why the last check failed
}

// AgentInfo is one agent in the "agents & models" section, as
// ambiguous-agent --inspect reports it (see local-representative/agents.go).
export interface AgentInfo {
  name: string
  command: string
  installed: boolean
  path?: string
  version?: string
  model_selection: boolean
  default_model?: string
  models?: string[]
  detail?: string // why listing its models failed
  // Where a connectable agent's credential comes from (Step1SubstepEPrompt.md
  // Revision D): "connected", the env var's name when LR's environment sets
  // it, or absent.
  auth?: string
}

export interface AgentsState {
  status: string // "checking" | "ok" | "failed"
  detail?: string
  default_agent?: string
  agents: AgentInfo[]
  checked_at?: number
}

export interface SystemStateMsg {
  self: ProcInfo
  managed: ProcInfo[]
  connections?: ConnectionInfo[]
  agents?: AgentsState
  agent_ops?: AgentOp[]
}

// AgentOp is one agent call in the "agent operations" section, from its
// agent-records writing file (see local-representative/agentops.go).
// ended_at is absent while it runs; it's dropped 5 minutes after.
export interface AgentOp {
  id: string
  session: string
  session_name?: string
  agent: string // "" from an older clauditable
  model?: string
  command: string
  started_at: number
  ended_at?: number
  response?: string // once finished, from its -processed.txt (last 16 KiB)
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

// ConvoStateMsg is the "convo-state" payload this LR forwards from its managed
// the-conversationalist (see main.go). Empty (no http_port) once TC
// disconnects. Only started_at is read here, to announce a TC start/restart
// (see tcannounce.ts and condocs/initialRobotImpls/Step5Prompt.md, Revision D).
export interface ConvoStateMsg {
  http_port?: string
  transcribe_available?: boolean
  transcribe_reason?: string
  started_at?: number
}

export interface FileInfo {
  id: string
  name: string
  size: number
  kind: string // "text" | "image" | "archive" | "other" -- selects the wireframe icon
  state: string // "cached" | "held" | "persisted" -- selects the icon's color
  uploaded_at: number // unix seconds
  expires_at: number  // unix seconds; meaningless (0) once state is "persisted"
  highlighted: boolean // plain operator-set toggle, independent of state
  marked_up: boolean  // an in-progress markup-dialog session (arrow/rect/text) is open on this file
  sequence?: string   // "control" | "robot": YAML that validates for import as that sequence (seqfiles.go) -- its own icon
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
  build_failed?: boolean // from a failed rebuild until the next successful one
  // True while condoccer's '.condoc' lock file sits at the repo root (see
  // condocs/initialDistributedDevelopmentImpls/Step5Prompt.md) -- forces
  // rebuild_ready false regardless of dirty/head, so a condoc mid-transition
  // is never rebuilt out from under.
  condoc_locked?: boolean
  // The "dev branch" being watched ('' / absent when HEAD is detached),
  // every branch a "switch" may pick from, and the last refused/failed
  // switch -- Step1SubstepCPrompt.md Revision B.
  branch?: string
  branches?: string[]
  switch_error?: string
  // "ufabuild watch" (local-representative/ufabuild.go), read-only -- set by
  // LR's --ufabuild-watch launch flag, with no runtime toggle for now: when on,
  // rebuild_ready means a new .ufabuild request rather than HEAD moving, and
  // it builds without waiting on auto-rebuild. ufabuild_request is the short
  // SHA of the commit holding the current valid request; ufabuild_note says
  // why there isn't one.
  ufabuild_watch?: boolean
  ufabuild_request?: string
  ufabuild_note?: string
}

// The worker tab (condocs/initialRobotImpls/Step5SubstepGPrompt.md): what the
// host's dungeon-keeper reports -- see local-representative/worker.go.
export interface WorkerSignalInfo {
  id: string
  file: string
  role: string
  prompt: string
  agent?: string
  model?: string
  mode?: string
  work_type: string
  work_location?: string
  status: string // "pending" | "processing" | "completed" | "failed"
  created_at: number // unix ms
  started_at?: number
  completed_at?: number
  // the agent type it's for, and for slopspace work the slopspace it ran in
  // (both absent from a dungeon-keeper older than the visual sub-tab)
  agent_type?: string
  slopspace_id?: string
  comment?: string
  output?: string
  output_trimmed?: boolean // only the end of the output is included, or none of it if empty
}

export interface WorkerStateMsg {
  worker_id: string
  agent_type: string
  busy: boolean
  signals: WorkerSignalInfo[]
  // absent from a dungeon-keeper older than the slopspace view
  slopspaces?: SlopspaceInfo[]
  slopspaces_omitted?: number
}

export interface SlopspaceInfo {
  id: string
  created_at: number
  iteration: number
  deployed: boolean
  agent_type?: string
  in_use: boolean
  repos?: string[]
  // its directory under the slopspaces dir, and where its spaces are while
  // deployed (both absent from a dungeon-keeper older than the path field)
  path?: string
  deploy_path?: string
  // its deployment cycles, oldest first (absent from an older dungeon-keeper)
  deployments?: DeploymentInfo[]
}

export interface DeploymentInfo {
  agent_type: string
  deployed_at: number
  returned_at?: number // absent while still deployed
}

export interface WorkerReplyMsg {
  ok: boolean
  error?: string
}

// The slopspace sub-tab's manual control panel -- see worker.go and
// dungeon-keeper/slopctl.go.
export interface WorkerSlopspaceRequest {
  op: 'create' | 'deploy' | 'return' | 'delete' | 'add-repo' | 'write' | 'command'
  id?: string
  agent_type?: string
  repo?: string    // owner/name: add-repo; write (empty for every repo)
  branch?: string  // add-repo: the new branch
  message?: string // write: the commit message
  prompt?: string  // command: what to ask the agent in the deployed slopspace
}

export interface WorkerSlopspaceReply {
  op: string
  id?: string
  ok: boolean
  error?: string
  detail?: string
}
