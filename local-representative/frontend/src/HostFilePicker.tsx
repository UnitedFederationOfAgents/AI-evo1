import { useCallback, useEffect, useState } from 'react'

// HostFilePicker is the "select from host" dialog (Step4Prompt.md Revision F
// of condocs/initialRobotImpls): it browses local-representative's host
// rather than the browser's machine, through GET <api>/api/host-files
// (local-representative/hostfiles.go), and hands back the chosen files'
// absolute paths on that host. The same file is copied into
// agent-coordinator's, condoccer's and ianar's frontends -- keep the copies
// identical. api is '' on LR itself, '/host/<id>' through agent-coordinator,
// and condoccer's or ianar's own base path in theirs (they pass the listing
// through to their LR).

interface HostFileEntry {
  name: string
  dir: boolean
  size: number
  mtime: number
}

interface HostDirListing {
  path: string
  parent: string
  home: string
  entries: HostFileEntry[]
}

function joinHostPath(dir: string, name: string): string {
  return dir.endsWith('/') ? dir + name : `${dir}/${name}`
}

function baseName(p: string): string {
  return p.slice(p.lastIndexOf('/') + 1)
}

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`
  return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`
}

export function HostFilePicker({ api, multiple = false, title = 'select from host', onPick, onClose }: {
  api: string
  multiple?: boolean
  title?: string
  onPick: (paths: string[]) => void
  onClose: () => void
}) {
  const [listing, setListing] = useState<HostDirListing | null>(null)
  const [pathInput, setPathInput] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [showHidden, setShowHidden] = useState(false)
  const [selected, setSelected] = useState<string[]>([])

  const open = useCallback(async (path: string) => {
    setLoading(true)
    setError(null)
    try {
      const resp = await fetch(`${api}/api/host-files?path=${encodeURIComponent(path)}`)
      if (!resp.ok) {
        setError((await resp.text()).trim() || `couldn't open ${path || 'the home folder'} (${resp.status})`)
        return
      }
      const l = (await resp.json()) as HostDirListing
      setListing(l)
      setPathInput(l.path)
    } catch (err) {
      setError(String(err))
    } finally {
      setLoading(false)
    }
  }, [api])

  useEffect(() => { void open('') }, [open])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const toggle = (p: string) => {
    if (!multiple) {
      setSelected(s => (s[0] === p ? [] : [p]))
      return
    }
    setSelected(s => (s.includes(p) ? s.filter(x => x !== p) : [...s, p]))
  }

  const entries = (listing?.entries ?? []).filter(e => showHidden || !e.name.startsWith('.'))

  return (
    <div className="hfp-overlay" onClick={onClose}>
      <div className="hfp-dialog" onClick={e => e.stopPropagation()}>
        <div className="hfp-header">
          <span className="hfp-title">{title}</span>
          <button type="button" className="hfp-close" onClick={onClose}>×</button>
        </div>
        <div className="hfp-path-row">
          <button type="button" className="hfp-btn" disabled={!listing?.parent || loading} title="up one folder" onClick={() => listing && void open(listing.parent)}>↑</button>
          <button type="button" className="hfp-btn" disabled={loading} title="home folder" onClick={() => void open('')}>~</button>
          <input
            className="hfp-path-input"
            value={pathInput}
            spellCheck={false}
            onChange={e => setPathInput(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') void open(pathInput) }}
          />
          <label className="hfp-hidden-toggle">
            <input type="checkbox" checked={showHidden} onChange={e => setShowHidden(e.target.checked)} />
            hidden
          </label>
        </div>
        {error && <div className="hfp-error">{error}</div>}
        <div className="hfp-list">
          {loading && !listing && <div className="hfp-empty">loading…</div>}
          {listing && entries.length === 0 && <div className="hfp-empty">this folder is empty</div>}
          {listing && entries.map(e => {
            const p = joinHostPath(listing.path, e.name)
            const isSel = selected.includes(p)
            return (
              <div
                key={e.name}
                className={`hfp-entry${e.dir ? ' hfp-entry-dir' : ''}${isSel ? ' hfp-entry-selected' : ''}`}
                title={p}
                onClick={() => (e.dir ? void open(p) : toggle(p))}
                onDoubleClick={() => { if (!e.dir && !multiple) onPick([p]) }}
              >
                <span className="hfp-entry-icon">{e.dir ? '📁' : isSel ? '☑' : '📄'}</span>
                <span className="hfp-entry-name">{e.name}{e.dir ? '/' : ''}</span>
                {!e.dir && <span className="hfp-entry-size">{formatSize(e.size)}</span>}
              </div>
            )
          })}
        </div>
        <div className="hfp-footer">
          <span className="hfp-selection" title={selected.join('\n')}>
            {selected.length === 0 ? (multiple ? 'click files to select them' : 'click a file to select it') : selected.map(baseName).join(', ')}
          </span>
          <button type="button" className="hfp-btn" onClick={onClose}>cancel</button>
          <button type="button" className="hfp-btn hfp-btn-primary" disabled={selected.length === 0} onClick={() => onPick(selected)}>
            select{selected.length > 1 ? ` ${selected.length}` : ''}
          </button>
        </div>
      </div>
    </div>
  )
}
