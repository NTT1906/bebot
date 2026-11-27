package bot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func (b *Bot) startBreaking(pos protocol.BlockPos, animate bool, repeat bool) {
	b.breakAnimation = animate
	b.breakRepeat = repeat
	if b.gameMode == 1 || b.gameMode == 4 {
		b.mu.Lock()
		c := b.conn
		entityID := b.entityID
		b.mu.Unlock()
		if c == nil {
			log.Println("bebot: cannot break block: not connected")
			return
		}
		face := determineBlockFace(b.playerEyePos(), pos)
		if err := c.WritePacket(&packet.PlayerAction{
			EntityRuntimeID: entityID,
			ActionType:      protocol.PlayerActionCreativePlayerDestroyBlock,
			BlockPosition:   pos,
			ResultPosition:  pos,
			BlockFace:       face,
		}); err != nil {
			log.Println("bebot: creative break failed:", ExplainError(err))
		}
		if animate {
			_ = c.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: entityID, SwingSource: packet.AnimateSwingSourceMine})
		}
		if err := c.Flush(); err != nil {
			log.Println("bebot: creative break flush failed:", ExplainError(err))
		}
		b.mu.Lock()
		b.beginBreakTrackingLocked(pos)
		b.mu.Unlock()
		if tracePackets {
			log.Printf("bebot: creative break requested action=%d at %d %d %d\n", protocol.PlayerActionCreativePlayerDestroyBlock, pos.X(), pos.Y(), pos.Z())
		}
		return
	}
	b.mu.Lock()
	b.beginBreakTrackingLocked(pos)
	b.mu.Unlock()
	log.Printf("bebot: breaking block at %d %d %d\n", pos.X(), pos.Y(), pos.Z())
}

func (b *Bot) stopBreaking() {
	b.mu.Lock()
	if b.breakTarget == nil {
		b.mineLearnActive = false
		b.mineLearnWasBroken = false
		b.mineLearnStartedBreak = false
		b.mineLearnInitialRuntimeID = 0
		b.mu.Unlock()
		log.Println("bebot: no block breaking operation is active")
		return
	}
	pos := *b.breakTarget
	b.abortBreakTarget = &pos
	b.breakTarget = nil
	b.breakTicks = 0
	b.breakRepeat = false
	b.mineLearnActive = false
	b.mineLearnWasBroken = false
	b.mineLearnStartedBreak = false
	b.mineLearnInitialRuntimeID = 0
	b.mu.Unlock()

	log.Printf("bebot: stopped breaking block at %d %d %d\n", pos.X(), pos.Y(), pos.Z())
}

func (b *Bot) getBreakDuration() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.getBreakDurationLocked()
}

func (b *Bot) getBreakDurationLocked() int {
	if b.hasteLevel >= 2 {
		return 1
	}
	if b.hasteLevel == 1 {
		return 2
	}
	if b.gameMode == 1 || b.gameMode == 4 {
		return 2
	}
	if b.mineActive && b.mineTicksReq > 0 {
		return b.mineTicksReq
	}
	if b.mineActive && b.mineFastBreak {
		return 2
	}
	netID := b.heldItem.Stack.NetworkID
	name := b.itemNamesByID[netID]

	switch {
	case strings.Contains(name, "netherite_pickaxe"):
		return 4
	case strings.Contains(name, "diamond_pickaxe"):
		return 5
	case strings.Contains(name, "golden_pickaxe"):
		return 4
	case strings.Contains(name, "iron_pickaxe"):
		return 8
	case strings.Contains(name, "stone_pickaxe"):
		return 12
	case strings.Contains(name, "wooden_pickaxe"):
		return 20
	case strings.Contains(name, "axe"):
		return 4
	case strings.Contains(name, "shovel"):
		return 4
	case strings.Contains(name, "hoe"):
		return 4
	case strings.Contains(name, "shears"):
		return 2
	default:
		return 10
	}
}

func (b *Bot) isInMineBox(pos protocol.BlockPos) bool {
	return pos.X() >= b.mineBoxMin.X() && pos.X() <= b.mineBoxMax.X() &&
		pos.Y() >= b.mineBoxMin.Y() && pos.Y() <= b.mineBoxMax.Y() &&
		pos.Z() >= b.mineBoxMin.Z() && pos.Z() <= b.mineBoxMax.Z()
}

const defaultAirRuntimeIDsCacheFile = "data/air_runtime_ids.json"

type airCacheData struct {
	ServerUseHashes bool     `json:"server_use_hashes"`
	RuntimeIDs      []uint32 `json:"runtime_ids"`
}

type blockStateTag struct {
	Name   string         `nbt:"name"`
	States map[string]any `nbt:"states"`
}

// HashBlockState computes the Bedrock Little-Endian NBT 32-bit FNV-1a hash for a block permutation.
func HashBlockState(name string, states map[string]any) uint32 {
	if states == nil {
		states = map[string]any{}
	}
	tag := blockStateTag{
		Name:   name,
		States: states,
	}
	buf := new(bytes.Buffer)
	_ = nbt.NewEncoderWithEncoding(buf, nbt.LittleEndian).Encode(tag)

	h := fnv.New32a()
	_, _ = h.Write(buf.Bytes())
	return h.Sum32()
}

// computeDefaultAirRuntimeIDs computes initial air runtime IDs based on whether the server uses network hashes.
func computeDefaultAirRuntimeIDs(useHashes bool) map[uint32]bool {
	res := make(map[uint32]bool)
	res[0] = true // 0 is universally recognized (vanilla BDS air index or uninitialized fallback)
	if useHashes {
		for _, name := range []string{"minecraft:air", "minecraft:cave_air", "minecraft:void_air"} {
			res[HashBlockState(name, nil)] = true
		}
	}
	return res
}

// isAirRuntimeIDLocked reports whether rid is a known air runtime ID.
// b.mu must be held by the caller.
func (b *Bot) isAirRuntimeIDLocked(rid uint32) bool {
	return rid == 0 || (b.mineAirRuntimeIDs != nil && b.mineAirRuntimeIDs[rid])
}

// learnAirRuntimeIDLocked records a newly observed air runtime ID and persists only
// the air ID registry. Unmineable block coordinates are never written to this cache.
// b.mu must be held by the caller.
func (b *Bot) learnAirRuntimeIDLocked(rid uint32) bool {
	if rid == 0 {
		return false
	}
	if b.mineAirRuntimeIDs == nil {
		b.mineAirRuntimeIDs = make(map[uint32]bool)
	}
	if b.mineAirRuntimeIDs[rid] {
		return false
	}
	b.mineAirRuntimeIDs[rid] = true
	b.actualAirRID = rid
	if b.blockRegistry != nil {
		b.blockRegistry.SetActualAirRID(rid)
	}
	_ = b.saveAirRuntimeIDsCacheLocked(defaultAirRuntimeIDsCacheFile)
	return true
}

// beginBreakTrackingLocked starts tracking a new break target and captures its
// pre-break runtime ID when it is already known. This lets the bot distinguish a
// successful break (the runtime ID changed to air) from a rejected break
// (the runtime ID stayed solid) without depending on the block palette or chunk data.
// b.mu must be held by the caller.
func (b *Bot) beginBreakTrackingLocked(pos protocol.BlockPos) {
	b.breakTarget = &pos
	b.breakTicks = 0
	b.mineBreakInitialRuntimeID = 0
	b.mineBreakInitialKnown = false
	if b.blockRuntimeIDs != nil {
		if rid, ok := b.blockRuntimeIDs[pos]; ok && !b.isAirRuntimeIDLocked(rid) {
			b.mineBreakInitialRuntimeID = rid
			b.mineBreakInitialKnown = true
		}
	}
}

func (b *Bot) loadAirRuntimeIDsCache(filePath string) ([]uint32, bool) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, false
	}
	var cache airCacheData
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, false
	}
	return cache.RuntimeIDs, true
}

func (b *Bot) saveAirRuntimeIDsCacheLocked(filePath string) error {
	if b.mineAirRuntimeIDs == nil {
		return nil
	}
	ids := make([]uint32, 0, len(b.mineAirRuntimeIDs))
	for id := range b.mineAirRuntimeIDs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	cache := airCacheData{
		ServerUseHashes: b.serverUseHashes,
		RuntimeIDs:      ids,
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(filePath); dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}
	return os.WriteFile(filePath, data, 0644)
}

// initAirRuntimeIDs initializes air block IDs upon server connection and merges cached learned IDs.
func (b *Bot) initAirRuntimeIDs(useHashes bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.serverUseHashes = useHashes
	if useHashes {
		b.actualAirRID = HashBlockState("minecraft:air", nil)
	} else {
		b.actualAirRID = 0
	}
	if b.blockRegistry != nil {
		b.blockRegistry.SetActualAirRID(b.actualAirRID)
	}
	b.mineAirRuntimeIDs = computeDefaultAirRuntimeIDs(useHashes)

	// Load and merge cached IDs from disk
	if cachedIDs, ok := b.loadAirRuntimeIDsCache(defaultAirRuntimeIDsCacheFile); ok {
		for _, id := range cachedIDs {
			b.mineAirRuntimeIDs[id] = true
		}
	}

	// Persist the initialized/merged set to cache
	_ = b.saveAirRuntimeIDsCacheLocked(defaultAirRuntimeIDsCacheFile)

	ids := make([]uint32, 0, len(b.mineAirRuntimeIDs))
	for id := range b.mineAirRuntimeIDs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	log.Printf("bebot: initialized %d air block runtime IDs (useNetworkHashes=%v): %v\n", len(ids), useHashes, ids)
}

// BlockRuntimeIDByName resolves a block namespaced identifier (with optional state properties)
// to its Bedrock runtime network ID.
func (b *Bot) BlockRuntimeIDByName(name string, states map[string]any) (uint32, bool) {
	if name == "minecraft:air" || name == "air" {
		return b.ActualAirRID(), true
	}

	b.mu.Lock()
	reg := b.blockRegistry
	useHashes := b.serverUseHashes
	b.mu.Unlock()

	if reg != nil && reg.IsLoaded() {
		if rid, ok := reg.ResolveRID(name); ok {
			return rid, true
		}
	}

	if useHashes {
		return HashBlockState(name, states), true
	}
	return 0, false
}

func (b *Bot) startMiningArea(ax, ay, az, bx, by, bz int32, ticksReq int, repeat bool, faceAccurate bool) {
	minX, maxX := ax, bx
	if minX > maxX {
		minX, maxX = maxX, minX
	}
	minY, maxY := ay, by
	if minY > maxY {
		minY, maxY = maxY, minY
	}
	minZ, maxZ := az, bz
	if minZ > maxZ {
		minZ, maxZ = maxZ, minZ
	}

	total := int((maxX - minX + 1) * (maxY - minY + 1) * (maxZ - minZ + 1))
	if total <= 0 || total > 100000 {
		b.reply(fmt.Sprintf("Invalid area size: %d blocks (limit 1-100,000).", total))
		return
	}

	queue := make([]protocol.BlockPos, 0, total)
	airFiltered := 0
	for y := maxY; y >= minY; y-- {
		forward := true
		for x := minX; x <= maxX; x++ {
			if forward {
				for z := minZ; z <= maxZ; z++ {
					pos := protocol.BlockPos{x, y, z}
					if b.chunkCache != nil && b.chunkCache.IsEnabled() {
						if rid, ok := b.chunkCache.BlockAt(pos); ok {
							b.mu.Lock()
							isAir := b.isAirRuntimeIDLocked(rid)
							b.mu.Unlock()
							if isAir {
								airFiltered++
								continue
							}
						}
					}
					queue = append(queue, pos)
				}
			} else {
				for z := maxZ; z >= minZ; z-- {
					pos := protocol.BlockPos{x, y, z}
					if b.chunkCache != nil && b.chunkCache.IsEnabled() {
						if rid, ok := b.chunkCache.BlockAt(pos); ok {
							b.mu.Lock()
							isAir := b.isAirRuntimeIDLocked(rid)
							b.mu.Unlock()
							if isAir {
								airFiltered++
								continue
							}
						}
					}
					queue = append(queue, pos)
				}
			}
			forward = !forward
		}
	}

	b.mu.Lock()
	b.mineQueue = queue
	b.mineTotal = total
	b.mineCount = 0
	b.mineActive = true
	b.mineInitialDone = false
	b.mineCurrent = nil
	b.mineStuckTicks = 0
	b.mineTicksReq = ticksReq
	b.mineBoxMin = protocol.BlockPos{minX, minY, minZ}
	b.mineBoxMax = protocol.BlockPos{maxX, maxY, maxZ}
	b.mineRepeat = repeat
	b.mineContinuous = repeat
	b.mineFaceAccurate = faceAccurate
	b.mineAttempts = make(map[protocol.BlockPos]int)
	b.knownAirBlocks = make(map[protocol.BlockPos]bool)
	b.unmineableBlocks = make(map[protocol.BlockPos]bool)
	if b.mineAirRuntimeIDs == nil {
		b.mineAirRuntimeIDs = computeDefaultAirRuntimeIDs(b.serverUseHashes)
	}
	b.mineFastBreak = false
	b.mineWaitingForBlocks = false
	b.mineLastWasBroken = false
	b.mineLastFinishedPos = protocol.BlockPos{}
	b.mineLastFinishedTick = 0
	b.mineBreakInitialRuntimeID = 0
	b.mineBreakInitialKnown = false
	b.mu.Unlock()

	modeStr := "once"
	if repeat {
		modeStr = "repeat"
	}
	faceStr := "standard face"
	if faceAccurate {
		faceStr = "face-accurate"
	}
	if airFiltered > 0 {
		b.reply(fmt.Sprintf("Mining space (%d, %d, %d) to (%d, %d, %d): %d solid blocks queued (%d air pre-filtered, %s, %s).",
			minX, minY, minZ, maxX, maxY, maxZ, len(queue), airFiltered, modeStr, faceStr))
	} else {
		b.reply(fmt.Sprintf("Mining space (%d, %d, %d) to (%d, %d, %d): %d blocks queued (%s, %s).",
			minX, minY, minZ, maxX, maxY, maxZ, total, modeStr, faceStr))
	}
}

func (b *Bot) stopMiningArea() {
	b.mu.Lock()
	b.unmineableBlocks = nil
	if !b.mineActive && len(b.mineQueue) == 0 {
		b.mu.Unlock()
		return
	}
	b.mineActive = false
	b.mineRepeat = false
	b.mineContinuous = false
	b.mineFaceAccurate = false
	b.mineQueue = nil
	b.mineCurrent = nil
	b.mineStuckTicks = 0
	b.mineAttempts = nil
	b.breakTarget = nil
	b.breakTicks = 0
	b.mineInitialDone = false
	b.mineWaitingForBlocks = false
	b.mineLastWasBroken = false
	b.mineLastFinishedPos = protocol.BlockPos{}
	b.mineLastFinishedTick = 0
	b.mineBreakInitialRuntimeID = 0
	b.mineBreakInitialKnown = false
	b.mu.Unlock()
	b.reply("Stopped area mining.")
}

func (b *Bot) restartMiningQueue() {
	minX, maxX := b.mineBoxMin.X(), b.mineBoxMax.X()
	minY, maxY := b.mineBoxMin.Y(), b.mineBoxMax.Y()
	minZ, maxZ := b.mineBoxMin.Z(), b.mineBoxMax.Z()
	total := int((maxX - minX + 1) * (maxY - minY + 1) * (maxZ - minZ + 1))
	queue := make([]protocol.BlockPos, 0, total)
	for y := maxY; y >= minY; y-- {
		forward := true
		for x := minX; x <= maxX; x++ {
			if forward {
				for z := minZ; z <= maxZ; z++ {
					pos := protocol.BlockPos{x, y, z}
					if b.knownAirBlocks != nil && b.knownAirBlocks[pos] {
						continue
					}
					if b.unmineableBlocks != nil && b.unmineableBlocks[pos] {
						continue
					}
					queue = append(queue, pos)
				}
			} else {
				for z := maxZ; z >= minZ; z-- {
					pos := protocol.BlockPos{x, y, z}
					if b.knownAirBlocks != nil && b.knownAirBlocks[pos] {
						continue
					}
					if b.unmineableBlocks != nil && b.unmineableBlocks[pos] {
						continue
					}
					queue = append(queue, pos)
				}
			}
			forward = !forward
		}
	}
	b.mineQueue = queue
	b.mineTotal = len(queue)
	b.mineCount = 0
	b.mineCurrent = nil
	b.breakTarget = nil
	b.breakTicks = 0
	b.mineStuckTicks = 0
	b.mineInitialDone = false
	b.mineWaitingForBlocks = len(queue) == 0
	b.mineLastWasBroken = false
	b.mineBreakInitialRuntimeID = 0
	b.mineBreakInitialKnown = false
	b.mineAttempts = make(map[protocol.BlockPos]int)
	if len(queue) > 0 {
		log.Printf("bebot: repeating area mining (%d blocks re-queued)\n", len(queue))
	} else {
		log.Printf("bebot: repeating area mining: area is clear, waiting for new blocks to appear\n")
	}
}

func sortMineQueue(queue []protocol.BlockPos) {
	sort.SliceStable(queue, func(i, j int) bool {
		if queue[i].Y() != queue[j].Y() {
			return queue[i].Y() > queue[j].Y()
		}
		if queue[i].X() != queue[j].X() {
			return queue[i].X() < queue[j].X()
		}
		return queue[i].Z() < queue[j].Z()
	})
}

func (b *Bot) resolveSubChunkBlockPos(subPos protocol.BlockPos, blockPos protocol.BlockPos) protocol.BlockPos {
	if b.isInMineBox(blockPos) {
		return blockPos
	}
	candidateA := protocol.BlockPos{subPos.X()*16 + blockPos.X(), subPos.Y()*16 + blockPos.Y(), subPos.Z()*16 + blockPos.Z()}
	if b.isInMineBox(candidateA) {
		return candidateA
	}
	candidateB := protocol.BlockPos{subPos.X() + blockPos.X(), subPos.Y() + blockPos.Y(), subPos.Z() + blockPos.Z()}
	if b.isInMineBox(candidateB) {
		return candidateB
	}
	if blockPos.X() >= 0 && blockPos.X() <= 15 && blockPos.Z() >= 0 && blockPos.Z() <= 15 {
		if subPos.X() > -1000 && subPos.X() < 1000 {
			return candidateA
		}
		return candidateB
	}
	return blockPos
}

func (b *Bot) handleUpdateBlock(pos protocol.BlockPos, runtimeID uint32) {
	b.mu.Lock()
	if b.blockRuntimeIDs == nil {
		b.blockRuntimeIDs = make(map[protocol.BlockPos]uint32)
	}
	prevRuntimeID, prevKnown := b.blockRuntimeIDs[pos]
	b.blockRuntimeIDs[pos] = runtimeID
	isContainerTarget := b.containerOpening && pos == b.containerPos
	ch := b.containerRuntimeIDUpdated
	upCh := b.blockRuntimeIDUpdated
	b.mu.Unlock()

	if upCh != nil {
		select {
		case upCh <- pos:
		default:
		}
	}

	if isContainerTarget && ch != nil {
		select {
		case ch <- runtimeID:
		default:
		}
	}

	b.mu.Lock()
	if b.mineLearnActive && pos == b.mineLearnPos {
		// If breaking hasn't started yet, record the solid block ID.
		if !b.mineLearnStartedBreak {
			b.mineLearnInitialRuntimeID = runtimeID
			log.Printf("bebot: mine learn target (%d, %d, %d) initial solid block runtimeID: %d\n",
				pos.X(), pos.Y(), pos.Z(), runtimeID)
			b.mu.Unlock()
			return
		}

		// Breaking has started!
		if b.mineLearnInitialRuntimeID == 0 {
			// First update received after break started is the solid block syncing.
			b.mineLearnInitialRuntimeID = runtimeID
			log.Printf("bebot: mine learn target (%d, %d, %d) captured initial solid block runtimeID: %d (waiting for break result)\n",
				pos.X(), pos.Y(), pos.Z(), runtimeID)
			b.mu.Unlock()
			return
		}

		// The server sent an update for the calibration target.
		// If it's the exact same solid block, it hasn't broken yet.
		if runtimeID == b.mineLearnInitialRuntimeID {
			b.mu.Unlock()
			return
		}

		// The block runtime ID changed!
		// This handles BOTH normal blocks (after maxTicks) AND instant-break blocks (short grass, flowers, torches, slime)!
		if b.mineAirRuntimeIDs == nil {
			b.mineAirRuntimeIDs = computeDefaultAirRuntimeIDs(b.serverUseHashes)
		}
		delete(b.mineAirRuntimeIDs, b.mineLearnInitialRuntimeID)
		b.mineAirRuntimeIDs[runtimeID] = true
		_ = b.saveAirRuntimeIDsCacheLocked(defaultAirRuntimeIDsCacheFile)
		if b.knownAirBlocks == nil {
			b.knownAirBlocks = make(map[protocol.BlockPos]bool)
		}
		b.knownAirBlocks[pos] = true

		if b.unmineableBlocks != nil {
			delete(b.unmineableBlocks, pos)
		}
		solidID := b.mineLearnInitialRuntimeID
		b.mineLearnActive = false
		b.mineLearnWasBroken = false
		b.mineLearnStartedBreak = false
		b.mineLearnInitialRuntimeID = 0

		// Completely stop breaking and notify the server to abort/finish break interaction
		abortPos := pos
		b.abortBreakTarget = &abortPos
		b.breakTarget = nil
		b.breakTicks = 0
		b.breakRepeat = false
		b.mineCurrent = nil
		b.mineActive = false
		b.mineQueue = nil
		b.mu.Unlock()

		log.Printf("bebot: calibration complete! Solid block (%d) broke -> Confirmed air runtime ID: %d\n", solidID, runtimeID)
		b.reply(fmt.Sprintf("Calibration complete! Solid block (%d) broke -> Confirmed air block runtime ID: %d.", solidID, runtimeID))
		return
	}

	defer b.mu.Unlock()

	if !b.mineActive {
		// The world changed at this coordinate while idle. Clear a stale
		// unmineable entry only when the block actually changed (or is now air),
		// not when the server merely re-syncs the same solid block.
		if b.unmineableBlocks != nil && b.unmineableBlocks[pos] &&
			(b.isAirRuntimeIDLocked(runtimeID) || (prevKnown && prevRuntimeID != runtimeID)) {
			delete(b.unmineableBlocks, pos)
		}
		return
	}

	isCurrentBreak := b.breakTarget != nil && pos == *b.breakTarget
	isRecentFinish := pos == b.mineLastFinishedPos && (b.tick-b.mineLastFinishedTick <= 5)

	isAir := b.isAirRuntimeIDLocked(runtimeID)

	// Capture the target's solid runtime ID while the break is still in progress.
	// The server may send an UpdateBlock with its current state before the result.
	if isCurrentBreak && !isAir && !b.mineBreakInitialKnown {
		b.mineBreakInitialRuntimeID = runtimeID
		b.mineBreakInitialKnown = true
	}

	// A predicted break is only considered air when the server actually changed the
	// block away from its pre-break runtime ID. Otherwise it is treated as unmineable
	// below, so rejected breaks never pollute the air runtime ID registry.
	preBreakRID := b.mineBreakInitialRuntimeID
	preBreakKnown := b.mineBreakInitialKnown
	if !preBreakKnown && prevKnown && !b.isAirRuntimeIDLocked(prevRuntimeID) {
		preBreakRID = prevRuntimeID
		preBreakKnown = true
	}
	if !isAir && isRecentFinish && b.mineLastWasBroken && preBreakKnown && runtimeID != preBreakRID {
		if b.learnAirRuntimeIDLocked(runtimeID) {
			log.Printf("bebot: learned air block runtime ID %d\n", runtimeID)
		}
		isAir = true
	}

	if isAir {
		if b.unmineableBlocks != nil {
			delete(b.unmineableBlocks, pos)
		}
		if b.knownAirBlocks == nil {
			b.knownAirBlocks = make(map[protocol.BlockPos]bool)
		}
		b.knownAirBlocks[pos] = true

		if isCurrentBreak {
			if b.breakTicks <= 3 {
				b.mineFastBreak = true
			}
			b.breakTarget = nil
			b.breakTicks = 0
			b.mineCurrent = nil
			b.mineCount++
			if b.mineCount%25 == 0 || len(b.mineQueue) == 0 {
				log.Printf("bebot: block broken (%d mined)\n", b.mineCount)
			}
		} else if b.mineCurrent != nil && *b.mineCurrent == pos {
			b.mineCurrent = nil
		}

		if b.mineAttempts != nil {
			b.mineAttempts[pos] = 2
		}
	} else if isRecentFinish {
		// The bot attempted to break this block, but it did NOT turn into air!
		// It is unmineable (bedrock, protected area, server rejected break, oak log, etc.).
		// We NEVER pollute mineAirRuntimeIDs. We record this coordinate in the in-memory unmineable blacklist.
		if b.unmineableBlocks == nil {
			b.unmineableBlocks = make(map[protocol.BlockPos]bool)
		}
		b.unmineableBlocks[pos] = true
		if b.knownAirBlocks != nil {
			delete(b.knownAirBlocks, pos)
		}
		if b.mineAttempts != nil {
			b.mineAttempts[pos] = 2
		}
		log.Printf("bebot: block at (%d, %d, %d) did not break into air (runtimeID %d), marked as unmineable\n",
			pos.X(), pos.Y(), pos.Z(), runtimeID)
	} else if b.isInMineBox(pos) && !isRecentFinish && !isCurrentBreak {
		if b.unmineableBlocks != nil {
			delete(b.unmineableBlocks, pos)
		}
		if b.knownAirBlocks != nil {
			delete(b.knownAirBlocks, pos)
		}
		if b.mineAttempts != nil {
			delete(b.mineAttempts, pos)
		}
		alreadyQueued := false
		if b.mineCurrent != nil && *b.mineCurrent == pos {
			alreadyQueued = true
		}
		if b.breakTarget != nil && *b.breakTarget == pos {
			alreadyQueued = true
		}
		if !alreadyQueued {
			for _, q := range b.mineQueue {
				if q == pos {
					alreadyQueued = true
					break
				}
			}
		}
		if !alreadyQueued {
			b.mineQueue = append(b.mineQueue, pos)
			b.mineTotal++
			b.mineInitialDone = false
			b.mineWaitingForBlocks = false
			sortMineQueue(b.mineQueue)
			log.Printf("bebot: new block detected at (%d, %d, %d), added to mine queue (queue len=%d)\n", pos.X(), pos.Y(), pos.Z(), len(b.mineQueue))
		}
	}

	if isRecentFinish {
		// Consume the finish result once. Later updates at this coordinate are either
		// a genuine newly placed block or a late air confirmation.
		b.mineLastFinishedPos = protocol.BlockPos{}
		b.mineLastFinishedTick = 0
		b.mineBreakInitialRuntimeID = 0
		b.mineBreakInitialKnown = false
	}
}

func determineBlockFace(eyePos mgl32.Vec3, blockPos protocol.BlockPos) int32 {
	dx := float32(blockPos.X()) + 0.5 - eyePos.X()
	dy := float32(blockPos.Y()) + 0.5 - eyePos.Y()
	dz := float32(blockPos.Z()) + 0.5 - eyePos.Z()

	absX := float32(math.Abs(float64(dx)))
	absY := float32(math.Abs(float64(dy)))
	absZ := float32(math.Abs(float64(dz)))

	if absY >= absX && absY >= absZ {
		if dy > 0 {
			return 0
		}
		return 1
	} else if absX >= absZ {
		if dx > 0 {
			return 4
		}
		return 5
	} else {
		if dz > 0 {
			return 2
		}
		return 3
	}
}

func faceClickPosition(face int32) mgl32.Vec3 {
	switch face {
	case 0: // bottom
		return mgl32.Vec3{0.5, 0.0, 0.5}
	case 1: // top
		return mgl32.Vec3{0.5, 1.0, 0.5}
	case 2: // north (Z-)
		return mgl32.Vec3{0.5, 0.5, 0.0}
	case 3: // south (Z+)
		return mgl32.Vec3{0.5, 0.5, 1.0}
	case 4: // west (X-)
		return mgl32.Vec3{0.0, 0.5, 0.5}
	case 5: // east (X+)
		return mgl32.Vec3{1.0, 0.5, 0.5}
	default:
		return mgl32.Vec3{0.5, 0.5, 0.5}
	}
}

func (b *Bot) pickNextMineBlock() *protocol.BlockPos {
	for len(b.mineQueue) > 0 {
		pos := b.mineQueue[0]
		if (b.knownAirBlocks != nil && b.knownAirBlocks[pos]) ||
			(b.unmineableBlocks != nil && b.unmineableBlocks[pos]) ||
			(b.mineAttempts != nil && b.mineAttempts[pos] >= 2) {
			b.mineQueue = b.mineQueue[1:]
			continue
		}
		break
	}

	if len(b.mineQueue) == 0 {
		return nil
	}

	target := b.mineQueue[0]
	b.mineQueue = b.mineQueue[1:]
	return &target
}

func (b *Bot) startBreakingFront(animate bool, repeat bool) {
	yaw := b.facingYaw
	if b.mode == modeMimic {
		b.mu.Lock()
		if p := b.players[strings.ToLower(b.mimicTarget)]; p.id != 0 {
			yaw = p.yaw
		}
		b.mu.Unlock()
	}
	rad := float64(yaw) * math.Pi / 180
	x := int32(math.Floor(float64(b.pos.X() - float32(math.Sin(rad)))))
	y := int32(math.Floor(float64(b.pos.Y())))
	z := int32(math.Floor(float64(b.pos.Z() + float32(math.Cos(rad)))))
	b.startBreaking(protocol.BlockPos{x, y, z}, animate, repeat)
}

func (b *Bot) startLearningAir(pos *protocol.BlockPos) {
	b.mu.Lock()
	if b.mineActive {
		b.mineActive = false
		b.mineRepeat = false
		b.mineContinuous = false
		b.mineQueue = nil
		b.mineCurrent = nil
		b.unmineableBlocks = nil
	}
	var target protocol.BlockPos
	if pos != nil {
		target = *pos
	} else {
		yaw := b.facingYaw
		if b.mode == modeMimic {
			if p := b.players[strings.ToLower(b.mimicTarget)]; p.id != 0 {
				yaw = p.yaw
			}
		}
		rad := float64(yaw) * math.Pi / 180
		x := int32(math.Floor(float64(b.pos.X() - float32(math.Sin(rad)))))
		y := int32(math.Floor(float64(b.pos.Y())))
		z := int32(math.Floor(float64(b.pos.Z() + float32(math.Cos(rad)))))
		target = protocol.BlockPos{x, y, z}
	}
	initialRuntimeID := uint32(0)
	if rid, ok := b.blockRuntimeIDs[target]; ok {
		if b.mineAirRuntimeIDs != nil && b.mineAirRuntimeIDs[rid] {
			b.mu.Unlock()
			b.reply(fmt.Sprintf("Block at (%d, %d, %d) is already air (runtime ID %d). Please target a solid block.",
				target.X(), target.Y(), target.Z(), rid))
			return
		}
		initialRuntimeID = rid
	}
	b.mineLearnActive = true
	b.mineLearnPos = target
	b.mineLearnInitialRuntimeID = initialRuntimeID
	b.mineLearnWasBroken = false
	b.mineLearnStartedBreak = false
	b.beginBreakTrackingLocked(target)
	b.breakAnimation = true
	b.breakRepeat = false
	b.mu.Unlock()

	log.Printf("bebot: calibrating air block runtime ID at (%d, %d, %d)... initial runtimeID=%d\n", target.X(), target.Y(), target.Z(), initialRuntimeID)
	b.reply(fmt.Sprintf("Calibrating air block runtime ID at (%d, %d, %d)... breaking block.", target.X(), target.Y(), target.Z()))
}
