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

// litImage returns a w x h image with content in it -- i.e. one that
// hasVisibleContent reports true for.
//
// Every capture stub in this file has to return one of these rather than a
// bare image.NewRGBA, because a freshly allocated RGBA is all zeroes, which is
// opaque black, which captureNativeDisplay now treats as "this path returned
// no screen content" and moves past. That is the whole point of the check (an
// all-black frame was repeatedly mistaken for a working capture on the real
// host), so the tests have to model a capture that actually captured something.
func litImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 0x40, 0x80, 0xc0, 0xff
	}
	return img
}

// stubCapturePaths points every capture path captureNativeDisplay can take at
// a stub that fails, and restores the real ones when the test ends. Tests then
// override just the paths they're about and rely on this for the rest.
//
// Failing by default is deliberate: captureViaPortal's real implementation
// opens a session-bus connection and can sit for portalResponseTimeout waiting
// on a desktop permission dialog, so a test that forgot to stub it would hang
// the suite on any Linux box with a running desktop. Defaulting it to an error
// makes forgetting produce a fast, obvious failure instead.
func stubCapturePaths(t *testing.T) {
	t.Helper()
	origScreen, origRegion, origSize := captureScreenImg, captureRegionImg, screenSize
	origGeom, origComposite, origPortal := rootWindowGeometry, captureViaXComposite, captureViaPortal
	t.Cleanup(func() {
		captureScreenImg, captureRegionImg, screenSize = origScreen, origRegion, origSize
		rootWindowGeometry, captureViaXComposite, captureViaPortal = origGeom, origComposite, origPortal
	})
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("captureScreenImg not stubbed") }
	captureRegionImg = func(x, y, w, h int) (image.Image, error) {
		return nil, errors.New("captureRegionImg not stubbed")
	}
	screenSize = func() (int, int) { return 4, 4 }
	rootWindowGeometry = func() (int, int, int, int, bool) { return 0, 0, 0, 0, false }
	captureViaXComposite = func() (image.Image, error) { return nil, errors.New("captureViaXComposite not stubbed") }
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

func TestCaptureNativeDisplayPropagatesCaptureError(t *testing.T) {
	stubCapturePaths(t)
	// Every fallback fails too, so the original robotgo error should still
	// propagate (wrapped) rather than disappear.
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("boom") }
	captureViaPortal = func() (image.Image, error) { return nil, errors.New("no portal") }
	captureViaXComposite = func() (image.Image, error) { return nil, errors.New("no overlay") }

	if _, err := captureNativeDisplay(); err == nil {
		t.Fatalf("expected captureNativeDisplay to propagate robotgo's error")
	}
}

// ---- capture fallbacks ----

// TestCaptureNativeDisplayFallsBackToXCompositeOnCaptureError verifies
// captureNativeDisplay keeps going down the fallback chain -- and succeeds --
// when the primary robotgo capture fails, rather than giving up immediately
// the way it did before Revision L.
func TestCaptureNativeDisplayFallsBackToXCompositeOnCaptureError(t *testing.T) {
	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("boom") }
	captureViaPortal = func() (image.Image, error) { return nil, errors.New("no portal") }
	compositeCalled := false
	captureViaXComposite = func() (image.Image, error) {
		compositeCalled = true
		return litImage(4, 4), nil
	}

	if _, err := captureNativeDisplay(); err != nil {
		t.Fatalf("captureNativeDisplay: %v", err)
	}
	if !compositeCalled {
		t.Errorf("expected captureNativeDisplay to fall back to captureViaXComposite after the primary capture failed")
	}
}

// TestCaptureNativeDisplayPrefersPortalOverXComposite verifies the D-Bus
// compositor capture is tried before the X11 pixmap copy, and that reaching a
// working one stops the chain. The order matters on this subproject's own
// host: X11 there cannot see the desktop at all, and the pixmap copy's
// "success" is undefined server memory (see captureViaXComposite's doc
// comment), so a run that consulted it first would return garbage in
// preference to a real screenshot.
func TestCaptureNativeDisplayPrefersPortalOverXComposite(t *testing.T) {
	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("boom") }
	portalCalled, compositeCalled := false, false
	captureViaPortal = func() (image.Image, error) {
		portalCalled = true
		return litImage(4, 4), nil
	}
	captureViaXComposite = func() (image.Image, error) {
		compositeCalled = true
		return litImage(4, 4), nil
	}

	if _, err := captureNativeDisplay(); err != nil {
		t.Fatalf("captureNativeDisplay: %v", err)
	}
	if !portalCalled {
		t.Errorf("expected captureNativeDisplay to try captureViaPortal after the primary capture failed")
	}
	if compositeCalled {
		t.Errorf("captureViaXComposite should not have been reached once captureViaPortal returned content")
	}
}

// TestCaptureNativeDisplaySkipsBlankFrames verifies a path that succeeds but
// hands back an entirely black frame does not end the search, and that a later
// path with real content wins.
//
// This is the check whose absence let several revisions of this step conclude
// a capture path worked when it was returning nothing: an all-black PNG is
// structurally valid and looks like a success at every layer above this one.
func TestCaptureNativeDisplaySkipsBlankFrames(t *testing.T) {
	stubCapturePaths(t)
	// A bare NewRGBA -- all zeroes, i.e. opaque black -- is exactly the frame
	// robotgo/scrot/the overlay each produced on the real host.
	captureScreenImg = func() (image.Image, error) { return image.NewRGBA(image.Rect(0, 0, 4, 4)), nil }
	captureViaPortal = func() (image.Image, error) { return litImage(4, 4), nil }

	data, err := captureNativeDisplay()
	if err != nil {
		t.Fatalf("captureNativeDisplay: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("captureNativeDisplay returned no data")
	}
}

// TestCaptureNativeDisplayReturnsBlankFrameWhenNothingHasContent verifies an
// all-black capture is still returned (not turned into an error) when no path
// does better, since a genuinely blanked or locked screen really is black and
// shouldn't be a hard failure.
func TestCaptureNativeDisplayReturnsBlankFrameWhenNothingHasContent(t *testing.T) {
	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return image.NewRGBA(image.Rect(0, 0, 4, 4)), nil }
	captureViaPortal = func() (image.Image, error) { return nil, errors.New("no portal") }
	captureViaXComposite = func() (image.Image, error) { return nil, errors.New("no overlay") }

	if _, err := captureNativeDisplay(); err != nil {
		t.Fatalf("captureNativeDisplay should return the blank frame rather than an error: %v", err)
	}
}

// TestCaptureNativeDisplayPropagatesCombinedErrorWhenBothFail verifies the
// returned error mentions every failure when no path succeeds, rather than
// silently dropping all but one.
func TestCaptureNativeDisplayPropagatesCombinedErrorWhenBothFail(t *testing.T) {
	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("primary boom") }
	captureViaPortal = func() (image.Image, error) { return nil, errors.New("portal boom") }
	captureViaXComposite = func() (image.Image, error) { return nil, errors.New("fallback boom") }

	_, err := captureNativeDisplay()
	if err == nil {
		t.Fatalf("expected an error when every capture path fails")
	}
	for _, want := range []string{"primary boom", "portal boom", "fallback boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err.Error(), want)
		}
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
	stubCapturePaths(t)
	screenSize = func() (int, int) { return 3840, 1080 }
	rootWindowGeometry = func() (int, int, int, int, bool) { return 0, 0, 0, 0, false }
	screenCalled, regionCalled := false, false
	captureScreenImg = func() (image.Image, error) {
		screenCalled = true
		return litImage(4, 4), nil
	}
	captureRegionImg = func(x, y, w, h int) (image.Image, error) {
		regionCalled = true
		return litImage(w, h), nil
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
	stubCapturePaths(t)
	screenSize = func() (int, int) { return 1920, 1080 }
	rootWindowGeometry = func() (int, int, int, int, bool) { return 0, 0, 1920, 1080, true }
	screenCalled, regionCalled := false, false
	captureScreenImg = func() (image.Image, error) {
		screenCalled = true
		return litImage(4, 4), nil
	}
	captureRegionImg = func(x, y, w, h int) (image.Image, error) {
		regionCalled = true
		return litImage(w, h), nil
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
	stubCapturePaths(t)
	screenSize = func() (int, int) { return 3840, 1080 }
	rootWindowGeometry = func() (int, int, int, int, bool) { return 0, 0, 1920, 1080, true }
	screenCalled := false
	var gotRegion [4]int
	captureScreenImg = func() (image.Image, error) {
		screenCalled = true
		return litImage(4, 4), nil
	}
	captureRegionImg = func(x, y, w, h int) (image.Image, error) {
		gotRegion = [4]int{x, y, w, h}
		return litImage(w, h), nil
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

	stubCapturePaths(t)
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("boom") }
	captureViaPortal = func() (image.Image, error) { return nil, errors.New("no portal") }
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
