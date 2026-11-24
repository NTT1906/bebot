# Minecraft Bedrock Protocol: Comprehensive Item Dropping Guide (drop_item2)

This document provides a complete technical reference for all three canonical ways to drop items in **Minecraft: Bedrock Edition** (Protocol 1.16+ through 1.21.20+ / Protocol 712+). It synthesizes protocol specifications, packet structures, server-authoritative transaction lifecycles, and real-world debugging discoveries from Bedrock Dedicated Server (BDS) implementations.

---

## 1. High-Level Comparison Matrix

| Feature | Method 1: First-Person In-World Drop (`drop` / `dropall`) | Method 2: Cursor Drag & Drop (`drop2` / `dropall2`) | Method 3: GUI Direct Slot Drop (`drop3` / `dropall3`) |
| :--- | :--- | :--- | :--- |
| **User Input in Game** | Pressing drop key (`Q`) while walking in the world | Left-clicking stack to cursor, dragging outside GUI window, and clicking | Hovering cursor over a slot in GUI and pressing `Q` |
| **Protocol Packet** | `packet.InventoryTransaction` | `packet.ItemStackRequest` | `packet.ItemStackRequest` |
| **Transaction Model** | Balanced container-to-world transaction | Server-authoritative action batch | Server-authoritative action batch |
| **Actions Involved** | 2 balanced `InventoryAction`s:<br>1. Container slot decrement<br>2. World spawn item entity | 2 actions per slot:<br>1. `TakeStackRequestAction`<br>2. `DropStackRequestAction` | 1 action per slot:<br>`DropStackRequestAction` |
| **Uses Virtual Cursor (`ContainerCursor`)?** | ❌ No | ✔️ Yes (`protocol.ContainerCursor` ID 59) | ❌ No *(bypasses cursor completely)* |
| **Container Screen Lifecycle** | ❌ **None** (works anywhere in world without opening/closing screens) | 🔄 **Auto Open/Close** (opens inventory screen with `TargetEntityRuntimeID: 0`, drops, closes) | 🔄 **Auto Open/Close** (opens inventory screen with `TargetEntityRuntimeID: 0`, drops, closes) |
| **Multi-Slot Batching in 1 Packet?** | ✔️ Coalesced egress flush across all slots | ❌ No (Destination collision on cursor slot 0 triggers `status=50`) | **✔️ Yes** (batch `ItemStackRequest` containing all slots simultaneously) |
| **Drops Bundles Intact?** | ✔️ **Yes** (drops bundle item itself, preserving contents) | ✔️ **Yes** (drops bundle item itself) | ✔️ **Yes** (drops bundle item itself) |
| **Drop Direction** | Look/eye vector (default `random=false`) or spherical scatter (`random=true`) | Look/eye vector (default `random=false`) or spherical scatter (`random=true`) | Look/eye vector (default `random=false`) or spherical scatter (`random=true`) |
| **Bebot Command** | **`bebot dropall [all\|each] [random]`**<br>**`bebot drop <slot> [count] [random]`** | **`bebot dropall2 [all\|each] [random]`**<br>**`bebot drop2 <slot> [count] [random]`** | **`bebot dropall3 [all\|each] [random]`**<br>**`bebot drop3 <slot> [count] [random]`** |

---

## 2. In-Depth Protocol Mechanics

### Method 1: First-Person In-World Drop (`drop` / `dropall`)
When walking around in the world with no inventory GUI open, the client communicates drops via legacy balanced transactions rather than the modern `ItemStackRequest` queue.

```
[ Player Inventory Slot ] ──(Container Action)──> [ Player Hand / Eyes ] ──(World Action)──> [ World Entity Spawn ]
```

#### Packet Structure
* **Packet**: `packet.InventoryTransaction`
* **TransactionData**: `&protocol.NormalTransactionData{}`
* **Actions**: Exactly two paired `protocol.InventoryAction` entries that balance the equation ($\text{OldItem} = \text{NewItem} + \text{DroppedItem}$):
  1. **Action 1 (Source Slot - Container)**:
     * `SourceType`: `protocol.InventoryActionSourceContainer` (ID `0`)
     * `WindowID`: `protocol.Option(int8(protocol.WindowIDInventory))` (`0`)
     * `InventorySlot`: Target slot index (`0`–`35`)
     * `OldItem`: Item stack before drop (count $N$, matching server `StackNetworkID`)
     * `NewItem`: Remaining item stack (count $N - \text{dropCount}$, or empty air if $0$)
  2. **Action 2 (World Entity Spawn - World)**:
     * `SourceType`: `protocol.InventoryActionSourceWorld` (ID `2`)
     * `SourceFlags`: `protocol.Option(uint32(0))` for look/eye vector (or `1` for random scatter)
     * `InventorySlot`: `0`
     * `OldItem`: Air (`protocol.ItemInstance{}`)
     * `NewItem`: Dropped stack entity (count $\text{dropCount}$)

#### Why It Is Ideal for General Play:
* **Zero UI Overhead**: Never opens or closes an inventory screen.
* **All Slots Accessible**: Drops items from hotbar (0–8) and backpack (9–35) alike.
* **Instant Single Network Flush**: All drops are serialized and sent in a single network flush.

---

### Method 2: Cursor Drag & Drop (`drop2` / `dropall2`)
When a player clicks an item with the mouse cursor, drags it outside the GUI window boundaries, and clicks to drop it:

```
[ Open Container Screen ] ──> [ Slot ] ──(Take)──> [ ContainerCursor #59 ] ──(Drop)──> [ World ] ──> [ Close Container Screen ]
```

#### Key Characteristics
* **Packet**: `packet.ItemStackRequest`
* **Auto Container Lifecycle**:
  * If no container screen is open, automatically opens inventory screen:
    `packet.Interact{ActionType: packet.InteractActionOpenInventory, TargetEntityRuntimeID: 0}`
  * Performs drops sequentially slot-by-slot.
  * Closes inventory screen when done:
    `packet.ContainerClose{WindowID: 0, ContainerType: byte(protocol.ContainerTypeInventory), ServerSide: false}`
* **Actions per Slot**: Two sequential actions executed in order:
  1. **Step 1: Pick up to Cursor (`TakeStackRequestAction`)**:
     * `Source`: `protocol.StackRequestSlotInfo` of the origin inventory slot.
     * `Destination`: `protocol.StackRequestSlotInfo` of `protocol.ContainerCursor` (Container ID `59`, Slot `0`, `StackNetworkID: 0`).
     * `Count`: Count picked up to cursor (clamped to $[1, 64]$).
  2. **Step 2: Drop from Cursor (`DropStackRequestAction`)**:
     * `Source`: `protocol.StackRequestSlotInfo` of `protocol.ContainerCursor` (Container ID `59`, Slot `0`, `StackNetworkID: itemStackNetID`).
     * `Count`: Count dropped from cursor into the world.
     * `Randomly`: `false` (eye direction) or `true` (random scatter).

#### Crucial Batching Constraint: Must Be Slot-by-Slot
Because `protocol.ContainerCursor` contains only **a single slot (Slot 0)**, multiple items cannot be picked up to the cursor at the same time. If a client attempts to batch multiple `[Take, Drop]` pairs in a single `ItemStackRequest`, BDS evaluates the destination slots upfront before executing drops, resulting in:
```
status=50 (failed to validate destination slot)
```
Therefore, Cursor Drag & Drop iterates sequentially slot-by-slot with a small interval (30ms).

---

### Method 3: GUI Direct Slot Drop (`drop3` / `dropall3`)
When viewing an inventory screen, hovering the mouse cursor over a slot and pressing `Q` (or `Ctrl+Q` to drop the whole stack) fires a direct slot drop.

```
[ Open Container Screen ] ──> [ Inventory Slot ] ─────(DropStackRequestAction)─────> [ World Entity Spawn ] ──> [ Close Screen ]
                                                (No Cursor Involved)
```

#### Key Characteristics
* **Packet**: `packet.ItemStackRequest`
* **Auto Container Lifecycle**:
  * Automatically opens inventory screen (`InteractActionOpenInventory`, `TargetEntityRuntimeID: 0`).
  * Closes inventory screen when done (`ContainerClose`).
* **Actions**: Exactly one `protocol.DropStackRequestAction` per dropped stack:
  ```go
  &protocol.DropStackRequestAction{
      Count: byte(dropCount),
      Source: protocol.StackRequestSlotInfo{
          Container:      protocol.FullContainerName{ContainerID: containerID},
          Slot:           slotInContainer,
          StackNetworkID: currentStackNetworkID,
      },
      Randomly: random, // false = eye direction, true = random scatter
  }
  ```
* **Why It Is Highly Efficient**:
  1. **Zero Cursor Interaction**: Bypasses `ContainerCursor` completely, eliminating virtual cursor desync and `status=50` errors.
  2. **Single-Packet Batching**: Bedrock servers permit multiple `DropStackRequestAction` entries inside a single `packet.ItemStackRequest`. Bebot drops all non-empty inventory slots simultaneously in one atomic network packet (`bebot dropall3`). If batching is rejected, it automatically falls back to slot-by-slot execution.
  3. **Halved Packet Size**: Requires only 1 action per slot instead of 2 actions.

---

## 3. Real-World Debugging Lessons & Server Pitfalls

### Pitfall 1: Target Entity Runtime ID for `InteractActionOpenInventory`
* **The Bug:**
  When implementing `openInventory`, setting `TargetEntityRuntimeID: botEntityID` was interpreted by BDS as right-clicking/interacting with oneself. BDS anti-cheat flagged this as an illegal entity interaction, sending `PacketViolationWarning (Severity 2)` and terminating the connection (`nethernet: SCTP transport closed`).
* **The Protocol Fix:**
  Per Bedrock protocol specifications (gophertunnel `packet.Interact`):
  > *"TargetEntityRuntimeID is the runtime ID of the entity that the player interacted with. This is empty (0) for the InteractActionOpenInventory action type."*
  Setting `TargetEntityRuntimeID: 0` properly notifies BDS that the player has opened their client-side inventory screen without triggering anti-cheat checks.

---

### Pitfall 2: `ContainerClose` Window and Container Type
* When closing the player's personal inventory screen, the packet must specify:
  * `WindowID: 0` (`protocol.WindowIDInventory`)
  * `ContainerType: byte(protocol.ContainerTypeInventory)` (value `255` / `-1` as uint8)
  * `ServerSide: false`

---

### Pitfall 3: Action Count Range Assertion `[1, 64]`
In Bedrock's C++ binary packet deserializer (`ItemStackRequestPacket::read` / `readNoHeader`), the item `Count` field in `DropStackRequestAction`, `TakeStackRequestAction`, and `PlaceStackRequestAction` has an explicit stream-level bounds check:
* **Strict Range:** The count value **must be in the closed range $[1, 64]$**.
* **What Happens if Violated:**
  If an action specifies `Count: 0` or `Count > 64` (e.g. `Count: 66` after two successive bundle item extractions merged into one oversized slot), BDS's `ReadOnlyBinaryStream` throws an unrecoverable deserialization exception:
  ```
  PacketViolationWarning Type=0 Severity=2 PacketID=147 Context="the input is outside of the accepted range [1, 64]\nreadNoHeader failed! packetId: 147"
  ```
  The server terminates the SCTP network channel.
* **Solution**:
  1. `dropSlots` chunks every drop action into slices of at most 64 items (`rem = 66 -> [64, 2]`), ensuring every transmitted action strictly complies with $[1, 64]$.

---

### Pitfall 4: Drop Velocity & Eye Direction vs. Random Scattering (`SourceFlags`)
* In Bedrock Dedicated Server (`InventorySource.h` / `Player.h`):
  ```cpp
  enum class InventorySourceFlags : uint {
      NoFlag                 = 0,
      WorldInteractionRandom = 1,
  };
  virtual bool drop(::ItemStack const& item, bool const randomly);
  ```
  * When `SourceFlags` in `InventoryActionSourceWorld` is set to `1` (`WorldInteractionRandom`), BDS calculates a random spherical scatter vector.
  * When `SourceFlags` is set to `0` (`NoFlag`), BDS projects the item velocity vector along the player's exact `yaw` and `pitch` (eye look vector from `PlayerAuthInput`).
* **Implementation:**
  All drop commands default to eye direction (`random: false`). An optional parameter `random: true` enables random scatter.

---

### Pitfall 5: Bundles Dropped Intact
In Minecraft Bedrock, bundles inside personal inventory slots are single items (`protocol.ItemInstance`) containing bundle ID and item arrays in their NBT metadata.
* Dropping a slot containing a bundle drops the bundle item itself into the world intact, preserving all items inside.

---

### Pitfall 6: `ContainerInventory` Zero-Indexed Slot Offset (`slot - 9`)
* **The BDS Protocol Architecture:**
  * `ContainerHotBar` (ID 32 / `hotbar_items`): 9 slots, indexed `0` through `8`. Maps directly to player inventory slots `0` through `8`.
  * `ContainerInventory` (ID 33 / `inventory_items`): 27 slots, strictly indexed `0` through `26`. Maps to player inventory slots `9` through `35`.
* **The Bug:**
  Previously, `playerSlotContainer` returned `protocol.ContainerInventory, byte(slot)` without subtracting 9.
  * When attempting to drop slot 9, it sent `slot: 9` in `ContainerInventory`.
  * BDS interpreted `slot: 9` in `ContainerInventory` as the 10th slot of the backpack ($9 + 9 = \text{player slot } 18$). Because player slot 18 was empty, BDS rejected the transaction with `status=49 (failed to validate source slot)`.
  * This also caused batch requests in `dropall3` to fail atomically whenever items were present in slots 9–35, forcing fallback to slot-by-slot mode where slots 0–8 succeeded but 9+ failed.
* **The Fix:**
  * `playerSlotContainer` maps slots $9 \le \text{slot} \le 35$ to `protocol.ContainerInventory, byte(slot - 9)`.
  * `conn.go` translates incoming `ItemStackResponse` for `ContainerInventory` with `slotIdx = int(sInfo.Slot) + 9`.
  * With all slots valid, `dropall3` executes in a **single atomic batch packet**, dropping all items in one tick.

---

### Pitfall 7: `ContainerCursor` Destination Slot Validation (`status=50`)
* **What Happened:**
  In `drop2` / `dropall2`, `TakeStackRequestAction` to `protocol.ContainerCursor` (ID 63 / 59) returned:
  ```
  inventory request=-861 rejected status=50 (failed to validate destination slot)
  ```
* **The Protocol Cause:**
  BDS only instantiates `PlayerCursorContainer` when an external container (chest, dropper, barrel, etc.) is open via a server-authoritative `ContainerManagerModel`. When standing in the world, BDS does not register a virtual cursor container.
* **The Fix:**
  If `take` to `ContainerCursor` is rejected with `status=50`, Bebot automatically falls back to executing `DropStackRequestAction` directly from the source slot.

---

### Pitfall 8: BDS ScreenHandlerHUD Container Registry and Resilient Slot Pipeline
* **What Happened:**
  When executing `dropall2` or `dropall3`, hotbar slots 0–8 succeeded, but backpack slots 9–35 failed:
  ```
  bebot: inventory request=-897 rejected status=49
  bebot: drop2 slot 9 failed: status=49 (failed to validate source slot)
  ```
* **The Root Cause in BDS C++:**
  * In Bedrock Dedicated Server, `ItemStackRequestActionHandler::_validateRequestSlot` queries `mContainerManagerModel->getContainer(containerID)`.
  * In the open world, BDS uses `ScreenHandlerHUD`, which strictly registers `ContainerHotBar` (Container ID `28`, slots `0`–`8`).
  * When no external block container (such as a chest or dropper) is open, BDS does not register `ContainerInventory` (Container ID `29`) or `ContainerCombinedHotBarAndInventory` (Container ID `12`) in its HUD screen model. Any request targeting backpack slots 9–35 outside an external container triggers `status=49 (failed to validate source slot)`.
* **The Architecture & Resolution:**
  1. **Canonical Container Mapping:**
     * Hotbar slots (0–8) map to `protocol.ContainerHotBar` (slots `0`–`8`).
     * Backpack slots (9–35) map to `protocol.ContainerInventory` (slots `0`–`26`, offset `slot - 9`), matching Bedrock network specifications and incoming `ItemStackResponse` decoding.
  2. **Primary Protocol Path (`packet.ItemStackRequest`):**
     * Always attempts native `ItemStackRequest` actions first (`Take` + `Drop` for Method 2, `DropStackRequestAction` for Method 3).
     * If destination validation fails on cursor (`status=50`), falls back to direct slot drop via `ItemStackRequest`.
  3. **Seamless Transaction Fallback (`packet.InventoryTransaction`):**
     * If BDS rejects storage slots with `status=49` (due to HUD container validator restrictions), the bot immediately completes the drop for that slot via `b.dropSlotTx` (`packet.InventoryTransaction`).
     * Balanced transactions operate directly on `WindowIDInventory` (0) across all 36 slots (0–35) without requiring server screen container bindings.
     * **Result:** 100% of non-empty inventory slots (0–35) are dropped with zero items left stuck in inventory, while preserving the new protocol wherever the server accepts it.

---

### Pitfall 9: `WindowID=1` and `openInventory` / `closeInventory` Isolation
* **What Happened:**
  Logs previously showed:
  ```
  bebot: ContainerClose WindowID=1 ServerSide=false
  ```
* **The Root Cause:**
  In `conn.go`, incoming `InventorySlot` or `InventoryContent` packets with `WindowID < 119` set `b.containerWindowID = byte(p.WindowID)`. When personal inventory was opened, setting `b.containerActive = true` caused subsequent packets with `WindowID=1` (e.g. from crafting or external container sync) to overwrite `containerWindowID` with `1`. Closing personal inventory then emitted `ContainerClose WindowID=1` instead of `WindowID=0`.
* **The Fix:**
  Personal inventory opening/closing explicitly sets and matches `WindowID=0` (`protocol.WindowIDInventory`), leaving `b.containerActive` and `containerWindowID` untouched unless an actual world container block is opened.

---

## 4. Bebot Implementation & Command Reference

Implemented in [src/bot/inventory.go](file:///d:/code/goland/bebot/src/bot/inventory.go) and [src/bot/chat.go](file:///d:/code/goland/bebot/src/bot/chat.go):

| In-Game Chat Command | Method Used | Execution Characteristics |
| :--- | :--- | :--- |
| **`bebot dropall [all\|each] [random]`** | **Method 1: First-Person In-World** | Default (`all` or omitted): drops all slots simultaneously in a single network flush. With `each`: drops sequentially 1-by-1 with 50ms interval. Eye direction (default) or random scatter (`random`). |
| **`bebot drop <slot> [count] [random]`** | **Method 1: First-Person In-World** | Drops `count` items (or all) from inventory slot `slot` directly in eye direction (default `random=false`). |
| **`bebot dropall2 [all\|each] [random]`** | **Method 2: Cursor Drag & Drop** | Default (`all` or omitted): batches all slots into a single atomic `ItemStackRequest` (or rapid fallback). With `each`: drops sequentially 1-by-1 with 50ms interval. |
| **`bebot drop2 <slot> [count] [random]`** | **Method 2: Cursor Drag & Drop** | Takes `count` items from `slot` to cursor and drops to world via `ItemStackRequest`. |
| **`bebot dropall3 [all\|each] [random]`** | **Method 3: GUI Direct Slot Drop** | Default (`all` or omitted): batches all slots into a single atomic `ItemStackRequest` (or rapid fallback). With `each`: drops sequentially 1-by-1 with 50ms interval. |
| **`bebot drop3 <slot> [count] [random]`** | **Method 3: GUI Direct Slot Drop** | Direct slot drop via `DropStackRequestAction` natively via `ItemStackRequest`. |
| **`bebot open <x y z>`** | **Container Management** | Opens external block container (chest, dropper, barrel) and keeps it open on server until `close`. Enables pure `ItemStackRequest` across all slots 0–35. |
| **`bebot close`** | **Container Management** | Closes currently open container window. |
