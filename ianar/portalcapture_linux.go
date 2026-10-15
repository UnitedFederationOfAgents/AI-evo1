//go:build linux

package main

import (
	"fmt"
	"image"
	"image/png"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// This file captures the screen by asking the compositor over D-Bus, for
// GNOME Wayland sessions. There DISPLAY is rootless XWayland, which never
// composites anything into the X root window, so robotgo's XGetImage has no
// pixels to read. Two D-Bus interfaces are tried, in-process via
// github.com/godbus/dbus/v5:
//
//   - org.gnome.Shell.Screenshot -- gnome-shell's own interface. Synchronous,
//     non-interactive, no permission prompt. Recent GNOME restricts it to an
//     allowlist of callers, so it may answer AccessDenied; it is tried first
//     anyway because when it does work it is by far the cheaper path.
//   - org.freedesktop.portal.Screenshot -- the xdg-desktop-portal API, which is
//     the supported route for an arbitrary application and is running on this
//     host (xdg-desktop-portal.service and xdg-desktop-portal-gnome.service are
//     both up). It is asynchronous (the call returns a request handle and the
//     result arrives as a Response signal) and may show a permission dialog the
//     first time. That is acceptable here: capture-native is a button a human
//     clicks, and the grant is remembered afterwards.
//
// init() overrides robot.go's captureViaPortal var, so non-Linux builds keep
// the no-op and tests can substitute their own.
func init() {
	captureViaPortal = func() (image.Image, error) {
		conn, err := dbus.ConnectSessionBus()
		if err != nil {
			return nil, fmt.Errorf("connecting to the session bus: %w", err)
		}
		defer conn.Close()

		img, shellErr := captureViaGnomeShell(conn)
		if shellErr == nil {
			return img, nil
		}
		log.Printf("robot: org.gnome.Shell.Screenshot was not usable (%v); falling back to"+
			" the xdg-desktop-portal Screenshot API", shellErr)

		img, portalErr := captureViaScreenshotPortal(conn)
		if portalErr == nil {
			return img, nil
		}
		return nil, fmt.Errorf("org.gnome.Shell.Screenshot: %v; xdg-desktop-portal Screenshot: %w",
			shellErr, portalErr)
	}
}

// portalResponseTimeout bounds the wait for the portal's Response signal. The
// portal may be showing the user a permission dialog, so this has to allow for
// a human reaction, but it must not be unbounded: captureNativeDisplay holds
// robotMu across the call, and a wait that never returns would wedge every
// later capture and circle-mouse request behind it.
const portalResponseTimeout = 90 * time.Second

// captureViaGnomeShell asks gnome-shell to screenshot straight to a file we
// name, then decodes it. Synchronous and promptless when the caller is
// permitted; returns an AccessDenied error when it is not, which is the
// expected outcome on GNOME versions that restrict this interface.
func captureViaGnomeShell(conn *dbus.Conn) (image.Image, error) {
	f, err := os.CreateTemp("", "ianar-shell-capture-*.png")
	if err != nil {
		return nil, fmt.Errorf("creating a temp file for the screenshot: %w", err)
	}
	path := f.Name()
	// gnome-shell writes the file itself; close (and, on every failure below,
	// remove) our handle so we're not holding an fd to a file it replaces.
	f.Close()
	defer os.Remove(path)

	obj := conn.Object("org.gnome.Shell.Screenshot", dbus.ObjectPath("/org/gnome/Shell/Screenshot"))
	var ok bool
	var usedPath string
	// Screenshot(in b include_cursor, in b flash, in s filename,
	//            out b success, out s filename_used)
	err = obj.Call("org.gnome.Shell.Screenshot.Screenshot", 0, false, false, path).Store(&ok, &usedPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("gnome-shell reported the screenshot failed")
	}
	if usedPath != "" && usedPath != path {
		// gnome-shell is free to pick a different filename than the one asked
		// for; read the one it says it actually wrote, and clean that up too.
		defer os.Remove(usedPath)
		path = usedPath
	}
	return decodePNGFile(path)
}

// captureViaScreenshotPortal drives the xdg-desktop-portal Screenshot API:
// call Screenshot(), then wait for the Response signal carrying the URI of the
// PNG the portal wrote.
//
// The signal is subscribed to *before* the method call, against the request
// path predicted from our own unique bus name and handle token, because the
// portal is allowed to answer before the method call's own reply arrives --
// subscribing afterwards is a race that loses the response and hangs until the
// timeout. Predicting the path is the documented way to avoid it.
func captureViaScreenshotPortal(conn *dbus.Conn) (image.Image, error) {
	names := conn.Names()
	if len(names) == 0 {
		return nil, fmt.Errorf("the session bus connection has no unique name yet")
	}
	// ":1.234" -> "1_234", per the portal's request-object path convention.
	sender := strings.ReplaceAll(strings.TrimPrefix(names[0], ":"), ".", "_")
	token := fmt.Sprintf("ianar_%d", time.Now().UnixNano())
	wantPath := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)

	matchResponse := []dbus.MatchOption{
		dbus.WithMatchObjectPath(wantPath),
		dbus.WithMatchInterface("org.freedesktop.portal.Request"),
		dbus.WithMatchMember("Response"),
	}
	if err := conn.AddMatchSignal(matchResponse...); err != nil {
		return nil, fmt.Errorf("subscribing to the portal's Response signal: %w", err)
	}
	defer conn.RemoveMatchSignal(matchResponse...)

	sigCh := make(chan *dbus.Signal, 8)
	conn.Signal(sigCh)
	defer conn.RemoveSignal(sigCh)

	obj := conn.Object("org.freedesktop.portal.Desktop", dbus.ObjectPath("/org/freedesktop/portal/desktop"))
	var reqPath dbus.ObjectPath
	// Screenshot(in s parent_window, in a{sv} options, out o handle).
	// parent_window is empty: ianar has no toplevel of its own to parent the
	// permission dialog to. interactive=false asks for the screen as-is rather
	// than opening the shell's area-select UI; the portal may still prompt for
	// permission the first time, which is a different thing.
	err := obj.Call("org.freedesktop.portal.Screenshot.Screenshot", 0, "", map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token),
		"modal":        dbus.MakeVariant(false),
		"interactive":  dbus.MakeVariant(false),
	}).Store(&reqPath)
	if err != nil {
		return nil, fmt.Errorf("calling the portal's Screenshot method: %w", err)
	}
	if reqPath != wantPath {
		// The portal handed back a path other than the one we predicted, so the
		// subscription above is watching the wrong object. Add the real one too
		// rather than waiting out the timeout on a signal that can never arrive.
		extra := []dbus.MatchOption{
			dbus.WithMatchObjectPath(reqPath),
			dbus.WithMatchInterface("org.freedesktop.portal.Request"),
			dbus.WithMatchMember("Response"),
		}
		if err := conn.AddMatchSignal(extra...); err != nil {
			return nil, fmt.Errorf("the portal returned request path %q rather than the predicted %q,"+
				" and subscribing to that path failed: %w", reqPath, wantPath, err)
		}
		defer conn.RemoveMatchSignal(extra...)
	}

	deadline := time.After(portalResponseTimeout)
	for {
		select {
		case sig := <-sigCh:
			if sig == nil || sig.Path != reqPath || sig.Name != "org.freedesktop.portal.Request.Response" {
				continue
			}
			uri, err := screenshotURIFromResponse(sig.Body)
			if err != nil {
				return nil, err
			}
			path, err := fileURIToPath(uri)
			if err != nil {
				return nil, err
			}
			// The portal hands the file over to us; it does not clean it up.
			defer os.Remove(path)
			return decodePNGFile(path)
		case <-deadline:
			return nil, fmt.Errorf("the portal did not answer within %s (a permission dialog may be"+
				" waiting for the user on the desktop)", portalResponseTimeout)
		}
	}
}

// screenshotURIFromResponse pulls the screenshot URI out of a
// org.freedesktop.portal.Request.Response signal body, which is
// (u response, a{sv} results).
func screenshotURIFromResponse(body []interface{}) (string, error) {
	if len(body) < 2 {
		return "", fmt.Errorf("the portal's Response signal carried %d values, want 2", len(body))
	}
	code, ok := body[0].(uint32)
	if !ok {
		return "", fmt.Errorf("the portal's Response code was %T, want uint32", body[0])
	}
	switch code {
	case 0: // success
	case 1:
		return "", fmt.Errorf("the screenshot was cancelled (the user dismissed the portal's dialog)")
	default:
		return "", fmt.Errorf("the portal ended the screenshot request with response code %d", code)
	}
	results, ok := body[1].(map[string]dbus.Variant)
	if !ok {
		return "", fmt.Errorf("the portal's Response results were %T, want a{sv}", body[1])
	}
	v, ok := results["uri"]
	if !ok {
		return "", fmt.Errorf("the portal reported success but its results carried no \"uri\"")
	}
	uri, ok := v.Value().(string)
	if !ok {
		return "", fmt.Errorf("the portal's result \"uri\" was %T, want a string", v.Value())
	}
	return uri, nil
}

// fileURIToPath converts the portal's file:// URI to a local path. The portal
// percent-encodes it, so this cannot just trim the scheme.
func fileURIToPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parsing the portal's screenshot URI %q: %w", uri, err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("the portal returned a %q URI (%q); only file:// can be read directly", u.Scheme, uri)
	}
	return u.Path, nil
}

// decodePNGFile reads and decodes the PNG a compositor screenshot wrote.
func decodePNGFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening the captured screenshot: %w", err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decoding the captured screenshot %q: %w", path, err)
	}
	return img, nil
}
