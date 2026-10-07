package main

import (
	"encoding/json"
	"net/http"
	"net/url"
)

// GET /api/file-peers tells condoccer where else to look for highlighted
// files when it adds a "Highlighted" resource
// (condocs/initialRobotImpls/Step3Prompt.md, Revision K): every other LR
// connected to agent-coordinator, each as the base URL of AC's read-only
// /host/<node>/* proxy to it. GET <base>/api/files and
// GET <base>/api/files/<id> through that proxy work the same as against
// this LR directly (both are ungated on proxiedHeader), so condoccer reads
// each peer's files tab exactly as it reads this one's.
//
// With no agent-coordinator connection, peers is empty and condoccer only
// looks at this LR.

// FilePeer is one other node whose files tab condoccer can read.
type FilePeer struct {
	Node string `json:"node"`
	Base string `json:"base"` // "http://<ac>/host/<node>"
}

// FilePeersMsg is GET /api/file-peers' answer. Node is this LR's own name.
type FilePeersMsg struct {
	Node  string     `json:"node"`
	Peers []FilePeer `json:"peers"`
}

func (s *Server) handleFilePeers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	msg := FilePeersMsg{Node: s.lrName, Peers: []FilePeer{}}
	if addr, ok := s.acHTTPAddr(); ok {
		msg.Peers = s.filePeersFrom("http://" + addr)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msg)
}

// filePeersFrom lists the other LRs agent-coordinator (at acBase) shows as
// connected, as FilePeers reached through its /host/<node>/* proxy.
func (s *Server) filePeersFrom(acBase string) []FilePeer {
	peers := []FilePeer{}
	for _, node := range s.listPeerHosts(acBase) {
		peers = append(peers, FilePeer{Node: node, Base: acBase + "/host/" + url.PathEscape(node)})
	}
	return peers
}
