package main

import (
	"path/filepath"
	"testing"

	dktypes "dungeon-keeper/pkg/types"
)

func TestRunAgentOverride(t *testing.T) {
	s := newServer()
	s.slop.agent, s.slop.model = "claude", ""
	spec := &PointerSpec{Agent: "claude", Model: "opus"}

	if a, m := s.runAgent(spec); a != "claude" || m != "opus" {
		t.Errorf("no override: %s/%s", a, m)
	}
	if a, m := s.runAgent(&PointerSpec{}); a != "claude" || m != "" {
		t.Errorf("no override, blank spec: %s/%s", a, m)
	}

	// The override's pair replaces the condoc's, a blank model included.
	s.slop.override = AgentOverride{Agent: "pi"}
	if a, m := s.runAgent(spec); a != "pi" || m != "" {
		t.Errorf("override pi: %s/%s", a, m)
	}
	s.slop.override = AgentOverride{Agent: "opencode", Model: "anthropic/claude-sonnet-5-5"}
	if a, m := s.runAgent(spec); a != "opencode" || m != "anthropic/claude-sonnet-5-5" {
		t.Errorf("override opencode: %s/%s", a, m)
	}
	// The condoc itself is left alone.
	if spec.Agent != "claude" || spec.Model != "opus" {
		t.Errorf("spec changed: %+v", spec)
	}
}

func TestValidateAgentOverride(t *testing.T) {
	for _, o := range []AgentOverride{
		{},
		{Agent: "claude"},
		{Agent: " pi ", Model: " anthropic/claude-opus-5-5 "},
		{Agent: "opencode", Model: "openai/gpt-5.1:high"},
	} {
		if _, err := validateAgentOverride(o); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
	for _, o := range []AgentOverride{
		{Model: "opus"},
		{Agent: "Claude Code"},
		{Agent: "claude", Model: "two words"},
		{Agent: "claude", Model: `"opus"`},
	} {
		if _, err := validateAgentOverride(o); err == nil {
			t.Errorf("%+v was accepted", o)
		}
	}
}

func TestAgentOverridePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "condocv2", "agent-override.json")
	s := newServer()
	s.loadAgentOverride(path)
	if err := s.setAgentOverride(AgentOverride{Agent: "pi", Model: "anthropic/claude-opus-5-5"}); err != nil {
		t.Fatal(err)
	}

	again := newServer()
	again.loadAgentOverride(path)
	if o := again.agentOverride(); o.Agent != "pi" || o.Model != "anthropic/claude-opus-5-5" {
		t.Errorf("reloaded %+v", o)
	}

	if err := again.setAgentOverride(AgentOverride{}); err != nil {
		t.Fatal(err)
	}
	cleared := newServer()
	cleared.loadAgentOverride(path)
	if o := cleared.agentOverride(); o.Agent != "" {
		t.Errorf("cleared override reloaded as %+v", o)
	}
	if err := cleared.setAgentOverride(AgentOverride{Agent: "bad name"}); err == nil {
		t.Error("an invalid override was saved")
	}
}

func TestReplyHidesAgentBanner(t *testing.T) {
	for _, banner := range []string{
		"invoking claude in write (files read/write) mode...",
		"invoking \x1b[38;5;51mpi\x1b[0m (\x1b[38;5;51manthropic/claude-opus-5-5\x1b[0m) in write (files read/write) mode...",
		"invoking opencode (openai/gpt-5.1) in write (files read/write) mode...",
	} {
		out := banner + "\n● session: /records/2026-11-03\nDid the thing.\nagent completed successfully\n"
		signal := &dktypes.WorkSignal{Status: dktypes.WorkStatusCompleted}
		got := replyFromSignal(signal, []dktypes.WorkEvent{{StatusUpdate: "output", Output: out}})
		want := "● session: /records/2026-11-03\nDid the thing.\nagent completed successfully"
		if got != want {
			t.Errorf("%q:\ngot  %q\nwant %q", banner, got, want)
		}
	}

	// Text that merely mentions invoking an agent stays.
	keep := "I tried invoking claude in write mode... it worked."
	if got := stripAgentBanner(keep); got != keep {
		t.Errorf("mid-line text changed: %q", got)
	}
}
