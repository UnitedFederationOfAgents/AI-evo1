package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newActiveSubstepFixture lays out a condoc mid-substep: Step 1 has handed
// off to Substep A, whose reply has landed and which awaits the human's next
// action.
func newActiveSubstepFixture(t *testing.T) (root, mainRelPath string) {
	t.Helper()
	root = t.TempDir()
	implDirPath := filepath.Join(root, "condocs", "xImpls")
	if err := os.MkdirAll(implDirPath, 0755); err != nil {
		t.Fatal(err)
	}
	mainContent := "# X\n\n### Step 1 - Do it\n\n[Step 1 Prompt](xImpls/Step1Prompt.md)\n\n\n" +
		"## Human-Prompt\n\nThe flow of the condoc is now within the first step.\n"
	if err := os.WriteFile(filepath.Join(root, "condocs", "X.md"), []byte(mainContent), 0644); err != nil {
		t.Fatal(err)
	}
	stepContent := "# Prompt\n\nDo something.\n\n## Reply\n\nDone initial work.\n\n" +
		"## Substep A - Feedback tools\n\n[Step 1 Substep A](Step1SubstepAPrompt.md)\n\n\n" +
		"## Human-Prompt\n\nSubstep A is now active. Interact with the substep file (Step1SubstepAPrompt.md)\n"
	if err := os.WriteFile(filepath.Join(implDirPath, "Step1Prompt.md"), []byte(stepContent), 0644); err != nil {
		t.Fatal(err)
	}
	substepContent := "# Prompt\n\n[Step1Prompt](Step1Prompt.md)\n\nDo the substep.\n\n## Reply\n\nSubstep work done.\n\n\n" +
		"## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>\n\n\n" +
		"## Human-Prompt\n\n" + cfHandoffOrCompleted + "\n"
	if err := os.WriteFile(filepath.Join(implDirPath, "Step1SubstepAPrompt.md"), []byte(substepContent), 0644); err != nil {
		t.Fatal(err)
	}
	return root, "condocs/X.md"
}

// TestForceCompleteActionWritesDirective verifies Force Complete on a
// same-repo condoc leaves !FORCE-COMPLETED! on the main file -- not the
// active substep file -- once, and that the condoc then reads as
// agent_running until federation-command acts on it.
func TestForceCompleteActionWritesDirective(t *testing.T) {
	root, mainRel := newActiveSubstepFixture(t)
	mainPath := filepath.Join(root, mainRel)
	s := newServer(root)
	for i := 0; i < 2; i++ {
		info, err := detectPhase(root, mainPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.applyAction(root, mainPath, info, ActionRequest{Action: "force_complete", Path: mainRel}, true); err != nil {
			t.Fatalf("force_complete #%d: %v", i+1, err)
		}
	}
	b, _ := os.ReadFile(mainPath)
	if n := len(forceCompletedDirectiveRe.FindAllIndex(b, -1)); n != 1 {
		t.Errorf("main file has %d force-complete directives, want 1:\n%s", n, b)
	}
	sb, _ := os.ReadFile(filepath.Join(root, "condocs", "xImpls", "Step1SubstepAPrompt.md"))
	if strings.Contains(string(sb), "!FORCE-COMPLETED!") {
		t.Error("directive was written to the substep file")
	}
	info, err := detectPhase(root, mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Phase != PhaseAgentRunning {
		t.Errorf("phase = %v, want %v while the directive is pending", info.Phase, PhaseAgentRunning)
	}
}

// TestForceCompleteRefusesUnstartedOrCompleted verifies a proposal (no
// branch to PR) and an already-completed condoc can't be force completed.
func TestForceCompleteRefusesUnstartedOrCompleted(t *testing.T) {
	if err := checkForceCompletable(CondocInfo{Name: "X", Phase: PhaseProposed}); err == nil {
		t.Error("accepted a proposal")
	}
	if err := checkForceCompletable(CondocInfo{Name: "X", Phase: PhaseCompleted, StepNum: 2}); err == nil {
		t.Error("accepted a completed condoc")
	}
	if err := checkForceCompletable(CondocInfo{Name: "X", Phase: PhaseAwaitingStep, StepNum: 1}); err != nil {
		t.Errorf("refused an awaiting-step condoc: %v", err)
	}
}

// TestForceFinalizeFiles verifies the slopspace handler's force complete
// closes the active substep and step and completes the main file.
func TestForceFinalizeFiles(t *testing.T) {
	root, mainRel := newActiveSubstepFixture(t)
	mainPath := filepath.Join(root, mainRel)
	info, err := detectPhase(root, mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Phase != PhaseAwaitingAction || info.SubstepFile == "" {
		t.Fatalf("fixture reads as %v (substep %q), want awaiting_action in a substep", info.Phase, info.SubstepFile)
	}
	if err := forceFinalizeFiles(root, mainPath, info); err != nil {
		t.Fatal(err)
	}
	info, err = detectPhase(root, mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Phase != PhaseCompleted {
		t.Errorf("phase = %v, want %v", info.Phase, PhaseCompleted)
	}
	for file, heading := range map[string]string{
		"Step1SubstepAPrompt.md": "## Substep Completed",
		"Step1Prompt.md":         "## Step Completed",
	} {
		b, _ := os.ReadFile(filepath.Join(root, "condocs", "xImpls", file))
		if !strings.Contains(string(b), heading) {
			t.Errorf("%s lacks %q", file, heading)
		}
		if strings.Contains(string(b), "## Human-Prompt") || strings.Contains(string(b), "<REPLACE-") {
			t.Errorf("%s still has its Human-Prompt or an unfilled template:\n%s", file, b)
		}
	}
}
