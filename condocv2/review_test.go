package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dktypes "dungeon-keeper/pkg/types"
)

func TestPointerSpecHoldRoundTrip(t *testing.T) {
	spec := PointerSpec{
		Name: "Foo", StartTime: 7, ControlScheme: condocControlScheme,
		Branch: "condocv2/Foo-7/main", CondocRepo: "a/condocs", CondocPath: "condocs/Foo.md",
		Targets: []string{"a/one"}, Phase: PhaseReviewing, Interactions: 2,
		Hold: &ProposalHold{
			Interaction: 2, Proposals: []string{"read/prs.dtt.yaml", "write/repo.dtt.yaml", "write/gone.dtt.yaml"},
			Done: []string{"read/prs.dtt.yaml"},
			Call: &ToolCall{Sub: 2, Proposal: "write/repo.dtt.yaml", Decision: "accept", Mode: "assert", Stage: toolRan,
				Image: "/s/readspaces/dtt-images/write/repo.image.json", Failed: "line 3: no such field\nand more"},
		},
	}
	got, err := parsePointerSpec(spec.render())
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, spec.render())
	}
	got.UpdatedAt = ""
	if !reflect.DeepEqual(got, spec) {
		t.Errorf("round trip:\n got %+v\nwant %+v\n hold %+v\n call %+v", got, spec, got.Hold, got.Hold.Call)
	}
	if p := spec.Hold.pending(); !reflect.DeepEqual(p, []string{"write/gone.dtt.yaml"}) {
		t.Errorf("pending = %v", p)
	}
	e := entryFromSpec(spec, "pointers/Foo-7.yaml")
	if e.HoldInteraction != 2 || e.HoldStage != toolRan || e.HoldSub != 2 || e.HoldDone != 1 || len(e.HoldProposals) != 1 || e.HoldMode != "assert" {
		t.Errorf("entry = %+v", e)
	}
	spec.Hold.Call = nil
	if e := entryFromSpec(spec, "pointers/Foo-7.yaml"); e.HoldStage != "waiting" || len(e.HoldProposals) != 2 {
		t.Errorf("waiting entry = %+v", e)
	}
}

func TestToolCallTimeline(t *testing.T) {
	log := []byte(strings.Join([]string{
		"c1\x1f100\x1fcondocv2: Foo response-dtt 2",
		"c2\x1f101\x1fcondocv2: Foo record 2",
		"c3\x1f102\x1fcondocv2: Foo dtt-read 2.1",
		"c4\x1f103\x1fcondocv2: Foo dtt-image 2.1",
		"c5\x1f104\x1fcondocv2: Foo dtt-reject 2.2",
		"c6\x1f105\x1fcondocv2: Foo dtt-assert 2.3",
		"c7\x1f106\x1fcondocv2: Foo dtt-failed 2.3",
		"c8\x1f107\x1fcondocv2: Foo review-accept 1",
	}, "\n"))
	events := timelineEvents("Foo", "condoc", "a/condocs", parseGitLog(log), "response", "record", "dtt", "result", "review")
	want := [][4]any{
		{"response", 2, 0, "dtt"}, {"record", 2, 0, ""}, {"dtt", 2, 1, "read"}, {"result", 2, 1, "ok"},
		{"dtt", 2, 2, "reject"}, {"dtt", 2, 3, "assert"}, {"result", 2, 3, "failed"}, {"review", 1, 0, "accept"},
	}
	if len(events) != len(want) {
		t.Fatalf("events = %+v", events)
	}
	for i, w := range want {
		if got := [4]any{events[i].Kind, events[i].Interaction, events[i].Sub, events[i].Note}; got != w {
			t.Errorf("event %d = %v, want %v", i, got, w)
		}
	}

	// A sub-iteration comes after its interaction's unlock, in order, and
	// before the next interaction's lock.
	order := []TimelineEvent{
		{Kind: "record", Interaction: 1}, {Kind: "unlock", Interaction: 1},
		{Kind: "lock", Interaction: 1, Sub: 1}, {Kind: "dtt", Interaction: 1, Sub: 1}, {Kind: "result", Interaction: 1, Sub: 1},
		{Kind: "unlock", Interaction: 1, Sub: 1}, {Kind: "lock", Interaction: 1, Sub: 2}, {Kind: "unlock", Interaction: 1, Sub: 12},
		{Kind: "lock", Interaction: 2}, {Kind: "unlock", Interaction: 40}, {Kind: "complete"}, {Kind: "merged"},
	}
	for i := 1; i < len(order); i++ {
		if eventSlot(order[i-1]) >= eventSlot(order[i]) {
			t.Errorf("slot(%+v) >= slot(%+v)", order[i-1], order[i])
		}
	}

	// Waiting for the human has no pending node (the record's "?" is it);
	// a sub-iteration in flight has one.
	spec := &PointerSpec{Hold: &ProposalHold{Interaction: 2, Proposals: []string{"read/a.dtt.yaml"}}}
	if p := pendingEvents(spec); p != nil {
		t.Errorf("waiting: %+v", p)
	}
	spec.Hold.Call = &ToolCall{Sub: 1, Proposal: "read/a.dtt.yaml", Decision: "accept", Mode: "read", Stage: toolProposed}
	if p := pendingEvents(spec); len(p) != 1 || p[0].Kind != "result" || p[0].Sub != 1 || p[0].Interaction != 2 {
		t.Errorf("running: %+v", p)
	}
	spec.Hold.Call.Decision = "reject"
	if p := pendingEvents(spec); len(p) != 1 || p[0].Lane != "pointer" || p[0].Kind != "unlock" || p[0].Sub != 1 {
		t.Errorf("rejected: %+v", p)
	}
}

func TestToolCallSections(t *testing.T) {
	step := filepath.Join(t.TempDir(), "Step1.md")
	if err := writeFileString(step, initialStepFile("Foo")); err != nil {
		t.Fatal(err)
	}
	read := &ToolCall{Sub: 1, Proposal: "read/prs.dtt.yaml", Decision: "accept", Mode: "read"}
	assert := &ToolCall{Sub: 2, Proposal: "write/repo.dtt.yaml", Decision: "accept", Mode: "assert", Failed: "boom"}
	reject := &ToolCall{Sub: 3, Proposal: "write/x.dtt.yaml", Decision: "reject"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(appendSection(step, "Prompt", 1, "Do it."))
	must(appendSection(step, "Response", 1, "Proposed."))
	must(appendSection(step, "Proposals", 1, "- [`read/prs.dtt.yaml`](dtt/1/read/prs.dtt.yaml)"))
	must(appendSubSection(step, "DTT", 1, 1, toolCallBody(read, 1, true)))
	must(appendSubSection(step, "Result", 1, 1, toolResultBody(read, "dtt/1/read/prs.image.json")))
	must(appendSubSection(step, "DTT", 1, 1, "again?")) // a retried stage: already there
	must(appendSubSection(step, "DTT", 1, 2, toolCallBody(assert, 1, false)))
	must(appendSubSection(step, "Result", 1, 2, toolResultBody(assert, "")))
	must(appendSubSection(step, "DTT", 1, 3, toolCallBody(reject, 1, true)))
	must(appendSection(step, "Prompt", 2, "Next."))

	content, _ := readFileString(step)
	got := parseInteractions(content)
	if len(got) != 2 || len(got[0].Tools) != 3 || got[0].Proposals == "" {
		t.Fatalf("interactions = %+v\n%s", got, content)
	}
	tools := got[0].Tools
	if !strings.Contains(tools[0].DTT, "**Accepted** [`read/prs.dtt.yaml`](dtt/1/read/prs.dtt.yaml)") || !strings.Contains(tools[0].DTT, "`dtt read`") {
		t.Errorf("DTT 1.1 = %q", tools[0].DTT)
	}
	if tools[0].Result != "✓ `dtt read` returned [its image](dtt/1/read/prs.image.json)." {
		t.Errorf("Result 1.1 = %q", tools[0].Result)
	}
	if !strings.Contains(tools[1].Result, "✗ `dtt assert` failed") || !strings.Contains(tools[1].Result, "> boom") {
		t.Errorf("Result 1.2 = %q", tools[1].Result)
	}
	if !strings.HasPrefix(tools[2].DTT, "**Rejected**") || tools[2].Result != "" {
		t.Errorf("1.3 = %+v", tools[2])
	}
	if got[1].Prompt != "Next." || got[1].Tools != nil {
		t.Errorf("interaction 2 = %+v", got[1])
	}
	if !proposalTaken(content, 1, "write/x.dtt.yaml") || proposalTaken(content, 1, "write/y.dtt.yaml") || proposalTaken(content, 2, "read/prs.dtt.yaml") {
		t.Error("proposalTaken")
	}
	if !validDecision("accept") || validDecision("maybe") {
		t.Error("validDecision")
	}
}

func TestNextDecided(t *testing.T) {
	h := &ProposalHold{Proposals: []string{"read/a", "read/b", "write/c"}, Done: []string{"read/a"}}
	d := &dktypes.Deployment{Proposals: h.Proposals, Decided: []dktypes.ProposalVerdict{
		{Proposal: "read/a", Decision: dktypes.DecisionAccept}, {Proposal: "write/c", Decision: dktypes.DecisionReject},
	}}
	if p, dec := nextDecided(h, d); p != "write/c" || dec != "reject" {
		t.Errorf("next = %q %q", p, dec)
	}
	h.Call = &ToolCall{Proposal: "write/c"}
	if p, _ := nextDecided(h, d); p != "" {
		t.Errorf("next with c in flight = %q, want none (b is undecided)", p)
	}
	// A whole-set answer on the slopspace tab covers the rest.
	h.Call = nil
	d.Decided, d.Decision = nil, dktypes.DecisionAccept
	if p, dec := nextDecided(h, d); p != "read/b" || dec != "accept" {
		t.Errorf("whole-set next = %q %q", p, dec)
	}
	d.Decision = dktypes.DecisionModify
	if p, _ := nextDecided(h, d); p != "" {
		t.Errorf("modify must not start a sub-iteration: %q", p)
	}
}

func TestAnchorOps(t *testing.T) {
	tl := Timeline{Events: []TimelineEvent{
		{Lane: "pointer", Kind: "propose", Time: 10},
		{Lane: "pointer", Kind: "lock", Interaction: 1, Time: 100},
		{Lane: "condoc", Kind: "prompt", Interaction: 1, Time: 101},
		{Lane: "condoc", Kind: "response", Interaction: 1, Time: 200},
		{Lane: "a/one", Kind: "commit", Interaction: 1, Time: 201}, // targets never anchor
		{Lane: "pointer", Kind: "lock", Interaction: 1, Sub: 1, Time: 300},
		{Lane: "condoc", Kind: "dtt", Interaction: 1, Sub: 1, Time: 301},
		{Lane: "condoc", Kind: "result", Interaction: 1, Sub: 1, Time: 310},
		{Lane: "condoc", Kind: "response", Interaction: 2, Pending: true},
	}}
	ops := []LocalOperation{{Kind: "agent", Start: 105}, {Kind: "dtt", Start: 305}, {Kind: "agent", Start: 5}, {Kind: "agent", Start: 100},
		{Kind: "dtt", Start: 300}, {Kind: "dtt", Start: 310}}
	anchorOps(ops, tl)
	want := [][4]any{{"condoc", "prompt", 1, 0}, {"condoc", "dtt", 1, 1}, {"pointer", "propose", 0, 0}, {"condoc", "prompt", 1, 0},
		{"condoc", "dtt", 1, 1}, {"condoc", "dtt", 1, 1}}
	for i, w := range want {
		if got := [4]any{ops[i].AnchorLane, ops[i].AnchorKind, ops[i].Interaction, ops[i].Sub}; got != w {
			t.Errorf("op %d anchored at %v, want %v", i, got, w)
		}
	}
}

// A write proposal read before it is decided: its own sub-iteration, which
// leaves it awaiting accept or reject.
func TestPreReadSubIteration(t *testing.T) {
	h := &ProposalHold{Interaction: 2, Proposals: []string{"write/repo.dtt.yaml", "read/prs.dtt.yaml"}, Done: []string{"read/prs.dtt.yaml"}}
	h.Call = &ToolCall{Sub: h.nextSub(), Proposal: "write/repo.dtt.yaml", Decision: decisionPreRead, Mode: "read", Stage: toolProposed}
	if h.Call.Sub != 2 || !h.Call.runs() {
		t.Errorf("call = %+v", h.Call)
	}
	if p := h.pending(); !reflect.DeepEqual(p, []string{"write/repo.dtt.yaml"}) {
		t.Errorf("a pre-read must not take its proposal: pending = %v", p)
	}
	spec := &PointerSpec{
		Name: "Foo", StartTime: 7, ControlScheme: condocControlScheme, Branch: "condocv2/Foo-7/main",
		CondocRepo: "a/condocs", Phase: PhaseReviewing, Interactions: 2, Hold: h,
	}
	if p := pendingEvents(spec); len(p) != 1 || p[0].Kind != "result" || p[0].Note != "running dtt read" {
		t.Errorf("running: %+v", p)
	}

	// Unlocked, it counts towards the numbering but not the answers.
	h.PreReads = append(h.PreReads, h.Call.Proposal)
	h.Call = nil
	got, err := parsePointerSpec(spec.render())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Hold.PreReads, []string{"write/repo.dtt.yaml"}) || got.Hold.nextSub() != 3 {
		t.Errorf("hold = %+v", got.Hold)
	}
	if e := entryFromSpec(*spec, "pointers/Foo-7.yaml"); e.HoldDone != 2 || len(e.HoldProposals) != 1 || len(e.HoldPreReads) != 1 {
		t.Errorf("entry = %+v", e)
	}

	// Its section doesn't answer the proposal.
	step := filepath.Join(t.TempDir(), "Step1.md")
	if err := writeFileString(step, initialStepFile("Foo")); err != nil {
		t.Fatal(err)
	}
	pre := &ToolCall{Sub: 2, Proposal: "write/repo.dtt.yaml", Decision: decisionPreRead, Mode: "read"}
	for _, err := range []error{
		appendSection(step, "Prompt", 2, "Do it."),
		appendSubSection(step, "DTT", 2, 2, toolCallBody(pre, 2, true)),
		appendSubSection(step, "Result", 2, 2, toolResultBody(pre, "dtt/2/write/repo.read.image.json")),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	content, _ := readFileString(step)
	if proposalTaken(content, 2, "write/repo.dtt.yaml") {
		t.Error("a pre-read must not count as the proposal's sub-iteration")
	}
	if kind, note := timelineKind("dtt-preread"); kind != "dtt" || note != "preread" {
		t.Errorf("timelineKind = %s %s", kind, note)
	}
}
