package main

import (
	"net/http"
	"net/url"
	"time"
)

// This file is clauditable's half of the distributed-sessions wiring
// described in docs/DistributedSessionsBrainstorm.md's "Working Section":
// the trigger that asks local-representative to once-transfer another
// LR-active host's "-s-processed" records for a session in, before a
// freshly-declared primary does anything else. (The mirror-image "sync"
// trigger, for a *reader* refreshing "session.jsonl"/"-processed" before
// viewing a session, belongs to whatever actually reads a session for that
// purpose — see session-manager/repr.go's triggerSessionSync — not to
// clauditable, which never reads a session it didn't just write.)
//
// The actual cross-host pull (discovering which other hosts are LR-active,
// fetching their matching files, checksum bookkeeping) is performed by
// local-representative itself (see local-representative/sessions.go) — this
// file just asks it to, over one best-effort local HTTP call. clauditable is
// too short-lived to keep a persistent representable connection of its own
// the way session-manager does (see session-manager/repr.go), so it can't
// learn local-representative's HTTP port the way an already-connected
// sibling can (representable.Client.PeerHTTPPort, disclosed on connect) —
// instead it just uses the same env-var-driven discovery convention as
// AGENT_RECORDS_PATH/AGENT_SESSION elsewhere in this file.
const (
	// EnvLRHTTPHost / EnvLRHTTPPort name the local-representative HTTP API
	// this host's clauditable invocations should ask for a once-transfer.
	EnvLRHTTPHost = "LR_HTTP_HOST"
	EnvLRHTTPPort = "LR_HTTP_PORT"

	// DefaultLRHTTPHost / DefaultLRHTTPPort match local-representative's own
	// "-port" flag default (see local-representative/main.go) for the
	// overwhelmingly common case of an unconfigured local install.
	DefaultLRHTTPHost = "localhost"
	DefaultLRHTTPPort = "8081"

	// onceTransferTimeout bounds how long a freshly-declared primary waits on
	// the local local-representative before giving up and proceeding exactly
	// as if distributed sessions weren't in play. This must never meaningfully
	// delay an ordinary invocation when no local-representative is running at
	// all (the common case for tests and simple local use) — a refused
	// connection fails in well under this, so the timeout only bites when
	// local-representative exists but is unresponsive.
	onceTransferTimeout = 2 * time.Second
)

// triggerOnceTransfer asks the local local-representative to once-transfer
// every other LR-active host's not-yet-seen "*-s-processed.txt" files for
// session into this host's own session directory. Called right after a
// clauditable invocation is declared primary, before it runs the wrapped
// command — see main.go — so that by the time this invocation reaches
// consolidatePrimaryToJSONL at completion, remote secondaries already sit
// alongside local ones, ready to be folded into session.jsonl the same way.
//
// Best-effort and silent: local-representative may not be running, may not
// be connected to agent-coordinator, or may have no peers at all for this
// session — none of that is an error clauditable should ever surface or let
// slow down the command it's wrapping.
func triggerOnceTransfer(session string) {
	requestSessionPull(session, "*-s-processed.txt", "once")
}

// requestSessionPull issues the actual best-effort POST described above.
func requestSessionPull(session, glob, mode string) {
	host := getEnvOrDefault(EnvLRHTTPHost, DefaultLRHTTPHost)
	port := getEnvOrDefault(EnvLRHTTPPort, DefaultLRHTTPPort)

	u := url.URL{
		Scheme: "http",
		Host:   host + ":" + port,
		// Path holds the unescaped form -- url.URL.String() escapes it (and
		// any special characters session carries) itself; pre-escaping here
		// too would double-encode it.
		Path: "/api/sessions/" + session + "/pull",
	}
	q := u.Query()
	q.Set("glob", glob)
	q.Set("mode", mode)
	u.RawQuery = q.Encode()

	client := &http.Client{Timeout: onceTransferTimeout}
	resp, err := client.Post(u.String(), "application/octet-stream", nil)
	if err != nil {
		// No local-representative to ask, or it's unreachable — proceed
		// exactly as if distributed sessions weren't in play at all.
		return
	}
	resp.Body.Close()
}
