package main

// AWS Polly speech (condocs/initialRobotImpls/Step5SubstepGPrompt.md,
// Revision I): option B of docs/ConversationalistSpeechOutput.md. For now
// it's one isolated button beside the notepad's browser-speech button: the
// tab sends the text, this server synthesizes it with Polly and returns the
// MP3 to that tab alone, which plays it locally.
//
// Polly is called over its REST API with the core SDK's SigV4 signer rather
// than aws-sdk-go-v2/service/polly, so no new module is needed. Credentials
// and region come from the same loadAWSConfig Transcribe uses (transcribe.go),
// so a ufa-loader temporary principal and the us-east-2 fallback apply here
// too. The principal needs polly:SynthesizeSpeech.
//
// The voice and engine come from a Polly configuration, and each clip is
// kept as a recent clip that the same request reuses: see pollyconfig.go
// (condocs/dTT-IntegrationImpls/Step3Prompt.md).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	// pollyMaxChars is Polly's SynthesizeSpeech limit on billed characters
	// per request; longer text is refused rather than split.
	pollyMaxChars = 3000

	pollyTimeout = 30 * time.Second

	// pollyMaxAudioBytes caps the response read: 3000 characters of MP3
	// speech is well under this.
	pollyMaxAudioBytes = 16 << 20

	defaultPollyVoice  = "Joanna"
	defaultPollyEngine = "standard"
)

// PollyAudioMsg is the "polly-audio" WebSocket payload answering a
// "polly-speak" request: base64 MP3 audio on success, else Error. ID echoes
// the request's id so the frontend can ignore an answer it no longer wants.
//
// When the request asked to save the clip, SavedAs/FileID name the copy
// uploaded to local-representative's files area -- where composer picks
// Polly clips up as sources (see composer/sources.go) -- or SaveError says
// why it wasn't saved; the audio is returned to play either way.
type PollyAudioMsg struct {
	ID          uint64 `json:"id"`
	Audio       string `json:"audio,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Voice       string `json:"voice,omitempty"`
	Error       string `json:"error,omitempty"`
	SavedAs     string `json:"saved_as,omitempty"`
	FileID      string `json:"file_id,omitempty"`
	SaveError   string `json:"save_error,omitempty"`

	// The recent clip it is (see pollyconfig.go): its configuration and
	// cost, and whether a recent clip was reused rather than calling Polly
	// (a replay is one too).
	Clip       uint64 `json:"clip,omitempty"`
	Config     string `json:"config,omitempty"`
	Engine     string `json:"engine,omitempty"`
	Characters int    `json:"characters,omitempty"`
	Cost       string `json:"cost,omitempty"`
	Reused     bool   `json:"reused,omitempty"`
}

// setClip fills res's audio and clip fields from c.
func (res *PollyAudioMsg) setClip(c *pollyClip, reused bool) {
	res.Audio = base64.StdEncoding.EncodeToString(c.audio)
	res.ContentType = c.ContentType
	res.Clip, res.Config, res.Voice, res.Engine = c.ID, c.Config, c.Voice, c.Engine
	res.Characters, res.Cost, res.Reused = c.Characters, c.Cost, reused
}

// pollyEndpoint is Polly's regional endpoint, or s.pollyURL when tests set it.
func (s *Server) pollyEndpoint(region string) string {
	if s.pollyURL != "" {
		return s.pollyURL
	}
	return fmt.Sprintf("https://polly.%s.amazonaws.com", region)
}

// handlePollySpeak speaks text for client c in configuration config (the
// active one for "") -- a recent clip if one already says it -- and sends
// the result back to c alone; with save, it also uploads the clip to
// local-representative's files area (see pollyClipName).
func (s *Server) handlePollySpeak(c *wsClient, id uint64, text, config string, save bool) {
	res := PollyAudioMsg{ID: id}
	spoken, err := s.speak(text, config, "notepad")
	if err != nil {
		log.Printf("polly: %v", err)
		res.Error = err.Error()
	} else {
		res.setClip(spoken.clip, spoken.reused)
		if save {
			s.savePollyClip(&res, spoken.clip)
		}
	}
	s.sendToClient(c, "polly-audio", res)
}

// handlePollyReplay sends recent clip clipID's audio to client c, as the
// answer to its request id.
func (s *Server) handlePollyReplay(c *wsClient, id, clipID uint64) {
	res := PollyAudioMsg{ID: id}
	if clip := s.recentClip(clipID); clip != nil {
		res.setClip(clip, true)
	} else {
		res.Error = "that clip is no longer held -- only the most recent ones are kept"
	}
	s.sendToClient(c, "polly-audio", res)
}

// savePollyClip uploads one clip to local-representative's files area,
// recording the outcome on res.
func (s *Server) savePollyClip(res *PollyAudioMsg, clip *pollyClip) {
	base, err := s.lrFilesBase()
	if err != nil {
		res.SaveError = err.Error()
		return
	}
	uploaded, err := uploadToFiles(base, pollyClipName(clip.Voice, clip.Text, time.Now()), clip.audio)
	if err != nil {
		log.Printf("polly: saving clip: %v", err)
		res.SaveError = err.Error()
		return
	}
	res.SavedAs = uploaded.Name
	res.FileID = uploaded.ID
	s.noteClipSaved(clip, uploaded.Name, uploaded.ID)
}

// pollyClipName names a saved clip "polly-<voice>-<first words>-<stamp>.mp3",
// e.g. "polly-Joanna-hello-there-2026-10-25T14-03-09.mp3" -- the "polly-"
// prefix is how composer tells a Polly clip from other audio.
func pollyClipName(voice, text string, now time.Time) string {
	var words []string
	for _, f := range strings.Fields(strings.ToLower(text)) {
		w := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				return r
			}
			return -1
		}, f)
		if w == "" {
			continue
		}
		words = append(words, w)
		if len(words) == 4 {
			break
		}
	}
	slug := strings.Join(words, "-")
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	name := "polly-" + voice
	if slug != "" {
		name += "-" + slug
	}
	return name + "-" + now.Format("2006-01-02T15-04-05") + ".mp3"
}

// synthesizeSpeech returns Polly's MP3 rendering of text in voice, on
// engine, and the region it asked Polly in: region, or the AWS config's for
// "" (see pollyconfig.go's configurations).
func (s *Server) synthesizeSpeech(text, voice, engine, region string) ([]byte, string, string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, "", "", errors.New("nothing to speak")
	}
	if n := utf8.RuneCountInString(text); n > pollyMaxChars {
		return nil, "", "", fmt.Errorf("text is %d characters; Polly takes at most %d", n, pollyMaxChars)
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollyTimeout)
	defer cancel()
	cfg, _, err := s.loadAWSConfig(ctx)
	if err != nil {
		return nil, "", "", err
	}
	if region != "" {
		cfg.Region = region
	}
	audio, contentType, err := pollySynthesize(ctx, cfg, s.pollyEndpoint(cfg.Region), voice, engine, text)
	if err != nil && strings.Contains(err.Error(), "not supported in this region") {
		err = fmt.Errorf("%w (asked in %s: give the configuration a region that offers the %s engine, such as %s)", err, cfg.Region, engine, pollyPresetRegion)
	}
	return audio, contentType, cfg.Region, err
}

// pollySynthesize calls Polly's SynthesizeSpeech (POST /v1/speech) at
// endpoint, signed with cfg's credentials for cfg.Region.
func pollySynthesize(ctx context.Context, cfg aws.Config, endpoint, voice, engine, text string) ([]byte, string, error) {
	if cfg.Credentials == nil {
		return nil, "", errors.New("no AWS credentials found")
	}
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("retrieving AWS credentials: %w", err)
	}

	body, err := json.Marshal(map[string]string{
		"Engine":       engine,
		"OutputFormat": "mp3",
		"Text":         text,
		"TextType":     "text",
		"VoiceId":      voice,
	})
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(endpoint, "/")+"/v1/speech", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	sum := sha256.Sum256(body)
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), "polly", cfg.Region, time.Now()); err != nil {
		return nil, "", fmt.Errorf("signing Polly request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("calling AWS Polly: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, pollyMaxAudioBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading AWS Polly response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", pollyError(resp, data)
	}
	if len(data) > pollyMaxAudioBytes {
		return nil, "", errors.New("AWS Polly returned more audio than expected")
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	return data, contentType, nil
}

// pollyError turns a non-200 Polly response into an error naming AWS's
// error type and message, e.g. "AWS Polly: AccessDeniedException: ...".
func pollyError(resp *http.Response, body []byte) error {
	var e struct {
		Message  string `json:"message"`
		Message2 string `json:"Message"`
		Type     string `json:"__type"`
	}
	json.Unmarshal(body, &e)
	typ := resp.Header.Get("X-Amzn-Errortype")
	if typ == "" {
		typ = e.Type
	}
	if i := strings.Index(typ, ":"); i > 0 {
		typ = typ[:i] // "AccessDeniedException:http://..." -> "AccessDeniedException"
	}
	msg := e.Message
	if msg == "" {
		msg = e.Message2
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if typ == "" {
		typ = resp.Status
	}
	if msg == "" {
		return fmt.Errorf("AWS Polly: %s", typ)
	}
	return fmt.Errorf("AWS Polly: %s: %s", typ, msg)
}
