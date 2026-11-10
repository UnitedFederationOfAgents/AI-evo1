package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---- Rendering with ffmpeg ----
//
// The browser is composer's main renderer (frontend/src/render.ts). A render
// job (renderjobs.go) can instead be rendered here, with ffmpeg, so a
// composition can become a file with no compose tab open. The result is
// meant to match a browser render: the same back-to-back tracks, title
// cards centred in the same colours (in a font found on the host, so the
// lettering differs), 440 Hz-style beeps at the same level, black after a
// short video track and silence after a short audio track, recorded into a
// WebM (VP8 and Opus here). Media sources are fetched from
// local-representative's files area into a scratch directory first; ones
// with no duration are measured with ffprobe.

const (
	ffmpegTimeout = 10 * time.Minute
	renderFPS     = 30
	sampleRate    = 48000
	toneLevel     = 0.4 // the browser's oscillator gain (render.ts)
)

// fontCandidates are tried, in order, when --font-file isn't given.
var fontCandidates = []string{
	"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
	"/usr/share/fonts/dejavu/DejaVuSans-Bold.ttf",
	"/usr/share/fonts/TTF/DejaVuSans-Bold.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf",
	"/usr/share/fonts/liberation/LiberationSans-Bold.ttf",
	"/usr/share/fonts/truetype/freefont/FreeSansBold.ttf",
	"/System/Library/Fonts/Helvetica.ttc",
}

// ffmpegPath is the ffmpeg render jobs use, or why there is none.
func (s *Server) ffmpegPath() (string, error) {
	p, err := exec.LookPath(s.ffmpegBin)
	if err != nil {
		return "", fmt.Errorf("ffmpeg isn't installed here (looked for %q) -- install it, or let an open compose tab render (renderer browser)", s.ffmpegBin)
	}
	return p, nil
}

// RenderCapabilityMsg is the "render-capability" WebSocket payload, and part
// of GET /api/status: whether composer can render jobs itself, with ffmpeg,
// or a job needs an open compose tab. It's looked up afresh each time, so
// installing ffmpeg shows on the next check without a restart.
type RenderCapabilityMsg struct {
	FFmpeg  string `json:"ffmpeg,omitempty"`  // ffmpeg's path; "" when it isn't installed
	FFprobe string `json:"ffprobe,omitempty"` // ffprobe's path, which checks clips before a render; "" when missing
	Error   string `json:"error,omitempty"`   // why there's no ffmpeg
}

func (s *Server) renderCapability() RenderCapabilityMsg {
	p, err := s.ffmpegPath()
	if err != nil {
		return RenderCapabilityMsg{Error: err.Error()}
	}
	msg := RenderCapabilityMsg{FFmpeg: p}
	if probe, err := ffprobePath(p); err == nil {
		msg.FFprobe = probe
	}
	return msg
}

// ffprobePath is the ffprobe beside ffmpeg, else the one on PATH.
func ffprobePath(ffmpeg string) (string, error) {
	beside := filepath.Join(filepath.Dir(ffmpeg), "ffprobe")
	if st, err := os.Stat(beside); err == nil && !st.IsDir() {
		return beside, nil
	}
	return exec.LookPath("ffprobe")
}

// titleFont is the drawtext option naming the title cards' font.
func (s *Server) titleFont() string {
	if s.fontFile != "" {
		return "fontfile=" + filterQuote(s.fontFile)
	}
	for _, p := range fontCandidates {
		if _, err := os.Stat(p); err == nil {
			return "fontfile=" + filterQuote(p)
		}
	}
	return "font=" + filterQuote("Sans:style=Bold") // fontconfig picks one
}

// cssHex accepts the colours ffmpeg reads the same way a browser does:
// #rgb, #rrggbb and #rrggbbaa. Anything else (a CSS name ffmpeg may not
// know) falls back to def.
var cssHex = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

func ffColor(c, def string) string {
	c = strings.TrimSpace(c)
	if !cssHex.MatchString(c) {
		switch strings.ToLower(c) {
		case "black", "white", "red", "green", "blue", "yellow", "gray", "grey", "orange", "purple", "navy", "teal":
			return strings.ToLower(c)
		}
		return def
	}
	if len(c) == 4 { // #rgb -> #rrggbb
		c = "#" + strings.Repeat(c[1:2], 2) + strings.Repeat(c[2:3], 2) + strings.Repeat(c[3:4], 2)
	}
	return "0x" + c[1:]
}

// filterQuote quotes a value for an ffmpeg filter graph option.
func filterQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

func secs(d float64) string { return strconv.FormatFloat(d, 'f', 3, 64) }

// wrapTitle breaks text into lines that fit 85% of the frame, as the
// browser's wrapLines does, estimating a character at 0.55 of the font size.
func wrapTitle(text string, width, size int) string {
	maxChars := int(float64(width) * 0.85 / (float64(size) * 0.55))
	if maxChars < 1 {
		maxChars = 1
	}
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case len([]rune(line))+1+len([]rune(word)) > maxChars:
				lines = append(lines, line)
				line = word
			default:
				line += " " + word
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// ffPlan is one ffmpeg command being put together.
type ffPlan struct {
	inputs  []string // -i ... arguments, in order
	nInputs int
	filters []string // filter_complex chains
}

func (p *ffPlan) input(args ...string) int {
	p.inputs = append(p.inputs, args...)
	p.nInputs++
	return p.nInputs - 1
}

// renderFFmpeg renders c into a WebM -- audio only, for an audio
// composition -- and returns its bytes.
func (s *Server) renderFFmpeg(ctx context.Context, c *Composition) ([]byte, error) {
	ffmpeg, err := s.ffmpegPath()
	if err != nil {
		return nil, err
	}
	base, err := s.lrBaseFn()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "composer-render-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	// Fetch every media source once, and measure the ones with no duration.
	files := map[string]string{} // file id -> local path
	fetch := func(cl Clip) (string, error) {
		if p, ok := files[cl.FileID]; ok {
			return p, nil
		}
		p := filepath.Join(dir, fmt.Sprintf("src-%d%s", len(files), strings.ToLower(path.Ext(cl.FileName))))
		if err := downloadFile(ctx, base, cl.FileID, p); err != nil {
			return "", err
		}
		files[cl.FileID] = p
		return p, nil
	}
	lengthOf := func(track string, i int, cl Clip) (float64, string, error) {
		label := fmt.Sprintf("%s clip %d", track, i+1)
		if cl.FileName != "" {
			label += " (" + cl.FileName + ")"
		}
		p := ""
		if cl.Kind == clipMedia {
			var err error
			if p, err = fetch(cl); err != nil {
				return 0, "", fmt.Errorf("%s: %w", label, err)
			}
		}
		if cl.Duration > 0 {
			return cl.Duration, p, nil
		}
		d, err := probeDuration(ctx, ffmpeg, p)
		if err != nil {
			return 0, "", fmt.Errorf("%s: unknown length (%v) -- give it a duration", label, err)
		}
		return d, p, nil
	}

	w, h := c.Width, c.Height
	size := int(float64(h) * 0.08)
	if size < 12 {
		size = 12
	}
	font := s.titleFont()
	frame := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,fps=%d,format=yuv420p", w, h, w, h, renderFPS)
	black := func(d float64) string {
		return fmt.Sprintf("color=c=black:s=%dx%d:r=%d:d=%s,setsar=1,format=yuv420p", w, h, renderFPS, secs(d))
	}
	audioFmt := fmt.Sprintf("aresample=%d,aformat=sample_fmts=fltp:sample_rates=%d:channel_layouts=stereo", sampleRate, sampleRate)
	silence := func(d float64) string {
		return fmt.Sprintf("anullsrc=r=%d:cl=stereo,atrim=duration=%s,%s", sampleRate, secs(d), audioFmt)
	}

	plan := &ffPlan{}
	var videoLabels, audioLabels []string
	videoEnd, audioEnd := 0.0, 0.0
	for i, cl := range c.Video {
		d, p, err := lengthOf("video", i, cl)
		if err != nil {
			return nil, err
		}
		label := fmt.Sprintf("[v%d]", i)
		switch {
		case cl.Kind == clipTitle:
			textFile := filepath.Join(dir, fmt.Sprintf("title-%d.txt", i))
			if err := os.WriteFile(textFile, []byte(wrapTitle(cl.Text, w, size)), 0o644); err != nil {
				return nil, err
			}
			plan.filters = append(plan.filters, fmt.Sprintf(
				"color=c=%s:s=%dx%d:r=%d:d=%s,drawtext=%s:textfile=%s:expansion=none:fontcolor=%s:fontsize=%d:line_spacing=%d:x=(w-text_w)/2:y=(h-text_h)/2,setsar=1,format=yuv420p%s",
				ffColor(cl.Background, "black"), w, h, renderFPS, secs(d), font, filterQuote(textFile), ffColor(cl.Color, "white"), size, size/4, label))
		case mediaKind(cl.FileName) == "image":
			n := plan.input("-loop", "1", "-framerate", strconv.Itoa(renderFPS), "-t", secs(d), "-i", p)
			plan.filters = append(plan.filters, fmt.Sprintf("[%d:v]%s,trim=duration=%s,setpts=PTS-STARTPTS%s", n, frame, secs(d), label))
		default: // a video, drawn without its sound
			if err := hasVideoStream(ctx, ffmpeg, p); err != nil {
				return nil, fmt.Errorf("video clip %d (%s): %v", i+1, cl.FileName, err)
			}
			n := plan.input("-i", p)
			plan.filters = append(plan.filters, fmt.Sprintf("[%d:v]setpts=PTS-STARTPTS,%s,tpad=stop_mode=clone:stop_duration=%s,trim=duration=%s,setpts=PTS-STARTPTS%s", n, frame, secs(d), secs(d), label))
		}
		videoLabels = append(videoLabels, label)
		videoEnd += d
	}
	for i, cl := range c.Audio {
		d, p, err := lengthOf("audio", i, cl)
		if err != nil {
			return nil, err
		}
		label := fmt.Sprintf("[a%d]", i)
		switch cl.Kind {
		case clipTone:
			fade := d / 4
			if fade > 0.01 {
				fade = 0.01
			}
			// sine's amplitude is 1/8; scale it to the browser's gain.
			plan.filters = append(plan.filters, fmt.Sprintf(
				"sine=frequency=%s:sample_rate=%d:duration=%s,volume=%s,afade=t=in:d=%s,afade=t=out:st=%s:d=%s,%s%s",
				strconv.FormatFloat(cl.Frequency, 'f', -1, 64), sampleRate, secs(d), strconv.FormatFloat(toneLevel*8, 'f', -1, 64),
				secs(fade), secs(d-fade), secs(fade), audioFmt, label))
		case clipSilence:
			plan.filters = append(plan.filters, silence(d)+label)
		default: // media: an audio file, or a video's sound
			n := plan.input("-i", p)
			plan.filters = append(plan.filters, fmt.Sprintf("[%d:a]asetpts=PTS-STARTPTS,%s,apad=whole_dur=%s,atrim=duration=%s,asetpts=PTS-STARTPTS%s", n, audioFmt, secs(d), secs(d), label))
		}
		audioLabels = append(audioLabels, label)
		audioEnd += d
	}

	// The render lasts as long as the longer track; pad the shorter one.
	total := videoEnd
	if audioEnd > total {
		total = audioEnd
	}
	if total <= 0 {
		return nil, errors.New("the composition has no length")
	}
	audioOnly := c.audioOnly() // no video track to pad or record
	if gap := total - videoEnd; gap > 0.0005 && !audioOnly {
		label := fmt.Sprintf("[v%d]", len(videoLabels))
		plan.filters = append(plan.filters, black(gap)+label)
		videoLabels = append(videoLabels, label)
	}
	if gap := total - audioEnd; gap > 0.0005 {
		label := fmt.Sprintf("[a%d]", len(audioLabels))
		plan.filters = append(plan.filters, silence(gap)+label)
		audioLabels = append(audioLabels, label)
	}
	if !audioOnly {
		plan.filters = append(plan.filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[vout]", strings.Join(videoLabels, ""), len(videoLabels)))
	}
	plan.filters = append(plan.filters, fmt.Sprintf("%sconcat=n=%d:v=0:a=1[aout]", strings.Join(audioLabels, ""), len(audioLabels)))

	out := filepath.Join(dir, "render.webm")
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}
	args = append(args, plan.inputs...)
	args = append(args, "-filter_complex", strings.Join(plan.filters, ";"))
	if audioOnly {
		args = append(args, "-map", "[aout]", "-vn")
	} else {
		args = append(args, "-map", "[vout]", "-map", "[aout]",
			"-c:v", "libvpx", "-b:v", "2M", "-deadline", "realtime", "-cpu-used", "8")
	}
	args = append(args,
		"-c:a", "libopus", "-b:a", "128k",
		"-t", secs(total), "-f", "webm", out)
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ffmpeg didn't finish within %s", ffmpegTimeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 600 {
			msg = "…" + msg[len(msg)-600:]
		}
		return nil, fmt.Errorf("ffmpeg failed (%v): %s", err, msg)
	}
	// A render that came out short has lost clips: say so rather than
	// saving it as though it were the composition.
	if got, err := probeDuration(ctx, ffmpeg, out); err == nil && got < total-renderShortfall {
		return nil, fmt.Errorf("the render came out %ss long, not the composition's %ss -- some clips didn't make it in, so it wasn't saved", secs(got), secs(total))
	}
	return os.ReadFile(out)
}

// renderShortfall is how much shorter than its composition a render may
// come out (the last frame or audio packet) before it counts as missing clips.
const renderShortfall = 1.0

// hasVideoStream reports why file has no picture to draw, if it hasn't.
func hasVideoStream(ctx context.Context, ffmpeg, file string) error {
	ffprobe, err := ffprobePath(ffmpeg)
	if err != nil {
		return nil // can't tell; ffmpeg itself will complain if there's none
	}
	out, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v",
		"-show_entries", "stream=codec_type", "-of", "csv=p=0", file).Output()
	if err != nil {
		return fmt.Errorf("ffprobe couldn't read it: %v", err)
	}
	if !strings.Contains(string(out), "video") {
		return errors.New("it has no picture (an audio-only file? put it on the audio track)")
	}
	return nil
}

// downloadFile saves file id from local-representative's files area at base
// into dst.
func downloadFile(ctx context.Context, base, id, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/files/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching it from local-representative: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching it from local-representative: status %d (it may have been swept from the files area -- hold sources you want to keep)", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// probeDuration measures a media file's length in seconds with ffprobe.
// A WebM written by MediaRecorder (or a recorder stopped before it could
// seek back) often has no duration in its header -- ffprobe says "N/A" --
// so then the file is read through (its packets copied, not decoded) and
// the last timestamp ffmpeg reached is its length, as the browser renderer
// does by seeking to the end.
func probeDuration(ctx context.Context, ffmpeg, file string) (float64, error) {
	if file == "" {
		return 0, errors.New("no file")
	}
	ffprobe, err := ffprobePath(ffmpeg)
	if err != nil {
		return 0, errors.New("ffprobe, which measures it, isn't installed")
	}
	out, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", file).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %v", err)
	}
	if d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil && d > 0 {
		return d, nil
	}
	if d, err := readThroughDuration(ctx, ffmpeg, file); err == nil && d > 0 {
		return d, nil
	}
	return 0, errors.New("the file doesn't say")
}

// statsTime matches the "time=HH:MM:SS.ss" of ffmpeg's progress lines.
var statsTime = regexp.MustCompile(`time=(\d+):(\d{2}):(\d{2}(?:\.\d+)?)`)

// readThroughDuration copies file's packets to nowhere and returns the last
// time ffmpeg reported reaching.
func readThroughDuration(ctx context.Context, ffmpeg, file string) (float64, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "info", "-stats",
		"-i", file, "-map", "0", "-c", "copy", "-f", "null", "-")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffmpeg: %v", err)
	}
	m := statsTime.FindAllStringSubmatch(stderr.String(), -1)
	if len(m) == 0 {
		return 0, errors.New("ffmpeg reported no time")
	}
	last := m[len(m)-1]
	h, _ := strconv.ParseFloat(last[1], 64)
	mins, _ := strconv.ParseFloat(last[2], 64)
	s, _ := strconv.ParseFloat(last[3], 64)
	return h*3600 + mins*60 + s, nil
}
