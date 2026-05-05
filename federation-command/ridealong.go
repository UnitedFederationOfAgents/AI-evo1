package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ridealongStepKind distinguishes shell commands from depth-first sub-file dives.
type ridealongStepKind int

const (
	stepCommand ridealongStepKind = iota
	stepDive
)

// ridealongStep is one item in a ridealong's ordered execution sequence.
type ridealongStep struct {
	kind  ridealongStepKind
	value string // shell command text, or absolute path to sub-file
}

// Ridealong represents an active ridealong session parsed from a markdown file.
type Ridealong struct {
	filePath     string
	steps        []ridealongStep
	currentIndex int
	prevExitCode int // -1 = no command executed yet
	active       bool
	menuIndex    int        // 0 = execute command, 1 = exit
	parent       *Ridealong // non-nil when this is a nested ridealong
}

// ridealongBlockOpenRegex matches the opening fence of a ```ridealong block.
var ridealongBlockOpenRegex = regexp.MustCompile("^```ridealong\\s*$")

// ridealongLinkRegex matches a markdown inline link [text](path).
var ridealongLinkRegex = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

// ridealongContinuesMarker is the annotation signalling a depth-first dive.
const ridealongContinuesMarker = "<!-- ride along continues -->"

// parseContinuesLine inspects a single source line.  If it contains exactly one
// markdown link and the ride-along-continues marker, it returns the resolved
// absolute path of the linked file and true; otherwise it returns "", false.
func parseContinuesLine(line, baseFilePath string) (string, bool) {
	if !strings.Contains(line, ridealongContinuesMarker) {
		return "", false
	}
	matches := ridealongLinkRegex.FindAllStringSubmatch(line, -1)
	if len(matches) != 1 {
		return "", false
	}
	linkPath := matches[0][2]
	if !filepath.IsAbs(linkPath) {
		linkPath = filepath.Join(filepath.Dir(baseFilePath), linkPath)
	}
	return linkPath, true
}

// extractContinuesLinks returns the absolute paths of every sub-file referenced
// by <!-- ride along continues --> annotations in filePath.
func extractContinuesLinks(filePath string) []string {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	var links []string
	for _, line := range strings.Split(string(content), "\n") {
		if p, ok := parseContinuesLine(line, filePath); ok {
			links = append(links, p)
		}
	}
	return links
}

// hasCycle returns true when the ridealong dependency graph rooted at startPath
// contains a cycle (uses standard DFS white/gray/black colouring).
func hasCycle(startPath string) bool {
	type visitState int
	const (
		white visitState = iota
		gray
		black
	)
	state := map[string]visitState{}

	var dfs func(path string) bool
	dfs = func(path string) bool {
		switch state[path] {
		case gray:
			return true
		case black:
			return false
		}
		state[path] = gray
		for _, link := range extractContinuesLinks(path) {
			if dfs(link) {
				return true
			}
		}
		state[path] = black
		return false
	}
	return dfs(startPath)
}

// parseRidealongSteps reads a markdown file and produces an ordered list of
// steps: shell commands (from ```ridealong blocks) interleaved with depth-first
// dive annotations (from <!-- ride along continues --> lines), preserving
// document order throughout.
func parseRidealongSteps(filePath string) ([]ridealongStep, string) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, "ridealong: cannot read file: " + err.Error()
	}

	var steps []ridealongStep
	inBlock := false

	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if ridealongBlockOpenRegex.MatchString(trimmed) {
				inBlock = true
				continue
			}
			if p, ok := parseContinuesLine(line, filePath); ok {
				steps = append(steps, ridealongStep{kind: stepDive, value: p})
			}
		} else {
			if trimmed == "```" {
				inBlock = false
				continue
			}
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				steps = append(steps, ridealongStep{kind: stepCommand, value: trimmed})
			}
		}
	}
	return steps, ""
}

// NewRidealong creates a new top-level ridealong session from a markdown file.
// Returns nil plus an error message when the file cannot be read, contains no
// actionable steps, or has a cyclic dependency.
func NewRidealong(filePath string) (*Ridealong, string) {
	if hasCycle(filePath) {
		return nil, "ridealong: cyclic dependency detected in " + filepath.Base(filePath)
	}
	steps, errMsg := parseRidealongSteps(filePath)
	if errMsg != "" {
		return nil, errMsg
	}
	if len(steps) == 0 {
		return nil, "ridealong: no commands or links found in file"
	}
	return &Ridealong{
		filePath:     filePath,
		steps:        steps,
		currentIndex: 0,
		prevExitCode: -1,
		active:       true,
		menuIndex:    0,
	}, ""
}

// stepDisplay returns the human-readable label used throughout the UI for a step.
func (r *Ridealong) stepDisplay(s ridealongStep) string {
	if s.kind == stepCommand {
		return s.value
	}
	return "→ " + filepath.Base(s.value)
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

// IsDiveStep returns true when the current step is a depth-first sub-file dive.
func (r *Ridealong) IsDiveStep() bool {
	if r == nil || r.currentIndex >= len(r.steps) {
		return false
	}
	return r.steps[r.currentIndex].kind == stepDive
}

// CurrentDivePath returns the resolved file path for the current dive step.
func (r *Ridealong) CurrentDivePath() string {
	if r == nil || r.currentIndex >= len(r.steps) || r.steps[r.currentIndex].kind != stepDive {
		return ""
	}
	return r.steps[r.currentIndex].value
}

// CurrentCommand returns the display label of the current step.
func (r *Ridealong) CurrentCommand() string {
	if r == nil || r.currentIndex >= len(r.steps) {
		return ""
	}
	return r.stepDisplay(r.steps[r.currentIndex])
}

// PreviousCommand returns the display label and exit code of the most recently
// completed step, or an empty string and -1 if no step has been completed yet.
func (r *Ridealong) PreviousCommand() (string, int) {
	if r == nil || r.currentIndex == 0 {
		return "", -1
	}
	return r.stepDisplay(r.steps[r.currentIndex-1]), r.prevExitCode
}

// NextCommand returns the display label of the step after the current one,
// or "<end>" when the current step is the last.
func (r *Ridealong) NextCommand() string {
	if r == nil || r.currentIndex+1 >= len(r.steps) {
		return "<end>"
	}
	return r.stepDisplay(r.steps[r.currentIndex+1])
}

// AdvanceCommand records exitCode and moves to the next step.
// Returns true when more steps remain, false when all steps are exhausted.
func (r *Ridealong) AdvanceCommand(exitCode int) bool {
	if r == nil {
		return false
	}
	r.prevExitCode = exitCode
	r.currentIndex++
	return r.currentIndex < len(r.steps)
}

// MenuUp moves menu selection toward "execute command".
func (r *Ridealong) MenuUp() {
	if r != nil && r.menuIndex > 0 {
		r.menuIndex--
	}
}

// MenuDown moves menu selection toward "exit".
func (r *Ridealong) MenuDown() {
	if r != nil && r.menuIndex < 1 {
		r.menuIndex++
	}
}

// MenuSelection returns the current menu index: 0 = execute, 1 = exit.
func (r *Ridealong) MenuSelection() int {
	if r == nil {
		return 0
	}
	return r.menuIndex
}

// FileName returns the base filename of the ridealong file.
func (r *Ridealong) FileName() string {
	if r == nil {
		return ""
	}
	return filepath.Base(r.filePath)
}

// DisplayTitle returns a breadcrumb title reflecting nesting depth.
// e.g. "parent.md > child.md" when inside a sub-file.
func (r *Ridealong) DisplayTitle() string {
	if r == nil {
		return ""
	}
	if r.parent == nil {
		return r.FileName()
	}
	return r.parent.DisplayTitle() + " > " + r.FileName()
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

	// Title row: ◈ ridealong  ---    <breadcrumb title>
	title := ridealongTitleStyle.Render("◈ ridealong")
	separator := ridealongDividerStyle.Render("  ---    ")
	fileLabel := ridealongFileStyle.Render(r.DisplayTitle())
	titleRow := title + separator + fileLabel
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

	return pane + "\n"
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
