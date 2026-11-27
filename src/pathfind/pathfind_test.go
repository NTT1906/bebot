package pathfind

import (
	"testing"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// mockWorld implements WorldView in memory for unit testing.
type mockWorld struct {
	blocks map[protocol.BlockPos]BlockType
}

func newMockWorld() *mockWorld {
	return &mockWorld{blocks: make(map[protocol.BlockPos]BlockType)}
}

func (m *mockWorld) GetBlockType(pos protocol.BlockPos) BlockType {
	if t, ok := m.blocks[pos]; ok {
		return t
	}
	return BlockAir // default to air if unspecified
}

func (m *mockWorld) set(x, y, z int32, t BlockType) {
	m.blocks[protocol.BlockPos{x, y, z}] = t
}

// setFloor sets a flat horizontal solid floor from (x1, z1) to (x2, z2) at floorY.
func (m *mockWorld) setFloor(x1, z1, x2, z2, floorY int32) {
	for x := x1; x <= x2; x++ {
		for z := z1; z <= z2; z++ {
			m.set(x, floorY, z, BlockSolid)
		}
	}
}

// TestPlan_StraightLine tests a basic horizontal walk on flat ground.
func TestPlan_StraightLine(t *testing.T) {
	w := newMockWorld()
	// Flat floor at Y=63
	w.setFloor(0, 0, 10, 0, 63)

	planner := NewPlanner()
	start := protocol.BlockPos{0, 64, 0}
	goal := protocol.BlockPos{5, 64, 0}

	res, ok := planner.Plan(w, start, goal, DefaultSearchOptions())
	if !ok {
		t.Fatalf("expected path to be found, got false")
	}
	if len(res.Nodes) != 6 {
		t.Fatalf("expected 6 nodes in path (start + 5 steps), got %d: %v", len(res.Nodes), res.Nodes)
	}
	if res.Nodes[len(res.Nodes)-1] != goal {
		t.Errorf("expected final node to be %v, got %v", goal, res.Nodes[len(res.Nodes)-1])
	}
}

// TestPlan_ObstacleAvoidance tests navigating around a solid wall.
func TestPlan_ObstacleAvoidance(t *testing.T) {
	w := newMockWorld()
	// Flat floor at Y=63 from X=0..10, Z=-5..5
	w.setFloor(0, -5, 10, 5, 63)

	// Wall at X=5, Z=-2..2 of height 3 (Y=64, 65, 66)
	for z := int32(-2); z <= 2; z++ {
		for y := int32(64); y <= 66; y++ {
			w.set(5, y, z, BlockSolid)
		}
	}

	planner := NewPlanner()
	start := protocol.BlockPos{2, 64, 0}
	goal := protocol.BlockPos{8, 64, 0}

	res, ok := planner.Plan(w, start, goal, DefaultSearchOptions())
	if !ok {
		t.Fatalf("expected path around wall to be found")
	}
	if !res.ReachedGoal {
		t.Errorf("expected ReachedGoal to be true")
	}
	// Check that none of the path nodes intersect the wall
	for _, n := range res.Nodes {
		if n.X() == 5 && n.Z() >= -2 && n.Z() <= 2 {
			t.Errorf("path erroneously intersected wall at %v", n)
		}
	}
}

// TestPlan_CornerClearance verifies that diagonal moves do NOT cut through solid corners.
func TestPlan_CornerClearance(t *testing.T) {
	w := newMockWorld()
	w.setFloor(0, 0, 5, 5, 63)

	// Corner wall layout:
	// Start at (1, 64, 1), Goal at (2, 64, 2)
	// Place solid blocks at both cardinal neighbors: (2, 64, 1) and (1, 64, 2)
	// Diagonal movement from (1,1) to (2,2) should be blocked by corner clearance!
	w.set(2, 64, 1, BlockSolid)
	w.set(2, 65, 1, BlockSolid)
	w.set(1, 64, 2, BlockSolid)
	w.set(1, 65, 2, BlockSolid)

	neighbors := GetNeighbors(w, protocol.BlockPos{1, 64, 1}, DefaultSearchOptions())
	for _, m := range neighbors {
		if m.To == (protocol.BlockPos{2, 64, 2}) {
			t.Fatalf("illegal diagonal corner cut allowed: %+v", m)
		}
	}
}

// TestPlan_StepUpElevation tests ascending 1-block elevation steps.
func TestPlan_StepUpElevation(t *testing.T) {
	w := newMockWorld()
	// Floor 1: X=0..2 at Y=63 (standing Y=64)
	w.setFloor(0, 0, 2, 0, 63)
	// Step Up to Floor 2: X=3..5 at Y=64 (standing Y=65)
	w.setFloor(3, 0, 5, 0, 64)

	planner := NewPlanner()
	start := protocol.BlockPos{1, 64, 0}
	goal := protocol.BlockPos{4, 65, 0}

	res, ok := planner.Plan(w, start, goal, DefaultSearchOptions())
	if !ok {
		t.Fatalf("expected elevation step-up path to be found")
	}
	hasStepUp := false
	for _, m := range res.Moves {
		if m.Type == MoveStepUp {
			hasStepUp = true
			break
		}
	}
	if !hasStepUp {
		t.Errorf("expected at least one MoveStepUp in trajectory, got: %v", res.Moves)
	}
}

// TestPlan_DropDownLimits tests safe 1..3 drops and rejecting unsafe >3 block cliffs.
func TestPlan_DropDownLimits(t *testing.T) {
	w := newMockWorld()
	// High ledge at X=0..1, Y=70 (standing Y=71)
	w.set(0, 70, 0, BlockSolid)
	w.set(1, 70, 0, BlockSolid)

	// Safe 2-block drop: landing at X=2, Y=68 (standing Y=69)
	w.set(2, 68, 0, BlockSolid)

	neighbors := GetNeighbors(w, protocol.BlockPos{1, 71, 0}, DefaultSearchOptions())
	foundDrop := false
	for _, m := range neighbors {
		if m.Type == MoveDropDown && m.To == (protocol.BlockPos{2, 69, 0}) {
			foundDrop = true
			break
		}
	}
	if !foundDrop {
		t.Errorf("expected safe 2-block drop to (2, 69, 0) to be available")
	}

	// Unsafe 5-block cliff: landing at X=0, Y=65 from (0, 71, 0)
	// (Drop is 5 blocks, must be rejected!)
	w.set(0, 71, -1, BlockAir)
	w.set(0, 65, -1, BlockSolid) // 5 blocks down
	neighborsUnsafe := GetNeighbors(w, protocol.BlockPos{0, 71, 0}, DefaultSearchOptions())
	for _, m := range neighborsUnsafe {
		if m.To.Y() <= 66 {
			t.Errorf("unsafe >3 block drop erroneously permitted: %+v", m)
		}
	}
}

// TestPlan_BestEffortFallback verifies that if a goal is unreachable, the planner returns
// the closest accessible node rather than an empty failure.
func TestPlan_BestEffortFallback(t *testing.T) {
	w := newMockWorld()
	// Flat floor at Y=63
	w.setFloor(0, 0, 10, 0, 63)

	// Completely seal off X=5 with bedrock wall from ceiling to floor
	for y := int32(60); y <= 70; y++ {
		w.set(5, y, 0, BlockSolid)
	}

	planner := NewPlanner()
	start := protocol.BlockPos{0, 64, 0}
	unreachableGoal := protocol.BlockPos{8, 64, 0}

	res, ok := planner.Plan(w, start, unreachableGoal, SearchOptions{
		MaxNodes: 100,
		Timeout:  20 * time.Millisecond,
	})
	if ok {
		t.Errorf("expected exact path to be false due to wall, got true")
	}
	if len(res.Nodes) == 0 {
		t.Fatalf("expected best-effort path nodes, got empty")
	}
	// The best-effort path should bring us as close to the wall as possible (X=4)
	lastNode := res.Nodes[len(res.Nodes)-1]
	if lastNode.X() != 4 {
		t.Errorf("expected best-effort path to terminate near obstacle at X=4, got %v", lastNode)
	}
}
