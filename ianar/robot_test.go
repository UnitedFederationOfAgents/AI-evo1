package main

import (
	"encoding/json"
	"errors"
	"image"
	"math"
	"strings"
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

// litImage returns a w x h image with content in it -- one hasVisibleContent
// reports true for. A bare image.NewRGBA is all black, which
// captureNativeDisplay treats as "no screen content".
func litImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 0x40, 0x80, 0xc0, 0xff
	}
	return img
}

// stubCapturePaths points every capture path captureNativeDisplay can take at
// a stub that fails, and restores the real ones when the test ends. Failing
// by default keeps a test that forgot to stub captureViaPortal from opening a
// real session-bus connection and waiting on a desktop permission dialog.
func stubCapturePaths(t *testing.T) {
	t.Helper()
	origScreen, origSize, origPortal := captureScreenImg, screenSize, captureViaPortal
	t.Cleanup(func() {
		captureScreenImg, screenSize, captureViaPortal = origScreen, origSize, origPortal
	})
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("captureScreenImg not stubbed") }
	screenSize = func() (int, int) { return 4, 4 }
	captureViaPortal = func() (image.Image, error) { return nil, errors.New("captureViaPortal not stubbed") }
}

// ---- captureNativeDisplay ----

func TestCaptureNativeDisplayEncodesCapturedImage(t *testing.T) {
	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return litImage(4, 4), nil }

	data, err := captureNativeDisplay()
	if err != nil {
		t.Fatalf("captureNativeDisplay: %v", err)
	}
	// A valid PNG starts with the 8-byte PNG signature.
	pngSig := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if len(data) < len(pngSig) {
		t.Fatalf("captured data too short to be a PNG: %d bytes", len(data))
	}
	for i, b := range pngSig {
		if data[i] != b {
			t.Fatalf("captured data isn't a PNG (byte %d = %#x, want %#x)", i, data[i], b)
		}
	}
}

// TestCaptureNativeDisplayFallsBackToPortal verifies the D-Bus compositor
// capture is used when robotgo's capture fails.
func TestCaptureNativeDisplayFallsBackToPortal(t *testing.T) {
	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("boom") }
	portalCalled := false
	captureViaPortal = func() (image.Image, error) {
		portalCalled = true
		return litImage(4, 4), nil
	}

	if _, err := captureNativeDisplay(); err != nil {
		t.Fatalf("captureNativeDisplay: %v", err)
	}
	if !portalCalled {
		t.Errorf("expected captureNativeDisplay to fall back to captureViaPortal")
	}
}

// TestCaptureNativeDisplaySkipsBlankFrames verifies a path that succeeds but
// hands back an entirely black frame does not end the search.
func TestCaptureNativeDisplaySkipsBlankFrames(t *testing.T) {
	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return image.NewRGBA(image.Rect(0, 0, 4, 4)), nil }
	portalCalled := false
	captureViaPortal = func() (image.Image, error) {
		portalCalled = true
		return litImage(4, 4), nil
	}

	if _, err := captureNativeDisplay(); err != nil {
		t.Fatalf("captureNativeDisplay: %v", err)
	}
	if !portalCalled {
		t.Errorf("expected captureNativeDisplay to move past robotgo's black frame to captureViaPortal")
	}
}

// TestCaptureNativeDisplayReturnsBlankFrameWhenNothingHasContent verifies an
// all-black capture is still returned (not turned into an error) when no path
// does better, since a genuinely blanked or locked screen really is black.
func TestCaptureNativeDisplayReturnsBlankFrameWhenNothingHasContent(t *testing.T) {
	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return image.NewRGBA(image.Rect(0, 0, 4, 4)), nil }
	captureViaPortal = func() (image.Image, error) { return nil, errors.New("no portal") }

	if _, err := captureNativeDisplay(); err != nil {
		t.Fatalf("captureNativeDisplay should return the blank frame rather than an error: %v", err)
	}
}

// TestCaptureNativeDisplayReportsEveryFailure verifies the returned error
// mentions each path's failure when no path succeeds.
func TestCaptureNativeDisplayReportsEveryFailure(t *testing.T) {
	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("primary boom") }
	captureViaPortal = func() (image.Image, error) { return nil, errors.New("portal boom") }

	_, err := captureNativeDisplay()
	if err == nil {
		t.Fatalf("expected an error when every capture path fails")
	}
	for _, want := range []string{"primary boom", "portal boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err.Error(), want)
		}
	}
}

// ---- warmUpRobotDisplay ----

func TestWarmUpRobotDisplayInstallsErrorHandler(t *testing.T) {
	origInstall, origSize := installXErrorHandler, screenSize
	defer func() { installXErrorHandler, screenSize = origInstall, origSize }()

	installed := false
	installXErrorHandler = func() { installed = true }
	screenSize = func() (int, int) { return 4, 4 }

	warmUpRobotDisplay()

	if !installed {
		t.Fatalf("warmUpRobotDisplay did not call installXErrorHandler")
	}
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

// stubCompositorInput makes moveByViaCompositor report unavailable (so
// circleMouse takes the robotgo path) and restores it when the test ends.
// Keeps a test from opening a real session-bus connection and moving the
// desktop's actual pointer.
func stubCompositorInput(t *testing.T) {
	t.Helper()
	orig := moveByViaCompositor
	t.Cleanup(func() { moveByViaCompositor = orig })
	moveByViaCompositor = func(func(func(dx, dy float64) error) error) error {
		return errCompositorInputUnavailable
	}
}

func TestCircleMousePropagatesLocationError(t *testing.T) {
	stubCompositorInput(t)
	orig := mouseLocation
	defer func() { mouseLocation = orig }()
	mouseLocation = func() (int, int, error) { return 0, 0, errors.New("boom") }

	if _, err := circleMouse(); err == nil {
		t.Fatalf("expected an error when mouseLocation fails")
	}
}

// TestCircleMousePrefersCompositor verifies robotgo isn't touched when the
// compositor path works.
func TestCircleMousePrefersCompositor(t *testing.T) {
	origComp, origLoc, origSleep := moveByViaCompositor, mouseLocation, sleep
	defer func() { moveByViaCompositor, mouseLocation, sleep = origComp, origLoc, origSleep }()
	sleep = func(time.Duration) {}
	moveByViaCompositor = func(drive func(func(dx, dy float64) error) error) error {
		return drive(func(dx, dy float64) error { return nil })
	}
	mouseLocation = func() (int, int, error) {
		t.Fatalf("robotgo path used even though the compositor path succeeded")
		return 0, 0, nil
	}

	via, err := circleMouse()
	if err != nil {
		t.Fatalf("circleMouse: %v", err)
	}
	if !strings.Contains(via, "compositor") {
		t.Errorf("via = %q, want the compositor path", via)
	}
}

// TestCircleMouseDoesNotFallBackMidDrive verifies a compositor failure after
// motion has started is reported rather than retried through robotgo.
func TestCircleMouseDoesNotFallBackMidDrive(t *testing.T) {
	origComp, origLoc := moveByViaCompositor, mouseLocation
	defer func() { moveByViaCompositor, mouseLocation = origComp, origLoc }()
	moveByViaCompositor = func(func(func(dx, dy float64) error) error) error {
		return errors.New("NotifyPointerMotionRelative: boom")
	}
	mouseLocation = func() (int, int, error) {
		t.Fatalf("robotgo path used after the compositor drive had started")
		return 0, 0, nil
	}

	if _, err := circleMouse(); err == nil {
		t.Fatalf("expected the compositor drive's error")
	}
}

// TestDriveCircleRelative verifies the relative deltas add up to the same
// waypoints driveCircle visits, relative to the starting position.
func TestDriveCircleRelative(t *testing.T) {
	origSleep := sleep
	defer func() { sleep = origSleep }()
	sleep = func(time.Duration) {}

	var x, y float64
	var visited [][2]int
	err := driveCircleRelative(func(dx, dy float64) error {
		x, y = x+dx, y+dy
		visited = append(visited, [2]int{int(x), int(y)})
		return nil
	})
	if err != nil {
		t.Fatalf("driveCircleRelative: %v", err)
	}
	want := circlePoints(0, 0, circleRadius, circleSteps)
	if len(visited) != len(want) {
		t.Fatalf("len(visited) = %d, want %d", len(visited), len(want))
	}
	for i, p := range want {
		if visited[i] != p {
			t.Errorf("visited[%d] = %v, want %v", i, visited[i], p)
		}
	}
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

func TestHandleCaptureNativeReportsError(t *testing.T) {
	s := newServer()
	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}

	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("boom") }
	captureViaPortal = func() (image.Image, error) { return nil, errors.New("no portal") }

	s.handleCaptureNative(c)
	var p CaptureResultMsg
	decodeSent(t, c, "capture-result", &p)
	if p.Success || p.Source != "native" || p.Error == "" {
		t.Errorf("unexpected result: %+v", p)
	}
}

func TestHandleCircleMouseReportsError(t *testing.T) {
	s := newServer()
	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}

	stubCompositorInput(t)
	orig := mouseLocation
	defer func() { mouseLocation = orig }()
	mouseLocation = func() (int, int, error) { return 0, 0, errors.New("boom") }

	s.handleCircleMouse(c)
	var p CircleMouseResultMsg
	decodeSent(t, c, "circle-mouse-result", &p)
	if p.Success || p.Error == "" {
		t.Errorf("unexpected result: %+v", p)
	}
}

// TestDriveCircle verifies the stepping loop visits every circlePoints
// waypoint via moveMouse, sleeping circleDur/circleSteps between each --
// split out from circleMouse (see driveCircle) specifically so this is
// testable without robotgo or a real second of wall-clock time.
func TestDriveCircle(t *testing.T) {
	origMove, origSleep := moveMouse, sleep
	defer func() { moveMouse, sleep = origMove, origSleep }()

	var moves [][2]int
	var slept []time.Duration
	moveMouse = func(x, y int) error {
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

// TestDriveCirclePropagatesMoveError verifies a failing moveMouse call
// aborts the loop rather than continuing through the remaining waypoints.
func TestDriveCirclePropagatesMoveError(t *testing.T) {
	origMove, origSleep := moveMouse, sleep
	defer func() { moveMouse, sleep = origMove, origSleep }()

	calls := 0
	moveMouse = func(x, y int) error {
		calls++
		return errors.New("boom")
	}
	sleep = func(time.Duration) {}

	if err := driveCircle(0, 0); err == nil {
		t.Fatalf("expected driveCircle to propagate moveMouse's error")
	}
	if calls != 1 {
		t.Errorf("moveMouse called %d times, want 1 (loop should abort on first error)", calls)
	}
}
