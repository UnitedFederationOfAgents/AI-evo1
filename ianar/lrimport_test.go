package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const lrImportYAML = `format: ianar-sequence-v2
actions:
  - id: lr-import-print
    name: Print a line
    do:
      - {op: print, text: hi}
sequences:
  - id: lr-import-hello
    name: Hello from the files tab
    steps:
      - action: lr-import-print
`

// TestImportForLR covers "__robot:seq-import" without run: the document is
// merged into the library and the answer says so.
func TestImportForLR(t *testing.T) {
	s := &Server{seqLib: openSeqLibrary("")}
	res := s.importForLR(RobotSeqImportRequest{Req: "r1", YAML: lrImportYAML})
	if !res.Success || res.Req != "r1" || res.Ran != "" {
		t.Fatalf("got %+v", res)
	}
	if !strings.Contains(res.Message, "lr-import-hello") {
		t.Errorf("message %q doesn't name the sequence", res.Message)
	}
	if _, err := s.seqLib.compile("lr-import-hello", nil, time.Now()); err != nil {
		t.Errorf("the imported sequence doesn't compile: %v", err)
	}
}

// TestImportForLRErrors checks a bad document, and a run of a document with
// no sequence, fail without touching the library.
func TestImportForLRErrors(t *testing.T) {
	s := &Server{seqLib: openSeqLibrary("")}
	if res := s.importForLR(RobotSeqImportRequest{Req: "r2", YAML: "{{{"}); res.Success || res.Error == "" {
		t.Errorf("bad yaml: got %+v", res)
	}
	actionsOnly := lrImportYAML[:strings.Index(lrImportYAML, "sequences:")]
	if res := s.importForLR(RobotSeqImportRequest{Req: "r3", YAML: actionsOnly, Run: true}); res.Success || !strings.Contains(res.Error, "no sequence") {
		t.Errorf("run without a sequence: got %+v", res)
	}
	if _, ok := s.seqLib.actionMap()["lr-import-print"]; ok {
		t.Error("a failed import changed the library")
	}
}

// TestReplayImported checks a tab that connects after an imported run began
// gets its started message, each step's latest progress in step order, and
// the result -- until importedReplayFor after it finished.
func TestReplayImported(t *testing.T) {
	now := time.Now()
	origClock := clock
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = origClock })

	s := &Server{clients: map[*wsClient]bool{}}
	// replayTypes lists what a newly connected tab is replayed, progress
	// messages as "seq2-progress:<step>".
	replayTypes := func() []string {
		c := &wsClient{send: make(chan []byte, 64)}
		s.replayImported(c)
		close(c.send)
		var types []string
		for b := range c.send {
			var m wsMsg
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			if m.Type == "seq2-progress" {
				var p SequenceProgressMsg
				_ = json.Unmarshal(m.Payload, &p)
				m.Type = fmt.Sprintf("%s:%d", m.Type, p.Step)
			}
			types = append(types, m.Type)
		}
		return types
	}
	if got := replayTypes(); len(got) != 0 {
		t.Fatalf("nothing run yet, replayed %v", got)
	}

	def := SequenceDef{ID: "q", Steps: []SequenceStepDef{{}, {}}}
	s.broadcastImported("seq2-started", -1, Seq2StartedMsg{SequenceID: "q", Def: def, Imported: true})
	s.broadcastImported("seq2-progress", 1, SequenceProgressMsg{SequenceID: "q", Step: 1, Status: "running"})
	s.broadcastImported("seq2-progress", 0, SequenceProgressMsg{SequenceID: "q", Step: 0, Status: "success"})
	s.broadcastImported("seq2-progress", 1, SequenceProgressMsg{SequenceID: "q", Step: 1, Status: "success"})
	if got, want := strings.Join(replayTypes(), " "), "seq2-started seq2-progress:0 seq2-progress:1"; got != want {
		t.Errorf("mid-run replay = %q, want %q", got, want)
	}

	s.broadcastImported("seq2-result", -1, SequenceResultMsg{SequenceID: "q", FailedStep: -1})
	if got, want := strings.Join(replayTypes(), " "), "seq2-started seq2-progress:0 seq2-progress:1 seq2-result"; got != want {
		t.Errorf("finished replay = %q, want %q", got, want)
	}

	now = now.Add(importedReplayFor + time.Second)
	if got := replayTypes(); len(got) != 0 {
		t.Errorf("long-finished run replayed %v", got)
	}
}
