// xcompositediag is a temporary, standalone diagnostic for
// condocs/initialRobotImpls/Step1SubstepDPrompt.md -- NOT wired into any
// Makefile/build, and bypasses both robotgo and ianar's own package
// entirely, same as Revision G's (now-deleted) xgetimagediag.
//
// Revisions C/E/F/K already proved the BadMatch/X_GetImage failure chased
// since Revision A doesn't depend on GetImage's rectangle, format, or
// plane_mask -- every variant of those, including a raw-Xlib battery that
// never touched robotgo's own bindings, failed identically against this
// host's root window, while `scrot` succeeded against the same display.
// Revision L's theory was that the failure is specific to *which window* is
// read: a compositing manager paints the composited frame only into the X
// Composite extension's overlay window, leaving the bare root without the
// content a plain XGetImage expects.
//
// The first run of this after Revision N (done outside the condoc, before
// Revision O rolled the results back into ianar) reported that the overlay
// GetImage failed too,
// concluding Revision L's theory was dead. That conclusion was drawn from a
// run whose evidence had been destroyed by a bug in this file, and this
// revision fixes it:
//
//  1. Every informative line this diagnostic produced -- both probe labels,
//     both OK/FAILED lines, and critically the X error text/code/serial from
//     the error handler -- was written with C `printf` to stdout, while the
//     surrounding narration used Go's `fmt`. Go's `fmt` write(2)s straight to
//     fd 1; C `printf` goes through glibc's stdio, which full-buffers when
//     stdout is a pipe (it was: the run was `... | tee`). Nothing then
//     flushed that buffer -- `os.Exit` skips libc's atexit handlers, and even
//     a normal return from a Go `main` exits via the runtime, not libc
//     `exit()`. So on *every* path, piped C output was silently dropped.
//     That run's transcript shows exactly this: three Go lines, then three
//     bare newlines where the two probes' output should have been.
//
//     The consequence is that that run learned only "overlay GetImage
//     returned NULL" (a Go-side observation of the return value). Whether the
//     root probe failed, and what X error *either* probe raised -- the entire
//     point of running this -- was never seen. "failed the same way the root
//     window does" was not established.
//
//     Fixed by routing all output through Go and having the X error handler
//     record the error into a struct Go reads (see takeXError), so no output
//     can be lost to buffering, redirection, or exit path again.
//
//  2. A cheap check nobody had run settles more than any of these probes:
//     `ps -ef` on this host shows `/usr/bin/Xwayland :0 -rootless` running as
//     a child of `gnome-shell --mode=ubuntu`. This is a GNOME *Wayland*
//     session, and DISPLAY=:0 is rootless XWayland -- not a real X server.
//     That is consistent with every invariant observed so far (the failure
//     tracking the drawable rather than the request, and `_NET_WM_CM_S0`
//     having an owner while the Composite overlay still yields nothing: the
//     compositing is done by mutter on the Wayland side, entirely outside X).
//
//     It also means the remaining question is no longer "which X drawable
//     holds the pixels" but "are the pixels in the X server at all". The
//     probes below are chosen to answer that, and to resolve the one fact
//     that contradicts it -- Revision K's report that `scrot` succeeded,
//     which is why probe (5) measures captured *content* and not just the
//     absence of an error. An all-black "success" explains both observations
//     at once; a genuinely populated image means the theory is wrong.
//
// This prints, independently and cheaply:
//  1. the X server's vendor/release and full extension list -- whether an
//     `XWAYLAND` extension is advertised identifies rootless XWayland from
//     inside the client, rather than inferring it from the process tree;
//  2. the root window's class, map_state and backing_store. GetImage requires
//     its drawable be viewable, so if the root reports anything other than
//     IsViewable the BadMatch is fully explained and needs no theory;
//  3. whether a compositing manager owns `_NET_WM_CM_S0`, and whether the
//     Composite extension is present/what version;
//  4. a plain root-window GetImage, with its X error, for contrast;
//  5. the same GetImage against the Composite overlay window, plus that
//     window's own attributes (an unmapped overlay would also force BadMatch
//     on its own, independently of Revision L's theory);
//  6. a third route Revision L never tried: XCopyArea the root into a pixmap
//     we allocate ourselves, then GetImage *that*. This separates "GetImage
//     rejects this drawable" from "the root has no readable content" -- and
//     if the copy route works, it is a viable production capture path, not
//     just a datum.
//
// Each probe reports the X error it raised, and each success reports what
// fraction of the captured pixels are non-black and writes a PNG, so a
// "success" that returns an empty frame is not mistaken for a working
// capture. No probe short-circuits the others.
//
// Run with `cd ianar/cmd/xcompositediag && CGO_ENABLED=1 go run .` on a host
// that can reproduce the failure. Delete it once the cause is confirmed (or
// ruled out), per Revision G's precedent.
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

// The error handler records the most recent X protocol error into these
// statics for the Go side to read, rather than printing it itself. Printing
// from C is what lost this diagnostic's entire output in its first run -- see
// this file's header comment (1). Recording it also lets Go attribute each
// error to the probe that caused it and print them in order.
static char xErrText[128];
static int xErrCode, xErrRequest, xErrMinor, xErrSeen;
static unsigned long xErrSerial;

// xErrorHandler mirrors ianar/xerror_linux.go's: without it, the plain
// root-window GetImage call below would take this whole diagnostic process
// down the same way it used to crash ianar itself before Revision D.
static int xErrorHandler(Display *d, XErrorEvent *e) {
	XGetErrorText(d, e->error_code, xErrText, sizeof(xErrText));
	xErrCode = e->error_code;
	xErrRequest = e->request_code;
	xErrMinor = e->minor_code;
	xErrSerial = e->serial;
	xErrSeen = 1;
	return 0;
}

static void installHandler(void) {
	XSetErrorHandler(xErrorHandler);
}

// hasCompositingManager reports whether the ICCCM _NET_WM_CM_S0 selection
// has an owner -- the standard, widely-supported way any X client checks
// whether a compositing manager is running, independent of which one. Note
// that on a Wayland session mutter owns this selection on XWayland's behalf,
// so a "yes" here does not imply there is an X-side composited frame to read.
static int hasCompositingManager(Display *d) {
	Atom cmAtom = XInternAtom(d, "_NET_WM_CM_S0", False);
	Window owner = XGetSelectionOwner(d, cmAtom);
	return owner != None;
}

static void clearXError(void) {
	xErrSeen = 0;
	xErrText[0] = '\0';
}

static int haveXError(void) { return xErrSeen; }
static const char *xErrorText(void) { return xErrText; }
static int xErrorCode(void) { return xErrCode; }
static int xErrorRequest(void) { return xErrRequest; }
static int xErrorMinor(void) { return xErrMinor; }
static unsigned long xErrorSerial(void) { return xErrSerial; }

// syncDisplay forces a round trip so any error from the preceding request has
// been delivered to the handler before Go reads it. X errors are asynchronous;
// XGetImage happens to be a round trip already, but XCopyArea and
// XCreatePixmap are not, and their errors would otherwise land against a
// later probe.
static void syncDisplay(Display *d) {
	XSync(d, False);
}

// serverVendor/vendorRelease wrap the ServerVendor/VendorRelease macros,
// which cgo can't bind directly for the same reason it can't bind
// XDestroyImage (see destroyImage below).
static const char *serverVendor(Display *d) { return ServerVendor(d); }
static int vendorRelease(Display *d) { return VendorRelease(d); }

// getImage wraps XGetImage mainly so AllPlanes -- a macro, not a constant cgo
// can reference -- expands in C. Takes a Drawable so the pixmap probe can
// share it with the two window probes.
static XImage *getImage(Display *d, Drawable drw, int w, int h) {
	return XGetImage(d, drw, 0, 0, w, h, AllPlanes, ZPixmap);
}

// copyAreaToPixmap runs just the XCopyArea half of probe (6), so the Go side
// can read the copy's X error separately from the subsequent GetImage's.
static void copyAreaToPixmap(Display *d, Drawable src, Pixmap dst, int w, int h) {
	GC gc = XCreateGC(d, dst, 0, NULL);
	XCopyArea(d, src, dst, gc, 0, 0, w, h, 0, 0);
	XFreeGC(d, gc);
	XSync(d, False);
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
	"strings"
	"unsafe"
)

func main() {
	// run returns rather than calling os.Exit so deferred cleanup happens.
	// All output below goes through Go's fmt, never C printf -- see this
	// file's header comment (1).
	os.Exit(run())
}

func run() int {
	d := C.XOpenDisplay(nil)
	if d == nil {
		fmt.Fprintln(os.Stderr, "XOpenDisplay failed -- is DISPLAY set?")
		return 1
	}
	defer C.XCloseDisplay(d)
	C.installHandler()

	isXWayland := reportServer(d)

	root := C.XDefaultRootWindow(d)
	var attrs C.XWindowAttributes
	if C.XGetWindowAttributes(d, root, &attrs) == 0 {
		fmt.Fprintln(os.Stderr, "XGetWindowAttributes on root failed")
		return 1
	}
	fmt.Printf("\nroot window: %dx%d depth=%d %s\n",
		int(attrs.width), int(attrs.height), int(attrs.depth), describeAttrs(&attrs))
	rootViewable := attrs.map_state == C.IsViewable

	if C.hasCompositingManager(d) != 0 {
		fmt.Println("compositing manager: PRESENT (_NET_WM_CM_S0 has an owner)")
	} else {
		fmt.Println("compositing manager: NONE (_NET_WM_CM_S0 has no owner)")
	}

	var evBase, errBase C.int
	haveComposite := C.XCompositeQueryExtension(d, &evBase, &errBase) != 0
	if haveComposite {
		var major, minor C.int
		C.XCompositeQueryVersion(d, &major, &minor)
		fmt.Printf("X Composite extension: present, version %d.%d\n", int(major), int(minor))
	} else {
		fmt.Println("X Composite extension: NOT PRESENT -- Revision L's fallback can't work on this host at all")
	}

	w, h := C.int(attrs.width), C.int(attrs.height)

	// Probe 4: plain root-window GetImage. The first run never saw this result.
	rootOK, rootNonBlack := probe(d, "4) plain root-window GetImage",
		C.Drawable(root), w, h, "xcompositediag_root.png")

	// Probe 5: the Composite overlay window, plus its own attributes.
	overlayOK, overlayNonBlack := false, -1.0
	if haveComposite {
		fmt.Println()
		C.clearXError()
		overlay := C.XCompositeGetOverlayWindow(d, root)
		C.syncDisplay(d)
		if overlay == 0 {
			fmt.Printf("5) XCompositeGetOverlayWindow failed (returned 0)%s\n", xErrSuffix())
		} else {
			var oa C.XWindowAttributes
			if C.XGetWindowAttributes(d, C.Window(overlay), &oa) != 0 {
				fmt.Printf("5) overlay window 0x%x: %dx%d depth=%d %s\n",
					uint64(overlay), int(oa.width), int(oa.height), int(oa.depth), describeAttrs(&oa))
			} else {
				fmt.Printf("5) overlay window 0x%x: XGetWindowAttributes failed%s\n", uint64(overlay), xErrSuffix())
			}
			overlayOK, overlayNonBlack = probe(d, "5) Composite overlay-window GetImage",
				C.Drawable(overlay), w, h, "xcompositediag_overlay.png")
			C.XCompositeReleaseOverlayWindow(d, root)
		}
	}

	// Probe 6: XCopyArea the root into our own pixmap, then GetImage that.
	pixmapOK, pixmapNonBlack := probePixmapCopy(d, root, w, h, attrs.depth)

	summarize(isXWayland, rootViewable, haveComposite,
		rootOK, rootNonBlack, overlayOK, overlayNonBlack, pixmapOK, pixmapNonBlack)
	if rootOK || overlayOK || pixmapOK {
		return 0
	}
	return 1
}

// reportServer prints the server's identity and extension list, and reports
// whether an XWAYLAND extension is advertised -- the in-client confirmation
// of what this host's process tree already shows (header comment (2)).
func reportServer(d *C.Display) bool {
	fmt.Printf("X server: vendor=%q release=%d\n",
		C.GoString(C.serverVendor(d)), int(C.vendorRelease(d)))

	var n C.int
	list := C.XListExtensions(d, &n)
	if list == nil {
		fmt.Println("X extensions: XListExtensions returned none")
		return false
	}
	defer C.XFreeExtensionList(list)
	exts := make([]string, 0, int(n))
	for _, p := range unsafe.Slice(list, int(n)) {
		exts = append(exts, C.GoString(p))
	}
	fmt.Printf("X extensions (%d): %s\n", len(exts), strings.Join(exts, " "))
	for _, e := range exts {
		if strings.EqualFold(e, "XWAYLAND") {
			fmt.Println("  -> XWAYLAND extension present: this is XWayland, not a real X server")
			return true
		}
	}
	fmt.Println("  -> no XWAYLAND extension advertised")
	return false
}

// describeAttrs renders the three XGetWindowAttributes fields that decide
// whether GetImage is permitted against a window: GetImage requires a
// viewable InputOutput drawable, so a non-viewable or InputOnly window
// forces BadMatch on its own, with no further theory needed.
func describeAttrs(a *C.XWindowAttributes) string {
	class := fmt.Sprintf("class=%d(?)", int(a.class))
	switch a.class {
	case C.InputOutput:
		class = "class=InputOutput"
	case C.InputOnly:
		class = "class=InputOnly"
	}
	state := fmt.Sprintf("map_state=%d(?)", int(a.map_state))
	switch a.map_state {
	case C.IsUnmapped:
		state = "map_state=IsUnmapped"
	case C.IsUnviewable:
		state = "map_state=IsUnviewable"
	case C.IsViewable:
		state = "map_state=IsViewable"
	}
	backing := fmt.Sprintf("backing_store=%d(?)", int(a.backing_store))
	switch a.backing_store {
	case C.NotUseful:
		backing = "backing_store=NotUseful"
	case C.WhenMapped:
		backing = "backing_store=WhenMapped"
	case C.Always:
		backing = "backing_store=Always"
	}
	return class + " " + state + " " + backing
}

// takeXError returns the X protocol error recorded since the last
// clearXError, formatted for appending to a probe's result line, plus whether
// there was one at all. Reading the flag rather than pattern-matching the
// returned text keeps callers that branch on it (probePixmapCopy) honest.
// This is the output the first run dropped entirely.
func takeXError() (string, bool) {
	if C.haveXError() == 0 {
		return " (no X error raised)", false
	}
	s := fmt.Sprintf(" -> X error: %s (error code %d, request code %d, minor code %d, serial %d)",
		C.GoString(C.xErrorText()), int(C.xErrorCode()), int(C.xErrorRequest()),
		int(C.xErrorMinor()), uint64(C.xErrorSerial()))
	C.clearXError()
	return s, true
}

// xErrSuffix is takeXError for the callers that only need the text.
func xErrSuffix() string {
	s, _ := takeXError()
	return s
}

// probe runs one GetImage against drw and reports the outcome, the X error if
// any, and -- on success -- how much of the captured frame is actually
// non-black. It returns whether GetImage succeeded and that percentage (-1 if
// it failed or couldn't be decoded).
func probe(d *C.Display, label string, drw C.Drawable, w, h C.int, pngName string) (bool, float64) {
	fmt.Printf("\n%s (%dx%d):\n", label, int(w), int(h))
	C.clearXError()
	img := C.getImage(d, drw, w, h)
	C.syncDisplay(d)
	if img == nil {
		fmt.Printf("  FAILED (XGetImage returned NULL)%s\n", xErrSuffix())
		return false, -1
	}
	defer C.destroyImage(img)
	fmt.Printf("  OK%s\n", xErrSuffix())
	return true, describeImage(img, pngName)
}

// probePixmapCopy is probe (6): allocate a pixmap, XCopyArea the root into
// it, then GetImage the pixmap. Each step's X error is read separately,
// because a failed copy still leaves a readable (but uninitialised) pixmap --
// so "GetImage succeeded" here means nothing without the copy's error and the
// content check.
func probePixmapCopy(d *C.Display, root C.Window, w, h, depth C.int) (bool, float64) {
	fmt.Printf("\n6) XCopyArea root -> own pixmap, then GetImage the pixmap (%dx%d depth=%d):\n",
		int(w), int(h), int(depth))
	C.clearXError()
	pm := C.XCreatePixmap(d, C.Drawable(root), C.uint(w), C.uint(h), C.uint(depth))
	C.syncDisplay(d)
	if pm == 0 {
		fmt.Printf("  XCreatePixmap FAILED%s\n", xErrSuffix())
		return false, -1
	}
	defer C.XFreePixmap(d, pm)
	fmt.Printf("  XCreatePixmap ok (0x%x)%s\n", uint64(pm), xErrSuffix())

	C.clearXError()
	C.copyAreaToPixmap(d, C.Drawable(root), pm, w, h)
	copyErr, copyFailed := takeXError()
	if copyFailed {
		fmt.Printf("  XCopyArea from root FAILED%s\n", copyErr)
	} else {
		fmt.Println("  XCopyArea from root raised no error")
	}

	C.clearXError()
	img := C.getImage(d, C.Drawable(pm), w, h)
	C.syncDisplay(d)
	if img == nil {
		fmt.Printf("  GetImage on the pixmap FAILED (returned NULL)%s\n", xErrSuffix())
		return false, -1
	}
	defer C.destroyImage(img)
	fmt.Printf("  GetImage on the pixmap OK%s\n", xErrSuffix())
	pct := describeImage(img, "xcompositediag_pixmap.png")
	if copyFailed {
		fmt.Println("  NOTE: the copy above failed, so this pixmap's contents are " +
			"uninitialised -- this is not a working capture no matter what it decoded to")
		return false, pct
	}
	return true, pct
}

// describeImage reports a captured XImage's geometry and, for the 32bpp
// TrueColor layout this host reports, what fraction of its pixels are
// non-black, and writes it out as a PNG. Revision K's scrot output was held
// to a visual bar, and "XGetImage returned non-NULL" has repeatedly not meant
// the capture worked -- an all-black frame from a rootless XWayland root is
// exactly the kind of "success" that would otherwise be reported as a fix.
// Returns the non-black percentage, or -1 if it couldn't be decoded.
func describeImage(img *C.XImage, pngName string) float64 {
	width, height := int(img.width), int(img.height)
	stride := int(img.bytes_per_line)
	bpp := int(img.bits_per_pixel)
	fmt.Printf("  geometry: %dx%d depth=%d bits_per_pixel=%d bytes_per_line=%d\n",
		width, height, int(img.depth), bpp, stride)

	// The decode below indexes 4 bytes per pixel in BGRX order, which is the
	// usual 32bpp TrueColor layout but not guaranteed. Bail out with the
	// actual geometry rather than panicking on a slice overrun -- the point of
	// this diagnostic is which probes succeeded at all, and losing that to a
	// decode panic would cost another whole debug cycle.
	if bpp != 32 || stride < width*4 || width <= 0 || height <= 0 {
		fmt.Println("  (only a non-empty 32bpp BGRX layout with stride >= width*4 can be" +
			" decoded here, so no content check and no PNG)")
		return -1
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(img.data)), stride*height)
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	nonBlack := 0
	for y := 0; y < height; y++ {
		rowOff := y * stride
		for x := 0; x < width; x++ {
			px := rowOff + x*4
			b, g, r := raw[px], raw[px+1], raw[px+2]
			if r|g|b != 0 {
				nonBlack++
			}
			out.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 0xff})
		}
	}
	pct := 100 * float64(nonBlack) / float64(width*height)
	fmt.Printf("  content: %.2f%% of pixels non-black\n", pct)
	if pct == 0 {
		fmt.Println("  -> the frame is entirely black: GetImage was answered, but with no" +
			" screen content in it (this is what a rootless XWayland root window looks like)")
	}
	f, err := os.Create(pngName)
	if err != nil {
		fmt.Printf("  (couldn't write %s: %v)\n", pngName, err)
		return pct
	}
	defer f.Close()
	if err := png.Encode(f, out); err != nil {
		fmt.Printf("  (couldn't encode %s: %v)\n", pngName, err)
		return pct
	}
	fmt.Printf("  wrote %s -- open it to confirm the content visually\n", pngName)
	return pct
}

// summarize states which theory the probes above actually support, so the
// next revision starts from a conclusion rather than re-reading raw output.
// Deliberately distinguishes "GetImage failed" from "GetImage succeeded with
// an empty frame", because those two call for completely different fixes.
func summarize(isXWayland, rootViewable, haveComposite bool,
	rootOK bool, rootPct float64, overlayOK bool, overlayPct float64,
	pixmapOK bool, pixmapPct float64) {

	usable := func(ok bool, pct float64) bool { return ok && pct > 0 }
	fmt.Println("\n--- summary ---")

	switch {
	case usable(rootOK, rootPct):
		fmt.Println("The plain root-window GetImage WORKED and returned real content. The" +
			" failure ianar hits is therefore not inherent to this display -- compare this" +
			" process's environment (DISPLAY/XAUTHORITY) against the server's, since that is" +
			" now the only thing left that differs from ianar's own failing call.")
	case usable(overlayOK, overlayPct):
		fmt.Println("The Composite overlay-window GetImage WORKED and returned real content" +
			" where the root did not: Revision L's theory is confirmed after all, and" +
			" the first run's report to the contrary came from the lost-output bug described in" +
			" this file's header. ianar/xcomposite_linux.go's fallback is the right fix.")
	case usable(pixmapOK, pixmapPct):
		fmt.Println("Neither window could be read directly, but XCopyArea root -> pixmap ->" +
			" GetImage returned a non-black frame.")
		fmt.Println("\n!! DO NOT TREAT THAT AS A WORKING CAPTURE. !! An earlier revision of this" +
			" file said it was a viable production path, ianar adopted it on that basis, and it" +
			" was wrong -- the frame it produces is undefined server memory, not a screenshot." +
			" A new pixmap's contents are undefined by the X protocol, and a root window that" +
			" cannot itself be GetImage'd has no backing storage, so XCopyArea out of it copies" +
			" nothing and leaves whatever was already there. On XWayland that memory is recycled" +
			" X client window buffers, which is why the result looks convincingly like real" +
			" terminals and browsers: those are XWayland clients, reassembled at meaningless" +
			" offsets. A non-black percentage, and even a glance at the PNG, will not catch it.")
		fmt.Println("\nTo see it for yourself, open the PNG written below at full size and look" +
			" for the same window appearing twice at an offset, wrapping at the image edge --" +
			" that is the signature. selfshift.py in this directory scores it numerically" +
			" (stdlib only, no PIL needed): a coherent frame correlates with itself only at" +
			" (0,0), while this one shows a second strong peak at the duplication offset.")
		fmt.Println("\nThe capture that does work here goes through the compositor over D-Bus --" +
			" see ianar/portalcapture_linux.go.")
	case rootOK || overlayOK || pixmapOK:
		fmt.Println("At least one GetImage was answered, but no probe produced a frame with" +
			" real content in it (each either decoded to entirely black or couldn't be" +
			" decoded -- see the per-probe geometry above to tell which). If they were black," +
			" the pixels are simply not in the X server and no X11 capture API can produce" +
			" them, which is the expected result under rootless XWayland.")
		fallthrough
	default:
		if isXWayland {
			fmt.Println("This display is XWayland (see the extension list above), so the" +
				" compositing happens in mutter on the Wayland side and never reaches an X" +
				" drawable. No choice of X11 drawable, rectangle, or format can fix this;" +
				" Revisions C/E/F/G/K/L were all searching a space that doesn't contain the" +
				" answer. Capture has to go through a Wayland-side route instead -- the" +
				" xdg-desktop-portal ScreenCast API (already running on this host per" +
				" Revision K's apt output) or org.gnome.Shell.Screenshot over D-Bus.")
		} else {
			fmt.Println("No capture route worked and this does not appear to be XWayland." +
				" The per-probe X errors above (which the first run never got to see) are the" +
				" thing to read: a BadMatch against a window reported IsViewable above means" +
				" something other than viewability is rejecting the drawable.")
		}
		if !rootViewable {
			fmt.Println("NOTE: the root window is not IsViewable (see its attributes above)." +
				" GetImage requires a viewable drawable, so that alone forces BadMatch and" +
				" explains the whole invariant-across-every-parameter signature.")
		}
		if !haveComposite {
			fmt.Println("NOTE: the Composite extension is absent, so the overlay probe above" +
				" was skipped entirely rather than failing.")
		}
	}
	fmt.Println("\nThe `scrot` question this diagnostic used to end on is settled: it was run" +
		" against this display and its PNG is entirely black. Revision K read scrot's zero" +
		" exit status as success and never opened the file, and that single unchecked" +
		" assumption kept the compositing-manager theory alive through five revisions. Exit" +
		" status is not evidence of content; neither is a non-black pixel count. Open the" +
		" image.")
}
