//go:build linux

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
#include <stdlib.h>
#include <string.h>

// Failure reason codes for captureViaXCompositeC's *outReason, one per
// distinct failure point. Revision N's production error collapsed all of
// these into a single "extension unavailable, no overlay window, or
// GetImage rejected it the same way it rejects the bare root" message,
// which made it impossible to tell from the log/UI alone which of the three
// actually happened without separately running ianar/cmd/xcompositediag by
// hand. Threading the real reason back up to captureViaXComposite below
// means the next failure report already names the actual cause.
#define XCOMPOSITE_FAIL_OPEN_DISPLAY 1
#define XCOMPOSITE_FAIL_NO_EXTENSION 2
#define XCOMPOSITE_FAIL_ROOT_ATTRS   3
#define XCOMPOSITE_FAIL_NO_OVERLAY   4
#define XCOMPOSITE_FAIL_GETIMAGE     5

// captureViaXCompositeC grabs the root window's *composited* image via the
// X Composite extension's overlay window, rather than XGetImage'ing the
// bare root window directly -- see captureViaXComposite's Go-side doc
// comment in robot.go for why. On success, copies the captured ZPixmap
// bytes into a freshly malloc'd buffer (owned by the caller -- freed from
// the Go side) and reports its dimensions/stride so the Go side can walk
// it correctly; returns 0 on any failure and sets *outReason to one of the
// XCOMPOSITE_FAIL_* codes above instead of touching the other out-params.
static int captureViaXCompositeC(int *outW, int *outH, unsigned char **outData, int *outBytesPerLine, int *outReason) {
	Display *d = XOpenDisplay(NULL);
	if (d == NULL) {
		*outReason = XCOMPOSITE_FAIL_OPEN_DISPLAY;
		return 0;
	}
	int event_base, error_base;
	if (!XCompositeQueryExtension(d, &event_base, &error_base)) {
		*outReason = XCOMPOSITE_FAIL_NO_EXTENSION;
		XCloseDisplay(d);
		return 0;
	}
	Window root = DefaultRootWindow(d);
	XWindowAttributes attrs;
	if (!XGetWindowAttributes(d, root, &attrs)) {
		*outReason = XCOMPOSITE_FAIL_ROOT_ATTRS;
		XCloseDisplay(d);
		return 0;
	}
	Window overlay = XCompositeGetOverlayWindow(d, root);
	if (overlay == 0) {
		*outReason = XCOMPOSITE_FAIL_NO_OVERLAY;
		XCloseDisplay(d);
		return 0;
	}
	XImage *img = XGetImage(d, overlay, 0, 0, attrs.width, attrs.height, AllPlanes, ZPixmap);
	XCompositeReleaseOverlayWindow(d, root);
	if (img == NULL) {
		*outReason = XCOMPOSITE_FAIL_GETIMAGE;
		XCloseDisplay(d);
		return 0;
	}
	size_t n = (size_t)img->bytes_per_line * (size_t)img->height;
	unsigned char *buf = malloc(n);
	if (buf == NULL) {
		*outReason = XCOMPOSITE_FAIL_GETIMAGE; // malloc failure; not worth its own code
		XDestroyImage(img);
		XCloseDisplay(d);
		return 0;
	}
	memcpy(buf, img->data, n);
	*outW = img->width;
	*outH = img->height;
	*outBytesPerLine = img->bytes_per_line;
	*outData = buf;
	XDestroyImage(img);
	XCloseDisplay(d);
	return 1;
}
*/
import "C"

import (
	"fmt"
	"image"
	"image/color"
	"unsafe"
)

// init overrides captureViaXComposite (declared as a no-op var in robot.go
// for non-Linux builds, following the same overridable-var pattern as
// installXErrorHandler/rootWindowGeometry) with the real Xlib/XComposite
// call, on the one platform this subproject's native capture/input
// actually targets.
func init() {
	captureViaXComposite = func() (image.Image, error) {
		var w, h, bpl, reason C.int
		var data *C.uchar
		if C.captureViaXCompositeC(&w, &h, &data, &bpl, &reason) == 0 {
			return nil, fmt.Errorf("XComposite overlay-window capture failed: %s", xCompositeFailureReason(int(reason)))
		}
		defer C.free(unsafe.Pointer(data))

		width, height, stride := int(w), int(h), int(bpl)
		raw := unsafe.Slice((*byte)(unsafe.Pointer(data)), stride*height)

		// XImage's ZPixmap data for the TrueColor/24-bit-depth visual this
		// host reports (see xgeometry_linux.go's rootWindowGeometryC, and the
		// "depth=24 visual-id=0x25" logged by Build Debug/Resource logs
		// earlier on this step) is 4 bytes/pixel, little-endian BGRX/BGRA --
		// swap to Go's RGBA order pixel by pixel rather than assuming the
		// row stride matches a freshly-allocated image.RGBA's Stride.
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			rowOff := y * stride
			for x := 0; x < width; x++ {
				px := rowOff + x*4
				b, g, r := raw[px], raw[px+1], raw[px+2]
				img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 0xff})
			}
		}
		return img, nil
	}
}

// xCompositeFailureReason maps captureViaXCompositeC's *outReason code (one
// of the XCOMPOSITE_FAIL_* constants in the cgo preamble above) to a
// specific, human-readable cause, so a failed capture names which of the
// three previously-conflated possibilities (extension missing, no overlay
// window, GetImage itself rejecting the overlay) actually happened, instead
// of listing all of them every time.
func xCompositeFailureReason(code int) string {
	switch code {
	case C.XCOMPOSITE_FAIL_OPEN_DISPLAY:
		return "couldn't open the X display"
	case C.XCOMPOSITE_FAIL_NO_EXTENSION:
		return "X Composite extension not present"
	case C.XCOMPOSITE_FAIL_ROOT_ATTRS:
		return "XGetWindowAttributes on the root window failed"
	case C.XCOMPOSITE_FAIL_NO_OVERLAY:
		return "XCompositeGetOverlayWindow returned no overlay window"
	case C.XCOMPOSITE_FAIL_GETIMAGE:
		return "GetImage against the overlay window itself failed (rejected the same way as the bare root)"
	default:
		return fmt.Sprintf("unknown failure (code %d)", code)
	}
}
