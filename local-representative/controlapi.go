package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file is the control library and runner over HTTP
// (condocs/dTT-IntegrationImpls/Step1Prompt.md, Revision V), for programs
// rather than the control tab -- dtt's localrep provider
// (dtt/declare_localrep.go) imports and runs sequences through it:
//
//	GET    /api/control/sequences              {node, sequences: [ControlSequenceView]}
//	GET    /api/control/sequences/<id>         {node, sequence: ControlSequenceView or null}
//	DELETE /api/control/sequences/<id>         {message}
//	POST   /api/control/import[?check=1]       body: lr-control-v1 YAML -> {message, sequences: [{id, definition}]}
//	POST   /api/control/runs                   {sequence, controls, record_override, key} -> {run}
//	GET    /api/control/runs?key=<key>         {runs: [ControlRunMsg]}, oldest first
//	GET    /api/control/runs/<id>[?wait=<s>]   {run}, waiting up to wait seconds (60 at most) while it runs
//	GET    /api/control/coordinator            CoordinatorView: the agent-coordinator this LR is connected to
//
// A sequence's definition is a fingerprint of it and the actions it uses,
// so a caller can tell whether the library already holds what it would
// import: import?check=1 reports the definitions a document would give,
// without importing it.
//
// Runs are the control tab's: one at a time, shown there as they go. A run
// started here carries the caller's key (ControlRunMsg.Key), and the most
// recent finished runs are kept in memory (controlEngine.history) so a
// caller can find one again after another has started -- not across a
// restart.
//
// ?node=<node> sends any of these to that node's LR instead, through
// agent-coordinator's /host/<node>/* proxy -- the sequence is imported and
// run there, on its robot and screen. A run on another node that has
// finished has its recordings copied into this LR's files tab, as a
// recording block on another node's screen does (controlrecord.go), with
// their file ids rewritten to the copies, so whatever this LR's caller
// does next with them (composer, say) finds them here.

const (
	controlAPIMaxBody   = 1 << 20 // an imported document, or a run request
	controlAPIMaxWait   = 60      // seconds a GET of a run may wait for it to finish
	controlHistoryKept  = 20      // finished runs kept for the API
	controlRelayTimeout = (controlAPIMaxWait + 30) * time.Second
)

// ControlSequenceView is one sequence as the API reports it.
type ControlSequenceView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Controls    []string `json:"controls"` // the controls' names
	Steps       int      `json:"steps"`
	Recordings  int      `json:"recordings"` // its recording blocks
	Definition  string   `json:"definition"`
	Rev         int      `json:"rev,omitempty"`
	Error       string   `json:"error,omitempty"` // why it can't be compiled, if it can't
}

// ctlDefinition fingerprints sequence q with the actions it uses (from
// actions; one missing counts as missing).
func ctlDefinition(q ControlSequenceDef, actions map[string]ControlActionDef) string {
	h := sha256.New()
	io.WriteString(h, ctlSequencePrint(q))
	var ids []string
	seen := map[string]bool{}
	for _, st := range q.Steps {
		if !seen[st.Action] {
			seen[st.Action] = true
			ids = append(ids, st.Action)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if a, ok := actions[id]; ok {
			io.WriteString(h, "\x00"+ctlActionPrint(a))
		} else {
			io.WriteString(h, "\x00missing "+id)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// sequenceViews lists the library's sequences.
func (e *controlEngine) sequenceViews() []ControlSequenceView {
	snap := e.lib.snapshot(e.getRobotOps(), e.s.lrName)
	actions := map[string]ControlActionDef{}
	for _, a := range snap.Actions {
		actions[a.ID] = a
	}
	infos := map[string]ControlSequenceInfo{}
	for _, info := range e.sequenceInfos() {
		infos[info.ID] = info
	}
	out := []ControlSequenceView{}
	for _, q := range snap.Sequences {
		v := ControlSequenceView{ID: q.ID, Name: q.Name, Description: q.Description, Controls: []string{},
			Steps: len(q.Steps), Recordings: len(q.Recordings), Definition: ctlDefinition(q, actions)}
		for _, c := range q.Controls {
			v.Controls = append(v.Controls, c.Name)
		}
		if info, ok := infos[q.ID]; ok {
			v.Rev, v.Error = info.Rev, info.Error
		}
		out = append(out, v)
	}
	return out
}

// importDefinitions are the definitions doc's sequences would have once
// imported: its own actions over the library's. The library doc would
// make (as controlLibrary.merge makes it) is checked as the import checks
// it, so check=1 fails where the import would.
func (e *controlEngine) importDefinitions(doc controlLibDoc) ([]map[string]string, error) {
	snap := e.lib.snapshot(e.getRobotOps(), e.s.lrName)
	merged := append([]ControlActionDef(nil), snap.Actions...)
	for _, a := range doc.Actions {
		if i := indexOfControlAction(merged, a.ID); i >= 0 {
			merged[i] = a
		} else {
			merged = append(merged, a)
		}
	}
	sequences := append([]ControlSequenceDef(nil), snap.Sequences...)
	for _, q := range doc.Sequences {
		if i := indexOfControlSequence(sequences, q.ID); i >= 0 {
			sequences[i] = q
		} else {
			sequences = append(sequences, q)
		}
	}
	if err := checkControlLibrary(merged, sequences); err != nil {
		return nil, err
	}
	actions := map[string]ControlActionDef{}
	for _, a := range merged {
		actions[a.ID] = a
	}
	out := []map[string]string{}
	for _, q := range doc.Sequences {
		out = append(out, map[string]string{"id": q.ID, "definition": ctlDefinition(q, actions)})
	}
	return out, nil
}

// remember keeps a finished run for the API.
func (e *controlEngine) remember(run ControlRunMsg) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = append(e.history, run)
	if n := len(e.history) - controlHistoryKept; n > 0 {
		e.history = append([]ControlRunMsg(nil), e.history[n:]...)
	}
}

// findRuns returns the runs (the current one and those remembered) with
// run id id, or key key, oldest first.
func (e *controlEngine) findRuns(id, key string) []ControlRunMsg {
	match := func(r ControlRunMsg) bool {
		return (id != "" && r.ID == id) || (key != "" && r.Key == key)
	}
	var out []ControlRunMsg
	seen := map[string]bool{}
	e.mu.Lock()
	for _, r := range e.history {
		if match(r) {
			out = append(out, r)
			seen[r.ID] = true
		}
	}
	e.mu.Unlock()
	if cur := e.runCopy(); cur != nil && match(*cur) {
		if seen[cur.ID] {
			// Finished and remembered: the live copy has anything added since
			// (where it was saved, say).
			for i := range out {
				if out[i].ID == cur.ID {
					out[i] = *cur
				}
			}
		} else {
			out = append(out, *cur)
		}
	}
	return out
}

func controlAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func controlAPIError(w http.ResponseWriter, status int, err error) {
	controlAPIJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) handleControlAPI(w http.ResponseWriter, r *http.Request) {
	if s.control == nil {
		controlAPIError(w, http.StatusServiceUnavailable, errors.New("this local-representative has no control engine"))
		return
	}
	if node := strings.TrimSpace(r.URL.Query().Get("node")); node != "" && node != s.lrName {
		s.relayControlAPI(w, r, node)
		return
	}
	e := s.control
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/control/"), "/")
	kind, id, _ := strings.Cut(rest, "/")
	switch {
	case kind == "sequences" && r.Method == http.MethodGet:
		views := e.sequenceViews()
		if id == "" {
			controlAPIJSON(w, http.StatusOK, map[string]any{"node": s.lrName, "sequences": views})
			return
		}
		var found *ControlSequenceView
		for i := range views {
			if views[i].ID == id {
				found = &views[i]
			}
		}
		controlAPIJSON(w, http.StatusOK, map[string]any{"node": s.lrName, "sequence": found})

	case kind == "sequences" && id != "" && r.Method == http.MethodDelete:
		reply := e.handleLibRequest(ControlLibRequest{Op: "delete-sequence", ID: id})
		if !reply.Success {
			controlAPIError(w, http.StatusUnprocessableEntity, errors.New(reply.Error))
			return
		}
		controlAPIJSON(w, http.StatusOK, map[string]any{"message": reply.Message})

	case kind == "import" && id == "" && r.Method == http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, controlAPIMaxBody+1))
		if err != nil || len(body) > controlAPIMaxBody {
			controlAPIError(w, http.StatusBadRequest, errors.New("the document should be lr-control-v1 YAML of at most 1 MiB"))
			return
		}
		src := string(body)
		var wrapped struct {
			YAML string `json:"yaml"`
		}
		if strings.HasPrefix(strings.TrimSpace(src), "{") && json.Unmarshal(body, &wrapped) == nil {
			src = wrapped.YAML
		}
		doc, err := decodeControlLib(src)
		if err != nil {
			controlAPIError(w, http.StatusUnprocessableEntity, err)
			return
		}
		defs, err := e.importDefinitions(doc)
		if err != nil {
			controlAPIError(w, http.StatusUnprocessableEntity, err)
			return
		}
		if r.URL.Query().Get("check") == "1" {
			controlAPIJSON(w, http.StatusOK, map[string]any{"message": "checked, not imported", "sequences": defs})
			return
		}
		reply := e.handleLibRequest(ControlLibRequest{Op: "import", YAML: src})
		if !reply.Success {
			controlAPIError(w, http.StatusUnprocessableEntity, errors.New(reply.Error))
			return
		}
		controlAPIJSON(w, http.StatusOK, map[string]any{"message": reply.Message, "sequences": defs})

	case kind == "runs" && id == "" && r.Method == http.MethodPost:
		var req struct {
			Sequence       string            `json:"sequence"`
			Controls       map[string]string `json:"controls"`
			RecordOverride string            `json:"record_override"`
			Key            string            `json:"key"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, controlAPIMaxBody)).Decode(&req); err != nil {
			controlAPIError(w, http.StatusBadRequest, fmt.Errorf("bad run request: %v", err))
			return
		}
		runID, err := e.startKeyed(req.Sequence, req.Controls, req.RecordOverride, strings.TrimSpace(req.Key))
		if err != nil {
			controlAPIError(w, http.StatusUnprocessableEntity, err)
			return
		}
		run := ControlRunMsg{ID: runID, Sequence: req.Sequence, Key: strings.TrimSpace(req.Key), Status: "running"}
		if runs := e.findRuns(runID, ""); len(runs) > 0 {
			run = runs[len(runs)-1]
		}
		controlAPIJSON(w, http.StatusOK, map[string]any{"run": run})

	case kind == "runs" && id == "" && r.Method == http.MethodGet:
		key := strings.TrimSpace(r.URL.Query().Get("key"))
		if key == "" {
			controlAPIError(w, http.StatusBadRequest, errors.New("give the run's key (?key=...), or GET /api/control/runs/<id>"))
			return
		}
		runs := e.findRuns("", key)
		if runs == nil {
			runs = []ControlRunMsg{}
		}
		controlAPIJSON(w, http.StatusOK, map[string]any{"runs": runs})

	case kind == "runs" && id != "" && r.Method == http.MethodGet:
		wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
		if wait > controlAPIMaxWait {
			wait = controlAPIMaxWait
		}
		deadline := time.Now().Add(time.Duration(wait) * time.Second)
		for {
			runs := e.findRuns(id, "")
			if len(runs) == 0 {
				controlAPIError(w, http.StatusNotFound, fmt.Errorf("no run %s (only the last %d finished runs are kept, and not across a restart)", id, controlHistoryKept))
				return
			}
			run := runs[len(runs)-1]
			if run.Status != "running" || !time.Now().Before(deadline) {
				controlAPIJSON(w, http.StatusOK, map[string]any{"run": run})
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}

	case kind == "coordinator" && id == "" && r.Method == http.MethodGet:
		controlAPIJSON(w, http.StatusOK, s.coordinatorView())

	default:
		http.NotFound(w, r)
	}
}

// ---- agent-coordinator ----

// CoordinatorView is the agent-coordinator this LR is connected to, as
// another node would reach its web UI: URL is http://<host>:<http port>
// (condocs/dTT-IntegrationImpls/Step1Prompt.md, Revision Y -- dtt's
// localrep.coordinator, so a demo can open AC on another node's browser).
// When this LR reached AC on loopback (AC runs here), host is this host's
// LAN address instead, and Note says so.
type CoordinatorView struct {
	Node      string `json:"node"`
	Connected bool   `json:"connected"`
	Host      string `json:"host,omitempty"` // as this LR dialled it
	HTTPPort  string `json:"http_port,omitempty"`
	URL       string `json:"url,omitempty"`
	Note      string `json:"note,omitempty"`
}

func (s *Server) coordinatorView() CoordinatorView {
	v := CoordinatorView{Node: s.lrName, HTTPPort: s.acHTTPPort}
	addr, ok := s.acHTTPAddr()
	if !ok {
		v.Note = "this local-representative isn't connected to agent-coordinator"
		return v
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		v.Note = err.Error()
		return v
	}
	v.Connected, v.Host = true, host
	if isLoopbackHost(host) {
		if ip := lanIPv4(); ip != "" {
			v.Note = fmt.Sprintf("agent-coordinator runs on this host (dialled as %s); other nodes reach it at this host's address, %s", host, ip)
			host = ip
		} else {
			v.Note = fmt.Sprintf("agent-coordinator runs on this host (dialled as %s), and this host has no other address for other nodes to reach it by", host)
		}
	}
	v.URL = "http://" + net.JoinHostPort(host, port)
	return v
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// lanIPv4 is this host's first IPv4 address on an interface that's up and
// not loopback, a container bridge or a virtual pair; "" if it has none.
func lanIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		skip := false
		for _, p := range []string{"docker", "br-", "veth", "virbr", "cni", "flannel", "tailscale"} {
			skip = skip || strings.HasPrefix(ifc.Name, p)
		}
		if skip {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLinkLocalUnicast() {
				return ip4.String()
			}
		}
	}
	return ""
}

// ---- Another node's ----

// relayControlAPI sends r to node's LR through agent-coordinator, and
// answers with its answer -- a finished run's recordings copied here first.
func (s *Server) relayControlAPI(w http.ResponseWriter, r *http.Request, node string) {
	addr, ok := s.acHTTPAddr()
	if !ok {
		controlAPIError(w, http.StatusBadGateway, fmt.Errorf("this local-representative isn't connected to agent-coordinator, so it can't reach %s", node))
		return
	}
	q := r.URL.Query()
	q.Del("node")
	target := "http://" + addr + "/host/" + url.PathEscape(node) + r.URL.Path
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, controlAPIMaxBody+1))
	if err != nil {
		controlAPIError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlRelayTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(body))
	if err != nil {
		controlAPIError(w, http.StatusBadRequest, err)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		controlAPIError(w, http.StatusBadGateway, fmt.Errorf("reaching %s through agent-coordinator: %v", node, err))
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		controlAPIError(w, http.StatusBadGateway, err)
		return
	}
	if resp.StatusCode == http.StatusOK && strings.Contains(r.URL.Path, "/runs") {
		if data, err = s.copyRelayedRecordings(node, data); err != nil {
			controlAPIError(w, http.StatusBadGateway, err)
			return
		}
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	w.Write(data)
}

// relayedCopies are recordings already copied from other nodes, by
// "<node>/<file id there>", so polling a finished run copies each once.
var relayedCopies = struct {
	sync.Mutex
	m map[string]ControlRecording
}{m: map[string]ControlRecording{}}

// copyRelayedRecordings rewrites a relayed {run} or {runs} answer so each
// finished run's recordings are copies in this files tab.
func (s *Server) copyRelayedRecordings(node string, data []byte) ([]byte, error) {
	var ans map[string]json.RawMessage
	if err := json.Unmarshal(data, &ans); err != nil {
		return data, nil // not a run answer; pass it on as it is
	}
	// strict: a recording that can't be copied fails the answer (one run,
	// just finished). Listing runs by key leaves it as the node has it
	// instead, so an older run whose recording has since expired there
	// doesn't make the key's runs unreadable for good.
	fix := func(run *ControlRunMsg, strict bool) error {
		if run.Status == "running" {
			return nil
		}
		for i, rec := range run.Recordings {
			key := node + "/" + rec.FileID
			relayedCopies.Lock()
			done, ok := relayedCopies.m[key]
			relayedCopies.Unlock()
			if !ok {
				info, err := s.copyNodeFileLimit(node, rec.FileID, rec.Name, controlMaxRecordingCopy, controlRecordCopyTimeout)
				if err != nil {
					err = fmt.Errorf("copying %s's recording %s here: %v", node, rec.Name, err)
					if strict {
						return err
					}
					log.Printf("control API: %v", err)
					continue
				}
				done = rec
				done.FileID, done.Name = info.ID, info.Name
				relayedCopies.Lock()
				relayedCopies.m[key] = done
				relayedCopies.Unlock()
			}
			run.Recordings[i] = done
		}
		return nil
	}
	if raw, ok := ans["run"]; ok {
		var run ControlRunMsg
		if err := json.Unmarshal(raw, &run); err != nil {
			return data, nil
		}
		if err := fix(&run, true); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(run)
		ans["run"] = b
	}
	if raw, ok := ans["runs"]; ok {
		var runs []ControlRunMsg
		if err := json.Unmarshal(raw, &runs); err != nil {
			return data, nil
		}
		for i := range runs {
			if err := fix(&runs[i], false); err != nil {
				return nil, err
			}
		}
		b, _ := json.Marshal(runs)
		ans["runs"] = b
	}
	return json.Marshal(ans)
}
