package bot

import (
	"fmt"
	"log"
	"strconv"
	"time"

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
