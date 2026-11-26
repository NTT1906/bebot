package bot

import (
	"testing"
)

func TestSpinCommand(t *testing.T) {
	b := &Bot{}

	// Test default spin
	b.spinCmd(nil)
	if !b.spinActive {
		t.Fatalf("expected spinActive to be true")
	}
	if b.spinDirection != 1.0 {
		t.Fatalf("expected spinDirection 1.0 (clockwise), got %f", b.spinDirection)
	}
	if b.spinSpeed != 10.0 {
		t.Fatalf("expected default speed 10.0, got %f", b.spinSpeed)
	}

	// Test counterclockwise
	b.spinCmd([]string{"counterclockwise", "15"})
	if !b.spinActive || b.spinDirection != -1.0 || b.spinSpeed != 15.0 {
		t.Fatalf("expected CCW at 15.0, got active=%v dir=%f speed=%f", b.spinActive, b.spinDirection, b.spinSpeed)
	}

	// Test reverse alias
	b.spinCmd([]string{"reverse"})
	if !b.spinActive || b.spinDirection != -1.0 {
		t.Fatalf("expected reverse direction, got %f", b.spinDirection)
	}

	// Test stop
	b.spinCmd([]string{"stop"})
	if b.spinActive {
		t.Fatalf("expected spinActive false after stop")
	}

	// Test negative speed implicitly setting counterclockwise
	b.spinCmd([]string{"-20"})
	if !b.spinActive || b.spinDirection != -1.0 || b.spinSpeed != 20.0 {
		t.Fatalf("expected CCW at 20.0 for negative speed, got dir=%f speed=%f", b.spinDirection, b.spinSpeed)
	}
}

func TestSpinMoveTickYawUpdate(t *testing.T) {
	b := &Bot{
		spinActive:    true,
		spinDirection: 1.0,
		spinSpeed:     15.0,
		idleYaw:       350.0,
	}

	// Advance yaw simulation:
	b.idleYaw += b.spinSpeed * b.spinDirection
	for b.idleYaw >= 360 {
		b.idleYaw -= 360
	}
	if b.idleYaw != 5.0 {
		t.Fatalf("expected wrapped idleYaw 5.0, got %f", b.idleYaw)
	}

	// Reverse wrap
	b.spinDirection = -1.0
	b.idleYaw = 5.0
	b.idleYaw += b.spinSpeed * b.spinDirection
	for b.idleYaw < 0 {
		b.idleYaw += 360
	}
	if b.idleYaw != 350.0 {
		t.Fatalf("expected wrapped idleYaw 350.0, got %f", b.idleYaw)
	}
}
