package main

import (
	"encoding/json"
	"log"

	"ufa-loader/restartsignal"
)

// lrState is local-representative's own restart-carryover payload: the live
// in-memory state, as of the moment a restart was requested, that this LR
// attaches to its restart announcement (see restartsignal.AnnounceState) for
// the newly launched instance replacing it to read back (see
// loadPreviousState) and apply on top of whatever flags/config it was
// relaunched with. Deliberately covers only LR's own state -- nothing yet
// for the sub-applications it manages -- per
// condocs/initialDistributedDevelopmentImpls/Step3Prompt.md Revision D.
type lrState struct {
	// AutoRebuild mirrors the dev-repo watcher's auto-rebuild toggle (see
	// repowatch.go). Always false (a no-op to apply) when this LR wasn't
	// watching a repo at all.
	AutoRebuild bool `json:"auto_rebuild,omitempty"`

	// AutoConnect, ACHost and ACPort mirror whether this LR was connected --
	// or attempting to connect, via the startup auto-connect retry loop or
	// an explicit operator connect -- to agent-coordinator, and at what
	// address, so the newly launched instance picks the connection back up
	// at the same target regardless of what --auto-connect/--ac-host/
	// --ac-port it happens to be relaunched with.
	AutoConnect bool   `json:"auto_connect,omitempty"`
	ACHost      string `json:"ac_host,omitempty"`
	ACPort      string `json:"ac_port,omitempty"`
}

// currentState captures the live LR-specific state a restart should carry
// forward to the instance replacing this one -- see lrState.
func (s *Server) currentState() lrState {
	var st lrState
	if s.repoWatch != nil {
		st.AutoRebuild = s.repoWatch.snapshot().AutoRebuild
	}
	ac := s.getACState()
	st.AutoConnect = ac.Connected || ac.Connecting
	if st.AutoConnect {
		st.ACHost, st.ACPort = ac.Host, ac.Port
	}
	return st
}

// loadPreviousState reads the state a prior instance of this same LR
// attached to the restart that led to this launch (see
// restartsignal.PreviousState), returning ok=false on a fresh launch or an
// absent/unparseable payload -- in which case the caller falls back to
// ordinary flag/config resolution.
func loadPreviousState() (st lrState, ok bool) {
	raw, present := restartsignal.PreviousState()
	if !present {
		return lrState{}, false
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		log.Printf("ignoring unparseable restart state: %v", err)
		return lrState{}, false
	}
	return st, true
}

// applyToConfig overrides cfg's auto-connect settings with this restored
// state -- the auto-rebuild half is applied separately once repoWatch
// exists, since it isn't part of appConfig (see main). The live state takes
// precedence over cfg's own values (from flags/config), which is why this
// unconditionally overwrites AutoConnect rather than only filling gaps --
// see Revision D ("the live state will take precedence over arguments where
// applicable").
func (st lrState) applyToConfig(cfg *appConfig) {
	cfg.autoConnect = st.AutoConnect
	if st.ACHost != "" {
		cfg.acHost = st.ACHost
	}
	if st.ACPort != "" {
		cfg.acPort = st.ACPort
	}
}
