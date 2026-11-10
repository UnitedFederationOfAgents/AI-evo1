package main

import (
	"encoding/json"
	"log"

	"ufa-loader/restartsignal"
)

// acState is agent-coordinator's own restart-carryover payload: the live
// in-memory state, as of the moment a restart was requested, that this AC
// attaches to its restart announcement (see restartsignal.AnnounceState) for
// the newly launched instance replacing it to read back (see
// loadPreviousACState) and apply on top of a fresh startup. Mirrors
// local-representative's own lrState (reststate.go), scoped down to just the
// one piece of live state AC itself carries -- see
// condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step1SubstepCPrompt.md Revision D.
type acState struct {
	// AutoUpdate mirrors selfVersion's auto-update toggle (see
	// selfversion.go): without carrying this forward, AC would silently turn
	// auto-update back off the very first time it fires a restart, so an
	// operator would have to re-tick the checkbox after every single update
	// instead of it "just happening" from then on.
	AutoUpdate bool `json:"auto_update,omitempty"`
}

// currentACState captures the live AC-specific state a restart should carry
// forward to the instance replacing this one -- see acState.
func (s *Server) currentACState() acState {
	return acState{AutoUpdate: s.selfVersion.autoUpdateEnabled()}
}

// loadPreviousACState reads the state a prior instance of this same AC
// attached to the restart that led to this launch (see
// restartsignal.PreviousState), returning ok=false on a fresh launch or an
// absent/unparseable payload.
func loadPreviousACState() (st acState, ok bool) {
	raw, present := restartsignal.PreviousState()
	if !present {
		return acState{}, false
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		log.Printf("ignoring unparseable restart state: %v", err)
		return acState{}, false
	}
	return st, true
}
