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
	for _, id := range []string{"fc-hello-world", "firefox-weather"} {
		if indexOfSequence(sequences, id) < 0 {
			t.Errorf("no example sequence %q", id)
		}
	}
	// Retired examples aren't shipped any more.
	shipped := shippedExamples()
	for _, key := range retiredExamples {
		if _, ok := shipped[key]; ok {
			t.Errorf("retired example %s is still shipped", key)
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

// oldOpenURL is open-url as an older IANAR might have shipped it: without
// selecting the address bar first.
func oldOpenURL(actions []ActionDef) ActionDef {
	a := actions[indexOfAction(actions, "open-url")]
	a.Do = append([]Instruction(nil), a.Do[1:]...)
	return a
}

func TestOpenSeqLibraryUpgradesUneditedExamples(t *testing.T) {
	actions, sequences := exampleLibrary()
	old := oldOpenURL(actions)
	actions[indexOfAction(actions, "open-url")] = old
	// press-keys was edited after an older IANAR saved it.
	pk := indexOfAction(actions, "press-keys")
	edited := actions[pk]
	edited.Description = "my own description"
	actions[pk] = edited
	rec := shippedExamples()
	rec[exampleKey("action", "open-url")] = actionPrint(old)
	rec[exampleKey("action", "press-keys")] = "0123456789abcdef"

	path := filepath.Join(t.TempDir(), "lib.yaml")
	os.WriteFile(path, []byte(encodeSeqLib(seqLibDoc{Actions: actions, Sequences: sequences, Examples: rec})), 0o644)
	l := openSeqLibrary(path)
	shipped, _ := exampleLibrary()
	if got := l.actionMap()["open-url"]; !reflect.DeepEqual(got, shipped[indexOfAction(shipped, "open-url")]) {
		t.Errorf("open-url wasn't updated: %+v", got)
	}
	if got := l.actionMap()["press-keys"]; got.Description != "my own description" {
		t.Error("the edited press-keys was replaced")
	}
	if !strings.Contains(l.note, "updated") || !strings.Contains(l.note, "action open-url") {
		t.Errorf("note = %q", l.note)
	}
	if note := l.snapshot().Note; !strings.Contains(note, "action press-keys differs") {
		t.Errorf("snapshot note = %q", note)
	}
	if w := l.staleFor("firefox-weather"); len(w) != 1 || !strings.Contains(w[0], "action press-keys") {
		t.Errorf("staleFor = %q", w)
	}

	// The update was saved, with its record: reopening changes nothing more.
	again := openSeqLibrary(path)
	if again.note != "" || !reflect.DeepEqual(again.actions, l.actions) {
		t.Errorf("reopened: note %q, same actions %v", again.note, reflect.DeepEqual(again.actions, l.actions))
	}
}

func TestOpenSeqLibraryKeepsUnrecordedExamples(t *testing.T) {
	// A library saved before the record was kept (as on Revision K's host):
	// its old open-url can't be told from an edited one, so it's kept and
	// flagged, until Restore examples.
	actions, sequences := exampleLibrary()
	old := oldOpenURL(actions)
	actions[indexOfAction(actions, "open-url")] = old
	path := filepath.Join(t.TempDir(), "lib.yaml")
	os.WriteFile(path, []byte(encodeSeqLib(seqLibDoc{Actions: actions, Sequences: sequences})), 0o644)
	l := openSeqLibrary(path)
	if !reflect.DeepEqual(l.actionMap()["open-url"], old) {
		t.Fatal("an unrecorded open-url was replaced")
	}
	w := l.staleFor("firefox-weather")
	if len(w) != 1 || !strings.Contains(w[0], "action open-url differs") || !strings.Contains(w[0], "Restore examples") {
		t.Errorf("staleFor = %q", w)
	}
	if w := l.staleFor("fc-hello-world"); len(w) != 0 {
		t.Errorf("fc-hello-world doesn't use open-url: %q", w)
	}

	if _, err := l.restoreExamples(); err != nil {
		t.Fatal(err)
	}
	if w := l.staleFor("firefox-weather"); len(w) != 0 || l.snapshot().Note != "" {
		t.Errorf("after restoring: staleFor = %q, note = %q", w, l.snapshot().Note)
	}
	doc, err := decodeSeqLib(mustRead(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Examples, shippedExamples()) {
		t.Errorf("saved record = %v", doc.Examples)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// retiredOpenFile and retiredTextFile stand in for the examples Revision M
// removed, as a library an older IANAR saved holds them. open-file uses
// show-window, an op that's gone.
func retiredOpenFile() ActionDef {
	return ActionDef{
		ID: "open-file", Name: "Open a file in its default app",
		Controls: []Control{{Name: "path", Label: "Path"}, {Name: "ready_text", Label: "Title showing it's open"}},
		Do: []Instruction{
			{"op": "open", "path": "{{path}}"},
			{"op": "show-window", "title": "{{ready_text}}"},
		},
	}
}

func retiredTextFile() SequenceV2 {
	return SequenceV2{
		ID: "desktop-text-file", Name: "Desktop text file: hello world",
		Steps: []StepRef{{Action: "open-file", With: map[string]string{"path": "{{desktop}}/x.txt", "ready_text": "x.txt"}}},
	}
}

func writeLibWithRetired(t *testing.T, open ActionDef, text SequenceV2, rec map[string]string) string {
	t.Helper()
	actions, sequences := exampleLibrary()
	path := filepath.Join(t.TempDir(), "lib.yaml")
	doc := seqLibDoc{Actions: append(actions, open), Sequences: append(sequences, text), Examples: rec}
	os.WriteFile(path, []byte(encodeSeqLib(doc)), 0o644)
	return path
}

func TestOpenSeqLibraryRetiresUneditedExamples(t *testing.T) {
	rec := shippedExamples()
	rec[exampleKey("action", "open-file")] = actionPrint(retiredOpenFile())
	rec[exampleKey("sequence", "desktop-text-file")] = sequencePrint(retiredTextFile())
	path := writeLibWithRetired(t, retiredOpenFile(), retiredTextFile(), rec)

	l := openSeqLibrary(path)
	if _, ok := l.actionMap()["open-file"]; ok || indexOfSequence(l.sequences, "desktop-text-file") >= 0 {
		t.Error("the retired examples are still in the library")
	}
	if !strings.Contains(l.note, "removed") || !strings.Contains(l.note, "sequence desktop-text-file") || !strings.Contains(l.note, "action open-file") {
		t.Errorf("note = %q", l.note)
	}
	// Saved without them (not set aside as broken), so reopening changes
	// nothing more.
	doc, err := decodeSeqLib(mustRead(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if indexOfAction(doc.Actions, "open-file") >= 0 || !reflect.DeepEqual(doc.Examples, shippedExamples()) {
		t.Errorf("saved: open-file at %d, record %v", indexOfAction(doc.Actions, "open-file"), doc.Examples)
	}
	if again := openSeqLibrary(path); again.note != "" {
		t.Errorf("reopened: note %q", again.note)
	}
}

func TestOpenSeqLibraryKeepsEditedRetiredExamples(t *testing.T) {
	// An edited desktop-text-file is kept, and so is the (unedited) action
	// it uses -- here one that doesn't use the op that's gone.
	open := retiredOpenFile()
	open.Do = open.Do[:1]
	edited := retiredTextFile()
	edited.Description = "my own"
	rec := shippedExamples()
	rec[exampleKey("action", "open-file")] = actionPrint(open)
	rec[exampleKey("sequence", "desktop-text-file")] = sequencePrint(retiredTextFile())
	path := writeLibWithRetired(t, open, edited, rec)

	l := openSeqLibrary(path)
	if indexOfSequence(l.sequences, "desktop-text-file") < 0 {
		t.Error("the edited desktop-text-file was removed")
	}
	if _, ok := l.actionMap()["open-file"]; !ok {
		t.Error("open-file was removed, though a sequence kept uses it")
	}
	if l.note != "" {
		t.Errorf("note = %q", l.note)
	}
	// They're the user's own now: no longer recorded as examples.
	doc, err := decodeSeqLib(mustRead(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Examples, shippedExamples()) {
		t.Errorf("saved record = %v", doc.Examples)
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
	// Screens: IANAR's own step list (already naming "private"), the dock
	// menu (its item misread by one letter), the private window, then the
	// weather page.
	ianarLine := OCRLine{Text: "Open a new private Firefox window", X: 900, Y: 400, W: 300, H: 12}
	stubScreenLines(t, func(n int) []OCRLine {
		switch n {
		case 0:
			return []OCRLine{ianarLine}
		case 1:
			return []OCRLine{{Text: "New Window", X: 60, Y: 200, W: 100, H: 12}, {Text: "New Prlvate Window", X: 60, Y: 230, W: 160, H: 12}}
		case 2:
			return []OCRLine{ianarLine}
		case 3:
			return []OCRLine{ianarLine, {Text: "New Private Tab", X: 10, Y: 10, W: 100, H: 12}}
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

// The file actions Revision M's removed desktop-text-file sequence used
// stay in the definer, for sequences of the user's own.
func TestRunFileActions(t *testing.T) {
	stubSequence(t)
	desk := t.TempDir()
	origDesk := desktopDir
	t.Cleanup(func() { desktopDir = origDesk })
	desktopDir = func() string { return desk }
	stubCapture(t, image.NewRGBA(image.Rect(0, 0, 64, 48)))

	l := openSeqLibrary("")
	file := "{{desktop}}/notes.txt"
	err := l.saveSequence(SequenceV2{ID: "files", Name: "Files", Steps: []StepRef{
		{Action: "create-file", With: map[string]string{"path": file, "content": "hello world"}},
		{Action: "expect-file", With: map[string]string{"path": file, "contains": "hello"}},
		{Action: "save-screenshot", With: map[string]string{"path": "{{desktop}}/shot.png"}},
		{Action: "delete-file", With: map[string]string{"path": file}},
		{Action: "expect-file", With: map[string]string{"path": file, "contains": "hello"}},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	q, err := l.compile("files", nil, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	res := runSequence(q, func(SequenceProgressMsg) {})
	// Everything up to the last check works; that one finds the file gone.
	if res.Success || res.FailedStep != 4 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(desk, "shot.png")); err != nil {
		t.Errorf("no screenshot saved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(desk, "notes.txt")); !os.IsNotExist(err) {
		t.Error("notes.txt should have been deleted")
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
