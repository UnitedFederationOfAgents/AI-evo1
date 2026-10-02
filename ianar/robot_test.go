package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

// decodeSent unmarshals a single marshalMsg'd message off a wsClient's send
// channel, failing the test if none arrived or the type doesn't match.
func decodeSent(t *testing.T, c *wsClient, wantType string, out interface{}) {
	t.Helper()
	select {
	case raw := <-c.send:
		var m wsMsg
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal wsMsg: %v", err)
		}
		if m.Type != wantType {
			t.Fatalf("message type = %q, want %q", m.Type, wantType)
		}
		if out != nil {
			if err := json.Unmarshal(m.Payload, out); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
		}
	default:
		t.Fatalf("expected a %q message, got none", wantType)
	}
}

// withStubbedLookPath overrides the package-level lookPath for the duration
// of fn, restoring the original afterwards -- lets findNativeCaptureCommand
// and circleMouse be tested without depending on what's actually installed
// on the box running the tests.
func withStubbedLookPath(t *testing.T, found map[string]bool, fn func()) {
	t.Helper()
	orig := lookPath
	lookPath = func(bin string) (string, error) {
		if found[bin] {
			return "/usr/bin/" + bin, nil
		}
		return "", fmt.Errorf("exec: %q: executable file not found in $PATH", bin)
	}
	defer func() { lookPath = orig }()
	fn()
}

// ---- findNativeCaptureCommand ----

func TestFindNativeCaptureCommandPrefersFirstMatch(t *testing.T) {
	withStubbedLookPath(t, map[string]bool{"maim": true, "gnome-screenshot": true}, func() {
		bin, _, err := findNativeCaptureCommand()
		if err != nil {
			t.Fatalf("findNativeCaptureCommand: %v", err)
		}
		if bin != "maim" {
			t.Errorf("bin = %q, want %q (first match in preference order)", bin, "maim")
		}
	})
}

func TestFindNativeCaptureCommandNoneFound(t *testing.T) {
	withStubbedLookPath(t, map[string]bool{}, func() {
		_, _, err := findNativeCaptureCommand()
		if err == nil {
			t.Fatalf("expected an error when no candidate is on PATH")
		}
	})
}

// ---- captureNativeDisplay ----

func TestCaptureNativeDisplayNoToolFound(t *testing.T) {
	withStubbedLookPath(t, map[string]bool{}, func() {
		if _, err := captureNativeDisplay(); err == nil {
			t.Fatalf("expected an error when no screenshot tool is on PATH")
		}
	})
}

func TestCaptureNativeDisplayWritesAndReadsTempFile(t *testing.T) {
	origRun := runCapture
	defer func() { runCapture = origRun }()
	runCapture = func(bin string, args []string) error {
		// Simulate the tool writing its output to the path IANAR resolved
		// in place of the capture command's "%s" placeholder.
		path := args[len(args)-1]
		return os.WriteFile(path, []byte("fake-png-bytes"), 0o600)
	}

	withStubbedLookPath(t, map[string]bool{"scrot": true}, func() {
		data, err := captureNativeDisplay()
		if err != nil {
			t.Fatalf("captureNativeDisplay: %v", err)
		}
		if string(data) != "fake-png-bytes" {
			t.Errorf("captured data = %q, want %q", data, "fake-png-bytes")
		}
	})
}

func TestCaptureNativeDisplayToolFailure(t *testing.T) {
	origRun := runCapture
	defer func() { runCapture = origRun }()
	runCapture = func(bin string, args []string) error {
		return errors.New("boom")
	}

	withStubbedLookPath(t, map[string]bool{"scrot": true}, func() {
		if _, err := captureNativeDisplay(); err == nil {
			t.Fatalf("expected captureNativeDisplay to propagate the tool's error")
		}
	})
}

// ---- circlePoints ----

// TestCirclePointsShape verifies circlePoints returns a closed loop (first
// and last points coincide) of the requested length, every point at exactly
// radius from the center.
func TestCirclePointsShape(t *testing.T) {
	pts := circlePoints(100, 100, 80, 36)
	if len(pts) != 37 { // steps+1: inclusive of both endpoints
		t.Fatalf("len(pts) = %d, want 37", len(pts))
	}
	if pts[0] != pts[36] {
		t.Errorf("circlePoints should close the loop: first=%v last=%v", pts[0], pts[36])
	}
	for i, p := range pts {
		dx, dy := float64(p[0])-100, float64(p[1])-100
		dist := math.Hypot(dx, dy)
		if math.Abs(dist-80) > 1 { // rounding to int can be off by <1px
			t.Errorf("point %d = %v is %.2fpx from center, want ~80", i, p, dist)
		}
	}
}

// TestCirclePointsClockwise verifies the second point moves down-and-right
// from the first (theta=0 is due east; screen y grows downward, so
// increasing theta moves clockwise as drawn on screen).
func TestCirclePointsClockwise(t *testing.T) {
	pts := circlePoints(0, 0, 100, 4) // quarter turns: east, south, west, north, east
	want := [][2]int{{100, 0}, {0, 100}, {-100, 0}, {0, -100}, {100, 0}}
	for i, w := range want {
		if pts[i] != w {
			t.Errorf("pts[%d] = %v, want %v", i, pts[i], w)
		}
	}
}

// ---- circleMouse ----

func TestCircleMouseNoXdotool(t *testing.T) {
	withStubbedLookPath(t, map[string]bool{}, func() {
		if err := circleMouse(); err == nil {
			t.Fatalf("expected an error when xdotool is not on PATH")
		}
	})
}

// ---- WebSocket handlers ----

func TestHandleCaptureBrowserEmptyData(t *testing.T) {
	s := newServer()
	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	s.handleCaptureBrowser(c, "")
	var p CaptureResultMsg
	decodeSent(t, c, "capture-result", &p)
	if p.Success || p.Source != "browser" {
		t.Errorf("unexpected result: %+v", p)
	}
}

func TestHandleCaptureBrowserPassesThroughImage(t *testing.T) {
	s := newServer()
	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	s.handleCaptureBrowser(c, "data:image/png;base64,Zm9v")
	var p CaptureResultMsg
	decodeSent(t, c, "capture-result", &p)
	if !p.Success || p.Source != "browser" || p.ImageURL != "data:image/png;base64,Zm9v" {
		t.Errorf("unexpected result: %+v", p)
	}
}

func TestHandleCaptureNativeNoToolReportsError(t *testing.T) {
	s := newServer()
	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	withStubbedLookPath(t, map[string]bool{}, func() {
		s.handleCaptureNative(c)
	})
	var p CaptureResultMsg
	decodeSent(t, c, "capture-result", &p)
	if p.Success || p.Source != "native" || p.Error == "" {
		t.Errorf("unexpected result: %+v", p)
	}
}

func TestHandleCircleMouseReportsError(t *testing.T) {
	s := newServer()
	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	withStubbedLookPath(t, map[string]bool{}, func() {
		s.handleCircleMouse(c)
	})
	var p CircleMouseResultMsg
	decodeSent(t, c, "circle-mouse-result", &p)
	if p.Success || p.Error == "" {
		t.Errorf("unexpected result: %+v", p)
	}
}

// TestDriveCircle verifies the stepping loop visits every circlePoints
// waypoint via runMouseMove, sleeping circleDur/circleSteps between each --
// split out from circleMouse (see driveCircle) specifically so this is
// testable without xdotool or a real second of wall-clock time.
func TestDriveCircle(t *testing.T) {
	origMove, origSleep := runMouseMove, sleep
	defer func() { runMouseMove, sleep = origMove, origSleep }()

	var moves [][2]int
	var slept []time.Duration
	runMouseMove = func(x, y int) error {
		moves = append(moves, [2]int{x, y})
		return nil
	}
	sleep = func(d time.Duration) { slept = append(slept, d) }

	if err := driveCircle(100, 100); err != nil {
		t.Fatalf("driveCircle: %v", err)
	}

	wantPts := circlePoints(100, 100, circleRadius, circleSteps)
	if len(moves) != len(wantPts) {
		t.Fatalf("len(moves) = %d, want %d", len(moves), len(wantPts))
	}
	for i, p := range wantPts {
		if moves[i] != p {
			t.Errorf("moves[%d] = %v, want %v", i, moves[i], p)
		}
	}
	if len(slept) != len(wantPts) {
		t.Fatalf("len(slept) = %d, want %d", len(slept), len(wantPts))
	}
	wantInterval := circleDur / circleSteps
	for i, d := range slept {
		if d != wantInterval {
			t.Errorf("slept[%d] = %v, want %v", i, d, wantInterval)
		}
	}
}

// TestDriveCirclePropagatesMoveError verifies a failing runMouseMove call
// aborts the loop rather than continuing through the remaining waypoints.
func TestDriveCirclePropagatesMoveError(t *testing.T) {
	origMove, origSleep := runMouseMove, sleep
	defer func() { runMouseMove, sleep = origMove, origSleep }()

	calls := 0
	runMouseMove = func(x, y int) error {
		calls++
		return errors.New("boom")
	}
	sleep = func(time.Duration) {}

	if err := driveCircle(0, 0); err == nil {
		t.Fatalf("expected driveCircle to propagate runMouseMove's error")
	}
	if calls != 1 {
		t.Errorf("runMouseMove called %d times, want 1 (loop should abort on first error)", calls)
	}
}
