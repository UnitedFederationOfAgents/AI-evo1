//go:build linux

package main

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

// This file drives the pointer by asking the compositor over D-Bus, for
// GNOME Wayland sessions. There DISPLAY is rootless XWayland, and robotgo's
// Move (an XWarpPointer on that X server) only moves XWayland's own idea of
// the pointer -- the compositor owns the real cursor and ignores the warp,
// so nothing visibly moves and no error is reported.
//
// org.gnome.Mutter.RemoteDesktop is the interface gnome-remote-desktop uses
// to inject input. A session is created, started, fed relative pointer
// motions, and stopped; it is bound to our bus connection, so it is also torn
// down if ianar exits mid-drive. While it runs, GNOME shows its
// remote-control indicator in the top bar.
//
// init() overrides robot.go's moveByViaCompositor var, so non-Linux builds
// keep the "unavailable" stub and tests can substitute their own.
func init() {
	moveByViaCompositor = func(drive func(moveBy func(dx, dy float64) error) error) error {
		conn, err := dbus.ConnectSessionBus()
		if err != nil {
			return fmt.Errorf("%w: connecting to the session bus: %v", errCompositorInputUnavailable, err)
		}
		defer conn.Close()

		rd := conn.Object("org.gnome.Mutter.RemoteDesktop", dbus.ObjectPath("/org/gnome/Mutter/RemoteDesktop"))
		var sessionPath dbus.ObjectPath
		if err := rd.Call("org.gnome.Mutter.RemoteDesktop.CreateSession", 0).Store(&sessionPath); err != nil {
			// No Mutter (not GNOME), or it refused us: let the caller fall back
			// to robotgo, which is the right path on a plain X11 session.
			return fmt.Errorf("%w: org.gnome.Mutter.RemoteDesktop.CreateSession: %v", errCompositorInputUnavailable, err)
		}

		const iface = "org.gnome.Mutter.RemoteDesktop.Session"
		session := conn.Object("org.gnome.Mutter.RemoteDesktop", sessionPath)
		if err := session.Call(iface+".Start", 0).Err; err != nil {
			return fmt.Errorf("%w: starting remote desktop session: %v", errCompositorInputUnavailable, err)
		}
		defer session.Call(iface+".Stop", 0)

		return drive(func(dx, dy float64) error {
			// NotifyPointerMotionRelative(in d dx, in d dy). Relative motion
			// needs no screencast stream, unlike the absolute variant.
			if err := session.Call(iface+".NotifyPointerMotionRelative", 0, dx, dy).Err; err != nil {
				return fmt.Errorf("NotifyPointerMotionRelative: %w", err)
			}
			return nil
		})
	}
}
