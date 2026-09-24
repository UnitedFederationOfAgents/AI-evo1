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
// relaunched with. Originally (per
// condocs/initialDistributedDevelopmentImpls/Step3Prompt.md Revision D)
// covered only LR's own state; ManagedApps below extends it to the
// sub-applications LR itself launches, per Step4Prompt.md Revision I.
type lrState struct {
	// AutoRebuild mirrors the dev-repo watcher's auto-rebuild toggle (see
	// repowatch.go). Always false (a no-op to apply) when this LR wasn't
	// watching a repo at all.
	AutoRebuild bool `json:"auto_rebuild,omitempty"`

	// AutoConnect is the persistent auto-connect toggle (see Revision I of
	// Step3Prompt.md) -- a first-class state independent of any single
	// connection attempt, so it carries forward whether this LR was armed to
	// keep reaching for agent-coordinator, not merely whether it happened to
	// be connected/connecting at the instant of the restart. ACHost/ACPort
	// carry the target it was armed for, so the newly launched instance
	// picks the connection back up at the same address regardless of what
	// --auto-connect/--ac-host/--ac-port it happens to be relaunched with.
	AutoConnect bool   `json:"auto_connect,omitempty"`
	ACHost      string `json:"ac_host,omitempty"`
	ACPort      string `json:"ac_port,omitempty"`

	// ManagedApps is the auto-launch-style token list ("app" or "app:N", see
	// parseAutoLaunchEntry) for every LR-launched managed sub-application
	// instance still running at the moment a restart was requested (see
	// runningManagedTokens). A restart terminates those instances as its own
	// final act (see terminateManagedForRestart) before exiting, so the
	// instance replacing this one relaunches them fresh -- see Revision I of
	// Step4Prompt.md. Deliberately scoped to LR's own launched children only:
	// a future "manage-on-connect" instance (not yet implemented) is neither
	// terminated by a restart nor carried forward here.
	ManagedApps []string `json:"managed_apps,omitempty"`
}

// currentState captures the live LR-specific state a restart should carry
// forward to the instance replacing this one -- see lrState.
func (s *Server) currentState() lrState {
	var st lrState
	if s.repoWatch != nil {
		st.AutoRebuild = s.repoWatch.snapshot().AutoRebuild
	}
	ac := s.getACState()
	st.AutoConnect = ac.AutoConnect
	if st.AutoConnect {
		st.ACHost, st.ACPort = ac.Host, ac.Port
	}
	st.ManagedApps = s.runningManagedTokens()
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

// applyToConfig overrides cfg's auto-connect and auto-launch settings with
// this restored state -- the auto-rebuild half is applied separately once
// repoWatch exists, since it isn't part of appConfig (see main). The live
// state takes precedence over cfg's own values (from flags/config), which is
// why this unconditionally overwrites AutoConnect and autoLaunch rather than
// only filling gaps -- see Revision D ("the live state will take precedence
// over arguments where applicable") and Revision I (ManagedApps is what was
// actually running when the restart was requested, which may differ from
// whatever --auto-launch this instance happens to be relaunched with -- e.g.
// an instance an operator had since terminated by hand shouldn't come back).
func (st lrState) applyToConfig(cfg *appConfig) {
	cfg.autoConnect = st.AutoConnect
	if st.ACHost != "" {
		cfg.acHost = st.ACHost
	}
	if st.ACPort != "" {
		cfg.acPort = st.ACPort
	}
	cfg.autoLaunch = st.ManagedApps
}
