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
}

export interface LRSystemStateMsg {
  host_id: string
  active: boolean
  self: ProcInfo
  managed: ProcInfo[]
}
