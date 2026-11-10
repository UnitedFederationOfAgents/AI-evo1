// super-linter:ignore:GO
// super-linter:ignore:GO_MODULES

package test

import (
	"testing"

	"github.com/gruntwork-io/terratest/modules/terraform"
	"github.com/stretchr/testify/assert"
)

func TestExecution(t *testing.T) {
	t.Parallel()

	terraformOptions := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: "../../examples/execution",
	})

	defer terraform.Destroy(t, terraformOptions)

	terraform.InitAndApply(t, terraformOptions)

	// Get output and verify it exists
	output := terraform.OutputMap(t, terraformOptions, "result")
	assert.NotEmpty(t, output, "Expected non-empty output from module")

	// Verify the id output exists
	id, ok := output["id"]
	assert.True(t, ok, "Expected 'id' key in result output")
	assert.NotEmpty(t, id, "Expected non-empty id value")
}
