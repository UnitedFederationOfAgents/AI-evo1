package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Connectivity checks: which outside providers this host holds credentials
// for, and whether those credentials currently work -- surfaced on the
// system tab (and agent-coordinator's topology cards / per-host "providers"
// section) alongside the process list. See
// condocs/slopspaceCondocsImpls/Step1SubstepCPrompt.md.

const (
	// connectivityPollInterval is how often working credentials are
	// re-checked; connectivityRetryInterval is used instead while any check
	// is failing, so a freshly-created ufa-loader principal that AWS hasn't
	// accepted yet turns ok promptly.
	connectivityPollInterval  = 5 * time.Minute
	connectivityRetryInterval = 30 * time.Second
	connectivityCheckTimeout  = 20 * time.Second

	defaultGitHubAPI = "https://api.github.com"
)

// Connection statuses: "checking" until the first verification finishes,
// then "ok" or "failed".
const (
	connChecking = "checking"
	connOK       = "ok"
	connFailed   = "failed"
)

// ConnectionInfo is one provider entry in SystemStateMsg.Connections. A
// provider only appears when credentials for it are present at all.
type ConnectionInfo struct {
	Provider  string `json:"provider"`            // "aws" | "github"
	Status    string `json:"status"`              // connChecking | connOK | connFailed
	Principal string `json:"principal,omitempty"` // AWS caller ARN / GitHub login, once verified
	Source    string `json:"source,omitempty"`    // where the credentials came from, e.g. "TF_VAR_github_pat"
	Detail    string `json:"detail,omitempty"`    // why the last check failed
}

// githubTokenEnvVars are checked in order for a GitHub PAT -- the same
// list condoccer's pointer repo uses (condoccer/pointer.go), led by the
// variable dungeon-keeper's readspace/writespace read.
var githubTokenEnvVars = []string{"TF_VAR_github_pat", "GH_TOKEN", "GITHUB_TOKEN"}

// Connected providers (Step1SubstepEPrompt.md): the "providers" section's
// "connect" control hands LR a credential at runtime. Only GitHub, by PAT,
// so far. A connected credential is held in memory only -- never written
// to disk, so it's gone once LR restarts -- takes precedence over any
// environment credential for the same provider, and is passed to every
// managed app LR launches afterwards as TF_VAR_github_pat (see childEnv).
const (
	connectedSource       = "connected"
	connectedGitHubPATEnv = "TF_VAR_github_pat"
)

// connectableProviders lists the providers the "connect" control accepts.
var connectableProviders = []string{"github"}

// connectivityWatch periodically detects and verifies provider credentials.
// The awsIdentity/stsEndpoint/githubAPI/http fields exist so tests can
// stand in for the real services.
type connectivityWatch struct {
	notify func()

	awsIdentity func(ctx context.Context, creds awsCredentials) (string, error)
	stsEndpoint string
	githubAPI   string
	http        *http.Client

	// wake asks watchLoop to re-check now rather than at its next poll,
	// after a connect/disconnect.
	wake chan struct{}

	mu        sync.RWMutex
	conns     []ConnectionInfo
	githubPAT string // connected GitHub PAT, "" when none
}

func newConnectivityWatch(notify func()) *connectivityWatch {
	w := &connectivityWatch{
		notify:      notify,
		stsEndpoint: defaultSTSEndpoint,
		githubAPI:   defaultGitHubAPI,
		http:        &http.Client{Timeout: connectivityCheckTimeout},
		wake:        make(chan struct{}, 1),
	}
	w.awsIdentity = w.awsCallerIdentity
	return w
}

// connect keeps credential (in memory) as provider's connected credential and
// re-checks straight away. The credential itself never appears in the
// returned error or the log.
func (w *connectivityWatch) connect(provider, credential string) error {
	if provider != "github" {
		return fmt.Errorf("provider %q can't be connected (supported: %s)", provider, strings.Join(connectableProviders, ", "))
	}
	tok := strings.TrimSpace(credential)
	if tok == "" {
		return errors.New("GitHub PAT is empty")
	}
	if strings.ContainsAny(tok, " \t\r\n") {
		return errors.New("GitHub PAT contains whitespace")
	}
	w.mu.Lock()
	w.githubPAT = tok
	w.mu.Unlock()
	w.recheck()
	return nil
}

// disconnect forgets provider's connected credential (falling back to any
// environment credential) and re-checks straight away.
func (w *connectivityWatch) disconnect(provider string) error {
	if provider != "github" {
		return fmt.Errorf("provider %q can't be disconnected (supported: %s)", provider, strings.Join(connectableProviders, ", "))
	}
	w.mu.Lock()
	w.githubPAT = ""
	w.mu.Unlock()
	w.recheck()
	return nil
}

// connectProvider handles the providers section's "connect" control -- the
// system tab's WebSocket "connect-provider" or agent-coordinator's
// "__system:connect <provider> <credential>". Failures are logged (without
// the credential); success shows up as the provider's connection entry.
func (s *Server) connectProvider(provider, credential string) {
	if s.connectivity == nil {
		return
	}
	if err := s.connectivity.connect(provider, credential); err != nil {
		log.Printf("connectivity: connect %s: %v", provider, err)
		return
	}
	log.Printf("connectivity: %s credential connected", provider)
}

// disconnectProvider is connectProvider's "disconnect" counterpart
// ("disconnect-provider" / "__system:disconnect <provider>").
func (s *Server) disconnectProvider(provider string) {
	if s.connectivity == nil {
		return
	}
	if err := s.connectivity.disconnect(provider); err != nil {
		log.Printf("connectivity: disconnect %s: %v", provider, err)
		return
	}
	log.Printf("connectivity: %s credential disconnected", provider)
}

func (w *connectivityWatch) recheck() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// childEnv is the KEY=VALUE entries a managed app gets on top of LR's own
// environment so it uses the connected credentials; nil-safe.
func (w *connectivityWatch) childEnv() []string {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.githubPAT == "" {
		return nil
	}
	return []string{connectedGitHubPATEnv + "=" + w.githubPAT}
}

// connections returns a copy of the latest check results; nil-safe so the
// system-state snapshot can be built before the watch exists.
func (w *connectivityWatch) connections() []ConnectionInfo {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]ConnectionInfo(nil), w.conns...)
}

func (w *connectivityWatch) watchLoop() {
	for {
		d := connectivityPollInterval
		if w.check() {
			d = connectivityRetryInterval
		}
		select {
		case <-time.After(d):
		case <-w.wake:
		}
	}
}

// check re-detects and re-verifies every provider, notifying when the
// result differs from last time. It reports whether any check failed.
func (w *connectivityWatch) check() (anyFailed bool) {
	present := w.detect()

	// Publish newly-appeared providers as "checking" straight away, so the
	// icon shows up before a slow verification returns.
	w.mu.Lock()
	prev := w.conns
	pending := make([]ConnectionInfo, len(present))
	for i, p := range present {
		pending[i] = p.info
		for _, c := range prev {
			if c.Provider == p.info.Provider && c.Source == p.info.Source {
				pending[i] = c
				break
			}
		}
	}
	changed := !slices.Equal(prev, pending)
	w.conns = pending
	w.mu.Unlock()
	if changed && w.notify != nil {
		w.notify()
	}

	results := make([]ConnectionInfo, len(present))
	for i, p := range present {
		ctx, cancel := context.WithTimeout(context.Background(), connectivityCheckTimeout)
		principal, err := p.verify(ctx)
		cancel()
		info := p.info
		if err != nil {
			info.Status = connFailed
			info.Detail = err.Error()
			anyFailed = true
		} else {
			info.Status = connOK
			info.Principal = principal
		}
		results[i] = info
	}

	w.mu.Lock()
	changed = !slices.Equal(w.conns, results)
	w.conns = results
	w.mu.Unlock()
	if changed && w.notify != nil {
		w.notify()
	}
	return anyFailed
}

type presentProvider struct {
	info   ConnectionInfo
	verify func(ctx context.Context) (string, error)
}

// detect lists the providers whose credentials are present, AWS first.
func (w *connectivityWatch) detect() []presentProvider {
	var out []presentProvider
	if creds, source, ok := resolveAWSCredentials(); ok {
		out = append(out, presentProvider{
			info: ConnectionInfo{Provider: "aws", Status: connChecking, Source: source},
			verify: func(ctx context.Context) (string, error) {
				return w.awsIdentity(ctx, creds)
			},
		})
	}
	w.mu.RLock()
	tok, source := w.githubPAT, connectedSource
	w.mu.RUnlock()
	for _, k := range githubTokenEnvVars {
		if tok != "" {
			break
		}
		tok, source = os.Getenv(k), k
	}
	if tok != "" {
		out = append(out, presentProvider{
			info: ConnectionInfo{Provider: "github", Status: connChecking, Source: source},
			verify: func(ctx context.Context) (string, error) {
				return w.githubLogin(ctx, tok)
			},
		})
	}
	return out
}

// awsCallerIdentity is the default awsIdentity: STS GetCallerIdentity via
// LR's own signer (awssts.go).
func (w *connectivityWatch) awsCallerIdentity(ctx context.Context, creds awsCredentials) (string, error) {
	return stsCallerIdentity(ctx, w.http, w.stsEndpoint, creds, time.Now())
}

// githubLogin returns the login tok authenticates as (GET /user).
func (w *connectivityWatch) githubLogin(ctx context.Context, tok string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.githubAPI+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := w.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub /user: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("GitHub /user: decoding response: %w", err)
	}
	if body.Login == "" {
		return "", fmt.Errorf("GitHub /user: response carried no login")
	}
	return body.Login, nil
}
