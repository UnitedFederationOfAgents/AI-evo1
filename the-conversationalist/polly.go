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
type PollyAudioMsg struct {
	ID          uint64 `json:"id"`
	Audio       string `json:"audio,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Voice       string `json:"voice,omitempty"`
	Error       string `json:"error,omitempty"`
}

// pollyEndpoint is Polly's regional endpoint, or s.pollyURL when tests set it.
func (s *Server) pollyEndpoint(region string) string {
	if s.pollyURL != "" {
		return s.pollyURL
	}
	return fmt.Sprintf("https://polly.%s.amazonaws.com", region)
}

// handlePollySpeak synthesizes text for client c and sends the result back
// to c alone.
func (s *Server) handlePollySpeak(c *wsClient, id uint64, text string) {
	res := PollyAudioMsg{ID: id, Voice: s.pollyVoice}
	audio, contentType, err := s.synthesizeSpeech(text)
	if err != nil {
		log.Printf("polly: %v", err)
		res.Error = err.Error()
	} else {
		res.Audio = base64.StdEncoding.EncodeToString(audio)
		res.ContentType = contentType
	}
	s.sendToClient(c, "polly-audio", res)
}

// synthesizeSpeech returns Polly's MP3 rendering of text.
func (s *Server) synthesizeSpeech(text string) ([]byte, string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, "", errors.New("nothing to speak")
	}
	if n := utf8.RuneCountInString(text); n > pollyMaxChars {
		return nil, "", fmt.Errorf("text is %d characters; Polly takes at most %d", n, pollyMaxChars)
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollyTimeout)
	defer cancel()
	cfg, _, err := s.loadAWSConfig(ctx)
	if err != nil {
		return nil, "", err
	}
	return pollySynthesize(ctx, cfg, s.pollyEndpoint(cfg.Region), s.pollyVoice, s.pollyEngine, text)
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
