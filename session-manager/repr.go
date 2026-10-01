package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"representable"
	ufaversion "ufa-version"
)

// Auto-connect (--auto-connect) tuning: on startup session-manager dials
// local-representative in the background, retrying on an interval until the
// window elapses. Mirrors condoccer's/federation-command's/
// local-representative's --auto-connect so session-manager joins the same
// autolaunch chain.
const (
	autoConnectInterval    = 10 * time.Second
	autoConnectWindow      = 10 * time.Minute
	autoConnectDialTimeout = 3 * time.Second
)

// SessionsStateMsg is the "sessions-state" data payload session-manager
// pushes to local-representative (which forwards a copy up to
// agent-coordinator). It lets the rest of the stack know which port to
// reverse-proxy the session-manager UI from. Grows domain-specific fields in
// a later step.
type SessionsStateMsg struct {
	HTTPPort string `json:"http_port"`
}

// ReprStatusMsg is the "repr-status" WebSocket payload pushed to the frontend
// so its manual connect/disconnect widget reflects session-manager's actual
// link to local-representative, whichever of --auto-connect or the widget
// started it.
type ReprStatusMsg struct {
	Status string `json:"status"` // "disconnected" | "connecting" | "connected"
	Host   string `json:"host,omitempty"`
	Port   string `json:"port,omitempty"`
	// AutoConnect is the persistent auto-connect toggle: true whenever the
	// cycle is armed, whether or not it's currently connected/connecting --
	// it stays true across a successful connection, and only an explicit
	// disconnect turns it off.
	AutoConnect bool `json:"auto_connect,omitempty"`
}

// SelfInfoMsg discloses this instance's own dev-mode status and build
// version to its frontend (see docs/DevMode.md) -- sent once when a browser
// client connects.
type SelfInfoMsg struct {
	DevMode bool   `json:"dev_mode"`
	Version string `json:"version"`
}

// ModeMismatchMsg discloses that the connected local-representative's
// dev-mode status differs from this instance's own. Mismatched=false clears
// a previously-disclosed mismatch.
type ModeMismatchMsg struct {
	Mismatched bool   `json:"mismatched"`
	PeerMode   string `json:"peer_mode,omitempty"`
}

// setModeMismatch records the current mismatch verdict against
// local-representative and broadcasts it to every connected browser client.
func (s *Server) setModeMismatch(mismatched bool, peerMode string) {
	s.reprMu.Lock()
	s.modeMismatch = mismatched
	s.modeMismatchPeer = peerMode
	s.reprMu.Unlock()
	msg := s.marshalMsg("mode-mismatch", ModeMismatchMsg{Mismatched: mismatched, PeerMode: peerMode})
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

// sendModeMismatch sends the current mismatch verdict to a single (usually
// newly-connected) WebSocket client.
func (s *Server) sendModeMismatch(c *wsClient) {
	s.reprMu.Lock()
	mismatched, peerMode := s.modeMismatch, s.modeMismatchPeer
	s.reprMu.Unlock()
	s.sendToClient(c, "mode-mismatch", ModeMismatchMsg{Mismatched: mismatched, PeerMode: peerMode})
}

// startConnectLoop launches a fresh connectLoop dialling host:port, first
// stopping any loop already running (an earlier --auto-connect or widget
// "connect"). Used both for --auto-connect at startup and for the frontend's
// manual "Connect" button.
func (s *Server) startConnectLoop(host, port string) {
	s.stopConnectLoop()
	stopCh := make(chan struct{})
	s.reprMu.Lock()
	s.reprStop = stopCh
	s.reprHost = host
	s.reprPort = port
	s.reprMu.Unlock()
	go s.connectLoop(host, port, stopCh)
}

// stopConnectLoop signals any running connectLoop to give up rather than
// retry, and closes an active connection if there is one. It's the mechanical
// half of a "disconnect" -- also reused by startConnectLoop to clear out a
// prior attempt before a fresh "connect" replaces it -- so unlike
// disconnectRepr it deliberately leaves the auto-connect toggle untouched.
func (s *Server) stopConnectLoop() {
	s.reprMu.Lock()
	stopCh := s.reprStop
	client := s.reprClient
	s.reprStop = nil
	s.reprClient = nil
	s.reprMu.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
	if client != nil {
		client.Close()
	}
	if stopCh != nil || client != nil {
		s.setReprStatus("disconnected")
		s.setModeMismatch(false, "")
	}
}

// disconnectRepr is the widget's explicit "disconnect" action. Being
// operator-driven, it also terminates auto-connect entirely -- unlike an
// unintentional drop, which resumes the retry cycle automatically as long as
// auto-connect is still armed (see connectLoop).
func (s *Server) disconnectRepr() {
	s.reprMu.Lock()
	s.reprAutoConnect = false
	s.reprMu.Unlock()
	s.stopConnectLoop()
}

// setAutoConnect drives session-manager's persistent auto-connect toggle
// from the frontend widget, or from --auto-connect at startup: a first-class
// state independent of any single connection attempt. Enabling it arms the
// flag -- so a later unintentional disconnect resumes the retry cycle on its
// own -- and starts a connectLoop unless one is already running; disabling
// it only stops that cycle from resuming. It never forces an active
// connection down; only disconnectRepr does that.
func (s *Server) setAutoConnect(enabled bool, host, port string) {
	s.reprMu.Lock()
	s.reprAutoConnect = enabled
	if host == "" {
		host = s.reprHost
	}
	if port == "" {
		port = s.reprPort
	}
	running := s.reprStop != nil
	status := s.reprStatus
	s.reprMu.Unlock()

	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "8082"
	}

	if enabled && !running {
		s.startConnectLoop(host, port)
		return
	}
	s.setReprStatus(status)
}

// connectLoop maintains session-manager's representable connection to
// local-representative. It retries every autoConnectInterval for up to
// autoConnectWindow to establish the link; once connected it pushes the
// current state and blocks until the connection drops, then starts a fresh
// window. It gives up instead of retrying as soon as stopCh is closed —
// that's how a widget "disconnect" (or a replacing "connect") ends a
// previous loop. Runs in its own goroutine.
func (s *Server) connectLoop(host, port string, stopCh chan struct{}) {
	addr := net.JoinHostPort(host, port)
	for {
		select {
		case <-stopCh:
			return
		default:
		}

		s.setReprStatus("connecting")
		deadline := time.Now().Add(autoConnectWindow)
		var client *representable.Client
		for client == nil {
			c, err := representable.Connect(addr, s.name, representable.Mode(s.devMode), autoConnectDialTimeout)
			if err == nil {
				client = c
				break
			}
			select {
			case <-stopCh:
				s.setReprStatus("disconnected")
				return
			default:
			}
			if time.Now().After(deadline) {
				log.Printf("connect: gave up after %s — local-representative at %s did not respond",
					autoConnectWindow, addr)
				s.reprMu.Lock()
				if s.reprStop == stopCh {
					s.reprStop = nil
				}
				s.reprMu.Unlock()
				s.setReprStatus("disconnected")
				return
			}
			time.Sleep(autoConnectInterval)
		}

		log.Printf("connected to local-representative at %s as %q", addr, s.name)
		s.reprMu.Lock()
		s.reprClient = client
		s.reprMu.Unlock()
		s.setReprStatus("connected")

		client.SetCommandHandler(s.handleReprCommand)
		client.SetModeMismatchHandler(func(mismatched bool, peerMode string) {
			s.setModeMismatch(mismatched, peerMode)
		})
		s.pushSessionsState()
		s.sendVersion()
		go s.refreshSessionsAfterConnect(client)

		<-client.DisconnectCh()

		s.reprMu.Lock()
		if s.reprClient == client {
			s.reprClient = nil
		}
		autoConnect := s.reprAutoConnect
		s.reprMu.Unlock()
		s.setModeMismatch(false, "")

		select {
		case <-stopCh:
			// An explicit disconnect (disconnectRepr) already closed stopCh
			// before this fired -- an intentional drop, so no retry.
			s.setReprStatus("disconnected")
			return
		default:
		}
		if !autoConnect {
			// Not armed to keep trying: an unintentional drop (the remote end
			// closing -- an intentional one already returned above via
			// stopCh) ends this one-shot connection rather than retrying.
			log.Printf("disconnected from local-representative at %s", addr)
			s.reprMu.Lock()
			if s.reprStop == stopCh {
				s.reprStop = nil
			}
			s.reprMu.Unlock()
			s.setReprStatus("disconnected")
			return
		}
		log.Printf("disconnected from local-representative at %s — auto-connect resuming the retry cycle", addr)
	}
}

// setReprStatus records the current connection status and pushes it to every
// WebSocket client so the manual connect/disconnect widget stays live.
func (s *Server) setReprStatus(status string) {
	s.reprMu.Lock()
	s.reprStatus = status
	host, port, autoConnect := s.reprHost, s.reprPort, s.reprAutoConnect
	s.reprMu.Unlock()
	s.broadcastReprStatus(status, host, port, autoConnect)
}

// broadcastReprStatus sends a "repr-status" message to every connected
// WebSocket client.
func (s *Server) broadcastReprStatus(status, host, port string, autoConnect bool) {
	msg := s.marshalMsg("repr-status", ReprStatusMsg{Status: status, Host: host, Port: port, AutoConnect: autoConnect})
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

// sendReprStatus sends the current connection status to a single (usually
// newly-connected) WebSocket client.
func (s *Server) sendReprStatus(c *wsClient) {
	s.reprMu.Lock()
	status, host, port, autoConnect := s.reprStatus, s.reprHost, s.reprPort, s.reprAutoConnect
	s.reprMu.Unlock()
	s.sendToClient(c, "repr-status", ReprStatusMsg{Status: status, Host: host, Port: port, AutoConnect: autoConnect})
}

// pushSessionsState sends the current state to local-representative. No-op
// when not connected.
func (s *Server) pushSessionsState() {
	s.reprMu.Lock()
	client := s.reprClient
	s.reprMu.Unlock()
	if client == nil {
		return
	}
	client.SendData("sessions-state", SessionsStateMsg{
		HTTPPort: s.httpPort,
	})
}

// versionPayload is sent once over the representable data channel right
// after connecting, so local-representative's system tab can list this
// instance's build version alongside its own (see docs/DevMode.md
// "Versioning").
type versionPayload struct {
	Version string `json:"version"`
}

// sendVersion reports this binary's build version to local-representative.
func (s *Server) sendVersion() {
	s.reprMu.Lock()
	client := s.reprClient
	s.reprMu.Unlock()
	if client == nil {
		return
	}
	client.SendData("version", versionPayload{Version: ufaversion.Version})
}

// handleReprCommand handles commands local-representative relays down the
// representable channel (originating from agent-coordinator). session-manager
// only acts on the "__sessions:" namespace, mirroring condoccer's
// "__condoccer:" convention; the forwarded WebSocket UI carries everything
// else.
//
//	__sessions:refresh
func (s *Server) handleReprCommand(raw string) {
	if !strings.HasPrefix(raw, "__sessions:") {
		return
	}
	rest := strings.TrimSpace(strings.TrimPrefix(raw, "__sessions:"))
	verb, _, _ := strings.Cut(rest, " ")
	switch verb {
	case "refresh":
		s.pushSessionsState()
	default:
		log.Printf("repr: ignoring unrecognised command %q", raw)
	}
}

// sessionSyncGlobs are the file patterns a session-view read keeps fresh --
// see triggerSessionSync. session.yaml is included so a session that exists
// only on a remote host (never before pulled here -- see
// listSessionsWithRemote's Remote-tagged entries) gets its name/metadata
// materialized locally too, not just its transcript.
var sessionSyncGlobs = []string{"session.jsonl", "session.yaml", "*-processed.txt"}

// sessionSyncTimeout bounds each glob's pull request -- see
// triggerSessionSync.
const sessionSyncTimeout = 2 * time.Second

// ChainCallEntry captures one outbound HTTP call this session-manager
// backend issued to local-representative -- the "sm->lr" half of the
// SM<->LR<->AC chain (condocs/initialDistributedSessionsImpls/
// Step1SubstepBPrompt.md Revision F). A browser's own window.fetch capture
// (see agent-coordinator's DebugView) can't see this: it never touches this
// process's own HTTP client. Reported to local-representative over
// representable (SendData("chain-call", ...)), which folds it into the same
// buffer it keeps for its own "lr->ac" hop and relays both up to
// agent-coordinator's debug view -- see local-representative/sessions.go's
// mirrored copy of this type.
type ChainCallEntry struct {
	Hop        string `json:"hop"` // always "sm->lr" from this binary
	Method     string `json:"method"`
	URL        string `json:"url"`
	Status     int    `json:"status"` // 0 on a network-level failure
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	TS         int64  `json:"ts"` // unix seconds, when the call was made
}

// reportChainCall records one sm->lr HTTP call's outcome and forwards it to
// local-representative, best-effort (same posture as every other SendData
// call -- see representable.Client.SendData). resp may be nil if err is set.
// repr may also be nil (e.g. a caller exercising the request-shaping logic
// without a live connection) -- a no-op, same posture as a nil client would
// get from any other best-effort report.
func reportChainCall(repr *representable.Client, method, target string, start time.Time, resp *http.Response, err error) {
	if repr == nil {
		return
	}
	entry := ChainCallEntry{
		Hop:        "sm->lr",
		Method:     method,
		URL:        target,
		DurationMS: time.Since(start).Milliseconds(),
		TS:         time.Now().Unix(),
	}
	if err != nil {
		entry.Error = err.Error()
	} else {
		entry.Status = resp.StatusCode
	}
	repr.SendData("chain-call", entry)
}

// triggerSessionSync asks local-representative to refresh id's
// "session.jsonl" and "*-processed.txt" files from every other LR-active
// host before this view is rendered -- see
// docs/DistributedSessionsBrainstorm.md: "Whenever any host is about to read
// a session (ie: ... bringing it up in session-manager for viewing) then it
// will request the jsonl and the -processed glob for sync (refreshed any
// number of times)." Called from sendSessionView (sessions.go) right before
// it reads the local session directory.
//
// Best-effort and bounded: with no live local-representative connection, or
// one that hasn't disclosed its HTTP port yet (see
// representable.Client.PeerHTTPPort, set from local-representative's "hello"
// message on connect), this is a no-op (incomplete is false: there's nothing
// to report as having failed) and the view renders exactly as it would have
// before this increment -- a purely local read.
//
// incomplete is true when any glob's pull reported a peer it couldn't
// actually reach (local-representative's SessionPullResultMsg.Errors > 0,
// not just "nothing new") -- see requestSessionPull and
// condocs/initialDistributedSessionsImpls/31e41125_network-debug-1790867771753.log's
// "502 then 200" sequence (Step1SubstepBPrompt.md Revision G). sendSessionView
// (sessions.go) surfaces this on the rendered view so a session that looks
// empty because its remote peer was unreachable isn't indistinguishable from
// one that's genuinely empty.
func (s *Server) triggerSessionSync(id string) (incomplete bool) {
	s.reprMu.Lock()
	client := s.reprClient
	host := s.reprHost
	s.reprMu.Unlock()
	if client == nil {
		return false
	}
	port := client.PeerHTTPPort()
	if port == "" {
		return false
	}
	for _, glob := range sessionSyncGlobs {
		if !requestSessionPull(client, host, port, id, glob) {
			incomplete = true
		}
	}
	return incomplete
}

// sessionPullResultMsg mirrors local-representative's SessionPullResultMsg --
// only Errors is read here; Hosts/Fetched are purely informational.
type sessionPullResultMsg struct {
	Errors int `json:"errors"`
}

// requestSessionPull issues one best-effort POST asking local-representative
// to sync-pull id's files matching glob from every other LR-active host --
// the mirror image of clauditable/distsync.go's requestSessionPull (mode
// "once" there, "sync" here). repr is the already-connected representable
// client to report the call's outcome to (see reportChainCall). Returns false
// when the pull itself failed outright (network error, non-200) or when
// local-representative reports at least one peer it couldn't reach --
// true otherwise, including the ordinary "had nothing new to pull" case.
func requestSessionPull(repr *representable.Client, lrHost, lrPort, sessionID, glob string) bool {
	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(lrHost, lrPort),
		// Path holds the unescaped form -- url.URL.String() escapes it (and
		// any special characters sessionID carries) itself; pre-escaping
		// here too would double-encode it.
		Path: "/api/sessions/" + sessionID + "/pull",
	}
	q := u.Query()
	q.Set("glob", glob)
	q.Set("mode", "sync")
	u.RawQuery = q.Encode()

	httpClient := &http.Client{Timeout: sessionSyncTimeout}
	start := time.Now()
	resp, err := httpClient.Post(u.String(), "application/octet-stream", nil)
	reportChainCall(repr, "POST", u.String(), start, resp, err)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var result sessionPullResultMsg
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("session pull: decoding result for %s %s: %v", sessionID, glob, err)
		return false
	}
	return result.Errors == 0
}

// remoteSessionEntry mirrors local-representative's RemoteSessionEntry (see
// sessions.go's handleSessionsDiscover) -- this binary's own copy of the
// wire shape, same posture as this file's/sessions.go's other mirrored
// helpers.
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

// sessionsDiscoveryDelay gives local-representative's "hello" time to
// disclose its HTTP port (see representable.Client.PeerHTTPPort) before the
// on-connect discovery poll fires -- without it, a poll fired the instant
// connectLoop adopts a client would almost always race the hello and
// silently no-op (triggerSessionsDiscovery's own bounded-timeout posture
// handles every other failure mode already).
const sessionsDiscoveryDelay = 500 * time.Millisecond

// sessionsDiscoveryTimeout bounds triggerSessionsDiscovery's HTTP request,
// deliberately distinct from (and longer than) sessionSyncTimeout: this has
// to cover local-representative's own worst-case round trip through
// handleSessionsDiscover -- a bounded call to list peers via
// agent-coordinator (up to local-representative's sessionsPullHTTPTimeout,
// 5s) followed by a concurrent per-peer indexing fan-out (up to its
// sessionsDiscoverHTTPTimeout, 2s, regardless of peer count) -- not just the
// network hop to it. sessionSyncTimeout's 2s is fine for a sync-pull (a
// single hop LR makes on our behalf with its own short-lived fetch), but was
// too short reused here: it let this call give up before
// handleSessionsDiscover could ever have succeeded, silently discarding
// real results on a live multi-host setup. This runs in its own goroutine
// (triggerSessionsDiscovery is always called off listSessionsWithRemote,
// itself only ever invoked from sendSessions/broadcastSessions's "go"
// callers -- see main.go/sessions.go), so a longer bound here doesn't risk
// blocking anything.
const sessionsDiscoveryTimeout = 8 * time.Second

// refreshSessionsAfterConnect fires the discovery poll once more shortly
// after connecting -- Step1SubstepBPrompt.md Revision A: "It will also
// happen on connect of FC or SM" (renderSessions' callers already cover
// "any list-sessions behaviour", since sendSessions/broadcastSessions run on
// every browser connect/mutation too). Run in its own goroutine from
// connectLoop so it never delays adopting the connection; client guards
// against firing for a connection already superseded by the time the delay
// elapses.
func (s *Server) refreshSessionsAfterConnect(client *representable.Client) {
	time.Sleep(sessionsDiscoveryDelay)
	s.reprMu.Lock()
	stillCurrent := s.reprClient == client
	s.reprMu.Unlock()
	if stillCurrent {
		s.broadcastSessions()
	}
}

// triggerSessionsDiscovery asks local-representative which sessions every
// other LR-active host has, for listSessionsWithRemote (sessions.go) to
// merge into a rendered session list as remote entries -- the fan-out half
// of closing condocs/initialDistributedSessionsImpls/RemoteSessionListingGap.md's
// gap. Best-effort and bounded, same posture as triggerSessionSync: returns
// nil with no live local-representative connection, one that hasn't
// disclosed its HTTP port yet, or on any request/decode failure --
// listSessionsWithRemote then returns exactly listSessions's purely-local
// result.
func (s *Server) triggerSessionsDiscovery() []remoteSessionEntry {
	s.reprMu.Lock()
	client := s.reprClient
	host := s.reprHost
	s.reprMu.Unlock()
	if client == nil {
		return nil
	}
	port := client.PeerHTTPPort()
	if port == "" {
		return nil
	}
	u := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/api/sessions/discover"}
	httpClient := &http.Client{Timeout: sessionsDiscoveryTimeout}
	start := time.Now()
	resp, err := httpClient.Post(u.String(), "application/octet-stream", nil)
	reportChainCall(client, "POST", u.String(), start, resp, err)
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
