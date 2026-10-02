package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// This file is IANAR's "channels to collect and drive native capture and
// input, as well as browser based" (condocs/InitialRobot.md): a native
// display capture, a pass-through for a browser-side capture, and a
// native-input "circle mouse" action, each driven by its own WebSocket
// message (see main.go's handleClientMsg) and reported back over
// "capture-result" / "circle-mouse-result".

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

// ---- Native display capture ----

// nativeCaptureCommands lists candidate screenshot tools to probe for via
// lookPath, in preference order -- there's no single screenshot binary
// guaranteed present across distros. Each entry's args has a single "%s"
// placeholder for the destination PNG path.
var nativeCaptureCommands = []struct {
	bin  string
	args []string
}{
	{"scrot", []string{"-o", "%s"}},
	{"maim", []string{"%s"}},
	{"import", []string{"-window", "root", "%s"}}, // ImageMagick
	{"gnome-screenshot", []string{"-f", "%s"}},
}

// lookPath is overridden in tests so findNativeCaptureCommand and
// circleMouse can be exercised without depending on what's actually
// installed on the box running the tests.
var lookPath = exec.LookPath

// runCapture is overridden in tests to avoid actually invoking a screenshot
// tool; it writes whatever captureNativeDisplay's caller needs to path and
// reports the error the real exec.Command would.
var runCapture = func(bin string, args []string) error {
	cmd := exec.Command(bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", bin, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// findNativeCaptureCommand returns the first candidate in
// nativeCaptureCommands whose binary is on PATH, or an error listing every
// binary tried if none are.
func findNativeCaptureCommand() (bin string, args []string, err error) {
	tried := make([]string, 0, len(nativeCaptureCommands))
	for _, cand := range nativeCaptureCommands {
		if _, lerr := lookPath(cand.bin); lerr == nil {
			return cand.bin, cand.args, nil
		}
		tried = append(tried, cand.bin)
	}
	return "", nil, fmt.Errorf("no native screenshot tool found on PATH (tried: %s)", strings.Join(tried, ", "))
}

// captureNativeDisplay shells out to whichever screenshot tool
// findNativeCaptureCommand locates to capture the full native display,
// returning the resulting PNG bytes.
func captureNativeDisplay() ([]byte, error) {
	bin, args, err := findNativeCaptureCommand()
	if err != nil {
		return nil, err
	}

	f, err := os.CreateTemp("", "ianar-capture-*.png")
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	resolved := make([]string, len(args))
	for i, a := range args {
		resolved[i] = strings.ReplaceAll(a, "%s", path)
	}

	if err := runCapture(bin, resolved); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading captured image: %w", err)
	}
	return data, nil
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
// tested (see TestCirclePoints) without depending on xdotool actually being
// installed.
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

// runMouseMove is overridden in tests so circleMouse's step-by-step xdotool
// calls can be exercised without moving a real mouse.
var runMouseMove = func(x, y int) error {
	return exec.Command("xdotool", "mousemove", strconv.Itoa(x), strconv.Itoa(y)).Run()
}

// sleep is overridden in tests so circleMouse's full-second drive doesn't
// actually have to take a second.
var sleep = time.Sleep

// currentMouseLocation shells out to `xdotool getmouselocation --shell` and
// parses its X=/Y= output.
func currentMouseLocation() (x, y int, err error) {
	out, err := exec.Command("xdotool", "getmouselocation", "--shell").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("xdotool getmouselocation: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		switch k {
		case "X":
			x = n
		case "Y":
			y = n
		}
	}
	return x, y, nil
}

// circleMouse drives the native pointer through a clockwise circle centered
// on its current position, via xdotool mousemove -- "native control" per
// condocs/InitialRobot.md. Blocks for about circleDur.
func circleMouse() error {
	if _, err := lookPath("xdotool"); err != nil {
		return fmt.Errorf("xdotool not found on PATH: %w", err)
	}
	cx, cy, err := currentMouseLocation()
	if err != nil {
		return err
	}
	return driveCircle(cx, cy)
}

// driveCircle steps the pointer through circlePoints around (cx, cy), one
// runMouseMove call at a time with a sleep between each -- split out from
// circleMouse so the stepping/timing logic is testable (see
// TestDriveCircle) without depending on xdotool/currentMouseLocation.
func driveCircle(cx, cy int) error {
	pts := circlePoints(float64(cx), float64(cy), circleRadius, circleSteps)
	interval := circleDur / time.Duration(circleSteps)
	for _, p := range pts {
		if err := runMouseMove(p[0], p[1]); err != nil {
			return fmt.Errorf("xdotool mousemove: %w", err)
		}
		sleep(interval)
	}
	return nil
}
