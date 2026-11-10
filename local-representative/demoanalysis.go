package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A demo record's analysis/ (PrivateRepoOps Step 5, Revision A): frames
// pulled out of its videos with ffmpeg, so a reviewing agent (demoreview.go)
// can see what the videos show without watching them. It's a compilation
// output -- everything in it can be made again from videos/, the image and
// the files tab -- so it's kept apart from artifacts/ (what the run itself
// left) and is made afresh each time (analyseDemoRecord):
//
//	analysis/
//	    analysis.json                    demoAnalysisIndex: every frame, its time, step and result
//	    steps/<n>-s<step>-<start|end>-<label>.jpg
//	                                     the primary video at each step boundary of the
//	                                     control runs it shows
//	    scenes/<n>-<time>.jpg            the primary video wherever its picture changes a lot
//	    contact-sheet.jpg                those scene frames tiled into one image
//	    failure/                         only when the demo failed:
//	        primary/frames/, contact-sheet.jpg
//	                                     the whole primary video, sampled evenly
//	        <n>-<run key>/steps/, frames/, contact-sheet.jpg
//	                                     a failed control run's own recording (out of the
//	                                     files tab): its step boundaries, and frames close
//	                                     together around the step that failed
//
// A step's time in a video is where its recording sits in the video (the
// composition's clips before it: title cards' durations, other media's
// lengths) plus how long after the recording started the step did, from the
// start times LR's control runs carry since this revision (ControlStepResult
// and ControlRecording.StartedAt, through dtt's localrep.control.runs
// state). A record from an older LR has no step frames, only scenes.
//
// MAP.md, beside record.json, is the record's map (demoMap): short, it says
// what each file is and how big, lightest first, with the frames' tables --
// the first thing a reviewing agent reads, before it decides what else to
// bring into its context.

const (
	demoAnalysisDir      = "analysis"
	demoAnalysisFile     = "analysis.json"
	demoAnalysisFormat   = "lr-demo-analysis-v1"
	demoMapFile          = "MAP.md"
	demoContactSheet     = "contact-sheet.jpg"
	maxDemoStepFrames    = 40
	maxDemoSceneFrames   = 30
	maxDemoFailureFrames = 40
	demoSceneThreshold   = 0.3
	demoFailureLead      = 1.0 // seconds of a failed run's recording sampled before its failing step
	demoFailureTail      = 3.0 // ...and after it ended
	demoFFmpegTimeout    = 3 * time.Minute
)

// demoFFmpeg finds ffmpeg (a var so tests can do without it).
var demoFFmpeg = func() (string, error) {
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", errors.New("ffmpeg isn't installed on this host, so no frames were pulled from the videos")
	}
	return p, nil
}

// demoFFprobe is the ffprobe beside ffmpeg, else the one on PATH, else "".
func demoFFprobe(ffmpeg string) string {
	beside := filepath.Join(filepath.Dir(ffmpeg), "ffprobe")
	if info, err := os.Stat(beside); err == nil && !info.IsDir() {
		return beside
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		return p
	}
	return ""
}

// DemoAnalysis is what a record's analysis/ holds, in brief, for
// record.json; analysis.json has the rest.
type DemoAnalysis struct {
	Video         string   `json:"video,omitempty"` // the primary video it read, in videos/
	StepFrames    int      `json:"step_frames"`
	SceneFrames   int      `json:"scene_frames"`
	FailureFrames int      `json:"failure_frames,omitempty"`
	Sheets        []string `json:"contact_sheets,omitempty"` // in the record, the scenes' first
	Notes         []string `json:"notes,omitempty"`
}

// demoFrame is one frame in analysis/.
type demoFrame struct {
	File    string  `json:"file"` // its path in the record
	Kind    string  `json:"kind"` // step-start, step-end, scene or sample
	At      float64 `json:"at"`   // seconds into its video
	Run     string  `json:"run,omitempty"`
	Step    int     `json:"step,omitempty"` // 1-based
	Label   string  `json:"label,omitempty"`
	Status  string  `json:"status,omitempty"`
	Message string  `json:"message,omitempty"`
}

// demoFrameSet is the frames pulled from one video for one purpose.
type demoFrameSet struct {
	Title        string      `json:"title"`
	Video        string      `json:"video"`             // videos/<file>, or "files tab: <name>"
	Seconds      float64     `json:"seconds,omitempty"` // the video's length, when known
	Dir          string      `json:"dir"`               // in the record
	ContactSheet string      `json:"contact_sheet,omitempty"`
	Every        float64     `json:"every,omitempty"` // seconds between samples
	Frames       []demoFrame `json:"frames"`
}

// demoFailure is a failed control run, and the frames of it there are.
type demoFailure struct {
	Run      string        `json:"run"`
	Sequence string        `json:"sequence"`
	Phase    string        `json:"phase"`
	Call     string        `json:"call,omitempty"`
	Status   string        `json:"status"`
	Error    string        `json:"error,omitempty"`
	Step     int           `json:"step,omitempty"`
	Label    string        `json:"label,omitempty"`
	Steps    *demoFrameSet `json:"steps,omitempty"`
	Samples  *demoFrameSet `json:"samples,omitempty"`
	Note     string        `json:"note,omitempty"`
}

// demoAnalysisIndex is analysis.json.
type demoAnalysisIndex struct {
	Format   string        `json:"format"`
	Record   string        `json:"record"`
	Made     int64         `json:"made"` // unix ms
	Steps    *demoFrameSet `json:"steps,omitempty"`
	Scenes   *demoFrameSet `json:"scenes,omitempty"`
	Primary  *demoFrameSet `json:"primary_samples,omitempty"` // a failed demo's primary video, sampled evenly
	Failures []demoFailure `json:"failures,omitempty"`
	Notes    []string      `json:"notes,omitempty"`
}

// ---- What's in the image ----

// demoComp is a composition in a demo's image, with its video track.
type demoComp struct {
	ID, Call string
	Clips    []demoClip
}

type demoClip struct {
	Kind, FileID string
	Duration     float64
}

// demoSubImage is the image a dtt.calls object keeps in its state, and the
// call aliases down to it, as demoRenders follows them.
func demoSubImage(state map[string]any, id, alias, call string, depth int) (demoImage, string, bool) {
	sub, ok := state["image"]
	if !ok || depth >= maxDemoCallDepth {
		return demoImage{}, "", false
	}
	b, err := json.Marshal(sub)
	if err != nil {
		return demoImage{}, "", false
	}
	var img demoImage
	if json.Unmarshal(b, &img) != nil {
		return demoImage{}, "", false
	}
	if alias == "" {
		alias = id
	}
	if call != "" {
		alias = call + "/" + alias
	}
	return img, alias, true
}

// demoComps are the compositions in img and in its calls' images.
func demoComps(img demoImage, call string, depth int) []demoComp {
	var out []demoComp
	for _, o := range img.Objects {
		switch o.Path {
		case "localrep.compositions":
			c := demoComp{ID: o.ID, Call: call}
			comp, _ := o.State["composition"].(map[string]any)
			video, _ := comp["video"].([]any)
			for _, x := range video {
				m, _ := x.(map[string]any)
				var cl demoClip
				cl.Kind, _ = m["kind"].(string)
				cl.FileID, _ = m["file_id"].(string)
				cl.Duration, _ = m["duration"].(float64)
				c.Clips = append(c.Clips, cl)
			}
			out = append(out, c)
		case "dtt.calls":
			if sub, alias, ok := demoSubImage(o.State, o.ID, o.Alias, call, depth); ok {
				out = append(out, demoComps(sub, alias, depth+1)...)
			}
		}
	}
	return out
}

// demoSpan is a control run's recording inside a video: it starts At
// seconds in and lasts Len (0: not known).
type demoSpan struct {
	At, Len float64
	Run     *demoRunOutput
	Video   demoRunVideo
}

// demoSpans places the control runs' recordings in render p's video, from
// its composition's clips; length gives a media clip's length in seconds
// (0: not known). Placing stops at a clip of unknown length, with why.
func demoSpans(p DemoRender, comps []demoComp, runs []demoRunOutput, length func(fileID string) float64) ([]demoSpan, string) {
	var comp *demoComp
	for i := range comps {
		if comps[i].ID == p.Composition && comps[i].Call == p.Call {
			comp = &comps[i]
			break
		}
	}
	if comp == nil {
		return nil, fmt.Sprintf("the primary video's composition (%s) isn't in the image, so its step times aren't known", p.Composition)
	}
	var spans []demoSpan
	at := 0.0
	for k, c := range comp.Clips {
		var run *demoRunOutput
		var vid demoRunVideo
		if c.Kind == "media" && c.FileID != "" {
			for i := range runs {
				for _, v := range runs[i].Videos {
					if v.FileID == c.FileID {
						run, vid = &runs[i], v
					}
				}
			}
		}
		n := c.Duration
		if n <= 0 && c.Kind == "media" {
			n = length(c.FileID)
			if n <= 0 && run != nil && vid.DurationMs > 0 {
				n = float64(vid.DurationMs) / 1000
			}
		}
		if run != nil {
			spans = append(spans, demoSpan{At: at, Len: n, Run: run, Video: vid})
		}
		if n <= 0 {
			if k < len(comp.Clips)-1 {
				return spans, fmt.Sprintf("clip %d of %s has no length known here, so the clips after it couldn't be placed", k+1, p.Composition)
			}
			break
		}
		at += n
	}
	if len(spans) == 0 {
		return nil, "the primary video shows no control run's recording, so it has no step frames"
	}
	return spans, ""
}

// demoStepMarks are the times in span s's video to take a frame at: the
// start of the recording's first step, then the end of each step it
// recorded (the moment before the next one starts), up to max. A
// recording or step without a start time (an older LR) has none.
func demoStepMarks(s demoSpan, max int) []demoFrame {
	r, v := s.Run, s.Video
	if v.StartedAt == 0 || len(r.Steps) == 0 {
		return nil
	}
	from, to := v.From, v.To
	if from < 1 || from > len(r.Steps) {
		from, to = 1, len(r.Steps)
	}
	if to < from || to > len(r.Steps) {
		to = len(r.Steps)
	}
	clamp := func(t float64) float64 {
		if s.Len > 0 && t > s.At+s.Len-0.05 {
			t = s.At + s.Len - 0.05
		}
		return math.Max(t, s.At)
	}
	var out []demoFrame
	for i := from; i <= to && len(out) < max; i++ {
		st := r.Steps[i-1]
		if st.StartedAt == 0 {
			continue // never reached
		}
		start := s.At + float64(st.StartedAt-v.StartedAt)/1000
		f := demoFrame{Run: r.Key, Step: i, Label: st.Label, Status: st.Status, Message: st.Message}
		if len(out) == 0 {
			if max < 2 {
				break
			}
			first := f
			first.Kind, first.At, first.Status, first.Message = "step-start", clamp(start), "starting", ""
			out = append(out, first)
		}
		f.Kind, f.At = "step-end", clamp(start+float64(st.DurationMs)/1000-0.1)
		out = append(out, f)
	}
	return out
}

// demoRunFailed says whether a control run in a demo's image ended badly.
func demoRunFailed(r demoRunOutput) bool {
	return r.Status == "error" || r.Status == "cancelled"
}

// demoFailingStep is the 1-based step run r failed at, or 0.
func demoFailingStep(r demoRunOutput) int {
	last := 0
	for i, st := range r.Steps {
		if st.Status == "error" {
			return i + 1
		}
		if st.StartedAt > 0 {
			last = i + 1
		}
	}
	return last
}

// ---- Pulling frames ----

type demoAnalyser struct {
	ffmpeg, ffprobe string
	recDir          string
	notes           *[]string
}

func (a *demoAnalyser) note(format string, args ...any) {
	*a.notes = append(*a.notes, fmt.Sprintf(format, args...))
}

// run runs ffmpeg with args, answering its stderr.
func (a *demoAnalyser) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), demoFFmpegTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.ffmpeg, append([]string{"-hide_banner", "-nostdin", "-y"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return stderr.String(), fmt.Errorf("ffmpeg: %v: %s", err, msg)
	}
	return stderr.String(), nil
}

var demoStatsTimeRe = regexp.MustCompile(`time=(\d+):(\d+):(\d+(?:\.\d+)?)`)

// seconds is how long the video at src is: ffprobe's word for it, else how
// far ffmpeg got reading it through (a browser's WebM often has no length
// in its header). 0 when neither says.
func (a *demoAnalyser) seconds(src string) float64 {
	if a.ffprobe != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, a.ffprobe, "-v", "error", "-show_entries", "format=duration",
			"-of", "default=noprint_wrappers=1:nokey=1", src).Output()
		cancel()
		if d, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil && perr == nil && d > 0 {
			return d
		}
	}
	stderr, err := a.run("-loglevel", "info", "-stats", "-i", src, "-map", "0:v:0", "-f", "null", "-")
	if err != nil {
		return 0
	}
	m := demoStatsTimeRe.FindAllStringSubmatch(stderr, -1)
	if len(m) == 0 {
		return 0
	}
	last := m[len(m)-1]
	h, _ := strconv.ParseFloat(last[1], 64)
	min, _ := strconv.ParseFloat(last[2], 64)
	s, _ := strconv.ParseFloat(last[3], 64)
	return h*3600 + min*60 + s
}

// frame writes the frame at seconds at of src to out, a JPEG at most
// width wide.
func (a *demoAnalyser) frame(src string, at float64, out string, width int) error {
	if _, err := a.run("-loglevel", "error", "-ss", fmt.Sprintf("%.3f", at), "-i", src, "-frames:v", "1",
		"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", width), "-q:v", "3", out); err != nil {
		return err
	}
	if _, err := os.Stat(out); err != nil {
		return fmt.Errorf("no frame at %s", demoClock(at))
	}
	return nil
}

// numbered lists dir's frames ffmpeg numbered (01.jpg, 02.jpg, ...), in order.
func numbered(dir string) []string {
	var out []string
	for n := 1; ; n++ {
		p := filepath.Join(dir, fmt.Sprintf("%02d.jpg", n))
		if _, err := os.Stat(p); err != nil {
			return out
		}
		out = append(out, p)
	}
}

// contactSheet tiles dir's n numbered frames into one image, out, six to
// a row.
func (a *demoAnalyser) contactSheet(dir string, n int, out string) error {
	if n == 0 {
		return errors.New("no frames to tile")
	}
	cols := n
	if cols > 6 {
		cols = 6
	}
	rows := (n + cols - 1) / cols
	_, err := a.run("-loglevel", "error", "-framerate", "1", "-start_number", "1", "-i", filepath.Join(dir, "%02d.jpg"),
		"-vf", fmt.Sprintf("scale=320:-2,tile=%dx%d:padding=4:margin=4", cols, rows), "-frames:v", "1", "-q:v", "3", out)
	return err
}

// rel is p's path in the record.
func (a *demoAnalyser) rel(p string) string {
	if r, err := filepath.Rel(a.recDir, p); err == nil {
		return filepath.ToSlash(r)
	}
	return p
}

// stepFrames takes a frame at each of marks in src into dir.
func (a *demoAnalyser) stepFrames(src string, marks []demoFrame, dir string) []demoFrame {
	if len(marks) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.note("making %s: %v", a.rel(dir), err)
		return nil
	}
	var out []demoFrame
	for i, f := range marks {
		which := "end"
		if f.Kind == "step-start" {
			which = "start"
		}
		p := filepath.Join(dir, fmt.Sprintf("%02d-s%02d-%s-%s.jpg", i+1, f.Step, which, demoSlug(f.Label)))
		if err := a.frame(src, f.At, p, 1280); err != nil {
			a.note("step %d (%s) at %s: %v", f.Step, f.Label, demoClock(f.At), err)
			continue
		}
		f.File = a.rel(p)
		out = append(out, f)
	}
	return out
}

var demoShowinfoTimeRe = regexp.MustCompile(`pts_time:\s*([0-9.]+)`)

// sceneFrames takes src's first frame and those where its picture changes
// a lot, up to maxDemoSceneFrames, into dir, and tiles them into sheet.
func (a *demoAnalyser) sceneFrames(src, dir, sheet string) ([]demoFrame, bool) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.note("making %s: %v", a.rel(dir), err)
		return nil, false
	}
	filter := fmt.Sprintf(`select=eq(n\,0)+gt(scene\,%.2f),showinfo,scale='min(640,iw)':-2`, demoSceneThreshold)
	stderr, err := a.run("-loglevel", "info", "-i", src, "-vf", filter, "-vsync", "vfr",
		"-frames:v", strconv.Itoa(maxDemoSceneFrames), "-q:v", "4", filepath.Join(dir, "%02d.jpg"))
	if err != nil {
		a.note("scene changes: %v", err)
		return nil, false
	}
	var times []float64
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.Contains(line, "showinfo") {
			continue
		}
		if m := demoShowinfoTimeRe.FindStringSubmatch(line); m != nil {
			t, _ := strconv.ParseFloat(m[1], 64)
			times = append(times, t)
		}
	}
	files := numbered(dir)
	madeSheet := false
	if err := a.contactSheet(dir, len(files), sheet); err != nil {
		a.note("the scene changes' contact sheet: %v", err)
	} else {
		madeSheet = true
	}
	var out []demoFrame
	for i, p := range files {
		f := demoFrame{Kind: "scene", At: -1}
		if i < len(times) {
			f.At = times[i]
		}
		f.File = a.rel(a.renameTimed(p, i+1, f.At))
		out = append(out, f)
	}
	return out, madeSheet
}

// samples takes frames of src every so often from from to to seconds (at
// most max, no closer than min apart) into dir, and tiles them into sheet.
func (a *demoAnalyser) samples(src string, from, to, minEvery float64, max int, dir, sheet string) ([]demoFrame, float64, bool) {
	if to <= from {
		to = from + 1
	}
	every := math.Max(minEvery, (to-from)/float64(max))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.note("making %s: %v", a.rel(dir), err)
		return nil, every, false
	}
	if _, err := a.run("-loglevel", "error", "-ss", fmt.Sprintf("%.3f", from), "-t", fmt.Sprintf("%.3f", to-from), "-i", src,
		"-vf", fmt.Sprintf("fps=1/%.3f,scale='min(960,iw)':-2", every), "-frames:v", strconv.Itoa(max), "-q:v", "4",
		filepath.Join(dir, "%02d.jpg")); err != nil {
		a.note("sampling %s: %v", a.rel(dir), err)
		return nil, every, false
	}
	files := numbered(dir)
	madeSheet := false
	if err := a.contactSheet(dir, len(files), sheet); err != nil {
		a.note("%s's contact sheet: %v", a.rel(dir), err)
	} else {
		madeSheet = true
	}
	var out []demoFrame
	for i, p := range files {
		at := from + float64(i)*every
		out = append(out, demoFrame{Kind: "sample", At: at, File: a.rel(a.renameTimed(p, i+1, at))})
	}
	return out, every, madeSheet
}

// renameTimed renames frame p (ffmpeg's NN.jpg) to NN-<time>.jpg, so its
// name says where in the video it is; it answers the new path, or p.
func (a *demoAnalyser) renameTimed(p string, n int, at float64) string {
	if at < 0 {
		return p
	}
	q := filepath.Join(filepath.Dir(p), fmt.Sprintf("%02d-%s.jpg", n, demoFileClock(at)))
	if os.Rename(p, q) != nil {
		return p
	}
	return q
}

// ---- The record's analysis ----

// analyseDemoRecord makes rec's analysis/ afresh from its primary video and,
// when the demo failed, its failed runs' recordings, and sets
// rec.Analysis. img is the demo's image; runs its control runs in it.
func (w *toolsWatch) analyseDemoRecord(rec *DemoRecord, img demoImage, runs []demoRunOutput) {
	dir := filepath.Join(rec.Dir, demoAnalysisDir)
	os.RemoveAll(dir)
	idx := demoAnalysisIndex{Format: demoAnalysisFormat, Record: rec.ID, Made: time.Now().UnixMilli()}
	sum := &DemoAnalysis{}
	defer func() {
		sum.Notes = idx.Notes
		sets := []*demoFrameSet{idx.Scenes, idx.Primary}
		for _, f := range idx.Failures {
			sets = append(sets, f.Samples)
		}
		for _, s := range sets {
			if s != nil && s.ContactSheet != "" {
				sum.Sheets = append(sum.Sheets, s.ContactSheet)
			}
		}
		rec.Analysis = sum
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		b, _ := json.MarshalIndent(idx, "", "  ")
		os.WriteFile(filepath.Join(dir, demoAnalysisFile), append(b, '\n'), 0o644)
	}()

	failed := rec.Status == "failed"
	var failedRuns []int
	for i, r := range runs {
		if demoRunFailed(r) {
			failed = true
			failedRuns = append(failedRuns, i)
		}
	}
	ffmpeg, err := demoFFmpeg()
	if err != nil {
		idx.Notes = append(idx.Notes, err.Error())
		return
	}
	a := &demoAnalyser{ffmpeg: ffmpeg, recDir: rec.Dir, notes: &idx.Notes}
	a.ffprobe = demoFFprobe(ffmpeg)
	fileLength := func(id string) float64 {
		if path, ok := w.demoFilePath(id); ok {
			return a.seconds(path)
		}
		return 0
	}

	// The primary video: its step boundaries and scene changes -- and, when
	// the demo failed, frames across the whole of it.
	switch p := primaryRender(rec.Renders); {
	case p == nil:
		a.note("the record has no primary video, so there are no step or scene frames")
	case p.Kept == "":
		a.note("the primary video (%s) wasn't kept with the record (%s), so there are no step or scene frames", p.Composition, p.KeepError)
	default:
		src := filepath.Join(rec.Dir, demoVideosDir, p.Kept)
		video := demoVideosDir + "/" + p.Kept
		length := a.seconds(src)
		sum.Video = video
		spans, why := demoSpans(*p, demoComps(img, "", 0), runs, fileLength)
		if why != "" {
			a.note("%s", why)
		}
		var marks []demoFrame
		for _, s := range spans {
			if s.Video.StartedAt == 0 {
				a.note("run %s's recording has no start time (its LR is from before step times were kept), so it has no step frames", s.Run.Key)
				continue
			}
			marks = append(marks, demoStepMarks(s, maxDemoStepFrames-len(marks))...)
		}
		if length > 0 {
			for i := range marks {
				marks[i].At = math.Min(marks[i].At, math.Max(0, length-0.05))
			}
		}
		if frames := a.stepFrames(src, marks, filepath.Join(dir, "steps")); len(frames) > 0 {
			idx.Steps = &demoFrameSet{Title: "the primary video at each step boundary", Video: video, Seconds: length,
				Dir: demoAnalysisDir + "/steps", Frames: frames}
			sum.StepFrames = len(frames)
		}
		sheet := filepath.Join(dir, demoContactSheet)
		if frames, ok := a.sceneFrames(src, filepath.Join(dir, "scenes"), sheet); len(frames) > 0 {
			idx.Scenes = &demoFrameSet{Title: "the primary video's scene changes", Video: video, Seconds: length,
				Dir: demoAnalysisDir + "/scenes", Frames: frames}
			if ok {
				idx.Scenes.ContactSheet = a.rel(sheet)
			}
			sum.SceneFrames = len(frames)
		}
		if failed && length > 0 {
			sub := filepath.Join(dir, "failure", "primary")
			sheet := filepath.Join(sub, demoContactSheet)
			frames, every, ok := a.samples(src, 0, length, 1, maxDemoFailureFrames, filepath.Join(sub, "frames"), sheet)
			if len(frames) > 0 {
				idx.Primary = &demoFrameSet{Title: "the whole primary video, sampled evenly (the demo failed)", Video: video, Seconds: length,
					Dir: a.rel(filepath.Join(sub, "frames")), Every: every, Frames: frames}
				if ok {
					idx.Primary.ContactSheet = a.rel(sheet)
				}
				sum.FailureFrames += len(frames)
			}
		}
	}

	// Each failed run's own recording: the failure is often in a phase whose
	// render never got made, so its raw recording is the only picture of it.
	for n, i := range failedRuns {
		r := &runs[i]
		f := demoFailure{Run: r.Key, Sequence: r.Sequence, Phase: r.Phase, Call: r.Call, Status: r.Status, Error: r.Error, Step: demoFailingStep(*r)}
		if f.Step > 0 {
			f.Label = r.Steps[f.Step-1].Label
		}
		var vid *demoRunVideo
		for k := range r.Videos {
			v := &r.Videos[k]
			if vid == nil || (v.From <= f.Step && f.Step <= v.To) {
				vid = v
			}
		}
		path, ok := "", false
		if vid != nil {
			path, ok = w.demoFilePath(vid.FileID)
		}
		switch {
		case vid == nil:
			f.Note = "it recorded no video"
		case !ok:
			f.Note = fmt.Sprintf("its recording %s isn't in this host's files tab (any more)", vid.Name)
		default:
			sub := filepath.Join(dir, "failure", fmt.Sprintf("%02d-%s", n+1, demoSlug(r.Key)))
			video := "files tab: " + vid.Name
			length := a.seconds(path)
			span := demoSpan{Len: length, Run: r, Video: *vid}
			if frames := a.stepFrames(path, demoStepMarks(span, maxDemoStepFrames), filepath.Join(sub, "steps")); len(frames) > 0 {
				f.Steps = &demoFrameSet{Title: "its recording at each step boundary", Video: video, Seconds: length,
					Dir: a.rel(filepath.Join(sub, "steps")), Frames: frames}
				sum.FailureFrames += len(frames)
			}
			// Close together around the failing step; the whole recording
			// when its time isn't known.
			from, to := 0.0, length
			if f.Step > 0 && vid.StartedAt > 0 {
				st := r.Steps[f.Step-1]
				start := float64(st.StartedAt-vid.StartedAt) / 1000
				from, to = math.Max(0, start-demoFailureLead), start+float64(st.DurationMs)/1000+demoFailureTail
				if length > 0 {
					to = math.Min(to, length)
				}
			}
			if to <= 0 {
				to = float64(vid.DurationMs) / 1000
			}
			sheet := filepath.Join(sub, demoContactSheet)
			frames, every, made := a.samples(path, from, to, 0.5, maxDemoFailureFrames, filepath.Join(sub, "frames"), sheet)
			if len(frames) > 0 {
				f.Samples = &demoFrameSet{Title: "its recording around the failing step", Video: video, Seconds: length,
					Dir: a.rel(filepath.Join(sub, "frames")), Every: every, Frames: frames}
				if made {
					f.Samples.ContactSheet = a.rel(sheet)
				}
				sum.FailureFrames += len(frames)
			}
		}
		idx.Failures = append(idx.Failures, f)
	}
	if failed && len(failedRuns) == 0 {
		a.note("the demo failed, but none of its control runs did -- see artifacts/log.txt for where it stopped")
	}
}

// demoFilePath is where files-tab entry id is on this host.
func (w *toolsWatch) demoFilePath(id string) (string, bool) {
	if w.demoFile == nil || id == "" || id != filepath.Base(id) || strings.HasPrefix(id, ".") {
		return "", false
	}
	return w.demoFile(id)
}

func readDemoAnalysis(recDir string) *demoAnalysisIndex {
	b, err := os.ReadFile(filepath.Join(recDir, demoAnalysisDir, demoAnalysisFile))
	if err != nil {
		return nil
	}
	var idx demoAnalysisIndex
	if json.Unmarshal(b, &idx) != nil || idx.Format != demoAnalysisFormat {
		return nil
	}
	return &idx
}

// ---- MAP.md ----

// writeDemoMap writes rec's MAP.md from it and its analysis.json.
func writeDemoMap(rec DemoRecord) {
	os.WriteFile(filepath.Join(rec.Dir, demoMapFile), []byte(demoMap(rec, readDemoAnalysis(rec.Dir))), 0o644)
}

// demoMap is a record's map: what happened in a few lines, then every file
// worth reading, lightest first, with how big it is, then the frames'
// tables -- so an agent can bring it into context first and choose the
// rest.
func demoMap(rec DemoRecord, idx *demoAnalysisIndex) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Map of demo record %s\n\n", rec.ID)
	b.WriteString("Read this first. It lists what this directory holds, lightest first, so you can choose what to read next. ")
	b.WriteString("Times in the tables are into the video named above each one. Paths are from this directory.\n\n")

	fmt.Fprintf(&b, "Demo **%s** (%s), %s, **%s**, on %s", rec.Demo, rec.Title, rec.Mode, rec.Status, rec.Node)
	if rec.StartedAt > 0 {
		fmt.Fprintf(&b, ", %s", time.UnixMilli(rec.StartedAt).UTC().Format("2006-01-02 15:04 UTC"))
		if rec.FinishedAt > rec.StartedAt {
			fmt.Fprintf(&b, ", took %s", (time.Duration(rec.FinishedAt-rec.StartedAt) * time.Millisecond).Round(100*time.Millisecond))
		}
	}
	b.WriteString(".")
	if rec.Suite != "" {
		fmt.Fprintf(&b, " One demo of suite %s (run %s).", rec.Suite, rec.SuiteRecord)
	}
	b.WriteString("\n")
	if rec.Error != "" {
		fmt.Fprintf(&b, "\nError: %s\n", demoOneLine(rec.Error, 400))
	}
	if len(rec.Phases) > 0 {
		b.WriteString("\nPhases:")
		for i, p := range rec.Phases {
			sep := " ·"
			if i == 0 {
				sep = ""
			}
			fmt.Fprintf(&b, "%s %s (%s) %s", sep, p.Name, p.Role, p.Status)
			if p.DurationMs > 0 {
				fmt.Fprintf(&b, " %s", formatSeconds(p.DurationMs))
			}
			if p.Error != "" {
				fmt.Fprintf(&b, " -- %s", demoOneLine(p.Error, 160))
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("\n## Files, lightest first\n\n")
	size := func(rel string) string {
		return demoSize(fileSize(filepath.Join(rec.Dir, filepath.FromSlash(rel))))
	}
	line := func(rel, what string) {
		if _, err := os.Stat(filepath.Join(rec.Dir, filepath.FromSlash(rel))); err == nil {
			fmt.Fprintf(&b, "- `%s` (%s) -- %s\n", rel, size(rel), what)
		}
	}
	art := func(name string) string { return demoArtifactsDir + "/" + name }
	fmt.Fprintf(&b, "- `%s` -- this map\n", demoMapFile)
	if idx != nil {
		if idx.Scenes != nil && idx.Scenes.ContactSheet != "" {
			line(idx.Scenes.ContactSheet, fmt.Sprintf("**one image** of the primary video's %d scene changes, tiled left to right, top to bottom (times below) -- the cheapest way to see the video", len(idx.Scenes.Frames)))
		}
		for _, f := range idx.Failures {
			if f.Samples != nil && f.Samples.ContactSheet != "" {
				line(f.Samples.ContactSheet, fmt.Sprintf("**one image** of failed run %s around its failing step, %d frames %s apart", f.Run, len(f.Samples.Frames), demoSeconds(f.Samples.Every)))
			}
		}
		if idx.Primary != nil && idx.Primary.ContactSheet != "" {
			line(idx.Primary.ContactSheet, fmt.Sprintf("**one image** of the whole primary video, %d frames %s apart", len(idx.Primary.Frames), demoSeconds(idx.Primary.Every)))
		}
	}
	line(art(demoSummaryFile), "the record in words: phases, videos, every artifact")
	line(art(demoOutputsFile), "what each control run printed, and its error")
	line(art(demoLogFile), "the end of dtt's log")
	line(demoDocFile, "the DTT composition that ran: its phases call dtt's examples, then compose a video")
	line(demoRecordFile, "the record as local-representative keeps it")
	line(art(demoImageFile), "every object's state, phase by phase, calls' images nested -- large: search it rather than read it whole")
	var shots []string
	for _, a := range rec.Artifacts {
		if a.Kind == "screenshot" && a.File != "" {
			shots = append(shots, "`"+art(a.File)+"`")
		}
	}
	if len(shots) > 0 {
		fmt.Fprintf(&b, "- %d screenshot(s) the robot took itself: %s\n", len(shots), strings.Join(shots, ", "))
	}
	if idx != nil {
		set := func(s *demoFrameSet, what string) {
			if s != nil && len(s.Frames) > 0 {
				fmt.Fprintf(&b, "- `%s/` -- %d frames: %s\n", s.Dir, len(s.Frames), what)
			}
		}
		set(idx.Steps, "the primary video at each step boundary (table below)")
		set(idx.Scenes, "the scene changes one by one, named by time")
		set(idx.Primary, "the whole primary video one frame at a time, named by time")
		for _, f := range idx.Failures {
			set(f.Steps, fmt.Sprintf("failed run %s's recording at each step boundary (table below)", f.Run))
			set(f.Samples, fmt.Sprintf("failed run %s's recording around its failing step, named by time", f.Run))
		}
		fmt.Fprintf(&b, "- `%s/%s` -- every frame above with its time, step and result, as JSON\n", demoAnalysisDir, demoAnalysisFile)
	}
	if n := len(rec.Renders); n > 0 {
		kept := 0
		for _, r := range rec.Renders {
			if r.Kept != "" {
				kept++
			}
		}
		fmt.Fprintf(&b, "- `%s/` -- %d of %d videos kept; you can't watch them -- the frames in `%s/` stand in for them\n", demoVideosDir, kept, n, demoAnalysisDir)
	}
	if reviews := readDemoReviews(rec.Dir); len(reviews) > 0 {
		fmt.Fprintf(&b, "- `%s/` -- %d earlier review(s) of this record\n", demoReviewsDir, len(reviews))
	}
	if p := primaryRender(rec.Renders); p != nil {
		where := "not kept"
		if p.Kept != "" {
			where = "`" + demoVideosDir + "/" + p.Kept + "`"
		}
		fmt.Fprintf(&b, "\nThe primary video -- the demo's subject, without lead-in and tear-down -- is %s (%s, phase %s).\n", where, p.Composition, p.Phase)
	}

	if idx == nil {
		b.WriteString("\nThere is no analysis/ for this record: no frames were pulled from its videos.\n")
		return b.String()
	}
	if s := idx.Steps; s != nil {
		fmt.Fprintf(&b, "\n## Primary video at each step boundary\n\n`%s`%s.\n\n", s.Video, demoLength(s.Seconds))
		demoStepTable(&b, s.Frames)
	}
	if s := idx.Scenes; s != nil {
		fmt.Fprintf(&b, "\n## Primary video's scene changes\n\n")
		if s.ContactSheet != "" {
			fmt.Fprintf(&b, "Tiles of `%s`, in order: ", s.ContactSheet)
		} else {
			fmt.Fprintf(&b, "Frames in `%s/`, in order: ", s.Dir)
		}
		for i, f := range s.Frames {
			if i > 0 {
				b.WriteString(" · ")
			}
			fmt.Fprintf(&b, "%d %s", i+1, demoClock(f.At))
		}
		b.WriteString("\n")
	}
	if idx.Primary != nil {
		fmt.Fprintf(&b, "\n## The demo failed: the whole primary video\n\n%d frames, one every %s from 0:00, in `%s/`.\n", len(idx.Primary.Frames), demoSeconds(idx.Primary.Every), idx.Primary.Dir)
	}
	for _, f := range idx.Failures {
		fmt.Fprintf(&b, "\n## Failed run %s\n\nSequence %s in phase %s", f.Run, f.Sequence, f.Phase)
		if f.Call != "" {
			fmt.Fprintf(&b, " (call %s)", f.Call)
		}
		fmt.Fprintf(&b, ", %s", f.Status)
		if f.Step > 0 {
			fmt.Fprintf(&b, " at step %d (%s)", f.Step, f.Label)
		}
		if f.Error != "" {
			fmt.Fprintf(&b, ": %s", demoOneLine(f.Error, 300))
		}
		b.WriteString(".\n")
		if f.Note != "" {
			fmt.Fprintf(&b, "\nNo frames of it: %s.\n", f.Note)
			continue
		}
		if s := f.Samples; s != nil {
			fmt.Fprintf(&b, "\nIts own recording (%s%s), sampled every %s", s.Video, demoLength(s.Seconds), demoSeconds(s.Every))
			if len(s.Frames) > 0 {
				fmt.Fprintf(&b, " from %s to %s", demoClock(s.Frames[0].At), demoClock(s.Frames[len(s.Frames)-1].At))
			}
			fmt.Fprintf(&b, ": `%s/`.\n", s.Dir)
		}
		if s := f.Steps; s != nil {
			fmt.Fprintf(&b, "\nIts recording at each step boundary:\n\n")
			demoStepTable(&b, s.Frames)
		}
	}
	if len(idx.Notes) > 0 {
		b.WriteString("\n## Notes on the analysis\n\n")
		for _, n := range idx.Notes {
			fmt.Fprintf(&b, "- %s\n", demoOneLine(n, 300))
		}
	}
	return b.String()
}

func demoStepTable(b *strings.Builder, frames []demoFrame) {
	b.WriteString("| at | frame | step | status | result |\n|---|---|---|---|---|\n")
	for _, f := range frames {
		fmt.Fprintf(b, "| %s | `%s` | %d %s | %s | %s |\n", demoClock(f.At), f.File, f.Step,
			strings.ReplaceAll(f.Label, "|", "/"), f.Status, strings.ReplaceAll(demoOneLine(f.Message, 120), "|", "/"))
	}
}

// ---- Formatting ----

// demoClock is t seconds as m:ss.s.
func demoClock(t float64) string {
	if t < 0 {
		return "?"
	}
	m := int(t) / 60
	return fmt.Sprintf("%d:%04.1f", m, t-float64(m*60))
}

// demoFileClock is t seconds for a file name: 1m04.2s.
func demoFileClock(t float64) string {
	m := int(t) / 60
	return fmt.Sprintf("%dm%04.1fs", m, t-float64(m*60))
}

func demoSeconds(t float64) string {
	return strconv.FormatFloat(t, 'f', -1, 64) + "s"
}

func demoLength(t float64) string {
	if t <= 0 {
		return ""
	}
	return ", " + demoClock(t) + " long"
}

func demoSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// demoOneLine is s on one line, cut to max bytes.
func demoOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

var demoSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// demoSlug is s made safe and short for a file name.
func demoSlug(s string) string {
	s = strings.Trim(demoSlugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		s = "x"
	}
	return s
}
