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
	BlinkerConnected                      // Slow blue blink when connected to local-representative (remote control)
	BlinkerLocalControl                   // Slow orange blink when connected but FC has local control
)

// Standard cursor blink interval (typical terminal cursor blink rate)
const BlinkInterval = 530 * time.Millisecond

// ConnectingBlinkInterval is the fast blue flash rate while connecting to LR
const ConnectingBlinkInterval = 100 * time.Millisecond

// Accent blink: a brief blue pulse overlaid on whatever mode is active, used to
// signal that a background auto-connect attempt is in progress. accentBlinkGap is
// the quiet time between pulses; accentBlinkDur is how long each pulse shows.
const (
	accentBlinkGap = 3 * time.Second
	accentBlinkDur = 130 * time.Millisecond
)

// Blinker manages the blinker slot state and rendering
type Blinker struct {
	state         BlinkerState
	visible       bool // Whether the indicator is currently visible (for blinking)
	flashing      bool // Whether we're in a flash state (for invalid key press in select mode)
	flashCount    int  // Number of remaining flash cycles
	gen           int  // Generation counter; invalidates stale tick timers on state changes
	ridealongBlue bool // For ridealong mode: toggles between red (false) and blue (true)

	accentEnabled bool // When true, a brief blue accent pulse is overlaid on the current mode
	accentOn      bool // Whether the accent pulse is currently showing
	accentGen     int  // Generation counter for the independent accent tick chain
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

// AccentBlinkMsg drives the accent-blink chain; gen must match the blinker's
// current accentGen or the tick is ignored (stale from a disabled chain).
type AccentBlinkMsg struct{ gen int }

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

// accentTickCmd schedules the next accent-blink transition, capturing the current
// accent generation so a disabled/re-enabled chain ignores stale ticks.
func (b *Blinker) accentTickCmd() tea.Cmd {
	gen := b.accentGen
	d := accentBlinkGap
	if b.accentOn {
		d = accentBlinkDur
	}
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return AccentBlinkMsg{gen: gen}
	})
}

// EnableAccent turns on the background accent blink. The caller is responsible for
// emitting the initial tick (Init does this via accentTickCmd); a running program
// should batch b.accentTickCmd() alongside enabling.
func (b *Blinker) EnableAccent() {
	b.accentEnabled = true
	b.accentOn = false
	b.accentGen++
}

// DisableAccent stops the accent blink and invalidates its tick chain.
func (b *Blinker) DisableAccent() {
	b.accentEnabled = false
	b.accentOn = false
	b.accentGen++
}

// AccentTick advances the accent-blink cycle: it flips accentOn and reschedules
// itself. It returns nil once the accent blink has been disabled or the tick is
// stale, ending the chain.
func (b *Blinker) AccentTick(gen int) tea.Cmd {
	if !b.accentEnabled || gen != b.accentGen {
		return nil
	}
	b.accentOn = !b.accentOn
	return b.accentTickCmd()
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

	// Connected/connecting style - dark blue (connecting fast blink, local-control slow blink)
	blinkerConnectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("33"))

	// Remote-control style - light blue, alternates with blinkerConnectedStyle for remote-control indication
	blinkerConnectedLightStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("81"))

	// Accent-blink style - vivid blue pulse overlaid on the current mode during background auto-connect
	blinkerAccentStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("27"))
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
	if b.state == BlinkerRidealong || b.state == BlinkerCondoc || b.state == BlinkerConnected {
		// Toggle colour each tick (always visible): ridealong/condoc alternate their colours;
		// BlinkerConnected alternates dark-blue/light-blue for remote-control indication.
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

// IsConnected returns true when FC has an active connection to LR in remote control mode.
func (b *Blinker) IsConnected() bool {
	return b.state == BlinkerConnected
}

// IsLocalControl returns true when FC is connected to LR but has local control.
func (b *Blinker) IsLocalControl() bool {
	return b.state == BlinkerLocalControl
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
	case BlinkerConnecting:
		if b.visible {
			content = blinkerConnectedStyle.Render(SolidBlock)
		} else {
			content = " "
		}
	case BlinkerConnected:
		// Remote control: always visible, alternates dark-blue / light-blue.
		if b.ridealongBlue {
			content = blinkerConnectedStyle.Render(SolidBlock)
		} else {
			content = blinkerConnectedLightStyle.Render(SolidBlock)
		}
	case BlinkerLocalControl:
		// Local control: slow on/off blue blink (user's blinking cursor already signals local mode).
		if b.visible {
			content = blinkerConnectedStyle.Render(SolidBlock)
		} else {
			content = " "
		}
	}

	// Accent blink: brief vivid-blue pulse overlaid on whatever mode is active,
	// signalling that a background auto-connect attempt is still in progress.
	if b.accentOn {
		content = blinkerAccentStyle.Render(SolidBlock)
	}

	return openBracket + content + closeBracket
}

// IsCondocMode returns true if the blinker is in condoc mode
func (b *Blinker) IsCondocMode() bool {
	return b.state == BlinkerCondoc
}

// IsRemoteControlActive returns true when FC is connected to LR (remote or local control).
func (b *Blinker) IsRemoteControlActive() bool {
	return b.state == BlinkerConnected || b.state == BlinkerLocalControl
}

// ShouldBlink returns true if the blinker should be ticking
func (b *Blinker) ShouldBlink() bool {
	return b.state != BlinkerInactive && !b.flashing
}
