package bot

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func (b *Bot) selectHotbar(slot byte) {
	b.mu.Lock()
	c := b.conn
	entityID := b.entityID
	var item protocol.ItemInstance
	if int(slot) < len(b.inventory) {
		item = b.inventory[slot]
	}
	b.hotbarSlot = slot
	b.heldItem = item
	b.mu.Unlock()
	if c == nil {
		b.reply("Cannot change hotbar: bot is not connected.")
		return
	}
	if err := c.WritePacket(&packet.MobEquipment{
		EntityRuntimeID: entityID,
		NewItem:         item,
		InventorySlot:   slot,
		HotBarSlot:      slot,
		WindowID:        0,
	}); err != nil {
		b.reply("Hotbar selection failed: " + ExplainError(err))
		return
	}
	_ = c.Flush()
	b.reply(fmt.Sprintf("Selected hotbar slot %d.", slot))
}

func playerSlotContainer(slot int) (byte, byte, bool) {
	switch {
	case slot >= 0 && slot <= 8:
		return protocol.ContainerHotBar, byte(slot), true
	case slot >= 9 && slot <= 35:
		return protocol.ContainerInventory, byte(slot), true
	}
	return 0, 0, false
}

func stackReqSlotInfo(container, slot byte, stackID int32) protocol.StackRequestSlotInfo {
	return protocol.StackRequestSlotInfo{
		Container:      protocol.FullContainerName{ContainerID: container},
		Slot:           slot,
		StackNetworkID: stackID,
	}
}

func stackStatusName(status uint8) string {
	switch status {
	case protocol.ItemStackResponseStatusOK:
		return "OK"
	case protocol.ItemStackResponseStatusError:
		return "generic error"
	case protocol.ItemStackResponseStatusInvalidRequestActionType:
		return "invalid request action type"
	case protocol.ItemStackResponseStatusActionRequestNotAllowed:
		return "action request not allowed"
	case protocol.ItemStackResponseStatusInvalidItemNetId:
		return "invalid stack network ID"
	case protocol.ItemStackResponseStatusRequestAlreadyInProgress:
		return "request already in progress"
	case protocol.ItemStackResponseStatusResultTransferFailed:
		return "result transfer failed"
	case protocol.ItemStackResponseStatusFailedToValidateSrcSlot:
		return "failed to validate source slot"
	case protocol.ItemStackResponseStatusFailedToValidateDstSlot:
		return "failed to validate destination slot"
	case protocol.ItemStackResponseStatusInvalidTransferAmount:
		return "invalid transfer amount"
	case protocol.ItemStackResponseStatusCannotSwapItem:
		return "cannot swap item"
	case protocol.ItemStackResponseStatusCannotPlaceItem:
		return "cannot place item"
	case protocol.ItemStackResponseStatusInvalidRemovedAmount:
		return "invalid removed amount"
	case protocol.ItemStackResponseStatusCannotDropItem:
		return "cannot drop item"
	case protocol.ItemStackResponseStatusInvalidSourceContainer:
		return "invalid source container"
	case protocol.ItemStackResponseStatusScreenStackError:
		return "screen stack error"
	}
	return fmt.Sprintf("unknown status %d", status)
}

func (b *Bot) openInventory(c *minecraft.Conn) {
	select {
	case <-b.containerOpen:
	default:
	}
	b.mu.Lock()
	entityID := b.entityID
	b.mu.Unlock()
	_ = c.WritePacket(&packet.Interact{ActionType: packet.InteractActionOpenInventory, TargetEntityRuntimeID: entityID})
	_ = c.Flush()
	select {
	case <-b.containerOpen:
	case <-time.After(2 * time.Second):
	}
}

func (b *Bot) closeInventory(c *minecraft.Conn) {
	_ = c.WritePacket(&packet.ContainerClose{WindowID: 0, ContainerType: 247, ServerSide: false})
	_ = c.Flush()
}

func (b *Bot) clearStackPending(id int32) {
	b.mu.Lock()
	if b.stackPending != nil && b.stackPending.id == id {
		b.stackPending = nil
	}
	b.mu.Unlock()
}

func (b *Bot) sendItemStackRequest(c *minecraft.Conn, actions []protocol.StackRequestAction) (bool, uint8) {
	b.mu.Lock()
	b.itemRequestID -= 2
	id := b.itemRequestID
	resp := make(chan uint8, 1)
	b.stackPending = &stackRequest{id: id, resp: resp}
	b.mu.Unlock()
	if err := c.WritePacket(&packet.ItemStackRequest{Requests: []protocol.ItemStackRequest{{RequestID: id, Actions: actions, FilterCause: -1}}}); err != nil {
		b.clearStackPending(id)
		b.reply("Item request failed: " + ExplainError(err))
		return false, 0
	}
	if err := c.Flush(); err != nil {
		b.clearStackPending(id)
		b.reply("Item request flush failed: " + ExplainError(err))
		return false, 0
	}
	select {
	case status := <-resp:
		return status == protocol.ItemStackResponseStatusOK, status
	case <-time.After(3 * time.Second):
		b.clearStackPending(id)
		return false, 0
	}
}

func (b *Bot) transferCmd(p []string) {
	b.invMu.Lock()
	defer b.invMu.Unlock()
	if len(p) != 3 {
		b.reply("Usage: transfer <slot a> <slot b>")
		return
	}
	slotA, errA := strconv.Atoi(p[1])
	slotB, errB := strconv.Atoi(p[2])
	b.mu.Lock()
	c := b.conn
	inventory := append([]protocol.ItemInstance(nil), b.inventory...)
	b.mu.Unlock()
	if errA != nil || errB != nil || slotA < 0 || slotB < 0 || slotA >= len(inventory) || slotB >= len(inventory) {
		b.reply("Usage: transfer <slot a> <slot b>")
		return
	}
	if c == nil {
		b.reply("Cannot transfer items: bot is not connected.")
		return
	}
	if slotA == slotB {
		b.reply("Cannot transfer an item to its own slot.")
		return
	}
	srcItem := inventory[slotA]
	dstItem := inventory[slotB]
	if srcItem.Stack.Count == 0 {
		b.reply(fmt.Sprintf("Slot %d is empty.", slotA))
		return
	}
	srcContainer, srcSlot, ok := playerSlotContainer(slotA)
	if !ok {
		b.reply(fmt.Sprintf("Slot %d is not a transferable inventory slot.", slotA))
		return
	}
	dstContainer, dstSlot, ok := playerSlotContainer(slotB)
	if !ok {
		b.reply(fmt.Sprintf("Slot %d is not a transferable inventory slot.", slotB))
		return
	}
	var actions []protocol.StackRequestAction
	verb := "Moved"
	if dstItem.Stack.Count == 0 {
		count := srcItem.Stack.Count
		if count > 255 {
			count = 255
		}
		take := &protocol.TakeStackRequestAction{}
		take.Count = byte(count)
		take.Source = stackReqSlotInfo(srcContainer, srcSlot, srcItem.StackNetworkID)
		take.Destination = stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
		place := &protocol.PlaceStackRequestAction{}
		place.Count = byte(count)
		place.Source = stackReqSlotInfo(protocol.ContainerCursor, 0, srcItem.StackNetworkID)
		place.Destination = stackReqSlotInfo(dstContainer, dstSlot, 0)
		actions = []protocol.StackRequestAction{take, place}
	} else {
		swap := &protocol.SwapStackRequestAction{
			Source:      stackReqSlotInfo(srcContainer, srcSlot, srcItem.StackNetworkID),
			Destination: stackReqSlotInfo(dstContainer, dstSlot, dstItem.StackNetworkID),
		}
		actions = []protocol.StackRequestAction{swap}
		verb = "Swapped"
	}
	b.openInventory(c)
	defer b.closeInventory(c)
	ok, status := b.sendItemStackRequest(c, actions)
	if !ok {
		b.reply(fmt.Sprintf("Transfer failed (slot %d <-> %d): %s.", slotA, slotB, stackStatusName(status)))
		return
	}
	b.reply(fmt.Sprintf("%s %d item(s) from slot %d to slot %d.", verb, srcItem.Stack.Count, slotA, slotB))
}

func (b *Bot) dropSlots(start, count, requested int) {
	b.invMu.Lock()
	defer b.invMu.Unlock()
	b.mu.Lock()
	c := b.conn
	type dropRequest struct {
		slot  int
		item  protocol.ItemInstance
		count uint16
	}
	drops := make([]dropRequest, 0, count)
	for slot := start; slot < start+count && slot < len(b.inventory); slot++ {
		item := b.inventory[slot]
		if item.Stack.Count == 0 {
			continue
		}
		dropCount := int(item.Stack.Count)
		if requested > 0 {
			if requested > dropCount {
				requested = dropCount
			}
			dropCount = requested
		}
		drops = append(drops, dropRequest{slot: slot, item: item, count: uint16(dropCount)})
	}
	b.mu.Unlock()
	if c == nil {
		b.reply("Cannot drop items: bot is not connected.")
		return
	}
	if len(drops) == 0 {
		b.reply("No items found to drop.")
		return
	}
	b.openInventory(c)
	defer b.closeInventory(c)
	var actions []protocol.StackRequestAction
	var validDrops []dropRequest
	for _, drop := range drops {
		container, slotIn, ok := playerSlotContainer(drop.slot)
		if !ok {
			continue
		}
		count := drop.count
		if count > 255 {
			count = 255
		}
		fromSlot := stackReqSlotInfo(container, slotIn, drop.item.StackNetworkID)
		cursorEmpty := stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
		cursorFull := stackReqSlotInfo(protocol.ContainerCursor, 0, drop.item.StackNetworkID)
		take := &protocol.TakeStackRequestAction{}
		take.Count = byte(count)
		take.Source = fromSlot
		take.Destination = cursorEmpty
		dropAction := &protocol.DropStackRequestAction{Count: byte(count), Source: cursorFull}
		actions = append(actions, take, dropAction)
		validDrops = append(validDrops, drop)
	}

	if len(actions) == 0 {
		b.reply("No valid items to drop.")
		return
	}

	ok, status := b.sendItemStackRequest(c, actions)
	if !ok {
		b.reply(fmt.Sprintf("Drop request failed: %s", stackStatusName(status)))
		return
	}

	b.mu.Lock()
	for _, drop := range validDrops {
		count := drop.count
		if count > 255 {
			count = 255
		}
		if b.inventory[drop.slot].Stack.Count > count {
			b.inventory[drop.slot].Stack.Count -= count
		} else {
			b.inventory[drop.slot] = protocol.ItemInstance{}
		}
		if byte(drop.slot) == b.hotbarSlot {
			b.heldItem = b.inventory[drop.slot]
		}
	}
	b.mu.Unlock()
	b.sendHeldItem(c)

	b.reply(fmt.Sprintf("Dropped items from %d slot(s).", len(validDrops)))
}

func (b *Bot) sendHeldItem(c *minecraft.Conn) {
	b.mu.Lock()
	entityID := b.entityID
	slot := b.hotbarSlot
	item := b.heldItem
	b.mu.Unlock()
	if c == nil {
		return
	}
	if err := c.WritePacket(&packet.MobEquipment{
		EntityRuntimeID: entityID,
		NewItem:         item,
		InventorySlot:   slot,
		HotBarSlot:      slot,
		WindowID:        0,
	}); err != nil {
		log.Println("bebot: held-item update failed:", ExplainError(err))
	}
}

func (b *Bot) interactBlock(pos protocol.BlockPos) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return
	}
	_ = b.conn.WritePacket(&packet.Interact{ActionType: packet.InteractActionMouseOverEntity, TargetEntityRuntimeID: 0})
	_ = b.conn.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStartSleeping, BlockPosition: pos, ResultPosition: pos, BlockFace: 1})
	_ = b.conn.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStartItemUseOn, BlockPosition: pos, ResultPosition: pos, BlockFace: 1})
	_ = b.conn.Flush()
}

func (b *Bot) itemName(netID int32) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if name, ok := b.itemNamesByID[netID]; ok && name != "" {
		return strings.TrimPrefix(name, "minecraft:")
	}
	return fmt.Sprintf("item_%d", netID)
}

func (b *Bot) getFrontBlockPos() protocol.BlockPos {
	b.mu.Lock()
	pos := b.pos
	yaw := b.facingYaw
	pitch := b.facingPitch
	b.mu.Unlock()

	yawRad := float64(yaw) * math.Pi / 180.0
	pitchRad := float64(pitch) * math.Pi / 180.0

	dx := -math.Sin(yawRad) * math.Cos(pitchRad)
	dy := -math.Sin(pitchRad)
	dz := math.Cos(yawRad) * math.Cos(pitchRad)

	eyeY := float64(pos.Y()) + 1.62
	fx := float64(pos.X()) + dx*1.5
	fy := eyeY + dy*1.5
	fz := float64(pos.Z()) + dz*1.5

	return protocol.BlockPos{
		int32(math.Floor(fx)),
		int32(math.Floor(fy)),
		int32(math.Floor(fz)),
	}
}

func (b *Bot) parseContainerPos(p []string) (protocol.BlockPos, []string, error) {
	if len(p) >= 4 {
		x, errX := strconv.Atoi(p[1])
		y, errY := strconv.Atoi(p[2])
		z, errZ := strconv.Atoi(p[3])
		if errX == nil && errY == nil && errZ == nil {
			return protocol.BlockPos{int32(x), int32(y), int32(z)}, p[4:], nil
		}
	}
	if len(p) >= 2 && strings.EqualFold(p[1], "front") {
		return b.getFrontBlockPos(), p[2:], nil
	}
	return b.getFrontBlockPos(), p[1:], nil
}

func (b *Bot) openBlockContainer(c *minecraft.Conn, pos protocol.BlockPos) (byte, error) {
	b.mu.Lock()
	botPos := b.pos
	hotbarSlot := b.hotbarSlot
	heldItem := b.heldItem
	entityID := b.entityID
	select {
	case <-b.containerOpen:
	default:
	}
	select {
	case <-b.containerContent:
	default:
	}
	b.containerActive = false
	b.containerItems = make(map[byte]protocol.ItemInstance)
	b.containerPos = pos
	b.containerContainerID = protocol.ContainerLevelEntity
	b.mu.Unlock()

	centerPos := mgl32.Vec3{float32(pos.X()) + 0.5, float32(pos.Y()) + 0.5, float32(pos.Z()) + 0.5}
	eyePos := botPos.Add(mgl32.Vec3{0, 1.62, 0})
	diff := centerPos.Sub(eyePos)
	dist := diff.Len()
	if dist > 6.0 {
		return 0, fmt.Errorf("container at %v is too far (%.1f blocks, max 6.0)", pos, dist)
	}

	face := determineBlockFace(eyePos, pos)
	clickPos := faceClickPosition(face)

	dx := float64(centerPos.X()) - float64(botPos.X())
	dy := float64(centerPos.Y()) - float64(eyePos.Y())
	dz := float64(centerPos.Z()) - float64(botPos.Z())
	horizontal := math.Sqrt(dx*dx + dz*dz)
	yaw := float32(0)
	if horizontal > 0.001 {
		yaw = float32(math.Atan2(-dx, dz) * 180 / math.Pi)
		if yaw < 0 {
			yaw += 360
		}
	}
	pitch := float32(-math.Atan2(dy, horizontal) * 180 / math.Pi)

	b.mu.Lock()
	b.facingYaw = yaw
	b.facingPitch = pitch
	b.idleYaw = yaw
	b.idlePitch = pitch
	runtimeID := uint32(0)
	if rid, ok := b.blockRuntimeIDs[pos]; ok {
		runtimeID = rid
	}
	b.pendingInteraction = &blockInteractTarget{pos: pos, face: face}
	b.mu.Unlock()

	log.Printf("bebot: opening container at %v from bot pos (%.2f, %.2f, %.2f) (dist=%.2f, face=%d, runtimeID=%d)\n", pos, botPos.X(), botPos.Y(), botPos.Z(), dist, face, runtimeID)

	_ = c.WritePacket(&packet.InventoryTransaction{
		TransactionData: &protocol.UseItemTransactionData{
			ActionType:      protocol.UseItemActionClickBlock,
			BlockPosition:   pos,
			BlockFace:       face,
			HotBarSlot:      int32(hotbarSlot),
			HeldItem:        heldItem,
			Position:        botPos,
			ClickedPosition: clickPos,
			BlockRuntimeID:  runtimeID,
		},
	})
	_ = c.WritePacket(&packet.PlayerAction{
		EntityRuntimeID: entityID,
		ActionType:      protocol.PlayerActionInteractWithBlock,
		BlockPosition:   pos,
		ResultPosition:  pos,
		BlockFace:       face,
	})
	_ = c.WritePacket(&packet.PlayerAction{
		EntityRuntimeID: entityID,
		ActionType:      protocol.PlayerActionStartItemUseOn,
		BlockPosition:   pos,
		ResultPosition:  pos,
		BlockFace:       face,
	})
	_ = c.WritePacket(&packet.Animate{
		ActionType:      packet.AnimateActionSwingArm,
		EntityRuntimeID: entityID,
	})
	if err := c.Flush(); err != nil {
		return 0, fmt.Errorf("flush failed: %w", err)
	}

	timer := time.NewTimer(3500 * time.Millisecond)
	defer timer.Stop()
	retryTimer := time.NewTimer(1200 * time.Millisecond)
	defer retryTimer.Stop()

	for {
		select {
		case <-b.containerOpen:
			b.mu.Lock()
			itemsCount := len(b.containerItems)
			wID := b.containerWindowID
			b.mu.Unlock()

			if itemsCount == 0 {
				select {
				case <-b.containerContent:
				case <-time.After(1500 * time.Millisecond):
				}
			}
			return wID, nil

		case <-retryTimer.C:
			b.mu.Lock()
			if !b.containerActive {
				b.pendingInteraction = &blockInteractTarget{pos: pos, face: face}
			}
			b.mu.Unlock()

		case <-timer.C:
			return 0, fmt.Errorf("timeout waiting for container to open at %v (ensure target block has an inventory)", pos)
		}
	}
}

func (b *Bot) closeBlockContainer(c *minecraft.Conn, windowID byte) {
	b.mu.Lock()
	cType := b.containerType
	b.containerActive = false
	b.mu.Unlock()
	_ = c.WritePacket(&packet.ContainerClose{
		WindowID:      windowID,
		ContainerType: cType,
		ServerSide:    false,
	})
	_ = c.Flush()
}

func (b *Bot) findFreePlayerSlot() (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.inventory) < 36 {
		grown := make([]protocol.ItemInstance, 36)
		copy(grown, b.inventory)
		b.inventory = grown
	}
	for i := 0; i < 36; i++ {
		if b.inventory[i].Stack.Count == 0 {
			return i, true
		}
	}
	return -1, false
}

func (b *Bot) lootCmd(p []string) {
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	if c == nil {
		b.reply("Cannot loot: bot is not connected.")
		return
	}

	pos, _, err := b.parseContainerPos(p)
	if err != nil {
		b.reply("Usage: loot [x y z] or loot front")
		return
	}

	b.invMu.Lock()
	defer b.invMu.Unlock()

	wID, err := b.openBlockContainer(c, pos)
	if err != nil {
		b.reply("Failed to open container: " + err.Error())
		return
	}
	defer b.closeBlockContainer(c, wID)

	b.mu.Lock()
	containerID := b.containerContainerID
	if containerID == 0 {
		containerID = protocol.ContainerLevelEntity
	}
	var slots []byte
	for s, item := range b.containerItems {
		if item.Stack.Count > 0 {
			slots = append(slots, s)
		}
	}
	b.mu.Unlock()

	if len(slots) == 0 {
		b.reply(fmt.Sprintf("Container at (%d, %d, %d) is empty.", pos.X(), pos.Y(), pos.Z()))
		return
	}

	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })

	lootedCount := 0
	var lootedSummary []string
	for _, cSlot := range slots {
		b.mu.Lock()
		if !b.containerActive {
			b.mu.Unlock()
			break
		}
		cItem := b.containerItems[cSlot]
		b.mu.Unlock()

		if cItem.Stack.Count == 0 {
			continue
		}

		dstSlot, ok := b.findFreePlayerSlot()
		if !ok {
			b.reply("Player inventory is full!")
			break
		}

		dstContainer, dstSlotInContainer, ok := playerSlotContainer(dstSlot)
		if !ok {
			continue
		}

		count := int(cItem.Stack.Count)
		if count > 255 {
			count = 255
		}

		take := &protocol.TakeStackRequestAction{}
		take.Count = byte(count)
		take.Source = stackReqSlotInfo(containerID, cSlot, cItem.StackNetworkID)
		take.Destination = stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
		place := &protocol.PlaceStackRequestAction{}
		place.Count = byte(count)
		place.Source = stackReqSlotInfo(protocol.ContainerCursor, 0, cItem.StackNetworkID)
		place.Destination = stackReqSlotInfo(dstContainer, dstSlotInContainer, 0)

		reqOk, status := b.sendItemStackRequest(c, []protocol.StackRequestAction{take, place})
		if !reqOk {
			log.Printf("bebot: loot slot %d failed: %s\n", cSlot, stackStatusName(status))
			continue
		}

		b.mu.Lock()
		if len(b.inventory) <= dstSlot {
			grown := make([]protocol.ItemInstance, dstSlot+1)
			copy(grown, b.inventory)
			b.inventory = grown
		}
		b.inventory[dstSlot] = cItem
		delete(b.containerItems, cSlot)
		b.mu.Unlock()

		lootedCount++
		name := b.itemName(cItem.Stack.ItemType.NetworkID)
		if len(lootedSummary) < 5 {
			lootedSummary = append(lootedSummary, fmt.Sprintf("%dx %s", count, name))
		}

		time.Sleep(40 * time.Millisecond)
	}

	if lootedCount == 0 {
		b.reply(fmt.Sprintf("Could not take any items from container at (%d, %d, %d).", pos.X(), pos.Y(), pos.Z()))
		return
	}

	details := strings.Join(lootedSummary, ", ")
	if lootedCount > len(lootedSummary) {
		details += fmt.Sprintf(" and %d more", lootedCount-len(lootedSummary))
	}
	b.reply(fmt.Sprintf("Looted %d item stack(s) from container at (%d, %d, %d): %s.", lootedCount, pos.X(), pos.Y(), pos.Z(), details))
}

func (b *Bot) takeCmd(p []string) {
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	if c == nil {
		b.reply("Cannot take item: bot is not connected.")
		return
	}

	pos, rem, err := b.parseContainerPos(p)
	if err != nil || len(rem) < 1 {
		b.reply("Usage: take [x y z] <slot> [count] or take <slot> [count]")
		return
	}

	slotNum, err := strconv.Atoi(rem[0])
	if err != nil || slotNum < 0 || slotNum > 255 {
		b.reply("Invalid slot number. Expected 0-255.")
		return
	}
	cSlot := byte(slotNum)

	reqCount := 0
	if len(rem) >= 2 {
		if val, err := strconv.Atoi(rem[1]); err == nil && val > 0 {
			reqCount = val
		}
	}

	b.invMu.Lock()
	defer b.invMu.Unlock()

	wID, err := b.openBlockContainer(c, pos)
	if err != nil {
		b.reply("Failed to open container: " + err.Error())
		return
	}
	defer b.closeBlockContainer(c, wID)

	b.mu.Lock()
	containerID := b.containerContainerID
	if containerID == 0 {
		containerID = protocol.ContainerLevelEntity
	}
	cItem, hasItem := b.containerItems[cSlot]
	b.mu.Unlock()

	if !hasItem || cItem.Stack.Count == 0 {
		b.reply(fmt.Sprintf("Slot %d in container at (%d, %d, %d) is empty.", cSlot, pos.X(), pos.Y(), pos.Z()))
		return
	}

	count := int(cItem.Stack.Count)
	if reqCount > 0 && reqCount < count {
		count = reqCount
	}
	if count > 255 {
		count = 255
	}

	dstSlot, ok := b.findFreePlayerSlot()
	if !ok {
		b.reply("Player inventory is full!")
		return
	}

	dstContainer, dstSlotInContainer, ok := playerSlotContainer(dstSlot)
	if !ok {
		b.reply("Could not map player destination slot.")
		return
	}

	take := &protocol.TakeStackRequestAction{}
	take.Count = byte(count)
	take.Source = stackReqSlotInfo(containerID, cSlot, cItem.StackNetworkID)
	take.Destination = stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
	place := &protocol.PlaceStackRequestAction{}
	place.Count = byte(count)
	place.Source = stackReqSlotInfo(protocol.ContainerCursor, 0, cItem.StackNetworkID)
	place.Destination = stackReqSlotInfo(dstContainer, dstSlotInContainer, 0)

	reqOk, status := b.sendItemStackRequest(c, []protocol.StackRequestAction{take, place})
	if !reqOk {
		b.reply(fmt.Sprintf("Take failed (slot %d): %s.", cSlot, stackStatusName(status)))
		return
	}

	b.mu.Lock()
	if len(b.inventory) <= dstSlot {
		grown := make([]protocol.ItemInstance, dstSlot+1)
		copy(grown, b.inventory)
		b.inventory = grown
	}
	newItem := cItem
	newItem.Stack.Count = uint16(count)
	b.inventory[dstSlot] = newItem

	if int(cItem.Stack.Count) > count {
		cItem.Stack.Count -= uint16(count)
		b.containerItems[cSlot] = cItem
	} else {
		delete(b.containerItems, cSlot)
	}
	b.mu.Unlock()

	name := b.itemName(cItem.Stack.ItemType.NetworkID)
	b.reply(fmt.Sprintf("Took %dx %s from slot %d of container at (%d, %d, %d) into inventory slot %d.", count, name, cSlot, pos.X(), pos.Y(), pos.Z(), dstSlot))
}

func (b *Bot) viewContainerCmd(p []string) {
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	if c == nil {
		b.reply("Cannot view container: bot is not connected.")
		return
	}

	pos, _, err := b.parseContainerPos(p)
	if err != nil {
		b.reply("Usage: container [x y z] or container front")
		return
	}

	b.invMu.Lock()
	defer b.invMu.Unlock()

	wID, err := b.openBlockContainer(c, pos)
	if err != nil {
		b.reply("Failed to open container: " + err.Error())
		return
	}
	defer b.closeBlockContainer(c, wID)

	b.mu.Lock()
	var slots []byte
	for s, item := range b.containerItems {
		if item.Stack.Count > 0 {
			slots = append(slots, s)
		}
	}
	b.mu.Unlock()

	if len(slots) == 0 {
		b.reply(fmt.Sprintf("Container at (%d, %d, %d) is empty.", pos.X(), pos.Y(), pos.Z()))
		return
	}

	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })

	var itemsSummary []string
	b.mu.Lock()
	for _, s := range slots {
		it := b.containerItems[s]
		name := b.itemName(it.Stack.ItemType.NetworkID)
		itemsSummary = append(itemsSummary, fmt.Sprintf("Slot %d: %dx %s", s, it.Stack.Count, name))
		if len(itemsSummary) >= 10 {
			remaining := len(slots) - 10
			if remaining > 0 {
				itemsSummary = append(itemsSummary, fmt.Sprintf("...and %d more slot(s)", remaining))
			}
			break
		}
	}
	b.mu.Unlock()

	b.reply(fmt.Sprintf("Container at (%d, %d, %d) has %d item(s):\n%s", pos.X(), pos.Y(), pos.Z(), len(slots), strings.Join(itemsSummary, "\n")))
}
