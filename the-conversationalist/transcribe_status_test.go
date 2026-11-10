package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// isolateAWSEnv points the AWS SDK's config/credential resolution at empty
// locations so a test sees no region or credentials from the host.
func isolateAWSEnv(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_CONFIG_FILE", empty)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", empty)
	// Keep the instance-role lookup from waiting on a metadata endpoint
	// that isn't there.
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("HOME", t.TempDir())
}

// TestCheckTranscribeAccessNoRegion verifies that when no region resolves the
// availability check falls back to defaultAWSRegion and reports it as a
// warning rather than the reason Transcribe is unavailable (Revision F). With
// no credentials either, it still stops before reaching the network.
func TestCheckTranscribeAccessNoRegion(t *testing.T) {
	isolateAWSEnv(t)

	st := newServer().checkTranscribeAccess(context.Background())
	if st.Region != defaultAWSRegion {
		t.Errorf("region = %q, want %s", st.Region, defaultAWSRegion)
	}
	if !strings.Contains(st.Warning, "default region "+defaultAWSRegion) {
		t.Errorf("warning = %q, want it to mention the default region", st.Warning)
	}
	if strings.Contains(st.Reason, "region") {
		t.Errorf("reason = %q, want the missing region reported only as a warning", st.Reason)
	}
}

// TestLoadAWSConfigRegion verifies loadAWSConfig only reports a defaulted
// region when it actually fell back to defaultAWSRegion.
func TestLoadAWSConfigRegion(t *testing.T) {
	isolateAWSEnv(t)

	s := newServer()
	cfg, defaulted, err := s.loadAWSConfig(context.Background())
	if err != nil {
		t.Fatalf("loadAWSConfig: %v", err)
	}
	if cfg.Region != defaultAWSRegion || !defaulted {
		t.Errorf("no region configured: got region %q defaulted=%v, want %s defaulted=true", cfg.Region, defaulted, defaultAWSRegion)
	}

	t.Setenv("AWS_REGION", "eu-west-1")
	cfg, defaulted, err = s.loadAWSConfig(context.Background())
	if err != nil {
		t.Fatalf("loadAWSConfig: %v", err)
	}
	if cfg.Region != "eu-west-1" || defaulted {
		t.Errorf("AWS_REGION set: got region %q defaulted=%v, want eu-west-1 defaulted=false", cfg.Region, defaulted)
	}
}

// TestCheckTranscribeAccessNoCredentials verifies a resolved region alone
// isn't reported as available: with no credentials anywhere in the chain the
// check stops before calling AWS.
func TestCheckTranscribeAccessNoCredentials(t *testing.T) {
	isolateAWSEnv(t)

	s := newServer()
	s.awsRegion = "us-east-1"
	st := s.checkTranscribeAccess(context.Background())
	if st.State != "unavailable" {
		t.Fatalf("state = %q, want unavailable", st.State)
	}
	if st.Region != "us-east-1" {
		t.Errorf("region = %q, want us-east-1", st.Region)
	}
	if !strings.Contains(st.Reason, "credentials") {
		t.Errorf("reason = %q, want it to mention credentials", st.Reason)
	}
	if st.Warning != "" {
		t.Errorf("warning = %q, want none with --aws-region set", st.Warning)
	}
}

// TestRefreshTranscribeStatusBroadcasts verifies a check result reaches
// every connected client and is what a newly connected client is sent.
func TestRefreshTranscribeStatusBroadcasts(t *testing.T) {
	s := newServer()
	s.transcribeProbe = func(context.Context) TranscribeStatusMsg {
		return TranscribeStatusMsg{State: "available", Engine: transcribeEngineName, Region: "us-west-2"}
	}
	a := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	b := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	s.clients[a] = true
	s.clients[b] = true

	s.refreshTranscribeStatus()

	for name, c := range map[string]*wsClient{"a": a, "b": b} {
		if st := readTranscribeStatus(t, c); st.State != "available" || st.Region != "us-west-2" || st.CheckedAt == "" {
			t.Errorf("client %s got %+v, want available in us-west-2 with checked_at set", name, st)
		}
	}

	late := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	s.sendTranscribeStatus(late)
	if st := readTranscribeStatus(t, late); st.State != "available" {
		t.Errorf("late client got state %q, want available", st.State)
	}
}

// TestSendTranscribeStatusBeforeFirstCheck verifies a client connecting
// before the first check finishes is told "checking" rather than an empty
// state.
func TestSendTranscribeStatusBeforeFirstCheck(t *testing.T) {
	s := newServer()
	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	s.sendTranscribeStatus(c)
	if st := readTranscribeStatus(t, c); st.State != "checking" {
		t.Errorf("state = %q, want checking", st.State)
	}
}

func readTranscribeStatus(t *testing.T, c *wsClient) TranscribeStatusMsg {
	t.Helper()
	select {
	case raw := <-c.send:
		var m wsMsg
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Type != "transcribe-status" {
			t.Fatalf("expected a transcribe-status message, got %q", m.Type)
		}
		var st TranscribeStatusMsg
		json.Unmarshal(m.Payload, &st)
		return st
	default:
		t.Fatal("expected a transcribe-status message to be queued")
	}
	return TranscribeStatusMsg{}
}
