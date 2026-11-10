package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func proposalWatch(t *testing.T) *toolsWatch {
	t.Helper()
	w, _ := demoWatch(t, func(map[string]string) ToolRunResult { return ToolRunResult{OK: true, Tool: "declare"} })
	return w
}

// TestProposalAgentCreate: agent create keeps a new proposal and hands the
// worker its creation job, in the proposal's directory; once the job is
// done and the agent has written the demo there, the proposal is created
// -- its demo copied into the library, its icon, files and coverage read
// -- and deleting it leaves that demo be. Without a worker, anything to go
// on, or a worker that takes it, nothing is kept.
func TestProposalAgentCreate(t *testing.T) {
	w := proposalWatch(t)
	create := func(p DemoProposal) ToolsReply {
		return w.handle(ToolsRequest{Op: "demo-proposal-agent-create", Proposal: &p}, false, "")
	}
	if r := create(DemoProposal{Name: "robot-tour", Description: "the robot reads a page"}); r.OK {
		t.Error("without a worker, agent create should be refused")
	}
	var gotPrompt, gotDir string
	submitErr := errors.New("no worker connected")
	w.proposalSubmit = func(prompt, dir, agent, model string) error {
		gotPrompt, gotDir = prompt, dir
		return submitErr
	}
	var signals []WorkerSignalInfo
	w.workerSignals = func() []WorkerSignalInfo { return signals }
	// Nothing to go on, a bad name, a demo's name, and the worker won't take
	// it.
	for _, bad := range []DemoProposal{
		{Name: "robot-tour"},
		{Name: "Bad Name", Title: "x"},
		{Name: "robot-hello-read", Title: "taken by a demo"},
		{Name: "robot-tour", Description: "refused by the worker"},
	} {
		if r := create(bad); r.OK {
			t.Errorf("agent create of %+v should be refused", bad)
		}
	}
	if l := w.handle(ToolsRequest{Op: "demos"}, false, ""); len(l.DemoProposals) != 0 {
		t.Fatalf("a refused agent create kept %+v", l.DemoProposals)
	}

	submitErr = nil
	r := create(DemoProposal{Name: "robot-tour", Title: "A tour by the robot", Description: "the robot opens a page, reads it back and zooms in"})
	if !r.OK || r.DemoProposal == nil || r.DemoProposal.Status != "creating" || r.DemoProposal.Job == "" {
		t.Fatalf("agent create: %+v", r)
	}
	dir := r.DemoProposal.Dir
	// The prompt leads with the one file to write, by its absolute path, and
	// says the agent writes the demo's script rather than giving the demo.
	target := filepath.Join(dir, "robot-tour.demo.dtt.yaml")
	if gotDir != dir || !strings.HasPrefix(gotPrompt, "Your job is to write ONE file: "+target+"\n") || !strings.Contains(gotPrompt, "the robot opens a page") ||
		!strings.Contains(gotPrompt, "You are writing the script, not giving the demo") || !strings.Contains(gotPrompt, "Check "+target+" exists") ||
		!strings.Contains(gotPrompt, "only place you may create or change files") || !strings.Contains(gotPrompt, "icon.svg") ||
		!strings.Contains(gotPrompt, "# Demo -- A tour by the robot") || !strings.Contains(gotPrompt, "- robot-hello-read: ") {
		t.Errorf("submitted %q in %q", gotPrompt, gotDir)
	}
	// It shows a phase's render as the built-in demos reference it, through
	// the called document's alias, and refuses stand-in subjects (PrivateRepoOps
	// Step 5, Revision P: an agent guessed ${{ridealong.render}} and called an
	// unrelated example).
	for _, want := range []string{
		`"file_id": "${{prep.by_alias.prep-comp.render.file_id}}"`, `"kind": "media"`, `"width": 1280`,
		"${{<phase alias>.render}} does not exist", "Never call an unrelated example as a stand-in",
	} {
		if !strings.Contains(gotPrompt, want) {
			t.Errorf("the prompt doesn't say %q", want)
		}
	}
	if !slices.Equal(r.DemoProposal.Files, []string{proposalDocFile}) {
		t.Errorf("files before the agent wrote any: %q", r.DemoProposal.Files)
	}
	if l := w.handle(ToolsRequest{Op: "demos"}, false, ""); len(l.DemoProposals) != 1 || l.DemoProposals[0].Status != "creating" {
		t.Errorf("listed while creating: %+v", l.DemoProposals)
	}
	if r := create(DemoProposal{Name: "robot-tour", Title: "again"}); r.OK {
		t.Error("a taken proposal name should be refused")
	}

	// The agent writes the demo, an example it needs and an icon.
	src, err := builtinDemosFS.ReadFile("demos/robot-hello-read.demo.dtt.yaml")
	if err != nil {
		t.Fatal(err)
	}
	demo := strings.ReplaceAll(strings.Replace(string(src), "Robot hello world, read", "A tour by the robot", 1), "robot-hello-read.demo.dtt.yaml", "robot-tour.demo.dtt.yaml")
	for name, body := range map[string]string{
		"robot-tour.demo.dtt.yaml":     demo,
		"examples/robot-tour.dtt.yaml": "# new\n",
		"icon.svg":                     `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`,
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Still creating until the job is done.
	if r := w.handle(ToolsRequest{Op: "demo-proposal", Name: "robot-tour"}, false, ""); !r.OK || r.DemoProposal.Status != "creating" || r.DemoProposal.Demo != "" {
		t.Errorf("before the job is done: %+v", r.DemoProposal)
	}
	sig := WorkerSignalInfo{ID: "sig-1", File: "COMPLETE-demo-proposal-1.jsonl", Role: demoProposalRole, WorkLocation: dir, Status: "processing", CreatedAt: time.Now().UnixMilli()}
	signals = []WorkerSignalInfo{sig}
	w.workerStateChanged(WorkerStateMsg{Signals: signals})
	sig.Status, sig.Output = "completed", "wrote robot-tour.demo.dtt.yaml"
	signals = []WorkerSignalInfo{sig}
	w.workerStateChanged(WorkerStateMsg{Signals: signals})

	d, err := w.findDemo("robot-tour")
	if err != nil {
		t.Fatalf("the demo wasn't put into the library: %v", err)
	}
	if d.Origin != demoOriginConfigured || !strings.HasPrefix(d.Title, "A tour by the robot") || len(d.Phases) == 0 {
		t.Errorf("the library's demo: %+v", d)
	}
	r = w.handle(ToolsRequest{Op: "demo-proposal", Name: "robot-tour"}, false, "")
	if !r.OK {
		t.Fatalf("after the job: %+v", r)
	}
	p := r.DemoProposal
	if p.Status != "created" || p.Error != "" || p.Demo != "robot-tour" || p.DemoFile != "robot-tour.demo.dtt.yaml" ||
		!strings.HasPrefix(p.Icon, "data:image/svg+xml;base64,") || p.Description != "the robot opens a page, reads it back and zooms in" {
		t.Errorf("created: %+v", p)
	}
	if want := []string{proposalDocFile, "examples/robot-tour.dtt.yaml", "icon.svg", "robot-tour.demo.dtt.yaml"}; !slices.Equal(p.Files, want) {
		t.Errorf("files %q, want %q", p.Files, want)
	}
	if p.Coverage == nil || !slices.Contains(p.Coverage.Focal, "ianar/vision.go") {
		t.Errorf("coverage: %+v", p.Coverage)
	}

	if r := w.handle(ToolsRequest{Op: "demo-proposal-delete", Name: "robot-tour"}, false, ""); !r.OK {
		t.Fatalf("delete: %+v", r)
	}
	if r := w.handle(ToolsRequest{Op: "demo-proposal", Name: "robot-tour"}, false, ""); r.OK {
		t.Error("a deleted proposal should be gone")
	}
	if _, err := w.findDemo("robot-tour"); err != nil {
		t.Errorf("deleting the proposal shouldn't take its demo: %v", err)
	}
}

// TestProposalFailed: a job done without a demo document left at the top
// level fails the proposal, and nothing goes into the library; one kept
// before proposals were agent-built reads its brief as its description.
func TestProposalFailed(t *testing.T) {
	w := proposalWatch(t)
	w.proposalSubmit = func(prompt, dir, agent, model string) error { return nil }
	var signals []WorkerSignalInfo
	w.workerSignals = func() []WorkerSignalInfo { return signals }
	r := w.handle(ToolsRequest{Op: "demo-proposal-agent-create", Proposal: &DemoProposal{Name: "zoom-tour", Title: "Zoom"}}, false, "")
	if !r.OK {
		t.Fatalf("agent create: %+v", r)
	}
	dir := r.DemoProposal.Dir
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte("not a demo"), 0o644)
	signals = []WorkerSignalInfo{{ID: "sig-1", File: "COMPLETE-demo-proposal-1.jsonl", Role: demoProposalRole, WorkLocation: dir, Status: "completed", CreatedAt: time.Now().UnixMilli(),
		Output: "invoking claude...\nI can't create the repository from here,\nso I wrote notes.md instead."}}
	l := w.handle(ToolsRequest{Op: "demos"}, false, "")
	if len(l.DemoProposals) != 1 || l.DemoProposals[0].Status != "failed" || !strings.Contains(l.DemoProposals[0].Error, "zoom-tour.demo.dtt.yaml") ||
		!strings.Contains(l.DemoProposals[0].Error, "its reply ended: invoking claude... I can't create the repository from here, so I wrote notes.md instead.") {
		t.Errorf("failed: %+v", l.DemoProposals)
	}
	if _, err := w.findDemo("zoom-tour"); err == nil {
		t.Error("a failed proposal shouldn't put a demo in the library")
	}

	old := filepath.Join(proposalsRoot(w.demoDir()), "old-one")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, proposalFile), []byte(`{"name":"old-one","title":"Old","brief":"from before","sections":{"arc":"x"},"created_at":1}`), 0o644)
	r = w.handle(ToolsRequest{Op: "demo-proposal", Name: "old-one"}, false, "")
	if !r.OK || r.DemoProposal.Description != "from before" || r.DemoProposal.Status != "failed" {
		t.Errorf("an older proposal: %+v", r)
	}
}
