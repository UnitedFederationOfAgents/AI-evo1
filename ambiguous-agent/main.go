// ambiguous-agent provides a generic interface for invoking AI coding agents
// without knowing which agent/model will fulfill the request.
//
// It wraps calls with clauditable for record-keeping and supports:
// - Agent selection (-a flag)
// - Mode flags: -p (prompt only), -r (read), -w (write), -x (execute)
// - Session awareness via AGENT_SESSION environment variable
// - Add-dir functionality for accessing agent records
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Mode constants for agent permission levels
const (
	ModePrompt  = "p" // Prompt only - chat without file access
	ModeRead    = "r" // Read only - can read files but not modify
	ModeWrite   = "w" // Read and write files
	ModeExecute = "x" // Full access - read, write, and execute commands
)

// Default configuration
const (
	DefaultAgent       = "claude"
	DefaultMode        = ModeRead
	DefaultRecordsPath = "/workspaces/agent-records/"
)

// AgentConfig defines how to invoke a specific AI CLI agent
type AgentConfig struct {
	Command       string   // Base command to run (e.g., "claude", "gemini")
	PromptFlag    string   // Flag to pass prompts (e.g., "-p")
	AddDirFlag    string   // Flag to add directories (if supported)
	ModelFlag     string   // Flag to specify model (if supported)
	DefaultModel  string   // Default model for this agent
	Models        []string // Available models for this agent
	ModeArgs      map[string][]string // Arguments per mode
}

// agentConfigs maps agent names to their configuration
var agentConfigs = map[string]*AgentConfig{
	"copilot": {
		Command:    "copilot",
		PromptFlag: "-p",
		AddDirFlag: "--add-dir",
		ModeArgs: map[string][]string{
			ModePrompt:  {},
			ModeRead:    {},
			ModeWrite:   {},
			ModeExecute: {"--allow-all-tools"},
		},
	},
	"gemini": {
		Command:    "gemini",
		PromptFlag: "-p",
		ModelFlag:  "--model",
		Models:     []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash-001", "gemini-2.0-flash-lite"},
		ModeArgs: map[string][]string{
			ModePrompt:  {},
			ModeRead:    {},
			ModeWrite:   {},
			ModeExecute: {"--sandbox=permissive"},
		},
	},
	"claude": {
		Command:    "claude",
		PromptFlag: "-p",
		AddDirFlag: "--add-dir",
		ModelFlag:  "--model",
		Models:     []string{"opus", "sonnet", "haiku", "claude-opus-4-5-20251101", "claude-sonnet-4-20250514", "claude-sonnet-4-5-20250929"},
		ModeArgs: map[string][]string{
			ModePrompt:  {},
			ModeRead:    {},
			ModeWrite:   {"--permission-mode", "acceptEdits"},
			ModeExecute: {"--dangerously-skip-permissions"},
		},
	},
	"opencode": {
		Command:    "opencode",
		PromptFlag: "", // opencode uses positional prompt
		ModelFlag:  "--model",
		Models:     []string{"openai/gpt-4.1", "openai/gpt-4.1-mini", "openai/gpt-4.1-nano", "openai/o4-mini", "openai/o3", "openai/o3-mini", "anthropic/claude-sonnet-4-20250514", "anthropic/claude-opus-4-5-20251101", "google/gemini-2.5-pro", "google/gemini-2.5-flash"},
		ModeArgs: map[string][]string{
			ModePrompt:  {},
			ModeRead:    {},
			ModeWrite:   {},
			ModeExecute: {},
		},
	},
	"codex": {
		Command:    "codex",
		PromptFlag: "-p",
		ModeArgs: map[string][]string{
			ModePrompt:  {},
			ModeRead:    {},
			ModeWrite:   {},
			ModeExecute: {"--full-auto"},
		},
	},
	"grok": {
		Command:    "grok",
		PromptFlag: "-p",
		ModelFlag:  "--model",
		Models:     []string{"grok-4.20-multi-agent-0309", "grok-4.20-multi-agent", "grok-code-fast-1", "grok-code-fast", "grok-3", "grok-3-mini"},
		ModeArgs: map[string][]string{
			ModePrompt:  {},
			ModeRead:    {},
			ModeWrite:   {},
			ModeExecute: {},
		},
	},
	// clod is a test agent for CI/development
	"clod": {
		Command:    "clod",
		PromptFlag: "-p",
		ModeArgs: map[string][]string{
			ModePrompt:  {},
			ModeRead:    {},
			ModeWrite:   {"--permission-mode", "acceptEdits"},
			ModeExecute: {"--permission-mode", "acceptEdits"},
		},
	},
}

// availableAgents is the list of supported agents (order matters for display)
var availableAgents = []string{"copilot", "gemini", "claude", "opencode", "codex", "grok", "clod"}

// Agent colors for visual distinction (matching sandbox/AI-sandboxing/ambiguous-agent/main.go)
var agentColors = map[string]lipgloss.Color{
	"copilot":  lipgloss.Color("39"),  // Cyan (GitHub blue)
	"gemini":   lipgloss.Color("33"),  // Blue (Google blue)
	"claude":   lipgloss.Color("208"), // Orange (Anthropic)
	"opencode": lipgloss.Color("34"),  // Green
	"codex":    lipgloss.Color("99"),  // Purple (OpenAI)
	"grok":     lipgloss.Color("196"), // Red (xAI)
	"clod":     lipgloss.Color("141"), // Light purple (test agent)
}

// Styles for output
var (
	sessionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Italic(true)

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("34"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	modeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("220")).
			Bold(true)
)

// getAgentStyle returns a styled text renderer for the given agent
func getAgentStyle(agent string) lipgloss.Style {
	color := agentColors[agent]
	if color == "" {
		color = lipgloss.Color("141") // Fallback purple
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true)
}

// modeDescription returns a human-readable description of the mode
func modeDescription(mode string) string {
	switch mode {
	case ModePrompt:
		return "prompt (chat only)"
	case ModeRead:
		return "read (files read-only)"
	case ModeWrite:
		return "write (files read/write)"
	case ModeExecute:
		return "execute (full access)"
	default:
		return "unknown"
	}
}

func main() {
	// Define flags
	var (
		agent      string
		model      string
		promptMode bool
		readMode   bool
		writeMode  bool
		execMode   bool
		listAgents bool
		addDirs    string
		prompt     string
		session    string
	)

	flag.StringVar(&agent, "a", "", "Select agent (default: claude, or AGENT_NAME env var)")
	flag.StringVar(&model, "m", "", "Select model for agent (if supported)")
	flag.BoolVar(&promptMode, "p", false, "Prompt mode: chat only, no file access")
	flag.BoolVar(&readMode, "r", false, "Read mode: read files only (default)")
	flag.BoolVar(&writeMode, "w", false, "Write mode: read and write files")
	flag.BoolVar(&execMode, "x", false, "Execute mode: full access including command execution")
	flag.BoolVar(&listAgents, "list-agents", false, "List available agents")
	flag.StringVar(&addDirs, "add-dirs", "", "Colon-separated list of directories to add (for agent records access)")
	flag.StringVar(&prompt, "prompt", "", "Prompt to send to the agent (alternative to positional argument)")
	flag.StringVar(&session, "session", "", "Session identifier (default: AGENT_SESSION env var or auto-generated)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `ambiguous-agent - Generic interface for AI coding agents

Usage:
  ambiguous-agent [options] <prompt>
  ambiguous-agent --list-agents

Modes (mutually exclusive, default is -r):
  -p    Prompt mode: chat only, no file access
  -r    Read mode: read files only (default)
  -w    Write mode: read and write files
  -x    Execute mode: full access including command execution

Options:
  -a <agent>      Select agent (default: claude, or AGENT_NAME env var)
  -m <model>      Select model for agent (if supported)
  -add-dirs <dirs> Colon-separated directories to add for agent records access
  -prompt <text>  Prompt text (alternative to positional argument)
  -session <id>   Session identifier (default: AGENT_SESSION or auto)
  --list-agents   List available agents and exit

Environment:
  AGENT_NAME          Default agent selection
  AGENT_MODEL         Default model selection
  AGENT_SESSION       Session identifier (used for records grouping)
  AGENT_RECORDS_PATH  Directory for agent records (default: %s)
  AGENT_ADD_DIRS      Colon-separated additional directories to add

Examples:
  ambiguous-agent -r "What files are in this directory?"
  ambiguous-agent -w -a gemini "Update the README with installation instructions"
  ambiguous-agent -x "Run the tests and fix any failures"
  ambiguous-agent --list-agents

`, DefaultRecordsPath)
	}

	flag.Parse()

	// Handle --list-agents
	if listAgents {
		printAgentList()
		return
	}

	// Get prompt from positional args or -prompt flag
	if prompt == "" && flag.NArg() > 0 {
		prompt = strings.Join(flag.Args(), " ")
	}

	if prompt == "" {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: no prompt provided"))
		fmt.Fprintln(os.Stderr, "Usage: ambiguous-agent [options] <prompt>")
		fmt.Fprintln(os.Stderr, "Use --help for more information")
		os.Exit(1)
	}

	// Determine agent
	if agent == "" {
		agent = os.Getenv("AGENT_NAME")
	}
	if agent == "" {
		agent = DefaultAgent
	}

	// Validate agent
	config, ok := agentConfigs[agent]
	if !ok {
		fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("Error: unknown agent '%s'", agent)))
		fmt.Fprintln(os.Stderr, sessionStyle.Render("Use --list-agents to see available agents"))
		os.Exit(1)
	}

	// Determine model
	if model == "" {
		model = os.Getenv("AGENT_MODEL")
	}

	// Determine mode (default to read, only one can be set)
	mode := DefaultMode
	modeCount := 0
	if promptMode {
		mode = ModePrompt
		modeCount++
	}
	if readMode {
		mode = ModeRead
		modeCount++
	}
	if writeMode {
		mode = ModeWrite
		modeCount++
	}
	if execMode {
		mode = ModeExecute
		modeCount++
	}
	if modeCount > 1 {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: only one mode flag (-p, -r, -w, -x) can be specified"))
		os.Exit(1)
	}

	// Get session
	if session == "" {
		session = os.Getenv("AGENT_SESSION")
	}
	if session == "" {
		// Auto-generate session based on current time
		now := time.Now()
		session = fmt.Sprintf("session-%s_%d", now.Format("2006-01-02_15-04-05"), now.Unix())
	}

	// Get records path
	recordsPath := os.Getenv("AGENT_RECORDS_PATH")
	if recordsPath == "" {
		recordsPath = DefaultRecordsPath
	}

	// Collect additional directories
	var additionalDirs []string
	if addDirs != "" {
		additionalDirs = strings.Split(addDirs, ":")
	}
	if envAddDirs := os.Getenv("AGENT_ADD_DIRS"); envAddDirs != "" {
		additionalDirs = append(additionalDirs, strings.Split(envAddDirs, ":")...)
	}

	// Build session directory path
	sessionDir := filepath.Join(recordsPath, session)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to create session directory: %v\n", err)
	}

	// Print invocation info with visual flare
	agentStyled := getAgentStyle(agent)
	modeStyled := modeStyle.Render(modeDescription(mode))

	if model != "" {
		fmt.Println(lipgloss.JoinHorizontal(lipgloss.Top,
			sessionStyle.Render("invoking "),
			agentStyled.Render(agent),
			sessionStyle.Render(" ("),
			agentStyled.Render(model),
			sessionStyle.Render(") in "),
			modeStyled,
			sessionStyle.Render(" mode..."),
		))
	} else {
		fmt.Println(lipgloss.JoinHorizontal(lipgloss.Top,
			sessionStyle.Render("invoking "),
			agentStyled.Render(agent),
			sessionStyle.Render(" in "),
			modeStyled,
			sessionStyle.Render(" mode..."),
		))
	}
	fmt.Println(sessionStyle.Render(fmt.Sprintf("● session: %s", sessionDir)))

	// Build the agent command
	args := buildAgentArgs(config, mode, model, prompt, sessionDir, additionalDirs)

	// Wrap with clauditable
	exitCode := invokeWithClauditable(config.Command, args, agent, model, sessionDir)

	// Print completion status
	if exitCode == 0 {
		fmt.Println(successStyle.Render("agent completed successfully"))
	} else {
		fmt.Println(errorStyle.Render(fmt.Sprintf("agent exited with code %d", exitCode)))
	}

	os.Exit(exitCode)
}

// buildAgentArgs constructs the command-line arguments for the agent
func buildAgentArgs(config *AgentConfig, mode, model, prompt, sessionDir string, additionalDirs []string) []string {
	var args []string

	// Add model flag if specified and supported
	if model != "" && config.ModelFlag != "" {
		args = append(args, config.ModelFlag, model)
	}

	// Add mode-specific arguments
	if modeArgs, ok := config.ModeArgs[mode]; ok {
		args = append(args, modeArgs...)
	}

	// Add directories if supported
	if config.AddDirFlag != "" {
		// Always add session directory for records access
		args = append(args, config.AddDirFlag, sessionDir)

		// Add additional directories
		for _, dir := range additionalDirs {
			dir = strings.TrimSpace(dir)
			if dir != "" {
				args = append(args, config.AddDirFlag, dir)
			}
		}
	}

	// Add prompt
	if config.PromptFlag != "" {
		args = append(args, config.PromptFlag, prompt)
	} else {
		// For agents like opencode that use positional prompts
		args = append(args, prompt)
	}

	return args
}

// invokeWithClauditable wraps the agent invocation with clauditable for record-keeping
func invokeWithClauditable(agentCmd string, args []string, agent, model, sessionDir string) int {
	// Check if clauditable is available
	clauditablePath, err := exec.LookPath("clauditable")
	if err != nil {
		// Clauditable not found, invoke agent directly with warning
		fmt.Fprintln(os.Stderr, sessionStyle.Render("Warning: clauditable not found, invoking agent directly"))
		return invokeAgent(agentCmd, args)
	}

	// Build clauditable command: clauditable <agent-command> <args...>
	clauditableArgs := append([]string{agentCmd}, args...)
	cmd := exec.Command(clauditablePath, clauditableArgs...)

	// Set environment variables for clauditable
	env := os.Environ()
	env = append(env,
		"AGENT_SESSION="+filepath.Base(sessionDir),
		"AGENT_RECORDS_PATH="+filepath.Dir(sessionDir),
		"UFA_AGENT="+agent,
	)
	if model != "" {
		env = append(env, "UFA_MODEL="+model)
	}
	cmd.Env = env

	// Connect I/O
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

// invokeAgent invokes the agent directly without clauditable wrapping
func invokeAgent(agentCmd string, args []string) int {
	cmd := exec.Command(agentCmd, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

// printAgentList displays available agents with visual styling
func printAgentList() {
	fmt.Println(sessionStyle.Render("Available agents:"))
	fmt.Println()

	for _, agent := range availableAgents {
		config := agentConfigs[agent]
		agentStyled := getAgentStyle(agent)

		// Build agent info line
		info := agentStyled.Render(fmt.Sprintf("  %s", agent))

		// Add model support indicator
		if len(config.Models) > 0 {
			info += sessionStyle.Render(" (supports model selection)")
		}

		// Add add-dir support indicator
		if config.AddDirFlag != "" {
			info += sessionStyle.Render(" [add-dir]")
		}

		fmt.Println(info)

		// Show available models if any
		if len(config.Models) > 0 {
			modelsStr := strings.Join(config.Models, ", ")
			if len(modelsStr) > 60 {
				modelsStr = modelsStr[:57] + "..."
			}
			fmt.Println(sessionStyle.Render(fmt.Sprintf("    models: %s", modelsStr)))
		}
	}
	fmt.Println()
	fmt.Println(sessionStyle.Render("Use -a <agent> to select an agent"))
}

// Ensure io is used (for future stdout/stderr handling if needed)
var _ = io.Copy
