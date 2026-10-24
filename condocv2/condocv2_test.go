package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidateProposalBranches(t *testing.T) {
	p := SlopspaceProposal{Name: "Foo", Targets: []string{"a/one"}, CondocRepo: "a/condocs"}
	spec := PointerSpec{StartTime: 42}
	if err := validateProposal(p, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Branch != "condocv2/Foo-42/main" || spec.ControlScheme != condocControlScheme {
		t.Errorf("branch %q scheme %q", spec.Branch, spec.ControlScheme)
	}

	// v1's branches are incompatible.
	p.Branch = "condoc/Foo-42/main"
	if err := validateProposal(p, &PointerSpec{StartTime: 42}); err == nil {
		t.Error("a condoc/ branch was accepted")
	}
	p.Branch = "condocv2/custom"
	if err := validateProposal(p, &PointerSpec{StartTime: 42}); err != nil {
		t.Errorf("condocv2/custom: %v", err)
	}
}

func TestInitialFilesAreV2Only(t *testing.T) {
	main := initialMainFile("Foo", 42, "condocv2/Foo-42/main", "a/pointers", "What it is for.")
	if !condocV2HeaderRe.MatchString(main) {
		t.Errorf("no condocv2-yaml header:\n%s", main)
	}
	// condoccer and federation-command recognize condoc-yaml only.
	if strings.Contains(main, "```condoc-yaml") {
		t.Errorf("main file carries a v1 header:\n%s", main)
	}
	if !strings.Contains(main, "[Step 1](fooImpls/Step1.md)") {
		t.Errorf("no step link:\n%s", main)
	}
	if got := stepFilePath("/r/condocs/Foo.md"); got != filepath.FromSlash("/r/condocs/fooImpls/Step1.md") {
		t.Errorf("stepFilePath = %q", got)
	}
}

func TestStepFileSections(t *testing.T) {
	step := filepath.Join(t.TempDir(), "Step1.md")
	if err := writeFileString(step, initialStepFile("Foo")); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(appendSection(step, "Prompt", 1, "Do the thing.\n## Response 1\nnot really"))
	must(appendSection(step, "Response", 1, "Done."))
	must(appendSection(step, "Response", 1, "Done twice?")) // a retried stage: already there
	must(appendSection(step, "Record", 1, recordBody([]string{"a/one", "a/two"}, []string{"a/one@1111111aaa", "a/two@2222222"}, []string{"a/one@3333333bbb", "a/two@2222222"})))
	must(appendSection(step, "Prompt", 2, "Next."))

	content, _ := readFileString(step)
	got := parseInteractions(content)
	if len(got) != 2 {
		t.Fatalf("interactions = %+v\n%s", got, content)
	}
	if got[0].Prompt != "Do the thing.\n\\## Response 1\nnot really" {
		t.Errorf("prompt 1 = %q", got[0].Prompt)
	}
	if got[0].Response != "Done." {
		t.Errorf("response 1 = %q", got[0].Response)
	}
	wantRecord := "- a/one: [`1111111`](https://github.com/a/one/commit/1111111aaa) → [`3333333`](https://github.com/a/one/commit/3333333bbb)\n- a/two: (no changes)"
	if got[0].Record != wantRecord {
		t.Errorf("record 1 = %q", got[0].Record)
	}
	if !reflect.DeepEqual(got[1], Interaction{N: 2, Prompt: "Next."}) {
		t.Errorf("interaction 2 = %+v", got[1])
	}
}

func TestTimelineEventsAndOrder(t *testing.T) {
	log := []byte(strings.Join([]string{
		"c1\x1f100\x1fcondocv2: Foo begin",
		"c2\x1f101\x1fcondocv2: Foo prompt 1",
		"c3\x1f102\x1fsomeone else's commit",
		"c4\x1f103\x1fcondocv2: Bar prompt 1",
		"c5\x1f104\x1fcondocv2: Foo response 1",
		"c6\x1f105\x1fcondocv2: Foo record 1",
		"c7\x1f106\x1fcondocv2: Foo lock 2",
	}, "\n"))
	events := timelineEvents("Foo", "condoc", "a/condocs", parseGitLog(log), "begin", "prompt", "response", "record")
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "begin,prompt,response,record" {
		t.Fatalf("kinds = %v", kinds)
	}
	if events[1].Interaction != 1 || events[1].URL != "https://github.com/a/condocs/commit/c2" {
		t.Errorf("prompt event = %+v", events[1])
	}

	// One interaction's slots run lock, prompt, response, commit, record,
	// unlock, all after begin and before the next interaction's lock.
	order := []TimelineEvent{
		{Kind: "propose"}, {Kind: "begin"},
		{Kind: "lock", Interaction: 1}, {Kind: "prompt", Interaction: 1}, {Kind: "response", Interaction: 1},
		{Kind: "commit", Interaction: 1}, {Kind: "record", Interaction: 1}, {Kind: "unlock", Interaction: 1},
		{Kind: "lock", Interaction: 2},
	}
	for i := 1; i < len(order); i++ {
		if eventSlot(order[i-1]) >= eventSlot(order[i]) {
			t.Errorf("slot(%+v) >= slot(%+v)", order[i-1], order[i])
		}
	}
}

func TestPendingEvents(t *testing.T) {
	spec := &PointerSpec{Targets: []string{"a/one", "a/two"}}
	if pendingEvents(spec) != nil {
		t.Error("pending events without a lock")
	}
	spec.Lock = &InteractionLock{Interaction: 3, Stage: stageSignalled}
	if p := pendingEvents(spec); len(p) != 1 || p[0].Lane != "condoc" || p[0].Kind != "response" || p[0].Interaction != 3 {
		t.Errorf("signalled: %+v", p)
	}
	spec.Lock.Stage = stageResponded
	if p := pendingEvents(spec); len(p) != 2 || p[0].Lane != "a/one" || p[1].Kind != "commit" {
		t.Errorf("responded: %+v", p)
	}
	spec.Lock.Stage = stageRecorded
	if p := pendingEvents(spec); len(p) != 1 || p[0].Lane != "pointer" || p[0].Kind != "unlock" {
		t.Errorf("recorded: %+v", p)
	}
}
