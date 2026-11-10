package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExamplesAreValid(t *testing.T) {
	if _, ok := exampleByID("hello-world"); !ok {
		t.Fatal("the hello-world example is missing")
	}
	for _, e := range examples {
		c := e.Composition
		c.normalize()
		if err := c.validate(); err != nil {
			t.Errorf("example %s: %v", e.ID, err)
		}
	}
}

func TestHelloWorldNeedsNoSources(t *testing.T) {
	e, _ := exampleByID("hello-world")
	for _, cl := range append(append([]Clip{}, e.Composition.Video...), e.Composition.Audio...) {
		if cl.Kind == clipMedia {
			t.Errorf("hello-world uses a media clip (%s); it should render with no source files", cl.FileName)
		}
	}
}

func TestFromExampleSavesCopies(t *testing.T) {
	st := newStore(t.TempDir())
	a, err := st.fromExample("hello-world")
	if err != nil {
		t.Fatalf("fromExample: %v", err)
	}
	b, err := st.fromExample("hello-world")
	if err != nil {
		t.Fatalf("fromExample again: %v", err)
	}
	if a.ID != "hello-world" || b.ID != "hello-world-2" {
		t.Errorf("ids = %q, %q; want hello-world, hello-world-2", a.ID, b.ID)
	}
	if _, err := st.fromExample("nope"); err == nil {
		t.Error("an unknown example should be an error")
	}
	list, err := st.list()
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %v, %v; want 2 compositions", list, err)
	}
	if list[0].Clips != 5 {
		t.Errorf("clips = %d, want 5", list[0].Clips)
	}
}

func TestSaveUpdateKeepsRenders(t *testing.T) {
	st := newStore(t.TempDir())
	c, err := st.fromExample("hello-world")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.addRender(c.ID, RenderInfo{FileID: "f1", Name: "composer-hello-world-x.webm"}); err != nil {
		t.Fatalf("addRender: %v", err)
	}
	edit := *c
	edit.Name = "Hello again"
	edit.Renders = nil // a client's copy can't drop the history
	saved, err := st.save(edit)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ID != c.ID || saved.Name != "Hello again" || len(saved.Renders) != 1 {
		t.Errorf("saved = %+v; want same id, new name, one render", saved)
	}
	got, err := st.get(c.ID)
	if err != nil || got.Name != "Hello again" {
		t.Errorf("get = %+v, %v", got, err)
	}
}

func TestSaveUnknownIDFails(t *testing.T) {
	st := newStore(t.TempDir())
	c := examples[0].Composition
	c.ID = "never-saved"
	if _, err := st.save(c); err == nil {
		t.Error("saving over a composition that doesn't exist should fail")
	}
}

func TestRemove(t *testing.T) {
	st := newStore(t.TempDir())
	c, _ := st.fromExample("hello-world")
	if err := st.remove(c.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := st.remove(c.ID); err == nil {
		t.Error("removing twice should fail")
	}
	if _, err := st.get(c.ID); err == nil {
		t.Error("get after remove should fail")
	}
}

func TestRemoveAll(t *testing.T) {
	if n, err := newStore(filepath.Join(t.TempDir(), "missing")).removeAll(); n != 0 || err != nil {
		t.Errorf("removeAll on no store dir = %d, %v; want 0, nil", n, err)
	}
	dir := t.TempDir()
	st := newStore(dir)
	st.fromExample("hello-world")
	st.fromExample("hello-world")
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := st.removeAll()
	if err != nil || n != 2 {
		t.Fatalf("removeAll = %d, %v; want 2, nil", n, err)
	}
	if list, _ := st.list(); len(list) != 0 {
		t.Errorf("list after removeAll = %v, want none", list)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("removeAll should leave non-composition files: %v", err)
	}
}

func TestInvalidIDsRejected(t *testing.T) {
	st := newStore(t.TempDir())
	for _, id := range []string{"", "../x", "a/b", "UPPER", "-lead"} {
		if _, err := st.get(id); err == nil {
			t.Errorf("get(%q) should fail", id)
		}
		if err := st.remove(id); err == nil {
			t.Errorf("remove(%q) should fail", id)
		}
	}
}

func TestListSkipsJunk(t *testing.T) {
	dir := t.TempDir()
	st := newStore(dir)
	st.fromExample("hello-world")
	os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644)
	list, err := st.list()
	if err != nil || len(list) != 1 {
		t.Errorf("list = %v, %v; want just hello-world", list, err)
	}
}

func TestListMissingDir(t *testing.T) {
	st := newStore(filepath.Join(t.TempDir(), "absent"))
	list, err := st.list()
	if err != nil || len(list) != 0 {
		t.Errorf("list = %v, %v; want empty", list, err)
	}
}

func TestValidate(t *testing.T) {
	base := func() Composition {
		return Composition{Name: "x", Video: []Clip{{Kind: clipTitle, Duration: 1, Text: "hi"}}}
	}
	for _, tc := range []struct {
		name string
		edit func(*Composition)
		want string // substring of the error; "" means valid
	}{
		{"ok", func(*Composition) {}, ""},
		{"no name", func(c *Composition) { c.Name = " " }, "needs a name"},
		{"no clips", func(c *Composition) { c.Video = nil }, "at least one clip"},
		{"too small", func(c *Composition) { c.Width = 8 }, "outside"},
		{"title without duration", func(c *Composition) { c.Video[0].Duration = 0 }, "needs a duration"},
		{"too long", func(c *Composition) { c.Video[0].Duration = 601 }, "outside"},
		{"tone on video", func(c *Composition) { c.Video[0].Kind = clipTone }, "don't go on the video track"},
		{"title on audio", func(c *Composition) { c.Audio = []Clip{{Kind: clipTitle, Duration: 1}} }, "don't go on the audio track"},
		{"media without file", func(c *Composition) { c.Video[0] = Clip{Kind: clipMedia} }, "needs a file"},
		{"video file", func(c *Composition) {
			c.Video[0] = Clip{Kind: clipMedia, FileID: "a", FileName: "ianar-recording-x.webm"}
		}, ""},
		{"image without duration", func(c *Composition) {
			c.Video[0] = Clip{Kind: clipMedia, FileID: "a", FileName: "shot.png"}
		}, "image needs a duration"},
		{"audio on video", func(c *Composition) {
			c.Video[0] = Clip{Kind: clipMedia, FileID: "a", FileName: "polly-Joanna-hi.mp3"}
		}, "put it on the audio track"},
		{"image on audio", func(c *Composition) {
			c.Audio = []Clip{{Kind: clipMedia, FileID: "a", FileName: "shot.png", Duration: 1}}
		}, "is an image"},
		{"unusable file", func(c *Composition) {
			c.Video[0] = Clip{Kind: clipMedia, FileID: "a", FileName: "notes.txt"}
		}, "not an audio, video or image"},
		{"polly clip on audio", func(c *Composition) {
			c.Audio = []Clip{{Kind: clipMedia, FileID: "a", FileName: "polly-Joanna-hi.mp3"}}
		}, ""},
		{"tone default frequency", func(c *Composition) { c.Audio = []Clip{{Kind: clipTone, Duration: 1}} }, ""},
		{"tone too high", func(c *Composition) { c.Audio = []Clip{{Kind: clipTone, Duration: 1, Frequency: 30000}} }, "outside 20..20000"},
		{"silence without duration", func(c *Composition) { c.Audio = []Clip{{Kind: clipSilence}} }, "needs a duration"},
		{"audio output", func(c *Composition) {
			c.Output, c.Video = outputAudio, nil
			c.Audio = []Clip{{Kind: clipMedia, FileID: "a", FileName: "polly-Joanna-hi.mp3"}, {Kind: clipSilence, Duration: 0.5}}
		}, ""},
		{"video output named", func(c *Composition) { c.Output = outputVideo }, ""},
		{"audio output with video", func(c *Composition) {
			c.Output = outputAudio
			c.Audio = []Clip{{Kind: clipTone, Duration: 1}}
		}, "has no video track"},
		{"audio output with no audio", func(c *Composition) { c.Output, c.Video = outputAudio, nil }, "needs at least one audio clip"},
		{"unknown output", func(c *Composition) { c.Output = "gif" }, "should be video or audio"},
	} {
		c := base()
		tc.edit(&c)
		c.normalize()
		err := c.validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.want)
		}
	}
}

func TestRenderNameByOutput(t *testing.T) {
	now := time.Date(2026, 10, 27, 14, 3, 9, 0, time.UTC)
	if got := renderName(&Composition{ID: "hello"}, now); got != "composer-hello-2026-10-27T14-03-09.webm" {
		t.Errorf("video render name = %q", got)
	}
	got := renderName(&Composition{ID: "clips", Output: outputAudio}, now)
	if got != "composer-clips-2026-10-27T14-03-09.weba" || mediaKind(got) != "audio" {
		t.Errorf("audio render name = %q (%s), want a .weba composer reads as audio", got, mediaKind(got))
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Hello, world":     "hello-world",
		"  Trip -- Day 2 ": "trip-day-2",
		"ABC123":           "abc123",
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if got := slugify("¡¿!"); !strings.HasPrefix(got, "composition-") || !validID(got) {
		t.Errorf("slugify of no letters = %q, want a valid random composition-<hex> id", got)
	}
	if got := slugify(strings.Repeat("a", 100)); len(got) > 48 {
		t.Errorf("slugify of a long name is %d characters, want at most 48", len(got))
	}
}
