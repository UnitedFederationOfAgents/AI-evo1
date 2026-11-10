import { useEffect, useRef, useState } from 'react'
import type { Host } from './types'

/* ---- Global system timeline ----
 *
 * The global system tab's timeline sub-tab (privateRepoOpsImpls
 * Step6Prompt.md Revision J, after its swimlanes sketch): every host active
 * during the window is a host lane, split into up to three faintly divided
 * sections -- global (agent-coordinator, on the host it runs on), LR
 * (local-representative and its internal managed sub-apps) and external
 * (managed external apps and plug-ins). Each component is a laser across the
 * time axis: fat for AC and LR, slim for the rest. A laser's look follows its
 * state -- blue while up, green marching ants while building, red while its
 * last build has failed, and a green halo hugging the laser, around its own
 * colour, for the 10 minutes after a start or restart (the topology view's
 * "fresh" halo; Revision K). LR's laser wears an orange "dirty" halo the same
 * way for as long as its watched repo has uncommitted changes -- in place of
 * the fresh one, which it outranks -- and is drawn as the marching ants in
 * orange for as long as a .ufabuild request waits for its rebuild ("pending";
 * never while dirty, since rebuild_ready excludes it, and, like the green
 * ants, without the fresh halo). A change of reported build version is an
 * amber diamond on that sub-app's own laser, tagged with the new version's
 * short form, so each rebuilt sub-app is picked out by itself. A new
 * .ufabuild build request is a violet tick on LR's laser. A restart is a
 * vertical bar: LR's runs through the LR and external sections, since LR manages
 * them; anything else's runs through its own row. The history is recorded by
 * agent-coordinator (timeline.go) and polled from GET /api/timeline.
 *
 * The scroll wheel over the time axis or any laser pans the window: up or
 * left goes back in time, down or right forward, a tenth of the window per
 * notch. A panned window holds still on the time it shows (until= on the
 * API) instead of following now; panning forward to now, or "now" in the
 * toolbar, makes it follow again. It can't pan past now, nor further back
 * than AC keeps history (timeline.go's timelineRetention).
 */

interface TimelineSpan { from: number; to: number; state: 'up' | 'building' | 'failed'; dirty?: boolean; pending?: boolean }
interface TimelineMark {
  at: number
  kind: 'start' | 'restart' | 'version' | 'rebuild_requested'
  version?: string // on a 'version' mark: what it changed to, and from
  prev?: string
  request?: string // on a 'rebuild_requested' mark: the .ufabuild request that arrived
}
interface TimelineTrack {
  name: string
  section: 'global' | 'lr' | 'external'
  main?: boolean
  spans: TimelineSpan[]
  marks?: TimelineMark[]
  auto_rebuild_seconds?: number // the last auto-rebuild countdown seen, right-edge tooltip
}
interface TimelineLane { host: string; tracks: TimelineTrack[] }
interface TimelineMsg {
  now: number
  since: number
  until?: number
  sample_interval: number
  self_host_id: string
  lanes: TimelineLane[]
}

const TIMELINE_WINDOWS = [
  { label: '15m', secs: 15 * 60 },
  { label: '1h', secs: 60 * 60 },
  { label: '6h', secs: 6 * 60 * 60 },
  { label: '24h', secs: 24 * 60 * 60 },
] as const

const TIMELINE_POLL_MS = 5000
const TIMELINE_FRESH_SECS = 10 * 60 // as App.tsx's FRESH_UPTIME_SECS
// A sub-app restart this close to an LR restart is that restart: LR's own
// bar already covers its row.
const TIMELINE_RESTART_SLOP_SECS = 30
const TIMELINE_TICK_STEPS = [60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600]
const TIMELINE_RETENTION_SECS = 24 * 60 * 60 // as timeline.go's timelineRetention
// Panning: a wheel notch (~100px of deltaY) moves the window a tenth of its
// width; a panned window is fetched once the wheel settles for this long.
const TIMELINE_PAN_PX_PER_WINDOW = 1000
const TIMELINE_PAN_FETCH_DELAY_MS = 150

// wheelPixels is a wheel event's movement in pixels, whichever axis moved
// more: line- and page-mode wheels (Firefox) report in other units.
function wheelPixels(e: WheelEvent): number {
  const d = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY
  return e.deltaMode === 1 ? d * 40 : e.deltaMode === 2 ? d * 800 : d
}

const SECTION_ORDER: TimelineTrack['section'][] = ['global', 'lr', 'external']
const SECTION_LABELS: Record<TimelineTrack['section'], string> = {
  global: 'global',
  lr: 'LR apps',
  external: 'external',
}

// The topology cards' abbreviations, so a lane reads like its card.
const TRACK_ABBREVIATIONS: Record<string, string> = {
  'agent-coordinator': 'AC',
  'local-representative': 'LR',
  'federation-command': 'FC',
  condocv2: 'C2',
  convo: 'TC',
  sessions: 'SM',
  robot: 'RB',
  compose: 'CM',
  dg: 'DG',
  worker: 'W',
}

function trackLabel(name: string): string {
  return TRACK_ABBREVIATIONS[name] ?? name
}

function formatClock(unix: number, withSeconds = false): string {
  const d = new Date(unix * 1000)
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  if (!withSeconds) return `${hh}:${mm}`
  return `${hh}:${mm}:${String(d.getSeconds()).padStart(2, '0')}`
}

function axisTicks(since: number, now: number): number[] {
  const span = now - since
  const step = TIMELINE_TICK_STEPS.find(s => span / s <= 8) ?? TIMELINE_TICK_STEPS[TIMELINE_TICK_STEPS.length - 1]
  const ticks: number[] = []
  for (let t = Math.ceil(since / step) * step; t <= now; t += step) ticks.push(t)
  return ticks
}

// Positions are percentages of the track area, so the view scales with the
// pane and needs no measuring.
function pct(t: number, since: number, now: number): number {
  return Math.min(100, Math.max(0, ((t - since) / Math.max(1, now - since)) * 100))
}

function sortTracks(tracks: TimelineTrack[]): TimelineTrack[] {
  return [...tracks].sort((a, b) => (a.main === b.main ? a.name.localeCompare(b.name) : a.main ? -1 : 1))
}

// freshRanges is where a track wears the fresh halo: the 10 minutes after
// each start or restart, only where it was actually up. While building (or
// pending) the marching ants replace it, as on the topology tiles, and
// while dirty the orange halo does: it's the one that needs acting on.
function freshRanges(track: TimelineTrack): Array<[number, number]> {
  const out: Array<[number, number]> = []
  for (const m of track.marks ?? []) {
    if (m.kind !== 'start' && m.kind !== 'restart') continue
    const end = m.at + TIMELINE_FRESH_SECS
    for (const s of track.spans) {
      if (s.state === 'building' || s.pending || s.dirty) continue
      const from = Math.max(s.from, m.at)
      const to = Math.min(s.to, end)
      if (to > from) out.push([from, to])
    }
  }
  return out
}

function restarts(track: TimelineTrack | undefined): number[] {
  return (track?.marks ?? []).filter(m => m.kind === 'restart').map(m => m.at)
}

// dirtyRanges is where LR wears the dirty halo: its watched repo had
// uncommitted changes. Exactly the fresh halo's shape, in orange.
function dirtyRanges(track: TimelineTrack): Array<[number, number]> {
  return track.spans.filter(s => s.dirty).map(s => [s.from, s.to] as [number, number])
}

// laserClass is a span's look: its state, except that a pending rebuild
// draws it as the building ants in orange. A running build's green ants
// outrank it (AC doesn't report pending while one runs anyway).
function laserClass(s: TimelineSpan): string {
  return s.pending && s.state !== 'building' ? 'tl-state-pending' : `tl-state-${s.state}`
}

// rebuildRequests is when a .ufabuild build request arrived on LR's laser.
function rebuildRequests(track: TimelineTrack): TimelineMark[] {
  return (track.marks ?? []).filter(m => m.kind === 'rebuild_requested').sort((a, b) => a.at - b.at)
}

function versionChanges(track: TimelineTrack): TimelineMark[] {
  return (track.marks ?? []).filter(m => m.kind === 'version').sort((a, b) => a.at - b.at)
}

// shortVersion is a version's tag on the laser: the commit sha of a
// compute-version.sh "<tag>-<branch>-<sha>" version, else the whole of it (an
// exact tag, or "dev").
function shortVersion(v: string | undefined): string {
  if (!v) return '?'
  const m = /-([0-9a-f]{7,})$/.exec(v)
  return m ? m[1] : v
}

// spanTitle is a laser segment's tooltip: its state, LR's dirty halo or
// pending rebuild, and -- on the track's right edge only -- the
// auto-rebuild countdown while one is brewing.
function spanTitle(track: TimelineTrack, s: TimelineSpan, atRightEdge: boolean): string {
  let state = s.state === 'failed' ? 'up, last build failed' : s.state
  if (s.dirty) state += ', repo dirty'
  if (s.pending) state += ', rebuild pending'
  if (atRightEdge && (track.auto_rebuild_seconds ?? 0) > 0) state += ` (auto-rebuild in ~${track.auto_rebuild_seconds}s)`
  return `${track.name}: ${state}\n${formatClock(s.from, true)} – ${formatClock(s.to, true)}`
}

// A version tag is left off when the track's next version change is closer
// than this (a percentage of the window), so tags never pile up; the diamond
// and its tooltip still show.
const TIMELINE_VERSION_TAG_GAP_PCT = 8

// clampEnd keeps a panned window's end between the oldest history AC keeps
// and now; null (follow now) once it reaches now.
function clampEnd(end: number, now: number, windowSecs: number): number | null {
  const clamped = Math.max(end, now - TIMELINE_RETENTION_SECS + windowSecs)
  return clamped >= now ? null : clamped
}

export function GlobalTimelinePanel({ hosts, onOpenHost }: {
  hosts: Host[]
  onOpenHost?: (hostId: string) => void
}) {
  const [windowSecs, setWindowSecs] = useState<number>(60 * 60)
  // Where a panned window ends (unix seconds); null follows now.
  const [pinnedEnd, setPinnedEnd] = useState<number | null>(null)
  const [data, setData] = useState<TimelineMsg | null>(null)
  const [error, setError] = useState<string>('')
  const panelRef = useRef<HTMLDivElement>(null)
  // What the wheel handler, bound once, pans against.
  const viewRef = useRef({ now: 0, windowSecs })
  viewRef.current = { now: data?.now ?? 0, windowSecs }

  useEffect(() => {
    let cancelled = false
    const load = () => {
      const end = pinnedEnd === null ? Math.floor(Date.now() / 1000) : Math.round(pinnedEnd)
      const until = pinnedEnd === null ? '' : `&until=${end}`
      fetch(`/api/timeline?since=${end - windowSecs}${until}`)
        .then(r => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
        .then((msg: TimelineMsg) => {
          if (cancelled) return
          setData(msg)
          setError('')
        })
        .catch(err => { if (!cancelled) setError(String(err?.message ?? err)) })
    }
    // While the wheel is turning, the lasers already slide with the window;
    // fetch what it newly shows once it settles.
    const first = setTimeout(load, pinnedEnd === null ? 0 : TIMELINE_PAN_FETCH_DELAY_MS)
    const id = setInterval(load, TIMELINE_POLL_MS)
    return () => { cancelled = true; clearTimeout(first); clearInterval(id) }
  }, [windowSecs, pinnedEnd])

  // A native listener: React's onWheel is passive, so it couldn't stop the
  // page scrolling as well. Only the time areas pan, so the wheel over the
  // labels still scrolls the page.
  useEffect(() => {
    const panel = panelRef.current
    if (!panel) return
    const onWheel = (e: WheelEvent) => {
      if (e.ctrlKey || !(e.target instanceof Element) || !e.target.closest('.tl-area')) return
      e.preventDefault()
      const { now, windowSecs: w } = viewRef.current
      if (!now) return
      const delta = (wheelPixels(e) / TIMELINE_PAN_PX_PER_WINDOW) * w
      setPinnedEnd(prev => clampEnd((prev ?? now) + delta, now, w))
    }
    panel.addEventListener('wheel', onWheel, { passive: false })
    return () => panel.removeEventListener('wheel', onWheel)
  }, [])

  const chooseWindow = (secs: number) => {
    setWindowSecs(secs)
    if (pinnedEnd !== null && data) setPinnedEnd(clampEnd(pinnedEnd, data.now, secs))
  }

  // "now" is the window's right edge: the server's now, or where it's panned to.
  const now = pinnedEnd ?? data?.now ?? 0
  const since = data ? now - windowSecs : 0
  const live = pinnedEnd === null
  // Until a panned window's fetch lands, data still holds the last window's.
  const inView = (t: number) => t >= since && t <= now
  const hostById = new Map(hosts.map(h => [h.id, h]))
  // AC's own host first, like the topology view's self card.
  const lanes = [...(data?.lanes ?? [])].sort((a, b) =>
    a.host === data?.self_host_id ? -1 : b.host === data?.self_host_id ? 1 : a.host.localeCompare(b.host))
  const ticks = data ? axisTicks(since, now) : []

  return (
    <div className="tl-panel" ref={panelRef}>
      <div className="tl-toolbar">
        <span className="tl-toolbar-label">window</span>
        {TIMELINE_WINDOWS.map(w => (
          <button
            key={w.label}
            className={`tl-window-btn${windowSecs === w.secs ? ' tl-window-btn-active' : ''}`}
            onClick={() => chooseWindow(w.secs)}
          >
            {w.label}
          </button>
        ))}
        <button
          className={`tl-window-btn${live ? ' tl-window-btn-active' : ''}`}
          title={live ? 'following now -- scroll over the timeline to pan back' : 'stop panning and follow now again'}
          onClick={() => setPinnedEnd(null)}
        >
          now
        </button>
        {!live && data && (
          <span className="tl-pan-range" title="panned: scroll down or right to go forward">
            {formatClock(since)} – {formatClock(now)}
          </span>
        )}
        <span className="tl-legend">
          <span className="tl-legend-item"><span className="tl-legend-swatch tl-state-up" />up</span>
          <span className="tl-legend-item"><span className="tl-legend-swatch tl-state-building" />building</span>
          <span className="tl-legend-item"><span className="tl-legend-swatch tl-state-failed" />build failed</span>
          <span className="tl-legend-item"><span className="tl-legend-swatch tl-state-up tl-legend-fresh" />fresh</span>
          <span className="tl-legend-item"><span className="tl-legend-swatch tl-state-up tl-legend-dirty" />dirty</span>
          <span className="tl-legend-item"><span className="tl-legend-swatch tl-state-pending" />rebuild pending</span>
          <span className="tl-legend-item"><span className="tl-legend-bar" />restart</span>
          <span className="tl-legend-item"><span className="tl-legend-bar tl-legend-request" />rebuild requested</span>
          <span className="tl-legend-item"><span className="tl-legend-version"><span className="tl-version-diamond" /></span>new version</span>
        </span>
        {error && <span className="tl-error" title={error}>timeline unavailable: {error}</span>}
      </div>

      <div className="tl-axis">
        <div className="tl-gutter" />
        <div className="tl-area" title="scroll to pan the window">
          {ticks.map(t => (
            <span key={t} className="tl-tick" style={{ left: `${pct(t, since, now)}%` }}>{formatClock(t)}</span>
          ))}
          {data && live && <span className="tl-tick tl-tick-now" style={{ left: '100%' }}>now</span>}
        </div>
      </div>

      {data && lanes.length === 0 && (
        <div className="service-empty">no host was active in this window</div>
      )}

      {lanes.map(lane => {
        const host = hostById.get(lane.host)
        const lr = lane.tracks.find(t => t.name === 'local-representative')
        const lrRestarts = restarts(lr)
        return (
          <div key={lane.host} className="tl-lane">
            <div
              className="tl-lane-header"
              title={onOpenHost ? "double-click to open this host's system tab" : undefined}
              onDoubleClick={onOpenHost && host ? () => onOpenHost(lane.host) : undefined}
            >
              <span className={`host-dot ${!host ? 'host-dot-unknown' : host.status === 'connected' ? 'host-dot-connected' : 'host-dot-disconnected'}`} />
              <span className="tl-lane-label">{host?.label ?? lane.host}</span>
              {lane.host === data?.self_host_id && <span className="topo-node-self-tag">self</span>}
            </div>
            {SECTION_ORDER.map(section => {
              const tracks = sortTracks(lane.tracks.filter(t => t.section === section))
              if (tracks.length === 0) return null
              return (
                <div key={section} className={`tl-section tl-section-${section}`}>
                  <div className="tl-section-label">{SECTION_LABELS[section]}</div>
                  {tracks.map(track => {
                    // LR's restart bars run through every row it manages.
                    const hostBars = section === 'global' ? [] : lrRestarts
                    const ownBars = track === lr ? [] : restarts(track)
                      .filter(at => inView(at) && !hostBars.some(h => Math.abs(h - at) <= TIMELINE_RESTART_SLOP_SECS))
                    return (
                      <div key={track.name} className={`tl-track${track.main ? ' tl-track-main' : ''}`}>
                        <div className="tl-gutter tl-track-label" title={track.name}>{trackLabel(track.name)}</div>
                        <div className="tl-area">
                          {ticks.map(t => (
                            <span key={t} className="tl-gridline" style={{ left: `${pct(t, since, now)}%` }} />
                          ))}
                          {track.spans.filter(s => s.to >= since && s.from <= now).map((s, i, all) => (
                            <span
                              key={i}
                              className={`tl-laser ${laserClass(s)}`}
                              style={{ left: `${pct(s.from, since, now)}%`, width: `max(2px, ${pct(s.to, since, now) - pct(s.from, since, now)}%)` }}
                              title={spanTitle(track, s, i === all.length - 1)}
                            />
                          ))}
                          {dirtyRanges(track).filter(([from, to]) => to >= since && from <= now).map(([from, to], i) => (
                            <span
                              key={`d${i}`}
                              className="tl-dirty"
                              style={{ left: `${pct(from, since, now)}%`, width: `${pct(to, since, now) - pct(from, since, now)}%` }}
                            />
                          ))}
                          {freshRanges(track).filter(([from, to]) => to >= since && from <= now).map(([from, to], i) => (
                            <span
                              key={`f${i}`}
                              className="tl-fresh"
                              style={{ left: `${pct(from, since, now)}%`, width: `${pct(to, since, now) - pct(from, since, now)}%` }}
                            />
                          ))}
                          {versionChanges(track).filter(m => inView(m.at)).map((m, i, all) => {
                            const at = pct(m.at, since, now)
                            const next = all[i + 1]
                            const tagged = !next || pct(next.at, since, now) - at >= TIMELINE_VERSION_TAG_GAP_PCT
                            return (
                              <span
                                key={`v${m.at}`}
                                className="tl-version"
                                style={{ left: `${at}%` }}
                                title={`${track.name}: new version ${m.prev ?? '?'} → ${m.version ?? '?'}\n${formatClock(m.at, true)}`}
                              >
                                <span className="tl-version-diamond" />
                                {tagged && (
                                  <span className={`tl-version-tag${at > 88 ? ' tl-version-tag-left' : ''}`}>{shortVersion(m.version)}</span>
                                )}
                              </span>
                            )
                          })}
                          {rebuildRequests(track).filter(m => inView(m.at)).map(m => (
                            <span
                              key={`q${m.at}`}
                              className="tl-request"
                              style={{ left: `${pct(m.at, since, now)}%` }}
                              title={`${track.name}: rebuild requested ${m.request ?? ''}\n${formatClock(m.at, true)}`}
                            />
                          ))}
                          {hostBars.filter(inView).map(at => (
                            <span
                              key={`h${at}`}
                              className="tl-restart tl-restart-host"
                              style={{ left: `${pct(at, since, now)}%` }}
                              title={`local-representative restarted at ${formatClock(at, true)}`}
                            />
                          ))}
                          {ownBars.map(at => (
                            <span
                              key={`r${at}`}
                              className="tl-restart"
                              style={{ left: `${pct(at, since, now)}%` }}
                              title={`${track.name} restarted at ${formatClock(at, true)}`}
                            />
                          ))}
                        </div>
                      </div>
                    )
                  })}
                </div>
              )
            })}
          </div>
        )
      })}
    </div>
  )
}
