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
			var fullCount byte = 1
			var innerNetID int32

			if len(innerItems) > 0 {
				firstItem := innerItems[0]
				innerName, _ = firstItem["Name"].(string)
				if cnt, ok := firstItem["Count"].(byte); ok && cnt > 0 {
					fullCount = cnt
				} else if cnt, ok := firstItem["Count"].(int32); ok && cnt > 0 {
					fullCount = byte(cnt)
				} else if cnt, ok := firstItem["Count"].(int16); ok && cnt > 0 {
					fullCount = byte(cnt)
				} else if cnt, ok := firstItem["Count"].(int); ok && cnt > 0 {
					fullCount = byte(cnt)
				}
				innerNetID = b.itemNetworkIDByName(innerName)
			} else if bundleID != 0 {
				b.mu.Lock()
				dynItems, hasDyn := b.dynamicContainerItems[bundleID]
				b.mu.Unlock()
				if hasDyn && len(dynItems) > 0 {
					first := dynItems[0]
					innerNetID = first.Stack.ItemType.NetworkID
					innerName = b.itemName(innerNetID)
					if first.Stack.Count > 0 {
						fullCount = byte(first.Stack.Count)
					}
				} else {
					innerName = "bundle item"
				}
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

			bundleCount := byte(bundleItem.Stack.Count)
			if bundleCount == 0 {
				bundleCount = 1
			}

			log.Printf("bebot: [bundletest] attempt %d: bundle at %v slot=%d (bundle_id=%d, bundleStackNetID=%d) has %s (count=%d) — testing extractions...\n",
				attempt+1, targetPos, bundleSlot, targetDynID, bundleItem.StackNetworkID, innerName, fullCount)

			dstSlot, hasDst := b.findPlayerSlotForItem(innerNetID)
			var reqOk bool
			var status uint8
			var extractedCount byte

			// tryTx attempts a transaction for a given candidate source.
			// It tests balanced take+place (matching Bedrock server-authoritative rules)
			// with matching StackNetworkID on cursor, and single take.
			tryTx := func(name string, src protocol.StackRequestSlotInfo, count byte) bool {
				if count == 0 {
					count = 1
				}
				if hasDst {
					dstContainer, dstSlotInContainer, mapOk := playerSlotContainer(dstSlot)
					if mapOk {
						cursorEmpty := stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
						cursorFull := stackReqSlotInfo(protocol.ContainerCursor, 0, src.StackNetworkID)
						dstSlotInfo := stackReqSlotInfo(dstContainer, dstSlotInContainer, 0)

						take := &protocol.TakeStackRequestAction{}
						take.Count = count
						take.Source = src
						take.Destination = cursorEmpty

						place := &protocol.PlaceStackRequestAction{}
						place.Count = count
						place.Source = cursorFull
						place.Destination = dstSlotInfo

						ok, st := b.sendItemStackRequest(c, []protocol.StackRequestAction{take, place})
						log.Printf("bebot: [bundletest] %s (count=%d, balanced take+place): ok=%v status=%d (%s)\n",
							name, count, ok, st, stackStatusName(st))
						if ok {
							reqOk = true
							status = st
							extractedCount = count
							return true
						}
					}
				}

				takeSingle := &protocol.TakeStackRequestAction{}
				takeSingle.Count = count
				takeSingle.Source = src
				takeSingle.Destination = stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
				ok, st := b.sendItemStackRequest(c, []protocol.StackRequestAction{takeSingle})
				log.Printf("bebot: [bundletest] %s (count=%d, single take): ok=%v status=%d (%s)\n",
					name, count, ok, st, stackStatusName(st))
				if ok {
					reqOk = true
					status = st
					extractedCount = count
					return true
				}
				return false
			}

			// Candidates to test:
			// 1. "Take all" inner items from DynamicContainer (ContainerDynamic = 67, slot 0)
			if !reqOk {
				tryTx("fmt1-all", protocol.StackRequestSlotInfo{
					Container: protocol.FullContainerName{
						ContainerID:        protocol.ContainerDynamic,
						DynamicContainerID: protocol.Option(targetDynID),
					},
					Slot:           0,
					StackNetworkID: 0,
				}, fullCount)
			}

			// 2. "Take 1" inner item from DynamicContainer (ContainerDynamic = 67, slot 0)
			if !reqOk && fullCount > 1 {
				tryTx("fmt1-one", protocol.StackRequestSlotInfo{
					Container: protocol.FullContainerName{
						ContainerID:        protocol.ContainerDynamic,
						DynamicContainerID: protocol.Option(targetDynID),
					},
					Slot:           0,
					StackNetworkID: 0,
				}, 1)
			}

			// 3. Dropper container with DynamicContainerID & bundleSlot + bundleStackNetworkID
			if !reqOk {
				tryTx("fmt2-all", protocol.StackRequestSlotInfo{
					Container: protocol.FullContainerName{
						ContainerID:        containerID,
						DynamicContainerID: protocol.Option(targetDynID),
					},
					Slot:           bundleSlot,
					StackNetworkID: bundleItem.StackNetworkID,
				}, fullCount)
			}

			// 4. Dropper container with DynamicContainerID & Slot 0, netID 0
			if !reqOk {
				tryTx("fmt3-all", protocol.StackRequestSlotInfo{
					Container: protocol.FullContainerName{
						ContainerID:        containerID,
						DynamicContainerID: protocol.Option(targetDynID),
					},
					Slot:           0,
					StackNetworkID: 0,
				}, fullCount)
			}

			// 5. Dropper container direct bundleSlot (taking bundle count) - as in commit 5208bb1
			if !reqOk {
				tryTx("fmt4-dropper-bundle", protocol.StackRequestSlotInfo{
					Container: protocol.FullContainerName{
						ContainerID: containerID,
					},
					Slot:           bundleSlot,
					StackNetworkID: bundleItem.StackNetworkID,
				}, bundleCount)
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
							Count:    uint16(extractedCount),
						},
					}
				} else {
					b.inventory[dstSlot].Stack.Count += uint16(extractedCount)
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
