package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// apiCall sends one request to s's control API and decodes its answer.
func apiCall(t *testing.T, s *Server, method, target, body string, out any) int {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleControlAPI(w, req)
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, target, err, w.Body.String())
		}
	}
	return w.Code
}

const apiTestLib = `format: lr-control-v1
actions:
  - id: api-say
    name: Say something
    controls:
      - name: words
        default: hi
    do:
      - {op: output, label: said, value: "{{words}}"}
sequences:
  - id: api-hello
    name: API hello
    controls:
      - name: words
        default: hello
    steps:
      - action: api-say
        with:
          words: "{{words}}"
`

func TestControlAPIImportAndRun(t *testing.T) {
	s := newServer("test-lr")

	var one struct {
		Node     string               `json:"node"`
		Sequence *ControlSequenceView `json:"sequence"`
	}
	if code := apiCall(t, s, "GET", "/api/control/sequences/api-hello", "", &one); code != 200 || one.Node != "test-lr" || one.Sequence != nil {
		t.Fatalf("before import: %d %+v", code, one)
	}

	// check=1 reports the definition without importing.
	var checked struct {
		Sequences []map[string]string `json:"sequences"`
	}
	if code := apiCall(t, s, "POST", "/api/control/import?check=1", apiTestLib, &checked); code != 200 || len(checked.Sequences) != 1 || checked.Sequences[0]["id"] != "api-hello" {
		t.Fatalf("check: %d %+v", code, checked)
	}
	apiCall(t, s, "GET", "/api/control/sequences/api-hello", "", &one)
	if one.Sequence != nil {
		t.Fatal("check=1 imported it")
	}
	// check=1 fails where the import would: a step with no such action.
	broken := "format: lr-control-v1\nsequences:\n  - id: api-broken\n    name: Broken\n    steps:\n      - action: no-such-action\n"
	if code := apiCall(t, s, "POST", "/api/control/import?check=1", broken, nil); code != http.StatusUnprocessableEntity {
		t.Errorf("check of a broken document: %d", code)
	}
	if code := apiCall(t, s, "POST", "/api/control/import", apiTestLib, nil); code != 200 {
		t.Fatalf("import: %d", code)
	}
	apiCall(t, s, "GET", "/api/control/sequences/api-hello", "", &one)
	if one.Sequence == nil || one.Sequence.Definition != checked.Sequences[0]["definition"] || one.Sequence.Steps != 1 || one.Sequence.Error != "" {
		t.Fatalf("after import: %+v (checked %v)", one.Sequence, checked)
	}

	// A keyed run, found again by its id and its key.
	var started struct {
		Run ControlRunMsg `json:"run"`
	}
	if code := apiCall(t, s, "POST", "/api/control/runs", `{"sequence": "api-hello", "key": "k1", "controls": {"words": "from the API"}}`, &started); code != 200 || started.Run.Key != "k1" {
		t.Fatalf("start: %d %+v", code, started.Run)
	}
	var got struct {
		Run ControlRunMsg `json:"run"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		apiCall(t, s, "GET", "/api/control/runs/"+started.Run.ID+"?wait=1", "", &got)
		if got.Run.Status != "running" || time.Now().After(deadline) {
			break
		}
	}
	if got.Run.Status != "success" || len(got.Run.Output) != 1 || got.Run.Output[0].Value != "from the API" {
		t.Fatalf("run: %+v", got.Run)
	}
	var byKey struct {
		Runs []ControlRunMsg `json:"runs"`
	}
	if apiCall(t, s, "GET", "/api/control/runs?key=k1", "", &byKey); len(byKey.Runs) != 1 || byKey.Runs[0].ID != started.Run.ID {
		t.Errorf("by key: %+v", byKey.Runs)
	}
	if apiCall(t, s, "GET", "/api/control/runs?key=other", "", &byKey); len(byKey.Runs) != 0 {
		t.Errorf("another key: %+v", byKey.Runs)
	}

	// Without agent-coordinator, another node can't be reached.
	if code := apiCall(t, s, "GET", "/api/control/sequences?node=elsewhere", "", nil); code != http.StatusBadGateway {
		t.Errorf("another node: %d", code)
	}

	if code := apiCall(t, s, "DELETE", "/api/control/sequences/api-hello", "", nil); code != 200 {
		t.Errorf("delete: %d", code)
	}
	apiCall(t, s, "GET", "/api/control/sequences/api-hello", "", &one)
	if one.Sequence != nil {
		t.Error("still there after delete")
	}
}

// TestDTTRobotHelloSequence checks the control sequence dtt's
// localrep-robot-hello example imports reads, imports and compiles here.
func TestDTTRobotHelloSequence(t *testing.T) {
	src, err := os.ReadFile("../dtt/examples/compositions/resources/robot-hello.control.yaml")
	if err != nil {
		t.Skip("dtt's examples aren't beside this checkout:", err)
	}
	s := newServer("test-lr")
	if code := apiCall(t, s, "POST", "/api/control/import", string(src), nil); code != 200 {
		t.Fatalf("import: %d", code)
	}
	var one struct {
		Sequence *ControlSequenceView `json:"sequence"`
	}
	apiCall(t, s, "GET", "/api/control/sequences/dtt-robot-hello", "", &one)
	if one.Sequence == nil || one.Sequence.Error != "" || one.Sequence.Steps != 6 || one.Sequence.Recordings != 1 {
		t.Fatalf("dtt-robot-hello: %+v", one.Sequence)
	}
}

// TestDTTFCRidealongSequence checks the control sequence dtt's
// localrep-demo-fc-ridealong example imports reads, imports and compiles
// here -- its long printf command is a block-mapping instruction.
func TestDTTFCRidealongSequence(t *testing.T) {
	src, err := os.ReadFile("../dtt/examples/compositions/resources/demo-fc-ridealong.control.yaml")
	if err != nil {
		t.Skip("dtt's examples aren't beside this checkout:", err)
	}
	s := newServer("test-lr")
	if code := apiCall(t, s, "POST", "/api/control/import", string(src), nil); code != 200 {
		t.Fatalf("import: %d", code)
	}
	var one struct {
		Sequence *ControlSequenceView `json:"sequence"`
	}
	apiCall(t, s, "GET", "/api/control/sequences/dtt-demo-fc-ridealong", "", &one)
	if one.Sequence == nil || one.Sequence.Error != "" || one.Sequence.Steps != 10 || one.Sequence.Recordings != 1 {
		t.Fatalf("dtt-demo-fc-ridealong: %+v", one.Sequence)
	}
}
