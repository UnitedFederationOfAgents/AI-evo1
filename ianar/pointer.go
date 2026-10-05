package main

import (
	"errors"
	"fmt"
	"log"
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
// top-left corner of any desktop, from wherever it starts.
const pointerHome = -100000

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
// address, so the compositor path instead pins the pointer to the top-left
// corner with one large relative move, then moves by (x, y) from there.
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
		if err := p.moveBy(pointerHome, pointerHome); err != nil {
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
