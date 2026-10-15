package main

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-vgo/robotgo"
)

// This file finds and focuses windows by title for sequence-v2's
// focus-window op (see seqv2_ops.go's focusByTitle) -- first written for
// the terminal window federation-command runs in, which can also be found
// by process.
//
// The window is found by sight first (focusViaVision): federation-command
// sets its terminal window's title to "federation-command" at startup, so
// IANAR reads the screen (see vision.go) for a line reading exactly that --
// the title bar -- and clicks it. That needs nothing from the window manager,
// which on GNOME Wayland gives other apps no way to list or focus windows.
//
// If that fails, the window-manager paths below are tried: matching by title,
// and otherwise by process -- the window owned by the closest ancestor of a
// running federation-command process (its terminal emulator). None of these
// can see a window for an FC running in a detached tmux/screen session,
// which has no window until something attaches to it.

const (
	fcProcessName = "federation-command"
	fcWindowTitle = "federation-command" // see federation-command/main.go's fcWindowTitle
)

// windowTarget describes the window to focus.
type windowTarget struct {
	title string // exact window title to match first (not a substring: a browser tab can mention FC too)
	pids  []int  // owning pids to match next, best first
}

// errFocusPathUnavailable marks a focus path that can't be used here at all
// (wrong desktop, interface refused), as opposed to one that ran and found
// no matching window.
var errFocusPathUnavailable = errors.New("window focus path unavailable")

// focusAttempt is one way of focusing a window, named for logs and errors.
// focus returns a description of the window it focused.
type focusAttempt struct {
	name  string
	focus func(windowTarget) (string, error)
}

// focusViaGnomeShell and focusViaWindowCalls ask gnome-shell to focus the
// window over D-Bus. Overridden on Linux by window_linux.go; unavailable
// elsewhere and overridable in tests.
var (
	focusViaGnomeShell = func(windowTarget) (string, error) {
		return "", fmt.Errorf("%w: not on this platform", errFocusPathUnavailable)
	}
	focusViaWindowCalls = func(windowTarget) (string, error) {
		return "", fmt.Errorf("%w: not on this platform", errFocusPathUnavailable)
	}
)

// focusAttempts lists the focus paths in preference order.
func focusAttempts() []focusAttempt {
	return []focusAttempt{
		{"gnome-shell (org.gnome.Shell.Eval)", focusViaGnomeShell},
		{"gnome-shell window-calls extension", focusViaWindowCalls},
		{"robotgo (X11)", focusViaRobotgo},
	}
}

// focusWindow tries each attempt in turn and reports the first success.
func focusWindow(t windowTarget, attempts []focusAttempt) (string, error) {
	var failures []string
	for _, a := range attempts {
		desc, err := a.focus(t)
		if err == nil {
			return fmt.Sprintf("%s via %s", desc, a.name), nil
		}
		log.Printf("robot: focusing %q via %s: %v", t.title, a.name, err)
		failures = append(failures, fmt.Sprintf("%s: %v", a.name, err))
	}
	return "", fmt.Errorf("could not focus the window: %s", strings.Join(failures, "; "))
}

// procRoot is overridden in tests with a fake /proc.
var procRoot = "/proc"

// findProcessPids returns the pids of running processes named name, matched
// on argv[0]'s base name or, failing that, the kernel's (15-byte truncated)
// comm.
func findProcessPids(name string) []int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	comm := name
	if len(comm) > 15 {
		comm = comm[:15]
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join(procRoot, e.Name())
		if cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil && len(cmdline) > 0 {
			argv0, _, _ := strings.Cut(string(cmdline), "\x00")
			if filepath.Base(argv0) == name {
				pids = append(pids, pid)
				continue
			}
		}
		if c, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil && strings.TrimSpace(string(c)) == comm {
			pids = append(pids, pid)
		}
	}
	return pids
}

// parentPid returns pid's parent from /proc/<pid>/stat, or 0.
func parentPid(pid int) int {
	stat, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	// The comm field is parenthesised and may contain spaces, so parse after
	// its closing paren: "<pid> (<comm>) <state> <ppid> ...".
	i := strings.LastIndexByte(string(stat), ')')
	if i < 0 {
		return 0
	}
	fields := strings.Fields(string(stat)[i+1:])
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// ancestryOf returns pids followed by their ancestors, nearest generation
// first, without duplicates, stopping short of init (pid 1).
func ancestryOf(pids []int) []int {
	seen := map[int]bool{}
	var out []int
	gen := pids
	for len(gen) > 0 {
		var next []int
		for _, p := range gen {
			if p <= 1 || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
			next = append(next, parentPid(p))
		}
		gen = next
	}
	return out
}

// selfPid is overridden in tests alongside procRoot.
var selfPid = os.Getpid

// federationCommandTarget finds the running federation-command processes and
// describes the window to focus. It fails if none is running.
//
// Ancestors FC shares with IANAR itself (e.g. a local-representative that
// launched both, the terminal that's running it, the session manager) are
// left out of the pid match: their windows aren't FC's terminal, and typing
// into one by mistake would run the sequence's command somewhere else.
func federationCommandTarget() (windowTarget, error) {
	pids := findProcessPids(fcProcessName)
	if len(pids) == 0 {
		return windowTarget{}, fmt.Errorf("no %s process is running", fcProcessName)
	}
	shared := map[int]bool{}
	for _, p := range ancestryOf([]int{selfPid()}) {
		shared[p] = true
	}
	var own []int
	for _, p := range ancestryOf(pids) {
		if !shared[p] {
			own = append(own, p)
		}
	}
	return windowTarget{title: fcWindowTitle, pids: own}, nil
}

// focusFederationCommand finds federation-command's terminal window and
// focuses it: by sight first, then through the window manager. It also
// returns an image of what visual detection saw, if it got that far.
// Overridable in tests.
var focusFederationCommand = func() (string, string, error) {
	desc, shot, visErr := focusViaVision(fcWindowTitle)
	if visErr == nil {
		return desc, shot, nil
	}
	log.Printf("robot: visual detection of %s's window failed (%v); trying the window manager", fcProcessName, visErr)
	t, err := federationCommandTarget()
	if err == nil {
		desc, err = focusWindow(t, focusAttempts())
		if err == nil {
			return fmt.Sprintf("%s (visual detection failed: %v)", desc, visErr), shot, nil
		}
	}
	return "", shot, fmt.Errorf("visual detection: %v; window-manager fallback: %v", visErr, err)
}

// focusViaVision finds the window titled title on screen by reading it (see
// vision.go) and clicks its title to focus it. It reports what it did and a
// JPEG data: URL showing what it saw: a crop around the title it clicked,
// boxed, or on failure the whole screen with any near misses boxed.
//
// Only a line reading exactly title counts, not one merely containing it --
// IANAR's own sequence-v2 tab mentions federation-command, as can a browser
// tab or terminal output. If several lines match (FC in two terminals, or a
// tab label as well as the window title), the top-most is clicked.
func focusViaVision(title string) (desc, shot string, err error) {
	sr, err := readScreen()
	if err != nil {
		return "", "", err
	}
	exact, partial := findLines(sr.lines, title)
	var near []image.Rectangle
	for _, l := range partial {
		near = append(near, l.rect())
	}
	if len(exact) == 0 {
		shot = jpegDataURL(annotate(sr.img, map[color.RGBA][]image.Rectangle{otherColor: near}), inspectMaxWidth)
		var seen []string
		for _, l := range partial {
			seen = append(seen, fmt.Sprintf("%q", l.Text))
		}
		msg := fmt.Sprintf("no line on screen reads %q (read %d lines of text)", title, len(sr.lines))
		if len(seen) > 0 {
			msg += "; lines containing it: " + strings.Join(seen, ", ")
		}
		return "", shot, errors.New(msg)
	}

	hit := exact[0]
	for _, l := range exact[1:] {
		near = append(near, l.rect())
	}
	marked := annotate(sr.img, map[color.RGBA][]image.Rectangle{matchColor: {hit.rect()}, otherColor: near})
	at := hit.center()
	shot = jpegDataURL(cropAround(marked, at, matchCropW, matchCropH), matchCropW)

	via, err := clickAt(at.X, at.Y, sr.img.Bounds().Dx())
	if err != nil {
		return "", shot, fmt.Errorf("found %q at (%d, %d) but clicking it via %s failed: %w", hit.Text, at.X, at.Y, via, err)
	}
	sleep(focusSettle)
	desc = fmt.Sprintf("saw %q at (%d, %d) and clicked it via %s", hit.Text, at.X, at.Y, via)
	if len(exact) > 1 {
		desc += fmt.Sprintf(" (top-most of %d matches)", len(exact))
	}
	return desc, shot, nil
}

// focusViaRobotgo activates the X11 window owned by the best-ranked pid that
// has one (robotgo matches _NET_WM_PID). It only sees X11/XWayland windows;
// the title isn't used to pick since robotgo can only look windows up by pid.
func focusViaRobotgo(t windowTarget) (string, error) {
	robotMu.Lock()
	defer robotMu.Unlock()
	for _, pid := range t.pids {
		if err := robotgo.ActivePid(pid); err != nil {
			continue
		}
		sleep(focusSettle)
		return fmt.Sprintf("%q (pid %d)", robotgo.GetTitle(), pid), nil
	}
	return "", fmt.Errorf("no X11 window belongs to %s or its terminal (pids %v)", fcProcessName, t.pids)
}
