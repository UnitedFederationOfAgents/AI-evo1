package main

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestRunSequenceCancelledStopsBeforeNextStep verifies a run whose cancel is
// closed partway stops before its next step and reports it cancelled, with
// the recording still stopped -- see condocs/initialRobotImpls/
// Step3Prompt.md Revision M.
func TestRunSequenceCancelledStopsBeforeNextStep(t *testing.T) {
	kb, stops := stubSequence(t)
	cancel := make(chan struct{})
	q := testSequence()
	first := q.steps[0].run
	q.steps[0].run = func(env *seqEnv) (string, error) {
		note, err := first(env)
		close(cancel)
		return note, err
	}
	q.cancel = cancel

	res := runSequence(q, func(SequenceProgressMsg) {})

	if res.Success || !res.Cancelled || res.FailedStep != 1 {
		t.Fatalf("result = %+v, want cancelled at step 2", res)
	}
	if want := []string{"a"}; !reflect.DeepEqual(kb.calls, want) {
		t.Errorf("keyboard calls = %q, want %q", kb.calls, want)
	}
	if got := []string{res.Steps[0].Status, res.Steps[1].Status, res.Steps[2].Status}; !reflect.DeepEqual(got, []string{"success", "error", "skipped"}) {
		t.Errorf("step statuses = %q", got)
	}
	if *stops != 1 {
		t.Errorf("recording stopped %d times, want 1", *stops)
	}
}

// cancellingKeyboard closes cancel on its first key press.
type cancellingKeyboard struct {
	*fakeKeyboard
	cancel chan struct{}
}

func (k cancellingKeyboard) tap(key string, mods ...string) error {
	if len(k.calls) == 0 {
		close(k.cancel)
	}
	return k.fakeKeyboard.tap(key, mods...)
}

// TestActionRunnerStopsBetweenInstructions verifies a cancel lands between
// one step's instructions, not only between steps.
func TestActionRunnerStopsBetweenInstructions(t *testing.T) {
	origSleep := sleep
	t.Cleanup(func() { sleep = origSleep })
	sleep = func(time.Duration) {}

	kb := cancellingKeyboard{&fakeKeyboard{failAt: -1}, make(chan struct{})}
	env := &seqEnv{kb: kb, vars: map[string]string{}, cancel: kb.cancel}
	run := actionRunner(ActionDef{ID: "x", Do: []Instruction{{"op": "key", "keys": "a"}, {"op": "key", "keys": "b"}}}, StepRef{Action: "x"})
	if _, err := run(env); !errors.Is(err, errSeqCancelled) {
		t.Fatalf("err = %v, want errSeqCancelled", err)
	}
	if want := []string{"a"}; !reflect.DeepEqual(kb.calls, want) {
		t.Errorf("keyboard calls = %q, want %q", kb.calls, want)
	}
}

// TestPollScreenEndsOnCancel verifies a wait for text on screen ends as soon
// as the run is cancelled rather than running out its timeout.
func TestPollScreenEndsOnCancel(t *testing.T) {
	reads := stubScreenLines(t, func(int) []OCRLine { return nil })
	cancel := make(chan struct{})
	close(cancel)
	env := &seqEnv{cancel: cancel}
	start := time.Now()
	_, ok, err := pollScreen(env, time.Hour, func(*screenReading) bool { return false })
	if ok || !errors.Is(err, errSeqCancelled) {
		t.Fatalf("ok = %v, err = %v; want errSeqCancelled", ok, err)
	}
	if *reads != 1 {
		t.Errorf("screen read %d times, want once", *reads)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %s to notice the cancel", time.Since(start))
	}
}

// TestCancelLRRun verifies "__robot:cancel" closes the named run's cancel
// exactly once and ignores runs it doesn't know.
func TestCancelLRRun(t *testing.T) {
	s := newServer()
	ch := make(chan struct{})
	s.lrRuns["r1"] = ch

	s.handleRobotCancel(`{"run": "r1"}`)
	select {
	case <-ch:
	default:
		t.Fatal("run r1 wasn't cancelled")
	}
	if s.cancelLRRun("r1") {
		t.Error("cancelling r1 a second time reported it still going")
	}
	if s.cancelLRRun("nope") {
		t.Error("cancelling an unknown run reported it going")
	}
}
