package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakePolly points s at a fake Polly that answers every request with
// "mp3:<voice>:<engine>:<text>", and counts the calls.
func fakePolly(t *testing.T, s *Server) *int {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "credentials"), nil, 0o644)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_REGION", "us-east-2")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("mp3:" + req["VoiceId"] + ":" + req["Engine"] + ":" + req["Text"]))
	}))
	t.Cleanup(srv.Close)
	s.pollyURL = srv.URL
	return &calls
}

func TestPollyPresetsAndEditing(t *testing.T) {
	s := newServer()
	st := s.pollyState()
	if st.Active != "default" || len(st.Configs) < 4 {
		t.Fatalf("state = %+v, want default active among the presets", st)
	}
	for _, c := range st.Configs {
		if err := checkPollyConfig(c); err != nil {
			t.Errorf("preset %s: %v", c.Name, err)
		}
	}

	// Edit a preset: it's marked edited, and removing it resets it.
	if err := s.savePollyConfig(PollyConfig{Name: "neural", Voice: "Matthew", Engine: "neural"}, ""); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.pollyConfig("neural"); c.Voice != "Matthew" || !c.Preset || !c.Edited {
		t.Errorf("edited neural = %+v", c)
	}
	if err := s.removePollyConfig("neural"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.pollyConfig("neural"); c.Voice != "Joanna" || c.Edited {
		t.Errorf("reset neural = %+v", c)
	}

	// Add one, make it active, rename it: the active one follows.
	if err := s.savePollyConfig(PollyConfig{Name: "brian", Voice: "Brian", Engine: "standard"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.setPollyActive("brian"); err != nil {
		t.Fatal(err)
	}
	if err := s.savePollyConfig(PollyConfig{Name: "brian-uk", Voice: "Brian", Engine: "neural"}, "brian"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.pollyConfig(""); c.Name != "brian-uk" || c.Engine != "neural" {
		t.Errorf("active after the rename = %+v", c)
	}
	if _, err := s.pollyConfig("brian"); err == nil {
		t.Error("brian is still there after the rename")
	}
	if err := s.removePollyConfig("brian-uk"); err != nil {
		t.Fatal(err)
	}
	if s.pollyState().Active != "default" {
		t.Error("removing the active configuration should make default active")
	}

	// Refusals.
	for _, c := range []PollyConfig{
		{Name: "Bad Name", Voice: "Joanna", Engine: "standard"},
		{Name: "x", Voice: "joanna", Engine: "standard"},
		{Name: "x", Voice: "Joanna", Engine: "turbo"},
	} {
		if err := s.savePollyConfig(c, ""); err == nil {
			t.Errorf("saving %+v: want an error", c)
		}
	}
	if err := s.savePollyConfig(PollyConfig{Name: "renamed", Voice: "Joanna", Engine: "standard"}, "default"); err == nil {
		t.Error("renaming a preset: want an error")
	}
}

func TestPollyConfigsPersist(t *testing.T) {
	file := filepath.Join(t.TempDir(), "polly.json")
	s := newServer()
	s.pollyConfigFile = file
	s.initPollyConfigs()
	s.savePollyConfig(PollyConfig{Name: "slow", Voice: "Kendra", Engine: "neural", Description: "mine"}, "")
	s.savePollyConfig(PollyConfig{Name: "british", Voice: "Emma", Engine: "neural"}, "")
	s.setPollyActive("slow")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the configurations weren't saved: %v", err)
	}

	again := newServer()
	again.pollyConfigFile = file
	again.initPollyConfigs()
	if c, err := again.pollyConfig(""); err != nil || c.Name != "slow" || c.Voice != "Kendra" {
		t.Errorf("active after a restart = %+v, %v", c, err)
	}
	if c, _ := again.pollyConfig("british"); c.Voice != "Emma" || !c.Edited {
		t.Errorf("edited preset after a restart = %+v", c)
	}
}

func TestFillPollyText(t *testing.T) {
	cfg := PollyConfig{Name: "neural", Voice: "Joanna", Engine: "neural"}
	if got := fillPollyText("plain text", cfg); got != "plain text" {
		t.Errorf("plain text changed: %q", got)
	}
	got := fillPollyText("I'm [[voice]] on [[engine]] ([[config]]), at [[price]]: these [[characters]] characters cost [[cost]].", cfg)
	chars := utf8.RuneCountInString(got)
	want := "I'm Joanna on neural (neural), at 16 dollars per million characters: these " +
		itoa(chars) + " characters cost " + pollyCostWords(pollyCost("neural", chars)) + "."
	if got != want {
		t.Errorf("filled =\n  %q\nwant\n  %q", got, want)
	}
	if pollyCostWords(0.0032) != "0.32 cents" || pollyCostWords(1.5) != "1.50 dollars" {
		t.Errorf("cost words: %q, %q", pollyCostWords(0.0032), pollyCostWords(1.5))
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestSpeakReusesRecentClips(t *testing.T) {
	s := newServer()
	calls := fakePolly(t, s)

	first, err := s.speak("hello there", "", "notepad")
	if err != nil {
		t.Fatal(err)
	}
	if first.reused || *calls != 1 || string(first.clip.audio) != "mp3:Joanna:standard:hello there" {
		t.Fatalf("first = %+v (reused %v), calls %d", first.clip.PollyClipView, first.reused, *calls)
	}
	if first.clip.Characters != 11 || first.clip.CostUSD != pollyCost("standard", 11) {
		t.Errorf("first's cost = %d characters, %v", first.clip.Characters, first.clip.CostUSD)
	}

	again, err := s.speak("  hello there ", "default", "api")
	if err != nil {
		t.Fatal(err)
	}
	if !again.reused || again.clip != first.clip || *calls != 1 || again.clip.Uses != 2 {
		t.Errorf("the same text again: reused %v, uses %d, calls %d", again.reused, again.clip.Uses, *calls)
	}

	// Another configuration is another clip.
	neural, err := s.speak("hello there", "neural", "api")
	if err != nil {
		t.Fatal(err)
	}
	if neural.reused || *calls != 2 || neural.clip.Engine != "neural" {
		t.Errorf("neural: reused %v, calls %d, engine %s", neural.reused, *calls, neural.clip.Engine)
	}
	st := s.pollyState()
	if len(st.Clips) != 2 || st.Clips[0].ID != neural.clip.ID {
		t.Errorf("recent clips = %+v, want neural's first", st.Clips)
	}
	if _, err := s.speak("hello", "nope", "api"); err == nil || !strings.Contains(err.Error(), "no Polly configuration") {
		t.Errorf("an unknown configuration: err = %v", err)
	}

	// Only pollyClipsKept are held.
	for i := 0; i < pollyClipsKept+3; i++ {
		s.speak("clip "+itoa(i), "", "api")
	}
	if n := len(s.pollyState().Clips); n != pollyClipsKept {
		t.Errorf("%d clips held, want %d", n, pollyClipsKept)
	}
	if s.recentClip(first.clip.ID) != nil {
		t.Error("the oldest clip should have been dropped")
	}
}

// TestPollyConfigRegion covers Revision A: a configuration's region is where
// Polly is asked (the request is signed for it), "" is the AWS config's, and
// the presets on neural and the other engines Polly doesn't offer
// everywhere name us-east-1.
func TestPollyConfigRegion(t *testing.T) {
	s := newServer()
	fakePolly(t, s) // the AWS config's region is us-east-2
	var regions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Credential=AKIDEXAMPLE/<date>/<region>/polly/aws4_request
		parts := strings.Split(r.Header.Get("Authorization"), "/")
		if len(parts) > 2 {
			regions = append(regions, parts[2])
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("mp3"))
	}))
	defer srv.Close()
	s.pollyURL = srv.URL

	for _, c := range s.pollyState().Configs {
		want := pollyPresetRegion
		if c.Engine == "standard" {
			want = ""
		}
		if c.Region != want {
			t.Errorf("preset %s (%s) region = %q, want %q", c.Name, c.Engine, c.Region, want)
		}
	}
	def, err := s.speak("in the default region", "default", "api")
	if err != nil {
		t.Fatal(err)
	}
	neural, err := s.speak("in [[region]]", "neural", "api")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(regions, ",") != "us-east-2,us-east-1" {
		t.Errorf("signed for %v, want us-east-2 then us-east-1", regions)
	}
	if def.clip.Region != "us-east-2" || neural.clip.Region != "us-east-1" || neural.clip.Text != "in us-east-1" {
		t.Errorf("clips = %+v, %+v", def.clip.PollyClipView, neural.clip.PollyClipView)
	}

	if err := s.savePollyConfig(PollyConfig{Name: "default", Voice: "Joanna", Engine: "standard", Region: " EU-West-1 "}, ""); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.pollyConfig("default"); c.Region != "eu-west-1" || !c.Edited {
		t.Errorf("default with a region = %+v", c)
	}
	if err := s.savePollyConfig(PollyConfig{Name: "x", Voice: "Joanna", Engine: "standard", Region: "mars"}, ""); err == nil {
		t.Error("a region that isn't one: want an error")
	}
}

func TestPollyAPIConfigsAndClips(t *testing.T) {
	s := newServer()
	fakePolly(t, s)
	s.speak("hello", "british", "notepad")
	mux := s.setupRoutes(true)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/polly/configs", nil))
	var configs struct {
		Active  string             `json:"active"`
		Configs []PollyConfig      `json:"configs"`
		Prices  map[string]float64 `json:"prices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &configs); err != nil || configs.Active != "default" || configs.Prices["neural"] != 16 {
		t.Errorf("configs = %s (%v)", rec.Body, err)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/polly/clips", nil))
	var clips struct {
		Clips []PollyClipView `json:"clips"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &clips); err != nil || len(clips.Clips) != 1 || clips.Clips[0].Voice != "Amy" {
		t.Errorf("clips = %s (%v)", rec.Body, err)
	}

	// An unknown configuration is refused before anything else.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/polly", strings.NewReader(`{"text":"hi","config":"nope"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown config: %d %s", rec.Code, rec.Body)
	}
}
