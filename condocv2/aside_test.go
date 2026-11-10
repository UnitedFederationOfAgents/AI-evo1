package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAsideHistory(t *testing.T) {
	s := newServer()
	s.slop.asidesDir = t.TempDir()
	stem := "Cats-1700000000"
	for _, a := range []Aside{
		{Number: 2, Stem: stem, Interaction: 3, Prompt: "why?", Status: asideCompleted, AskedAt: 20, StartedAt: 21, CompletedAt: 30, Reply: "because"},
		{Number: 1, Stem: stem, Interaction: 1, Step: "1", Prompt: "what?", Status: asideFailed, AskedAt: 10, Reply: "(the agent run failed)"},
		{Number: 3, Stem: stem, Interaction: 3, Prompt: "and then?", Status: asideQueued, AskedAt: 40},
	} {
		a := a
		if err := s.saveAside(&a); err != nil {
			t.Fatal(err)
		}
	}
	// Junk in the dir is skipped.
	os.WriteFile(filepath.Join(s.asideDir(stem), "notes.txt"), []byte("x"), 0o644)

	got := s.asides(stem)
	if len(got) != 3 || got[0].Number != 1 || got[1].Number != 2 || got[2].Number != 3 {
		t.Fatalf("asides = %+v", got)
	}
	if s.asides("Other-1") != nil {
		t.Fatal("another condoc's asides should be none")
	}

	done := s.doneAsidesBefore(stem, 3)
	if len(done) != 2 {
		t.Fatalf("done before 3 = %+v", done)
	}
	dir := filepath.Join(t.TempDir(), asideReadDir)
	if err := writeAsideFiles(dir, done); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "Aside-2.md"))
	if err != nil {
		t.Fatal(err)
	}
	if md := string(b); !strings.Contains(md, "# Aside 2") || !strings.Contains(md, "after interaction 3") ||
		!strings.Contains(md, "why?") || !strings.Contains(md, "because") {
		t.Fatalf("Aside-2.md = %q", md)
	}
	if note := asidesNote(dir, 2); !strings.Contains(note, dir) {
		t.Fatalf("note = %q", note)
	}
	if asidesNote(dir, 0) != "" {
		t.Fatal("no asides, no note")
	}
}

// An aside deploys the slopspace, so it needs one that can be deployed.
func TestAsideBlocked(t *testing.T) {
	for _, c := range []struct {
		spec PointerSpec
		ok   bool
	}{
		{PointerSpec{SlopspaceID: "s1", Phase: PhaseAwaitingPrompt}, true},
		{PointerSpec{SlopspaceID: "s1", Phase: PhaseLocked}, true},
		{PointerSpec{SlopspaceID: "s1", Phase: PhaseReviewing}, false},
		{PointerSpec{SlopspaceID: "s1", Phase: PhasePRDrafting}, false},
		{PointerSpec{Phase: PhaseAwaitingPrompt}, false},
	} {
		if why := asideBlocked(&c.spec); (why == "") != c.ok {
			t.Errorf("asideBlocked(%s, slopspace %q) = %q", c.spec.Phase, c.spec.SlopspaceID, why)
		}
	}
}

func TestAsideRepoNote(t *testing.T) {
	note := asideRepoNote("/deploy/writespaces/repos", []string{"o/a", "o/b"}, "o/condocs")
	for _, want := range []string{"/deploy/writespaces/repos", "o/a, o/b", "o/condocs", "change nothing"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q lacks %q", note, want)
		}
	}
}

func TestAsidesOff(t *testing.T) {
	s := newServer()
	if s.asides("Cats-1") != nil {
		t.Fatal("asides are off without a dir")
	}
	if err := s.slopAside("Cats-1", "why?"); err == nil {
		t.Fatal("an aside with asides off should be refused")
	}
}

func TestAsideOps(t *testing.T) {
	ops := asideOps([]Aside{
		{Number: 1, Status: asideCompleted, AskedAt: 100, StartedAt: 105, CompletedAt: 200, Role: "condocv2-aside-Cats-1", SignalAt: 105, Prompt: "why?\nmore"},
		{Number: 2, Step: "1", Status: asideQueued, AskedAt: 300, Prompt: "and?"},
	})
	if len(ops) != 2 || ops[0].Kind != "aside" || ops[0].Title != "aside" {
		t.Fatalf("ops = %+v", ops)
	}
	if ops[0].Start != 105 || ops[0].End != 200 || ops[0].Signal == "" || !strings.Contains(ops[0].Detail, "why?") || strings.Contains(ops[0].Detail, "more") {
		t.Fatalf("op 1 = %+v", ops[0])
	}
	if ops[1].Start != 300 || ops[1].Status != "pending" || ops[1].Step != "1" || ops[1].Signal != "" {
		t.Fatalf("op 2 = %+v", ops[1])
	}

	// An aside hangs off its own scope's last node before it; an agent off
	// whichever was last.
	tl := Timeline{Events: []TimelineEvent{
		{Lane: "condoc", Kind: "record", Interaction: 1, Time: 50},
		{Lane: "condoc", Kind: "record", Interaction: 2, Time: 250, Step: "1"},
		{Lane: "condoc", Kind: "record", Interaction: 3, Time: 280},
	}}
	all := append(ops, LocalOperation{Kind: "agent", Start: 290})
	anchorOps(all, tl)
	if all[0].Interaction != 1 || all[0].AnchorKind != "record" {
		t.Fatalf("aside 1 anchored at %+v", all[0])
	}
	if all[1].Interaction != 2 {
		t.Fatalf("aside 2 (step 1) anchored at %+v", all[1])
	}
	if all[2].Interaction != 3 {
		t.Fatalf("agent anchored at %+v", all[2])
	}
}
