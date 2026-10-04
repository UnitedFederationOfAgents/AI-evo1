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

// CaptureResultMsg is the "capture-result" payload reporting a completed
// native or browser capture (see robot.go).
export interface CaptureResultMsg {
  source: 'native' | 'browser'
  success: boolean
  image_url?: string
  error?: string
}

// CircleMouseResultMsg is the "circle-mouse-result" payload reporting
// whether the circle-mouse action completed (see robot.go).
export interface CircleMouseResultMsg {
  success: boolean
  via?: string // which input path drove the pointer
  error?: string
}

// ClipFrame is one sampled frame of a fallback native clip, at_ms after the
// clip started (see clip.go).
export interface ClipFrame {
  image_url: string
  at_ms: number
}

// ClipResultMsg is the "clip-result" payload reporting a completed native
// clip (see clip.go). On success exactly one of video_url (compositor
// recording) or frames (sampled fallback) is set.
export interface ClipResultMsg {
  success: boolean
  via?: string // which capture path recorded the clip
  video_url?: string
  frames?: ClipFrame[]
  duration_ms?: number
  error?: string
}

export type ServerMsg =
  | { type: 'repr-status'; payload: ReprStatusMsg }
  | { type: 'self-info'; payload: SelfInfoMsg }
  | { type: 'mode-mismatch'; payload: ModeMismatchMsg }
  | { type: 'capture-result'; payload: CaptureResultMsg }
  | { type: 'circle-mouse-result'; payload: CircleMouseResultMsg }
  | { type: 'clip-result'; payload: ClipResultMsg }
