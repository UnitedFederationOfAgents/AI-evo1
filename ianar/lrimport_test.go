package main

import (
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
