package main

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// analysisRuns is a control run whose recording (f-rec) started at 10s on
// its LR's clock and recorded both its steps: step 1 from 11s for 2s, step 2
// from 13s for half a second, failing.
func analysisRuns() []demoRunOutput {
	return []demoRunOutput{{
		Phase: "read", Call: "rh", Sequence: "dtt-demo-rh-read", Key: "demo-rh-read-1", Status: "error", Error: "heading never showed",
		Steps: []demoRunStep{
			{Label: "open the page", Status: "success", Message: "opened", StartedAt: 11000, DurationMs: 2000},
			{Label: "wait for heading", Status: "error", Message: "timed out", StartedAt: 13000, DurationMs: 500},
			{Label: "close", Status: "pending"},
		},
		Videos: []demoRunVideo{{FileID: "f-rec", Name: "rec.webm", From: 1, To: 3, StartedAt: 10000, DurationMs: 5000}},
	}}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// TestDemoSpansAndStepMarks: a run's recording is placed in the primary
// video after the title card before it, and each step boundary lands where
// the step started or ended in it.
func TestDemoSpansAndStepMarks(t *testing.T) {
	runs := analysisRuns()
	comps := []demoComp{
		{ID: "other", Call: "rh"},
		{ID: "demo-rh-read-1", Call: "rh", Clips: []demoClip{{Kind: "title", Duration: 3}, {Kind: "media", FileID: "f-rec"}, {Kind: "title", Duration: 2}}},
	}
	p := DemoRender{Composition: "demo-rh-read-1", Call: "rh", Phase: "read"}
	spans, why := demoSpans(p, comps, runs, func(string) float64 { return 0 })
	if why != "" || len(spans) != 1 || !near(spans[0].At, 3) || !near(spans[0].Len, 5) || spans[0].Run.Key != "demo-rh-read-1" {
		t.Fatalf("spans %+v, %q", spans, why)
	}
	marks := demoStepMarks(spans[0], 40)
	want := []struct {
		kind string
		step int
		at   float64
	}{{"step-start", 1, 4}, {"step-end", 1, 5.9}, {"step-end", 2, 6.4}}
	if len(marks) != len(want) {
		t.Fatalf("marks %+v", marks)
	}
	for i, w := range want {
		if m := marks[i]; m.Kind != w.kind || m.Step != w.step || !near(m.At, w.at) {
			t.Errorf("mark %d: %+v, want %+v", i, m, w)
		}
	}
	if marks[2].Status != "error" || marks[2].Message != "timed out" || demoFailingStep(runs[0]) != 2 {
		t.Errorf("the failing step: %+v", marks[2])
	}

	// A clip of unknown length before the recording stops the placing.
	comps[1].Clips = append([]demoClip{{Kind: "media", FileID: "f-unknown"}}, comps[1].Clips...)
	if spans, why := demoSpans(p, comps, runs, func(string) float64 { return 0 }); len(spans) != 0 || !strings.Contains(why, "clip 1") {
		t.Errorf("unknown length: %+v, %q", spans, why)
	}
	// A recording from an older LR has no start time, so no marks.
	old := spans[0]
	old.Video.StartedAt = 0
	if m := demoStepMarks(old, 40); len(m) != 0 {
		t.Errorf("marks without times: %+v", m)
	}
}

// TestDemoMap: the map names each file that's there, lightest first, the
// contact sheet before the rest, and tabulates the step frames.
func TestDemoMap(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, demoArtifactsDir), 0o755)
	os.MkdirAll(filepath.Join(dir, demoAnalysisDir), 0o755)
	for _, f := range []string{demoArtifactsDir + "/" + demoOutputsFile, demoArtifactsDir + "/" + demoImageFile, demoAnalysisDir + "/" + demoContactSheet} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	rec := DemoRecord{ID: "robot-hello-read-1", Demo: "robot-hello-read", Title: "Robot hello (read)", Mode: "assert", Status: "failed", Node: "n2", Dir: dir,
		Error: "phase read failed", Phases: []DemoRecordPhase{{Name: "read", Role: "subject", Status: "failed", Error: "heading never showed"}},
		Renders: []DemoRender{{Composition: "demo-rh-read-1", Phase: "read", Kept: "02-read-subject.webm", Primary: true}}}
	idx := &demoAnalysisIndex{
		Steps: &demoFrameSet{Video: "videos/02-read-subject.webm", Seconds: 10, Dir: "analysis/steps", Frames: []demoFrame{
			{File: "analysis/steps/01-s01-start-open-the-page.jpg", Kind: "step-start", At: 4, Step: 1, Label: "open the page", Status: "starting"},
			{File: "analysis/steps/03-s02-end-wait-for-heading.jpg", Kind: "step-end", At: 6.4, Step: 2, Label: "wait for heading", Status: "error", Message: "timed | out"},
		}},
		Scenes: &demoFrameSet{Dir: "analysis/scenes", ContactSheet: "analysis/contact-sheet.jpg", Frames: []demoFrame{{At: 0}, {At: 65.3}}},
		Failures: []demoFailure{{Run: "demo-rh-read-1", Sequence: "dtt-demo-rh-read", Phase: "read", Status: "error", Step: 2, Label: "wait for heading",
			Note: "its recording rec.webm isn't in this host's files tab (any more)"}},
		Notes: []string{"a note"},
	}
	m := demoMap(rec, idx)
	for _, want := range []string{
		"Read this first", "**failed**", "read (subject) failed -- heading never showed",
		"`analysis/contact-sheet.jpg` (1 B) -- **one image**", "`artifacts/outputs.txt`",
		"| 0:06.4 | `analysis/steps/03-s02-end-wait-for-heading.jpg` | 2 wait for heading | error | timed / out |",
		"1 0:00.0 · 2 1:05.3", "## Failed run demo-rh-read-1", "No frames of it: its recording", "- a note",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("MAP.md lacks %q:\n%s", want, m)
		}
	}
	if strings.Index(m, "contact-sheet.jpg") > strings.Index(m, "outputs.txt") || strings.Index(m, "outputs.txt") > strings.Index(m, "image.json") {
		t.Errorf("MAP.md's files aren't lightest first:\n%s", m)
	}
	if strings.Contains(m, demoLogFile) {
		t.Errorf("MAP.md names a log that isn't there:\n%s", m)
	}
}

// TestDemoAnalysisFFmpeg pulls real frames from a made-up video: step
// frames, scene frames and their contact sheet, and -- the demo failed --
// samples of the whole primary video and of the failed run's recording.
func TestDemoAnalysisFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	files := t.TempDir()
	videos := filepath.Join(dir, demoVideosDir)
	os.MkdirAll(videos, 0o755)
	// Eight seconds: two of red, then a test pattern; the recording is
	// five seconds of the pattern.
	mk := func(out string, args ...string) {
		t.Helper()
		cmd := exec.Command(ff, append(append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...), "-c:v", "mpeg4", "-pix_fmt", "yuv420p", "-f", "mp4", out)...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("this ffmpeg can't make a test video: %v: %s", err, b)
		}
	}
	mk(filepath.Join(videos, "02-read-subject.mp4"), "-f", "lavfi", "-i", "color=c=red:s=320x240:r=10:d=2",
		"-f", "lavfi", "-i", "testsrc=s=320x240:r=10:d=6", "-filter_complex", "[0:v][1:v]concat=n=2:v=1[v]", "-map", "[v]")
	mk(filepath.Join(files, "f-rec"), "-f", "lavfi", "-i", "testsrc=s=320x240:r=10:d=5")

	w := newToolsWatch(nil, nil, func() (string, error) { return "/fake/dtt", nil })
	w.demoFile = func(id string) (string, bool) {
		p := filepath.Join(files, id)
		_, err := os.Stat(p)
		return p, err == nil
	}
	rec := DemoRecord{ID: "robot-hello-read-1", Demo: "robot-hello-read", Mode: "assert", Status: "failed", Dir: dir,
		Renders: []DemoRender{{Composition: "demo-rh-read-1", Call: "rh", Phase: "read", Kept: "02-read-subject.mp4", Primary: true}}}
	img := demoImage{}
	img.Objects = append(img.Objects, struct {
		ID    string         `json:"id"`
		Path  string         `json:"path"`
		Alias string         `json:"alias"`
		Phase string         `json:"phase"`
		State map[string]any `json:"state"`
	}{ID: "dtt.calls[x]", Path: "dtt.calls", Alias: "rh", Phase: "read", State: map[string]any{"image": map[string]any{"objects": []any{
		map[string]any{"id": "demo-rh-read-1", "path": "localrep.compositions", "state": map[string]any{"composition": map[string]any{"video": []any{
			map[string]any{"kind": "title", "duration": 3.0},
			map[string]any{"kind": "media", "file_id": "f-rec"},
		}}}},
	}}}})
	w.analyseDemoRecord(&rec, img, analysisRuns())

	a := rec.Analysis
	if a == nil || a.StepFrames != 3 || a.SceneFrames == 0 || a.FailureFrames == 0 {
		t.Fatalf("analysis %+v", a)
	}
	idx := readDemoAnalysis(dir)
	if idx == nil || idx.Steps == nil || idx.Scenes == nil || idx.Scenes.ContactSheet == "" || idx.Primary == nil || len(idx.Failures) != 1 ||
		idx.Failures[0].Samples == nil || idx.Failures[0].Steps == nil {
		t.Fatalf("analysis.json %+v", idx)
	}
	check := func(rel string) {
		t.Helper()
		if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil || info.Size() == 0 {
			t.Errorf("%s: %v", rel, err)
		}
	}
	for _, f := range idx.Steps.Frames {
		check(f.File)
	}
	for _, f := range idx.Scenes.Frames {
		check(f.File)
	}
	check(idx.Scenes.ContactSheet)
	check(idx.Primary.ContactSheet)
	check(idx.Failures[0].Samples.ContactSheet)
	// The red start and the cut to the pattern are both scenes.
	if len(idx.Scenes.Frames) < 2 || idx.Scenes.Frames[0].At != 0 || idx.Scenes.Frames[1].At < 1.5 || idx.Scenes.Frames[1].At > 2.5 {
		t.Errorf("scenes %+v", idx.Scenes.Frames)
	}
	// Around the failing step: from a second before it (3s into the
	// recording) to the recording's end, every half second.
	s := idx.Failures[0].Samples
	if s.Every != 0.5 || len(s.Frames) < 4 || !near(s.Frames[0].At, 2) {
		t.Errorf("failure samples every %v: %+v", s.Every, s.Frames)
	}
	writeDemoMap(rec)
	if b, _ := os.ReadFile(filepath.Join(dir, demoMapFile)); !strings.Contains(string(b), "## Primary video at each step boundary") {
		t.Errorf("MAP.md:\n%s", b)
	}
}
