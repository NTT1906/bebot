package bot

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// bundleTestCmd implements: bebot bundletest <ax> <ay> <az> <bx> <by> <bz> [attempts]
// or: bebot bundletest stop
//
// The bot will:
//  1. Open dropper A and B alternately in a tight loop.
//  2. Whenever a bundle is found in the currently open dropper, immediately fire
//     a TakeStackRequestAction (without cursor step, direct from bundle container)
//     to try to pull an item while the dropper transfer is in flight.
//  3. Log the server's ItemStackResponse status (OK = potential dupe evidence,
//     rejection = server caught the race).
//  4. After all attempts (default 50), report how many OKs vs rejections.
func (b *Bot) bundleTestCmd(p []string) {
	if len(p) >= 2 && strings.EqualFold(p[1], "stop") {
		b.mu.Lock()
		cancel := b.bundleTestCancel
		b.bundleTestActive = false
		b.bundleTestCancel = nil
		b.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		b.reply("Bundle test stopped.")
		return
	}

	var posA, posB protocol.BlockPos
	attempts := 50

	if len(p) == 4 || len(p) == 5 {
		// Single dropper mode: bundletest <x> <y> <z> [attempts]
		coords := make([]int32, 3)
		for i := 0; i < 3; i++ {
			v, err := strconv.Atoi(p[i+1])
			if err != nil {
				b.reply(fmt.Sprintf("Invalid coordinate %q: %v", p[i+1], err))
				return
			}
			coords[i] = int32(v)
		}
		posA = protocol.BlockPos{coords[0], coords[1], coords[2]}
		posB = posA
		if len(p) == 5 {
			if v, err := strconv.Atoi(p[4]); err == nil && v > 0 {
				attempts = v
			}
		}
	} else if len(p) >= 7 {
		// Two dropper mode: bundletest <ax> <ay> <az> <bx> <by> <bz> [attempts]
		coords := make([]int32, 6)
		for i := 0; i < 6; i++ {
			v, err := strconv.Atoi(p[i+1])
			if err != nil {
				b.reply(fmt.Sprintf("Invalid coordinate %q: %v", p[i+1], err))
				return
			}
			coords[i] = int32(v)
		}
		posA = protocol.BlockPos{coords[0], coords[1], coords[2]}
		posB = protocol.BlockPos{coords[3], coords[4], coords[5]}
		if len(p) >= 8 {
			if v, err := strconv.Atoi(p[7]); err == nil && v > 0 {
				attempts = v
			}
		}
	} else {
		b.reply("Usage: bundletest <x> <y> <z> [attempts]  or  bundletest <ax> <ay> <az> <bx> <by> <bz> [attempts]  |  bundletest stop")
		return
	}

	b.mu.Lock()
	if b.bundleTestActive {
		b.mu.Unlock()
		b.reply("Bundle test is already running. Use 'bundletest stop' first.")
		return
	}
	c := b.conn
	b.mu.Unlock()
	if c == nil {
		b.reply("Cannot run bundle test: bot is not connected.")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	b.mu.Lock()
	b.bundleTestActive = true
	b.bundleTestCancel = cancel
	b.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			b.mu.Lock()
			b.bundleTestActive = false
			b.bundleTestCancel = nil
			b.mu.Unlock()
		}()

		if posA == posB {
			b.reply(fmt.Sprintf(
				"Bundle test started: dropper at (%d,%d,%d) attempts=%d",
				posA.X(), posA.Y(), posA.Z(),
				attempts,
			))
			log.Printf("bebot: [bundletest] starting %d attempts on single dropper %v\n", attempts, posA)
		} else {
			b.reply(fmt.Sprintf(
				"Bundle test started: droppers A=(%d,%d,%d) B=(%d,%d,%d) attempts=%d",
				posA.X(), posA.Y(), posA.Z(),
				posB.X(), posB.Y(), posB.Z(),
				attempts,
			))
			log.Printf("bebot: [bundletest] starting %d attempts on droppers A=%v B=%v\n", attempts, posA, posB)
		}

		var okCount, rejectCount, noBundle, emptyBundleCount int
		var lockedPos *protocol.BlockPos
		if posA == posB {
			lockedPos = &posA
		}

		var curWindowID byte
		var containerOpen bool

		defer func() {
			b.mu.Lock()
			c := b.conn
			b.mu.Unlock()
			if containerOpen && curWindowID != 0 && c != nil {
				b.closeBlockContainer(c, curWindowID)
				b.invMu.Unlock()
			}
		}()

		for attempt := 0; attempt < attempts; attempt++ {
			select {
			case <-ctx.Done():
				b.reply("Bundle test cancelled.")
				return
			default:
			}

			// If we've discovered which dropper holds the bundle, stay locked into it!
			// The bundle won't move away, so we avoid wasting attempts on empty droppers.
			targetPos := posA
			if lockedPos != nil {
				targetPos = *lockedPos
			} else if attempt%2 == 1 {
				targetPos = posB
			}

			b.mu.Lock()
			c = b.conn
			active := b.containerActive
			curPos := b.containerPos
			b.mu.Unlock()
			if c == nil {
				b.reply("Bundle test aborted: disconnected.")
				return
			}

			// Keep container open while testing on the same dropper.
			// A human player keeps the GUI open and clicks repeatedly; opening and closing
			// every 50ms causes UpdateBlock desynchronization while the redstone clock is ticking.
			if !containerOpen || !active || curPos != targetPos {
				if containerOpen {
					b.closeBlockContainer(c, curWindowID)
					b.invMu.Unlock()
					containerOpen = false
				}
				b.invMu.Lock()
				wID, err := b.openBlockContainer(c, targetPos)
				if err != nil {
					b.invMu.Unlock()
					log.Printf("bebot: [bundletest] attempt %d: open %v failed: %v\n", attempt+1, targetPos, err)
					noBundle++
					time.Sleep(50 * time.Millisecond)
					continue
				}
				curWindowID = wID
				containerOpen = true
			}

			// Scan container items for a bundle.
			b.mu.Lock()
			containerID := b.containerContainerID
			if containerID == 0 {
				containerID = protocol.ContainerLevelEntity
			}
			var bundleSlot byte
			var bundleItem protocol.ItemInstance
			foundBundle := false
			var itemsSummary []string
			for slot, item := range b.containerItems {
				if item.Stack.Count > 0 {
					name := b.itemNameLocked(item.Stack.ItemType.NetworkID)
					itemsSummary = append(itemsSummary, fmt.Sprintf("slot %d: %s (id=%d count=%d netID=%d)", slot, name, item.Stack.ItemType.NetworkID, item.Stack.Count, item.StackNetworkID))
					isBundle := strings.Contains(strings.ToLower(name), "bundle")
					if !isBundle && item.Stack.NBTData != nil {
						if _, hasItems := item.Stack.NBTData["Items"]; hasItems {
							isBundle = true
						}
					}
					if isBundle {
						bundleSlot = slot
						bundleItem = item
						foundBundle = true
					}
				}
			}
			b.mu.Unlock()

			if !foundBundle {
				if len(itemsSummary) == 0 {
					log.Printf("bebot: [bundletest] attempt %d: container at %v is empty (0 items loaded)\n", attempt+1, targetPos)
				} else {
					log.Printf("bebot: [bundletest] attempt %d: no bundle in %v (found: %s)\n", attempt+1, targetPos, strings.Join(itemsSummary, ", "))
				}
				noBundle++
				if lockedPos == nil && containerOpen {
					b.closeBlockContainer(c, curWindowID)
					b.invMu.Unlock()
					containerOpen = false
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}

			// Bundle found! Lock into this dropper since the bundle won't move away.
			if lockedPos == nil {
				lockedPos = &targetPos
				log.Printf("bebot: [bundletest] bundle found at %v — locked onto this dropper for all remaining attempts\n", targetPos)
				b.reply(fmt.Sprintf("Bundle found at (%d, %d, %d)! Locked onto this dropper.", targetPos.X(), targetPos.Y(), targetPos.Z()))
			}

			// Bundle found. Inspect its NBT to inspect inner stored items or dynamic bundle_id.
			bundleNBT := bundleItem.Stack.NBTData
			var bundleID uint32
			if bundleNBT != nil {
				if rawID, ok := bundleNBT["bundle_id"]; ok {
					switch id := rawID.(type) {
					case int64:
						bundleID = uint32(id)
					case int32:
						bundleID = uint32(id)
					case int16:
						bundleID = uint32(id)
					case byte:
						bundleID = uint32(id)
					case uint32:
						bundleID = id
					case int:
						bundleID = uint32(id)
					}
				}
			}

			innerItems := extractBundleInnerItems(bundleNBT)
			var innerName string
			var innerCount byte = 1
			var innerNetID int32

			if len(innerItems) > 0 {
				firstItem := innerItems[0]
				innerName, _ = firstItem["Name"].(string)
				innerCount = 1 // Right-click extracts 1 item from bundle
				innerNetID = b.itemNetworkIDByName(innerName)
			} else if bundleID != 0 {
				b.mu.Lock()
				dynItems, hasDyn := b.dynamicContainerItems[bundleID]
				b.mu.Unlock()
				if hasDyn && len(dynItems) > 0 {
					first := dynItems[0]
					innerNetID = first.Stack.ItemType.NetworkID
					innerName = b.itemName(innerNetID)
				} else {
					innerName = "bundle item"
				}
				innerCount = 1 // Right-click extracts 1 item from bundle
			}

			isEmpty := false
			if len(innerItems) == 0 {
				if bundleID == 0 {
					isEmpty = true
				} else {
					b.mu.Lock()
					dynItems, hasDyn := b.dynamicContainerItems[bundleID]
					b.mu.Unlock()
					if hasDyn && len(dynItems) == 0 {
						isEmpty = true
					}
				}
			}

			if isEmpty {
				if containerOpen {
					b.closeBlockContainer(c, curWindowID)
					b.invMu.Unlock()
					containerOpen = false
				}
				log.Printf("bebot: [bundletest] attempt %d: bundle at %v slot=%d has no inner items (NBT=%+v)\n", attempt+1, targetPos, bundleSlot, bundleNBT)
				noBundle++
				emptyBundleCount++
				if emptyBundleCount >= 3 {
					if okCount > 0 {
						b.reply(fmt.Sprintf("Bundle at (%d, %d, %d) slot %d ran out of items after %d extractions. Please refill items.", targetPos.X(), targetPos.Y(), targetPos.Z(), bundleSlot, okCount))
					} else {
						b.reply(fmt.Sprintf("Bundle found at (%d, %d, %d) slot %d, but it has no items inside it! Please put items into the bundle first.", targetPos.X(), targetPos.Y(), targetPos.Z(), bundleSlot))
					}
					return
				}
				time.Sleep(40 * time.Millisecond)
				continue
			}
			emptyBundleCount = 0

			targetDynID := bundleID
			if targetDynID == 0 {
				targetDynID = uint32(bundleItem.StackNetworkID)
			}

			log.Printf("bebot: [bundletest] attempt %d: bundle at %v slot=%d (bundle_id=%d, bundleStackNetID=%d) has %s — extracting %dx %s...\n",
				attempt+1, targetPos, bundleSlot, targetDynID, bundleItem.StackNetworkID, innerName, innerCount, strings.TrimPrefix(innerName, "minecraft:"))

			// RACE: Extract inner item from bundle onto cursor (Right-Click extraction).
			// As documented in bundle.md & bundle_glitch.md, right-clicking a bundle performs
			// a single TakeStackRequestAction popping the inner item directly onto the virtual
			// cursor (ContainerCursor #58). The bundle stays inside the dropper!
			dstSlot, hasDst := b.findPlayerSlotForItem(innerNetID)
			cursorEmpty := stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
			var reqOk bool
			var status uint8

			// Format 1: Dynamic container (ContainerDynamic = 67) with DynamicContainerID, Slot 0, StackNetworkID 0
			sourceDyn := protocol.StackRequestSlotInfo{
				Container: protocol.FullContainerName{
					ContainerID:        protocol.ContainerDynamic,
					DynamicContainerID: protocol.Option(targetDynID),
				},
				Slot:           0,
				StackNetworkID: 0,
			}
			take1 := &protocol.TakeStackRequestAction{}
			take1.Count = innerCount
			take1.Source = sourceDyn
			take1.Destination = cursorEmpty

			reqOk, status = b.sendItemStackRequest(c, []protocol.StackRequestAction{take1})
			log.Printf("bebot: [bundletest] fmt1 (ContainerDynamic 67, dynID=%d, slot=0, netID=0): reqOk=%v status=%d (%s)\n",
				targetDynID, reqOk, status, stackStatusName(status))

			// Format 2: Dropper container with DynamicContainerID & bundle slot + stackNetworkID
			if !reqOk {
				sourceSlotDyn := protocol.StackRequestSlotInfo{
					Container: protocol.FullContainerName{
						ContainerID:        containerID,
						DynamicContainerID: protocol.Option(targetDynID),
					},
					Slot:           bundleSlot,
					StackNetworkID: bundleItem.StackNetworkID,
				}
				take2 := &protocol.TakeStackRequestAction{}
				take2.Count = innerCount
				take2.Source = sourceSlotDyn
				take2.Destination = cursorEmpty

				reqOk, status = b.sendItemStackRequest(c, []protocol.StackRequestAction{take2})
				log.Printf("bebot: [bundletest] fmt2 (containerID=%d, dynID=%d, slot=%d, netID=%d): reqOk=%v status=%d (%s)\n",
					containerID, targetDynID, bundleSlot, bundleItem.StackNetworkID, reqOk, status, stackStatusName(status))
			}

			// Format 3: Dropper container with DynamicContainerID & Slot 0, netID 0
			if !reqOk {
				sourceDrop := protocol.StackRequestSlotInfo{
					Container: protocol.FullContainerName{
						ContainerID:        containerID,
						DynamicContainerID: protocol.Option(targetDynID),
					},
					Slot:           0,
					StackNetworkID: 0,
				}
				take3 := &protocol.TakeStackRequestAction{}
				take3.Count = innerCount
				take3.Source = sourceDrop
				take3.Destination = cursorEmpty

				reqOk, status = b.sendItemStackRequest(c, []protocol.StackRequestAction{take3})
				log.Printf("bebot: [bundletest] fmt3 (containerID=%d, dynID=%d, slot=0, netID=0): reqOk=%v status=%d (%s)\n",
					containerID, targetDynID, reqOk, status, stackStatusName(status))
			}

			// Format 4: Dropper container with DynamicContainerID & Slot 0, stackNetworkID
			if !reqOk {
				sourceDropNet := protocol.StackRequestSlotInfo{
					Container: protocol.FullContainerName{
						ContainerID:        containerID,
						DynamicContainerID: protocol.Option(targetDynID),
					},
					Slot:           0,
					StackNetworkID: bundleItem.StackNetworkID,
				}
				take4 := &protocol.TakeStackRequestAction{}
				take4.Count = innerCount
				take4.Source = sourceDropNet
				take4.Destination = cursorEmpty

				reqOk, status = b.sendItemStackRequest(c, []protocol.StackRequestAction{take4})
				log.Printf("bebot: [bundletest] fmt4 (containerID=%d, dynID=%d, slot=0, netID=%d): reqOk=%v status=%d (%s)\n",
					containerID, targetDynID, bundleItem.StackNetworkID, reqOk, status, stackStatusName(status))
			}

			// Format 5: Dropper container direct bundle slot (count=1)
			if !reqOk {
				sourceBundleSlot := protocol.StackRequestSlotInfo{
					Container: protocol.FullContainerName{
						ContainerID: containerID,
					},
					Slot:           bundleSlot,
					StackNetworkID: bundleItem.StackNetworkID,
				}
				take5 := &protocol.TakeStackRequestAction{}
				take5.Count = innerCount
				take5.Source = sourceBundleSlot
				take5.Destination = cursorEmpty

				reqOk, status = b.sendItemStackRequest(c, []protocol.StackRequestAction{take5})
				log.Printf("bebot: [bundletest] fmt5 (containerID=%d, slot=%d, netID=%d): reqOk=%v status=%d (%s)\n",
					containerID, bundleSlot, bundleItem.StackNetworkID, reqOk, status, stackStatusName(status))
			}

			if reqOk && hasDst {
				b.mu.Lock()
				if len(b.inventory) <= dstSlot {
					grown := make([]protocol.ItemInstance, dstSlot+1)
					copy(grown, b.inventory)
					b.inventory = grown
				}
				if b.inventory[dstSlot].Stack.Count == 0 {
					b.inventory[dstSlot] = protocol.ItemInstance{
						Stack: protocol.ItemStack{
							ItemType: protocol.ItemType{NetworkID: innerNetID},
							Count:    uint16(innerCount),
						},
					}
				} else {
					b.inventory[dstSlot].Stack.Count += uint16(innerCount)
				}
				b.mu.Unlock()
			}

			if reqOk {
				okCount++
				log.Printf("bebot: [bundletest] attempt %d: ItemStackResponse=OK — server accepted (possible race window hit!)\n", attempt+1)
			} else {
				rejectCount++
				log.Printf("bebot: [bundletest] attempt %d: ItemStackResponse rejected status=%d (%s)\n",
					attempt+1, status, stackStatusName(status))
			}

			// Small delay between attempts so we don't flood the connection.
			time.Sleep(40 * time.Millisecond)
		}

		if containerOpen && curWindowID != 0 {
			b.mu.Lock()
			c = b.conn
			b.mu.Unlock()
			if c != nil {
				b.closeBlockContainer(c, curWindowID)
			}
			b.invMu.Unlock()
			containerOpen = false
		}

		summary := fmt.Sprintf(
			"Bundle test complete (%d attempts): OK=%d  Rejected=%d  No-bundle=%d",
			attempts, okCount, rejectCount, noBundle,
		)
		log.Printf("bebot: [bundletest] %s\n", summary)
		b.reply(summary)

		if okCount > 0 {
			b.reply(fmt.Sprintf(
				"⚠ %d server-accepted responses while dropper was potentially transferring — check if items were actually duplicated by counting bundle contents and player inventory!",
				okCount,
			))
		} else {
			b.reply("No server-accepted races observed. Server may be patched or timing needs adjustment.")
		}
	}()
}

func (b *Bot) itemNetworkIDByName(name string) int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	clean := name
	if !strings.HasPrefix(clean, "minecraft:") {
		clean = "minecraft:" + clean
	}
	for id, n := range b.itemNamesByID {
		if strings.EqualFold(n, clean) || strings.EqualFold(strings.TrimPrefix(n, "minecraft:"), strings.TrimPrefix(clean, "minecraft:")) {
			return id
		}
	}
	return 0
}

func extractBundleInnerItems(nbt map[string]any) []map[string]any {
	if nbt == nil {
		return nil
	}
	for _, key := range []string{"Items", "items", "bundle_contents", "minecraft:storage_item", "contents"} {
		if raw, ok := nbt[key]; ok {
			items := parseItemList(raw)
			if len(items) > 0 {
				return items
			}
		}
	}
	if tag, ok := nbt["tag"].(map[string]any); ok {
		items := extractBundleInnerItems(tag)
		if len(items) > 0 {
			return items
		}
	}
	for _, val := range nbt {
		items := parseItemList(val)
		if len(items) > 0 {
			return items
		}
	}
	return nil
}

func parseItemList(raw any) []map[string]any {
	var result []map[string]any
	switch list := raw.(type) {
	case []any:
		for _, it := range list {
			if m, ok := it.(map[string]any); ok {
				result = append(result, m)
			}
		}
	case []map[string]any:
		result = append(result, list...)
	}
	return result
}
