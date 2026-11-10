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
// "condocv2") for a downlink; mismatched: false clears a prior disclosure.
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
  rebuild_ready: boolean  // the rebuild button is active -- a .ufabuild request is waiting, and the repo isn't dirty
  building: boolean       // 'make deploy-dev-binaries' is running right now
  auto_rebuild: boolean
  // 90s auto-rebuild debounce timer (Step3Prompt.md Revision E): pending is
  // true from the moment a .ufabuild request is ready with auto-rebuild on,
  // until the debounced build actually starts; seconds counts down and is
  // re-armed to 90 whenever a further request lands.
  auto_rebuild_pending?: boolean
  auto_rebuild_seconds?: number
  head?: string
  last_error?: string
  build_failed?: boolean // from a failed rebuild until the next successful one
  // The "dev branch" being watched ('' / absent when HEAD is detached),
  // every branch a "switch" may pick from, and the last refused/failed
  // switch -- Step1SubstepCPrompt.md Revision B.
  branch?: string
  branches?: string[]
  switch_error?: string
  // The watched repo's gitignored .ufabuild build request
  // (local-representative/ufabuild.go), written when a pulled commit carries
  // a [condocv2-xxxx] tag: ufabuild_request is the short commit it asks to
  // build ("pending" for one naming none), ufabuild_note what's notable when
  // there isn't one (or when its last build failed).
  ufabuild_request?: string
  ufabuild_note?: string
  // The core repo's per-sub-app build (local-representative/ufaplan.go):
  // build_plan is what the pending request would rebuild (sub-apps whose
  // inputs changed since each was last built), build_plan_note what's
  // notable about it (nothing changed, or it couldn't be worked out and a
  // full deploy-dev-binaries will run). build_apps/build_full/build_managed
  // are what the running build -- or the last one -- covers: sub-apps, a
  // full build, and the system-tab/timeline names they show as. restarted/
  // restart_note are what the last successful build restarted.
  build_plan?: string[]
  build_plan_note?: string
  build_apps?: string[]
  build_full?: boolean
  build_managed?: string[]
  restarted?: string[]
  restart_note?: string
  // Directories beside the watched repo registered for watching
  // (local-representative/extwatch.go) -- watched only, never built, for now
  // -- and the last refused/failed (un)registration. Per
  // condocs/privateRepoOpsImpls/Step6Prompt.md Revision C.
  externals?: ExternalWatchState[]
  external_error?: string
}

export interface ExternalWatchState {
  path: string // as registered: relative to the watched repo's root, or absolute
  abs: string
  registered_at: number // unix seconds
  exists: boolean
  git: boolean  // the root of its own git repo, with at least one commit
  branch?: string
  head?: string
  dirty: boolean
  changed_at?: number // unix seconds head/dirty last moved; absent if not since LR began watching
  note?: string       // why it can't be fully read
  checked_at?: number
  // A plug-in dev repo (local-representative/extbuild.go, Step6Prompt.md
  // Revision D): pulled, built from a gitignored .ufabuild request written
  // when condoc-tagged commits come in, and its plug-in approved here.
  plugin?: boolean
  app_id?: string
  plugin_note?: string // how approving app_id went
  pull_note?: string   // why the last poll didn't pull, if it didn't
  ufabuild?: string    // the pending request's short commit, or "pending"
  building?: boolean
  built_head?: string
  build_at?: number    // unix seconds the last build finished
  build_error?: string
  // Its plug-in's lifecycle (extlaunch.go, Revision F): a managed app
  // launched under app_id from its deployed binary, restarted after builds.
  bin?: string         // where builds are deployed: /AI-evo1-dev/plugin/<app-id>
  deployed?: boolean   // a build is there to launch
  launch_note?: string // what the last build did to the running instance
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
  // the DTT proposals its last return left awaiting review (it can't be
  // deployed again until they're answered), and how many more weren't listed
  pending_proposals?: string[]
  proposals_omitted?: number
  // whether it deploys (or was deployed) with writespaces/dtt-requests for
  // DTT proposals (absent from an older dungeon-keeper)
  dtt_proposals?: boolean
}

export type ProposalDecision = 'accept' | 'reject' | 'modify'

export interface DeploymentInfo {
  agent_type: string
  deployed_at: number
  returned_at?: number // absent while still deployed
  // the DTT proposals it returned with, and the human's answer to them
  // (decision is absent while they await review; mixed when they were
  // answered one at a time, some each way -- accepted/rejected count those)
  proposals?: number
  accepted?: number
  rejected?: number
  decision?: ProposalDecision | 'mixed'
  decided_at?: number
  note?: string
}

export interface WorkerReplyMsg {
  ok: boolean
  error?: string
}

// The slopspace sub-tab's manual control panel -- see worker.go and
// dungeon-keeper/slopctl.go.
export interface WorkerSlopspaceRequest {
  op: 'create' | 'deploy' | 'return' | 'delete' | 'add-repo' | 'write' | 'command' | 'review' | 'files'
  id?: string
  agent_type?: string
  repo?: string    // owner/name: add-repo; write (empty for every repo)
  branch?: string  // add-repo: the new branch
  message?: string // write: the commit message
  prompt?: string  // command: what to ask the agent in the deployed slopspace
  decision?: ProposalDecision // review
  note?: string    // review: what to change (modify), or why
  proposals?: string[] // review: accept or reject only these (absent: all of them)
  path?: string    // files: relative to its spaces; '' for the top
  dtt_proposals?: boolean // deploy: offer DTT proposals this deployment (absent keeps its setting)
}

export interface WorkerSlopspaceReply {
  op: string
  id?: string
  ok: boolean
  error?: string
  detail?: string
  path?: string                // files: the path asked for
  files?: SlopspaceFileListing // files: what's there
}

// A "files" answer -- see dungeon-keeper/pkg/slopspace/files.go.
export interface SlopspaceFileListing {
  path: string
  root: string
  dir: boolean
  entries?: { name: string; dir?: boolean; size?: number }[]
  omitted?: number
  size?: number
  content?: string
  truncated?: boolean
  binary?: boolean
}

// The "plugins-state" message: this LR's plug-in registry -- see
// local-representative/plugins.go (PluginsStateMsg). Mirrors
// agent-coordinator's copy; only the fields this dashboard reads.
export interface UIManifest {
  manifest_version: number
  app_id: string
  display_name: string
  short_name?: string
  entry?: string
  icon?: string
  order?: number
  health_key?: string
  version?: string // the plug-in's own build/release, any manifest version (Step3Prompt.md Revision G)
  // Version 2 (Step3Prompt.md Revision E); only read when manifest_version >= 2.
  icon_mask?: boolean // a monochrome icon: drawn in the tab's own colour
  description?: string
}

export interface PluginInfo {
  app_id: string
  status: string // "online" | "offline" | "pending"
  trust: string  // "first-party" | "sandboxed" -- see docs/PluginTrust.md
  source?: string // "hello" | "manual" (Revision E)
  hello: { display_name: string; http_port: string }
  manifest?: UIManifest
  manifest_error?: string
  base_path: string
  hello_at?: number // unix seconds
  manifest_at?: number
  disconnect_at?: number
}

// A manual registration (Revision E) -- plugins.go's ManualPlugin.
export interface ManualPlugin {
  app_id: string
  http_port: string
  display_name?: string
  registered_at: number
}

export interface PluginsStateMsg {
  config?: {
    external_approved?: string[]
    approvals_path?: string
    approvals_error?: string
    manual?: ManualPlugin[] | null
    manual_poll_secs?: number
    example_app_id?: string
    example_port?: string
  }
  plugins: PluginInfo[] | null
  example?: { at?: number; result?: string; error?: string }
}
