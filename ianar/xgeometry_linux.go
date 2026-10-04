//go:build linux

package main

/*
#cgo LDFLAGS: -lX11
#include <X11/Xlib.h>

// rootWindowGeometryC queries the real geometry of the default screen's
// root window directly from the X server via XGetWindowAttributes -- see
// rootWindowGeometry's Go-side doc comment in robot.go for why this is
// worth knowing separately from robotgo's own self-reported screenSize().
// Opens its own short-lived display connection (distinct from whatever
// connection robotgo's capture calls use) so it can be called regardless
// of robotgo's own connection state, and returns 0 (leaving *x/*y/*w/*h
// untouched) if it can't even open a display or query the attributes, so
// callers degrade to "no ground truth available" rather than crashing.
static int rootWindowGeometryC(int *x, int *y, int *w, int *h) {
	Display *d = XOpenDisplay(NULL);
	if (d == NULL) {
		return 0;
	}
	Window root = DefaultRootWindow(d);
	XWindowAttributes attrs;
	int ok = XGetWindowAttributes(d, root, &attrs);
	if (ok) {
		*x = attrs.x;
		*y = attrs.y;
		*w = attrs.width;
		*h = attrs.height;
	}
	XCloseDisplay(d);
	return ok;
}

// displayIsXWaylandC reports whether the X server behind DISPLAY advertises
// the XWAYLAND extension -- the in-client way to tell that the "X server" is
// really rootless XWayland, carried over from ianar/cmd/xcompositediag's
// reportServer. Opens its own short-lived connection for the same reason
// rootWindowGeometryC does. Returns 0 if the display can't be opened.
static int displayIsXWaylandC(void) {
	int opcode, event, error;
	int found;
	Display *d = XOpenDisplay(NULL);
	if (d == NULL) {
		return 0;
	}
	found = XQueryExtension(d, "XWAYLAND", &opcode, &event, &error);
	XCloseDisplay(d);
	return found;
}
*/
import "C"

// init overrides rootWindowGeometry (declared as a no-op var in robot.go
// for non-Linux builds, following xerror_linux.go's same pattern) with the
// real Xlib call, on the one platform this subproject's native
// capture/input actually targets.
func init() {
	rootWindowGeometry = func() (x, y, w, h int, ok bool) {
		var cx, cy, cw, ch C.int
		if C.rootWindowGeometryC(&cx, &cy, &cw, &ch) == 0 {
			return 0, 0, 0, 0, false
		}
		return int(cx), int(cy), int(cw), int(ch), true
	}
	displayIsXWayland = func() bool {
		return C.displayIsXWaylandC() != 0
	}
}
