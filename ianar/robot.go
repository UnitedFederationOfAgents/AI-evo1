package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"log"
	"math"
	"sync"
	"time"

	"github.com/go-vgo/robotgo"
)

// This file is IANAR's "channels to collect and drive native capture and
// input, as well as browser based" (condocs/InitialRobot.md): a native
// display capture, a pass-through for a browser-side capture, and a
// native-input "circle mouse" action, each driven by its own WebSocket
// message (see main.go's handleClientMsg) and reported back over
// "capture-result" / "circle-mouse-result".
//
// Native capture and native input are both driven in-process via robotgo
// (github.com/go-vgo/robotgo) rather than shelling out to external tools
// (scrot/maim/xdotool/etc.) -- see condocs/initialRobotImpls/Step1Prompt.md
// Revision A.

// CaptureResultMsg is the "capture-result" WebSocket payload reporting a
// completed native or browser capture.
type CaptureResultMsg struct {
	Source   string `json:"source"` // "native" | "browser"
	Success  bool   `json:"success"`
	ImageURL string `json:"image_url,omitempty"` // data: URL
	Error    string `json:"error,omitempty"`
}

// CircleMouseResultMsg is the "circle-mouse-result" WebSocket payload
// reporting whether the circle-mouse action completed.
type CircleMouseResultMsg struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// handleCaptureNative runs captureNativeDisplay and reports the result back
// to the requesting client as a "capture-result" message.
func (s *Server) handleCaptureNative(c *wsClient) {
	data, err := captureNativeDisplay()
	if err != nil {
		s.sendToClient(c, "capture-result", CaptureResultMsg{Source: "native", Success: false, Error: err.Error()})
		return
	}
	s.sendToClient(c, "capture-result", CaptureResultMsg{
		Source:   "native",
		Success:  true,
		ImageURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data),
	})
}

// handleCaptureBrowser hands a browser-side capture (the frontend's own
// getDisplayMedia grab, see App.tsx's captureBrowser) back through the same
// "capture-result" channel handleCaptureNative uses, so the frontend renders
// native and browser captures with one code path.
func (s *Server) handleCaptureBrowser(c *wsClient, dataURL string) {
	if dataURL == "" {
		s.sendToClient(c, "capture-result", CaptureResultMsg{Source: "browser", Success: false, Error: "no image data received"})
		return
	}
	s.sendToClient(c, "capture-result", CaptureResultMsg{Source: "browser", Success: true, ImageURL: dataURL})
}

// handleCircleMouse runs circleMouse and reports the result back to the
// requesting client as a "circle-mouse-result" message.
func (s *Server) handleCircleMouse(c *wsClient) {
	if err := circleMouse(); err != nil {
		s.sendToClient(c, "circle-mouse-result", CircleMouseResultMsg{Success: false, Error: err.Error()})
		return
	}
	s.sendToClient(c, "circle-mouse-result", CircleMouseResultMsg{Success: true})
}

// robotMu serializes every call into robotgo. On Linux robotgo's capture
// and input functions are thin cgo wrappers around Xlib, which is only
// safe for single-threaded access to a given Display connection -- it
// isn't goroutine-safe, and robotgo never calls XInitThreads() to make it
// so. main.go's WebSocket loop hands each incoming message (including
// "capture-native" and "circle-mouse") to its own goroutine
// (`go s.handleClientMsg(c, m)`), so two overlapping requests -- e.g. a
// double-clicked capture button, or a capture while circle-mouse is still
// stepping -- can run robotgo calls concurrently on different OS threads.
// That corrupts Xlib's request sequencing: the symptom is exactly an
// "X_GetImage"/"BadMatch"-style X protocol error with the failing
// request's serial number matching (or trailing just behind) the
// connection's current serial, reported as a crash because Xlib's default
// error handler calls exit() on unexpected protocol errors. Holding this
// lock around every robotgo entry point below ensures only one such call
// is ever in flight at a time, regardless of how many goroutines the
// WebSocket layer spins up.
var robotMu sync.Mutex

// ---- Native display capture ----

// captureScreenImg is overridden in tests so captureNativeDisplay can be
// exercised without robotgo actually needing a real display to grab a
// screenshot from.
var captureScreenImg = func() (image.Image, error) {
	return robotgo.CaptureImg()
}

// screenSize is overridden in tests so captureNativeDisplay's logging
// (added in Step1SubstepDPrompt.md Revision C) can be exercised without
// robotgo needing a real display to query the screen size from.
var screenSize = func() (w, h int) {
	return robotgo.GetScreenSize()
}

// rootWindowGeometry queries the *real* geometry of the X server's root
// window directly via Xlib's XGetWindowAttributes (see xgeometry_linux.go),
// as ground truth to compare against robotgo's self-reported screenSize --
// see captureNativeDisplay's doc comment for why that comparison turned out
// to matter. Overridden in tests, and a no-op reporting "not available" on
// non-Linux builds, following the same pattern as installXErrorHandler.
var rootWindowGeometry = func() (x, y, w, h int, ok bool) {
	return 0, 0, 0, 0, false
}

// captureNativeDisplay uses robotgo (in-process; no external screenshot
// binary required) to capture the full native display, returning the
// resulting PNG bytes.
//
// Revision C's hedge (warming up robotgo's X connection at startup, see
// warmUpRobotDisplay) didn't hold: Revision D's logs show the warm-up
// itself completing cleanly -- logging the same 3840x1080 screen size --
// and the very next real capture-native request still crashing with the
// identical BadMatch/X_GetImage at request serial 7. Revision D's actual
// fix (installXErrorHandler, so a BadMatch logs and returns an ordinary
// error instead of taking the whole process down) held up, but per
// Revision E's prompt capture-native then just *fails* every time instead,
// still at the same request serial.
//
// Revision E's theory -- that a single X_GetImage spanning two
// differently-backed side-by-side outputs (screenSize kept reporting
// 3840x1080, suspiciously exactly two 1920x1080 outputs wide) was the
// trigger -- is now falsified by this revision's evidence (Resource 4,
// "Browser Error 2"): capturing just the *left half* (0,0,1920x1080) as
// its own separate region failed with the identical error. A rectangle
// fully inside one output's own bounds should have been unaffected by a
// dual-output boundary problem, so the failure isn't about which pixels
// are being read.
//
// Resource 5 ("Log Errors 2") has the more useful clue: the whole-screen
// attempt and the left-half retry -- two necessarily distinct GetImage
// requests -- both get reported at the exact same X request serial number
// (7). Serial numbers only ever increase on a given connection, so two
// different requests landing on the same one means each capture call is
// opening its own brand-new X display connection (robotgo re-dials rather
// than reusing one persistent connection) and every such fresh connection
// hits BadMatch on its very first GetImage, regardless of the requested
// rectangle's size or position. That also retroactively explains why
// Revision C's warm-up hedge never had a chance: its connection was never
// the one any real capture request used anyway.
//
// A failure that's unconditional on geometry, but specific to GetImage
// (circleMouse's pointer warps still work fine -- see Revision E's reply),
// points at a mismatch between what robotgo's screenSize() reports and the
// real root window GetImage actually reads from (e.g. screenSize sourced
// from RandR's combined virtual-desktop size, while the literal root
// window Xlib hands GetImage is a different, smaller rectangle) rather
// than anything about this display's dual-output wiring. This revision
// queries that ground truth directly (rootWindowGeometry, above) and, when
// it disagrees with robotgo's self-report, captures the real rectangle
// instead of trusting the mismatched one. If the two numbers actually
// agree, this changes nothing -- but the log line below will at least
// rule the mismatch theory in or out for whoever looks at this next,
// which neither of the last two revisions' hedges (warm-up timing,
// split-screen geometry) were able to do.
func captureNativeDisplay() ([]byte, error) {
	robotMu.Lock()
	w, h := screenSize()
	rx, ry, rw, rh, haveReal := rootWindowGeometry()
	useReal := haveReal && (rx != 0 || ry != 0 || rw != w || rh != h)
	switch {
	case !haveReal:
		log.Printf("robot: capturing native display, robotgo reports screen size %dx%d (couldn't query real root window geometry to compare)", w, h)
	case useReal:
		log.Printf("robot: capturing native display, robotgo reports screen size %dx%d but real root window geometry is (%d,%d) %dx%d -- capturing the real rectangle instead", w, h, rx, ry, rw, rh)
	default:
		log.Printf("robot: capturing native display, robotgo reports screen size %dx%d (matches real root window geometry)", w, h)
	}

	var img image.Image
	var err error
	if useReal {
		img, err = captureRegionImg(rx, ry, rw, rh)
	} else {
		img, err = captureScreenImg()
	}
	robotMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("robotgo capture: %w", err)
	}
	log.Printf("robot: native capture succeeded, image bounds %v", img.Bounds())

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encoding capture as png: %w", err)
	}
	return buf.Bytes(), nil
}

// captureRegionImg is overridden in tests, parallel to captureScreenImg --
// it's the same robotgo entry point (CaptureImg is variadic: no args means
// "whole screen", explicit x/y/w/h means "just this region"), split out
// separately so captureNativeDisplay's real-geometry capture can be
// exercised without robotgo needing a real display.
var captureRegionImg = func(x, y, w, h int) (image.Image, error) {
	return robotgo.CaptureImg(x, y, w, h)
}

// installXErrorHandler overrides Xlib's default X protocol error handler
// -- which calls exit() on an unexpected error such as the
// BadMatch/X_GetImage this step keeps hitting -- with one that just logs
// and lets the process continue (see xerror_linux.go for the real
// handler). It's a no-op on non-Linux builds: this subproject's native
// capture/input is Linux/Xlib-only (see robotMu's doc comment), and
// everything else here follows the same overridable-var pattern
// (captureScreenImg, screenSize, rootWindowGeometry, mouseLocation,
// moveMouse, sleep) so xerror_linux.go's init() can swap in the real
// implementation without an import cycle or a build-tagged call site.
//
// An Xlib error handler is necessarily process-global and async relative
// to the call that triggered it (XErrorEvent doesn't identify which Go
// call provoked it), so this can't turn a BadMatch into a clean, local Go
// `error` the way the rest of robot.go reports failures -- it only stops
// that error from taking the whole process down. Must be installed before
// anything touches the display, which is why it's called from
// warmUpRobotDisplay below rather than per-capture.
var installXErrorHandler = func() {}

// warmUpRobotDisplay is called once from main() at startup, before the
// server accepts any WebSocket clients, to (a) install the non-fatal X
// error handler above before anything else touches the display, and (b)
// force robotgo's underlying X display connection to get used once in a
// controlled spot rather than only on whichever capture-native/circle-mouse
// request a client happens to send first. (Revision D's logs show that
// second part, on its own, doesn't prevent the BadMatch/X_GetImage crash
// this step is chasing -- see captureNativeDisplay's doc comment -- so (a)
// is this revision's actual fix for that; (b) is kept since it's still
// useful for surfacing *other* display-setup problems at a predictable
// point in the startup log.) Logs the reported screen size either way, as
// a startup-time data point to compare against whatever captureNativeDisplay
// logs later.
func warmUpRobotDisplay() {
	robotMu.Lock()
	defer robotMu.Unlock()
	installXErrorHandler()
	w, h := screenSize()
	log.Printf("robot: warmed up robotgo display connection at startup, reported screen size %dx%d", w, h)
}

// ---- Circle-mouse (native input) ----

const (
	circleRadius = 80.0 // px -- "medium sized circle"
	circleSteps  = 36
	circleDur    = 1 * time.Second
)

// circlePoints returns the (x, y) waypoints, inclusive of both endpoints, of
// a clockwise circle of the given radius centered at (cx, cy) in screen
// coordinates (y grows downward). It's a pure function so the geometry is
// tested (see TestCirclePoints) without depending on robotgo actually being
// able to drive the pointer.
func circlePoints(cx, cy, radius float64, steps int) [][2]int {
	pts := make([][2]int, 0, steps+1)
	for i := 0; i <= steps; i++ {
		theta := 2 * math.Pi * float64(i) / float64(steps)
		// Screen y grows downward, so incrementing theta this way traces the
		// circle clockwise as drawn on screen (it would be counterclockwise
		// under the usual math convention, where y grows upward).
		x := cx + radius*math.Cos(theta)
		y := cy + radius*math.Sin(theta)
		pts = append(pts, [2]int{int(math.Round(x)), int(math.Round(y))})
	}
	return pts
}

// mouseLocation is overridden in tests so circleMouse can be exercised
// without robotgo needing a real display to query the pointer's current
// position.
var mouseLocation = func() (x, y int, err error) {
	x, y = robotgo.Location()
	return x, y, nil
}

// moveMouse is overridden in tests so circleMouse's step-by-step calls can
// be exercised without moving a real pointer.
var moveMouse = func(x, y int) error {
	robotgo.Move(x, y)
	return nil
}

// sleep is overridden in tests so circleMouse's full-second drive doesn't
// actually have to take a second.
var sleep = time.Sleep

// circleMouse drives the native pointer through a clockwise circle centered
// on its current position, via robotgo -- "native control" per
// condocs/InitialRobot.md. Blocks for about circleDur.
//
// Held under robotMu for its whole ~circleDur run (not just the individual
// mouseLocation/moveMouse calls) so a capture-native (or another
// circle-mouse) request can't interleave its own robotgo calls partway
// through the drive -- see robotMu's doc comment.
func circleMouse() error {
	robotMu.Lock()
	defer robotMu.Unlock()
	cx, cy, err := mouseLocation()
	if err != nil {
		return err
	}
	return driveCircle(cx, cy)
}

// driveCircle steps the pointer through circlePoints around (cx, cy), one
// moveMouse call at a time with a sleep between each -- split out from
// circleMouse so the stepping/timing logic is testable (see
// TestDriveCircle) without depending on robotgo/mouseLocation.
func driveCircle(cx, cy int) error {
	pts := circlePoints(float64(cx), float64(cy), circleRadius, circleSteps)
	interval := circleDur / time.Duration(circleSteps)
	for _, p := range pts {
		if err := moveMouse(p[0], p[1]); err != nil {
			return fmt.Errorf("robotgo move: %w", err)
		}
		sleep(interval)
	}
	return nil
}
