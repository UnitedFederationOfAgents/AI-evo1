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
