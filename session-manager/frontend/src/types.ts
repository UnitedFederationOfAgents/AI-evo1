export type ReprStatus = 'disconnected' | 'connecting' | 'connected'

export interface ReprStatusMsg {
  status: ReprStatus
  host?: string
  port?: string
  // auto_connect is the persistent auto-connect toggle: true whenever the
  // cycle is armed, whether or not it's currently connected/connecting -- it
  // stays true across a successful connection, and only an explicit
  // disconnect turns it off.
  auto_connect?: boolean
}

// SelfInfoMsg discloses this instance's own dev-mode status and build
// version (see docs/DevMode.md) -- sent once when the WebSocket connects.
export interface SelfInfoMsg {
  dev_mode: boolean
  version: string
}

// ModeMismatchMsg discloses that the connected local-representative's
// dev-mode status differs from this instance's own. mismatched: false clears
// a prior disclosure.
export interface ModeMismatchMsg {
  mismatched: boolean
  peer_mode?: string
}

// ---- Session management ("ufa session" sub-menu parity, see sessions.go) ----

// SessionSummary is one row of the session list (the "sessions" payload).
// remote/host are set only for a session discovered on another host but not
// (yet) present in this host's own AGENT_RECORDS_PATH -- see sessions.go's
// listSessionsWithRemote.
export interface SessionSummary {
  id: string
  name: string
  file_count: number
  current: boolean
  remote?: boolean
  host?: string
}

// SessionsMsg is the "sessions" WebSocket payload: the full session list
// plus which one (if any) is current, pushed on connect and after any
// mutation (new/set/rename/archive).
export interface SessionsMsg {
  sessions: SessionSummary[]
  current: string
}

// SessionInfo is the "session-info" payload ("ufa session describe" parity):
// a session's on-disk location plus every field its session.yaml carries.
export interface SessionInfo {
  id: string
  location: string
  fields: [string, string][]
}

// SessionEntry is one readable turn of a session's transcript.
export interface SessionEntry {
  timestamp: string
  event_type?: string
  agent?: string
  model?: string
  duration_ms: number
  exit_code: number
  input?: string
  output?: string
  error?: string
}

// SessionView is the "session-view" payload: a session's transcript parsed
// out of session.jsonl into readable entries.
export interface SessionView {
  id: string
  name: string
  entries: SessionEntry[]
}

// ArchiveResultMsg is the "archive-result" payload ("ufa session archive"
// parity's outcome).
export interface ArchiveResultMsg {
  count: number
  path?: string
  error?: string
}

export type ServerMsg =
  | { type: 'repr-status'; payload: ReprStatusMsg }
  | { type: 'self-info'; payload: SelfInfoMsg }
  | { type: 'mode-mismatch'; payload: ModeMismatchMsg }
  | { type: 'sessions'; payload: SessionsMsg }
  | { type: 'session-info'; payload: SessionInfo }
  | { type: 'session-view'; payload: SessionView }
  | { type: 'archive-result'; payload: ArchiveResultMsg }
  | { type: 'error'; payload: string }
