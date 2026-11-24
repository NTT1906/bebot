# Minecraft Bedrock Protocol: Comprehensive Item Dropping Guide (drop_item2)

This document provides a complete technical reference for all three canonical ways to drop items in **Minecraft: Bedrock Edition** (Protocol 1.16+ through 1.21.20+ / Protocol 712+). It synthesizes protocol specifications, packet structures, server-authoritative transaction lifecycles, and real-world debugging discoveries from Bedrock Dedicated Server (BDS) implementations.

---

## 1. High-Level Comparison Matrix

| Feature | Method 1: First-Person In-World Drop | Method 2: GUI Direct Slot Drop | Method 3: Cursor Drag & Drop |
| :--- | :--- | :--- | :--- |
| **User Input in Game** | Pressing drop key while walking in the world | Hovering cursor over a slot in GUI and pressing `Q` | Left-clicking stack to cursor, dragging outside GUI window, and clicking |
| **Protocol Packet** | `packet.InventoryTransaction` | `packet.ItemStackRequest` | `packet.ItemStackRequest` |
| **Transaction Model** | Balanced container-to-world transaction | Server-authoritative action batch | Server-authoritative action batch |
| **Actions Involved** | 2 balanced `InventoryAction`s:<br>1. Container slot decrement<br>2. World spawn item entity | 1 action per slot:<br>`DropStackRequestAction` | 2 actions per slot:<br>1. `TakeStackRequestAction`<br>2. `DropStackRequestAction` |
| **Uses Virtual Cursor (`ContainerCursor`)?** | ❌ No | ❌ No *(bypasses cursor completely)* | ✔️ Yes (`protocol.ContainerCursor` ID 59) |
| **Container Screen Required?** | ❌ **No** (works anywhere in world) | ⚠️ **Yes** (requires active GUI screen; fails in HUD with `status=49`) | ⚠️ **Yes** (requires active block/container window) |
| **Multi-Slot Batching in 1 Packet?** | ✔️ Coalesced egress flush across all slots | **✔️ Yes** (batch `ItemStackRequest` when GUI is open) | ❌ No (Destination collision on cursor slot 0 triggers `status=50`) |
| **Drops Bundles Intact?** | ✔️ **Yes** (drops bundle item itself, preserving contents) | ✔️ **Yes** (drops bundle item itself) | ✔️ **Yes** (drops bundle item itself) |
| **Bebot Command** | **`bebot dropall [all]`**<br>**`bebot drop <slot> [amount]`** | GUI context drop | **`bebot dropall2 [all]`**<br>**`bebot drop2 <slot> [amount]`** |

---

## 2. In-Depth Protocol Mechanics

### Method 1: First-Person Hotbar / In-World Drop
When walking around in the world with no inventory GUI open, the client communicates drops via legacy balanced transactions rather than the modern `ItemStackRequest` queue.

```
[ Player Hotbar Slot ] ──(Container Action)──> [ Player Hand / Eyes ] ──(World Action)──> [ World Entity Spawn ]
```

#### Packet Structure
* **Packet**: `packet.InventoryTransaction`
* **TransactionData**: `&protocol.NormalTransactionData{}`
* **Legacy Request ID**: Negative odd/even integer tracking request sequence (e.g. `-860`).
* **LegacySetItemSlots**: Lists the container and slot modified:
  ```go
  LegacySetItemSlots: []protocol.LegacySetItemSlot{
      {ContainerID: protocol.ContainerHotBar, Slots: []byte{slotIn}},
  }
  ```
* **Actions**: Exactly two paired `protocol.InventoryAction` entries that balance the equation ($\text{OldItem} = \text{NewItem} + \text{DroppedItem}$):
  1. **Action 1 (Source Slot - Container)**:
     * `SourceType`: `protocol.InventoryActionSourceContainer` (ID `0`)
     * `WindowID`: `protocol.Option(int8(protocol.WindowIDInventory))` (`0`)
     * `InventorySlot`: Target slot index (`0`–`35`)
     * `OldItem`: Item stack before drop (count $N$, matching server `StackNetworkID`)
     * `NewItem`: Remaining item stack (count $N - \text{dropCount}$, or empty air if $0$)
  2. **Action 2 (World Entity Spawn - World)**:
     * `SourceType`: `protocol.InventoryActionSourceWorld` (ID `2`)
     * `SourceFlags`: `protocol.Option(uint32(1))` (`InventoryActionSourceWorldFlagDropItem`)
     * `InventorySlot`: `0`
     * `OldItem`: Air (`protocol.ItemInstance{}`)
     * `NewItem`: Dropped stack entity (count $\text{dropCount}$)

---

### Method 2: Direct Slot Drop (GUI `Q` Key)
When viewing an inventory screen, hovering the mouse cursor over a slot and pressing `Q` (or `Ctrl+Q` to drop the whole stack) fires a direct slot drop.

```
[ Inventory Slot ] ─────────────────────(DropStackRequestAction)─────────────────────> [ World Entity Spawn ]
                                   (No Cursor Involved)
```

#### Key Characteristics
* **Packet**: `packet.ItemStackRequest`
* **Actions**: Exactly one `protocol.DropStackRequestAction` per dropped stack:
  ```go
  &protocol.DropStackRequestAction{
      Count: byte(dropCount),
      Source: protocol.StackRequestSlotInfo{
          Container:      protocol.FullContainerName{ContainerID: containerID},
          Slot:           slotInContainer,
          StackNetworkID: currentStackNetworkID,
      },
      Randomly: false, // Random scatter velocity flag
  }
  ```
* **Why It Is the Most Efficient Method**:
  1. **Zero Cursor Interaction**: Bypasses `ContainerCursor` completely, eliminating virtual cursor desync and `status=50` errors.
  2. **Single-Packet Batching**: Bedrock servers permit multiple `DropStackRequestAction` entries inside a single `packet.ItemStackRequest`. Bebot can drop all 36 inventory slots simultaneously in one atomic network packet (`bebot dropall all`).
  3. **Halved Packet Size**: Requires only 1 action per slot instead of 2 actions.

---

### Method 3: Cursor Drag & Drop (Outside GUI Window)
When a player clicks an item with the mouse cursor, drags it into the void outside the GUI window boundaries, and clicks to toss it on the ground:

```
[ Inventory Slot ] ──(TakeStackRequestAction)──> [ ContainerCursor #59 ] ──(DropStackRequestAction)──> [ World Entity Spawn ]
```

#### Key Characteristics
* **Packet**: `packet.ItemStackRequest`
* **Actions**: Two sequential actions executed in order:
  1. **Step 1: Pick up to Cursor (`TakeStackRequestAction`)**:
     * `Source`: `protocol.StackRequestSlotInfo` of the origin inventory slot.
     * `Destination`: `protocol.StackRequestSlotInfo` of `protocol.ContainerCursor` (Container ID `59`, Slot `0`, `StackNetworkID: 0`).
     * `Count`: Count picked up to cursor.
  2. **Step 2: Drop from Cursor (`DropStackRequestAction`)**:
     * `Source`: `protocol.StackRequestSlotInfo` of `protocol.ContainerCursor` (Container ID `59`, Slot `0`, `StackNetworkID: itemStackNetID`).
     * `Count`: Count dropped from cursor into the world.
     * `Randomly`: `false`.

#### Crucial Batching Constraint: Must Be Slot-by-Slot
Because `protocol.ContainerCursor` contains only **a single slot (Slot 0)**, multiple items cannot be picked up to the cursor at the same time. If a client attempts to batch multiple `[Take, Drop]` pairs in a single `ItemStackRequest`, BDS evaluates the destination slots upfront before executing drops, resulting in:
```
status=50 (failed to validate destination slot)
```
Therefore, Cursor Drag & Drop (`bebot dropall2`) must iterate sequentially slot-by-slot with a small interval (e.g. 30ms).

---

## 3. Real-World Debugging Lessons & Server Pitfalls

### Pitfall 1: The `InteractActionOpenInventory` on Self Trap
A common architectural error in Bedrock bot development is assuming that dropping items requires opening the player's inventory GUI over the network:
```go
// DO NOT DO THIS!
c.WritePacket(&packet.Interact{
    ActionType:            packet.InteractActionOpenInventory,
    TargetEntityRuntimeID: botEntityID, // Interacting with self
})
```
* **What Happens**: In Bedrock, `InteractActionOpenInventory` is strictly for **external entities** (horses, donkeys, llamas, chest boats). When sent targeting the player's own entity ID, BDS responds with a placeholder window:
  ```
  ContainerOpen WindowID=X ContainerType=255 Pos=[-13 67 19]
  ```
  (`ContainerType=255` is `CONTAINER_TYPE_NONE`).
* **The Fatal Consequence**: 500ms after opening `ContainerType=255`, BDS's anti-cheat flags an unauthorized entity interaction:
  ```
  last packet recv: *packet.PacketViolationWarning
  nethernet: SCTP transport closed
  ```
  The server instantly terminates the connection!
* **Rule**: **Never send `InteractActionOpenInventory` for player inventory drops.** Personal inventory is client-side and does not require any network interaction packet to view or drop.

---

### Pitfall 2: `ContainerCursor` (ID 59) Lifecycle on BDS
* When an **external block container** (chest, dropper, dispenser, furnace) is open:
  BDS creates an active `ContainerManagerModel` containing a registered `PlayerCursorContainer`. In this state, `TakeStackRequestAction` to `ContainerCursor` (59) succeeds (`status=0 OK`).
* When **no container screen is open** (or when dummy `ContainerType=255` is open):
  BDS has no registered cursor container in the active container model. Any `TakeStackRequestAction` specifying `ContainerCursor` returns:
  ```
  status=50 (ItemStackResponseStatusFailedToValidateDstSlot)
  ```
  because the destination container does not exist.

---

### Pitfall 3: Dynamic `StackNetworkID` Desynchronization
Bedrock Dedicated Server strictly enforces that `Source.StackNetworkID` matches the server's internal runtime ID:
* If the client passes `StackNetworkID: 0` (or an outdated ID from before a container transfer), BDS rejects the transaction with:
  ```
  status=49 (ItemStackResponseStatusFailedToValidateSrcSlot)
  ```
* **Solution**: Bebot listens for incoming `packet.ItemStackResponse` and updates `b.inventory[slot].StackNetworkID` directly from `response.ContainerInfo.SlotInfo` on every transaction.

---

### Pitfall 4: Action Count Range Assertion `[1, 64]`
In Bedrock's C++ binary packet deserializer (`ItemStackRequestPacket::read` / `readNoHeader`), the item `Count` field in `DropStackRequestAction`, `TakeStackRequestAction`, and `PlaceStackRequestAction` has an explicit stream-level bounds check:
* **Strict Range:** The count value **must be in the closed range $[1, 64]$**.
* **What Happens if Violated:**
  If an action specifies `Count: 0` or `Count > 64` (e.g. `Count: 66` after two successive bundle item extractions merged into one oversized slot), BDS's `ReadOnlyBinaryStream` throws an unrecoverable deserialization exception:
  ```
  PacketViolationWarning Type=0 Severity=2 PacketID=147 Context="the input is outside of the accepted range [1, 64]\nreadNoHeader failed! packetId: 147"
  ```
  The server flags this as a critical protocol violation and immediately terminates the SCTP network channel (`nethernet: SCTP transport closed`).
* **Solution**:
  1. `findPlayerSlotForCount(netID, incomingCount)` ensures placing items into player inventory never allows a slot stack to exceed 64 items (automatically spilling into a new empty slot).
  2. `dropSlots` chunks every drop action into slices of at most 64 items (`rem = 66 -> [64, 2]`), ensuring every transmitted `DropStackRequestAction` strictly complies with $[1, 64]$.

---

### Pitfall 5: HUD ScreenHandler Rejection (`status=49`) & First-Person Drop Solution
* **What Happened:**
  When a bot stands in the world without an open container screen and sends an `ItemStackRequest` containing `DropStackRequestAction`, BDS evaluates the request through `ScreenHandlerHUD` / `HUDContainerScreenValidator`.
  Because `HUDContainerScreenValidator` only permits HUD quick-interactions and does NOT permit container-screen `DropStackRequestAction`s on personal inventory slots, BDS rejects the request immediately with:
  ```
  inventory request=-861 rejected status=49 (failed to validate source slot)
  ```
* **Why Bundles Drop Intact:**
  In Minecraft Bedrock, bundles inside personal inventory slots are single items (`protocol.ItemInstance`) containing bundle ID and item arrays in their NBT metadata. When dropping a bundle slot directly via `InventoryTransaction`, the bundle item itself is transferred to `InventoryActionSourceWorld`, leaving its internal items preserved inside the dropped entity.
* **The Solution:**
  Native Bedrock clients drop items in first-person via `packet.InventoryTransaction` with `NormalTransactionData` (Method 1). All slot transactions are queued and flushed in a single network transmission, dropping items instantaneously without requiring an open container screen.

---

## 4. Bebot Implementation & Command Reference

Implemented in [src/bot/inventory.go](file:///d:/code/goland/bebot/src/bot/inventory.go):

| In-Game Chat Command | Method Used | Execution Characteristics |
| :--- | :--- | :--- |
| **`bebot dropall [all]`** | **First-Person World Drop** | Iterates all non-empty inventory slots, constructs balanced `InventoryTransaction` pairs, and flushes in a single network burst. Drops any item (including bundles intact). |
| **`bebot drop <slot> [count]`** | **First-Person World Drop** | Drops `count` items (or all) from inventory slot `slot` directly into the world via native `InventoryTransaction`. |
| **`bebot dropall2 [all]`** | **Cursor Drag & Drop** | Drops each inventory slot sequentially via virtual cursor (`[Take to cursor, Drop from cursor]`). Requires an active container window. |
| **`bebot drop2 <slot> [count]`** | **Cursor Drag & Drop** | Takes `count` items from `slot` to virtual cursor, then drops them from cursor to the world. Requires an active container window. |
