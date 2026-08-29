export interface Host {
  id: string
  label: string
  addr: string
  status: 'unknown' | 'connected' | 'disconnected'
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
