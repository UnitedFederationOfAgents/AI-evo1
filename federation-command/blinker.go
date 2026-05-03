package main

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// BlinkerState represents the current state of the blinker
type BlinkerState int

const (
	BlinkerIdle     BlinkerState = iota // Blinking with hollow grey block (default)
	BlinkerInactive                     // Blank, not blinking (user has typed)
	BlinkerSelect                       // Blinking with solid grey block (blinker select mode)
)

// Standard cursor blink interval (typical terminal cursor blink rate)
const BlinkInterval = 530 * time.Millisecond

// Blinker manages the blinker slot state and rendering
type Blinker struct {
	state      BlinkerState
	visible    bool // Whether the block character is currently visible (for blinking)
	flashing   bool // Whether we're in a flash state (for invalid key press in select mode)
	flashCount int  // Number of remaining flash cycles
}

// NewBlinker creates a new blinker in the default idle (blinking) state
func NewBlinker() Blinker {
	return Blinker{
		state:   BlinkerIdle,
		visible: true,
	}
}

// BlinkerTickMsg is sent on each blink interval
type BlinkerTickMsg struct{}

// BlinkerFlashMsg is sent during flash animation
type BlinkerFlashMsg struct{}

// blinkerTickCmd returns a command that sends BlinkerTickMsg after the blink interval
func blinkerTickCmd() tea.Cmd {
	return tea.Tick(BlinkInterval, func(t time.Time) tea.Msg {
		return BlinkerTickMsg{}
	})
}

// blinkerFlashCmd returns a command for flash animation (faster than normal blink)
func blinkerFlashCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return BlinkerFlashMsg{}
	})
}

// Styles for the blinker
var (
	// Light blue brackets (like info commands displaying local binaries)
	blinkerBracketStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("117")) // Light blue

	// Grey block characters
	blinkerBlockStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243")) // Grey

	// Flash style - brighter to draw attention
	blinkerFlashStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("255")) // Bright white
)

// Unicode block characters
const (
	HollowBlock = "▯" // U+25AF - hollow/white rectangle
	SolidBlock  = "▮" // U+25AE - solid/black rectangle
)

// Tick handles the blink tick, toggling visibility
func (b *Blinker) Tick() tea.Cmd {
	if b.state == BlinkerInactive {
		return nil
	}

	b.visible = !b.visible
	return blinkerTickCmd()
}

// Flash handles the flash animation for invalid key press in select mode
func (b *Blinker) Flash() tea.Cmd {
	if b.flashCount > 0 {
		b.visible = !b.visible
		b.flashCount--
		return blinkerFlashCmd()
	}
	b.flashing = false
	b.visible = true
	return blinkerTickCmd()
}

// StartFlash initiates a flash sequence (called when invalid key pressed in select mode)
func (b *Blinker) StartFlash() tea.Cmd {
	b.flashing = true
	b.flashCount = 6 // 3 full blink cycles
	b.visible = false
	return blinkerFlashCmd()
}

// SetState changes the blinker state
func (b *Blinker) SetState(state BlinkerState) {
	b.state = state
	if state == BlinkerInactive {
		b.visible = false
	} else {
		b.visible = true
	}
}

// State returns the current blinker state
func (b *Blinker) State() BlinkerState {
	return b.state
}

// IsSelectMode returns true if the blinker is in select mode
func (b *Blinker) IsSelectMode() bool {
	return b.state == BlinkerSelect
}

// View renders the blinker slot
func (b *Blinker) View() string {
	openBracket := blinkerBracketStyle.Render("[")
	closeBracket := blinkerBracketStyle.Render("]")

	var content string
	switch b.state {
	case BlinkerInactive:
		content = " " // Blank space
	case BlinkerIdle:
		if b.visible {
			if b.flashing {
				content = blinkerFlashStyle.Render(HollowBlock)
			} else {
				content = blinkerBlockStyle.Render(HollowBlock)
			}
		} else {
			content = " "
		}
	case BlinkerSelect:
		if b.visible {
			if b.flashing {
				content = blinkerFlashStyle.Render(SolidBlock)
			} else {
				content = blinkerBlockStyle.Render(SolidBlock)
			}
		} else {
			content = " "
		}
	}

	return openBracket + content + closeBracket
}

// ShouldBlink returns true if the blinker should be ticking
func (b *Blinker) ShouldBlink() bool {
	return b.state != BlinkerInactive && !b.flashing
}
