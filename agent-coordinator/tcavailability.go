package main

// TCAvailabilityMsg is the "tc-availability" WebSocket payload broadcast to
// browser clients: the aggregate answer to "is a the-conversationalist
// instance available on any host" -- the mic icon shown beside the
// camera/screenshot icon in the agent-coordinator header (illuminated when
// Available), and the same signal relayed down the representable command
// channel to every connected local-representative (see sendTCAvailabilityTo
// below and local-representative/tcavailability.go), which further relays it
// to its own managed condoccer so condoccer's text-input mic icons know
// whether The Conversationalist can transcribe for them. See
// condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step2Prompt.md.
type TCAvailabilityMsg struct {
	Available bool `json:"available"`
}

// tcAvailabilityCommand renders the current aggregate as the command string
// sent down the representable channel -- "__tc-availability:true" or
// "__tc-availability:false", mirroring the "__system:"/"__ridealong:"
// namespaced-command convention used elsewhere in this chain.
func tcAvailabilityCommand(available bool) string {
	if available {
		return "__tc-availability:true"
	}
	return "__tc-availability:false"
}

// anyConvoAvailable reports whether at least one connected local-representative
// is currently forwarding a live the-conversationalist instance -- the "on
// any host" test from Step2Prompt.md.
func (s *Server) anyConvoAvailable() bool {
	s.hostsMu.RLock()
	defer s.hostsMu.RUnlock()
	for _, hs := range s.hostStates {
		hs.mu.RLock()
		available := hs.convo != nil
		hs.mu.RUnlock()
		if available {
			return true
		}
	}
	return false
}

// sendTCAvailabilityTo pushes the current aggregate down to a single named
// local-representative -- used when that LR has just (re)connected and so
// hasn't received any tc-availability command yet, regardless of whether the
// aggregate itself has changed recently.
func (s *Server) sendTCAvailabilityTo(name string) {
	if s.reprServer == nil {
		return
	}
	s.reprServer.SendCommand(name, tcAvailabilityCommand(s.anyConvoAvailable()))
}

// broadcastTCAvailability recomputes the aggregate the-conversationalist
// availability and, if it changed since the last call, broadcasts it to
// every connected browser client and relays it down the representable
// command channel to every connected local-representative. Called whenever a
// host's convo-state changes or a host disconnects.
func (s *Server) broadcastTCAvailability() {
	available := s.anyConvoAvailable()

	s.tcMu.Lock()
	changed := s.tcAvailable != available
	s.tcAvailable = available
	s.tcMu.Unlock()
	if !changed {
		return
	}

	s.broadcast("tc-availability", TCAvailabilityMsg{Available: available})

	if s.reprServer == nil {
		return
	}
	cmd := tcAvailabilityCommand(available)
	s.hostsMu.RLock()
	names := make([]string, 0, len(s.hostStates))
	for name, hs := range s.hostStates {
		hs.mu.RLock()
		connected := hs.connected
		hs.mu.RUnlock()
		if connected {
			names = append(names, name)
		}
	}
	s.hostsMu.RUnlock()
	for _, name := range names {
		s.reprServer.SendCommand(name, cmd)
	}
}
