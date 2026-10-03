//go:build linux

// Command xgetimagediag is a *temporary* standalone diagnostic for the
// X_GetImage BadMatch chased across Step1SubstepDPrompt.md's Revisions A-F
// (see ../../robot.go's captureNativeDisplay doc comment for the full
// history). It bypasses robotgo entirely and talks to Xlib directly, so it
// can be run with nothing but a C toolchain, libx11-dev, and a real
// DISPLAY -- the same minimal set of things scripts/install-dev-deps.sh
// already installs for ianar's main build, see ../../Makefile's
// build-go comment -- on whatever host can actually reproduce the failure,
// something no sandbox used on this step so far has had (every prior
// reply above this one notes it couldn't invoke `go build`/`go test`
// itself).
//
// Three revisions in a row (C's warm-up timing, E's split-screen fallback,
// F's root-geometry check) each guessed a plausible rectangle/timing-shaped
// cause for the failure and were falsified by the very next round's logs:
// the error is identical (same request serial, same BadMatch) whether the
// capture asks for the whole screen or a region fully inside one output,
// and persists even once the requested rectangle is confirmed to exactly
// match the root window's real geometry. That rules "which pixels are
// being read" out as an axis worth guessing on again. This tool instead
// holds the rectangle fixed and varies the *other* XGetImage parameters
// (format, plane_mask) one at a time, and also prints the root window's and
// default screen's depth/visual info up front -- if GetImage's plane_mask
// doesn't cover the drawable's actual depth (e.g. a 32-bit ARGB visual
// under a compositor, vs. the AllPlanes/24-bit assumption plain robotgo
// capture makes), that's a textbook cause of exactly this error signature,
// and it would otherwise be invisible to every rectangle-shaped theory
// tried so far.
//
// Not wired into this subproject's Makefile/build (go build -o ianar .
// only builds this directory's own package, so adding this one doesn't
// touch the real binary) -- run it by hand and delete it once Step 1's
// capture failure is actually understood:
//
//	cd ianar/cmd/xgetimagediag && CGO_ENABLED=1 go run .
package main

/*
#cgo LDFLAGS: -lX11
#include <X11/Xlib.h>
// XDestroyImage isn't declared by Xlib.h itself -- it's a macro in
// Xutil.h that dispatches through the XImage's own function table
// (((ximage)->f.destroy_image)(ximage)), which is why the compiler saw
// it as an undeclared function rather than finding a missing prototype.
#include <X11/Xutil.h>
#include <stdio.h>
#include <string.h>

static char lastAttempt[128] = "(startup)";

// diagErrorHandler replaces Xlib's default handler (which would exit() the
// whole process on the very first BadMatch, same as robot.go's
// installXErrorHandler works around) so every attempt in the battery below
// runs to completion and reports its own error instead of only ever seeing
// the first one.
static int diagErrorHandler(Display *d, XErrorEvent *e) {
	char buf[128];
	XGetErrorText(d, e->error_code, buf, sizeof(buf));
	fprintf(stderr, "  -> X error during %-28s: %s (request code %d, minor code %d, serial %lu)\n",
		lastAttempt, buf, e->request_code, e->minor_code, e->serial);
	return 0;
}

static void installDiagErrorHandler(void) {
	XSetErrorHandler(diagErrorHandler);
}

// printRootInfo prints the root window's and default screen's depth/visual
// info -- the thing none of Revisions C/E/F's rectangle-shaped theories
// looked at -- so a depth/visual mismatch (e.g. root window depth 32 under
// a compositor vs. the server's nominal default depth 24) is visible even
// before any GetImage attempt below runs.
static void printRootInfo(Display *d) {
	int screen = XDefaultScreen(d);
	Window root = RootWindow(d, screen);
	XWindowAttributes attrs;
	if (!XGetWindowAttributes(d, root, &attrs)) {
		fprintf(stderr, "XGetWindowAttributes on root window failed\n");
		return;
	}
	printf("root window: %dx%d depth=%d visual-id=0x%lx class=%s map_state=%d\n",
		attrs.width, attrs.height, attrs.depth,
		attrs.visual ? attrs.visual->visualid : 0,
		attrs.class == InputOutput ? "InputOutput" : "InputOnly",
		attrs.map_state);
	printf("default screen: depth=%d\n", DefaultDepth(d, screen));

	int count = 0;
	int *depths = XListDepths(d, screen, &count);
	if (depths != NULL) {
		printf("depths supported by default screen:");
		for (int i = 0; i < count; i++) {
			printf(" %d", depths[i]);
		}
		printf("\n");
		XFree(depths);
	}
}

// tryGetImage issues one XGetImage call and reports whether it returned a
// non-NULL image. XGetImage gives no way to distinguish *why* it failed on
// its own (robotgo's "Capture image not found" wrapper is exactly this
// same NULL, with the same information loss) -- diagErrorHandler above is
// what actually surfaces the X error, matched back to this call via
// lastAttempt and the XSync right after issuing it, which blocks until the
// server has either returned the image or delivered the error for this
// specific request before the next attempt starts.
static int tryGetImage(Display *d, const char *label, Window win,
                        int x, int y, unsigned int w, unsigned int h,
                        unsigned long plane_mask, int format) {
	strncpy(lastAttempt, label, sizeof(lastAttempt) - 1);
	lastAttempt[sizeof(lastAttempt) - 1] = '\0';
	XImage *img = XGetImage(d, win, x, y, w, h, plane_mask, format);
	int ok = (img != NULL);
	if (img != NULL) {
		XDestroyImage(img);
	}
	XSync(d, False);
	printf("%-28s -> %s\n", label, ok ? "OK" : "FAILED (see X error above, if any)");
	return ok;
}

// runBattery runs every XGetImage variant worth distinguishing between.
// Each one holds everything but its own one varied parameter the same as
// attempt 1 (the exact call robotgo's CaptureImg issues), so whichever
// variant's result differs from the others points at that parameter.
static void runBattery(Display *d) {
	int screen = XDefaultScreen(d);
	Window root = RootWindow(d, screen);
	XWindowAttributes attrs;
	if (!XGetWindowAttributes(d, root, &attrs)) {
		fprintf(stderr, "XGetWindowAttributes on root window failed, aborting battery\n");
		return;
	}
	unsigned long allPlanes = (unsigned long)~0L;
	unsigned long depthPlanes = (attrs.depth >= (int)(sizeof(unsigned long) * 8))
		? allPlanes
		: ((1UL << attrs.depth) - 1);

	tryGetImage(d, "1) whole-root ZPixmap AllPlanes", root, 0, 0, attrs.width, attrs.height, allPlanes, ZPixmap);
	tryGetImage(d, "2) 1x1px ZPixmap AllPlanes", root, 0, 0, 1, 1, allPlanes, ZPixmap);
	tryGetImage(d, "3) whole-root ZPixmap depth-mask", root, 0, 0, attrs.width, attrs.height, depthPlanes, ZPixmap);
	tryGetImage(d, "4) whole-root XYPixmap AllPlanes", root, 0, 0, attrs.width, attrs.height, allPlanes, XYPixmap);
	if (attrs.width >= 2 * attrs.height) {
		// Parity with Revision E/F's left-half region, now run alongside the
		// 1x1 case above rather than alone -- if 1x1 also fails identically,
		// that alone already disproves any remaining rectangle-size theory.
		tryGetImage(d, "5) left-half region ZPixmap AllPlanes", root, 0, 0, attrs.width / 2, attrs.height, allPlanes, ZPixmap);
	}
}
*/
import "C"

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("DISPLAY =", os.Getenv("DISPLAY"))

	d := C.XOpenDisplay(nil)
	if d == nil {
		fmt.Fprintln(os.Stderr, "XOpenDisplay failed -- no X server reachable at that DISPLAY")
		os.Exit(1)
	}
	defer C.XCloseDisplay(d)

	C.installDiagErrorHandler()
	C.printRootInfo(d)
	fmt.Println()
	C.runBattery(d)
}
