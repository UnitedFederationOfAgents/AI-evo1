package main

import (
	"reflect"
	"testing"
)

func TestIsFCName(t *testing.T) {
	for name, want := range map[string]bool{
		"federation-command":         true,
		"federation-command#2":       true,
		"federation-command@fc-ab12": true,
		"robot":                      false,
		"condoccer":                  false,
		"federation-commander":       false,
		"not-federation-command#2":   false,
	} {
		if got := isFCName(name); got != want {
			t.Errorf("isFCName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFCInstancesOrderAndLabels(t *testing.T) {
	s := newServer("test-lr")
	for _, key := range []string{"federation-command@fc-zz", "federation-command#10", "federation-command#2", "federation-command"} {
		s.setFCInstanceState(key, "remote-control")
	}
	var keys, labels []string
	for _, inst := range s.fcInstances().Instances {
		keys = append(keys, inst.Key)
		labels = append(labels, inst.Label)
	}
	wantKeys := []string{"federation-command#2", "federation-command#10", "federation-command", "federation-command@fc-zz"}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Errorf("order = %v, want %v", keys, wantKeys)
	}
	wantLabels := []string{"#2", "#10", "federation-command", "@fc-zz"}
	if !reflect.DeepEqual(labels, wantLabels) {
		t.Errorf("labels = %v, want %v", labels, wantLabels)
	}
}

func TestFCInstanceStateAndDisconnect(t *testing.T) {
	s := newServer("test-lr")
	s.setFCInstanceState("federation-command#1", "remote-control")
	s.setFCInstanceState("federation-command#2", "local-control")

	if got := s.fcState("federation-command#2"); got != "local-control" {
		t.Errorf("fcState(#2) = %q, want local-control", got)
	}
	if got := s.fcKeyForInstanceID("federation-command#1"); got != "federation-command#1" {
		t.Errorf("fcKeyForInstanceID(#1) = %q", got)
	}

	s.setFCInstanceState("federation-command#1", "disconnected")
	if got := s.fcState("federation-command#1"); got != "" {
		t.Errorf("fcState(#1) after disconnect = %q, want empty", got)
	}
	if n := len(s.fcInstances().Instances); n != 1 {
		t.Errorf("%d instances after one disconnected, want 1", n)
	}
}

func TestDefaultFCKeyPrefersRemoteControl(t *testing.T) {
	s := newServer("test-lr")
	if got := s.defaultFCKey(); got != "" {
		t.Errorf("defaultFCKey with no instances = %q, want empty", got)
	}
	s.setFCInstanceState("federation-command#1", "local-control")
	s.setFCInstanceState("federation-command#2", "remote-control")
	if got := s.defaultFCKey(); got != "federation-command#2" {
		t.Errorf("defaultFCKey = %q, want the remote-control instance", got)
	}
	if got := s.resolveFCKey("federation-command#1"); got != "federation-command#1" {
		t.Errorf("resolveFCKey(#1) = %q", got)
	}
	if got := s.resolveFCKey("robot"); got != "" {
		t.Errorf("resolveFCKey(robot) = %q, want empty", got)
	}
}

func TestFCInstanceSessionAndRidealong(t *testing.T) {
	s := newServer("test-lr")
	s.setFCInstanceState("federation-command@fc-ab12", "remote-control")
	s.setFCInstanceSession("federation-command@fc-ab12", FCSessionMsg{ID: "sess-1", Name: "work", InstanceID: "federation-command#4", Head: "fc-ab12"})
	s.setFCInstanceRidealong("federation-command@fc-ab12", RidealongStateMsg{Active: true, Title: "demo"})

	if got := s.fcSessionFor("federation-command#4"); got != "work" {
		t.Errorf("fcSessionFor = %q, want work", got)
	}
	inst := s.fcInstances().Instances[0]
	if inst.InstanceID != "federation-command#4" || inst.Head != "fc-ab12" || inst.Session != "work" {
		t.Errorf("instance = %+v", inst)
	}
	if inst.Ridealong == nil || inst.Ridealong.Title != "demo" || inst.Ridealong.FC != "federation-command@fc-ab12" {
		t.Errorf("ridealong = %+v", inst.Ridealong)
	}

	s.setFCInstanceRidealong("federation-command@fc-ab12", RidealongStateMsg{Active: false})
	if inst := s.fcInstances().Instances[0]; inst.Ridealong != nil {
		t.Errorf("ridealong after it ended = %+v, want nil", inst.Ridealong)
	}
}
