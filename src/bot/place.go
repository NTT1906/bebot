package bot

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// placeCmd handles "bebot place <x y z>".
func (b *Bot) placeCmd(p []string) {
	if len(p) < 4 {
		b.reply("Usage: place <x y z>")
		return
	}

	x, errX := strconv.Atoi(p[1])
	y, errY := strconv.Atoi(p[2])
	z, errZ := strconv.Atoi(p[3])
	if errX != nil || errY != nil || errZ != nil {
		b.reply("Usage: place <x y z> (coordinates must be integers)")
		return
	}

	targetPos := protocol.BlockPos{int32(x), int32(y), int32(z)}
	b.placeBlockAt(targetPos)
}

// isPlaceableBlockItem checks whether an item can be placed as a block in the world.
func isPlaceableBlockItem(name string, item protocol.ItemInstance) bool {
	if item.Stack.Count == 0 {
		return false
	}
	clean := strings.ToLower(strings.TrimPrefix(name, "minecraft:"))

	// If the item explicitly carries BlockRuntimeID > 0, it represents a block.
	if item.Stack.BlockRuntimeID > 0 {
		return true
	}

	// Tools, weapons, armor, food, and non-placeable utilities
	if isUnstackable(clean) {
		return false
	}

	// Common non-block item suffixes
	if strings.HasSuffix(clean, "_ingot") ||
		strings.HasSuffix(clean, "_nugget") ||
		strings.HasSuffix(clean, "_raw") ||
		strings.HasPrefix(clean, "raw_") ||
		strings.HasSuffix(clean, "_seeds") ||
		strings.HasSuffix(clean, "_shard") ||
		strings.HasSuffix(clean, "_crystal") ||
		strings.HasSuffix(clean, "_dust") ||
		strings.HasSuffix(clean, "_scrap") ||
		strings.HasSuffix(clean, "_dye") ||
		strings.HasSuffix(clean, "_template") ||
		strings.HasSuffix(clean, "_pattern") ||
		strings.HasSuffix(clean, "_feather") ||
		strings.HasSuffix(clean, "_stick") {
		return false
	}

	// Common non-block specific items
	switch clean {
	case "stick", "feather", "flint", "coal", "charcoal", "diamond", "emerald",
		"netherite_scrap", "nether_star", "quartz", "amethyst_shard", "iron_nugget",
		"gold_nugget", "iron_ingot", "gold_ingot", "copper_ingot", "netherite_ingot",
		"brick", "netherbrick", "clay_ball", "leather", "rabbit_hide", "string",
		"slime_ball", "magma_cream", "blaze_rod", "blaze_powder", "ghast_tear",
		"gunpowder", "glowstone_dust",
		"redstone", "sugar", "bone", "bone_meal", "wheat", "paper", "book",
		"compass", "recovery_compass", "clock", "honeycomb", "prismarine_shard",
		"prismarine_crystals", "nautilus_shell", "heart_of_the_sea", "firework_star",
		"firework_rocket", "popped_chorus_fruit", "echo_shard", "disc_fragment_5",
		"breeze_rod", "wind_charge",
		// Foods
		"apple", "golden_apple", "enchanted_golden_apple", "baked_potato", "potato",
		"poisonous_potato", "carrot", "golden_carrot", "beetroot", "bread",
		"beef", "cooked_beef", "porkchop", "cooked_porkchop", "mutton", "cooked_mutton",
		"chicken", "cooked_chicken", "rabbit", "cooked_rabbit", "cod", "cooked_cod",
		"salmon", "cooked_salmon", "tropical_fish", "pufferfish", "cookie", "melon_slice",
		"glistering_melon_slice", "sweet_berries", "glow_berries", "chorus_fruit", "honey_bottle",
		"rotten_flesh", "spider_eye", "fermented_spider_eye",
		// Projectiles and small items
		"arrow", "spectral_arrow", "tipped_arrow", "snowball", "egg", "ender_pearl", "eye_of_ender",
		"experience_bottle", "potion", "splash_potion", "lingering_potion":
		return false
	}

	return true
}

// oppositeFace returns the face of the neighboring block that touches targetPos.
func oppositeFace(face int32) int32 {
	switch face {
	case 0:
		return 1 // bottom <-> top
	case 1:
		return 0
	case 2:
		return 3 // north (Z-) <-> south (Z+)
	case 3:
		return 2
	case 4:
		return 5 // west (X-) <-> east (X+)
	case 5:
		return 4
	default:
		return 1
	}
}

// blockPosOffset returns pos shifted by 1 block in the direction of face.
func blockPosOffset(pos protocol.BlockPos, face int32) protocol.BlockPos {
	switch face {
	case 0: // bottom (Y-)
		return protocol.BlockPos{pos.X(), pos.Y() - 1, pos.Z()}
	case 1: // top (Y+)
		return protocol.BlockPos{pos.X(), pos.Y() + 1, pos.Z()}
	case 2: // north (Z-)
		return protocol.BlockPos{pos.X(), pos.Y(), pos.Z() - 1}
	case 3: // south (Z+)
		return protocol.BlockPos{pos.X(), pos.Y(), pos.Z() + 1}
	case 4: // west (X-)
		return protocol.BlockPos{pos.X() - 1, pos.Y(), pos.Z()}
	case 5: // east (X+)
		return protocol.BlockPos{pos.X() + 1, pos.Y(), pos.Z()}
	default:
		return pos
	}
}

// placeBlockAt executes the block placement logic.
func (b *Bot) placeBlockAt(targetPos protocol.BlockPos) {
	b.mu.Lock()
	c := b.conn
	if c == nil {
		b.mu.Unlock()
		b.reply("Cannot place block: bot is not connected.")
		return
	}

	held := b.heldItem
	hotbarSlot := b.hotbarSlot
	entityID := b.entityID
	botPos := b.pos
	gameMode := b.gameMode
	name := b.itemNameLocked(held.Stack.ItemType.NetworkID)
	cleanName := strings.TrimPrefix(name, "minecraft:")

	// 1. Verify bot is holding an item
	if held.Stack.Count == 0 {
		b.mu.Unlock()
		b.reply("Cannot place: not holding any item.")
		return
	}

	// 2. Verify held item is placeable
	if !isPlaceableBlockItem(name, held) {
		b.mu.Unlock()
		b.reply(fmt.Sprintf("Cannot place: %s is not a placeable block.", cleanName))
		return
	}

	// 3. Verify reach distance (Vanilla reach: 5.0 blocks in survival, 6.5 in creative)
	centerTarget := mgl32.Vec3{float32(targetPos.X()) + 0.5, float32(targetPos.Y()) + 0.5, float32(targetPos.Z()) + 0.5}
	eyePos := b.pos
	frac := b.pos.Y() - float32(math.Floor(float64(b.pos.Y())))
	if frac < 0.2 {
		eyePos = b.pos.Add(mgl32.Vec3{0, 1.62, 0})
	}
	diff := centerTarget.Sub(eyePos)
	dist := float32(math.Sqrt(float64(diff.Dot(diff))))
	maxReach := float32(5.0)
	if gameMode == 1 || gameMode == 4 {
		maxReach = 6.5
	}
	if dist > maxReach+1.0 {
		b.mu.Unlock()
		b.reply(fmt.Sprintf("Target (%d, %d, %d) is too far away (distance: %.1f, reach: %.1f).",
			targetPos.X(), targetPos.Y(), targetPos.Z(), dist, maxReach))
		return
	}

	// 4. Check if target block position is already occupied
	// A: Check if local cache knows this block and it is not air
	if targetRID, known := b.blockRuntimeIDs[targetPos]; known {
		isAir := targetRID == 0 || (b.mineAirRuntimeIDs != nil && b.mineAirRuntimeIDs[targetRID])
		if !isAir {
			b.mu.Unlock()
			b.reply(fmt.Sprintf("Cannot place: (%d, %d, %d) is already occupied by a solid block.",
				targetPos.X(), targetPos.Y(), targetPos.Z()))
			return
		}
	}

	// B: Check if bot or another player's bounding box intersects target block
	// Standard player bounding box in Bedrock is width=0.6 (radius 0.3), height=1.8
	targetMin := mgl32.Vec3{float32(targetPos.X()), float32(targetPos.Y()), float32(targetPos.Z())}
	targetMax := mgl32.Vec3{float32(targetPos.X()) + 1.0, float32(targetPos.Y()) + 1.0, float32(targetPos.Z()) + 1.0}

	isBoxIntersect := func(pos mgl32.Vec3) bool {
		// Player's feet are at pos.X, pos.Y, pos.Z
		pMin := mgl32.Vec3{pos.X() - 0.3, pos.Y(), pos.Z() - 0.3}
		pMax := mgl32.Vec3{pos.X() + 0.3, pos.Y() + 1.8, pos.Z() + 0.3}

		return pMin.X() < targetMax.X() && pMax.X() > targetMin.X() &&
			pMin.Y() < targetMax.Y() && pMax.Y() > targetMin.Y() &&
			pMin.Z() < targetMax.Z() && pMax.Z() > targetMin.Z()
	}

	// Check own bot
	if isBoxIntersect(b.pos) {
		b.mu.Unlock()
		b.reply(fmt.Sprintf("Cannot place: bot is standing inside (%d, %d, %d).",
			targetPos.X(), targetPos.Y(), targetPos.Z()))
		return
	}

	// Check other players
	for _, p := range b.players {
		if p.online && p.id != 0 && isBoxIntersect(p.position) {
			b.mu.Unlock()
			b.reply(fmt.Sprintf("Cannot place: player %s is standing inside (%d, %d, %d).",
				p.name, targetPos.X(), targetPos.Y(), targetPos.Z()))
			return
		}
	}

	// 5. Determine neighbor block to click against and the clicked face
	// In Minecraft Bedrock, clicking face F on neighborBlock places the new block at neighborBlock + faceOffset(F).
	// Therefore, to place at targetPos, we want: neighborPos + offset(clickedFace) == targetPos.
	// That means clickedFace is the face of neighborPos pointing towards targetPos.
	var (
		clickedBlockPos protocol.BlockPos
		clickedFace     int32
		neighborRuntime uint32
		foundNeighbor   bool
	)

	// Faces to test: 0:bottom (neighbor is below target, clicked top face 1), etc.
	// Priority: check below first (face 1), then horizontal faces, then above (face 0).
	type candidate struct {
		face       int32 // face of neighbor pointing toward target
		neighborOf protocol.BlockPos
	}
	candidates := []candidate{
		{face: 1, neighborOf: blockPosOffset(targetPos, 0)}, // neighbor is at target Y-1, clicking top face 1 -> places at target
		{face: 2, neighborOf: blockPosOffset(targetPos, 3)}, // neighbor is at target Z+1, clicking north face 2 -> places at target
		{face: 3, neighborOf: blockPosOffset(targetPos, 2)}, // neighbor is at target Z-1, clicking south face 3 -> places at target
		{face: 4, neighborOf: blockPosOffset(targetPos, 5)}, // neighbor is at target X+1, clicking west face 4 -> places at target
		{face: 5, neighborOf: blockPosOffset(targetPos, 4)}, // neighbor is at target X-1, clicking east face 5 -> places at target
		{face: 0, neighborOf: blockPosOffset(targetPos, 1)}, // neighbor is at target Y+1, clicking bottom face 0 -> places at target
	}

	for _, cand := range candidates {
		rid, known := b.blockRuntimeIDs[cand.neighborOf]
		if known && !b.mineAirRuntimeIDs[rid] {
			clickedBlockPos = cand.neighborOf
			clickedFace = cand.face
			neighborRuntime = rid
			foundNeighbor = true
			break
		}
	}

	// If no known solid neighbor block is found in cache (e.g. initial spawn),
	// default to placing on top of the block below targetPos (face 1 of target Y-1).
	if !foundNeighbor {
		clickedBlockPos = blockPosOffset(targetPos, 0)
		clickedFace = 1
		neighborRuntime = b.blockRuntimeIDs[clickedBlockPos]
	}

	// 5. Orient the bot towards the clicked block
	cCenter := mgl32.Vec3{float32(clickedBlockPos.X()) + 0.5, float32(clickedBlockPos.Y()) + 0.5, float32(clickedBlockPos.Z()) + 0.5}
	lookDiff := cCenter.Sub(eyePos)
	yaw := float32(math.Atan2(float64(-lookDiff.X()), float64(lookDiff.Z())) * 180 / math.Pi)
	if yaw < 0 {
		yaw += 360
	}
	hDist := math.Sqrt(float64(lookDiff.X()*lookDiff.X() + lookDiff.Z()*lookDiff.Z()))
	pitch := float32(-math.Atan2(float64(lookDiff.Y()), hDist) * 180 / math.Pi)

	b.facingYaw = yaw
	b.facingPitch = pitch
	b.idleYaw = yaw
	b.idlePitch = pitch

	clickPos := faceClickPosition(clickedFace)

	// Prepare placement transaction
	txData := protocol.UseItemTransactionData{
		ActionType:       protocol.UseItemActionClickBlock,
		TriggerType:      protocol.TriggerTypePlayerInput,
		BlockPosition:    clickedBlockPos,
		BlockFace:        clickedFace,
		HotBarSlot:       int32(hotbarSlot),
		HeldItem:         held,
		Position:         botPos,
		ClickedPosition:  clickPos,
		BlockRuntimeID:   neighborRuntime,
		ClientPrediction: protocol.ClientPredictionSuccess,
	}

	// Solution 1: Queue inside the active PlayerAuthInput tick (Official Bedrock client-prediction flow)
	b.placePending = &txData
	b.placePendingSwing = true

	// Drain any leftover block update notifications from previous operations
	if b.blockRuntimeIDUpdated != nil {
		for len(b.blockRuntimeIDUpdated) > 0 {
			<-b.blockRuntimeIDUpdated
		}
	}
	upCh := b.blockRuntimeIDUpdated
	b.mu.Unlock()

	// Wait 80ms (covers 1-2 ticks) for moveTick to transmit PlayerAuthInput with ItemInteractionData
	time.Sleep(80 * time.Millisecond)

	// Direct fallback: also send the explicit InventoryTransaction packet
	b.mu.Lock()
	c = b.conn
	if c != nil {
		// Refresh runtime ID in case moveTick or an update arrived
		if currentRID, ok := b.blockRuntimeIDs[clickedBlockPos]; ok && currentRID != 0 {
			txData.BlockRuntimeID = currentRID
		}
		txData.Position = b.pos
	}
	b.mu.Unlock()

	if c != nil {
		b.markPacketSent(fmt.Sprintf("InventoryTransaction (UseItemActionClickBlock place pos=%v face=%d target=%v)", clickedBlockPos, clickedFace, targetPos))
		_ = c.WritePacket(&packet.InventoryTransaction{
			TransactionData: &txData,
		})
		_ = c.WritePacket(&packet.Animate{
			ActionType:      packet.AnimateActionSwingArm,
			EntityRuntimeID: entityID,
		})
		_ = c.Flush()
	}

	// Solution 3 Auto-Retry Fallback:
	// If the neighbor runtime ID was initially 0 or unknown, wait up to 150ms for a corrective UpdateBlock.
	// If one arrives with the updated block ID, immediately re-fire the transaction with the verified ID.
	if neighborRuntime == 0 && upCh != nil {
		go func() {
			timer := time.NewTimer(150 * time.Millisecond)
			defer timer.Stop()

			select {
			case updatedPos := <-upCh:
				if updatedPos == clickedBlockPos {
					b.mu.Lock()
					conn := b.conn
					newRID := b.blockRuntimeIDs[clickedBlockPos]
					curHeld := b.heldItem
					curHotbar := b.hotbarSlot
					curPos := b.pos
					b.mu.Unlock()

					if conn != nil && newRID != 0 {
						log.Printf("bebot: auto-retry placement with corrective blockRuntimeID=%d at (%d, %d, %d)\n",
							newRID, targetPos.X(), targetPos.Y(), targetPos.Z())
						retryTx := txData
						retryTx.BlockRuntimeID = newRID
						retryTx.HeldItem = curHeld
						retryTx.HotBarSlot = int32(curHotbar)
						retryTx.Position = curPos

						_ = conn.WritePacket(&packet.InventoryTransaction{
							TransactionData: &retryTx,
						})
						_ = conn.WritePacket(&packet.Animate{
							ActionType:      packet.AnimateActionSwingArm,
							EntityRuntimeID: entityID,
						})
						_ = conn.Flush()
					}
				}
			case <-timer.C:
			}
		}()
	}

	log.Printf("bebot: placed %s at (%d, %d, %d) clickedBlock=(%d, %d, %d) face=%d\n",
		cleanName, targetPos.X(), targetPos.Y(), targetPos.Z(), clickedBlockPos.X(), clickedBlockPos.Y(), clickedBlockPos.Z(), clickedFace)
	b.reply(fmt.Sprintf("Placed %s at (%d, %d, %d).", cleanName, targetPos.X(), targetPos.Y(), targetPos.Z()))
}
