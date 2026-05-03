package main

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const dynapaneScrollInterval = 80 * time.Millisecond

// DynapaneTickMsg is sent on each scroll interval
type DynapaneTickMsg struct{}

func dynapaneTickCmd() tea.Cmd {
	return tea.Tick(dynapaneScrollInterval, func(t time.Time) tea.Msg {
		return DynapaneTickMsg{}
	})
}

type dynapaneState int

const (
	dynapaneInactive    dynapaneState = iota
	dynapaneRollingDown               // expanding frame by frame
	dynapaneActive                    // fully open, scrolling
	dynapaneRollingUp                 // collapsing frame by frame
)

// rollFrameCount is the number of content lines; also the number of roll animation steps.
const rollFrameCount = 4

var (
	dynapaneBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("99"))

	dynapaneTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("141")).
				Bold(true)

	dynapaneSubtitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243")).
				Italic(true)

	dynapaneScrollStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("117"))

	dynapaneAccentStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("99"))
)

// scrollText is the marquee content that loops horizontally
const scrollText = "  ✦ federation-command  ·  dynapane demo active  ·  this pane will vanish after the next command  ·  dynamic panes render above the prompt in the live view area  ·  use 'above' mode to overlay context, status, or guidance  ·  scrolling content supported  ✦  "

// Dynapane is a temporary pane that renders above the prompt.
// It animates open (roll down) and closed (roll up) rather than snapping.
type Dynapane struct {
	state        dynapaneState
	rollFrame    int
	scrollOffset int
}

// Activate begins the roll-down animation and starts the tick.
func (d *Dynapane) Activate() tea.Cmd {
	d.state = dynapaneRollingDown
	d.rollFrame = 0
	d.scrollOffset = 0
	return dynapaneTickCmd()
}

// Deactivate begins the roll-up animation when fully open; otherwise hides instantly.
func (d *Dynapane) Deactivate() {
	if d.state == dynapaneActive {
		d.state = dynapaneRollingUp
		d.rollFrame = 0
	} else {
		d.state = dynapaneInactive
	}
}

// IsActive returns true while the pane is visible (including during animation).
func (d *Dynapane) IsActive() bool {
	return d.state != dynapaneInactive
}

// Tick advances the animation or scroll position and schedules the next tick.
func (d *Dynapane) Tick() tea.Cmd {
	switch d.state {
	case dynapaneInactive:
		return nil

	case dynapaneRollingDown:
		d.rollFrame++
		if d.rollFrame >= rollFrameCount {
			d.state = dynapaneActive
		}

	case dynapaneActive:
		d.scrollOffset++

	case dynapaneRollingUp:
		d.rollFrame++
		if d.rollFrame > rollFrameCount {
			d.state = dynapaneInactive
			return nil
		}
	}

	return dynapaneTickCmd()
}

// View renders the dynapane, sized to windowWidth.
// During roll-down/up only a partial number of content lines is shown.
func (d *Dynapane) View(windowWidth int) string {
	if d.state == dynapaneInactive {
		return ""
	}

	if windowWidth <= 0 {
		windowWidth = 80
	}

	// Inner width = total - 2 (border sides) - 2 (padding)
	innerWidth := windowWidth - 4
	if innerWidth < 10 {
		innerWidth = 10
	}

	// Build all content lines up front
	title := dynapaneTitleStyle.Render("◈ dynapane")
	badge := dynapaneAccentStyle.Render("above · demo")
	gap := innerWidth - lipgloss.Width(title) - lipgloss.Width(badge)
	if gap < 1 {
		gap = 1
	}
	titleRow := title + strings.Repeat(" ", gap) + badge

	subtitle := dynapaneSubtitleStyle.Render("disappears after next command")
	divider := dynapaneAccentStyle.Render(strings.Repeat("─", innerWidth))

	scrollLine := buildScrollLine(scrollText, d.scrollOffset, innerWidth)
	scrollRendered := dynapaneScrollStyle.Render(scrollLine)

	allLines := []string{titleRow, subtitle, divider, scrollRendered}

	// Determine how many lines are visible based on roll state
	var visibleLines int
	switch d.state {
	case dynapaneRollingDown:
		// rollFrame 0 = empty box; each tick reveals one more line
		visibleLines = d.rollFrame
	case dynapaneActive:
		visibleLines = rollFrameCount
	case dynapaneRollingUp:
		// rolls back down: full at frame 0, one fewer per tick
		visibleLines = rollFrameCount - d.rollFrame
		if visibleLines < 0 {
			visibleLines = 0
		}
	}

	var content string
	if visibleLines > 0 {
		content = strings.Join(allLines[:visibleLines], "\n")
	}

	pane := dynapaneBorderStyle.
		Width(innerWidth).
		Render(content)

	return pane + "\n"
}

// buildScrollLine returns a slice of scrollText offset by pos, cropped to width
func buildScrollLine(text string, offset, width int) string {
	runes := []rune(text)
	n := len(runes)
	if n == 0 || width <= 0 {
		return strings.Repeat(" ", width)
	}

	pos := offset % n
	var b strings.Builder
	for i := 0; i < width; i++ {
		b.WriteRune(runes[(pos+i)%n])
	}
	return b.String()
}
