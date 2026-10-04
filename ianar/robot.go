package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/go-vgo/robotgo"
)

// This file is IANAR's "channels to collect and drive native capture and
// input, as well as browser based" (condocs/InitialRobot.md): a native
// display capture, a pass-through for a browser-side capture, and a
// native-input "circle mouse" action, each driven by its own WebSocket
// message (see main.go's handleClientMsg) and reported back over
// "capture-result" / "circle-mouse-result". The "Native Clip" recording
// lives in clip.go.
//
// Native input and capture both go through robotgo
// (github.com/go-vgo/robotgo). On a Wayland session, where DISPLAY is
// rootless XWayland and holds no desktop pixels, native capture falls back
// to asking the compositor over D-Bus (see portalcapture_linux.go). Pointer
// input goes to the compositor first for the same reason (see
// remotedesktop_linux.go), with robotgo as the fallback.

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
	Via     string `json:"via,omitempty"` // which input path drove the pointer
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
	via, err := circleMouse()
	if err != nil {
		s.sendToClient(c, "circle-mouse-result", CircleMouseResultMsg{Success: false, Via: via, Error: err.Error()})
		return
	}
	s.sendToClient(c, "circle-mouse-result", CircleMouseResultMsg{Success: true, Via: via})
}

// robotMu serializes every call into robotgo. On Linux robotgo's capture
// and input functions are thin cgo wrappers around Xlib, which isn't
// goroutine-safe (robotgo never calls XInitThreads()), and main.go hands
// each incoming WebSocket message to its own goroutine -- so overlapping
// requests would otherwise race on the X connection.
var robotMu sync.Mutex

// ---- Native display capture ----

// captureViaPortal asks the compositor for the screen over D-Bus, for
// Wayland sessions where robotgo's X11 capture can't see the desktop.
// Overridden on Linux by portalcapture_linux.go; a no-op elsewhere and
// overridable in tests.
var captureViaPortal = func() (image.Image, error) {
	return nil, fmt.Errorf("D-Bus compositor capture not available on this platform")
}

// hasVisibleContent reports whether an image contains any non-black pixel.
// On XWayland robotgo can hand back a structurally valid but entirely black
// frame, so captureNativeDisplay uses this to decide whether to move on to
// the next capture path. Samples on a grid rather than walking every pixel.
func hasVisibleContent(img image.Image) bool {
	if img == nil {
		return false
	}
	b := img.Bounds()
	if b.Empty() {
		return false
	}
	const samplesPerAxis = 256
	stepX := max(1, b.Dx()/samplesPerAxis)
	stepY := max(1, b.Dy()/samplesPerAxis)
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			if r, g, bl, _ := img.At(x, y).RGBA(); r|g|bl != 0 {
				return true
			}
		}
	}
	return false
}

// captureScreenImg is overridden in tests so captureNativeDisplay can be
// exercised without robotgo needing a real display to grab a screenshot
// from.
var captureScreenImg = func() (image.Image, error) {
	return robotgo.CaptureImg()
}

// screenSize is overridden in tests so captureNativeDisplay can be
// exercised without robotgo needing a real display to query.
var screenSize = func() (w, h int) {
	return robotgo.GetScreenSize()
}

// captureAttempt is one native frame-grab path, named for logs and errors.
type captureAttempt struct {
	name string
	grab func() (image.Image, error)
}

// nativeCaptureAttempts lists the native frame-grab paths in preference
// order: robotgo first, then the D-Bus compositor screenshot.
func nativeCaptureAttempts() []captureAttempt {
	return []captureAttempt{
		{"robotgo", captureScreenImg},
		{"D-Bus compositor screenshot", captureViaPortal},
	}
}

// grabNativeFrame tries each attempt in order under robotMu, stopping at the
// first frame with visible content, and reports which attempt produced it.
// If no attempt produces content but one returned a black frame (e.g. a
// genuinely blank or locked screen), that frame is returned with a warning
// rather than an error.
func grabNativeFrame(attempts []captureAttempt) (image.Image, string, error) {
	robotMu.Lock()
	var img, blank image.Image
	var from, blankFrom string
	var failures []string
	for _, a := range attempts {
		got, err := a.grab()
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", a.name, err))
			continue
		}
		if !hasVisibleContent(got) {
			failures = append(failures, fmt.Sprintf("%s: every pixel is black", a.name))
			if blank == nil {
				blank, blankFrom = got, a.name
			}
			continue
		}
		img, from = got, a.name
		break
	}
	robotMu.Unlock()

	switch {
	case img != nil:
		return img, from, nil
	case blank != nil:
		log.Printf("robot: WARNING -- no capture path returned screen content; returning the black frame from %s (%s)",
			blankFrom, strings.Join(failures, "; "))
		return blank, blankFrom, nil
	default:
		return nil, "", fmt.Errorf("every capture path failed: %s", strings.Join(failures, "; "))
	}
}

// captureNativeDisplay captures the full native display and returns it as
// PNG bytes, via the first of nativeCaptureAttempts to produce content.
func captureNativeDisplay() ([]byte, error) {
	robotMu.Lock()
	w, h := screenSize()
	robotMu.Unlock()
	log.Printf("robot: capturing native display, robotgo reports screen size %dx%d", w, h)

	img, _, err := grabNativeFrame(nativeCaptureAttempts())
	if err != nil {
		return nil, fmt.Errorf("native capture: %w", err)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encoding capture as png: %w", err)
	}
	return buf.Bytes(), nil
}

// installXErrorHandler replaces Xlib's default error handler -- which calls
// exit() on any protocol error, e.g. the BadMatch robotgo's XGetImage gets
// on XWayland -- with one that logs and continues (see xerror_linux.go). A
// no-op on non-Linux builds.
var installXErrorHandler = func() {}

// warmUpRobotDisplay is called once from main() at startup, before the
// server accepts any WebSocket clients, so the non-fatal X error handler is
// in place before anything touches the display.
func warmUpRobotDisplay() {
	robotMu.Lock()
	defer robotMu.Unlock()
	installXErrorHandler()
	w, h := screenSize()
	log.Printf("robot: robotgo display ready, reported screen size %dx%d", w, h)
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

// errCompositorInputUnavailable marks a moveByViaCompositor failure that
// happened before any pointer motion was sent, so circleMouse can safely fall
// back to robotgo instead.
var errCompositorInputUnavailable = errors.New("compositor pointer input unavailable")

// moveByViaCompositor opens a compositor input session and runs drive with a
// relative-motion function bound to it. Overridden on Linux by
// remotedesktop_linux.go; unavailable elsewhere and overridable in tests.
var moveByViaCompositor = func(drive func(moveBy func(dx, dy float64) error) error) error {
	return errCompositorInputUnavailable
}

// circleMouse drives the native pointer through a clockwise circle centered
// on its current position -- "native control" per condocs/InitialRobot.md --
// and reports which input path it used. Blocks for about circleDur.
//
// The compositor path is tried first: on a Wayland session robotgo's Move
// only warps XWayland's pointer, which "succeeds" without the real cursor
// moving. robotgo is the fallback for X11 sessions and non-GNOME desktops.
//
// Held under robotMu for its whole ~circleDur run so a capture-native (or
// another circle-mouse) request can't interleave its own robotgo calls
// partway through the drive -- see robotMu's doc comment.
func circleMouse() (via string, err error) {
	robotMu.Lock()
	defer robotMu.Unlock()

	err = moveByViaCompositor(driveCircleRelative)
	if err == nil {
		return "compositor (org.gnome.Mutter.RemoteDesktop)", nil
	}
	if !errors.Is(err, errCompositorInputUnavailable) {
		return "compositor (org.gnome.Mutter.RemoteDesktop)", err
	}
	log.Printf("robot: %v; driving the pointer with robotgo instead", err)

	cx, cy, err := mouseLocation()
	if err != nil {
		return "robotgo", err
	}
	return "robotgo", driveCircle(cx, cy)
}

// driveCircleRelative traces the same circle as driveCircle, but as relative
// motions from wherever the pointer currently is (treated as the center), for
// input paths that can't address absolute screen coordinates.
func driveCircleRelative(moveBy func(dx, dy float64) error) error {
	pts := circlePoints(0, 0, circleRadius, circleSteps)
	interval := circleDur / time.Duration(circleSteps)
	px, py := 0, 0
	for _, p := range pts {
		if err := moveBy(float64(p[0]-px), float64(p[1]-py)); err != nil {
			return fmt.Errorf("compositor move: %w", err)
		}
		px, py = p[0], p[1]
		sleep(interval)
	}
	return nil
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
