package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dungeon-keeper/pkg/types"
)

func testWorker(t *testing.T) *Worker {
	t.Helper()
	dir := t.TempDir()
	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = dir + "/slopspaces"
	cfg.WorkSignalsDir = dir + "/work"
	cfg.AgentSlopspaceRoot = dir + "/agent"
	cfg.AgentRecordsPath = dir + "/records"
	cfg.WorkerID = "test"
	return NewWorker(cfg)
}

func TestSubmitCreatesPromptOnlyInPlaceSignal(t *testing.T) {
	w := testWorker(t)

	path, err := w.submit(SubmitRequest{Prompt: "  say hello  "})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	signal, _, err := w.workSignalMgr.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if signal.WorkType != types.WorkTypeInPlace || signal.WorkLocation != os.TempDir() {
		t.Errorf("want in-place work in %s, got %s in %q", os.TempDir(), signal.WorkType, signal.WorkLocation)
	}
	if signal.Mode != "prompt" || signal.Prompt != "say hello" || signal.Status != types.WorkStatusPending {
		t.Errorf("unexpected signal: mode=%q prompt=%q status=%q", signal.Mode, signal.Prompt, signal.Status)
	}
	if signal.AgentType != w.config.AgentType {
		t.Errorf("want agent type %s, got %s", w.config.AgentType, signal.AgentType)
	}
	select {
	case <-w.wake:
	default:
		t.Error("submit should wake the watch loop")
	}

	if _, err := w.submit(SubmitRequest{Prompt: "   "}); err == nil {
		t.Error("an empty prompt should be refused")
	}
}

func TestSubmitSessionPromptUsesTempDirAndSession(t *testing.T) {
	w := testWorker(t)

	path, err := w.submit(SubmitRequest{Prompt: "say hello", Session: "my-session", TempDir: true})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	signal, _, err := w.workSignalMgr.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if signal.Session != "my-session" || signal.Role != sessionRole || !signal.RemoveWorkLocation {
		t.Errorf("unexpected signal: session=%q role=%q remove=%v", signal.Session, signal.Role, signal.RemoveWorkLocation)
	}
	dir := signal.WorkLocation
	if filepath.Dir(dir) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(dir), tempWorkPrefix) {
		t.Fatalf("want a fresh temp dir, got %q", dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("temp dir %s should exist until the work runs: %v", dir, err)
	}
	if st := w.state(false); len(st.Signals) != 1 || st.Signals[0].Session != "my-session" {
		t.Errorf("worker-state should carry the session: %+v", st.Signals)
	}

	w.removeTempWorkLocation(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp dir %s should be gone, got %v", dir, err)
	}

	if _, err := w.submit(SubmitRequest{Prompt: "hi", Session: "../escape"}); err == nil {
		t.Error("a session with a path separator should be refused")
	}
}

// TestSubmitDemoReviewRunsReadOnlyInPlace: a demo record's review runs in
// read mode in the record's own directory, which is never removed; nothing
// else may pick a work location or mode.
func TestSubmitDemoReviewRunsReadOnlyInPlace(t *testing.T) {
	w := testWorker(t)
	record := t.TempDir()

	path, err := w.submit(SubmitRequest{Prompt: "review it", Role: demoReviewRole, WorkLocation: record, Mode: "read"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	signal, _, err := w.workSignalMgr.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if signal.Role != demoReviewRole || signal.WorkLocation != record || signal.Mode != "read" ||
		signal.WorkType != types.WorkTypeInPlace || signal.RemoveWorkLocation {
		t.Errorf("unexpected signal: %+v", signal)
	}

	// A demo proposal's creation runs the same way in the proposal's
	// directory, and may write there.
	proposal := filepath.Join(t.TempDir(), "proposals", "robot-tour")
	if err := os.MkdirAll(proposal, 0o755); err != nil {
		t.Fatal(err)
	}
	path, err = w.submit(SubmitRequest{Prompt: "build it", Role: demoProposalRole, WorkLocation: proposal, Mode: "write"})
	if err != nil {
		t.Fatalf("submit a creation: %v", err)
	}
	if signal, _, err = w.workSignalMgr.Read(path); err != nil {
		t.Fatalf("read: %v", err)
	}
	if signal.Role != demoProposalRole || signal.WorkLocation != proposal || signal.Mode != "write" || signal.RemoveWorkLocation {
		t.Errorf("unexpected creation signal: %+v", signal)
	}

	for name, req := range map[string]SubmitRequest{
		"a write outside a proposal":  {Prompt: "x", Role: demoProposalRole, WorkLocation: record, Mode: "write"},
		"a location without the role": {Prompt: "x", WorkLocation: record},
		"a mode without the role":     {Prompt: "x", Mode: "write"},
		"a writing review":            {Prompt: "x", Role: demoReviewRole, WorkLocation: record, Mode: "write"},
		"a relative location":         {Prompt: "x", Role: demoReviewRole, WorkLocation: "records/x"},
		"a missing location":          {Prompt: "x", Role: demoReviewRole, WorkLocation: filepath.Join(record, "gone")},
		"an unknown role":             {Prompt: "x", Role: "anything"},
	} {
		if _, err := w.submit(req); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
}

func TestRemoveTempWorkLocationOnlyRemovesItsOwnDirs(t *testing.T) {
	w := testWorker(t)
	other, err := os.MkdirTemp("", "not-ours-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(other)
	w.removeTempWorkLocation(other)
	w.removeTempWorkLocation(os.TempDir())
	if _, err := os.Stat(other); err != nil {
		t.Errorf("%s isn't a dir submit made and should be left alone: %v", other, err)
	}
	if _, err := os.Stat(os.TempDir()); err != nil {
		t.Errorf("the temp dir itself should be left alone: %v", err)
	}
}

func TestSubmitTwiceInOneSecondKeepsBoth(t *testing.T) {
	w := testWorker(t)
	a, err := w.submit(SubmitRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.submit(SubmitRequest{Prompt: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("both submits wrote %s", a)
	}
	if got := len(w.state(false).Signals); got != 2 {
		t.Errorf("want 2 signals, got %d", got)
	}
}

func TestStateCarriesOutputOfCompletedSignal(t *testing.T) {
	w := testWorker(t)
	path, err := w.submit(SubmitRequest{Prompt: "say hello"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.workSignalMgr.TakeOwnership(path, "test"); err != nil {
		t.Fatal(err)
	}
	w.recordOutput(path, []byte("hello\n"))
	if err := w.workSignalMgr.Complete(path, true, "Work completed"); err != nil {
		t.Fatal(err)
	}

	st := w.state(false)
	if len(st.Signals) != 1 {
		t.Fatalf("want 1 signal, got %d", len(st.Signals))
	}
	got := st.Signals[0]
	if got.Status != string(types.WorkStatusCompleted) || got.Output != "hello\n" || got.Comment != "Work completed" {
		t.Errorf("unexpected summary: status=%q output=%q comment=%q", got.Status, got.Output, got.Comment)
	}
	if got.CompletedAt == 0 {
		t.Error("want a completion time")
	}
}

// TestStateCarriesTimingAgentTypeAndSlopspace: what the worker tab's visual
// sub-tab needs -- when work started, its agent type, and the slopspace it ran
// in, recorded as an event before the agent starts.
func TestStateCarriesTimingAgentTypeAndSlopspace(t *testing.T) {
	w := testWorker(t)
	path, err := w.submit(SubmitRequest{Prompt: "say hello"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.workSignalMgr.TakeOwnership(path, "test"); err != nil {
		t.Fatal(err)
	}
	if err := w.workSignalMgr.AppendEvent(path, &types.WorkEvent{StatusUpdate: "slopspace", SlopspaceID: "abc123"}); err != nil {
		t.Fatal(err)
	}
	w.recordOutput(path, []byte("hello\n"))

	got := w.state(false).Signals[0]
	if got.StartedAt == 0 || got.StartedAt < got.CreatedAt {
		t.Errorf("want a start time at or after creation, got created=%d started=%d", got.CreatedAt, got.StartedAt)
	}
	if got.AgentType != string(w.config.AgentType) {
		t.Errorf("want agent type %s, got %q", w.config.AgentType, got.AgentType)
	}
	if got.SlopspaceID != "abc123" || got.Output != "hello\n" {
		t.Errorf("want slopspace abc123 and the output, got %q and %q", got.SlopspaceID, got.Output)
	}
}

// TestStateListsSlopspaces: a deployed slopspace is in use only while slopspace
// work for its agent type is processing, and in-use ones sort first.
func TestStateListsSlopspaces(t *testing.T) {
	w := testWorker(t)
	idle, err := w.slopspaceMgr.Create()
	if err != nil {
		t.Fatal(err)
	}
	deployed, err := w.slopspaceMgr.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.slopspaceMgr.Deploy(deployed.ID, w.config.AgentType); err != nil {
		t.Fatal(err)
	}

	st := w.state(false)
	if len(st.Slopspaces) != 2 {
		t.Fatalf("want 2 slopspaces, got %d", len(st.Slopspaces))
	}
	first := st.Slopspaces[0]
	if first.ID != deployed.ID || !first.Deployed || first.InUse || first.AgentType != string(w.config.AgentType) {
		t.Errorf("want the deployed, not yet in use slopspace first, got %+v", first)
	}
	if second := st.Slopspaces[1]; second.ID != idle.ID || second.Deployed || second.InUse {
		t.Errorf("want the undeployed slopspace second, got %+v", second)
	}
	if first.Path != deployed.RootPath || first.DeployPath != w.config.DeployPath() {
		t.Errorf("want the deployed slopspace's store and deploy paths, got %q and %q", first.Path, first.DeployPath)
	}
	if second := st.Slopspaces[1]; second.Path != idle.RootPath || second.DeployPath != "" {
		t.Errorf("want only a store path for the undeployed slopspace, got %q and %q", second.Path, second.DeployPath)
	}

	path, err := w.workSignalMgr.Create(&types.WorkSignal{
		WorkType:  types.WorkTypeSlopspace,
		AgentType: w.config.AgentType,
		Role:      "test",
		Prompt:    "work",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if st := w.state(false); st.Slopspaces[0].InUse {
		t.Error("a pending signal shouldn't make the slopspace in use")
	}
	if err := w.workSignalMgr.TakeOwnership(path, "test"); err != nil {
		t.Fatal(err)
	}
	st = w.state(false)
	if !st.Slopspaces[0].InUse || st.Slopspaces[1].InUse {
		t.Errorf("want only the deployed slopspace in use, got %+v", st.Slopspaces)
	}
}

// TestFitStateStaysUnderLineLimit: a worker-state is one representable line,
// which its reader can't take past 64 KiB.
func TestFitStateStaysUnderLineLimit(t *testing.T) {
	big := strings.Repeat("<a & b>\n", maxStateOutput/8)
	st := WorkerStateMsg{WorkerID: "test"}
	for i := 0; i < maxStateCompleted; i++ {
		st.Signals = append(st.Signals, WorkerSignalInfo{ID: fmt.Sprint(i), Prompt: strings.Repeat("p", maxStatePrompt), Output: big})
	}
	fitState(&st)
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxStateBytes {
		t.Errorf("worker-state is %d bytes, want at most %d", len(b), maxStateBytes)
	}
	if st.Signals[0].Output == "" || !st.Signals[0].OutputTrimmed {
		t.Error("the newest signal should keep the end of its output")
	}
	if last := st.Signals[len(st.Signals)-1]; last.Output != "" || !last.OutputTrimmed {
		t.Error("the oldest signal's output should be dropped first")
	}

	small := WorkerStateMsg{Signals: []WorkerSignalInfo{{Prompt: "hi", Output: "hello"}}}
	fitState(&small)
	if small.Signals[0].Output != "hello" || small.Signals[0].OutputTrimmed {
		t.Error("a small state should be left alone")
	}
}
