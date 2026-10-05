package main

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/go-vgo/robotgo"
)

// This file is IANAR's native keyboard input, used by the sequence-v1 tab's
// key presses and typing (see sequence.go). Like circle-mouse's pointer
// input (robot.go), the compositor is tried first -- on a Wayland session
// robotgo's XTest key events only reach XWayland clients, not a native
// Wayland terminal -- with robotgo as the fallback for X11 sessions and
// non-GNOME desktops.
//
// Key names follow robotgo's ("right", "left", "enter", "end", "ctrl", "u",
// ...) so both paths share them; the compositor path maps them to X keysyms.

// keyboard is one native keyboard input path.
type keyboard interface {
	// tap presses and releases key with mods held down.
	tap(key string, mods ...string) error
	// typeText types text one character at a time.
	typeText(text string) error
}

// keyTypeInterval paces typeText so the receiving terminal sees distinct key
// presses rather than a burst.
const keyTypeInterval = 15 * time.Millisecond

// openCompositorKeyboard opens a compositor input session and returns a
// keyboard bound to it, plus a function that ends the session. Overridden on
// Linux by remotedesktop_linux.go; unavailable elsewhere and overridable in
// tests.
var openCompositorKeyboard = func() (keyboard, func(), error) {
	return nil, nil, errCompositorInputUnavailable
}

// openKeyboard returns the first usable keyboard input path, the name of
// that path, and a function to release it.
func openKeyboard() (keyboard, string, func(), error) {
	kb, closeFn, err := openCompositorKeyboard()
	if err == nil {
		return kb, "compositor (org.gnome.Mutter.RemoteDesktop)", closeFn, nil
	}
	if !errors.Is(err, errCompositorInputUnavailable) {
		return nil, "", nil, err
	}
	log.Printf("robot: %v; driving the keyboard with robotgo instead", err)
	return robotgoKeyboard{}, "robotgo", func() {}, nil
}

// robotgoKeyboard drives the keyboard through robotgo (XTest), under robotMu.
type robotgoKeyboard struct{}

func (robotgoKeyboard) tap(key string, mods ...string) error {
	args := make([]interface{}, len(mods))
	for i, m := range mods {
		args[i] = robotgoKey(m)
	}
	robotMu.Lock()
	defer robotMu.Unlock()
	if err := robotgo.KeyTap(robotgoKey(key), args...); err != nil {
		return fmt.Errorf("robotgo key tap %q: %w", key, err)
	}
	return nil
}

func (robotgoKeyboard) typeText(text string) error {
	for _, r := range text {
		robotMu.Lock()
		robotgo.TypeStr(string(r))
		robotMu.Unlock()
		sleep(keyTypeInterval)
	}
	return nil
}

// keysyms maps the named (non-character) keys IANAR uses to X keysyms, for
// the compositor path.
var keysyms = map[string]uint32{
	"enter":     0xff0d,
	"tab":       0xff09,
	"backspace": 0xff08,
	"escape":    0xff1b,
	"home":      0xff50,
	"left":      0xff51,
	"up":        0xff52,
	"right":     0xff53,
	"down":      0xff54,
	"end":       0xff57,
	"shift":     0xffe1,
	"ctrl":      0xffe3,
	"alt":       0xffe9,
	"super":     0xffeb,
	"space":     0x0020,
	"insert":    0xff63,
	"delete":    0xffff,
	"pageup":    0xff55,
	"pagedown":  0xff56,
	"f1":        0xffbe,
	"f2":        0xffbf,
	"f3":        0xffc0,
	"f4":        0xffc1,
	"f5":        0xffc2,
	"f6":        0xffc3,
	"f7":        0xffc4,
	"f8":        0xffc5,
	"f9":        0xffc6,
	"f10":       0xffc7,
	"f11":       0xffc8,
	"f12":       0xffc9,
}

// robotgoKeyNames maps the key names above that robotgo calls something
// else.
var robotgoKeyNames = map[string]string{"super": "cmd"}

func robotgoKey(k string) string {
	if r, ok := robotgoKeyNames[k]; ok {
		return r
	}
	return k
}

// keysymFor maps a key name, or a single character, to its X keysym.
// Latin-1 characters are their own keysym; other Unicode characters use the
// 0x01000000 + codepoint form.
func keysymFor(key string) (uint32, error) {
	if sym, ok := keysyms[key]; ok {
		return sym, nil
	}
	runes := []rune(key)
	if len(runes) != 1 {
		return 0, fmt.Errorf("no keysym for key %q", key)
	}
	return runeKeysym(runes[0]), nil
}

func runeKeysym(r rune) uint32 {
	if r >= 0x20 && r <= 0xff {
		return uint32(r)
	}
	return 0x01000000 | uint32(r)
}

// keysymKeyboard is a keyboard over a press/release-a-keysym primitive, as
// the compositor's input session provides.
type keysymKeyboard struct {
	notify func(sym uint32, pressed bool) error
}

func (k keysymKeyboard) tap(key string, mods ...string) error {
	sym, err := keysymFor(key)
	if err != nil {
		return err
	}
	var held []uint32
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			k.notify(held[i], false)
		}
	}()
	for _, m := range mods {
		msym, err := keysymFor(m)
		if err != nil {
			return err
		}
		if err := k.notify(msym, true); err != nil {
			return fmt.Errorf("pressing %s: %w", m, err)
		}
		held = append(held, msym)
	}
	if err := k.notify(sym, true); err != nil {
		return fmt.Errorf("pressing %s: %w", key, err)
	}
	if err := k.notify(sym, false); err != nil {
		return fmt.Errorf("releasing %s: %w", key, err)
	}
	return nil
}

func (k keysymKeyboard) typeText(text string) error {
	for _, r := range text {
		sym := runeKeysym(r)
		if err := k.notify(sym, true); err != nil {
			return fmt.Errorf("typing %q: %w", r, err)
		}
		if err := k.notify(sym, false); err != nil {
			return fmt.Errorf("typing %q: %w", r, err)
		}
		sleep(keyTypeInterval)
	}
	return nil
}
