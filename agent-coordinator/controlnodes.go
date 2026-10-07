package main

import (
	"encoding/json"
	"log"
	"sort"
)

// This file is agent-coordinator's part in control sequences that reach
// other nodes (condocs/initialRobotImpls/Step3Prompt.md, Revision F; see
// local-representative/controlnodes.go):
//
//   - every connected local-representative is told which nodes (hosts) are
//     connected, "__control:nodes [...]", whenever one comes or goes (and
//     when it asks, "control-nodes-request"), so its control tab's node
//     controls can offer only those;
//   - a host's "node-capture" request is passed to the node it names as
//     "__control:node-capture", stamped with the asking host's name, and the
//     node's "node-capture-result" is passed back to that host as
//     "__control:node-capture-result". A node that isn't connected is
//     answered for, so the asking run fails at once rather than timing out.
//   - "node-fetch" (files for node-fetch-file) and its "node-fetch-result"
//     are relayed the same way.

// nodeRequestKinds are the requests one host's control run makes of
// another node, relayed by relayNodeRequest; each is answered by
// "<kind>-result" (relayNodeResult).
var nodeRequestKinds = map[string]string{
	"node-capture": "screenshot",
	"node-fetch":   "file",
}

// connectedHostNames lists the hosts whose local-representative is
// connected, sorted.
func (s *Server) connectedHostNames() []string {
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
	sort.Strings(names)
	return names
}

// broadcastControlNodes tells every connected local-representative which
// nodes are connected. Called when a host connects or disconnects.
func (s *Server) broadcastControlNodes() {
	names := s.connectedHostNames()
	for _, name := range names {
		s.sendControlNodes(name, names)
	}
}

// sendControlNodes tells one local-representative which nodes are
// connected -- also on its asking ("control-nodes-request", sent when it
// connects), in case it reconnected before its disconnect was noticed here.
func (s *Server) sendControlNodes(to string, names []string) {
	if s.reprServer == nil {
		return
	}
	b, err := json.Marshal(names)
	if err != nil {
		return
	}
	s.reprServer.SendCommand(to, "__control:nodes "+string(b))
}

func (s *Server) hostConnected(name string) bool {
	s.hostsMu.RLock()
	hs, ok := s.hostStates[name]
	s.hostsMu.RUnlock()
	if !ok {
		return false
	}
	hs.mu.RLock()
	defer hs.mu.RUnlock()
	return hs.connected
}

// relayNodeRequest passes host from's request of kind (one of
// nodeRequestKinds) to the node it names.
func (s *Server) relayNodeRequest(kind, from string, data json.RawMessage) {
	var req map[string]interface{}
	if err := json.Unmarshal(data, &req); err != nil {
		log.Printf("control: bad %s request from %s: %v", kind, from, err)
		return
	}
	node, _ := req["node"].(string)
	req["from"] = from // whoever it says it is, it's the host that sent it
	if node == "" || !s.hostConnected(node) {
		s.answerNodeRequest(kind, from, map[string]interface{}{
			"req": req["req"], "from": from, "node": node, "success": false,
			"error": "node " + quoteName(node) + " isn't connected to agent-coordinator",
		})
		return
	}
	b, err := json.Marshal(req)
	if err != nil || s.reprServer == nil {
		return
	}
	log.Printf("control: relaying %s's %s request to %s", from, nodeRequestKinds[kind], node)
	s.reprServer.SendCommand(node, "__control:"+kind+" "+string(b))
}

// relayNodeResult passes a node's "<kind>-result" back to the host that
// asked.
func (s *Server) relayNodeResult(kind, node string, data json.RawMessage) {
	var res map[string]interface{}
	if err := json.Unmarshal(data, &res); err != nil {
		log.Printf("control: bad %s result from %s: %v", kind, node, err)
		return
	}
	from, _ := res["from"].(string)
	if from == "" || !s.hostConnected(from) {
		return // the host that asked has gone
	}
	res["node"] = node
	s.answerNodeRequest(kind, from, res)
}

func (s *Server) answerNodeRequest(kind, to string, res map[string]interface{}) {
	b, err := json.Marshal(res)
	if err != nil || s.reprServer == nil {
		return
	}
	s.reprServer.SendCommand(to, "__control:"+kind+"-result "+string(b))
}

func quoteName(name string) string {
	b, _ := json.Marshal(name)
	return string(b)
}
