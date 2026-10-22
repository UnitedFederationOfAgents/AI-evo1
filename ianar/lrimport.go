package main

import (
	"encoding/json"
	"fmt"
	"log"
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
	s.broadcast("seq2-started", Seq2StartedMsg{SequenceID: id, Def: q.def()})
	res := runSequence(q, func(p SequenceProgressMsg) { s.broadcast("seq2-progress", p) })
	res.Warnings = s.seqLib.staleFor(id)
	if res.Recording != nil {
		res.ArtifactID = s.artifacts.keep(sequenceArtifact(q.def(), res))
	}
	s.broadcast("seq2-result", res)
}
