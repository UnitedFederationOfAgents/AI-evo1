package main

import (
	"strings"
	"testing"
	"time"
)

// The you-tell-me example's ops (Step3Prompt.md Revision G): ask-user waits
// for continue, fc-capture-echo takes the phrase someone echoed, output
// brings it back.

func TestAskUserWaitsForContinue(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)
	e := s.control
	e.mu.Lock()
	e.run.ID = "run1"
	e.mu.Unlock()

	if err := e.continueRun(""); err == nil {
		t.Error("continue with nothing waiting was accepted")
	}
	done := make(chan error, 1)
	go func() {
		_, err := opAskUser(r, opArgs{"message": "do the thing", "timeout": "5s"})
		done <- err
	}()
	var prompt *ControlPrompt
	for i := 0; i < 100 && prompt == nil; i++ {
		time.Sleep(10 * time.Millisecond)
		prompt = e.state().Run.Prompt
	}
	if prompt == nil || prompt.Message != "do the thing" || prompt.Step != 0 {
		t.Fatalf("prompt = %+v", prompt)
	}
	if err := e.continueRun("another-run"); err == nil {
		t.Error("continue for another run was accepted")
	}
	if err := e.continueRun("run1"); err != nil {
		t.Fatalf("continue: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ask-user: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask-user didn't return after continue")
	}
	if p := e.state().Run.Prompt; p != nil {
		t.Errorf("prompt still shown after continue: %+v", p)
	}
}

func TestAskUserCancelled(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.control.cancel()
	}()
	if _, err := opAskUser(r, opArgs{"message": "x", "timeout": "5s"}); err != errControlCancelled {
		t.Fatalf("err = %v, want errControlCancelled", err)
	}
}

func TestFCCaptureEcho(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)
	fc := "federation-command#1"
	r.stepMark = r.e.logMark()
	s.handleFCLog(fc, `echo "first try"`, "cmd")
	s.handleFCLog(fc, "first try", "output")
	s.handleFCLog("federation-command#2", `echo "not this one"`, "cmd")
	s.handleFCLog(fc, `echo "the  phrase"`, "cmd")
	s.handleFCLog(fc, "the  phrase", "output")

	note, err := opFCCaptureEcho(r, opArgs{"fc": fc, "save_as": "phrase", "timeout": "1s"})
	if err != nil || r.vars["phrase"] != "the  phrase" {
		t.Fatalf("captured %q (%s), err %v", r.vars["phrase"], note, err)
	}

	// Output other than the echo's argument is taken once the wait is up.
	r.stepMark = r.e.logMark()
	s.handleFCLog(fc, `echo "$HOME"`, "cmd")
	s.handleFCLog(fc, "/home/someone", "output")
	if _, err := opFCCaptureEcho(r, opArgs{"fc": fc, "save_as": "phrase", "timeout": "200ms"}); err != nil || r.vars["phrase"] != "/home/someone" {
		t.Errorf("captured %q, err %v", r.vars["phrase"], err)
	}
}

func TestFCCaptureEchoFailures(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)
	fc := "federation-command#1"
	r.stepMark = r.e.logMark()
	if _, err := opFCCaptureEcho(r, opArgs{"fc": fc, "save_as": "phrase", "timeout": "200ms"}); err == nil || !strings.Contains(err.Error(), "no echo") {
		t.Errorf("no echo entered: err = %v", err)
	}
	s.handleFCLog(fc, `echo ""`, "cmd")
	if _, err := opFCCaptureEcho(r, opArgs{"fc": fc, "save_as": "phrase", "timeout": "5s"}); err == nil || !strings.Contains(err.Error(), "echoes nothing") {
		t.Errorf("empty echo: err = %v", err)
	}
}

func TestEchoArgument(t *testing.T) {
	for in, want := range map[string]string{
		`"hello world"`:     "hello world",
		`'it is'`:           "it is",
		`hello    world`:    "hello world",
		`"multiple   gaps"`: "multiple   gaps",
		`"say \"hi\""`:      `say "hi"`,
		`'single "inside"'`: `single "inside"`,
		`  " padded "  `:    "padded",
		`"a" "b"`:           "a b",
		`""`:                "",
		``:                  "",
	} {
		if got := echoArgument(in); got != want {
			t.Errorf("echoArgument(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOutputOp(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)
	if _, err := opOutput(r, opArgs{"label": "captured phrase", "value": "hi there"}); err != nil {
		t.Fatal(err)
	}
	out := s.control.state().Run.Output
	if len(out) != 1 || out[0].Label != "captured phrase" || out[0].Value != "hi there" {
		t.Errorf("output = %+v", out)
	}
}
