package main

import (
	"reflect"
	"strings"
	"testing"
)

// TestExternalSpecRoundTrip: the external changes survive the pointer file.
func TestExternalSpecRoundTrip(t *testing.T) {
	spec := PointerSpec{
		Name: "Foo", StartTime: 42, ControlScheme: condocControlScheme, Branch: "condocv2/Foo-42/main",
		CondocRepo: "a/condocs", Targets: []string{"a/one"}, Phase: PhaseAwaitingPrompt, Interactions: 2,
		Externals: []ExternalChange{
			{Interaction: 0, Seq: 1, Repo: "a/one", From: "f1", To: "t1", Commits: 2, At: externalAtRefresh, Time: 100},
			{Interaction: 2, Seq: 1, Repo: "a/condocs", From: "f2", To: "t2", Commits: 1, At: externalAtPrompt, Time: 200},
		},
	}
	got, err := parsePointerSpec(spec.render())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Externals, spec.Externals) {
		t.Errorf("Externals = %+v, want %+v", got.Externals, spec.Externals)
	}
}

// TestRefreshable: remotes are refreshed only between interactions of a
// begun condoc.
func TestRefreshable(t *testing.T) {
	for _, ok := range []PointerSpec{
		{Phase: PhaseAwaitingPrompt, SlopspaceID: "s"},
		{Phase: PhaseReviewing, SlopspaceID: "s", Hold: &ProposalHold{Interaction: 1}},
	} {
		if why := ok.refreshable(); why != "" {
			t.Errorf("%s: not refreshable: %s", ok.Phase, why)
		}
	}
	for _, no := range []PointerSpec{
		{Phase: PhaseProposed},
		{Phase: PhaseLocked, SlopspaceID: "s", Lock: &InteractionLock{Interaction: 1}},
		{Phase: PhaseReviewing, SlopspaceID: "s", Hold: &ProposalHold{Interaction: 1, Call: &ToolCall{Sub: 1}}},
		{Phase: PhaseAwaitingPrompt, SlopspaceID: "s", Error: "boom"},
		{Phase: PhaseAwaitingPrompt, SlopspaceID: "s", Configure: &RepoConfigure{Interaction: 1, Seq: 1}},
		{Phase: PhasePROpen, SlopspaceID: "s"},
	} {
		if no.refreshable() == "" {
			t.Errorf("%+v is refreshable", no)
		}
	}
}

// TestExternalSeqAndRecord: e counts the external changes after each
// interaction, and the next interaction's record lists them.
func TestExternalSeqAndRecord(t *testing.T) {
	spec := PointerSpec{Externals: []ExternalChange{
		{Interaction: 1, Seq: 1, Repo: "a/one", From: "aaaaaaa1", To: "bbbbbbb2", Commits: 3, At: externalAtRefresh},
		{Interaction: 1, Seq: 1, Repo: "a/condocs", From: "ccccccc3", To: "ddddddd4", Commits: 1, At: externalAtRefresh},
		{Interaction: 1, Seq: 2, Repo: "a/one", From: "bbbbbbb2", To: "eeeeeee5", At: externalAtPrompt},
	}}
	if e := spec.nextExternalSeq(1); e != 3 {
		t.Errorf("after 1: %d", e)
	}
	if e := spec.nextExternalSeq(2); e != 1 {
		t.Errorf("after 2: %d", e)
	}
	if got := len(spec.externalsAfter(1)); got != 3 {
		t.Errorf("externalsAfter(1) = %d", got)
	}
	rec := externalRecord(spec.externalsAfter(1))
	for _, want := range []string{"- a/one: ", "3 commits (external 1.1)", "1 commit (external 1.1)", "(external 1.2)"} {
		if !strings.Contains(rec, want) {
			t.Errorf("record %q lacks %q", rec, want)
		}
	}
	if externalRecord(nil) != "" {
		t.Error("a record with no external changes says something")
	}
	if got := externalLine(spec.Externals[2]); got != "1.2 a/one: commits (before the next prompt)" {
		t.Errorf("line %q", got)
	}
}

// TestExternalTimeline: an external change is its own section, labelled
// "external", between the interaction it followed and the next, with the
// pointer's commit and each moved lane's node on its row.
func TestExternalTimeline(t *testing.T) {
	spec := &PointerSpec{
		Name: "Foo", CondocRepo: "a/condocs", Targets: []string{"a/one"},
		Externals: []ExternalChange{
			{Interaction: 1, Seq: 1, Repo: "a/one", To: "t1", Commits: 2, At: externalAtRefresh, Time: 300},
			{Interaction: 1, Seq: 1, Repo: "a/condocs", To: "t2", Commits: 1, At: externalAtRefresh, Time: 300},
		},
	}
	pointer := timelineEvents("Foo", "pointer", "p/ptr", []gitCommit{
		{sha: "p1", time: 100, subject: "condocv2: Foo lock 1"},
		{sha: "p2", time: 200, subject: "condocv2: Foo unlock 1"},
		{sha: "p3", time: 300, subject: "condocv2: Foo external 1.1"},
		{sha: "p4", time: 400, subject: "condocv2: Foo lock 2"},
	}, "lock", "unlock", "external")
	if e := pointer[2]; e.Kind != "external" || e.Interaction != 1 || e.Seq != 1 || e.Sub != 0 {
		t.Fatalf("pointer external event %+v", e)
	}
	events := append(pointer, externalEvents(spec)...)
	sortTimeline(events)
	var order []string
	for _, e := range events {
		order = append(order, e.Kind+":"+e.Lane)
	}
	want := []string{"lock:pointer", "unlock:pointer", "external:pointer", "external:a/one", "external:condoc", "lock:pointer"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}

	l := newSwimLayout(events, nil)
	row := l.external[[2]int{1, 1}]
	if row != l.base[1]+3 || l.base[2] != row+1 {
		t.Errorf("external row %d, interaction 1 at %d, 2 at %d", row, l.base[1], l.base[2])
	}
	found := false
	for _, s := range l.sections {
		if s.label == "external" && s.row == row && s.solid {
			found = true
		}
	}
	if !found {
		t.Errorf("no external section at row %d: %+v", row, l.sections)
	}
	for _, e := range events {
		if e.Kind == "external" && l.eventRow(e) != row {
			t.Errorf("%s's external node on row %d, want %d", e.Lane, l.eventRow(e), row)
		}
	}
	svg := swimlaneSVG(Timeline{Lanes: []TimelineLane{{ID: "pointer"}, {ID: "condoc"}, {ID: "a/one"}}, Events: events}, nil)
	if !strings.Contains(svg, ">external</text>") || !strings.Contains(svg, "3 commits · one, condoc") {
		t.Errorf("the record's swimlanes don't show the external change")
	}
}
