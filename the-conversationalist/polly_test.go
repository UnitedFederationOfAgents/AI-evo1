package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func testPollyConfig() aws.Config {
	return aws.Config{
		Region: "us-east-2",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"}, nil
		}),
	}
}

func TestPollySynthesizeSignsAndReturnsAudio(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/speech" {
			t.Errorf("request = %s %s, want POST /v1/speech", r.Method, r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") || !strings.Contains(auth, "/us-east-2/polly/aws4_request") {
			t.Errorf("Authorization = %q, want a SigV4 polly signature for us-east-2", auth)
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("mp3-bytes"))
	}))
	defer srv.Close()

	audio, contentType, err := pollySynthesize(context.Background(), testPollyConfig(), srv.URL, "Joanna", "standard", "hello there")
	if err != nil {
		t.Fatalf("pollySynthesize: %v", err)
	}
	if string(audio) != "mp3-bytes" || contentType != "audio/mpeg" {
		t.Errorf("got %q (%s), want mp3-bytes (audio/mpeg)", audio, contentType)
	}
	want := map[string]string{"Engine": "standard", "OutputFormat": "mp3", "Text": "hello there", "TextType": "text", "VoiceId": "Joanna"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("request %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestPollySynthesizeReportsAWSError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Amzn-Errortype", "AccessDeniedException:http://internal.amazon.com/coral/com.amazon.coral.service/")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"not authorized to perform: polly:SynthesizeSpeech"}`))
	}))
	defer srv.Close()

	_, _, err := pollySynthesize(context.Background(), testPollyConfig(), srv.URL, "Joanna", "standard", "hello")
	if err == nil {
		t.Fatal("pollySynthesize succeeded, want an error")
	}
	want := "AWS Polly: AccessDeniedException: not authorized to perform: polly:SynthesizeSpeech"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestSynthesizeSpeechRejectsEmptyAndLongText(t *testing.T) {
	s := newServer()
	if _, _, err := s.synthesizeSpeech("   "); err == nil {
		t.Error("blank text: want an error")
	}
	if _, _, err := s.synthesizeSpeech(strings.Repeat("a", pollyMaxChars+1)); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("long text: err = %v, want a length error", err)
	}
}
