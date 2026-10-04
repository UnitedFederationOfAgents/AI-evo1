package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"log"
	"math"
	"os"
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
// "capture-result" / "circle-mouse-result".
//
// Native *input* (circleMouse) is driven in-process via robotgo
// (github.com/go-vgo/robotgo) -- see condocs/initialRobotImpls/Step1Prompt.md
// Revision A. Native *capture* is too, in-process, via robotgo.CaptureImg
// (an Xlib XGetImage wrapper) -- Revision K had replaced that with shelling
// out to the external `scrot` binary after Revisions A/C/D/E/F/G chased an
// in-process-only fix through goroutine-safety, warm-up timing,
// split-screen geometry, and root-window geometry theories without success,
// but Revision L asked to go back to robotgo rather than depend on an
// external binary. Dropping `scrot` cost nothing in the end: it exits 0 on
// this host but writes an all-black PNG. See captureViaXComposite's doc
// comment below for the in-process fallback that does return real pixels.

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

// captureViaXComposite is the native-X11 fallback tried when a plain
// robotgo.CaptureImg() call fails -- see captureNativeDisplay's doc comment
// for the full chain of theories this follows on from. Declared as a no-op
// returning an error here so non-Linux builds degrade cleanly; overridden
// with the real Xlib implementation by xcomposite_linux.go's init(),
// following the same overridable-var pattern as installXErrorHandler/
// rootWindowGeometry.
//
// The name is historical. Revision L introduced this to read the X Composite
// extension's *overlay window*, on the theory that a compositing manager
// paints the composited result there and not onto the bare root. That theory
// is now settled, and it was wrong in its second half: ianar/cmd/xcompositediag
// shows the overlay exists but is IsUnmapped, and GetImage against it fails
// with the same BadMatch as the root.
//
// A later out-of-condoc iteration (between Step1SubstepDPrompt.md's Revisions
// N and O, rolled back in by Revision O) then reported that the indirection
// rather than the drawable was
// the answer -- XCopyArea the root into a pixmap we own, then GetImage *that*
// -- on the strength of a PNG that was 93.55% non-black and "visually
// confirmed". **That was wrong, and it is the most dangerous wrong answer this
// step has produced**, because unlike every earlier failure it looked like a
// success all the way to the UI. Opening the PNG at full size shows the same
// window content repeated twice at a nonsense offset of roughly (1920, 540),
// wrapping at the image edge: the top-left terminal panel reappears
// pixel-identically at bottom-right, scroll position and all, as does the
// YouTube player bar. Real desktops do not contain shifted copies of
// themselves, so the frame is not a screenshot of anything.
//
// The mechanism is now understood and makes that inevitable. A freshly created
// pixmap's contents are undefined by the X protocol, and the root window on
// rootless XWayland has no backing storage at all, so XCopyArea from it leaves
// the destination holding whatever was already in server memory. On XWayland
// that memory is recycled X client window buffers -- which is exactly why the
// garbage looks like real terminals: those terminals *are* XWayland clients,
// just reassembled at meaningless offsets. The black pre-fill in
// xcomposite_linux.go was meant to make a no-op copy detectable, but a copy
// that succeeds and transfers undefined memory overwrites the pre-fill with
// plausible-looking junk and defeats the check entirely.
//
// So this path is no longer trusted on its own. It now first probes whether a
// plain GetImage against the root succeeds; if that fails -- as it does on this
// host -- the root has no readable content, anything copied out of it is
// undefined, and the capture is refused rather than returned. See
// captureViaPortal below for the path that actually works here.
//
// Why a fallback is needed at all, rather than fixing robotgo's call:
// Revision K's diagnostic (ianar/cmd/xgetimagediag, since deleted per its own
// "delete it once confirmed" comment) called XGetImage *directly* via raw
// Xlib cgo -- bypassing robotgo's bindings entirely -- varying
// format/plane_mask/rectangle size, and still hit the identical BadMatch
// every time. That rules out "robotgo's cgo bindings are buggy" (asked about
// in Revision L's prompt): the failure reproduces with zero robotgo code in
// the picture, so there is no robotgo-specific glue bug to fix.
//
// Two things previously recorded here as supporting evidence were wrong, and
// are corrected rather than deleted because they were each load-bearing for a
// revision's conclusion:
//
//   - "`scrot` succeeds on this display, because it has compositing-aware
//     capture logic." It does exit 0, which is what Revision K observed, but
//     the PNG it writes is entirely black. A zero exit status was never
//     evidence that it captured anything, and treating it as such is what kept
//     the compositing-manager theory alive for several revisions.
//   - "GetImage rejects the overlay the same way it rejects the bare root."
//     The first out-of-condoc xcompositediag run after Revision N reported
//     this from a run whose X errors had been lost to C
//     stdio buffering, so the two failures were never actually compared. They
//     do in fact both fail with BadMatch -- but that was confirmed only once
//     xcompositediag routed every probe's output through Go.
//
// The settled picture: this host is a GNOME *Wayland* session whose only X
// server is rootless XWayland (`/usr/bin/Xwayland :0 -rootless`, a child of
// gnome-shell), with mutter compositing on the Wayland side. _NET_WM_CM_S0 has
// an owner, so the usual compositing-manager check cannot distinguish this
// case. No X11 drawable here holds the composited desktop: not the root, not
// the Composite overlay, and not a pixmap copied from either. The desktop
// exists only inside mutter, on the Wayland side, so the compositor has to be
// asked for it -- which is captureViaPortal's job.
var captureViaXComposite = func() (image.Image, error) {
	return nil, fmt.Errorf("native X11 pixmap-copy capture not available on this platform")
}

// captureViaPortal asks the *compositor* for the screen over D-Bus, rather
// than trying to read it out of an X drawable. On a Wayland session this is
// the only thing that can work at all (see portalcapture_linux.go, which
// overrides this on Linux, for the full reasoning and the two interfaces it
// tries); on a real X server it is simply unavailable and the X11 paths above
// are the ones that matter. Declared here as a no-op returning an error so
// non-Linux builds degrade cleanly and tests can substitute their own,
// following the same overridable-var pattern as captureViaXComposite.
var captureViaPortal = func() (image.Image, error) {
	return nil, fmt.Errorf("D-Bus compositor capture not available on this platform")
}

// hasVisibleContent reports whether an image contains any non-black pixel.
//
// Every capture path on this host has at some point returned a frame that was
// structurally valid and completely empty -- robotgo's, `scrot`'s, the
// Composite overlay's -- and an all-black PNG is indistinguishable from a
// working capture at every layer above this one, which is how the step's
// earlier revisions kept concluding a path worked when it did not. So the
// answer to "did this capture anything" is computed, not assumed, on every
// path rather than only on the fallbacks.
//
// Samples on a grid rather than walking all 4M pixels of a 3840x1080 frame:
// any real screen content lights up far more than one sample in a thousand,
// and an all-black frame is all-black everywhere, so the sparse walk reaches
// the same verdict for a fraction of the work. Fully opaque black counts as
// empty; alpha is ignored, since a capture path that returns zeroed alpha
// everywhere is still carrying content.
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

// waylandSessionNote returns a note naming a Wayland session as relevant
// context, for appending to a capture failure (or an all-black capture).
//
// This host runs GNOME Shell on Wayland with DISPLAY=:0 served by rootless
// XWayland (`/usr/bin/Xwayland :0 -rootless` as a child of `gnome-shell`), and
// mutter composites on the Wayland side. That is why GetImage against the root
// window and against the Composite overlay both fail, why `scrot` writes an
// all-black PNG here, and why no choice of rectangle or pixel format
// (Revisions C/E/F/G/K/L) changed the outcome. Note that _NET_WM_CM_S0 still
// has an owner, so the usual compositing-manager check does not distinguish
// this case.
//
// Two earlier revisions of this note each overstated the position in opposite
// directions and both are corrected here. "No X11 path can work" was too
// strong only in the sense that a pixmap copy *returns* something; "the pixmap
// copy reads back real content" was simply false -- what it reads back is
// undefined server memory that happens to resemble windows (see
// captureViaXComposite's doc comment). For capturing this desktop the practical
// conclusion is the blunt one: X11 cannot do it, and the compositor has to be
// asked over D-Bus instead (captureViaPortal).
//
// Checked two ways: from the environment, which the session sets itself, and
// by asking the X server whether it advertises the XWAYLAND extension (see
// displayIsXWayland), which is what ianar/cmd/xcompositediag uses to confirm
// the same fact from inside the client. The second catches ianar being
// started with WAYLAND_DISPLAY/XDG_SESSION_TYPE stripped from its environment
// (e.g. by a service launcher) while DISPLAY still points at XWayland. Purely
// advisory -- it only ever adds explanatory text.
func waylandSessionNote() string {
	var why string
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("XDG_SESSION_TYPE") == "wayland":
		why = "this is a Wayland session (WAYLAND_DISPLAY/XDG_SESSION_TYPE are set)"
	case displayIsXWayland():
		why = "the X server behind DISPLAY advertises the XWAYLAND extension"
	default:
		return ""
	}
	return " NOTE: " + why + ", so" +
		" DISPLAY is rootless XWayland and no X11 drawable holds the composited desktop --" +
		" the compositor must be asked over D-Bus instead." +
		" Run ianar/cmd/xcompositediag to see what is and isn't readable here."
}

// displayIsXWayland reports whether the X server behind DISPLAY is XWayland,
// by checking for its XWAYLAND extension (see xgeometry_linux.go, which
// overrides this on Linux). Declared as a no-op reporting false so non-Linux
// builds and tests never touch a real display, following the same
// overridable-var pattern as rootWindowGeometry.
var displayIsXWayland = func() bool {
	return false
}

// captureScreenImg is overridden in tests so captureNativeDisplay can be
// exercised without robotgo needing a real display to grab a screenshot
// from.
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

// captureNativeDisplay captures the full native display (or, if the real
// root window geometry disagrees with robotgo's self-reported screenSize,
// just the real rectangle), returning the resulting PNG bytes, via
// robotgo.CaptureImg -- falling back to captureViaXComposite (see its doc
// comment above) if that fails, per Revision L.
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
//
// Revision K's follow-up exhausted that geometry-matching theory too
// (its own log shows "matches real root window geometry" immediately
// before the identical BadMatch) and, via xgetimagediag's wider battery,
// every GetImage format/plane_mask variant along with it -- all read
// directly from the root window, which is the one axis none of C/E/F/K
// varied. Revision L targeted exactly that axis, and the out-of-condoc
// xcompositediag work that followed Revision N settled it: on this host
// (rootless XWayland) no X11 drawable holds the desktop at all, and the
// pixmap-copy "fix" returns undefined server memory rather than a screenshot
// -- see captureViaXComposite's doc comment above. The capture that can work
// here goes through the compositor over D-Bus (captureViaPortal), which is
// why it is tried before the X11 fallback below. The useReal/captureRegionImg
// branch is unchanged by any of that -- it still decides which rectangle is
// the real one to ask robotgo.CaptureImg for; the fallbacks only come into
// play if that whole first attempt fails or returns an all-black frame.
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

	// Each capture path in turn, best first, stopping at the first one that
	// returns a frame with actual content in it. "Returned an image" is not the
	// same test as "captured the screen" on this host -- see hasVisibleContent
	// -- so a path that succeeds but hands back an entirely black frame does not
	// end the search. It is still kept, though: a genuinely blanked or locked
	// screen really is all black, and that shouldn't become a hard failure, so
	// if no path produces content we return the first blank frame with a loud
	// warning rather than an error.
	//
	// robotgo stays first, per Revision L: it is the in-process primary this
	// step is meant to use, and on a normal X server it is the right answer. It
	// is just not one this particular host can satisfy.
	type attempt struct {
		name string
		grab func() (image.Image, error)
	}
	attempts := []attempt{{
		name: "robotgo",
		grab: func() (image.Image, error) {
			if useReal {
				return captureRegionImg(rx, ry, rw, rh)
			}
			return captureScreenImg()
		},
	}, {
		// Whole-display only (no region support) for both fallbacks: the
		// requested rectangle was never the issue -- the failure tracks the
		// drawable, not the request -- so there is nothing to preserve by
		// threading rx/ry/rw/rh through them.
		name: "D-Bus compositor screenshot",
		grab: captureViaPortal,
	}, {
		name: "native X11 pixmap copy",
		grab: captureViaXComposite,
	}}

	var img image.Image
	var err error
	var blank image.Image
	var blankFrom string
	var failures []string
	for _, a := range attempts {
		got, grabErr := a.grab()
		switch {
		case grabErr != nil:
			failures = append(failures, fmt.Sprintf("%s: %v", a.name, grabErr))
		case !hasVisibleContent(got):
			failures = append(failures, fmt.Sprintf("%s: succeeded but every pixel is black", a.name))
			if blank == nil {
				blank, blankFrom = got, a.name
			}
		default:
			if len(failures) > 0 {
				log.Printf("robot: capture via %s succeeded after %d earlier path(s) did not: %s",
					a.name, len(failures), strings.Join(failures, "; "))
			}
			img = got
		}
		if img != nil {
			break
		}
	}
	switch {
	case img != nil:
	case blank != nil:
		log.Printf("robot: WARNING -- no capture path returned any screen content; handing back the"+
			" entirely black frame from %s, which carries none. Tried: %s.%s",
			blankFrom, strings.Join(failures, "; "), waylandSessionNote())
		img = blank
	default:
		err = fmt.Errorf("every capture path failed: %s%s", strings.Join(failures, "; "), waylandSessionNote())
	}
	robotMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("native capture: %w", err)
	}
	log.Printf("robot: native capture succeeded, image bounds %v", img.Bounds())

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encoding capture as png: %w", err)
	}
	return buf.Bytes(), nil
}

// captureRegionImg is overridden in tests, parallel to captureScreenImg --
// it's the same robotgo.CaptureImg entry point with explicit region args,
// split out separately so captureNativeDisplay's real-geometry capture can
// be exercised without robotgo needing a real display.
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
	// Say up front, rather than only on a failed capture, when DISPLAY is
	// XWayland: robotgo's capture (and the X11 fallback) cannot see the desktop
	// there, so the first capture-native will go to the D-Bus compositor path.
	if note := waylandSessionNote(); note != "" {
		log.Printf("robot: native capture will rely on the D-Bus compositor path.%s", note)
	}
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
