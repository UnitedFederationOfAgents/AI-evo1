package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

const testPlan = `{"progress":"plan","at":1,"format":"dtt-progress-v1","mode":"read","steps":[` +
	`{"id":"s1","key":"timestamp","path":"timestamp","provider":"timestamp","line":1,"after":[],"level":0},` +
	`{"id":"s2","key":"github.users.x","path":"github.users.x","provider":"github","line":3,"after":["s1"],"level":1},` +
	`{"id":"s3","key":"filesystem.items","path":"filesystem.items","provider":"filesystem","line":6,"after":["s2"],"level":2}]}`

func TestDTTProgressSplitter(t *testing.T) {
	var rest bytes.Buffer
	var got []string
	s := &dttProgressSplitter{emit: func(l []byte) { got = append(got, string(l)) }, rest: &rest}
	// Lines arrive in pieces; the answer after them is left whole.
	for _, chunk := range []string{`{"progress":"be`, `gin","step":"s1"}` + "\n" + `{"progress":"end","st`, `ep":"s1"}` + "\n{\n  \"ok\": true\n", "}"} {
		s.Write([]byte(chunk))
	}
	s.flush()
	if len(got) != 2 || got[0] != `{"progress":"begin","step":"s1"}` || got[1] != `{"progress":"end","step":"s1"}` {
		t.Fatalf("progress %q", got)
	}
	if rest.String() != "{\n  \"ok\": true\n}" {
		t.Fatalf("rest %q", rest.String())
	}
	if out := stripDTTProgress(`{"progress":"begin","step":"s1"}` + "\n{\"ok\": true}\n"); strings.TrimSpace(out) != `{"ok": true}` {
		t.Fatalf("stripped %q", out)
	}
}

// TestToolRunProgress: a run's progress lines, as dtt prints them, show in
// tools-state while it runs -- each step pending, running, ok or failed --
// and a step still running when the run fails ends failed with it.
func TestToolRunProgress(t *testing.T) {
	s := toolsServer(t, &fakeDTT{})
	mid, release := make(chan struct{}), make(chan struct{})
	var gotArgs []string
	s.tools.dtt = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		if args[0] == "list" {
			return []byte(treeListJSON), nil
		}
		gotArgs = args
		emit := dttProgressFrom(ctx)
		if emit == nil {
			return nil, errors.New("no progress handler")
		}
		for _, l := range []string{testPlan,
			`{"progress":"begin","at":2,"step":"s1","stage":"measure"}`,
			`{"progress":"end","at":3,"step":"s1","stage":"measure","status":"ok","duration_ms":1,"objects":[{"id":"timestamp","state":{}}]}`,
			`{"progress":"begin","at":4,"step":"s2","stage":"measure"}`} {
			emit([]byte(l))
		}
		close(mid)
		<-release
		b, _ := json.Marshal(ToolRunResult{OK: false, Tool: "materialize-tree", Error: "github said no", Outputs: map[string]string{}})
		return b, nil
	}
	r := s.tools.handle(ToolsRequest{Op: "run", Tool: "materialize-tree"}, false, "test-lr")
	if !r.OK {
		t.Fatalf("reply %+v", r)
	}
	<-mid
	statuses := func(run ToolRun) string {
		if run.Progress == nil {
			return "no progress"
		}
		var parts []string
		for _, s := range run.Progress.Steps {
			parts = append(parts, s.ID+"="+s.Status)
		}
		return strings.Join(parts, " ")
	}
	var running ToolRun
	for _, run := range s.tools.tabState().Runs {
		if run.ID == r.RunID {
			running = run
		}
	}
	if got := statuses(running); got != "s1=ok s2=running s3=pending" {
		t.Fatalf("while running: %s", got)
	}
	if p := running.Progress; p.Mode != "read" || p.Steps[1].Provider != "github" || p.Steps[1].After[0] != "s1" ||
		len(p.Steps[0].Objects) != 1 || p.Steps[0].Objects[0] != "timestamp" {
		t.Errorf("progress %+v", p)
	}
	if !slices.Contains(gotArgs, "--progress") {
		t.Errorf("dtt was called %v", gotArgs)
	}
	// Through agent-coordinator the steps come without their objects.
	for _, run := range s.tools.tabStateForAC().Runs {
		if run.ID == r.RunID && (run.Progress == nil || run.Progress.Steps[0].Objects != nil || run.Progress.Steps[0].ObjectsOmitted != 1) {
			t.Errorf("relayed progress %+v", run.Progress)
		}
	}
	close(release)

	done := waitRun(t, s.tools, r.RunID)
	if got := statuses(done); got != "s1=ok s2=failed s3=pending" || done.Progress.Steps[1].Error != "github said no" {
		t.Fatalf("finished: %s (%+v)", got, done.Progress.Steps[1])
	}
}
