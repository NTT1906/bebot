package bot

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// chunkCmd handles "bebot chunk [load|unload|status]".
func (b *Bot) chunkCmd(args []string) {
	if len(args) == 0 {
		b.chunkStatus()
		return
	}

	action := strings.ToLower(args[0])
	switch action {
	case "load", "enable", "on", "start":
		b.mu.Lock()
		pos := b.pos
		b.mu.Unlock()

		subPos := protocol.SubChunkPos{int32(pos.X()) >> 4, int32(pos.Y()) >> 4, int32(pos.Z()) >> 4}
		b.chunkCache.Enable(subPos)

		missing := b.chunkCache.MissingOffsets()
		if len(missing) > 0 {
			b.requestSubChunks(subPos, missing)
		}

		count := b.chunkCache.Count()
		if len(missing) > 0 {
			b.reply(fmt.Sprintf("[ChunkCache] Loaded 3x3x3 sub-chunk window (%d/27 cached, requesting %d from server around <%d, %d, %d>).",
				count, len(missing), subPos.X(), subPos.Y(), subPos.Z()))
		} else {
			b.reply(fmt.Sprintf("[ChunkCache] Loaded 3x3x3 sub-chunk window (%d/27 active around sub-chunk <%d, %d, %d>).",
				count, subPos.X(), subPos.Y(), subPos.Z()))
		}

	case "unload", "disable", "off", "clear", "stop":
		b.chunkCache.Disable()
		b.reply("[ChunkCache] Unloaded chunk data. Memory freed.")

	case "status", "info":
		b.chunkStatus()

	default:
		b.reply("Usage: chunk load | unload | status")
	}
}

func (b *Bot) chunkStatus() {
	if !b.chunkCache.IsEnabled() {
		b.reply("[ChunkCache] Status: UNLOADED. Run 'bebot chunk load' to enable.")
		return
	}

	center := b.chunkCache.Center()
	count := b.chunkCache.Count()
	b.reply(fmt.Sprintf("[ChunkCache] Status: LOADED (%d/27 sub-chunks active, center: <%d, %d, %d>).",
		count, center.X(), center.Y(), center.Z()))
}

// inspectCmd handles "bebot inspect <x> <y> <z>" (or "bebot probe <x> <y> <z>").
func (b *Bot) inspectCmd(args []string) {
	if !b.chunkCache.IsEnabled() {
		b.reply("[ChunkCache] Chunk data is not loaded. Run 'bebot chunk load' first.")
		return
	}

	if len(args) < 3 {
		b.reply("Usage: inspect <x> <y> <z>")
		return
	}

	x, errX := strconv.ParseInt(args[0], 10, 32)
	y, errY := strconv.ParseInt(args[1], 10, 32)
	z, errZ := strconv.ParseInt(args[2], 10, 32)
	if errX != nil || errY != nil || errZ != nil {
		b.reply("Invalid coordinates. Usage: inspect <x> <y> <z>")
		return
	}

	target := protocol.BlockPos{int32(x), int32(y), int32(z)}
	rid, found := b.chunkCache.BlockAt(target)
	if !found {
		center := b.chunkCache.Center()
		b.reply(fmt.Sprintf("Block at (%d, %d, %d) is outside loaded 3x3x3 window (center: <%d, %d, %d>).",
			target.X(), target.Y(), target.Z(), center.X(), center.Y(), center.Z()))
		return
	}

	b.mu.Lock()
	isAir := rid == 0 || (b.mineAirRuntimeIDs != nil && b.mineAirRuntimeIDs[rid])
	b.mu.Unlock()

	blockDesc := fmt.Sprintf("RuntimeID: %d", rid)
	if isAir {
		blockDesc = fmt.Sprintf("minecraft:air (RuntimeID: %d)", rid)
	}

	b.reply(fmt.Sprintf("Block at (%d, %d, %d): %s", target.X(), target.Y(), target.Z(), blockDesc))
}
