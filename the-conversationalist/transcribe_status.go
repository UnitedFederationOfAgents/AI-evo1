package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Transcription availability (condocs/initialRobotImpls/Step5Prompt.md,
// Revision A): whether this instance can currently reach its transcription
// engine -- so far only AWS Transcribe -- so the frontend can mark the
// capability unavailable up front instead of only finding out when Start
// fails.
//
// There is no free "ping" for AWS Transcribe streaming: every
// StartStreamTranscription call is billed (with a per-request minimum), so
// the check deliberately stops short of opening a stream. It verifies
// everything up to that point instead: the AWS config loads, a region is
// resolved, credentials resolve, and AWS accepts those credentials (STS
// GetCallerIdentity, which needs no IAM permission and costs nothing). It
// cannot see a missing transcribe:StartStreamTranscription permission --
// that still surfaces as a Start error, which re-runs this check.

const (
	transcribeEngineName = "aws-transcribe"

	// transcribeCheckInterval re-runs the check in the background so
	// credentials that expire (SSO sessions, temporary keys) or appear
	// later flip the indicator without a restart.
	transcribeCheckInterval = 5 * time.Minute
	transcribeCheckTimeout  = 15 * time.Second

	// transcribeRetryInterval is the shorter re-check used while the last
	// result was "unavailable": a brand-new key from ufa-loader's temporary
	// principal (ufa-loader/awsprincipal) can take a little while to be
	// accepted, and a failed first check shouldn't hold for 5 minutes.
	transcribeRetryInterval = 30 * time.Second
)

// TranscribeStatusMsg is the "transcribe-status" WebSocket payload: the
// result of the most recent availability check. State is "checking" (no
// result yet, or a re-check is underway with no earlier result),
// "available", or "unavailable" -- in which case Reason says why.
type TranscribeStatusMsg struct {
	State     string `json:"state"`
	Engine    string `json:"engine"`
	Region    string `json:"region,omitempty"`
	Reason    string `json:"reason,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"` // RFC 3339; empty until the first check finishes

	// Identity is the ARN AWS says the credentials belong to, and
	// CredentialSource is where the SDK found them (e.g.
	// "EnvConfigCredentials", "SharedConfigCredentials"). Together they show
	// whether a temporary principal from ufa-loader actually reached this
	// process (Step5SubstepGPrompt.md Revision E). Identity is empty until
	// AWS has accepted the credentials.
	Identity         string `json:"identity,omitempty"`
	CredentialSource string `json:"credential_source,omitempty"`
}

// checkTranscribeAccess is the default Server.transcribeProbe: it runs the
// checks described at the top of this file against the real AWS SDK.
func (s *Server) checkTranscribeAccess(ctx context.Context) TranscribeStatusMsg {
	st := TranscribeStatusMsg{State: "unavailable", Engine: transcribeEngineName}

	cfg, err := s.loadAWSConfig(ctx)
	if err != nil {
		st.Reason = err.Error()
		return st
	}
	st.Region = cfg.Region

	const noCreds = "no AWS credentials found: configure them in the environment, the shared AWS config/credentials files, or an instance role"
	if cfg.Credentials == nil {
		st.Reason = noCreds
		return st
	}
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		st.Reason = fmt.Sprintf("resolving AWS credentials: %v", err)
		return st
	}
	if !creds.HasKeys() {
		st.Reason = noCreds
		return st
	}
	st.CredentialSource = creds.Source

	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		st.Reason = fmt.Sprintf("AWS rejected the configured credentials (key %s from %s): %v", creds.AccessKeyID, creds.Source, err)
		return st
	}
	if id.Arn != nil {
		st.Identity = *id.Arn
	}

	st.State = "available"
	return st
}

// refreshTranscribeStatus runs the availability check and broadcasts the
// result to every connected client. Concurrent calls collapse into the one
// already running.
func (s *Server) refreshTranscribeStatus() {
	s.transcribeStatusMu.Lock()
	if s.transcribeChecking {
		s.transcribeStatusMu.Unlock()
		return
	}
	s.transcribeChecking = true
	if s.transcribeStatus.State == "" {
		s.transcribeStatus = TranscribeStatusMsg{State: "checking", Engine: transcribeEngineName}
	}
	s.transcribeStatusMu.Unlock()

	probe := s.transcribeProbe
	if probe == nil {
		probe = s.checkTranscribeAccess
	}
	ctx, cancel := context.WithTimeout(context.Background(), transcribeCheckTimeout)
	st := probe(ctx)
	cancel()
	st.CheckedAt = time.Now().UTC().Format(time.RFC3339)

	s.transcribeStatusMu.Lock()
	prev := s.transcribeStatus
	s.transcribeStatus = st
	s.transcribeChecking = false
	s.transcribeStatusMu.Unlock()

	if prev.State != st.State || prev.Reason != st.Reason || prev.Identity != st.Identity {
		if st.State == "available" {
			log.Printf("transcribe: %s available (region %s, as %s via %s)", st.Engine, st.Region, st.Identity, st.CredentialSource)
		} else {
			log.Printf("transcribe: %s unavailable: %s", st.Engine, st.Reason)
		}
		// Re-announce to local-representative so the mic icons up and down
		// the chain follow the change (Revision B).
		s.pushConvoState()
	}
	s.broadcast("transcribe-status", st)
}

// watchTranscribeStatus runs the availability check at startup and then
// every transcribeCheckInterval, or every transcribeRetryInterval while the
// last result was "unavailable". Run in its own goroutine; never returns.
func (s *Server) watchTranscribeStatus() {
	for {
		s.refreshTranscribeStatus()
		s.transcribeStatusMu.Lock()
		unavailable := s.transcribeStatus.State == "unavailable"
		s.transcribeStatusMu.Unlock()
		if unavailable {
			time.Sleep(transcribeRetryInterval)
		} else {
			time.Sleep(transcribeCheckInterval)
		}
	}
}

// sendTranscribeStatus sends the latest availability result to a single
// (usually newly-connected) client.
func (s *Server) sendTranscribeStatus(c *wsClient) {
	s.transcribeStatusMu.Lock()
	st := s.transcribeStatus
	s.transcribeStatusMu.Unlock()
	if st.State == "" {
		st = TranscribeStatusMsg{State: "checking", Engine: transcribeEngineName}
	}
	s.sendToClient(c, "transcribe-status", st)
}

// broadcast sends one message to every connected client.
func (s *Server) broadcast(typ string, payload interface{}) {
	msg := s.marshalMsg(typ, payload)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}
