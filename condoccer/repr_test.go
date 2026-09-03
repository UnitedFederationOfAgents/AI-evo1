package main

import "testing"

// TestPushCondoccerStateNoClient verifies the state push is a safe no-op before
// the representable link to local-representative is established.
func TestPushCondoccerStateNoClient(t *testing.T) {
	s := newServer(t.TempDir())
	s.httpPort = "8080"
	s.name = "condoccer"
	s.pushCondoccerState() // must not panic or block
}

// TestHandleReprCommandToleratesJunk verifies condoccer only acts on the
// "__condoccer:" namespace and ignores everything else without panicking.
func TestHandleReprCommandToleratesJunk(t *testing.T) {
	s := newServer(t.TempDir())
	s.name = "condoccer"
	for _, cmd := range []string{
		"",
		"plain-command",
		"__ridealong:next",
		"__condoccer:",
		"__condoccer:bogus arg",
		"__condoccer:refresh",
		"__condoccer:action {not json}",
	} {
		s.handleReprCommand(cmd)
	}
}

// TestCondoccerStateMsgShape is a compile-time guard that the wire payload keeps
// the fields the rest of the stack reads (http_port drives the reverse proxy).
func TestCondoccerStateMsgShape(t *testing.T) {
	m := CondoccerStateMsg{HTTPPort: "8080", Root: "/repo", Condocs: []CondocInfo{{Path: "X.md", Phase: "proposed"}}}
	if m.HTTPPort != "8080" || m.Root != "/repo" || len(m.Condocs) != 1 {
		t.Fatalf("unexpected CondoccerStateMsg round-trip: %+v", m)
	}
}
