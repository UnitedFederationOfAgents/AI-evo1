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
}
