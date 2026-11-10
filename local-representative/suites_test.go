package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// robotHelloImageJSON is a robot-hello demo's image: the subject phase's
// call ran a control sequence that took a screenshot (f-shot) and printed
// hello, and rendered f-subject; the demo's own composition is f-demo.
const robotHelloImageJSON = `{"ok": true, "summary": {"changed": 4},
  "phases": [
    {"name": "lead-in", "status": "done"}, {"name": "read", "status": "done"},
    {"name": "tear-down", "status": "done"}, {"name": "compose", "status": "done"}
  ],
  "objects": [
    {"id": "dtt.calls[localrep-demo-robot-hello-read.dtt.yaml]", "path": "dtt.calls", "alias": "rh", "phase": "read", "state": {"image": {"ok": true,
      "objects": [
        {"id": "localrep.control.runs[x]", "path": "localrep.control.runs", "alias": "rh-read-run", "state": {"sequence": "dtt-demo-rh-read", "key": "demo-rh-read-1", "status": "success",
          "output": {"hello": "hello world from n2"},
          "recordings": [{"file_id": "f-rec", "name": "rec.webm", "video": true}, {"file_id": "f-shot", "name": "robot-hello-read.png", "video": false}]}},
        {"id": "localrep.compositions[demo-rh-read-1]", "path": "localrep.compositions", "alias": "rh-read-comp", "state": {"render": {"file_id": "f-subject", "name": "subject.webm"}}}
      ]}}},
    {"id": "localrep.compositions[demo-robot-hello-read-1]", "path": "localrep.compositions", "phase": "compose", "state": {"render": {"file_id": "f-demo", "name": "demo.webm"}}}
  ]}`

// TestBuiltinSuite: the robot-hello suite lists the three robot hello
// demos, asks for node and greeting once each, and says which demos take
// which.
func TestBuiltinSuite(t *testing.T) {
	w, _ := demoWatch(t, func(map[string]string) ToolRunResult { return ToolRunResult{OK: true, Tool: "declare"} })
	var s SuiteInfo
	for _, x := range w.suiteLibrary(w.demoLibrary()) {
		if x.Name == "robot-hello" {
			s = x
		}
	}
	if s.Error != "" || s.OnFailure != "continue" || strings.Join(s.Demos, " ") != "robot-hello-read robot-hello-note robot-hello-zoom" {
		t.Fatalf("robot-hello suite: %+v", s)
	}
	if len(s.Variables) != 2 || s.Variables[0].Name != "node" || len(s.Variables[0].Demos) != 3 ||
		s.Variables[1].Name != "greeting" || strings.Join(s.Variables[1].Demos, " ") != "robot-hello-note robot-hello-zoom" {
		t.Errorf("variables %+v", s.Variables)
	}
	if !strings.HasPrefix(s.Title, "Robot hello world") || s.Description == "" {
		t.Errorf("title %q, description %q", s.Title, s.Description)
	}
	for src, want := range map[string]string{
		"demos:\n  - a\non_failure: maybe\n": "on_failure",
		"demos:\n":                          "no demos",
		"demos:\n  - Not A Name\n":          "isn't a demo name",
		"steps:\n  - a\n":                   "unknown key",
	} {
		if got := parseSuite("x", src, demoOriginConfigured, ""); !strings.Contains(got.Error, want) {
			t.Errorf("%q: error %q, want %q", src, got.Error, want)
		}
	}
}

// TestSuiteRunContinuesPastFailure: a suite runs its demos in turn, each
// with the variables it declares, keeps going after one fails, and keeps a
// record naming each demo's record; the free-text variables are remembered,
// the node isn't.
func TestSuiteRunContinuesPastFailure(t *testing.T) {
	w, inputs := demoWatch(t, func(in map[string]string) ToolRunResult {
		doc, _ := os.ReadFile(in["document_file"])
		ok := !strings.Contains(string(doc), "localrep-demo-robot-hello-note.dtt.yaml")
		return ToolRunResult{OK: ok, Tool: "declare", Outputs: map[string]string{toolImageOutput: robotHelloImageJSON}}
	})
	last := filepath.Join(t.TempDir(), "last.json")
	w.demoLastValues = func() string { return last }

	r := w.handle(ToolsRequest{Op: "suite-run", Suite: "robot-hello", Variables: map[string]string{"node": "n2", "greeting": " hi there "}}, false, "test-lr")
	if !r.OK || r.SuiteRecord == nil || len(r.SuiteRecord.Entries) != 3 {
		t.Fatalf("suite-run: %+v", r)
	}
	if again := w.handle(ToolsRequest{Op: "demo-run", Demo: "robot-cool"}, false, "test-lr"); again.OK {
		t.Error("a demo shouldn't start while a suite runs")
	}
	var rec SuiteRecord
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list := w.suiteRecords(w.demoDir())
		if len(list) == 1 && list[0].Status != "running" {
			rec = list[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec.Status != "failed" {
		t.Fatalf("suite record %+v", rec)
	}
	got := []string{}
	for _, e := range rec.Entries {
		got = append(got, e.Demo+"="+e.Status)
		d, err := w.demoRecord(w.demoDir(), e.Record)
		if err != nil || d.Suite != "robot-hello" || d.SuiteRecord != rec.ID {
			t.Errorf("%s's record %s: %+v, %v", e.Demo, e.Record, d, err)
		}
	}
	if strings.Join(got, " ") != "robot-hello-read=ok robot-hello-note=failed robot-hello-zoom=ok" {
		t.Errorf("entries %s", strings.Join(got, " "))
	}
	if len(*inputs) != 3 || (*inputs)[0]["variables"] != "node=n2" || (*inputs)[1]["variables"] != "greeting=hi there\nnode=n2" {
		t.Errorf("declare's inputs %+v", *inputs)
	}
	values := w.readDemoLastValues()
	if values["greeting"] != "hi there" || values["node"] != "" {
		t.Errorf("last values %+v", values)
	}
	if list := w.handle(ToolsRequest{Op: "demos"}, false, "test-lr"); list.DemoLastValues["greeting"] != "hi there" || len(list.Suites) == 0 || len(list.SuiteRecords) != 1 {
		t.Errorf("demos: last values %+v, %d suites, %d suite records", list.DemoLastValues, len(list.Suites), len(list.SuiteRecords))
	}
	// Given empty, it's forgotten.
	w.rememberDemoValues(map[string]string{"greeting": ""}, []DemoVariable{{Name: "greeting"}})
	if _, ok := w.readDemoLastValues()["greeting"]; ok {
		t.Error("an emptied value should be forgotten")
	}
}

// TestDemoArtifactsAndPrimary: an assert keeps its control runs' outputs
// and screenshots in artifacts/, and its subject phase's render is the
// primary video.
func TestDemoArtifactsAndPrimary(t *testing.T) {
	w, _ := demoWatch(t, func(map[string]string) ToolRunResult {
		return ToolRunResult{OK: true, Tool: "declare", Outputs: map[string]string{toolImageOutput: robotHelloImageJSON}, LogTail: "done"}
	})
	shot, _ := w.demoFile("f-shot")
	os.WriteFile(shot, []byte("png"), 0o644)
	r := w.handle(ToolsRequest{Op: "demo-run", Demo: "robot-hello-read"}, false, "test-lr")
	if !r.OK {
		t.Fatalf("demo-run: %+v", r)
	}
	rec := waitDemoRecord(t, w, r.DemoRecord.ID)
	p := primaryRender(rec.Renders)
	if p == nil || p.FileID != "f-subject" || p.Phase != "read" {
		t.Errorf("primary %+v of %+v", p, rec.Renders)
	}
	var shots []DemoArtifact
	for _, a := range rec.Artifacts {
		if a.Kind == "screenshot" {
			shots = append(shots, a)
		}
	}
	if len(shots) != 1 || shots[0].File != "screenshots/01-read-robot-hello-read.png" || shots[0].Size != 3 {
		t.Fatalf("screenshots %+v", rec.Artifacts)
	}
	art := filepath.Join(rec.Dir, demoArtifactsDir)
	if b, err := os.ReadFile(filepath.Join(art, shots[0].File)); err != nil || string(b) != "png" {
		t.Errorf("the screenshot: %q, %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(art, demoOutputsFile)); !strings.Contains(string(b), "hello: hello world from n2") {
		t.Errorf("outputs.txt: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(art, demoSummaryFile)); !strings.Contains(string(b), "**primary**") || !strings.Contains(string(b), shots[0].File) {
		t.Errorf("summary.md: %s", b)
	}
	// analysis/ notes why it has no frames, and MAP.md maps the record.
	if rec.Analysis == nil || len(rec.Analysis.Notes) == 0 || !strings.Contains(rec.Analysis.Notes[0], "ffmpeg") {
		t.Errorf("analysis %+v", rec.Analysis)
	}
	if b, _ := os.ReadFile(filepath.Join(rec.Dir, demoMapFile)); !strings.Contains(string(b), "Read this first") ||
		!strings.Contains(string(b), "`artifacts/"+demoOutputsFile+"`") || !strings.Contains(string(b), "no ffmpeg") {
		t.Errorf("MAP.md: %s", b)
	}
	// The artifacts are served beside the videos, as are the map and analysis/.
	for path, want := range map[string]int{
		"/artifacts/" + shots[0].File: 200, "/artifacts/" + demoOutputsFile: 200,
		"/artifacts/screenshots": 404, "/artifacts/../record.json": 404, "/reviews/x.md": 404,
		"/" + demoMapFile: 200, "/analysis/" + demoAnalysisFile: 200, "/analysis/../record.json": 404,
		"/analysis/steps/../../record.json": 404, "/record.json": 404,
	} {
		srv := httptest.NewRecorder()
		w.handleDemoRecordFile(srv, httptest.NewRequest("GET", "/api/demo-records/"+rec.ID+path, nil))
		if srv.Code != want {
			t.Errorf("%s: %d, want %d", path, srv.Code, want)
		}
	}
}

// TestDemoReview: a review goes to the worker read-only in the record's
// directory, is kept in reviews/, and takes the worker's status and reply
// once its signal shows up.
func TestDemoReview(t *testing.T) {
	w, _ := demoWatch(t, func(map[string]string) ToolRunResult {
		return ToolRunResult{OK: true, Tool: "declare", Outputs: map[string]string{toolImageOutput: robotHelloImageJSON}}
	})
	if r := w.handle(ToolsRequest{Op: "demo-review", Record: "nothing-1"}, false, ""); r.OK {
		t.Error("reviewing a record that isn't there should fail")
	}
	run := w.handle(ToolsRequest{Op: "demo-run", Demo: "robot-hello-read"}, false, "test-lr")
	rec := waitDemoRecord(t, w, run.DemoRecord.ID)

	if r := w.handle(ToolsRequest{Op: "demo-review", Record: rec.ID}, false, ""); r.OK {
		t.Error("without a worker, a review should be refused")
	}
	var gotPrompt, gotDir string
	w.reviewSubmit = func(prompt, dir, agent, model string) error {
		gotPrompt, gotDir = prompt, dir
		return nil
	}
	var signals []WorkerSignalInfo
	w.workerSignals = func() []WorkerSignalInfo { return signals }

	r := w.handle(ToolsRequest{Op: "demo-review", Record: rec.ID, Question: "did it zoom?"}, false, "")
	if !r.OK || len(r.DemoReviews) != 1 || r.DemoReviews[0].Status != "submitted" {
		t.Fatalf("demo-review: %+v", r)
	}
	if gotDir != rec.Dir || !strings.Contains(gotPrompt, "read-only") || !strings.Contains(gotPrompt, "did it zoom?") ||
		!strings.Contains(gotPrompt, "artifacts/screenshots/") || !strings.Contains(gotPrompt, "primary video") || !strings.Contains(gotPrompt, "MAP.md") {
		t.Errorf("submitted %q in %q", gotPrompt, gotDir)
	}

	// The worker lists it, then finishes it.
	sig := WorkerSignalInfo{ID: "sig-1", File: "COMPLETE-demo-review-1.jsonl", Role: demoReviewRole, WorkLocation: rec.Dir, Status: "processing", CreatedAt: time.Now().UnixMilli()}
	signals = []WorkerSignalInfo{sig}
	if r := w.handle(ToolsRequest{Op: "demo-reviews", Record: rec.ID}, false, ""); r.DemoReviews[0].SignalID != "sig-1" || r.DemoReviews[0].Status != "processing" {
		t.Errorf("listed: %+v", r.DemoReviews[0])
	}
	sig.Status, sig.Output = "completed", "it read the page fine"
	w.workerStateChanged(WorkerStateMsg{Signals: []WorkerSignalInfo{sig}})
	reviews := readDemoReviews(rec.Dir)
	if reviews[0].Status != "completed" || reviews[0].Output != "it read the page fine" {
		t.Fatalf("after the worker finished: %+v", reviews[0])
	}
	if b, err := os.ReadFile(filepath.Join(rec.Dir, demoReviewsDir, reviews[0].ID+".md")); err != nil || !strings.Contains(string(b), "it read the page fine") {
		t.Errorf("the review's reply: %q, %v", b, err)
	}
	// A signal pointing outside the demo records is left alone.
	w.workerStateChanged(WorkerStateMsg{Signals: []WorkerSignalInfo{{ID: "x", Role: demoReviewRole, WorkLocation: t.TempDir(), Status: "completed"}}})
}
