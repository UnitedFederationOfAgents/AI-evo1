package main

import (
	"strings"
	"testing"
)

func hasPath(paths []string, p string) bool {
	for _, x := range paths {
		if x == p {
			return true
		}
	}
	return false
}

// TestDemoCoverage: a demo's coverage is what its document declares and
// what its phases call -- a subject's calls focal, a lead-in's, a
// tear-down's and the machinery supporting -- with nothing in both, and
// the declaring lines kept out of its description.
func TestDemoCoverage(t *testing.T) {
	var d DemoInfo
	for _, x := range builtinDemos() {
		if x.Name == "robot-hello-read" {
			d = x
		}
	}
	c := d.Coverage
	if c == nil {
		t.Fatalf("robot-hello-read has no coverage: %+v", d)
	}
	for _, p := range []string{
		"ianar/vision.go", "ianar/seqv2_ops.go",
		"dtt/examples/compositions/localrep-demo-robot-hello-read.dtt.yaml",
		"dtt/examples/compositions/resources/demo-robot-hello-read.control.yaml",
	} {
		if !hasPath(c.Focal, p) {
			t.Errorf("focal %v hasn't %s", c.Focal, p)
		}
	}
	for _, p := range []string{
		"ianar/lrrun.go",
		"dtt/examples/compositions/localrep-demo-prepare.dtt.yaml",
		"dtt/examples/compositions/resources/demo-shutdown.control.yaml",
		"composer/",
		"local-representative/demos/robot-hello-read.demo.dtt.yaml",
		"local-representative/demos.go",
	} {
		if !hasPath(c.Supporting, p) {
			t.Errorf("supporting %v hasn't %s", c.Supporting, p)
		}
	}
	for _, p := range c.Supporting {
		if coverageCovers(c.Focal, p) {
			t.Errorf("%s is focal and supporting", p)
		}
	}
	if strings.Contains(d.Description, "focal:") || strings.Contains(d.Description, "supporting:") || !strings.Contains(d.Description, "protosurf map") {
		t.Errorf("description %q", d.Description)
	}

	// Declared paths are cleaned; a focal directory takes in a supporting
	// path under it.
	src := "# Demo -- x\n#\n#   focal: a/, ./b/c.go , ../up\n#   supporting: a/x.go\tz/\n" +
		"dtt.calls:\n  select:\n    document: other.dtt.yaml\n  phase: lead-in\n"
	h := parseDemo("x", src, demoOriginConfigured, "/lib/x.demo.dtt.yaml")
	if got := strings.Join(h.Coverage.Focal, " "); got != "a/ b/c.go" {
		t.Errorf("host demo's focal %q", got)
	}
	if hasPath(h.Coverage.Supporting, "a/x.go") || !hasPath(h.Coverage.Supporting, "z/") ||
		!hasPath(h.Coverage.Supporting, "dtt/examples/compositions/other.dtt.yaml") || hasPath(h.Coverage.Supporting, "composer/") {
		t.Errorf("host demo's supporting %v", h.Coverage.Supporting)
	}
	for _, p := range h.Coverage.Supporting {
		if strings.HasPrefix(p, "local-representative/demos/") {
			t.Errorf("a host demo's document isn't in the project: %s", p)
		}
	}
}

// TestSuiteCoverage: a suite's coverage is its demos' together, so the two
// built-in suites pick out different parts of the map.
func TestSuiteCoverage(t *testing.T) {
	w, _ := demoWatch(t, func(map[string]string) ToolRunResult { return ToolRunResult{OK: true, Tool: "declare"} })
	byName := map[string]SuiteInfo{}
	for _, s := range w.suiteLibrary(w.demoLibrary()) {
		byName[s.Name] = s
	}
	robot, sub := byName["robot-hello"], byName["subapp-hello"]
	if sub.Error != "" || strings.Join(sub.Demos, " ") != "session-hello condoc-hello" || sub.OnFailure != "continue" {
		t.Fatalf("subapp-hello suite: %+v", sub)
	}
	if robot.Coverage == nil || sub.Coverage == nil {
		t.Fatalf("coverage %+v / %+v", robot.Coverage, sub.Coverage)
	}
	for _, p := range []string{"ianar/vision.go", "ianar/keyboard.go", "ianar/lrcapture.go", "dtt/examples/compositions/localrep-demo-robot-hello-zoom.dtt.yaml"} {
		if !hasPath(robot.Coverage.Focal, p) {
			t.Errorf("robot-hello's focal %v hasn't %s", robot.Coverage.Focal, p)
		}
	}
	for _, p := range []string{"session-manager/", "condocv2/", "dtt/examples/compositions/localrep-demo-condoc-hello.dtt.yaml"} {
		if !hasPath(sub.Coverage.Focal, p) {
			t.Errorf("subapp-hello's focal %v hasn't %s", sub.Coverage.Focal, p)
		}
	}
	if !hasPath(sub.Coverage.Supporting, "ianar/") || !hasPath(sub.Coverage.Supporting, "federation-command/") || coverageCovers(sub.Coverage.Focal, "ianar/vision.go") {
		t.Errorf("subapp-hello's supporting %v", sub.Coverage.Supporting)
	}
	// lrcapture.go is supporting in two of robot-hello's demos and focal in
	// the third: it's the suite's focal.
	if hasPath(robot.Coverage.Supporting, "ianar/lrcapture.go") {
		t.Errorf("robot-hello's supporting %v has a focal path", robot.Coverage.Supporting)
	}
}
