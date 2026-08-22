package main

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// BlinkerState represents the current state of the blinker
type BlinkerState int

const (
	BlinkerIdle       BlinkerState = iota // Blinking with hollow grey circle (default)
	BlinkerInactive                       // Blank, not blinking (user has typed)
	BlinkerSelect                         // Blinking with solid grey circle (blinker select mode)
	BlinkerRidealong                      // Blinking red/blue for ridealong mode (always on)
	BlinkerCondoc                         // Blinking green/yellow for condoc mode (always on)
	BlinkerConnecting                     // Fast blue blink while connecting to local-representative
	BlinkerConnected                      // Slow blue blink when connected to local-representative
)

// Standard cursor blink interval (typical terminal cursor blink rate)
const BlinkInterval = 530 * time.Millisecond

// ConnectingBlinkInterval is the fast blue flash rate while connecting to LR
const ConnectingBlinkInterval = 100 * time.Millisecond

// Blinker manages the blinker slot state and rendering
type Blinker struct {
	state         BlinkerState
	visible       bool // Whether the indicator is currently visible (for blinking)
	flashing      bool // Whether we're in a flash state (for invalid key press in select mode)
	flashCount    int  // Number of remaining flash cycles
	gen           int  // Generation counter; invalidates stale tick timers on state changes
	ridealongBlue bool // For ridealong mode: toggles between red (false) and blue (true)
}

// NewBlinker creates a new blinker in the default idle (blinking) state
func NewBlinker() Blinker {
	return Blinker{
		state:   BlinkerIdle,
		visible: true,
	}
}

// BlinkerTickMsg is sent on each blink interval; gen must match the blinker's
// current generation or the tick is ignored (stale from an old chain).
type BlinkerTickMsg struct{ gen int }

// BlinkerFlashMsg is sent during flash animation
type BlinkerFlashMsg struct{}

// BlinkerConnectingTickMsg drives the fast blue flash during a connection attempt.
type BlinkerConnectingTickMsg struct{}

// tickCmd schedules the next tick, capturing the current generation.
func (b *Blinker) tickCmd() tea.Cmd {
	gen := b.gen
	return tea.Tick(BlinkInterval, func(t time.Time) tea.Msg {
		return BlinkerTickMsg{gen: gen}
	})
}

// blinkerFlashCmd returns a command for flash animation (faster than normal blink)
func blinkerFlashCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg {
		return BlinkerFlashMsg{}
	})
}

// connectingTickCmd drives the fast blue pulse while a connection attempt is in flight.
func connectingTickCmd() tea.Cmd {
	return tea.Tick(ConnectingBlinkInterval, func(t time.Time) tea.Msg {
		return BlinkerConnectingTickMsg{}
	})
}

// ResetTick starts a fresh tick chain by incrementing the generation, which
// causes any already-scheduled BlinkerTickMsgs to be ignored when they arrive.
// Returns nil if the blinker is currently inactive.
func (b *Blinker) ResetTick() tea.Cmd {
	b.gen++
	if b.state == BlinkerInactive {
		return nil
	}
	return b.tickCmd()
}

// Styles for the blinker
var (
	// Light blue brackets
	blinkerBracketStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("117"))

	// Grey indicator characters
	blinkerBlockStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243"))

	// Flash style - brighter to draw attention
	blinkerFlashStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("255"))

	// Ridealong mode styles - alternates red/blue
	blinkerRedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	blinkerBlueStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("39"))

	// Condoc mode styles - alternates green/yellow
	blinkerGreenStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("34"))

	blinkerYellowStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("220"))

	// Connected/connecting style - blue, used for both connecting (fast) and connected (slow)
	blinkerConnectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("33"))
)

// Indicator characters — circles are reliably single-cell-wide in all terminals
const (
	HollowBlock = "○" // U+25CB WHITE CIRCLE  (idle)
	SolidBlock  = "●" // U+25CF BLACK CIRCLE  (selected/ridealong)
)

// Tick handles a blink tick, toggling visibility. The generation check is
// performed by the Update() handler before calling this method; Tick() itself
// continues the chain using the current generation.
func (b *Blinker) Tick() tea.Cmd {
	if b.state == BlinkerInactive {
		return nil
	}
	if b.state == BlinkerRidealong || b.state == BlinkerCondoc {
		// In ridealong/condoc mode, toggle colour each tick (always visible)
		b.ridealongBlue = !b.ridealongBlue
		b.visible = true
	} else {
		b.visible = !b.visible
	}
	return b.tickCmd()
}

// Flash handles the flash animation for an invalid key press in select mode
func (b *Blinker) Flash() tea.Cmd {
	if b.flashCount > 0 {
		b.visible = !b.visible
		b.flashCount--
		return blinkerFlashCmd()
	}
	b.flashing = false
	b.visible = true
	return b.ResetTick() // resume normal tick chain after flash
}

// StartFlash initiates a flash sequence (called when invalid key pressed in select mode)
func (b *Blinker) StartFlash() tea.Cmd {
	if b.flashing {
		// Already flashing — reset the count but don't start a second chain
		b.flashCount = 4
		b.visible = false
		return nil
	}
	b.flashing = true
	b.flashCount = 4 // 2 full blink cycles at 80 ms each = 320 ms
	b.visible = false
	b.gen++ // invalidate any running tick chain
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

// StartConnecting switches to BlinkerConnecting and returns the fast tick command.
// It invalidates the normal tick chain so the two don't interfere.
func (b *Blinker) StartConnecting() tea.Cmd {
	b.state = BlinkerConnecting
	b.visible = true
	b.gen++ // invalidate any running normal tick chain
	return connectingTickCmd()
}

// ConnectingTick toggles the connecting flash and reschedules itself.
// Returns nil once the state is no longer BlinkerConnecting, ending the chain.
func (b *Blinker) ConnectingTick() tea.Cmd {
	if b.state != BlinkerConnecting {
		return nil
	}
	b.visible = !b.visible
	return connectingTickCmd()
}

// State returns the current blinker state
func (b *Blinker) State() BlinkerState {
	return b.state
}

// IsSelectMode returns true if the blinker is in select mode
func (b *Blinker) IsSelectMode() bool {
	return b.state == BlinkerSelect
}

// IsRidealongMode returns true if the blinker is in ridealong mode
func (b *Blinker) IsRidealongMode() bool {
	return b.state == BlinkerRidealong
}

// IsConnecting returns true while a connection attempt to LR is in flight.
func (b *Blinker) IsConnecting() bool {
	return b.state == BlinkerConnecting
}

// IsConnected returns true when FC has an active connection to LR.
func (b *Blinker) IsConnected() bool {
	return b.state == BlinkerConnected
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
	case BlinkerRidealong:
		// Always visible in ridealong mode, alternates red/blue
		if b.ridealongBlue {
			content = blinkerBlueStyle.Render(SolidBlock)
		} else {
			content = blinkerRedStyle.Render(SolidBlock)
		}
	case BlinkerCondoc:
		// Always visible in condoc mode, alternates green/yellow
		if b.ridealongBlue {
			content = blinkerGreenStyle.Render(SolidBlock)
		} else {
			content = blinkerYellowStyle.Render(SolidBlock)
		}
	case BlinkerConnecting, BlinkerConnected:
		if b.visible {
			content = blinkerConnectedStyle.Render(SolidBlock)
		} else {
			content = " "
		}
	}

	return openBracket + content + closeBracket
}

// IsCondocMode returns true if the blinker is in condoc mode
func (b *Blinker) IsCondocMode() bool {
	return b.state == BlinkerCondoc
}

// ShouldBlink returns true if the blinker should be ticking
func (b *Blinker) ShouldBlink() bool {
	return b.state != BlinkerInactive && !b.flashing
}
