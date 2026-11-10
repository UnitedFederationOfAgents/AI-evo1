package main

import (
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/go-vgo/robotgo"
)

// This file clicks at a point on screen, for acting on what visual
// inspection found (see vision.go) -- e.g. clicking federation-command's
// title bar to focus its window, or (sequence-v2, seqv2_ops.go) right-
// clicking an app's dock icon. Like circle-mouse (robot.go), the compositor
// is tried first, with robotgo as the fallback for X11 sessions and
// non-GNOME desktops.

// compositorPointer is a compositor input session's pointer: relative
// motion and the buttons (evdev codes, see pointerButtons).
type compositorPointer struct {
	moveBy func(dx, dy float64) error
	button func(code int32, pressed bool) error
}

// pointerButtons maps button names to their evdev codes (BTN_LEFT, ...).
var pointerButtons = map[string]int32{"left": 0x110, "right": 0x111, "middle": 0x112}

// doubleClickGap separates the two clicks of a double click.
const doubleClickGap = 80 * time.Millisecond

// openCompositorPointer opens a compositor input session and returns its
// pointer, plus a function that ends the session. Overridden on Linux by
// remotedesktop_linux.go; unavailable elsewhere and overridable in tests.
var openCompositorPointer = func() (compositorPointer, func(), error) {
	return compositorPointer{}, nil, errCompositorInputUnavailable
}

// pointerHome is a relative move far enough to pin the pointer against the
// top-left corner of the monitor it is on.
const pointerHome = -100000

// pointerHomeStep is the size of each relative move homePointer makes:
// smaller than any monitor, so a move from one monitor lands on the next.
const pointerHomeStep = 200

// homePointer moves the pointer to the top-left corner of the whole desktop
// (w by h in pointer coordinates; 0 if unknown).
//
// Mutter rejects a relative move whose end is on no monitor by clamping the
// pointer to the monitor it started on. One huge move therefore only reaches
// the corner of the current monitor: with two side-by-side monitors and the
// pointer on the right one it stopped at (1920, 0), and every click landed a
// monitor's width to the right (Step2Prompt.md, Revision H). Moving up and
// left in small alternating steps crosses each monitor edge instead.
func homePointer(moveBy func(dx, dy float64) error, w, h float64) error {
	if w <= 0 {
		w = 8192
	}
	if h <= 0 {
		h = 8192
	}
	nx := int(math.Ceil(w/pointerHomeStep)) + 1
	ny := int(math.Ceil(h/pointerHomeStep)) + 1
	for i := 0; i < nx || i < ny; i++ {
		if i < ny {
			if err := moveBy(0, -pointerHomeStep); err != nil {
				return err
			}
		}
		if i < nx {
			if err := moveBy(-pointerHomeStep, 0); err != nil {
				return err
			}
		}
	}
	// Pin to the corner of the top-left monitor, wherever rounding left it.
	return moveBy(pointerHome, pointerHome)
}

// pointerScale is the factor from capture pixels to pointer coordinates.
// A compositor screenshot is in physical pixels while the pointer moves in
// logical ones, which differ on a scaled (HiDPI) display; robotgo's screen
// size is the logical one. Any implausible ratio is taken as 1.
func pointerScale(captureW int) float64 {
	robotMu.Lock()
	w, _ := screenSize()
	robotMu.Unlock()
	if w <= 0 || captureW <= 0 {
		return 1
	}
	s := float64(w) / float64(captureW)
	if s < 0.2 || s > 1.01 {
		return 1
	}
	return s
}

// clickAt left-clicks at (x, y) in capture pixels from a capture captureW
// wide, and reports which input path it used.
func clickAt(x, y, captureW int) (string, error) {
	return clickAtWith(x, y, captureW, "left", false)
}

// clickAtWith clicks button ("left", "right" or "middle") at (x, y) in
// capture pixels from a capture captureW wide -- twice if double -- and
// reports which input path it used.
//
// The compositor's absolute pointer motion needs a screencast stream to
// address, so the compositor path instead homes the pointer to the desktop's
// top-left corner with relative moves (homePointer), then moves by (x, y)
// from there in one move -- accepted, as it ends on a monitor.
func clickAtWith(x, y, captureW int, button string, double bool) (string, error) {
	code, ok := pointerButtons[button]
	if !ok {
		return "", fmt.Errorf("no mouse button %q", button)
	}
	s := pointerScale(captureW)
	lx, ly := float64(x)*s, float64(y)*s

	robotMu.Lock()
	defer robotMu.Unlock()

	p, stop, err := openCompositorPointer()
	if err == nil {
		defer stop()
		const via = "compositor (org.gnome.Mutter.RemoteDesktop)"
		w, h := screenSize()
		if err := homePointer(p.moveBy, float64(w), float64(h)); err != nil {
			return via, err
		}
		sleep(focusSettle / 3)
		if err := p.moveBy(lx, ly); err != nil {
			return via, err
		}
		sleep(focusSettle / 3)
		clicks := 1
		if double {
			clicks = 2
		}
		for i := 0; i < clicks; i++ {
			if i > 0 {
				sleep(doubleClickGap)
			}
			if err := p.button(code, true); err != nil {
				return via, err
			}
			if err := p.button(code, false); err != nil {
				return via, err
			}
		}
		return via, nil
	}
	if !errors.Is(err, errCompositorInputUnavailable) {
		return "compositor (org.gnome.Mutter.RemoteDesktop)", err
	}
	log.Printf("robot: %v; clicking with robotgo instead", err)
	if err := robotClick(int(lx+0.5), int(ly+0.5), button, double); err != nil {
		return "robotgo", fmt.Errorf("robotgo click: %w", err)
	}
	return "robotgo", nil
}

// robotClick moves the X11 pointer to (x, y) and clicks button, twice if
// double. Overridable in tests; called under robotMu.
var robotClick = func(x, y int, button string, double bool) error {
	robotgo.Move(x, y)
	sleep(focusSettle / 3)
	robotgo.Click(button, double)
	return nil
}
