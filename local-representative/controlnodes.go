package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
//
//   - The node-wait-connected op waits for a node to (re)connect to
//     agent-coordinator, from the same "__control:nodes" lists; setNodes
//     counts each node's connections so "fresh" can tell a new one from a
//     connection that never dropped.
//
//   - The node-fetch-file op copies files from a node into the files tab,
//     the way node-capture copies a screenshot ("node-fetch" and
//     "node-fetch-result", relayed by AC as "__control:node-fetch" and
//     "__control:node-fetch-result"): the node saves the files a path or
//     glob matches into its own files tab, and this LR copies them across.
//     A node hands over any file by default (control-fetch-allow "*"); a
//     control-fetch-allow list of paths limits it to the files that list
//     allows, whoever asks -- this node included.

// controlTypeNode is the ControlParam.Type of a node control.
const controlTypeNode = "node"

const (
	controlCaptureTimeout  = 45 * time.Second // a node's robot capturing and saving a screenshot
	controlCopyTimeout     = 2 * time.Minute  // copying another node's screenshot into this files tab
	controlMaxCopy         = 64 << 20         // the most a copied screenshot may be, as for an upload
	controlNodeWaitTimeout = 15 * time.Minute // a node (re)connecting to agent-coordinator
	controlFetchTimeout    = 1 * time.Minute  // a node saving the files asked for
	controlFetchMaxFiles   = 100              // the most files one node-fetch-file may bring back
	controlRobotRetry      = 5 * time.Second  // between asks while a node's robot isn't running (wait_robot)
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
// node's own files tab. NoRobot says it failed because the node's robot
// isn't running, which wait_robot waits out.
type NodeCaptureResult struct {
	Req     string `json:"req"`
	From    string `json:"from,omitempty"`
	Node    string `json:"node"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	NoRobot bool   `json:"no_robot,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	SavedAs string `json:"saved_as,omitempty"`
	SavedID string `json:"saved_id,omitempty"`
}

// NodeFetchRequest asks a node for files: sent to AC as "node-fetch", and
// by AC to that node as "__control:node-fetch" with From set to the node
// asking.
type NodeFetchRequest struct {
	Req      string `json:"req"`
	From     string `json:"from,omitempty"`
	Node     string `json:"node"`
	Path     string `json:"path"`
	MaxFiles int    `json:"max_files"`
	MaxSize  int64  `json:"max_size"`
}

// NodeFetchResult is the node's answer: sent to AC as "node-fetch-result",
// and by AC back to From as "__control:node-fetch-result". Success means
// the request was carried out, even if nothing matched (Files empty).
type NodeFetchResult struct {
	Req     string            `json:"req"`
	From    string            `json:"from,omitempty"`
	Node    string            `json:"node"`
	Success bool              `json:"success"`
	Error   string            `json:"error,omitempty"`
	Files   []NodeFetchedFile `json:"files,omitempty"`
	Skipped []string          `json:"skipped,omitempty"` // "<path>: why", for matches not handed over
}

// NodeFetchedFile is one file a node saved into its files tab for a fetch.
// Truncated means only its last max_size bytes were kept.
type NodeFetchedFile struct {
	Path      string `json:"path"`
	SavedAs   string `json:"saved_as"`
	SavedID   string `json:"saved_id"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated,omitempty"`
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
	for _, n := range clean {
		if !containsString(e.nodes, n) {
			e.nodeConnects[n]++
		}
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
//	__control:node-fetch <json NodeFetchRequest>              (as the node asked)
//	__control:node-fetch-result <json NodeFetchResult>        (as the node asking)
//	__control:node-record <json NodeRecordRequest>            (as the node asked)
//	__control:node-record-result <json NodeRecordResult>      (as the node asking)
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
	case "node-fetch":
		var req NodeFetchRequest
		if err := json.Unmarshal([]byte(arg), &req); err != nil || req.Req == "" {
			log.Printf("control: bad node-fetch request %q: %v", arg, err)
			return
		}
		go e.fetchForNode(req)
	case "node-fetch-result":
		var res NodeFetchResult
		if err := json.Unmarshal([]byte(arg), &res); err != nil {
			log.Printf("control: bad node-fetch result %q: %v", arg, err)
			return
		}
		e.mu.Lock()
		ch := e.nodeFetches[res.Req]
		e.mu.Unlock()
		if ch != nil {
			select {
			case ch <- res:
			default:
			}
		}
	case "node-record":
		var req NodeRecordRequest
		if err := json.Unmarshal([]byte(arg), &req); err != nil || req.Req == "" || req.Rec == "" {
			log.Printf("control: bad node-record request %q: %v", arg, err)
			return
		}
		go e.recordForNode(req) // see controlrecord.go
	case "node-record-result":
		var res NodeRecordResult
		if err := json.Unmarshal([]byte(arg), &res); err != nil {
			log.Printf("control: bad node-record result %q: %v", arg, err)
			return
		}
		e.noteNodeRecord(res)
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
		out.NoRobot = !e.s.robotUp()
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
	if !s.robotUp() {
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
	return s.copyNodeFileLimit(node, id, name, controlMaxCopy, controlCopyTimeout)
}

// copyNodeFileLimit is copyNodeFile, keeping up to limit bytes and taking
// up to timeout -- larger and longer for a recording (controlrecord.go).
func (s *Server) copyNodeFileLimit(node, id, name string, limit int64, timeout time.Duration) (FileInfo, error) {
	addr, ok := s.acHTTPAddr()
	if !ok {
		return FileInfo{}, errors.New("this local-representative isn't connected to agent-coordinator")
	}
	target := "http://" + addr + "/host/" + url.PathEscape(node) + "/api/files/" + url.PathEscape(id)
	resp, err := s.httpGetWithTimeout(target, timeout)
	if err != nil {
		return FileInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return FileInfo{}, fmt.Errorf("agent-coordinator answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	info, err := s.saveFileFrom(name, io.LimitReader(resp.Body, limit))
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
	var waitRobot time.Duration
	if strings.TrimSpace(a["wait_robot"]) != "" {
		if waitRobot, err = a.duration("wait_robot"); err != nil {
			return "", err
		}
	}

	var res NodeCaptureResult
	here := node == r.s.lrName
	robotBy := time.Now().Add(waitRobot)
	for {
		if here {
			r.note(fmt.Sprintf("asking %s's robot (this node's) for a screenshot…", node))
			var rc RobotCaptureMsg
			rc, err = r.e.captureHere(name, timeout, r.cancel)
			res = NodeCaptureResult{Node: node, Success: rc.Success, Error: rc.Error, NoRobot: err != nil && !r.s.robotUp(), Width: rc.Width, Height: rc.Height, SavedAs: rc.SavedAs, SavedID: rc.SavedID}
		} else {
			r.note(fmt.Sprintf("asking %s's robot for a screenshot through agent-coordinator…", node))
			res, err = r.e.captureOn(node, name, timeout, r.cancel)
		}
		if errors.Is(err, errControlCancelled) || !res.NoRobot || time.Now().Add(controlRobotRetry).After(robotBy) {
			break
		}
		r.note(fmt.Sprintf("%s's robot isn't running yet; asking again until %s…", node, robotBy.Format("15:04:05")))
		select {
		case <-r.cancel:
			return "", errControlCancelled
		case <-time.After(controlRobotRetry):
		}
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

// robotUp reports whether this node's robot is connected to its LR.
func (s *Server) robotUp() bool {
	return s.reprServer != nil && s.reprServer.IsHealthy("robot")
}

// ---- Waiting for a node ----

// nodeMatches reports whether name is node or, with prefix, node-<suffix>
// (ufahostid names a node <hostname>-<4 characters>, and a rebuilt node
// draws new ones).
func nodeMatches(name, node string, prefix bool) bool {
	return name == node || prefix && strings.HasPrefix(name, node+"-")
}

// nodeConnections returns how many times this LR has seen each node matching
// node (see nodeMatches) connect, and which of them are connected now,
// sorted.
func (e *controlEngine) nodeConnections(node string, prefix bool) (map[string]int, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	counts := map[string]int{}
	for name, n := range e.nodeConnects {
		if nodeMatches(name, node, prefix) {
			counts[name] = n
		}
	}
	var up []string
	for _, name := range e.nodes {
		if nodeMatches(name, node, prefix) {
			up = append(up, name)
		}
	}
	return counts, up
}

// opNodeWaitConnected is the node-wait-connected op (see controlops.go).
func opNodeWaitConnected(r *controlRun, a opArgs) (string, error) {
	node := strings.TrimSpace(a["node"])
	if node == "" || strings.Contains(node, "{{") {
		return "", fmt.Errorf("no node given (node is %q)", a["node"])
	}
	fresh, err := a.flag("fresh")
	if err != nil {
		return "", err
	}
	prefix, err := a.flag("prefix")
	if err != nil {
		return "", err
	}
	timeout, err := a.duration("timeout")
	if err != nil {
		return "", err
	}
	if r.s.getACClient() == nil {
		return "", fmt.Errorf("this local-representative isn't connected to agent-coordinator, so it can't see %s", node)
	}
	what := node
	if prefix {
		what = fmt.Sprintf("%s (or %s-…)", node, node)
	}
	before, _ := r.e.nodeConnections(node, prefix)
	if fresh {
		r.note(fmt.Sprintf("waiting for %s to connect anew…", what))
	} else {
		r.note(fmt.Sprintf("waiting for %s to be connected…", what))
	}
	start := time.Now()
	var found string
	ok, err := r.waitFor(timeout, func() bool {
		counts, up := r.e.nodeConnections(node, prefix)
		for _, name := range up {
			if !fresh || counts[name] > before[name] {
				found = name
				return true
			}
		}
		return false
	})
	if err != nil {
		return "", err
	}
	if !ok {
		if _, up := r.e.nodeConnections(node, prefix); len(up) > 0 && fresh {
			return "", fmt.Errorf("%s stayed connected but nothing connected anew as %s within %s", strings.Join(up, ", "), what, timeout)
		}
		return "", fmt.Errorf("%s didn't connect to agent-coordinator within %s", what, timeout)
	}
	if saveAs := strings.TrimSpace(a["save_as"]); saveAs != "" {
		if err := r.save(saveAs, found); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%s is connected (after %s)", found, time.Since(start).Round(time.Second)), nil
}

// ---- Fetching files ----

// opNodeFetchFile is the node-fetch-file op (see controlops.go).
func opNodeFetchFile(r *controlRun, a opArgs) (string, error) {
	node := strings.TrimSpace(a["node"])
	if node == "" || strings.Contains(node, "{{") {
		return "", fmt.Errorf("no node given (node is %q)", a["node"])
	}
	p := strings.TrimSpace(a["path"])
	if p == "" {
		return "", errors.New("no path given")
	}
	maxFiles, err := strconv.Atoi(strings.TrimSpace(a["max_files"]))
	if err != nil || maxFiles < 1 || maxFiles > controlFetchMaxFiles {
		return "", fmt.Errorf("max_files %q should be a number from 1 to %d", a["max_files"], controlFetchMaxFiles)
	}
	maxSize, err := strconv.ParseInt(strings.TrimSpace(a["max_size"]), 10, 64)
	if err != nil || maxSize < 1 || maxSize > controlMaxCopy {
		return "", fmt.Errorf("max_size %q should be a number of bytes from 1 to %d", a["max_size"], controlMaxCopy)
	}
	optional, err := a.flag("optional")
	if err != nil {
		return "", err
	}
	timeout, err := a.duration("timeout")
	if err != nil {
		return "", err
	}
	req := NodeFetchRequest{Node: node, Path: p, MaxFiles: maxFiles, MaxSize: maxSize}

	var res NodeFetchResult
	here := node == r.s.lrName
	if here {
		r.note(fmt.Sprintf("fetching %s from this node…", p))
		res = r.e.fetchHere(req)
	} else {
		r.note(fmt.Sprintf("asking %s for %s through agent-coordinator…", node, p))
		if res, err = r.e.fetchOn(req, timeout, r.cancel); err != nil {
			return "", err
		}
	}
	if !res.Success {
		msg := res.Error
		if msg == "" {
			msg = "the fetch failed"
		}
		return "", fmt.Errorf("%s: %s", node, msg)
	}
	skipped := append([]string(nil), res.Skipped...)
	var saved []string
	for _, f := range res.Files {
		id, name := f.SavedID, f.SavedAs
		if !here {
			info, err := r.s.copyNodeFile(node, f.SavedID, f.SavedAs)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("%s: saved in %s's files tab as %s, but copying it here failed: %v", f.Path, node, f.SavedAs, err))
				continue
			}
			id, name = info.ID, info.Name
		}
		label := name
		if f.Truncated {
			label += fmt.Sprintf(" (last %d bytes)", f.Size)
		}
		r.e.addValue("file from "+node, f.Path+" → "+label+" (files tab)")
		r.e.addRecording(ControlRecording{Who: node, FileID: id, Name: name, File: true, Path: f.Path})
		saved = append(saved, name)
	}
	if len(saved) == 0 && !optional {
		if len(skipped) > 0 {
			return "", fmt.Errorf("%s: nothing fetched for %s: %s", node, p, strings.Join(skipped, "; "))
		}
		return "", fmt.Errorf("%s: nothing matches %s", node, p)
	}
	note := fmt.Sprintf("%s: fetched %d file(s) for %s", node, len(saved), p)
	if len(saved) == 0 {
		note = fmt.Sprintf("%s: nothing fetched for %s (optional)", node, p)
	}
	if len(skipped) > 0 {
		note += "; skipped " + strings.Join(skipped, "; ")
	}
	return note, nil
}

// fetchOn asks node, through AC, to save the files req names into its own
// files tab.
func (e *controlEngine) fetchOn(req NodeFetchRequest, timeout time.Duration, cancel <-chan struct{}) (NodeFetchResult, error) {
	ac := e.s.getACClient()
	if ac == nil {
		return NodeFetchResult{}, fmt.Errorf("this local-representative isn't connected to agent-coordinator, so it can't reach %s", req.Node)
	}
	req.Req = randomToken(8)
	ch := make(chan NodeFetchResult, 1)
	e.mu.Lock()
	e.nodeFetches[req.Req] = ch
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.nodeFetches, req.Req)
		e.mu.Unlock()
	}()
	ac.SendData("node-fetch", req)
	select {
	case res := <-ch:
		return res, nil
	case <-time.After(timeout):
		return NodeFetchResult{}, fmt.Errorf("%s didn't answer through agent-coordinator within %s", req.Node, timeout)
	case <-cancel:
		return NodeFetchResult{}, errControlCancelled
	}
}

// fetchForNode saves the files another node asked for, and answers it
// through AC.
func (e *controlEngine) fetchForNode(req NodeFetchRequest) {
	log.Printf("control: %q asked through agent-coordinator for %s from this node (%s)", req.From, req.Path, req.Req)
	res := e.fetchHere(req)
	res.Req, res.From = req.Req, req.From
	if ac := e.s.getACClient(); ac != nil {
		ac.SendData("node-fetch-result", res)
	}
}

// fetchHere saves the files req.Path matches on this node into its files
// tab: any file, unless control-fetch-allow is a list that doesn't allow it.
func (e *controlEngine) fetchHere(req NodeFetchRequest) NodeFetchResult {
	out := NodeFetchResult{Node: e.s.lrName}
	allow := e.getFetchAllow()
	pattern, err := expandHome(req.Path)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if !filepath.IsAbs(pattern) {
		out.Error = fmt.Sprintf("%q isn't an absolute path", req.Path)
		return out
	}
	matches, err := filepath.Glob(filepath.Clean(pattern))
	if err != nil {
		out.Error = fmt.Sprintf("bad glob %q: %v", req.Path, err)
		return out
	}
	sort.Strings(matches)
	maxFiles, maxSize := req.MaxFiles, req.MaxSize
	if maxFiles < 1 || maxFiles > controlFetchMaxFiles {
		maxFiles = controlFetchMaxFiles
	}
	if maxSize < 1 || maxSize > controlMaxCopy {
		maxSize = controlMaxCopy
	}
	out.Success = true
	for i, m := range matches {
		if len(out.Files) == maxFiles {
			out.Skipped = append(out.Skipped, fmt.Sprintf("%d more match(es): max_files is %d", len(matches)-i, maxFiles))
			break
		}
		f, err := e.fetchOne(m, allow, maxSize)
		if err != nil {
			if !errors.Is(err, errNotAFile) {
				out.Skipped = append(out.Skipped, m+": "+err.Error())
			}
			continue
		}
		out.Files = append(out.Files, f)
	}
	if len(out.Files) > 0 {
		e.s.broadcastFiles()
	}
	return out
}

// errNotAFile marks a glob match that isn't a regular file (a directory,
// say), passed over without comment.
var errNotAFile = errors.New("not a regular file")

// fetchOne saves file p into this node's files tab, keeping only its last
// maxSize bytes. With an allow list, p and the file it resolves to must
// both be on it; an empty one allows everything.
func (e *controlEngine) fetchOne(p string, allow []string, maxSize int64) (NodeFetchedFile, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return NodeFetchedFile{}, err
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return NodeFetchedFile{}, err
	}
	if !st.Mode().IsRegular() {
		return NodeFetchedFile{}, errNotAFile
	}
	if len(allow) > 0 && (!fetchAllowed(p, allow) || !fetchAllowed(resolved, allow)) {
		msg := "not allowed by control-fetch-allow"
		if resolved != p {
			msg += " (it resolves to " + resolved + ")"
		}
		msg += ": this node's setting is " + strings.Join(allow, ", ")
		if from := e.getFetchAllowFrom(); from != "" {
			msg += ", from " + from
		}
		return NodeFetchedFile{}, errors.New(msg + `; remove it, or set it to "*", to hand over any file`)
	}
	f, err := os.Open(resolved)
	if err != nil {
		return NodeFetchedFile{}, err
	}
	defer f.Close()
	out := NodeFetchedFile{Path: p, Size: st.Size()}
	if st.Size() > maxSize {
		if _, err := f.Seek(st.Size()-maxSize, io.SeekStart); err != nil {
			return NodeFetchedFile{}, err
		}
		out.Truncated, out.Size = true, maxSize
	}
	name := e.s.lrName + "_" + strings.ReplaceAll(strings.TrimPrefix(p, "/"), "/", "_")
	info, err := e.s.saveFileFrom(name, io.LimitReader(f, maxSize))
	if err != nil {
		return NodeFetchedFile{}, err
	}
	out.SavedAs, out.SavedID = info.Name, info.ID
	return out, nil
}

func (e *controlEngine) getFetchAllow() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.fetchAllow
}

func (e *controlEngine) getFetchAllowFrom() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.fetchAllowFrom
}

// fetchAllowAll is the control-fetch-allow entry, and its default, that
// hands over any file.
const fetchAllowAll = "*"

// setFetchAllow takes the control-fetch-allow setting: the paths this node
// hands over to node-fetch-file. "*" (the default) or none hands over any
// file. Otherwise each entry is an absolute path or glob (~ for the home
// directory); one ending in "/" allows everything under that directory.
func (e *controlEngine) setFetchAllow(entries []string) error {
	var clean []string
	all := false
	for _, entry := range entries {
		if entry == fetchAllowAll {
			all = true
			continue
		}
		dir := strings.HasSuffix(entry, "/")
		p, err := expandHome(entry)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(p) {
			return fmt.Errorf("control-fetch-allow: %q isn't an absolute path", entry)
		}
		p = filepath.Clean(p)
		if _, err := filepath.Match(p, ""); err != nil {
			return fmt.Errorf("control-fetch-allow: bad glob %q: %v", entry, err)
		}
		if dir && p != "/" {
			p += "/"
		}
		clean = append(clean, p)
	}
	if all {
		clean = nil
	}
	e.mu.Lock()
	e.fetchAllow = clean
	e.mu.Unlock()
	return nil
}

// fetchAllowed reports whether path p (absolute, clean) is allowed by one
// of allow's entries (see setFetchAllow).
func fetchAllowed(p string, allow []string) bool {
	p = filepath.Clean(p)
	for _, a := range allow {
		if strings.HasSuffix(a, "/") {
			if strings.HasPrefix(p, a) {
				return true
			}
			continue
		}
		if ok, _ := filepath.Match(a, p); ok {
			return true
		}
	}
	return false
}

// expandHome replaces a leading ~ with the home directory.
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("can't expand ~ in %q: %v", p, err)
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}
