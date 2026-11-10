package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStepPaths(t *testing.T) {
	for path, want := range map[string]string{"1": "", "1-2": "1", "1-2-3": "1-2", "": ""} {
		if got := stepParent(path); got != want {
			t.Errorf("stepParent(%q) = %q, want %q", path, got, want)
		}
	}
	if got := stepAncestors("1-2-3"); !reflect.DeepEqual(got, []string{"", "1", "1-2"}) {
		t.Errorf("stepAncestors = %q", got)
	}
	if got := stepDepth("1-2"); got != 2 {
		t.Errorf("stepDepth = %d", got)
	}

	spec := PointerSpec{}
	if got := spec.nextStepPath(); got != "1" {
		t.Errorf("first step = %q, want 1", got)
	}
	spec.Steps = []StepScope{{Path: "1", Completed: true}}
	if got := spec.nextStepPath(); got != "2" {
		t.Errorf("second step = %q, want 2", got)
	}
	spec.Steps = append(spec.Steps, StepScope{Path: "2"}, StepScope{Path: "2-1", Completed: true})
	spec.Step = "2"
	if got := spec.nextStepPath(); got != "2-2" {
		t.Errorf("second substep of 2 = %q, want 2-2", got)
	}
	spec.Step = "2-2"
	if got := spec.nextStepPath(); got != "2-2-1" {
		t.Errorf("first substep of 2-2 = %q, want 2-2-1", got)
	}
}

// stepSpec: interactions 1-2 in the main scope; step 1 entered after 2, with
// 3 and 5 in it and substep 1-1 (entered after 3, left after 4) holding 4;
// back out to the main scope after 5 for 6.
func stepSpec() PointerSpec {
	return PointerSpec{
		Interactions: 6,
		Steps: []StepScope{
			{Path: "1", From: 2, EnteredAt: 100, Completed: true, To: 5, CompletedAt: 500},
			{Path: "1-1", From: 3, EnteredAt: 300, Completed: true, To: 4, CompletedAt: 400},
		},
	}
}

func TestInteractionScope(t *testing.T) {
	spec := stepSpec()
	want := map[int]string{1: "", 2: "", 3: "1", 4: "1-1", 5: "1", 6: ""}
	for n, scope := range want {
		if got := spec.interactionScope(n); got != scope {
			t.Errorf("interaction %d is in %q, want %q", n, got, scope)
		}
	}
	// An external change after interaction 2: before step 1 was entered, in
	// the main scope; after, in step 1.
	if got := spec.scopeAt(2, 50); got != "" {
		t.Errorf("scopeAt(2, 50) = %q", got)
	}
	if got := spec.scopeAt(2, 150); got != "1" {
		t.Errorf("scopeAt(2, 150) = %q", got)
	}
}

func TestStepTimeline(t *testing.T) {
	spec := stepSpec()
	spec.Name = "Foo"
	commits := []gitCommit{
		{sha: "a", time: 100, subject: "condocv2: Foo step 1"},
		{sha: "b", time: 300, subject: "condocv2: Foo step 1-1"},
		{sha: "c", time: 400, subject: "condocv2: Foo step-complete 1-1"},
		{sha: "d", time: 250, subject: "condocv2: Foo prompt 3"},
	}
	events := timelineEvents("Foo", "condoc", "o/c", commits, "step", "step-complete", "prompt")
	tl := spec.scopeTimeline(Timeline{Events: events})
	got := map[string][3]any{}
	for _, e := range tl.Events {
		got[e.SHA] = [3]any{e.Path, e.Interaction, e.Step}
	}
	want := map[string][3]any{
		"a": {"1", 2, ""},    // entered from the main scope, after 2
		"b": {"1-1", 3, "1"}, // entered from step 1, after 3
		"c": {"1-1", 4, "1"}, // left back to step 1, after 4
		"d": {"", 3, "1"},    // interaction 3 is in step 1
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events:\n got %v\nwant %v", got, want)
	}
}

func TestStepSpecRoundTrip(t *testing.T) {
	spec := PointerSpec{Name: "Foo", StartTime: 1, ControlScheme: condocControlScheme, Branch: "condocv2/x", CondocRepo: "o/c",
		Phase: PhaseAwaitingPrompt, Interactions: 4, Step: "1-1",
		Steps: []StepScope{
			{Path: "1", Title: "Do: the thing", From: 2, EnteredAt: 100},
			{Path: "1-1", From: 3, EnteredAt: 300},
			{Path: "2", From: 0, EnteredAt: 50, Completed: true, To: 1, CompletedAt: 60},
		}}
	got, err := parsePointerSpec(spec.render())
	if err != nil {
		t.Fatal(err)
	}
	if got.Step != spec.Step || !reflect.DeepEqual(got.Steps, spec.Steps) {
		t.Errorf("round trip: step %q, steps %+v", got.Step, got.Steps)
	}
}

func TestScopeFiles(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "condocs", "Foo.md")
	if got := scopeFilePath(main, "1-2"); got != filepath.Join(dir, "condocs", "fooImpls", "Step-1-2.md") {
		t.Errorf("scopeFilePath = %q", got)
	}
	spec := stepSpec()
	files := map[string]string{
		stepFilePath(main):          initialStepFile("Foo") + "\n## Prompt 1\n\none\n\n## Prompt 2\n\ntwo\n\n## Step 1\n\n[Step 1](Step-1.md)\n\n## Step 1 Completed\n\ndone\n\n## Prompt 6\n\nsix\n",
		scopeFilePath(main, "1"):   initialScopeFile("1", "", "first") + "\n## Prompt 3\n\nthree\n\n## Response 3\n\nok\n\n## Step 1-1\n\n[Step 1-1](Step-1-1.md)\n\n## Prompt 5\n\nfive\n",
		scopeFilePath(main, "1-1"): initialScopeFile("1-1", "1", "") + "\n## Prompt 4\n\nfour\n",
	}
	read := func(path string) (string, error) {
		if c, ok := files[path]; ok {
			return c, nil
		}
		t.Fatalf("read %s", path)
		return "", nil
	}
	its, mainContent, err := readScopeInteractions(&spec, main, read)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mainContent, "## Prompt 1") {
		t.Errorf("main content: %q", mainContent)
	}
	var got []string
	for _, it := range its {
		got = append(got, it.Prompt+"@"+it.Step)
	}
	want := []string{"one@", "two@", "three@1", "four@1-1", "five@1", "six@"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("interactions = %q, want %q", got, want)
	}
	// A step heading ends the interaction before it.
	if its[2].Response != "ok" {
		t.Errorf("response 3 = %q", its[2].Response)
	}
}

func TestAppendStepSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Step1.md")
	if err := writeFileString(path, initialStepFile("Foo")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // a retried step commit: already there
		if err := appendStepSection(path, "1", false, "[Step 1](Step-1.md)"); err != nil {
			t.Fatal(err)
		}
	}
	if err := appendStepSection(path, "1", true, "done"); err != nil {
		t.Fatal(err)
	}
	content, _ := readFileString(path)
	if strings.Count(content, "## Step 1\n") != 1 || strings.Count(content, "## Step 1 Completed\n") != 1 {
		t.Errorf("content:\n%s", content)
	}
}

func TestStepBlocked(t *testing.T) {
	spec := PointerSpec{Phase: PhaseAwaitingPrompt, SlopspaceID: "s"}
	if why := spec.stepBlocked(); why != "" {
		t.Errorf("idle condoc blocked: %s", why)
	}
	spec.Lock = &InteractionLock{Interaction: 3}
	if why := spec.stepBlocked(); !strings.Contains(why, "interaction 3") {
		t.Errorf("locked condoc: %q", why)
	}
	spec.Lock, spec.Hold = nil, &ProposalHold{Interaction: 2}
	if why := spec.stepBlocked(); why == "" {
		t.Errorf("held condoc not blocked")
	}
}

func TestSwimlaneSteps(t *testing.T) {
	spec := stepSpec()
	tl := spec.scopeTimeline(Timeline{
		Lanes: []TimelineLane{{ID: "pointer", Role: "pointer"}, {ID: "condoc", Role: "condoc"}},
		Events: []TimelineEvent{
			{Lane: "pointer", Kind: "step", Path: "1", Time: 101},
			{Lane: "condoc", Kind: "step", Path: "1", Time: 100},
			{Lane: "condoc", Kind: "step-complete", Path: "1", Time: 500},
			{Lane: "condoc", Kind: "prompt", Interaction: 3, Time: 200},
		},
	})
	sortTimeline(tl.Events)
	layout := newSwimLayout(tl.Events, nil)
	enter, done := layout.steps["step 1"], layout.steps["step-complete 1"]
	if enter == 0 || done == 0 || !(enter < layout.base[3] && layout.base[3] < done) {
		t.Errorf("rows: step 1 at %d, interaction 3 at %d, step 1 done at %d", enter, layout.base[3], done)
	}
	svg := swimlaneSVG(tl, nil)
	for _, want := range []string{"→", "←", "step 1 done"} {
		if !strings.Contains(svg, want) {
			t.Errorf("svg has no %q", want)
		}
	}
}
