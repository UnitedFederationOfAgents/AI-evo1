// federation-command is an interactive CLI shell for orchestrating AI agents.
// It wraps all commands with clauditable for record-keeping and supports:
// - Interactive readline-based input with multi-line support
// - Agent selection and model configuration
// - Mode-based invocation (-p/r/w/x for prompt/read/write/execute)
// - Session recording and management
//
// This is the spiritual successor to sandbox/AI-sandboxing/ambiguous-agent/ambiguous-shell.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/chzyer/readline"
	"github.com/google/uuid"
)

// Version information
const Version = "0.1.0"

// Default configuration
const (
	DefaultRecordsPath = "/host-agent-files/agent-records"
	DefaultAgent       = "claude"
)

// Mode constants for agent permission levels
const (
	ModePrompt  = "p" // Prompt only - chat without file access
	ModeRead    = "r" // Read only - can read files but not modify
	ModeWrite   = "w" // Read and write files
	ModeExecute = "x" // Full access - read, write, and execute commands
)

// Environment variables
const (
	EnvAgentRecordsPath = "AGENT_RECORDS_PATH"
	EnvAgentName        = "AGENT_NAME"
	EnvAgentModel       = "AGENT_MODEL"
	EnvAgentSession     = "AGENT_SESSION"
	EnvIsClauditable    = "IS_CLAUDITABLE" // Used to prevent double-wrapping
)

// Available agents (must match ambiguous-agent configurations)
var availableAgents = []string{"copilot", "gemini", "claude", "opencode", "codex", "grok", "clod"}

// AgentModelConfig defines model support for each agent
type AgentModelConfig struct {
	ModelFlag    string   // CLI flag to pass model (e.g., "--model")
	DefaultModel string   // Default model, empty means agent's built-in default
	Models       []string // Available models for this agent
}

// agentModelConfigs maps agent names to their model configuration
var agentModelConfigs = map[string]AgentModelConfig{
	"copilot": {ModelFlag: "", DefaultModel: "", Models: nil},
	"gemini": {
		ModelFlag:    "--model",
		DefaultModel: "",
		Models: []string{
			"gemini-2.5-pro", "gemini-2.5-flash",
			"gemini-2.0-flash-001", "gemini-2.0-flash-lite",
		},
	},
	"claude": {
		ModelFlag:    "--model",
		DefaultModel: "",
		Models: []string{
			"opus", "sonnet", "haiku",
			"claude-opus-4-5-20251101", "claude-sonnet-4-20250514", "claude-sonnet-4-5-20250929",
		},
	},
	"opencode": {
		ModelFlag:    "--model",
		DefaultModel: "",
		Models: []string{
			"openai/gpt-4.1", "openai/gpt-4.1-mini", "openai/gpt-4.1-nano",
			"openai/o4-mini", "openai/o3", "openai/o3-mini",
			"anthropic/claude-sonnet-4-20250514", "anthropic/claude-opus-4-5-20251101",
			"google/gemini-2.5-pro", "google/gemini-2.5-flash",
		},
	},
	"codex": {ModelFlag: "", DefaultModel: "", Models: nil},
	"grok": {
		ModelFlag:    "--model",
		DefaultModel: "",
		Models: []string{
			"grok-4.20-multi-agent-0309", "grok-4.20-multi-agent", "grok-4.20-multi-agent-beta",
			"grok-4.20-0309-reasoning", "grok-4.20-beta-0309", "grok-4.20-beta", "grok-beta",
			"grok-4.20-0309-non-reasoning",
			"grok-4-1-fast-reasoning", "grok-4-1-fast",
			"grok-4-1-fast-non-reasoning",
			"grok-4-fast-reasoning", "grok-4-fast",
			"grok-4-fast-non-reasoning",
			"grok-4-0709",
			"grok-code-fast-1", "grok-code-fast",
			"grok-3",
			"grok-3-mini", "grok-3-mini-fast",
		},
	},
	"clod": {ModelFlag: "", DefaultModel: "", Models: nil},
}

// Agent colors for visual distinction (matching ambiguous-agent)
var agentColors = map[string]lipgloss.Color{
	"copilot":  lipgloss.Color("39"),  // Cyan (GitHub blue)
	"gemini":   lipgloss.Color("33"),  // Blue (Google blue)
	"claude":   lipgloss.Color("208"), // Orange (Anthropic)
	"opencode": lipgloss.Color("34"),  // Green
	"codex":    lipgloss.Color("99"),  // Purple (OpenAI)
	"grok":     lipgloss.Color("196"), // Red (xAI)
	"clod":     lipgloss.Color("141"), // Light purple (test agent)
}

// Styles - consistent with ambiguous-agent
var (
	sessionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Italic(true)

	promptStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("39")).
			Bold(true)

	exitStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("208"))

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("34"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	continuationStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243"))

	modeStyles = map[string]lipgloss.Style{
		ModePrompt:  lipgloss.NewStyle().Foreground(lipgloss.Color("34")).Bold(true),  // green
		ModeRead:    lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true), // yellow
		ModeWrite:   lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true), // orange
		ModeExecute: lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true), // red
	}
)

// ShellCompleter implements readline.AutoCompleter for tab completion
type ShellCompleter struct {
	cwd *string // Pointer to track current working directory changes
}

// Do implements readline.AutoCompleter
func (c *ShellCompleter) Do(line []rune, pos int) (newLine [][]rune, length int) {
	lineStr := string(line[:pos])

	// Extract the word being completed (last space-separated token)
	lastSpace := strings.LastIndex(lineStr, " ")
	var prefix string
	if lastSpace == -1 {
		prefix = lineStr
	} else {
		prefix = lineStr[lastSpace+1:]
	}

	// Use filepath completion
	candidates := completeFilepath(prefix, *c.cwd)

	// Convert candidates to readline format
	length = len(prefix)
	for _, cand := range candidates {
		suffix := []rune(cand[len(prefix):])
		newLine = append(newLine, suffix)
	}

	return newLine, length
}

// completeFilepath returns filepath completion candidates for the given prefix
func completeFilepath(prefix string, cwd string) []string {
	if prefix == "" {
		return listDir(cwd, "", true)
	}

	home := os.Getenv("HOME")

	// Expand tilde
	expandedPrefix := prefix
	tildeExpanded := false
	if strings.HasPrefix(prefix, "~/") {
		expandedPrefix = filepath.Join(home, prefix[2:])
		tildeExpanded = true
	} else if prefix == "~" {
		expandedPrefix = home
		tildeExpanded = true
	}

	// Determine the directory to search and the partial filename
	var searchDir, partial string
	if filepath.IsAbs(expandedPrefix) {
		searchDir = filepath.Dir(expandedPrefix)
		partial = filepath.Base(expandedPrefix)
	} else {
		searchDir = filepath.Join(cwd, filepath.Dir(expandedPrefix))
		partial = filepath.Base(expandedPrefix)
	}

	// Handle case where prefix ends with separator
	if strings.HasSuffix(expandedPrefix, string(filepath.Separator)) || expandedPrefix == home {
		searchDir = expandedPrefix
		partial = ""
	}

	candidates := listDir(searchDir, partial, false)

	var result []string
	for _, cand := range candidates {
		fullPath := filepath.Join(searchDir, cand)

		// Check if it's a directory and append separator
		if info, err := os.Stat(fullPath); err == nil && info.IsDir() {
			cand += string(filepath.Separator)
		}

		// Build the completion string
		var completion string
		if tildeExpanded {
			if strings.HasPrefix(fullPath, home) {
				completion = "~" + fullPath[len(home):]
			} else {
				completion = fullPath
			}
		} else if filepath.IsAbs(prefix) || strings.Contains(prefix, string(filepath.Separator)) {
			dir := filepath.Dir(prefix)
			if strings.HasSuffix(prefix, string(filepath.Separator)) {
				completion = prefix + cand
			} else {
				completion = filepath.Join(dir, cand)
			}
			if info, err := os.Stat(fullPath); err == nil && info.IsDir() && !strings.HasSuffix(completion, string(filepath.Separator)) {
				completion += string(filepath.Separator)
			}
		} else {
			completion = cand
			if info, err := os.Stat(fullPath); err == nil && info.IsDir() && !strings.HasSuffix(completion, string(filepath.Separator)) {
				completion += string(filepath.Separator)
			}
		}

		result = append(result, completion)
	}

	return result
}

// listDir returns entries in dir that start with prefix
func listDir(dir string, prefix string, showHidden bool) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	showDotFiles := showHidden || strings.HasPrefix(prefix, ".")

	var result []string
	for _, entry := range entries {
		name := entry.Name()

		if !showDotFiles && strings.HasPrefix(name, ".") {
			continue
		}

		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}

		result = append(result, name)
	}

	sort.Strings(result)
	return result
}

// CommandRecord holds metadata for each command
type CommandRecord struct {
	ID        string `json:"id"`
	Command   string `json:"cmd"`
	Timestamp string `json:"ts"`
	DeltaMs   int64  `json:"delta_ms"`
	ExitCode  int    `json:"exit"`
}

func main() {
	// Handle --version flag
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("federation-command %s\n", Version)
		return
	}

	recordsPath := os.Getenv(EnvAgentRecordsPath)
	if recordsPath == "" {
		recordsPath = DefaultRecordsPath
	}

	// Initialize current agent from environment or use default
	currentAgent := os.Getenv(EnvAgentName)
	if currentAgent == "" {
		currentAgent = DefaultAgent
	}

	// Initialize current model from environment
	currentModel := os.Getenv(EnvAgentModel)

	// Create session directory
	// Use AGENT_SESSION if set, otherwise generate one with timestamp format
	// Note: We use our own session format, not just date, to allow multiple sessions per day
	now := time.Now()
	sessionID := os.Getenv(EnvAgentSession)
	if sessionID == "" {
		// Generate session ID: date_time_unix (no "session-" prefix to match clauditable format)
		sessionID = fmt.Sprintf("%s_%d", now.Format("2006-01-02_15-04-05"), now.Unix())
	}
	sessionDir := filepath.Join(recordsPath, sessionID)

	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error creating session directory: %v\n", err)
		os.Exit(1)
	}

	// Set environment for child processes
	os.Setenv(EnvAgentRecordsPath, recordsPath)
	os.Setenv(EnvAgentSession, sessionID)

	logPath := filepath.Join(sessionDir, "session.jsonl")
	logFile, err := os.Create(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating session log: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	// Print session info
	fmt.Println(sessionStyle.Render(fmt.Sprintf("● session: %s", sessionDir)))
	fmt.Println(sessionStyle.Render(fmt.Sprintf("  agent: %s | 'set-agent <name>' to change | 'list-agents' for options", currentAgent)))
	fmt.Println(sessionStyle.Render("  model: 'set-model <name>' to override | 'list-models' for options"))
	fmt.Println(sessionStyle.Render("  type 'exit!' to end | 'agent <prompt>' to invoke AI"))
	fmt.Println(sessionStyle.Render("  modes: -p (prompt) | -r (read) | -w (write) | -x (execute)"))
	fmt.Println(sessionStyle.Render("  multi-line: trailing \\, unclosed quotes, or <<<DELIMITER"))
	fmt.Println()

	readlineRecords := filepath.Join(os.Getenv("HOME"), ".federation_records")
	cwd, _ := os.Getwd()
	oldCwd := cwd

	completer := &ShellCompleter{cwd: &cwd}

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          buildPrompt(cwd, currentAgent, currentModel, 0),
		HistoryFile:     readlineRecords,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
		AutoComplete:    completer,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error initializing readline: %v\n", err)
		os.Exit(1)
	}
	defer rl.Close()

	var lastCommandTime time.Time
	var lastExitCode int
	encoder := json.NewEncoder(logFile)

	for {
		initialLine, err := rl.Readline()
		if err == readline.ErrInterrupt {
			if len(initialLine) == 0 {
				break
			}
			continue
		}
		if err == io.EOF {
			break
		}

		initialLine = strings.TrimSpace(initialLine)
		if initialLine == "" {
			continue
		}

		// Handle multi-line input
		mainPrompt := buildPrompt(cwd, currentAgent, currentModel, lastExitCode)
		line, err := readMultiLine(rl, initialLine, mainPrompt)
		if err == readline.ErrInterrupt {
			continue
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("input error: %v", err)))
			continue
		}

		if line == "exit!" {
			fmt.Println(exitStyle.Render("session ended."))
			break
		}

		// Calculate delta from last command
		var deltaMs int64
		commandTime := time.Now()
		if !lastCommandTime.IsZero() {
			deltaMs = commandTime.Sub(lastCommandTime).Milliseconds()
		}
		lastCommandTime = commandTime

		var exitCode int

		// Handle cd command
		if line == "cd" || strings.HasPrefix(line, "cd ") {
			target := strings.TrimSpace(strings.TrimPrefix(line, "cd"))
			newDir, err := handleCd(target, cwd, oldCwd)
			if err != nil {
				fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("cd: %v", err)))
			} else {
				oldCwd = cwd
				cwd = newDir
				rl.SetPrompt(buildPrompt(cwd, currentAgent, currentModel, lastExitCode))
			}
			continue
		}

		// Handle export command
		if line == "export" || strings.HasPrefix(line, "export ") {
			arg := strings.TrimSpace(strings.TrimPrefix(line, "export"))
			if err := handleExport(arg); err != nil {
				fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("export: %v", err)))
			}
			continue
		}

		// Handle built-in commands
		if strings.HasPrefix(line, "set-agent ") {
			newAgent := strings.TrimSpace(strings.TrimPrefix(line, "set-agent "))
			if isValidAgent(newAgent) {
				currentAgent = newAgent
				currentModel = ""
				rl.SetPrompt(buildPrompt(cwd, currentAgent, currentModel, lastExitCode))
				fmt.Println(successStyle.Render(fmt.Sprintf("agent set to: %s", currentAgent)))
			} else {
				fmt.Println(errorStyle.Render(fmt.Sprintf("unknown agent: %s", newAgent)))
				fmt.Println(sessionStyle.Render(fmt.Sprintf("available: %s", strings.Join(availableAgents, ", "))))
			}
			continue
		} else if line == "set-agent" {
			fmt.Println(errorStyle.Render("usage: set-agent <name>"))
			fmt.Println(sessionStyle.Render(fmt.Sprintf("available: %s", strings.Join(availableAgents, ", "))))
			continue
		} else if line == "list-agents" {
			listAgents(currentAgent)
			continue
		} else if strings.HasPrefix(line, "set-model ") {
			newModel := strings.TrimSpace(strings.TrimPrefix(line, "set-model "))
			if err := setModel(currentAgent, newModel); err != nil {
				fmt.Println(errorStyle.Render(err.Error()))
			} else {
				currentModel = newModel
				rl.SetPrompt(buildPrompt(cwd, currentAgent, currentModel, lastExitCode))
				fmt.Println(successStyle.Render(fmt.Sprintf("model set to: %s", currentModel)))
			}
			continue
		} else if line == "set-model" {
			fmt.Println(errorStyle.Render("usage: set-model <name>"))
			fmt.Println(sessionStyle.Render("use 'list-models' to see available models for current agent"))
			continue
		} else if line == "clear-model" {
			currentModel = ""
			rl.SetPrompt(buildPrompt(cwd, currentAgent, currentModel, lastExitCode))
			fmt.Println(successStyle.Render("model cleared - using agent's default"))
			continue
		} else if line == "list-models" {
			listModels(currentAgent, currentModel, sessionDir)
			continue
		} else if strings.HasPrefix(line, "agent ") {
			prompt := strings.TrimPrefix(line, "agent ")
			exitCode = runAgent(prompt, currentAgent, currentModel, sessionDir)
		} else if line == "agent" {
			fmt.Println(errorStyle.Render("usage: agent [-p|-r|-w|-x] <prompt>"))
			continue
		} else {
			// Regular command - wrap with clauditable using agent=none
			exitCode = runCommand(line, sessionDir)
		}

		lastExitCode = exitCode

		// Write metadata record
		record := CommandRecord{
			ID:        uuid.New().String()[:8],
			Command:   line,
			Timestamp: commandTime.Format(time.RFC3339),
			DeltaMs:   deltaMs,
			ExitCode:  exitCode,
		}
		encoder.Encode(record)

		// Update prompt
		newCwd, _ := os.Getwd()
		if newCwd != cwd {
			cwd = newCwd
		}
		rl.SetPrompt(buildPrompt(cwd, currentAgent, currentModel, lastExitCode))
	}
}

// isValidAgent checks if the agent name is in the available agents list
func isValidAgent(name string) bool {
	for _, a := range availableAgents {
		if a == name {
			return true
		}
	}
	return false
}

// setModel validates and sets the model for the current agent
func setModel(agent string, model string) error {
	cfg, ok := agentModelConfigs[agent]
	if !ok || len(cfg.Models) == 0 {
		return fmt.Errorf("agent '%s' does not support model selection", agent)
	}

	var models []string

	if agent == "opencode" || agent == "grok" {
		// Query models dynamically from the tool
		cmd := exec.Command(agent, "models")
		output, err := cmd.Output()
		if err != nil {
			models = cfg.Models
		} else {
			if agent == "grok" {
				models = parseGrokModels(string(output))
			} else {
				lines := strings.Split(strings.TrimSpace(string(output)), "\n")
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line != "" {
						models = append(models, line)
					}
				}
			}
		}
	} else {
		models = cfg.Models
	}

	valid := false
	for _, m := range models {
		if m == model {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("model '%s' is not available for agent '%s'", model, agent)
	}

	return nil
}

// listAgents displays available agents
func listAgents(currentAgent string) {
	fmt.Println(sessionStyle.Render("available agents:"))
	for _, a := range availableAgents {
		color := agentColors[a]
		if color == "" {
			color = lipgloss.Color("141")
		}
		agentNameStyle := lipgloss.NewStyle().Foreground(color).Bold(true)
		modelSupport := ""
		if cfg, ok := agentModelConfigs[a]; ok && len(cfg.Models) > 0 {
			modelSupport = " (supports model selection)"
		}
		if a == currentAgent {
			fmt.Println(agentNameStyle.Render(fmt.Sprintf("  -> %s (selected)%s", a, modelSupport)))
		} else {
			fmt.Println(agentNameStyle.Render(fmt.Sprintf("    %s%s", a, modelSupport)))
		}
	}
}

// listModels displays available models for an agent by calling ambiguous-agent
// via clauditable for consistency and record-keeping
func listModels(agent string, currentModel string, sessionDir string) {
	// Check for double-wrapping
	if os.Getenv(EnvIsClauditable) == "true" {
		// Already in clauditable context, call ambiguous-agent directly
		listModelsDirect(agent, currentModel)
		return
	}

	// Find binaries
	ambiguousAgentPath, err := findBinary("ambiguous-agent")
	if err != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: ambiguous-agent not found"))
		listModelsFallback(agent, currentModel)
		return
	}

	clauditablePath, err := findBinary("clauditable")
	if err != nil {
		// Fallback to direct call without clauditable
		listModelsDirect(agent, currentModel)
		return
	}

	// Call ambiguous-agent --list-models via clauditable
	cmd := exec.Command(clauditablePath, ambiguousAgentPath, "--list-models", "-a", agent)

	env := os.Environ()
	env = append(env,
		EnvAgentRecordsPath+"="+filepath.Dir(sessionDir),
		EnvAgentSession+"="+filepath.Base(sessionDir),
		"UFA_AGENT=none",
		EnvIsClauditable+"=true",
	)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// Fall back to direct call on error
		listModelsFallback(agent, currentModel)
		return
	}

	// Show current selection status (ambiguous-agent doesn't know about it)
	if currentModel != "" {
		fmt.Println()
		fmt.Println(sessionStyle.Render(fmt.Sprintf("current selection: %s", currentModel)))
	} else {
		fmt.Println()
		fmt.Println(sessionStyle.Render("no model explicitly set - using agent's built-in default"))
	}
}

// listModelsDirect calls ambiguous-agent directly without clauditable
func listModelsDirect(agent string, currentModel string) {
	ambiguousAgentPath, err := findBinary("ambiguous-agent")
	if err != nil {
		listModelsFallback(agent, currentModel)
		return
	}

	cmd := exec.Command(ambiguousAgentPath, "--list-models", "-a", agent)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		listModelsFallback(agent, currentModel)
		return
	}

	// Show current selection status
	if currentModel != "" {
		fmt.Println()
		fmt.Println(sessionStyle.Render(fmt.Sprintf("current selection: %s", currentModel)))
	} else {
		fmt.Println()
		fmt.Println(sessionStyle.Render("no model explicitly set - using agent's built-in default"))
	}
}

// listModelsFallback displays models using the local configuration when ambiguous-agent is unavailable
func listModelsFallback(agent string, currentModel string) {
	cfg, ok := agentModelConfigs[agent]
	if !ok || len(cfg.Models) == 0 {
		fmt.Println(sessionStyle.Render(fmt.Sprintf("agent '%s' does not support model selection", agent)))
		fmt.Println(sessionStyle.Render("the agent uses its built-in default model"))
		return
	}

	color := agentColors[agent]
	if color == "" {
		color = lipgloss.Color("141")
	}
	agentNameStyle := lipgloss.NewStyle().Foreground(color).Bold(true)

	fmt.Println(sessionStyle.Render(fmt.Sprintf("available models for %s (fallback list):", agentNameStyle.Render(agent))))

	for _, m := range cfg.Models {
		prefix := "    "
		suffix := ""
		if m == currentModel {
			prefix = "  -> "
			suffix = " (selected)"
		} else if m == cfg.DefaultModel {
			suffix = " (default)"
		}
		fmt.Println(sessionStyle.Render(fmt.Sprintf("%s%s%s", prefix, m, suffix)))
	}

	if currentModel == "" {
		fmt.Println()
		fmt.Println(sessionStyle.Render("no model explicitly set - using agent's built-in default"))
	}
}

// handleCd processes a cd command
func handleCd(target string, cwd string, oldCwd string) (string, error) {
	home := os.Getenv("HOME")

	var targetDir string
	switch {
	case target == "" || target == "~":
		targetDir = home
	case target == "-":
		if oldCwd == cwd {
			return "", fmt.Errorf("OLDPWD not set")
		}
		targetDir = oldCwd
		fmt.Println(targetDir)
	case strings.HasPrefix(target, "~/"):
		targetDir = filepath.Join(home, target[2:])
	default:
		if (strings.HasPrefix(target, "\"") && strings.HasSuffix(target, "\"")) ||
			(strings.HasPrefix(target, "'") && strings.HasSuffix(target, "'")) {
			target = target[1 : len(target)-1]
		}
		targetDir = target
	}

	if !filepath.IsAbs(targetDir) {
		targetDir = filepath.Join(cwd, targetDir)
	}

	targetDir = filepath.Clean(targetDir)

	if err := os.Chdir(targetDir); err != nil {
		return "", err
	}

	newCwd, err := os.Getwd()
	if err != nil {
		return targetDir, nil
	}
	return newCwd, nil
}

// handleExport processes an export command
func handleExport(arg string) error {
	if arg == "" {
		for _, env := range os.Environ() {
			fmt.Printf("declare -x %s\n", env)
		}
		return nil
	}

	assignments := parseExportArgs(arg)
	for _, assignment := range assignments {
		if err := processExportAssignment(assignment); err != nil {
			return err
		}
	}
	return nil
}

// parseExportArgs splits export arguments respecting quotes
func parseExportArgs(input string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)

	for _, r := range input {
		switch {
		case !inQuote && (r == '"' || r == '\''):
			inQuote = true
			quoteChar = r
			current.WriteRune(r)
		case inQuote && r == quoteChar:
			inQuote = false
			quoteChar = 0
			current.WriteRune(r)
		case !inQuote && r == ' ':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}

	if current.Len() > 0 {
		args = append(args, current.String())
	}

	return args
}

// processExportAssignment handles a single VAR=value assignment
func processExportAssignment(assignment string) error {
	eqIdx := strings.Index(assignment, "=")
	if eqIdx == -1 {
		return nil
	}

	name := assignment[:eqIdx]
	value := assignment[eqIdx+1:]

	if name == "" {
		return fmt.Errorf("invalid variable name")
	}
	if !isValidVarName(name) {
		return fmt.Errorf("'%s': not a valid identifier", name)
	}

	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
	}

	return os.Setenv(name, value)
}

// isValidVarName checks if a string is a valid shell variable name
func isValidVarName(name string) bool {
	if len(name) == 0 {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
				return false
			}
		} else {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
				return false
			}
		}
	}
	return true
}

// abbreviatePath shortens a path for display in the prompt
func abbreviatePath(path string, maxLen int) string {
	home := os.Getenv("HOME")

	if home != "" && strings.HasPrefix(path, home) {
		path = "~" + path[len(home):]
	}

	if len(path) <= maxLen {
		return path
	}

	if path == "/" || path == "~" {
		return path
	}

	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) <= 2 {
		if len(path) > maxLen {
			return "..." + path[len(path)-(maxLen-3):]
		}
		return path
	}

	result := parts[len(parts)-1]
	for i := len(parts) - 2; i >= 0; i-- {
		candidate := parts[i] + string(filepath.Separator) + result
		if len(candidate)+4 > maxLen {
			break
		}
		result = candidate
	}

	if !strings.HasPrefix(path, result) {
		result = ".../" + result
	}

	return result
}

func buildPrompt(cwd string, agent string, model string, lastExitCode int) string {
	dir := abbreviatePath(cwd, 30)
	color := agentColors[agent]
	if color == "" {
		color = lipgloss.Color("141")
	}
	agentPromptStyle := lipgloss.NewStyle().Foreground(color).Bold(true)

	var promptLabel string
	if model != "" {
		promptLabel = "[" + agent + "::" + model + "]"
	} else {
		promptLabel = "[" + agent + "]"
	}
	prompt := agentPromptStyle.Render(promptLabel) + " " + promptStyle.Render(dir) + " > "
	if lastExitCode != 0 {
		rcStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
		prompt = rcStyle.Render(fmt.Sprintf("[%d]", lastExitCode)) + " " + prompt
	}
	return prompt
}

// runCommand executes a shell command wrapped with clauditable (agent=none)
func runCommand(cmdLine string, sessionDir string) int {
	// Check for double-wrapping
	if os.Getenv(EnvIsClauditable) == "true" {
		// Already in clauditable context, run directly
		return runCommandDirect(cmdLine)
	}

	// Find clauditable
	clauditablePath, err := findBinary("clauditable")
	if err != nil {
		// Fallback to direct execution
		fmt.Fprintln(os.Stderr, sessionStyle.Render("Warning: clauditable not found, running directly"))
		return runCommandDirect(cmdLine)
	}

	// Wrap with clauditable, setting agent to "none" for non-agentic commands
	cmd := exec.Command(clauditablePath, "bash", "-c", cmdLine)

	env := os.Environ()
	env = append(env,
		EnvAgentRecordsPath+"="+filepath.Dir(sessionDir),
		EnvAgentSession+"="+filepath.Base(sessionDir),
		"UFA_AGENT=none",
		EnvIsClauditable+"=true",
	)
	cmd.Env = env
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

// runCommandDirect executes a shell command directly without clauditable wrapping
func runCommandDirect(cmdLine string) int {
	cmd := exec.Command("bash", "-c", cmdLine)
	cmd.Env = os.Environ()
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

// runAgent invokes an AI agent with the given prompt
func runAgent(input string, agent string, model string, sessionDir string) int {
	// Parse mode flags: -p (prompt), -r (read), -w (write), -x (execute)
	mode := ModeRead // Default mode
	args := parseArgs(input)
	var promptParts []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p":
			mode = ModePrompt
		case "-r":
			mode = ModeRead
		case "-w":
			mode = ModeWrite
		case "-x":
			mode = ModeExecute
		default:
			promptParts = append(promptParts, args[i])
		}
	}

	prompt := strings.Join(promptParts, " ")
	if prompt == "" {
		fmt.Println(errorStyle.Render("no prompt provided"))
		return 1
	}

	// Check for double-wrapping
	if os.Getenv(EnvIsClauditable) == "true" {
		// Already in clauditable context, invoke ambiguous-agent directly
		return runAgentDirect(prompt, agent, model, mode, sessionDir)
	}

	// Find ambiguous-agent and clauditable
	ambiguousAgentPath, err := findBinary("ambiguous-agent")
	if err != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: ambiguous-agent not found"))
		return 1
	}

	clauditablePath, err := findBinary("clauditable")
	if err != nil {
		fmt.Fprintln(os.Stderr, sessionStyle.Render("Warning: clauditable not found, invoking agent directly"))
		return runAgentDirect(prompt, agent, model, mode, sessionDir)
	}

	// Build ambiguous-agent args
	var agentArgs []string
	agentArgs = append(agentArgs, "-"+mode) // -p, -r, -w, or -x
	agentArgs = append(agentArgs, "-a", agent)
	if model != "" {
		agentArgs = append(agentArgs, "-m", model)
	}
	agentArgs = append(agentArgs, prompt)

	// Wrap with clauditable
	clauditableArgs := append([]string{ambiguousAgentPath}, agentArgs...)
	cmd := exec.Command(clauditablePath, clauditableArgs...)

	env := os.Environ()
	env = append(env,
		EnvAgentRecordsPath+"="+filepath.Dir(sessionDir),
		EnvAgentSession+"="+filepath.Base(sessionDir),
		"UFA_AGENT="+agent,
		EnvIsClauditable+"=true",
	)
	if model != "" {
		env = append(env, "UFA_MODEL="+model)
	}
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Display invocation message
	color := agentColors[agent]
	if color == "" {
		color = lipgloss.Color("141")
	}
	agentNameStyle := lipgloss.NewStyle().Foreground(color).Bold(true)
	modeStyled := modeStyles[mode].Render(modeDescription(mode))

	if model != "" {
		fmt.Println(lipgloss.JoinHorizontal(lipgloss.Top,
			sessionStyle.Render("invoking "),
			agentNameStyle.Render(agent),
			sessionStyle.Render(" ("),
			agentNameStyle.Render(model),
			sessionStyle.Render(") in "),
			modeStyled,
			sessionStyle.Render(" mode..."),
		))
	} else {
		fmt.Println(lipgloss.JoinHorizontal(lipgloss.Top,
			sessionStyle.Render("invoking "),
			agentNameStyle.Render(agent),
			sessionStyle.Render(" in "),
			modeStyled,
			sessionStyle.Render(" mode..."),
		))
	}

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			fmt.Println(errorStyle.Render(fmt.Sprintf("agent exited: %d", code)))
			return code
		}
		fmt.Println(errorStyle.Render(fmt.Sprintf("agent error: %v", err)))
		return 1
	}

	fmt.Println(successStyle.Render("agent completed"))
	return 0
}

// runAgentDirect invokes ambiguous-agent directly without clauditable wrapping
func runAgentDirect(prompt string, agent string, model string, mode string, sessionDir string) int {
	ambiguousAgentPath, err := findBinary("ambiguous-agent")
	if err != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: ambiguous-agent not found"))
		return 1
	}

	var args []string
	args = append(args, "-"+mode)
	args = append(args, "-a", agent)
	if model != "" {
		args = append(args, "-m", model)
	}
	args = append(args, prompt)

	cmd := exec.Command(ambiguousAgentPath, args...)
	cmd.Env = os.Environ()
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

// modeDescription returns a human-readable description of the mode
func modeDescription(mode string) string {
	switch mode {
	case ModePrompt:
		return "prompt"
	case ModeRead:
		return "read"
	case ModeWrite:
		return "write"
	case ModeExecute:
		return "execute"
	default:
		return "unknown"
	}
}

// findBinary finds a binary by checking PATH first, then the directory of the running executable
func findBinary(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}

	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH and could not determine executable directory: %w", name, err)
	}
	localPath := filepath.Join(filepath.Dir(self), name)
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}

	return "", fmt.Errorf("%s not found on PATH or in %s", name, filepath.Dir(self))
}

// parseArgs splits a string into arguments, respecting quoted strings
func parseArgs(input string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)

	for _, r := range input {
		switch {
		case !inQuote && (r == '"' || r == '\''):
			inQuote = true
			quoteChar = r
		case inQuote && r == quoteChar:
			inQuote = false
			quoteChar = 0
		case !inQuote && r == ' ':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}

	if current.Len() > 0 {
		args = append(args, current.String())
	}

	return args
}

// stripAnsiCodes removes ANSI escape sequences from a string
func stripAnsiCodes(s string) string {
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*[mG]`)
	return ansiRegex.ReplaceAllString(s, "")
}

// parseGrokModels parses the output of 'grok models' command
func parseGrokModels(output string) []string {
	var models []string
	lines := strings.Split(output, "\n")
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if line == "" || !strings.Contains(line, " -- ") {
			i++
			continue
		}
		parts := strings.SplitN(line, " -- ", 2)
		if len(parts) == 2 {
			modelName := stripAnsiCodes(strings.TrimSpace(parts[0]))
			models = append(models, modelName)
			i++
			if i < len(lines) {
				i++
			}
			if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "aliases: ") {
				aliasesStr := strings.TrimSpace(strings.TrimPrefix(lines[i], "aliases: "))
				aliases := strings.Fields(aliasesStr)
				models = append(models, aliases...)
				i++
			}
		} else {
			i++
		}
	}
	return models
}

// readMultiLine reads a potentially multi-line input
func readMultiLine(rl *readline.Instance, initialLine string, mainPrompt string) (string, error) {
	line := initialLine
	continuationPrompt := continuationStyle.Render("  > ")

	// Check for heredoc syntax: <<<DELIMITER
	if strings.HasPrefix(line, "<<<") {
		delimiter := strings.TrimSpace(strings.TrimPrefix(line, "<<<"))
		if delimiter == "" {
			delimiter = "EOF"
		}
		rl.SetPrompt(continuationPrompt)
		var lines []string
		for {
			nextLine, err := rl.Readline()
			if err != nil {
				rl.SetPrompt(mainPrompt)
				return "", err
			}
			if strings.TrimSpace(nextLine) == delimiter {
				break
			}
			lines = append(lines, nextLine)
		}
		rl.SetPrompt(mainPrompt)
		return strings.Join(lines, "\n"), nil
	}

	// Check for backslash continuation or unclosed quotes
	for {
		needsContinuation, quoteChar := checkContinuation(line)
		if !needsContinuation {
			break
		}

		if quoteChar != 0 {
			rl.SetPrompt(continuationStyle.Render(string(quoteChar) + "> "))
		} else {
			rl.SetPrompt(continuationPrompt)
		}

		nextLine, err := rl.Readline()
		if err != nil {
			rl.SetPrompt(mainPrompt)
			return "", err
		}

		if quoteChar == 0 && strings.HasSuffix(strings.TrimRight(line, " \t"), "\\") {
			line = strings.TrimSuffix(strings.TrimRight(line, " \t"), "\\") + "\n" + nextLine
		} else {
			line = line + "\n" + nextLine
		}
	}

	rl.SetPrompt(mainPrompt)
	return line, nil
}

// checkContinuation determines if the line needs continuation
func checkContinuation(line string) (bool, rune) {
	trimmed := strings.TrimRight(line, " \t")
	if strings.HasSuffix(trimmed, "\\") && !strings.HasSuffix(trimmed, "\\\\") {
		return true, 0
	}

	var singleQuotes, doubleQuotes int
	inSingle, inDouble := false, false
	escaped := false

	for _, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && !inSingle {
			escaped = true
			continue
		}
		if r == '\'' && !inDouble {
			inSingle = !inSingle
			singleQuotes++
		}
		if r == '"' && !inSingle {
			inDouble = !inDouble
			doubleQuotes++
		}
	}

	if singleQuotes%2 != 0 {
		return true, '\''
	}
	if doubleQuotes%2 != 0 {
		return true, '"'
	}

	return false, 0
}
