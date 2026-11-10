import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { DemoCoverage, ProtosurfMap, ToolsReply, ToolsRequest, ToolsRequestFn } from './Tools'

// The demo tab's protosurf map (PrivateRepoOps Step 5, Revision B): the
// prototype surface, the content of the project the host is developing
// (AI-evo1-spiral) as a treemap -- a tile per file, its area the file's
// lines, nested in its directories -- coloured by language, by the part it
// plays in the system, or by density. The host scans the project on
// request (local-representative's protosurf.go). Clicking a tile zooms one
// directory in; the trail above the map zooms back out. Demo and test
// coverage are to be drawn on this same map. Kept identical in
// local-representative and agent-coordinator.
//
// Revision C: selecting a demo or a suite on the demo tab picks out its
// declared coverage (local-representative's democoverage.go) on the map --
// the focal files, what it's about, in one colour, the supporting ones,
// what it needs on the way, in another, and the rest receding -- with the
// lines of each counted in the legend.

type ColourBy = 'language' | 'role' | 'density' | 'coverage'

// coverage is offered only while a demo or suite is picked out.
const COLOUR_BY: { key: ColourBy; label: string; help: string }[] = [
  { key: 'language', label: 'language', help: 'each file by its language, from its extension' },
  { key: 'role', label: 'system role', help: 'each file by the part it plays: service code, frontend, tests, condocs, docs, dtt declarations, build and config' },
  { key: 'density', label: 'density', help: 'each file by the share of its lines that carry content -- neither blank nor comments' },
  { key: 'coverage', label: 'coverage', help: 'the selected demo’s or suite’s declared coverage: focal files in one colour, supporting ones in another' },
]

// The dark categorical slots, in their fixed order (the dataviz palette's,
// validated on a dark surface); OTHER is the neutral every unnamed category
// folds into. A category keeps its slot whatever's on the map, so a colour
// always means the same thing.
const SLOTS = ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9']
const OTHER = '#6b6b68'

const LANG_ORDER = ['go', 'typescript', 'markdown', 'yaml', 'css', 'shell', 'json']
const ROLE_ORDER = ['service', 'frontend', 'test', 'condoc', 'docs', 'declaration', 'build']

const ROLE_LABEL: Record<string, string> = {
  service: 'service code', frontend: 'frontend', test: 'tests', condoc: 'condocs', docs: 'docs',
  declaration: 'dtt declarations', build: 'build and config', other: 'other',
}

// DENSITY is one blue ramp, dark (little content, receding into the
// surface) to light (dense), over seven equal bins of the content share.
const DENSITY = ['#0d366b', '#184f95', '#256abf', '#3987e5', '#6da7ec', '#9ec5f4', '#cde2fb']

// The coverage colours: focal and supporting take two slots far apart,
// and what neither covers recedes into the surface.
type Cover = 'focal' | 'supporting'
const COVER: Record<Cover, string> = { focal: SLOTS[1], supporting: SLOTS[0] }
const UNCOVERED = '#2e2e2c'
const COVER_LABEL: Record<Cover, string> = { focal: 'focal -- what it’s about', supporting: 'supporting -- what it needs on the way' }

function slotColour(order: string[], name: string): string {
  const i = order.indexOf(name)
  return i >= 0 ? SLOTS[i] : OTHER
}

function densityColour(share: number): string {
  return DENSITY[Math.min(DENSITY.length - 1, Math.max(0, Math.floor(share * DENSITY.length)))]
}

// ---- the tree ----

type Leaf = { kind: 'file'; name: string; path: string; lang: string; role: string; lines: number; code: number; comment: number; folded: number }
type Dir = {
  kind: 'dir'; name: string; path: string; lines: number; code: number; files: number
  dirs: Map<string, Dir>; leaves: Leaf[]
  byLang: Record<string, number>; byRole: Record<string, number>
}
type Node = Leaf | Dir

function newDir(name: string, path: string): Dir {
  return { kind: 'dir', name, path, lines: 0, code: 0, files: 0, dirs: new Map(), leaves: [], byLang: {}, byRole: {} }
}

function buildTree(m: ProtosurfMap): Dir {
  const root = newDir('', '')
  for (const [di, name, li, ri, lines, code, comment, folded] of m.files) {
    const dir = m.dirs[di] ?? ''
    const leaf: Leaf = { kind: 'file', name, path: dir ? `${dir}/${name}` : name, lang: m.langs[li] ?? 'other', role: m.roles[ri] ?? 'other', lines, code, comment, folded: folded ?? 0 }
    const chain = [root]
    let at = root
    for (const seg of dir ? dir.split('/') : []) {
      let next = at.dirs.get(seg)
      if (!next) {
        next = newDir(seg, at.path ? `${at.path}/${seg}` : seg)
        at.dirs.set(seg, next)
      }
      at = next
      chain.push(at)
    }
    at.leaves.push(leaf)
    for (const d of chain) {
      d.lines += lines
      d.code += code
      d.files += Math.max(1, leaf.folded)
      d.byLang[leaf.lang] = (d.byLang[leaf.lang] ?? 0) + lines
      d.byRole[leaf.role] = (d.byRole[leaf.role] ?? 0) + lines
    }
  }
  return root
}

function children(d: Dir): Node[] {
  return [...d.dirs.values(), ...d.leaves].filter(n => n.lines > 0).sort((a, b) => b.lines - a.lines)
}

function findDir(root: Dir, path: string): Dir | undefined {
  let at: Dir | undefined = root
  for (const seg of path ? path.split('/') : []) at = at?.dirs.get(seg)
  return at
}

function dominant(by: Record<string, number>): string {
  let best = 'other', n = -1
  for (const [k, v] of Object.entries(by)) if (v > n) { best = k; n = v }
  return best
}

// ---- the layout ----

type Rect = { x: number; y: number; w: number; h: number }

// squarify lays nodes (largest first) into r, rows of tiles kept as near
// square as they'll go (Bruls, Huizing and van Wijk's squarified treemap).
function squarify(nodes: Node[], r: Rect): { n: Node; r: Rect }[] {
  const out: { n: Node; r: Rect }[] = []
  const total = nodes.reduce((s, n) => s + n.lines, 0)
  if (total <= 0 || r.w <= 0 || r.h <= 0) return out
  const scale = (r.w * r.h) / total
  const rest = nodes.map(n => ({ n, a: n.lines * scale }))
  let { x, y, w, h } = r
  let i = 0
  const worst = (sum: number, max: number, min: number, side: number) =>
    Math.max((side * side * max) / (sum * sum), (sum * sum) / (side * side * min))
  while (i < rest.length) {
    const side = Math.min(w, h)
    let j = i + 1
    let sum = rest[i].a
    while (j < rest.length) {
      const next = sum + rest[j].a
      if (worst(next, rest[i].a, rest[j].a, side) > worst(sum, rest[i].a, rest[j - 1].a, side)) break
      sum = next
      j++
    }
    const thick = sum / side
    let off = 0
    for (let k = i; k < j; k++) {
      const len = rest[k].a / thick
      out.push({ n: rest[k].n, r: w >= h ? { x, y: y + off, w: thick, h: len } : { x: x + off, y, w: len, h: thick } })
      off += len
    }
    if (w >= h) { x += thick; w -= thick } else { y += thick; h -= thick }
    i = j
  }
  return out
}

// A directory big enough gets a header strip with its name; one too small
// to open is drawn as one tile, coloured as its content is overall.
const HEADER = 13
const MIN_OPEN = 18
const MAX_TILES = 5000

type Tile = { n: Node; r: Rect; depth: number; open: boolean; top: string }

// layout tiles d's content into r: each child of the focus remembers which
// of them it's under (top), for a click to zoom into it.
function layout(d: Dir, r: Rect): Tile[] {
  const tiles: Tile[] = []
  const walk = (dir: Dir, rect: Rect, depth: number, top: string) => {
    for (const { n, r: c } of squarify(children(dir), rect)) {
      if (tiles.length >= MAX_TILES) return
      const t = depth === 0 ? n.path : top
      const open = n.kind === 'dir' && c.w >= MIN_OPEN && c.h >= MIN_OPEN + HEADER
      tiles.push({ n, r: c, depth, open, top: t })
      if (open && n.kind === 'dir') {
        walk(n, { x: c.x + 2, y: c.y + HEADER, w: c.w - 4, h: c.h - HEADER - 2 }, depth + 1, t)
      }
    }
  }
  walk(d, r, 0, '')
  return tiles
}

// ---- the colours ----

function categoryOf(n: Node, by: 'language' | 'role'): string {
  if (n.kind === 'file') return by === 'language' ? n.lang : n.role
  return dominant(by === 'language' ? n.byLang : n.byRole)
}

function shareOf(n: Node): number {
  return n.lines > 0 ? n.code / n.lines : 0
}

function fillOf(n: Node, by: 'language' | 'role' | 'density'): string {
  if (by === 'density') return densityColour(shareOf(n))
  return slotColour(by === 'language' ? LANG_ORDER : ROLE_ORDER, categoryOf(n, by))
}

// coverFill is a tile's colour by coverage: a file's own, or a directory
// too small to open in the colour of what it covers -- focal before
// supporting -- partial unless it's all covered.
function coverFill(n: Node, c: DemoCoverage, tally: Map<Dir, Tally>): { fill: string; partial: boolean; cover: Cover | null } {
  if (n.kind === 'file') {
    const k = coverOfPath(c, n.path) ?? (n.folded ? coverOfFolded(c, n.path) : null)
    return { fill: k ? COVER[k] : UNCOVERED, partial: !!k && !coverOfPath(c, n.path), cover: k }
  }
  const whole = coverOfPath(c, `${n.path}/`)
  if (whole === 'focal') return { fill: COVER.focal, partial: false, cover: 'focal' }
  const t = tally.get(n)
  if (t && t.focal > 0) return { fill: COVER.focal, partial: true, cover: 'focal' }
  if (whole) return { fill: COVER[whole], partial: false, cover: whole }
  if (!t || t.supporting === 0) return { fill: UNCOVERED, partial: false, cover: null }
  return { fill: COVER.supporting, partial: true, cover: 'supporting' }
}

// coverOfFolded is what a "+N files" tile may hold: a declared file
// directly in its directory, folded in with the other small ones.
function coverOfFolded(c: DemoCoverage, foldedPath: string): Cover | null {
  const dir = foldedPath.slice(0, foldedPath.lastIndexOf('/') + 1)
  const inDir = (paths: string[]) => paths.some(p => !p.endsWith('/') && p.startsWith(dir) && !p.slice(dir.length).includes('/'))
  return inDir(c.focal) ? 'focal' : inDir(c.supporting) ? 'supporting' : null
}

function categoryLabel(by: ColourBy, k: string): string {
  return by === 'role' ? ROLE_LABEL[k] ?? k : k
}

// ---- coverage ----

// covers says whether project path p (a file, or a directory with its
// trailing "/") is within one of paths: the same file, or under a
// directory. A folded "+N files" tile is under its directory.
function covers(paths: string[], p: string): boolean {
  return paths.some(c => c === p || (c.endsWith('/') && p.startsWith(c)))
}

function coverOfPath(c: DemoCoverage, p: string): Cover | null {
  return covers(c.focal, p) ? 'focal' : covers(c.supporting, p) ? 'supporting' : null
}

// Tally is how many of a node's lines each kind of coverage takes.
type Tally = { focal: number; supporting: number }

// tallyTree counts every directory's covered lines, for a directory too
// small to open to be coloured by what it holds, and for the legend.
function tallyTree(root: Dir, c: DemoCoverage): Map<Dir, Tally> {
  const out = new Map<Dir, Tally>()
  const walk = (d: Dir): Tally => {
    const t: Tally = { focal: 0, supporting: 0 }
    for (const sub of d.dirs.values()) {
      const s = walk(sub)
      t.focal += s.focal
      t.supporting += s.supporting
    }
    for (const l of d.leaves) {
      const k = coverOfPath(c, l.path)
      if (k) t[k] += l.lines
    }
    out.set(d, t)
    return t
  }
  walk(root)
  return out
}

// offMap are the declared paths the map has nothing for: another project
// mapped, or a file since moved.
function offMap(root: Dir, c: DemoCoverage): string[] {
  const has = (p: string) => {
    if (p.endsWith('/')) return !!findDir(root, p.slice(0, -1))
    const i = p.lastIndexOf('/')
    const d = findDir(root, i >= 0 ? p.slice(0, i) : '')
    // A small file may be folded into its directory's "+N files".
    return !!d && (d.leaves.some(l => l.path === p) || d.leaves.some(l => l.folded > 0))
  }
  return [...c.focal, ...c.supporting].filter(p => !has(p))
}

// MapHighlight is what the demo tab has picked out on the map: a demo's or
// a suite's coverage, or that of the demo a proposal built.
export type MapHighlight = { kind: 'demo' | 'suite' | 'proposal'; name: string; title: string; coverage: DemoCoverage }

function pct(n: number): string {
  return `${Math.round(n * 100)}%`
}

function describe(n: Node): string {
  if (n.kind === 'file') {
    return [
      n.folded ? `${n.path} -- ${n.folded} small files folded together` : n.path,
      `${n.lines} lines: ${n.code} content, ${n.comment} comment, ${n.lines - n.code - n.comment} blank`,
      `${n.lang} · ${ROLE_LABEL[n.role] ?? n.role} · density ${pct(shareOf(n))}`,
    ].join('\n')
  }
  const langs = Object.entries(n.byLang).sort((a, b) => b[1] - a[1]).slice(0, 3).map(([k, v]) => `${k} ${pct(v / n.lines)}`).join(', ')
  return [`${n.path || 'the project'}/`, `${n.files} files, ${n.lines} lines · density ${pct(shareOf(n))}`, langs].join('\n')
}

// ---- the panel ----

export function ProtosurfMapView({ request, replies, connected, highlight, onClearHighlight }: {
  request?: ToolsRequestFn
  replies: Record<string, ToolsReply>
  connected: boolean
  // highlight is the demo or suite picked out on the demo tab, if any.
  highlight?: MapHighlight | null
  onClearHighlight?: () => void
}) {
  const [req, setReq] = useState<string | null>(null)
  const reply = req ? replies[req] : undefined
  const pending = req !== null && !reply
  // The last good map stays shown while the next is asked for.
  const [shown, setShown] = useState<{ map: ProtosurfMap; detail?: string } | null>(null)
  const [path, setPath] = useState('')
  const [by, setBy] = useState<ColourBy>('language')
  const [focus, setFocus] = useState('')
  const [hover, setHover] = useState<Node | null>(null)
  const [only, setOnly] = useState<string | null>(null)

  const send = useRef<() => void>(() => {})
  send.current = () => {
    if (!request) return
    const r: ToolsRequest = { op: 'protosurf' }
    if (path.trim()) r.path = path.trim()
    setReq(request(r))
  }
  const canRequest = !!request
  useEffect(() => { if (canRequest && connected) send.current() }, [canRequest, connected])
  useEffect(() => {
    if (reply?.ok && reply.protosurf) {
      setShown({ map: reply.protosurf, detail: reply.detail })
      setFocus('')
    }
  }, [reply])

  // The map's width follows its box; its height is fixed.
  const box = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(900)
  useLayoutEffect(() => {
    const el = box.current
    if (!el) return
    const update = () => setWidth(Math.max(240, el.clientWidth))
    update()
    const ro = new ResizeObserver(update)
    ro.observe(el)
    return () => ro.disconnect()
  }, [shown !== null])
  const height = 480

  const tree = useMemo(() => shown ? buildTree(shown.map) : null, [shown])
  const at = (tree && findDir(tree, focus)) ?? tree
  const tiles = useMemo(() => at ? layout(at, { x: 0, y: 0, w: width, h: height }) : [], [at, width])
  useEffect(() => { setOnly(null) }, [by])
  // Picking a demo or suite out shows its coverage; letting it go goes
  // back to the language.
  const picked = highlight ? `${highlight.kind}:${highlight.name}` : ''
  useEffect(() => { setBy(b => picked ? 'coverage' : b === 'coverage' ? 'language' : b) }, [picked])
  const coverage = highlight?.coverage
  const tally = useMemo(() => tree && coverage ? tallyTree(tree, coverage) : null, [tree, coverage])
  const missing = useMemo(() => tree && coverage ? offMap(tree, coverage) : [], [tree, coverage])
  // cv is the coverage the map shows, when it's coloured by coverage.
  const cv = by === 'coverage' && coverage && tally ? { c: coverage, t: tally } : null

  if (!request) return <div className="sys-providers-none">the map is of a host’s project -- open a host’s demo tab to see it</div>

  // plain is the colouring when coverage isn't shown (or, for the moment
  // after a pick is let go, isn't there to show).
  const plain = by === 'coverage' ? 'language' : by

  // The legend counts what's in view: its categories, largest first, the
  // named ones in their slots and the rest folded into "other" -- or, by
  // coverage, the focal, supporting and uncovered lines.
  const legend: { key: string; label: string; colour: string; lines: number }[] = []
  if (at && cv) {
    const t = cv.t.get(at) ?? { focal: 0, supporting: 0 }
    legend.push(
      { key: 'focal', label: COVER_LABEL.focal, colour: COVER.focal, lines: t.focal },
      { key: 'supporting', label: COVER_LABEL.supporting, colour: COVER.supporting, lines: t.supporting },
      { key: 'none', label: 'not covered', colour: UNCOVERED, lines: at.lines - t.focal - t.supporting },
    )
  } else if (at && plain !== 'density') {
    const order = plain === 'language' ? LANG_ORDER : ROLE_ORDER
    const totals: Record<string, number> = {}
    for (const [k, v] of Object.entries(plain === 'language' ? at.byLang : at.byRole)) {
      const key = order.includes(k) ? k : 'other'
      totals[key] = (totals[key] ?? 0) + v
    }
    for (const [k, v] of Object.entries(totals).sort((a, b) => (a[0] === 'other' ? 1 : b[0] === 'other' ? -1 : b[1] - a[1]))) {
      legend.push({ key: k, label: categoryLabel(plain, k), colour: slotColour(order, k), lines: v })
    }
  }
  const legendKey = (n: Node) => {
    if (cv) return coverFill(n, cv.c, cv.t).cover ?? 'none'
    if (plain === 'density') return ''
    const k = categoryOf(n, plain)
    return (plain === 'language' ? LANG_ORDER : ROLE_ORDER).includes(k) ? k : 'other'
  }
  const dimmed = (n: Node) => only !== null && (cv !== null || (plain !== 'density' && n.kind === 'file')) && legendKey(n) !== only
  // tileFill is a closed tile's colour, and whether it's only partly
  // covered.
  const tileFill = (n: Node) => cv ? coverFill(n, cv.c, cv.t) : { fill: fillOf(n, plain), partial: false, cover: null }
  // An open directory holding covered files is outlined in their colour.
  const dirStroke = (d: Dir): string | undefined => {
    const t = cv?.t.get(d)
    return t?.focal ? COVER.focal : t?.supporting ? COVER.supporting : undefined
  }
  const coverWord = (n: Node): string => {
    if (!cv) return ''
    const f = coverFill(n, cv.c, cv.t)
    if (!f.cover) return 'not covered'
    if (!f.partial) return f.cover
    if (n.kind === 'file') return `may hold ${f.cover} files, folded in here`
    const t = cv.t.get(n)
    return t ? `${pct(t.focal / n.lines)} focal, ${pct(t.supporting / n.lines)} supporting` : f.cover
  }

  const trail = focus ? focus.split('/') : []
  const m = shown?.map

  return (
    <div className="ps-map">
      <div className="demo-actions">
        <div className="ctl-subtabs ps-modes">
          {COLOUR_BY.filter(c => c.key !== 'coverage' || coverage).map(c => (
            <button key={c.key} className={`ctl-subtab${by === c.key ? ' ctl-subtab-active' : ''}`} title={c.help} onClick={() => setBy(c.key)}>{c.label}</button>
          ))}
        </div>
        <input className="demo-dup-name ps-path" value={path} placeholder="project path (default: the host’s own)" title="another checkout on the host to map -- only on the host’s own tab; through agent-coordinator just the host’s own project is mapped" onChange={e => setPath(e.target.value)} />
        <button className="sys-btn sys-btn-neutral" disabled={!connected || pending} onClick={() => send.current()}>{pending ? 'mapping…' : 'rescan'}</button>
      </div>
      {reply && !reply.ok && <div className="ctl-run-error-msg">{reply.error}</div>}
      {highlight && (
        <div className="ps-pick">
          <span className="demo-count">picked out:</span>
          <span className="demo-app-name">{highlight.title}</span>
          <span className="demo-count">
            {highlight.kind} {highlight.name} · declared coverage: {highlight.coverage.focal.length} focal, {highlight.coverage.supporting.length} supporting path(s)
            {tree && tally ? ` · ${pct((tally.get(tree)?.focal ?? 0) / Math.max(1, tree.lines))} of the project’s lines focal, ${pct((tally.get(tree)?.supporting ?? 0) / Math.max(1, tree.lines))} supporting` : ''}
          </span>
          {onClearHighlight && <button className="sys-btn sys-btn-neutral" onClick={onClearHighlight}>clear</button>}
        </div>
      )}
      {highlight && missing.length > 0 && (
        <div className="ctl-hint" title={missing.join('\n')}>{missing.length} declared path(s) aren’t on this map: {missing.slice(0, 4).join(', ')}{missing.length > 4 ? ', …' : ''}</div>
      )}
      {!m || !tree || !at ? (
        <div className="sys-providers-none">{pending ? 'mapping the project…' : connected ? 'no map yet' : 'not connected'}</div>
      ) : (
        <>
          <div className="demo-count" title={shown?.detail}>
            {m.root} ({m.source === 'git' ? 'git’s files' : 'every file under it'}) · {tree.files} files · {tree.lines.toLocaleString()} lines · density {pct(shareOf(tree))} · mapped {new Date(m.scanned_at).toLocaleTimeString()}
            {m.skipped && ` · left off: ${Object.entries(m.skipped).map(([k, v]) => `${v} ${k}`).join(', ')}`}
            {m.folded ? ` · ${m.folded} small files folded to fit the relay` : ''}
          </div>
          <div className="ps-trail">
            <span className={`ps-crumb${focus ? ' ps-crumb-link' : ''}`} onClick={() => setFocus('')}>{m.root.split('/').pop() || 'project'}</span>
            {trail.map((seg, i) => {
              const p = trail.slice(0, i + 1).join('/')
              return (
                <span key={p}>
                  <span className="ps-crumb-sep">/</span>
                  <span className={`ps-crumb${p !== focus ? ' ps-crumb-link' : ''}`} onClick={() => setFocus(p)}>{seg}</span>
                </span>
              )
            })}
            <span className="demo-count">{at.files} files · {at.lines.toLocaleString()} lines{at !== tree ? ` · ${pct(at.lines / tree.lines)} of the project` : ''}</span>
          </div>
          <div className="ps-hover">{hover ? [...describe(hover).split('\n'), coverWord(hover)].filter(Boolean).join(' · ') : 'hover a tile for its detail; click one to zoom into its directory'}</div>
          <div className="ps-box" ref={box}>
            <svg width={width} height={height} className="ps-svg" onMouseLeave={() => setHover(null)}>
              {tiles.map((t, i) => {
                const { x, y, w, h } = t.r
                const zoom = () => {
                  const target = t.n.kind === 'dir' ? t.top : t.top !== t.n.path ? t.top : ''
                  if (target && findDir(tree, target)) setFocus(target)
                }
                if (t.open && t.n.kind === 'dir') {
                  const chars = Math.floor((w - 6) / 6.2)
                  const stroke = dirStroke(t.n)
                  return (
                    <g key={i} className="ps-dir" onClick={zoom} onMouseEnter={() => setHover(t.n)}>
                      <rect x={x + 0.5} y={y + 0.5} width={Math.max(0, w - 1)} height={Math.max(0, h - 1)} className={`ps-dir-box ps-depth-${Math.min(t.depth, 3)}`} style={stroke ? { stroke, strokeWidth: 1.5 } : undefined} />
                      {chars >= 2 && (
                        <text x={x + 4} y={y + 10} className="ps-dir-label">{t.n.name.length > chars ? `${t.n.name.slice(0, chars - 1)}…` : t.n.name}</text>
                      )}
                    </g>
                  )
                }
                const f = tileFill(t.n)
                return (
                  <rect
                    key={i}
                    x={x + 1} y={y + 1} width={Math.max(0, w - 2)} height={Math.max(0, h - 2)} rx={w > 8 && h > 8 ? 2 : 0}
                    fill={f.fill}
                    className={`ps-tile${dimmed(t.n) ? ' ps-tile-dim' : ''}${f.partial ? ' ps-tile-partial' : ''}${hover === t.n ? ' ps-tile-hover' : ''}`}
                    onMouseEnter={() => setHover(t.n)}
                    onClick={zoom}
                  >
                    <title>{[describe(t.n), coverWord(t.n)].filter(Boolean).join('\n')}</title>
                  </rect>
                )
              })}
            </svg>
          </div>
          {plain === 'density' && !cv ? (
            <div className="ps-legend">
              <span className="demo-count">content share of each file’s lines (neither blank nor comment):</span>
              {DENSITY.map((c, i) => (
                <span key={c} className="ps-legend-item"><span className="ps-swatch" style={{ background: c }} />{pct(i / DENSITY.length)}–{pct((i + 1) / DENSITY.length)}</span>
              ))}
            </div>
          ) : (
            <table className="ps-legend-table">
              <tbody>
                {legend.map(l => (
                  <tr
                    key={l.key}
                    className={`ps-legend-row${only === l.key ? ' ps-legend-on' : ''}`}
                    title={only === l.key ? 'show every category again' : `pick out the ${l.label} files`}
                    onClick={() => setOnly(only === l.key ? null : l.key)}
                  >
                    <td><span className="ps-swatch" style={{ background: l.colour }} /></td>
                    <td className="ps-legend-name">{l.label}</td>
                    <td className="ps-legend-num">{l.lines.toLocaleString()} lines</td>
                    <td className="ps-legend-num">{pct(l.lines / at.lines)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}
    </div>
  )
}
