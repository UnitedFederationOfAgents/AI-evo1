package main

import (
	"context"
	"errors"
	"testing"
)

func TestAgentsWatch(t *testing.T) {
	notified := 0
	w := newAgentsWatch(func() { notified++ }, func() (string, error) { return "/fake/ambiguous-agent", nil })
	if st := w.snapshot(); st.Status != connChecking || st.Agents == nil {
		t.Fatalf("before the first inspection: %+v", st)
	}

	w.inspect = func(ctx context.Context, bin string) ([]byte, error) {
		if bin != "/fake/ambiguous-agent" {
			t.Errorf("inspected with %q", bin)
		}
		return []byte(`{"default_agent":"claude","agents":[
			{"name":"claude","command":"claude","installed":true,"path":"/usr/bin/claude","version":"2.0.0 (Claude Code)","model_selection":true,"models":["opus","sonnet"]},
			{"name":"gemini","command":"gemini","installed":false,"model_selection":true}]}`), nil
	}
	w.check()
	st := w.snapshot()
	if st.Status != connOK || st.DefaultAgent != "claude" || len(st.Agents) != 2 || st.CheckedAt == 0 {
		t.Fatalf("after inspection: %+v", st)
	}
	if c := st.Agents[0]; c.Name != "claude" || !c.Installed || len(c.Models) != 2 || c.Version == "" {
		t.Errorf("claude entry: %+v", c)
	}
	if notified != 1 {
		t.Errorf("notified %d times, want 1", notified)
	}

	w.inspect = func(ctx context.Context, bin string) ([]byte, error) { return nil, errors.New("boom") }
	w.check()
	if st := w.snapshot(); st.Status != connFailed || st.Detail == "" || st.Agents == nil {
		t.Errorf("after a failed inspection: %+v", st)
	}

	w.resolve = func() (string, error) { return "", errors.New("not found") }
	w.check()
	if st := w.snapshot(); st.Status != connFailed || st.Detail != "not found" {
		t.Errorf("without ambiguous-agent: %+v", st)
	}

	var nilWatch *agentsWatch
	nilWatch.reinspect()
	if st := nilWatch.snapshot(); st.Status != connChecking {
		t.Errorf("nil watch snapshot: %+v", st)
	}
	if nilWatch.connectable("claude") || nilWatch.childEnv() != nil {
		t.Errorf("nil watch should be neither connectable nor contribute env")
	}
}

func TestConnectedAgentToken(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	w := newAgentsWatch(nil, func() (string, error) { return "/fake/ambiguous-agent", nil })
	w.inspect = func(ctx context.Context, bin string) ([]byte, error) {
		return []byte(`{"agents":[{"name":"claude","command":"claude","installed":true},{"name":"gemini","command":"gemini","installed":true}]}`), nil
	}
	w.check()
	if a := w.snapshot().Agents[0]; a.Auth != "" {
		t.Errorf("claude auth before connecting: %q", a.Auth)
	}

	if !w.connectable("claude") || w.connectable("gemini") {
		t.Errorf("only claude should be connectable")
	}
	if err := w.connect("gemini", "tok"); err == nil {
		t.Errorf("connecting gemini should fail")
	}
	for _, bad := range []string{"", "  ", "a b", "a\nb"} {
		if err := w.connect("claude", bad); err == nil {
			t.Errorf("connect(claude, %q) should fail", bad)
		}
	}
	if err := w.connect("claude", " sk-ant-oat-123 "); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if env := w.childEnv(); len(env) != 1 || env[0] != "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat-123" {
		t.Errorf("child env after connecting: %v", env)
	}
	st := w.snapshot()
	if st.Agents[0].Auth != connectedSource || st.Agents[1].Auth != "" {
		t.Errorf("auth after connecting: %+v", st.Agents)
	}

	if err := w.disconnect("claude"); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if env := w.childEnv(); len(env) != 0 {
		t.Errorf("child env after disconnecting: %v", env)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "from-env")
	if a := w.snapshot().Agents[0]; a.Auth != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("claude auth from LR's environment: %q", a.Auth)
	}
}
