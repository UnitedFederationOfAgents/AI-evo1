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
	filePath          string
	startPath         string // working directory when the ridealong was launched
	steps             []ridealongStep
	currentIndex      int
	prevExitCode      int // -1 = no command executed yet
	active            bool
	menuIndex         int
	debug             bool
	scrollbackLogPath string
	lastStepReview    string
	fixingIssue       bool // true after fix is selected; clears on step advance
	parent            *Ridealong // non-nil when this is a nested ridealong

	// waypoint state
	waypoints          map[string]int // name → step index
	waypointOrder      []string       // waypoint names in document order
	waypointMenuActive bool
	waypointMenuIndex  int

	// autoplay state
	autoplay      bool
	autoplayTick  int       // increments on each dynapane tick; used for animation only
	autoplayStart time.Time // wall-clock time when the current countdown began
}

// ridealongBlockOpenRegex matches the opening fence of a ```ridealong block.
var ridealongBlockOpenRegex = regexp.MustCompile("^```ridealong\\s*$")

// ridealongLinkRegex matches a markdown inline link [text](path).
var ridealongLinkRegex = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

// ridealongWaypointRegex matches <!-- ridealong waypoint NAME --> comments.
var ridealongWaypointRegex = regexp.MustCompile(`<!--\s*ridealong\s+waypoint\s+(\S+)\s*-->`)

// ridealongContinuesMarker is the annotation signalling a depth-first dive.
const ridealongContinuesMarker = "<!-- ridealong continues -->"

// parseContinuesLine inspects a single source line.  If it contains exactly one
// markdown link and the ridealong-continues marker, it returns the resolved
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
// by <!-- ridealong continues --> annotations in filePath.
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

// ridealongParseResult holds the output of a full ridealong file parse.
type ridealongParseResult struct {
	steps         []ridealongStep
	waypoints     map[string]int
	waypointOrder []string
	errMsg        string
}

// parseRidealong reads a markdown file and returns steps, waypoints, and any error.
// Steps are shell commands (from ```ridealong blocks) interleaved with depth-first
// dive annotations (from <!-- ridealong continues --> lines), in document order.
// Waypoints are recorded from <!-- ridealong waypoint NAME --> comments and map
// each name to the index of the next step after the comment.
func parseRidealong(filePath string) ridealongParseResult {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return ridealongParseResult{errMsg: "ridealong: cannot read file: " + err.Error()}
	}

	var steps []ridealongStep
	waypoints := map[string]int{}
	var waypointOrder []string
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
				continue
			}
			if m := ridealongWaypointRegex.FindStringSubmatch(trimmed); m != nil {
				name := m[1]
				if _, exists := waypoints[name]; !exists {
					waypoints[name] = len(steps)
					waypointOrder = append(waypointOrder, name)
				}
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
	return ridealongParseResult{
		steps:         steps,
		waypoints:     waypoints,
		waypointOrder: waypointOrder,
	}
}

// parseRidealongSteps is a convenience wrapper around parseRidealong for callers
// that only need the step list and error message.
func parseRidealongSteps(filePath string) ([]ridealongStep, string) {
	r := parseRidealong(filePath)
	return r.steps, r.errMsg
}

// NewRidealong creates a new top-level ridealong session from a markdown file.
// Returns nil plus an error message when the file cannot be read, contains no
// actionable steps, or has a cyclic dependency.
func NewRidealong(filePath string) (*Ridealong, string) {
	if hasCycle(filePath) {
		return nil, "ridealong: cyclic dependency detected in " + filepath.Base(filePath)
	}
	result := parseRidealong(filePath)
	if result.errMsg != "" {
		return nil, result.errMsg
	}
	if len(result.steps) == 0 {
		return nil, "ridealong: no commands or links found in file"
	}
	return &Ridealong{
		filePath:      filePath,
		steps:         result.steps,
		currentIndex:  0,
		prevExitCode:  -1,
		active:        true,
		menuIndex:     0,
		waypoints:     result.waypoints,
		waypointOrder: result.waypointOrder,
	}, ""
}

// JumpToWaypoint moves the ridealong to the step indicated by the named waypoint.
// Returns false if the waypoint name is not found.
func (r *Ridealong) JumpToWaypoint(name string) bool {
	if r == nil {
		return false
	}
	idx, ok := r.waypoints[name]
	if !ok {
		return false
	}
	r.currentIndex = idx
	r.prevExitCode = -1
	r.menuIndex = 0
	return true
}

// EnableDebug turns on the debug ridealong actions and records the scrollback
// log file they should pass to the agent.
func (r *Ridealong) EnableDebug(logPath string) {
	if r == nil {
		return
	}
	r.debug = true
	r.scrollbackLogPath = logPath
}

// RootStartPath returns the startPath of the topmost ridealong in the chain.
func (r *Ridealong) RootStartPath() string {
	for r != nil && r.parent != nil {
		r = r.parent
	}
	if r == nil {
		return ""
	}
	return r.startPath
}

// EnableAutoplay activates autoplay on this ridealong, resetting the countdown.
func (r *Ridealong) EnableAutoplay() {
	if r == nil {
		return
	}
	r.autoplay = true
	r.autoplayTick = 0
	r.autoplayStart = time.Now()
}

// AutoplayDeactivate deactivates autoplay on this ridealong and all ancestors.
func (r *Ridealong) AutoplayDeactivate() {
	for r != nil {
		r.autoplay = false
		r = r.parent
	}
}

// ResetAutoplayCountdown resets the countdown for the next step.
func (r *Ridealong) ResetAutoplayCountdown() {
	if r != nil {
		r.autoplayTick = 0
		r.autoplayStart = time.Now()
	}
}

// TickAutoplay increments the animation tick and returns true when 3 seconds
// have elapsed since the countdown started.
func (r *Ridealong) TickAutoplay() bool {
	if r == nil || !r.autoplay {
		return false
	}
	r.autoplayTick++
	return time.Since(r.autoplayStart) >= 3*time.Second
}

// CountdownDisplay returns the formatted countdown string (e.g. "3..") or "" if
// autoplay is not active.
func (r *Ridealong) CountdownDisplay() string {
	if r == nil || !r.autoplay {
		return ""
	}
	elapsed := time.Since(r.autoplayStart).Seconds()
	state := int(elapsed * 4) // 4 display states per second
	if state >= 12 {
		state = 11
	}
	num := 3 - state/4
	dots := strings.Repeat(".", state%4)
	return fmt.Sprintf("%d%s", num, dots)
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
	r.lastStepReview = ""
	r.fixingIssue = false
	r.menuIndex = 0
	r.currentIndex++
	if r.autoplay {
		r.autoplayTick = 0
		r.autoplayStart = time.Now()
	}
	return r.currentIndex < len(r.steps)
}

// StartFix marks that a fix action is in progress, hiding debug menu options
// until the next step is executed.
func (r *Ridealong) StartFix() {
	if r == nil {
		return
	}
	r.fixingIssue = true
	r.menuIndex = 0
}

type ridealongMenuAction int

const (
	ridealongActionExecute    ridealongMenuAction = iota
	ridealongActionReview
	ridealongActionFixReviewed
	ridealongActionFixLastStep
	ridealongActionWaypoint
	ridealongActionAutoplay
	ridealongActionExit
)

// menuItem is used internally to build the ordered menu list.
type ridealongMenuItem struct {
	label   string
	isDebug bool
	action  ridealongMenuAction
}

// buildMenuItems constructs the current ordered list of menu items.
func (r *Ridealong) buildMenuItems() []ridealongMenuItem {
	items := []ridealongMenuItem{{"execute command", false, ridealongActionExecute}}
	if r.debug && !r.fixingIssue {
		if r.lastStepReview != "" {
			items = append(items, ridealongMenuItem{"fix issue", true, ridealongActionFixReviewed})
		} else {
			items = append(items,
				ridealongMenuItem{"review last step", true, ridealongActionReview},
				ridealongMenuItem{"fix issue from last step", true, ridealongActionFixLastStep},
			)
		}
	}
	if len(r.waypointOrder) > 0 {
		items = append(items, ridealongMenuItem{"waypoint", false, ridealongActionWaypoint})
	}
	items = append(items, ridealongMenuItem{"autoplay", false, ridealongActionAutoplay})
	items = append(items, ridealongMenuItem{"exit", false, ridealongActionExit})
	return items
}

func (r *Ridealong) menuItemCount() int {
	if r == nil {
		return 2
	}
	if r.waypointMenuActive {
		return len(r.waypointOrder) + 1 // waypoints + cancel
	}
	return len(r.buildMenuItems())
}

// MenuUp moves menu selection toward "execute command".
func (r *Ridealong) MenuUp() {
	if r == nil {
		return
	}
	if r.waypointMenuActive {
		if r.waypointMenuIndex > 0 {
			r.waypointMenuIndex--
		}
	} else if r.menuIndex > 0 {
		r.menuIndex--
	}
}

// MenuDown moves menu selection toward "exit".
func (r *Ridealong) MenuDown() {
	if r == nil {
		return
	}
	if r.waypointMenuActive {
		if r.waypointMenuIndex < len(r.waypointOrder) {
			r.waypointMenuIndex++
		}
	} else if r.menuIndex < r.menuItemCount()-1 {
		r.menuIndex++
	}
}

// MenuSelection returns the current menu index.
func (r *Ridealong) MenuSelection() int {
	if r == nil {
		return 0
	}
	return r.menuIndex
}

// MenuAction returns the action for the currently selected menu item.
func (r *Ridealong) MenuAction() ridealongMenuAction {
	if r == nil {
		return ridealongActionExecute
	}
	items := r.buildMenuItems()
	if r.menuIndex >= 0 && r.menuIndex < len(items) {
		return items[r.menuIndex].action
	}
	return ridealongActionExit
}

func (r *Ridealong) CacheReview(review string) {
	if r == nil {
		return
	}
	r.lastStepReview = strings.TrimSpace(review)
	if r.debug && r.lastStepReview != "" {
		r.menuIndex = 1
	}
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

// Tick handles dynapane ticks (for animations and autoplay countdown).
func (rd *RidealongDynapane) Tick() tea.Cmd {
	if !rd.active {
		return nil
	}
	return ridealongDynapaneTickCmd()
}

// autoplay color palette — cycles across letters for the glowing effect.
var autoplayColors = []lipgloss.Color{
	"99", "105", "111", "117", "123", "129", "135", "141",
}

// renderAutoplayText renders "autoplaying..." with per-character color cycling
// offset by the current tick so the colors appear to scroll.
func renderAutoplayText(tick int) string {
	text := "autoplaying..."
	var sb strings.Builder
	for i, ch := range text {
		colorIdx := (i + tick/2) % len(autoplayColors)
		style := lipgloss.NewStyle().Foreground(autoplayColors[colorIdx]).Bold(true)
		sb.WriteString(style.Render(string(ch)))
	}
	return sb.String()
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

	ridealongMenuDebugSelectedStyle = lipgloss.NewStyle().
						Foreground(lipgloss.Color("208")).
						Bold(true)

	ridealongMenuDebugStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("136"))

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

	ridealongCountdownStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("220")).
				Bold(true)

	ridealongHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243")).
				Italic(true)

	ridealongWaypointHeaderStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("141")).
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
	title := ridealongTitleStyle.Render("🚔 ridealong")
	separator := ridealongDividerStyle.Render("  ---    ")
	fileLabel := ridealongFileStyle.Render(r.DisplayTitle())
	titleRow := title + separator + fileLabel
	titleRowWidth := lipgloss.Width(titleRow)
	if titleRowWidth < innerWidth {
		titleRow += strings.Repeat(" ", innerWidth-titleRowWidth)
	}

	divider := ridealongDividerStyle.Render(strings.Repeat("─", innerWidth))

	var menuLines []string

	if r.waypointMenuActive {
		// Waypoint sub-menu
		menuLines = append(menuLines, ridealongWaypointHeaderStyle.Render("  select a waypoint:"))
		for i, name := range r.waypointOrder {
			if r.waypointMenuIndex == i {
				menuLines = append(menuLines, ridealongMenuSelectedStyle.Render("◈ "+name))
			} else {
				menuLines = append(menuLines, ridealongMenuStyle.Render("  "+name))
			}
		}
		cancelIdx := len(r.waypointOrder)
		if r.waypointMenuIndex == cancelIdx {
			menuLines = append(menuLines, ridealongMenuSelectedStyle.Render("◈ cancel"))
		} else {
			menuLines = append(menuLines, ridealongMenuStyle.Render("  cancel"))
		}
	} else {
		// Normal menu
		items := r.buildMenuItems()
		for i, item := range items {
			selected := r.menuIndex == i

			if item.action == ridealongActionAutoplay && r.autoplay {
				// Animated autoplay item with countdown
				animText := renderAutoplayText(r.autoplayTick)
				countdown := ridealongCountdownStyle.Render(" [" + r.CountdownDisplay() + "]")
				if selected {
					menuLines = append(menuLines, ridealongMenuSelectedStyle.Render("◈ ")+animText+countdown)
				} else {
					menuLines = append(menuLines, ridealongMenuStyle.Render("  ")+animText+countdown)
				}
			} else {
				label := item.label
				if selected {
					if item.isDebug {
						menuLines = append(menuLines, ridealongMenuDebugSelectedStyle.Render("◈ "+label))
					} else {
						menuLines = append(menuLines, ridealongMenuSelectedStyle.Render("◈ "+label))
					}
				} else {
					if item.isDebug {
						menuLines = append(menuLines, ridealongMenuDebugStyle.Render("  "+label))
					} else {
						menuLines = append(menuLines, ridealongMenuStyle.Render("  "+label))
					}
				}
			}
		}
		if r.autoplay {
			menuLines = append(menuLines, ridealongHintStyle.Render("  ◂ press left to exit autoplay"))
		}
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
			prefix = ridealongErrorCodeStyle.Render("[" + fmt.Sprintf("%d", prevExitCode) + "] ")
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
	}
	allLines = append(allLines, menuLines...)
	allLines = append(allLines, divider, prevCmdLine, currentCmdLine, nextCmdLine)

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
