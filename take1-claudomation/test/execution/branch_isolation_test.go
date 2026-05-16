// super-linter:ignore:GO
// super-linter:ignore:GO_MODULES

package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gruntwork-io/terratest/modules/terraform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBranchIsolation proves that the branch-isolation example can execute a
// full flow:
//   - create a new GitHub repo (keeps evo1 repo clean)
//   - create a slopspace and populate a writespace with the new repo
//   - deploy the slopspace
//   - execute changes (simulated, so no running dungeon-keeper worker required)
//   - return the slopspace and push commits back
//   - open a PR for review
//
// Required environment variables (test is skipped when absent):
//
//	GITHUB_PAT   – personal access token with repo write permissions
//	GITHUB_OWNER – GitHub owner under which the test repo is created
//
// The test creates real GitHub resources.  terraform destroy is deferred so
// the repo and PR are cleaned up even when assertions fail.
func TestBranchIsolation(t *testing.T) {
	t.Parallel()

	githubPAT := os.Getenv("GITHUB_PAT")
	if githubPAT == "" {
		t.Skip("GITHUB_PAT not set — skipping integration test")
	}
	githubOwner := os.Getenv("GITHUB_OWNER")
	if githubOwner == "" {
		t.Skip("GITHUB_OWNER not set — skipping integration test")
	}

	// Resolve a temp directory for slopspaces and work signals so the test
	// is isolated from any real host-agent-files state.
	tmpDir := t.TempDir()
	slopspacesDir := filepath.Join(tmpDir, "slopspaces")
	workSignalsDir := filepath.Join(tmpDir, "work")
	ledgerPath := filepath.Join(tmpDir, "ledger.jsonl")

	terraformOptions := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: "../../examples/execution/branch-isolation",
		Vars: map[string]interface{}{
			"github_pat":           githubPAT,
			"github_owner":         githubOwner,
			"assignment_name":      "briso-test",
			"instruction":          "Add a HELLO.md file with the text 'Hello from the test.'",
			"simulate_execution":   true,
			"slopspaces_dir":       slopspacesDir,
			"work_signals_dir":     workSignalsDir,
			"ledger_path":          ledgerPath,
		},
	})

	defer terraform.Destroy(t, terraformOptions)
	terraform.InitAndApply(t, terraformOptions)

	// --- assignment identity ---
	assignmentID := terraform.Output(t, terraformOptions, "assignment_id")
	assert.NotEmpty(t, assignmentID, "assignment_id should be non-empty")
	assert.True(t, strings.HasPrefix(assignmentID, "assign_briso-test_"),
		"assignment_id should be prefixed with assign_<name>_<timestamp>")

	// --- repository created ---
	repoFullName := terraform.Output(t, terraformOptions, "repo_full_name")
	assert.NotEmpty(t, repoFullName, "repo_full_name should be non-empty")
	assert.True(t, strings.HasPrefix(repoFullName, githubOwner+"/"),
		"repo_full_name should be under the expected owner")

	// --- branch created ---
	branchName := terraform.Output(t, terraformOptions, "branch_name")
	assert.NotEmpty(t, branchName, "branch_name should be non-empty")
	assert.True(t, strings.HasPrefix(branchName, "assign-briso-test-"),
		"branch_name should be prefixed with assign-<name>-<timestamp>")

	// --- PR opened ---
	prURL := terraform.Output(t, terraformOptions, "pr_url")
	require.NotEmpty(t, prURL, "pr_url should be non-empty")
	assert.True(t, strings.HasPrefix(prURL, "https://github.com/"),
		"pr_url should be a GitHub URL")

	prNumber := terraform.Output(t, terraformOptions, "pr_number")
	assert.NotEmpty(t, prNumber, "pr_number should be non-empty")

	// --- PR collector picked up the open PR ---
	prConclusionState := terraform.Output(t, terraformOptions, "pr_conclusion_state")
	assert.Equal(t, "active", prConclusionState,
		"PR should be active immediately after creation")
}
