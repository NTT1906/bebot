# Bedrock Block Placement: Edge Cases & Design Solutions

This document details the architectural solutions for three common edge cases encountered when placing blocks in Minecraft Bedrock Edition via autonomous bots or network protocols.

---

## 1. Interactive Supporting Blocks (Chests, Furnaces, Doors, Levers)

### Problem
When the bot clicks against a supporting block that is interactive (e.g. Chest, Crafting Table, Furnace, Anvil, Door, Trapdoor, Lever, Hopper), the server defaults to executing the block interaction (opening the container or toggling the mechanism) instead of placing the held block.

### Analysis & Solution
In vanilla Minecraft, **Sneaking (Shift-Clicking)** is the canonical game mechanic designed specifically to suppress interactive block GUI/toggling and prioritize item placement on the targeted face.

#### Implementation Strategies

#### Strategy A: Ephemeral Sneak Input Flag in Auth Input (Recommended)
Rather than making the bot physically enter a crouch state in the game world over multiple ticks:
* On the exact tick the block placement transaction is dispatched (`PlayerAuthInput` with `ItemInteractionData`), set `packet.InputFlagSneaking` inside `auth.InputData`.
* BDS evaluates the placement action within a sneak context, bypassing the container/block interaction and placing the block on the face.
* The bot never needs to visibly crouch or slow down.

#### Strategy B: Prefer Non-Interactive Supporting Neighbors
The bot scans up to 6 adjacent neighbor blocks (bottom, north, south, west, east, top):
* Filter candidates to prioritize neutral, non-interactive solid blocks (e.g. stone, dirt, wood planks) over containers or interactables.
* Fall back to Strategy A if all neighboring blocks are interactive or only the bottom block is available.

---

## 2. Replaceable Blocks (Short Grass, Flowers, Ferns, Snow Layer, Water)

### Problem
When placing a block at coordinate `targetPos` where a non-solid or replaceable block already exists (e.g., short grass, tall flowers, dead bush, snow layer 1, or water):
* Treating `targetPos` as a solid supporting block causes the bot to place against its face, resulting in the block floating 1 unit higher at `targetPos + offset` (e.g., placing dirt *on top* of the grass instead of replacing it).
* Rejecting `targetPos` as "occupied" prevents building on natural terrain.

### Analysis & Solution
Bedrock blocks have a `replaceable` attribute in their block definitions. In vanilla gameplay, right-clicking a replaceable block targets the block itself, overwriting or waterlogging it directly at that coordinate.

#### Implementation Architecture

1. **Replaceable Detection:**
   * Maintain a list of known vanilla replaceable block IDs/names:
     * `minecraft:short_grass`, `minecraft:grass`, `minecraft:fern`
     * `minecraft:tall_grass`, `minecraft:large_fern`
     * `minecraft:poppy`, `minecraft:dandelion`, and other small flowers
     * `minecraft:deadbush`
     * `minecraft:snow_layer` (at layer height 1)
     * `minecraft:water`, `minecraft:flowing_water`
2. **Direct Replacement Targeting:**
   * If `targetPos` contains pure air $\rightarrow$ find a solid neighbor (e.g. `targetPos.Y - 1`) and click its face pointing to `targetPos`.
   * If `targetPos` contains a **replaceable block** $\rightarrow$ target `targetPos` directly:
     * `clickedBlockPos = targetPos`
     * `clickedFace = 1` (top)
     * The server automatically destroys/replaces the grass/snow or waterlogs it, placing the new block directly inside `targetPos`.

---

## 3. Detecting Placement Failures (Server Lag, Rejections, Invisible Entities)

### Problem
When a block placement fails on the server (due to an entity standing inside the coordinate, an anti-cheat / reach violation, or server lag), Bedrock Dedicated Server (BDS) **never sends an explicit error or failure packet**. The client predicts placement, but the block fails to exist in the world. How can the bot detect and report that the placement failed?

### Analysis & Feedback Considerations

#### Point 1: `UpdateBlock` Resync Check & Replaceable Blocks
* **The Question:** *"What if it failed, but the original block was not air, but water, grass, or flowers? Then it would not be air, right?"*
* **The Follow-up:** *"How does the bot know what `placedBlockRuntimeID` is in the first place?"*
* **Connecting Item Network IDs to Block Runtime IDs:**
  An item held in hand has an item network ID (e.g. `ItemType.NetworkID = 35` for dirt), whereas a block in the world has a block runtime ID (e.g. `RuntimeID = 1482`). The bot resolves and verifies this using three complementary mechanisms:

  1. **Direct Protocol Field (`ItemStack.BlockRuntimeID`):**
     * In Bedrock's network protocol, `protocol.ItemStack` includes a dedicated `BlockRuntimeID int32` field.
     * When inventory packets (`InventoryContent`, `MobEquipment`, `InventorySlot`) are transmitted by the server, this field is pre-populated for placeable block items with that block's default runtime state ID.
  2. **Deterministic FNV-1a Block State Hashing (`UseBlockNetworkIDHashes`):**
     * On Bedrock servers with `c.GameData().UseBlockNetworkIDHashes = true`, block runtime IDs are derived deterministically by hashing the Little-Endian NBT compound tag (`TAG_Compound{"name": "minecraft:...", "states": {}}`) using 32-bit FNV-1a. For example, `hashNBT("minecraft:air")` produces `3690217760` (`0xDBF44120`).
     * On standard vanilla BDS servers where `UseBlockNetworkIDHashes = false`, the runtime ID is the 0-based index in `canonical_block_states.nbt` (`minecraft:air` = index `0`).
  3. **Original State Contrast (The Practical Shortcut):**
     * Regardless of whether the target coordinate was pure air, water, or tall grass, the bot already recorded the coordinate's pre-placement state in `b.blockRuntimeIDs[targetPos]`.
     * **Success Condition:** Server sends `UpdateBlock` with a new, solid runtime ID where $\text{newRuntimeID} \neq \text{oldRuntimeID}$ and $\text{newRuntimeID}$ is not air.
     * **Failure Condition:** Server rolls back the placement by sending `UpdateBlock` re-asserting $\text{newRuntimeID} == \text{oldRuntimeID}$ (the original water, grass, or air).

* **Rule:** If $\text{newRuntimeID} == \text{oldRuntimeID}$, the placement was rejected by the server and rolled back. If $\text{newRuntimeID} \neq \text{oldRuntimeID}$ and is non-air, the block was successfully placed.

#### Point 2: Inventory Count Reconciliation (Survival vs. Creative)
* **The Question:** *"Inventory decrement works in Survival, but how does it work in Creative mode?"*
* **The Solution:** GameMode-Dependent Verification:
  * **Survival / Adventure (GameMode 0/2):** The server deducts 1 item from the hotbar. If an `InventorySlot` packet arrives restoring the stack count, or if no deduction occurs, it indicates failure.
  * **Creative Mode (GameMode 1):** In Creative, item stacks are never decremented by the server (`count` stays constant). Therefore, inventory tracking is automatically skipped in Creative mode. Creative relies strictly on:
    1. The `UpdateBlock` runtime ID confirmation (Point 1).
    2. The network acknowledgement timeout (Point 3).

#### Point 3: Network Acknowledgement Timeout (Lag, Tick Freezes & Packet Drops)
* **The Question:** *"If server lag happens, the placement never happened yet so no block update arrives. Do we wait for a timeout or what?"*
* **The Problem:** 
  In high-ping conditions or during server tick freezes (e.g. Bedrock garbage collection spikes or heavy world loading), the server sends **zero packets** within the normal 50ms tick. Without an explicit timeout:
  1. The bot could block its command worker indefinitely waiting on a response.
  2. Or worse, the bot could prematurely assume success and falsely report *"Placed!"* while the server never received or acknowledged the block.
* **The Solution: Three-Way State Machine with Dynamic Countdown Timer**
  * When the placement action is dispatched, spawn a non-blocking timeout channel (`time.NewTimer`):
    * **LAN Worlds (`-local`):** 200ms – 250ms (local network RTT is typically < 5ms).
    * **Dedicated Servers (BDS):** 350ms – 500ms (accommodates internet jitter + 1 tick processing delay).
  * The bot selects over three potential outcomes:

```
                     [Dispatched Placement Action]
                                  │
                                  ▼
                     [Wait for Server Feedback]
                      (Timer: 250ms countdown)
                                  │
         ┌────────────────────────┼────────────────────────┐
         ▼                        ▼                        ▼
     Outcome A                Outcome B                Outcome C
[UpdateBlock Arrives]    [UpdateBlock Arrives]    [Timer Fires (250ms)]
(ID == Placed Block)     (ID != Placed Block)      (No Packets Arrived)
         │                        │                        │
         ▼                        ▼                        ▼
  STATUS: SUCCESS         STATUS: REJECTED          STATUS: TIMEOUT / LAG
"Placed dirt at (x,y,z)" "Server rejected: entity   "Placement timed out: server
                          or obstruction at block"   lag or dropped packet"
```

* **Handling Delayed Confirmations (Late Arrival):**
  If the server was temporarily frozen and unfreezes 1–2 seconds later:
  * The bot's background packet handler (`handleUpdateBlock`) still processes the late `UpdateBlock` packet, updating `b.blockRuntimeIDs` without state desynchronization.
  * Optionally, on timeout, the bot can auto-retry the placement action once before surfacing a failure message to the user.

### Implementation Architecture Summary

```
[Place Command Dispatched]
         │
         ├──► Survival: Monitor held stack count (did count decrement by 1?)
         │
         ├──► All Modes: Listen on blockRuntimeIDUpdated for targetPos
         │       ├──► newID == placedBlockID ──► SUCCESS: "Placed block at (x, y, z)"
         │       └──► newID != placedBlockID ──► FAILURE: "Server rejected placement (entity or obstruction)"
         │
         └──► Timer (250ms): No packet received ──► TIMEOUT: "Placement timed out (server lag / dropped packet)"
```
