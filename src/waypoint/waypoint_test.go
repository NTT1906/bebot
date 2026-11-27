package waypoint

import (
	"path/filepath"
	"testing"
)

func TestWaypointsLoadAndSave(t *testing.T) {
	tmpDir := t.TempDir()
	wpFile := filepath.Join(tmpDir, "sub", "waypoints.json")

	// Initially empty when missing
	wps := LoadWaypoints(wpFile)
	if len(wps) != 0 {
		t.Fatalf("expected empty waypoints, got %d entries", len(wps))
	}

	// Add waypoints and save (verifies directory creation)
	wps["base"] = Waypoint{X: 100, Y: 64, Z: 200, Note: "Home base"}
	wps["mine"] = Waypoint{X: -50, Y: 12, Z: 300, Note: "Diamond level"}
	if err := SaveWaypoints(wpFile, wps); err != nil {
		t.Fatalf("SaveWaypoints failed: %v", err)
	}

	// Reload and verify
	loaded := LoadWaypoints(wpFile)
	if len(loaded) != 2 {
		t.Fatalf("expected 2 waypoints, got %d", len(loaded))
	}
	if base, ok := loaded["base"]; !ok || base.X != 100 || base.Y != 64 || base.Z != 200 || base.Note != "Home base" {
		t.Errorf("base waypoint mismatch: %+v", base)
	}
	if mine, ok := loaded["mine"]; !ok || mine.X != -50 || mine.Y != 12 || mine.Z != 300 || mine.Note != "Diamond level" {
		t.Errorf("mine waypoint mismatch: %+v", mine)
	}
}
