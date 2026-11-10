package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func timelineTrackNamed(t *testing.T, tl *timelineRecorder, host, name string) *timelineTrack {
	t.Helper()
	lane := tl.lanes[host]
	if lane == nil {
		t.Fatalf("no lane for %s", host)
	}
	for _, tr := range lane.Tracks {
		if tr.Name == name {
			return tr
		}
	}
	t.Fatalf("no track %s on %s", name, host)
	return nil
}

func TestTimelineSpansMergeAndBackfill(t *testing.T) {
	tl := newTimelineRecorder("")
	lr := func(state string, started int64) []timelineObs {
		return []timelineObs{{Name: "local-representative", Section: timelineSectionLR, Main: true, State: state, StartedAt: started}}
	}
	tl.observe(1000, "h", lr(timelineStateUp, 900))
	tl.observe(1005, "h", lr(timelineStateUp, 900))
	tl.observe(1010, "h", lr(timelineStateBuilding, 900))
	tl.observe(1015, "h", lr(timelineStateBuilding, 900))

	tr := timelineTrackNamed(t, tl, "h", "local-representative")
	want := []timelineSpan{{From: 900, To: 1005, State: timelineStateUp}, {From: 1005, To: 1015, State: timelineStateBuilding}}
	if len(tr.Spans) != len(want) {
		t.Fatalf("spans = %+v, want %+v", tr.Spans, want)
	}
	for i := range want {
		if tr.Spans[i] != want[i] {
			t.Fatalf("spans = %+v, want %+v", tr.Spans, want)
		}
	}
	if len(tr.Marks) != 1 || tr.Marks[0] != (timelineMark{At: 900, Kind: timelineMarkStart}) {
		t.Fatalf("marks = %+v, want one start at 900", tr.Marks)
	}
}

func TestTimelineRestartAndGap(t *testing.T) {
	tl := newTimelineRecorder("")
	obs := func(started int64) []timelineObs {
		return []timelineObs{{Name: "convo", Section: timelineSectionLR, State: timelineStateUp, StartedAt: started}}
	}
	tl.observe(1000, "h", obs(1000))
	tl.observe(1005, "h", obs(1000))
	// Restarted at 1003, seen at 1010: the old run ends at the restart.
	tl.observe(1010, "h", obs(1003))
	// Unseen for a long while (AC down) but the same run: no gap.
	tl.observe(2000, "h", obs(1003))
	// A component without started_at that goes missing gets a gap.
	tl.observe(2000, "h", []timelineObs{{Name: "robot", Section: timelineSectionLR, State: timelineStateUp}})
	tl.observe(3000, "h", []timelineObs{{Name: "robot", Section: timelineSectionLR, State: timelineStateUp}})

	tr := timelineTrackNamed(t, tl, "h", "convo")
	if len(tr.Spans) != 2 || tr.Spans[0].To != 1003 || tr.Spans[1].From != 1003 || tr.Spans[1].To != 2000 {
		t.Fatalf("convo spans = %+v", tr.Spans)
	}
	if len(tr.Marks) != 2 || tr.Marks[1] != (timelineMark{At: 1003, Kind: timelineMarkRestart}) {
		t.Fatalf("convo marks = %+v", tr.Marks)
	}
	rb := timelineTrackNamed(t, tl, "h", "robot")
	if len(rb.Spans) != 2 {
		t.Fatalf("robot spans = %+v, want a gap", rb.Spans)
	}
}

func TestTimelineVersionMarks(t *testing.T) {
	tl := newTimelineRecorder("")
	obs := func(started int64, version string) []timelineObs {
		return []timelineObs{{Name: "hyena", Section: timelineSectionExternal, State: timelineStateUp, StartedAt: started, Version: version}}
	}
	tl.observe(1000, "h", obs(1000, ""))   // not reported yet
	tl.observe(1005, "h", obs(1000, "v1")) // first version: no change
	tl.observe(1010, "h", obs(1000, "v1"))
	tl.observe(1020, "h", obs(1017, "v2")) // restarted into v2: mark at the restart
	tl.observe(1025, "h", obs(1017, ""))   // a blank report isn't a change
	tl.observe(1030, "h", obs(1017, "v3")) // changed without a restart: mark at the sample

	tr := timelineTrackNamed(t, tl, "h", "hyena")
	var got []timelineMark
	for _, m := range tr.Marks {
		if m.Kind == timelineMarkVersion {
			got = append(got, m)
		}
	}
	want := []timelineMark{
		{At: 1017, Kind: timelineMarkVersion, Version: "v2", Prev: "v1"},
		{At: 1030, Kind: timelineMarkVersion, Version: "v3", Prev: "v2"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("version marks = %+v, want %+v", got, want)
	}
	if tr.Version != "v3" {
		t.Fatalf("version = %q, want v3", tr.Version)
	}
}

func TestTimelineSnapshotPruneAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.json")
	tl := newTimelineRecorder(path)
	now := time.Now().Unix()
	old := now - int64(timelineRetention/time.Second) - 100
	// A same-run pair 60s apart (its started_at proves it was up the whole
	// time): the snapshotted spans are clipped to the window.
	robotUp := []timelineObs{{Name: "robot", Section: timelineSectionLR, State: timelineStateUp, StartedAt: now - 60}}
	tl.observe(old, "gone", []timelineObs{{Name: "robot", Section: timelineSectionLR, State: timelineStateUp}})
	tl.observe(now-60, "h", robotUp)
	tl.observe(now, "h", robotUp)

	snap := tl.snapshot(now-30, now)
	if len(snap) != 1 || snap[0].Host != "h" || snap[0].Tracks[0].Spans[0].From != now-30 {
		b, _ := json.Marshal(snap)
		t.Fatalf("snapshot = %s", b)
	}
	// A window panned back clips at until, and drops what's wholly after it.
	snap = tl.snapshot(now-90, now-45)
	if len(snap) != 1 || snap[0].Tracks[0].Spans[0].From != now-60 || snap[0].Tracks[0].Spans[0].To != now-45 {
		b, _ := json.Marshal(snap)
		t.Fatalf("panned snapshot = %s", b)
	}
	if snap = tl.snapshot(now-200, now-100); len(snap) != 0 {
		b, _ := json.Marshal(snap)
		t.Fatalf("snapshot before anything = %s", b)
	}

	tl.prune(now)
	if _, ok := tl.lanes["gone"]; ok {
		t.Fatal("prune kept a lane older than the retention window")
	}
	tl.save()
	back := newTimelineRecorder(path)
	if tr := timelineTrackNamed(t, back, "h", "robot"); len(tr.Spans) != 1 || tr.Spans[0].To != now {
		t.Fatalf("reloaded spans = %+v", tr.Spans)
	}
}

func TestTimelineObservationsSections(t *testing.T) {
	s := newServer()
	plugins := json.RawMessage(`{"config":{"first_party":["cats-ufa-plugin"]},"plugins":[]}`)
	externals := json.RawMessage(`[{"plugin":true,"app_id":"hyena-external-ufa-plugin","building":true}]`)
	hs := &hostState{
		connected: true,
		services:  []ServiceStatus{{Name: "convo", Status: "healthy"}, {Name: "robot", Status: "unhealthy"}},
		system: &SystemStateMsg{
			Self: ProcInfo{StartedAt: 500},
			Managed: []ProcInfo{
				{Name: "convo", Status: "running", StartedAt: 510},
				{Name: "cats-ufa-plugin", Status: "running", StartedAt: 520},
				{Name: "hyena-external-ufa-plugin", Status: "running", StartedAt: 530},
			},
		},
		repo:    &RepoStateMsg{Watched: true, BuildFailed: true, Externals: externals},
		plugins: plugins,
	}
	got := map[string]timelineObs{}
	for _, o := range s.timelineObservations(hs, true) {
		got[o.Name] = o
	}
	check := func(name, section, state string) {
		t.Helper()
		o, ok := got[name]
		if !ok || o.Section != section || o.State != state {
			t.Errorf("%s = %+v (present %v), want section %s state %s", name, o, ok, section, state)
		}
	}
	check("agent-coordinator", timelineSectionGlobal, timelineStateUp)
	check("local-representative", timelineSectionLR, timelineStateFailed)
	check("convo", timelineSectionLR, timelineStateUp)
	check("cats-ufa-plugin", timelineSectionExternal, timelineStateUp)
	check("hyena-external-ufa-plugin", timelineSectionExternal, timelineStateBuilding)
	if _, ok := got["robot"]; ok {
		t.Error("an unhealthy, unmanaged sub-app was observed as up")
	}
	if got["convo"].StartedAt != 510 {
		t.Errorf("convo started_at = %d, want the managed one", got["convo"].StartedAt)
	}
}

// TestTimelineObservationsPerSubAppBuild checks that a per-sub-app core
// build colours only the rebuilt sub-apps' tracks -- not LR's, which wasn't
// rebuilt -- and gives a rebuilt sub-app that isn't running a building track.
func TestTimelineObservationsPerSubAppBuild(t *testing.T) {
	s := newServer()
	hs := &hostState{
		connected: true,
		system: &SystemStateMsg{
			Self: ProcInfo{StartedAt: 500},
			Managed: []ProcInfo{
				{Name: "convo", Status: "running", StartedAt: 510},
				{Name: "sessions", Status: "running", StartedAt: 520},
			},
		},
		repo: &RepoStateMsg{Watched: true, Building: true, BuildApps: []string{"dtt", "the-conversationalist"}, BuildManaged: []string{"convo", "dtt"}},
	}
	got := map[string]timelineObs{}
	for _, o := range s.timelineObservations(hs, false) {
		got[o.Name] = o
	}
	for name, want := range map[string]string{
		"local-representative": timelineStateUp,
		"convo":                timelineStateBuilding,
		"sessions":             timelineStateUp,
		"dtt":                  timelineStateBuilding,
	} {
		if o, ok := got[name]; !ok || o.State != want {
			t.Errorf("%s = %+v (present %v), want state %s", name, o, ok, want)
		}
	}

	// A full build still colours LR's laser alone.
	hs.repo = &RepoStateMsg{Watched: true, Building: true, BuildFull: true, BuildManaged: []string{"convo"}}
	got = map[string]timelineObs{}
	for _, o := range s.timelineObservations(hs, false) {
		got[o.Name] = o
	}
	if got["local-representative"].State != timelineStateBuilding || got["convo"].State != timelineStateUp {
		t.Errorf("full build: lr %+v convo %+v", got["local-representative"], got["convo"])
	}
}

// TestTimelineDirtyHandshake checks the repo signals on LR's track: a dirty
// interval splits the laser into halo-carrying spans without gaps, a
// pending interval carries its flag the same way, a .ufabuild request
// records a rebuild_requested mark only when it arrives, the auto-rebuild
// countdown follows the latest sample (clearing once it's over), and all of
// it survives a save and reload.
func TestTimelineDirtyHandshake(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.json")
	tl := newTimelineRecorder(path)
	obs := func(dirty, pending bool, request string, autoSeconds int) []timelineObs {
		return []timelineObs{{
			Name: "local-representative", Section: timelineSectionLR, Main: true, State: timelineStateUp, StartedAt: 900,
			Dirty: dirty, Pending: pending, Request: request, AutoRebuildSeconds: autoSeconds,
		}}
	}
	tl.observe(1000, "h", obs(false, false, "", 0))           // clean, nothing brewing
	tl.observe(1005, "h", obs(true, false, "api change", 42)) // dirty: the halo, never the dots
	tl.observe(1010, "h", obs(false, true, "api change", 42)) // committed: pending
	tl.observe(1015, "h", obs(false, false, "", 0))           // built: neither, countdown over

	tr := timelineTrackNamed(t, tl, "h", "local-representative")
	if tr.AutoRebuildSeconds != 0 {
		t.Errorf("auto_rebuild_seconds = %d after the countdown ended, want 0", tr.AutoRebuildSeconds)
	}

	tl.observe(1020, "h", obs(false, true, "ui change", 5)) // a new request arrives
	want := []timelineSpan{
		{From: 900, To: 1000, State: timelineStateUp},
		{From: 1000, To: 1005, State: timelineStateUp, Dirty: true},
		{From: 1005, To: 1010, State: timelineStateUp, Pending: true},
		{From: 1010, To: 1015, State: timelineStateUp},
		{From: 1015, To: 1020, State: timelineStateUp, Pending: true},
	}
	if len(tr.Spans) != len(want) {
		t.Fatalf("spans = %+v, want %+v", tr.Spans, want)
	}
	for i := range want {
		if tr.Spans[i] != want[i] {
			t.Fatalf("spans[%d] = %+v, want %+v", i, tr.Spans[i], want[i])
		}
	}
	var marks []timelineMark
	for _, m := range tr.Marks {
		if m.Kind == timelineMarkRebuildRequested {
			marks = append(marks, m)
		}
	}
	wantMarks := []timelineMark{
		{At: 1005, Kind: timelineMarkRebuildRequested, Request: "api change"},
		{At: 1020, Kind: timelineMarkRebuildRequested, Request: "ui change"},
	}
	if len(marks) != len(wantMarks) || marks[0] != wantMarks[0] || marks[1] != wantMarks[1] {
		t.Fatalf("rebuild_requested marks = %+v, want %+v", marks, wantMarks)
	}
	if tr.AutoRebuildSeconds != 5 {
		t.Errorf("auto_rebuild_seconds = %d, want 5", tr.AutoRebuildSeconds)
	}
	if tr.Request != "ui change" {
		t.Errorf("request = %q, want the last one", tr.Request)
	}

	tl.save()
	back := timelineTrackNamed(t, newTimelineRecorder(path), "h", "local-representative")
	if len(back.Spans) != len(want) || back.Spans[1] != want[1] || back.Spans[2] != want[2] || back.AutoRebuildSeconds != 5 {
		t.Errorf("reloaded spans = %+v (auto %d), want %+v (auto 5)", back.Spans, back.AutoRebuildSeconds, want)
	}
}

// TestTimelineObservationsDirty checks that the dirty halo and rebuild-
// pending signals only ride on LR's own track, only when the repo is
// watched, and that dirty (never Pending at the same time -- RebuildReady
// already excludes it) doesn't touch the state-change machinery.
func TestTimelineObservationsDirty(t *testing.T) {
	s := newServer()
	hs := &hostState{
		connected: true,
		system: &SystemStateMsg{
			Self: ProcInfo{StartedAt: 500},
			Managed: []ProcInfo{
				{Name: "convo", Status: "running", StartedAt: 510},
			},
		},
		repo: &RepoStateMsg{Watched: true, Dirty: true, AutoRebuildPending: true, AutoRebuildSeconds: 42, UfabuildRequest: "api change"},
	}
	got := map[string]timelineObs{}
	for _, o := range s.timelineObservations(hs, true) {
		got[o.Name] = o
	}
	lr := got["local-representative"]
	if !lr.Dirty || lr.Pending || lr.State != timelineStateUp || lr.Request != "api change" || lr.AutoRebuildSeconds != 42 {
		t.Errorf("lr obs = %+v, want dirty with the request and countdown but no dots", lr)
	}
	if o := got["convo"]; o.Dirty || o.Pending || o.Request != "" || o.AutoRebuildSeconds != 0 {
		t.Errorf("convo obs = %+v, want no dirty-halo signals", o)
	}

	// Clean with a waiting request: rebuild_ready is pending, and the
	// countdown rides along.
	hs.repo = &RepoStateMsg{Watched: true, RebuildReady: true, AutoRebuildPending: true, AutoRebuildSeconds: 42, UfabuildRequest: "api change"}
	got = map[string]timelineObs{}
	for _, o := range s.timelineObservations(hs, true) {
		got[o.Name] = o
	}
	if lr = got["local-representative"]; !lr.Pending || lr.Dirty || lr.AutoRebuildSeconds != 42 {
		t.Errorf("clean obs = %+v, want pending without the halo", lr)
	}

	// The request being built isn't pending any more; it's building.
	hs.repo = &RepoStateMsg{Watched: true, RebuildReady: true, Building: true, UfabuildRequest: "api change"}
	got = map[string]timelineObs{}
	for _, o := range s.timelineObservations(hs, true) {
		got[o.Name] = o
	}
	if lr = got["local-representative"]; lr.Pending || lr.State != timelineStateBuilding {
		t.Errorf("building obs = %+v, want building and not pending", lr)
	}

	// Its build failed: LR leaves it ready for a manual retry, but it's no
	// longer waiting on its own -- the laser is the red failed state alone.
	hs.repo = &RepoStateMsg{Watched: true, RebuildReady: true, BuildFailed: true, UfabuildFailed: true, UfabuildRequest: "api change"}
	got = map[string]timelineObs{}
	for _, o := range s.timelineObservations(hs, true) {
		got[o.Name] = o
	}
	if lr = got["local-representative"]; lr.Pending || lr.State != timelineStateFailed {
		t.Errorf("failed obs = %+v, want failed and not pending", lr)
	}

	// A new request after the failure is pending again, next to the red.
	hs.repo = &RepoStateMsg{Watched: true, RebuildReady: true, BuildFailed: true, UfabuildRequest: "ui change"}
	got = map[string]timelineObs{}
	for _, o := range s.timelineObservations(hs, true) {
		got[o.Name] = o
	}
	if lr = got["local-representative"]; !lr.Pending || lr.State != timelineStateFailed {
		t.Errorf("new request after a failure: obs = %+v, want failed and pending", lr)
	}

	// An LR without --dev-repo has no watcher at all.
	hs.repo = &RepoStateMsg{Watched: false, Dirty: true, AutoRebuildSeconds: 42}
	got = map[string]timelineObs{}
	for _, o := range s.timelineObservations(hs, false) {
		got[o.Name] = o
	}
	if lr = got["local-representative"]; lr.Dirty || lr.Pending || lr.AutoRebuildSeconds != 0 {
		t.Errorf("unwatched repo: lr obs = %+v, want no dirty-halo signals", lr)
	}
}
