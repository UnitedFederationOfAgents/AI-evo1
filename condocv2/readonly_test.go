package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	dktypes "dungeon-keeper/pkg/types"
)

// TestReadOnlyProposal: read-only repos are owner/name[@ref], and a repo is
// either changed (a target, the condoc repo) or read-only, once.
func TestReadOnlyProposal(t *testing.T) {
	p := SlopspaceProposal{Name: "Foo", Targets: []string{"a/one"}, CondocRepo: "a/condocs",
		ReadOnly: []string{"b/lib", " https://github.com/b/docs.git@v1.2 ", ""}}
	spec := PointerSpec{StartTime: 42}
	if err := validateProposal(p, &spec); err != nil {
		t.Fatal(err)
	}
	want := []ReadOnlyRepo{{Repo: "b/lib"}, {Repo: "b/docs", Ref: "v1.2"}}
	if !reflect.DeepEqual(spec.ReadOnly, want) {
		t.Errorf("ReadOnly = %+v", spec.ReadOnly)
	}
	if got := readOnlyStrings(spec.ReadOnly); !reflect.DeepEqual(got, []string{"b/lib", "b/docs@v1.2"}) {
		t.Errorf("readOnlyStrings = %v", got)
	}

	for _, bad := range [][]string{
		{"a/one"},                 // a target
		{"a/condocs"},             // the condoc repo
		{"b/lib", "B/Lib"},        // twice
		{"nope"},                  // not owner/name
		{"b/lib@--upload-pack=x"}, // not a ref
	} {
		p.ReadOnly = bad
		if err := validateProposal(p, &PointerSpec{StartTime: 42}); err == nil {
			t.Errorf("read-only %v was accepted", bad)
		}
	}
}

// TestReadOnlySpecRoundTrip: the read-only repos and their resolutions
// survive the pointer file.
func TestReadOnlySpecRoundTrip(t *testing.T) {
	spec := PointerSpec{
		Name: "Foo", StartTime: 42, ControlScheme: condocControlScheme, Branch: "condocv2/Foo-42/main",
		CondocRepo: "a/condocs", Targets: []string{"a/one"}, Phase: PhaseAwaitingPrompt,
		ReadOnly: []ReadOnlyRepo{{Repo: "b/lib"}, {Repo: "b/docs", Ref: "v1.2"}},
		ReadResolutions: []ReadResolution{
			{Repo: "b/lib", SHA: "l1", At: readAtBegin, Time: 100},
			{Repo: "b/docs", SHA: "d1", At: readAtBegin, Time: 100},
			{Repo: "b/lib", SHA: "l2", Interaction: 1, At: readAtDeploy, Time: 200},
		},
	}
	got, err := parsePointerSpec(spec.render())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.ReadOnly, spec.ReadOnly) || !reflect.DeepEqual(got.ReadResolutions, spec.ReadResolutions) {
		t.Errorf("round trip:\n got %+v %+v\nwant %+v %+v", got.ReadOnly, got.ReadResolutions, spec.ReadOnly, spec.ReadResolutions)
	}
}

// TestAppendReadResolutions records only new commits of the condoc's own
// read-only repos, so calling it again (a resumed stage) adds nothing.
func TestAppendReadResolutions(t *testing.T) {
	spec := PointerSpec{
		ReadOnly:        []ReadOnlyRepo{{Repo: "b/lib"}, {Repo: "b/docs"}},
		ReadResolutions: []ReadResolution{{Repo: "b/lib", SHA: "l1", At: readAtBegin}, {Repo: "b/docs", SHA: "d1", At: readAtBegin}},
	}
	at := time.Unix(300, 0)
	repos := []dktypes.ReadspaceRepo{
		{Repo: "b/lib", SHA: "l2", ResolvedAt: at},
		{Repo: "b/docs", SHA: "d1", ResolvedAt: at},
		{Repo: "c/other", SHA: "o1", ResolvedAt: at},
	}
	if !appendReadResolutions(&spec, repos, 2, readAtReturn) {
		t.Fatal("no new commit recorded")
	}
	if appendReadResolutions(&spec, repos, 2, readAtReturn) {
		t.Error("the same commits were recorded twice")
	}
	want := ReadResolution{Repo: "b/lib", SHA: "l2", Interaction: 2, At: readAtReturn, Time: 300}
	if n := len(spec.ReadResolutions); n != 3 || spec.ReadResolutions[2] != want {
		t.Errorf("resolutions = %+v", spec.ReadResolutions)
	}

	note := readOnlyRepoNote("/agent/w/readspaces/repos", repos[:1])
	if !strings.Contains(note, "/agent/w/readspaces/repos") || !strings.Contains(note, "b/lib (its default branch, at l2)") {
		t.Errorf("note = %q", note)
	}
	if readOnlyRepoNote("/x", nil) != "" {
		t.Error("a note without read-only repos")
	}
}

// TestReadOnlyTimeline: each resolution is a resolve node in its repo's
// lane, ordered with begin or its interaction, and the record draws it as a
// dotted circle in a read-only lane.
func TestReadOnlyTimeline(t *testing.T) {
	spec := &PointerSpec{
		Name:     "Foo",
		ReadOnly: []ReadOnlyRepo{{Repo: "b/lib", Ref: "main"}},
		ReadResolutions: []ReadResolution{
			{Repo: "b/lib", SHA: "l1", At: readAtBegin, Time: 100},
			{Repo: "b/lib", SHA: "l3", Interaction: 1, At: readAtReturn, Time: 300},
			{Repo: "b/lib", SHA: "l2", Interaction: 1, At: readAtDeploy, Time: 200},
			{Repo: "gone/repo", SHA: "x", At: readAtBegin},
		},
	}
	events := append(readResolutionEvents(spec),
		TimelineEvent{Lane: "condoc", Kind: "record", Interaction: 1, Time: 400},
		TimelineEvent{Lane: "condoc", Kind: "response", Interaction: 1, Time: 250},
		TimelineEvent{Lane: "condoc", Kind: "prompt", Interaction: 1, Time: 150},
		TimelineEvent{Lane: "condoc", Kind: "begin", Time: 50},
	)
	sortTimeline(events)
	var order []string
	for _, e := range events {
		order = append(order, e.Kind+":"+e.SHA)
	}
	if want := []string{"begin:", "resolve:l1", "prompt:", "resolve:l2", "response:", "resolve:l3", "record:"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	for _, e := range events {
		if e.Kind == "resolve" && (e.Lane != "b/lib" || e.URL != commitURL("b/lib", e.SHA)) {
			t.Errorf("resolve = %+v", e)
		}
	}

	tl := Timeline{
		Lanes: []TimelineLane{
			{ID: "pointer", Repo: "a/pointers", Role: "pointer"},
			{ID: "condoc", Repo: "a/condocs", Role: "condoc"},
			{ID: "b/lib", Repo: "b/lib", Role: "readonly"},
		},
		Events: events,
	}
	svg := swimlaneSVG(tl, nil)
	for _, want := range []string{"read-only</text>", `stroke-dasharray="2 3"`, commitURL("b/lib", "l2")} {
		if !strings.Contains(svg, want) {
			t.Errorf("record svg lacks %q", want)
		}
	}
}
