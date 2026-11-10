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

// ---- Diagrams (see ../diagram.go and ../api.go) ----

export type Shape = 'rect' | 'round' | 'circle' | 'cylinder'

// DiagramNode is a box on the canvas; x, y is its top-left corner.
export interface DiagramNode {
  id: string
  label: string
  shape: Shape
  color?: string
  x: number
  y: number
  w: number
  h: number
}

export interface DiagramEdge {
  from: string
  to: string
  label?: string
}

// DiagramStep is one frame of a dynamic diagram: it highlights node ids and
// "from->to" edges, and dims the rest.
export interface DiagramStep {
  caption?: string
  highlight: string[]
}

export interface Diagram {
  name: string
  comment?: string
  title: string
  description?: string
  width: number
  height: number
  nodes: DiagramNode[]
  edges: DiagramEdge[]
  steps: DiagramStep[]
}

// DiagramSummary is one entry in the sidebar's list (GET /api/diagrams).
export interface DiagramSummary {
  name: string
  title: string
  steps: number
}

// DiagramView is a diagram with its DTT document and D2 export
// (GET /api/diagrams/<name>, POST /api/convert).
export interface DiagramView {
  diagram: Diagram
  dtt: string
  d2: string
}
