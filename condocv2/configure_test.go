package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestConfigureSpecRoundTrip: the configuration in flight, the ones done and
// the ref each resolution followed survive the pointer file.
func TestConfigureSpecRoundTrip(t *testing.T) {
	spec := PointerSpec{
		Name: "Foo", StartTime: 42, ControlScheme: condocControlScheme, Branch: "condocv2/Foo-42/main",
		CondocRepo: "a/condocs", Targets: []string{"a/one"}, Phase: PhaseAwaitingPrompt, Interactions: 2,
		ReadOnly: []ReadOnlyRepo{{Repo: "b/lib", Ref: "next"}},
		ReadResolutions: []ReadResolution{
			{Repo: "b/lib", SHA: "l1", At: readAtBegin, Time: 100},
			{Repo: "b/lib", Ref: "next", SHA: "l2", Interaction: 2, At: readAtConfigure, Time: 200},
		},
		Configure: &RepoConfigure{Interaction: 2, Seq: 2, Repo: "b/lib", From: "next", Stage: configureApplied, SHA: "l1"},
		Configurations: []RepoConfiguration{
			{Interaction: 0, Seq: 1, Repo: "b/lib", To: "nope", Failed: "ref \"nope\" doesn't name a branch", At: "2026-11-01T00:00:00Z"},
			{Interaction: 2, Seq: 1, Repo: "b/lib", To: "next", SHA: "l2", At: "2026-11-01T01:00:00Z"},
		},
	}
	got, err := parsePointerSpec(spec.render())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Configure, spec.Configure) {
		t.Errorf("Configure = %+v, want %+v", got.Configure, spec.Configure)
	}
	if !reflect.DeepEqual(got.Configurations, spec.Configurations) {
		t.Errorf("Configurations = %+v, want %+v", got.Configurations, spec.Configurations)
	}
	if !reflect.DeepEqual(got.ReadResolutions, spec.ReadResolutions) {
		t.Errorf("ReadResolutions = %+v, want %+v", got.ReadResolutions, spec.ReadResolutions)
	}
}

// TestConfigurable: repos are configured only between interactions, never
// over a lock, a running sub-iteration, an error or another configuration.
func TestConfigurable(t *testing.T) {
	for _, ok := range []PointerSpec{
		{Phase: PhaseProposed},
		{Phase: PhaseAwaitingPrompt},
		{Phase: PhaseReviewing, Hold: &ProposalHold{Interaction: 1}},
	} {
		if why := ok.configurable(); why != "" {
			t.Errorf("%s: not configurable: %s", ok.Phase, why)
		}
	}
	for _, no := range []PointerSpec{
		{Phase: PhaseLocked, Lock: &InteractionLock{Interaction: 1}},
		{Phase: PhaseReviewing, Hold: &ProposalHold{Interaction: 1, Call: &ToolCall{Sub: 1}}},
		{Phase: PhaseAwaitingPrompt, Error: "boom"},
		{Phase: PhaseAwaitingPrompt, Configure: &RepoConfigure{Interaction: 1, Seq: 1}},
		{Phase: PhasePROpen},
		{Phase: PhaseAbandoned},
	} {
		if no.configurable() == "" {
			t.Errorf("%+v is configurable", no)
		}
	}
}

// TestConfigureSeq: c counts the configurations after each interaction.
func TestConfigureSeq(t *testing.T) {
	spec := PointerSpec{Configurations: []RepoConfiguration{{Interaction: 0, Seq: 1}, {Interaction: 2, Seq: 1}, {Interaction: 2, Seq: 2}}}
	if c := spec.nextConfigureSeq(2); c != 3 {
		t.Errorf("after 2: %d", c)
	}
	if c := spec.nextConfigureSeq(1); c != 1 {
		t.Errorf("after 1: %d", c)
	}
	if got := subCommitMsg(&PointerSpec{Name: "Foo"}, "configure-lock", 0, 1); got != "condocv2: Foo configure-lock 0.1" {
		t.Errorf("commit message %q", got)
	}
	if got := configurationLine(2, 1, configureRef, "b/lib", "", "next", "boom"); got != "2.1 b/lib: the default branch -> next (failed: boom)" {
		t.Errorf("line %q", got)
	}
	if got := configurationLine(3, 1, configureAdd, "c/docs", "", "", ""); got != "3.1 c/docs: added, following the default branch" {
		t.Errorf("add line %q", got)
	}
}

// TestAddableReadOnly: a repo added mid-condoc follows the proposal form's
// rules -- never a target, the condoc repo, or a repo already read-only.
func TestAddableReadOnly(t *testing.T) {
	spec := &PointerSpec{Name: "Foo", CondocRepo: "a/condocs", Targets: []string{"a/one"}, ReadOnly: []ReadOnlyRepo{{Repo: "b/lib"}}}
	if why := spec.addableReadOnly("c/docs"); why != "" {
		t.Errorf("c/docs: %s", why)
	}
	for _, no := range []string{"A/One", "a/condocs", "B/lib"} {
		if spec.addableReadOnly(no) == "" {
			t.Errorf("%s is addable", no)
		}
	}
}

// TestUnlockConfigureAdd: an add's unlock puts the repo into readOnly with its
// ref and first commit; a failed one leaves the repos as they were. Both are
// kept in configurations, and survive the pointer file.
func TestUnlockConfigureAdd(t *testing.T) {
	spec := &PointerSpec{
		Name: "Foo", StartTime: 42, ControlScheme: condocControlScheme, Branch: "condocv2/Foo-42/main",
		CondocRepo: "a/condocs", Targets: []string{"a/one"}, Phase: PhaseAwaitingPrompt, Interactions: 3,
		ReadOnly: []ReadOnlyRepo{{Repo: "b/lib"}},
	}
	now := time.Unix(1000, 0)
	for _, c := range []*RepoConfigure{
		{Interaction: 3, Seq: 1, Kind: configureAdd, Repo: "c/gone", Stage: configureApplied, Failed: "no such repo"},
		{Interaction: 3, Seq: 2, Kind: configureAdd, Repo: "c/docs", To: "v2", Stage: configureApplied, SHA: "d1"},
	} {
		spec.Configure = c
		spec.takeConfigure(now)
	}
	if want := []ReadOnlyRepo{{Repo: "b/lib"}, {Repo: "c/docs", Ref: "v2"}}; !reflect.DeepEqual(spec.ReadOnly, want) {
		t.Errorf("ReadOnly = %+v, want %+v", spec.ReadOnly, want)
	}
	if want := []ReadResolution{{Repo: "c/docs", Ref: "v2", SHA: "d1", Interaction: 3, At: readAtConfigure, Time: 1000}}; !reflect.DeepEqual(spec.ReadResolutions, want) {
		t.Errorf("ReadResolutions = %+v, want %+v", spec.ReadResolutions, want)
	}
	if len(spec.Configurations) != 2 || spec.Configurations[0].Failed == "" || spec.Configurations[1].Kind != configureAdd {
		t.Errorf("Configurations = %+v", spec.Configurations)
	}
	spec.Configure = &RepoConfigure{Interaction: 3, Seq: 3, Kind: configureAdd, Repo: "c/more", Stage: configureLocked}
	got, err := parsePointerSpec(spec.render())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.ReadOnly, spec.ReadOnly) || !reflect.DeepEqual(got.Configurations, spec.Configurations) || !reflect.DeepEqual(got.Configure, spec.Configure) {
		t.Errorf("round trip: %+v / %+v / %+v", got.ReadOnly, got.Configurations, got.Configure)
	}
}

// TestConfigureTimeline: a configure's new commit comes after its
// interaction, and its node says the ref it followed then.
func TestConfigureTimeline(t *testing.T) {
	spec := &PointerSpec{
		Name:     "Foo",
		ReadOnly: []ReadOnlyRepo{{Repo: "b/lib", Ref: "next"}},
		ReadResolutions: []ReadResolution{
			{Repo: "b/lib", SHA: "l1", At: readAtBegin, Time: 100},
			{Repo: "b/lib", Ref: "next", SHA: "l2", Interaction: 1, At: readAtConfigure, Time: 500},
		},
	}
	events := append(readResolutionEvents(spec),
		TimelineEvent{Lane: "condoc", Kind: "response", Interaction: 1, Time: 250},
		TimelineEvent{Lane: "condoc", Kind: "prompt", Interaction: 2, Time: 600},
	)
	sortTimeline(events)
	var order []string
	for _, e := range events {
		order = append(order, e.Kind+":"+e.SHA)
	}
	if want := []string{"resolve:l1", "response:", "resolve:l2", "prompt:"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	if !strings.Contains(events[2].Subject, "next resolved") {
		t.Errorf("subject %q", events[2].Subject)
	}
}
