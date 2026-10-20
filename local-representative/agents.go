package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
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

	// Auth is where the agent's credential comes from, for a connectable
	// agent (see agentAuthEnv): connectedSource once one is connected, else
	// the env var's name when LR's own environment sets it, else "".
	Auth string `json:"auth,omitempty"`
}

// Connected agents (Step1SubstepEPrompt.md Revision D): the "agents &
// models" section's "connect" control hands LR an agent credential at
// runtime -- only Claude, by OAuth token, so far. Like a connected provider
// (connectivity.go) it's held in memory only and passed to every managed
// app LR launches afterwards, here as the env var below, so an agent those
// apps run through ambiguous-agent picks it up. LR doesn't verify it.
var agentAuthEnv = map[string]string{"claude": "CLAUDE_CODE_OAUTH_TOKEN"}

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

	mu     sync.RWMutex
	state  AgentsState
	tokens map[string]string // connected agent credentials, by agent name
}

func newAgentsWatch(notify func(), resolve func() (string, error)) *agentsWatch {
	return &agentsWatch{
		notify:  notify,
		resolve: resolve,
		inspect: runAgentInspection,
		wake:    make(chan struct{}, 1),
		state:   AgentsState{Status: connChecking, Agents: []AgentInfo{}},
		tokens:  map[string]string{},
	}
}

// connectable reports whether the "connect" control accepts agent.
func (w *agentsWatch) connectable(agent string) bool {
	_, ok := agentAuthEnv[agent]
	return w != nil && ok
}

// connect keeps credential (in memory) as agent's connected credential. The
// credential itself never appears in the returned error or the log.
func (w *agentsWatch) connect(agent, credential string) error {
	if !w.connectable(agent) {
		return fmt.Errorf("agent %q can't be connected", agent)
	}
	tok := strings.TrimSpace(credential)
	if tok == "" {
		return fmt.Errorf("%s token is empty", agentAuthEnv[agent])
	}
	if strings.ContainsAny(tok, " \t\r\n") {
		return fmt.Errorf("%s token contains whitespace", agentAuthEnv[agent])
	}
	w.mu.Lock()
	w.tokens[agent] = tok
	w.mu.Unlock()
	if w.notify != nil {
		w.notify()
	}
	return nil
}

// disconnect forgets agent's connected credential, falling back to any in
// LR's own environment.
func (w *agentsWatch) disconnect(agent string) error {
	if !w.connectable(agent) {
		return fmt.Errorf("agent %q can't be disconnected", agent)
	}
	w.mu.Lock()
	delete(w.tokens, agent)
	w.mu.Unlock()
	if w.notify != nil {
		w.notify()
	}
	return nil
}

// childEnv is the KEY=VALUE entries a managed app gets on top of LR's own
// environment so its agents use the connected credentials; nil-safe.
func (w *agentsWatch) childEnv() []string {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	var env []string
	for agent, tok := range w.tokens {
		env = append(env, agentAuthEnv[agent]+"="+tok)
	}
	return env
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
	for i, a := range st.Agents {
		env, ok := agentAuthEnv[a.Name]
		switch {
		case !ok:
		case w.tokens[a.Name] != "":
			st.Agents[i].Auth = connectedSource
		case os.Getenv(env) != "":
			st.Agents[i].Auth = env
		}
	}
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
