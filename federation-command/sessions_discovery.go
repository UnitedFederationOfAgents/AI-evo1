package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"representable"
)

// This file is federation-command's half of closing the remote-session-
// listing gap described in
// condocs/initialDistributedSessionsImpls/RemoteSessionListingGap.md:
// local-representative's unscoped GET /api/sessions (a host's own session
// index) fanned out across every other LR-active host via its POST
// /api/sessions/discover (see local-representative/sessions.go's
// handleSessionsDiscover) gives renderSessions something to merge in as
// remote entries, tagged with the host they came from.
//
// Step1SubstepBPrompt.md Revision A: "Any list-sessions behaviour will
// initiate this poll. It will also happen on connect of FC or SM." The
// first half is renderSessions' three callers in main.go each passing in a
// fresh discoverRemoteSessions() result; the second is
// sessionsDiscoveryDelayCmd below, queued from both connect paths
// (reprConnectedMsg and a successful autoConnectResultMsg).

// remoteSessionEntry mirrors local-representative's RemoteSessionEntry (see
// sessions.go's handleSessionsDiscover) -- federation-command's own copy of
// the wire shape, same posture as this binary's other mirrored session
// helpers (readSessionName, readSessionYAMLFields, ...).
type remoteSessionEntry struct {
	HostID string `json:"host_id"`
	ID     string `json:"id"`
	Name   string `json:"name"`
}

// sessionDiscoveryMsg mirrors local-representative's SessionDiscoveryMsg --
// only Sessions is read here; Hosts is purely informational.
type sessionDiscoveryMsg struct {
	Sessions []remoteSessionEntry `json:"sessions"`
}

// sessionsDiscoveryTimeout bounds the discovery HTTP request itself. It
// blocks whichever Update call triggered it -- list-sessions' renders
// already run synchronously in Update, same as every other locally-fast
// renderSessions call in this file.
//
// This has to cover local-representative's own worst-case round trip, not
// just the network hop to it: handleSessionsDiscover (see
// local-representative/sessions.go) itself makes a bounded outbound call to
// list peers via agent-coordinator (up to sessionsPullHTTPTimeout, 5s)
// followed by a concurrent per-peer indexing fan-out (up to
// sessionsDiscoverHTTPTimeout, 2s, regardless of peer count since that fan-
// out runs in parallel) before it can answer. A timeout here shorter than
// that inner budget would make this call give up before local-
// representative could ever have succeeded, silently discarding real
// results -- exactly what a too-short timeout here previously did on a live
// multi-host setup.
const sessionsDiscoveryTimeout = 8 * time.Second

// discoverRemoteSessions asks local-representative which sessions every
// other LR-active host has, for renderSessions to merge in as remote
// entries. Best-effort and bounded: returns nil with no live
// local-representative connection, one that hasn't disclosed its HTTP port
// yet (see representable.Client.PeerHTTPPort), or on any request/decode
// failure -- renderSessions then shows exactly the purely-local list it
// would have before this increment.
func (m appModel) discoverRemoteSessions() []remoteSessionEntry {
	if m.reprClient == nil {
		return nil
	}
	port := m.reprClient.PeerHTTPPort()
	if port == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(m.lrAddr)
	if err != nil {
		return nil
	}
	u := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/api/sessions/discover"}
	client := &http.Client{Timeout: sessionsDiscoveryTimeout}
	resp, err := client.Post(u.String(), "application/octet-stream", nil)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var msg sessionDiscoveryMsg
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return nil
	}
	return msg.Sessions
}

// sessionsDiscoveryDelay gives local-representative's "hello" time to
// disclose its HTTP port (see discoverRemoteSessions) before the on-connect
// poll fires -- without it, a poll fired the instant a connect path adopts
// a client would almost always race the hello and silently no-op.
const sessionsDiscoveryDelay = 500 * time.Millisecond

// sessionsDiscoveryTickMsg fires once sessionsDiscoveryDelay has elapsed
// after client adopted a connection -- see sessionsDiscoveryDelayCmd.
type sessionsDiscoveryTickMsg struct{ client *representable.Client }

// sessionsDiscoveryDelayCmd schedules the on-connect discovery poll --
// Step1SubstepBPrompt.md Revision A: "It will also happen on connect of FC
// or SM." Queued from both connect paths (reprConnectedMsg and a successful
// autoConnectResultMsg) alongside their other post-connect commands.
func sessionsDiscoveryDelayCmd(client *representable.Client) tea.Cmd {
	return tea.Tick(sessionsDiscoveryDelay, func(t time.Time) tea.Msg {
		return sessionsDiscoveryTickMsg{client: client}
	})
}

// sessionsDiscoveryNotice renders a one-line heads-up when the on-connect
// poll turns up sessions no other host has told this one about yet --
// enough to point the user at 'list-sessions' without dumping the full
// listing unprompted on every connect.
func sessionsDiscoveryNotice(remote []remoteSessionEntry) string {
	return sessionStyle.Render(fmt.Sprintf(
		"discovered %d session(s) on other hosts — run 'list-sessions' to view", len(remote)))
}
