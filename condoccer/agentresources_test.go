package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddAgentResources verifies files the agent staged are moved into the
// Impls folder under collision-free names and linked by a
// "## Resource N (agent)" block above the placeholder, that hidden files and
// directories are left alone, and that the staging folder is emptied of
// what was linked.
func TestAddAgentResources(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	impls := filepath.Join(dir, "condocImpls")
	if err := os.MkdirAll(filepath.Join(staging, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "report.md"), []byte("# Report\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, ".hidden"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "Step1Prompt.md")
	original := "# Prompt\n\nDo it.\n\n## Resource 1\n\nhuman\n\n## Reply B\n\nDone.\n\n## <REPLACE-Revision|Retry> C\n\n<REPLACE-PROMPT>"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	if err := addAgentResources(path, staging, impls, "Reply B"); err != nil {
		t.Fatalf("addAgentResources: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(got)
	if !strings.Contains(content, "## Resource 2 (agent)\n\nCreated by the agent with Reply B.\n\nNot linked: sub (not a regular file).\n\n- [report.md](") {
		t.Errorf("missing agent resource block:\n%s", content)
	}
	if strings.Index(content, "## Resource 2 (agent)") > strings.Index(content, "## <REPLACE-Revision|Retry> C") {
		t.Errorf("block not above the placeholder:\n%s", content)
	}

	entries, err := os.ReadDir(impls)
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), "_report.md") {
		t.Errorf("impls folder: %v %v", entries, err)
	}
	if fileExists(filepath.Join(staging, "report.md")) {
		t.Error("staged file wasn't removed")
	}
	if !fileExists(filepath.Join(staging, ".hidden")) {
		t.Error("hidden file was touched")
	}

	iters := parseIterations(content)
	var agentRes *Iteration
	for i := range iters {
		if iters[i].ID == "resource-2" {
			agentRes = &iters[i]
		}
		if iters[i].ID == "resource-1" && iters[i].Agent {
			t.Error("human resource marked as agent")
		}
	}
	if agentRes == nil || !agentRes.Agent || agentRes.Label != "Resource 2" {
		t.Errorf("agent resource iteration: %+v", agentRes)
	}
}

// TestAddAgentResourcesNothingStaged verifies an empty or missing staging
// folder adds no block.
func TestAddAgentResourcesNothingStaged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Step1Prompt.md")
	original := "# Prompt\n\n## Reply\n\nDone.\n\n## <REPLACE-Revision|Retry> A\n\n<REPLACE-PROMPT>"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := addAgentResources(path, filepath.Join(dir, "missing"), filepath.Join(dir, "impls"), "Reply"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Errorf("file changed:\n%s", got)
	}
}

// TestResourceHeadingNamedAgent verifies a named agent resource keeps its
// name and the marker, and nextResourceNum still counts it.
func TestResourceHeadingNamedAgent(t *testing.T) {
	content := "## Resource 4 (agent) -- Diagram\n\nx\n"
	iters := parseIterations(content)
	if len(iters) != 1 || !iters[0].Agent || iters[0].Label != "Resource 4 | Diagram" {
		t.Errorf("got %+v", iters)
	}
	if n := nextResourceNum(content); n != 5 {
		t.Errorf("nextResourceNum = %d, want 5", n)
	}
}
