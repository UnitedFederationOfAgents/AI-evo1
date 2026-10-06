package main

import (
	"strings"
	"testing"
)

func TestLRRecordingStartStop(t *testing.T) {
	_, stops := stubSequence(t)
	s := newServer()

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
