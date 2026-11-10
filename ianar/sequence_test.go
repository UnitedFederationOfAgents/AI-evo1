package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeKeyboard records every tap/typeText call as a string, failing the call
// whose index is failAt (if non-negative).
type fakeKeyboard struct {
	calls  []string
	failAt int
}

func (k *fakeKeyboard) record(call string) error {
	k.calls = append(k.calls, call)
	if len(k.calls)-1 == k.failAt {
		return errors.New("key boom")
	}
	return nil
}

func (k *fakeKeyboard) tap(key string, mods ...string) error {
	return k.record(strings.Join(append(append([]string{}, mods...), key), "+"))
}

func (k *fakeKeyboard) typeText(text string) error { return k.record("type " + text) }

// stubSequence stubs the keyboard, screen state, federation-command's window
// focus, recording and clock/sleep for a sequence run, restoring everything
// when the test ends. It returns the keyboard and a count of the times the
// recording was stopped.
func stubSequence(t *testing.T) (*fakeKeyboard, *int) {
	t.Helper()
	origKb, origFocus, origRec := openCompositorKeyboard, focusFederationCommand, startNativeRecording
	origClock, origSleep := clock, sleep
	t.Cleanup(func() {
		openCompositorKeyboard, focusFederationCommand, startNativeRecording = origKb, origFocus, origRec
		clock, sleep = origClock, origSleep
	})
	stubScreenState(t, false, false)
	kb := &fakeKeyboard{failAt: -1}
	openCompositorKeyboard = func() (keyboard, func(), error) { return kb, func() {}, nil }
	focusFederationCommand = func() (string, string, error) {
		return `saw "federation-command" at (640, 20) and clicked it via test`, "data:image/jpeg;base64,c2hvdA==", nil
	}
	stops := 0
	startNativeRecording = func(time.Duration, time.Duration) func() ClipResultMsg {
		return func() ClipResultMsg {
			stops++
			return ClipResultMsg{Success: true, Via: "test", VideoURL: "data:video/webm;base64,Zm9v"}
		}
	}
	now := time.Unix(0, 0)
	clock = func() time.Time { return now }
	sleep = func(d time.Duration) { now = now.Add(d) }
	return kb, &stops
}

// fcHelloWorld is the fc-hello-world example, compiled for a run.
func fcHelloWorld(t *testing.T) sequence {
	t.Helper()
	q, err := openSeqLibrary("").compile("fc-hello-world", nil, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// testSequence taps a, types b (leaving an image of what it "saw"), then
// taps c.
func testSequence() sequence {
	tap := func(key string) func(*seqEnv) (string, error) {
		return func(env *seqEnv) (string, error) {
			sleep(stepSettle)
			return "", env.kb.tap(key)
		}
	}
	return sequence{id: "test", name: "Test", steps: []seqStep{
		{SequenceStepDef{Label: "Tap a"}, tap("a")},
		{SequenceStepDef{Label: "Type b"}, func(env *seqEnv) (string, error) {
			env.shot = "data:image/jpeg;base64,c2hvdA=="
			return "typed b", env.kb.typeText("b")
		}},
		{SequenceStepDef{Label: "Tap c"}, tap("c")},
	}}
}

func TestRunSequenceRunsEachStep(t *testing.T) {
	kb, stops := stubSequence(t)
	var progress []string
	res := runSequence(testSequence(), func(p SequenceProgressMsg) {
		progress = append(progress, strconv.Itoa(p.Step)+":"+p.Status)
	})

	if !res.Success || res.FailedStep != -1 || res.Error != "" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if want := []string{"a", "type b", "c"}; !reflect.DeepEqual(kb.calls, want) {
		t.Errorf("keyboard calls = %q, want %q", kb.calls, want)
	}
	if want := []string{"0:running", "0:success", "1:running", "1:success", "2:running", "2:success"}; !reflect.DeepEqual(progress, want) {
		t.Errorf("progress = %q, want %q", progress, want)
	}
	for i, st := range res.Steps {
		if st.Status != "success" {
			t.Errorf("step %d status = %q", i, st.Status)
		}
	}
	if res.Steps[1].Message != "typed b" {
		t.Errorf("step 2 message = %q", res.Steps[1].Message)
	}
	if res.Steps[0].ImageURL != "" || res.Steps[1].ImageURL == "" || res.Steps[2].ImageURL != "" {
		t.Errorf("step images = %+v, want only step 2's", res.Steps)
	}
	if *stops != 1 || res.Recording == nil || !res.Recording.Success {
		t.Errorf("recording stopped %d times, result %+v", *stops, res.Recording)
	}
	if res.KeyboardVia == "" || res.DurationMs <= 0 {
		t.Errorf("keyboard_via = %q, duration = %dms", res.KeyboardVia, res.DurationMs)
	}
}

func TestRunSequenceStopsAtTheFirstFailure(t *testing.T) {
	kb, stops := stubSequence(t)
	kb.failAt = 1 // typing b

	res := runSequence(testSequence(), func(SequenceProgressMsg) {})
	if res.Success || res.FailedStep != 1 || !strings.Contains(res.Error, "step 2 (Type b): key boom") {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Steps[0].Status != "success" || res.Steps[1].Status != "error" || res.Steps[2].Status != "skipped" {
		t.Errorf("step statuses = %+v", res.Steps)
	}
	if res.Steps[1].ImageURL == "" {
		t.Error("the failed step's image should be kept")
	}
	if len(kb.calls) != 2 {
		t.Errorf("keys were sent after the failed step: %q", kb.calls)
	}
	if *stops != 1 || res.Recording == nil {
		t.Errorf("a failed run should still stop and report its recording")
	}
}

func TestRunSequenceRejectsConcurrentRuns(t *testing.T) {
	stubSequence(t)
	seqMu.Lock()
	res := runSequence(testSequence(), func(SequenceProgressMsg) {})
	seqMu.Unlock()
	if res.Success || res.Error == "" {
		t.Errorf("expected a concurrent run to be rejected, got %+v", res)
	}

	clipMu.Lock()
	res = runSequence(testSequence(), func(SequenceProgressMsg) {})
	clipMu.Unlock()
	if res.Success || !strings.Contains(res.Error, "clip") {
		t.Errorf("expected a run during a native clip to be rejected, got %+v", res)
	}
}

// TestStartNativeRecordingPrefersCompositor checks an open-ended recording
// runs from start to stop on the compositor's recorder.
func TestStartNativeRecordingPrefersCompositor(t *testing.T) {
	stubClipPaths(t)
	orig := startCompositorRecording
	t.Cleanup(func() { startCompositorRecording = orig })
	stopped := false
	startCompositorRecording = func() (func() ([]byte, string, error), error) {
		return func() ([]byte, string, error) {
			stopped = true
			return []byte("foo"), "video/webm", nil
		}, nil
	}

	stop := startNativeRecording(time.Minute, time.Second)
	sleep(3 * time.Second)
	res := stop()
	if !stopped || !res.Success || res.VideoURL != "data:video/webm;base64,Zm9v" || res.DurationMs != 3000 {
		t.Errorf("unexpected result: %+v (stopped=%v)", res, stopped)
	}
}

// ---- keyboard ----

func TestOpenKeyboardFallsBackToRobotgo(t *testing.T) {
	orig := openCompositorKeyboard
	t.Cleanup(func() { openCompositorKeyboard = orig })
	openCompositorKeyboard = func() (keyboard, func(), error) { return nil, nil, errCompositorInputUnavailable }

	kb, via, _, err := openKeyboard()
	if err != nil || via != "robotgo" {
		t.Fatalf("openKeyboard() = %v, %q, %v", kb, via, err)
	}
	if _, ok := kb.(robotgoKeyboard); !ok {
		t.Errorf("got %T, want robotgoKeyboard", kb)
	}
}

func TestKeysymKeyboard(t *testing.T) {
	origSleep := sleep
	t.Cleanup(func() { sleep = origSleep })
	sleep = func(time.Duration) {}

	type ev struct {
		sym     uint32
		pressed bool
	}
	var evs []ev
	kb := keysymKeyboard{notify: func(sym uint32, pressed bool) error {
		evs = append(evs, ev{sym, pressed})
		return nil
	}}

	if err := kb.tap("u", "ctrl"); err != nil {
		t.Fatal(err)
	}
	want := []ev{{0xffe3, true}, {'u', true}, {'u', false}, {0xffe3, false}}
	if !reflect.DeepEqual(evs, want) {
		t.Errorf("ctrl+u events = %v, want %v", evs, want)
	}

	evs = nil
	if err := kb.typeText(`a"!é→`); err != nil {
		t.Fatal(err)
	}
	var pressed []uint32
	for _, e := range evs {
		if e.pressed {
			pressed = append(pressed, e.sym)
		}
	}
	if want := []uint32{'a', '"', '!', 0xe9, 0x01002192}; !reflect.DeepEqual(pressed, want) {
		t.Errorf("typed keysyms = %#x, want %#x", pressed, want)
	}

	if err := kb.tap("no-such-key"); err == nil {
		t.Errorf("expected an unknown key name to fail")
	}
}

func TestKeysymKeyboardReleasesModifiersOnFailure(t *testing.T) {
	held := map[uint32]bool{}
	kb := keysymKeyboard{notify: func(sym uint32, pressed bool) error {
		if sym == 'u' {
			return errors.New("boom")
		}
		held[sym] = pressed
		return nil
	}}
	if err := kb.tap("u", "ctrl"); err == nil {
		t.Fatal("expected the failed key press to be reported")
	}
	if held[0xffe3] {
		t.Errorf("ctrl was left held down after a failed tap")
	}
}

// ---- window lookup ----

// fakeProc writes a fake /proc entry for pid with the given argv0, comm and
// parent pid.
func fakeProc(t *testing.T, root string, pid int, argv0, comm string, ppid int) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"cmdline": argv0 + "\x00--flag\x00",
		"comm":    comm + "\n",
		"stat":    strconv.Itoa(pid) + " (" + comm + ") S " + strconv.Itoa(ppid) + " 1 1 0 -1",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFederationCommandTarget(t *testing.T) {
	root := t.TempDir()
	origRoot, origSelf := procRoot, selfPid
	t.Cleanup(func() { procRoot, selfPid = origRoot, origSelf })
	procRoot = root
	selfPid = func() int { return 300 }

	// systemd(10) -> local-representative(20) -> xterm(30) -> FC(40)
	//                                       \-> ianar(300)
	fakeProc(t, root, 10, "/usr/lib/systemd/systemd", "systemd", 1)
	fakeProc(t, root, 20, "/opt/bin/local-representative", "local-represent", 10)
	fakeProc(t, root, 30, "/usr/bin/xterm", "xterm", 20)
	fakeProc(t, root, 40, "/opt/bin/federation-command", "federation-comm", 30)
	fakeProc(t, root, 300, "/opt/bin/ianar", "ianar", 20)
	// A process found only by its (truncated) comm, e.g. a rewritten argv.
	fakeProc(t, root, 50, "fc", "federation-comm", 1)

	tgt, err := federationCommandTarget()
	if err != nil {
		t.Fatal(err)
	}
	if tgt.title != "federation-command" {
		t.Errorf("title = %q", tgt.title)
	}
	// FC's own pids first, then its terminal; local-representative and
	// systemd are IANAR's ancestors too, so their windows are never chosen.
	if want := []int{40, 50, 30}; !reflect.DeepEqual(tgt.pids, want) {
		t.Errorf("pids = %v, want %v", tgt.pids, want)
	}
}

func TestFederationCommandTargetNotRunning(t *testing.T) {
	origRoot := procRoot
	t.Cleanup(func() { procRoot = origRoot })
	procRoot = t.TempDir()

	if _, err := federationCommandTarget(); err == nil || !strings.Contains(err.Error(), "no federation-command process") {
		t.Errorf("err = %v", err)
	}
}

func TestFocusWindowTriesEachPath(t *testing.T) {
	var tried []string
	attempt := func(name string, err error) focusAttempt {
		return focusAttempt{name, func(windowTarget) (string, error) {
			tried = append(tried, name)
			if err != nil {
				return "", err
			}
			return `"federation-command"`, nil
		}}
	}

	desc, err := focusWindow(windowTarget{}, []focusAttempt{
		attempt("a", errFocusPathUnavailable),
		attempt("b", nil),
		attempt("c", nil),
	})
	if err != nil || !strings.Contains(desc, "via b") || !reflect.DeepEqual(tried, []string{"a", "b"}) {
		t.Errorf("focusWindow = %q, %v; tried %v", desc, err, tried)
	}

	_, err = focusWindow(windowTarget{}, []focusAttempt{
		attempt("a", errors.New("a boom")),
		attempt("b", errors.New("b boom")),
	})
	if err == nil || !strings.Contains(err.Error(), "a boom") || !strings.Contains(err.Error(), "b boom") {
		t.Errorf("err = %v, want every path's failure", err)
	}
}
