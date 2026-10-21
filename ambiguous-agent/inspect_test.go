package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInspect(t *testing.T) {
	// Only a stand-in "claude" is on PATH.
	dir := t.TempDir()
	script := "#!/bin/sh\necho '9.9.9 (Claude Code)'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	var buf bytes.Buffer
	if err := printInspection(&buf, ""); err != nil {
		t.Fatalf("printInspection: %v", err)
	}
	var report InspectionReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("decoding %q: %v", buf.String(), err)
	}
	if report.DefaultAgent != DefaultAgent {
		t.Errorf("default_agent = %q, want %q", report.DefaultAgent, DefaultAgent)
	}
	if len(report.Agents) != len(availableAgents) {
		t.Fatalf("got %d agents, want %d", len(report.Agents), len(availableAgents))
	}
	for _, a := range report.Agents {
		if a.Name != "claude" {
			if a.Installed {
				t.Errorf("%s reported installed with an empty PATH", a.Name)
			}
			continue
		}
		if !a.Installed || a.Path != filepath.Join(dir, "claude") {
			t.Errorf("claude: installed=%v path=%q", a.Installed, a.Path)
		}
		if a.Version != "9.9.9 (Claude Code)" {
			t.Errorf("claude version = %q", a.Version)
		}
		if !a.ModelSelection || len(a.Models) != len(agentConfigs["claude"].Models) {
			t.Errorf("claude models = %v (model_selection %v)", a.Models, a.ModelSelection)
		}
	}

	buf.Reset()
	if err := printInspection(&buf, "claude"); err != nil {
		t.Fatalf("printInspection(claude): %v", err)
	}
	report = InspectionReport{}
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil || len(report.Agents) != 1 || report.Agents[0].Name != "claude" {
		t.Errorf("inspecting -a claude gave %q (err %v)", buf.String(), err)
	}
	if err := printInspection(&buf, "nonesuch"); err == nil {
		t.Errorf("inspecting an unknown agent should fail")
	}
}
