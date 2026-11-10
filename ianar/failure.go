package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

// This file is a failed sequence run's detailed report
// (condocs/dTT-IntegrationImpls/Step2Prompt.md, Revision H). A run's .zip
// shows each step's message and a downscaled picture of what it saw, which
// was not enough to tell why the condoc lifecycle demo twice waited 5
// minutes for "auto-submit once" while it was plainly on screen: was the
// IANAR on the node up to date, at what scale was the screen read, and did
// tesseract read the words at all, or read them and drop them?
//
// So the step a run fails at keeps its last screen reading, and its .zip
// gets a failure/ folder: the capture at full size, the capture with every
// line read boxed, every line read, every word both tesseract passes read
// (dropped ones included, with their confidence), and, in report.txt, which
// IANAR build and tesseract did the reading, how, and what came closest to
// the text the step was looking for. Successful runs' .zips are unchanged.

const (
	failureClosestLines = 10 // closest lines listed in the report
	failureCropW        = 960
	failureCropH        = 320
)

// seqFailure is the evidence from the step a run failed at.
type seqFailure struct {
	Step    int // 0-based
	Err     string
	Want    string         // the text the step was looking for, if any
	Reading *screenReading // its last look at the screen, nil if it didn't look
	Reads   int            // how many times it looked
	First   time.Time      // when it first looked
	At      time.Time      // when it failed
}

func newSeqFailure(step int, err error, env *seqEnv) *seqFailure {
	return &seqFailure{
		Step:    step,
		Err:     err.Error(),
		Want:    env.want,
		Reading: env.last,
		Reads:   env.reads,
		First:   env.first,
		At:      clock(),
	}
}

// ianarBuild describes this IANAR binary: its commit and build time, as Go
// stamps them, and when the executable was written.
func ianarBuild() string {
	var parts []string
	if bi, ok := debug.ReadBuildInfo(); ok {
		set := map[string]string{}
		for _, s := range bi.Settings {
			set[s.Key] = s.Value
		}
		if rev := set["vcs.revision"]; rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			if set["vcs.modified"] == "true" {
				rev += " (with uncommitted changes)"
			}
			parts = append(parts, "commit "+rev)
		}
		if t := set["vcs.time"]; t != "" {
			parts = append(parts, "committed "+t)
		}
		parts = append(parts, bi.GoVersion)
	}
	if exe, err := os.Executable(); err == nil {
		if st, err := os.Stat(exe); err == nil {
			parts = append(parts, "binary written "+st.ModTime().Format(time.RFC3339))
		}
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, ", ")
}

// tesseractVersion is the first line of `tesseract --version`. Overridable
// in tests.
var tesseractVersion = func() string {
	bin, err := exec.LookPath("tesseract")
	if err != nil {
		return "not installed"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil && first == "" {
		return fmt.Sprintf("%s (--version failed: %v)", bin, err)
	}
	return first
}

// wantTokens splits text into the normalized words to look for one by one.
func wantTokens(text string) []string {
	var out []string
	for _, f := range strings.Fields(text) {
		if n := normalizeText(f); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// nearWord reports whether a word read as got could be want misread:
// containing it, contained by it (a word split in two), or within one
// misread character per 4.
func nearWord(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	if strings.Contains(got, want) || (len(got) >= 3 && strings.Contains(want, got)) {
		return true
	}
	slack := max(1, len(want)/4)
	return editDistance(got, want, slack) <= slack
}

// lineScore is how many of tokens a line has, near enough.
func lineScore(l OCRLine, tokens []string) int {
	n := 0
	for _, t := range tokens {
		for _, w := range l.Words {
			if nearWord(normalizeText(w.Text), t) {
				n++
				break
			}
		}
	}
	return n
}

// closestLines are the lines (bridged ones included) with the most of
// want's words, best first.
func closestLines(lines []OCRLine, want string) []OCRLine {
	tokens := wantTokens(want)
	if len(tokens) == 0 {
		return nil
	}
	type scored struct {
		l OCRLine
		n int
	}
	var all []scored
	for _, l := range bridgedLines(lines) {
		if n := lineScore(l, tokens); n > 0 {
			all = append(all, scored{l, n})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return len(all[i].l.Words) < len(all[j].l.Words)
	})
	var out []OCRLine
	for _, s := range all {
		dup := false
		for _, o := range out {
			if o.rect() == s.l.rect() {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, s.l)
		}
		if len(out) == failureClosestLines {
			break
		}
	}
	return out
}

// wordKept reports whether w, from one pass, survived mergeWords.
func wordKept(w OCRWord, kept []OCRWord) bool {
	for _, k := range kept {
		if k == w {
			return true
		}
	}
	return false
}

var passNames = [2]string{"as-is", "inverted"}

// failureReport is report.txt's section on the failure, and failureFiles
// the files it refers to.
func failureReport(f *seqFailure) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nfailure detail (step %d):\n", f.Step+1)
	fmt.Fprintf(&b, "error: %s\n", f.Err)
	fmt.Fprintf(&b, "failed at: %s\n", f.At.Format(time.RFC3339))
	fmt.Fprintf(&b, "IANAR build: %s\n", ianarBuild())
	fmt.Fprintf(&b, "tesseract: %s\n", tesseractVersion())
	fmt.Fprintf(&b, "OCR settings: captures under %dpx tall read at 2x, words under %.0f%% confidence dropped, %s per tesseract pass\n",
		ocrUpscaleBelow, ocrMinConf, ocrTimeout)

	sr := f.Reading
	if sr == nil {
		b.WriteString("screen: this step didn't read the screen, so there is no capture of it\n")
		return b.String()
	}
	bd := sr.img.Bounds()
	fmt.Fprintf(&b, "screen: looked %d time(s)", f.Reads)
	if f.Reads > 1 && !f.First.IsZero() {
		fmt.Fprintf(&b, ", from %s to %s", f.First.Format("15:04:05"), sr.at.Format("15:04:05"))
	}
	fmt.Fprintf(&b, "; the last look (below) captured %dx%d via %s, read at %dx in %s\n",
		bd.Dx(), bd.Dy(), sr.from, sr.scale, sr.took.Round(time.Millisecond))
	fmt.Fprintf(&b, "read: %d word(s) kept of %d as-is and %d inverted, in %d line(s)",
		len(sr.words), len(sr.raw[0]), len(sr.raw[1]), len(sr.lines))
	if len(sr.lines) > 0 {
		hs := make([]int, len(sr.lines))
		for i, l := range sr.lines {
			hs[i] = l.H
		}
		sort.Ints(hs)
		fmt.Fprintf(&b, "; line heights %d-%dpx, median %dpx", hs[0], hs[len(hs)-1], hs[len(hs)/2])
	}
	b.WriteString("\n")

	if f.Want != "" {
		exact, partial := findLines(sr.lines, f.Want)
		fmt.Fprintf(&b, "\nlooking for %q (as %q): %d line(s) read it, %d contain it\n",
			f.Want, normalizeText(f.Want), len(exact), len(partial))
		closest := closestLines(sr.lines, f.Want)
		if len(closest) == 0 {
			b.WriteString("closest lines: none has any of its words\n")
		} else {
			b.WriteString("closest lines (most of its words first; boxed amber in failure/capture-boxed.png):\n")
			for _, l := range closest {
				fmt.Fprintf(&b, "  (%d, %d) %dx%d %q\n", l.X, l.Y, l.W, l.H, l.Text)
			}
		}
		b.WriteString("its words as each tesseract pass read them, dropped ones included:\n")
		for _, t := range wantTokens(f.Want) {
			n := 0
			for p, ws := range sr.raw {
				for _, w := range ws {
					if !nearWord(normalizeText(w.Text), t) {
						continue
					}
					n++
					state := "kept"
					switch {
					case w.Conf < ocrMinConf:
						state = "dropped: low confidence"
					case !wordKept(w, sr.words):
						state = "dropped: the other pass read it more confidently"
					}
					fmt.Fprintf(&b, "  %q: %s pass read %q at (%d, %d) %dx%d, %.0f%% confident -- %s\n",
						t, passNames[p], w.Text, w.X, w.Y, w.W, w.H, w.Conf, state)
				}
			}
			if n == 0 {
				fmt.Fprintf(&b, "  %q: not read by either pass, at any confidence\n", t)
			}
		}
	}

	b.WriteString("\nfailure/ holds: capture.png (the last look, full size), capture-boxed.png (every line read boxed grey,\n" +
		"the closest amber), lines.txt (every line read), words.tsv (every word each pass read, dropped ones\n" +
		"included)")
	if f.Want != "" && len(closestLines(sr.lines, f.Want)) > 0 {
		b.WriteString(", closest.png (full-size crop around the closest line)")
	}
	b.WriteString(".\nTo read capture.png as IANAR did: upscale it by the scale above, then\n" +
		"  tesseract <png> stdout --psm 11 -l eng tsv\n" +
		"(and again with the image inverted).\n")
	return b.String()
}

// failureFiles are the failure/ folder's files.
func failureFiles(f *seqFailure) []zipFile {
	sr := f.Reading
	if sr == nil {
		return nil
	}
	var files []zipFile
	var buf bytes.Buffer
	if err := png.Encode(&buf, sr.img); err == nil {
		files = append(files, zipFile{"failure/capture.png", buf.Bytes()})
	}

	var closest []OCRLine
	if f.Want != "" {
		closest = closestLines(sr.lines, f.Want)
	}
	boxes := map[color.RGBA][]image.Rectangle{}
	grey := color.RGBA{0x90, 0x90, 0x90, 0xff}
	for _, l := range sr.lines {
		boxes[grey] = append(boxes[grey], l.rect())
	}
	for _, l := range closest {
		boxes[otherColor] = append(boxes[otherColor], l.rect())
	}
	marked := annotate(sr.img, boxes)
	buf = bytes.Buffer{}
	if err := png.Encode(&buf, marked); err == nil {
		files = append(files, zipFile{"failure/capture-boxed.png", buf.Bytes()})
	}
	if len(closest) > 0 {
		buf = bytes.Buffer{}
		if err := png.Encode(&buf, cropAround(marked, closest[0].center(), failureCropW, failureCropH)); err == nil {
			files = append(files, zipFile{"failure/closest.png", buf.Bytes()})
		}
	}

	var lines strings.Builder
	fmt.Fprintf(&lines, "%d lines read, at (x, y) wxh in capture pixels:\n", len(sr.lines))
	for _, l := range sr.lines {
		fmt.Fprintf(&lines, "(%d, %d) %dx%d %s\n", l.X, l.Y, l.W, l.H, l.Text)
	}
	files = append(files, zipFile{"failure/lines.txt", []byte(lines.String())})

	var words strings.Builder
	words.WriteString("pass\tconf\tkept\tx\ty\tw\th\ttext\n")
	for p, ws := range sr.raw {
		for _, w := range ws {
			fmt.Fprintf(&words, "%s\t%.0f\t%t\t%d\t%d\t%d\t%d\t%s\n",
				passNames[p], w.Conf, wordKept(w, sr.words), w.X, w.Y, w.W, w.H, w.Text)
		}
	}
	files = append(files, zipFile{"failure/words.tsv", []byte(words.String())})
	return files
}
