package bot

import (
	"strings"
	"testing"
	"time"

	"bebot/src/config"
	"bebot/src/pathfind"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// newLoadedBlockRegistry builds a registry that satisfies the palette
// prerequisite for pathfinding, without loading a full palette from disk.
func newLoadedBlockRegistry() *BlockRegistry {
	reg := NewBlockRegistry()
	reg.loaded = true
	return reg
}

func newTestPathBot() *Bot {
	reg := newLoadedBlockRegistry()
	reg.SetActualAirRID(0)
	reg.Register(1, "minecraft:stone")
	b := &Bot{
		blockRegistry:   reg,
		chunkCache:      NewChunkCache(),
		blockRuntimeIDs: make(map[protocol.BlockPos]uint32),
		players:         make(map[string]player),
	}
	b.pathPlanner = pathfind.NewPlanner()
	return b
}

// TestBotWorldView tests the spatial block type classification in BotWorldView.
func TestBotWorldView(t *testing.T) {
	reg := newLoadedBlockRegistry()
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
		blockRegistry:   newLoadedBlockRegistry(),
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
		blockRegistry:   newLoadedBlockRegistry(),
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
		blockRegistry:   newLoadedBlockRegistry(),
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
		blockRegistry:   newLoadedBlockRegistry(),
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

// TestPathArrivalRequires3DProximity verifies that arriving at a destination requires
// proximity in all 3 axes (X, Y, and Z). A horizontal match with vertical elevation mismatch
// must NOT falsely trigger "Destination reached".
func TestPathArrivalRequires3DProximity(t *testing.T) {
	cache := NewChunkCache()
	cache.Enable(protocol.SubChunkPos{0, 4, 0})

	var replied []string
	b := &Bot{
		pos:             mgl32.Vec3{0, 64, 0},
		chunkCache:      cache,
		blockRegistry:   newLoadedBlockRegistry(),
		blockRuntimeIDs: make(map[protocol.BlockPos]uint32),
		players:         make(map[string]player),
		testReplyHook: func(msg string) {
			replied = append(replied, msg)
		},
	}
	b.pathPlanner = pathfind.NewPlanner()

	// Floor at Y=63
	for x := int32(0); x <= 5; x++ {
		cache.OnBlockUpdate(protocol.BlockPos{x, 63, 0}, 1)
		cache.OnBlockUpdate(protocol.BlockPos{x, 64, 0}, 0)
		cache.OnBlockUpdate(protocol.BlockPos{x, 65, 0}, 0)
		cache.OnBlockUpdate(protocol.BlockPos{x, 66, 0}, 0)
		cache.OnBlockUpdate(protocol.BlockPos{x, 67, 0}, 0)
	}

	// Target is 3 blocks higher in elevation: (3, 67, 0)
	b.StartPath(protocol.BlockPos{3, 67, 0}, "")
	if !b.IsPathActive() {
		t.Fatal("expected path to be active")
	}

	// 1. Bot reaches X=3, Z=0 horizontally, but remains at Y=64 (vertical difference = 3 blocks)
	b.mu.Lock()
	b.pathIndex = len(b.pathNodes)
	b.pos = mgl32.Vec3{3.5, 64.0, 0.5}
	_, msg := b.onSegmentCompleteLocked()

	if strings.Contains(msg, "Destination reached") {
		t.Fatalf("did NOT expect 'Destination reached' when vertical distance is 3 blocks off! got: %q", msg)
	}
	if !strings.Contains(msg, "Cannot progress further") && !strings.Contains(msg, "blocked") {
		t.Errorf("expected unreachable elevation message, got: %q", msg)
	}

	// 2. Start path where 3D target is reached in X, Y, and Z: (3, 64, 0)
	b.mu.Lock()
	b.pos = mgl32.Vec3{0.5, 64.0, 0.5}
	b.mu.Unlock()
	b.StartPath(protocol.BlockPos{3, 64, 0}, "")
	b.mu.Lock()
	b.pathIndex = len(b.pathNodes)
	b.pos = mgl32.Vec3{3.5, 64.0, 0.5}
	_, msg = b.onSegmentCompleteLocked()

	if !strings.Contains(msg, "Destination reached") {
		t.Errorf("expected 'Destination reached' when bot arrived in X, Y, and Z, got: %q", msg)
	}
}

// TestDynamicPlayerTracking3D verifies that when a tracked target player changes elevation (Y),
// dynamic tracking detects the 3D movement and replans.
func TestDynamicPlayerTracking3D(t *testing.T) {
	cache := NewChunkCache()
	cache.Enable(protocol.SubChunkPos{0, 4, 0})

	b := &Bot{
		pos:             mgl32.Vec3{0, 64, 0},
		chunkCache:      cache,
		blockRegistry:   newLoadedBlockRegistry(),
		blockRuntimeIDs: make(map[protocol.BlockPos]uint32),
		players:         make(map[string]player),
	}
	b.pathPlanner = pathfind.NewPlanner()

	// Multi-level terrain
	for x := int32(0); x <= 5; x++ {
		cache.OnBlockUpdate(protocol.BlockPos{x, 63, 0}, 1)
		cache.OnBlockUpdate(protocol.BlockPos{x, 64, 0}, 0)
		cache.OnBlockUpdate(protocol.BlockPos{x, 65, 0}, 0)
		cache.OnBlockUpdate(protocol.BlockPos{x, 66, 0}, 0)
		cache.OnBlockUpdate(protocol.BlockPos{x, 67, 0}, 0)
	}

	b.players["steve"] = player{
		name:        "Steve",
		id:          12345,
		online:      true,
		hasLocation: true,
		position:    mgl32.Vec3{3, 64, 0},
	}

	b.StartPath(protocol.BlockPos{3, 64, 0}, "Steve")
	if !b.IsPathActive() {
		t.Fatal("expected path to be active")
	}

	// Steve changes elevation in 3D: moves from Y=64 to Y=67 with zero horizontal movement
	b.mu.Lock()
	b.players["steve"] = player{
		name:        "Steve",
		id:          12345,
		online:      true,
		hasLocation: true,
		position:    mgl32.Vec3{3, 67, 0},
	}
	b.mu.Unlock()

	// Follow tick should detect vertical movement (dy >= 2) and replan targetPos to Y=67
	b.pathFollowTick(nil)

	b.mu.Lock()
	targetY := b.pathTargetPos.Y()
	b.mu.Unlock()

	if targetY != 67 {
		t.Errorf("expected pathTargetPos.Y to be updated to 67 after 3D player movement, got %d", targetY)
	}
}

// TestReplanTimeoutAndGiveUp verifies that:
// 1. Failing to reach the next node in 50 ticks triggers re-pathfinding.
// 2. Failing to make progress after multiple consecutive replans gives up and broadcasts.
func TestReplanTimeoutAndGiveUp(t *testing.T) {
	cache := NewChunkCache()
	cache.Enable(protocol.SubChunkPos{0, 4, 0})

	var replied []string
	b := &Bot{
		pos:             mgl32.Vec3{0, 64, 0},
		chunkCache:      cache,
		blockRegistry:   newLoadedBlockRegistry(),
		blockRuntimeIDs: make(map[protocol.BlockPos]uint32),
		players:         make(map[string]player),
		testReplyHook: func(msg string) {
			replied = append(replied, msg)
		},
	}
	b.pathPlanner = pathfind.NewPlanner()

	// Flat ground
	for x := int32(0); x <= 10; x++ {
		cache.OnBlockUpdate(protocol.BlockPos{x, 63, 0}, 1)
		cache.OnBlockUpdate(protocol.BlockPos{x, 64, 0}, 0)
		cache.OnBlockUpdate(protocol.BlockPos{x, 65, 0}, 0)
	}

	b.StartPath(protocol.BlockPos{5, 64, 0}, "")
	if !b.IsPathActive() {
		t.Fatal("expected path to be active")
	}

	// 1. Test position stalled timeout (25 ticks)
	for i := 0; i < 24; i++ {
		b.pathFollowTick(nil)
	}
	b.mu.Lock()
	count := b.pathReplanCount
	b.mu.Unlock()
	if count != 0 {
		t.Fatalf("did not expect replan before 25 ticks of stall, got %d", count)
	}

	// 25th tick triggers replan attempt 1
	_, msg := b.pathFollowTick(nil)
	if !strings.Contains(msg, "attempt 1/3") {
		t.Errorf("expected attempt 1/3 replan message, got %q", msg)
	}

	// Trigger attempt 2
	for i := 0; i < 25; i++ {
		b.pathFollowTick(nil)
	}
	b.mu.Lock()
	count = b.pathReplanCount
	b.mu.Unlock()
	if count != 2 {
		t.Errorf("expected replan count 2, got %d", count)
	}

	// Trigger attempt 3
	for i := 0; i < 25; i++ {
		b.pathFollowTick(nil)
	}

	// Next failure after 3 attempts must give up and broadcast
	var finalMsg string
	for i := 0; i < 25; i++ {
		_, m := b.pathFollowTick(nil)
		if m != "" {
			finalMsg = m
		}
	}

	if !strings.Contains(finalMsg, "giving up") && !strings.Contains(finalMsg, "multiple replan attempts") {
		t.Errorf("expected give up broadcast message, got %q", finalMsg)
	}
	if !strings.Contains(finalMsg, "Feet debug map") {
		t.Errorf("expected debug map in final give up message, got %q", finalMsg)
	}
	if !strings.Contains(finalMsg, "=") {
		t.Errorf("expected debug map to contain bot '=', got %q", finalMsg)
	}
	if b.IsPathActive() {
		t.Errorf("expected path to be deactivated after giving up")
	}
}

// TestReplanCountDoesNotResetOnRetreat verifies that retreating backward or failing to approach the target
// does not wipe the consecutive replan counter, preventing oscillation loops.
func TestReplanCountDoesNotResetOnRetreat(t *testing.T) {
	b := newTestPathBot()
	cache := b.chunkCache
	cache.Enable(protocol.SubChunkPos{0, 4, 0})

	// Flat platform Y=63
	for x := int32(-10); x <= 15; x++ {
		for z := int32(-5); z <= 5; z++ {
			cache.OnBlockUpdate(protocol.BlockPos{x, 63, z}, 1)
			cache.OnBlockUpdate(protocol.BlockPos{x, 64, z}, 0)
			cache.OnBlockUpdate(protocol.BlockPos{x, 65, z}, 0)
		}
	}

	b.mu.Lock()
	b.pos = mgl32.Vec3{5.5, 64, 0.5}
	b.pathActive = true
	b.pathTargetPos = protocol.BlockPos{10, 64, 0}
	b.pathTargetMinDist = 5.0 // Closest reached so far is 5.0m
	b.pathReplanCount = 2     // Already had 2 failed attempts
	// 3-node path: retreat node at x=4, then x=3, so reaching x=4 doesn't trigger segment complete
	b.pathNodes = []protocol.BlockPos{{5, 64, 0}, {4, 64, 0}, {3, 64, 0}}
	b.pathIndex = 1
	b.mu.Unlock()

	// Bot reaches retreat waypoint at x=4.5 (distance to 10 is ~5.5m >= 5.0m - 0.5m)
	b.mu.Lock()
	b.pos = mgl32.Vec3{4.5, 64, 0.5}
	b.mu.Unlock()

	b.pathFollowTick(nil)

	b.mu.Lock()
	count := b.pathReplanCount
	b.mu.Unlock()

	if count != 2 {
		t.Errorf("expected replanCount to remain 2 when retreating away from target, got %d", count)
	}

	// Now bot advances to forward node at x=7.5 (distance to 10 is ~2.5m < 5.0m - 0.5m)
	b.mu.Lock()
	b.pathNodes = []protocol.BlockPos{{4, 64, 0}, {7, 64, 0}}
	b.pathIndex = 1
	b.pos = mgl32.Vec3{7.5, 64, 0.5}
	b.mu.Unlock()

	b.pathFollowTick(nil)

	b.mu.Lock()
	count = b.pathReplanCount
	b.mu.Unlock()

	if count != 0 {
		t.Errorf("expected replanCount to reset to 0 upon genuine forward progress towards target, got %d", count)
	}
}

// TestStepUpJumpDetection verifies that approaching a +1 Y elevation transition triggers jump=true.
func TestStepUpJumpDetection(t *testing.T) {
	b := newTestPathBot()
	cache := b.chunkCache
	cache.Enable(protocol.SubChunkPos{0, 4, 0})

	// Floor at Y=63, step up at Y=64
	for x := int32(0); x <= 5; x++ {
		cache.OnBlockUpdate(protocol.BlockPos{x, 63, 0}, 1)
	}
	cache.OnBlockUpdate(protocol.BlockPos{3, 64, 0}, 1) // step block

	b.mu.Lock()
	b.pos = mgl32.Vec3{2.5, 64, 0.5} // Standing at feet Y=64, floor Y=63
	b.pathActive = true
	b.pathTargetPos = protocol.BlockPos{3, 65, 0} // Target is on top of step
	b.pathNodes = []protocol.BlockPos{{2, 64, 0}, {3, 65, 0}}
	b.pathMoves = []pathfind.Move{
		{From: protocol.BlockPos{2, 64, 0}, To: protocol.BlockPos{3, 65, 0}, Type: pathfind.MoveStepUp, Cost: 2.0},
	}
	b.pathIndex = 1
	b.mu.Unlock()

	jump, _ := b.pathFollowTick(nil)
	if !jump {
		t.Errorf("expected jump to be true when approaching a step up waypoint")
	}
}

// TestFormatFeetDebugMap verifies the 9x9 ASCII grid generated for feet-level diagnostics.
func TestFormatFeetDebugMap(t *testing.T) {
	b := newTestPathBot()
	cache := b.chunkCache
	cache.Enable(protocol.SubChunkPos{0, 4, 0})

	// Setup: bot at (5, 64, 5), target at (7, 64, 5)
	// Floor at Y=63
	// Surrounding blocks at Y=64: air everywhere except a solid wall at (5, 64, 6)
	cache.OnBlockUpdate(protocol.BlockPos{5, 64, 6}, 1) // solid wall
	cache.OnBlockUpdate(protocol.BlockPos{7, 64, 5}, 0) // air at target

	botFeet := protocol.BlockPos{5, 64, 5}
	target := protocol.BlockPos{7, 64, 5}
	wv := NewBotWorldView(b)

	out := formatFeetDebugMap(wv, botFeet, target)

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 10 { // 1 header line + 9 grid rows
		t.Fatalf("expected 10 lines (header + 9 rows), got %d: %q", len(lines), out)
	}

	header := lines[0]
	if !strings.Contains(header, "Y=64") || !strings.Contains(header, "<5,5>") {
		t.Errorf("expected header to contain Y=64 and center <5,5>, got %q", header)
	}

	// Verify grid rows (lines 1 to 9)
	for i := 1; i <= 9; i++ {
		row := lines[i]
		if len(row) != 9 {
			t.Errorf("row %d length: got %d, want 9 (%q)", i, len(row), row)
		}
	}

	// Center row (dz = 0) is line 5 (index 5, since index 0 is header, 1 is dz=-4, 2 is dz=-3, 3 is dz=-2, 4 is dz=-1, 5 is dz=0)
	centerRow := lines[5]
	// Center character (dx = 0) is col 4 (index 4)
	if centerRow[4] != '=' {
		t.Errorf("expected center character at dz=0, dx=0 to be '=', got %c in row %q", centerRow[4], centerRow)
	}
	// Target at dx = +2 (col index 4 + 2 = 6)
	if centerRow[6] != '+' {
		t.Errorf("expected target character at dz=0, dx=2 to be '+', got %c in row %q", centerRow[6], centerRow)
	}

	// Wall at (5, 64, 6) -> dx = 0, dz = +1. Row dz = +1 is index 6. Col dx = 0 is index 4.
	wallRow := lines[6]
	if wallRow[4] != 'x' {
		t.Errorf("expected wall block at dz=+1, dx=0 to be 'x', got %c in row %q", wallRow[4], wallRow)
	}
}

func TestStartPathReportsAllMissingPrerequisites(t *testing.T) {
	var replies []string
	b := &Bot{
		pos:             mgl32.Vec3{0, 64, 0},
		blockRegistry:   NewBlockRegistry(),
		chunkCache:      NewChunkCache(),
		blockRuntimeIDs: make(map[protocol.BlockPos]uint32),
		testReplyHook: func(msg string) {
			replies = append(replies, msg)
		},
	}
	b.pathPlanner = pathfind.NewPlanner()

	b.StartPath(protocol.BlockPos{3, 64, 0}, "")

	joined := strings.Join(replies, "\n")
	if !strings.Contains(joined, "palette load") {
		t.Errorf("expected palette warning, got %q", joined)
	}
	if !strings.Contains(joined, "chunk load") {
		t.Errorf("expected chunk warning, got %q", joined)
	}
	if b.chunkCache.IsEnabled() {
		t.Error("goto must not auto-enable the chunk cache when auto-load is off")
	}
	if b.IsPathActive() {
		t.Error("path should not start without the required data")
	}
}

func TestStartPathReportsOnlyMissingPalette(t *testing.T) {
	var replies []string
	cache := NewChunkCache()
	cache.Enable(protocol.SubChunkPos{0, 4, 0})
	b := &Bot{
		pos:             mgl32.Vec3{0, 64, 0},
		blockRegistry:   NewBlockRegistry(),
		chunkCache:      cache,
		blockRuntimeIDs: make(map[protocol.BlockPos]uint32),
		testReplyHook: func(msg string) {
			replies = append(replies, msg)
		},
	}
	b.pathPlanner = pathfind.NewPlanner()

	b.StartPath(protocol.BlockPos{3, 64, 0}, "")

	joined := strings.Join(replies, "\n")
	if !strings.Contains(joined, "palette load") {
		t.Errorf("expected palette warning, got %q", joined)
	}
	if strings.Contains(joined, "chunk load") {
		t.Errorf("chunks are already loaded; unexpected warning: %q", joined)
	}
}

func TestPathSearchOptionsFromConfig(t *testing.T) {
	var cfg config.Config
	cfg.Behaviour.Pathfinding.MaxNodes = 900
	cfg.Behaviour.Pathfinding.TimeoutMs = 120
	on := true
	off := false
	cfg.Behaviour.Pathfinding.AllowDiagonals = &on
	cfg.Behaviour.Pathfinding.AllowDrops = &off
	cfg.Behaviour.Pathfinding.Weights.StepUp = 2.5

	opts := pathSearchOptionsFromConfig(cfg)
	if opts.MaxNodes != 900 || opts.Timeout != 120*time.Millisecond {
		t.Errorf("unexpected search limits: %+v", opts)
	}
	if !opts.AllowDiagonals || opts.AllowDrops {
		t.Errorf("unexpected move toggles: %+v", opts)
	}
	if opts.Weights.StepUp != 2.5 || opts.Weights.WalkCardinal != pathfind.CostWalkCardinal {
		t.Errorf("unexpected weights: %+v", opts.Weights)
	}
}
