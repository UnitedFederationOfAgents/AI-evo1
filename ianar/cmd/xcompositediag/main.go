// xcompositediag is a temporary, standalone diagnostic for
// condocs/initialRobotImpls/Step1SubstepDPrompt.md Revision L -- NOT wired
// into any Makefile/build, and bypasses both robotgo and ianar's own
// package entirely, same as Revision G's (now-deleted) xgetimagediag.
//
// Revisions C/E/F/K already proved the BadMatch/X_GetImage failure chased
// since Revision A doesn't depend on GetImage's rectangle, format, or
// plane_mask -- every variant of those, including a raw-Xlib battery that
// never touched robotgo's own bindings, failed identically against this
// host's root window, while `scrot` succeeded against the same display.
// Revision L's new theory is that the failure is specific to *which
// window* is being read: this host runs GNOME Shell as its compositing
// manager (see Step1SubstepDPrompt.md Revision K's Resource -- its apt
// output lists `org.gnome.Shell@ubuntu.service` and the
// `xdg-desktop-portal(-gnome)` services running alongside it), and
// compositing managers commonly paint the final composited frame only into
// the X Composite extension's overlay window, leaving the bare root window
// underneath without the content a plain XGetImage expects to read --
// which would produce exactly this BadMatch regardless of rectangle/
// format/plane_mask, since none of those change which drawable is read.
//
// This prints, independently and cheaply (no guessing needed once this
// runs):
//  1. whether a compositing manager actually owns the ICCCM
//     `_NET_WM_CM_S0` selection (the standard, well-known way to detect
//     one) -- if this says "no compositing manager", the whole theory
//     above is wrong and this diagnostic's capture attempts below aren't
//     expected to behave any differently from a plain root GetImage;
//  2. whether the X Composite extension is even present/what version;
//  3. the result of a plain root-window GetImage, for contrast (expected
//     to fail with the by-now-familiar BadMatch);
//  4. the result of the same GetImage call against the Composite
//     extension's overlay window instead -- if *this* succeeds where (3)
//     fails, that's a direct, independent confirmation of Revision L's
//     theory (and of the production fallback added alongside this in
//     ianar/xcomposite_linux.go), writing a PNG to disk to visually
//     confirm the captured content is right, not just "an error didn't
//     happen".
//
// Run with `cd ianar/cmd/xcompositediag && CGO_ENABLED=1 go run .` on a
// host that can reproduce the failure. Delete it once the cause is
// confirmed (or ruled out), per Revision G's precedent.
package main

/*
#cgo LDFLAGS: -lX11 -lXcomposite
#include <X11/Xlib.h>
// XDestroyImage below isn't a plain Xlib function but a macro defined in
// Xutil.h that dispatches through the XImage struct's own function table --
// same fix as Revision J's (now-deleted) xgetimagediag diagnostic needed for
// the identical error.
#include <X11/Xutil.h>
#include <X11/extensions/Xcomposite.h>
#include <stdio.h>
#include <string.h>
#include <stdlib.h>

// xErrorHandler mirrors ianar/xerror_linux.go's: without it, the plain
// root-window GetImage call below would take this whole diagnostic process
// down the same way it used to crash ianar itself before Revision D.
static int xErrorHandler(Display *d, XErrorEvent *e) {
	char buf[128];
	XGetErrorText(d, e->error_code, buf, sizeof(buf));
	printf("  -> X error: %s (request code %d, minor code %d, serial %lu)\n",
		buf, e->request_code, e->minor_code, e->serial);
	return 0;
}

static void installHandler(void) {
	XSetErrorHandler(xErrorHandler);
}

// hasCompositingManager reports whether the ICCCM _NET_WM_CM_S0 selection
// has an owner -- the standard, widely-supported way any X client checks
// whether a compositing manager is running, independent of which one.
static int hasCompositingManager(Display *d) {
	Atom cmAtom = XInternAtom(d, "_NET_WM_CM_S0", False);
	Window owner = XGetSelectionOwner(d, cmAtom);
	return owner != None;
}

// tryGetImage runs a plain XGetImage against the given drawable (root or
// the Composite overlay window) and reports success/failure, returning the
// XImage (caller must destroyImage) on success or NULL on failure.
static XImage *tryGetImage(Display *d, Window win, int w, int h, const char *label) {
	printf("%s (%dx%d):\n", label, w, h);
	XImage *img = XGetImage(d, win, 0, 0, w, h, AllPlanes, ZPixmap);
	if (img == NULL) {
		printf("  -> FAILED (XGetImage returned NULL, see X error above if any)\n");
		return NULL;
	}
	printf("  -> OK (depth=%d bits_per_pixel=%d bytes_per_line=%d)\n", img->depth, img->bits_per_pixel, img->bytes_per_line);
	return img;
}

// destroyImage wraps XDestroyImage -- which isn't a plain function but a
// macro expanding to a call through the XImage struct's own function-pointer
// table -- in a real, cgo-callable C function. Resource 6's
// "could not determine what C.XDestroyImage refers to" came from calling
// C.XDestroyImage directly from Go: cgo can only call actual exported
// symbols, and can't resolve a macro that expands to a struct-field function
// call, even with the right header included (that fixed the separate
// implicit-declaration error in Revision J/M, but not this one). Routing the
// call through this wrapper, which plain C compiles the macro inside just
// fine, is the same reason xcomposite_linux.go's captureViaXCompositeC
// already works: it calls XDestroyImage from C code, never from Go.
static void destroyImage(XImage *img) {
	XDestroyImage(img);
}
*/
import "C"

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"unsafe"
)

func main() {
	d := C.XOpenDisplay(nil)
	if d == nil {
		fmt.Fprintln(os.Stderr, "XOpenDisplay failed -- is DISPLAY set?")
		os.Exit(1)
	}
	defer C.XCloseDisplay(d)
	C.installHandler()

	root := C.XDefaultRootWindow(d)
	var attrs C.XWindowAttributes
	if C.XGetWindowAttributes(d, root, &attrs) == 0 {
		fmt.Fprintln(os.Stderr, "XGetWindowAttributes on root failed")
		os.Exit(1)
	}
	fmt.Printf("root window: %dx%d depth=%d\n", attrs.width, attrs.height, attrs.depth)

	if C.hasCompositingManager(d) != 0 {
		fmt.Println("compositing manager: PRESENT (_NET_WM_CM_S0 has an owner)")
	} else {
		fmt.Println("compositing manager: NONE (_NET_WM_CM_S0 has no owner) -- Revision L's overlay-window theory doesn't apply here if so")
	}

	var evBase, errBase C.int
	haveComposite := C.XCompositeQueryExtension(d, &evBase, &errBase) != 0
	if !haveComposite {
		fmt.Println("X Composite extension: NOT PRESENT -- Revision L's fallback can't work on this host either")
		os.Exit(0)
	}
	var major, minor C.int
	C.XCompositeQueryVersion(d, &major, &minor)
	fmt.Printf("X Composite extension: present, version %d.%d\n", int(major), int(minor))

	fmt.Println()
	rootLabel := C.CString("1) plain root-window GetImage")
	defer C.free(unsafe.Pointer(rootLabel))
	rootImg := C.tryGetImage(d, root, attrs.width, attrs.height, rootLabel)
	if rootImg != nil {
		C.destroyImage(rootImg)
	}

	fmt.Println()
	overlay := C.XCompositeGetOverlayWindow(d, root)
	if overlay == 0 {
		fmt.Println("2) XCompositeGetOverlayWindow failed (returned 0) -- can't try the overlay-window GetImage")
		os.Exit(1)
	}
	overlayLabel := C.CString("2) Composite overlay-window GetImage")
	defer C.free(unsafe.Pointer(overlayLabel))
	overlayImg := C.tryGetImage(d, overlay, attrs.width, attrs.height, overlayLabel)
	C.XCompositeReleaseOverlayWindow(d, root)
	if overlayImg == nil {
		fmt.Println("\nOverlay-window GetImage failed the same way the root window does -- Revision L's theory is likely wrong too; see this step's next-debugging-steps notes for what to try instead.")
		os.Exit(1)
	}
	defer C.destroyImage(overlayImg)

	// Success: decode and write a PNG so the captured content can be
	// visually confirmed, not just "no error happened" -- same bar Revision
	// K's scrot output was held to.
	width, height := int(overlayImg.width), int(overlayImg.height)
	stride := int(overlayImg.bytes_per_line)
	raw := unsafe.Slice((*byte)(unsafe.Pointer(overlayImg.data)), stride*height)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		rowOff := y * stride
		for x := 0; x < width; x++ {
			px := rowOff + x*4
			b, g, r := raw[px], raw[px+1], raw[px+2]
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 0xff})
		}
	}
	out, err := os.Create("xcompositediag_overlay.png")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating output PNG: %v\n", err)
		os.Exit(1)
	}
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		fmt.Fprintf(os.Stderr, "encoding output PNG: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nWrote xcompositediag_overlay.png -- confirms the overlay-window capture both succeeds and captures real screen content.")
}
