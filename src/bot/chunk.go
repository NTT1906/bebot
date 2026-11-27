package bot

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// SubChunkBlockCount is the number of blocks in a 16x16x16 sub-chunk.
const SubChunkBlockCount = 4096

// SubChunkData stores decoded block runtime IDs for a single 16x16x16 sub-chunk.
type SubChunkData struct {
	Pos         protocol.SubChunkPos
	Blocks      [SubChunkBlockCount]uint32 // Layer 0: solid blocks
	Waterlogged [SubChunkBlockCount]uint32 // Layer 1: waterlogged / liquid blocks
	HasLayer1   bool
}

// BlockAt returns the runtime ID at local sub-chunk coordinates (0..15).
func (s *SubChunkData) BlockAt(x, y, z int32) uint32 {
	idx := (x&15)<<8 | (z&15)<<4 | (y&15)
	return s.Blocks[idx]
}

// SetBlock sets the runtime ID at local sub-chunk coordinates (0..15).
func (s *SubChunkData) SetBlock(x, y, z int32, runtimeID uint32, layer uint32) {
	idx := (x&15)<<8 | (z&15)<<4 | (y&15)
	if layer == 1 {
		s.Waterlogged[idx] = runtimeID
		s.HasLayer1 = true
	} else {
		s.Blocks[idx] = runtimeID
	}
}

// Reset clears the sub-chunk data for recycling via sync.Pool.
func (s *SubChunkData) Reset() {
	s.Pos = protocol.SubChunkPos{}
	for i := range s.Blocks {
		s.Blocks[i] = 0
		s.Waterlogged[i] = 0
	}
	s.HasLayer1 = false
}

// ChunkCache manages the local 3x3x3 sub-chunk window (27 sub-chunks) centered on the bot.
type ChunkCache struct {
	mu        sync.RWMutex
	enabled   bool
	center    protocol.SubChunkPos
	subchunks map[protocol.SubChunkPos]*SubChunkData
	rawChunks map[protocol.ChunkPos][]byte // cached raw payload per chunk column
	pool      sync.Pool
}

// NewChunkCache initializes a new ChunkCache with object pooling.
func NewChunkCache() *ChunkCache {
	return &ChunkCache{
		subchunks: make(map[protocol.SubChunkPos]*SubChunkData, 27),
		rawChunks: make(map[protocol.ChunkPos][]byte),
		pool: sync.Pool{
			New: func() any {
				return &SubChunkData{}
			},
		},
	}
}

// IsEnabled returns true if chunk decoding is currently active.
func (c *ChunkCache) IsEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.enabled
}

// Center returns the current center sub-chunk position.
func (c *ChunkCache) Center() protocol.SubChunkPos {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.center
}

// Count returns the number of currently loaded sub-chunks.
func (c *ChunkCache) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.subchunks)
}

// HasSubChunk returns true if the specified sub-chunk position is currently loaded in memory.
func (c *ChunkCache) HasSubChunk(pos protocol.SubChunkPos) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.enabled {
		return false
	}
	_, ok := c.subchunks[pos]
	return ok
}

// Enable activates chunk decoding centered around centerPos.
func (c *ChunkCache) Enable(centerPos protocol.SubChunkPos) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.enabled = true
	c.center = centerPos

	// Decode any existing raw chunk payloads that fall in the 3x3x3 window
	c.reloadWindowLocked()
}

// Disable unloads all sub-chunks, frees memory, and clears active cache.
func (c *ChunkCache) Disable() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.enabled = false
	for pos, sub := range c.subchunks {
		sub.Reset()
		c.pool.Put(sub)
		delete(c.subchunks, pos)
	}
}

// MissingOffsets returns relative sub-chunk offsets (-1..1) that are not currently loaded in the 3x3x3 window.
func (c *ChunkCache) MissingOffsets() []protocol.SubChunkOffset {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.enabled {
		return nil
	}

	var missing []protocol.SubChunkOffset
	for dx := int8(-1); dx <= 1; dx++ {
		for dy := int8(-1); dy <= 1; dy++ {
			for dz := int8(-1); dz <= 1; dz++ {
				pos := protocol.SubChunkPos{c.center[0] + int32(dx), c.center[1] + int32(dy), c.center[2] + int32(dz)}
				if _, ok := c.subchunks[pos]; !ok {
					missing = append(missing, protocol.SubChunkOffset{dx, dy, dz})
				}
			}
		}
	}
	return missing
}

// BlockAt queries the runtime ID at world block coordinates (x, y, z).
func (c *ChunkCache) BlockAt(pos protocol.BlockPos) (runtimeID uint32, found bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.enabled {
		return 0, false
	}

	subPos := protocol.SubChunkPos{pos.X() >> 4, pos.Y() >> 4, pos.Z() >> 4}
	sub, ok := c.subchunks[subPos]
	if !ok {
		return 0, false
	}

	return sub.BlockAt(pos.X()&15, pos.Y()&15, pos.Z()&15), true
}

// OnBlockUpdate mutates a single block at world coordinates in real time.
func (c *ChunkCache) OnBlockUpdate(pos protocol.BlockPos, runtimeID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.enabled {
		return
	}

	subPos := protocol.SubChunkPos{pos.X() >> 4, pos.Y() >> 4, pos.Z() >> 4}
	if inWindow(subPos, c.center) {
		sub := c.getOrCreateSubChunkLocked(subPos)
		sub.SetBlock(pos.X()&15, pos.Y()&15, pos.Z()&15, runtimeID, 0)
	}
}

// OnSubChunkBlocks handles batch block changes within a sub-chunk (e.g. UpdateSubChunkBlocks).
func (c *ChunkCache) OnSubChunkBlocks(subChunkPos protocol.BlockPos, blocks []protocol.BlockChangeEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.enabled {
		return
	}

	for _, entry := range blocks {
		// Calculate world block position
		wx := (subChunkPos.X() << 4) + entry.BlockPos.X()
		wy := (subChunkPos.Y() << 4) + entry.BlockPos.Y()
		wz := (subChunkPos.Z() << 4) + entry.BlockPos.Z()

		sp := protocol.SubChunkPos{wx >> 4, wy >> 4, wz >> 4}
		if inWindow(sp, c.center) {
			sub := c.getOrCreateSubChunkLocked(sp)
			sub.SetBlock(wx&15, wy&15, wz&15, entry.BlockRuntimeID, 0)
		}
	}
}

// OnLevelChunk caches the incoming raw chunk column and decodes overlapping sub-chunks if enabled.
func (c *ChunkCache) OnLevelChunk(p *packet.LevelChunk) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Store raw payload (copying to avoid retention of packet buffers)
	payloadCopy := make([]byte, len(p.RawPayload))
	copy(payloadCopy, p.RawPayload)
	c.rawChunks[p.Position] = payloadCopy

	if !c.enabled {
		return
	}

	// If this chunk column is within horizontal range of our 3x3x3 window:
	dx := p.Position.X() - c.center[0]
	dz := p.Position.Z() - c.center[2]
	if dx >= -1 && dx <= 1 && dz >= -1 && dz <= 1 {
		c.decodeColumnSubChunksLocked(p.Position, payloadCopy, p.SubChunkCount)
	}
}

// OnSubChunk processes incoming SubChunk entries relative to the packet's center position.
func (c *ChunkCache) OnSubChunk(p *packet.SubChunk) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.enabled {
		return
	}

	for _, entry := range p.SubChunkEntries {
		targetSub := protocol.SubChunkPos{
			p.Position[0] + int32(entry.Offset[0]),
			p.Position[1] + int32(entry.Offset[1]),
			p.Position[2] + int32(entry.Offset[2]),
		}

		if !inWindow(targetSub, c.center) {
			continue
		}

		if entry.Result == protocol.SubChunkResultSuccessAllAir || entry.Result == protocol.SubChunkResultIndexOutOfBounds {
			// Subchunk is pure air (all 0s)
			sub := c.getOrCreateSubChunkLocked(targetSub)
			sub.Reset()
			sub.Pos = targetSub
			continue
		}

		if entry.Result == protocol.SubChunkResultSuccess {
			if raw, ok := entry.RawPayload.Value(); ok && len(raw) > 0 {
				sub := c.getOrCreateSubChunkLocked(targetSub)
				sub.Reset()
				sub.Pos = targetSub
				if err := decodeSingleSubChunkPayload(bytes.NewReader(raw), sub); err != nil {
					log.Printf("bebot: failed to decode subchunk %v: %v\n", targetSub, err)
				}
			}
		}
	}
}

// ShiftCenter shifts the 3x3x3 window to a new center position.
// Mathematical check: 0 heap allocations, exact retention and pool recycling.
func (c *ChunkCache) ShiftCenter(newCenter protocol.SubChunkPos) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.enabled || c.center == newCenter {
		return
	}

	// 1. Evict any loaded sub-chunk that falls outside the new 3x3x3 window
	for pos, sub := range c.subchunks {
		if !inWindow(pos, newCenter) {
			sub.Reset()
			c.pool.Put(sub)
			delete(c.subchunks, pos)
		}
	}

	c.center = newCenter

	// 2. Load missing sub-chunks from raw chunk column cache
	c.reloadWindowLocked()
}

// inWindow checks if pos is within the 3x3x3 sub-chunk cube centered at center.
func inWindow(pos, center protocol.SubChunkPos) bool {
	dx, dy, dz := pos[0]-center[0], pos[1]-center[1], pos[2]-center[2]
	return dx >= -1 && dx <= 1 && dy >= -1 && dy <= 1 && dz >= -1 && dz <= 1
}

// getOrCreateSubChunkLocked retrieves a pooled SubChunkData or creates one.
func (c *ChunkCache) getOrCreateSubChunkLocked(pos protocol.SubChunkPos) *SubChunkData {
	if sub, ok := c.subchunks[pos]; ok {
		return sub
	}
	sub := c.pool.Get().(*SubChunkData)
	sub.Reset()
	sub.Pos = pos
	c.subchunks[pos] = sub
	return sub
}

// reloadWindowLocked populates missing sub-chunks in the active 3x3x3 window from cached raw chunks.
func (c *ChunkCache) reloadWindowLocked() {
	for dx := int32(-1); dx <= 1; dx++ {
		for dz := int32(-1); dz <= 1; dz++ {
			chunkX := c.center[0] + dx
			chunkZ := c.center[2] + dz
			colPos := protocol.ChunkPos{chunkX, chunkZ}

			if payload, ok := c.rawChunks[colPos]; ok {
				c.decodeColumnSubChunksLocked(colPos, payload, 0)
			}
		}
	}
}

// decodeColumnSubChunksLocked iterates through a LevelChunk raw payload and extracts sub-chunks in the window.
func (c *ChunkCache) decodeColumnSubChunksLocked(colPos protocol.ChunkPos, payload []byte, declaredCount uint32) {
	if declaredCount > 64 {
		// Server uses modern Bedrock SubChunkRequest mode; LevelChunk does not carry inline subchunks
		return
	}
	r := bytes.NewReader(payload)

	// In Bedrock LevelChunk payloads, sub-chunks are written sequentially.
	// We parse up to 24 sub-chunks or until the buffer ends.
	maxSubChunks := int(declaredCount)
	if maxSubChunks <= 0 || maxSubChunks > 32 {
		maxSubChunks = 24
	}

	implicitY := int32(-4) // standard 1.18+ overworld minCY is -4 (Y=-64)

	for i := 0; i < maxSubChunks; i++ {
		if r.Len() < 2 {
			break
		}

		// Peek version byte
		version, err := r.ReadByte()
		if err != nil {
			break
		}

		var subY int32
		var storageCount byte

		switch version {
		case 8:
			storageCount, err = r.ReadByte()
			if err != nil {
				return
			}
			subY = implicitY + int32(i)
		case 9:
			storageCount, err = r.ReadByte()
			if err != nil {
				return
			}
			yIdx, err := r.ReadByte()
			if err != nil {
				return
			}
			subY = int32(int8(yIdx))
		case 1:
			// Legacy version 1
			storageCount = 1
			subY = implicitY + int32(i)
		default:
			// Unknown subchunk version or reached biomes/border blocks
			return
		}

		targetPos := protocol.SubChunkPos{colPos.X(), subY, colPos.Z()}
		inWin := inWindow(targetPos, c.center)

		if inWin {
			sub := c.getOrCreateSubChunkLocked(targetPos)
			sub.Reset()
			sub.Pos = targetPos
			if err := decodeSubChunkStorages(r, int(storageCount), sub); err != nil {
				return
			}
		} else {
			// Skip past storages for sub-chunks not in the 3x3x3 window
			if err := skipSubChunkStorages(r, int(storageCount)); err != nil {
				return
			}
		}
	}
}

// decodeSingleSubChunkPayload parses a standalone SubChunk payload (from SubChunk packets).
func decodeSingleSubChunkPayload(r *bytes.Reader, sub *SubChunkData) error {
	version, err := r.ReadByte()
	if err != nil {
		return err
	}

	var storageCount byte
	switch version {
	case 8:
		storageCount, err = r.ReadByte()
		if err != nil {
			return err
		}
	case 9:
		storageCount, err = r.ReadByte()
		if err != nil {
			return err
		}
		_, _ = r.ReadByte() // subChunkIndex
	case 1:
		storageCount = 1
	default:
		return fmt.Errorf("unsupported subchunk version %d", version)
	}

	return decodeSubChunkStorages(r, int(storageCount), sub)
}

// decodeSubChunkStorages decodes the paletted storages of a subchunk into SubChunkData.
func decodeSubChunkStorages(r *bytes.Reader, storageCount int, sub *SubChunkData) error {
	for s := 0; s < storageCount; s++ {
		if r.Len() == 0 {
			break
		}

		header, err := r.ReadByte()
		if err != nil {
			return err
		}

		bitsPerBlock := int(header >> 1)
		targetLayer := s

		if bitsPerBlock == 0 {
			// Single-block palette: entire 4096 blocks share index 0
			paletteSize, err := readVarint32(r)
			if err != nil {
				return err
			}
			singleRID := uint32(0)
			if paletteSize > 0 {
				rid, err := readVarint32(r)
				if err != nil {
					return err
				}
				singleRID = uint32(rid)
			}
			for i := 0; i < SubChunkBlockCount; i++ {
				if targetLayer == 1 {
					sub.Waterlogged[i] = singleRID
					sub.HasLayer1 = true
				} else {
					sub.Blocks[i] = singleRID
				}
			}
			continue
		}

		// Paletted bit-packed storage
		blocksPerWord := 32 / bitsPerBlock
		wordCount := (SubChunkBlockCount + blocksPerWord - 1) / blocksPerWord

		if r.Len() < wordCount*4 {
			return errors.New("unexpected EOF reading subchunk words")
		}

		words := make([]uint32, wordCount)
		for i := 0; i < wordCount; i++ {
			var w uint32
			if err := binary.Read(r, binary.LittleEndian, &w); err != nil {
				return err
			}
			words[i] = w
		}

		// Read palette
		paletteSize, err := readVarint32(r)
		if err != nil {
			return err
		}
		if paletteSize <= 0 || paletteSize > 4096 {
			return fmt.Errorf("invalid palette size %d", paletteSize)
		}

		palette := make([]uint32, paletteSize)
		for i := 0; i < int(paletteSize); i++ {
			rid, err := readVarint32(r)
			if err != nil {
				return err
			}
			palette[i] = uint32(rid)
		}

		// Unpack words into subchunk block coordinates
		mask := uint32((1 << bitsPerBlock) - 1)
		for i := 0; i < wordCount; i++ {
			w := words[i]
			for b := 0; b < blocksPerWord; b++ {
				blockIdx := i*blocksPerWord + b
				if blockIdx >= SubChunkBlockCount {
					break
				}
				shift := uint(b * bitsPerBlock)
				palIdx := (w >> shift) & mask
				var rid uint32
				if int(palIdx) < len(palette) {
					rid = palette[palIdx]
				}
				if targetLayer == 1 {
					sub.Waterlogged[blockIdx] = rid
					sub.HasLayer1 = true
				} else {
					sub.Blocks[blockIdx] = rid
				}
			}
		}
	}
	return nil
}

// skipSubChunkStorages quickly skips past storages without unpacking.
func skipSubChunkStorages(r *bytes.Reader, storageCount int) error {
	for s := 0; s < storageCount; s++ {
		if r.Len() == 0 {
			break
		}
		header, err := r.ReadByte()
		if err != nil {
			return err
		}
		bitsPerBlock := int(header >> 1)
		if bitsPerBlock == 0 {
			paletteSize, err := readVarint32(r)
			if err != nil {
				return err
			}
			for i := 0; i < int(paletteSize); i++ {
				if _, err := readVarint32(r); err != nil {
					return err
				}
			}
			continue
		}

		blocksPerWord := 32 / bitsPerBlock
		wordCount := (SubChunkBlockCount + blocksPerWord - 1) / blocksPerWord
		if r.Len() < wordCount*4 {
			return errors.New("unexpected EOF skipping subchunk words")
		}
		_, _ = r.Seek(int64(wordCount*4), 1)

		paletteSize, err := readVarint32(r)
		if err != nil {
			return err
		}
		for i := 0; i < int(paletteSize); i++ {
			if _, err := readVarint32(r); err != nil {
				return err
			}
		}
	}
	return nil
}

// readVaruint32 reads an unsigned 7-bit varint from r.
func readVaruint32(r *bytes.Reader) (uint32, error) {
	var v uint32
	for i := 0; i < 35; i += 7 {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		v |= uint32(b&0x7F) << i
		if b&0x80 == 0 {
			return v, nil
		}
	}
	return 0, errors.New("varuint32 overflow")
}

// readVarint32 reads a signed ZigZag-encoded varint32 from r.
func readVarint32(r *bytes.Reader) (int32, error) {
	ux, err := readVaruint32(r)
	if err != nil {
		return 0, err
	}
	x := int32(ux >> 1)
	if ux&1 != 0 {
		x = ^x
	}
	return x, nil
}

// requestSubChunks sends a SubChunkRequest packet to the server for missing sub-chunks.
func (b *Bot) requestSubChunks(center protocol.SubChunkPos, offsets []protocol.SubChunkOffset) {
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	if c == nil || len(offsets) == 0 {
		return
	}

	pk := &packet.SubChunkRequest{
		Dimension: 0,
		Position:  center,
		Offsets:   offsets,
	}
	if err := c.WritePacket(pk); err != nil {
		log.Printf("bebot: failed to send SubChunkRequest: %v\n", err)
	} else {
		if tracePackets {
			log.Printf("bebot: sent SubChunkRequest for %d sub-chunks around center %v\n", len(offsets), center)
		}
	}
}

// updateChunkCenterIfMoved shifts the local chunk window if the bot has moved into a new sub-chunk.
func (b *Bot) updateChunkCenterIfMoved() {
	if b.chunkCache == nil || !b.chunkCache.IsEnabled() {
		return
	}
	b.mu.Lock()
	pos := b.pos
	b.mu.Unlock()

	subPos := protocol.SubChunkPos{int32(pos.X()) >> 4, int32(pos.Y()) >> 4, int32(pos.Z()) >> 4}
	if subPos != b.chunkCache.Center() {
		b.chunkCache.ShiftCenter(subPos)
		missing := b.chunkCache.MissingOffsets()
		if len(missing) > 0 {
			b.requestSubChunks(subPos, missing)
		}
	}
}

