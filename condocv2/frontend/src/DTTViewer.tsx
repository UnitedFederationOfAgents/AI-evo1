import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import type { Interaction } from './types'
import { RepoLink } from './RepoLink'

// DTTViewer shows an interaction's DTT material -- each tool call it
// proposed, and the images dtt returned for them (a read before deciding,
// then the accepted run) -- as a menu down the side and the picked one's
// content (privateRepoOpsImpls Step 4, Revision B). Both tabs open it: a
// proposal's file name, a DTT or Result section's links, and on the visual
// a proposal or image node, an answer, the wait, or a DTT run's box.
//
// The files are the condoc's own copies, under dtt/<N>/ beside the step
// file, which condocv2 serves at api/dtt/<stem>/<N>/<file> (review.go's
// handleDTTFile).

export interface DTTEntry {
  label: string
  rel: string // under dtt/<N>/
  image?: boolean
}

export interface DTTTarget {
  n: number
  entries: DTTEntry[]
  index: number
}

export type OnOpenDTT = (n: number, rel?: string) => void

// dttFileURL is a file under dtt/<n>/ as condocv2 serves it -- relative to
// the path the UI was served under, so it works reverse-proxied too.
export function dttFileURL(stem: string, n: number, rel: string): string {
  const dir = window.location.pathname.replace(/\/[^/]*\.[^/]*$/, '/')
  const base = dir.endsWith('/') ? dir.slice(0, -1) : dir
  return `${base}/api/dtt/${encodeURIComponent(stem)}/${n}/${rel.split('/').map(encodeURIComponent).join('/')}`
}

// proposalOf reads the proposal a "## DTT N.k" section is about.
export function proposalOf(text?: string): string {
  return text?.match(/`([^`]+)`/)?.[1] ?? ''
}

// isPreRead: a "## DTT N.k" that read a write proposal before deciding it
// (review.go's preReadMark).
export const isPreRead = (text?: string) => !!text?.startsWith('**Read before deciding**')

const DTT_LINK = /\]\(dtt\/(\d+)\/([^)\s]+)\)/

// imageOf reads the image a "## Result N.k" links to, under dtt/<N>/.
export function imageOf(text?: string): string {
  return text?.match(DTT_LINK)?.[2] ?? ''
}

// dttEntries is the menu for interaction n: each proposal, then its images.
export function dttEntries(interactions: Interaction[], n: number, held: string[] = []): DTTEntry[] {
  const it = interactions.find((i) => i.n === n)
  const proposals: string[] = []
  const add = (p: string) => { if (p && !proposals.includes(p)) proposals.push(p) }
  for (const m of (it?.proposals ?? '').matchAll(/^- \[?`([^`]+)`/gm)) add(m[1])
  for (const t of it?.tools ?? []) add(proposalOf(t.dtt))
  held.forEach(add)
  const out: DTTEntry[] = []
  for (const p of proposals) {
    out.push({ label: p, rel: p })
    for (const t of it?.tools ?? []) {
      const img = imageOf(t.result)
      if (proposalOf(t.dtt) !== p || !img) continue
      const mode = isPreRead(t.dtt) ? 'read before deciding' : `dtt ${t.result?.match(/`dtt (\w+)`/)?.[1] ?? 'run'}`
      out.push({ label: `${n}.${t.k} image · ${mode}`, rel: img, image: true })
    }
  }
  return out
}

// dttTarget is the viewer opened on rel (the first entry when it's not one).
export function dttTarget(interactions: Interaction[], n: number, rel?: string, held?: string[]): DTTTarget | null {
  const entries = dttEntries(interactions, n, held)
  if (rel && !entries.some((e) => e.rel === rel)) entries.push({ label: rel, rel, image: rel.endsWith('.json') })
  if (entries.length === 0) return null
  return { n, entries, index: Math.max(0, entries.findIndex((e) => e.rel === rel)) }
}

// DTTLinkedText is a section's body, with its dtt/<N>/ links opening the
// viewer and the rest of its links as anchors.
export function DTTLinkedText({ text, onOpen }: { text: string; onOpen: OnOpenDTT }) {
  const out: ReactNode[] = []
  const re = /\[([^\]]+)\]\(([^)\s]+)\)/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) out.push(text.slice(last, m.index))
    const label = m[1].replace(/^`|`$/g, '')
    const dtt = m[2].match(/^dtt\/(\d+)\/(.+)$/)
    if (dtt) {
      const n = Number(dtt[1])
      const rel = dtt[2]
      out.push(
        <button key={m.index} type="button" className="dtt-link" title="show it" onClick={() => onOpen(n, rel)}>
          <code>{label}</code>
        </button>,
      )
    } else {
      out.push(<RepoLink key={m.index} href={m[2]}><code>{label}</code></RepoLink>)
    }
    last = m.index + m[0].length
  }
  if (last < text.length) out.push(text.slice(last))
  return <>{out}</>
}

function pretty(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}

// onSelect hears each entry picked from the menu, so the app can keep it in
// its hash.
export function DTTViewer({ stem, target, onClose, onSelect }: {
  stem: string
  target: DTTTarget
  onClose: () => void
  onSelect?: (rel: string) => void
}) {
  const [index, setIndex] = useState(target.index)
  useEffect(() => setIndex(target.index), [target])
  const entry = target.entries[index] ?? target.entries[0]
  const url = dttFileURL(stem, target.n, entry.rel)
  const [body, setBody] = useState<{ url: string; text?: string; error?: string } | null>(null)

  useEffect(() => {
    let live = true
    setBody(null)
    fetch(url)
      .then(async (r) => {
        if (!r.ok) throw new Error((await r.text()).trim() || `${r.status} ${r.statusText}`)
        return r.text()
      })
      .then((text) => { if (live) setBody({ url, text: pretty(text) }) })
      .catch((err: Error) => { if (live) setBody({ url, error: err.message }) })
    return () => { live = false }
  }, [url])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div className="dtt-viewer-backdrop" onClick={onClose}>
      <div className="dtt-viewer" onClick={(e) => e.stopPropagation()}>
        <div className="dtt-viewer-header">
          <span>DTT tool calls of interaction {target.n}</span>
          <button type="button" className="tc-capture-close" onClick={onClose} aria-label="Close">×</button>
        </div>
        <div className="dtt-viewer-main">
          <ul className="dtt-viewer-menu">
            {target.entries.map((e, i) => (
              <li key={e.rel}>
                <button
                  type="button"
                  className={`dtt-viewer-item${i === index ? ' selected' : ''}${e.image ? ' dtt-viewer-image' : ''}`}
                  onClick={() => { setIndex(i); onSelect?.(e.rel) }}
                >
                  {e.image ? `↳ ${e.label}` : e.label}
                </button>
              </li>
            ))}
          </ul>
          <div className="dtt-viewer-body">
            <div className="dtt-viewer-path">
              <code>dtt/{target.n}/{entry.rel}</code>{' '}
              <a href={url} target="_blank" rel="noreferrer">open raw</a>
            </div>
            {!body || body.url !== url ? (
              <div className="pointer-note"><span className="spinner" /> loading…</div>
            ) : body.error !== undefined ? (
              <div className="pointer-message">
                {body.error === 'Failed to fetch' ? 'It is in the merged condoc repo -- open it raw.' : body.error}
              </div>
            ) : (
              <pre className="v2-body dtt-viewer-text">{body.text}</pre>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
