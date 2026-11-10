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
// version (see docs/DevMode.md) -- sent once when the WebSocket connects --
// and when the server process started (unix ms), used to announce a
// start/restart (see tcannounce.ts) -- only sent while announce_start, the
// sidebar's "announce start" toggle, is on (../../announce.go). Sent again
// whenever that toggle changes.
export interface SelfInfoMsg {
  dev_mode: boolean
  version: string
  started_at?: number
  announce_start?: boolean
}

// ModeMismatchMsg discloses that the connected local-representative's
// dev-mode status differs from this instance's own. mismatched: false clears
// a prior disclosure.
export interface ModeMismatchMsg {
  mismatched: boolean
  peer_mode?: string
}

// TranscriptMsg is the "transcript" WebSocket payload pushed as AWS
// Transcribe returns partial/final results for the in-progress recording
// (see transcribe.go).
export interface TranscriptMsg {
  text: string
  is_final: boolean
}

// SaveResultMsg is the "save-result" WebSocket payload reporting whether the
// accumulated transcript was written into local-representative's files area
// (see transcribe.go's saveTranscript).
export interface SaveResultMsg {
  success: boolean
  error?: string
  file_id?: string
  name?: string
}

// NotepadMsg is the "notepad" WebSocket payload carrying the shared
// notepad's full current text (see notepad.go) -- sent on connect and
// whenever another client edits it. rev increments on every accepted edit.
export interface NotepadMsg {
  text: string
  rev: number
}

// TranscribeStatusMsg is the "transcribe-status" WebSocket payload: whether
// this instance can currently reach its transcription engine (so far only
// AWS Transcribe -- see transcribe_status.go). Sent on connect and whenever a
// check finishes; reason explains an "unavailable" state.
export type TranscribeState = 'checking' | 'available' | 'unavailable'

export interface TranscribeStatusMsg {
  state: TranscribeState
  engine: string
  region?: string
  reason?: string
  checked_at?: string
  identity?: string // ARN AWS reports for the credentials in use
  credential_source?: string // where the server's AWS SDK found them
  warning?: string // non-fatal, shown with any state (e.g. a defaulted region)
}

// NotesSource names which panel's "To Notes" button an "append-notes"
// request came from (see notes.go).
export type NotesSource = 'notepad' | 'transcript'

// NotesResultMsg is the "notes-result" WebSocket payload answering an
// "append-notes" request: whether the text was appended to the notes file
// at path.
export interface NotesResultMsg {
  source: NotesSource
  success: boolean
  error?: string
  path?: string
}

// PollyAudioMsg is the "polly-audio" WebSocket payload answering a
// "polly-speak" request (see polly.go): base64 audio on success, else error.
// id echoes the request's id.
export interface PollyAudioMsg {
  id: number
  audio?: string
  content_type?: string
  voice?: string
  error?: string
  // A request with save: where the clip was kept in local-representative's
  // files area, or why it wasn't.
  saved_as?: string
  file_id?: string
  save_error?: string
  // The recent clip it is (see pollyconfig.go): its configuration and cost,
  // and whether a recent clip was reused rather than asking Polly (a replay
  // is one too).
  clip?: number
  config?: string
  engine?: string
  characters?: number
  cost?: string
  reused?: boolean
}

// PollyPlayMsg is the "polly-play" WebSocket payload (see pollyplay.go): a
// clip made through the HTTP API with play, for this tab to play out loud
// after any it's still playing.
export interface PollyPlayMsg {
  audio: string // base64
  content_type: string
  clip: number
  config?: string
  voice?: string
  engine?: string
  cost?: string
  reused?: boolean
  saved_as?: string
}

// PollyConfig is one named Polly voice and engine (see pollyconfig.go).
export interface PollyConfig {
  name: string
  voice: string
  engine: string
  region?: string // the AWS region Polly is asked in; unset for the AWS config's
  description?: string
  preset?: boolean // built in
  edited?: boolean // a preset changed from its built-in values
}

// PollyClipView is a recent clip, without its audio.
export interface PollyClipView {
  id: number
  at: number // unix ms it was made
  last_used: number
  text: string
  config: string
  voice: string
  engine: string
  region: string // where Polly was asked
  characters: number
  cost_usd: number
  cost: string
  uses: number // all but the first reused it
  size: number
  content_type: string
  from: string // notepad or api
  saved_as?: string
  file_id?: string
}

// PollyStateMsg is the "polly-state" WebSocket payload: the configurations,
// the active one, the recent clips and each engine's price (US dollars per
// million characters). error is a change this tab asked for and was refused.
export interface PollyStateMsg {
  active: string
  configs: PollyConfig[]
  clips: PollyClipView[]
  prices: Record<string, number>
  engines: string[]
  error?: string
}

export type ServerMsg =
  | { type: 'repr-status'; payload: ReprStatusMsg }
  | { type: 'self-info'; payload: SelfInfoMsg }
  | { type: 'mode-mismatch'; payload: ModeMismatchMsg }
  | { type: 'transcript'; payload: TranscriptMsg }
  | { type: 'save-result'; payload: SaveResultMsg }
  | { type: 'notepad'; payload: NotepadMsg }
  | { type: 'transcribe-status'; payload: TranscribeStatusMsg }
  | { type: 'notes-result'; payload: NotesResultMsg }
  | { type: 'polly-audio'; payload: PollyAudioMsg }
  | { type: 'polly-state'; payload: PollyStateMsg }
  | { type: 'polly-play'; payload: PollyPlayMsg }
  | { type: 'error'; payload: string }
