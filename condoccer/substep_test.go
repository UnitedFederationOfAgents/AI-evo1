package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newCompletedSubstepFixture lays out a condoc whose step (Step 1) has just
// had its one substep (Substep A) completed -- the substep file carries
// "## Substep Completed" and the step file has already been resumed (a fresh
// "## <REPLACE-Revision|Retry>" placeholder under a plain "Add the
// '!HANDOFF!' or '!COMPLETED!' directive." Human-Prompt, mirroring what
// updateStepFileAfterSubstepComplete writes -- see
// condocs/initialShellsSessionManagerAndTheConversationalistImpls/Step1Prompt.md
// Revision C).
func newCompletedSubstepFixture(t *testing.T) (root, mainRelPath string) {
	t.Helper()
	root = t.TempDir()
	condocDir := filepath.Join(root, "condocs")
	if err := os.MkdirAll(condocDir, 0755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(condocDir, "X.md")
	if err := os.WriteFile(mainPath, []byte("# X\n\n### Step 1 - Do it\n"), 0644); err != nil {
		t.Fatal(err)
	}
	implDirPath := filepath.Join(condocDir, "xImpls")
	if err := os.MkdirAll(implDirPath, 0755); err != nil {
		t.Fatal(err)
	}
	stepContent := "# Prompt\n\nDo something.\n\n" +
		"## Reply\n\nDone initial work.\n\n" +
		"## Substep A - Feedback tools\n\n" +
		"[Step 1 Substep A](Step1SubstepAPrompt.md)\n\n\n" +
		"## <REPLACE-Revision|Retry> B\n\n<REPLACE-PROMPT>\n\n\n" +
		"## Human-Prompt\n\nAdd the '!HANDOFF!' or '!COMPLETED!' directive.\n"
	if err := os.WriteFile(filepath.Join(implDirPath, "Step1Prompt.md"), []byte(stepContent), 0644); err != nil {
		t.Fatal(err)
	}
	substepContent := "# Prompt\n\n[Step1Prompt](Step1Prompt.md)\n\nDo the substep.\n\n" +
		"## Reply\n\nSubstep work done.\n\n\n" +
		"## Substep Completed\n\nThis substep was completed at 1 (whenever).\n"
	if err := os.WriteFile(filepath.Join(implDirPath, "Step1SubstepAPrompt.md"), []byte(substepContent), 0644); err != nil {
		t.Fatal(err)
	}
	return root, "condocs/X.md"
}

// TestStepLastEventIsSubstep verifies the signal detectPhase uses to tell
// "a substep just completed, nothing new has happened on the step's own
// timeline since" apart from an ordinary revision/retry reply having landed
// on the step itself.
func TestStepLastEventIsSubstep(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "no substep at all",
			content: "## Reply\n\nDone.\n\n## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>",
			want:    false,
		},
		{
			name: "substep is the last event",
			content: "## Reply\n\nDone.\n\n## Substep A - Title\n\n[link](x.md)\n\n" +
				"## <REPLACE-Revision|Retry> B\n\n<REPLACE-PROMPT>",
			want: true,
		},
		{
			name: "a reply landed on the step after the substep",
			content: "## Substep A - Title\n\n[link](x.md)\n\n## Reply B\n\nMore work.\n\n" +
				"## <REPLACE-Revision|Retry> C\n\n<REPLACE-PROMPT>",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stepLastEventIsSubstep(c.content); got != c.want {
				t.Errorf("stepLastEventIsSubstep(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

// TestDetectPhaseJustResumedFromSubstep verifies detectPhase flags the
// "resumed from a completed substep" case so updateCondocLock can withhold
// the lock's removal, per Step1Prompt.md Revision C.
func TestDetectPhaseJustResumedFromSubstep(t *testing.T) {
	root, mainRelPath := newCompletedSubstepFixture(t)
	info, err := detectPhase(root, filepath.Join(root, mainRelPath))
	if err != nil {
		t.Fatal(err)
	}
	if info.Phase != PhaseAwaitingAction {
		t.Fatalf("phase = %v, want %v", info.Phase, PhaseAwaitingAction)
	}
	if !info.JustResumedFromSubstep {
		t.Error("expected JustResumedFromSubstep = true right after a substep completes")
	}
	if info.SubstepFile != "" {
		t.Errorf("expected no active substep file, got %q", info.SubstepFile)
	}
}

// TestUpdateCondocLockWithholdsRemovalForSubstepCompletion verifies the
// Step1Prompt.md Revision C carve-out: reaching "awaiting action" because a
// substep just completed does NOT remove the lock (unlike an ordinary
// revision/retry reply landing on the step), so local-representative doesn't
// see a rebuild as "ready" the instant a substep finishes mid-step.
func TestUpdateCondocLockWithholdsRemovalForSubstepCompletion(t *testing.T) {
	s := newServer(t.TempDir())

	// agent_running -> awaiting_action, but just-resumed-from-substep: stays locked.
	s.updateCondocLock(CondocInfo{Path: "X.md", Name: "X", Phase: PhaseAwaitingAction, JustResumedFromSubstep: true}, PhaseAgentRunning, true)
	lockContent(t, s.root)

	// Nothing changes while the human is still composing their next step: the
	// lock stays put (no transition).
	s.updateCondocLock(CondocInfo{Path: "X.md", Name: "X", Phase: PhaseAwaitingAction, JustResumedFromSubstep: true}, PhaseAwaitingAction, true)
	lockContent(t, s.root)

	// Once the step's own agent work actually lands (JustResumedFromSubstep
	// false), reaching awaiting_action unlocks exactly as it always has.
	s.updateCondocLock(CondocInfo{Path: "X.md", Name: "X", Phase: PhaseAgentRunning}, PhaseAwaitingAction, true)
	lockContent(t, s.root)
	s.updateCondocLock(CondocInfo{Path: "X.md", Name: "X", Phase: PhaseAwaitingAction}, PhaseAgentRunning, true)
	lockAbsent(t, s.root)
}

// TestGetCondocStateCompletedSubstepContents verifies a completed substep's
// full content stays reachable through CondocState even once it's no longer
// the "active" substep -- without this, SubstepContent/SubstepIterations
// (only ever populated for a currently-active substep) went empty the
// instant the substep completed, making its entire history vanish from the
// UI. See Step1Prompt.md Revision C.
func TestGetCondocStateCompletedSubstepContents(t *testing.T) {
	root, mainRelPath := newCompletedSubstepFixture(t)
	absPath := filepath.Join(root, mainRelPath)

	state, err := getCondocState(root, absPath)
	if err != nil {
		t.Fatal(err)
	}
	if state.SubstepContent != "" {
		t.Errorf("expected no active SubstepContent, got %q", state.SubstepContent)
	}
	content, ok := state.CompletedSubstepContents["A"]
	if !ok {
		t.Fatal("expected CompletedSubstepContents[\"A\"] to be populated")
	}
	if want := "Substep work done."; !strings.Contains(content, want) {
		t.Errorf("CompletedSubstepContents[%q] = %q, want it to contain %q", "A", content, want)
	}
}
