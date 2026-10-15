package main

import (
	"sort"
	"strings"
	"time"
)

// This file tracks every federation-command instance connected to this LR
// individually (condocs/initialRobotImpls/Step3Prompt.md: "make the
// federation-command tab able to recognize and handle the multiple FC
// instances"). Each FC instance connects to representable under its own
// name (see federation-command/main.go's fcReprName): its LR-assigned
// instance id ("federation-command#2") when this LR launched it, otherwise
// "federation-command@<head>". An older FC still connects as the bare
// "federation-command", which is kept as an instance of its own. That name is
// the instance's key everywhere: in the "fc-instances" snapshot, on every
// fc-state/fc-log/ridealong-state/condoc-state message (FC field), and as the
// target of a browser's or agent-coordinator's command.

const fcAppName = "federation-command"

// isFCName reports whether a representable client name belongs to a
// federation-command instance.
func isFCName(name string) bool {
	return name == fcAppName || strings.HasPrefix(name, fcAppName+"#") || strings.HasPrefix(name, fcAppName+"@")
}

// fcLabel is the short name the UI shows for an instance: "#2" for one this
// LR launched, "@fc-ab12" for one launched independently.
func fcLabel(key string) string {
	if key == fcAppName {
		return "federation-command"
	}
	return strings.TrimPrefix(key, fcAppName)
}

// fcInstance is one connected federation-command instance.
type fcInstance struct {
	key         string
	instanceID  string // LR-assigned instance id it reported (FC_INSTANCE_ID), "" if none
	head        string // its self-generated head ID
	state       string // "remote-control" or "local-control"
	sessionID   string
	sessionName string
	ridealong   *RidealongStateMsg
	condoc      *CondocStateMsg
	connectedAt time.Time
}

// FCInstanceInfo is one entry of the "fc-instances" snapshot.
type FCInstanceInfo struct {
	Key        string             `json:"key"`
	Label      string             `json:"label"`
	InstanceID string             `json:"instance_id,omitempty"`
	Head       string             `json:"head,omitempty"`
	State      string             `json:"state"`
	Session    string             `json:"session,omitempty"`
	Ridealong  *RidealongStateMsg `json:"ridealong,omitempty"`
	Condoc     *CondocStateMsg    `json:"condoc,omitempty"`
}

// FCInstancesMsg is the payload of "fc-instances" messages: every connected
// federation-command instance, sent to browser clients and agent-coordinator
// whenever one connects, disconnects or changes state.
type FCInstancesMsg struct {
	Instances []FCInstanceInfo `json:"instances"`
}

// fcInstanceLocked returns the record for key, creating it if needed.
// Callers must hold fcMu.
func (s *Server) fcInstanceLocked(key string) *fcInstance {
	inst := s.fcInst[key]
	if inst == nil {
		inst = &fcInstance{key: key, connectedAt: time.Now()}
		if strings.HasPrefix(key, fcAppName+"#") {
			inst.instanceID = key
		}
		s.fcInst[key] = inst
	}
	return inst
}

// fcInstances returns the current snapshot, ordered LR-launched instances
// first (by ordinal), then the rest by name.
func (s *Server) fcInstances() FCInstancesMsg {
	s.fcMu.RLock()
	out := make([]FCInstanceInfo, 0, len(s.fcInst))
	for _, inst := range s.fcInst {
		session := inst.sessionName
		if session == "" {
			session = inst.sessionID
		}
		out = append(out, FCInstanceInfo{
			Key:        inst.key,
			Label:      fcLabel(inst.key),
			InstanceID: inst.instanceID,
			Head:       inst.head,
			State:      inst.state,
			Session:    session,
			Ridealong:  inst.ridealong,
			Condoc:     inst.condoc,
		})
	}
	s.fcMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return fcKeyLess(out[i].Key, out[j].Key) })
	return FCInstancesMsg{Instances: out}
}

// fcKeyLess orders "federation-command#N" keys by N, ahead of every other key.
func fcKeyLess(a, b string) bool {
	na, aok := fcOrdinal(a)
	nb, bok := fcOrdinal(b)
	switch {
	case aok && bok:
		return na < nb
	case aok != bok:
		return aok
	}
	return a < b
}

func fcOrdinal(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, fcAppName+"#")
	if !ok || rest == "" {
		return 0, false
	}
	n := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// broadcastFCInstances pushes the snapshot to browser clients and
// agent-coordinator.
func (s *Server) broadcastFCInstances() {
	msg := s.fcInstances()
	s.broadcast("fc-instances", msg)
	if ac := s.getACClient(); ac != nil {
		ac.SendData("fc-instances", msg)
	}
}

// fcState returns one instance's control state ("" when not connected).
func (s *Server) fcState(key string) string {
	s.fcMu.RLock()
	defer s.fcMu.RUnlock()
	if inst := s.fcInst[key]; inst != nil {
		return inst.state
	}
	return ""
}

// fcKeyForInstanceID finds the connected instance LR launched as instanceID.
func (s *Server) fcKeyForInstanceID(instanceID string) string {
	s.fcMu.RLock()
	defer s.fcMu.RUnlock()
	for key, inst := range s.fcInst {
		if key == instanceID || inst.instanceID == instanceID {
			return key
		}
	}
	return ""
}

// fcInstanceIDClaimed reports whether a connected instance goes by
// instanceID, as its key or its reported instance id.
func (s *Server) fcInstanceIDClaimed(instanceID string) bool {
	return s.fcKeyForInstanceID(instanceID) != ""
}

// fcKeys returns the keys of every connected instance.
func (s *Server) fcKeys() map[string]bool {
	s.fcMu.RLock()
	defer s.fcMu.RUnlock()
	keys := make(map[string]bool, len(s.fcInst))
	for k := range s.fcInst {
		keys[k] = true
	}
	return keys
}

// anyFCHealthy reports whether at least one FC instance is connected and
// heartbeating -- the federation-command service status.
func (s *Server) anyFCHealthy() bool {
	if s.reprServer == nil {
		return false
	}
	s.fcMu.RLock()
	keys := make([]string, 0, len(s.fcInst))
	for k := range s.fcInst {
		keys = append(keys, k)
	}
	s.fcMu.RUnlock()
	for _, k := range keys {
		if s.reprServer.IsHealthy(k) {
			return true
		}
	}
	return false
}

// defaultFCKey picks the instance a command without an explicit target goes
// to: the only instance when there is one, otherwise the first (see
// fcInstances' order) in remote control, otherwise the first. "" if none.
func (s *Server) defaultFCKey() string {
	insts := s.fcInstances().Instances
	for _, inst := range insts {
		if inst.State == "remote-control" {
			return inst.Key
		}
	}
	if len(insts) > 0 {
		return insts[0].Key
	}
	return ""
}

// resolveFCKey maps a requested target (empty for "the default") to a
// connected instance's key.
func (s *Server) resolveFCKey(requested string) string {
	if requested == "" {
		return s.defaultFCKey()
	}
	if isFCName(requested) {
		return requested
	}
	return ""
}

// sendFCCommand delivers cmd to one FC instance (the default one when key is
// empty).
func (s *Server) sendFCCommand(key, cmd string) {
	if s.reprServer == nil {
		return
	}
	if key = s.resolveFCKey(key); key != "" {
		s.reprServer.SendCommand(key, cmd)
	}
}

// setFCInstanceState records a control-state change (or a disconnect) for
// one instance and announces it.
func (s *Server) setFCInstanceState(key, state string) {
	disconnected := state == "disconnected"
	s.fcMu.Lock()
	var instanceID string
	if disconnected {
		if inst := s.fcInst[key]; inst != nil {
			instanceID = inst.instanceID
		}
		delete(s.fcInst, key)
		state = ""
	} else {
		s.fcInstanceLocked(key).state = state
	}
	s.fcMu.Unlock()

	s.broadcast("fc-state", FCStateMsg{FC: key, State: state})
	ac := s.getACClient()
	if ac != nil {
		ac.SendData("fc-state", FCStateMsg{FC: key, State: state})
	}
	if disconnected {
		s.broadcast("ridealong-state", RidealongStateMsg{FC: key, Active: false})
		s.broadcast("condoc-state", CondocStateMsg{FC: key, Active: false})
		if ac != nil {
			ac.SendData("ridealong-state", RidealongStateMsg{FC: key, Active: false})
			ac.SendData("condoc-state", CondocStateMsg{FC: key, Active: false})
		}
		s.setModeMismatch(key, false, "")
		if instanceID != "" {
			s.clearFCHead(instanceID)
		}
		s.broadcastStateboard()
	}
	s.broadcastFCInstances()
}

// setFCInstanceRidealong records one instance's ridealong state.
func (s *Server) setFCInstanceRidealong(key string, payload RidealongStateMsg) {
	payload.FC = key
	s.fcMu.Lock()
	inst := s.fcInstanceLocked(key)
	if payload.Active {
		inst.ridealong = &payload
	} else {
		inst.ridealong = nil
	}
	s.fcMu.Unlock()
	s.broadcast("ridealong-state", payload)
	if ac := s.getACClient(); ac != nil {
		ac.SendData("ridealong-state", payload)
	}
	s.broadcastFCInstances()
}

// setFCInstanceCondoc records one instance's condoc state.
func (s *Server) setFCInstanceCondoc(key string, payload CondocStateMsg) {
	payload.FC = key
	s.fcMu.Lock()
	inst := s.fcInstanceLocked(key)
	if payload.Active {
		inst.condoc = &payload
	} else {
		inst.condoc = nil
	}
	s.fcMu.Unlock()
	s.broadcast("condoc-state", payload)
	if ac := s.getACClient(); ac != nil {
		ac.SendData("condoc-state", payload)
	}
	s.broadcastFCInstances()
}

// setFCInstanceSession records the session and identity one instance
// reported in its "fc-session" message.
func (s *Server) setFCInstanceSession(key string, payload FCSessionMsg) {
	s.fcMu.Lock()
	inst := s.fcInstanceLocked(key)
	changed := inst.sessionID != payload.ID || inst.sessionName != payload.Name ||
		(payload.InstanceID != "" && inst.instanceID != payload.InstanceID) || inst.head != payload.Head
	inst.sessionID, inst.sessionName, inst.head = payload.ID, payload.Name, payload.Head
	if payload.InstanceID != "" {
		inst.instanceID = payload.InstanceID
	}
	s.fcMu.Unlock()
	if changed {
		s.broadcastFCInstances()
		s.broadcastSystemState()
	}
}

// fcSessionFor returns the session the instance LR launched as instanceID
// reported (display name if it has one, else its id), "" if it hasn't.
func (s *Server) fcSessionFor(instanceID string) string {
	s.fcMu.RLock()
	defer s.fcMu.RUnlock()
	for key, inst := range s.fcInst {
		if key == instanceID || inst.instanceID == instanceID {
			if inst.sessionName != "" {
				return inst.sessionName
			}
			return inst.sessionID
		}
	}
	return ""
}

// handleFCLog relays one log/output line from an instance.
func (s *Server) handleFCLog(key, line, kind string) {
	msg := FCLogMsg{FC: key, Line: line, Kind: kind}
	s.broadcast("fc-log", msg)
	if ac := s.getACClient(); ac != nil {
		ac.SendData("fc-log", msg)
	}
	s.control.noteFCLog(key, line, kind)
}

// pushFCStateToAC sends every instance's state to agent-coordinator -- part
// of pushStateToAC's full snapshot.
func (s *Server) pushFCStateToAC() {
	ac := s.getACClient()
	if ac == nil {
		return
	}
	insts := s.fcInstances()
	ac.SendData("fc-instances", insts)
	for _, inst := range insts.Instances {
		ac.SendData("fc-state", FCStateMsg{FC: inst.Key, State: inst.State})
	}
}
