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

// ---- captureNativeDisplay ----

func TestCaptureNativeDisplayEncodesCapturedImage(t *testing.T) {
	orig, origSize, origGeom := captureScreenImg, screenSize, rootWindowGeometry
	defer func() { captureScreenImg, screenSize, rootWindowGeometry = orig, origSize, origGeom }()
	captureScreenImg = func() (image.Image, error) {
		return image.NewRGBA(image.Rect(0, 0, 4, 4)), nil
	}
	screenSize = func() (int, int) { return 4, 4 }
	rootWindowGeometry = func() (int, int, int, int, bool) { return 0, 0, 0, 0, false }

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

func TestCaptureNativeDisplayPropagatesCaptureError(t *testing.T) {
	orig, origSize, origGeom, origComposite := captureScreenImg, screenSize, rootWindowGeometry, captureViaXComposite
	defer func() {
		captureScreenImg, screenSize, rootWindowGeometry, captureViaXComposite = orig, origSize, origGeom, origComposite
	}()
	captureScreenImg = func() (image.Image, error) {
		return nil, errors.New("boom")
	}
	screenSize = func() (int, int) { return 4, 4 }
	rootWindowGeometry = func() (int, int, int, int, bool) { return 0, 0, 0, 0, false }
	// The XComposite fallback (Revision L) also fails here, so the original
	// robotgo error should still propagate (wrapped) rather than disappear.
	captureViaXComposite = func() (image.Image, error) { return nil, errors.New("no overlay") }

	if _, err := captureNativeDisplay(); err == nil {
		t.Fatalf("expected captureNativeDisplay to propagate robotgo's error")
	}
}

// ---- captureViaXComposite fallback (Revision L) ----

// TestCaptureNativeDisplayFallsBackToXCompositeOnCaptureError verifies
// captureNativeDisplay retries via captureViaXComposite -- and succeeds --
// when the primary robotgo capture fails, rather than giving up immediately
// the way it did before Revision L.
func TestCaptureNativeDisplayFallsBackToXCompositeOnCaptureError(t *testing.T) {
	orig, origSize, origGeom, origComposite := captureScreenImg, screenSize, rootWindowGeometry, captureViaXComposite
	defer func() {
		captureScreenImg, screenSize, rootWindowGeometry, captureViaXComposite = orig, origSize, origGeom, origComposite
	}()
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("boom") }
	screenSize = func() (int, int) { return 4, 4 }
	rootWindowGeometry = func() (int, int, int, int, bool) { return 0, 0, 0, 0, false }
	compositeCalled := false
	captureViaXComposite = func() (image.Image, error) {
		compositeCalled = true
		return image.NewRGBA(image.Rect(0, 0, 4, 4)), nil
	}

	if _, err := captureNativeDisplay(); err != nil {
		t.Fatalf("captureNativeDisplay: %v", err)
	}
	if !compositeCalled {
		t.Errorf("expected captureNativeDisplay to fall back to captureViaXComposite after the primary capture failed")
	}
}

// TestCaptureNativeDisplayPropagatesCombinedErrorWhenBothFail verifies the
// returned error mentions both failures when neither the primary capture
// nor the XComposite fallback succeeds, rather than silently dropping one.
func TestCaptureNativeDisplayPropagatesCombinedErrorWhenBothFail(t *testing.T) {
	orig, origSize, origGeom, origComposite := captureScreenImg, screenSize, rootWindowGeometry, captureViaXComposite
	defer func() {
		captureScreenImg, screenSize, rootWindowGeometry, captureViaXComposite = orig, origSize, origGeom, origComposite
	}()
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("primary boom") }
	screenSize = func() (int, int) { return 4, 4 }
	rootWindowGeometry = func() (int, int, int, int, bool) { return 0, 0, 0, 0, false }
	captureViaXComposite = func() (image.Image, error) { return nil, errors.New("fallback boom") }

	_, err := captureNativeDisplay()
	if err == nil {
		t.Fatalf("expected an error when both the primary capture and the XComposite fallback fail")
	}
	if !strings.Contains(err.Error(), "primary boom") || !strings.Contains(err.Error(), "fallback boom") {
		t.Errorf("error = %q, want it to mention both failures", err.Error())
	}
}

// ---- rootWindowGeometry ----

// TestCaptureNativeDisplayUsesRobotgoCaptureWhenGeometryUnavailable verifies
// captureNativeDisplay falls back to robotgo's own whole-screen
// captureScreenImg -- not captureRegionImg -- when rootWindowGeometry can't
// query the real root window at all (e.g. non-Linux, or no display to
// query), since there's no ground truth to prefer over robotgo's own
// report in that case.
func TestCaptureNativeDisplayUsesRobotgoCaptureWhenGeometryUnavailable(t *testing.T) {
	origCapture, origSize, origRegion, origGeom := captureScreenImg, screenSize, captureRegionImg, rootWindowGeometry
	defer func() {
		captureScreenImg, screenSize, captureRegionImg, rootWindowGeometry = origCapture, origSize, origRegion, origGeom
	}()

	screenSize = func() (int, int) { return 3840, 1080 }
	rootWindowGeometry = func() (int, int, int, int, bool) { return 0, 0, 0, 0, false }
	screenCalled, regionCalled := false, false
	captureScreenImg = func() (image.Image, error) {
		screenCalled = true
		return image.NewRGBA(image.Rect(0, 0, 4, 4)), nil
	}
	captureRegionImg = func(x, y, w, h int) (image.Image, error) {
		regionCalled = true
		return image.NewRGBA(image.Rect(0, 0, w, h)), nil
	}

	if _, err := captureNativeDisplay(); err != nil {
		t.Fatalf("captureNativeDisplay: %v", err)
	}
	if !screenCalled || regionCalled {
		t.Errorf("screenCalled = %v, regionCalled = %v, want true, false", screenCalled, regionCalled)
	}
}

// TestCaptureNativeDisplayUsesRobotgoCaptureWhenGeometryMatches verifies
// captureNativeDisplay still uses robotgo's own whole-screen captureScreenImg
// when the real root window geometry agrees with robotgo's self-reported
// screenSize, rather than needlessly switching paths when there's nothing
// to correct.
func TestCaptureNativeDisplayUsesRobotgoCaptureWhenGeometryMatches(t *testing.T) {
	origCapture, origSize, origRegion, origGeom := captureScreenImg, screenSize, captureRegionImg, rootWindowGeometry
	defer func() {
		captureScreenImg, screenSize, captureRegionImg, rootWindowGeometry = origCapture, origSize, origRegion, origGeom
	}()

	screenSize = func() (int, int) { return 1920, 1080 }
	rootWindowGeometry = func() (int, int, int, int, bool) { return 0, 0, 1920, 1080, true }
	screenCalled, regionCalled := false, false
	captureScreenImg = func() (image.Image, error) {
		screenCalled = true
		return image.NewRGBA(image.Rect(0, 0, 4, 4)), nil
	}
	captureRegionImg = func(x, y, w, h int) (image.Image, error) {
		regionCalled = true
		return image.NewRGBA(image.Rect(0, 0, w, h)), nil
	}

	if _, err := captureNativeDisplay(); err != nil {
		t.Fatalf("captureNativeDisplay: %v", err)
	}
	if !screenCalled || regionCalled {
		t.Errorf("screenCalled = %v, regionCalled = %v, want true, false", screenCalled, regionCalled)
	}
}

// TestCaptureNativeDisplayUsesRealGeometryWhenItDisagrees verifies
// captureNativeDisplay captures the real root window rectangle (via
// captureRegionImg) instead of robotgo's own whole-screen captureScreenImg
// when rootWindowGeometry disagrees with robotgo's self-reported
// screenSize -- see captureNativeDisplay's doc comment (Step1SubstepDPrompt
// Revision F) for why the real rectangle is the one worth trusting.
func TestCaptureNativeDisplayUsesRealGeometryWhenItDisagrees(t *testing.T) {
	origCapture, origSize, origRegion, origGeom := captureScreenImg, screenSize, captureRegionImg, rootWindowGeometry
	defer func() {
		captureScreenImg, screenSize, captureRegionImg, rootWindowGeometry = origCapture, origSize, origRegion, origGeom
	}()

	screenSize = func() (int, int) { return 3840, 1080 }
	rootWindowGeometry = func() (int, int, int, int, bool) { return 0, 0, 1920, 1080, true }
	screenCalled := false
	var gotRegion [4]int
	captureScreenImg = func() (image.Image, error) {
		screenCalled = true
		return image.NewRGBA(image.Rect(0, 0, 4, 4)), nil
	}
	captureRegionImg = func(x, y, w, h int) (image.Image, error) {
		gotRegion = [4]int{x, y, w, h}
		return image.NewRGBA(image.Rect(0, 0, w, h)), nil
	}

	if _, err := captureNativeDisplay(); err != nil {
		t.Fatalf("captureNativeDisplay: %v", err)
	}
	if screenCalled {
		t.Errorf("captureNativeDisplay should not have called captureScreenImg when real geometry disagrees")
	}
	if want := [4]int{0, 0, 1920, 1080}; gotRegion != want {
		t.Errorf("captureRegionImg called with %v, want %v", gotRegion, want)
	}
}

// ---- warmUpRobotDisplay ----

// TestWarmUpRobotDisplayInstallsErrorHandler verifies warmUpRobotDisplay
// installs the (overridable, see installXErrorHandler in robot.go) X error
// handler before querying the screen size, rather than only on the
// Linux-specific path exercised by xerror_linux.go's init().
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

func TestCircleMousePropagatesLocationError(t *testing.T) {
	orig := mouseLocation
	defer func() { mouseLocation = orig }()
	mouseLocation = func() (int, int, error) { return 0, 0, errors.New("boom") }

	if err := circleMouse(); err == nil {
		t.Fatalf("expected an error when mouseLocation fails")
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

	orig, origSize, origGeom, origComposite := captureScreenImg, screenSize, rootWindowGeometry, captureViaXComposite
	defer func() {
		captureScreenImg, screenSize, rootWindowGeometry, captureViaXComposite = orig, origSize, origGeom, origComposite
	}()
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("boom") }
	screenSize = func() (int, int) { return 4, 4 }
	rootWindowGeometry = func() (int, int, int, int, bool) { return 0, 0, 0, 0, false }
	captureViaXComposite = func() (image.Image, error) { return nil, errors.New("no overlay") }

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
