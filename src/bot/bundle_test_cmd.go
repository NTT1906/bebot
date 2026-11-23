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

	if len(p) < 7 {
		b.reply("Usage: bundletest <ax> <ay> <az> <bx> <by> <bz> [attempts]  |  bundletest stop")
		return
	}

	coords := make([]int32, 6)
	for i := 0; i < 6; i++ {
		v, err := strconv.Atoi(p[i+1])
		if err != nil {
			b.reply(fmt.Sprintf("Invalid coordinate %q: %v", p[i+1], err))
			return
		}
		coords[i] = int32(v)
	}
	posA := protocol.BlockPos{coords[0], coords[1], coords[2]}
	posB := protocol.BlockPos{coords[3], coords[4], coords[5]}

	attempts := 50
	if len(p) >= 8 {
		if v, err := strconv.Atoi(p[7]); err == nil && v > 0 {
			attempts = v
		}
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

		b.reply(fmt.Sprintf(
			"Bundle test started: dropper A=(%d,%d,%d) B=(%d,%d,%d) attempts=%d",
			posA.X(), posA.Y(), posA.Z(),
			posB.X(), posB.Y(), posB.Z(),
			attempts,
		))
		log.Printf("bebot: [bundletest] starting %d attempts on droppers A=%v B=%v\n", attempts, posA, posB)

		var okCount, rejectCount, noBundle int

		for attempt := 0; attempt < attempts; attempt++ {
			select {
			case <-ctx.Done():
				b.reply("Bundle test cancelled.")
				return
			default:
			}

			// Alternate between A and B each attempt so we're opening whichever
			// currently holds the bundle while the dropper clock may be firing.
			targetPos := posA
			if attempt%2 == 1 {
				targetPos = posB
			}

			b.mu.Lock()
			c = b.conn
			b.mu.Unlock()
			if c == nil {
				b.reply("Bundle test aborted: disconnected.")
				return
			}

			// Open the dropper (uses existing openBlockContainer which handles
			// ContainerOpen + InventoryContent wait).
			b.invMu.Lock()
			wID, err := b.openBlockContainer(c, targetPos)
			if err != nil {
				b.invMu.Unlock()
				log.Printf("bebot: [bundletest] attempt %d: open %v failed: %v\n", attempt+1, targetPos, err)
				noBundle++
				time.Sleep(50 * time.Millisecond)
				continue
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
					itemsSummary = append(itemsSummary, fmt.Sprintf("slot %d: %s (id=%d count=%d)", slot, name, item.Stack.ItemType.NetworkID, item.Stack.Count))
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
				b.closeBlockContainer(c, wID)
				b.invMu.Unlock()
				if len(itemsSummary) == 0 {
					log.Printf("bebot: [bundletest] attempt %d: container at %v is empty (0 items loaded)\n", attempt+1, targetPos)
				} else {
					log.Printf("bebot: [bundletest] attempt %d: no bundle in %v (found: %s)\n", attempt+1, targetPos, strings.Join(itemsSummary, ", "))
				}
				noBundle++
				time.Sleep(50 * time.Millisecond)
				continue
			}

			// Bundle found. Inspect its NBT to inspect inner stored items.
			bundleNBT := bundleItem.Stack.NBTData
			var innerItems []map[string]any
			var innerSummary []string
			if bundleNBT != nil {
				if rawItems, ok := bundleNBT["Items"]; ok {
					switch items := rawItems.(type) {
					case []any:
						for _, it := range items {
							if m, ok := it.(map[string]any); ok {
								innerItems = append(innerItems, m)
								n, _ := m["Name"].(string)
								cnt, _ := m["Count"].(byte)
								innerSummary = append(innerSummary, fmt.Sprintf("%dx %s", cnt, strings.TrimPrefix(n, "minecraft:")))
							}
						}
					case []map[string]any:
						for _, m := range items {
							innerItems = append(innerItems, m)
							n, _ := m["Name"].(string)
							cnt, _ := m["Count"].(byte)
							innerSummary = append(innerSummary, fmt.Sprintf("%dx %s", cnt, strings.TrimPrefix(n, "minecraft:")))
						}
					}
				}
			}

			if len(innerItems) == 0 {
				b.closeBlockContainer(c, wID)
				b.invMu.Unlock()
				log.Printf("bebot: [bundletest] attempt %d: bundle at %v slot=%d is empty (put items inside it first)\n", attempt+1, targetPos, bundleSlot)
				noBundle++
				time.Sleep(50 * time.Millisecond)
				continue
			}

			firstItem := innerItems[0]
			innerName, _ := firstItem["Name"].(string)
			innerCount := byte(1)
			if cnt, ok := firstItem["Count"].(byte); ok && cnt > 0 {
				innerCount = cnt
			} else if cnt, ok := firstItem["Count"].(int32); ok && cnt > 0 {
				innerCount = byte(cnt)
			} else if cnt, ok := firstItem["Count"].(int16); ok && cnt > 0 {
				innerCount = byte(cnt)
			}
			innerNetID := b.itemNetworkIDByName(innerName)

			contentsStr := strings.Join(innerSummary, ", ")
			log.Printf("bebot: [bundletest] attempt %d: bundle at %v slot=%d has %s — extracting %dx %s...\n",
				attempt+1, targetPos, bundleSlot, contentsStr, innerCount, strings.TrimPrefix(innerName, "minecraft:"))

			// RACE: Extract inner item from bundle onto cursor, then place into player inventory.
			// The bundle stays inside the dropper!
			dstSlot, hasDst := b.findFreePlayerSlot()
			var reqOk bool
			var status uint8

			if hasDst {
				dstContainer, dstSlotInContainer, mapOk := playerSlotContainer(dstSlot)
				if mapOk {
					cursorEmpty := stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
					cursorFull := stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
					dstSlotInfo := stackReqSlotInfo(dstContainer, dstSlotInContainer, 0)

					// Format 1: ContainerDynamic (67) with DynamicContainerID
					sourceDyn := protocol.StackRequestSlotInfo{
						Container: protocol.FullContainerName{
							ContainerID:        protocol.ContainerDynamic,
							DynamicContainerID: protocol.Option(uint32(bundleItem.StackNetworkID)),
						},
						Slot:           0,
						StackNetworkID: 0,
					}
					takeDyn := &protocol.TakeStackRequestAction{}
					takeDyn.Count = innerCount
					takeDyn.Source = sourceDyn
					takeDyn.Destination = cursorEmpty

					placeDyn := &protocol.PlaceStackRequestAction{}
					placeDyn.Count = innerCount
					placeDyn.Source = cursorFull
					placeDyn.Destination = dstSlotInfo

					reqOk, status = b.sendItemStackRequest(c, []protocol.StackRequestAction{takeDyn, placeDyn})

					// Format 2 fallback: dropper container with DynamicContainerID
					if !reqOk {
						sourceDrop := protocol.StackRequestSlotInfo{
							Container: protocol.FullContainerName{
								ContainerID:        containerID,
								DynamicContainerID: protocol.Option(uint32(bundleItem.StackNetworkID)),
							},
							Slot:           0,
							StackNetworkID: 0,
						}
						takeDrop := &protocol.TakeStackRequestAction{}
						takeDrop.Count = innerCount
						takeDrop.Source = sourceDrop
						takeDrop.Destination = cursorEmpty

						placeDrop := &protocol.PlaceStackRequestAction{}
						placeDrop.Count = innerCount
						placeDrop.Source = cursorFull
						placeDrop.Destination = dstSlotInfo

						reqOk, status = b.sendItemStackRequest(c, []protocol.StackRequestAction{takeDrop, placeDrop})
					}

					if reqOk {
						b.mu.Lock()
						if len(b.inventory) <= dstSlot {
							grown := make([]protocol.ItemInstance, dstSlot+1)
							copy(grown, b.inventory)
							b.inventory = grown
						}
						b.inventory[dstSlot] = protocol.ItemInstance{
							Stack: protocol.ItemStack{
								ItemType: protocol.ItemType{NetworkID: innerNetID},
								Count:    uint16(innerCount),
							},
						}
						b.mu.Unlock()
					}
				}
			}

			b.closeBlockContainer(c, wID)
			b.invMu.Unlock()

			if reqOk {
				okCount++
				log.Printf("bebot: [bundletest] attempt %d: ItemStackResponse=OK — server accepted (possible race window hit!)\n", attempt+1)
			} else {
				rejectCount++
				log.Printf("bebot: [bundletest] attempt %d: ItemStackResponse rejected status=%d (%s)\n",
					attempt+1, status, stackStatusName(status))
			}

			// Small delay between attempts so we don't flood the connection.
			time.Sleep(60 * time.Millisecond)
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
