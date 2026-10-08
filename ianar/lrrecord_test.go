package main

import (
	"strings"
	"testing"
	"time"
)

func TestLRRecordingStartStop(t *testing.T) {
	_, stops := stubSequence(t)
	s := newServer()
	defer func(w time.Duration) { lrRecordHandoffWait = w }(lrRecordHandoffWait)
	lrRecordHandoffWait = 100 * time.Millisecond

	if got := startLRRecording(RobotRecordRequest{Rec: "r1", Name: "fc-robot-handoff"}); got.Status != "recording" {
		t.Fatalf("start = %+v", got)
	}
	// One recording at a time: it holds the Native Clip lock.
	if got := startLRRecording(RobotRecordRequest{Rec: "r2"}); got.Status != "error" {
		t.Errorf("a second recording started: %+v", got)
	}
	if res := recordNativeClip(); res.Success {
		t.Errorf("a native clip recorded during the recording")
	}

	got := s.stopLRRecording(RobotRecordRequest{Rec: "r1"})
	if got.Status != "saved" || got.Via != "test" || *stops != 1 {
		t.Fatalf("stop = %+v (stops %d)", got, *stops)
	}
	if !clipMu.TryLock() {
		t.Fatal("the recording didn't release the clip lock")
	}
	clipMu.Unlock()
	if got := s.stopLRRecording(RobotRecordRequest{Rec: "r1"}); got.Status != "error" {
		t.Errorf("stopping it again = %+v", got)
	}
}

func TestRecordingArtifactName(t *testing.T) {
	a := recordingArtifact("FC robot hand-off!", ClipResultMsg{Success: true, VideoURL: "data:video/webm;base64,Zm9v"})
	if !strings.HasPrefix(a.name, "ianar-recording-fc-robot-hand-off-") || !strings.HasSuffix(a.name, ".webm") {
		t.Errorf("name = %q", a.name)
	}
}

// TestLRRecordingHandoff: a recording asked for while the one before it
// still holds the recorder -- back-to-back blocks of the same screen --
// waits for it to stop rather than failing.
func TestLRRecordingHandoff(t *testing.T) {
	_, stops := stubSequence(t)
	s := newServer()

	if got := startLRRecording(RobotRecordRequest{Rec: "r1", Name: "steps-2-4"}); got.Status != "recording" {
		t.Fatalf("start r1 = %+v", got)
	}
	// r2's start arrives before r1's stop, as it may through
	// agent-coordinator.
	started := make(chan RobotRecordMsg, 1)
	go func() { started <- startLRRecording(RobotRecordRequest{Rec: "r2", Name: "steps-5-7"}) }()
	time.Sleep(150 * time.Millisecond)
	if got := s.stopLRRecording(RobotRecordRequest{Rec: "r1"}); got.Status != "saved" {
		t.Fatalf("stop r1 = %+v", got)
	}
	select {
	case got := <-started:
		if got.Status != "recording" {
			t.Fatalf("start r2 = %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("r2 never started")
	}
	if got := s.stopLRRecording(RobotRecordRequest{Rec: "r2"}); got.Status != "saved" || *stops != 2 {
		t.Fatalf("stop r2 = %+v (stops %d)", got, *stops)
	}
	if lrRecordBusy() {
		t.Error("the recorder is still marked busy")
	}
}

// TestRobotRunWithoutRecording: a run LR asks not to record leaves the
// recorder (and Native Clip's lock) alone.
func TestRobotRunWithoutRecording(t *testing.T) {
	_, stops := stubSequence(t)
	q, err := compileRobotRun(RobotRunRequest{
		Run:      "abc",
		Steps:    []RobotRunStep{{Label: "Press Enter", Do: []Instruction{{"op": "key", "keys": "enter"}}}},
		NoRecord: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clipMu.Lock() // as an LR recording holds it
	defer clipMu.Unlock()
	res := runSequence(q, func(SequenceProgressMsg) {})
	if !res.Success || res.Recording != nil || *stops != 0 {
		t.Errorf("result = %+v (stops %d)", res, *stops)
	}
}
