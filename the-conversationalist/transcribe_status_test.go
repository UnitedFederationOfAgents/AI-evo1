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

// TestCheckTranscribeAccessNoRegion verifies the availability check reports
// AWS Transcribe unavailable, with the actionable region message, when no
// region resolves -- without reaching the network.
func TestCheckTranscribeAccessNoRegion(t *testing.T) {
	isolateAWSEnv(t)

	st := newServer().checkTranscribeAccess(context.Background())
	if st.State != "unavailable" {
		t.Fatalf("state = %q, want unavailable", st.State)
	}
	if !strings.Contains(st.Reason, "no AWS region configured") {
		t.Errorf("reason = %q, want it to mention the missing region", st.Reason)
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
