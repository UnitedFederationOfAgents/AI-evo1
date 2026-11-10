export type ReprStatus = 'disconnected' | 'connecting' | 'connected'

export interface ReprStatusMsg {
  status: ReprStatus
  host?: string
  port?: string
  // auto_connect is the persistent auto-connect toggle (see ../repr.go's
  // setAutoConnect): true whenever the retry cycle is armed.
  auto_connect?: boolean
}

// SelfInfoMsg discloses this instance's own dev-mode status and build
// version (see docs/DevMode.md).
export interface SelfInfoMsg {
  dev_mode: boolean
  version: string
}

// ModeMismatchMsg discloses that the connected local-representative's
// dev-mode status differs from this instance's own.
export interface ModeMismatchMsg {
  mismatched: boolean
  peer_mode?: string
}

// ---- Compositions (../compositions.go) ----

export type VideoClipKind = 'title' | 'media'
export type AudioClipKind = 'media' | 'tone' | 'silence'
export type ClipKind = VideoClipKind | AudioClipKind
export type Track = 'video' | 'audio'

export interface Clip {
  kind: ClipKind
  // Seconds. Required for title, tone, silence and images; 0/absent on
  // other media means the file's own length.
  duration?: number
  text?: string
  color?: string
  background?: string
  frequency?: number
  file_id?: string
  file_name?: string
}

export interface RenderInfo {
  file_id: string
  name: string
  size: number
  at: number
  renderer?: 'ffmpeg' | 'browser' // what made it; absent on renders from before it was kept
}

export interface Composition {
  id: string
  name: string
  width: number
  height: number
  output?: 'video' | 'audio' // audio: only the audio track is recorded, into an audio-only WebM
  video: Clip[]
  audio: Clip[]
  updated_at?: number
  renders?: RenderInfo[]
}

export interface CompositionSummary {
  id: string
  name: string
  clips: number
  updated_at: number
  renders: number
}

export interface ExampleSummary {
  id: string
  name: string
  description: string
}

export interface CompositionsMsg {
  compositions: CompositionSummary[]
  examples: ExampleSummary[]
  error?: string
}

export interface SavedMsg {
  action: 'save' | 'example' | 'delete' | 'delete-all'
  id?: string
  count?: number // on 'delete-all': how many were deleted
  composition?: Composition
  error?: string
}

// ---- Sources and renders (../sources.go) ----

export type MediaKind = 'audio' | 'video' | 'image'

export interface Source {
  id: string
  name: string
  size: number
  media: MediaKind
  origin?: 'ianar' | 'polly' | 'composer'
  uploaded_at?: number
}

export interface SourcesMsg {
  sources: Source[]
  error?: string
}

// ---- Render jobs (../renderjobs.go) ----

export interface RenderJob {
  id: string
  composition: string
  name: string
  renderer: 'ffmpeg' | 'browser'
  state: 'queued' | 'waiting' | 'rendering' | 'done' | 'failed'
  error?: string
  created_at: number
}

export interface RenderJobsMsg {
  waiting: RenderJob[]
}

export interface RenderJobMsg {
  job: RenderJob
  composition: Composition
}

export interface RenderResultMsg {
  composition: string
  file_id?: string
  name?: string
  size?: number
  renderer?: 'ffmpeg' | 'browser'
  error?: string
}

// RenderCapabilityMsg (../ffmpeg.go) says whether this node has ffmpeg, so
// render jobs need no open tab.
export interface RenderCapabilityMsg {
  ffmpeg?: string // its path; absent when it isn't installed
  ffprobe?: string
  error?: string // why there's no ffmpeg
}
