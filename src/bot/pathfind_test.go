package bot

import (
	"strings"
	"testing"
	"time"

	"bebot/src/pathfind"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// TestBotWorldView tests the spatial block type classification in BotWorldView.
func TestBotWorldView(t *testing.T) {
	reg := NewBlockRegistry()
	reg.SetActualAirRID(0)
	reg.Register(1, "minecraft:stone")
	reg.Register(2, "minecraft:water")
	reg.Register(3, "minecraft:lava")
	reg.Register(4, "minecraft:cactus")
	reg.Register(5, "minecraft:poppy")
	reg.Register(6, "minecraft:short_grass")

	cache := NewChunkCache()
	cache.Enable(protocol.SubChunkPos{0, 4, 0})

	b := &Bot{
		blockRegistry:   reg,
		chunkCache:      cache,
		blockRuntimeIDs: make(map[protocol.BlockPos]uint32),
	}
	wv := NewBotWorldView(b)

	// Air (RID 0)
	cache.OnBlockUpdate(protocol.BlockPos{0, 64, 0}, 0)
	if bt := wv.GetBlockType(protocol.BlockPos{0, 64, 0}); bt != pathfind.BlockAir {
		t.Errorf("expected BlockAir, got %v", bt)
	}

	// Stone (Solid)
	cache.OnBlockUpdate(protocol.BlockPos{0, 63, 0}, 1)
	if bt := wv.GetBlockType(protocol.BlockPos{0, 63, 0}); bt != pathfind.BlockSolid {
		t.Errorf("expected BlockSolid, got %v", bt)
	}

	// Water (LiquidWater)
	cache.OnBlockUpdate(protocol.BlockPos{1, 64, 0}, 2)
	if bt := wv.GetBlockType(protocol.BlockPos{1, 64, 0}); bt != pathfind.BlockLiquidWater {
		t.Errorf("expected BlockLiquidWater, got %v", bt)
	}

	// Lava (LiquidLava)
	cache.OnBlockUpdate(protocol.BlockPos{2, 64, 0}, 3)
	if bt := wv.GetBlockType(protocol.BlockPos{2, 64, 0}); bt != pathfind.BlockLiquidLava {
		t.Errorf("expected BlockLiquidLava, got %v", bt)
	}

	// Cactus (Hazard)
	cache.OnBlockUpdate(protocol.BlockPos{3, 64, 0}, 4)
	if bt := wv.GetBlockType(protocol.BlockPos{3, 64, 0}); bt != pathfind.BlockHazard {
		t.Errorf("expected BlockHazard, got %v", bt)
	}

	// Flowers and grass (Pass-through decorative -> BlockAir)
	cache.OnBlockUpdate(protocol.BlockPos{4, 64, 0}, 5) // poppy
	if bt := wv.GetBlockType(protocol.BlockPos{4, 64, 0}); bt != pathfind.BlockAir {
		t.Errorf("expected BlockAir for poppy, got %v", bt)
	}
	cache.OnBlockUpdate(protocol.BlockPos{5, 64, 0}, 6) // short_grass
	if bt := wv.GetBlockType(protocol.BlockPos{5, 64, 0}); bt != pathfind.BlockAir {
		t.Errorf("expected BlockAir for short_grass, got %v", bt)
	}

	// Single block updates override chunk cache
	b.blockRuntimeIDs[protocol.BlockPos{0, 63, 0}] = 0 // turn stone into air
	if bt := wv.GetBlockType(protocol.BlockPos{0, 63, 0}); bt != pathfind.BlockAir {
		t.Errorf("expected BlockAir after blockRuntimeIDs override, got %v", bt)
	}

	// Below bedrock void
	if bt := wv.GetBlockType(protocol.BlockPos{0, -70, 0}); bt != pathfind.BlockAir {
		t.Errorf("expected BlockAir below Y=-64, got %v", bt)
	}
}

// TestBotWorldViewStandingAndGoalNodes tests resolution of standing positions.
func TestBotWorldViewStandingAndGoalNodes(t *testing.T) {
	cache := NewChunkCache()
	cache.Enable(protocol.SubChunkPos{0, 4, 0})

	b := &Bot{
		blockRegistry:   NewBlockRegistry(),
		chunkCache:      cache,
		blockRuntimeIDs: make(map[protocol.BlockPos]uint32),
	}
	wv := NewBotWorldView(b)

	// Set up ground at Y=63 (RID 1 = solid), air at Y=64, 65 (RID 0 = air)
	cache.OnBlockUpdate(protocol.BlockPos{10, 63, 10}, 1)
	cache.OnBlockUpdate(protocol.BlockPos{10, 64, 10}, 0)
	cache.OnBlockUpdate(protocol.BlockPos{10, 65, 10}, 0)

	standing := wv.FindStandingNode(protocol.BlockPos{10, 64, 10})
	if standing != (protocol.BlockPos{10, 64, 10}) {
		t.Errorf("expected standing node at {10, 64, 10}, got %v", standing)
	}

	// If standing node is requested for solid block at Y=63, GoalNode resolves to onTop {10, 64, 10}
	goal := wv.FindGoalNode(protocol.BlockPos{10, 63, 10})
	if goal != (protocol.BlockPos{10, 64, 10}) {
		t.Errorf("expected goal node at {10, 64, 10}, got %v", goal)
	}
}

// TestPathFollowerLifecycle tests startPath, waypoint advancement, edge jump, and stopPath.
func TestPathFollowerLifecycle(t *testing.T) {
	cache := NewChunkCache()
	cache.Enable(protocol.SubChunkPos{0, 4, 0})

	var replied []string
	b := &Bot{
		pos:             mgl32.Vec3{0, 64, 0},
		chunkCache:      cache,
		blockRegistry:   NewBlockRegistry(),
		blockRuntimeIDs: make(map[protocol.BlockPos]uint32),
		players:         make(map[string]player),
		testReplyHook: func(msg string) {
			replied = append(replied, msg)
		},
	}
	b.pathPlanner = pathfind.NewPlanner()

	// Create a 5-block flat strip: Y=63 solid, Y=64 & 65 air
	for x := int32(0); x <= 5; x++ {
		cache.OnBlockUpdate(protocol.BlockPos{x, 63, 0}, 1)
		cache.OnBlockUpdate(protocol.BlockPos{x, 64, 0}, 0)
		cache.OnBlockUpdate(protocol.BlockPos{x, 65, 0}, 0)
	}

	// Start path from 0,64,0 to 3,64,0
	b.StartPath(protocol.BlockPos{3, 64, 0}, "")

	if !b.IsPathActive() {
		t.Fatal("expected path to be active")
	}
	if len(b.pathNodes) == 0 {
		t.Fatal("expected path nodes to be populated")
	}

	// Simulate ticking follower: bot moves close to first waypoint
	b.mu.Lock()
	firstWP := b.pathNodes[0]
	b.pos = mgl32.Vec3{float32(firstWP.X()) + 0.5, float32(firstWP.Y()), float32(firstWP.Z()) + 0.5}
	b.mu.Unlock()
	jump, _ := b.pathFollowTick(nil)

	if jump {
		t.Errorf("did not expect jump on flat terrain")
	}

	// Advance to destination
	b.mu.Lock()
	lastWP := b.pathNodes[len(b.pathNodes)-1]
	b.pathIndex = len(b.pathNodes) - 1
	b.pos = mgl32.Vec3{float32(lastWP.X()) + 0.5, float32(lastWP.Y()), float32(lastWP.Z()) + 0.5}
	b.mu.Unlock()
	_, msg := b.pathFollowTick(nil)

	if !strings.Contains(msg, "Destination reached") {
		t.Errorf("expected destination reached reply, got %q", msg)
	}
	if b.IsPathActive() {
		t.Errorf("expected path to be inactive after completion")
	}

	// Test stopPath (navigate back to 0, 64, 0)
	b.StartPath(protocol.BlockPos{0, 64, 0}, "")
	if !b.IsPathActive() {
		t.Fatal("expected path to be active after restart")
	}
	wasActive := b.StopPath()
	if !wasActive || b.IsPathActive() {
		t.Errorf("expected StopPath to stop active path")
	}
}

// TestGotoCommands tests command parsing for goto and stoppath.
func TestGotoCommands(t *testing.T) {
	cache := NewChunkCache()
	cache.Enable(protocol.SubChunkPos{0, 4, 0})

	var replied []string
	b := &Bot{
		pos:             mgl32.Vec3{0, 64, 0},
		chunkCache:      cache,
		blockRegistry:   NewBlockRegistry(),
		blockRuntimeIDs: make(map[protocol.BlockPos]uint32),
		players:         make(map[string]player),
		testReplyHook: func(msg string) {
			replied = append(replied, msg)
		},
	}
	b.pathPlanner = pathfind.NewPlanner()

	// Setup ground
	for x := int32(0); x <= 5; x++ {
		b.chunkCache.OnBlockUpdate(protocol.BlockPos{x, 63, 0}, 1)
		b.chunkCache.OnBlockUpdate(protocol.BlockPos{x, 64, 0}, 0)
		b.chunkCache.OnBlockUpdate(protocol.BlockPos{x, 65, 0}, 0)
	}

	// 1. Empty args
	replied = nil
	b.gotoCmd([]string{})
	if len(replied) == 0 || !strings.Contains(replied[0], "Usage") {
		t.Errorf("expected usage message, got %v", replied)
	}

	// 2. Coordinate navigation
	replied = nil
	b.gotoCmd([]string{"2", "64", "0"})
	time.Sleep(30 * time.Millisecond)
	if !b.IsPathActive() {
		t.Errorf("expected path to be active after goto 2 64 0")
	}

	// 3. Stoppath
	replied = nil
	b.stopPathCmd()
	if b.IsPathActive() {
		t.Errorf("expected path to be stopped")
	}
	if len(replied) == 0 || !strings.Contains(replied[0], "stopped") {
		t.Errorf("expected stopped confirmation, got %v", replied)
	}

	// 4. Goto stop alias
	b.StartPath(protocol.BlockPos{2, 64, 0}, "")
	b.gotoCmd([]string{"stop"})
	if b.IsPathActive() {
		t.Errorf("expected goto stop to stop path")
	}

	// 5. Goto player offline
	replied = nil
	b.gotoCmd([]string{"Notch"})
	if len(replied) == 0 || !strings.Contains(replied[0], "not found") {
		t.Errorf("expected not found message for offline player, got %v", replied)
	}

	// 6. Goto online player
	b.players["steve"] = player{
		name:        "Steve",
		id:          12345,
		online:      true,
		hasLocation: true,
		position:    mgl32.Vec3{3, 64, 0},
	}
	replied = nil
	b.gotoCmd([]string{"Steve"})
	time.Sleep(30 * time.Millisecond)
	if !b.IsPathActive() {
		t.Errorf("expected path to Steve to be active")
	}
	b.StopPath()
}

// TestMultiChunkSegmentProgression verifies that when a partial path reaches
// the edge of loaded chunks, the follower automatically plans the next segment.
func TestMultiChunkSegmentProgression(t *testing.T) {
	cache := NewChunkCache()
	cache.Enable(protocol.SubChunkPos{0, 4, 0})

	var replied []string
	b := &Bot{
		pos:             mgl32.Vec3{0, 64, 0},
		chunkCache:      cache,
		blockRegistry:   NewBlockRegistry(),
		blockRuntimeIDs: make(map[protocol.BlockPos]uint32),
		players:         make(map[string]player),
		testReplyHook: func(msg string) {
			replied = append(replied, msg)
		},
	}
	b.pathPlanner = pathfind.NewPlanner()

	// Only populate up to X=3 (X=4 is unloaded/solid)
	for x := int32(0); x <= 3; x++ {
		cache.OnBlockUpdate(protocol.BlockPos{x, 63, 0}, 1)
		cache.OnBlockUpdate(protocol.BlockPos{x, 64, 0}, 0)
		cache.OnBlockUpdate(protocol.BlockPos{x, 65, 0}, 0)
	}

	// Final destination is far away at X=10
	b.StartPath(protocol.BlockPos{10, 64, 0}, "")
	if !b.IsPathActive() {
		t.Fatal("expected path to be active for segment 1")
	}

	// Bot walks to node at X=3 (edge of currently loaded terrain)
	b.mu.Lock()
	b.pathIndex = len(b.pathNodes) - 1
	lastWP := b.pathNodes[b.pathIndex]
	b.pos = mgl32.Vec3{float32(lastWP.X()) + 0.5, float32(lastWP.Y()), float32(lastWP.Z()) + 0.5}
	b.mu.Unlock()

	// Now simulate new chunks loading ahead: populate X=4..X=6
	for x := int32(4); x <= 6; x++ {
		cache.OnBlockUpdate(protocol.BlockPos{x, 63, 0}, 1)
		cache.OnBlockUpdate(protocol.BlockPos{x, 64, 0}, 0)
		cache.OnBlockUpdate(protocol.BlockPos{x, 65, 0}, 0)
	}

	// Tick follower: should detect end of segment 1 and plan segment 2 towards X=10!
	_, msg := b.pathFollowTick(nil)

	if !strings.Contains(msg, "Continuing towards") {
		t.Errorf("expected 'Continuing towards' multi-segment message, got %q", msg)
	}
	if !b.IsPathActive() {
		t.Errorf("expected path to stay active for segment 2")
	}

	b.StopPath()
}
