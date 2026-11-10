package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ---- Files-tab integrations (Revision G of
// condocs/slopspaceSlopspaceCondocsImpls/Step1Prompt.md) ----
//
// A file whose type another tab can take in gets an "integrations" row of
// buttons in the file-details pane, below the standard ones. The first are a
// sequence YAML's (see seqfiles.go): "import" merges it into its tab's
// library, as that tab's own import does, and "import and run" then runs the
// document's first sequence with every control at its default -- so it only
// works for a sequence that can run that way. Both are
//
//	POST /api/files/<id>/import[?run=1]
//
// answering {"message": "..."} or an error. A control sequence goes to this
// LR's control engine; a robot sequence goes to IANAR as "__robot:seq-import
// <json>" (ianar/lrimport.go), and the answer waits for its reply. Like
// hold/persist, it isn't gated on proxiedHeader, so agent-coordinator's host
// view of the files tab reaches it through /host/<id>/*.

// robotImportTimeout is how long an import waits for IANAR's answer. The
// answer comes once the import is done and any run has started, not when
// the run finishes.
const robotImportTimeout = 15 * time.Second

// maxRelayedSeqFile caps a robot sequence sent to IANAR: the representable
// link reads a line at a time, up to 64 KiB (see maxRelayedLibrary).
const maxRelayedSeqFile = 56 << 10

// RobotSeqImportMsg is IANAR's "robot-seq-import-result" (ianar/lrimport.go).
type RobotSeqImportMsg struct {
	Req     string `json:"req"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
	Ran     string `json:"ran,omitempty"`
}

// handleFileImport implements POST /api/files/<id>/import[?run=1].
func (s *Server) handleFileImport(w http.ResponseWriter, r *http.Request, id string) {
	dir, ok := s.locateFile(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	run := r.URL.Query().Get("run") == "1"
	kind := classifySequenceFile(dir, id, s.fileRobotOps())
	if kind == "" {
		http.Error(w, "not a sequence file that imports cleanly", http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, id))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var msg string
	if kind == seqFileControl {
		msg, err = s.importControlSequence(string(data), run)
	} else {
		msg, err = s.importRobotSequence(string(data), run)
	}
	if err != nil {
		log.Printf("files: import of %s (run=%t): %v", id, run, err)
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	log.Printf("files: %s: %s", id, msg)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

// importControlSequence imports src into the control tab's library and,
// with run, starts its first sequence with its controls' defaults. A run
// that can't start still leaves the import in place; the error says so.
func (s *Server) importControlSequence(src string, run bool) (string, error) {
	if s.control == nil {
		return "", errors.New("the control tab isn't available")
	}
	doc, err := decodeControlLib(src)
	if err != nil {
		return "", err
	}
	reply := s.control.handleLibRequest(ControlLibRequest{Op: "import", YAML: src})
	if !reply.Success {
		return "", errors.New(reply.Error)
	}
	if !run {
		return reply.Message, nil
	}
	if len(doc.Sequences) == 0 {
		return "", fmt.Errorf("%s, but the file holds no sequence to run", reply.Message)
	}
	seq := doc.Sequences[0].ID
	if err := s.control.start(seq, nil, ""); err != nil {
		return "", fmt.Errorf("%s, but sequence %q didn't start: %v", reply.Message, seq, err)
	}
	return fmt.Sprintf("%s; running sequence %q in the control tab", reply.Message, seq), nil
}

// importRobotSequence sends src to IANAR to import and, with run, start, and
// waits for its answer.
func (s *Server) importRobotSequence(src string, run bool) (string, error) {
	if s.reprServer == nil || !s.reprServer.IsHealthy("robot") {
		return "", errors.New("the robot (IANAR) isn't connected")
	}
	if len(src) > maxRelayedSeqFile {
		return "", fmt.Errorf("the file is over %dKiB, too large to send to the robot -- import it from the robot tab", maxRelayedSeqFile>>10)
	}
	req := randomToken(8)
	b, err := json.Marshal(map[string]any{"req": req, "yaml": src, "run": run})
	if err != nil {
		return "", err
	}
	ch := make(chan RobotSeqImportMsg, 1)
	s.robotImportsMu.Lock()
	if s.robotImports == nil {
		s.robotImports = make(map[string]chan RobotSeqImportMsg)
	}
	s.robotImports[req] = ch
	s.robotImportsMu.Unlock()
	defer func() {
		s.robotImportsMu.Lock()
		delete(s.robotImports, req)
		s.robotImportsMu.Unlock()
	}()

	s.reprServer.SendCommand("robot", "__robot:seq-import "+string(b))
	select {
	case res := <-ch:
		if !res.Success {
			return "", errors.New(res.Error)
		}
		if res.Ran != "" {
			return fmt.Sprintf("%s; running sequence %q in the robot tab", res.Message, res.Ran), nil
		}
		return res.Message, nil
	case <-time.After(robotImportTimeout):
		return "", errors.New("the robot didn't answer")
	}
}

// noteRobotSeqImport hands IANAR's answer to the import waiting on it.
func (s *Server) noteRobotSeqImport(res RobotSeqImportMsg) {
	s.robotImportsMu.Lock()
	ch, ok := s.robotImports[res.Req]
	s.robotImportsMu.Unlock()
	if !ok {
		log.Printf("files: robot answered import %s, which is no longer waiting", res.Req)
		return
	}
	select {
	case ch <- res:
	default:
	}
}
