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
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

// Failure reason codes for captureViaPixmapCopyC's *outReason, one per
// distinct failure point, so a failed capture names the actual cause instead
// of listing every possibility (Revision N's production error collapsed all
// of them into one message, which made the logs useless without separately
// running ianar/cmd/xcompositediag by hand).
#define XCAPTURE_FAIL_OPEN_DISPLAY  1
#define XCAPTURE_FAIL_SRC_ATTRS     2
#define XCAPTURE_FAIL_NO_EXTENSION  3
#define XCAPTURE_FAIL_NO_OVERLAY    4
#define XCAPTURE_FAIL_CREATE_PIXMAP 5
#define XCAPTURE_FAIL_COPY_AREA     6
#define XCAPTURE_FAIL_GETIMAGE      7
#define XCAPTURE_FAIL_ALLOC         8

// Source selectors for captureViaPixmapCopyC's first argument.
#define XCAPTURE_SRC_ROOT    0
#define XCAPTURE_SRC_OVERLAY 1

// Size of the formatted X-error text, used for the C-side static below, the
// Go-side receiving buffer (see capturePixmapCopy) and XGetErrorText's own
// scratch buffer, so no two of them can drift apart and silently truncate.
//
// XCAPTURE_ERRTEXT_LEN has to hold XGetErrorText's output
// (XCAPTURE_XERRTEXT_LEN-1 chars at worst) plus the 51-character " (error code
// %d, request code %d, minor code %d)" suffix with all three codes at their
// widest, plus a NUL. Sizing this by eye is what produced the
// -Wformat-truncation warning these constants replace: 160 bytes could not
// hold the 179 the format can emit, so a verbose error text would have dropped
// the three codes -- the part most worth having. The static assertion below
// keeps that from recurring if either number is edited.
#define XCAPTURE_XERRTEXT_LEN 128
#define XCAPTURE_ERRTEXT_LEN  192

typedef char xcapErrTextFits[(XCAPTURE_ERRTEXT_LEN >= XCAPTURE_XERRTEXT_LEN + 51) ? 1 : -1];

// --- scoped X error recording ---
//
// xerror_linux.go installs a process-wide handler that logs and returns, which
// is what keeps a protocol error from killing ianar. That is the right default,
// but it discards the error, and here we must know whether a *specific* request
// failed: XCopyArea is asynchronous and does not report failure in its return
// value, and a copy that fails still leaves a perfectly readable pixmap. So the
// capture below swaps in this recording handler for its own duration and
// restores the previous one before returning.
//
// The statics are safe because captureNativeDisplay holds robotMu across the
// whole call (see robot.go), so only one capture is ever in flight.
static int  xcapErrSeen;
static char xcapErrText[XCAPTURE_ERRTEXT_LEN];

static int xcapErrorHandler(Display *d, XErrorEvent *e) {
	char buf[XCAPTURE_XERRTEXT_LEN];
	XGetErrorText(d, e->error_code, buf, sizeof(buf));
	xcapErrSeen = 1;
	snprintf(xcapErrText, sizeof(xcapErrText), "%s (error code %d, request code %d, minor code %d)",
		buf, e->error_code, e->request_code, e->minor_code);
	return 0;
}

static void xcapClearErr(void) {
	xcapErrSeen = 0;
	xcapErrText[0] = '\0';
}

// captureViaPixmapCopyC copies a source drawable into a pixmap we own and
// XGetImage's *that*, rather than XGetImage'ing the source drawable directly.
// The indirection only means something when the source drawable has readable
// backing content: on this host (rootless XWayland) the root and the Composite
// overlay both reject GetImage with BadMatch, and a pixmap copied out of either
// holds undefined server memory, not their pixels. The Go wrapper below
// therefore only calls this after rootGetImageReadableC passes -- see
// captureViaXComposite's doc comment in robot.go for the full account.
//
// src selects the drawable to copy from: XCAPTURE_SRC_ROOT (the root window)
// or XCAPTURE_SRC_OVERLAY (the Composite extension's overlay window). On
// success, copies the captured ZPixmap bytes into a freshly malloc'd buffer
// (owned by the caller -- freed from the Go side) and reports its
// dimensions/stride so the Go side can walk it correctly; returns 0 on any
// failure and sets *outReason to one of the XCAPTURE_FAIL_* codes above,
// copying the triggering X error's text (empty if none was raised) into
// errText.
static int captureViaPixmapCopyC(int src, int *outW, int *outH, unsigned char **outData,
	int *outBytesPerLine, int *outReason, char *errText, int errTextLen) {
	Display *d = NULL;
	Window root = 0, srcWin = 0, overlay = 0;
	XWindowAttributes attrs;
	Pixmap pm = 0;
	XImage *img = NULL;
	GC gc = NULL;
	XGCValues gcv;
	int event_base, error_base;
	int ok = 0;
	size_t n;
	unsigned char *buf;

	*outReason = 0;
	d = XOpenDisplay(NULL);
	if (d == NULL) {
		*outReason = XCAPTURE_FAIL_OPEN_DISPLAY;
		return 0;
	}
	XErrorHandler prev = XSetErrorHandler(xcapErrorHandler);
	xcapClearErr();

	root = DefaultRootWindow(d);
	srcWin = root;
	if (src == XCAPTURE_SRC_OVERLAY) {
		if (!XCompositeQueryExtension(d, &event_base, &error_base)) {
			*outReason = XCAPTURE_FAIL_NO_EXTENSION;
			goto done;
		}
		overlay = XCompositeGetOverlayWindow(d, root);
		if (overlay == 0) {
			*outReason = XCAPTURE_FAIL_NO_OVERLAY;
			goto done;
		}
		srcWin = overlay;
	}

	// Geometry and depth come from the source drawable itself: XCopyArea
	// requires source and destination to share a depth, and the overlay window
	// is not guaranteed to match the root's.
	if (!XGetWindowAttributes(d, srcWin, &attrs)) {
		*outReason = XCAPTURE_FAIL_SRC_ATTRS;
		goto done;
	}

	xcapClearErr();
	pm = XCreatePixmap(d, root, attrs.width, attrs.height, attrs.depth);
	XSync(d, False);
	if (pm == 0 || xcapErrSeen) {
		*outReason = XCAPTURE_FAIL_CREATE_PIXMAP;
		goto done;
	}

	// Pre-fill the pixmap with black before copying into it. A freshly created
	// pixmap's contents are undefined by the X protocol, and XCopyArea leaves
	// the destination untouched for any part of the source it cannot read
	// (reporting that out-of-band, via GraphicsExpose, rather than as an
	// error). Without this, a copy that quietly did nothing would hand back
	// whatever was in server memory -- which is indistinguishable from a real
	// screenshot by any check short of looking at it. Zeroing first means that
	// failure mode lands on the all-black path below, which is detected.
	gcv.foreground = BlackPixel(d, DefaultScreen(d));
	gc = XCreateGC(d, pm, GCForeground, &gcv);
	if (gc == NULL) {
		*outReason = XCAPTURE_FAIL_CREATE_PIXMAP;
		goto done;
	}
	XFillRectangle(d, pm, gc, 0, 0, attrs.width, attrs.height);
	xcapClearErr();
	XCopyArea(d, srcWin, pm, gc, 0, 0, attrs.width, attrs.height, 0, 0);
	XFreeGC(d, gc);
	gc = NULL;
	XSync(d, False);
	if (xcapErrSeen) {
		*outReason = XCAPTURE_FAIL_COPY_AREA;
		goto done;
	}

	xcapClearErr();
	img = XGetImage(d, pm, 0, 0, attrs.width, attrs.height, AllPlanes, ZPixmap);
	if (img == NULL) {
		*outReason = XCAPTURE_FAIL_GETIMAGE;
		goto done;
	}
	n = (size_t)img->bytes_per_line * (size_t)img->height;
	buf = malloc(n);
	if (buf == NULL) {
		*outReason = XCAPTURE_FAIL_ALLOC;
		goto done;
	}
	memcpy(buf, img->data, n);
	*outW = img->width;
	*outH = img->height;
	*outBytesPerLine = img->bytes_per_line;
	*outData = buf;
	ok = 1;

done:
	if (errText != NULL && errTextLen > 0) {
		snprintf(errText, errTextLen, "%s", xcapErrText);
	}
	if (img != NULL) {
		XDestroyImage(img);
	}
	if (gc != NULL) {
		XFreeGC(d, gc);
	}
	if (pm != 0) {
		XFreePixmap(d, pm);
	}
	if (overlay != 0) {
		XCompositeReleaseOverlayWindow(d, root);
	}
	XSetErrorHandler(prev);
	XCloseDisplay(d);
	return ok;
}

// rootGetImageReadableC reports whether a plain XGetImage against the root
// window succeeds, as a 1x1 probe. This is the test for whether the root has
// any readable backing content at all, and therefore whether a pixmap copied
// out of it means anything -- see the Go-side rootGetImageReadable below.
//
// 1x1 rather than the full screen because Revision K's battery already
// established the failure is invariant in the rectangle (a 1x1 request got the
// identical BadMatch as a whole-root one), so the smallest possible request is
// just as conclusive and costs nothing to issue on hosts where it succeeds.
static int rootGetImageReadableC(void) {
	Display *d = XOpenDisplay(NULL);
	XImage *img;
	int ok;
	if (d == NULL) {
		return 0;
	}
	XErrorHandler prev = XSetErrorHandler(xcapErrorHandler);
	xcapClearErr();
	img = XGetImage(d, DefaultRootWindow(d), 0, 0, 1, 1, AllPlanes, ZPixmap);
	XSync(d, False);
	ok = (img != NULL && !xcapErrSeen);
	if (img != NULL) {
		XDestroyImage(img);
	}
	XSetErrorHandler(prev);
	XCloseDisplay(d);
	return ok;
}
*/
import "C"

import (
	"fmt"
	"image"
	"log"
	"strings"
	"unsafe"
)

// init overrides captureViaXComposite (declared as a no-op var in robot.go
// for non-Linux builds, following the same overridable-var pattern as
// installXErrorHandler/rootWindowGeometry) with the real Xlib call, on the
// one platform this subproject's native capture/input actually targets.
//
// The mechanism is no longer "GetImage the Composite overlay window", which
// ianar/cmd/xcompositediag has now shown does not work on this host: the
// overlay exists but is IsUnmapped, and GetImage against it fails with the
// same BadMatch as the bare root.
//
// Nor is it "copy the root into a pixmap and read that back". The diagnostic
// appeared to establish that -- 93.55% non-black, and a PNG that looks at a
// glance like a desktop -- but the frame contains a shifted, wrapping duplicate
// of its own content and is recycled server memory, not a screenshot. See
// captureViaXComposite's doc comment in robot.go for the evidence and the
// mechanism. The copy is now gated behind a probe that refuses it on exactly
// the hosts where it would return garbage, which includes this one.
//
// So on this host this whole file is expected to decline, and the capture that
// works comes from portalcapture_linux.go instead. What remains here is a
// genuine fallback for a readable-root X server where only robotgo's own call
// failed.
func init() {
	captureViaXComposite = func() (image.Image, error) {
		// Refuse before copying anything if the source has no readable content.
		//
		// This is the guard that was missing when an out-of-condoc iteration
		// after Revision N shipped the pixmap copy as the primary capture path on the strength of a frame that turned
		// out to be recycled server memory. The X protocol makes the reasoning
		// short: a new pixmap's contents are undefined, and XCopyArea from a
		// drawable with no backing storage leaves them that way -- it is not an
		// error, so nothing downstream can tell. A root window that cannot even
		// be GetImage'd at 1x1 is exactly such a drawable. Checking that first
		// turns "silently returns plausible garbage" into "says why it can't".
		//
		// Kept rather than deleted outright because the inverse case is real: on
		// a host where the root *is* readable and only robotgo's particular call
		// failed, the copy is a legitimate fallback and this probe passes.
		if !rootGetImageReadable() {
			return nil, fmt.Errorf("the X root window cannot be read (a 1x1 GetImage against it"+
				" fails), so it holds no screen content and anything copied out of it would be"+
				" undefined server memory rather than a screenshot -- refusing to return it.%s",
				waylandSessionNote())
		}

		// Root first. The overlay is tried only if the root copy comes back
		// entirely black, which is the shape a compositing manager that genuinely
		// paints nothing onto the root would produce -- the case the overlay was
		// introduced for in the first place.
		img, nonBlack, rootErr := capturePixmapCopy(C.XCAPTURE_SRC_ROOT)
		if rootErr == nil && nonBlack > 0 {
			return img, nil
		}

		overlayImg, overlayNonBlack, overlayErr := capturePixmapCopy(C.XCAPTURE_SRC_OVERLAY)
		if overlayErr == nil && overlayNonBlack > 0 {
			if rootErr == nil {
				log.Printf("robot: the root-window pixmap copy succeeded but was entirely black;" +
					" using the Composite overlay window's copy, which has content")
			}
			return overlayImg, nil
		}

		// Nothing produced content. Prefer handing back an all-black-but-valid
		// frame over an error -- a genuinely blanked or locked screen is a real
		// possibility and shouldn't become a hard failure -- but say so loudly,
		// because at every layer above this one an all-black capture looks like a
		// working one.
		switch {
		case rootErr == nil:
			log.Printf("robot: WARNING -- the root-window pixmap capture succeeded (%v) but every"+
				" pixel is black, so it carries no screen content (the Composite overlay"+
				" fallback %s).%s",
				img.Bounds(), describeOverlayOutcome(overlayErr, overlayNonBlack), waylandSessionNote())
			return img, nil
		case overlayErr == nil:
			log.Printf("robot: WARNING -- the root-window pixmap capture failed (%v) and the"+
				" Composite overlay capture succeeded (%v) but is entirely black.%s",
				rootErr, overlayImg.Bounds(), waylandSessionNote())
			return overlayImg, nil
		}
		return nil, fmt.Errorf("native X11 capture failed: root-window pixmap copy: %v;"+
			" Composite overlay pixmap copy: %v%s", rootErr, overlayErr, waylandSessionNote())
	}
}

// rootGetImageReadable reports whether the X root window has readable backing
// content, by attempting the smallest possible GetImage against it. See
// rootGetImageReadableC in the cgo preamble, and the call site in init(), for
// why this gates the pixmap copy rather than merely annotating it.
func rootGetImageReadable() bool {
	return C.rootGetImageReadableC() != 0
}

// describeOverlayOutcome renders the overlay attempt's result as a clause for
// the all-black warning above, so that message says what the second attempt
// did rather than leaving it unmentioned.
func describeOverlayOutcome(err error, nonBlack int) string {
	if err != nil {
		return fmt.Sprintf("failed too: %v", err)
	}
	if nonBlack == 0 {
		return "was also entirely black"
	}
	return "had content" // unreachable: that case returns above
}

// capturePixmapCopy runs captureViaPixmapCopyC against one source drawable and
// decodes the result, returning the image and how many of its pixels are
// non-black. The caller needs that count separately from the error: "GetImage
// returned something" has repeatedly not meant the capture worked on this host,
// and an all-black frame is a distinct outcome from both success and failure.
func capturePixmapCopy(src C.int) (image.Image, int, error) {
	var w, h, bpl, reason C.int
	var data *C.uchar
	var errBuf [C.XCAPTURE_ERRTEXT_LEN]C.char

	if C.captureViaPixmapCopyC(src, &w, &h, &data, &bpl, &reason,
		&errBuf[0], C.int(len(errBuf))) == 0 {
		return nil, 0, fmt.Errorf("%s%s", xCaptureFailureReason(int(reason)),
			xErrorClause(C.GoString(&errBuf[0])))
	}
	defer C.free(unsafe.Pointer(data))

	width, height, stride := int(w), int(h), int(bpl)
	// The BGRX walk below indexes four bytes per pixel. That holds for the
	// 32-bits-per-pixel layout this host reports, but a server handing back a
	// packed 24bpp image would make it read past the end of each row -- refuse
	// explicitly rather than panicking deep inside the loop.
	if width <= 0 || height <= 0 || stride < width*4 {
		return nil, 0, fmt.Errorf("captured image has an unsupported layout (%dx%d, %d bytes per line;"+
			" need at least %d): this code only decodes 32-bits-per-pixel BGRX", width, height, stride, width*4)
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(data)), stride*height)

	// XImage's ZPixmap data for the TrueColor/24-bit-depth visual this host
	// reports (see xgeometry_linux.go's rootWindowGeometryC, and the
	// "depth=24 visual-id=0x25" logged by Build Debug/Resource logs earlier on
	// this step) is 4 bytes/pixel, little-endian BGRX/BGRA -- swap to Go's RGBA
	// order pixel by pixel rather than assuming the row stride matches a
	// freshly-allocated image.RGBA's Stride.
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	nonBlack := 0
	for y := 0; y < height; y++ {
		srcRow := raw[y*stride : y*stride+width*4]
		dstRow := img.Pix[y*img.Stride : y*img.Stride+width*4]
		for x := 0; x < width; x++ {
			b, g, r := srcRow[x*4], srcRow[x*4+1], srcRow[x*4+2]
			if r|g|b != 0 {
				nonBlack++
			}
			dstRow[x*4], dstRow[x*4+1], dstRow[x*4+2], dstRow[x*4+3] = r, g, b, 0xff
		}
	}
	return img, nonBlack, nil
}

// xErrorClause appends the X protocol error that accompanied a failure, when
// there was one. Several of the failure points below (notably the XCopyArea)
// are only detectable *as* X errors, so the text is the whole diagnosis;
// others raise none, and inventing one would repeat the mistake that misled
// the first out-of-condoc xcompositediag run after Revision N.
func xErrorClause(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return " [X error: " + text + "]"
}

// xCaptureFailureReason maps captureViaPixmapCopyC's *outReason code (one of
// the XCAPTURE_FAIL_* constants in the cgo preamble above) to a specific,
// human-readable cause, so a failed capture names which step actually failed
// rather than listing every possibility every time.
func xCaptureFailureReason(code int) string {
	switch code {
	case C.XCAPTURE_FAIL_OPEN_DISPLAY:
		return "couldn't open the X display"
	case C.XCAPTURE_FAIL_SRC_ATTRS:
		return "XGetWindowAttributes on the source drawable failed"
	case C.XCAPTURE_FAIL_NO_EXTENSION:
		return "X Composite extension not present"
	case C.XCAPTURE_FAIL_NO_OVERLAY:
		return "XCompositeGetOverlayWindow returned no overlay window"
	case C.XCAPTURE_FAIL_CREATE_PIXMAP:
		return "couldn't allocate the destination pixmap"
	case C.XCAPTURE_FAIL_COPY_AREA:
		// The copy is asynchronous and reports nothing in its return value, so
		// this is reached only via the scoped error handler. The pixmap was
		// pre-filled black, so no partial copy can be mistaken for content.
		return "XCopyArea from the source drawable into the pixmap was rejected"
	case C.XCAPTURE_FAIL_GETIMAGE:
		return "GetImage against our own pixmap returned no image"
	case C.XCAPTURE_FAIL_ALLOC:
		return "out of memory copying the captured image"
	default:
		return fmt.Sprintf("unknown failure (code %d)", code)
	}
}
