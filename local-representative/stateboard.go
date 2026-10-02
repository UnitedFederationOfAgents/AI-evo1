package main

import (
	"sort"
	"strconv"
	"strings"

	ufahostid "ufa-hostid"
)

// This file implements the debug view's "stateboard" tab
// (condocs/initialDistributedSessionsImpls/Step2Prompt.md Revision E): a
// generic key/value board any representable-connected sub-app can post
// custom entries to, plus a default "<app>-present"/"<app>-hosts" pair LR
// derives itself for every app it knows how to manage, from representable's
// own connection health -- mirrors debugLog/chainCall (procman.go) in shape
// and in how it's relayed up to agent-coordinator.

// StateboardEntry is one key/value row.
type StateboardEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// StateboardMsg is the payload of "stateboard-state" messages: the full
// current stateboard, broadcast to browser clients and mirrored up to
// agent-coordinator -- mirrors DebugLogStateMsg/ChainCallStateMsg.
type StateboardMsg struct {
	Entries []StateboardEntry `json:"entries"`
}

// stateboardApp names one sub-app that gets a default "<label>-present"/
// "<label>-hosts" pair computed automatically from representable's
// connection health. clientName is the name it connects to representable
// as (see managedApps' --name args); label is the human-facing app name
// used in the stateboard's key names, which isn't always the same string
// (e.g. session-manager connects as "sessions").
var stateboardApps = []struct{ clientName, label string }{
	{"federation-command", "federation-command"},
	{"condoccer", "condoccer"},
	{"sessions", "session-manager"},
	{"convo", "the-conversationalist"},
}

// setStateboardKV records one custom key/value pair submitted by a connected
// sub-app over representable's generic "stateboard" data message (see
// reprServer.SetDataHandler) -- any sub-app can post any key, which is what
// makes this capability generic rather than hard-coded per app name.
// Broadcasts a fresh stateboard snapshot.
func (s *Server) setStateboardKV(key, value string) {
	if key == "" {
		return
	}
	s.stateboardMu.Lock()
	s.stateboardCustom[key] = value
	s.stateboardMu.Unlock()
	s.broadcastStateboard()
}

// setFCHead records one federation-command instance's self-reported head ID
// (see federation-command/main.go's fcHeadID), keyed by the LR-assigned
// instance id it was launched with (FC_INSTANCE_ID -- see
// managedApps["federation-command"].buildEnv) -- the only way to tell apart
// instances of this N-per-host app on the stateboard's
// "federation-command-instances" row, since representable itself tracks one
// connection identity per app name regardless of how many instances share it
// (see versionMu's comment in main.go). A manually-launched FC that was
// never given an instance id (no --auto-connect launch from this LR) simply
// never appears here, the same way it's absent from the system tab's managed
// list. Broadcasts a fresh stateboard snapshot.
func (s *Server) setFCHead(instanceID, head string) {
	if instanceID == "" || head == "" {
		return
	}
	s.stateboardMu.Lock()
	s.fcHeads[instanceID] = head
	s.stateboardMu.Unlock()
	s.broadcastStateboard()
}

// clearFCHead drops a no-longer-running instance's self-reported head --
// called from reapManaged/terminateManaged once that instance stops.
func (s *Server) clearFCHead(instanceID string) {
	s.stateboardMu.Lock()
	_, had := s.fcHeads[instanceID]
	delete(s.fcHeads, instanceID)
	s.stateboardMu.Unlock()
	if had {
		s.broadcastStateboard()
	}
}

// stateboard assembles the current stateboard snapshot: every custom
// key/value pair a connected sub-app has submitted, plus the default
// "<app>-present"/"<app>-hosts" pair for every app in stateboardApps,
// derived live from representable's own connection health, plus
// federation-command's "-instances" row built from the heads self-reported
// by LR-launched instances (see setFCHead). Sorted by key for a stable
// display order.
func (s *Server) stateboard() StateboardMsg {
	host := ufahostid.GetHostID()

	s.stateboardMu.Lock()
	entries := make([]StateboardEntry, 0, len(s.stateboardCustom)+len(stateboardApps)*2+1)
	for k, v := range s.stateboardCustom {
		entries = append(entries, StateboardEntry{Key: k, Value: v})
	}
	heads := make([]string, 0, len(s.fcHeads))
	for _, h := range s.fcHeads {
		heads = append(heads, h)
	}
	s.stateboardMu.Unlock()

	for _, app := range stateboardApps {
		present := s.reprServer != nil && s.reprServer.IsHealthy(app.clientName)
		entries = append(entries, StateboardEntry{Key: app.label + "-present", Value: strconv.FormatBool(present)})
		hosts := ""
		if present {
			hosts = host
		}
		entries = append(entries, StateboardEntry{Key: app.label + "-hosts", Value: hosts})
	}

	if len(heads) > 0 {
		sort.Strings(heads)
		pairs := make([]string, len(heads))
		for i, h := range heads {
			pairs[i] = host + ":" + h
		}
		entries = append(entries, StateboardEntry{Key: "federation-command-instances", Value: strings.Join(pairs, ", ")})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return StateboardMsg{Entries: entries}
}

func (s *Server) broadcastStateboard() {
	st := s.stateboard()
	s.broadcast("stateboard-state", st)
	if ac := s.getACClient(); ac != nil {
		ac.SendData("stateboard-state", st)
	}
}
