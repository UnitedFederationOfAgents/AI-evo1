import { useRef, type PointerEvent as ReactPointerEvent } from 'react'
import type { Diagram, DiagramEdge, DiagramNode } from './types'

// DiagramSVG draws a diagram as a self-contained SVG: the canvas shows it,
// and every export (PNG, GIF, video, web page) is rendered from the same
// markup, so what's on the canvas is what gets exported. Colors are inline
// rather than CSS classes for that reason.

// Matches ../../d2.go's highlightColor.
export const HIGHLIGHT = '#e0a060'

const BG = '#1e1e1e'
const NODE_FILL = '#2a2a2a'
const NODE_STROKE = '#6a6a6a'
const EDGE = '#8a8a8a'
const TEXT = '#d4d4d4'
const DIM = 0.3
const FONT = "Menlo, Monaco, Consolas, monospace"

// CAPTION_H is the band under a dynamic diagram that carries the step's
// caption. Every step has it, so the frames are all the same size.
export const CAPTION_H = 40

const PARALLEL_GAP = 8 // how far apart a->b and b->a are drawn

// svgSize is the SVG's size for a diagram: its own, plus the caption band
// when it has steps.
export function svgSize(d: Diagram): { width: number; height: number } {
  return { width: d.width, height: d.height + (d.steps.length > 0 ? CAPTION_H : 0) }
}

interface DiagramSVGProps {
  diagram: Diagram
  // step is the step shown, or -1 for none (every node and edge at full
  // strength). A static diagram is always at -1.
  step: number
  // onNodeMove, when given, makes the nodes draggable: it's called with a
  // node's new top-left corner as it moves, and onNodeMoveEnd once it's let go.
  onNodeMove?: (id: string, x: number, y: number) => void
  onNodeMoveEnd?: () => void
  // progress, when given, is how far through the step the canvas is while
  // it plays (0 to 1): a dot travels along each highlighted edge. The video
  // export draws its frames with it too; the other exports leave it out and
  // draw each step still.
  progress?: number
}

export function DiagramSVG({ diagram, step, onNodeMove, onNodeMoveEnd, progress }: DiagramSVGProps) {
  const svgRef = useRef<SVGSVGElement>(null)
  const drag = useRef<{ id: string; dx: number; dy: number; pointer: number } | null>(null)
  const { width, height } = svgSize(diagram)
  const s = step >= 0 && step < diagram.steps.length ? diagram.steps[step] : null
  const lit = s ? new Set(s.highlight) : null

  const byId = new Map(diagram.nodes.map(n => [n.id, n]))
  const edgeIds = new Set(diagram.edges.map(e => `${e.from}->${e.to}`))

  // toSVG converts a pointer position to SVG user units.
  const toSVG = (clientX: number, clientY: number) => {
    const svg = svgRef.current
    const ctm = svg?.getScreenCTM()
    if (!svg || !ctm) return { x: clientX, y: clientY }
    const p = new DOMPoint(clientX, clientY).matrixTransform(ctm.inverse())
    return { x: p.x, y: p.y }
  }

  const onPointerDown = (n: DiagramNode) => (e: ReactPointerEvent) => {
    if (!onNodeMove) return
    const p = toSVG(e.clientX, e.clientY)
    drag.current = { id: n.id, dx: p.x - n.x, dy: p.y - n.y, pointer: e.pointerId }
    svgRef.current?.setPointerCapture(e.pointerId)
    e.preventDefault()
  }
  const onPointerMove = (e: ReactPointerEvent) => {
    const g = drag.current
    if (!g || !onNodeMove || e.pointerId !== g.pointer) return
    const p = toSVG(e.clientX, e.clientY)
    const n = byId.get(g.id)
    if (!n) return
    const x = Math.round(Math.min(Math.max(p.x - g.dx, 0), diagram.width - n.w))
    const y = Math.round(Math.min(Math.max(p.y - g.dy, 0), diagram.height - n.h))
    if (x !== n.x || y !== n.y) onNodeMove(g.id, x, y)
  }
  const onPointerUp = (e: ReactPointerEvent) => {
    if (!drag.current || e.pointerId !== drag.current.pointer) return
    drag.current = null
    svgRef.current?.releasePointerCapture(e.pointerId)
    onNodeMoveEnd?.()
  }

  return (
    <svg
      ref={svgRef}
      xmlns="http://www.w3.org/2000/svg"
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      fontFamily={FONT}
      onPointerMove={onNodeMove ? onPointerMove : undefined}
      onPointerUp={onNodeMove ? onPointerUp : undefined}
      onPointerCancel={onNodeMove ? onPointerUp : undefined}
    >
      <defs>
        <marker id="dg-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L10,5 L0,10 z" fill={EDGE} />
        </marker>
        <marker id="dg-arrow-lit" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L10,5 L0,10 z" fill={HIGHLIGHT} />
        </marker>
      </defs>
      <rect x={0} y={0} width={width} height={height} fill={BG} />
      <text x={14} y={22} fontSize={13} fill="#888">{diagram.title}</text>

      {diagram.edges.map((e, i) => {
        const from = byId.get(e.from)
        const to = byId.get(e.to)
        if (!from || !to) return null
        const id = `${e.from}->${e.to}`
        const on = !lit || lit.has(id)
        return (
          <EdgeShape
            key={`${id}-${i}`}
            edge={e}
            from={from}
            to={to}
            offset={edgeIds.has(`${e.to}->${e.from}`) ? PARALLEL_GAP / 2 : 0}
            lit={!!lit && on}
            dim={!on}
            progress={lit && on ? progress : undefined}
          />
        )
      })}

      {diagram.nodes.map(n => {
        const on = !lit || lit.has(n.id)
        return (
          <g
            key={n.id}
            opacity={on ? 1 : DIM}
            onPointerDown={onNodeMove ? onPointerDown(n) : undefined}
            style={onNodeMove ? { cursor: 'move' } : undefined}
          >
            <NodeShape node={n} stroke={lit && on ? HIGHLIGHT : NODE_STROKE} strokeWidth={lit && on ? 3 : 1.5} />
            <text
              x={n.x + n.w / 2}
              y={n.y + n.h / 2 + (n.shape === 'cylinder' ? 4 : 0)}
              fontSize={13}
              fill={TEXT}
              textAnchor="middle"
              dominantBaseline="central"
            >
              {n.label}
            </text>
          </g>
        )
      })}

      {diagram.steps.length > 0 && (
        <g>
          <line x1={0} y1={diagram.height} x2={width} y2={diagram.height} stroke="#333" />
          <text x={width / 2} y={diagram.height + CAPTION_H / 2} fontSize={13} fill={TEXT} textAnchor="middle" dominantBaseline="central">
            {s ? `${step + 1}/${diagram.steps.length}  ${s.caption ?? ''}` : `${diagram.steps.length} steps`}
          </text>
        </g>
      )}
    </svg>
  )
}

function NodeShape({ node: n, stroke, strokeWidth }: { node: DiagramNode; stroke: string; strokeWidth: number }) {
  const fill = n.color || NODE_FILL
  switch (n.shape) {
    case 'circle':
      return <ellipse cx={n.x + n.w / 2} cy={n.y + n.h / 2} rx={n.w / 2} ry={n.h / 2} fill={fill} stroke={stroke} strokeWidth={strokeWidth} />
    case 'cylinder': {
      const rx = n.w / 2
      const ry = Math.min(10, n.h / 4)
      const top = n.y + ry
      const bottom = n.y + n.h - ry
      const body = `M${n.x},${top} A${rx},${ry} 0 0 1 ${n.x + n.w},${top} V${bottom} A${rx},${ry} 0 0 1 ${n.x},${bottom} Z`
      const rim = `M${n.x},${top} A${rx},${ry} 0 0 0 ${n.x + n.w},${top}`
      return (
        <g>
          <path d={body} fill={fill} stroke={stroke} strokeWidth={strokeWidth} />
          <path d={rim} fill="none" stroke={stroke} strokeWidth={strokeWidth} />
        </g>
      )
    }
    default:
      return (
        <rect x={n.x} y={n.y} width={n.w} height={n.h} rx={n.shape === 'round' ? 14 : 2} fill={fill} stroke={stroke} strokeWidth={strokeWidth} />
      )
  }
}

// boundary is where the line from n's center in direction (dx, dy) leaves
// its shape.
function boundary(n: DiagramNode, cx: number, cy: number, dx: number, dy: number): [number, number] {
  const hw = n.w / 2
  const hh = n.h / 2
  let t: number
  if (n.shape === 'circle') {
    t = 1 / Math.sqrt((dx / hw) ** 2 + (dy / hh) ** 2)
  } else {
    t = Math.min(dx === 0 ? Infinity : hw / Math.abs(dx), dy === 0 ? Infinity : hh / Math.abs(dy))
  }
  return [cx + dx * t, cy + dy * t]
}

interface EdgeShapeProps {
  edge: DiagramEdge
  from: DiagramNode
  to: DiagramNode
  offset: number // shift to the right of travel, so a->b and b->a don't overlap
  lit: boolean
  dim: boolean
  progress?: number // where the travelling dot is, 0 to 1; no dot when unset
}

// MOTION_END is how far through a step the dot reaches the arrowhead; it
// rests there for the rest of the step.
const MOTION_END = 0.75

function EdgeShape({ edge, from, to, offset, lit, dim, progress }: EdgeShapeProps) {
  let ax = from.x + from.w / 2
  let ay = from.y + from.h / 2
  let bx = to.x + to.w / 2
  let by = to.y + to.h / 2
  const len = Math.hypot(bx - ax, by - ay)
  if (len === 0) return null
  const ux = (bx - ax) / len
  const uy = (by - ay) / len
  // Right of travel is (-uy, ux) in SVG's y-down coordinates.
  ax -= uy * offset
  ay += ux * offset
  bx -= uy * offset
  by += ux * offset
  const [x1, y1] = boundary(from, ax, ay, ux, uy)
  const [x2, y2] = boundary(to, bx, by, -ux, -uy)
  const color = lit ? HIGHLIGHT : EDGE
  const mx = (x1 + x2) / 2 - uy * (offset ? 10 : 0)
  const my = (y1 + y2) / 2 + ux * (offset ? 10 : 0)
  const labelW = (edge.label?.length ?? 0) * 7.2 + 10
  const t = progress === undefined ? null : Math.min(progress / MOTION_END, 1)
  return (
    <g opacity={dim ? DIM : 1}>
      <line
        x1={x1}
        y1={y1}
        x2={x2}
        y2={y2}
        stroke={color}
        strokeWidth={lit ? 2.5 : 1.5}
        markerEnd={lit ? 'url(#dg-arrow-lit)' : 'url(#dg-arrow)'}
      />
      {edge.label && (
        <g>
          <rect x={mx - labelW / 2} y={my - 9} width={labelW} height={18} rx={3} fill={BG} />
          <text x={mx} y={my} fontSize={12} fill={lit ? HIGHLIGHT : '#aaa'} textAnchor="middle" dominantBaseline="central">
            {edge.label}
          </text>
        </g>
      )}
      {t !== null && <circle cx={x1 + (x2 - x1) * t} cy={y1 + (y2 - y1) * t} r={5} fill={HIGHLIGHT} />}
    </g>
  )
}
