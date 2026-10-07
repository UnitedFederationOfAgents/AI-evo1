package main

import (
	"strings"
	"testing"
	"time"
)

func TestRandomTokenAvoidsLookalikes(t *testing.T) {
	tok := randomToken(200)
	if len(tok) != 200 {
		t.Fatalf("len = %d, want 200", len(tok))
	}
	if strings.ContainsAny(tok, "01oil") {
		t.Errorf("token %q contains a character OCR mixes up", tok)
	}
}

func TestControlStateListsSequences(t *testing.T) {
	s := newServer("test-lr")
	st := s.control.state()
	if len(st.Sequences) != 2 || st.Sequences[0].ID != "fc-robot-handoff" || st.Sequences[1].ID != "capture-two-nodes" {
		t.Fatalf("sequences = %+v", st.Sequences)
	}
	if n := len(st.Sequences[0].Steps); n != 6 {
		t.Errorf("fc-robot-handoff has %d steps, want 6", n)
	}
	for _, q := range st.Sequences {
		if q.Error != "" {
			t.Errorf("%s doesn't compile: %s", q.ID, q.Error)
		}
	}
	if st.Node != "test-lr" || len(st.Nodes) != 0 {
		t.Errorf("node %q, nodes %v before agent-coordinator said any", st.Node, st.Nodes)
	}
	if st.Run != nil {
		t.Errorf("run before any started = %+v", st.Run)
	}
}

func TestControlStartUnknownSequence(t *testing.T) {
	s := newServer("test-lr")
	if err := s.control.start("no-such-sequence", nil); err == nil {
		t.Error("start of an unknown sequence succeeded")
	}
}

// exampleSequence compiles the built-in fc-robot-handoff example.
func exampleSequence(t *testing.T) controlSequence {
	t.Helper()
	q, _, err := newControlLibrary().compile("fc-robot-handoff", nil, nil, time.Now())
	if err != nil {
		t.Fatalf("compiling fc-robot-handoff: %v", err)
	}
	return q
}

// runningEngine puts e in the middle of a run without starting any steps.
func runningEngine(s *Server) *controlRun {
	e := s.control
	e.mu.Lock()
	e.run = &ControlRunMsg{Status: "running", Steps: []ControlStepResult{{ControlStepInfo: ControlStepInfo{Label: "only"}}}}
	e.cancelCh = make(chan struct{})
	cancel := e.cancelCh
	e.mu.Unlock()
	return &controlRun{e: e, s: s, cancel: cancel, vars: map[string]string{}}
}

func TestWaitForOutputSeesOnlyNewLinesFromThatInstance(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)

	s.handleFCLog("federation-command#1", "found it", "output") // before the mark: doesn't count
	mark := r.e.logMark()
	s.handleFCLog("federation-command#2", "found it", "output") // another instance
	s.handleFCLog("federation-command#1", `echo "found it"`, "cmd")

	ok, err := r.waitForOutput("federation-command#1", mark, "found it", 300*time.Millisecond)
	if err != nil || ok {
		t.Fatalf("waitForOutput = %v, %v; want false (no new output from #1)", ok, err)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		s.handleFCLog("federation-command#1", "  found it  ", "output")
	}()
	ok, err = r.waitForOutput("federation-command#1", mark, "found it", 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("waitForOutput = %v, %v; want true", ok, err)
	}
}

func TestWaitForCancelled(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)
	s.control.cancel()
	_, err := r.waitFor(5*time.Second, func() bool { return false })
	if err != errControlCancelled {
		t.Fatalf("err = %v, want errControlCancelled", err)
	}
}

func TestRobotRunNeedsRobot(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)
	if _, err := r.robotRun("x", []robotStep{{Label: "a", Do: []map[string]string{{"op": "key", "keys": "right"}}}}); err == nil ||
		!strings.Contains(err.Error(), "robot") {
		t.Fatalf("err = %v, want a robot-not-connected error", err)
	}
}

func TestHandoffSequenceRecordsTheRobotsScreen(t *testing.T) {
	q := exampleSequence(t)
	if info := q.info(); len(info.Record) != 1 || info.Record[0] != recordLocalRobot {
		t.Errorf("record = %v, want [%s]", info.Record, recordLocalRobot)
	}
}

// TestRecordingWithoutRobotIsNotFatal: the run goes on, noting why there's
// no recording.
func TestRecordingWithoutRobotIsNotFatal(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)
	q := exampleSequence(t)
	r.startRecording(q, recordLocalRobot)
	r.stopRecording() // nothing to stop
	if r.rec != "" {
		t.Errorf("rec = %q", r.rec)
	}
	vals := s.control.state().Run.Values
	if len(vals) != 1 || vals[0].Label != recordingLabel || !strings.Contains(vals[0].Value, "not recording") {
		t.Errorf("values = %+v", vals)
	}
}

// TestHandoffSequenceUnlocksFirst: Revision B -- the sequence begins by
// unlocking the screen, before the recording starts, and only leading steps
// run ahead of the recording.
func TestHandoffSequenceUnlocksFirst(t *testing.T) {
	q := exampleSequence(t)
	if len(q.steps) == 0 || !q.steps[0].beforeRecording || !strings.Contains(q.steps[0].Label, "Unlock") {
		t.Fatalf("first step = %+v, want the unlock, before the recording", q.steps[0].ControlStepInfo)
	}
	recorded := false
	for i, st := range q.steps {
		if !st.beforeRecording {
			recorded = true
		} else if recorded {
			t.Errorf("step %d (%s) is marked beforeRecording after a recorded step", i+1, st.Label)
		}
	}
}

func TestIsPlayableVideo(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"ianar-recording-fc-robot-handoff-x.webm", true},
		{"x.MP4", true},
		{"x.mkv", true},
		{"ianar-recording-fc-robot-handoff-x.zip", false}, // sampled frames
		{"x", false},
	} {
		if got := isPlayableVideo(tc.name); got != tc.want {
			t.Errorf("isPlayableVideo(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRecordingsInState(t *testing.T) {
	s := newServer("test-lr")
	runningEngine(s)
	s.control.addRecording(ControlRecording{Who: recordLocalRobot, FileID: "abc-x.webm", Name: "x.webm", Video: true})
	recs := s.control.state().Run.Recordings
	if len(recs) != 1 || recs[0].FileID != "abc-x.webm" || !recs[0].Video {
		t.Errorf("recordings = %+v", recs)
	}
}

func TestNoteRobotRecordDelivers(t *testing.T) {
	s := newServer("test-lr")
	e := s.control
	ch := make(chan RobotRecordMsg, 1)
	e.mu.Lock()
	e.recs["k1"] = ch
	e.mu.Unlock()
	e.noteRobotRecord(RobotRecordMsg{Rec: "other", Status: "saved"})
	e.noteRobotRecord(RobotRecordMsg{Rec: "k1", Status: "saved", SavedAs: "ianar-recording-x.webm"})
	e.noteRobotRecord(RobotRecordMsg{Rec: "k1", Status: "saved"}) // a duplicate doesn't block
	if got := <-ch; got.SavedAs != "ianar-recording-x.webm" {
		t.Errorf("delivered %+v", got)
	}
}

func TestSetValueReplaces(t *testing.T) {
	s := newServer("test-lr")
	runningEngine(s)
	s.control.addValue("a", "1")
	s.control.setValue(recordingLabel, "recording…")
	s.control.setValue(recordingLabel, "x.webm")
	vals := s.control.state().Run.Values
	if len(vals) != 2 || vals[1].Value != "x.webm" {
		t.Errorf("values = %+v", vals)
	}
}

// TestMarkerPrintedBy: Revision A's second failure, the echo reaching an
// instance other than the one launched.
func TestMarkerPrintedBy(t *testing.T) {
	logs := []fcLogEvent{
		{fc: "federation-command#2", line: `echo "This is the one - abc"`, kind: "cmd"},
		{fc: "federation-command#2", line: "This is the one - abc", kind: "output"},
	}
	if got := markerPrintedBy(logs, "This is the one - abc", "federation-command#2"); got != "" {
		t.Errorf("got %q for the right instance", got)
	}
	logs = append(logs, fcLogEvent{fc: "federation-command#1", line: " This is the one - abc ", kind: "output"})
	if got := markerPrintedBy(logs, "This is the one - abc", "federation-command#2"); got != "federation-command#1" {
		t.Errorf("got %q, want federation-command#1", got)
	}
}

func TestNoteRobotRunDeliversResult(t *testing.T) {
	s := newServer("test-lr")
	e := s.control
	ch := make(chan RobotRunMsg, 1)
	var progressed []int
	e.mu.Lock()
	e.robot["r1"] = ch
	e.progress["r1"] = func(p RobotRunMsg) { progressed = append(progressed, p.Step) }
	e.mu.Unlock()

	e.noteRobotRun(false, RobotRunMsg{Run: "r1", Step: 1, Status: "running"})
	e.noteRobotRun(false, RobotRunMsg{Run: "other", Step: 7})
	e.noteRobotRun(true, RobotRunMsg{Run: "r1", Success: true})

	if len(progressed) != 1 || progressed[0] != 1 {
		t.Errorf("progress calls = %v, want [1]", progressed)
	}
	select {
	case res := <-ch:
		if !res.Success {
			t.Errorf("result = %+v", res)
		}
	default:
		t.Fatal("no result delivered")
	}
	if _, ok := e.robot["r1"]; ok {
		t.Error("result waiter not removed")
	}
}
