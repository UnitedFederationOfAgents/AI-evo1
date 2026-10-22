package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// This file backs the files tab's "import" and "import and run" buttons on a
// robot sequence (local-representative/fileintegrations.go, Revision G of
// condocs/slopspaceSlopspaceCondocsImpls/Step1Prompt.md). LR sends the file's
// YAML as "__robot:seq-import <json>"; it's merged into the sequence-v2
// library exactly as the composer's own import does ("seq2-import"), and with
// run set the document's first sequence is then run with every control at
// its default. The answer goes back as a "robot-seq-import-result" data
// message once the import is done (and, with run, the run has started); the
// run itself is broadcast to every robot tab as seq2-started/-progress/
// -result, the same messages a run from the runner tab streams to its own.

// RobotSeqImportRequest is the JSON after "__robot:seq-import ".
type RobotSeqImportRequest struct {
	Req  string `json:"req"` // LR's id for it, echoed back
	YAML string `json:"yaml"`
	Run  bool   `json:"run,omitempty"`
}

// RobotSeqImportMsg is the "robot-seq-import-result" payload.
type RobotSeqImportMsg struct {
	Req     string `json:"req"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
	Ran     string `json:"ran,omitempty"` // the sequence started, with run set
}

// handleRobotSeqImport carries out raw ("__robot:seq-import <json>") and
// answers LR.
func (s *Server) handleRobotSeqImport(arg string) {
	var req RobotSeqImportRequest
	if err := json.Unmarshal([]byte(arg), &req); err != nil {
		log.Printf("repr: bad seq-import request: %v", err)
		return
	}
	s.sendToLR("robot-seq-import-result", s.importForLR(req))
}

// importForLR imports req's document and, with req.Run, starts its first
// sequence in the background. A run that can't start (a control without a
// usable default, say) still leaves the import in place; the error says so.
func (s *Server) importForLR(req RobotSeqImportRequest) RobotSeqImportMsg {
	out := RobotSeqImportMsg{Req: req.Req}
	doc, err := decodeSeqLib(req.YAML)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if req.Run && len(doc.Sequences) == 0 {
		out.Error = "the file holds no sequence to run"
		return out
	}
	doc.Examples = nil // only the saved library records those
	msg, err := s.seqLib.merge(doc)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	s.broadcast("seq2-library", s.seqLib.snapshot())
	out.Message = "imported: " + msg
	log.Printf("robot: local-representative %s", out.Message)
	if !req.Run {
		out.Success = true
		return out
	}

	id := doc.Sequences[0].ID
	q, err := s.seqLib.compile(id, nil, clock())
	if err != nil {
		out.Error = fmt.Sprintf("%s, but sequence %q can't run with its defaults: %v", out.Message, id, err)
		out.Message = ""
		return out
	}
	out.Success, out.Ran = true, id
	go s.runImported(id, q)
	return out
}

// runImported runs an imported sequence as handleRunSequenceV2 does, but
// streams it to every robot tab rather than the one that asked.
func (s *Server) runImported(id string, q sequence) {
	log.Printf("robot: running imported sequence %q for local-representative", id)
	s.broadcastImported("seq2-started", -1, Seq2StartedMsg{SequenceID: id, Def: q.def(), Imported: true})
	res := runSequence(q, func(p SequenceProgressMsg) { s.broadcastImported("seq2-progress", p.Step, p) })
	res.Warnings = s.seqLib.staleFor(id)
	if res.Recording != nil {
		res.ArtifactID = s.artifacts.keep(sequenceArtifact(q.def(), res))
	}
	s.broadcastImported("seq2-result", -1, res)
}

// importedReplayFor is how long after an imported run finishes a newly
// opened robot tab is still shown it. "Import and run" sends the files tab
// to the robot tab (Revision H), whose page only connects once it's shown --
// usually after the run has started, often after a short one has finished.
const importedReplayFor = 2 * time.Minute

// importedRun is the latest imported run's messages, in the order a robot
// tab needs them: seq2-started, each step's latest seq2-progress, then
// seq2-result once it's done.
type importedRun struct {
	mu       sync.Mutex
	started  []byte
	progress map[int][]byte
	steps    int
	result   []byte
	doneAt   time.Time
}

// broadcastImported broadcasts one of an imported run's messages and keeps
// it for replayImported. step is the progress message's step, else -1.
// Holding imported.mu across the broadcast keeps a tab that's being replayed
// to from getting a newer message ahead of the replay.
func (s *Server) broadcastImported(typ string, step int, payload interface{}) {
	msg := s.marshalMsg(typ, payload)
	r := &s.imported
	r.mu.Lock()
	defer r.mu.Unlock()
	switch typ {
	case "seq2-started":
		r.started, r.progress, r.result = msg, map[int][]byte{}, nil
		r.steps = len(payload.(Seq2StartedMsg).Def.Steps)
	case "seq2-progress":
		r.progress[step] = msg
	case "seq2-result":
		r.result, r.doneAt = msg, clock()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

// replayImported sends a newly connected tab the imported run, if one is
// going or finished within importedReplayFor.
func (s *Server) replayImported(c *wsClient) {
	r := &s.imported
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started == nil || (r.result != nil && clock().Sub(r.doneAt) > importedReplayFor) {
		return
	}
	msgs := [][]byte{r.started}
	for i := 0; i < r.steps; i++ {
		if m, ok := r.progress[i]; ok {
			msgs = append(msgs, m)
		}
	}
	if r.result != nil {
		msgs = append(msgs, r.result)
	}
	for _, m := range msgs {
		select {
		case c.send <- m:
		default:
		}
	}
}
