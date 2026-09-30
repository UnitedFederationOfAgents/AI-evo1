package main

import "strings"

// TCAvailabilityMsg is the "tc-availability" WebSocket payload broadcast to
// condoccer's own browser clients: the aggregate answer (relayed down
// through local-representative from agent-coordinator's own aggregate -- see
// agent-coordinator/tcavailability.go and local-representative's
// tcavailability.go) to "is a the-conversationalist instance available on
// any host". It gates the mic icon shown on condoccer's text input fields --
// see condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step2Prompt.md.
type TCAvailabilityMsg struct {
	Available bool `json:"available"`
}

// handleTCAvailabilityCommand applies a "__tc-availability:true" /
// "__tc-availability:false" command relayed down from local-representative.
func (s *Server) handleTCAvailabilityCommand(cmd string) {
	rest := strings.TrimPrefix(cmd, "__tc-availability:")
	s.setTCAvailability(rest == "true")
}

// setTCAvailability records the aggregate verdict and broadcasts it to every
// connected browser client.
func (s *Server) setTCAvailability(available bool) {
	s.tcMu.Lock()
	s.tcAvailable = available
	s.tcMu.Unlock()
	msg := s.marshalMsg("tc-availability", TCAvailabilityMsg{Available: available})
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

// getTCAvailability returns the last aggregate verdict relayed down from
// local-representative (false until the first command arrives).
func (s *Server) getTCAvailability() bool {
	s.tcMu.RLock()
	defer s.tcMu.RUnlock()
	return s.tcAvailable
}

// sendTCAvailability sends the current aggregate verdict to a single
// (usually newly-connected) WebSocket client.
func (s *Server) sendTCAvailability(c *wsClient) {
	s.sendToClient(c, "tc-availability", TCAvailabilityMsg{Available: s.getTCAvailability()})
}
