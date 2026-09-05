package main

import (
	"strings"
	"testing"
)

// TestNewBlinker verifies initial blinker state
func TestNewBlinker(t *testing.T) {
	b := NewBlinker()

	if b.State() != BlinkerIdle {
		t.Errorf("expected initial state BlinkerIdle, got %v", b.State())
	}
	if !b.visible {
		t.Error("expected initial visible to be true")
	}
	if b.IsSelectMode() {
		t.Error("expected initial select mode to be false")
	}
}

// TestBlinkerSetState verifies state transitions
func TestBlinkerSetState(t *testing.T) {
	b := NewBlinker()

	// Set to inactive
	b.SetState(BlinkerInactive)
	if b.State() != BlinkerInactive {
		t.Errorf("expected BlinkerInactive, got %v", b.State())
	}
	if b.visible {
		t.Error("expected visible to be false when inactive")
	}

	// Set to select
	b.SetState(BlinkerSelect)
	if b.State() != BlinkerSelect {
		t.Errorf("expected BlinkerSelect, got %v", b.State())
	}
	if !b.visible {
		t.Error("expected visible to be true when in select mode")
	}
	if !b.IsSelectMode() {
		t.Error("expected IsSelectMode to be true")
	}

	// Set back to idle
	b.SetState(BlinkerIdle)
	if b.State() != BlinkerIdle {
		t.Errorf("expected BlinkerIdle, got %v", b.State())
	}
	if !b.visible {
		t.Error("expected visible to be true when idle")
	}
}

// TestBlinkerTick verifies blink toggling
func TestBlinkerTick(t *testing.T) {
	b := NewBlinker()

	// Initial visible state
	if !b.visible {
		t.Error("expected initial visible to be true")
	}

	// Tick should toggle visibility
	b.Tick()
	if b.visible {
		t.Error("expected visible to be false after first tick")
	}

	b.Tick()
	if !b.visible {
		t.Error("expected visible to be true after second tick")
	}
}

// TestBlinkerTickInactive verifies no ticking when inactive
func TestBlinkerTickInactive(t *testing.T) {
	b := NewBlinker()
	b.SetState(BlinkerInactive)

	initialVisible := b.visible
	cmd := b.Tick()

	if cmd != nil {
		t.Error("expected nil command when inactive")
	}
	if b.visible != initialVisible {
		t.Error("expected visible to remain unchanged when inactive")
	}
}

// TestBlinkerViewIdle verifies idle view rendering
func TestBlinkerViewIdle(t *testing.T) {
	b := NewBlinker()
	b.visible = true

	view := b.View()
	if !strings.Contains(view, HollowBlock) {
		t.Errorf("expected idle view to contain hollow block, got %q", view)
	}

	b.visible = false
	view = b.View()
	if strings.Contains(view, HollowBlock) {
		t.Errorf("expected idle view (invisible) to not contain hollow block, got %q", view)
	}
}

// TestBlinkerViewSelect verifies select mode view rendering
func TestBlinkerViewSelect(t *testing.T) {
	b := NewBlinker()
	b.SetState(BlinkerSelect)
	b.visible = true

	view := b.View()
	if !strings.Contains(view, SolidBlock) {
		t.Errorf("expected select view to contain solid block, got %q", view)
	}
}

// TestBlinkerViewInactive verifies inactive view rendering
func TestBlinkerViewInactive(t *testing.T) {
	b := NewBlinker()
	b.SetState(BlinkerInactive)

	view := b.View()
	// Should contain brackets but no block characters
	if strings.Contains(view, HollowBlock) || strings.Contains(view, SolidBlock) {
		t.Errorf("expected inactive view to not contain block characters, got %q", view)
	}
}

// TestBlinkerStartFlash verifies flash initiation
func TestBlinkerStartFlash(t *testing.T) {
	b := NewBlinker()
	b.SetState(BlinkerSelect)

	cmd := b.StartFlash()
	if cmd == nil {
		t.Error("expected StartFlash to return a command")
	}
	if !b.flashing {
		t.Error("expected flashing to be true after StartFlash")
	}
	if b.flashCount != 4 {
		t.Errorf("expected flashCount to be 4, got %d", b.flashCount)
	}
}

// TestBlinkerAccent verifies the auto-connect accent blink overlays the current
// mode with a brief blue pulse and can be disabled cleanly.
func TestBlinkerAccent(t *testing.T) {
	b := NewBlinker()

	// Disabled by default: no pulse, and ticks are inert.
	if b.accentOn {
		t.Fatal("expected accentOn to be false on a fresh blinker")
	}
	if cmd := b.AccentTick(b.accentGen); cmd != nil {
		t.Error("expected AccentTick to be a no-op while accent is disabled")
	}

	b.EnableAccent()
	if !b.accentEnabled {
		t.Fatal("expected accentEnabled to be true after EnableAccent")
	}
	gen := b.accentGen

	// First tick lights the pulse; View overlays blue regardless of base mode.
	if cmd := b.AccentTick(gen); cmd == nil {
		t.Error("expected AccentTick to return a follow-up command while enabled")
	}
	if !b.accentOn {
		t.Fatal("expected accentOn to be true after first AccentTick")
	}
	if !strings.Contains(b.View(), SolidBlock) {
		t.Errorf("expected accent pulse to render a solid block, got %q", b.View())
	}

	// Second tick clears the pulse.
	b.AccentTick(gen)
	if b.accentOn {
		t.Fatal("expected accentOn to be false after second AccentTick")
	}

	// Stale ticks (old generation) are ignored.
	b.EnableAccent()
	stale := gen
	if cmd := b.AccentTick(stale); cmd != nil {
		t.Error("expected stale-generation AccentTick to be ignored")
	}

	// DisableAccent stops the chain and clears the pulse.
	b.accentOn = true
	b.DisableAccent()
	if b.accentEnabled || b.accentOn {
		t.Error("expected DisableAccent to clear accentEnabled and accentOn")
	}
	if cmd := b.AccentTick(b.accentGen); cmd != nil {
		t.Error("expected AccentTick to be a no-op after DisableAccent")
	}
}

// TestBlinkerShouldBlink verifies ShouldBlink logic
func TestBlinkerShouldBlink(t *testing.T) {
	b := NewBlinker()

	// Idle should blink
	if !b.ShouldBlink() {
		t.Error("expected ShouldBlink to be true when idle")
	}

	// Inactive should not blink
	b.SetState(BlinkerInactive)
	if b.ShouldBlink() {
		t.Error("expected ShouldBlink to be false when inactive")
	}

	// Select should blink
	b.SetState(BlinkerSelect)
	if !b.ShouldBlink() {
		t.Error("expected ShouldBlink to be true when in select mode")
	}

	// Flashing should not trigger normal blink
	b.flashing = true
	if b.ShouldBlink() {
		t.Error("expected ShouldBlink to be false when flashing")
	}
}
