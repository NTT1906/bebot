package bot

import (
	"context"
	"fmt"
	"log"
	"math"
	"reflect"
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

type slotCandidate struct {
	container byte
	slot      byte
}

func playerSlotCandidates(slot int) []slotCandidate {
	if slot >= 0 && slot <= 8 {
		return []slotCandidate{
			{container: protocol.ContainerHotBar, slot: byte(slot)},
			{container: protocol.ContainerCombinedHotBarAndInventory, slot: byte(slot)},
		}
	}
	if slot >= 9 && slot <= 35 {
		return []slotCandidate{
			{container: protocol.ContainerCombinedHotBarAndInventory, slot: byte(slot)},
			{container: protocol.ContainerInventory, slot: byte(slot - 9)},
			{container: protocol.ContainerInventory, slot: byte(slot)},
			{container: protocol.ContainerCombinedHotBarAndInventory, slot: byte(slot - 9)},
		}
	}
	return nil
}

func playerSlotContainer(slot int) (byte, byte, bool) {
	cands := playerSlotCandidates(slot)
	if len(cands) > 0 {
		return cands[0].container, cands[0].slot, true
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

const (
	DropMethodFirstPerson = 1 // Method 1: packet.InventoryTransaction (First-Person World Drop, no container opened)
	DropMethodCursor      = 2 // Method 2: packet.ItemStackRequest via ContainerCursor (Cursor Drag & Drop, auto open/close container)
	DropMethodDirectStack = 3 // Method 3: packet.ItemStackRequest direct slot drop (GUI Q drop, auto open/close container)
)

func (b *Bot) openInventory(c *minecraft.Conn) (byte, bool) {
	b.mu.Lock()
	if b.containerActive && b.containerWindowID != 0 {
		wID := b.containerWindowID
		b.mu.Unlock()
		return wID, false
	}
	b.containerActive = false
	b.containerWindowID = protocol.WindowIDInventory
	b.mu.Unlock()

	_ = c.WritePacket(&packet.Interact{
		ActionType:            packet.InteractActionOpenInventory,
		TargetEntityRuntimeID: 0,
	})
	_ = c.Flush()
	time.Sleep(50 * time.Millisecond)
	return protocol.WindowIDInventory, true
}

func (b *Bot) closeInventory(c *minecraft.Conn, wID byte) {
	b.mu.Lock()
	if wID == 0 || b.containerWindowID == wID {
		b.containerActive = false
		b.containerWindowID = 0
	}
	b.mu.Unlock()
	_ = c.WritePacket(&packet.ContainerClose{
		WindowID:      wID,
		ContainerType: 255, // protocol.ContainerTypeInventory (-1 as uint8)
		ServerSide:    false,
	})
	_ = c.Flush()
	time.Sleep(30 * time.Millisecond)
}

func (b *Bot) dropSlotTx(c *minecraft.Conn, slot int, oldItem protocol.ItemInstance, count uint16, sourceFlags uint32) bool {
	newItem := oldItem
	droppedItem := oldItem
	droppedItem.Stack.Count = count
	if oldItem.Stack.Count > count {
		newItem.Stack.Count -= count
	} else {
		newItem = protocol.ItemInstance{}
	}

	tx := &packet.InventoryTransaction{
		Actions: []protocol.InventoryAction{
			{
				SourceType:    protocol.InventoryActionSourceContainer,
				WindowID:      protocol.Option(int8(protocol.WindowIDInventory)),
				InventorySlot: uint32(slot),
				OldItem:       oldItem,
				NewItem:       newItem,
			},
			{
				SourceType:    protocol.InventoryActionSourceWorld,
				SourceFlags:   protocol.Option(sourceFlags),
				InventorySlot: 0,
				OldItem:       protocol.ItemInstance{},
				NewItem:       droppedItem,
			},
		},
		TransactionData: &protocol.NormalTransactionData{},
	}
	if err := c.WritePacket(tx); err != nil {
		log.Printf("bebot: dropSlotTx slot %d write failed: %v\n", slot, err)
		return false
	}
	_ = c.Flush()
	return true
}

func (b *Bot) clearStackPending(id int32) {
	b.mu.Lock()
	delete(b.stackPending, id)
	b.mu.Unlock()
}

func (b *Bot) sendItemStackRequest(c *minecraft.Conn, actions []protocol.StackRequestAction) (bool, uint8) {
	b.mu.Lock()
	b.itemRequestID -= 2
	id := b.itemRequestID
	resp := make(chan uint8, 1)
	if b.stackPending == nil {
		b.stackPending = make(map[int32]chan uint8)
	}
	b.stackPending[id] = resp
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
	wID, opened := b.openInventory(c)
	if opened {
		defer b.closeInventory(c, wID)
	}
	ok, status := b.sendItemStackRequest(c, actions)
	if !ok {
		b.reply(fmt.Sprintf("Transfer failed (slot %d <-> %d): %s.", slotA, slotB, stackStatusName(status)))
		return
	}
	b.reply(fmt.Sprintf("%s %d item(s) from slot %d to slot %d.", verb, srcItem.Stack.Count, slotA, slotB))
}

func (b *Bot) dropSlots(start, count, requested, method int, random, each bool) {
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
			cnt := requested
			if cnt > dropCount {
				cnt = dropCount
			}
			dropCount = cnt
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

	modeStr := "eye direction"
	if random {
		modeStr = "random scatter"
	}
	seqStr := ""
	if each {
		seqStr = " sequentially"
	}

	if method == DropMethodCursor {
		// Method 2: Cursor drag & drop (Take to cursor #59, then Drop from cursor #59 via ItemStackRequest).
		// Automatically opens inventory screen if not already active and closes it when done.
		wID, opened := b.openInventory(c)
		if opened {
			defer b.closeInventory(c, wID)
		}

		sourceFlags := uint32(0)
		if random {
			sourceFlags = 1
		}

		// Try batching all drops into a single atomic ItemStackRequest if multiple slots, not 'each', and counts <= 64
		canBatch := len(drops) > 1 && !each
		for _, drop := range drops {
			if drop.count > 64 {
				canBatch = false
				break
			}
		}

		if canBatch {
			var batchActions []protocol.StackRequestAction
			for _, drop := range drops {
				candidates := playerSlotCandidates(drop.slot)
				if len(candidates) == 0 {
					continue
				}
				cand := candidates[0]
				fromSlot := stackReqSlotInfo(cand.container, cand.slot, drop.item.StackNetworkID)
				cursorEmpty := stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
				cursorFull := stackReqSlotInfo(protocol.ContainerCursor, 0, drop.item.StackNetworkID)
				take := &protocol.TakeStackRequestAction{}
				take.Count = byte(drop.count)
				take.Source = fromSlot
				take.Destination = cursorEmpty
				dropAction := &protocol.DropStackRequestAction{
					Count:    byte(drop.count),
					Source:   cursorFull,
					Randomly: random,
				}
				batchActions = append(batchActions, take, dropAction)
			}
			if len(batchActions) > 0 {
				ok, status := b.sendItemStackRequest(c, batchActions)
				if ok {
					b.mu.Lock()
					for _, drop := range drops {
						if b.inventory[drop.slot].Stack.Count > drop.count {
							b.inventory[drop.slot].Stack.Count -= drop.count
						} else {
							b.inventory[drop.slot] = protocol.ItemInstance{}
						}
						if byte(drop.slot) == b.hotbarSlot {
							b.heldItem = b.inventory[drop.slot]
						}
					}
					b.mu.Unlock()
					b.sendHeldItem(c)
					b.reply(fmt.Sprintf("Dropped items from %d slot(s) [cursor batch, %s].", len(drops), modeStr))
					return
				}
				log.Printf("bebot: batch drop2 failed: status=%d (%s), falling back to slot-by-slot\n", status, stackStatusName(status))
			}
		}

		slotDelay := 10 * time.Millisecond
		if each {
			slotDelay = 50 * time.Millisecond
		}

		var droppedSlots []dropRequest
		var lastStatus uint8
		for _, drop := range drops {
			candidates := playerSlotCandidates(drop.slot)
			if len(candidates) == 0 {
				continue
			}
			rem := drop.count
			slotSuccess := true
			for rem > 0 {
				cnt := rem
				if cnt > 64 {
					cnt = 64
				}
				rem -= cnt

				b.mu.Lock()
				curItem := b.inventory[drop.slot]
				b.mu.Unlock()
				if curItem.Stack.Count == 0 {
					break
				}

				cursorEmpty := stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
				cursorFull := stackReqSlotInfo(protocol.ContainerCursor, 0, curItem.StackNetworkID)

				ok := false
				for candIdx, cand := range candidates {
					fromSlot := stackReqSlotInfo(cand.container, cand.slot, curItem.StackNetworkID)
					take := &protocol.TakeStackRequestAction{}
					take.Count = byte(cnt)
					take.Source = fromSlot
					take.Destination = cursorEmpty
					dropAction := &protocol.DropStackRequestAction{
						Count:    byte(cnt),
						Source:   cursorFull,
						Randomly: random,
					}

					candOk, status := b.sendItemStackRequest(c, []protocol.StackRequestAction{take, dropAction})
					lastStatus = status
					if candOk {
						ok = true
						if candIdx > 0 {
							log.Printf("bebot: drop2 slot %d succeeded via container=%d slot=%d\n", drop.slot, cand.container, cand.slot)
						}
						break
					}
					if status == protocol.ItemStackResponseStatusFailedToValidateDstSlot {
						// ContainerCursor is not active on this screen validator (status 50).
						// Try direct drop from source slot via ItemStackRequest.
						directDrop := &protocol.DropStackRequestAction{
							Count:    byte(cnt),
							Source:   fromSlot,
							Randomly: random,
						}
						candOk, status = b.sendItemStackRequest(c, []protocol.StackRequestAction{directDrop})
						lastStatus = status
						if candOk {
							ok = true
							break
						}
					}
					log.Printf("bebot: drop2 slot %d cand (c=%d s=%d) failed: status=%d (%s)\n",
						drop.slot, cand.container, cand.slot, status, stackStatusName(status))
				}
				if !ok {
					// If BDS rejects ItemStackRequest on storage slots outside hotbar (e.g. ScreenHandlerHUD lacks ContainerInventory mapping),
					// complete the drop seamlessly via first-person inventory transaction so no items are left behind.
					log.Printf("bebot: drop2 slot %d: all ItemStackRequest candidates failed (last status=%d %s), completing drop via inventory transaction\n",
						drop.slot, lastStatus, stackStatusName(lastStatus))
					if b.dropSlotTx(c, drop.slot, curItem, cnt, sourceFlags) {
						ok = true
					}
				}
				if !ok {
					log.Printf("bebot: drop2 slot %d failed: status=%d (%s)\n", drop.slot, lastStatus, stackStatusName(lastStatus))
					slotSuccess = false
					break
				}
				time.Sleep(slotDelay)
			}
			if !slotSuccess {
				continue
			}

			b.mu.Lock()
			if b.inventory[drop.slot].Stack.Count > drop.count {
				b.inventory[drop.slot].Stack.Count -= drop.count
			} else {
				b.inventory[drop.slot] = protocol.ItemInstance{}
			}
			if byte(drop.slot) == b.hotbarSlot {
				b.heldItem = b.inventory[drop.slot]
			}
			b.mu.Unlock()
			droppedSlots = append(droppedSlots, drop)
		}
		b.sendHeldItem(c)
		if len(droppedSlots) == 0 {
			b.reply(fmt.Sprintf("Drop request failed: %s", stackStatusName(lastStatus)))
			return
		}
		b.reply(fmt.Sprintf("Dropped items from %d slot(s)%s [cursor drag & drop, %s].", len(droppedSlots), seqStr, modeStr))
		return
	}

	if method == DropMethodDirectStack {
		// Method 3: Direct Slot Drop via ItemStackRequest (DropStackRequestAction directly on source slot).
		// Automatically opens inventory screen if not already active and closes it when done.
		wID, opened := b.openInventory(c)
		if opened {
			defer b.closeInventory(c, wID)
		}

		sourceFlags := uint32(0)
		if random {
			sourceFlags = 1
		}

		// Try batching all drops in a single ItemStackRequest if multiple slots, not 'each', and all <= 64
		canBatch := len(drops) > 1 && !each
		for _, drop := range drops {
			if drop.count > 64 {
				canBatch = false
				break
			}
		}

		if canBatch {
			var batchActions []protocol.StackRequestAction
			for _, drop := range drops {
				container, slotIn, ok := playerSlotContainer(drop.slot)
				if !ok {
					continue
				}
				batchActions = append(batchActions, &protocol.DropStackRequestAction{
					Count:    byte(drop.count),
					Source:   stackReqSlotInfo(container, slotIn, drop.item.StackNetworkID),
					Randomly: random,
				})
			}
			if len(batchActions) > 0 {
				ok, status := b.sendItemStackRequest(c, batchActions)
				if ok {
					b.mu.Lock()
					for _, drop := range drops {
						if b.inventory[drop.slot].Stack.Count > drop.count {
							b.inventory[drop.slot].Stack.Count -= drop.count
						} else {
							b.inventory[drop.slot] = protocol.ItemInstance{}
						}
						if byte(drop.slot) == b.hotbarSlot {
							b.heldItem = b.inventory[drop.slot]
						}
					}
					b.mu.Unlock()
					b.sendHeldItem(c)
					b.reply(fmt.Sprintf("Dropped items from %d slot(s) [direct stack batch, %s].", len(drops), modeStr))
					return
				}
				log.Printf("bebot: batch drop3 failed: status=%d (%s), falling back to slot-by-slot\n", status, stackStatusName(status))
			}
		}

		slotDelay := 10 * time.Millisecond
		if each {
			slotDelay = 50 * time.Millisecond
		}

		// Slot-by-slot execution (for single slot, count > 64, or fallback from batch)
		var droppedSlots []dropRequest
		var lastStatus uint8
		for _, drop := range drops {
			candidates := playerSlotCandidates(drop.slot)
			if len(candidates) == 0 {
				continue
			}
			rem := drop.count
			slotSuccess := true
			for rem > 0 {
				cnt := rem
				if cnt > 64 {
					cnt = 64
				}
				rem -= cnt

				b.mu.Lock()
				curItem := b.inventory[drop.slot]
				b.mu.Unlock()
				if curItem.Stack.Count == 0 {
					break
				}

				ok := false
				for candIdx, cand := range candidates {
					fromSlot := stackReqSlotInfo(cand.container, cand.slot, curItem.StackNetworkID)
					dropAction := &protocol.DropStackRequestAction{
						Count:    byte(cnt),
						Source:   fromSlot,
						Randomly: random,
					}
					candOk, status := b.sendItemStackRequest(c, []protocol.StackRequestAction{dropAction})
					lastStatus = status
					if candOk {
						ok = true
						if candIdx > 0 {
							log.Printf("bebot: drop3 slot %d succeeded via container=%d slot=%d\n", drop.slot, cand.container, cand.slot)
						}
						break
					}
					log.Printf("bebot: drop3 slot %d cand (c=%d s=%d) failed: status=%d (%s)\n",
						drop.slot, cand.container, cand.slot, status, stackStatusName(status))
				}

				if !ok {
					// If BDS rejects ItemStackRequest on storage slots outside hotbar,
					// complete the drop seamlessly via first-person inventory transaction so no items are left behind.
					log.Printf("bebot: drop3 slot %d: all ItemStackRequest candidates failed (last status=%d %s), completing drop via inventory transaction\n",
						drop.slot, lastStatus, stackStatusName(lastStatus))
					if b.dropSlotTx(c, drop.slot, curItem, cnt, sourceFlags) {
						ok = true
					}
				}
				if !ok {
					log.Printf("bebot: drop3 slot %d failed: status=%d (%s)\n", drop.slot, lastStatus, stackStatusName(lastStatus))
					slotSuccess = false
					break
				}
				time.Sleep(slotDelay)
			}
			if !slotSuccess {
				continue
			}

			b.mu.Lock()
			if b.inventory[drop.slot].Stack.Count > drop.count {
				b.inventory[drop.slot].Stack.Count -= drop.count
			} else {
				b.inventory[drop.slot] = protocol.ItemInstance{}
			}
			if byte(drop.slot) == b.hotbarSlot {
				b.heldItem = b.inventory[drop.slot]
			}
			b.mu.Unlock()
			droppedSlots = append(droppedSlots, drop)
		}
		b.sendHeldItem(c)
		if len(droppedSlots) == 0 {
			b.reply(fmt.Sprintf("Drop request failed: %s", stackStatusName(lastStatus)))
			return
		}
		b.reply(fmt.Sprintf("Dropped items from %d slot(s)%s [direct stack request, %s].", len(droppedSlots), seqStr, modeStr))
		return
	}

	// Method 1: Direct First-Person World Drop (packet.InventoryTransaction):
	// Natively drops any inventory slot (including bundles) into the world.
	// Coalesced into a single network flush without needing open container screens.
	// SourceFlags: 0 = NoFlag (drops forward in eye/look direction), 1 = WorldInteractionRandom (scatters randomly).
	sourceFlags := uint32(0)
	if random {
		sourceFlags = 1
	}

	var droppedSlots []dropRequest
	for _, drop := range drops {
		oldItem := drop.item
		newItem := oldItem
		droppedItem := oldItem
		droppedItem.Stack.Count = drop.count
		if oldItem.Stack.Count > drop.count {
			newItem.Stack.Count -= drop.count
		} else {
			newItem = protocol.ItemInstance{}
		}

		tx := &packet.InventoryTransaction{
			Actions: []protocol.InventoryAction{
				{
					SourceType:    protocol.InventoryActionSourceContainer,
					WindowID:      protocol.Option(int8(protocol.WindowIDInventory)),
					InventorySlot: uint32(drop.slot),
					OldItem:       oldItem,
					NewItem:       newItem,
				},
				{
					SourceType:    protocol.InventoryActionSourceWorld,
					SourceFlags:   protocol.Option(sourceFlags),
					InventorySlot: 0,
					OldItem:       protocol.ItemInstance{},
					NewItem:       droppedItem,
				},
			},
			TransactionData: &protocol.NormalTransactionData{},
		}
		if err := c.WritePacket(tx); err != nil {
			log.Printf("bebot: drop slot %d write failed: %v\n", drop.slot, err)
			continue
		}
		droppedSlots = append(droppedSlots, drop)

		if each {
			_ = c.Flush()
			b.mu.Lock()
			if b.inventory[drop.slot].Stack.Count > drop.count {
				b.inventory[drop.slot].Stack.Count -= drop.count
			} else {
				b.inventory[drop.slot] = protocol.ItemInstance{}
			}
			if byte(drop.slot) == b.hotbarSlot {
				b.heldItem = b.inventory[drop.slot]
			}
			b.mu.Unlock()
			b.sendHeldItem(c)
			time.Sleep(50 * time.Millisecond)
		}
	}

	if len(droppedSlots) == 0 {
		b.reply("No valid items to drop.")
		return
	}

	if !each {
		_ = c.Flush()
		b.mu.Lock()
		for _, drop := range droppedSlots {
			if b.inventory[drop.slot].Stack.Count > drop.count {
				b.inventory[drop.slot].Stack.Count -= drop.count
			} else {
				b.inventory[drop.slot] = protocol.ItemInstance{}
			}
			if byte(drop.slot) == b.hotbarSlot {
				b.heldItem = b.inventory[drop.slot]
			}
		}
		b.mu.Unlock()
		b.sendHeldItem(c)
	}

	b.reply(fmt.Sprintf("Dropped items from %d slot(s)%s [direct first-person, %s].", len(droppedSlots), seqStr, modeStr))
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

var bedRuntimeIDs = map[uint32]bool{
	3410626621: true,
	718041354:  true,
	719534736:  true,
	1960963872: true,
	3285865971: true,
	1774841307: true,
	434679272:  true,
	3462326488: true,
	133499295:  true,
	860680903:  true,
	2902676369: true,
	2230559400: true,
	2355320050: true,
	295709842:  true,
	2937564980: true,
	3234261058: true,
	3739980241: true,
	3744311979: true,
	3450205046: true,
	2494858773: true,
	2573455936: true,
	4160501005: true,
	1773347925: true,
	2944348693: true,
	3165813567: true,
	2108673487: true,
	2903006083: true,
	468715448:  true,
	2689005408: true,
	3492496094: true,
	4078278255: true,
	1847699512: true,
}

func (b *Bot) findNearbyBed() (protocol.BlockPos, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	botPos := b.pos
	bestDist := float32(math.MaxFloat32)
	var bestPos protocol.BlockPos
	found := false

	bx, by, bz := int32(math.Floor(float64(botPos.X()))), int32(math.Floor(float64(botPos.Y()))), int32(math.Floor(float64(botPos.Z())))
	for dx := int32(-4); dx <= 4; dx++ {
		for dy := int32(-2); dy <= 2; dy++ {
			for dz := int32(-4); dz <= 4; dz++ {
				pos := protocol.BlockPos{bx + dx, by + dy, bz + dz}
				rid, ok := b.blockRuntimeIDs[pos]
				if ok && bedRuntimeIDs[rid] {
					diff := mgl32.Vec3{float32(pos.X()) + 0.5 - botPos.X(), float32(pos.Y()) + 0.5 - (botPos.Y() + 1.62), float32(pos.Z()) + 0.5 - botPos.Z()}
					dist := float32(math.Sqrt(float64(diff.Dot(diff))))
					if dist < bestDist {
						bestDist = dist
						bestPos = pos
						found = true
					}
				}
			}
		}
	}
	return bestPos, found
}

func (b *Bot) interactBed(pos protocol.BlockPos) {
	b.mu.Lock()
	c := b.conn
	entityID := b.entityID
	if c == nil {
		b.mu.Unlock()
		b.reply("Cannot sleep: not connected.")
		return
	}
	botPos := b.pos
	hotbarSlot := b.hotbarSlot
	heldItem := b.heldItem
	curRuntimeID := b.blockRuntimeIDs[pos]
	b.mu.Unlock()

	centerPos := mgl32.Vec3{float32(pos.X()) + 0.5, float32(pos.Y()) + 0.5, float32(pos.Z()) + 0.5}
	eyePos := b.playerEyePos()
	diff := centerPos.Sub(eyePos)
	dist := diff.Len()
	if dist > 5.5 {
		b.reply(fmt.Sprintf("Bed at %v is too far away (%.1f blocks, max 5.5).", pos, dist))
		return
	}

	face := int32(1) // Top face (UP) is standard for beds
	clickPos := faceClickPosition(face)
	targetPos := mgl32.Vec3{
		float32(pos.X()) + clickPos.X(),
		float32(pos.Y()) + clickPos.Y(),
		float32(pos.Z()) + clickPos.Z(),
	}

	dx := float64(targetPos.X()) - float64(eyePos.X())
	dy := float64(targetPos.Y()) - float64(eyePos.Y())
	dz := float64(targetPos.Z()) - float64(eyePos.Z())
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
	b.spawnPos = pos
	b.isSleeping = true
	b.mu.Unlock()

	// Wait 100ms for moveTick to transmit updated facing yaw and pitch to the server
	time.Sleep(100 * time.Millisecond)

	b.mu.Lock()
	botPos = b.pos
	hotbarSlot = b.hotbarSlot
	heldItem = b.heldItem
	if rid, ok := b.blockRuntimeIDs[pos]; ok {
		curRuntimeID = rid
	}
	b.mu.Unlock()

	b.markPacketSent(fmt.Sprintf("InventoryTransaction (UseItemActionClickBlock bed pos=%v face=%d runtimeID=%d)", pos, face, curRuntimeID))
	_ = c.WritePacket(&packet.InventoryTransaction{
		TransactionData: &protocol.UseItemTransactionData{
			ActionType:       protocol.UseItemActionClickBlock,
			TriggerType:      protocol.TriggerTypePlayerInput,
			BlockPosition:    pos,
			BlockFace:        face,
			HotBarSlot:       int32(hotbarSlot),
			HeldItem:         heldItem,
			Position:         botPos,
			ClickedPosition:  clickPos,
			BlockRuntimeID:   curRuntimeID,
			ClientPrediction: protocol.ClientPredictionSuccess,
		},
	})
	_ = c.WritePacket(&packet.Animate{
		ActionType:      packet.AnimateActionSwingArm,
		EntityRuntimeID: entityID,
	})
	_ = c.WritePacket(&packet.PlayerAction{
		EntityRuntimeID: entityID,
		ActionType:      protocol.PlayerActionStartSleeping,
		BlockPosition:   pos,
		ResultPosition:  pos,
		BlockFace:       face,
	})
	_ = c.Flush()

	log.Printf("bebot: interacted with bed at %v (dist=%.2f, runtimeID=%d)\n", pos, dist, curRuntimeID)
	b.reply(fmt.Sprintf("Interacted with bed at (%d, %d, %d). Send 'wakeup' to leave bed.", pos.X(), pos.Y(), pos.Z()))
}

func (b *Bot) wakeUp() {
	b.mu.Lock()
	c := b.conn
	entityID := b.entityID
	pos := b.spawnPos
	b.isSleeping = false
	b.mu.Unlock()

	if c == nil {
		b.reply("Cannot wake up: not connected.")
		return
	}
	_ = c.WritePacket(&packet.PlayerAction{
		EntityRuntimeID: entityID,
		ActionType:      protocol.PlayerActionStopSleeping,
		BlockPosition:   pos,
		ResultPosition:  pos,
		BlockFace:       -1,
	})
	_ = c.Flush()
	log.Printf("bebot: woke up from bed at %v\n", pos)
	b.reply("Woke up from bed.")
}

func (b *Bot) itemNameLocked(netID int32) string {
	if name, ok := b.itemNamesByID[netID]; ok && name != "" {
		return strings.TrimPrefix(name, "minecraft:")
	}
	return fmt.Sprintf("item_%d", netID)
}

func (b *Bot) itemName(netID int32) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.itemNameLocked(netID)
}

func (b *Bot) getFrontBlockPos() protocol.BlockPos {
	b.mu.Lock()
	yaw := b.facingYaw
	pitch := b.facingPitch
	b.mu.Unlock()
	eyePos := b.playerEyePos()

	yawRad := float64(yaw) * math.Pi / 180.0
	pitchRad := float64(pitch) * math.Pi / 180.0

	dx := -math.Sin(yawRad) * math.Cos(pitchRad)
	dy := -math.Sin(pitchRad)
	dz := math.Cos(yawRad) * math.Cos(pitchRad)

	fx := float64(eyePos.X()) + dx*1.5
	fy := float64(eyePos.Y()) + dy*1.5
	fz := float64(eyePos.Z()) + dz*1.5

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

func horizontalBlockFace(eyePos mgl32.Vec3, blockPos protocol.BlockPos) int32 {
	dx := float32(blockPos.X()) + 0.5 - eyePos.X()
	dz := float32(blockPos.Z()) + 0.5 - eyePos.Z()
	if math.Abs(float64(dx)) >= math.Abs(float64(dz)) {
		if dx > 0 {
			return 4
		}
		return 5
	}
	if dz > 0 {
		return 2
	}
	return 3
}

func (b *Bot) openBlockContainer(c *minecraft.Conn, pos protocol.BlockPos) (byte, error) {
	b.mu.Lock()
	botPos := b.pos
	hotbarSlot := b.hotbarSlot
	heldItem := b.heldItem
	entityID := b.entityID
	b.containerOpening = true
	b.containerActive = false
	b.containerContentReceived = false
	b.containerItems = make(map[byte]protocol.ItemInstance)
	b.containerPos = pos
	b.containerContainerID = protocol.ContainerLevelEntity
	b.mu.Unlock()

	// Drain any leftover buffered signals from previous container operations
	for {
		select {
		case <-b.containerOpen:
		case <-b.containerContent:
		default:
			goto Drained
		}
	}
Drained:

	defer func() {
		b.mu.Lock()
		b.containerOpening = false
		b.mu.Unlock()
	}()

	centerPos := mgl32.Vec3{float32(pos.X()) + 0.5, float32(pos.Y()) + 0.5, float32(pos.Z()) + 0.5}
	eyePos := b.playerEyePos()
	diff := centerPos.Sub(eyePos)
	dist := diff.Len()
	if dist > 6.0 {
		return 0, fmt.Errorf("container at %v is too far (%.1f blocks, max 6.0)", pos, dist)
	}

	face := determineBlockFace(eyePos, pos)
	clickPos := faceClickPosition(face)
	targetPos := mgl32.Vec3{
		float32(pos.X()) + clickPos.X(),
		float32(pos.Y()) + clickPos.Y(),
		float32(pos.Z()) + clickPos.Z(),
	}

	dx := float64(targetPos.X()) - float64(eyePos.X())
	dy := float64(targetPos.Y()) - float64(eyePos.Y())
	dz := float64(targetPos.Z()) - float64(eyePos.Z())
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
	b.mu.Unlock()

	// Wait 100ms for moveTick to transmit updated facing yaw and pitch to the server
	time.Sleep(100 * time.Millisecond)

	b.mu.Lock()
	botPos = b.pos
	hotbarSlot = b.hotbarSlot
	heldItem = b.heldItem
	b.mu.Unlock()

	log.Printf("bebot: opening container at %v from bot pos (%.2f, %.2f, %.2f) (dist=%.2f, face=%d, yaw=%.1f, pitch=%.1f, runtimeID=%d)\n", pos, botPos.X(), botPos.Y(), botPos.Z(), dist, face, yaw, pitch, runtimeID)

	sendOpenWithFace := func(f int32) {
		cp := faceClickPosition(f)
		b.mu.Lock()
		curRuntimeID := b.blockRuntimeIDs[pos]
		b.mu.Unlock()

		b.markPacketSent(fmt.Sprintf("InventoryTransaction (UseItemActionClickBlock pos=%v face=%d runtimeID=%d)", pos, f, curRuntimeID))
		_ = c.WritePacket(&packet.InventoryTransaction{
			TransactionData: &protocol.UseItemTransactionData{
				ActionType:       protocol.UseItemActionClickBlock,
				TriggerType:      protocol.TriggerTypePlayerInput,
				BlockPosition:    pos,
				BlockFace:        f,
				HotBarSlot:       int32(hotbarSlot),
				HeldItem:         heldItem,
				Position:         botPos,
				ClickedPosition:  cp,
				BlockRuntimeID:   curRuntimeID,
				ClientPrediction: protocol.ClientPredictionSuccess,
			},
		})
		_ = c.WritePacket(&packet.Animate{
			ActionType:      packet.AnimateActionSwingArm,
			EntityRuntimeID: entityID,
		})
		_ = c.Flush()
	}

	sendOpenWithFace(face)

	timer := time.NewTimer(3500 * time.Millisecond)
	defer timer.Stop()
	retryTimer := time.NewTimer(1500 * time.Millisecond)
	defer retryTimer.Stop()

	for {
		select {
		case <-b.containerContent:
			b.mu.Lock()
			wID := b.containerWindowID
			received := b.containerContentReceived
			b.mu.Unlock()
			if received {
				return wID, nil
			}

		case <-b.containerOpen:
			b.mu.Lock()
			wID := b.containerWindowID
			received := b.containerContentReceived
			b.mu.Unlock()
			if received {
				return wID, nil
			}
			// ContainerOpen arrived, wait for InventoryContent to arrive
			select {
			case <-b.containerContent:
				b.mu.Lock()
				wID = b.containerWindowID
				b.mu.Unlock()
				return wID, nil
			case <-time.After(1500 * time.Millisecond):
				return wID, nil
			}

		case rid := <-b.containerRuntimeIDUpdated:
			b.mu.Lock()
			active := b.containerActive
			b.mu.Unlock()
			if !active {
				log.Printf("bebot: received container UpdateBlock pos=%v runtimeID=%d, immediately re-trying open...\n", pos, rid)
				sendOpenWithFace(face)
			}

		case <-retryTimer.C:
			b.mu.Lock()
			active := b.containerActive
			b.mu.Unlock()
			if !active {
				altFace := horizontalBlockFace(eyePos, pos)
				log.Printf("bebot: retrying container open at %v with face=%d...\n", pos, altFace)
				sendOpenWithFace(altFace)
			}

		case <-timer.C:
			return 0, fmt.Errorf("timeout waiting for container to open at %v (ensure target block has an inventory)", pos)
		}
	}
}

func (b *Bot) closeBlockContainer(c *minecraft.Conn, windowID byte) {
	b.mu.Lock()
	cType := b.containerType
	b.containerActive = false
	b.containerWindowID = 0
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

func (b *Bot) findPlayerSlotForCount(netID int32, incoming int) (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.inventory) < 36 {
		grown := make([]protocol.ItemInstance, 36)
		copy(grown, b.inventory)
		b.inventory = grown
	}
	// First preference: existing stack of the same item that will not exceed 64 items
	if netID != 0 && incoming > 0 {
		for i := 0; i < 36; i++ {
			item := b.inventory[i]
			if item.Stack.ItemType.NetworkID == netID && item.Stack.Count > 0 && int(item.Stack.Count)+incoming <= 64 {
				return i, true
			}
		}
	}
	// Second preference: first empty slot
	for i := 0; i < 36; i++ {
		if b.inventory[i].Stack.Count == 0 {
			return i, true
		}
	}
	return -1, false
}

func (b *Bot) findPlayerSlotForItem(netID int32) (int, bool) {
	return b.findPlayerSlotForCount(netID, 1)
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
		b.mu.Lock()
		te, hasTE := b.containerTileEntities[pos]
		b.mu.Unlock()
		if hasTE {
			if items, ok := te["Items"].([]any); ok && len(items) > 0 {
				var itemsSummary []string
				for _, rawItem := range items {
					if itMap, ok := rawItem.(map[string]any); ok {
						name, _ := itMap["Name"].(string)
						count, _ := itMap["Count"].(byte)
						slot, _ := itMap["Slot"].(byte)
						itemsSummary = append(itemsSummary, fmt.Sprintf("Slot %d: %dx %s", slot, count, strings.TrimPrefix(name, "minecraft:")))
						if len(itemsSummary) >= 10 {
							remaining := len(items) - 10
							if remaining > 0 {
								itemsSummary = append(itemsSummary, fmt.Sprintf("...and %d more item(s)", remaining))
							}
							break
						}
					}
				}
				b.reply(fmt.Sprintf("Container at (%d, %d, %d) has %d item(s) (from tile entity cache):\n%s", pos.X(), pos.Y(), pos.Z(), len(items), strings.Join(itemsSummary, "\n")))
				return
			}
		}
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
		name := b.itemNameLocked(it.Stack.ItemType.NetworkID)
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

func (b *Bot) inventoryCmd(p []string) {
	b.mu.Lock()
	var lines []string
	itemCount := 0
	for i, item := range b.inventory {
		if item.Stack.Count > 0 {
			itemCount++
			name := b.itemNameLocked(item.Stack.ItemType.NetworkID)
			var nbtSummary string
			if item.Stack.NBTData != nil {
				if bID, ok := item.Stack.NBTData["bundle_id"]; ok {
					nbtSummary = fmt.Sprintf(" (bundle_id=%v)", bID)
				}
			}
			lines = append(lines, fmt.Sprintf("slot %d: %dx %s (netID=%d, stackNetID=%d)%s",
				i, item.Stack.Count, name, item.Stack.ItemType.NetworkID, item.StackNetworkID, nbtSummary))
		}
	}
	b.mu.Unlock()

	if len(lines) == 0 {
		b.reply("Bot inventory is empty.")
		return
	}
	log.Printf("bebot: inventory:\n  %s\n", strings.Join(lines, "\n  "))
	if len(lines) > 6 {
		b.reply(fmt.Sprintf("Inventory has %d item(s) in %d slot(s): %s... (and %d more)",
			itemCount, len(lines), strings.Join(lines[:6], ", "), len(lines)-6))
	} else {
		b.reply(fmt.Sprintf("Inventory (%d items): %s", itemCount, strings.Join(lines, ", ")))
	}
}

func (b *Bot) openContainerCmd(p []string) {
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	if c == nil {
		b.reply("Cannot open container: bot is not connected.")
		return
	}

	pos, _, err := b.parseContainerPos(p)
	if err != nil {
		b.reply("Usage: open [x y z] or open front")
		return
	}

	b.invMu.Lock()
	defer b.invMu.Unlock()

	b.mu.Lock()
	if b.containerActive && b.containerWindowID != 0 {
		oldWID := b.containerWindowID
		b.mu.Unlock()
		b.closeBlockContainer(c, oldWID)
		time.Sleep(50 * time.Millisecond)
	} else {
		b.mu.Unlock()
	}

	wID, err := b.openBlockContainer(c, pos)
	if err != nil {
		b.reply(fmt.Sprintf("Failed to open container at (%d, %d, %d): %s", pos.X(), pos.Y(), pos.Z(), err.Error()))
		return
	}

	b.reply(fmt.Sprintf("Opened container at (%d, %d, %d) [WindowID=%d]. Send 'close' when done.", pos.X(), pos.Y(), pos.Z(), wID))
}

func (b *Bot) closeContainerCmd(p []string) {
	b.mu.Lock()
	c := b.conn
	active := b.containerActive
	wID := b.containerWindowID
	b.mu.Unlock()
	if c == nil {
		b.reply("Cannot close container: bot is not connected.")
		return
	}

	if !active || wID == 0 {
		b.reply("No block container is currently open.")
		return
	}

	b.invMu.Lock()
	defer b.invMu.Unlock()

	b.closeBlockContainer(c, wID)
	b.reply(fmt.Sprintf("Closed container [WindowID=%d].", wID))
}

// dropAllNow drops all non-empty inventory slots at once using Method 1 (First-Person In-World).
// If verbose is true, it replies to chat with the result. If false, it logs silently to console.
// Returns the number of dropped slots.
func (b *Bot) dropAllNow(random bool, verbose bool) int {
	b.invMu.Lock()
	defer b.invMu.Unlock()

	b.mu.Lock()
	c := b.conn
	if c == nil {
		b.mu.Unlock()
		return 0
	}

	type dropInfo struct {
		slot  int
		item  protocol.ItemInstance
		count uint16
	}
	var drops []dropInfo
	for slot, item := range b.inventory {
		if item.Stack.Count > 0 {
			drops = append(drops, dropInfo{
				slot:  slot,
				item:  item,
				count: item.Stack.Count,
			})
		}
	}
	b.mu.Unlock()

	if len(drops) == 0 {
		if verbose {
			b.reply("No items found to drop.")
		}
		return 0
	}

	sourceFlags := uint32(0)
	if random {
		sourceFlags = 1
	}

	var droppedSlots []dropInfo
	for _, drop := range drops {
		oldItem := drop.item
		newItem := protocol.ItemInstance{}
		droppedItem := oldItem
		droppedItem.Stack.Count = drop.count

		tx := &packet.InventoryTransaction{
			Actions: []protocol.InventoryAction{
				{
					SourceType:    protocol.InventoryActionSourceContainer,
					WindowID:      protocol.Option(int8(protocol.WindowIDInventory)),
					InventorySlot: uint32(drop.slot),
					OldItem:       oldItem,
					NewItem:       newItem,
				},
				{
					SourceType:    protocol.InventoryActionSourceWorld,
					SourceFlags:   protocol.Option(sourceFlags),
					InventorySlot: 0,
					OldItem:       protocol.ItemInstance{},
					NewItem:       droppedItem,
				},
			},
			TransactionData: &protocol.NormalTransactionData{},
		}
		if err := c.WritePacket(tx); err != nil {
			log.Printf("bebot: drop slot %d write failed: %v\n", drop.slot, err)
			continue
		}
		droppedSlots = append(droppedSlots, drop)
	}

	if len(droppedSlots) == 0 {
		return 0
	}

	_ = c.Flush()

	b.mu.Lock()
	for _, drop := range droppedSlots {
		if drop.slot < len(b.inventory) {
			b.inventory[drop.slot] = protocol.ItemInstance{}
			if byte(drop.slot) == b.hotbarSlot {
				b.heldItem = protocol.ItemInstance{}
			}
		}
	}
	b.mu.Unlock()
	b.sendHeldItem(c)

	if verbose {
		modeStr := "eye direction"
		if random {
			modeStr = "random scatter"
		}
		b.reply(fmt.Sprintf("Dropped items from %d slot(s) [direct first-person, %s].", len(droppedSlots), modeStr))
	} else {
		log.Printf("bebot: droploop dropped items from %d slot(s) (random=%v)\n", len(droppedSlots), random)
	}
	return len(droppedSlots)
}

// startDropLoop initiates a continuous inventory drop loop every `ticks` game ticks (default 20gt = 1s).
func (b *Bot) startDropLoop(random bool, ticks int) {
	if ticks <= 0 {
		ticks = 20
	}
	b.stopDropLoop()

	ctx, cancel := context.WithCancel(context.Background())
	b.mu.Lock()
	b.dropLoopActive = true
	b.dropLoopCancel = cancel
	b.dropLoopRandom = random
	b.dropLoopTicks = ticks
	b.mu.Unlock()

	go b.runDropLoop(ctx, random, ticks)

	modeStr := "eye direction"
	if random {
		modeStr = "random direction"
	}
	b.reply(fmt.Sprintf("Drop loop started (%dgt / %.1fs interval, %s). Send 'droploop stop' or 'stopdrop' to cancel.",
		ticks, float64(ticks)*0.05, modeStr))
}

// stopDropLoop cancels any active drop loop. Returns true if a loop was cancelled.
func (b *Bot) stopDropLoop() bool {
	b.mu.Lock()
	active := b.dropLoopActive
	cancel := b.dropLoopCancel
	b.dropLoopActive = false
	b.dropLoopCancel = nil
	b.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return active
}

func (b *Bot) runDropLoop(ctx context.Context, random bool, ticks int) {
	if ticks <= 0 {
		ticks = 20
	}
	interval := time.Duration(ticks) * 50 * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial drop immediately on start if inventory has items
	b.dropAllNow(random, false)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if b.isServerStalled() {
				continue
			}
			b.dropAllNow(random, false)
		}
	}
}

func (b *Bot) dropLoopCmd(p []string) {
	if len(p) >= 2 {
		arg1 := strings.ToLower(p[1])
		if arg1 == "stop" || arg1 == "off" || arg1 == "cancel" || arg1 == "end" {
			if b.stopDropLoop() {
				b.reply("Drop loop stopped.")
			} else {
				b.reply("Drop loop was not running.")
			}
			return
		}
		if arg1 == "status" {
			b.mu.Lock()
			active := b.dropLoopActive
			rnd := b.dropLoopRandom
			ticks := b.dropLoopTicks
			b.mu.Unlock()
			if active {
				mode := "eye direction"
				if rnd {
					mode = "random direction"
				}
				b.reply(fmt.Sprintf("Drop loop is running (%dgt interval, %s). Send 'droploop stop' to stop.", ticks, mode))
			} else {
				b.reply("Drop loop is inactive.")
			}
			return
		}
	}

	random := false
	ticks := 20
	for _, arg := range p[1:] {
		argLower := strings.ToLower(arg)
		if argLower == "random" {
			random = true
		} else if argLower == "eye" || argLower == "front" || argLower == "normal" {
			random = false
		} else if bVal, err := strconv.ParseBool(argLower); err == nil {
			random = bVal
		} else {
			clean := strings.TrimSuffix(strings.TrimSuffix(argLower, "gt"), "ticks")
			if tVal, err := strconv.Atoi(clean); err == nil && tVal > 0 {
				ticks = tVal
			}
		}
	}

	b.startDropLoop(random, ticks)
}

func isUnstackable(name string) bool {
	return strings.HasSuffix(name, "_sword") ||
		strings.HasSuffix(name, "_pickaxe") ||
		strings.HasSuffix(name, "_axe") ||
		strings.HasSuffix(name, "_shovel") ||
		strings.HasSuffix(name, "_hoe") ||
		strings.HasSuffix(name, "_helmet") ||
		strings.HasSuffix(name, "_chestplate") ||
		strings.HasSuffix(name, "_leggings") ||
		strings.HasSuffix(name, "_boots") ||
		strings.HasSuffix(name, "_boat") ||
		strings.HasSuffix(name, "_raft") ||
		strings.Contains(name, "minecart") ||
		strings.Contains(name, "potion") ||
		strings.Contains(name, "stew") ||
		strings.Contains(name, "soup") ||
		strings.HasPrefix(name, "music_disc_") ||
		name == "bow" || name == "crossbow" || name == "trident" || name == "mace" ||
		name == "shield" || name == "elytra" || name == "flint_and_steel" || name == "shears" ||
		name == "fishing_rod" || name == "brush" || name == "spyglass" || name == "saddle" ||
		name == "totem_of_undying" || name == "bundle" || strings.HasSuffix(name, "_bundle") ||
		name == "enchanted_book" || name == "written_book" || name == "writable_book" ||
		name == "goat_horn" || name == "milk_bucket" || name == "ominous_bottle"
}

func isStack16(name string) bool {
	return name == "ender_pearl" || name == "egg" || name == "snowball" ||
		name == "honey_bottle" || name == "bucket" ||
		strings.HasSuffix(name, "_sign") || strings.HasSuffix(name, "_hanging_sign") ||
		strings.HasSuffix(name, "_banner")
}

func (b *Bot) itemMaxStackSize(item protocol.ItemInstance) int {
	if item.Stack.Count == 0 {
		return 64
	}
	if item.Stack.Count > 16 {
		return 64
	}
	name := b.itemName(item.Stack.ItemType.NetworkID)
	name = strings.ToLower(strings.TrimPrefix(name, "minecraft:"))

	if item.Stack.NBTData != nil {
		if _, hasDmg := item.Stack.NBTData["Damage"]; hasDmg {
			return 1
		}
	}
	if item.Stack.Count == 1 && isUnstackable(name) {
		return 1
	}
	if isStack16(name) {
		return 16
	}
	return 64
}

func canItemsMerge(a, b protocol.ItemInstance, maxStack int) bool {
	if a.Stack.Count == 0 || b.Stack.Count == 0 {
		return false
	}
	if maxStack <= 1 {
		return false
	}
	if int(a.Stack.Count) >= maxStack {
		return false
	}
	if a.Stack.ItemType.NetworkID != b.Stack.ItemType.NetworkID {
		return false
	}
	if a.Stack.ItemType.MetadataValue != b.Stack.ItemType.MetadataValue {
		return false
	}
	if !reflect.DeepEqual(a.Stack.NBTData, b.Stack.NBTData) {
		return false
	}
	return true
}

func (b *Bot) transferSlotItem(c *minecraft.Conn, srcSlot, dstSlot, count int) bool {
	if count <= 0 || srcSlot == dstSlot {
		return false
	}
	b.mu.Lock()
	if srcSlot >= len(b.inventory) || dstSlot >= len(b.inventory) {
		b.mu.Unlock()
		return false
	}
	srcItem := b.inventory[srcSlot]
	dstItem := b.inventory[dstSlot]
	b.mu.Unlock()

	if srcItem.Stack.Count == 0 || int(srcItem.Stack.Count) < count {
		return false
	}

	// Try ItemStackRequest first
	wID, opened := b.openInventory(c)
	if opened {
		defer b.closeInventory(c, wID)
	}

	srcCands := playerSlotCandidates(srcSlot)
	dstCands := playerSlotCandidates(dstSlot)

	reqSucceeded := false
	if len(srcCands) > 0 && len(dstCands) > 0 {
		srcCand := srcCands[0]
		dstCand := dstCands[0]

		take := &protocol.TakeStackRequestAction{}
		take.Count = byte(count)
		take.Source = stackReqSlotInfo(srcCand.container, srcCand.slot, srcItem.StackNetworkID)
		take.Destination = stackReqSlotInfo(protocol.ContainerCursor, 0, 0)

		place := &protocol.PlaceStackRequestAction{}
		place.Count = byte(count)
		place.Source = stackReqSlotInfo(protocol.ContainerCursor, 0, srcItem.StackNetworkID)
		place.Destination = stackReqSlotInfo(dstCand.container, dstCand.slot, dstItem.StackNetworkID)

		ok, _ := b.sendItemStackRequest(c, []protocol.StackRequestAction{take, place})
		if ok {
			reqSucceeded = true
		}
	}

	if reqSucceeded {
		b.mu.Lock()
		if int(b.inventory[srcSlot].Stack.Count) <= count {
			b.inventory[srcSlot] = protocol.ItemInstance{}
		} else {
			b.inventory[srcSlot].Stack.Count -= uint16(count)
		}
		if b.inventory[dstSlot].Stack.Count == 0 {
			b.inventory[dstSlot] = srcItem
			b.inventory[dstSlot].Stack.Count = uint16(count)
		} else {
			b.inventory[dstSlot].Stack.Count += uint16(count)
		}
		if byte(srcSlot) == b.hotbarSlot {
			b.heldItem = b.inventory[srcSlot]
		}
		if byte(dstSlot) == b.hotbarSlot {
			b.heldItem = b.inventory[dstSlot]
		}
		b.mu.Unlock()
		b.sendHeldItem(c)
		time.Sleep(15 * time.Millisecond)
		return true
	}

	// Fallback to balanced packet.InventoryTransaction
	newSrc := srcItem
	if int(srcItem.Stack.Count) <= count {
		newSrc = protocol.ItemInstance{}
	} else {
		newSrc.Stack.Count -= uint16(count)
	}

	newDst := dstItem
	if dstItem.Stack.Count == 0 {
		newDst = srcItem
		newDst.Stack.Count = uint16(count)
	} else {
		newDst.Stack.Count += uint16(count)
	}

	tx := &packet.InventoryTransaction{
		Actions: []protocol.InventoryAction{
			{
				SourceType:    protocol.InventoryActionSourceContainer,
				WindowID:      protocol.Option(int8(protocol.WindowIDInventory)),
				InventorySlot: uint32(srcSlot),
				OldItem:       srcItem,
				NewItem:       newSrc,
			},
			{
				SourceType:    protocol.InventoryActionSourceContainer,
				WindowID:      protocol.Option(int8(protocol.WindowIDInventory)),
				InventorySlot: uint32(dstSlot),
				OldItem:       dstItem,
				NewItem:       newDst,
			},
		},
		TransactionData: &protocol.NormalTransactionData{},
	}
	if err := c.WritePacket(tx); err != nil {
		log.Printf("bebot: sort-inv transfer tx slot %d -> %d failed: %v\n", srcSlot, dstSlot, err)
		return false
	}
	_ = c.Flush()

	b.mu.Lock()
	b.inventory[srcSlot] = newSrc
	b.inventory[dstSlot] = newDst
	if byte(srcSlot) == b.hotbarSlot {
		b.heldItem = newSrc
	}
	if byte(dstSlot) == b.hotbarSlot {
		b.heldItem = newDst
	}
	b.mu.Unlock()
	b.sendHeldItem(c)
	time.Sleep(15 * time.Millisecond)
	return true
}

func (b *Bot) swapOrMoveSlot(c *minecraft.Conn, srcSlot, dstSlot int) bool {
	if srcSlot == dstSlot {
		return true
	}
	b.mu.Lock()
	if srcSlot >= len(b.inventory) || dstSlot >= len(b.inventory) {
		b.mu.Unlock()
		return false
	}
	srcItem := b.inventory[srcSlot]
	dstItem := b.inventory[dstSlot]
	b.mu.Unlock()

	if srcItem.Stack.Count == 0 && dstItem.Stack.Count == 0 {
		return true
	}

	// Try ItemStackRequest first
	wID, opened := b.openInventory(c)
	if opened {
		defer b.closeInventory(c, wID)
	}

	srcCands := playerSlotCandidates(srcSlot)
	dstCands := playerSlotCandidates(dstSlot)

	reqSucceeded := false
	if len(srcCands) > 0 && len(dstCands) > 0 {
		srcCand := srcCands[0]
		dstCand := dstCands[0]

		if dstItem.Stack.Count == 0 {
			count := srcItem.Stack.Count
			if count > 255 {
				count = 255
			}
			take := &protocol.TakeStackRequestAction{}
			take.Count = byte(count)
			take.Source = stackReqSlotInfo(srcCand.container, srcCand.slot, srcItem.StackNetworkID)
			take.Destination = stackReqSlotInfo(protocol.ContainerCursor, 0, 0)

			place := &protocol.PlaceStackRequestAction{}
			place.Count = byte(count)
			place.Source = stackReqSlotInfo(protocol.ContainerCursor, 0, srcItem.StackNetworkID)
			place.Destination = stackReqSlotInfo(dstCand.container, dstCand.slot, 0)

			ok, _ := b.sendItemStackRequest(c, []protocol.StackRequestAction{take, place})
			if ok {
				reqSucceeded = true
			}
		} else {
			swap := &protocol.SwapStackRequestAction{
				Source:      stackReqSlotInfo(srcCand.container, srcCand.slot, srcItem.StackNetworkID),
				Destination: stackReqSlotInfo(dstCand.container, dstCand.slot, dstItem.StackNetworkID),
			}
			ok, _ := b.sendItemStackRequest(c, []protocol.StackRequestAction{swap})
			if ok {
				reqSucceeded = true
			}
		}
	}

	if reqSucceeded {
		b.mu.Lock()
		b.inventory[srcSlot] = dstItem
		b.inventory[dstSlot] = srcItem
		if byte(srcSlot) == b.hotbarSlot {
			b.heldItem = dstItem
		}
		if byte(dstSlot) == b.hotbarSlot {
			b.heldItem = srcItem
		}
		b.mu.Unlock()
		b.sendHeldItem(c)
		time.Sleep(15 * time.Millisecond)
		return true
	}

	// Fallback to balanced packet.InventoryTransaction
	tx := &packet.InventoryTransaction{
		Actions: []protocol.InventoryAction{
			{
				SourceType:    protocol.InventoryActionSourceContainer,
				WindowID:      protocol.Option(int8(protocol.WindowIDInventory)),
				InventorySlot: uint32(srcSlot),
				OldItem:       srcItem,
				NewItem:       dstItem,
			},
			{
				SourceType:    protocol.InventoryActionSourceContainer,
				WindowID:      protocol.Option(int8(protocol.WindowIDInventory)),
				InventorySlot: uint32(dstSlot),
				OldItem:       dstItem,
				NewItem:       srcItem,
			},
		},
		TransactionData: &protocol.NormalTransactionData{},
	}
	if err := c.WritePacket(tx); err != nil {
		log.Printf("bebot: sort-inv swap tx slot %d <-> %d failed: %v\n", srcSlot, dstSlot, err)
		return false
	}
	_ = c.Flush()

	b.mu.Lock()
	b.inventory[srcSlot] = dstItem
	b.inventory[dstSlot] = srcItem
	if byte(srcSlot) == b.hotbarSlot {
		b.heldItem = dstItem
	}
	if byte(dstSlot) == b.hotbarSlot {
		b.heldItem = srcItem
	}
	b.mu.Unlock()
	b.sendHeldItem(c)
	time.Sleep(15 * time.Millisecond)
	return true
}

func (b *Bot) sortInventoryCmd(p []string) {
	b.invMu.Lock()
	defer b.invMu.Unlock()

	b.mu.Lock()
	c := b.conn
	if c == nil {
		b.mu.Unlock()
		b.reply("Cannot sort inventory: bot is not connected.")
		return
	}
	invLen := len(b.inventory)
	if invLen > 36 {
		invLen = 36
	}
	inv := make([]protocol.ItemInstance, invLen)
	copy(inv, b.inventory[:invLen])
	b.mu.Unlock()

	initialSlots := 0
	for _, it := range inv {
		if it.Stack.Count > 0 {
			initialSlots++
		}
	}
	if initialSlots == 0 {
		b.reply("Inventory is empty, nothing to sort.")
		return
	}

	// Phase 1: Merge partial stacks to minimize slots taken
	merges := 0
	for i := 0; i < len(inv); i++ {
		if inv[i].Stack.Count == 0 {
			continue
		}
		maxStack := b.itemMaxStackSize(inv[i])
		if maxStack <= 1 || int(inv[i].Stack.Count) >= maxStack {
			continue
		}
		for j := i + 1; j < len(inv); j++ {
			if inv[j].Stack.Count == 0 {
				continue
			}
			if canItemsMerge(inv[i], inv[j], maxStack) {
				needed := maxStack - int(inv[i].Stack.Count)
				transferCount := int(inv[j].Stack.Count)
				if transferCount > needed {
					transferCount = needed
				}
				if transferCount > 0 {
					if b.transferSlotItem(c, j, i, transferCount) {
						merges++
						b.mu.Lock()
						copy(inv, b.inventory[:invLen])
						b.mu.Unlock()
					}
					if int(inv[i].Stack.Count) >= maxStack {
						break
					}
				}
			}
		}
	}

	// Phase 2: Group identical items together and compact to earliest slots
	type itemRef struct {
		slot int
		item protocol.ItemInstance
		name string
	}
	var nonEmpties []itemRef
	b.mu.Lock()
	for s := 0; s < invLen && s < len(b.inventory); s++ {
		it := b.inventory[s]
		if it.Stack.Count > 0 {
			nonEmpties = append(nonEmpties, itemRef{
				slot: s,
				item: it,
				name: b.itemNameLocked(it.Stack.ItemType.NetworkID),
			})
		}
	}
	b.mu.Unlock()

	// Sort the non-empty item list to establish desired grouping:
	// 1. Group by Item Name (alphabetical)
	// 2. NetworkID
	// 3. Descending by Stack Count (full stacks first)
	// 4. Metadata value
	sort.SliceStable(nonEmpties, func(i, j int) bool {
		if nonEmpties[i].name != nonEmpties[j].name {
			return nonEmpties[i].name < nonEmpties[j].name
		}
		if nonEmpties[i].item.Stack.ItemType.NetworkID != nonEmpties[j].item.Stack.ItemType.NetworkID {
			return nonEmpties[i].item.Stack.ItemType.NetworkID < nonEmpties[j].item.Stack.ItemType.NetworkID
		}
		if nonEmpties[i].item.Stack.Count != nonEmpties[j].item.Stack.Count {
			return nonEmpties[i].item.Stack.Count > nonEmpties[j].item.Stack.Count
		}
		return nonEmpties[i].item.Stack.ItemType.MetadataValue < nonEmpties[j].item.Stack.ItemType.MetadataValue
	})

	moves := 0
	for targetSlot := 0; targetSlot < len(nonEmpties); targetSlot++ {
		desired := nonEmpties[targetSlot]

		b.mu.Lock()
		curSlot := -1
		for s := targetSlot; s < invLen && s < len(b.inventory); s++ {
			it := b.inventory[s]
			if it.Stack.Count == desired.item.Stack.Count &&
				it.Stack.ItemType.NetworkID == desired.item.Stack.ItemType.NetworkID &&
				it.Stack.ItemType.MetadataValue == desired.item.Stack.ItemType.MetadataValue &&
				reflect.DeepEqual(it.Stack.NBTData, desired.item.Stack.NBTData) {
				curSlot = s
				break
			}
		}
		b.mu.Unlock()

		if curSlot != -1 && curSlot != targetSlot {
			if b.swapOrMoveSlot(c, curSlot, targetSlot) {
				moves++
			}
		}
	}

	b.mu.Lock()
	finalSlots := 0
	for s := 0; s < invLen && s < len(b.inventory); s++ {
		if b.inventory[s].Stack.Count > 0 {
			finalSlots++
		}
	}
	b.mu.Unlock()

	if merges == 0 && moves == 0 {
		b.reply(fmt.Sprintf("Inventory is already sorted and compacted (%d item(s) in %d slot(s)).", initialSlots, initialSlots))
		return
	}

	b.reply(fmt.Sprintf("Inventory sorted and compacted! Occupied slots reduced from %d to %d (%d stack merge(s), %d move(s)).",
		initialSlots, finalSlots, merges, moves))
}
