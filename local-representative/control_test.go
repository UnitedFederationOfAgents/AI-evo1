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
	if len(st.Sequences) != 1 || st.Sequences[0].ID != "fc-robot-handoff" {
		t.Fatalf("sequences = %+v", st.Sequences)
	}
	if n := len(st.Sequences[0].Steps); n != 5 {
		t.Errorf("fc-robot-handoff has %d steps, want 5", n)
	}
	if st.Run != nil {
		t.Errorf("run before any started = %+v", st.Run)
	}
}

func TestControlStartUnknownSequence(t *testing.T) {
	s := newServer("test-lr")
	if err := s.control.start("no-such-sequence"); err == nil {
		t.Error("start of an unknown sequence succeeded")
	}
}

// runningEngine puts e in the middle of a run without starting any steps.
func runningEngine(s *Server) *controlRun {
	e := s.control
	e.mu.Lock()
	e.run = &ControlRunMsg{Status: "running", Steps: []ControlStepResult{{Label: "only"}}}
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
