package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	ufaversion "ufa-version"
)

// The global system tab's timeline view (privateRepoOpsImpls Step6Prompt.md
// Revision J, see the swimlanes resource beside it): every host that was
// active during the window is a "host lane", split into up to three
// sections -- global (agent-coordinator itself, on the host it runs on), LR
// (local-representative and its internal managed sub-apps) and external
// (managed external apps and plug-ins). Each component is a "laser" drawn
// across the time axis. Nothing upstream keeps any history, so
// agent-coordinator records it here: every timelineSampleInterval it samples
// what it already holds for each connected host into one track per
// component, made of spans (a run of the same state) and marks (a start or
// restart, from the component's started_at moving).

const (
	timelineSampleInterval = 5 * time.Second
	timelineRetention      = 24 * time.Hour
	timelineSaveInterval   = time.Minute

	// A gap longer than this between two samples of the same state starts a
	// new span, so a component that was down (or AC itself was) shows a gap
	// -- unless the component's started_at proves it never stopped.
	timelineGapTolerance = 3 * timelineSampleInterval
)

// Timeline sections, in lane order.
const (
	timelineSectionGlobal   = "global"
	timelineSectionLR       = "lr"
	timelineSectionExternal = "external"
)

// Track states. "down" is never stored: a component that isn't observed
// simply has no span there.
const (
	timelineStateUp       = "up"
	timelineStateBuilding = "building"
	timelineStateFailed   = "failed" // its last build failed
)

// Mark kinds: "start" is the first start seen for a track, "restart" any
// later one (the frontend draws a vertical bar for each restart). "version"
// is the component's reported build version changing (Revision K), drawn as
// an amber diamond on the laser tagged with the new version.
// "rebuild_requested" is a .ufabuild build request arriving on LR's track,
// drawn as a violet tick tagged with the request.
const (
	timelineMarkStart            = "start"
	timelineMarkRestart          = "restart"
	timelineMarkVersion          = "version"
	timelineMarkRebuildRequested = "rebuild_requested"
)

type timelineSpan struct {
	From  int64  `json:"from"` // unix seconds
	To    int64  `json:"to"`
	State string `json:"state"`

	// Dirty marks the stretches where LR's watched repo had uncommitted
	// changes: the laser keeps its own colour and the frontend rings it with
	// an orange dirty halo, in place of the green fresh one.
	Dirty bool `json:"dirty,omitempty"`

	// Pending marks the stretches where a .ufabuild request waited for its
	// rebuild: the frontend draws the laser itself as the building ants in
	// orange.
	Pending bool `json:"pending,omitempty"`
}

type timelineMark struct {
	At   int64  `json:"at"` // unix seconds
	Kind string `json:"kind"`

	// Only on a "version" mark: the version it changed to, and from.
	Version string `json:"version,omitempty"`
	Prev    string `json:"prev,omitempty"`

	// Only on a "rebuild_requested" mark: the .ufabuild request that arrived.
	Request string `json:"request,omitempty"`
}

type timelineTrack struct {
	Name    string `json:"name"`
	Section string `json:"section"`
	Main    bool   `json:"main,omitempty"` // drawn as the fat laser: AC or LR

	Spans []timelineSpan `json:"spans"`
	Marks []timelineMark `json:"marks,omitempty"`

	// StartedAt is the last started_at seen, to spot restarts; 0 for a
	// component that doesn't report one (a connected but unmanaged sub-app).
	StartedAt int64 `json:"started_at,omitempty"`

	// Version is the last build version seen, to spot version changes; ""
	// until the component has reported one.
	Version string `json:"version,omitempty"`

	// AutoRebuildSeconds is the auto-rebuild countdown in the latest sample:
	// how long until LR's 90s auto-rebuild fires, for the right-edge
	// tooltip. 0 when none is brewing.
	AutoRebuildSeconds int `json:"auto_rebuild_seconds,omitempty"`

	// Request is the last .ufabuild request seen, to spot a new one; ""
	// until one appeared, and back to "" once it was handled.
	Request string `json:"request,omitempty"`
}

type timelineLane struct {
	Host   string           `json:"host"`
	Tracks []*timelineTrack `json:"tracks"`
}

// timelineObs is one component's state in one sample.
type timelineObs struct {
	Name      string
	Section   string
	Main      bool
	State     string
	StartedAt int64
	Version   string // "" when the component doesn't report one

	// Dirty, Request and AutoRebuildSeconds are LR's repo-watcher state: the
	// watched repo having uncommitted changes (its dirty halo), a pending
	// .ufabuild build request, and the auto-rebuild countdown. They only
	// matter on LR's own track.
	Dirty              bool
	Request            string
	AutoRebuildSeconds int

	// Pending is a .ufabuild request waiting for its rebuild: the repo
	// watcher's RebuildReady (which already excludes dirty), except while a
	// build runs or once the request's own build failed (see
	// timelineObservations). The frontend draws LR's laser as orange
	// marching ants for as long as it holds.
	Pending bool
}

type timelineRecorder struct {
	mu    sync.Mutex
	lanes map[string]*timelineLane
	path  string // "" keeps the timeline in memory only
	dirty bool
}

// TimelineMsg is GET /api/timeline's answer.
type TimelineMsg struct {
	Now            int64           `json:"now"`
	Since          int64           `json:"since"`
	Until          int64           `json:"until"` // now, unless the view is panned back
	SampleInterval int64           `json:"sample_interval"` // seconds
	SelfHostID     string          `json:"self_host_id"`
	Lanes          []*timelineLane `json:"lanes"`
}

// defaultTimelinePath is where the timeline is kept unless --timeline-file
// says otherwise, so it survives agent-coordinator's own restarts.
func defaultTimelinePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "agent-coordinator", "timeline.json")
}

type timelineFile struct {
	Version int             `json:"version"`
	Lanes   []*timelineLane `json:"lanes"`
}

func newTimelineRecorder(path string) *timelineRecorder {
	t := &timelineRecorder{lanes: map[string]*timelineLane{}, path: path}
	if path == "" {
		return t
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return t
	}
	var f timelineFile
	if err == nil {
		err = json.Unmarshal(b, &f)
	}
	if err != nil {
		log.Printf("timeline: ignoring unreadable %s: %v", path, err)
		return t
	}
	for _, l := range f.Lanes {
		if l != nil && l.Host != "" {
			t.lanes[l.Host] = l
		}
	}
	return t
}

// save writes the timeline to its file if it changed since the last save.
func (t *timelineRecorder) save() {
	t.mu.Lock()
	if t.path == "" || !t.dirty {
		t.mu.Unlock()
		return
	}
	f := timelineFile{Version: 1, Lanes: t.sortedLanesLocked()}
	b, err := json.Marshal(f)
	t.dirty = false
	t.mu.Unlock()
	if err != nil {
		log.Printf("timeline: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		log.Printf("timeline: %v", err)
		return
	}
	tmp := t.path + ".tmp"
	err = os.WriteFile(tmp, b, 0o644)
	if err == nil {
		err = os.Rename(tmp, t.path)
	}
	if err != nil {
		log.Printf("timeline: %v", err)
	}
}

func (t *timelineRecorder) sortedLanesLocked() []*timelineLane {
	out := make([]*timelineLane, 0, len(t.lanes))
	for _, l := range t.lanes {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// observe records one sample of host's components at now. Tracks absent
// from obs get no span for this sample, i.e. they're down.
func (t *timelineRecorder) observe(now int64, host string, obs []timelineObs) {
	t.mu.Lock()
	defer t.mu.Unlock()
	lane := t.lanes[host]
	if lane == nil {
		if len(obs) == 0 {
			return
		}
		lane = &timelineLane{Host: host}
		t.lanes[host] = lane
	}
	for _, o := range obs {
		var tr *timelineTrack
		for _, x := range lane.Tracks {
			if x.Name == o.Name {
				tr = x
				break
			}
		}
		if tr == nil {
			tr = &timelineTrack{Name: o.Name}
			lane.Tracks = append(lane.Tracks, tr)
		}
		tr.Section, tr.Main = o.Section, o.Main
		tr.record(now, o)
	}
	t.dirty = true
}

func (tr *timelineTrack) record(now int64, o timelineObs) {
	var last *timelineSpan
	if n := len(tr.Spans); n > 0 {
		last = &tr.Spans[n-1]
	}
	// started_at moving is a (re)start; the same started_at as before
	// proves it never stopped, however long since the last sample (say,
	// while AC itself was down).
	restarted := false
	sameRun := false
	if o.StartedAt > 0 {
		switch {
		case tr.StartedAt == 0:
			tr.Marks = append(tr.Marks, timelineMark{At: o.StartedAt, Kind: timelineMarkStart})
		case o.StartedAt != tr.StartedAt:
			tr.Marks = append(tr.Marks, timelineMark{At: o.StartedAt, Kind: timelineMarkRestart})
			restarted = true
		default:
			sameRun = true
		}
		tr.StartedAt = o.StartedAt
	}
	// A new version usually arrives with a restart, so place its mark at the
	// restart; otherwise at this sample. An empty version (not reported yet)
	// never counts as a change.
	if o.Version != "" {
		if tr.Version != "" && o.Version != tr.Version {
			at := now
			if restarted {
				at = o.StartedAt
			}
			tr.Marks = append(tr.Marks, timelineMark{At: at, Kind: timelineMarkVersion, Version: o.Version, Prev: tr.Version})
		}
		tr.Version = o.Version
	}
	// A new .ufabuild build request on LR's track: the frontend tags it with
	// what was requested. Only arrivals are marks; the request being handled
	// (cleared) isn't an event worth one.
	if o.Request != tr.Request {
		if o.Request != "" {
			tr.Marks = append(tr.Marks, timelineMark{At: now, Kind: timelineMarkRebuildRequested, Request: o.Request})
		}
		tr.Request = o.Request
	}
	tr.AutoRebuildSeconds = o.AutoRebuildSeconds
	if last != nil && !restarted && (sameRun || now-last.To <= int64(timelineGapTolerance/time.Second)) {
		if last.State == o.State && last.Dirty == o.Dirty && last.Pending == o.Pending {
			last.To = now
			return
		}
		// A change of state (or of repo dirtiness, or of a rebuild waiting)
		// continues the laser without a gap.
		tr.Spans = append(tr.Spans, timelineSpan{From: last.To, To: now, State: o.State, Dirty: o.Dirty, Pending: o.Pending})
		return
	}
	if restarted && last != nil && last.To > o.StartedAt {
		// The last sample of the old run came after the new one started
		// (sampling lag): end the old run at the restart.
		last.To = o.StartedAt
		if last.To < last.From {
			last.To = last.From
		}
	}
	from := now
	// A component seen for the first time (or just restarted) has been up
	// since its started_at, so backfill to it -- but never over what's
	// already recorded, nor past the retention window.
	if o.StartedAt > 0 && o.StartedAt < now {
		from = o.StartedAt
		if last != nil && from < last.To {
			from = last.To
		}
		if floor := now - int64(timelineRetention/time.Second); from < floor {
			from = floor
		}
	}
	tr.Spans = append(tr.Spans, timelineSpan{From: from, To: now, State: o.State, Dirty: o.Dirty, Pending: o.Pending})
}

// prune drops everything that ended before now-timelineRetention.
func (t *timelineRecorder) prune(now int64) {
	cutoff := now - int64(timelineRetention/time.Second)
	t.mu.Lock()
	defer t.mu.Unlock()
	for host, lane := range t.lanes {
		tracks := lane.Tracks[:0]
		for _, tr := range lane.Tracks {
			spans := tr.Spans[:0]
			for _, s := range tr.Spans {
				if s.To >= cutoff {
					spans = append(spans, s)
				}
			}
			tr.Spans = spans
			marks := tr.Marks[:0]
			for _, m := range tr.Marks {
				if m.At >= cutoff {
					marks = append(marks, m)
				}
			}
			tr.Marks = marks
			if len(tr.Spans) > 0 {
				tracks = append(tracks, tr)
			}
		}
		lane.Tracks = tracks
		if len(lane.Tracks) == 0 {
			delete(t.lanes, host)
		}
	}
}

// snapshot returns a copy of every lane with anything in [since, until],
// each track's spans and marks clipped to it.
func (t *timelineRecorder) snapshot(since, until int64) []*timelineLane {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := []*timelineLane{}
	for _, lane := range t.sortedLanesLocked() {
		cp := &timelineLane{Host: lane.Host}
		for _, tr := range lane.Tracks {
			c := &timelineTrack{Name: tr.Name, Section: tr.Section, Main: tr.Main, StartedAt: tr.StartedAt, Version: tr.Version, AutoRebuildSeconds: tr.AutoRebuildSeconds, Request: tr.Request, Spans: []timelineSpan{}}
			for _, s := range tr.Spans {
				if s.To < since || s.From > until {
					continue
				}
				if s.From < since {
					s.From = since
				}
				if s.To > until {
					s.To = until
				}
				c.Spans = append(c.Spans, s)
			}
			for _, m := range tr.Marks {
				if m.At >= since && m.At <= until {
					c.Marks = append(c.Marks, m)
				}
			}
			if len(c.Spans) > 0 || len(c.Marks) > 0 {
				cp.Tracks = append(cp.Tracks, c)
			}
		}
		if len(cp.Tracks) > 0 {
			out = append(out, cp)
		}
	}
	return out
}

// timelineObservations reads one host's components out of what AC already
// holds for it. self adds agent-coordinator's own track: it's on the host
// AC runs on.
func (s *Server) timelineObservations(hs *hostState, self bool) []timelineObs {
	var obs []timelineObs
	if self {
		obs = append(obs, timelineObs{
			Name: "agent-coordinator", Section: timelineSectionGlobal, Main: true,
			State: timelineStateUp, StartedAt: s.startedAt.Unix(), Version: ufaversion.Version,
		})
	}
	if hs == nil {
		return obs
	}
	hs.mu.RLock()
	defer hs.mu.RUnlock()
	if !hs.connected {
		return obs
	}

	var lrStarted int64
	var lrVersion string
	if hs.system != nil {
		lrStarted, lrVersion = hs.system.Self.StartedAt, hs.system.Self.Version
	}
	// The core repo's build (local-representative/ufaplan.go) colours the
	// tracks it covers: with a per-sub-app build only the rebuilt sub-apps'
	// (BuildManaged: their track names), LR's own laser among them only when
	// LR itself was rebuilt; a full build, one LR didn't start, or an LR from
	// before per-sub-app builds, covers LR's laser alone, as before.
	coreState := ""
	covered := map[string]bool{}
	if hs.repo != nil {
		switch {
		case hs.repo.Building:
			coreState = timelineStateBuilding
		case hs.repo.BuildFailed:
			coreState = timelineStateFailed
		}
		if !hs.repo.BuildFull && len(hs.repo.BuildManaged) > 0 {
			for _, n := range hs.repo.BuildManaged {
				covered[n] = true
			}
		} else {
			covered["local-representative"] = true
		}
	}
	stateOf := func(name string) string {
		if coreState != "" && covered[name] {
			return coreState
		}
		return timelineStateUp
	}
	lr := timelineObs{Name: "local-representative", Section: timelineSectionLR, Main: true, State: stateOf("local-representative"), StartedAt: lrStarted, Version: lrVersion}
	// LR's repo signals: while its watcher reports uncommitted changes the
	// laser wears the orange dirty halo, whatever its state; while a
	// .ufabuild request waits for its rebuild it's drawn as orange marching
	// ants. Dirty wins over pending -- rebuild_ready already excludes dirty,
	// since a rebuild would bake in uncommitted changes. Pending isn't shown
	// while a build runs (the request is being handled), nor once the
	// request's own build failed: it then waits for a manual retry, which
	// the red failed state already says.
	if hs.repo != nil && hs.repo.Watched {
		lr.Dirty = hs.repo.Dirty
		lr.Request = hs.repo.UfabuildRequest
		lr.AutoRebuildSeconds = hs.repo.AutoRebuildSeconds
		lr.Pending = hs.repo.RebuildReady && !hs.repo.Building && !hs.repo.UfabuildFailed
	}
	obs = append(obs, lr)

	external, devRepos := timelineExternalApps(hs.plugins, hs.repo)
	seen := map[string]bool{"local-representative": true}
	if hs.system != nil {
		for _, p := range hs.system.Managed {
			if p.Status != "running" || seen[p.Name] {
				continue
			}
			seen[p.Name] = true
			o := timelineObs{Name: p.Name, Section: timelineSectionLR, State: stateOf(p.Name), StartedAt: p.StartedAt, Version: p.Version}
			if external[p.Name] {
				o.Section = timelineSectionExternal
			}
			if d, ok := devRepos[p.Name]; ok {
				o.State = d
			}
			obs = append(obs, o)
		}
	}
	// A sub-app LR reports healthy without having launched it is still up.
	for _, svc := range hs.services {
		if svc.Status != "healthy" || seen[svc.Name] {
			continue
		}
		seen[svc.Name] = true
		section := timelineSectionLR
		if external[svc.Name] {
			section = timelineSectionExternal
		}
		obs = append(obs, timelineObs{Name: svc.Name, Section: section, State: stateOf(svc.Name)})
	}
	// A core sub-app being rebuilt that isn't running (a CLI like dtt, or an
	// app not launched) still gets a building track while it builds.
	if coreState == timelineStateBuilding && hs.repo != nil && !hs.repo.BuildFull {
		for _, n := range hs.repo.BuildManaged {
			if seen[n] {
				continue
			}
			seen[n] = true
			section := timelineSectionLR
			if external[n] {
				section = timelineSectionExternal
			}
			obs = append(obs, timelineObs{Name: n, Section: section, State: timelineStateBuilding})
		}
	}
	// A plug-in dev repo building a plug-in that isn't running yet.
	for appID, st := range devRepos {
		if !seen[appID] && st == timelineStateBuilding {
			obs = append(obs, timelineObs{Name: appID, Section: timelineSectionExternal, State: st})
		}
	}
	return obs
}

// timelineExternalApps returns the app ids that belong in a lane's external
// section -- every plug-in the host knows (registered, first-party or
// approved) and every plug-in dev repo's -- and each dev repo's build state
// (building, failed, or up).
func timelineExternalApps(plugins json.RawMessage, repo *RepoStateMsg) (external map[string]bool, devRepos map[string]string) {
	external = map[string]bool{}
	devRepos = map[string]string{}
	if len(plugins) > 0 {
		var st struct {
			Config struct {
				FirstParty       []string `json:"first_party"`
				ExternalApproved []string `json:"external_approved"`
				ExampleAppID     string   `json:"example_app_id"`
			} `json:"config"`
			Plugins []struct {
				AppID string `json:"app_id"`
			} `json:"plugins"`
		}
		if json.Unmarshal(plugins, &st) == nil {
			for _, id := range append(st.Config.FirstParty, st.Config.ExternalApproved...) {
				external[id] = true
			}
			if st.Config.ExampleAppID != "" {
				external[st.Config.ExampleAppID] = true
			}
			for _, p := range st.Plugins {
				external[p.AppID] = true
			}
		}
	}
	if repo != nil && len(repo.Externals) > 0 {
		var exts []struct {
			Plugin     bool   `json:"plugin"`
			AppID      string `json:"app_id"`
			Building   bool   `json:"building"`
			BuildError string `json:"build_error"`
		}
		if json.Unmarshal(repo.Externals, &exts) == nil {
			for _, e := range exts {
				if !e.Plugin || e.AppID == "" {
					continue
				}
				external[e.AppID] = true
				switch {
				case e.Building:
					devRepos[e.AppID] = timelineStateBuilding
				case e.BuildError != "":
					devRepos[e.AppID] = timelineStateFailed
				default:
					devRepos[e.AppID] = timelineStateUp
				}
			}
		}
	}
	delete(external, "")
	return external, devRepos
}

// sampleTimeline takes one sample of every known host, plus AC's own host
// even when no LR is connected there.
func (s *Server) sampleTimeline(now int64) {
	s.hostsMu.RLock()
	hosts := make(map[string]*hostState, len(s.hostStates))
	for name, hs := range s.hostStates {
		hosts[name] = hs
	}
	s.hostsMu.RUnlock()
	if _, ok := hosts[s.selfHostID]; !ok && s.selfHostID != "" {
		hosts[s.selfHostID] = nil
	}
	for name, hs := range hosts {
		s.timeline.observe(now, name, s.timelineObservations(hs, name == s.selfHostID))
	}
}

// timelineLoop samples, prunes and saves the timeline. Run in its own
// goroutine; never returns.
func (s *Server) timelineLoop() {
	sample := time.NewTicker(timelineSampleInterval)
	save := time.NewTicker(timelineSaveInterval)
	s.sampleTimeline(time.Now().Unix())
	for {
		select {
		case now := <-sample.C:
			s.sampleTimeline(now.Unix())
		case now := <-save.C:
			s.timeline.prune(now.Unix())
			s.timeline.save()
		}
	}
}

// handleTimelineAPI answers GET /api/timeline?since=<unix seconds>[&until=
// <unix seconds>]: every host lane active between them (default: the last
// hour, up to now). until lets the timeline view pan back through history;
// it's capped at now.
func (s *Server) handleTimelineAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	now := time.Now().Unix()
	since := now - 3600
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "since: not a unix time", http.StatusBadRequest)
			return
		}
		since = n
	}
	until := now
	if v := r.URL.Query().Get("until"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "until: not a unix time", http.StatusBadRequest)
			return
		}
		if n < now {
			until = n
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(TimelineMsg{
		Now: now, Since: since, Until: until, SampleInterval: int64(timelineSampleInterval / time.Second),
		SelfHostID: s.selfHostID, Lanes: s.timeline.snapshot(since, until),
	})
}
