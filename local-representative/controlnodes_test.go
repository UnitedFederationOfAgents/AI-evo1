package main

import (
	"os"
	"path/filepath"
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
	if err := s.control.start("capture-two-nodes", map[string]string{"first_node": "lr-a", "second_node": "lr-b"}, ""); err == nil {
		t.Fatal("started with no nodes connected")
	}
	s.control.setNodes([]string{"lr-a", "test-lr"})
	err := s.control.start("capture-two-nodes", map[string]string{"first_node": "lr-a", "second_node": "lr-b"}, "")
	if err == nil || !strings.Contains(err.Error(), `"lr-b"`) {
		t.Fatalf("err = %v, want lr-b isn't connected", err)
	}
	if st := s.control.state(); st.Run != nil {
		t.Errorf("a run started: %+v", st.Run)
	}
}

func TestCaptureTwoNodesExample(t *testing.T) {
	q, _, err := newControlLibrary().compile("capture-two-nodes", map[string]string{"first_node": "lr-a", "second_node": "lr-b"}, nil, time.Now(), "lr-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.steps) != 2 || len(q.records) != 0 {
		t.Fatalf("steps %+v, records %v", q.steps, q.records)
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

// TestNodeWaitConnectedFresh: fresh only counts a connection made since
// the step started; otherwise being connected is enough.
func TestNodeWaitConnectedFresh(t *testing.T) {
	s := newServer("test-lr")
	e := s.control
	e.setNodes([]string{"lr-b", "test-lr"})
	before, _ := e.nodeConnections("lr-b", false)
	// lr-b reboots and comes back.
	e.setNodes([]string{"test-lr"})
	e.setNodes([]string{"lr-b", "test-lr"})
	if counts, up := e.nodeConnections("lr-b", false); len(up) != 1 || counts["lr-b"] != before["lr-b"]+1 {
		t.Errorf("after reconnecting: up=%v connects=%v, before %v", up, counts, before)
	}
	// The same list again isn't a connection.
	e.setNodes([]string{"lr-b", "test-lr"})
	if counts, _ := e.nodeConnections("lr-b", false); counts["lr-b"] != before["lr-b"]+1 {
		t.Errorf("a repeated list counted as a connection: %v", counts)
	}
	// Rebuilt, it comes back under a new suffix: only prefix matches it.
	e.setNodes([]string{"lr-b-x7k2", "test-lr"})
	if _, up := e.nodeConnections("lr-b", false); len(up) != 0 {
		t.Errorf("lr-b-x7k2 matched lr-b without prefix: %v", up)
	}
	if counts, up := e.nodeConnections("lr-b", true); len(up) != 1 || up[0] != "lr-b-x7k2" || counts["lr-b-x7k2"] != 1 {
		t.Errorf("with prefix: up=%v connects=%v", up, counts)
	}
	if nodeMatches("lr-bb", "lr-b", true) {
		t.Error("lr-bb matched lr-b")
	}

	r := runningEngine(s)
	if _, err := opNodeWaitConnected(r, opArgs{"node": "lr-b", "fresh": "false", "timeout": "1s"}); err == nil || !strings.Contains(err.Error(), "agent-coordinator") {
		t.Errorf("without agent-coordinator: err = %v", err)
	}
	if _, err := opNodeWaitConnected(r, opArgs{"node": "{{node}}", "fresh": "false", "timeout": "1s"}); err == nil {
		t.Error("ran with no node")
	}
}

// TestFetchAllowed: control-fetch-allow entries are globs, or directories
// with a trailing /.
func TestFetchAllowed(t *testing.T) {
	e := newServer("test-lr").control
	if err := e.setFetchAllow([]string{"/var/log/cloud-init*.log", "/var/log/installer/", "~/notes.txt"}); err != nil {
		t.Fatal(err)
	}
	home, _ := expandHome("~")
	allow := e.getFetchAllow()
	allowed := []string{
		"/var/log/cloud-init-output.log",
		"/var/log/cloud-init.log",
		"/var/log/installer/curtin.log",
		"/var/log/installer/sub/x.log",
		home + "/notes.txt",
	}
	refused := []string{
		"/var/log/syslog",
		"/var/log/installer",
		"/var/log/installer/../../shadow",
		home + "/.ssh/id_rsa",
	}
	for _, p := range allowed {
		if !fetchAllowed(p, allow) {
			t.Errorf("%q isn't allowed", p)
		}
	}
	for _, p := range refused {
		if fetchAllowed(p, allow) {
			t.Errorf("%q is allowed", p)
		}
	}
	if err := e.setFetchAllow([]string{"relative/path"}); err == nil {
		t.Error("a relative entry was accepted")
	}
	// "*", the default, hands over any file, even beside other entries.
	if err := e.setFetchAllow([]string{fetchAllowAll}); err != nil || len(e.getFetchAllow()) != 0 {
		t.Errorf("\"*\" should allow any file: %v %v", e.getFetchAllow(), err)
	}
	if err := e.setFetchAllow([]string{"/var/log/", fetchAllowAll}); err != nil || len(e.getFetchAllow()) != 0 {
		t.Errorf("\"*\" beside a path should allow any file: %v %v", e.getFetchAllow(), err)
	}
}

// TestFetchHere: a node hands over any regular file without
// control-fetch-allow, and only allowed ones with it, keeping the end of a
// long one.
func TestFetchHere(t *testing.T) {
	s := newServer("test-lr")
	s.fileCacheDir = t.TempDir()
	src := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.log", "short")
	write("b.log", "0123456789")
	write("secret.txt", "no")
	if err := os.Mkdir(filepath.Join(src, "dir.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, "secret.txt"), filepath.Join(src, "link.log")); err != nil {
		t.Fatal(err)
	}

	req := NodeFetchRequest{Path: filepath.Join(src, "*.log"), MaxFiles: 10, MaxSize: 4}
	if res := s.control.fetchHere(req); !res.Success || len(res.Files) != 3 || len(res.Skipped) != 0 {
		t.Fatalf("without control-fetch-allow, want a.log, b.log and link.log: %+v", res)
	}
	if err := s.control.setFetchAllow([]string{filepath.Join(src, "*.log")}); err != nil {
		t.Fatal(err)
	}
	res := s.control.fetchHere(req)
	if !res.Success || len(res.Files) != 2 {
		t.Fatalf("fetch: %+v", res)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "link.log") {
		t.Errorf("the symlink out of the allowed files wasn't refused: %v", res.Skipped)
	} else if !strings.Contains(res.Skipped[0], "secret.txt") || !strings.Contains(res.Skipped[0], filepath.Join(src, "*.log")) {
		t.Errorf("the refusal doesn't say what the link resolves to and what the setting is: %v", res.Skipped[0])
	}
	b := res.Files[1]
	if !b.Truncated || b.Size != 4 || b.SavedAs != "test-lr_"+strings.ReplaceAll(strings.TrimPrefix(filepath.Join(src, "b.log"), "/"), "/", "_") {
		t.Errorf("b.log: %+v", b)
	}
	got, err := os.ReadFile(filepath.Join(s.fileCacheDir, b.SavedID))
	if err != nil || string(got) != "6789" {
		t.Errorf("b.log kept %q (%v), want its last 4 bytes", got, err)
	}

	req.MaxFiles = 1
	if res := s.control.fetchHere(req); len(res.Files) != 1 || len(res.Skipped) == 0 || !strings.Contains(res.Skipped[len(res.Skipped)-1], "max_files") {
		t.Errorf("max_files 1: %+v", res)
	}
}

// TestNodeFetchFileHere: a run fetching from its own node needs nothing
// from agent-coordinator, and fails on a path matching nothing unless it's
// optional.
func TestNodeFetchFileHere(t *testing.T) {
	s := newServer("test-lr")
	s.fileCacheDir = t.TempDir()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.control.setFetchAllow([]string{src + "/"}); err != nil {
		t.Fatal(err)
	}
	r := runningEngine(s)
	args := func(p, optional string) opArgs {
		return opArgs{"node": "test-lr", "path": p, "max_files": "20", "max_size": "1024", "optional": optional, "timeout": "1s"}
	}
	if _, err := opNodeFetchFile(r, args(filepath.Join(src, "x.txt"), "false")); err != nil {
		t.Fatal(err)
	}
	recs := s.control.state().Run.Recordings
	if len(recs) != 1 || !recs[0].File || recs[0].Path != filepath.Join(src, "x.txt") {
		t.Errorf("recordings = %+v", recs)
	}
	if _, err := opNodeFetchFile(r, args(filepath.Join(src, "none*"), "false")); err == nil {
		t.Error("matching nothing didn't fail")
	}
	if _, err := opNodeFetchFile(r, args(filepath.Join(src, "none*"), "true")); err != nil {
		t.Errorf("optional: %v", err)
	}
	if _, err := opNodeFetchFile(r, opArgs{"node": "lr-b", "path": "/x", "max_files": "20", "max_size": "1024", "optional": "false", "timeout": "1s"}); err == nil || !strings.Contains(err.Error(), "agent-coordinator") {
		t.Errorf("another node without agent-coordinator: err = %v", err)
	}
}

func TestNodeFetchResultsDelivered(t *testing.T) {
	e := newServer("test-lr").control
	ch := make(chan NodeFetchResult, 1)
	e.mu.Lock()
	e.nodeFetches["f1"] = ch
	e.mu.Unlock()
	e.handleCommand(`__control:node-fetch-result {"req":"f1","node":"lr-b","success":true,"files":[{"path":"/a","saved_as":"lr-b_a","saved_id":"ab_lr-b_a","size":1}]}`)
	e.handleCommand(`__control:node-fetch-result {"req":"f1","node":"lr-b"}`)
	if got := <-ch; !got.Success || len(got.Files) != 1 || got.Files[0].SavedID != "ab_lr-b_a" {
		t.Errorf("node fetch delivered %+v", got)
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
