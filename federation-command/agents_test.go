package main

import (
	"errors"
	"strings"
	"testing"
)

// TestRenderAgentInspectDone covers "ufa agent list" and "ufa agent model
// list" output for a report shaped like ambiguous-agent --inspect's.
func TestRenderAgentInspectDone(t *testing.T) {
	report := &inspectionReport{
		DefaultAgent: "claude",
		Agents: []agentInspection{
			{Name: "claude", Command: "claude", Installed: true, Path: "/usr/bin/claude", Version: "2.1.0 (Claude Code)",
				ModelSelection: true, DefaultModel: "sonnet", Models: []string{"opus", "sonnet", "haiku"}},
			{Name: "codex", Command: "codex", ModelSelection: true},
		},
	}

	out, code := renderAgentInspectDone(agentInspectDoneMsg{report: report, line: "ufa agent list"}, "claude", "")
	if code != 0 {
		t.Errorf("agent list: expected exit 0, got %d", code)
	}
	for _, want := range []string{"claude (selected, default)", "2.1.0 (Claude Code)", "/usr/bin/claude", "3 model(s)", "codex — not installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("agent list: expected %q in output:\n%s", want, out)
		}
	}

	out, code = renderAgentInspectDone(agentInspectDoneMsg{report: report, models: true, agent: "claude", line: "ufa agent model list"}, "claude", "opus")
	if code != 0 {
		t.Errorf("model list: expected exit 0, got %d", code)
	}
	for _, want := range []string{"models for claude", "-> opus (selected)", "sonnet (default)", "haiku", "current selection: opus"} {
		if !strings.Contains(out, want) {
			t.Errorf("model list: expected %q in output:\n%s", want, out)
		}
	}

	if _, code = renderAgentInspectDone(agentInspectDoneMsg{report: report, models: true, agent: "codex", line: "ufa agent model list codex"}, "claude", ""); code != 1 {
		t.Errorf("model list for an uninstalled agent: expected exit 1, got %d", code)
	}

	out, code = renderAgentInspectDone(agentInspectDoneMsg{err: errors.New("not found"), line: "ufa agent list"}, "claude", "")
	if code != 1 || !strings.Contains(out, "ufa agent list: not found") {
		t.Errorf("inspection failure: expected exit 1 and the error, got %d %q", code, out)
	}
}
