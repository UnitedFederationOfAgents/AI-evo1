package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestNodesFromAgentCoordinator: "__control:nodes" sets the nodes the
// control state offers, sorted and without repeats.
func TestNodesFromAgentCoordinator(t *testing.T) {
	s := newServer("test-lr")
	s.control.handleCommand(`__control:nodes ["lr-b","test-lr","lr-b"]`)
	if got, want := s.control.state().Nodes, []string{"lr-b", "test-lr"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nodes = %v, want %v", got, want)
	}
	s.control.setNodes(nil)
	if got := s.control.state().Nodes; len(got) != 0 {
		t.Errorf("nodes after agent-coordinator went = %v", got)
	}
}

// TestCheckNodeControls: a node control must name a node connected to
// agent-coordinator.
func TestCheckNodeControls(t *testing.T) {
	s := newServer("test-lr")
	e := s.control
	cs := []ControlParam{{Name: "plain"}, {Name: "n", Label: "First node", Type: controlTypeNode}}

	err := e.checkNodeControls(cs, map[string]string{"n": "lr-b"})
	if err == nil || !strings.Contains(err.Error(), "isn't connected to agent-coordinator") {
		t.Errorf("without agent-coordinator: err = %v", err)
	}
	e.setNodes([]string{"lr-b", "test-lr"})
	for _, tc := range []struct {
		value string
		ok    bool
	}{{"lr-b", true}, {"test-lr", true}, {"", false}, {"lr-gone", false}} {
		err := e.checkNodeControls(cs, map[string]string{"plain": "anything", "n": tc.value})
		if (err == nil) != tc.ok {
			t.Errorf("node %q: err = %v, want ok=%v", tc.value, err, tc.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "First node") {
			t.Errorf("node %q: the error doesn't name the control: %v", tc.value, err)
		}
	}
}

// TestCaptureTwoNodesNeedsConnectedNodes: Revision F's example won't start
// on nodes that aren't connected.
func TestCaptureTwoNodesNeedsConnectedNodes(t *testing.T) {
	s := newServer("test-lr")
	if err := s.control.start("capture-two-nodes", map[string]string{"first_node": "lr-a", "second_node": "lr-b"}); err == nil {
		t.Fatal("started with no nodes connected")
	}
	s.control.setNodes([]string{"lr-a", "test-lr"})
	err := s.control.start("capture-two-nodes", map[string]string{"first_node": "lr-a", "second_node": "lr-b"})
	if err == nil || !strings.Contains(err.Error(), `"lr-b"`) {
		t.Fatalf("err = %v, want lr-b isn't connected", err)
	}
	if st := s.control.state(); st.Run != nil {
		t.Errorf("a run started: %+v", st.Run)
	}
}

func TestCaptureTwoNodesExample(t *testing.T) {
	q, _, err := newControlLibrary().compile("capture-two-nodes", map[string]string{"first_node": "lr-a", "second_node": "lr-b"}, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(q.steps) != 2 || len(q.record) != 0 {
		t.Fatalf("steps %+v, record %v", q.steps, q.record)
	}
	if d := q.steps[0].Do; len(d) != 1 || d[0] != "screenshot lr-a with its robot" {
		t.Errorf("step 1 does %q", d)
	}
	if d := q.steps[1].Do; len(d) != 1 || d[0] != "screenshot lr-b with its robot" {
		t.Errorf("step 2 does %q", d)
	}
	nodeControls := 0
	for _, c := range q.controls {
		if c.Type == controlTypeNode {
			nodeControls++
		}
	}
	if nodeControls != 2 {
		t.Errorf("%d node controls, want 2", nodeControls)
	}
}

// TestNodeCaptureNeedsAgentCoordinator: another node's robot is only
// reachable through agent-coordinator; this node's needs its own robot.
func TestNodeCaptureNeedsAgentCoordinator(t *testing.T) {
	s := newServer("test-lr")
	r := runningEngine(s)
	_, err := opNodeCapture(r, opArgs{"node": "lr-b", "timeout": "1s"})
	if err == nil || !strings.Contains(err.Error(), "agent-coordinator") {
		t.Errorf("another node: err = %v", err)
	}
	_, err = opNodeCapture(r, opArgs{"node": "test-lr", "timeout": "1s"})
	if err == nil || !strings.Contains(err.Error(), "robot") {
		t.Errorf("this node: err = %v", err)
	}
	if _, err = opNodeCapture(r, opArgs{"node": "{{first_node}}", "timeout": "1s"}); err == nil {
		t.Error("ran with no node")
	}
}

func TestNodeCaptureResultsDelivered(t *testing.T) {
	s := newServer("test-lr")
	e := s.control
	caps := make(chan RobotCaptureMsg, 1)
	nodeCaps := make(chan NodeCaptureResult, 1)
	e.mu.Lock()
	e.caps["c1"] = caps
	e.nodeCaps["n1"] = nodeCaps
	e.mu.Unlock()

	e.noteRobotCapture(RobotCaptureMsg{Req: "other", Success: true})
	e.noteRobotCapture(RobotCaptureMsg{Req: "c1", Success: true, SavedAs: "x.png"})
	e.noteRobotCapture(RobotCaptureMsg{Req: "c1"}) // a duplicate doesn't block
	if got := <-caps; got.SavedAs != "x.png" {
		t.Errorf("robot capture delivered %+v", got)
	}

	e.handleCommand(`__control:node-capture-result {"req":"n1","node":"lr-b","success":true,"saved_as":"y.png","saved_id":"ab_y.png"}`)
	e.handleCommand(`__control:node-capture-result {"req":"n1","node":"lr-b"}`)
	if got := <-nodeCaps; !got.Success || got.Node != "lr-b" || got.SavedID != "ab_y.png" {
		t.Errorf("node capture delivered %+v", got)
	}
}

func TestScreenshotsInState(t *testing.T) {
	s := newServer("test-lr")
	runningEngine(s)
	s.control.addRecording(ControlRecording{Who: "lr-b", FileID: "ab_y.png", Name: "y.png", Image: true})
	recs := s.control.state().Run.Recordings
	if len(recs) != 1 || !recs[0].Image || recs[0].Video || recs[0].Who != "lr-b" {
		t.Errorf("recordings = %+v", recs)
	}
}
