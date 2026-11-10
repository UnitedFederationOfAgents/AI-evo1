package main

// Polly configurations and recent clips (condocs/dTT-IntegrationImpls/
// Step3Prompt.md). The notepad's gear button, beside its Polly button,
// opens a menu over them (App.tsx's PollyConfigMenu); "up" leaves it.
//
//   - A configuration is a named voice and engine, and optionally the AWS
//     region to ask Polly in. There's a default (the --polly-voice/
//     --polly-engine flags) and a few other presets; each is
//     editable, and an edited preset can be reset. New ones can be added
//     from any of them, and removed. One is active: the Polly button and
//     /api/polly use it unless a request names another. The configurations
//     and the active one are shared by every tab, and kept in
//     --polly-config-file (when it's set) so they outlast a restart.
//   - A configuration without a region uses the AWS config's (--aws-region,
//     else the SDK's own, else us-east-2 -- loadAWSConfig). Polly offers its
//     neural, generative and long-form engines in only some regions (neural
//     wasn't offered where Revision A ran), so the presets on those engines
//     name us-east-1, which has them all; the menu shows each one's region.
//   - Recent clips are the last pollyClipsKept clips made here, from the
//     notepad or /api/polly, held in memory with their audio. The menu
//     replays them. A request for text a recent clip already says, with the
//     same voice and engine, reuses that clip's audio instead of calling
//     Polly again (reported as reused, costing nothing).
//
// Text may name the clip it makes: [[config]], [[voice]], [[engine]],
// [[region]] (the configuration's, or "the default region"), [[price]] (the engine's price per million characters), [[characters]]
// (the billed characters of the text as spoken) and [[cost]] (what those
// characters cost). They're filled in before the text is sent -- dtt's
// polly-configurations example speaks its own cost this way.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// pollyClipsKept is how many recent clips are held, newest first;
	// pollyClipsMaxBytes caps their audio, dropping the oldest first.
	pollyClipsKept     = 20
	pollyClipsMaxBytes = 48 << 20
)

// pollyPrices are Polly's list prices in US dollars per million characters
// for each engine (aws.amazon.com/polly/pricing, us-east-1, October 2026);
// they're what the menu and [[cost]] estimate with, not a bill.
var pollyPrices = map[string]float64{
	"standard":   4,
	"neural":     16,
	"long-form":  100,
	"generative": 30,
}

// pollyEngines are the engines a configuration may name, cheapest first.
var pollyEngines = []string{"standard", "neural", "generative", "long-form"}

// pollyPresetRegion is where the presets on the engines Polly doesn't offer
// everywhere ask: us-east-1 has every engine.
const pollyPresetRegion = "us-east-1"

var (
	pollyConfigNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)
	pollyVoiceRe      = regexp.MustCompile(`^[A-Z][A-Za-z-]{1,30}$`)
	pollyRegionRe     = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
)

// PollyConfig is one named way of speaking.
type PollyConfig struct {
	Name        string `json:"name"`
	Voice       string `json:"voice"`
	Engine      string `json:"engine"`
	Region      string `json:"region,omitempty"` // the AWS region to ask Polly in; "" for the AWS config's
	Description string `json:"description,omitempty"`
	Preset      bool   `json:"preset,omitempty"` // one of pollyPresets
	Edited      bool   `json:"edited,omitempty"` // a preset changed from its built-in values
}

// pollyPresets are the built-in configurations; "default" takes its voice
// and engine from the flags.
func pollyPresets(voice, engine string) []PollyConfig {
	return []PollyConfig{
		{Name: "default", Voice: voice, Engine: engine, Description: "the notepad's own voice", Preset: true},
		{Name: "neural", Voice: "Joanna", Engine: "neural", Region: pollyPresetRegion, Description: "the same voice, more natural, at four times the price", Preset: true},
		{Name: "british", Voice: "Amy", Engine: "neural", Region: pollyPresetRegion, Description: "British English", Preset: true},
		{Name: "generative", Voice: "Ruth", Engine: "generative", Region: pollyPresetRegion, Description: "the most natural", Preset: true},
		{Name: "long-form", Voice: "Danielle", Engine: "long-form", Region: pollyPresetRegion, Description: "for reading long passages; the dearest", Preset: true},
	}
}

// pollyClip is one recent clip, with its audio.
type pollyClip struct {
	PollyClipView
	audio []byte
}

// PollyClipView is a recent clip as the menu and /api/polly/clips show it.
type PollyClipView struct {
	ID          uint64  `json:"id"`
	At          int64   `json:"at"` // unix ms it was made
	LastUsed    int64   `json:"last_used"`
	Text        string  `json:"text"` // as spoken, [[...]] filled in
	Config      string  `json:"config"`
	Voice       string  `json:"voice"`
	Engine      string  `json:"engine"`
	Region      string  `json:"region"` // where Polly was asked
	Characters  int     `json:"characters"`
	CostUSD     float64 `json:"cost_usd"`
	Cost        string  `json:"cost"`  // CostUSD in words: "0.32 cents"
	Uses        int     `json:"uses"`  // times it was asked for; all but the first reused it
	Size        int     `json:"size"`  // the audio's bytes
	ContentType string  `json:"content_type"`
	From        string  `json:"from"` // "notepad" or "api"
	SavedAs     string  `json:"saved_as,omitempty"`
	FileID      string  `json:"file_id,omitempty"`
}

// PollyStateMsg is the "polly-state" WebSocket payload: the configurations,
// the active one, the recent clips (without audio) and the prices. Sent on
// connect and to every tab whenever any of it changes.
type PollyStateMsg struct {
	Active  string             `json:"active"`
	Configs []PollyConfig      `json:"configs"`
	Clips   []PollyClipView    `json:"clips"`
	Prices  map[string]float64 `json:"prices"`
	Engines []string           `json:"engines"`
	Error   string             `json:"error,omitempty"` // the last configuration change refused, for the tab that asked
}

// pollyConfigFile is what --polly-config-file holds.
type pollyConfigFile struct {
	Active  string        `json:"active"`
	Configs []PollyConfig `json:"configs"`
}

func defaultPollyConfigFile() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ufa", "convo-polly.json")
}

// initPollyConfigs sets the presets up from the flags' voice and engine,
// then applies what s.pollyConfigFile holds, if anything.
func (s *Server) initPollyConfigs() {
	s.pollyMu.Lock()
	defer s.pollyMu.Unlock()
	s.pollyConfigs = pollyPresets(s.pollyVoice, s.pollyEngine)
	s.pollyActive = "default"
	if s.pollyConfigFile == "" {
		return
	}
	data, err := os.ReadFile(s.pollyConfigFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("polly: reading %s: %v", s.pollyConfigFile, err)
		}
		return
	}
	var f pollyConfigFile
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("polly: %s: %v", s.pollyConfigFile, err)
		return
	}
	for _, c := range f.Configs {
		if err := checkPollyConfig(c); err != nil {
			log.Printf("polly: %s: skipping %q: %v", s.pollyConfigFile, c.Name, err)
			continue
		}
		s.putPollyConfigLocked(c)
	}
	if s.findPollyConfigLocked(f.Active) >= 0 {
		s.pollyActive = f.Active
	}
}

// savePollyConfigsLocked writes the edited presets, the added
// configurations and the active one to s.pollyConfigFile.
func (s *Server) savePollyConfigsLocked() {
	if s.pollyConfigFile == "" {
		return
	}
	f := pollyConfigFile{Active: s.pollyActive, Configs: []PollyConfig{}}
	for _, c := range s.pollyConfigs {
		if !c.Preset || c.Edited {
			f.Configs = append(f.Configs, c)
		}
	}
	data, _ := json.MarshalIndent(f, "", "  ")
	err := os.MkdirAll(filepath.Dir(s.pollyConfigFile), 0o755)
	if err == nil {
		err = os.WriteFile(s.pollyConfigFile, append(data, '\n'), 0o644)
	}
	if err != nil {
		log.Printf("polly: saving %s: %v", s.pollyConfigFile, err)
	}
}

func checkPollyConfig(c PollyConfig) error {
	if !pollyConfigNameRe.MatchString(c.Name) {
		return errors.New("a configuration's name is lower-case letters, digits, '.', '_' and '-', at most 40")
	}
	if !pollyVoiceRe.MatchString(c.Voice) {
		return fmt.Errorf("voice %q should be a Polly voice id, like Joanna", c.Voice)
	}
	if _, ok := pollyPrices[c.Engine]; !ok {
		return fmt.Errorf("engine %q should be one of %s", c.Engine, strings.Join(pollyEngines, ", "))
	}
	if c.Region != "" && !pollyRegionRe.MatchString(c.Region) {
		return fmt.Errorf("region %q should be an AWS region, like us-east-1, or empty for the AWS config's", c.Region)
	}
	return nil
}

func (s *Server) findPollyConfigLocked(name string) int {
	for i, c := range s.pollyConfigs {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// putPollyConfigLocked adds c, or replaces the configuration of its name. A
// preset keeps being one, marked edited when it differs from its built-in
// values.
func (s *Server) putPollyConfigLocked(c PollyConfig) {
	c.Preset, c.Edited = false, false
	for _, p := range pollyPresets(s.pollyVoice, s.pollyEngine) {
		if p.Name == c.Name {
			c.Preset = true
			c.Edited = c.Voice != p.Voice || c.Engine != p.Engine || c.Region != p.Region || c.Description != p.Description
		}
	}
	if i := s.findPollyConfigLocked(c.Name); i >= 0 {
		s.pollyConfigs[i] = c
		return
	}
	s.pollyConfigs = append(s.pollyConfigs, c)
}

// pollyConfig is the configuration named name, or the active one for "".
func (s *Server) pollyConfig(name string) (PollyConfig, error) {
	s.pollyMu.Lock()
	defer s.pollyMu.Unlock()
	if name == "" {
		name = s.pollyActive
	}
	if i := s.findPollyConfigLocked(name); i >= 0 {
		return s.pollyConfigs[i], nil
	}
	names := make([]string, len(s.pollyConfigs))
	for i, c := range s.pollyConfigs {
		names[i] = c.Name
	}
	return PollyConfig{}, fmt.Errorf("there's no Polly configuration %q (there are %s)", name, strings.Join(names, ", "))
}

// setPollyActive makes name the active configuration.
func (s *Server) setPollyActive(name string) error {
	s.pollyMu.Lock()
	if s.findPollyConfigLocked(name) < 0 {
		s.pollyMu.Unlock()
		return fmt.Errorf("there's no Polly configuration %q", name)
	}
	s.pollyActive = name
	s.savePollyConfigsLocked()
	s.pollyMu.Unlock()
	s.broadcastPollyState()
	return nil
}

// savePollyConfig adds or changes c. previous, when given and not c's
// name, is a configuration c renames: it's removed (a preset can't be
// renamed). The active configuration follows a rename.
func (s *Server) savePollyConfig(c PollyConfig, previous string) error {
	c.Name, c.Voice, c.Engine = strings.TrimSpace(c.Name), strings.TrimSpace(c.Voice), strings.TrimSpace(c.Engine)
	c.Region = strings.ToLower(strings.TrimSpace(c.Region))
	c.Description = strings.TrimSpace(c.Description)
	if err := checkPollyConfig(c); err != nil {
		return err
	}
	s.pollyMu.Lock()
	if previous != "" && previous != c.Name {
		i := s.findPollyConfigLocked(previous)
		switch {
		case i < 0:
			s.pollyMu.Unlock()
			return fmt.Errorf("there's no Polly configuration %q to rename", previous)
		case s.pollyConfigs[i].Preset:
			s.pollyMu.Unlock()
			return fmt.Errorf("%s is a preset: it keeps its name (save a copy under a new name instead)", previous)
		case s.findPollyConfigLocked(c.Name) >= 0:
			s.pollyMu.Unlock()
			return fmt.Errorf("there's already a configuration %q", c.Name)
		}
		s.pollyConfigs = append(s.pollyConfigs[:i], s.pollyConfigs[i+1:]...)
		if s.pollyActive == previous {
			s.pollyActive = c.Name
		}
	}
	s.putPollyConfigLocked(c)
	s.savePollyConfigsLocked()
	s.pollyMu.Unlock()
	s.broadcastPollyState()
	return nil
}

// removePollyConfig deletes an added configuration, or resets a preset to
// its built-in values. Removing the active one makes "default" active.
func (s *Server) removePollyConfig(name string) error {
	s.pollyMu.Lock()
	i := s.findPollyConfigLocked(name)
	if i < 0 {
		s.pollyMu.Unlock()
		return fmt.Errorf("there's no Polly configuration %q", name)
	}
	if s.pollyConfigs[i].Preset {
		for _, p := range pollyPresets(s.pollyVoice, s.pollyEngine) {
			if p.Name == name {
				s.pollyConfigs[i] = p
			}
		}
	} else {
		s.pollyConfigs = append(s.pollyConfigs[:i], s.pollyConfigs[i+1:]...)
		if s.pollyActive == name {
			s.pollyActive = "default"
		}
	}
	s.savePollyConfigsLocked()
	s.pollyMu.Unlock()
	s.broadcastPollyState()
	return nil
}

func (s *Server) pollyState() PollyStateMsg {
	s.pollyMu.Lock()
	defer s.pollyMu.Unlock()
	st := PollyStateMsg{
		Active:  s.pollyActive,
		Configs: append([]PollyConfig(nil), s.pollyConfigs...),
		Clips:   make([]PollyClipView, len(s.pollyClips)),
		Prices:  pollyPrices,
		Engines: pollyEngines,
	}
	for i, c := range s.pollyClips {
		st.Clips[i] = c.PollyClipView
	}
	return st
}

func (s *Server) sendPollyState(c *wsClient) { s.sendToClient(c, "polly-state", s.pollyState()) }

func (s *Server) broadcastPollyState() {
	msg := s.marshalMsg("polly-state", s.pollyState())
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

// ---- costs and [[...]] ----

// pollyCost is what chars characters cost on engine, in dollars.
func pollyCost(engine string, chars int) float64 {
	return pollyPrices[engine] * float64(chars) / 1e6
}

// pollyCostWords says a cost as Polly can speak it: "0.32 cents", or
// "1.25 dollars" from a dollar up.
func pollyCostWords(usd float64) string {
	if usd >= 1 {
		return fmt.Sprintf("%.2f dollars", usd)
	}
	return fmt.Sprintf("%.2f cents", usd*100)
}

// pollyPriceWords says an engine's price: "16 dollars per million characters".
func pollyPriceWords(engine string) string {
	return fmt.Sprintf("%g dollars per million characters", pollyPrices[engine])
}

// pollyRegionWords says where a configuration asks Polly: its region, or
// "the default region" when it has none.
func pollyRegionWords(region string) string {
	if region == "" {
		return "the default region"
	}
	return region
}

var pollyFieldRe = regexp.MustCompile(`\[\[(config|voice|engine|region|price|characters|cost)\]\]`)

// fillPollyText fills text's [[...]] fields for cfg. [[characters]] and
// [[cost]] describe the filled text itself, so it's filled until its length
// settles (a cost's words are near enough the same length every time).
func fillPollyText(text string, cfg PollyConfig) string {
	if !pollyFieldRe.MatchString(text) {
		return text
	}
	chars := utf8.RuneCountInString(text)
	var out string
	for i := 0; i < 4; i++ {
		out = pollyFieldRe.ReplaceAllStringFunc(text, func(m string) string {
			switch m[2 : len(m)-2] {
			case "config":
				return cfg.Name
			case "voice":
				return cfg.Voice
			case "engine":
				return cfg.Engine
			case "region":
				return pollyRegionWords(cfg.Region)
			case "price":
				return pollyPriceWords(cfg.Engine)
			case "characters":
				return fmt.Sprint(chars)
			default:
				return pollyCostWords(pollyCost(cfg.Engine, chars))
			}
		})
		n := utf8.RuneCountInString(strings.TrimSpace(out))
		if n == chars {
			break
		}
		chars = n
	}
	return out
}

// ---- making clips ----

// pollySpoken is one request's result: the clip, and whether it was reused.
type pollySpoken struct {
	clip   *pollyClip
	reused bool
}

// speak returns the clip for text in configuration configName (the active
// one for ""): a recent clip saying the same with the same voice and
// engine, or a new one from Polly, kept as the newest recent clip.
func (s *Server) speak(text, configName, from string) (pollySpoken, error) {
	cfg, err := s.pollyConfig(configName)
	if err != nil {
		return pollySpoken{}, err
	}
	text = strings.TrimSpace(fillPollyText(strings.TrimSpace(text), cfg))
	if c := s.reuseClip(text, cfg); c != nil {
		return pollySpoken{clip: c, reused: true}, nil
	}
	audio, contentType, region, err := s.synthesizeSpeech(text, cfg.Voice, cfg.Engine, cfg.Region)
	if err != nil {
		return pollySpoken{}, err
	}
	chars := utf8.RuneCountInString(text)
	cost := pollyCost(cfg.Engine, chars)
	now := time.Now().UnixMilli()
	c := &pollyClip{audio: audio, PollyClipView: PollyClipView{
		At: now, LastUsed: now, Text: text, Config: cfg.Name, Voice: cfg.Voice, Engine: cfg.Engine, Region: region,
		Characters: chars, CostUSD: cost, Cost: pollyCostWords(cost), Uses: 1,
		Size: len(audio), ContentType: contentType, From: from,
	}}
	s.pollyMu.Lock()
	s.pollyClipSeq++
	c.ID = s.pollyClipSeq
	s.pollyClips = append([]*pollyClip{c}, s.pollyClips...)
	total := 0
	for i, k := range s.pollyClips {
		total += len(k.audio)
		if i >= pollyClipsKept || (i > 0 && total > pollyClipsMaxBytes) {
			s.pollyClips = s.pollyClips[:i]
			break
		}
	}
	s.pollyMu.Unlock()
	s.broadcastPollyState()
	return pollySpoken{clip: c}, nil
}

// reuseClip finds a recent clip saying text with cfg's voice and engine,
// and counts the use.
func (s *Server) reuseClip(text string, cfg PollyConfig) *pollyClip {
	s.pollyMu.Lock()
	var found *pollyClip
	for _, c := range s.pollyClips {
		if c.Text == text && c.Voice == cfg.Voice && c.Engine == cfg.Engine {
			found = c
			c.Uses++
			c.LastUsed = time.Now().UnixMilli()
			break
		}
	}
	s.pollyMu.Unlock()
	if found != nil {
		s.broadcastPollyState()
	}
	return found
}

// recentClip is the recent clip id, if it's still held.
func (s *Server) recentClip(id uint64) *pollyClip {
	s.pollyMu.Lock()
	defer s.pollyMu.Unlock()
	for _, c := range s.pollyClips {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// noteClipSaved records where a clip was last saved in LR's files area.
func (s *Server) noteClipSaved(c *pollyClip, name, fileID string) {
	s.pollyMu.Lock()
	c.SavedAs, c.FileID = name, fileID
	s.pollyMu.Unlock()
	s.broadcastPollyState()
}
