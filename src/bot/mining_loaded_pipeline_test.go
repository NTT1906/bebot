package bot

import (
	"strings"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

func newLoadedPipelineTestBot() *Bot {
	reg := NewBlockRegistry()
	reg.SetActualAirRID(0)
	reg.Register(0, "minecraft:air")
	reg.Register(1, "minecraft:stone")
	reg.Register(2, "minecraft:water")
	reg.Register(3, "minecraft:flowing_water")
	reg.Register(4, "minecraft:lava")
	reg.Register(5, "minecraft:flowing_lava")
	reg.Register(6, "minecraft:bedrock")
	reg.Register(7, "minecraft:dirt")
	reg.Register(8, "minecraft:barrier")
	reg.Register(9, "minecraft:reinforced_deepslate")
	reg.loaded = true

	cache := NewChunkCache()
	cache.Enable(protocol.SubChunkPos{0, 4, 0})

	b := &Bot{
		blockRegistry:     reg,
		chunkCache:        cache,
		mineAirRuntimeIDs: map[uint32]bool{0: true},
		knownAirBlocks:    make(map[protocol.BlockPos]bool),
		unmineableBlocks:  make(map[protocol.BlockPos]bool),
		mineAttempts:      make(map[protocol.BlockPos]int),
		blockRuntimeIDs:   make(map[protocol.BlockPos]uint32),
		gameMode:          0, // Survival mode default
	}
	return b
}

// TestMiningLoadedPipelineSurvivalMode verifies that when both chunk and palette data are loaded in Survival mode:
// air, water, lava, flowing water, flowing lava, and unbreakable blocks (bedrock, barrier, reinforced_deepslate)
// are automatically excluded from the mining queue.
func TestMiningLoadedPipelineSurvivalMode(t *testing.T) {
	b := newLoadedPipelineTestBot()
	var replies []string
	b.testReplyHook = func(msg string) {
		replies = append(replies, msg)
	}

	// 1x8 strip from X=0..7 at Y=64, Z=0
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{0, 64, 0}, 1) // stone (mineable)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{1, 64, 0}, 0) // air (skipped)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{2, 64, 0}, 2) // water (skipped)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{3, 64, 0}, 3) // flowing_water (skipped)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{4, 64, 0}, 4) // lava (skipped)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{5, 64, 0}, 5) // flowing_lava (skipped)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{6, 64, 0}, 6) // bedrock (unbreakable, skipped in survival)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{7, 64, 0}, 7) // dirt (mineable)

	b.startMiningArea(0, 64, 0, 7, 64, 0, 10, false, false)

	if len(b.mineQueue) != 2 {
		t.Fatalf("expected exactly 2 mineable blocks in survival (stone & dirt), got %d: %v", len(b.mineQueue), b.mineQueue)
	}

	expected := []protocol.BlockPos{{0, 64, 0}, {7, 64, 0}}
	for i, exp := range expected {
		if b.mineQueue[i] != exp {
			t.Errorf("expected queue[%d] = %v, got %v", i, exp, b.mineQueue[i])
		}
	}

	if len(replies) == 0 || !strings.Contains(replies[len(replies)-1], "6 pre-filtered") {
		t.Errorf("expected reply reporting 6 pre-filtered blocks, got %v", replies)
	}
}

// TestMiningLoadedPipelineCreativeMode verifies that in Creative mode:
// air, water, and lava are skipped, BUT bedrock and unbreakable blocks ARE included in the mining queue.
func TestMiningLoadedPipelineCreativeMode(t *testing.T) {
	b := newLoadedPipelineTestBot()
	b.gameMode = 1 // Creative mode
	var replies []string
	b.testReplyHook = func(msg string) {
		replies = append(replies, msg)
	}

	// 1x8 strip from X=0..7 at Y=64, Z=0
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{0, 64, 0}, 1) // stone (mineable)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{1, 64, 0}, 0) // air (skipped)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{2, 64, 0}, 2) // water (skipped)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{3, 64, 0}, 3) // flowing_water (skipped)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{4, 64, 0}, 4) // lava (skipped)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{5, 64, 0}, 5) // flowing_lava (skipped)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{6, 64, 0}, 6) // bedrock (mineable in creative!)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{7, 64, 0}, 7) // dirt (mineable)

	b.startMiningArea(0, 64, 0, 7, 64, 0, 10, false, false)

	// In Creative, bedrock is NOT skipped -> stone (0), bedrock (6), dirt (7) = 3 blocks
	if len(b.mineQueue) != 3 {
		t.Fatalf("expected 3 blocks in creative (stone, bedrock, dirt), got %d: %v", len(b.mineQueue), b.mineQueue)
	}

	expected := []protocol.BlockPos{{0, 64, 0}, {6, 64, 0}, {7, 64, 0}}
	for i, exp := range expected {
		if b.mineQueue[i] != exp {
			t.Errorf("expected queue[%d] = %v, got %v", i, exp, b.mineQueue[i])
		}
	}

	if len(replies) == 0 || !strings.Contains(replies[len(replies)-1], "5 pre-filtered") {
		t.Errorf("expected reply reporting 5 pre-filtered blocks, got %v", replies)
	}
}

// TestMiningFallbackPipelineWhenPaletteUnloaded verifies that if palette data is UNLOADED,
// the bot falls back to the existing mining pipeline unchanged (only air is pre-filtered, bedrock/fluids queued).
func TestMiningFallbackPipelineWhenPaletteUnloaded(t *testing.T) {
	b := newLoadedPipelineTestBot()
	b.blockRegistry = NewBlockRegistry() // Empty/unloaded registry!
	var replies []string
	b.testReplyHook = func(msg string) {
		replies = append(replies, msg)
	}

	// 1x4 strip from X=0..3 at Y=64, Z=0
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{0, 64, 0}, 1) // stone
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{1, 64, 0}, 0) // air (RID 0)
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{2, 64, 0}, 2) // water
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{3, 64, 0}, 6) // bedrock

	b.startMiningArea(0, 64, 0, 3, 64, 0, 10, false, false)

	// In fallback pipeline, without palette to identify water & bedrock, only RID 0 air is filtered
	// stone (0), water (2), bedrock (3) = 3 blocks queued
	if len(b.mineQueue) != 3 {
		t.Fatalf("expected 3 blocks in fallback pipeline, got %d: %v", len(b.mineQueue), b.mineQueue)
	}

	if len(replies) == 0 || !strings.Contains(replies[len(replies)-1], "1 air pre-filtered") {
		t.Errorf("expected fallback reply reporting '1 air pre-filtered', got %v", replies)
	}
}

// TestMiningFallbackPipelineWhenChunkCacheDisabled verifies that if chunkCache is DISABLED,
// the bot falls back to the existing mining pipeline unchanged (all blocks queued).
func TestMiningFallbackPipelineWhenChunkCacheDisabled(t *testing.T) {
	b := newLoadedPipelineTestBot()
	b.chunkCache.Disable()
	var replies []string
	b.testReplyHook = func(msg string) {
		replies = append(replies, msg)
	}

	b.startMiningArea(0, 64, 0, 3, 64, 0, 10, false, false)

	// When chunkCache is disabled, no blocks can be pre-filtered -> all 4 queued
	if len(b.mineQueue) != 4 {
		t.Fatalf("expected 4 blocks in fallback pipeline with chunkCache disabled, got %d", len(b.mineQueue))
	}
}

// TestPickNextMineBlockDynamicFiltering verifies that pickNextMineBlock skips fluid/unbreakable blocks
// on the fly when chunk and palette are loaded.
func TestPickNextMineBlockDynamicFiltering(t *testing.T) {
	b := newLoadedPipelineTestBot()
	b.gameMode = 0 // Survival

	// Pre-populate queue with 3 blocks
	b.mineQueue = []protocol.BlockPos{
		{0, 64, 0}, // water (should be dynamically skipped)
		{1, 64, 0}, // bedrock (should be dynamically skipped in survival)
		{2, 64, 0}, // stone (should be returned)
	}

	b.chunkCache.OnBlockUpdate(protocol.BlockPos{0, 64, 0}, 2) // water
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{1, 64, 0}, 6) // bedrock
	b.chunkCache.OnBlockUpdate(protocol.BlockPos{2, 64, 0}, 1) // stone

	target := b.pickNextMineBlock()
	if target == nil {
		t.Fatal("expected target, got nil")
	}
	if *target != (protocol.BlockPos{2, 64, 0}) {
		t.Errorf("expected target stone at {2, 64, 0}, got %v", *target)
	}
}

// TestRepeatingAreaMiningPrefilledAndRegeneratedBlocks tests that newly placed or regenerated blocks
// (such as a cobblestone generator or chests) inside the mine box are queued and picked up for mining
// without being learned as air or discarded by stale cache.
func TestRepeatingAreaMiningPrefilledAndRegeneratedBlocks(t *testing.T) {
	b := newLoadedPipelineTestBot()
	b.gameMode = 0 // Survival
	b.mineBoxMin = protocol.BlockPos{254, 63, -224}
	b.mineBoxMax = protocol.BlockPos{258, 67, -220}
	b.mineActive = true
	b.mineRepeat = true
	b.mineWaitingForBlocks = true
	b.mineQueue = []protocol.BlockPos{}

	chestPos := protocol.BlockPos{255, 64, -223}
	chestRID := uint32(1741778478)
	b.blockRegistry.Register(chestRID, "minecraft:chest")

	// Simulate break finish followed by cobblestone / chest appearance at the same coordinate
	b.mineLastFinishedPos = chestPos
	b.mineLastFinishedTick = b.tick
	b.mineLastWasBroken = true
	b.mineBreakInitialKnown = true
	b.mineBreakInitialRuntimeID = 1 // broke stone (RID 1)

	b.handleUpdateBlock(chestPos, chestRID)

	// Verify chest was NOT learned as air
	if b.mineAirRuntimeIDs[chestRID] {
		t.Fatalf("chest runtime ID %d was learned as air!", chestRID)
	}

	// Verify chest was NOT marked unmineable
	if b.unmineableBlocks[chestPos] {
		t.Fatalf("chest at %v was incorrectly marked as unmineable", chestPos)
	}

	// Verify chest was added to mineQueue
	if len(b.mineQueue) != 1 || b.mineQueue[0] != chestPos {
		t.Fatalf("expected chest %v to be added to mineQueue, got %v", chestPos, b.mineQueue)
	}

	// Verify pickNextMineBlock successfully picks up chest
	target := b.pickNextMineBlock()
	if target == nil || *target != chestPos {
		t.Fatalf("expected pickNextMineBlock to return %v, got %v", chestPos, target)
	}
}

// TestRestartMiningQueueWithRefilledBlocks tests that restartMiningQueue properly re-evaluates
// refilled solid blocks in the mine box even if they were previously mined into air.
func TestRestartMiningQueueWithRefilledBlocks(t *testing.T) {
	b := newLoadedPipelineTestBot()
	b.gameMode = 0
	b.mineBoxMin = protocol.BlockPos{0, 64, 0}
	b.mineBoxMax = protocol.BlockPos{1, 64, 0} // 2 blocks total
	b.mineActive = true
	b.mineRepeat = true

	// Both blocks were previously mined and recorded as known air
	b.knownAirBlocks[protocol.BlockPos{0, 64, 0}] = true
	b.knownAirBlocks[protocol.BlockPos{1, 64, 0}] = true
	b.mineAttempts[protocol.BlockPos{0, 64, 0}] = 2
	b.mineAttempts[protocol.BlockPos{1, 64, 0}] = 2

	// Now coordinate {0, 64, 0} refilled with stone (RID 1), while {1, 64, 0} remains air (RID 0)
	b.blockRuntimeIDs[protocol.BlockPos{0, 64, 0}] = 1
	b.blockRuntimeIDs[protocol.BlockPos{1, 64, 0}] = 0

	b.mu.Lock()
	b.restartMiningQueue()
	b.mu.Unlock()

	// Only {0, 64, 0} (refilled stone) should be queued; air {1, 64, 0} filtered
	if len(b.mineQueue) != 1 {
		t.Fatalf("expected 1 block queued after restart, got %d: %v", len(b.mineQueue), b.mineQueue)
	}
	if b.mineQueue[0] != (protocol.BlockPos{0, 64, 0}) {
		t.Fatalf("expected queued block to be {0, 64, 0}, got %v", b.mineQueue[0])
	}
}
