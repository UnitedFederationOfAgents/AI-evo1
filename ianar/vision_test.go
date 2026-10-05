package main

import (
	"errors"
	"image"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseTesseractTSV(t *testing.T) {
	tsv := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		"1\t1\t0\t0\t0\t0\t0\t0\t1920\t1080\t-1\t\n" +
		"5\t1\t1\t1\t1\t1\t100\t20\t180\t16\t93.5\tfederation-command\n" +
		"5\t1\t2\t1\t1\t1\t10\t500\t40\t14\t88\t\"quoted\n" +
		"5\t1\t3\t1\t1\t1\t10\t600\t40\t14\t-1\t \r\n"
	got := parseTesseractTSV([]byte(tsv))
	want := []OCRWord{
		{Text: "federation-command", Conf: 93.5, X: 100, Y: 20, W: 180, H: 16},
		{Text: `"quoted`, Conf: 88, X: 10, Y: 500, W: 40, H: 14},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseTesseractTSV = %+v, want %+v", got, want)
	}
}

func TestMergeWordsKeepsTheMoreConfidentReading(t *testing.T) {
	plain := []OCRWord{
		{Text: "federatlon-command", Conf: 51, X: 100, Y: 20, W: 180, H: 16},
		{Text: "~~", Conf: 90, X: 900, Y: 20, W: 20, H: 16}, // no letters or digits
		{Text: "faint", Conf: 12, X: 300, Y: 300, W: 40, H: 16},
	}
	inverted := []OCRWord{
		{Text: "federation-command", Conf: 95, X: 101, Y: 21, W: 178, H: 15},
		{Text: "hello", Conf: 80, X: 10, Y: 500, W: 40, H: 14},
	}
	got := mergeWords(plain, inverted)
	var texts []string
	for _, w := range got {
		texts = append(texts, w.Text)
	}
	if want := []string{"federation-command", "hello"}; !reflect.DeepEqual(texts, want) {
		t.Errorf("merged words = %q, want %q", texts, want)
	}
}

func TestGroupLines(t *testing.T) {
	words := []OCRWord{
		// A browser tab's text and, far to its right, a terminal title on
		// the same row: two lines, not one.
		{Text: "command:", X: 190, Y: 22, W: 80, H: 16},
		{Text: "federation", X: 100, Y: 20, W: 84, H: 16},
		{Text: "hello", X: 276, Y: 21, W: 50, H: 16},
		{Text: "federation-command", X: 900, Y: 20, W: 180, H: 16},
		// A line further down.
		{Text: "world", X: 50, Y: 300, W: 50, H: 16},
	}
	lines := groupLines(words)
	var texts []string
	for _, l := range lines {
		texts = append(texts, l.Text)
	}
	if want := []string{"federation command: hello", "federation-command", "world"}; !reflect.DeepEqual(texts, want) {
		t.Fatalf("lines = %q, want %q", texts, want)
	}
	if r := lines[0].rect(); r != image.Rect(100, 20, 326, 38) {
		t.Errorf("first line's box = %v", r)
	}
	if c := lines[1].center(); c != image.Pt(990, 28) {
		t.Errorf("title center = %v", c)
	}
}

func TestFindLinesMatchesWholeLinesOnly(t *testing.T) {
	lines := []OCRLine{
		{Text: "federation-command: hello world", Y: 5},
		{Text: "Federation—Command", Y: 20},
		{Text: "Select the terminal with federation-command", Y: 400},
		{Text: "federation command", Y: 700},
	}
	exact, partial := findLines(lines, "federation-command")
	if len(exact) != 2 || exact[0].Y != 20 || exact[1].Y != 700 {
		t.Errorf("exact = %+v", exact)
	}
	if len(partial) != 2 {
		t.Errorf("partial = %+v", partial)
	}
	if e, p := findLines(lines, " - "); e != nil || p != nil {
		t.Errorf("an empty query matched: %+v %+v", e, p)
	}
}

// TestWrappedLinesFindsAWrappedLabel: Revision K's desktop icon label,
// wrapped by GNOME as "ianar-hello-wor" over "ld.txt".
func TestWrappedLinesFindsAWrappedLabel(t *testing.T) {
	lines := []OCRLine{
		{Text: "ianar-hello-wor", X: 1200, Y: 500, W: 60, H: 12},
		{Text: "ld.txt", X: 1215, Y: 514, W: 30, H: 12},
		{Text: "ld.txt", X: 100, Y: 514, W: 30, H: 12},  // not under it
		{Text: "ld.txt", X: 1215, Y: 600, W: 30, H: 12}, // too far below
		{Text: "ianar-hello-world.txt", X: 10, Y: 10, W: 90, H: 12},
	}
	got := wrappedLines(lines, "ianar-hello-world.txt")
	if len(got) != 1 || got[0].Text != "ianar-hello-wor / ld.txt" || got[0].rect() != image.Rect(1200, 500, 1260, 526) {
		t.Errorf("wrappedLines = %+v", got)
	}
	if got := wrappedLines(lines[:2], "hello"); got != nil {
		t.Errorf("text on one line counted as wrapped: %+v", got)
	}
}

// TestReadScreenTextReadsBothWaysAndScales checks the capture is read as is
// and inverted, at 2x, and that word boxes come back in capture pixels.
func TestReadScreenTextReadsBothWaysAndScales(t *testing.T) {
	orig := runOCR
	t.Cleanup(func() { runOCR = orig })
	var calls atomic.Int32
	runOCR = func(png []byte) ([]OCRWord, error) {
		calls.Add(1)
		return []OCRWord{{Text: "federation-command", Conf: 90, X: 200, Y: 40, W: 360, H: 32}}, nil
	}
	words, err := readScreenText(image.NewRGBA(image.Rect(0, 0, 1280, 720)))
	if err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("OCR ran %d times, want 2 (as is and inverted)", n)
	}
	want := []OCRWord{{Text: "federation-command", Conf: 90, X: 100, Y: 20, W: 180, H: 16}}
	if !reflect.DeepEqual(words, want) {
		t.Errorf("words = %+v, want %+v", words, want)
	}
}

// TestReadScreenTextScalesWideCaptures checks two 1080p monitors side by
// side (3840 wide) are still read at 2x: their text is as small as on one.
func TestReadScreenTextScalesWideCaptures(t *testing.T) {
	orig := runOCR
	t.Cleanup(func() { runOCR = orig })
	runOCR = func(png []byte) ([]OCRWord, error) {
		return []OCRWord{{Text: "Window", Conf: 90, X: 200, Y: 40, W: 120, H: 24}}, nil
	}
	words, err := readScreenText(image.NewGray(image.Rect(0, 0, 3840, 1080)))
	if err != nil {
		t.Fatal(err)
	}
	if len(words) != 1 || words[0].X != 100 || words[0].H != 12 {
		t.Errorf("words = %+v, want boxes halved from a 2x reading", words)
	}
}

func TestSameTextAllowsOCRSlips(t *testing.T) {
	for _, c := range []struct {
		got, want string
		same      bool
	}{
		{"newprivatewindow", "newprivatewindow", true},
		{"newprlvatewindow", "newprivatewindow", true},  // one misread
		{"newprivatewndow", "newprivatewindow", true},   // one dropped
		{"newprivatewindows", "newprivatewindow", true}, // one extra
		{"nevprlvatewindow", "newprivatewindow", false}, // two slips in 16
		{"newwindow", "newprivatewindow", false},
		{"ianarhelloworldtx", "ianarhelloworldtxt", true},
		{"stop", "step", false}, // short texts must match exactly
	} {
		if got := sameText(c.got, c.want); got != c.same {
			t.Errorf("sameText(%q, %q) = %v, want %v", c.got, c.want, got, c.same)
		}
	}
}

func TestReadScreenTextReportsMissingOCR(t *testing.T) {
	orig := runOCR
	t.Cleanup(func() { runOCR = orig })
	runOCR = func([]byte) ([]OCRWord, error) { return nil, errOCRUnavailable }
	if _, err := readScreenText(image.NewRGBA(image.Rect(0, 0, 10, 10))); !errors.Is(err, errOCRUnavailable) {
		t.Errorf("err = %v, want errOCRUnavailable", err)
	}
}

func TestPrepareForOCR(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 2, 1))
	img.Pix[0], img.Pix[1] = 0, 200
	got := prepareForOCR(img, 2, true)
	if got.Bounds() != image.Rect(0, 0, 4, 2) {
		t.Fatalf("bounds = %v", got.Bounds())
	}
	if want := []uint8{255, 255, 55, 55, 255, 255, 55, 55}; !reflect.DeepEqual(got.Pix, want) {
		t.Errorf("pixels = %v, want %v", got.Pix, want)
	}
}

// stubVision stubs the screen reading, pointer and display size for
// focusViaVision, restoring everything when the test ends. The fake screen
// is 1280 px wide; the pointer sees it at half size (a 2x scaled display).
func stubVision(t *testing.T, lines []OCRLine) *[]string {
	t.Helper()
	origRead, origPtr, origSize, origSleep := readScreen, openCompositorPointer, screenSize, sleep
	t.Cleanup(func() {
		readScreen, openCompositorPointer, screenSize, sleep = origRead, origPtr, origSize, origSleep
	})
	sleep = func(time.Duration) {}
	screenSize = func() (int, int) { return 640, 360 }
	readScreen = func() (*screenReading, error) {
		return &screenReading{img: image.NewRGBA(image.Rect(0, 0, 1280, 720)), from: "test", lines: lines}, nil
	}
	var calls []string
	openCompositorPointer = func() (compositorPointer, func(), error) {
		return compositorPointer{
			moveBy: func(dx, dy float64) error {
				calls = append(calls, "move "+ftoa(dx)+","+ftoa(dy))
				return nil
			},
			button: func(code int32, pressed bool) error {
				calls = append(calls, map[bool]string{true: "press", false: "release"}[pressed])
				return nil
			},
		}, func() { calls = append(calls, "stop") }, nil
	}
	return &calls
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func TestFocusViaVisionClicksTheTitle(t *testing.T) {
	calls := stubVision(t, []OCRLine{
		{Text: "federation-command: hello world", X: 10, Y: 5, W: 300, H: 16},
		{Text: "federation-command", X: 500, Y: 12, W: 180, H: 16},
		{Text: "federation-command", X: 500, Y: 600, W: 180, H: 16},
	})
	desc, shot, err := focusViaVision("federation-command")
	if err != nil {
		t.Fatal(err)
	}
	// Home to the top-left of the 640x360 pointer space in steps (5 left,
	// 3 up), then to the title's center (590, 20) at half scale.
	want := []string{
		"move 0,-200", "move -200,0",
		"move 0,-200", "move -200,0",
		"move 0,-200", "move -200,0",
		"move -200,0", "move -200,0",
		"move -100000,-100000", "move 295,10", "press", "release", "stop",
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Errorf("pointer calls = %q, want %q", *calls, want)
	}
	if !strings.Contains(desc, "(590, 20)") || !strings.Contains(desc, "top-most of 2") {
		t.Errorf("desc = %q", desc)
	}
	if !strings.HasPrefix(shot, "data:image/jpeg;base64,") {
		t.Errorf("shot = %.40q", shot)
	}
}

func TestFocusViaVisionReportsNearMisses(t *testing.T) {
	calls := stubVision(t, []OCRLine{
		{Text: "Select the terminal with federation-command", X: 10, Y: 400, W: 400, H: 16},
	})
	_, shot, err := focusViaVision("federation-command")
	if err == nil || !strings.Contains(err.Error(), `"Select the terminal with federation-command"`) {
		t.Errorf("err = %v, want it to list the near miss", err)
	}
	if shot == "" {
		t.Error("a failed search should still show what was seen")
	}
	if len(*calls) != 0 {
		t.Errorf("clicked without a match: %q", *calls)
	}
}

func TestClickAtFallsBackToRobotgo(t *testing.T) {
	stubVision(t, nil)
	openCompositorPointer = func() (compositorPointer, func(), error) {
		return compositorPointer{}, nil, errCompositorInputUnavailable
	}
	origClick := robotClick
	t.Cleanup(func() { robotClick = origClick })
	var at image.Point
	robotClick = func(x, y int, _ string, _ bool) error { at = image.Pt(x, y); return nil }

	via, err := clickAt(101, 50, 1280)
	if err != nil || via != "robotgo" {
		t.Fatalf("clickAt = %q, %v", via, err)
	}
	if at != image.Pt(51, 25) {
		t.Errorf("clicked at %v, want (51, 25)", at)
	}
}

// TestClickAtAcrossMonitors models Mutter's pointer constraint on two
// side-by-side 1920x1080 monitors: a relative move ending on no monitor is
// clamped to the monitor the pointer started on. Starting on the right
// monitor, a click on the left one (Firefox's dock icon) must land there --
// not a monitor's width to the right, as it did in Revision H's debug runs.
func TestClickAtAcrossMonitors(t *testing.T) {
	stubVision(t, nil)
	screenSize = func() (int, int) { return 3840, 1080 }
	monitors := []image.Rectangle{image.Rect(0, 0, 1920, 1080), image.Rect(1920, 0, 3840, 1080)}
	pos := image.Pt(3000, 500)
	var pressedAt []image.Point
	openCompositorPointer = func() (compositorPointer, func(), error) {
		return compositorPointer{
			moveBy: func(dx, dy float64) error {
				to := image.Pt(pos.X+int(dx), pos.Y+int(dy))
				for _, m := range monitors {
					if to.In(m) {
						pos = to
						return nil
					}
				}
				for _, m := range monitors {
					if pos.In(m) {
						pos = image.Pt(min(max(to.X, m.Min.X), m.Max.X-1), min(max(to.Y, m.Min.Y), m.Max.Y-1))
						return nil
					}
				}
				return nil
			},
			button: func(_ int32, pressed bool) error {
				if pressed {
					pressedAt = append(pressedAt, pos)
				}
				return nil
			},
		}, func() {}, nil
	}

	for _, target := range []image.Point{{36, 60}, {2880, 156}} {
		pressedAt = nil
		if _, err := clickAtWith(target.X, target.Y, 3840, "right", false); err != nil {
			t.Fatal(err)
		}
		if len(pressedAt) != 1 || pressedAt[0] != target {
			t.Errorf("click for %v pressed at %v", target, pressedAt)
		}
	}
}

func TestPointerScale(t *testing.T) {
	orig := screenSize
	t.Cleanup(func() { screenSize = orig })
	for _, c := range []struct {
		screenW, captureW int
		want              float64
	}{
		{1920, 1920, 1},
		{1280, 2560, 0.5},
		{0, 1920, 1},    // no display to ask
		{3840, 1920, 1}, // implausible: pointer space larger than the capture
	} {
		screenSize = func() (int, int) { return c.screenW, 0 }
		if got := pointerScale(c.captureW); got != c.want {
			t.Errorf("pointerScale(screen %d, capture %d) = %v, want %v", c.screenW, c.captureW, got, c.want)
		}
	}
}

func TestCropAroundStaysInside(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	if got := cropAround(img, image.Pt(10, 10), 960, 320).Bounds(); got != image.Rect(0, 0, 960, 320) {
		t.Errorf("top-left crop = %v", got)
	}
	if got := cropAround(img, image.Pt(1270, 710), 960, 320).Bounds(); got != image.Rect(320, 400, 1280, 720) {
		t.Errorf("bottom-right crop = %v", got)
	}
}
