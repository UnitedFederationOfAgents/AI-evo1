package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// This file is IANAR's visual inspection (condocs/initialRobotImpls/
// Step2Prompt.md, Revision C): read the text on screen, and where it is,
// from a native screen capture. Sequence-v2's focus-window op uses it to find
// federation-command's window by its title bar (see window.go's
// focusViaVision), and the simple tab's "Inspect Screen" exposes it
// directly ("inspect-screen" -> "inspect-result") so what IANAR sees can be
// checked by eye.
//
// Text is read by the tesseract OCR engine (the `tesseract` command, from
// the tesseract-ocr package), run as a subprocess so the build needs no cgo
// OCR bindings. Each capture is read twice, in parallel -- as is and with
// its luminance inverted -- because tesseract reads dark text on a light
// background best, and dark-theme title bars and terminals are the reverse.
// The two readings are merged, keeping the more confident word wherever they
// overlap. Words are then grouped into lines by position, which is what
// matching is done against.

const (
	ocrUpscaleBelow = 1600             // captures shorter than this are read at 2x, so small UI text is legible to tesseract
	ocrTimeout      = 45 * time.Second // per tesseract run
	ocrMinConf      = 30.0             // words read with less confidence (0-100) are dropped
	inspectMaxWidth = 1280             // inspect-result's image is downscaled to keep the message small
	matchCropW      = 960              // size of the crop around a match shown with a step's result
	matchCropH      = 320
)

// OCRWord is one word read off the screen, in capture pixel coordinates.
type OCRWord struct {
	Text string  `json:"text"`
	Conf float64 `json:"conf"`
	X    int     `json:"x"`
	Y    int     `json:"y"`
	W    int     `json:"w"`
	H    int     `json:"h"`
}

func (w OCRWord) rect() image.Rectangle { return image.Rect(w.X, w.Y, w.X+w.W, w.Y+w.H) }

// OCRLine is a run of words on the same baseline, close enough together to
// read as one phrase (a window title, a button label, a line of terminal
// output).
type OCRLine struct {
	Text  string    `json:"text"`
	X     int       `json:"x"`
	Y     int       `json:"y"`
	W     int       `json:"w"`
	H     int       `json:"h"`
	Words []OCRWord `json:"-"`
}

func (l OCRLine) rect() image.Rectangle { return image.Rect(l.X, l.Y, l.X+l.W, l.Y+l.H) }

// center is where a click on the line lands.
func (l OCRLine) center() image.Point { return image.Pt(l.X+l.W/2, l.Y+l.H/2) }

// screenReading is one capture and the text read off it.
type screenReading struct {
	img   image.Image
	from  string // capture path that produced img
	words []OCRWord
	lines []OCRLine
}

// errOCRUnavailable marks an OCR engine that isn't installed or can't run.
var errOCRUnavailable = errors.New("OCR engine unavailable")

// runOCR reads the words in a PNG image. Overridable in tests.
var runOCR = tesseractOCR

// tesseractOCR runs `tesseract <png> stdout --psm 11 tsv`. Page segmentation
// mode 11 ("sparse text") suits screenshots: find as much text as possible,
// in no particular layout.
func tesseractOCR(pngData []byte) ([]OCRWord, error) {
	bin, err := exec.LookPath("tesseract")
	if err != nil {
		return nil, fmt.Errorf("%w: the tesseract command isn't installed (sudo apt install tesseract-ocr)", errOCRUnavailable)
	}
	f, err := os.CreateTemp("", "ianar-ocr-*.png")
	if err != nil {
		return nil, fmt.Errorf("creating a temp file for OCR: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(pngData); err != nil {
		f.Close()
		return nil, fmt.Errorf("writing the OCR input: %w", err)
	}
	f.Close()

	ctx, cancel := context.WithTimeout(context.Background(), ocrTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, f.Name(), "stdout", "--psm", "11", "-l", "eng", "tsv")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tesseract: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseTesseractTSV(out), nil
}

// parseTesseractTSV extracts the word rows (level 5) of tesseract's TSV
// output: level page_num block_num par_num line_num word_num left top width
// height conf text. It splits by hand rather than with encoding/csv: a word
// can start with a quote character, which csv would take as the start of a
// quoted field.
func parseTesseractTSV(data []byte) []OCRWord {
	var words []OCRWord
	for _, row := range strings.Split(string(data), "\n") {
		rec := strings.SplitN(strings.TrimRight(row, "\r"), "\t", 12)
		if len(rec) < 12 || rec[0] != "5" {
			continue
		}
		text := strings.TrimSpace(rec[11])
		if text == "" {
			continue
		}
		n := make([]int, 4)
		for i := range n {
			n[i], _ = strconv.Atoi(rec[6+i])
		}
		conf, _ := strconv.ParseFloat(rec[10], 64)
		words = append(words, OCRWord{Text: text, Conf: conf, X: n[0], Y: n[1], W: n[2], H: n[3]})
	}
	return words
}

// prepareForOCR returns img as grayscale, scaled by scale, optionally with
// its luminance inverted.
func prepareForOCR(img image.Image, scale int, invert bool) *image.Gray {
	b := img.Bounds()
	out := image.NewGray(image.Rect(0, 0, b.Dx()*scale, b.Dy()*scale))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			v := color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.Gray).Y
			if invert {
				v = 255 - v
			}
			for dy := 0; dy < scale; dy++ {
				row := out.Pix[(y*scale+dy)*out.Stride:]
				for dx := 0; dx < scale; dx++ {
					row[x*scale+dx] = v
				}
			}
		}
	}
	return out
}

// readScreenText reads the words in img: as is and inverted, in parallel,
// merged. Coordinates are in img's pixels, relative to its origin.
//
// Whether to upscale goes by the capture's height, not its width: two
// 1080p monitors side by side make a 3840-wide capture whose text is just as
// small as on one, and read at 1x tesseract missed menu items like Firefox's
// "New Private Window" (Revision I's debug run).
func readScreenText(img image.Image) ([]OCRWord, error) {
	scale := 1
	if img.Bounds().Dy() < ocrUpscaleBelow {
		scale = 2
	}
	var wg sync.WaitGroup
	results := make([][]OCRWord, 2)
	errs := make([]error, 2)
	for i, invert := range []bool{false, true} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf bytes.Buffer
			if err := png.Encode(&buf, prepareForOCR(img, scale, invert)); err != nil {
				errs[i] = fmt.Errorf("encoding the OCR input: %w", err)
				return
			}
			words, err := runOCR(buf.Bytes())
			if err != nil {
				errs[i] = err
				return
			}
			for j := range words {
				words[j].X /= scale
				words[j].Y /= scale
				words[j].W = max(1, words[j].W/scale)
				words[j].H = max(1, words[j].H/scale)
			}
			results[i] = words
		}()
	}
	wg.Wait()
	if errs[0] != nil && errs[1] != nil {
		return nil, errs[0]
	}
	return mergeWords(results[0], results[1]), nil
}

// mergeWords combines two readings of the same image, dropping words below
// ocrMinConf and, where words from the two readings overlap, keeping the
// more confident one.
func mergeWords(a, b []OCRWord) []OCRWord {
	var all []OCRWord
	for _, w := range append(append([]OCRWord{}, a...), b...) {
		if w.Conf >= ocrMinConf && strings.TrimFunc(w.Text, notWordRune) != "" {
			all = append(all, w)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Conf > all[j].Conf })
	var kept []OCRWord
	for _, w := range all {
		dup := false
		for _, k := range kept {
			if overlapFrac(w.rect(), k.rect()) > 0.5 {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, w)
		}
	}
	return kept
}

func notWordRune(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

// overlapFrac is the area of a∩b as a fraction of the smaller rectangle.
func overlapFrac(a, b image.Rectangle) float64 {
	in := a.Intersect(b)
	if in.Empty() {
		return 0
	}
	small := min(a.Dx()*a.Dy(), b.Dx()*b.Dy())
	if small <= 0 {
		return 0
	}
	return float64(in.Dx()*in.Dy()) / float64(small)
}

// groupLines groups words into lines: words whose vertical centers are
// within half a word height of each other, with a horizontal gap of at most
// 1.2 word heights between neighbors. Wider gaps start a new line, so text in
// two windows side by side doesn't read as one phrase. Lines are ordered top
// to bottom, then left to right.
func groupLines(words []OCRWord) []OCRLine {
	ws := append([]OCRWord{}, words...)
	sort.Slice(ws, func(i, j int) bool {
		if ws[i].X != ws[j].X {
			return ws[i].X < ws[j].X
		}
		return ws[i].Y < ws[j].Y
	})
	var lines []OCRLine
	for _, w := range ws {
		cy := w.Y + w.H/2
		placed := false
		for i := range lines {
			l := &lines[i]
			last := l.Words[len(l.Words)-1]
			h := max(last.H, w.H)
			lcy := last.Y + last.H/2
			gap := w.X - (last.X + last.W)
			if abs(cy-lcy)*2 <= h && gap <= h*6/5 && gap >= -h {
				l.Words = append(l.Words, w)
				placed = true
				break
			}
		}
		if !placed {
			lines = append(lines, OCRLine{Words: []OCRWord{w}})
		}
	}
	for i := range lines {
		l := &lines[i]
		r := l.Words[0].rect()
		texts := make([]string, len(l.Words))
		for j, w := range l.Words {
			r = r.Union(w.rect())
			texts[j] = w.Text
		}
		l.Text = strings.Join(texts, " ")
		l.X, l.Y, l.W, l.H = r.Min.X, r.Min.Y, r.Dx(), r.Dy()
	}
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].Y != lines[j].Y {
			return lines[i].Y < lines[j].Y
		}
		return lines[i].X < lines[j].X
	})
	return lines
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// normalizeText reduces s to lowercase letters and digits, so matching
// shrugs off what OCR gets wrong most: punctuation (a hyphen read as a
// dash), spacing, and case.
func normalizeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !notWordRune(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// sameText reports whether two normalizeText'd strings read the same,
// allowing one misread, dropped or extra character per 10 in longer texts
// (OCR reading "Wlndow" for "Window", say). Texts under 8 characters must
// match exactly.
func sameText(got, want string) bool {
	if got == want {
		return true
	}
	slack := len(want) / 10
	if len(want) < 8 || slack == 0 {
		return false
	}
	return editDistance(got, want, slack) <= slack
}

// editDistance is the Levenshtein distance between a and b, or limit+1 if
// it is more than limit.
func editDistance(a, b string, limit int) int {
	if abs(len(a)-len(b)) > limit {
		return limit + 1
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			rowMin = min(rowMin, cur[j])
		}
		if rowMin > limit {
			return limit + 1
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// findLines returns the lines reading text (after normalizeText, allowing
// sameText's slack), top-most first, plus the lines that merely contain it,
// for reporting near misses. Lines a short gap split are matched joined too
// (see bridgedLines).
func findLines(lines []OCRLine, text string) (exact, partial []OCRLine) {
	want := normalizeText(text)
	if want == "" {
		return nil, nil
	}
	for _, l := range bridgedLines(lines) {
		got := normalizeText(l.Text)
		switch {
		case sameText(got, want):
			exact = append(exact, l)
		case strings.Contains(got, want):
			partial = append(partial, l)
		}
	}
	exact = innermost(exact, nil)
	return exact, innermost(partial, exact)
}

// bridgeChars is the widest gap, in characters, that bridgedLines joins
// across, and bridgeMaxParts the most lines it joins into one.
const (
	bridgeChars    = 4
	bridgeMaxParts = 4
)

// bridgedLines returns lines plus, for each run of lines on one row split by
// a gap of no more than a few characters, that run joined into one line.
// groupLines splits at gaps wider than a word height or so, to keep side by
// side windows apart, but a terminal line can hold such a gap too: in
// "This is the one - vh4uyx" OCR drops the lone "-" (mergeWords keeps only
// words with letters or digits), leaving three blank characters between
// "one" and "vh4uyx", wider than the word height of "one" (Revision A's
// failed hand-off). The joined lines are only candidates for matching; a
// match is reported as the smallest line that holds it (see innermost).
func bridgedLines(lines []OCRLine) []OCRLine {
	next := make([]int, len(lines))
	for i, a := range lines {
		next[i] = -1
		if len(a.Words) == 0 {
			continue
		}
		last := a.Words[len(a.Words)-1]
		cw := last.W / max(1, len([]rune(last.Text)))
		best := 0
		for j, b := range lines {
			if j == i {
				continue
			}
			h := max(a.H, b.H)
			gap := b.X - (a.X + a.W)
			if gap < 0 || gap > max(bridgeChars*cw, h*6/5) || abs(a.Y+a.H/2-(b.Y+b.H/2))*2 > h {
				continue
			}
			if next[i] < 0 || gap < best {
				next[i], best = j, gap
			}
		}
	}
	out := append([]OCRLine{}, lines...)
	for i := range lines {
		joined := lines[i]
		for n, j := 1, next[i]; n < bridgeMaxParts && j >= 0 && j != i; n, j = n+1, next[j] {
			b := lines[j]
			r := joined.rect().Union(b.rect())
			joined = OCRLine{
				Text:  joined.Text + " " + b.Text,
				X:     r.Min.X,
				Y:     r.Min.Y,
				W:     r.Dx(),
				H:     r.Dy(),
				Words: append(append([]OCRWord{}, joined.Words...), b.Words...),
			}
			out = append(out, joined)
		}
	}
	return out
}

// innermost drops the lines in hits that enclose another of hits or one of
// also -- a joined line from bridgedLines matching only because one of its
// parts does -- and orders the rest top to bottom, then left to right.
func innermost(hits, also []OCRLine) []OCRLine {
	all := append(append([]OCRLine{}, hits...), also...)
	var out []OCRLine
	for _, h := range hits {
		encloses := false
		for _, o := range all {
			if r := o.rect(); !r.Empty() && r != h.rect() && r.In(h.rect()) {
				encloses = true
				break
			}
		}
		if !encloses {
			out = append(out, h)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Y != out[j].Y {
			return out[i].Y < out[j].Y
		}
		return out[i].X < out[j].X
	})
	return out
}

// wrappedLines finds text that reads as text only once a line is joined
// with the one just below it -- a label wrapped over two lines, as GNOME's
// desktop icons wrap "ianar-hello-wor" / "ld.txt" -- and returns each pair
// as one line spanning both. Matching never joins lines, so these show up
// in failure messages only, to explain a miss.
func wrappedLines(lines []OCRLine, text string) []OCRLine {
	want := normalizeText(text)
	if want == "" {
		return nil
	}
	var out []OCRLine
	for _, a := range lines {
		na := normalizeText(a.Text)
		if na == "" || strings.Contains(na, want) {
			continue
		}
		for _, b := range lines {
			nb := normalizeText(b.Text)
			gap := b.Y - (a.Y + a.H)
			if nb == "" || strings.Contains(nb, want) || gap < -a.H/2 || gap > a.H || b.X >= a.X+a.W || a.X >= b.X+b.W {
				continue // not just below a, under it
			}
			joined := na + nb
			if strings.Contains(joined, want) || sameText(joined, want) {
				r := a.rect().Union(b.rect())
				out = append(out, OCRLine{Text: a.Text + " / " + b.Text, X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()})
			}
		}
	}
	return out
}

// readScreen captures the native display and reads the text on it.
// Overridable in tests.
var readScreen = func() (*screenReading, error) {
	img, from, err := grabNativeFrame(nativeCaptureAttempts())
	if err != nil {
		return nil, fmt.Errorf("capturing the screen: %w", err)
	}
	t0 := time.Now()
	words, err := readScreenText(img)
	if err != nil {
		return nil, fmt.Errorf("reading the screen: %w", err)
	}
	log.Printf("robot: read %d words off a %dx%d capture (via %s) in %s",
		len(words), img.Bounds().Dx(), img.Bounds().Dy(), from, time.Since(t0).Round(time.Millisecond))
	return &screenReading{img: img, from: from, words: words, lines: groupLines(words)}, nil
}

// ---- Annotated images ----

var (
	matchColor = color.RGBA{0x3f, 0xd0, 0x5a, 0xff} // the line acted on
	otherColor = color.RGBA{0xff, 0xb0, 0x20, 0xff} // other candidate lines
)

// annotate returns a copy of img with each rectangle outlined.
func annotate(img image.Image, boxes map[color.RGBA][]image.Rectangle) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	for c, rs := range boxes {
		for _, r := range rs {
			outline(out, r.Inset(-4), 3, c)
		}
	}
	return out
}

func outline(img *image.RGBA, r image.Rectangle, thick int, c color.RGBA) {
	r = r.Intersect(img.Bounds())
	if r.Empty() {
		return
	}
	u := image.NewUniform(c)
	for _, edge := range []image.Rectangle{
		image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+thick),
		image.Rect(r.Min.X, r.Max.Y-thick, r.Max.X, r.Max.Y),
		image.Rect(r.Min.X, r.Min.Y, r.Min.X+thick, r.Max.Y),
		image.Rect(r.Max.X-thick, r.Min.Y, r.Max.X, r.Max.Y),
	} {
		draw.Draw(img, edge.Intersect(r), u, image.Point{}, draw.Src)
	}
}

// cropAround returns the w x h region of img centered on p, shifted to stay
// inside img.
func cropAround(img *image.RGBA, p image.Point, w, h int) image.Image {
	b := img.Bounds()
	w, h = min(w, b.Dx()), min(h, b.Dy())
	x := min(max(b.Min.X, p.X-w/2), b.Max.X-w)
	y := min(max(b.Min.Y, p.Y-h/2), b.Max.Y-h)
	return img.SubImage(image.Rect(x, y, x+w, y+h))
}

// jpegDataURL encodes img, shrunk to at most maxW wide, as a JPEG data: URL.
func jpegDataURL(img image.Image, maxW int) string {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, downscale(img, maxW), &jpeg.Options{Quality: 80}); err != nil {
		log.Printf("robot: encoding an inspection image: %v", err)
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// ---- "Inspect Screen" ----

// InspectResultMsg is the "inspect-result" WebSocket payload: a capture,
// the text read off it, and, if the request asked to find some text, the
// lines that matched. Coordinates are in the capture's pixels (Width x
// Height), not the (possibly downscaled) image's.
type InspectResultMsg struct {
	Success    bool      `json:"success"`
	Error      string    `json:"error,omitempty"`
	ImageURL   string    `json:"image_url,omitempty"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	CaptureVia string    `json:"capture_via,omitempty"`
	Lines      []OCRLine `json:"lines"`
	Find       string    `json:"find,omitempty"`
	Matches    []OCRLine `json:"matches"`
	Partial    []OCRLine `json:"partial"`
	DurationMs int64     `json:"duration_ms"`
	ArtifactID string    `json:"artifact_id,omitempty"` // names a successful inspection for "save-artifact" (see artifacts.go)
}

// inspectScreen captures the screen and reads it, matching find if given.
func inspectScreen(find string) InspectResultMsg {
	t0 := clock()
	res := InspectResultMsg{Find: find}
	sr, err := readScreen()
	res.DurationMs = clock().Sub(t0).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	b := sr.img.Bounds()
	res.Success = true
	res.Width, res.Height, res.CaptureVia = b.Dx(), b.Dy(), sr.from
	res.Lines = sr.lines
	res.ImageURL = jpegDataURL(sr.img, inspectMaxWidth)
	if strings.TrimSpace(find) != "" {
		res.Matches, res.Partial = findLines(sr.lines, find)
	}
	return res
}

// handleInspectScreen runs inspectScreen and reports the result back to the
// requesting client as an "inspect-result" message.
func (s *Server) handleInspectScreen(c *wsClient, find string) {
	res := inspectScreen(find)
	if res.Success {
		res.ArtifactID = s.artifacts.keep(inspectArtifact(res))
	}
	s.sendToClient(c, "inspect-result", res)
}
