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

// captureViaXCompositeC grabs the root window's *composited* image via the
// X Composite extension's overlay window, rather than XGetImage'ing the
// bare root window directly -- see captureViaXComposite's Go-side doc
// comment in robot.go for why. On success, copies the captured ZPixmap
// bytes into a freshly malloc'd buffer (owned by the caller -- freed from
// the Go side) and reports its dimensions/stride so the Go side can walk
// it correctly; returns 0 on any failure (extension missing, no overlay
// window, GetImage itself failing the same way it does against the bare
// root) and touches none of the out-params.
static int captureViaXCompositeC(int *outW, int *outH, unsigned char **outData, int *outBytesPerLine) {
	Display *d = XOpenDisplay(NULL);
	if (d == NULL) {
		return 0;
	}
	int event_base, error_base;
	if (!XCompositeQueryExtension(d, &event_base, &error_base)) {
		XCloseDisplay(d);
		return 0;
	}
	Window root = DefaultRootWindow(d);
	XWindowAttributes attrs;
	if (!XGetWindowAttributes(d, root, &attrs)) {
		XCloseDisplay(d);
		return 0;
	}
	Window overlay = XCompositeGetOverlayWindow(d, root);
	if (overlay == 0) {
		XCloseDisplay(d);
		return 0;
	}
	XImage *img = XGetImage(d, overlay, 0, 0, attrs.width, attrs.height, AllPlanes, ZPixmap);
	XCompositeReleaseOverlayWindow(d, root);
	if (img == NULL) {
		XCloseDisplay(d);
		return 0;
	}
	size_t n = (size_t)img->bytes_per_line * (size_t)img->height;
	unsigned char *buf = malloc(n);
	if (buf == NULL) {
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
		var w, h, bpl C.int
		var data *C.uchar
		if C.captureViaXCompositeC(&w, &h, &data, &bpl) == 0 {
			return nil, fmt.Errorf("XComposite overlay-window capture failed (extension unavailable, no overlay window, or GetImage rejected it the same way it rejects the bare root)")
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
