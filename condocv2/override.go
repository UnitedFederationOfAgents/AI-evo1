package main

// The agent override: an agent/model pair, set on this condocv2 rather than
// on any condoc, that drives every agent run it hands the worker -- each
// interaction's turn and the PR drafting (see submitSignal) -- in place of the
// pair the condoc was proposed with.
//
// It is the client's choice alone, so it never reaches the condoc: it is
// kept in this host's config dir (not the pointer, not the condoc files), the
// pointer keeps the condoc's own agent/model, and the agent's banner line --
// ambiguous-agent's "invoking <agent> (<model>) in <mode> mode..." -- is
// dropped from every reply before it becomes a Response (see
// replyFromSignal), so the step file reads the same whoever ran the turn.
// The only places it shows are this condocv2's own UI and the work signals
// on this host (the Visual tab's operations column reads them).

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AgentOverride is the pair; a blank Agent is no override. A blank Model is
// the agent's own default.
type AgentOverride struct {
	Agent string `json:"agent"`
	Model string `json:"model,omitempty"`
}

var (
	overrideAgentPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	overrideModelPattern = regexp.MustCompile(`^[A-Za-z0-9._:/@\[\]-]+$`)

	// agentBannerRe is ambiguous-agent's invocation line, ANSI already
	// stripped (ambiguous-agent/main.go).
	agentBannerRe = regexp.MustCompile(`(?m)^invoking \S+( \(.*\))? in .* mode\.\.\.[ \t]*\n?`)
)

// defaultAgentOverridePath is where the override is kept unless
// --agent-override-file says otherwise.
func defaultAgentOverridePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "condocv2", "agent-override.json")
}

func validateAgentOverride(o AgentOverride) (AgentOverride, error) {
	o.Agent, o.Model = strings.TrimSpace(o.Agent), strings.TrimSpace(o.Model)
	if o.Agent == "" {
		if o.Model != "" {
			return o, fmt.Errorf("an override model needs an agent")
		}
		return o, nil
	}
	if !overrideAgentPattern.MatchString(o.Agent) {
		return o, fmt.Errorf("agent %q: expected an ambiguous-agent name such as claude, pi or opencode", o.Agent)
	}
	if o.Model != "" && !overrideModelPattern.MatchString(o.Model) {
		return o, fmt.Errorf("model %q: no spaces or quotes", o.Model)
	}
	return o, nil
}

// loadAgentOverride reads the override from path ("" keeps it in memory
// only). A missing file is no override; an unreadable one is logged and also
// treated as none.
func (s *Server) loadAgentOverride(path string) {
	s.slop.mu.Lock()
	defer s.slop.mu.Unlock()
	s.slop.overridePath = path
	s.slop.override = AgentOverride{}
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("agent override %s: %v (no override)", path, err)
		}
		return
	}
	var o AgentOverride
	if err := json.Unmarshal(b, &o); err != nil {
		log.Printf("agent override %s: %v (no override)", path, err)
		return
	}
	if o, err = validateAgentOverride(o); err != nil {
		log.Printf("agent override %s: %v (no override)", path, err)
		return
	}
	s.slop.override = o
	if o.Agent != "" {
		log.Printf("agent override: %s", overrideLabel(o))
	}
}

// setAgentOverride sets (or, with a blank agent, clears) the override and
// saves it. Runs already handed to the worker keep the pair they were
// given; it applies from the next one.
func (s *Server) setAgentOverride(o AgentOverride) error {
	o, err := validateAgentOverride(o)
	if err != nil {
		return err
	}
	s.slop.mu.Lock()
	path := s.slop.overridePath
	if path != "" {
		if err := writeAgentOverride(path, o); err != nil {
			s.slop.mu.Unlock()
			return fmt.Errorf("saving the agent override: %v", err)
		}
	}
	s.slop.override = o
	s.slop.mu.Unlock()
	if o.Agent == "" {
		log.Printf("agent override cleared")
	} else {
		log.Printf("agent override set: %s", overrideLabel(o))
	}
	s.broadcastPointer()
	return nil
}

func writeAgentOverride(path string, o AgentOverride) error {
	if o.Agent == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(o, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// agentOverride is the override in effect (Agent blank when none).
func (s *Server) agentOverride() AgentOverride {
	s.slop.mu.Lock()
	defer s.slop.mu.Unlock()
	return s.slop.override
}

// runAgent is the agent/model a run for spec is handed to the worker with:
// the override when one is set (its model replacing the condoc's even when
// blank -- a model belongs to its agent), else the condoc's own pair, else
// this condocv2's defaults.
func (s *Server) runAgent(spec *PointerSpec) (agent, model string) {
	if o := s.agentOverride(); o.Agent != "" {
		return o.Agent, o.Model
	}
	agent, model = spec.Agent, spec.Model
	if agent == "" {
		agent = s.slop.agent
	}
	if model == "" {
		model = s.slop.model
	}
	return agent, model
}

func overrideLabel(o AgentOverride) string {
	if o.Model == "" {
		return o.Agent
	}
	return o.Agent + " / " + o.Model
}

// stripAgentBanner drops ambiguous-agent's invocation line from a reply, so
// the condoc never learns which agent ran it.
func stripAgentBanner(reply string) string {
	return agentBannerRe.ReplaceAllString(reply, "")
}
