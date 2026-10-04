//go:build linux

package main

/*
#cgo LDFLAGS: -lX11
#include <X11/Xlib.h>
#include <stdio.h>

// xErrorHandler replaces Xlib's default error handler, which calls exit()
// on any protocol error. This one logs the error to stderr (ianar's normal
// log destination) and returns 0 so the process keeps running.
static int xErrorHandler(Display *d, XErrorEvent *e) {
	char buf[128];
	XGetErrorText(d, e->error_code, buf, sizeof(buf));
	fprintf(stderr, "ianar: X protocol error: %s "
		"(request code %d, minor code %d, serial %lu)\n",
		buf, e->request_code, e->minor_code, e->serial);
	return 0;
}

static void installXErrorHandlerC(void) {
	XSetErrorHandler(xErrorHandler);
}
*/
import "C"

// init overrides installXErrorHandler (a no-op var in robot.go for
// non-Linux builds) with the real Xlib call.
func init() {
	installXErrorHandler = func() {
		C.installXErrorHandlerC()
	}
}
