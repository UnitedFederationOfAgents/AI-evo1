//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/godbus/dbus/v5"
)

// This file records the screen with gnome-shell's own screen recorder
// (org.gnome.Shell.Screencast, the one behind GNOME's built-in screen
// recording), for clip.go's Native Clip. It records the whole stage -- every
// monitor -- to a video file gnome-shell writes itself, which works on
// Wayland where nothing else in-process can see the desktop at video rates.
// While it runs, GNOME shows its recording indicator in the top bar.
//
// Like org.gnome.Shell.Screenshot (see portalcapture_linux.go), recent GNOME
// may restrict this interface to an allowlist of callers; an AccessDenied
// answer is reported as errCompositorRecordingUnavailable so the clip falls
// back to frame sampling.
//
// init() overrides clip.go's recordViaCompositor var, so non-Linux builds
// keep the "unavailable" stub and tests can substitute their own.
func init() {
	recordViaCompositor = func(d time.Duration) ([]byte, string, error) {
		conn, err := dbus.ConnectSessionBus()
		if err != nil {
			return nil, "", fmt.Errorf("%w: connecting to the session bus: %v", errCompositorRecordingUnavailable, err)
		}
		defer conn.Close()

		dir, err := os.MkdirTemp("", "ianar-clip-*")
		if err != nil {
			return nil, "", fmt.Errorf("creating a temp dir for the recording: %w", err)
		}
		defer os.RemoveAll(dir)

		const iface = "org.gnome.Shell.Screencast"
		obj := conn.Object(iface, dbus.ObjectPath("/org/gnome/Shell/Screencast"))
		var ok bool
		var usedPath string
		// Screencast(in s file_template, in a{sv} options,
		//            out b success, out s filename_used).
		// The template has no extension: newer gnome-shell appends one to
		// match the encoder it picks, so the file is found by its directory.
		err = obj.Call(iface+".Screencast", 0, filepath.Join(dir, "clip"), map[string]dbus.Variant{
			"draw-cursor": dbus.MakeVariant(true),
			"framerate":   dbus.MakeVariant(int32(30)),
		}).Store(&ok, &usedPath)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %s.Screencast: %v", errCompositorRecordingUnavailable, iface, err)
		}
		if !ok {
			return nil, "", fmt.Errorf("%w: gnome-shell reported the recording could not start", errCompositorRecordingUnavailable)
		}
		if usedPath != "" && filepath.Dir(usedPath) != dir {
			// gnome-shell chose somewhere other than our temp dir; clean that up too.
			defer os.Remove(usedPath)
		}

		sleep(d)

		if err := obj.Call(iface+".StopScreencast", 0).Err; err != nil {
			return nil, "", fmt.Errorf("%s.StopScreencast: %w", iface, err)
		}

		path, err := waitForRecording(dir, usedPath)
		if err != nil {
			return nil, "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("reading the recording: %w", err)
		}
		return data, mimeTypeForVideo(path), nil
	}
}

// recordingFinalizeTimeout bounds the wait, after StopScreencast, for
// gnome-shell to finish writing the file.
const recordingFinalizeTimeout = 3 * time.Second

// waitForRecording returns the path of the finished recording: usedPath if
// gnome-shell reported one that exists, otherwise the largest file in dir. It
// polls until the file's size is non-zero and has stopped changing, since
// the encoder may still be flushing when StopScreencast returns.
func waitForRecording(dir, usedPath string) (string, error) {
	deadline := time.Now().Add(recordingFinalizeTimeout)
	var lastSize int64 = -1
	for {
		path, size := findRecording(dir, usedPath)
		if size > 0 && size == lastSize {
			return path, nil
		}
		lastSize = size
		if time.Now().After(deadline) {
			if size > 0 {
				return path, nil
			}
			return "", fmt.Errorf("gnome-shell did not write a recording within %s of stopping", recordingFinalizeTimeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// findRecording returns the recording's path and current size (0 if none
// has been written yet).
func findRecording(dir, usedPath string) (string, int64) {
	if usedPath != "" {
		if fi, err := os.Stat(usedPath); err == nil && fi.Mode().IsRegular() {
			return usedPath, fi.Size()
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0
	}
	var best string
	var bestSize int64
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if fi.Size() > bestSize {
			best, bestSize = filepath.Join(dir, e.Name()), fi.Size()
		}
	}
	return best, bestSize
}
