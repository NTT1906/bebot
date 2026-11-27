package bot

import (
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

func newUnmineableTestBot() *Bot {
	return &Bot{
		fromConsole:          true,
		mineActive:           true,
		tick:                 100,
		mineQueue:            []protocol.BlockPos{},
		mineAttempts:         make(map[protocol.BlockPos]int),
		mineAirRuntimeIDs:    map[uint32]bool{0: true, 42: true},
		knownAirBlocks:       make(map[protocol.BlockPos]bool),
		unmineableBlocks:     make(map[protocol.BlockPos]bool),
		blockRuntimeIDs:      make(map[protocol.BlockPos]uint32),
		mineLastFinishedTick: 100,
	}
}

func TestRecentFinishSolidMarksUnmineable(t *testing.T) {
	b := newUnmineableTestBot()
	pos := protocol.BlockPos{1, 2, 3}

	b.mineLastFinishedPos = pos
	b.mineLastWasBroken = true
	b.mineBreakInitialKnown = true
	b.mineBreakInitialRuntimeID = 99
	b.knownAirBlocks[pos] = true
	b.mineAttempts[pos] = 2

	// Server reports the same solid runtime ID after the predicted break: the
	// break was rejected, so the coordinate belongs to the unmineable blacklist.
	b.handleUpdateBlock(pos, 99)

	if !b.unmineableBlocks[pos] {
		t.Fatalf("expected %v to be marked unmineable", pos)
	}
	if b.knownAirBlocks[pos] {
		t.Fatalf("expected %v to be removed from known air blocks", pos)
	}
	if b.mineAirRuntimeIDs[99] {
		t.Fatalf("solid runtime ID 99 must never be learned as air")
	}
	if b.mineLastFinishedPos != (protocol.BlockPos{}) {
		t.Fatalf("expected recent-finish result to be consumed once")
	}
}

func TestTimedOutBlockMarksUnmineable(t *testing.T) {
	b := newUnmineableTestBot()
	pos := protocol.BlockPos{2, 3, 4}

	b.mineLastFinishedPos = pos
	b.mineLastWasBroken = false // timeout path in move.go
	b.knownAirBlocks[pos] = true

	b.handleUpdateBlock(pos, 99)

	if !b.unmineableBlocks[pos] {
		t.Fatalf("expected timed-out %v to be marked unmineable", pos)
	}
	if b.knownAirBlocks[pos] {
		t.Fatalf("timed-out block must not remain in known air blocks")
	}
	if b.mineAirRuntimeIDs[99] {
		t.Fatalf("timed-out solid runtime ID 99 must never be learned as air")
	}
}

func TestRecentFinishDoesNotLearnAirDuringNormalMining(t *testing.T) {
	b := newUnmineableTestBot()
	pos := protocol.BlockPos{4, 5, 6}

	b.mineLastFinishedPos = pos
	b.mineLastWasBroken = true
	b.mineBreakInitialKnown = true
	b.mineBreakInitialRuntimeID = 99

	// The runtime ID changed away from the pre-break solid state, but dynamic learning
	// during normal mining is disabled so that regenerated blocks or other states
	// never pollute the air registry. Air runtime IDs are managed via defaults or explicit 'mine air add'.
	b.handleUpdateBlock(pos, 123)

	if b.mineAirRuntimeIDs[123] {
		t.Fatalf("runtime ID 123 must not be dynamically learned as air during normal mining")
	}
}

func TestRecentFinishAfterAirDoesNotLearnSolidAsAir(t *testing.T) {
	b := newUnmineableTestBot()
	pos := protocol.BlockPos{10, 11, 12}

	b.mineLastFinishedPos = pos
	b.mineLastWasBroken = true
	b.blockRuntimeIDs[pos] = 42 // the coordinate was air before the finish update

	b.handleUpdateBlock(pos, 99)

	if b.mineAirRuntimeIDs[99] {
		t.Fatalf("a solid runtime ID reported after an air block must not be learned as air")
	}
	if !b.unmineableBlocks[pos] {
		t.Fatalf("expected %v to be marked unmineable", pos)
	}
}

func TestAirUpdateClearsUnmineableBlacklist(t *testing.T) {
	b := newUnmineableTestBot()
	pos := protocol.BlockPos{7, 8, 9}
	b.unmineableBlocks[pos] = true

	b.handleUpdateBlock(pos, 42)

	if b.unmineableBlocks[pos] {
		t.Fatalf("expected air update to clear %v from the unmineable blacklist", pos)
	}
	if !b.knownAirBlocks[pos] {
		t.Fatalf("expected %v to be marked as air", pos)
	}
}

func TestStartMiningAreaOverridesUnmineableBlacklist(t *testing.T) {
	b := newUnmineableTestBot()
	b.chunkCache = NewChunkCache() // disabled: mine must not depend on chunk/palette data
	oldBlocked := protocol.BlockPos{0, 0, 0}
	b.unmineableBlocks[oldBlocked] = true

	b.startMiningArea(0, 0, 0, 1, 0, 0, 0, false, false)

	if len(b.mineQueue) != 2 {
		t.Fatalf("expected a new mine command to queue all 2 blocks, got %d: %v", len(b.mineQueue), b.mineQueue)
	}
	if b.unmineableBlocks[oldBlocked] {
		t.Fatalf("a new mine command must override the previous command's blacklist")
	}
}

func TestStopMiningAreaClearsUnmineableBlacklist(t *testing.T) {
	b := newUnmineableTestBot()
	pos := protocol.BlockPos{0, 0, 0}
	b.unmineableBlocks[pos] = true

	b.stopMiningArea()

	if b.unmineableBlocks[pos] {
		t.Fatalf("stopping the mine command must clear the unmineable blacklist")
	}
}

func TestRestartMiningQueueSkipsUnmineable(t *testing.T) {
	b := newUnmineableTestBot()
	b.mineBoxMin = protocol.BlockPos{0, 0, 0}
	b.mineBoxMax = protocol.BlockPos{1, 0, 0}
	blocked := protocol.BlockPos{0, 0, 0}
	b.unmineableBlocks[blocked] = true

	b.mu.Lock()
	b.restartMiningQueue()
	b.mu.Unlock()

	if len(b.mineQueue) != 1 {
		t.Fatalf("expected 1 queued block, got %d: %v", len(b.mineQueue), b.mineQueue)
	}
	if b.mineQueue[0] == blocked {
		t.Fatalf("unmineable block %v must not be re-queued", blocked)
	}
}

func TestPickNextMineBlockSkipsUnmineable(t *testing.T) {
	b := newUnmineableTestBot()
	blocked := protocol.BlockPos{0, 0, 0}
	next := protocol.BlockPos{1, 0, 0}
	b.mineQueue = []protocol.BlockPos{blocked, next}
	b.unmineableBlocks[blocked] = true

	b.mu.Lock()
	got := b.pickNextMineBlock()
	b.mu.Unlock()

	if got == nil || *got != next {
		t.Fatalf("expected picker to skip unmineable block and return %v, got %v", next, got)
	}
}

func TestUnmineableBlacklistSeparateFromAirRuntimeIDs(t *testing.T) {
	b := &Bot{
		serverUseHashes:   true,
		mineAirRuntimeIDs: map[uint32]bool{0: true, 3690217760: true},
		unmineableBlocks:  map[protocol.BlockPos]bool{{1, 2, 3}: true},
	}

	pos := protocol.BlockPos{1, 2, 3}
	if !b.unmineableBlocks[pos] {
		t.Fatalf("expected position %v to be marked unmineable", pos)
	}
	// Verify air map was unaffected by unmineable blacklist
	if len(b.mineAirRuntimeIDs) != 2 || !b.mineAirRuntimeIDs[0] || !b.mineAirRuntimeIDs[3690217760] {
		t.Fatalf("air runtime IDs corrupted: %v", b.mineAirRuntimeIDs)
	}
}
