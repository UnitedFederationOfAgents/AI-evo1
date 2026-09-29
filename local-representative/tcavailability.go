package main

import "strings"

// TCAvailabilityMsg is the "tc-availability" WebSocket payload broadcast to
// this local-representative's own browser clients: the aggregate answer (as
// relayed down from agent-coordinator's own aggregate -- see
// agent-coordinator/tcavailability.go) to "is a the-conversationalist
// instance available on any host". It backs the mic icon shown beside the
// camera/screenshot icon in local-representative's header (illuminated when
// Available), mirroring agent-coordinator's own header treatment, and is
// further relayed down to this LR's own managed condoccer (see
// sendTCAvailabilityToCondoccer) so condoccer's text-input mic icons know
// whether The Conversationalist can transcribe for them. See
// condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step2Prompt.md.
type TCAvailabilityMsg struct {
	Available bool `json:"available"`
}

// tcAvailabilityCommand renders available as the command string relayed down
// the representable channel -- "__tc-availability:true" or
// "__tc-availability:false", mirroring agent-coordinator's own helper of the
// same name and the "__system:"/"__condoccer:" namespaced-command convention
// used elsewhere in this chain.
func tcAvailabilityCommand(available bool) string {
	if available {
		return "__tc-availability:true"
	}
	return "__tc-availability:false"
}

// handleTCAvailabilityCommand applies a "__tc-availability:true" /
// "__tc-availability:false" command relayed down from agent-coordinator.
func (s *Server) handleTCAvailabilityCommand(cmd string) {
	rest := strings.TrimPrefix(cmd, "__tc-availability:")
	s.setTCAvailability(rest == "true")
}

// getTCAvailability returns the last aggregate verdict relayed down from
// agent-coordinator (false until the first command arrives).
func (s *Server) getTCAvailability() bool {
	s.tcMu.RLock()
	defer s.tcMu.RUnlock()
	return s.tcAvailable
}

// setTCAvailability records the aggregate verdict, broadcasts it to this LR's
// own browser clients, and relays it on down to condoccer.
func (s *Server) setTCAvailability(available bool) {
	s.tcMu.Lock()
	s.tcAvailable = available
	s.tcMu.Unlock()
	s.broadcast("tc-availability", TCAvailabilityMsg{Available: available})
	s.sendTCAvailabilityToCondoccer()
}

// sendTCAvailabilityToCondoccer pushes the current aggregate down to this
// LR's own managed condoccer over the representable command channel --
// called both whenever the aggregate changes (setTCAvailability) and right
// after condoccer (re)connects, since it otherwise wouldn't learn a value
// that hasn't changed since before it connected.
func (s *Server) sendTCAvailabilityToCondoccer() {
	if s.reprServer == nil {
		return
	}
	s.reprServer.SendCommand("condoccer", tcAvailabilityCommand(s.getTCAvailability()))
}
