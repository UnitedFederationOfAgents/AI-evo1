package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// --inspect: a machine-readable account of which agents this host can run
// and with which models -- what local-representative's "agents & models"
// section shows (see local-representative/agents.go and
// condocs/slopspaceCondocsImpls/Step1SubstepEPrompt.md Revision B), and what
// federation-command can consume instead of parsing --list-agents /
// --list-models' styled output.

// inspectVersionTimeout bounds each "<agent> --version" probe.
const inspectVersionTimeout = 10 * time.Second

// AgentInspection is one agent in --inspect's JSON output.
type AgentInspection struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	Installed bool   `json:"installed"`         // Command resolves on PATH
	Path      string `json:"path,omitempty"`    // where Command resolved to
	Version   string `json:"version,omitempty"` // first line of "<command> --version", when it answers
	// ModelSelection is whether the agent takes a model flag at all.
	ModelSelection bool     `json:"model_selection"`
	DefaultModel   string   `json:"default_model,omitempty"`
	Models         []string `json:"models,omitempty"`
	// Detail explains a failed model query (the agent is installed but
	// listing its models didn't work).
	Detail string `json:"detail,omitempty"`
}

// InspectionReport is --inspect's whole JSON output.
type InspectionReport struct {
	DefaultAgent string            `json:"default_agent"`
	Agents       []AgentInspection `json:"agents"`
}

// printInspection writes the inspection report for every available agent,
// or only for agent when it's non-empty, as indented JSON.
func printInspection(w io.Writer, agent string) error {
	names := availableAgents
	if agent != "" {
		if _, ok := agentConfigs[agent]; !ok {
			return fmt.Errorf("unknown agent '%s'", agent)
		}
		names = []string{agent}
	}
	report := InspectionReport{DefaultAgent: DefaultAgent, Agents: make([]AgentInspection, 0, len(names))}
	for _, name := range names {
		report.Agents = append(report.Agents, inspectAgent(name, agentConfigs[name]))
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// inspectAgent reports one agent. Models are only queried for an installed
// agent; a static model list is reported as-is.
func inspectAgent(name string, config *AgentConfig) AgentInspection {
	info := AgentInspection{
		Name:           name,
		Command:        config.Command,
		ModelSelection: config.ModelFlag != "",
		DefaultModel:   config.DefaultModel,
	}
	path, err := exec.LookPath(config.Command)
	if err != nil {
		return info
	}
	info.Installed = true
	info.Path = path
	info.Version = probeVersion(path)
	models, err := listModelsForAgent(name, config)
	if err != nil {
		info.Detail = err.Error()
	}
	info.Models = models
	return info
}

// probeVersion returns the first line of "<path> --version", or "" when the
// agent doesn't answer it in time.
func probeVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), inspectVersionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(line)
}
