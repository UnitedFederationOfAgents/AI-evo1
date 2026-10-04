package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"strings"
	"sync"
	"time"
)

// This file is IANAR's "Native Clip" control (condocs/initialRobotImpls/
// Step2Prompt.md): a clipDur recording of the native desktop, sent back over
// "clip-result" for the frontend to play back.
//
// The compositor's own screen recorder is tried first: it produces a real
// video file and, on a Wayland session, is the only thing that sees the
// desktop at a usable frame rate. When it isn't available (not GNOME, or
// gnome-shell refuses the caller) the clip falls back to sampling frames
// through the same paths captureNativeDisplay uses, and the frontend plays
// those frames back in sequence.

const (
	clipDur           = 4 * time.Second
	clipFrameInterval = 100 * time.Millisecond // fallback target of ~10 frames/s
	clipMaxFrameWidth = 1280                   // fallback frames are downscaled to keep the message small
	clipJPEGQuality   = 70
)

// ClipFrame is one sampled frame of a fallback clip, AtMs after the clip
// started.
type ClipFrame struct {
	ImageURL string `json:"image_url"` // data: URL
	AtMs     int64  `json:"at_ms"`
}

// ClipResultMsg is the "clip-result" WebSocket payload reporting a completed
// native clip. Exactly one of VideoURL (compositor recording) or Frames
// (sampled fallback) is set on success.
type ClipResultMsg struct {
	Success    bool        `json:"success"`
	Via        string      `json:"via,omitempty"`       // which capture path recorded the clip
	VideoURL   string      `json:"video_url,omitempty"` // data: URL
	Frames     []ClipFrame `json:"frames,omitempty"`
	DurationMs int64       `json:"duration_ms,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// handleClipNative runs recordNativeClip and reports the result back to the
// requesting client as a "clip-result" message.
func (s *Server) handleClipNative(c *wsClient) {
	s.sendToClient(c, "clip-result", recordNativeClip())
}

// clipMu allows one clip at a time. It is deliberately separate from robotMu
// so a clip doesn't block circle-mouse for its whole duration -- recording
// the pointer circling is exactly the kind of thing a clip is for.
var clipMu sync.Mutex

// errCompositorRecordingUnavailable marks a recordViaCompositor failure that
// means "this path isn't usable here", as opposed to a recording that started
// and then went wrong.
var errCompositorRecordingUnavailable = errors.New("compositor screen recording unavailable")

// recordViaCompositor records the screen for d with the compositor's own
// screen recorder and returns the video file's bytes and MIME type.
// Overridden on Linux by screencast_linux.go; unavailable elsewhere and
// overridable in tests.
var recordViaCompositor = func(d time.Duration) (data []byte, mimeType string, err error) {
	return nil, "", errCompositorRecordingUnavailable
}

// clock is overridden in tests alongside sleep so frame sampling can be
// exercised without real wall-clock time passing.
var clock = time.Now

// recordNativeClip records clipDur of the native desktop: via the compositor
// if it can, otherwise by sampling frames. Blocks for about clipDur.
func recordNativeClip() ClipResultMsg {
	if !clipMu.TryLock() {
		return ClipResultMsg{Error: "a native clip is already recording"}
	}
	defer clipMu.Unlock()

	log.Printf("robot: recording a %s native clip", clipDur)
	data, mimeType, err := recordViaCompositor(clipDur)
	if err == nil {
		return ClipResultMsg{
			Success:    true,
			Via:        "compositor (org.gnome.Shell.Screencast)",
			VideoURL:   "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data),
			DurationMs: clipDur.Milliseconds(),
		}
	}
	compositorErr := err
	log.Printf("robot: %v; sampling frames for the clip instead", compositorErr)

	frames, via, err := sampleNativeFrames(clipDur, clipFrameInterval)
	if err != nil {
		return ClipResultMsg{Error: fmt.Sprintf("native clip: compositor recording: %v; frame sampling: %v", compositorErr, err)}
	}
	return ClipResultMsg{
		Success:    true,
		Via:        "sampled frames (" + via + ")",
		Frames:     frames,
		DurationMs: clipDur.Milliseconds(),
	}
}

// sampleNativeFrames grabs a native frame every interval for d and returns
// them as downscaled JPEG data URLs, along with the capture path used. The
// first frame decides the path; later frames go straight to it rather than
// re-trying paths already known to fail (e.g. robotgo's black frame on
// XWayland) every time. If a grab fails partway through, the frames captured
// so far are returned as a shorter clip.
func sampleNativeFrames(d, interval time.Duration) ([]ClipFrame, string, error) {
	attempts := nativeCaptureAttempts()
	var frames []ClipFrame
	var via string
	start := clock()
	for {
		at := clock().Sub(start)
		if at >= d {
			break
		}
		img, from, err := grabNativeFrame(attempts)
		if err != nil {
			if len(frames) == 0 {
				return nil, "", err
			}
			log.Printf("robot: frame grab failed %s into the clip, ending it early: %v", at, err)
			break
		}
		if via == "" {
			via = from
			attempts = pinAttempt(attempts, from)
		}
		url, err := encodeClipFrame(img)
		if err != nil {
			return nil, "", err
		}
		frames = append(frames, ClipFrame{ImageURL: url, AtMs: at.Milliseconds()})
		if wait := interval - (clock().Sub(start) - at); wait > 0 {
			sleep(wait)
		}
	}
	return frames, via, nil
}

// pinAttempt narrows attempts to the one named, or leaves them as-is if none
// matches.
func pinAttempt(attempts []captureAttempt, name string) []captureAttempt {
	for _, a := range attempts {
		if a.name == name {
			return []captureAttempt{a}
		}
	}
	return attempts
}

// encodeClipFrame downscales img to at most clipMaxFrameWidth wide and
// returns it as a JPEG data URL.
func encodeClipFrame(img image.Image) (string, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, downscale(img, clipMaxFrameWidth), &jpeg.Options{Quality: clipJPEGQuality}); err != nil {
		return "", fmt.Errorf("encoding clip frame as jpeg: %w", err)
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// downscale returns img shrunk (nearest-neighbor, aspect preserved) to at
// most maxW wide, or img itself if it is already narrow enough.
func downscale(img image.Image, maxW int) image.Image {
	b := img.Bounds()
	if b.Dx() <= maxW {
		return img
	}
	w, h := maxW, max(1, b.Dy()*maxW/b.Dx())
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := b.Min.Y + y*b.Dy()/h
		for x := 0; x < w; x++ {
			out.Set(x, y, img.At(b.Min.X+x*b.Dx()/w, sy))
		}
	}
	return out
}

// mimeTypeForVideo maps a recorded file's extension to the MIME type the
// browser needs to play it back.
func mimeTypeForVideo(path string) string {
	switch {
	case strings.HasSuffix(path, ".mp4"):
		return "video/mp4"
	case strings.HasSuffix(path, ".mkv"):
		return "video/x-matroska"
	default:
		return "video/webm"
	}
}
