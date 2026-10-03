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

// captureNativeDisplay uses robotgo (in-process; no external screenshot
// binary required) to capture the full native display, returning the
// resulting PNG bytes.
//
// Revision C's BadMatch/X_GetImage report failed at the exact same request
// serial number (7) as the original, pre-Revision-A crash report, despite
// the two being different X errors -- that repetition across unrelated
// runs points at something tied to the *position* of the request in its X
// display connection's lifetime (e.g. landing right after Xlib's own
// handful of connection-setup requests) rather than to the request's
// arguments. See warmUpRobotDisplay for this revision's hedge against
// that. The log lines below exist so that, if the hedge doesn't fully
// resolve it, the next crash report at least has the screen size robotgo
// thought it was capturing immediately before the X server rejected the
// request -- compare it against the display's actual resolution.
func captureNativeDisplay() ([]byte, error) {
	robotMu.Lock()
	w, h := screenSize()
	log.Printf("robot: capturing native display, robotgo reports screen size %dx%d", w, h)
	img, err := captureScreenImg()
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

// warmUpRobotDisplay is called once from main() at startup, before the
// server accepts any WebSocket clients, to force robotgo's underlying X
// display connection (and whatever setup requests Xlib issues the first
// time that connection is actually used) to happen in a controlled spot
// rather than being deferred to whichever capture-native/circle-mouse
// request a client happens to send first -- see captureNativeDisplay's doc
// comment for why that early-connection-lifetime window is suspected.
// Logs the reported screen size either way, as a startup-time data point
// to compare against whatever captureNativeDisplay logs later.
func warmUpRobotDisplay() {
	robotMu.Lock()
	defer robotMu.Unlock()
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
