//go:build linux

package main

import (
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
)

// This file reads and wakes the GNOME screen for screenstate.go.
//
// Whether the screen is blanked comes from gnome-shell's screen saver
// interface (org.gnome.ScreenSaver.GetActive, true while the screen shield is
// up), and it is woken with SetActive(false). Whether it is locked comes from
// logind's LockedHint on the user's graphical session, which gnome-shell sets
// while the lock screen is up. IANAR is often started by local-representative
// outside any login session, so the session is looked up through the user
// (user/self's Display) before falling back to the caller's own session.
//
// A locked screen is unlocked by calling Unlock on that same session (as
// "loginctl unlock-session" does); gnome-shell drops its lock screen when
// logind signals it.
//
// init() overrides screenstate.go's screenLocked, screenBlanked,
// requestScreenWake and requestScreenUnlock vars, so non-Linux builds keep
// the "unavailable" stubs and tests can substitute their own.

const screenSaverIface = "org.gnome.ScreenSaver"

func screenSaver() (dbus.BusObject, func(), error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: connecting to the session bus: %v", errScreenStateUnavailable, err)
	}
	return conn.Object(screenSaverIface, dbus.ObjectPath("/org/gnome/ScreenSaver")), func() { conn.Close() }, nil
}

// graphicalSessionPath finds the logind session to read LockedHint from.
func graphicalSessionPath(conn *dbus.Conn) dbus.ObjectPath {
	const login1 = "org.freedesktop.login1"
	user := conn.Object(login1, dbus.ObjectPath("/org/freedesktop/login1/user/self"))
	if v, err := user.GetProperty(login1 + ".User.Display"); err == nil {
		// Display is (so): the session id and its object path.
		if d, ok := v.Value().([]interface{}); ok && len(d) == 2 {
			if p, ok := d[1].(dbus.ObjectPath); ok && p != "/" {
				return p
			}
		}
	}
	if id := os.Getenv("XDG_SESSION_ID"); id != "" {
		var p dbus.ObjectPath
		mgr := conn.Object(login1, dbus.ObjectPath("/org/freedesktop/login1"))
		if err := mgr.Call(login1+".Manager.GetSession", 0, id).Store(&p); err == nil {
			return p
		}
	}
	return "/org/freedesktop/login1/session/auto"
}

func init() {
	screenLocked = func() (bool, error) {
		conn, err := dbus.ConnectSystemBus()
		if err != nil {
			return false, fmt.Errorf("%w: connecting to the system bus: %v", errScreenStateUnavailable, err)
		}
		defer conn.Close()
		path := graphicalSessionPath(conn)
		v, err := conn.Object("org.freedesktop.login1", path).GetProperty("org.freedesktop.login1.Session.LockedHint")
		if err != nil {
			return false, fmt.Errorf("%w: reading %s's LockedHint: %v", errScreenStateUnavailable, path, err)
		}
		locked, ok := v.Value().(bool)
		if !ok {
			return false, fmt.Errorf("%w: %s's LockedHint is %v, not a bool", errScreenStateUnavailable, path, v)
		}
		return locked, nil
	}

	screenBlanked = func() (bool, error) {
		ss, closeFn, err := screenSaver()
		if err != nil {
			return false, err
		}
		defer closeFn()
		var active bool
		if err := ss.Call(screenSaverIface+".GetActive", 0).Store(&active); err != nil {
			return false, fmt.Errorf("%w: %s.GetActive: %v", errScreenStateUnavailable, screenSaverIface, err)
		}
		return active, nil
	}

	requestScreenUnlock = func() error {
		conn, err := dbus.ConnectSystemBus()
		if err != nil {
			return fmt.Errorf("%w: connecting to the system bus: %v", errScreenStateUnavailable, err)
		}
		defer conn.Close()
		path := graphicalSessionPath(conn)
		if err := conn.Object("org.freedesktop.login1", path).Call("org.freedesktop.login1.Session.Unlock", 0).Err; err != nil {
			return fmt.Errorf("%s Unlock: %w", path, err)
		}
		return nil
	}

	requestScreenWake = func() error {
		ss, closeFn, err := screenSaver()
		if err != nil {
			return err
		}
		defer closeFn()
		if err := ss.Call(screenSaverIface+".SetActive", 0, false).Err; err != nil {
			return fmt.Errorf("%s.SetActive(false): %w", screenSaverIface, err)
		}
		return nil
	}
}
