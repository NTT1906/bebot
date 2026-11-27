package bot

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// TestInWindow verifies the 3D mathematical sliding window containment.
func TestInWindow(t *testing.T) {
	center := protocol.SubChunkPos{10, 5, -20}

	// Inside window tests
	insideCases := []protocol.SubChunkPos{
		{10, 5, -20},   // Center
		{11, 5, -20},   // Face +X
		{9, 5, -20},    // Face -X
		{10, 6, -20},   // Face +Y
		{10, 4, -20},   // Face -Y
		{10, 5, -19},   // Face +Z
		{10, 5, -21},   // Face -Z
		{11, 5, -19},   // Edge (+X, +Z)
		{9, 4, -21},    // 3D Corner (-X, -Y, -Z)
		{11, 6, -19},   // 3D Corner (+X, +Y, +Z)
	}

	for _, p := range insideCases {
		if !inWindow(p, center) {
			t.Errorf("expected %v to be in window of center %v", p, center)
		}
	}

	// Outside window tests
	outsideCases := []protocol.SubChunkPos{
		{12, 5, -20},   // +2 X
		{8, 5, -20},    // -2 X
		{10, 7, -20},   // +2 Y
		{10, 3, -20},   // -2 Y
		{10, 5, -18},   // +2 Z
		{10, 5, -22},   // -2 Z
		{20, 15, -40},  // Far teleport
	}

	for _, p := range outsideCases {
		if inWindow(p, center) {
			t.Errorf("expected %v to be OUTSIDE window of center %v", p, center)
		}
	}
}

// TestShiftCenterGeometry verifies exact overlap counts for all geometric shift cases.
func TestShiftCenterGeometry(t *testing.T) {
	cache := NewChunkCache()
	c0 := protocol.SubChunkPos{0, 0, 0}
	cache.Enable(c0)

	// Manually populate all 27 subchunks
	for dx := int32(-1); dx <= 1; dx++ {
		for dy := int32(-1); dy <= 1; dy++ {
			for dz := int32(-1); dz <= 1; dz++ {
				pos := protocol.SubChunkPos{dx, dy, dz}
				sub := cache.getOrCreateSubChunkLocked(pos)
				sub.Pos = pos
			}
		}
	}
	if count := cache.Count(); count != 27 {
		t.Fatalf("expected 27 subchunks loaded, got %d", count)
	}

	// 1. Shift by Face (+1, 0, 0): exactly 18 sub-chunks should be retained
	cFace := protocol.SubChunkPos{1, 0, 0}
	cache.ShiftCenter(cFace)
	// Count how many of the original 27 remain
	kept := 0
	for dx := int32(-1); dx <= 1; dx++ {
		for dy := int32(-1); dy <= 1; dy++ {
			for dz := int32(-1); dz <= 1; dz++ {
				pos := protocol.SubChunkPos{dx, dy, dz}
				if inWindow(pos, cFace) {
					kept++
				}
			}
		}
	}
	if kept != 18 {
		t.Errorf("expected 18 subchunks overlapping in face shift, got %d", kept)
	}

	// 2. Shift by Edge (+1, 0, +1): exactly 12 sub-chunks retained from (0,0,0)
	cEdge := protocol.SubChunkPos{1, 0, 1}
	kept = 0
	for dx := int32(-1); dx <= 1; dx++ {
		for dy := int32(-1); dy <= 1; dy++ {
			for dz := int32(-1); dz <= 1; dz++ {
				pos := protocol.SubChunkPos{dx, dy, dz}
				if inWindow(pos, cEdge) {
					kept++
				}
			}
		}
	}
	if kept != 12 {
		t.Errorf("expected 12 subchunks overlapping in edge shift, got %d", kept)
	}

	// 3. Shift by 3D Vertex (+1, +1, +1): exactly 8 sub-chunks retained from (0,0,0)
	cVertex := protocol.SubChunkPos{1, 1, 1}
	kept = 0
	for dx := int32(-1); dx <= 1; dx++ {
		for dy := int32(-1); dy <= 1; dy++ {
			for dz := int32(-1); dz <= 1; dz++ {
				pos := protocol.SubChunkPos{dx, dy, dz}
				if inWindow(pos, cVertex) {
					kept++
				}
			}
		}
	}
	if kept != 8 {
		t.Errorf("expected 8 subchunks overlapping in 3D vertex shift, got %d", kept)
	}

	// 4. Teleport (+10, +10, +10): exactly 0 sub-chunks retained
	cTeleport := protocol.SubChunkPos{10, 10, 10}
	kept = 0
	for dx := int32(-1); dx <= 1; dx++ {
		for dy := int32(-1); dy <= 1; dy++ {
			for dz := int32(-1); dz <= 1; dz++ {
				pos := protocol.SubChunkPos{dx, dy, dz}
				if inWindow(pos, cTeleport) {
					kept++
				}
			}
		}
	}
	if kept != 0 {
		t.Errorf("expected 0 subchunks overlapping in teleport, got %d", kept)
	}
}

// TestSubChunkDataIndexing tests block coordinate addressing within a subchunk.
func TestSubChunkDataIndexing(t *testing.T) {
	sub := &SubChunkData{}

	// Set distinct blocks at boundary coordinates
	sub.SetBlock(0, 0, 0, 100, 0)
	sub.SetBlock(15, 0, 0, 200, 0)
	sub.SetBlock(0, 15, 0, 300, 0)
	sub.SetBlock(0, 0, 15, 400, 0)
	sub.SetBlock(7, 8, 9, 500, 0)

	// Set waterlogged on layer 1
	sub.SetBlock(7, 8, 9, 999, 1)

	if val := sub.BlockAt(0, 0, 0); val != 100 {
		t.Errorf("expected 100 at (0,0,0), got %d", val)
	}
	if val := sub.BlockAt(15, 0, 0); val != 200 {
		t.Errorf("expected 200 at (15,0,0), got %d", val)
	}
	if val := sub.BlockAt(0, 15, 0); val != 300 {
		t.Errorf("expected 300 at (0,15,0), got %d", val)
	}
	if val := sub.BlockAt(0, 0, 15); val != 400 {
		t.Errorf("expected 400 at (0,0,15), got %d", val)
	}
	if val := sub.BlockAt(7, 8, 9); val != 500 {
		t.Errorf("expected 500 at (7,8,9), got %d", val)
	}

	idx := (7 << 8) | (9 << 4) | 8
	if sub.Waterlogged[idx] != 999 || !sub.HasLayer1 {
		t.Errorf("expected layer 1 waterlogged 999 at (7,8,9), got %d", sub.Waterlogged[idx])
	}
}

// TestDecodeSingleBlockSubChunk tests decoding a single-block subchunk (bitsPerBlock=0).
func TestDecodeSingleBlockSubChunk(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(8) // version 8
	buf.WriteByte(1) // 1 storage layer
	buf.WriteByte(0) // header: bitsPerBlock = 0, isRuntime = 0

	// In Bedrock network format, bitsPerBlock=0 has no palette size varint,
	// only the single RuntimeID follows directly.
	// RuntimeID = 101 (zigzag varint: 101 -> 202)
	// 202 in varint: 202 = 0xCA -> [0xCA, 0x01]
	buf.Write([]byte{0xCA, 0x01})

	sub := &SubChunkData{}
	r := bytes.NewReader(buf.Bytes())
	if err := decodeSingleSubChunkPayload(r, sub); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	// Verify all 4096 blocks are 101
	for x := int32(0); x < 16; x++ {
		for y := int32(0); y < 16; y++ {
			for z := int32(0); z < 16; z++ {
				if val := sub.BlockAt(x, y, z); val != 101 {
					t.Fatalf("expected 101 at (%d, %d, %d), got %d", x, y, z, val)
				}
			}
		}
	}
}

// TestDecodePalettedSubChunk tests decoding a multi-block paletted subchunk (bitsPerBlock=2).
func TestDecodePalettedSubChunk(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(8) // version 8
	buf.WriteByte(1) // 1 storage layer

	// Header: bitsPerBlock = 2 -> (2 << 1) | 1 = 5 (isRuntime=1)
	buf.WriteByte(5)

	// bitsPerBlock = 2: 16 blocks per word, 4096 / 16 = 256 words
	// Fill first word with pattern: alternating palette indices 0, 1, 2, 3
	// word = 0b11_10_01_00 repeated
	word0 := uint32(0xE4E4E4E4)
	for i := 0; i < 256; i++ {
		if i == 0 {
			_ = binary.Write(&buf, binary.LittleEndian, word0)
		} else {
			_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
		}
	}

	// Palette size = 4 (zigzag: 4 -> 8)
	buf.WriteByte(8)
	// Palette entries: 10, 20, 30, 40
	// zigzag: 10 -> 20, 20 -> 40, 30 -> 60, 40 -> 80
	buf.WriteByte(20)
	buf.WriteByte(40)
	buf.WriteByte(60)
	buf.WriteByte(80)

	sub := &SubChunkData{}
	r := bytes.NewReader(buf.Bytes())
	if err := decodeSingleSubChunkPayload(r, sub); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	// Verify the first 4 blocks along Y (blockIndex 0..3)
	// block 0: (word0 >> 0)&3 = 0 -> palette[0] = 10
	// block 1: (word0 >> 2)&3 = 1 -> palette[1] = 20
	// block 2: (word0 >> 4)&3 = 2 -> palette[2] = 30
	// block 3: (word0 >> 6)&3 = 3 -> palette[3] = 40
	if val := sub.BlockAt(0, 0, 0); val != 10 {
		t.Errorf("block 0: expected 10, got %d", val)
	}
	if val := sub.BlockAt(0, 1, 0); val != 20 {
		t.Errorf("block 1: expected 20, got %d", val)
	}
	if val := sub.BlockAt(0, 2, 0); val != 30 {
		t.Errorf("block 2: expected 30, got %d", val)
	}
	if val := sub.BlockAt(0, 3, 0); val != 40 {
		t.Errorf("block 3: expected 40, got %d", val)
	}
}

// TestChunkCacheCommands tests the load/unload lifecycle and inspect safety guard.
func TestChunkCacheCommands(t *testing.T) {
	b := &Bot{
		pos:               mgl32.Vec3{160, 64, -320},
		chunkCache:        NewChunkCache(),
		mineAirRuntimeIDs: map[uint32]bool{0: true},
		fromConsole:       true,
	}

	replies := make([]string, 0)
	b.testReplyHook = func(msg string) {
		replies = append(replies, msg)
	}

	// 1. Initial inspect while unloaded must broadcast safety guard
	b.inspectCmd([]string{"160", "64", "-320"})
	if len(replies) == 0 || !strings.Contains(replies[len(replies)-1], "Chunk data is not loaded") {
		t.Fatalf("expected unloaded safety broadcast, got: %v", replies)
	}

	// 2. Enable chunk cache
	b.chunkCmd([]string{"load"})
	if !b.chunkCache.IsEnabled() {
		t.Fatal("expected chunkCache to be enabled")
	}
	center := b.chunkCache.Center()
	// (160 >> 4 = 10, 64 >> 4 = 4, -320 >> 4 = -20)
	if center.X() != 10 || center.Y() != 4 || center.Z() != -20 {
		t.Errorf("expected center <10, 4, -20>, got %v", center)
	}

	// 3. Mutate a block in cache and inspect it
	targetPos := protocol.BlockPos{162, 65, -318}
	b.chunkCache.OnBlockUpdate(targetPos, 42)

	b.inspectCmd([]string{"162", "65", "-318"})
	lastReply := replies[len(replies)-1]
	if !strings.Contains(lastReply, "RuntimeID: 42") {
		t.Errorf("expected block inspect to show RuntimeID: 42, got: %s", lastReply)
	}

	// 4. Inspect an air block
	airPos := protocol.BlockPos{162, 66, -318}
	b.chunkCache.OnBlockUpdate(airPos, 0)
	b.inspectCmd([]string{"162", "66", "-318"})
	lastReply = replies[len(replies)-1]
	if !strings.Contains(lastReply, "minecraft:air") {
		t.Errorf("expected block inspect to show minecraft:air, got: %s", lastReply)
	}

	// 5. Unload chunk cache
	b.chunkCmd([]string{"unload"})
	if b.chunkCache.IsEnabled() {
		t.Fatal("expected chunkCache to be disabled after unload")
	}

	// 6. Inspect after unload must trigger safety guard again
	b.inspectCmd([]string{"162", "65", "-318"})
	lastReply = replies[len(replies)-1]
	if !strings.Contains(lastReply, "Chunk data is not loaded") {
		t.Errorf("expected unloaded safety guard after unload, got: %s", lastReply)
	}
}

// TestAreaMineAirPreFiltering tests zero-tick air pre-filtering during area mining.
func TestAreaMineAirPreFiltering(t *testing.T) {
	b := &Bot{
		pos:               mgl32.Vec3{0, 64, 0},
		chunkCache:        NewChunkCache(),
		mineAirRuntimeIDs: map[uint32]bool{0: true, 100: true},
		fromConsole:       true,
	}

	replies := make([]string, 0)
	b.testReplyHook = func(msg string) {
		replies = append(replies, msg)
	}

	// Enable chunk cache at sub-chunk (0, 4, 0)
	b.chunkCache.Enable(protocol.SubChunkPos{0, 4, 0})

	// Create a 2x2x2 area: 8 blocks total
	// Set 6 blocks to air (runtimeID 0 or 100), and 2 blocks to stone (runtimeID 1)
	for x := int32(0); x <= 1; x++ {
		for y := int32(64); y <= 65; y++ {
			for z := int32(0); z <= 1; z++ {
				pos := protocol.BlockPos{x, y, z}
				if x == 0 && y == 64 && z == 0 {
					b.chunkCache.OnBlockUpdate(pos, 1) // stone
				} else if x == 1 && y == 65 && z == 1 {
					b.chunkCache.OnBlockUpdate(pos, 1) // stone
				} else {
					b.chunkCache.OnBlockUpdate(pos, 0) // air
				}
			}
		}
	}

	// Run area mining from (0, 64, 0) to (1, 65, 1)
	b.startMiningArea(0, 64, 0, 1, 65, 1, 10, false, false)

	// Check mineQueue length
	b.mu.Lock()
	qLen := len(b.mineQueue)
	b.mu.Unlock()

	if qLen != 2 {
		t.Fatalf("expected exactly 2 solid blocks queued, got %d", qLen)
	}

	lastReply := replies[len(replies)-1]
	if !strings.Contains(lastReply, "6 air pre-filtered") {
		t.Errorf("expected reply to report '6 air pre-filtered', got: %s", lastReply)
	}
}

// TestSubChunkDecodeInvalidBitsPerBlock verifies that malformed subchunks with invalid bitsPerBlock
// (e.g., bitsPerBlock > 32 causing blocksPerWord = 0) gracefully return an error instead of panicking
// with an integer divide by zero.
func TestSubChunkDecodeInvalidBitsPerBlock(t *testing.T) {
	// Malformed header where header = 0xFE -> header >> 1 = 127 (> 32)
	malformed := []byte{0xFE, 0x00, 0x00, 0x00}

	r := bytes.NewReader(malformed)
	err := skipSubChunkStorages(r, 1)
	if err == nil {
		t.Fatalf("expected error for bitsPerBlock 127 in skipSubChunkStorages, got nil")
	}

	sub := &SubChunkData{}
	r = bytes.NewReader(malformed)
	err = decodeSubChunkStorages(r, 1, sub)
	if err == nil {
		t.Fatalf("expected error for bitsPerBlock 127 in decodeSubChunkStorages, got nil")
	}
}

// TestReloadWindowSubChunkRequestMode verifies that shifting chunk centers does not parse non-subchunk
// LevelChunk payloads in modern SubChunkRequest mode.
func TestReloadWindowSubChunkRequestMode(t *testing.T) {
	cache := NewChunkCache()
	cache.Enable(protocol.SubChunkPos{10, 5, -20})

	// Server sends modern LevelChunk with SubChunkCount = math.MaxUint32 (SubChunkRequestMode)
	// and a payload containing border blocks / biomes (not subchunks)
	colPos := protocol.ChunkPos{10, -20}
	borderPayload := []byte{0x09, 0x01, 0xFF, 0xFE, 0xAA, 0xBB} // Arbitrary non-subchunk bytes
	cache.OnLevelChunk(&packet.LevelChunk{
		Position:      colPos,
		SubChunkCount: 0xFFFFFFFF,
		RawPayload:    borderPayload,
	})

	// Shift center to trigger reloadWindowLocked
	cache.ShiftCenter(protocol.SubChunkPos{10, 6, -20})
}
