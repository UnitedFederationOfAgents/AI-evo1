import { useEffect, useRef, useState } from 'react'
import type { Timeline } from './types'
import { RepoLink } from './RepoLink'

// DiffViewer is condoccer's multi-repo git diff view, for the rows of the
// timeline that move repos (condocv2/diffview.go): interaction N's commits --
// the agent's response commit on the condoc repo and its commit on each
// target -- or external change N.e, each branch pulled in from outside. Down
// the side, each repo's range and the files it changed; beside it, the picked
// file's diff with its hunks to jump to. The header's "files" toggle
// collapses the side to give the diff the width; on a phone, where the open
// side fills the screen, picking a file collapses it. The visual opens it
// from the "+/-" in the left margin of those rows, the text from a record.
//
// condocv2 serves it at api/diff/<stem>/<row>[/<owner>/<name>/<path>].

export type OnOpenDiff = (row: string) => void

// A row: "N", or "external-N.e".
export const externalRow = (n: number, e: number) => `external-${n}.${e}`

export function diffRowTitle(row: string): string {
  const x = row.match(/^external-(.+)$/)
  return x ? `external change ${x[1]}` : `interaction ${row}'s commits`
}

// diffRows is each row of the timeline with something to diff: an
// interaction whose response or target commits have landed, and every
// external change. None once the slopspace is gone.
export function diffRows(tl: Timeline): Set<string> {
  const rows = new Set<string>()
  if (tl.source === 'record') return rows
  for (const e of tl.events ?? []) {
    if (e.pending || (!e.interaction && e.kind !== 'external')) continue
    if (e.kind === 'external' && e.lane !== 'pointer') rows.add(externalRow(e.interaction ?? 0, e.seq ?? 0))
    else if (!e.sub && ((e.kind === 'response' && e.lane === 'condoc') || e.kind === 'commit')) rows.add(`${e.interaction}`)
  }
  return rows
}

interface DiffFile {
  path: string
  added: number
  deleted: number
  binary?: boolean
}

interface DiffRange {
  repo: string
  lane: string
  from: string
  to: string
  commits: number
  url?: string
  files: DiffFile[]
}

interface FileDiff {
  repo: string
  path: string
  content: string
  hunks: { header: string; line: number }[]
  truncated?: boolean
}

// diffURL is the row's diff, or one file's in it, as condocv2 serves it --
// relative to the path the UI was served under, so it works reverse-proxied too.
function diffURL(stem: string, row: string, file?: string): string {
  const dir = window.location.pathname.replace(/\/[^/]*\.[^/]*$/, '/')
  const base = dir.endsWith('/') ? dir.slice(0, -1) : dir
  const url = `${base}/api/diff/${encodeURIComponent(stem)}/${encodeURIComponent(row)}`
  return file ? `${url}/${file.split('/').map(encodeURIComponent).join('/')}` : url
}

async function getJSON<T>(url: string): Promise<T> {
  const r = await fetch(url)
  if (!r.ok) throw new Error((await r.text()).trim() || `${r.status} ${r.statusText}`)
  return r.json() as Promise<T>
}

const short = (sha: string) => sha.slice(0, 7)
const shortRepo = (repo: string) => repo.split('/').pop() ?? repo
const lineClass = (line: string) =>
  line.startsWith('@@') ? 'diff-line diff-hunk'
    : line.startsWith('+++') || line.startsWith('---') || /^(diff |index |new file|deleted file)/.test(line) ? 'diff-line diff-meta'
      : line.startsWith('+') ? 'diff-line diff-add'
        : line.startsWith('-') ? 'diff-line diff-remove'
          : 'diff-line'

// hunkLine is where a hunk starts in the new file ("@@ -a,b +c,d @@").
const hunkLine = (header: string) => header.match(/\+(\d+)/)?.[1]

// narrow is a phone-width screen, where index.css's mobile rules apply: there
// the file pane takes the whole width while it's open, so picking a file
// closes it to show that file's diff.
const narrow = () => window.matchMedia('(max-width: 760px)').matches

// file is "<owner>/<name>/<path>", '' for the first; onSelect hears each one
// picked, so the app can keep it in its hash.
export function DiffViewer({ stem, row, file, onClose, onSelect }: {
  stem: string
  row: string
  file: string
  onClose: () => void
  onSelect?: (file: string) => void
}) {
  const listURL = diffURL(stem, row)
  const [list, setList] = useState<{ url: string; repos?: DiffRange[]; error?: string } | null>(null)
  const [picked, setPicked] = useState(file)
  useEffect(() => setPicked(file), [file])
  // The file pane can be collapsed to give the diff the full width. On a
  // phone it starts collapsed when opened straight to a file.
  const [filesOpen, setFilesOpen] = useState(() => !(file && narrow()))

  useEffect(() => {
    let live = true
    setList(null)
    getJSON<{ repos: DiffRange[] }>(listURL)
      .then((p) => { if (live) setList({ url: listURL, repos: p.repos ?? [] }) })
      .catch((err: Error) => { if (live) setList({ url: listURL, error: err.message }) })
    return () => { live = false }
  }, [listURL])

  const repos = list?.url === listURL ? list.repos ?? [] : []
  const all = repos.flatMap((r) => r.files.map((f) => `${r.repo}/${f.path}`))
  const shown = picked && all.includes(picked) ? picked : all[0] ?? ''
  const fileURL = shown ? diffURL(stem, row, shown) : ''
  const [body, setBody] = useState<{ url: string; diff?: FileDiff; error?: string } | null>(null)

  useEffect(() => {
    if (!fileURL) return
    let live = true
    setBody(null)
    getJSON<FileDiff>(fileURL)
      .then((d) => { if (live) setBody({ url: fileURL, diff: d }) })
      .catch((err: Error) => { if (live) setBody({ url: fileURL, error: err.message }) })
    return () => { live = false }
  }, [fileURL])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const hunkRefs = useRef<Record<number, HTMLDivElement | null>>({})
  const pick = (f: string) => {
    setPicked(f)
    onSelect?.(f)
    if (narrow()) setFilesOpen(false)
  }
  const diff = body?.url === fileURL ? body.diff : undefined
  const totals = repos.reduce((t, r) => r.files.reduce((u, f) => ({ a: u.a + f.added, d: u.d + f.deleted }), t), { a: 0, d: 0 })

  return (
    <div className="dtt-viewer-backdrop" onClick={onClose}>
      <div className="dtt-viewer diff-viewer" onClick={(e) => e.stopPropagation()}>
        <div className="dtt-viewer-header">
          <button
            type="button"
            className="diff-files-toggle"
            onClick={() => setFilesOpen((o) => !o)}
            aria-expanded={filesOpen}
            title={filesOpen ? 'Hide the file list' : 'Show the file list'}
          >
            {filesOpen ? '◀' : '▶'} files{!filesOpen && all.length > 0 ? ` (${all.length})` : ''}
          </button>
          <span className="diff-title">
            Changes · {diffRowTitle(row)}
            {repos.length > 0 && (
              <span className="diff-totals">
                {' '}{all.length} file{all.length === 1 ? '' : 's'} in {repos.length} repo{repos.length === 1 ? '' : 's'}{' '}
                <span className="diff-added">+{totals.a}</span> <span className="diff-deleted">−{totals.d}</span>
              </span>
            )}
          </span>
          <button type="button" className="tc-capture-close" onClick={onClose} aria-label="Close">×</button>
        </div>
        <div className={`dtt-viewer-main${filesOpen ? ' files-open' : ' files-collapsed'}`}>
          <ul className="dtt-viewer-menu">
            {!list || list.url !== listURL ? (
              <li className="pointer-note"><span className="spinner" /> loading…</li>
            ) : list.error !== undefined ? (
              <li className="pointer-message">{list.error}</li>
            ) : repos.map((r) => (
              <li key={r.repo}>
                <div className="diff-repo-head" title={`${r.repo}: ${r.from} → ${r.to}`}>
                  <span className="diff-repo-name">{r.lane === 'condoc' ? `${shortRepo(r.repo)} (condoc)` : shortRepo(r.repo)}</span>
                  <span className="diff-repo-range">
                    {short(r.from)} → {r.url ? <RepoLink href={r.url}>{short(r.to)}</RepoLink> : short(r.to)}
                    {` · ${r.commits || ''}${r.commits ? ' ' : ''}commit${r.commits === 1 ? '' : 's'}`}
                  </span>
                </div>
                {r.files.length === 0 && <div className="nav-empty">no files changed</div>}
                <ul className="diff-file-list">
                  {r.files.map((f) => {
                    const key = `${r.repo}/${f.path}`
                    return (
                      <li key={key}>
                        <button
                          type="button"
                          className={`dtt-viewer-item diff-file${key === shown ? ' selected' : ''}`}
                          title={f.path}
                          onClick={() => pick(key)}
                        >
                          <span className="diff-file-name">{f.path}</span>
                          <span className="diff-file-stat">
                            {f.binary ? 'binary' : <><span className="diff-added">+{f.added}</span> <span className="diff-deleted">−{f.deleted}</span></>}
                          </span>
                        </button>
                      </li>
                    )
                  })}
                </ul>
              </li>
            ))}
          </ul>
          <div className="dtt-viewer-body">
            {shown && (
              <div className="dtt-viewer-path">
                <code>{shown}</code>
                {diff && diff.hunks.length > 1 && (
                  <span className="diff-hunks">
                    {diff.hunks.map((h, i) => (
                      <button
                        key={i}
                        type="button"
                        className="dtt-link"
                        title={h.header}
                        onClick={() => hunkRefs.current[h.line]?.scrollIntoView({ behavior: 'smooth', block: 'start' })}
                      >
                        {hunkLine(h.header) ? `line ${hunkLine(h.header)}` : `hunk ${i + 1}`}
                      </button>
                    ))}
                  </span>
                )}
              </div>
            )}
            {!shown ? (
              list?.repos && <div className="pointer-note">Nothing changed.</div>
            ) : !body || body.url !== fileURL ? (
              <div className="pointer-note"><span className="spinner" /> loading…</div>
            ) : body.error !== undefined ? (
              <div className="pointer-message">{body.error}</div>
            ) : (
              <div className="v2-body diff-content">
                {(diff?.content ?? '').split('\n').map((line, i) => (
                  <div key={i} className={lineClass(line)} ref={line.startsWith('@@') ? (el) => { hunkRefs.current[i] = el } : undefined}>
                    {line || ' '}
                  </div>
                ))}
                {diff?.truncated && <div className="diff-line diff-meta">… (diff truncated)</div>}
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
