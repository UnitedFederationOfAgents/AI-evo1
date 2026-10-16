package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"ufa-loader/awsprincipal"
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
	// accepted yet (see awsprincipal.Provision) turns ok promptly.
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

// connectivityWatch periodically detects and verifies provider credentials.
// The awsIdentity/githubAPI/http fields exist so tests can stand in for the
// real services.
type connectivityWatch struct {
	notify func()

	awsIdentity func(ctx context.Context, creds awsprincipal.Credentials) (string, error)
	githubAPI   string
	http        *http.Client

	mu    sync.RWMutex
	conns []ConnectionInfo
}

func newConnectivityWatch(notify func()) *connectivityWatch {
	return &connectivityWatch{
		notify:      notify,
		awsIdentity: awsprincipal.CallerIdentity,
		githubAPI:   defaultGitHubAPI,
		http:        &http.Client{Timeout: connectivityCheckTimeout},
	}
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
		time.Sleep(d)
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
			if c.Provider == p.info.Provider {
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
	if creds, ok := awsprincipal.ResolveCredentials(); ok {
		out = append(out, presentProvider{
			info: ConnectionInfo{Provider: "aws", Status: connChecking, Source: awsCredentialSource()},
			verify: func(ctx context.Context) (string, error) {
				return w.awsIdentity(ctx, creds)
			},
		})
	}
	for _, k := range githubTokenEnvVars {
		if tok := os.Getenv(k); tok != "" {
			out = append(out, presentProvider{
				info: ConnectionInfo{Provider: "github", Status: connChecking, Source: k},
				verify: func(ctx context.Context) (string, error) {
					return w.githubLogin(ctx, tok)
				},
			})
			break
		}
	}
	return out
}

// awsCredentialSource names where awsprincipal.ResolveCredentials found its
// keys -- the environment (as ufa-loader leaves its temporary principal) or
// a shared-file profile.
func awsCredentialSource() string {
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != "" {
		return "environment"
	}
	for _, k := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE"} {
		if p := os.Getenv(k); p != "" {
			return "profile " + p
		}
	}
	return "profile default"
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
