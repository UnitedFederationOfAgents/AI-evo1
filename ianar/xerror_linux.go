//go:build linux

package main

/*
#cgo LDFLAGS: -lX11
#include <X11/Xlib.h>
#include <stdio.h>

// xErrorHandler replaces Xlib's default error handler. Xlib's default
// handler calls exit() synchronously, from inside whichever Xlib call
// provoked the protocol error (e.g. the XGetImage robotgo's CaptureImg
// issues) -- see installXErrorHandler's Go-side doc comment. This one
// just logs the error's fields to stderr (ianar's normal log destination)
// and returns 0, which tells Xlib "handled, don't kill the process".
static int xErrorHandler(Display *d, XErrorEvent *e) {
	char buf[128];
	XGetErrorText(d, e->error_code, buf, sizeof(buf));
	fprintf(stderr, "ianar: X protocol error (previously would have crashed the process): %s "
		"(request code %d, minor code %d, serial %lu)\n",
		buf, e->request_code, e->minor_code, e->serial);
	return 0;
}

static void installXErrorHandlerC(void) {
	XSetErrorHandler(xErrorHandler);
}
*/
import "C"

// init overrides installXErrorHandler (declared as a no-op var in robot.go
// for non-Linux builds, following the same overridable-var pattern as
// captureScreenImg/screenSize/etc.) with the real Xlib call, on the one
// platform this subproject's native capture/input actually targets.
func init() {
	installXErrorHandler = func() {
		C.installXErrorHandlerC()
	}
}
