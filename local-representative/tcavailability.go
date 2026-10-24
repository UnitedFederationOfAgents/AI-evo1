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

// getTCAvailability returns the verdict this LR shows and relays: the last
// aggregate relayed down from agent-coordinator (false until the first
// command arrives), further gated on this host's own managed
// the-conversationalist reporting its transcription engine reachable. The
// local gate matters because every mic this LR's verdict drives -- its own
// header and new-text-file dialog, and condoccer's inputs -- captures
// through this LR's own /convo/ proxy, so another host's working TC doesn't
// help them (condocs/initialRobotImpls/Step5Prompt.md, Revision B).
func (s *Server) getTCAvailability() bool {
	s.tcMu.RLock()
	aggregate := s.tcAvailable
	s.tcMu.RUnlock()
	if !aggregate {
		return false
	}
	cv := s.getConvoState()
	return cv != nil && cv.HTTPPort != "" && cv.TranscribeAvailable
}

// setTCAvailability records the aggregate verdict relayed down from
// agent-coordinator and pushes the resulting effective verdict out.
func (s *Server) setTCAvailability(available bool) {
	s.tcMu.Lock()
	s.tcAvailable = available
	s.tcMu.Unlock()
	s.pushTCAvailability()
}

// pushTCAvailability broadcasts the current effective verdict to this LR's
// own browser clients and relays it on down to condoccer -- called whenever
// either input to getTCAvailability changes (the relayed aggregate, or the
// local the-conversationalist's convo-state).
func (s *Server) pushTCAvailability() {
	s.broadcast("tc-availability", TCAvailabilityMsg{Available: s.getTCAvailability()})
	s.sendTCAvailabilityToCondoccer()
}

// sendTCAvailabilityToCondoccer pushes the current aggregate down to this
// LR's own managed condoccer over the representable command channel --
// called both whenever the aggregate changes (setTCAvailability) and right
// after condoccer (re)connects, since it otherwise wouldn't learn a value
// that hasn't changed since before it connected. condocv2 (a copy of
// condoccer, mutually exclusive with it) gets the same push; SendCommand is a
// no-op for whichever of the two isn't connected.
func (s *Server) sendTCAvailabilityToCondoccer() {
	if s.reprServer == nil {
		return
	}
	cmd := tcAvailabilityCommand(s.getTCAvailability())
	s.reprServer.SendCommand("condoccer", cmd)
	s.reprServer.SendCommand("condocv2", cmd)
}
