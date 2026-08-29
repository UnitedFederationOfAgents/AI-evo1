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
}
