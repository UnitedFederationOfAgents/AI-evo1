package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// This file is the sequence-v2 tab's primitive operations: what a definer
// action's instructions can do (see seqv2.go). Each is carried out with the
// same native capabilities the simple tab uses -- keyboard
// (keyboard.go), pointer (pointer.go), screen reading (vision.go), screen
// capture (robot.go) -- plus icon finding (icon.go) and a few file
// operations.
//
// Every op that waits on the screen polls it (capture + OCR, so roughly a
// second or two per look) until what it's waiting for appears or its
// timeout passes, and leaves an image of what it saw with the step's
// result: the match boxed in green, or near misses in amber on failure.

const (
	screenPollInterval = 500 * time.Millisecond // between looks while waiting on the screen
	keyChordGap        = 100 * time.Millisecond // between key chords in one "key" instruction
	shotThumbWidth     = 960                    // width of a saved screenshot's preview
	fileExcerptLen     = 200                    // how much of a file an expect-file failure quotes
	openTimeout        = 15 * time.Second       // for the command that opens a file in its app
)

// opArg documents one argument of an op.
type opArg struct {
	Name     string `json:"name"`
	Help     string `json:"help"`
	Required bool   `json:"required,omitempty"`
	Default  string `json:"default,omitempty"`
}

// opSpec is a primitive operation. Describe is how a step lists the
// instruction, with {arg} replaced by the argument's value.
type opSpec struct {
	Op       string  `json:"op"`
	Summary  string  `json:"summary"`
	Describe string  `json:"describe"`
	Args     []opArg `json:"args"`
	run      func(env *seqEnv, a opArgs) (string, error)
}

func (s opSpec) arg(name string) *opArg {
	for i := range s.Args {
		if s.Args[i].Name == name {
			return &s.Args[i]
		}
	}
	return nil
}

func (s opSpec) argNames() string {
	if len(s.Args) == 0 {
		return "nothing"
	}
	names := make([]string, len(s.Args))
	for i, a := range s.Args {
		names[i] = a.Name
	}
	return strings.Join(names, ", ")
}

func findOp(name string) (opSpec, bool) {
	for _, s := range seqOps {
		if s.Op == name {
			return s, true
		}
	}
	return opSpec{}, false
}

var describeRe = regexp.MustCompile(`\{([a-z_]+)\}`)

// describeInstruction renders an instruction for a step's detail list.
func describeInstruction(in map[string]string) string {
	spec, ok := findOp(in["op"])
	if !ok {
		return in["op"]
	}
	return describeRe.ReplaceAllStringFunc(spec.Describe, func(m string) string {
		name := m[1 : len(m)-1]
		v := in[name]
		if v == "" {
			if a := spec.arg(name); a != nil {
				v = a.Default
			}
		}
		return v
	})
}

// opArgs are an instruction's arguments, filled in and with defaults
// applied.
type opArgs map[string]string

func (a opArgs) flag(name string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(a[name])) {
	case "", "false", "no", "0", "off":
		return false, nil
	case "true", "yes", "1", "on":
		return true, nil
	}
	return false, fmt.Errorf("%s should be true or false, not %q", name, a[name])
}

// dur reads a duration: "500ms", "2s", or a bare number of milliseconds.
func (a opArgs) dur(name string) (time.Duration, error) {
	v := strings.TrimSpace(a[name])
	if v == "" {
		return 0, nil
	}
	if ms, err := strconv.Atoi(v); err == nil {
		return time.Duration(ms) * time.Millisecond, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s should be a duration like 500ms or 2s, not %q", name, v)
	}
	return d, nil
}

// path reads an absolute file path, expanding a leading ~.
func (a opArgs) path(name string) (string, error) {
	p := strings.TrimSpace(a[name])
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.Join(homeDir(), p[1:])
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%s should be an absolute path (or start with ~/ or {{desktop}}), not %q", name, a[name])
	}
	return filepath.Clean(p), nil
}

// saveVar stores value under the name in argument save_as, if one is given.
func (a opArgs) saveVar(env *seqEnv, value string) error {
	name := strings.TrimSpace(a["save_as"])
	if name == "" {
		return nil
	}
	if !varNameRe.MatchString(name) {
		return fmt.Errorf("save_as %q isn't a valid name", name)
	}
	env.vars[name] = value
	return nil
}

// ---- Screen helpers ----

// captureScreen grabs the native display. Overridable in tests.
var captureScreen = func() (image.Image, string, error) {
	return grabNativeFrame(nativeCaptureAttempts())
}

// clickPoint clicks at a point in capture pixels. Overridable in tests.
var clickPoint = clickAtWith

// pollScreen reads the screen until done accepts a reading or timeout
// passes, looking at least once, and returns the last reading.
func pollScreen(timeout time.Duration, done func(*screenReading) bool) (*screenReading, bool, error) {
	deadline := clock().Add(timeout)
	for {
		sr, err := readScreen()
		if err != nil {
			return nil, false, err
		}
		if done(sr) {
			return sr, true, nil
		}
		if !clock().Before(deadline) {
			return sr, false, nil
		}
		sleep(screenPollInterval)
	}
}

// textMatches returns the lines reading text (exact, allowing sameText's
// slack for OCR misreads) or containing it, after normalizeText, other than
// those containing exclude. Lines a short gap split are matched joined too
// (see bridgedLines).
func textMatches(lines []OCRLine, text, exclude string, exact bool) []OCRLine {
	want, ex := normalizeText(text), normalizeText(exclude)
	if want == "" {
		return nil
	}
	var out []OCRLine
	for _, l := range bridgedLines(lines) {
		got := normalizeText(l.Text)
		if ex != "" && strings.Contains(got, ex) {
			continue
		}
		if (exact && sameText(got, want)) || (!exact && strings.Contains(got, want)) {
			out = append(out, l)
		}
	}
	return innermost(out, nil)
}

// notSeen returns the lines that don't overlap any of seen -- the lines
// that have appeared since seen was recorded.
func notSeen(lines []OCRLine, seen []image.Rectangle) []OCRLine {
	var out []OCRLine
	for _, l := range lines {
		old := false
		for _, r := range seen {
			if overlapFrac(l.rect(), r) > 0.5 {
				old = true
				break
			}
		}
		if !old {
			out = append(out, l)
		}
	}
	return out
}

// screenShot draws hits (green) and others (amber) on a reading's capture:
// cropped around the hit if there's exactly one, else the whole screen.
func screenShot(sr *screenReading, hits, others []OCRLine) string {
	boxes := map[color.RGBA][]image.Rectangle{}
	for _, l := range hits {
		boxes[matchColor] = append(boxes[matchColor], l.rect())
	}
	for _, l := range others {
		boxes[otherColor] = append(boxes[otherColor], l.rect())
	}
	marked := annotate(sr.img, boxes)
	if len(hits) == 1 {
		return jpegDataURL(cropAround(marked, hits[0].center(), matchCropW, matchCropH), matchCropW)
	}
	return jpegDataURL(marked, inspectMaxWidth)
}

// wrappedNote mentions text found only wrapped over two lines (see
// wrappedLines), for a failure message.
func wrappedNote(wrapped []OCRLine) string {
	if len(wrapped) == 0 {
		return ""
	}
	return "; it shows only wrapped over two lines (which don't count): " + quoteLines(wrapped)
}

func quoteLines(ls []OCRLine) string {
	q := make([]string, len(ls))
	for i, l := range ls {
		q[i] = strconv.Quote(l.Text)
	}
	return strings.Join(q, ", ")
}

// ---- Keys ----

// keyAliases maps other common key names to the ones keyboard.go knows.
var keyAliases = map[string]string{
	"plus": "+", "minus": "-", "return": "enter", "esc": "escape", "del": "delete",
	"win": "super", "meta": "super", "cmd": "super", "control": "ctrl", "pgup": "pageup", "pgdn": "pagedown",
}

var modifierKeys = map[string]bool{"ctrl": true, "shift": true, "alt": true, "super": true}

// parseChords reads a "key" instruction's keys: chords separated by spaces,
// each a key with any modifiers joined by + ("end", "ctrl+u",
// "ctrl+shift+p", "alt+f2"). Each chord is returned modifiers first, key
// last.
func parseChords(s string) ([][]string, error) {
	var chords [][]string
	for _, f := range strings.Fields(s) {
		parts := strings.Split(strings.ToLower(f), "+")
		for i, p := range parts {
			if p == "" {
				return nil, fmt.Errorf("%q: write the + key as \"plus\" (e.g. ctrl+plus)", f)
			}
			if a, ok := keyAliases[p]; ok {
				parts[i] = a
			}
		}
		for _, m := range parts[:len(parts)-1] {
			if !modifierKeys[m] {
				return nil, fmt.Errorf("%q: %q isn't a modifier (ctrl, shift, alt, super)", f, m)
			}
		}
		if _, err := keysymFor(parts[len(parts)-1]); err != nil {
			return nil, fmt.Errorf("%q: unknown key %q", f, parts[len(parts)-1])
		}
		chords = append(chords, parts)
	}
	if len(chords) == 0 {
		return nil, errors.New("no keys given")
	}
	return chords, nil
}

// ---- The ops ----

// seqOps are the primitive operations, in the order the definer lists them.
var seqOps = []opSpec{
	{
		Op:       "ensure-awake",
		Summary:  "Check the screen is unlocked, waking it if it has blanked. Fails on a locked screen.",
		Describe: "check the screen is unlocked, and wake it if it has blanked",
		run: func(env *seqEnv, a opArgs) (string, error) {
			note, err := ensureScreenAwake()
			if err == nil && note == "" {
				note = "the screen is awake and unlocked"
			}
			return note, err
		},
	},
	{
		Op:       "focus-window",
		Summary:  "Find a window by its title on screen (OCR) and click the title to focus it; falls back to asking the window manager.",
		Describe: `find the line reading exactly "{title}" on screen (a title bar) and click it; failing that, ask the window manager to focus that window`,
		Args:     []opArg{{Name: "title", Help: "the window title, exactly", Required: true}},
		run: func(env *seqEnv, a opArgs) (string, error) {
			desc, shot, err := focusByTitle(a["title"])
			env.shot = shot
			if err != nil {
				return "", err
			}
			sleep(focusSettle)
			return desc, nil
		},
	},
	{
		Op:       "key",
		Summary:  "Press keys: chords separated by spaces, modifiers joined with + (end, ctrl+u, ctrl+shift+p, alt+f2).",
		Describe: "press {keys}",
		Args:     []opArg{{Name: "keys", Help: "e.g. enter, ctrl+l, end ctrl+u, alt+f2 (write + as plus)", Required: true}},
		run: func(env *seqEnv, a opArgs) (string, error) {
			chords, err := parseChords(a["keys"])
			if err != nil {
				return "", err
			}
			for i, ch := range chords {
				if i > 0 {
					sleep(keyChordGap)
				}
				if err := env.kb.tap(ch[len(ch)-1], ch[:len(ch)-1]...); err != nil {
					return "", err
				}
			}
			sleep(stepSettle)
			return "", nil
		},
	},
	{
		Op:       "type",
		Summary:  "Type text into whatever has focus, one key at a time.",
		Describe: `type "{text}"`,
		Args:     []opArg{{Name: "text", Help: "the text to type", Required: true}},
		run: func(env *seqEnv, a opArgs) (string, error) {
			if err := env.kb.typeText(a["text"]); err != nil {
				return "", err
			}
			sleep(stepSettle)
			return "", nil
		},
	},
	{
		Op:       "wait",
		Summary:  "Pause.",
		Describe: "wait {duration}",
		Args:     []opArg{{Name: "duration", Help: "e.g. 500ms, 2s (a bare number is milliseconds)", Required: true}},
		run: func(env *seqEnv, a opArgs) (string, error) {
			d, err := a.dur("duration")
			if err != nil {
				return "", err
			}
			sleep(d)
			return "", nil
		},
	},
	{
		Op:       "count-text",
		Summary:  "Count the lines on screen showing some text, saving the count -- e.g. to check later that one more appeared, or (new_since) to click the new one.",
		Describe: `count the lines on screen containing "{text}" (not "{exclude}") as {{{save_as}}}`,
		Args: []opArg{
			{Name: "text", Help: "the text to look for", Required: true},
			{Name: "exact", Help: "true: count only lines reading exactly this", Default: "false"},
			{Name: "exclude", Help: "skip lines containing this"},
			{Name: "save_as", Help: "name to save the count under", Required: true},
		},
		run: func(env *seqEnv, a opArgs) (string, error) {
			exact, err := a.flag("exact")
			if err != nil {
				return "", err
			}
			sr, err := readScreen()
			if err != nil {
				return "", err
			}
			hits := textMatches(sr.lines, a["text"], a["exclude"], exact)
			env.shot = screenShot(sr, hits, nil)
			if err := a.saveVar(env, strconv.Itoa(len(hits))); err != nil {
				return "", err
			}
			if env.seen != nil {
				var rs []image.Rectangle
				for _, l := range hits {
					rs = append(rs, l.rect())
				}
				env.seen[strings.TrimSpace(a["save_as"])] = rs
			}
			return fmt.Sprintf("%d line(s) on screen show %q", len(hits), a["text"]), nil
		},
	},
	{
		Op:       "wait-for-text",
		Summary:  "Wait until text shows on screen -- or, with more_than, until more lines show it than before.",
		Describe: `wait up to {timeout} for more than {more_than} line(s) on screen containing "{text}"`,
		Args: []opArg{
			{Name: "text", Help: "the text to wait for", Required: true},
			{Name: "exact", Help: "true: a line must read exactly this", Default: "false"},
			{Name: "exclude", Help: "skip lines containing this"},
			{Name: "more_than", Help: "how many matching lines must be exceeded, e.g. {{before}} from count-text", Default: "0"},
			{Name: "timeout", Help: "how long to keep looking", Default: "10s"},
		},
		run: func(env *seqEnv, a opArgs) (string, error) {
			exact, err := a.flag("exact")
			if err != nil {
				return "", err
			}
			timeout, err := a.dur("timeout")
			if err != nil {
				return "", err
			}
			more, err := strconv.Atoi(strings.TrimSpace(a["more_than"]))
			if err != nil {
				return "", fmt.Errorf("more_than should be a number, not %q", a["more_than"])
			}
			sr, ok, err := pollScreen(timeout, func(sr *screenReading) bool {
				return len(textMatches(sr.lines, a["text"], a["exclude"], exact)) > more
			})
			if err != nil {
				return "", err
			}
			hits := textMatches(sr.lines, a["text"], a["exclude"], exact)
			if !ok {
				_, partial := findLines(sr.lines, a["text"])
				wrapped := wrappedLines(sr.lines, a["text"])
				env.shot = screenShot(sr, nil, append(append(hits, partial...), wrapped...))
				return "", fmt.Errorf("after %s, %d line(s) on screen show %q -- needed more than %d%s", timeout, len(hits), a["text"], more, wrappedNote(wrapped))
			}
			env.shot = screenShot(sr, hits, nil)
			return fmt.Sprintf("%d line(s) on screen show %q", len(hits), a["text"]), nil
		},
	},
	{
		Op:       "click-text",
		Summary:  "Find a line of text on screen (a button, menu item, label) and click it.",
		Describe: `{button}-click the line on screen reading "{text}" (waiting up to {timeout} for it)`,
		Args: []opArg{
			{Name: "text", Help: "the text to click", Required: true},
			{Name: "exact", Help: "true: the line must read exactly this; false: it need only contain it", Default: "true"},
			{Name: "exclude", Help: "skip lines containing this"},
			{Name: "button", Help: "left, right or middle", Default: "left"},
			{Name: "double", Help: "true for a double click", Default: "false"},
			{Name: "pick", Help: "top or bottom: which match to click if several", Default: "top"},
			{Name: "new_since", Help: "a count-text's save_as name: only click a line that wasn't among those it counted"},
			{Name: "timeout", Help: "how long to keep looking", Default: "5s"},
		},
		run: func(env *seqEnv, a opArgs) (string, error) {
			exact, err := a.flag("exact")
			if err != nil {
				return "", err
			}
			var seen []image.Rectangle
			if name := strings.TrimSpace(a["new_since"]); name != "" {
				var ok bool
				if seen, ok = env.seen[name]; !ok {
					return "", fmt.Errorf("new_since: no count-text saved %q earlier in the run", name)
				}
			}
			matches := func(sr *screenReading) []OCRLine {
				return notSeen(textMatches(sr.lines, a["text"], a["exclude"], exact), seen)
			}
			double, err := a.flag("double")
			if err != nil {
				return "", err
			}
			timeout, err := a.dur("timeout")
			if err != nil {
				return "", err
			}
			if a["pick"] != "top" && a["pick"] != "bottom" {
				return "", fmt.Errorf("pick should be top or bottom, not %q", a["pick"])
			}
			if _, ok := pointerButtons[a["button"]]; !ok {
				return "", fmt.Errorf("button should be left, right or middle, not %q", a["button"])
			}
			sr, ok, err := pollScreen(timeout, func(sr *screenReading) bool {
				return len(matches(sr)) > 0
			})
			if err != nil {
				return "", err
			}
			hits := matches(sr)
			if !ok {
				_, partial := findLines(sr.lines, a["text"])
				wrapped := wrappedLines(sr.lines, a["text"])
				env.shot = screenShot(sr, nil, append(partial, wrapped...))
				msg := fmt.Sprintf("no line on screen reads %q (read %d lines of text)", a["text"], len(sr.lines))
				if seen != nil {
					msg = fmt.Sprintf("no new line on screen reads %q (read %d lines of text; %d seen before don't count)", a["text"], len(sr.lines), len(seen))
				}
				if len(partial) > 0 {
					msg += "; lines containing it: " + quoteLines(partial)
				}
				return "", errors.New(msg + wrappedNote(wrapped))
			}
			i := 0
			if a["pick"] == "bottom" {
				i = len(hits) - 1
			}
			hit := hits[i]
			others := append(append([]OCRLine{}, hits[:i]...), hits[i+1:]...)
			env.shot = screenShot(sr, []OCRLine{hit}, others)
			at := hit.center()
			via, err := clickPoint(at.X, at.Y, sr.img.Bounds().Dx(), a["button"], double)
			if err != nil {
				return "", fmt.Errorf("found %q at (%d, %d) but clicking it via %s failed: %w", hit.Text, at.X, at.Y, via, err)
			}
			sleep(focusSettle)
			desc := fmt.Sprintf("saw %q at (%d, %d) and %s-clicked it via %s", hit.Text, at.X, at.Y, a["button"], via)
			if len(hits) > 1 {
				desc += fmt.Sprintf(" (%s of %d matches)", a["pick"], len(hits))
			}
			return desc, nil
		},
	},
	{
		Op:       "click-icon",
		Summary:  "Find an app's icon on screen (in the dock, say) by its picture and click it.",
		Describe: "{button}-click {app}'s icon on screen (waiting up to {timeout} for it)",
		Args: []opArg{
			{Name: "app", Help: "the app, as named in its .desktop file (firefox, org.gnome.TextEditor, ...)"},
			{Name: "icon", Help: "or: the icon's PNG file, to match instead of the app's own"},
			{Name: "button", Help: "left, right or middle", Default: "left"},
			{Name: "double", Help: "true for a double click", Default: "false"},
			{Name: "timeout", Help: "how long to keep looking", Default: "5s"},
		},
		run: runClickIcon,
	},
	{
		Op:       "read-text",
		Summary:  "Read a value off the screen: the first match of a pattern on a line containing some text. Saves it, and can print it with the run's results.",
		Describe: `read /{pattern}/ off the line on screen containing "{near}" as {{{save_as}}}, waiting up to {timeout}`,
		Args: []opArg{
			{Name: "pattern", Help: "a regular expression; its first (...) group is the value, if it has one", Required: true},
			{Name: "near", Help: "only lines containing this text"},
			{Name: "save_as", Help: "name to save the value under"},
			{Name: "report", Help: "if set, print the value under this label with the run's results"},
			{Name: "timeout", Help: "how long to keep looking", Default: "10s"},
		},
		run: runReadText,
	},
	{
		Op:       "print",
		Summary:  "Print a line with the run's results.",
		Describe: `print "{text}"`,
		Args: []opArg{
			{Name: "text", Help: "what to print -- {{name}} values included", Required: true},
			{Name: "label", Help: "an optional label for it"},
		},
		run: func(env *seqEnv, a opArgs) (string, error) {
			env.outputs = append(env.outputs, SequenceOutput{Label: a["label"], Value: a["text"]})
			return "printed " + strconv.Quote(a["text"]), nil
		},
	},
	{
		Op:       "open",
		Summary:  "Open a file in its default app, as double-clicking it would (gio open, else xdg-open). The app may open behind other windows -- follow with a click on its title to focus it.",
		Describe: "open {path} in its default app",
		Args:     []opArg{{Name: "path", Help: "absolute path, e.g. {{desktop}}/notes.txt", Required: true}},
		run: func(env *seqEnv, a opArgs) (string, error) {
			p, err := a.path("path")
			if err != nil {
				return "", err
			}
			via, err := openPath(p)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("opened %s via %s", p, via), nil
		},
	},
	{
		Op:       "create-file",
		Summary:  "Create a file (on this host, directly).",
		Describe: "create the file {path}",
		Args: []opArg{
			{Name: "path", Help: "absolute path, e.g. {{desktop}}/notes.txt", Required: true},
			{Name: "content", Help: "what to put in it (empty by default)"},
			{Name: "overwrite", Help: "true: replace the file if it exists", Default: "false"},
		},
		run: func(env *seqEnv, a opArgs) (string, error) {
			p, err := a.path("path")
			if err != nil {
				return "", err
			}
			overwrite, err := a.flag("overwrite")
			if err != nil {
				return "", err
			}
			flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			if !overwrite {
				flags |= os.O_EXCL
			}
			f, err := os.OpenFile(p, flags, 0o644)
			if errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("%s already exists (set overwrite to true to replace it)", p)
			}
			if err != nil {
				return "", err
			}
			_, werr := f.WriteString(a["content"])
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				return "", werr
			}
			return "created " + p, nil
		},
	},
	{
		Op:       "expect-file",
		Summary:  "Check a file contains some text -- e.g. that typing and saving into it worked.",
		Describe: `check {path} contains "{contains}"`,
		Args: []opArg{
			{Name: "path", Help: "absolute path", Required: true},
			{Name: "contains", Help: "the text it must contain", Required: true},
		},
		run: func(env *seqEnv, a opArgs) (string, error) {
			p, err := a.path("path")
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			if !strings.Contains(string(data), a["contains"]) {
				excerpt := string(data)
				if len(excerpt) > fileExcerptLen {
					excerpt = excerpt[:fileExcerptLen] + "…"
				}
				return "", fmt.Errorf("%s doesn't contain %q; it holds %q", p, a["contains"], excerpt)
			}
			return fmt.Sprintf("%s contains %q", p, a["contains"]), nil
		},
	},
	{
		Op:       "save-screenshot",
		Summary:  "Capture the screen and save it as a PNG file.",
		Describe: "save a screenshot to {path}",
		Args:     []opArg{{Name: "path", Help: "absolute path, e.g. {{desktop}}/shot-{{timestamp}}.png", Required: true}},
		run: func(env *seqEnv, a opArgs) (string, error) {
			p, err := a.path("path")
			if err != nil {
				return "", err
			}
			img, from, err := captureScreen()
			if err != nil {
				return "", fmt.Errorf("capturing the screen: %w", err)
			}
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err != nil {
				return "", err
			}
			if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
				return "", err
			}
			env.shot = jpegDataURL(img, shotThumbWidth)
			b := img.Bounds()
			return fmt.Sprintf("saved a %dx%d screenshot (via %s) to %s", b.Dx(), b.Dy(), from, p), nil
		},
	},
	{
		Op:       "delete-file",
		Summary:  "Delete a file (not a folder).",
		Describe: "delete the file {path}",
		Args:     []opArg{{Name: "path", Help: "absolute path", Required: true}},
		run: func(env *seqEnv, a opArgs) (string, error) {
			p, err := a.path("path")
			if err != nil {
				return "", err
			}
			fi, err := os.Lstat(p)
			if err != nil {
				return "", err
			}
			if !fi.Mode().IsRegular() {
				return "", fmt.Errorf("%s isn't a regular file; not deleting it", p)
			}
			if err := os.Remove(p); err != nil {
				return "", err
			}
			return "deleted " + p, nil
		},
	},
}

// focusByTitle focuses the window titled title: by sight, then through the
// window manager. federation-command's window goes through
// focusFederationCommand (window.go), whose fallback can also find it by
// process.
// Overridable in tests.
var focusByTitle = func(title string) (string, string, error) {
	if title == fcWindowTitle {
		return focusFederationCommand()
	}
	desc, shot, visErr := focusViaVision(title)
	if visErr == nil {
		return desc, shot, nil
	}
	desc, err := focusWindow(windowTarget{title: title}, focusAttempts())
	if err == nil {
		return fmt.Sprintf("%s (visual detection failed: %v)", desc, visErr), shot, nil
	}
	return "", shot, fmt.Errorf("visual detection: %v; window-manager fallback: %v", visErr, err)
}

// openPath opens p in its default app and returns the command that did it.
// It runs the command directly rather than typing it into GNOME's Alt+F2
// Run dialog: in Revision I's debug run the dialog never took the typed
// command, with no menu left open to blame. Overridable in tests.
var openPath = func(p string) (string, error) {
	var errs []string
	for _, c := range [][]string{{"gio", "open", p}, {"xdg-open", p}} {
		bin, err := exec.LookPath(c[0])
		if err != nil {
			errs = append(errs, c[0]+" isn't installed")
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
		cmd := exec.CommandContext(ctx, bin, c[1:]...)
		cmd.Env = guiEnv()
		// The app it starts can inherit the output pipe and hold it open;
		// don't wait on it once the command itself has exited.
		cmd.WaitDelay = time.Second
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil || (errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success()) {
			return c[0], nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v: %s", c[0], err, strings.TrimSpace(string(out))))
	}
	return "", fmt.Errorf("couldn't open %s: %s", p, strings.Join(errs, "; "))
}

// guiEnv is IANAR's environment, plus WAYLAND_DISPLAY if neither it nor
// DISPLAY is set but the session's Wayland socket exists (IANAR started
// from a service that didn't inherit them), so apps it opens can show.
func guiEnv() []string {
	env := os.Environ()
	if os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != "" {
		return env
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		if _, err := os.Stat(filepath.Join(rt, "wayland-0")); err == nil {
			env = append(env, "WAYLAND_DISPLAY=wayland-0")
		}
	}
	return env
}

func runReadText(env *seqEnv, a opArgs) (string, error) {
	re, err := regexp.Compile(a["pattern"])
	if err != nil {
		return "", fmt.Errorf("pattern: %v", err)
	}
	timeout, err := a.dur("timeout")
	if err != nil {
		return "", err
	}
	near := normalizeText(a["near"])
	var value string
	var hit OCRLine
	find := func(sr *screenReading) bool {
		for _, l := range sr.lines {
			if near != "" && !strings.Contains(normalizeText(l.Text), near) {
				continue
			}
			if m := re.FindStringSubmatch(l.Text); m != nil {
				value, hit = m[0], l
				if len(m) > 1 && m[1] != "" {
					value = m[1]
				}
				return true
			}
		}
		return false
	}
	sr, ok, err := pollScreen(timeout, find)
	if err != nil {
		return "", err
	}
	if !ok {
		var nearLines []OCRLine
		where := ""
		if near != "" {
			nearLines = textMatches(sr.lines, a["near"], "", false)
			where = fmt.Sprintf(" containing %q", a["near"])
			if len(nearLines) > 0 {
				where += " (" + quoteLines(nearLines) + ")"
			}
		}
		env.shot = screenShot(sr, nil, nearLines)
		return "", fmt.Errorf("after %s, no line on screen%s matches /%s/", timeout, where, a["pattern"])
	}
	env.shot = screenShot(sr, []OCRLine{hit}, nil)
	if err := a.saveVar(env, value); err != nil {
		return "", err
	}
	if a["report"] != "" {
		env.outputs = append(env.outputs, SequenceOutput{Label: a["report"], Value: value})
	}
	return fmt.Sprintf("read %q from the line %q", value, hit.Text), nil
}

func runClickIcon(env *seqEnv, a opArgs) (string, error) {
	double, err := a.flag("double")
	if err != nil {
		return "", err
	}
	timeout, err := a.dur("timeout")
	if err != nil {
		return "", err
	}
	if _, ok := pointerButtons[a["button"]]; !ok {
		return "", fmt.Errorf("button should be left, right or middle, not %q", a["button"])
	}
	iconPath := strings.TrimSpace(a["icon"])
	if iconPath == "" {
		if strings.TrimSpace(a["app"]) == "" {
			return "", errors.New("give an app or an icon file")
		}
		if iconPath, err = appIconFile(a["app"]); err != nil {
			return "", err
		}
	}
	icon, err := loadIconImage(iconPath)
	if err != nil {
		return "", err
	}
	what := a["app"]
	if what == "" {
		what = filepath.Base(iconPath)
	}

	deadline := clock().Add(timeout)
	for {
		img, _, err := captureScreen()
		if err != nil {
			return "", fmt.Errorf("capturing the screen: %w", err)
		}
		m := locateIcon(img, icon)
		if m.found {
			marked := annotate(img, map[color.RGBA][]image.Rectangle{matchColor: {m.rect}})
			at := image.Pt((m.rect.Min.X+m.rect.Max.X)/2, (m.rect.Min.Y+m.rect.Max.Y)/2)
			env.shot = jpegDataURL(cropAround(marked, at, matchCropW, matchCropH), matchCropW)
			via, err := clickPoint(at.X, at.Y, img.Bounds().Dx(), a["button"], double)
			if err != nil {
				return "", fmt.Errorf("found %s's icon at (%d, %d) but clicking it via %s failed: %w", what, at.X, at.Y, via, err)
			}
			sleep(focusSettle)
			return fmt.Sprintf("found %s's icon (%s) at (%d, %d), %dpx, difference %.0f/255, and %s-clicked it via %s",
				what, iconPath, at.X, at.Y, m.rect.Dx(), m.score, a["button"], via), nil
		}
		if !clock().Before(deadline) {
			var near []image.Rectangle
			if !m.rect.Empty() {
				near = append(near, m.rect)
			}
			env.shot = jpegDataURL(annotate(img, map[color.RGBA][]image.Rectangle{otherColor: near}), inspectMaxWidth)
			msg := fmt.Sprintf("didn't find %s's icon (%s) on screen", what, iconPath)
			if !m.rect.Empty() {
				msg += fmt.Sprintf("; the closest likeness, boxed, differs by %.0f/255 (%.0f or less counts as found)", m.score, iconMaxScore)
			}
			return "", errors.New(msg + ". Is it showing -- the dock may be hidden while a window is maximized?")
		}
		sleep(screenPollInterval)
	}
}
