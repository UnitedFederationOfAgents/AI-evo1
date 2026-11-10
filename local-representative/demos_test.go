package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// declareListJSON is "dtt list --json" with the built-in declare tool.
const declareListJSON = `{
  "ok": true, "version": "test",
  "tools": [{
    "op": "tool.declare", "summary": "Read or assert a DTT document.", "describe": "declare {mode}",
    "args": [{"name": "document_file", "help": "", "type": "path"}, {"name": "mode", "help": "", "type": "text"}],
    "name": "declare", "description": "", "values": [{"name": "image_json"}], "files": [{"name": "image"}],
    "runtime": "exec", "script": "run.sh", "timeout": "60m", "origin": "builtin", "path": "builtin/declare"
  }],
  "dirs": [], "problems": [], "shadowed": []
}`

// demoImageJSON is an assert's image: three phases done, a fourth failed,
// the demo's own composition rendered, and the lead-in's call's too.
const demoImageJSON = `{"ok": false, "format": "dtt-image-v1", "mode": "assert", "taken": "now",
  "summary": {"changed": 3, "held": 0},
  "phases": [
    {"name": "lead-in", "status": "done", "duration_ms": 1000},
    {"name": "robot", "status": "done", "duration_ms": 2000},
    {"name": "tear-down", "status": "failed", "error": "the page was gone"},
    {"name": "compose", "status": "not-run"}
  ],
  "objects": [
    {"id": "dtt.calls[localrep-demo-prepare.dtt.yaml]", "path": "dtt.calls", "alias": "prep", "phase": "lead-in", "state": {"image": {"ok": true,
      "objects": [{"id": "localrep.compositions[prep-1]", "path": "localrep.compositions", "alias": "prep-comp", "state": {"render": {"file_id": "f-2", "name": "prep.webm"}}}]}}},
    {"id": "localrep.compositions[demo-robot-cool-1]", "path": "localrep.compositions", "phase": "compose", "state": {"render": {"file_id": "f-1", "name": "demo.webm"}}}
  ],
  "error": "phase tear-down failed"}`

func demoWatch(t *testing.T, run func(in map[string]string) ToolRunResult) (*toolsWatch, *[]map[string]string) {
	t.Helper()
	var inputs []map[string]string
	w := newToolsWatch(nil, nil, func() (string, error) { return "/fake/dtt", nil })
	w.runDir = t.TempDir()
	root := t.TempDir()
	w.demoRoot = func() string { return root }
	files := t.TempDir() // the files tab: f-1 is there, f-2 isn't yet
	os.WriteFile(filepath.Join(files, "f-1"), []byte("the demo's video"), 0o644)
	w.demoFile = func(id string) (string, bool) {
		p := filepath.Join(files, id)
		_, err := os.Stat(p)
		return p, err == nil
	}
	w.dtt = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		switch args[0] {
		case "list":
			return []byte(declareListJSON), nil
		case "run":
			var in map[string]string
			if err := json.Unmarshal(stdin, &in); err != nil {
				return nil, err
			}
			inputs = append(inputs, in)
			b, _ := json.Marshal(run(in))
			return b, nil
		}
		return nil, errors.New("unexpected call")
	}
	t.Cleanup(func() { setToolControlOps(nil, false) })
	// The videos here aren't videos: analysis/ just notes there's no ffmpeg
	// (demoanalysis_test.go runs the real one).
	was := demoFFmpeg
	demoFFmpeg = func() (string, error) { return "", errors.New("no ffmpeg in this test") }
	t.Cleanup(func() { demoFFmpeg = was })
	w.check()
	return w, &inputs
}

func waitDemoRecord(t *testing.T, w *toolsWatch, id string) DemoRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec, err := w.demoRecord(w.demoDir(), id)
		if err == nil && rec.Status != "running" {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("demo record %s didn't finish", id)
	return DemoRecord{}
}

// TestBuiltinDemos: every built-in demo parses, with the lead-in /
// tear-down shape and the examples it calls.
func TestBuiltinDemos(t *testing.T) {
	demos := builtinDemos()
	byName := map[string]DemoInfo{}
	for _, d := range demos {
		if d.Error != "" {
			t.Errorf("%s: %s", d.Name, d.Error)
		}
		if d.Origin != demoOriginEmbedded {
			t.Errorf("%s's origin %q, want %s", d.Name, d.Origin, demoOriginEmbedded)
		}
		byName[d.Name] = d
	}
	for _, name := range []string{"robot-cool", "session-hello", "condoc-hello", "condoc-local", "robot-hello-read", "robot-hello-note", "robot-hello-zoom", "federation-command-ridealong1"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("no built-in demo %s (have %d)", name, len(demos))
		}
	}
	var got []string
	for _, p := range byName["session-hello"].Phases {
		got = append(got, p.Name+"="+p.Role+":"+strings.Join(p.Calls, ","))
	}
	want := "lead-in=lead-in:localrep-demo-prepare.dtt.yaml " +
		"session-lead-in=lead-in:localrep-demo-session-prepare.dtt.yaml " +
		"hello=subject:localrep-demo-session-hello.dtt.yaml " +
		"session-tear-down=tear-down:localrep-demo-session-shutdown.dtt.yaml " +
		"tear-down=tear-down:localrep-demo-shutdown.dtt.yaml " +
		"compose=compose:"
	if strings.Join(got, " ") != want {
		t.Errorf("session-hello's phases:\n%s\nwant\n%s", strings.Join(got, " "), want)
	}
	d := byName["condoc-hello"]
	if len(d.Variables) != 3 || d.Variables[0].Name != "node" || d.Variables[2].Name != "pointer_dir" || d.Variables[1].Help == "" {
		t.Errorf("condoc-hello's variables: %+v", d.Variables)
	}
	if !strings.HasPrefix(d.Title, "condocv2 hello world") || strings.Contains(d.Description, "dtt assert") || !strings.Contains(d.Description, "lead-in") {
		t.Errorf("condoc-hello's title %q / description %q", d.Title, d.Description)
	}
	if d.SUT != "local" {
		t.Errorf("condoc-hello's sut %q, want local", d.SUT)
	}

	// condoc-local runs on the node running it (SSUT): its only input is
	// agent-coordinator's URL.
	d = byName["condoc-local"]
	if d.SUT != "same" || len(d.Variables) != 1 || d.Variables[0].Name != "ac_url" || strings.Contains(d.Description, "sut:") {
		t.Errorf("condoc-local's sut %q / variables %+v", d.SUT, d.Variables)
	}

	// condoc-local's repos come and go in phases of their own, with no call.
	got = nil
	for _, p := range byName["condoc-local"].Phases {
		got = append(got, p.Name+"="+p.Role+":"+strings.Join(p.Calls, ","))
	}
	want = "lead-in=lead-in:localrep-demo-prepare.dtt.yaml " +
		"repos-lead-in=lead-in: " +
		"condoc-lead-in=lead-in:localrep-demo-condoc-prepare.dtt.yaml " +
		"lifecycle=subject:localrep-demo-condoc-local.dtt.yaml " +
		"condoc-tear-down=tear-down:localrep-demo-condoc-shutdown.dtt.yaml " +
		"repos-tear-down=tear-down: " +
		"tear-down=tear-down:localrep-demo-shutdown.dtt.yaml " +
		"compose=compose:"
	if strings.Join(got, " ") != want {
		t.Errorf("condoc-local's phases:\n%s\nwant\n%s", strings.Join(got, " "), want)
	}

	// FC101 (federation-command-ridealong1), embedded from the host library
	// it was proposed into, calls the ridealong example as its subject.
	got = nil
	for _, p := range byName["federation-command-ridealong1"].Phases {
		got = append(got, p.Name+"="+p.Role+":"+strings.Join(p.Calls, ","))
	}
	want = "lead-in=lead-in:localrep-demo-prepare.dtt.yaml " +
		"ridealong=subject:localrep-demo-fc-ridealong.dtt.yaml " +
		"tear-down=tear-down:localrep-demo-shutdown.dtt.yaml " +
		"compose=compose:"
	if strings.Join(got, " ") != want {
		t.Errorf("federation-command-ridealong1's phases:\n%s\nwant\n%s", strings.Join(got, " "), want)
	}
	if d := byName["federation-command-ridealong1"]; d.SUT != "local" || len(d.Variables) != 1 || d.Variables[0].Name != "node" {
		t.Errorf("federation-command-ridealong1's sut %q / variables %+v", d.SUT, d.Variables)
	}
}

// TestDemoRunKeepsRecord: a demo runs as one declare call on the document
// written into its record, and the record keeps how it went, the image and
// the render; deleting undoes it with that image.
func TestDemoRunKeepsRecord(t *testing.T) {
	w, inputs := demoWatch(t, func(in map[string]string) ToolRunResult {
		return ToolRunResult{OK: in["mode"] == "delete", Tool: "declare", Outputs: map[string]string{toolImageOutput: demoImageJSON}, Files: []ToolRunFile{}, LogTail: "calling localrep-demo-prepare.dtt.yaml (assert)", Error: "phase tear-down failed"}
	})
	r := w.handle(ToolsRequest{Op: "demo-run", Demo: "robot-cool", Variables: map[string]string{"node": "n2", "unused": " "}}, false, "test-lr")
	if !r.OK || r.DemoRecord == nil || r.RunID == "" {
		t.Fatalf("demo-run: %+v", r)
	}
	again := w.handle(ToolsRequest{Op: "demo-run", Demo: "robot-cool"}, false, "test-lr")
	rec := waitDemoRecord(t, w, r.DemoRecord.ID)
	if again.OK {
		// The first may have finished already; then the second is a run too.
		waitDemoRecord(t, w, again.DemoRecord.ID)
	}
	in := (*inputs)[0]
	if in["mode"] != "assert" || in["variables"] != "node=n2" || in["document_file"] != filepath.Join(rec.Dir, demoDocFile) {
		t.Errorf("declare's inputs: %+v", in)
	}
	if doc, err := os.ReadFile(in["document_file"]); err != nil || !strings.Contains(string(doc), "localrep-demo-cool.dtt.yaml") {
		t.Errorf("the record's document: %v", err)
	}
	if rec.Status != "failed" || rec.Error != "phase tear-down failed" || rec.Node != "test-lr" || rec.Origin != "demo tab" || !rec.HasImage {
		t.Errorf("record %+v", rec)
	}
	if len(rec.Phases) != 4 || rec.Phases[2].Role != "tear-down" || rec.Phases[2].Status != "failed" || rec.Phases[3].Status != "not-run" {
		t.Errorf("phases %+v", rec.Phases)
	}
	if len(rec.Renders) != 2 || rec.Renders[0].FileID != "f-1" || rec.Renders[0].Phase != "compose" ||
		rec.Renders[1].FileID != "f-2" || rec.Renders[1].Phase != "lead-in" || rec.Renders[1].Call != "prep" {
		t.Fatalf("renders %+v", rec.Renders)
	}
	// The demo's own video is kept with the record; the call's wasn't in the
	// files tab, so it says why.
	if v := rec.Renders[0]; v.Kept != "01-compose-demo.webm" || v.Size != int64(len("the demo's video")) {
		t.Errorf("kept %+v", v)
	} else if b, err := os.ReadFile(filepath.Join(rec.Dir, demoVideosDir, v.Kept)); err != nil || string(b) != "the demo's video" {
		t.Errorf("the kept video: %q, %v", b, err)
	}
	if v := rec.Renders[1]; v.Kept != "" || !strings.Contains(v.KeepError, "f-2") {
		t.Errorf("unkept %+v", v)
	}
	// No subject phase rendered, so the demo's own video is the primary one.
	if !rec.Renders[0].Primary || rec.Renders[1].Primary {
		t.Errorf("primary %+v", rec.Renders)
	}
	// The image, the log and the summary are in artifacts/.
	for _, f := range []string{demoImageFile, demoLogFile, demoSummaryFile} {
		if _, err := os.Stat(filepath.Join(rec.Dir, demoArtifactsDir, f)); err != nil {
			t.Errorf("artifacts/%s: %v", f, err)
		}
	}
	// Once it is, demo-keep-videos fetches it.
	f2, _ := w.demoFile("f-2")
	os.WriteFile(f2, []byte("prep"), 0o644)
	kept := w.handle(ToolsRequest{Op: "demo-keep-videos", Record: rec.ID}, false, "test-lr")
	if !kept.OK || kept.DemoRecord.Renders[1].Kept != "02-lead-in-prep.webm" || kept.DemoRecord.Renders[1].KeepError != "" {
		t.Fatalf("demo-keep-videos: %+v", kept)
	}
	// And they're served from the record.
	srv := httptest.NewRecorder()
	w.handleDemoRecordFile(srv, httptest.NewRequest("GET", "/api/demo-records/"+rec.ID+"/videos/02-lead-in-prep.webm", nil))
	if srv.Code != 200 || srv.Body.String() != "prep" {
		t.Errorf("serving the video: %d %q", srv.Code, srv.Body.String())
	}
	for _, p := range []string{rec.ID + "/record.json", rec.ID + "/videos/../record.json", "../x/videos/a"} {
		srv := httptest.NewRecorder()
		w.handleDemoRecordFile(srv, httptest.NewRequest("GET", "/api/demo-records/"+p, nil))
		if srv.Code != 404 {
			t.Errorf("%s: %d, want 404", p, srv.Code)
		}
	}

	list := w.handle(ToolsRequest{Op: "demos"}, false, "test-lr")
	if !list.OK || len(list.Demos) < 3 || len(list.DemoRecords) == 0 || list.DemoDir != w.demoDir() {
		t.Fatalf("demos: %+v", list)
	}
	detail := w.handle(ToolsRequest{Op: "demo-record", Record: rec.ID}, false, "test-lr")
	if !detail.OK || !strings.Contains(detail.DemoImage, "the page was gone") || detail.DemoRecord.LogTail == "" {
		t.Errorf("demo-record: %+v", detail)
	}

	del := w.handle(ToolsRequest{Op: "demo-run", Mode: "delete", Record: rec.ID}, false, "test-lr")
	if !del.OK {
		t.Fatalf("delete: %+v", del)
	}
	undone := waitDemoRecord(t, w, del.DemoRecord.ID)
	last := (*inputs)[len(*inputs)-1]
	if undone.UndoOf != rec.ID || undone.Mode != "delete" || last["mode"] != "delete" || last["from_image"] != filepath.Join(rec.Dir, demoArtifactsDir, demoImageFile) || last["variables"] != "node=n2" {
		t.Errorf("delete record %+v, inputs %+v", undone, last)
	}

	// A record whose run LR no longer holds (it restarted) was interrupted.
	stale := rec
	stale.ID, stale.Status, stale.RunID, stale.Dir = "robot-cool-stale", "running", "gone-1", filepath.Join(w.demoDir(), demoRecordsDir, "robot-cool-stale")
	os.MkdirAll(stale.Dir, 0o755)
	writeDemoRecord(stale)
	if got, _ := w.demoRecord(w.demoDir(), stale.ID); got.Status != "interrupted" {
		t.Errorf("stale record is %s", got.Status)
	}
}

// TestDemoDuplicate: a duplicate lands in the host's library under its new
// name and runs from there; names are kept unique.
func TestDemoDuplicate(t *testing.T) {
	w, _ := demoWatch(t, func(map[string]string) ToolRunResult { return ToolRunResult{OK: true, Tool: "declare"} })
	r := w.handle(ToolsRequest{Op: "demo-duplicate", Demo: "session-hello", Name: "worker-hello"}, false, "")
	if !r.OK || len(r.Demos) != 1 || r.Demos[0].Origin != demoOriginConfigured {
		t.Fatalf("duplicate: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(w.demoDir(), demoLibraryDir, "worker-hello"+demoSuffix)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worker-hello", "robot-cool", "Bad Name"} {
		if r := w.handle(ToolsRequest{Op: "demo-duplicate", Demo: "session-hello", Name: name}, false, ""); r.OK {
			t.Errorf("duplicating as %q should be refused", name)
		}
	}
	if _, err := w.findDemo("worker-hello"); err != nil {
		t.Error(err)
	}
}
