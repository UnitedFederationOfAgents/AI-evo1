package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// stubScreenState stubs the screen's lock and blank state, restoring
// everything when the test ends. Asking the screen to wake unblanks it; it
// returns how many times that was asked.
func stubScreenState(t *testing.T, locked, blanked bool) *int {
	t.Helper()
	origLocked, origBlanked, origWake, origUnlock, origNudge := screenLocked, screenBlanked, requestScreenWake, requestScreenUnlock, nudgePointer
	t.Cleanup(func() {
		screenLocked, screenBlanked, requestScreenWake, requestScreenUnlock, nudgePointer = origLocked, origBlanked, origWake, origUnlock, origNudge
	})
	wakes := 0
	screenLocked = func() (bool, error) { return locked, nil }
	screenBlanked = func() (bool, error) { return blanked, nil }
	requestScreenWake = func() error {
		wakes++
		blanked = false
		return nil
	}
	// Unlocking takes down the shield along with the lock screen.
	requestScreenUnlock = func() error {
		locked, blanked = false, false
		return nil
	}
	nudgePointer = func() error { return nil }
	return &wakes
}

// stubScreenClock makes sleep advance clock, restoring both when the test
// ends.
func stubScreenClock(t *testing.T) {
	t.Helper()
	origClock, origSleep := clock, sleep
	t.Cleanup(func() { clock, sleep = origClock, origSleep })
	now := time.Unix(0, 0)
	clock = func() time.Time { return now }
	sleep = func(d time.Duration) { now = now.Add(d) }
}

func TestEnsureScreenAwake(t *testing.T) {
	t.Run("awake", func(t *testing.T) {
		stubScreenClock(t)
		wakes := stubScreenState(t, false, false)
		note, err := ensureScreenAwake()
		if err != nil || note != "" || *wakes != 0 {
			t.Errorf("ensureScreenAwake() = %q, %v (woke %d times)", note, err, *wakes)
		}
	})

	t.Run("locked", func(t *testing.T) {
		stubScreenClock(t)
		wakes := stubScreenState(t, true, true)
		if _, err := ensureScreenAwake(); !errors.Is(err, errScreenLocked) || *wakes != 0 {
			t.Errorf("err = %v (woke %d times), want errScreenLocked without waking", err, *wakes)
		}
	})

	t.Run("blanked wakes", func(t *testing.T) {
		stubScreenClock(t)
		wakes := stubScreenState(t, false, true)
		note, err := ensureScreenAwake()
		if err != nil || !strings.Contains(note, "woke") || *wakes != 1 {
			t.Errorf("ensureScreenAwake() = %q, %v (woke %d times)", note, err, *wakes)
		}
	})

	t.Run("blanked then locked", func(t *testing.T) {
		stubScreenClock(t)
		stubScreenState(t, false, true)
		calls := 0
		screenLocked = func() (bool, error) {
			calls++
			return calls > 1, nil // the lock screen shows once it wakes
		}
		if _, err := ensureScreenAwake(); !errors.Is(err, errScreenLocked) {
			t.Errorf("err = %v, want errScreenLocked", err)
		}
	})

	t.Run("blanked never wakes", func(t *testing.T) {
		stubScreenClock(t)
		stubScreenState(t, false, true)
		requestScreenWake = func() error { return errors.New("refused") }
		if _, err := ensureScreenAwake(); err == nil || !strings.Contains(err.Error(), "didn't wake") {
			t.Errorf("err = %v, want a timeout", err)
		}
	})

	t.Run("state unavailable", func(t *testing.T) {
		stubScreenClock(t)
		stubScreenState(t, false, false)
		unavailable := func() (bool, error) { return false, fmt.Errorf("%w: test", errScreenStateUnavailable) }
		screenLocked, screenBlanked = unavailable, unavailable
		if note, err := ensureScreenAwake(); err != nil || note != "" {
			t.Errorf("ensureScreenAwake() = %q, %v, want to carry on", note, err)
		}
	})
}

func TestUnlockScreen(t *testing.T) {
	t.Run("not locked", func(t *testing.T) {
		stubScreenClock(t)
		stubScreenState(t, false, false)
		note, err := unlockScreen()
		if err != nil || !strings.Contains(note, "wasn't locked") {
			t.Errorf("unlockScreen() = %q, %v", note, err)
		}
	})

	t.Run("not locked but blanked wakes", func(t *testing.T) {
		stubScreenClock(t)
		wakes := stubScreenState(t, false, true)
		note, err := unlockScreen()
		if err != nil || !strings.Contains(note, "woke") || *wakes != 1 {
			t.Errorf("unlockScreen() = %q, %v (woke %d times)", note, err, *wakes)
		}
	})

	t.Run("locked unlocks", func(t *testing.T) {
		stubScreenClock(t)
		stubScreenState(t, true, true)
		note, err := unlockScreen()
		if err != nil || !strings.Contains(note, "unlocked it") {
			t.Errorf("unlockScreen() = %q, %v", note, err)
		}
	})

	t.Run("unlock refused", func(t *testing.T) {
		stubScreenClock(t)
		stubScreenState(t, true, true)
		requestScreenUnlock = func() error { return errors.New("interactive authentication required") }
		if _, err := unlockScreen(); err == nil || !strings.Contains(err.Error(), "interactive authentication required") {
			t.Errorf("err = %v, want logind's refusal", err)
		}
	})

	t.Run("stays locked", func(t *testing.T) {
		stubScreenClock(t)
		stubScreenState(t, true, true)
		requestScreenUnlock = func() error { return nil }
		if _, err := unlockScreen(); err == nil || !strings.Contains(err.Error(), "still locked") {
			t.Errorf("err = %v, want a timeout", err)
		}
	})
}
