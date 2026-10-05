//go:build linux

package main

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

// This file drives the pointer and keyboard by asking the compositor over
// D-Bus, for GNOME Wayland sessions. There DISPLAY is rootless XWayland, and
// robotgo's Move (an XWarpPointer on that X server) only moves XWayland's own
// idea of the pointer -- the compositor owns the real cursor and ignores the
// warp, so nothing visibly moves and no error is reported. robotgo's key
// events likewise only reach XWayland clients.
//
// org.gnome.Mutter.RemoteDesktop is the interface gnome-remote-desktop uses
// to inject input. A session is created, started, fed input, and stopped; it
// is bound to our bus connection, so it is also torn down if ianar exits
// mid-drive. While it runs, GNOME shows its remote-control indicator in the
// top bar.
//
// init() overrides robot.go's moveByViaCompositor, pointer.go's
// openCompositorPointer and keyboard.go's openCompositorKeyboard vars, so non-Linux builds keep the "unavailable"
// stubs and tests can substitute their own.

const rdSessionIface = "org.gnome.Mutter.RemoteDesktop.Session"

// startRemoteDesktopSession connects to the session bus and starts a Mutter
// remote desktop session. The returned stop ends the session and closes the
// connection. Failures are errCompositorInputUnavailable: nothing has been
// sent yet, so the caller can fall back to robotgo.
func startRemoteDesktopSession() (dbus.BusObject, func(), error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: connecting to the session bus: %v", errCompositorInputUnavailable, err)
	}

	rd := conn.Object("org.gnome.Mutter.RemoteDesktop", dbus.ObjectPath("/org/gnome/Mutter/RemoteDesktop"))
	var sessionPath dbus.ObjectPath
	if err := rd.Call("org.gnome.Mutter.RemoteDesktop.CreateSession", 0).Store(&sessionPath); err != nil {
		conn.Close()
		// No Mutter (not GNOME), or it refused us: let the caller fall back
		// to robotgo, which is the right path on a plain X11 session.
		return nil, nil, fmt.Errorf("%w: org.gnome.Mutter.RemoteDesktop.CreateSession: %v", errCompositorInputUnavailable, err)
	}

	session := conn.Object("org.gnome.Mutter.RemoteDesktop", sessionPath)
	if err := session.Call(rdSessionIface+".Start", 0).Err; err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("%w: starting remote desktop session: %v", errCompositorInputUnavailable, err)
	}
	return session, func() {
		session.Call(rdSessionIface+".Stop", 0)
		conn.Close()
	}, nil
}

func init() {
	moveByViaCompositor = func(drive func(moveBy func(dx, dy float64) error) error) error {
		session, stop, err := startRemoteDesktopSession()
		if err != nil {
			return err
		}
		defer stop()

		return drive(func(dx, dy float64) error {
			// NotifyPointerMotionRelative(in d dx, in d dy). Relative motion
			// needs no screencast stream, unlike the absolute variant.
			if err := session.Call(rdSessionIface+".NotifyPointerMotionRelative", 0, dx, dy).Err; err != nil {
				return fmt.Errorf("NotifyPointerMotionRelative: %w", err)
			}
			return nil
		})
	}

	openCompositorPointer = func() (compositorPointer, func(), error) {
		session, stop, err := startRemoteDesktopSession()
		if err != nil {
			return compositorPointer{}, nil, err
		}
		return compositorPointer{
			moveBy: func(dx, dy float64) error {
				if err := session.Call(rdSessionIface+".NotifyPointerMotionRelative", 0, dx, dy).Err; err != nil {
					return fmt.Errorf("NotifyPointerMotionRelative: %w", err)
				}
				return nil
			},
			button: func(code int32, pressed bool) error {
				// NotifyPointerButton(in i button, in b state), button as an
				// evdev code.
				if err := session.Call(rdSessionIface+".NotifyPointerButton", 0, code, pressed).Err; err != nil {
					return fmt.Errorf("NotifyPointerButton: %w", err)
				}
				return nil
			},
		}, stop, nil
	}

	openCompositorKeyboard = func() (keyboard, func(), error) {
		session, stop, err := startRemoteDesktopSession()
		if err != nil {
			return nil, nil, err
		}
		return keysymKeyboard{notify: func(sym uint32, pressed bool) error {
			// NotifyKeyboardKeysym(in u keysym, in b state). Mutter picks the
			// keycode and applies any shift level the keysym needs itself.
			if err := session.Call(rdSessionIface+".NotifyKeyboardKeysym", 0, sym, pressed).Err; err != nil {
				return fmt.Errorf("NotifyKeyboardKeysym: %w", err)
			}
			return nil
		}}, stop, nil
	}
}
