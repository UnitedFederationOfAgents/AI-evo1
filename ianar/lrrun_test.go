package main

import (
	"strings"
	"testing"
)

func TestCompileRobotRun(t *testing.T) {
	q, err := compileRobotRun(RobotRunRequest{
		Run:  "abc",
		Name: "find it",
		Steps: []RobotRunStep{
			{Label: "Click the marker", Do: []Instruction{{"op": "click-text", "text": "This is the one - x7k2qp", "exact": "true"}}},
			{Label: "Press →", Do: []Instruction{{"op": "key", "keys": "right"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.id != "lr-abc" || q.name != "find it" {
		t.Errorf("id/name = %q/%q", q.id, q.name)
	}
	if len(q.steps) != 2 || q.steps[0].Label != "Click the marker" || q.steps[1].Label != "Press →" {
		t.Fatalf("steps = %+v", q.def().Steps)
	}
	if d := strings.Join(q.steps[0].Detail, " "); !strings.Contains(d, "This is the one - x7k2qp") {
		t.Errorf("step 1 detail %q doesn't name the text it clicks", d)
	}
}

func TestCompileRobotRunRejectsBadSteps(t *testing.T) {
	for name, req := range map[string]RobotRunRequest{
		"no steps":        {Run: "a"},
		"no instructions": {Run: "a", Steps: []RobotRunStep{{Label: "empty"}}},
		"unknown op":      {Run: "a", Steps: []RobotRunStep{{Label: "x", Do: []Instruction{{"op": "teleport"}}}}},
		"missing arg":     {Run: "a", Steps: []RobotRunStep{{Label: "x", Do: []Instruction{{"op": "key"}}}}},
	} {
		if _, err := compileRobotRun(req); err == nil {
			t.Errorf("%s: compiled without error", name)
		}
	}
}
