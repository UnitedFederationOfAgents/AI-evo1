package main

import (
	"errors"
	"fmt"
	"log"
	"time"
)

// This file makes sure the desktop is awake and unlocked before the
// sequence-v1 runner acts on it (condocs/initialRobotImpls/Step2Prompt.md,
// Revision E). A run started from another node's browser finds the target
// host as its user left it -- typically idle, so GNOME has blanked the screen
// and, by default, locked it. Synthetic input then goes to the screen shield
// rather than to federation-command: the first event only wakes the screen,
// and on a locked screen every key after it lands in the password field.
// A run started from the target's own browser never sees this, since its
// user has just been using the desktop.
//
// So before step 1 looks at the screen: a locked screen fails the run with a
// clear error (virtual input can't, and shouldn't, unlock it), and a blanked
// one is woken and waited for.

const (
	screenWakeTimeout = 3 * time.Second        // how long a blanked screen is given to wake
	screenWakePoll    = 250 * time.Millisecond // how often it is checked while waking
	screenWakeSettle  = 1 * time.Second        // GNOME fades the desktop back in after waking
)

// errScreenStateUnavailable marks a desktop whose lock/blank state can't be
// read (not GNOME, no session bus), as opposed to one that answered.
var errScreenStateUnavailable = errors.New("screen state unavailable")

var errScreenLocked = errors.New("the screen is locked: IANAR's virtual input can't unlock it, and keys sent now would go to the lock screen instead of federation-command. Unlock this host (or turn off its automatic screen lock) and run again")

// screenLocked, screenBlanked and requestScreenWake ask the desktop about
// and wake its screen. Overridden on Linux by screenstate_linux.go;
// unavailable elsewhere and overridable in tests.
var (
	screenLocked = func() (bool, error) {
		return false, fmt.Errorf("%w: not on this platform", errScreenStateUnavailable)
	}
	screenBlanked = func() (bool, error) {
		return false, fmt.Errorf("%w: not on this platform", errScreenStateUnavailable)
	}
	requestScreenWake = func() error {
		return fmt.Errorf("%w: not on this platform", errScreenStateUnavailable)
	}
)

// nudgePointer moves the pointer a pixel and back through the compositor,
// which GNOME counts as user activity. Overridable in tests.
var nudgePointer = func() error {
	robotMu.Lock()
	defer robotMu.Unlock()
	p, stop, err := openCompositorPointer()
	if err != nil {
		return err
	}
	defer stop()
	if err := p.moveBy(1, 0); err != nil {
		return err
	}
	return p.moveBy(-1, 0)
}

// ensureScreenAwake fails if the screen is locked and wakes it if it has
// blanked. It returns a note on what it did ("" if the screen was already
// awake). A desktop whose state can't be read is assumed awake.
func ensureScreenAwake() (string, error) {
	if locked, err := screenLocked(); err != nil {
		log.Printf("robot: can't tell whether the screen is locked (%v); assuming not", err)
	} else if locked {
		return "", errScreenLocked
	}
	blanked, err := screenBlanked()
	if err != nil {
		log.Printf("robot: can't tell whether the screen is blanked (%v); assuming not", err)
		return "", nil
	}
	if !blanked {
		return "", nil
	}

	log.Printf("robot: the screen has blanked; waking it")
	if err := requestScreenWake(); err != nil {
		log.Printf("robot: asking the screen saver to wake: %v", err)
	}
	if err := nudgePointer(); err != nil {
		log.Printf("robot: nudging the pointer to wake the screen: %v", err)
	}
	deadline := clock().Add(screenWakeTimeout)
	for {
		sleep(screenWakePoll)
		if b, err := screenBlanked(); err == nil && !b {
			break
		}
		if !clock().Before(deadline) {
			return "", fmt.Errorf("the screen has blanked and didn't wake within %s", screenWakeTimeout)
		}
	}
	// Waking a screen that has been blank past GNOME's lock delay shows the
	// lock screen.
	if locked, err := screenLocked(); err == nil && locked {
		return "", errScreenLocked
	}
	sleep(screenWakeSettle)
	return "the screen had blanked; woke it first", nil
}
