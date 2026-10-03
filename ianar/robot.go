package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
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

// captureNativeDisplay uses robotgo (in-process; no external screenshot
// binary required) to capture the full native display, returning the
// resulting PNG bytes.
//
// Revision C's hedge (warming up robotgo's X connection at startup, see
// warmUpRobotDisplay) didn't hold: Revision D's logs show the warm-up
// itself completing cleanly -- logging the same 3840x1080 screen size --
// and the very next real capture-native request still crashing with the
// identical BadMatch/X_GetImage at request serial 7. That rules out "early
// position in the display connection's lifetime" as the cause: the
// warmed-up connection never even reached the failing request.
//
// Revision D's actual fix (installXErrorHandler, so a BadMatch logs and
// returns an ordinary error instead of taking the whole process down) held
// up: per Revision E's prompt (Browser Error 1 / Log Errors 1), the crash
// is gone, but capture-native now just *fails* every time instead, still
// at the same request serial (7) as every report of this bug. Revision E
// also notes circleMouse (native input) works fine on this same display
// connection -- and circleMouse's pointer warps
// (XWarpPointer/XTestFakeMotionEvent) are a fundamentally different kind
// of X request from X_GetImage: they ask the server to resolve a
// coordinate, not read pixel data back. So whatever's wrong is specific to
// reading pixels, and -- since it's utterly deterministic, not an
// occasional race -- specific to *this* request's parameters against the
// server's actual state, not timing. The one fact logged on every report
// of this bug is the screen size: 3840x1080, which is exactly two
// 1920x1080 outputs' width side by side. A single X_GetImage spanning both
// halves of a dual-output layout is a known failure mode when the two
// outputs are backed differently server-side (e.g. hybrid/PRIME-offload
// graphics splitting rendering across GPUs) -- the same combined root
// window that a coordinate-only request like XWarpPointer resolves fine,
// but that a single whole-desktop pixel read gets rejected on.
//
// I can't confirm that's actually this machine's setup -- same build/X
// access sandbox gate as every prior reply on this step -- so rather than
// changing the primary capture path on an unconfirmed theory, this
// revision adds captureSplitScreenFallback as a fallback: only once the
// whole-screen captureScreenImg call has already failed, and only when the
// reported screen size looks plausibly like two side-by-side outputs (see
// shouldTrySplitFallback), it retries as two separate same-height regions
// and composites them, so a genuinely single-monitor or
// differently-shaped failure isn't masked by a pointless retry. If the
// fallback also fails, both errors are returned together.
func captureNativeDisplay() ([]byte, error) {
	robotMu.Lock()
	w, h := screenSize()
	log.Printf("robot: capturing native display, robotgo reports screen size %dx%d", w, h)
	img, err := captureScreenImg()
	if err != nil && shouldTrySplitFallback(w, h) {
		log.Printf("robot: whole-screen capture failed (%v); screen reports as %dx%d (wide enough to be two side-by-side outputs), retrying as two separate regions", err, w, h)
		if fbImg, fbErr := captureSplitScreenFallback(w, h); fbErr == nil {
			img, err = fbImg, nil
			log.Printf("robot: split-screen fallback capture succeeded")
		} else {
			err = fmt.Errorf("%w (split-screen fallback also failed: %v)", err, fbErr)
		}
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
// separately so captureSplitScreenFallback's two region calls can be
// exercised without robotgo needing a real display.
var captureRegionImg = func(x, y, w, h int) (image.Image, error) {
	return robotgo.CaptureImg(x, y, w, h)
}

// shouldTrySplitFallback reports whether a screen of the given reported
// size looks enough like two side-by-side outputs (at least twice as wide
// as it is tall) to be worth retrying as captureSplitScreenFallback --
// see captureNativeDisplay's doc comment -- rather than retrying (and
// potentially masking a differently-caused failure) on every whole-screen
// capture error regardless of shape.
func shouldTrySplitFallback(w, h int) bool {
	return h > 0 && w >= 2*h
}

// captureSplitScreenFallback re-captures the screen as two side-by-side
// halves instead of one whole-desktop region, then composites them back
// into a single image the size screenSize() reported -- see
// captureNativeDisplay's doc comment for why a split capture might survive
// where the single whole-screen one didn't.
func captureSplitScreenFallback(w, h int) (image.Image, error) {
	leftW := w / 2
	rightW := w - leftW
	left, err := captureRegionImg(0, 0, leftW, h)
	if err != nil {
		return nil, fmt.Errorf("left half (0,0,%dx%d): %w", leftW, h, err)
	}
	right, err := captureRegionImg(leftW, 0, rightW, h)
	if err != nil {
		return nil, fmt.Errorf("right half (%d,0,%dx%d): %w", leftW, rightW, h, err)
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(out, left.Bounds(), left, image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(leftW, 0, w, h), right, right.Bounds().Min, draw.Src)
	return out, nil
}

// installXErrorHandler overrides Xlib's default X protocol error handler
// -- which calls exit() on an unexpected error such as the
// BadMatch/X_GetImage this step keeps hitting -- with one that just logs
// and lets the process continue (see xerror_linux.go for the real
// handler). It's a no-op on non-Linux builds: this subproject's native
// capture/input is Linux/Xlib-only (see robotMu's doc comment), and
// everything else here follows the same overridable-var pattern
// (captureScreenImg, screenSize, mouseLocation, moveMouse, sleep) so
// xerror_linux.go's init() can swap in the real implementation without an
// import cycle or a build-tagged call site.
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
