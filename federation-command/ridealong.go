package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Ridealong represents an active ridealong session parsed from a markdown file.
type Ridealong struct {
	filePath     string   // Path to the ridealong file
	commands     []string // Extracted bash commands from ridealong blocks
	currentIndex int      // Index of the current command to execute
	prevExitCode int      // Exit code of the last executed command (0 = success, -1 = none yet)
	active       bool     // Whether ridealong mode is active
	menuIndex    int      // 0 = execute command, 1 = exit
}

// ridealongBlockRegex matches ```ridealong code blocks in markdown
var ridealongBlockRegex = regexp.MustCompile("(?ms)```ridealong\\s*\\n(.+?)```")

// NewRidealong creates a new ridealong session from a file.
// Returns nil and an error message if the file doesn't exist or has no ridealong blocks.
func NewRidealong(filePath string) (*Ridealong, string) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, "ridealong: cannot read file: " + err.Error()
	}

	matches := ridealongBlockRegex.FindAllStringSubmatch(string(content), -1)
	if len(matches) == 0 {
		return nil, "ridealong: no ```ridealong blocks found in file"
	}

	var commands []string
	for _, match := range matches {
		blockContent := strings.TrimSpace(match[1])
		// Split block into individual commands (one per line, skip empty lines)
		lines := strings.Split(blockContent, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				commands = append(commands, line)
			}
		}
	}

	if len(commands) == 0 {
		return nil, "ridealong: no commands found in ridealong blocks"
	}

	return &Ridealong{
		filePath:     filePath,
		commands:     commands,
		currentIndex: 0,
		prevExitCode: -1, // -1 indicates no command has been executed yet
		active:       true,
		menuIndex:    0,
	}, ""
}

// IsActive returns whether ridealong mode is currently active.
func (r *Ridealong) IsActive() bool {
	return r != nil && r.active
}

// Deactivate exits ridealong mode.
func (r *Ridealong) Deactivate() {
	if r != nil {
		r.active = false
	}
}

// CurrentCommand returns the current command to be executed, or empty string if at end.
func (r *Ridealong) CurrentCommand() string {
	if r == nil || r.currentIndex >= len(r.commands) {
		return ""
	}
	return r.commands[r.currentIndex]
}

// PreviousCommand returns the previous command and its exit code, or empty string if none.
func (r *Ridealong) PreviousCommand() (string, int) {
	if r == nil || r.currentIndex == 0 {
		return "", -1
	}
	return r.commands[r.currentIndex-1], r.prevExitCode
}

// NextCommand returns the next command after current, or "<end>" if at the last command.
func (r *Ridealong) NextCommand() string {
	if r == nil || r.currentIndex+1 >= len(r.commands) {
		return "<end>"
	}
	return r.commands[r.currentIndex+1]
}

// AdvanceCommand moves to the next command after recording the exit code.
// Returns true if there are more commands, false if we've reached the end.
func (r *Ridealong) AdvanceCommand(exitCode int) bool {
	if r == nil {
		return false
	}
	r.prevExitCode = exitCode
	r.currentIndex++
	return r.currentIndex < len(r.commands)
}

// MenuUp moves menu selection up (execute command -> exit wraps to end)
func (r *Ridealong) MenuUp() {
	if r == nil {
		return
	}
	if r.menuIndex > 0 {
		r.menuIndex--
	}
}

// MenuDown moves menu selection down (exit -> execute command wraps to start)
func (r *Ridealong) MenuDown() {
	if r == nil {
		return
	}
	if r.menuIndex < 1 {
		r.menuIndex++
	}
}

// MenuSelection returns the current menu selection: 0 = execute, 1 = exit
func (r *Ridealong) MenuSelection() int {
	if r == nil {
		return 0
	}
	return r.menuIndex
}

// FileName returns just the filename portion of the ridealong file path
func (r *Ridealong) FileName() string {
	if r == nil {
		return ""
	}
	parts := strings.Split(r.filePath, "/")
	return parts[len(parts)-1]
}

// ===== RIDEALONG DYNAPANE =====

// RidealongDynapane renders the ridealong-specific dynapane above the prompt.
type RidealongDynapane struct {
	active    bool
	ridealong *Ridealong
}

// RidealongDynapaneTickMsg is sent on each tick interval for the ridealong dynapane
type RidealongDynapaneTickMsg struct{}

const ridealongDynapaneTickInterval = 80 * time.Millisecond

func ridealongDynapaneTickCmd() tea.Cmd {
	return tea.Tick(ridealongDynapaneTickInterval, func(t time.Time) tea.Msg {
		return RidealongDynapaneTickMsg{}
	})
}

// Activate activates the ridealong dynapane with the given ridealong session.
func (rd *RidealongDynapane) Activate(r *Ridealong) tea.Cmd {
	rd.active = true
	rd.ridealong = r
	return ridealongDynapaneTickCmd()
}

// Deactivate hides the ridealong dynapane.
func (rd *RidealongDynapane) Deactivate() {
	rd.active = false
	rd.ridealong = nil
}

// IsActive returns whether the ridealong dynapane is visible.
func (rd *RidealongDynapane) IsActive() bool {
	return rd.active
}

// Tick handles dynapane ticks (for potential animations).
func (rd *RidealongDynapane) Tick() tea.Cmd {
	if !rd.active {
		return nil
	}
	return ridealongDynapaneTickCmd()
}

// Styles for ridealong dynapane
var (
	ridealongBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("99"))

	ridealongTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("141")).
				Bold(true)

	ridealongFileStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243")).
				Italic(true)

	ridealongDividerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("99"))

	ridealongMenuSelectedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("117")).
					Bold(true)

	ridealongMenuStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243"))

	ridealongPrevCmdStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243"))

	ridealongCurrentCmdStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("220")).
					Bold(true)

	ridealongNextCmdStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243"))

	ridealongErrorCodeStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("196")).
				Bold(true)
)

// View renders the ridealong dynapane.
func (rd *RidealongDynapane) View(windowWidth int) string {
	if !rd.active || rd.ridealong == nil {
		return ""
	}

	if windowWidth <= 0 {
		windowWidth = 100
	}

	// Inner width = total - 2 (border sides) - 2 (padding)
	innerWidth := windowWidth - 4
	if innerWidth < 20 {
		innerWidth = 20
	}

	r := rd.ridealong

	// Title row: ◈ ridealong  ---    <filename>
	title := ridealongTitleStyle.Render("◈ ridealong")
	separator := ridealongDividerStyle.Render("  ---    ")
	fileName := ridealongFileStyle.Render(r.FileName())
	titleRow := title + separator + fileName
	// Pad to inner width
	titleRowWidth := lipgloss.Width(titleRow)
	if titleRowWidth < innerWidth {
		titleRow += strings.Repeat(" ", innerWidth-titleRowWidth)
	}

	// Divider
	divider := ridealongDividerStyle.Render(strings.Repeat("─", innerWidth))

	// Menu items
	var executeItem, exitItem string
	if r.menuIndex == 0 {
		executeItem = ridealongMenuSelectedStyle.Render("◈ execute command")
		exitItem = ridealongMenuStyle.Render("  exit")
	} else {
		executeItem = ridealongMenuStyle.Render("  execute command")
		exitItem = ridealongMenuSelectedStyle.Render("◈ exit")
	}

	// Command display section
	prevCmd, prevExitCode := r.PreviousCommand()
	currentCmd := r.CurrentCommand()
	nextCmd := r.NextCommand()

	var prevCmdLine string
	if prevCmd == "" {
		prevCmdLine = ridealongPrevCmdStyle.Render("  (no previous command)")
	} else {
		prefix := "  "
		if prevExitCode != 0 {
			prefix = ridealongErrorCodeStyle.Render("["+fmt.Sprintf("%d", prevExitCode)+"] ")
		}
		prevCmdLine = prefix + ridealongPrevCmdStyle.Render(truncateCommand(prevCmd, innerWidth-10))
	}

	currentCmdLine := ridealongCurrentCmdStyle.Render("✦ " + truncateCommand(currentCmd, innerWidth-4))

	var nextCmdLine string
	if nextCmd == "<end>" {
		nextCmdLine = ridealongNextCmdStyle.Render("  <end>")
	} else {
		nextCmdLine = ridealongNextCmdStyle.Render("  " + truncateCommand(nextCmd, innerWidth-4))
	}

	// Build all content lines
	allLines := []string{
		titleRow,
		divider,
		executeItem,
		exitItem,
		divider,
		prevCmdLine,
		currentCmdLine,
		nextCmdLine,
	}

	content := strings.Join(allLines, "\n")

	pane := ridealongBorderStyle.
		Width(innerWidth).
		Render(content)

	return "\n" + pane + "\n"
}

// truncateCommand shortens a command string if it exceeds maxLen.
func truncateCommand(cmd string, maxLen int) string {
	if len(cmd) <= maxLen {
		return cmd
	}
	if maxLen <= 3 {
		return cmd[:maxLen]
	}
	return cmd[:maxLen-3] + "..."
}
