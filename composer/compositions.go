package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- Compositions ----
//
// A composition is the recipe composer turns into one piece of media: two
// tracks, each a list of clips played back to back from time zero.
//
//   - the video track's clips are "title" cards (text on a background) or
//     "media" -- an image or video file from local-representative's files
//     area, such as an IANAR recording;
//   - the audio track's clips are "media" -- an audio file from the same
//     place, such as a Polly clip The Conversationalist saved -- "tone" (a
//     sine beep) or "silence".
//
// The server only stores and validates compositions; the browser renders
// them (frontend/src/render.ts) by drawing the video track onto a canvas
// and mixing the audio track with Web Audio, recording both with
// MediaRecorder. The result comes back through POST /api/render (see
// sources.go) and lands in local-representative's files area beside its
// sources.
//
// A composition's output is "video" (the default: a WebM with both tracks)
// or "audio": only the audio track, recorded into an audio-only WebM
// (".weba", Opus) -- a pure audio track, such as Polly clips put together
// (condocs/dTT-IntegrationImpls/Step3Prompt.md, Revision A). An audio
// composition has no video track, and its size is unused.
//
// Compositions are kept one JSON file each under --data-dir (default
// ~/.ufa/composer/compositions/).

// Clip kinds. The video track takes title and media; the audio track takes
// media, tone and silence.
const (
	clipTitle   = "title"
	clipMedia   = "media"
	clipTone    = "tone"
	clipSilence = "silence"
)

// Outputs: what a render records.
const (
	outputVideo = "video"
	outputAudio = "audio"
)

// Limits a composition is validated against.
const (
	maxClipsPerTrack = 200
	maxClipSeconds   = 600.0
	minDimension     = 16
	maxDimension     = 3840
	defaultWidth     = 1280
	defaultHeight    = 720
	maxNameLen       = 120
	maxTextLen       = 500
)

// Clip is one entry on a track.
type Clip struct {
	Kind string `json:"kind"`
	// Duration is in seconds. Required for title, tone and silence clips and
	// for an image; 0 on other media means "the file's own length".
	Duration float64 `json:"duration,omitempty"`

	// title
	Text       string `json:"text,omitempty"`
	Color      string `json:"color,omitempty"`      // CSS color of the text; default white
	Background string `json:"background,omitempty"` // CSS color behind it; default black

	// tone
	Frequency float64 `json:"frequency,omitempty"` // Hz; default 440

	// media: a file in local-representative's files area. FileName is kept
	// for display and to tell images from audio/video (see mediaKind).
	FileID   string `json:"file_id,omitempty"`
	FileName string `json:"file_name,omitempty"`
}

// RenderInfo records one finished render uploaded to local-representative.
type RenderInfo struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	At     int64  `json:"at"` // unix seconds

	// Renderer is what made it: "ffmpeg" (composer itself, for a render
	// job) or "browser" (a compose tab -- Render, or a job it claimed).
	// Renders recorded before this was kept have none.
	Renderer string `json:"renderer,omitempty"`
}

// Composition is one saved recipe; see the comment at the top of this file.
type Composition struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Width     int          `json:"width"`
	Height    int          `json:"height"`
	Output    string       `json:"output,omitempty"` // "video" ("" too) or "audio"; see the comment at the top of this file
	Video     []Clip       `json:"video"`
	Audio     []Clip       `json:"audio"`
	UpdatedAt int64        `json:"updated_at"`        // unix seconds, set on every save
	Renders   []RenderInfo `json:"renders,omitempty"` // newest first (addRender prepends); a render's at >= updated_at means it's of the current version
}

// maxRendersKept bounds a composition's render history.
const maxRendersKept = 20

var compositionIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// audioOnly reports whether c renders only its audio track.
func (c *Composition) audioOnly() bool { return c.Output == outputAudio }

// validID reports whether id is safe to use as a file name.
func validID(id string) bool {
	return compositionIDPattern.MatchString(id)
}

// normalize fills in defaults (size, colors, frequency) and trims text.
func (c *Composition) normalize() {
	c.Name = strings.TrimSpace(c.Name)
	c.Output = strings.TrimSpace(c.Output)
	if c.Width == 0 {
		c.Width = defaultWidth
	}
	if c.Height == 0 {
		c.Height = defaultHeight
	}
	if c.Video == nil {
		c.Video = []Clip{}
	}
	if c.Audio == nil {
		c.Audio = []Clip{}
	}
	for i := range c.Video {
		cl := &c.Video[i]
		cl.Kind = strings.TrimSpace(cl.Kind)
		if cl.Kind == clipTitle {
			if cl.Color == "" {
				cl.Color = "#ffffff"
			}
			if cl.Background == "" {
				cl.Background = "#000000"
			}
		}
	}
	for i := range c.Audio {
		cl := &c.Audio[i]
		cl.Kind = strings.TrimSpace(cl.Kind)
		if cl.Kind == clipTone && cl.Frequency == 0 {
			cl.Frequency = 440
		}
	}
}

// validate reports the first problem with c, naming the clip it's in.
func (c *Composition) validate() error {
	if c.Name == "" {
		return errors.New("a composition needs a name")
	}
	if len(c.Name) > maxNameLen {
		return fmt.Errorf("name is over %d characters", maxNameLen)
	}
	if c.Width < minDimension || c.Width > maxDimension || c.Height < minDimension || c.Height > maxDimension {
		return fmt.Errorf("size %dx%d is outside %d..%d", c.Width, c.Height, minDimension, maxDimension)
	}
	switch c.Output {
	case "", outputVideo:
	case outputAudio:
		if len(c.Video) > 0 {
			return errors.New("an audio composition has no video track -- remove its video clips, or make its output video")
		}
		if len(c.Audio) == 0 {
			return errors.New("an audio composition needs at least one audio clip")
		}
	default:
		return fmt.Errorf("output %q should be video or audio", c.Output)
	}
	if len(c.Video) == 0 && len(c.Audio) == 0 {
		return errors.New("a composition needs at least one clip")
	}
	if len(c.Video) > maxClipsPerTrack || len(c.Audio) > maxClipsPerTrack {
		return fmt.Errorf("a track can hold at most %d clips", maxClipsPerTrack)
	}
	for i, cl := range c.Video {
		if err := cl.validate("video"); err != nil {
			return fmt.Errorf("video clip %d: %w", i+1, err)
		}
	}
	for i, cl := range c.Audio {
		if err := cl.validate("audio"); err != nil {
			return fmt.Errorf("audio clip %d: %w", i+1, err)
		}
	}
	return nil
}

// validate checks one clip on the named track ("video" or "audio").
func (cl Clip) validate(track string) error {
	if cl.Duration < 0 || cl.Duration > maxClipSeconds {
		return fmt.Errorf("duration %.2fs is outside 0..%.0fs", cl.Duration, maxClipSeconds)
	}
	switch {
	case track == "video" && cl.Kind == clipTitle:
		if len(cl.Text) > maxTextLen {
			return fmt.Errorf("title text is over %d characters", maxTextLen)
		}
		if cl.Duration == 0 {
			return errors.New("a title needs a duration")
		}
	case cl.Kind == clipMedia:
		if cl.FileID == "" {
			return errors.New("a media clip needs a file")
		}
		kind := mediaKind(cl.FileName)
		switch {
		case kind == "":
			return fmt.Errorf("%q is not an audio, video or image file composer can use", cl.FileName)
		case track == "audio" && kind == "image":
			return fmt.Errorf("%q is an image; the audio track takes audio or video files", cl.FileName)
		case track == "video" && kind == "audio":
			return fmt.Errorf("%q is audio; put it on the audio track", cl.FileName)
		case kind == "image" && cl.Duration == 0:
			return errors.New("an image needs a duration")
		}
	case track == "audio" && cl.Kind == clipTone:
		if cl.Duration == 0 {
			return errors.New("a tone needs a duration")
		}
		if cl.Frequency < 20 || cl.Frequency > 20000 {
			return fmt.Errorf("frequency %.0fHz is outside 20..20000Hz", cl.Frequency)
		}
	case track == "audio" && cl.Kind == clipSilence:
		if cl.Duration == 0 {
			return errors.New("silence needs a duration")
		}
	default:
		return fmt.Errorf("%q clips don't go on the %s track", cl.Kind, track)
	}
	return nil
}

// CompositionSummary is one row of the composition list.
type CompositionSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Clips     int    `json:"clips"`
	UpdatedAt int64  `json:"updated_at"`
	Renders   int    `json:"renders"`
}

// store keeps compositions as <dir>/<id>.json.
type store struct {
	dir string
	mu  sync.Mutex
}

func newStore(dir string) *store {
	return &store{dir: dir}
}

func (st *store) path(id string) string {
	return filepath.Join(st.dir, id+".json")
}

// list returns every saved composition, most recently updated first. A file
// that doesn't parse is skipped.
func (st *store) list() ([]CompositionSummary, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	entries, err := os.ReadDir(st.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []CompositionSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []CompositionSummary{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || !validID(id) {
			continue
		}
		c, err := st.readLocked(id)
		if err != nil {
			continue
		}
		out = append(out, CompositionSummary{
			ID: c.ID, Name: c.Name, Clips: len(c.Video) + len(c.Audio), UpdatedAt: c.UpdatedAt, Renders: len(c.Renders),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// get reads one composition.
func (st *store) get(id string) (*Composition, error) {
	if !validID(id) {
		return nil, fmt.Errorf("invalid composition id %q", id)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.readLocked(id)
}

func (st *store) readLocked(id string) (*Composition, error) {
	b, err := os.ReadFile(st.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no composition %q", id)
	}
	if err != nil {
		return nil, err
	}
	var c Composition
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("composition %q: %w", id, err)
	}
	c.ID = id
	return &c, nil
}

// save validates and writes c. An empty ID creates a new composition with
// an ID derived from its name; otherwise c replaces the one already saved
// under its ID, keeping that one's render history. Returns what was saved.
func (st *store) save(c Composition) (*Composition, error) {
	c.normalize()
	if err := c.validate(); err != nil {
		return nil, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if c.ID == "" {
		c.ID = st.freeIDLocked(slugify(c.Name))
		c.Renders = nil
	} else {
		if !validID(c.ID) {
			return nil, fmt.Errorf("invalid composition id %q", c.ID)
		}
		prev, err := st.readLocked(c.ID)
		if err != nil {
			return nil, err
		}
		c.Renders = prev.Renders
	}
	c.UpdatedAt = time.Now().Unix()
	if err := st.writeLocked(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// put validates and writes c under its own ID, creating it or replacing the
// one already there (keeping that one's render history). It's how a caller
// that names its compositions -- the dtt composer tool, over PUT
// /api/compositions/<id> -- makes the same one again on every run. Returns
// what was saved and whether it was new.
func (st *store) put(c Composition) (*Composition, bool, error) {
	if !validID(c.ID) {
		return nil, false, fmt.Errorf("invalid composition id %q: lower-case letters, digits and '-', starting with a letter or digit", c.ID)
	}
	c.normalize()
	if err := c.validate(); err != nil {
		return nil, false, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	created := true
	c.Renders = nil
	if prev, err := st.readLocked(c.ID); err == nil {
		created = false
		c.Renders = prev.Renders
	}
	c.UpdatedAt = time.Now().Unix()
	if err := st.writeLocked(&c); err != nil {
		return nil, false, err
	}
	return &c, created, nil
}

// addRender records a finished render on composition id.
func (st *store) addRender(id string, r RenderInfo) (*Composition, error) {
	if !validID(id) {
		return nil, fmt.Errorf("invalid composition id %q", id)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	c, err := st.readLocked(id)
	if err != nil {
		return nil, err
	}
	c.Renders = append([]RenderInfo{r}, c.Renders...)
	if len(c.Renders) > maxRendersKept {
		c.Renders = c.Renders[:maxRendersKept]
	}
	if err := st.writeLocked(c); err != nil {
		return nil, err
	}
	return c, nil
}

// remove deletes composition id.
func (st *store) remove(id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid composition id %q", id)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	err := os.Remove(st.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no composition %q", id)
	}
	return err
}

// removeAll deletes every saved composition (the compose tab's "Delete all"),
// returning how many went. Only composition files are touched; their renders
// live in local-representative's files area and stay.
func (st *store) removeAll() (int, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	entries, err := os.ReadDir(st.dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || !validID(id) {
			continue
		}
		if err := os.Remove(st.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return n, err
		}
		n++
	}
	return n, nil
}

// writeLocked writes c atomically: a temp file renamed over the old one.
func (st *store) writeLocked(c *Composition) error {
	if err := os.MkdirAll(st.dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(st.dir, ".composition-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), st.path(c.ID))
}

// freeIDLocked returns base, or base-2, base-3, ... -- the first not taken.
func (st *store) freeIDLocked(base string) string {
	id := base
	for n := 2; ; n++ {
		if _, err := os.Stat(st.path(id)); errors.Is(err, os.ErrNotExist) {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

// slugify turns a name into an ID: lower-case letters and digits joined by
// single dashes, at most 48 characters. A name with none of those gets a
// random ID.
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	slug := strings.TrimRight(b.String(), "-")
	if slug == "" {
		var buf [4]byte
		rand.Read(buf[:])
		slug = "composition-" + hex.EncodeToString(buf[:])
	}
	return slug
}

// ---- Examples ----

// Example is a ready-made composition the UI offers to start from.
type Example struct {
	ID          string      `json:"id"`
	Description string      `json:"description"`
	Composition Composition `json:"composition"`
}

// examples are the compositions "new from example" copies. hello-world
// needs no source files at all -- two title cards over two beeps -- so it
// renders on any host, which makes it the quickest check that rendering
// and the upload back to local-representative work.
var examples = []Example{
	{
		ID:          "hello-world",
		Description: "two title cards over two beeps; needs no source files",
		Composition: Composition{
			Name:   "Hello, world",
			Width:  defaultWidth,
			Height: defaultHeight,
			Video: []Clip{
				{Kind: clipTitle, Duration: 2, Text: "Hello, world!", Color: "#ffffff", Background: "#1e3a5f"},
				{Kind: clipTitle, Duration: 2, Text: "composed by composer", Color: "#1a1a1a", Background: "#e0c060"},
			},
			Audio: []Clip{
				{Kind: clipTone, Duration: 0.4, Frequency: 440},
				{Kind: clipSilence, Duration: 1.6},
				{Kind: clipTone, Duration: 0.4, Frequency: 660},
			},
		},
	},
}

// exampleByID finds an example.
func exampleByID(id string) (Example, bool) {
	for _, e := range examples {
		if e.ID == id {
			return e, true
		}
	}
	return Example{}, false
}

// fromExample saves a fresh copy of example id.
func (st *store) fromExample(id string) (*Composition, error) {
	e, ok := exampleByID(id)
	if !ok {
		return nil, fmt.Errorf("no example %q", id)
	}
	c := e.Composition
	c.ID = ""
	c.Video = append([]Clip(nil), c.Video...)
	c.Audio = append([]Clip(nil), c.Audio...)
	return st.save(c)
}
