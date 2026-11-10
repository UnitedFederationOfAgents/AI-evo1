package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// "ufa agent list" / "ufa agent model list [<agent>]": what agents this host
// can run, and with which models, as reported by the ambiguous-agent layer's
// "--inspect" (ambiguous-agent/inspect.go) -- the same source LR's "agents &
// models" section uses (condocs/slopspaceCondocsImpls/Step1SubstepEPrompt.md
// Revisions B and C). Unlike "list-agents" / "list-models", these show what's
// actually installed rather than FC's own static tables.

// agentInspectTimeout bounds one "ambiguous-agent --inspect" run; it probes
// every agent's "--version", each of which ambiguous-agent bounds at 10s.
const agentInspectTimeout = 60 * time.Second

// agentInspection mirrors ambiguous-agent's AgentInspection.
type agentInspection struct {
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

// inspectionReport mirrors ambiguous-agent's InspectionReport.
type inspectionReport struct {
	DefaultAgent string            `json:"default_agent"`
	Agents       []agentInspection `json:"agents"`
}

// agentInspectDoneMsg carries an inspection back to Update. models is set
// for "ufa agent model list", whose report covers only agent.
type agentInspectDoneMsg struct {
	report  *inspectionReport
	err     error
	models  bool
	agent   string
	line    string
	cmdTime time.Time
	deltaMs int64
}

// inspectAgentsCmd runs "ambiguous-agent --inspect [-a <agent>]" off the UI
// goroutine, since probing every agent can take a few seconds.
func inspectAgentsCmd(agent string, models bool, line string, cmdTime time.Time, deltaMs int64) tea.Cmd {
	return func() tea.Msg {
		done := agentInspectDoneMsg{models: models, agent: agent, line: line, cmdTime: cmdTime, deltaMs: deltaMs}
		done.report, done.err = runAgentInspection(agent)
		return done
	}
}

func runAgentInspection(agent string) (*inspectionReport, error) {
	bin, err := findBinary("ambiguous-agent")
	if err != nil {
		return nil, err
	}
	args := []string{"--inspect"}
	if agent != "" {
		args = append(args, "-a", agent)
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentInspectTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("ambiguous-agent --inspect: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("ambiguous-agent --inspect: %w", err)
	}
	var report inspectionReport
	if err := json.Unmarshal(out, &report); err != nil {
		return nil, fmt.Errorf("ambiguous-agent --inspect: decoding output: %w", err)
	}
	return &report, nil
}

// renderAgentInspection lists every agent with whether it's installed, its
// version and path, marking the session's current agent.
func renderAgentInspection(report *inspectionReport, currentAgent string) string {
	var b strings.Builder
	b.WriteString(sessionStyle.Render("agents (via ambiguous-agent --inspect):"))
	for _, a := range report.Agents {
		b.WriteString("\n")
		marker := "    "
		if a.Name == currentAgent {
			marker = "  -> "
		}
		var notes []string
		if a.Name == currentAgent {
			notes = append(notes, "selected")
		}
		if a.Name == report.DefaultAgent {
			notes = append(notes, "default")
		}
		label := a.Name
		if len(notes) > 0 {
			label += " (" + strings.Join(notes, ", ") + ")"
		}
		if !a.Installed {
			b.WriteString(sessionStyle.Render(fmt.Sprintf("%s%s — not installed ('%s' not on PATH)", marker, label, a.Command)))
			continue
		}
		color := agentColors[a.Name]
		if color == "" {
			color = lipgloss.Color("141")
		}
		b.WriteString(lipgloss.NewStyle().Foreground(color).Bold(true).Render(marker + label))
		details := []string{}
		if a.Version != "" {
			details = append(details, a.Version)
		}
		details = append(details, a.Path)
		if a.ModelSelection {
			details = append(details, fmt.Sprintf("%d model(s)", len(a.Models)))
		}
		b.WriteString(sessionStyle.Render(" — " + strings.Join(details, " · ")))
	}
	b.WriteString("\n" + sessionStyle.Render("'ufa agent model list [<agent>]' for an agent's models"))
	return b.String()
}

// renderAgentModels lists one agent's models, marking its default and the
// session's current model selection (when the agent is the current one).
func renderAgentModels(a agentInspection, currentAgent, currentModel string) string {
	var b strings.Builder
	switch {
	case !a.Installed:
		b.WriteString(errorStyle.Render(fmt.Sprintf("agent '%s' is not installed ('%s' not on PATH)", a.Name, a.Command)))
		return b.String()
	case !a.ModelSelection:
		b.WriteString(sessionStyle.Render(fmt.Sprintf("agent '%s' does not support model selection — it uses its built-in default model", a.Name)))
		return b.String()
	}
	b.WriteString(sessionStyle.Render(fmt.Sprintf("models for %s (via ambiguous-agent --inspect):", a.Name)))
	if a.Detail != "" {
		b.WriteString("\n" + errorStyle.Render("  "+a.Detail))
	}
	selected := ""
	if a.Name == currentAgent {
		selected = currentModel
	}
	for _, model := range a.Models {
		marker := "    "
		var notes []string
		if model == selected {
			marker = "  -> "
			notes = append(notes, "selected")
		}
		if model == a.DefaultModel {
			notes = append(notes, "default")
		}
		label := model
		if len(notes) > 0 {
			label += " (" + strings.Join(notes, ", ") + ")"
		}
		b.WriteString("\n" + marker + label)
	}
	if a.Name == currentAgent {
		if currentModel != "" {
			b.WriteString("\n" + sessionStyle.Render("current selection: "+currentModel))
		} else {
			b.WriteString("\n" + sessionStyle.Render("no model explicitly set - using agent's built-in default"))
		}
	}
	return b.String()
}

// renderAgentInspectDone turns a finished inspection into the command's
// output and exit code.
func renderAgentInspectDone(msg agentInspectDoneMsg, currentAgent, currentModel string) (string, int) {
	if msg.err != nil {
		return errorStyle.Render(strings.TrimSpace(msg.line)+": "+msg.err.Error()), 1
	}
	if !msg.models {
		return renderAgentInspection(msg.report, currentAgent), 0
	}
	for _, a := range msg.report.Agents {
		if a.Name == msg.agent {
			out := renderAgentModels(a, currentAgent, currentModel)
			if !a.Installed {
				return out, 1
			}
			return out, 0
		}
	}
	return errorStyle.Render(fmt.Sprintf("%s: ambiguous-agent did not report agent '%s'", strings.TrimSpace(msg.line), msg.agent)), 1
}

func ufaAgentHelpText() string {
	lines := []string{
		sessionStyle.Render("ufa agent — agents and models on this host (via ambiguous-agent --inspect)"),
		"",
		"  ufa agent help                      show this help",
		"  ufa agent list                      list agents: installed or not, version, path",
		"  ufa agent model list [<agent>]      list an agent's models (default: the current agent)",
		"",
		sessionStyle.Render("use 'set-agent <name>' / 'set-model <name>' to change the selection"),
	}
	return strings.Join(lines, "\n")
}
