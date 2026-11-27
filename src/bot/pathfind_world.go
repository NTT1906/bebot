package bot

import (
	"math"
	"strings"

	"bebot/src/pathfind"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// BotWorldView adapts the bot's chunkCache, blockRuntimeIDs, and blockRegistry
// to the pathfind.WorldView interface.
type BotWorldView struct {
	b *Bot
}

// NewBotWorldView creates a new BotWorldView for the given Bot.
func NewBotWorldView(b *Bot) *BotWorldView {
	return &BotWorldView{b: b}
}

// botEyeOffset returns the vertical offset (~1.62 blocks in Bedrock) if pos includes eye height.
func botEyeOffset(pos mgl32.Vec3) float32 {
	frac := pos.Y() - float32(math.Floor(float64(pos.Y())))
	if frac >= 0.2 {
		return 1.62
	}
	return 0.0
}

// vec3ToBlockPos converts floating-point entity coordinates into world feet block coordinates.
func vec3ToBlockPos(v mgl32.Vec3) protocol.BlockPos {
	feetY := v.Y() - botEyeOffset(v)
	return protocol.BlockPos{
		int32(math.Floor(float64(v.X()))),
		int32(math.Floor(float64(feetY))),
		int32(math.Floor(float64(v.Z()))),
	}
}

// isPassThroughBlock returns true for non-solid decorative or pass-through blocks
// (such as flowers, torches, rails, tallgrass, etc.) that do not impede player movement.
func isPassThroughBlock(name string) bool {
	if strings.Contains(name, "grass_block") {
		return false
	}
	passThroughKeywords := []string{
		"air", "torch", "flower", "tulip", "daisy", "rose", "dandelion", "orchid",
		"allium", "bluet", "poppy", "sunflower", "lilac", "peony", "grass", "fern",
		"sapling", "mushroom", "rail", "redstone_wire", "lever", "button",
		"tripwire", "pressure_plate", "vine", "wheat", "carrots", "potatoes",
		"beetroots", "sugar_cane", "kelp", "seagrass",
	}
	for _, kw := range passThroughKeywords {
		if strings.Contains(name, kw) {
			return true
		}
	}
	return false
}

// GetBlockType resolves the spatial navigation BlockType for a world coordinate.
func (w *BotWorldView) GetBlockType(pos protocol.BlockPos) pathfind.BlockType {
	if w.b == nil {
		return pathfind.BlockAir
	}

	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	return w.b.getBlockTypeLocked(pos)
}

// getBlockTypeLocked resolves the spatial navigation BlockType when w.b.mu is already held.
func (b *Bot) getBlockTypeLocked(pos protocol.BlockPos) pathfind.BlockType {
	// 1. Check recent single block updates (highest precedence)
	var rid uint32
	var found bool
	if b.blockRuntimeIDs != nil {
		rid, found = b.blockRuntimeIDs[pos]
	}

	// 2. Check chunk cache (3x3x3 sub-chunk window)
	if !found && b.chunkCache != nil && b.chunkCache.IsEnabled() {
		rid, found = b.chunkCache.BlockAt(pos)
	}

	// 3. If unmapped or chunk not loaded
	if !found {
		// Treat the void below bedrock as non-solid air
		if pos.Y() < -64 {
			return pathfind.BlockAir
		}
		// Treat unmapped blocks as solid obstacles to prevent falling into unloaded voids
		return pathfind.BlockSolid
	}

	// 4. Quick check for universally recognized 0 (unmapped / standard air)
	if rid == 0 {
		return pathfind.BlockAir
	}

	// 5. If blockRegistry is available, resolve block name and characteristics (highest semantic truth).
	// Prevents misconfigured or corrupted air runtime IDs from treating solid blocks (like oak logs) as air.
	if b.blockRegistry != nil {
		if name, ok := b.blockRegistry.ResolveName(rid); ok {
			lower := strings.ToLower(name)

			if strings.Contains(lower, "air") {
				return pathfind.BlockAir
			}
			if strings.Contains(lower, "water") {
				return pathfind.BlockLiquidWater
			}
			if strings.Contains(lower, "lava") {
				return pathfind.BlockLiquidLava
			}
			if strings.Contains(lower, "fire") || strings.Contains(lower, "cactus") ||
				strings.Contains(lower, "sweet_berry") || strings.Contains(lower, "magma") ||
				strings.Contains(lower, "wither_rose") {
				return pathfind.BlockHazard
			}
			if isPassThroughBlock(lower) {
				return pathfind.BlockAir
			}

			return pathfind.BlockSolid
		}
	}

	// 6. Check for Air by Runtime ID fallback (when palette is unloaded or RID is unknown to palette)
	airRID := b.actualAirRIDLocked()
	if rid == airRID || (b.mineAirRuntimeIDs != nil && b.mineAirRuntimeIDs[rid]) {
		return pathfind.BlockAir
	}

	// Default fallback: any non-air block is solid
	return pathfind.BlockSolid
}

// FindStandingNode determines the nearest valid standing node around pos.
func (w *BotWorldView) FindStandingNode(pos protocol.BlockPos) protocol.BlockPos {
	// 1. Check pos directly (feet at pos, floor at pos.Y - 1)
	if w.isStandingNode(pos) {
		return pos
	}
	// 2. Check pos.Y + 1 (e.g. standing on slab or path block)
	up := protocol.BlockPos{pos.X(), pos.Y() + 1, pos.Z()}
	if w.isStandingNode(up) {
		return up
	}
	// 3. Check pos.Y - 1 (e.g. standing slightly below integer bound)
	down := protocol.BlockPos{pos.X(), pos.Y() - 1, pos.Z()}
	if w.isStandingNode(down) {
		return down
	}
	return pos
}

// FindGoalNode resolves a valid standing goal node for the given target coordinate.
func (w *BotWorldView) FindGoalNode(target protocol.BlockPos) protocol.BlockPos {
	// If the target block is solid, standing on top of it is the intended goal
	targetType := w.GetBlockType(target)
	if targetType == pathfind.BlockSolid {
		onTop := protocol.BlockPos{target.X(), target.Y() + 1, target.Z()}
		if w.isStandingNode(onTop) {
			return onTop
		}
	}
	// If target is already a valid standing node
	if w.isStandingNode(target) {
		return target
	}
	// If target was given as eye position, check 1 block below
	down := protocol.BlockPos{target.X(), target.Y() - 1, target.Z()}
	if w.isStandingNode(down) {
		return down
	}
	return target
}

// isStandingNode checks if a coordinate satisfies standing criteria:
// non-solid feet and head, solid floor (or floating in water).
func (w *BotWorldView) isStandingNode(feet protocol.BlockPos) bool {
	footType := w.GetBlockType(feet)
	if footType != pathfind.BlockAir && footType != pathfind.BlockLiquidWater {
		return false
	}
	headType := w.GetBlockType(protocol.BlockPos{feet.X(), feet.Y() + 1, feet.Z()})
	if headType != pathfind.BlockAir && headType != pathfind.BlockLiquidWater {
		return false
	}
	if footType == pathfind.BlockLiquidWater {
		return true // Swimming / floating in water of any depth
	}
	floorType := w.GetBlockType(protocol.BlockPos{feet.X(), feet.Y() - 1, feet.Z()})
	return floorType == pathfind.BlockSolid
}
