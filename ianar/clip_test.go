package main

import (
	"errors"
	"image"
	"strings"
	"testing"
	"time"
)

// stubClipPaths stubs every capture path (see stubCapturePaths), makes the
// compositor recorder unavailable, and swaps clock/sleep for a fake clock
// that only advances when slept on, restoring everything when the test ends.
func stubClipPaths(t *testing.T) {
	t.Helper()
	stubCapturePaths(t)
	origRecord, origClock, origSleep := recordViaCompositor, clock, sleep
	t.Cleanup(func() {
		recordViaCompositor, clock, sleep = origRecord, origClock, origSleep
	})
	recordViaCompositor = func(time.Duration) ([]byte, string, error) {
		return nil, "", errCompositorRecordingUnavailable
	}
	now := time.Unix(0, 0)
	clock = func() time.Time { return now }
	sleep = func(d time.Duration) { now = now.Add(d) }
}

func TestRecordNativeClipPrefersCompositor(t *testing.T) {
	stubClipPaths(t)
	recordViaCompositor = func(d time.Duration) ([]byte, string, error) {
		if d != clipDur {
			t.Errorf("recorded for %s, want %s", d, clipDur)
		}
		return []byte("foo"), "video/webm", nil
	}

	res := recordNativeClip()
	if !res.Success || res.VideoURL != "data:video/webm;base64,Zm9v" || len(res.Frames) != 0 {
		t.Errorf("unexpected result: %+v", res)
	}
}

// TestRecordNativeClipFallsBackToFrames verifies frame sampling covers the
// whole clip at clipFrameInterval, and pins later frames to the capture path
// the first frame came from.
func TestRecordNativeClipFallsBackToFrames(t *testing.T) {
	stubClipPaths(t)
	robotgoCalls := 0
	captureScreenImg = func() (image.Image, error) {
		robotgoCalls++
		return image.NewRGBA(image.Rect(0, 0, 4, 4)), nil // black, as on XWayland
	}
	captureViaPortal = func() (image.Image, error) { return litImage(4, 4), nil }

	res := recordNativeClip()
	if !res.Success || res.VideoURL != "" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if want := int(clipDur / clipFrameInterval); len(res.Frames) != want {
		t.Errorf("got %d frames, want %d", len(res.Frames), want)
	}
	for i, f := range res.Frames {
		if want := int64(i) * clipFrameInterval.Milliseconds(); f.AtMs != want {
			t.Errorf("frame %d at %dms, want %dms", i, f.AtMs, want)
		}
		if !strings.HasPrefix(f.ImageURL, "data:image/jpeg;base64,") {
			t.Errorf("frame %d isn't a jpeg data URL", i)
		}
	}
	if robotgoCalls != 1 {
		t.Errorf("robotgo was tried %d times, want once before pinning to the portal", robotgoCalls)
	}
	if !strings.Contains(res.Via, "D-Bus compositor screenshot") {
		t.Errorf("via = %q, want it to name the portal path", res.Via)
	}
}

// TestRecordNativeClipKeepsFramesOnMidClipFailure verifies a grab failing
// partway through ends the clip early instead of discarding it.
func TestRecordNativeClipKeepsFramesOnMidClipFailure(t *testing.T) {
	stubClipPaths(t)
	calls := 0
	captureScreenImg = func() (image.Image, error) {
		calls++
		if calls > 3 {
			return nil, errors.New("boom")
		}
		return litImage(4, 4), nil
	}

	res := recordNativeClip()
	if !res.Success || len(res.Frames) != 3 {
		t.Errorf("want a 3-frame clip, got %+v", res)
	}
}

func TestRecordNativeClipReportsEveryFailure(t *testing.T) {
	stubClipPaths(t)
	recordViaCompositor = func(time.Duration) ([]byte, string, error) {
		return nil, "", errors.New("recorder boom")
	}
	captureScreenImg = func() (image.Image, error) { return nil, errors.New("primary boom") }
	captureViaPortal = func() (image.Image, error) { return nil, errors.New("portal boom") }

	res := recordNativeClip()
	if res.Success {
		t.Fatalf("expected failure, got %+v", res)
	}
	for _, want := range []string{"recorder boom", "primary boom", "portal boom"} {
		if !strings.Contains(res.Error, want) {
			t.Errorf("error = %q, want it to mention %q", res.Error, want)
		}
	}
}

func TestRecordNativeClipRejectsConcurrentClip(t *testing.T) {
	stubClipPaths(t)
	clipMu.Lock()
	defer clipMu.Unlock()

	if res := recordNativeClip(); res.Success || res.Error == "" {
		t.Errorf("expected a concurrent clip to be rejected, got %+v", res)
	}
}

func TestHandleClipNativeReportsResult(t *testing.T) {
	stubClipPaths(t)
	recordViaCompositor = func(time.Duration) ([]byte, string, error) { return []byte("foo"), "video/webm", nil }
	s := newServer()
	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}

	s.handleClipNative(c)
	var p ClipResultMsg
	decodeSent(t, c, "clip-result", &p)
	if !p.Success || p.VideoURL == "" || p.DurationMs != clipDur.Milliseconds() {
		t.Errorf("unexpected result: %+v", p)
	}
}

func TestDownscale(t *testing.T) {
	small := litImage(4, 2)
	if got := downscale(small, 8); got != image.Image(small) {
		t.Errorf("an image already narrow enough should be returned as-is")
	}
	got := downscale(litImage(3840, 1080), 1280).Bounds()
	if got.Dx() != 1280 || got.Dy() != 360 {
		t.Errorf("downscaled to %dx%d, want 1280x360", got.Dx(), got.Dy())
	}
}

func TestMimeTypeForVideo(t *testing.T) {
	for path, want := range map[string]string{
		"/tmp/clip.webm": "video/webm",
		"/tmp/clip.mp4":  "video/mp4",
		"/tmp/clip.mkv":  "video/x-matroska",
		"/tmp/clip":      "video/webm",
	} {
		if got := mimeTypeForVideo(path); got != want {
			t.Errorf("mimeTypeForVideo(%q) = %q, want %q", path, got, want)
		}
	}
}
