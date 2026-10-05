package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---- YAML ----

func TestExampleLibraryIsValid(t *testing.T) {
	actions, sequences := exampleLibrary()
	if err := checkLibrary(actions, sequences); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"fc-hello-world", "firefox-weather", "desktop-text-file"} {
		if indexOfSequence(sequences, id) < 0 {
			t.Errorf("no example sequence %q", id)
		}
	}
}

func TestSeqLibYAMLRoundTrip(t *testing.T) {
	actions, sequences := exampleLibrary()
	text := encodeSeqLib(seqLibDoc{Actions: actions, Sequences: sequences})
	doc, err := decodeSeqLib(text)
	if err != nil {
		t.Fatalf("decoding what encodeSeqLib wrote: %v\n%s", err, text)
	}
	if !reflect.DeepEqual(doc.Actions, actions) {
		t.Errorf("actions changed in the round trip:\n got %#v\nwant %#v", doc.Actions, actions)
	}
	if !reflect.DeepEqual(doc.Sequences, sequences) {
		t.Errorf("sequences changed in the round trip:\n got %#v\nwant %#v", doc.Sequences, sequences)
	}
}

func TestDecodeHandWrittenYAML(t *testing.T) {
	src := `---
# a hand-written library
format: ianar-sequence-v2
actions:
- id: greet          # a sequence at the same indent as its key
  name: 'Greet: someone''s window'
  controls:
    - name: who
      default: "world"
  do:
    - {op: type, text: "hello {{who}}"}
    - op: key
      keys: enter
    - {op: print, text: |-
        unused}
sequences:
  - id: hi
    name: Say hi
    steps:
      - action: greet
        with:
          who: Ana   # comment after a value
      - action: greet
`
	// The flow mapping above with a | block isn't supported -- check the
	// error is clear, then try the version without it.
	if _, err := decodeSeqLib(src); err == nil {
		t.Fatal("expected an error for a literal block inside {...}")
	}
	src = strings.Replace(src, "    - {op: print, text: |-\n        unused}\n", "    - op: print\n      text: |\n        line one\n        line two\n", 1)
	doc, err := decodeSeqLib(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Actions) != 1 || len(doc.Sequences) != 1 {
		t.Fatalf("got %d actions, %d sequences", len(doc.Actions), len(doc.Sequences))
	}
	a := doc.Actions[0]
	if a.Name != "Greet: someone's window" {
		t.Errorf("name = %q", a.Name)
	}
	if !reflect.DeepEqual(a.Controls, []Control{{Name: "who", Default: "world"}}) {
		t.Errorf("controls = %#v", a.Controls)
	}
	want := []Instruction{
		{"op": "type", "text": "hello {{who}}"},
		{"op": "key", "keys": "enter"},
		{"op": "print", "text": "line one\nline two\n"},
	}
	if !reflect.DeepEqual(a.Do, want) {
		t.Errorf("do = %#v", a.Do)
	}
	q := doc.Sequences[0]
	if len(q.Steps) != 2 || q.Steps[0].With["who"] != "Ana" || q.Steps[1].Action != "greet" {
		t.Errorf("steps = %#v", q.Steps)
	}
	if err := checkLibrary(doc.Actions, doc.Sequences); err != nil {
		t.Error(err)
	}
}

func TestDecodeSeqLibErrors(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"format: other\nactions: []\n", `format is "other"`},
		{"actions:\n  - id: a\n    nmae: x\n", `unknown key "nmae"`},
		{"actions:\n  - id: a\n    do: {op: key}\n", "do should be a list"},
		{"sequences:\n  - id: q\n    steps:\n      - action: a\n      bad\n", "line 5"},
		{"\tactions: []\n", "tabs"},
		{"actions: []\n", "no actions or sequences"},
		{`actions: [{id: "a}]`, "unterminated"},
	} {
		_, err := decodeSeqLib(c.src)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("decodeSeqLib(%q) = %v, want an error containing %q", c.src, err, c.want)
		}
	}
}

func TestYAMLScalarQuoting(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"federation-command", "federation-command"},
		{"New Private Window", "New Private Window"},
		{"500", `"500"`},
		{"true", `"true"`},
		{"{{title}}", `"{{title}}"`},
		{"a: b", `"a: b"`},
		{"", `""`},
		{"-x", `"-x"`},
	} {
		if got := yamlScalar(c.in); got != c.want {
			t.Errorf("yamlScalar(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// ---- Templates, keys, validation ----

func TestExpand(t *testing.T) {
	vars := map[string]string{"country": "United Kingdom", "file": "/home/u/Desktop/a.txt"}
	lookup := func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
	got, err := expand("https://wttr.in/{{country|url}}?x / {{ file | base }} / {{country}}", lookup)
	if err != nil || got != "https://wttr.in/United+Kingdom?x / a.txt / United Kingdom" {
		t.Errorf("expand = %q, %v", got, err)
	}
	if _, err := expand("{{nope}}", lookup); err == nil || !strings.Contains(err.Error(), "{{nope}}") {
		t.Errorf("unknown name: err = %v", err)
	}
	if err := checkTemplate("{{x|shout}}"); err == nil {
		t.Error("an unknown filter should be rejected")
	}
}

func TestParseChords(t *testing.T) {
	got, err := parseChords("end ctrl+u Ctrl+Shift+P alt+f2 ctrl+plus ctrl+=")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"end"}, {"ctrl", "u"}, {"ctrl", "shift", "p"}, {"alt", "f2"}, {"ctrl", "+"}, {"ctrl", "="}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseChords = %q, want %q", got, want)
	}
	for _, bad := range []string{"", "ctrl++", "u+ctrl", "ctrl+nosuchkey"} {
		if _, err := parseChords(bad); err == nil {
			t.Errorf("parseChords(%q) should fail", bad)
		}
	}
}

func TestActionValidation(t *testing.T) {
	ok := ActionDef{ID: "a", Name: "A", Do: []Instruction{{"op": "key", "keys": "enter"}}}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		mod  func(*ActionDef)
		want string
	}{
		{func(a *ActionDef) { a.ID = "Bad Id" }, "ids are"},
		{func(a *ActionDef) { a.Do = nil }, "no instructions"},
		{func(a *ActionDef) { a.Do = []Instruction{{"op": "fly"}} }, `unknown op "fly"`},
		{func(a *ActionDef) { a.Do = []Instruction{{"op": "key"}} }, `needs "keys"`},
		{func(a *ActionDef) { a.Do = []Instruction{{"op": "key", "keys": "x", "kyes": "y"}} }, `no argument "kyes"`},
		{func(a *ActionDef) { a.Controls = []Control{{Name: "desktop"}} }, "built-in"},
		{func(a *ActionDef) { a.Controls = []Control{{Name: "x"}, {Name: "x"}} }, "twice"},
	} {
		a := ok
		c.mod(&a)
		if err := a.validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("validate = %v, want an error containing %q", err, c.want)
		}
	}
}

// ---- The library ----

func TestSeqLibraryEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ianar", "lib.yaml")
	l := openSeqLibrary(path)

	// Renaming an action updates the sequences using it.
	actions := l.actionMap()
	a := actions["press-keys"]
	a.ID = "keys"
	if err := l.saveAction(a, "press-keys"); err != nil {
		t.Fatal(err)
	}
	for _, q := range l.sequences {
		for _, st := range q.Steps {
			if st.Action == "press-keys" {
				t.Fatalf("sequence %q still uses press-keys", q.ID)
			}
		}
	}
	// An action in use can't be deleted; a new one can be added and removed.
	if err := l.deleteAction("keys"); err == nil || !strings.Contains(err.Error(), "used by") {
		t.Errorf("deleting a used action: %v", err)
	}
	extra := ActionDef{ID: "extra", Name: "Extra", Do: []Instruction{{"op": "wait", "duration": "1s"}}}
	if err := l.saveAction(extra, ""); err != nil {
		t.Fatal(err)
	}
	if err := l.saveAction(ActionDef{ID: "extra", Name: "dup", Do: extra.Do}, "keys"); err == nil {
		t.Error("renaming onto an existing id should fail")
	}
	// A sequence that uses a missing action is refused.
	if err := l.saveSequence(SequenceV2{ID: "s", Name: "S", Steps: []StepRef{{Action: "missing"}}}, ""); err == nil {
		t.Error("a sequence using a missing action should be refused")
	}
	if err := l.saveSequence(SequenceV2{ID: "s", Name: "S", Steps: []StepRef{{Action: "extra"}}}, ""); err != nil {
		t.Fatal(err)
	}

	// It was all saved: reopening finds the same library.
	again := openSeqLibrary(path)
	if again.note != "" {
		t.Fatal(again.note)
	}
	if !reflect.DeepEqual(again.actions, l.actions) || !reflect.DeepEqual(again.sequences, l.sequences) {
		t.Error("the reopened library differs")
	}

	// Exporting a sequence brings the actions it uses; importing it
	// elsewhere works.
	text, name, err := l.export("sequences", []string{"s"})
	if err != nil || name != "ianar-sequence-s.yaml" || !strings.Contains(text, "id: extra") || strings.Contains(text, "id: keys") {
		t.Fatalf("export = %q, %q, %v", name, text, err)
	}
	other := openSeqLibrary("")
	doc, err := decodeSeqLib(text)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := other.merge(doc)
	if err != nil || !strings.Contains(msg, "added action extra, sequence s") {
		t.Errorf("merge = %q, %v", msg, err)
	}
}

func TestOpenSeqLibrarySetsABrokenFileAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lib.yaml")
	os.WriteFile(path, []byte("actions:\n  - id: [oops\n"), 0o644)
	l := openSeqLibrary(path)
	if !strings.Contains(l.note, "moved it to") || len(l.sequences) == 0 {
		t.Errorf("note = %q, %d sequences", l.note, len(l.sequences))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the broken file should have been moved aside")
	}
}

// ---- Running ----

// stubScreenLines makes readScreen return, on its nth call, screens(n).
func stubScreenLines(t *testing.T, screens func(n int) []OCRLine) *int {
	t.Helper()
	orig := readScreen
	t.Cleanup(func() { readScreen = orig })
	n := 0
	readScreen = func() (*screenReading, error) {
		lines := screens(n)
		n++
		return &screenReading{img: image.NewRGBA(image.Rect(0, 0, 1280, 720)), from: "test", lines: lines}, nil
	}
	return &n
}

type click struct {
	button string
	at     image.Point
}

// stubClicks records clicks instead of making them.
func stubClicks(t *testing.T) *[]click {
	t.Helper()
	orig := clickPoint
	t.Cleanup(func() { clickPoint = orig })
	var clicks []click
	clickPoint = func(x, y, w int, button string, double bool) (string, error) {
		clicks = append(clicks, click{button, image.Pt(x, y)})
		return "test", nil
	}
	return &clicks
}

func runV2(t *testing.T, id string, values map[string]string) SequenceResultMsg {
	t.Helper()
	q, err := openSeqLibrary("").compile(id, values, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return runSequence(q, func(SequenceProgressMsg) {})
}

func TestRunFCHelloWorldV2(t *testing.T) {
	kb, _ := stubSequence(t)
	// The output check counts once before Enter, then waits for one more.
	stubScreenLines(t, func(n int) []OCRLine {
		lines := []OCRLine{{Text: `$ echo "hello world!"`, X: 0, Y: 0, W: 100, H: 10}}
		for i := 0; i <= min(n, 1); i++ {
			lines = append(lines, OCRLine{Text: "hello world!", X: 0, Y: 20 + 20*i, W: 100, H: 10})
		}
		return lines
	})
	res := runV2(t, "fc-hello-world", nil)
	if !res.Success {
		t.Fatalf("run failed: %s", res.Error)
	}
	want := []string{"right", "end", "ctrl+u", `type echo "hello world!"`, "enter", "left"}
	if !reflect.DeepEqual(kb.calls, want) {
		t.Errorf("keys = %q, want %q", kb.calls, want)
	}
}

func TestRunFCHelloWorldV2FailsWhenNoOutputAppears(t *testing.T) {
	stubSequence(t)
	stubScreenLines(t, func(int) []OCRLine { return []OCRLine{{Text: "hello world!", W: 10, H: 10}} })
	res := runV2(t, "fc-hello-world", nil)
	if res.Success || res.FailedStep != 4 || !strings.Contains(res.Error, "needed more than 1") {
		t.Errorf("result = %+v", res)
	}
}

// testIcon is a 64x64 icon of four colored quadrants on a transparent
// border.
func testIcon() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	cols := []color.NRGBA{{230, 90, 20, 255}, {40, 60, 200, 255}, {250, 210, 40, 255}, {120, 30, 160, 255}}
	for y := 4; y < 60; y++ {
		for x := 4; x < 60; x++ {
			img.SetNRGBA(x, y, cols[(y/32)*2+x/32])
		}
	}
	return img
}

// screenWithIcon is a grey screen with icon drawn size x size at at.
func screenWithIcon(icon image.Image, at image.Point, size int) *image.RGBA {
	scr := image.NewRGBA(image.Rect(0, 0, 640, 400))
	for i := range scr.Pix {
		scr.Pix[i] = 0x50
	}
	b := icon.Bounds()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			c := color.NRGBAModel.Convert(icon.At(x*b.Dx()/size, y*b.Dy()/size)).(color.NRGBA)
			if c.A > 128 {
				scr.Set(at.X+x, at.Y+y, color.RGBA{c.R, c.G, c.B, 255})
			}
		}
	}
	return scr
}

func TestLocateIcon(t *testing.T) {
	icon := testIcon()
	m := locateIcon(screenWithIcon(icon, image.Pt(300, 340), 48), icon)
	if !m.found {
		t.Fatalf("not found: best %v, score %.1f", m.rect, m.score)
	}
	c := image.Pt((m.rect.Min.X+m.rect.Max.X)/2, (m.rect.Min.Y+m.rect.Max.Y)/2)
	if abs(c.X-324) > 3 || abs(c.Y-364) > 3 {
		t.Errorf("found at %v (center %v), want centered near (324, 364)", m.rect, c)
	}
	if m := locateIcon(screenWithIcon(icon, image.Pt(0, 0), 0), icon); m.found {
		t.Errorf("found an icon on a blank screen: %v, score %.1f", m.rect, m.score)
	}
}

// stubIconApp installs a fake firefox.desktop naming icon, under a temp
// data dir.
func stubIconApp(t *testing.T, icon image.Image) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "applications"), 0o755)
	iconPath := filepath.Join(dir, "firefox.png")
	f, _ := os.Create(iconPath)
	png.Encode(f, icon)
	f.Close()
	os.WriteFile(filepath.Join(dir, "applications", "firefox_firefox.desktop"),
		[]byte("[Desktop Entry]\nName=Firefox\nIcon="+iconPath+"\n\n[Desktop Action new-private-window]\nIcon=other\n"), 0o644)
	orig := iconDataDirs
	t.Cleanup(func() { iconDataDirs = orig })
	iconDataDirs = func() []string { return []string{dir} }
}

func stubCapture(t *testing.T, img image.Image) {
	t.Helper()
	orig := captureScreen
	t.Cleanup(func() { captureScreen = orig })
	captureScreen = func() (image.Image, string, error) { return img, "test", nil }
}

func TestRunFirefoxWeather(t *testing.T) {
	kb, _ := stubSequence(t)
	icon := testIcon()
	stubIconApp(t, icon)
	stubCapture(t, screenWithIcon(icon, image.Pt(10, 300), 48))
	clicks := stubClicks(t)
	// Screens: the dock menu, the private window, then the weather page.
	stubScreenLines(t, func(n int) []OCRLine {
		switch n {
		case 0:
			return []OCRLine{{Text: "New Window", X: 60, Y: 200, W: 100, H: 12}, {Text: "New Private Window", X: 60, Y: 230, W: 160, H: 12}}
		case 1:
			return []OCRLine{{Text: "New Private Tab", X: 10, Y: 10, W: 100, H: 12}}
		default:
			return []OCRLine{
				{Text: "wttr.in/Spain?format=%l:+%t&m", X: 10, Y: 40, W: 300, H: 12},
				{Text: "Spain: +18°C", X: 10, Y: 90, W: 120, H: 14},
			}
		}
	})
	res := runV2(t, "firefox-weather", map[string]string{"country": "Spain"})
	if !res.Success {
		t.Fatalf("run failed: %s", res.Error)
	}
	// The icon (drawn at (10, 300), 48px) is right-clicked near its
	// center, then the menu item's center is clicked.
	c := *clicks
	if len(c) != 2 || c[0].button != "right" || abs(c[0].at.X-34) > 3 || abs(c[0].at.Y-324) > 3 ||
		c[1] != (click{"left", image.Pt(140, 236)}) {
		t.Errorf("clicks = %+v, want a right click near (34, 324), then a left click at (140, 236)", c)
	}
	wantKeys := []string{"escape", "ctrl+l", "type https://wttr.in/Spain?format=%l:+%t&m", "enter", "ctrl+=", "ctrl+=", "ctrl+="}
	if !reflect.DeepEqual(kb.calls, wantKeys) {
		t.Errorf("keys = %q, want %q", kb.calls, wantKeys)
	}
	if want := []SequenceOutput{{Label: "Temperature in Spain", Value: "+18°C"}}; !reflect.DeepEqual(res.Outputs, want) {
		t.Errorf("outputs = %+v, want %+v", res.Outputs, want)
	}
}

// savingKeyboard is a fakeKeyboard that, like an editor, writes what was
// typed to file on Ctrl+S.
type savingKeyboard struct {
	fakeKeyboard
	file, typed string
}

func (k *savingKeyboard) tap(key string, mods ...string) error {
	if key == "s" && len(mods) == 1 && mods[0] == "ctrl" {
		os.WriteFile(k.file, []byte(k.typed), 0o644)
	}
	return k.fakeKeyboard.tap(key, mods...)
}

func (k *savingKeyboard) typeText(text string) error {
	if !strings.HasPrefix(text, "xdg-open") {
		k.typed += text
	}
	return k.fakeKeyboard.typeText(text)
}

func TestRunDesktopTextFile(t *testing.T) {
	stubSequence(t)
	desk := t.TempDir()
	origDesk := desktopDir
	t.Cleanup(func() { desktopDir = origDesk })
	desktopDir = func() string { return desk }
	file := filepath.Join(desk, "ianar-hello-world.txt")
	kb := &savingKeyboard{fakeKeyboard: fakeKeyboard{failAt: -1}, file: file}
	openCompositorKeyboard = func() (keyboard, func(), error) { return kb, func() {}, nil }
	stubCapture(t, image.NewRGBA(image.Rect(0, 0, 64, 48)))
	// Before opening: the desktop icon's label. After: the editor's title too.
	stubScreenLines(t, func(n int) []OCRLine {
		lines := []OCRLine{{Text: "ianar-hello-world.txt", X: 10, Y: 300, W: 120, H: 12}}
		if n > 0 {
			lines = append(lines, OCRLine{Text: "ianar-hello-world.txt", X: 400, Y: 10, W: 120, H: 12})
		}
		return lines
	})

	res := runV2(t, "desktop-text-file", nil)
	if !res.Success {
		t.Fatalf("run failed: %s", res.Error)
	}
	want := []string{"escape", "alt+f2", `type xdg-open "` + file + `"`, "enter", "type hello world", "ctrl+s", "ctrl+w"}
	if !reflect.DeepEqual(kb.calls, want) {
		t.Errorf("keys = %q, want %q", kb.calls, want)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("the text document should have been deleted")
	}
	shots, _ := filepath.Glob(filepath.Join(desk, "ianar-hello-world-2026-10-05T12-00-00.png"))
	if len(shots) != 1 {
		entries, _ := os.ReadDir(desk)
		t.Errorf("no screenshot on the desktop; it holds %v", entries)
	}
}

func TestRunStopsAtAFileCheck(t *testing.T) {
	stubSequence(t)
	desk := t.TempDir()
	origDesk := desktopDir
	t.Cleanup(func() { desktopDir = origDesk })
	desktopDir = func() string { return desk }
	stubCapture(t, image.NewRGBA(image.Rect(0, 0, 64, 48)))
	stubScreenLines(t, func(n int) []OCRLine {
		if n == 0 {
			return nil
		}
		return []OCRLine{{Text: "ianar-hello-world.txt", W: 10, H: 10}}
	})
	// The fake keyboard saves nothing, so the document stays empty.
	res := runV2(t, "desktop-text-file", nil)
	if res.Success || res.FailedStep != 5 || !strings.Contains(res.Error, `doesn't contain "hello world"`) {
		t.Errorf("result = %+v", res)
	}
}

func TestCompileFillsLabelsAndDetail(t *testing.T) {
	q, err := openSeqLibrary("").compile("firefox-weather", map[string]string{"country": "Portugal"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d := q.def()
	if d.Steps[3].Label != "Print the temperature in Portugal" {
		t.Errorf("label = %q", d.Steps[3].Label)
	}
	if got := strings.Join(d.Steps[1].Detail, " | "); !strings.Contains(got, "type \"https://wttr.in/Portugal?format=%l:+%t&m\"") {
		t.Errorf("detail = %q", got)
	}
	if _, err := openSeqLibrary("").compile("nope", nil, time.Now()); err == nil {
		t.Error("compiling a missing sequence should fail")
	}
}
