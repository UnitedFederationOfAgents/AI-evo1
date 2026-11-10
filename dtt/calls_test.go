package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecentCalls: each execution puts its call's directory -- an in-place
// call's, or a pathless call's staging area -- at the top of the recent
// calls, once; dtt calls lists them and describes one; and a pathless
// call's staging directory calls the same call again.
func TestRecentCalls(t *testing.T) {
	e := newEnv(t)
	files := t.TempDir()
	src := "filesystem.items:\n  select: {abspath: " + files + "}\n"

	// A pathless call: a document with no .dtttc beside it.
	loose := filepath.Join(t.TempDir(), "loose.dtt.yaml")
	if err := os.WriteFile(loose, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := e.dtt("", "read", loose); code != exitOK {
		t.Fatalf("read of a loose document exited %d: %s", code, out)
	}
	stage := pathlessStage(e.pathlessDir, "loose.dtt.yaml", src)

	// An in-place call: a directory holding .dtttc.
	inPlace := filepath.Join(t.TempDir(), "my-call")
	if err := os.MkdirAll(inPlace, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{dtttcFile: "", "it.dtt.yaml": src} {
		if err := os.WriteFile(filepath.Join(inPlace, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code, out := e.dtt("", "read", inPlace); code != exitOK {
		t.Fatalf("read of an in-place call exited %d: %s", code, out)
	}

	list := func() []RecentCall {
		t.Helper()
		code, out := e.dtt("", "calls", "--json")
		var got struct {
			OK    bool         `json:"ok"`
			File  string       `json:"file"`
			Calls []RecentCall `json:"calls"`
		}
		if err := json.Unmarshal([]byte(out), &got); code != exitOK || err != nil || !got.OK || got.File != e.recentCalls {
			t.Fatalf("dtt calls exited %d (%v): %s", code, err, out)
		}
		return got.Calls
	}
	calls := list()
	if len(calls) != 2 || calls[0].Dir != inPlace || calls[0].Kind != callInPlace || calls[0].Name != "my-call" ||
		calls[1].Dir != stage || calls[1].Kind != callPathless || calls[1].Name != "loose_dtt_yaml" || calls[1].From != loose {
		t.Fatalf("recent calls: %+v", calls)
	}
	if calls[1].Document != filepath.Join(stage, stagedDocName) || calls[1].Mode != "read" || calls[1].Executions != 1 {
		t.Errorf("the pathless call's entry: %+v", calls[1])
	}
	if _, err := os.Stat(calls[0].Image); err != nil {
		t.Errorf("the in-place call's image: %v", err)
	}

	// Calling the pathless call's staging directory is the same call: it
	// goes back to the top, executed twice, and is kept in the same place.
	if code, out := e.dtt("", "read", stage); code != exitOK {
		t.Fatalf("read of a pathless staging directory exited %d: %s", code, out)
	}
	calls = list()
	if len(calls) != 2 || calls[0].Dir != stage || calls[0].Executions != 2 || calls[0].From != "" || calls[1].Dir != inPlace {
		t.Fatalf("after calling the staging directory: %+v", calls)
	}

	// dtt calls <call> describes it, its images newest first.
	code, out := e.dtt("", "calls", stage, "--json")
	var got struct {
		OK   bool     `json:"ok"`
		Call CallInfo `json:"call"`
	}
	if err := json.Unmarshal([]byte(out), &got); code != exitOK || err != nil || !got.OK {
		t.Fatalf("dtt calls <stage> exited %d (%v): %s", code, err, out)
	}
	c := got.Call
	if c.Dir != stage || c.Kind != callPathless || !c.Staged || c.Source != src || len(c.Images) != 2 || c.Recent == nil || c.Recent.Executions != 2 {
		t.Fatalf("the pathless call described: %+v", c)
	}
	if c.Images[0].Mode != "read" || c.Images[0].At == "" || c.Images[0].Path != calls[0].Image {
		t.Errorf("its newest image: %+v (the recent call kept %s)", c.Images[0], calls[0].Image)
	}

	// An unlisted document is described too, not yet staged.
	other := filepath.Join(t.TempDir(), "other.dtt.yaml")
	if err := os.WriteFile(other, []byte(src+"\n# other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out = e.dtt("", "calls", other, "--json"); code != exitOK || !strings.Contains(out, `"staged": false`) || !strings.Contains(out, "other_dtt_yaml") {
		t.Fatalf("dtt calls <unlisted> exited %d: %s", code, out)
	}
	// A directory without .dtttc is a pathless call of its only document.
	loneDir := filepath.Dir(other)
	if code, out = e.dtt("", "calls", loneDir, "--json"); code != exitOK || !strings.Contains(out, `"kind": "pathless"`) || !strings.Contains(out, "other_dtt_yaml") {
		t.Fatalf("dtt calls <dir without .dtttc> exited %d: %s", code, out)
	}
	// ...but not a file that isn't a document, or a directory with none.
	notDoc := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(notDoc, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{notDoc, files, "-"} {
		if code, out = e.dtt("", "calls", bad, "--json"); code != exitBadInput {
			t.Errorf("dtt calls %s exited %d: %s", bad, code, out)
		}
	}
}

// TestRecentCallsLimit: the list keeps the newest maxRecentCalls calls,
// each once.
func TestRecentCallsLimit(t *testing.T) {
	file := filepath.Join(t.TempDir(), "dtt", recentCallsName)
	dir := func(i int) string { return filepath.Join("/calls", strings.Repeat("x", i+1)) }
	// maxRecentCalls+5 calls, then one of the newest again.
	for _, i := range seq(maxRecentCalls + 5) {
		if err := noteRecentCall(file, RecentCall{Dir: dir(i), Kind: callInPlace, Executions: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := noteRecentCall(file, RecentCall{Dir: dir(maxRecentCalls), Kind: callInPlace, Executions: 1}); err != nil {
		t.Fatal(err)
	}
	rcs, err := readRecentCalls(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(rcs.Calls) != maxRecentCalls {
		t.Fatalf("%d recent calls, want %d", len(rcs.Calls), maxRecentCalls)
	}
	seen := map[string]bool{}
	for _, rc := range rcs.Calls {
		if seen[rc.Dir] {
			t.Fatalf("%s listed twice", rc.Dir)
		}
		seen[rc.Dir] = true
	}
	// The one called again is first, executed twice; the oldest are gone.
	if first := rcs.Calls[0]; first.Dir != dir(maxRecentCalls) || first.Executions != 2 {
		t.Errorf("first: %+v", first)
	}
	if seen[dir(0)] || seen[dir(4)] || !seen[dir(5)] {
		t.Errorf("the oldest calls should be dropped: %v", seen)
	}
	// A file that isn't the list is started afresh.
	if err := os.WriteFile(file, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := noteRecentCall(file, RecentCall{Dir: "/a", Executions: 1}); err != nil {
		t.Fatal(err)
	}
	if rcs, err = readRecentCalls(file); err != nil || len(rcs.Calls) != 1 {
		t.Fatalf("after a bad file: %+v, %v", rcs, err)
	}
}

// TestSpecExamples: dtt examples --specifications lists the example
// specifications, each unpacked under examples-dir as an in-place call
// (.dtttc, document, .gitignore for its images), and they execute from
// there -- json-with-note calling the composition its .dtttc includes.
func TestSpecExamples(t *testing.T) {
	e := newEnv(t)
	list := func() []SpecExample {
		t.Helper()
		code, out := e.dtt("", "examples", "--specifications", "--json")
		var got struct {
			OK             bool          `json:"ok"`
			Dir            string        `json:"dir"`
			Specifications []SpecExample `json:"specifications"`
		}
		if err := json.Unmarshal([]byte(out), &got); code != exitOK || err != nil || !got.OK || got.Dir != filepath.Join(e.examplesDir, "specifications") {
			t.Fatalf("dtt examples --specifications exited %d (%v): %s", code, err, out)
		}
		return got.Specifications
	}
	specs := list()
	byName := map[string]SpecExample{}
	for _, s := range specs {
		if s.Error != "" {
			t.Errorf("%s: %s", s.Name, s.Error)
		}
		if s.Title == "" || s.Description == "" || len(s.Commands) == 0 || s.Dir != filepath.Join(e.examplesDir, "specifications", s.Name) {
			t.Errorf("%s: %+v", s.Name, s)
		}
		// The repo's copy is what's embedded and unpacked.
		for _, f := range []string{dtttcFile, ".gitignore", s.Document} {
			repo, err := os.ReadFile(filepath.Join(filepath.FromSlash(s.Path), f))
			if err != nil {
				t.Errorf("%s: %v", s.Name, err)
				continue
			}
			if unpacked, err := os.ReadFile(filepath.Join(s.Dir, f)); err != nil || string(unpacked) != string(repo) {
				t.Errorf("%s: unpacked %s differs from %s (%v)", s.Name, f, s.Path, err)
			}
		}
		if gi, _ := os.ReadFile(filepath.Join(s.Dir, ".gitignore")); !strings.Contains(string(gi), imagesDirName+"/") {
			t.Errorf("%s: .gitignore doesn't ignore %s/: %q", s.Name, imagesDirName, gi)
		}
		byName[s.Name] = s
	}
	self, comp := byName["hello-files"], byName["json-with-note"]
	if len(self.Includes) != 0 || len(comp.Includes) != 1 || comp.Includes[0] != "compositions/filesystem-json.dtt.yaml" {
		t.Fatalf("includes: hello-files %v, json-with-note %v", self.Includes, comp.Includes)
	}

	// An edit to an unpacked file is undone by listing again; an image
	// left there is kept.
	doc := filepath.Join(self.Dir, self.Document)
	if err := os.WriteFile(doc, []byte("# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(self.Dir, imagesDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(self.Dir, imagesDirName, "kept.image.json")
	if err := os.WriteFile(kept, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	list()
	if data, _ := os.ReadFile(doc); string(data) != self.Source {
		t.Errorf("the edited document wasn't put back: %q", data)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("the image left there: %v", err)
	}

	// Both read from where they're unpacked, as in-place calls; the
	// self-contained one in a directory of its own.
	files := t.TempDir()
	if code, out := e.dtt("", "read", self.Dir, "--var", "dir="+files+"/"); code != exitOK {
		t.Fatalf("read of hello-files exited %d: %s", code, out)
	}
	code, out := e.dtt("", "read", comp.Dir)
	if code != exitOK || !strings.Contains(out, `"called": true`) || !strings.Contains(out, "dtt.calls[compositions/filesystem-json.dtt.yaml]") {
		t.Fatalf("read of json-with-note exited %d: %s", code, out)
	}
	code, out = e.dtt("", "calls", comp.Dir, "--json")
	if code != exitOK || !strings.Contains(out, `"kind": "in-place"`) || !strings.Contains(out, `"compositions/filesystem-json.dtt.yaml"`) {
		t.Fatalf("dtt calls <json-with-note> exited %d: %s", code, out)
	}
}

// TestDtttcInclude: an in-place call reads a dtt example only when its
// .dtttc includes it, and an include must be one of dtt's examples.
func TestDtttcInclude(t *testing.T) {
	e := newEnv(t)
	call := func(dtttc string) (int, string) {
		dir := filepath.Join(t.TempDir(), "inc")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		doc := "dtt.calls:\n  select:\n    document: compositions/filesystem-json.dtt.yaml\n"
		for name, content := range map[string]string{dtttcFile: dtttc, "it.dtt.yaml": doc} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return e.dtt("", "read", dir)
	}
	if code, out := call(""); code != exitBadInput {
		t.Errorf("without the include: exited %d: %s", code, out)
	}
	if code, out := call("include:\n  - compositions/filesystem-json.dtt.yaml\n"); code != exitOK {
		t.Errorf("with the include: exited %d: %s", code, out)
	}
	if code, out := call("include: examples/compositions\n"); code != exitOK {
		t.Errorf("including the whole directory: exited %d: %s", code, out)
	}
	for _, bad := range []string{"include:\n  - compositions/nosuch.dtt.yaml\n", "include:\n  - ../calls.go\n", "includes: x\n"} {
		if code, out := call(bad); code != exitBadInput {
			t.Errorf(".dtttc %q: exited %d: %s", bad, code, out)
		}
	}
	// parseDtttc on its own.
	spec, err := parseDtttc("document: a.dtt.yaml\ninclude: compositions/resources\n")
	if err != nil || spec.document != "a.dtt.yaml" || len(spec.includes) != 1 || spec.includes[0] != "compositions/resources" {
		t.Errorf("parseDtttc: %+v, %v", spec, err)
	}
	en := &engine{inPlace: "/x", includes: spec.includes}
	if p, ok := en.included("resources/robot-hello.control.yaml"); !ok || p != compositionsRoot+"/resources/robot-hello.control.yaml" {
		t.Errorf("an included composition's own resources: %q %v", p, ok)
	}
	if _, ok := en.included("filesystem-json.dtt.yaml"); ok {
		t.Error("a composition not included was found")
	}
}

func seq(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = i
	}
	return s
}
