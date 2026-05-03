// federation-command is an interactive CLI shell for orchestrating AI agents.
// It wraps all commands with clauditable for record-keeping and supports:
// - Interactive bubbletea-based input with multi-line support
// - Agent selection and model configuration
// - Mode-based invocation (-p/r/w/x for prompt/read/write/execute)
// - Session recording and management
//
// This is the spiritual successor to sandbox/AI-sandboxing/ambiguous-agent/ambiguous-shell.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// ===== BUBBLETEA MODEL =====

type mlMode int

const (
	mlNone mlMode = iota
	mlHeredoc
	mlContinuation
)

// appModel is the bubbletea model for the federation-command shell
type appModel struct {
	input        textinput.Model
	cwd          string
	oldCwd       string
	currentAgent string
	currentModel string
	lastExitCode int
	sessionID    string
	sessionDir   string
	recordsPath  string

	logFile *os.File
	encoder *json.Encoder

	lastCmdTime time.Time

	// History navigation
	history      []string
	historyIdx   int
	historyStash string // saves current input when browsing history

	// Multi-line input state
	inMultiLine   bool
	mlMode        mlMode
	mlAccumulated string // accumulated text so far
	mlDelim       string // heredoc delimiter
	mlQuote       rune   // unclosed quote char (0 = backslash continuation)

	// Blinker state
	blinker      Blinker
	prevInputLen int // Track previous input length to detect changes

	// Dynapane state
	dynapane   Dynapane
	pendingCmd string // deferred command waiting for roll-up animation to finish

	quitting    bool
	windowWidth int
}

// Bubbletea messages
type cmdDoneMsg struct {
	exitCode int
	line     string
	cmdTime  time.Time
	deltaMs  int64
}

type agentDoneMsg struct {
	exitCode int
	execErr  error
	line     string
	cmdTime  time.Time
	deltaMs  int64
}

type listModelsDoneMsg struct {
	exitCode     int
	currentModel string
}

func newAppModel(recordsPath, sessionID, sessionDir string, logFile *os.File, encoder *json.Encoder) appModel {
	ti := textinput.New()
	ti.Focus()
	ti.PromptStyle = lipgloss.NewStyle() // pass-through: prompt is already ANSI-styled
	ti.TextStyle = lipgloss.NewStyle()

	cwd, _ := os.Getwd()

	currentAgent := os.Getenv(EnvAgentName)
	if currentAgent == "" {
		currentAgent = DefaultAgent
	}
	currentModel := os.Getenv(EnvAgentModel)

	m := appModel{
		input:        ti,
		cwd:          cwd,
		oldCwd:       cwd,
		currentAgent: currentAgent,
		currentModel: currentModel,
		sessionID:    sessionID,
		sessionDir:   sessionDir,
		recordsPath:  recordsPath,
		logFile:      logFile,
		encoder:      encoder,
		blinker:      NewBlinker(),
	}

	m.input.Prompt = buildPrompt(cwd, currentAgent, currentModel, 0)
	m.history = loadHistory(historyFilePath())
	m.historyIdx = len(m.history)

	return m
}

func historyFilePath() string {
	return filepath.Join(os.Getenv("HOME"), ".federation_records")
}

func loadHistory(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func appendHistoryEntry(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

func longestCommonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		for !strings.HasPrefix(s, prefix) {
			if len(prefix) == 0 {
				return ""
			}
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
}

func (m appModel) Init() tea.Cmd {
	info := strings.Join([]string{
		sessionStyle.Render("● session: " + m.sessionDir),
		sessionStyle.Render("  agent: " + m.currentAgent + " | 'set-agent <name>' to change | 'list-agents' for options"),
		sessionStyle.Render("  model: 'set-model <name>' to override | 'list-models' for options"),
		sessionStyle.Render("  type 'exit' to end | 'agent [-p|-r|-w|-x] <prompt>' to invoke AI"),
		sessionStyle.Render("  modes: -p (prompt) | -r (read) | -w (write) | -x (execute)"),
		sessionStyle.Render("  records: 'list-sessions' | add '-provide-records <id>' to agent command"),
		sessionStyle.Render("  multi-line: trailing \\, unclosed quotes, or <<<DELIMITER"),
		"",
	}, "\n")
	return tea.Batch(textinput.Blink, tea.Println(info), m.blinker.tickCmd())
}

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			if m.inMultiLine {
				m.inMultiLine = false
				m.mlAccumulated = ""
				m.mlDelim = ""
				m.mlQuote = 0
				m.mlMode = mlNone
				m.input.SetValue("")
				m.input.Prompt = buildPrompt(m.cwd, m.currentAgent, m.currentModel, m.lastExitCode)
				// Reset blinker to idle (was inactive while typing multi-line)
				m.blinker.SetState(BlinkerIdle)
				m.prevInputLen = 0
				m.input.Focus()
				return m, m.blinker.ResetTick()
			}
			// Exit blinker select mode if active
			if m.blinker.IsSelectMode() {
				m.blinker.SetState(BlinkerIdle)
				m.input.Focus()
				return m, nil // tick chain is already running (select→idle, same gen)
			}
			if m.input.Value() == "" {
				m.quitting = true
				return m, tea.Quit
			}
			m.input.SetValue("")
			m.prevInputLen = 0
			// Reset blinker to idle after clearing input (was inactive while typing)
			m.blinker.SetState(BlinkerIdle)
			return m, m.blinker.ResetTick()

		case tea.KeyCtrlD:
			if m.input.Value() == "" && !m.inMultiLine {
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil

		case tea.KeyEnter:
			return m.handleEnter()

		case tea.KeyUp:
			return m.handleHistoryUp()

		case tea.KeyDown:
			return m.handleHistoryDown()

		case tea.KeyTab:
			return m.handleTab()

		case tea.KeyLeft:
			return m.handleLeft()

		case tea.KeyRight:
			return m.handleRight()

		case tea.KeyBackspace:
			return m.handleBackspace()

		default:
			// Handle other keys in blinker select mode
			if m.blinker.IsSelectMode() {
				// Flash the blinker to alert user they're in select mode
				cmd := m.blinker.StartFlash()
				return m, cmd
			}
		}

	case cmdDoneMsg:
		m.lastExitCode = msg.exitCode
		if newCwd, err := os.Getwd(); err == nil && newCwd != m.cwd {
			m.cwd = newCwd
		}
		setPromptWidth(&m.input, buildPrompt(m.cwd, m.currentAgent, m.currentModel, msg.exitCode), m.windowWidth)
		record := CommandRecord{
			ID:        uuid.New().String()[:8],
			Command:   msg.line,
			Timestamp: msg.cmdTime.Format(time.RFC3339),
			DeltaMs:   msg.deltaMs,
			ExitCode:  msg.exitCode,
		}
		m.encoder.Encode(record)
		// Reset blinker to idle state after command completes
		m.blinker.SetState(BlinkerIdle)
		m.prevInputLen = 0
		return m, m.blinker.ResetTick()

	case agentDoneMsg:
		m.lastExitCode = msg.exitCode
		m.input.Prompt = buildPrompt(m.cwd, m.currentAgent, m.currentModel, msg.exitCode)
		record := CommandRecord{
			ID:        uuid.New().String()[:8],
			Command:   msg.line,
			Timestamp: msg.cmdTime.Format(time.RFC3339),
			DeltaMs:   msg.deltaMs,
			ExitCode:  msg.exitCode,
		}
		m.encoder.Encode(record)
		var postOutput string
		if msg.execErr != nil {
			if _, ok := msg.execErr.(*exec.ExitError); ok {
				postOutput = errorStyle.Render(fmt.Sprintf("agent exited: %d", msg.exitCode))
			} else {
				postOutput = errorStyle.Render(fmt.Sprintf("agent error: %v", msg.execErr))
			}
		} else {
			postOutput = successStyle.Render("agent completed")
		}
		// Reset blinker to idle state after agent completes
		m.blinker.SetState(BlinkerIdle)
		m.prevInputLen = 0
		return m, tea.Batch(tea.Println(postOutput), m.blinker.ResetTick())

	case listModelsDoneMsg:
		m.lastExitCode = msg.exitCode
		setPromptWidth(&m.input, buildPrompt(m.cwd, m.currentAgent, m.currentModel, msg.exitCode), m.windowWidth)
		var suffix string
		if msg.currentModel != "" {
			suffix = "\n" + sessionStyle.Render("current selection: "+msg.currentModel)
		} else {
			suffix = "\n" + sessionStyle.Render("no model explicitly set - using agent's built-in default")
		}
		return m, tea.Println(suffix)

	case tea.WindowSizeMsg:
		m.windowWidth = msg.Width
		setPromptWidth(&m.input, m.input.Prompt, m.windowWidth)
		return m, nil

	case BlinkerTickMsg:
		if msg.gen != m.blinker.gen {
			return m, nil // stale tick from an old chain — discard
		}
		cmd := m.blinker.Tick()
		return m, cmd

	case BlinkerFlashMsg:
		cmd := m.blinker.Flash()
		return m, cmd

	case DynapaneTickMsg:
		cmd := m.dynapane.Tick()
		return m, cmd

	case DynapaneRollUpDoneMsg:
		if m.pendingCmd != "" {
			line := m.pendingCmd
			m.pendingCmd = ""
			return m.executeCommandCore(line)
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)

	// Track input changes to manage blinker state
	currentLen := len(m.input.Value())
	if currentLen != m.prevInputLen {
		if currentLen > 0 {
			// User has typed something - deactivate blinker
			if m.blinker.State() != BlinkerInactive {
				m.blinker.SetState(BlinkerInactive)
			}
		}
		m.prevInputLen = currentLen
	}

	return m, cmd
}

func (m appModel) View() string {
	if m.quitting {
		return ""
	}
	pane := m.dynapane.View(m.windowWidth)
	blinkerSlot := m.blinker.View()
	inputView := m.input.View()
	combined := blinkerSlot + inputView
	if m.windowWidth > 0 {
		return pane + wrapAtWidth(combined, m.windowWidth)
	}
	return pane + combined
}

// wrapAtWidth inserts newlines so the rendered string fits within width visible
// columns. ANSI escape sequences are treated as zero-width.
func wrapAtWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	var b strings.Builder
	col := 0
	i := 0
	for i < len(s) {
		// ANSI escape sequence — zero display width, copy verbatim
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
				j++
			}
			if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		if s[i] == '\n' {
			b.WriteByte('\n')
			col = 0
			i++
			continue
		}
		if s[i] == '\r' {
			b.WriteByte('\r')
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			if col >= width {
				b.WriteByte('\n')
				col = 0
			}
			b.WriteByte(s[i])
			col++
			i++
			continue
		}
		if col >= width {
			b.WriteByte('\n')
			col = 0
		}
		b.WriteRune(r)
		col++
		i += size
	}
	return b.String()
}

func (m appModel) handleEnter() (appModel, tea.Cmd) {
	// If in blinker select mode, flash and do nothing
	if m.blinker.IsSelectMode() {
		cmd := m.blinker.StartFlash()
		return m, cmd
	}

	line := m.input.Value()
	// Capture prompt+line now so it persists in terminal scroll history.
	echoLine := m.input.Prompt + line
	m.input.SetValue("")

	if m.inMultiLine {
		echo := tea.Println(echoLine)
		switch m.mlMode {
		case mlHeredoc:
			if strings.TrimSpace(line) == m.mlDelim {
				accumulated := m.mlAccumulated
				m.inMultiLine = false
				m.mlAccumulated = ""
				m.mlDelim = ""
				m.mlMode = mlNone
				m.input.Prompt = buildPrompt(m.cwd, m.currentAgent, m.currentModel, m.lastExitCode)
				newM, execCmd := m.executeCommand(accumulated)
				return newM, tea.Sequence(echo, execCmd)
			}
			if m.mlAccumulated == "" {
				m.mlAccumulated = line
			} else {
				m.mlAccumulated = m.mlAccumulated + "\n" + line
			}
			return m, echo

		case mlContinuation:
			var newAccumulated string
			if m.mlQuote == 0 {
				// Backslash continuation: strip trailing backslash, join with space
				trimmed := strings.TrimSuffix(strings.TrimRight(m.mlAccumulated, " \t"), "\\")
				newAccumulated = trimmed + " " + line
			} else {
				// Unclosed quote continuation: join with newline
				newAccumulated = m.mlAccumulated + "\n" + line
			}

			needsMore, quoteChar := checkContinuation(newAccumulated)
			if !needsMore {
				m.inMultiLine = false
				m.mlAccumulated = ""
				m.mlQuote = 0
				m.mlMode = mlNone
				m.input.Prompt = buildPrompt(m.cwd, m.currentAgent, m.currentModel, m.lastExitCode)
				newM, execCmd := m.executeCommand(newAccumulated)
				return newM, tea.Sequence(echo, execCmd)
			}
			m.mlAccumulated = newAccumulated
			m.mlQuote = quoteChar
			m.input.Prompt = continuationPromptFor(quoteChar)
			return m, echo
		}
	}

	// Not in multi-line mode
	line = strings.TrimSpace(line)
	if line == "" {
		return m, nil
	}

	echo := tea.Println(echoLine)

	// Heredoc trigger
	if strings.HasPrefix(line, "<<<") {
		m.inMultiLine = true
		m.mlMode = mlHeredoc
		m.mlDelim = strings.TrimSpace(strings.TrimPrefix(line, "<<<"))
		if m.mlDelim == "" {
			m.mlDelim = "EOF"
		}
		m.mlAccumulated = ""
		m.input.Prompt = continuationStyle.Render("  > ")
		return m, echo
	}

	// Continuation check
	needsMore, quoteChar := checkContinuation(line)
	if needsMore {
		m.inMultiLine = true
		m.mlMode = mlContinuation
		m.mlAccumulated = line
		m.mlQuote = quoteChar
		m.input.Prompt = continuationPromptFor(quoteChar)
		return m, echo
	}

	newM, execCmd := m.executeCommand(line)
	return newM, tea.Sequence(echo, execCmd)
}

func continuationPromptFor(quoteChar rune) string {
	if quoteChar != 0 {
		return continuationStyle.Render(string(quoteChar) + "> ")
	}
	return continuationStyle.Render("  > ")
}

func (m appModel) handleHistoryUp() (appModel, tea.Cmd) {
	// If in blinker select mode, flash
	if m.blinker.IsSelectMode() {
		cmd := m.blinker.StartFlash()
		return m, cmd
	}

	if len(m.history) == 0 || m.inMultiLine {
		return m, nil
	}
	if m.historyIdx == len(m.history) {
		m.historyStash = m.input.Value()
	}
	if m.historyIdx > 0 {
		m.historyIdx--
		m.input.SetValue(m.history[m.historyIdx])
		m.input.CursorEnd()
		m.prevInputLen = len(m.input.Value())
		// History item selected - deactivate blinker
		if m.blinker.State() != BlinkerInactive {
			m.blinker.SetState(BlinkerInactive)
		}
	}
	return m, nil
}

func (m appModel) handleHistoryDown() (appModel, tea.Cmd) {
	// If in blinker select mode, flash
	if m.blinker.IsSelectMode() {
		cmd := m.blinker.StartFlash()
		return m, cmd
	}

	if m.inMultiLine {
		return m, nil
	}
	if m.historyIdx < len(m.history) {
		m.historyIdx++
		if m.historyIdx == len(m.history) {
			m.input.SetValue(m.historyStash)
		} else {
			m.input.SetValue(m.history[m.historyIdx])
		}
		m.input.CursorEnd()
		m.prevInputLen = len(m.input.Value())

		// If we're back to empty input (stash was empty), resume blinker
		if m.input.Value() == "" {
			if m.blinker.State() == BlinkerInactive {
				m.blinker.SetState(BlinkerIdle)
				return m, m.blinker.ResetTick()
			}
		} else {
			// Has content - ensure blinker is inactive
			if m.blinker.State() != BlinkerInactive {
				m.blinker.SetState(BlinkerInactive)
			}
		}
	}
	return m, nil
}

func (m appModel) handleTab() (appModel, tea.Cmd) {
	// If in blinker select mode, flash
	if m.blinker.IsSelectMode() {
		cmd := m.blinker.StartFlash()
		return m, cmd
	}

	if m.inMultiLine {
		return m, nil
	}

	line := m.input.Value()
	pos := m.input.Position()
	lineRunes := []rune(line)
	lineUpToCursor := string(lineRunes[:pos])

	lastSpace := strings.LastIndex(lineUpToCursor, " ")
	var prefix string
	if lastSpace == -1 {
		prefix = lineUpToCursor
	} else {
		prefix = lineUpToCursor[lastSpace+1:]
	}

	completions := completeFilepath(prefix, m.cwd)
	if len(completions) == 0 {
		return m, nil
	}

	prefixRunes := []rune(prefix)
	prefixLen := len(prefixRunes)

	if len(completions) == 1 {
		completionRunes := []rune(completions[0])
		newVal := string(lineRunes[:pos-prefixLen]) + completions[0] + string(lineRunes[pos:])
		m.input.SetValue(newVal)
		m.input.SetCursor(pos - prefixLen + len(completionRunes))
		return m, nil
	}

	// Multiple completions: apply common prefix extension, then show list
	common := longestCommonPrefix(completions)
	if len([]rune(common)) > prefixLen {
		commonRunes := []rune(common)
		newVal := string(lineRunes[:pos-prefixLen]) + common + string(lineRunes[pos:])
		m.input.SetValue(newVal)
		m.input.SetCursor(pos - prefixLen + len(commonRunes))
	}

	output := strings.Join(completions, "  ")
	return m, tea.Println(output)
}

func (m appModel) handleLeft() (appModel, tea.Cmd) {
	if m.inMultiLine {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(tea.KeyMsg{Type: tea.KeyLeft})
		return m, cmd
	}

	inputVal := m.input.Value()
	cursorPos := m.input.Position()

	// If cursor is at position 0 and input is empty, handle blinker transitions
	if cursorPos == 0 && inputVal == "" {
		switch m.blinker.State() {
		case BlinkerIdle:
			// Enter blinker select mode — tick chain is already running, no new one needed
			m.blinker.SetState(BlinkerSelect)
			m.input.Blur()
			return m, nil
		case BlinkerInactive:
			// Resume idle blinking from stopped state
			m.blinker.SetState(BlinkerIdle)
			return m, m.blinker.ResetTick()
		case BlinkerSelect:
			// Already in select mode, flash to indicate we can't go further left
			cmd := m.blinker.StartFlash()
			return m, cmd
		}
	}

	// If cursor is at position 0 but there's text, just resume blinker if inactive
	if cursorPos == 0 && inputVal != "" {
		// Don't move cursor further left, but don't change blinker state
		return m, nil
	}

	// Normal left arrow - move cursor
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(tea.KeyMsg{Type: tea.KeyLeft})
	return m, cmd
}

func (m appModel) handleRight() (appModel, tea.Cmd) {
	if m.inMultiLine {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(tea.KeyMsg{Type: tea.KeyRight})
		return m, cmd
	}

	// If in blinker select mode, exit to idle mode and restore cursor
	// Tick chain is still running (select→idle, same gen) so no new tick needed.
	if m.blinker.IsSelectMode() {
		m.blinker.SetState(BlinkerIdle)
		m.input.Focus()
		return m, textinput.Blink
	}

	inputVal := m.input.Value()
	cursorPos := m.input.Position()

	// If there's no text, right arrow makes blinker inactive
	if inputVal == "" {
		if m.blinker.State() == BlinkerIdle {
			m.blinker.SetState(BlinkerInactive)
		}
		return m, nil
	}

	// If cursor is at the end, just deactivate blinker if it was active
	if cursorPos >= len([]rune(inputVal)) {
		if m.blinker.State() != BlinkerInactive {
			m.blinker.SetState(BlinkerInactive)
		}
		return m, nil
	}

	// Normal right arrow - move cursor and ensure blinker is inactive
	if m.blinker.State() != BlinkerInactive {
		m.blinker.SetState(BlinkerInactive)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(tea.KeyMsg{Type: tea.KeyRight})
	return m, cmd
}

func (m appModel) handleBackspace() (appModel, tea.Cmd) {
	if m.inMultiLine {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		return m, cmd
	}

	// If in blinker select mode, flash
	if m.blinker.IsSelectMode() {
		cmd := m.blinker.StartFlash()
		return m, cmd
	}

	inputVal := m.input.Value()
	cursorPos := m.input.Position()

	// If already empty and at position 0, resume blinker idle
	if inputVal == "" && cursorPos == 0 {
		if m.blinker.State() == BlinkerInactive {
			m.blinker.SetState(BlinkerIdle)
			return m, m.blinker.ResetTick()
		}
		return m, nil
	}

	// Process the backspace
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(tea.KeyMsg{Type: tea.KeyBackspace})

	// Check if we just emptied the input
	if m.input.Value() == "" && m.blinker.State() == BlinkerInactive {
		m.blinker.SetState(BlinkerIdle)
		return m, tea.Batch(cmd, m.blinker.ResetTick())
	}

	return m, cmd
}

func extractExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 1
}

func (m appModel) logRecord(line string, cmdTime time.Time, deltaMs int64, exitCode int) {
	record := CommandRecord{
		ID:        uuid.New().String()[:8],
		Command:   line,
		Timestamp: cmdTime.Format(time.RFC3339),
		DeltaMs:   deltaMs,
		ExitCode:  exitCode,
	}
	m.encoder.Encode(record)
}

func (m appModel) executeCommand(line string) (appModel, tea.Cmd) {
	// Add to history
	m.history = append(m.history, line)
	m.historyIdx = len(m.history)
	m.historyStash = ""
	appendHistoryEntry(historyFilePath(), line)

	// If dynapane is open, roll it up first then execute.
	if m.dynapane.IsActive() && line != "dynapane demo" {
		m.pendingCmd = line
		return m, m.dynapane.StartRollUp()
	}

	return m.executeCommandCore(line)
}

func (m appModel) executeCommandCore(line string) (appModel, tea.Cmd) {
	// Dismiss any active dynapane (re-activated below if this IS dynapane demo)
	m.dynapane.Deactivate()

	cmdTime := time.Now()
	var deltaMs int64
	if !m.lastCmdTime.IsZero() {
		deltaMs = cmdTime.Sub(m.lastCmdTime).Milliseconds()
	}
	m.lastCmdTime = cmdTime

	// dynapane demo
	if line == "dynapane demo" {
		m.logRecord(line, cmdTime, deltaMs, 0)
		cmd := m.dynapane.Activate()
		return m, cmd
	}

	// exit
	if line == "exit" {
		m.quitting = true
		m.logRecord(line, cmdTime, deltaMs, 0)
		return m, tea.Batch(
			tea.Println(exitStyle.Render("session ended.")),
			tea.Quit,
		)
	}

	// cd
	if line == "cd" || strings.HasPrefix(line, "cd ") {
		target := strings.TrimSpace(strings.TrimPrefix(line, "cd"))
		newDir, cdOutput, err := handleCd(target, m.cwd, m.oldCwd)
		var exitCode int
		var output string
		if err != nil {
			exitCode = 1
			output = errorStyle.Render("cd: " + err.Error())
		} else {
			m.oldCwd = m.cwd
			m.cwd = newDir
			if cdOutput != "" {
				output = cdOutput
			}
		}
		m.lastExitCode = exitCode
		m.input.Prompt = buildPrompt(m.cwd, m.currentAgent, m.currentModel, exitCode)
		m.logRecord(line, cmdTime, deltaMs, exitCode)
		if output != "" {
			return m, tea.Println(output)
		}
		return m, nil
	}

	// export
	if line == "export" || strings.HasPrefix(line, "export ") {
		arg := strings.TrimSpace(strings.TrimPrefix(line, "export"))
		output, err := handleExport(arg)
		var exitCode int
		var printOutput string
		if err != nil {
			exitCode = 1
			printOutput = errorStyle.Render("export: " + err.Error())
		} else if output != "" {
			printOutput = strings.TrimRight(output, "\n")
		}
		m.logRecord(line, cmdTime, deltaMs, exitCode)
		if printOutput != "" {
			return m, tea.Println(printOutput)
		}
		return m, nil
	}

	// set-agent <name>
	if strings.HasPrefix(line, "set-agent ") {
		newAgent := strings.TrimSpace(strings.TrimPrefix(line, "set-agent "))
		var output string
		var exitCode int
		if isValidAgent(newAgent) {
			m.currentAgent = newAgent
			m.currentModel = ""
			m.input.Prompt = buildPrompt(m.cwd, m.currentAgent, m.currentModel, m.lastExitCode)
			output = successStyle.Render("agent set to: " + m.currentAgent)
		} else {
			exitCode = 1
			output = errorStyle.Render("unknown agent: "+newAgent) + "\n" +
				sessionStyle.Render("available: "+strings.Join(availableAgents, ", "))
		}
		m.lastExitCode = exitCode
		m.logRecord(line, cmdTime, deltaMs, exitCode)
		return m, tea.Println(output)
	}

	if line == "set-agent" {
		m.logRecord(line, cmdTime, deltaMs, 1)
		output := errorStyle.Render("usage: set-agent <name>") + "\n" +
			sessionStyle.Render("available: "+strings.Join(availableAgents, ", "))
		return m, tea.Println(output)
	}

	if line == "list-agents" {
		m.logRecord(line, cmdTime, deltaMs, 0)
		return m, tea.Println(renderAgents(m.currentAgent))
	}

	// set-model <name>
	if strings.HasPrefix(line, "set-model ") {
		newModel := strings.TrimSpace(strings.TrimPrefix(line, "set-model "))
		var output string
		var exitCode int
		if err := setModel(m.currentAgent, newModel); err != nil {
			exitCode = 1
			output = errorStyle.Render(err.Error())
		} else {
			m.currentModel = newModel
			m.input.Prompt = buildPrompt(m.cwd, m.currentAgent, m.currentModel, m.lastExitCode)
			output = successStyle.Render("model set to: " + m.currentModel)
		}
		m.lastExitCode = exitCode
		m.logRecord(line, cmdTime, deltaMs, exitCode)
		return m, tea.Println(output)
	}

	if line == "set-model" {
		m.logRecord(line, cmdTime, deltaMs, 1)
		output := errorStyle.Render("usage: set-model <name>") + "\n" +
			sessionStyle.Render("use 'list-models' to see available models for current agent")
		return m, tea.Println(output)
	}

	if line == "clear-model" {
		m.currentModel = ""
		m.input.Prompt = buildPrompt(m.cwd, m.currentAgent, m.currentModel, m.lastExitCode)
		m.logRecord(line, cmdTime, deltaMs, 0)
		return m, tea.Println(successStyle.Render("model cleared - using agent's default"))
	}

	if line == "list-models" {
		cmd, fallbackText := buildListModelsCmd(m.currentAgent, m.currentModel, m.sessionDir)
		if cmd == nil {
			m.logRecord(line, cmdTime, deltaMs, 0)
			return m, tea.Println(fallbackText)
		}
		currentModel := m.currentModel
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
			return listModelsDoneMsg{exitCode: extractExitCode(err), currentModel: currentModel}
		})
	}

	if line == "list-sessions" {
		m.logRecord(line, cmdTime, deltaMs, 0)
		return m, tea.Println(renderSessions(filepath.Dir(m.sessionDir), filepath.Base(m.sessionDir)))
	}

	// set-session <id>
	if strings.HasPrefix(line, "set-session ") {
		newSessionID := strings.TrimSpace(strings.TrimPrefix(line, "set-session "))
		newSessionDir := filepath.Join(m.recordsPath, newSessionID)
		if err := os.MkdirAll(newSessionDir, 0755); err != nil {
			m.logRecord(line, cmdTime, deltaMs, 1)
			return m, tea.Println(errorStyle.Render("set-session: " + err.Error()))
		}
		newLogFile, err := os.OpenFile(filepath.Join(newSessionDir, "session.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			m.logRecord(line, cmdTime, deltaMs, 1)
			return m, tea.Println(errorStyle.Render("set-session: " + err.Error()))
		}
		m.logFile.Close()
		m.logFile = newLogFile
		m.encoder = json.NewEncoder(newLogFile)
		m.sessionID = newSessionID
		m.sessionDir = newSessionDir
		os.Setenv(EnvAgentSession, newSessionID)
		m.logRecord(line, cmdTime, deltaMs, 0)
		return m, tea.Println(successStyle.Render("session set to: " + newSessionDir))
	}

	if line == "set-session" {
		m.logRecord(line, cmdTime, deltaMs, 1)
		return m, tea.Println(errorStyle.Render("usage: set-session <id>"))
	}

	if line == "clear-session" {
		now := time.Now()
		newSessionID := fmt.Sprintf("%s_%d", now.Format("2006-01-02_15-04-05"), now.Unix())
		newSessionDir := filepath.Join(m.recordsPath, newSessionID)
		if err := os.MkdirAll(newSessionDir, 0755); err != nil {
			m.logRecord(line, cmdTime, deltaMs, 1)
			return m, tea.Println(errorStyle.Render("clear-session: " + err.Error()))
		}
		newLogFile, err := os.OpenFile(filepath.Join(newSessionDir, "session.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			m.logRecord(line, cmdTime, deltaMs, 1)
			return m, tea.Println(errorStyle.Render("clear-session: " + err.Error()))
		}
		m.logFile.Close()
		m.logFile = newLogFile
		m.encoder = json.NewEncoder(newLogFile)
		m.sessionID = newSessionID
		m.sessionDir = newSessionDir
		os.Setenv(EnvAgentSession, newSessionID)
		m.logRecord(line, cmdTime, deltaMs, 0)
		return m, tea.Println(successStyle.Render("session reset to: " + newSessionDir))
	}

	// agent <args>
	if strings.HasPrefix(line, "agent ") {
		prompt := strings.TrimPrefix(line, "agent ")
		agentCmd, errOutput := buildAgentCmd(prompt, m.currentAgent, m.currentModel, m.sessionDir)
		if agentCmd == nil {
			m.logRecord(line, cmdTime, deltaMs, 1)
			return m, tea.Println(errOutput)
		}
		return m, tea.ExecProcess(agentCmd, func(err error) tea.Msg {
			return agentDoneMsg{
				exitCode: extractExitCode(err),
				execErr:  err,
				line:     line,
				cmdTime:  cmdTime,
				deltaMs:  deltaMs,
			}
		})
	}

	if line == "agent" {
		m.logRecord(line, cmdTime, deltaMs, 1)
		return m, tea.Println(errorStyle.Render("usage: agent [-p|-r|-w|-x] [-provide-records <id>...] <prompt>"))
	}

	// Regular command - wrap with clauditable
	runCmd := buildRunCmd(line, m.sessionDir)
	return m, tea.ExecProcess(runCmd, func(err error) tea.Msg {
		return cmdDoneMsg{
			exitCode: extractExitCode(err),
			line:     line,
			cmdTime:  cmdTime,
			deltaMs:  deltaMs,
		}
	})
}

// buildRunCmd builds an exec.Cmd for a shell command (wrapped with clauditable if available).
// Stdin/Stdout/Stderr are NOT set; tea.ExecProcess handles those.
func buildRunCmd(cmdLine, sessionDir string) *exec.Cmd {
	if os.Getenv(EnvIsClauditable) == "true" {
		cmd := exec.Command("bash", "-c", cmdLine)
		cmd.Env = os.Environ()
		return cmd
	}

	clauditablePath, err := findBinary("clauditable")
	if err != nil {
		cmd := exec.Command("bash", "-c", cmdLine)
		cmd.Env = os.Environ()
		return cmd
	}

	cmd := exec.Command(clauditablePath, "bash", "-c", cmdLine)
	env := os.Environ()
	env = append(env,
		EnvAgentRecordsPath+"="+filepath.Dir(sessionDir),
		EnvAgentSession+"="+filepath.Base(sessionDir),
		"UFA_AGENT=none",
		EnvIsClauditable+"=true",
	)
	cmd.Env = env
	return cmd
}

// buildAgentCmd builds an exec.Cmd for agent invocation, parsing mode flags and -provide-records.
// Returns (nil, errMsg) if the invocation is invalid.
// Stdin/Stdout/Stderr are NOT set; tea.ExecProcess handles those.
func buildAgentCmd(input, agent, model, sessionDir string) (*exec.Cmd, string) {
	mode := ModeRead
	args := parseArgs(input)
	var promptParts []string
	var provideRecordsSessions []string

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
		case "-provide-records":
			if i+1 < len(args) {
				i++
				provideRecordsSessions = append(provideRecordsSessions, args[i])
			}
		default:
			promptParts = append(promptParts, args[i])
		}
	}

	prompt := strings.Join(promptParts, " ")
	if prompt == "" {
		return nil, errorStyle.Render("no prompt provided")
	}

	ambiguousAgentPath, err := findBinary("ambiguous-agent")
	if err != nil {
		return nil, errorStyle.Render("Error: ambiguous-agent not found")
	}

	var agentArgs []string
	agentArgs = append(agentArgs, "-"+mode)
	agentArgs = append(agentArgs, "-a", agent)
	if model != "" {
		agentArgs = append(agentArgs, "-m", model)
	}
	for _, id := range provideRecordsSessions {
		agentArgs = append(agentArgs, "-provide-records", id)
	}
	agentArgs = append(agentArgs, prompt)

	if os.Getenv(EnvIsClauditable) == "true" {
		cmd := exec.Command(ambiguousAgentPath, agentArgs...)
		cmd.Env = os.Environ()
		return cmd, ""
	}

	clauditablePath, err := findBinary("clauditable")
	if err != nil {
		cmd := exec.Command(ambiguousAgentPath, agentArgs...)
		cmd.Env = os.Environ()
		return cmd, ""
	}

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
	return cmd, ""
}

// buildListModelsCmd builds an exec.Cmd for listing models, or returns fallback text if unavailable.
// Stdin/Stdout/Stderr are NOT set; tea.ExecProcess handles those.
func buildListModelsCmd(agent, currentModel, sessionDir string) (*exec.Cmd, string) {
	if os.Getenv(EnvIsClauditable) == "true" {
		ambiguousAgentPath, err := findBinary("ambiguous-agent")
		if err != nil {
			return nil, renderModelsFallback(agent, currentModel)
		}
		cmd := exec.Command(ambiguousAgentPath, "--list-models", "-a", agent)
		cmd.Env = os.Environ()
		return cmd, ""
	}

	ambiguousAgentPath, err := findBinary("ambiguous-agent")
	if err != nil {
		return nil, renderModelsFallback(agent, currentModel)
	}

	clauditablePath, err := findBinary("clauditable")
	if err != nil {
		cmd := exec.Command(ambiguousAgentPath, "--list-models", "-a", agent)
		cmd.Env = os.Environ()
		return cmd, ""
	}

	cmd := exec.Command(clauditablePath, ambiguousAgentPath, "--list-models", "-a", agent)
	env := os.Environ()
	env = append(env,
		EnvAgentRecordsPath+"="+filepath.Dir(sessionDir),
		EnvAgentSession+"="+filepath.Base(sessionDir),
		"UFA_AGENT=none",
		EnvIsClauditable+"=true",
	)
	cmd.Env = env
	return cmd, ""
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
	if !ok || cfg.ModelFlag == "" {
		return fmt.Errorf("agent '%s' does not support model selection", agent)
	}

	var models []string

	// Query models dynamically for agents that support it
	switch agent {
	case "opencode":
		cmd := exec.Command("opencode", "models")
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to query models from opencode: %v - ensure it's installed", err)
		}
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				models = append(models, line)
			}
		}
	case "grok":
		cmd := exec.Command("grok", "models")
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to query models from grok: %v - ensure it's installed", err)
		}
		models = parseGrokModels(string(output))
	default:
		// For agents without dynamic model listing, accept the model without validation
		return nil
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

// renderAgents returns a styled string listing available agents
func renderAgents(currentAgent string) string {
	var b strings.Builder
	b.WriteString(sessionStyle.Render("available agents:"))
	for _, a := range availableAgents {
		b.WriteString("\n")
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
			b.WriteString(agentNameStyle.Render(fmt.Sprintf("  -> %s (selected)%s", a, modelSupport)))
		} else {
			b.WriteString(agentNameStyle.Render(fmt.Sprintf("    %s%s", a, modelSupport)))
		}
	}
	return b.String()
}

// renderModelsFallback returns a styled error string when ambiguous-agent is unavailable
func renderModelsFallback(agent string, currentModel string) string {
	var b strings.Builder
	cfg, ok := agentModelConfigs[agent]
	if !ok || cfg.ModelFlag == "" {
		b.WriteString(sessionStyle.Render(fmt.Sprintf("agent '%s' does not support model selection", agent)))
		b.WriteString("\n")
		b.WriteString(sessionStyle.Render("the agent uses its built-in default model"))
	} else {
		b.WriteString(errorStyle.Render("failed to query available models"))
		b.WriteString("\n")
		b.WriteString(sessionStyle.Render(fmt.Sprintf("ensure '%s' is installed and available on PATH", agent)))
		b.WriteString("\n")
		b.WriteString(sessionStyle.Render("consult the agent's documentation for available models"))
	}
	if currentModel != "" {
		b.WriteString("\n\n")
		b.WriteString(sessionStyle.Render("current selection: " + currentModel))
	}
	return b.String()
}

// handleCd processes a cd command. Returns (newCwd, printOutput, error).
// printOutput is non-empty only for "cd -" which prints the target directory.
func handleCd(target string, cwd string, oldCwd string) (string, string, error) {
	home := os.Getenv("HOME")

	var targetDir string
	var printOutput string
	switch {
	case target == "" || target == "~":
		targetDir = home
	case target == "-":
		if oldCwd == cwd {
			return "", "", fmt.Errorf("OLDPWD not set")
		}
		targetDir = oldCwd
		printOutput = oldCwd
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
		return "", "", err
	}

	newCwd, err := os.Getwd()
	if err != nil {
		return targetDir, printOutput, nil
	}
	return newCwd, printOutput, nil
}

// handleExport processes an export command. Returns (output, error).
// output is non-empty when called with no arguments (lists environment).
func handleExport(arg string) (string, error) {
	if arg == "" {
		var b strings.Builder
		for _, env := range os.Environ() {
			fmt.Fprintf(&b, "declare -x %s\n", env)
		}
		return b.String(), nil
	}

	assignments := parseExportArgs(arg)
	for _, assignment := range assignments {
		if err := processExportAssignment(assignment); err != nil {
			return "", err
		}
	}
	return "", nil
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

// setPromptWidth sets the prompt on input. Width is intentionally left at 0
// (unlimited) so View() can perform explicit wrapping via wrapAtWidth.
func setPromptWidth(input *textinput.Model, prompt string, windowWidth int) {
	input.Prompt = prompt
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

// renderSessions returns a styled string listing available sessions
func renderSessions(recordsPath string, currentSession string) string {
	var b strings.Builder
	entries, err := os.ReadDir(recordsPath)
	if err != nil {
		return errorStyle.Render(fmt.Sprintf("Error reading records directory: %v", err))
	}

	b.WriteString(sessionStyle.Render(fmt.Sprintf("available sessions in %s:", recordsPath)))
	b.WriteString("\n\n")

	var sessions []string
	for _, entry := range entries {
		if entry.IsDir() {
			sessions = append(sessions, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(sessions)))

	for _, session := range sessions {
		prefix := "    "
		suffix := ""
		if session == currentSession {
			prefix = "  -> "
			suffix = " (current)"
		}
		sessionPath := filepath.Join(recordsPath, session)
		files, _ := os.ReadDir(sessionPath)
		fileCount := len(files)
		b.WriteString(sessionStyle.Render(fmt.Sprintf("%s%s%s (%d files)", prefix, session, suffix, fileCount)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(sessionStyle.Render("use 'agent -provide-records <id> [-p|-r|-w|-x] <prompt>' to include session context"))
	return b.String()
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

	// Create session directory
	now := time.Now()
	sessionID := os.Getenv(EnvAgentSession)
	if sessionID == "" {
		sessionID = fmt.Sprintf("%s_%d", now.Format("2006-01-02_15-04-05"), now.Unix())
	}
	sessionDir := filepath.Join(recordsPath, sessionID)

	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error creating session directory: %v\n", err)
		os.Exit(1)
	}

	os.Setenv(EnvAgentRecordsPath, recordsPath)
	os.Setenv(EnvAgentSession, sessionID)

	logPath := filepath.Join(sessionDir, "session.jsonl")
	logFile, err := os.Create(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating session log: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	encoder := json.NewEncoder(logFile)

	model := newAppModel(recordsPath, sessionID, sessionDir, logFile, encoder)

	p := tea.NewProgram(model, tea.WithInput(os.Stdin))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
