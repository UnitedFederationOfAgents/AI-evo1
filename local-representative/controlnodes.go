package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// This file lets a control sequence reach other nodes -- the hosts
// connected to agent-coordinator, each with its own LR and robot
// (condocs/initialRobotImpls/Step3Prompt.md, Revision F):
//
//   - Controls of type "node" (controlTypeNode) choose one of the nodes
//     connected to agent-coordinator. AC tells every LR which those are
//     whenever one comes or goes ("__control:nodes [...]"); the runner offers
//     only them, and a run whose node control names any other doesn't start.
//
//   - The node-capture op has a node's robot take a native capture and save
//     it as a screenshot in the files tab. For this node that's its own
//     robot ("__robot:capture", see ianar/lrcapture.go). For another node it
//     goes through agent-coordinator:
//
//     this LR  --"node-capture" {req, node, name}-->  AC
//     AC       --"__control:node-capture" {req, from, node, name}-->  node's LR
//     node's LR has its robot capture into its own files tab, then
//     node's LR --"node-capture-result" {req, from, node, ...}-->  AC
//     AC       --"__control:node-capture-result" {...}-->  this LR
//
//     and this LR copies the screenshot from the node's files tab into its
//     own, through AC's read-only /host/<node>/* proxy (as sessions.go pulls
//     session files), so a run's screenshots are all in the files tab of the
//     LR that ran it, and the control tab can show them beside the steps.
//     The image never crosses a representable link: its lines are capped
//     well below a screenshot's size.

// controlTypeNode is the ControlParam.Type of a node control.
const controlTypeNode = "node"

const (
	controlCaptureTimeout = 45 * time.Second // a node's robot capturing and saving a screenshot
	controlCopyTimeout    = 2 * time.Minute  // copying another node's screenshot into this files tab
	controlMaxCopy        = 64 << 20         // the most a copied screenshot may be, as for an upload
)

// RobotCaptureMsg mirrors ianar's "robot-capture-result" payload
// (ianar/lrcapture.go).
type RobotCaptureMsg struct {
	Req     string `json:"req"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	SavedAs string `json:"saved_as,omitempty"`
	SavedID string `json:"saved_id,omitempty"`
}

// NodeCaptureRequest asks another node's robot for a capture: sent to AC as
// "node-capture", and by AC to that node as "__control:node-capture" with
// From set to the node asking.
type NodeCaptureRequest struct {
	Req  string `json:"req"`
	From string `json:"from,omitempty"`
	Node string `json:"node"`
	Name string `json:"name,omitempty"`
}

// NodeCaptureResult is the node's answer: sent to AC as
// "node-capture-result", and by AC back to From as
// "__control:node-capture-result". SavedID is the screenshot's id in the
// node's own files tab.
type NodeCaptureResult struct {
	Req     string `json:"req"`
	From    string `json:"from,omitempty"`
	Node    string `json:"node"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	SavedAs string `json:"saved_as,omitempty"`
	SavedID string `json:"saved_id,omitempty"`
}

// ---- The nodes connected to agent-coordinator ----

func (e *controlEngine) getNodes() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.nodes...)
}

// setNodes takes the nodes AC says are connected; nil once this LR isn't.
func (e *controlEngine) setNodes(nodes []string) {
	var clean []string
	seen := map[string]bool{}
	for _, n := range nodes {
		if n = strings.TrimSpace(n); n != "" && !seen[n] {
			seen[n] = true
			clean = append(clean, n)
		}
	}
	sort.Strings(clean)
	e.mu.Lock()
	same := len(clean) == len(e.nodes)
	for i := 0; same && i < len(clean); i++ {
		same = clean[i] == e.nodes[i]
	}
	e.nodes = clean
	e.mu.Unlock()
	if !same {
		e.broadcast()
	}
}

// checkNodeControls checks each node control of a sequence (in vars, as the
// run would use them) names a node connected to agent-coordinator.
func (e *controlEngine) checkNodeControls(cs []ControlParam, vars map[string]string) error {
	nodes := e.getNodes()
	for _, c := range cs {
		if c.Type != controlTypeNode {
			continue
		}
		label := c.Label
		if label == "" {
			label = c.Name
		}
		v := strings.TrimSpace(vars[c.Name])
		switch {
		case len(nodes) == 0:
			return fmt.Errorf("%s: this local-representative isn't connected to agent-coordinator, so there are no nodes to choose from", label)
		case v == "":
			return fmt.Errorf("%s: choose a node (connected to agent-coordinator: %s)", label, strings.Join(nodes, ", "))
		case !containsString(nodes, v):
			return fmt.Errorf("%s: %q isn't connected to agent-coordinator (connected: %s)", label, v, strings.Join(nodes, ", "))
		}
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// handleNodeCommand handles AC's node commands (see handleCommand):
//
//	__control:nodes <json list of node names>
//	__control:node-capture <json NodeCaptureRequest>          (as the node asked)
//	__control:node-capture-result <json NodeCaptureResult>    (as the node asking)
func (e *controlEngine) handleNodeCommand(verb, arg string) {
	switch verb {
	case "nodes":
		var nodes []string
		if err := json.Unmarshal([]byte(arg), &nodes); err != nil {
			log.Printf("control: bad node list %q: %v", arg, err)
			return
		}
		e.setNodes(nodes)
	case "node-capture":
		var req NodeCaptureRequest
		if err := json.Unmarshal([]byte(arg), &req); err != nil || req.Req == "" {
			log.Printf("control: bad node-capture request %q: %v", arg, err)
			return
		}
		go e.captureForNode(req)
	case "node-capture-result":
		var res NodeCaptureResult
		if err := json.Unmarshal([]byte(arg), &res); err != nil {
			log.Printf("control: bad node-capture result %q: %v", arg, err)
			return
		}
		e.mu.Lock()
		ch := e.nodeCaps[res.Req]
		e.mu.Unlock()
		if ch != nil {
			select {
			case ch <- res:
			default: // nobody waiting any more
			}
		}
	}
}

// captureForNode has this node's robot take the capture another node asked
// for, and answers it through AC.
func (e *controlEngine) captureForNode(req NodeCaptureRequest) {
	log.Printf("control: %q asked through agent-coordinator for a screenshot of this node (%s)", req.From, req.Req)
	out := NodeCaptureResult{Req: req.Req, From: req.From, Node: e.s.lrName}
	res, err := e.captureHere(req.Name, controlCaptureTimeout, nil)
	switch {
	case err != nil:
		out.Error = err.Error()
	case !res.Success:
		out.Error = res.Error
	default:
		out.Success, out.Width, out.Height, out.SavedAs, out.SavedID = true, res.Width, res.Height, res.SavedAs, res.SavedID
	}
	if ac := e.s.getACClient(); ac != nil {
		ac.SendData("node-capture-result", out)
	}
}

// ---- Captures ----

// captureHere has this node's robot capture the screen into this files tab.
// cancel may be nil.
func (e *controlEngine) captureHere(name string, timeout time.Duration, cancel <-chan struct{}) (RobotCaptureMsg, error) {
	s := e.s
	if s.reprServer == nil || !s.reprServer.IsHealthy("robot") {
		return RobotCaptureMsg{}, fmt.Errorf("the robot (ianar) isn't connected to %s's local-representative -- launch it from its system tab", s.lrName)
	}
	req := randomToken(8)
	b, err := json.Marshal(struct {
		Req  string `json:"req"`
		Name string `json:"name,omitempty"`
	}{req, name})
	if err != nil {
		return RobotCaptureMsg{}, err
	}
	ch := make(chan RobotCaptureMsg, 1)
	e.mu.Lock()
	e.caps[req] = ch
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.caps, req)
		e.mu.Unlock()
	}()
	s.reprServer.SendCommand("robot", "__robot:capture "+string(b))
	select {
	case res := <-ch:
		return res, nil
	case <-time.After(timeout):
		return RobotCaptureMsg{}, fmt.Errorf("%s's robot didn't answer within %s", s.lrName, timeout)
	case <-cancel:
		return RobotCaptureMsg{}, errControlCancelled
	}
}

// noteRobotCapture delivers the robot's "robot-capture-result" to the
// capture waiting on it.
func (e *controlEngine) noteRobotCapture(msg RobotCaptureMsg) {
	e.mu.Lock()
	ch := e.caps[msg.Req]
	e.mu.Unlock()
	if ch != nil {
		select {
		case ch <- msg:
		default:
		}
	}
}

// captureOn asks node's robot, through AC, to capture its screen into its
// own files tab.
func (e *controlEngine) captureOn(node, name string, timeout time.Duration, cancel <-chan struct{}) (NodeCaptureResult, error) {
	ac := e.s.getACClient()
	if ac == nil {
		return NodeCaptureResult{}, fmt.Errorf("this local-representative isn't connected to agent-coordinator, so it can't reach %s", node)
	}
	req := randomToken(8)
	ch := make(chan NodeCaptureResult, 1)
	e.mu.Lock()
	e.nodeCaps[req] = ch
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.nodeCaps, req)
		e.mu.Unlock()
	}()
	ac.SendData("node-capture", NodeCaptureRequest{Req: req, Node: node, Name: name})
	select {
	case res := <-ch:
		return res, nil
	case <-time.After(timeout):
		return NodeCaptureResult{}, fmt.Errorf("%s didn't answer through agent-coordinator within %s", node, timeout)
	case <-cancel:
		return NodeCaptureResult{}, errControlCancelled
	}
}

// copyNodeFile copies file id from node's files tab into this one, through
// agent-coordinator's /host/<node>/* proxy, under name.
func (s *Server) copyNodeFile(node, id, name string) (FileInfo, error) {
	addr, ok := s.acHTTPAddr()
	if !ok {
		return FileInfo{}, errors.New("this local-representative isn't connected to agent-coordinator")
	}
	target := "http://" + addr + "/host/" + url.PathEscape(node) + "/api/files/" + url.PathEscape(id)
	resp, err := s.httpGetWithTimeout(target, controlCopyTimeout)
	if err != nil {
		return FileInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return FileInfo{}, fmt.Errorf("agent-coordinator answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	info, err := s.saveFileFrom(name, io.LimitReader(resp.Body, controlMaxCopy))
	if err != nil {
		return FileInfo{}, err
	}
	s.broadcastFiles()
	return info, nil
}

// opNodeCapture is the node-capture op (see controlops.go).
func opNodeCapture(r *controlRun, a opArgs) (string, error) {
	node := strings.TrimSpace(a["node"])
	if node == "" || strings.Contains(node, "{{") {
		return "", fmt.Errorf("no node given (node is %q)", a["node"])
	}
	timeout, err := a.duration("timeout")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(a["name"])
	if name == "" {
		name = node
	}

	var res NodeCaptureResult
	here := node == r.s.lrName
	if here {
		r.note(fmt.Sprintf("asking %s's robot (this node's) for a screenshot…", node))
		var rc RobotCaptureMsg
		rc, err = r.e.captureHere(name, timeout, r.cancel)
		res = NodeCaptureResult{Node: node, Success: rc.Success, Error: rc.Error, Width: rc.Width, Height: rc.Height, SavedAs: rc.SavedAs, SavedID: rc.SavedID}
	} else {
		r.note(fmt.Sprintf("asking %s's robot for a screenshot through agent-coordinator…", node))
		res, err = r.e.captureOn(node, name, timeout, r.cancel)
	}
	if err != nil {
		return "", err
	}
	if !res.Success {
		msg := res.Error
		if msg == "" {
			msg = "the capture failed"
		}
		return "", fmt.Errorf("%s's robot: %s", node, msg)
	}

	fileID, saved := res.SavedID, res.SavedAs
	if !here {
		r.note(fmt.Sprintf("%s saved %s; copying it into this node's files tab…", node, res.SavedAs))
		info, err := r.s.copyNodeFile(node, res.SavedID, res.SavedAs)
		if err != nil {
			return "", fmt.Errorf("%s saved %s in its own files tab, but copying it here failed: %v", node, res.SavedAs, err)
		}
		fileID, saved = info.ID, info.Name
	}
	r.e.addValue("screenshot of "+node, saved+" (files tab)")
	r.e.addRecording(ControlRecording{Who: node, FileID: fileID, Name: saved, Image: true})
	size := ""
	if res.Width > 0 {
		size = fmt.Sprintf("%dx%d ", res.Width, res.Height)
	}
	return fmt.Sprintf("%s: saved a %sscreenshot as %s", node, size, saved), nil
}
