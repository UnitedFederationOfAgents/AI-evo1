package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Agents & models (Step1SubstepEPrompt.md Revision B): which AI coding agents
// this host can run, and with which models -- the system tab's "agents &
// models" section, below "providers" (and agent-coordinator's per-host
// copy). LR doesn't know agents itself: it asks the ambiguous-agent layer,
// via "ambiguous-agent --inspect" (ambiguous-agent/inspect.go), the same
// layer federation-command invokes agents through.

const (
	// agentsPollInterval is how often the inspection is re-run, so an agent
	// installed (or removed) after LR started shows up without a restart;
	// the section's "re-inspect" control re-runs it on demand.
	agentsPollInterval = 10 * time.Minute
	agentsCheckTimeout = 60 * time.Second

	ambiguousAgentBin = "ambiguous-agent"
)

// AgentInfo mirrors ambiguous-agent's AgentInspection: one agent, whether
// it's installed here, and the models it offers.
type AgentInfo struct {
	Name           string   `json:"name"`
	Command        string   `json:"command"`
	Installed      bool     `json:"installed"`
	Path           string   `json:"path,omitempty"`
	Version        string   `json:"version,omitempty"`
	ModelSelection bool     `json:"model_selection"`
	DefaultModel   string   `json:"default_model,omitempty"`
	Models         []string `json:"models,omitempty"`
	Detail         string   `json:"detail,omitempty"`
}

// AgentsState is SystemStateMsg.Agents. Status reuses the connection
// statuses: connChecking until the first inspection finishes, then connOK,
// or connFailed (with Detail) when ambiguous-agent couldn't be found or run.
type AgentsState struct {
	Status       string      `json:"status"`
	Detail       string      `json:"detail,omitempty"`
	DefaultAgent string      `json:"default_agent,omitempty"`
	Agents       []AgentInfo `json:"agents"`
	CheckedAt    int64       `json:"checked_at,omitempty"` // unix seconds of the last finished inspection
}

// agentsWatch periodically inspects agents through ambiguous-agent. resolve
// locates the ambiguous-agent binary (Server.resolveAppBinary); inspect runs
// it -- both swappable by tests.
type agentsWatch struct {
	notify  func()
	resolve func() (string, error)
	inspect func(ctx context.Context, bin string) ([]byte, error)

	wake chan struct{}

	mu    sync.RWMutex
	state AgentsState
}

func newAgentsWatch(notify func(), resolve func() (string, error)) *agentsWatch {
	return &agentsWatch{
		notify:  notify,
		resolve: resolve,
		inspect: runAgentInspection,
		wake:    make(chan struct{}, 1),
		state:   AgentsState{Status: connChecking, Agents: []AgentInfo{}},
	}
}

// runAgentInspection is the default inspect: "<bin> --inspect".
func runAgentInspection(ctx context.Context, bin string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "--inspect")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%v: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}

// reinspect asks watchLoop to inspect now rather than at its next poll --
// the "re-inspect" control ("inspect-agents" / "__system:inspect-agents").
func (w *agentsWatch) reinspect() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// snapshot returns a copy of the latest inspection; nil-safe so the
// system-state snapshot can be built before the watch exists.
func (w *agentsWatch) snapshot() AgentsState {
	if w == nil {
		return AgentsState{Status: connChecking, Agents: []AgentInfo{}}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	st := w.state
	st.Agents = append([]AgentInfo{}, w.state.Agents...)
	return st
}

func (w *agentsWatch) watchLoop() {
	for {
		w.check()
		select {
		case <-time.After(agentsPollInterval):
		case <-w.wake:
			// Show the on-demand re-inspection as "checking" (keeping the
			// last result's agents) until it finishes.
			w.mu.Lock()
			w.state.Status, w.state.Detail = connChecking, ""
			w.mu.Unlock()
			if w.notify != nil {
				w.notify()
			}
		}
	}
}

// check runs one inspection and publishes it (always, so checked_at moves).
func (w *agentsWatch) check() {
	next := w.run()
	w.mu.Lock()
	w.state = next
	w.mu.Unlock()
	if w.notify != nil {
		w.notify()
	}
}

func (w *agentsWatch) run() AgentsState {
	failed := func(err error) AgentsState {
		log.Printf("agents: inspection failed: %v", err)
		return AgentsState{Status: connFailed, Detail: err.Error(), Agents: []AgentInfo{}, CheckedAt: time.Now().Unix()}
	}
	bin, err := w.resolve()
	if err != nil {
		return failed(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentsCheckTimeout)
	defer cancel()
	out, err := w.inspect(ctx, bin)
	if err != nil {
		return failed(fmt.Errorf("%s --inspect: %w", ambiguousAgentBin, err))
	}
	var report struct {
		DefaultAgent string      `json:"default_agent"`
		Agents       []AgentInfo `json:"agents"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		return failed(fmt.Errorf("%s --inspect: decoding output: %w", ambiguousAgentBin, err))
	}
	if report.Agents == nil {
		report.Agents = []AgentInfo{}
	}
	return AgentsState{Status: connOK, DefaultAgent: report.DefaultAgent, Agents: report.Agents, CheckedAt: time.Now().Unix()}
}
