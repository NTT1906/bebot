# Non-Destructive Block Runtime ID Probing in Minecraft Bedrock

## 1. Overview & Motivation

In Minecraft Bedrock Edition, determining the runtime ID of a block at a given coordinate $(x, y, z)$ is critical for navigation, mining calibration, and placement verification. 

Previously, the primary mechanism was destructive:
* **`bebot mine learn <x y z>`:** Dispatched mining packets to physically break the block and observed the resulting air runtime ID sent in the server's `UpdateBlock` packet.

While effective, breaking blocks is undesirable when:
* Inspecting blocks in survival without tools or permissions.
* Checking the integrity of a build or floor without leaving holes.
* Verifying block states (e.g. crop growth, stair orientation, water level) non-invasively.

Below are three non-destructive probing methods supported by the Bedrock network protocol.

---

## 2. Method 1: The "Server Rejection / Resync" Probe (Universal)

### Mechanism
In Bedrock client-authoritative prediction, when a player attempts to place a block into a coordinate already occupied by a solid block:
1. The client sends a placement interaction (via `PlayerAuthInput.ItemInteractionData` or `InventoryTransaction`).
2. The server performs server-side collision and rule checks. Detecting that $(x, y, z)$ is already occupied, the server rejects the placement.
3. To prevent client ghost blocks and desynchronization, the server immediately sends a corrective **`UpdateBlock`** (or `UpdateBlockSynced`) packet:
   $$\text{Position}: (x, y, z),\quad \text{NewBlockRuntimeID}: \mathbf{\text{Exact\ current\ runtime\ ID}}$$

```
[Bot: Predict Placement into (x, y, z)]
                 │
                 ▼
[Server: Collision Check -> Block Already Solid!]
                 │
                 ▼
[Server Sends Corrective UpdateBlock Packet]
                 │
                 ├──► Position: (x, y, z)
                 └──► NewBlockRuntimeID: <Current Solid Block ID>
                 │
                 ▼
[Bot Records ID into b.blockRuntimeIDs[pos]]
(Block took 0 damage and was never broken)
```

### Advantages
* **100% Non-Destructive:** The block takes zero damage and is never modified.
* **Works in Both Game Modes:** Operates identically in Survival and Creative.
* **Fast:** Resolves within 1 round-trip time ($\approx 50\text{ms}$).

### Edge Case Consideration
* If the target coordinate is actually air, placing into it will place a block. To probe safely, the probe interaction can be directed at the solid face itself rather than an adjacent air coordinate, or rolled back if needed.

---

## 3. Method 2: Middle-Click / `BlockPickRequest` (Creative Mode)

### Mechanism
Bedrock protocol includes a dedicated packet used when players middle-click (Pick Block) in the world:

```go
type BlockPickRequest struct {
    Position    protocol.BlockPos // Target coordinate
    AddBlockNBT bool              // Preserve tile entity data
    HotBarSlot  byte              // Hotbar slot to place the picked item
}
```

When received by Bedrock Dedicated Server (BDS):
1. **Creative Mode (GameMode 1):** The server identifies the block at `Position` and sends an **`InventorySlot`** packet to the designated `HotBarSlot`. Inside that item instance:
   ```go
   item.BlockRuntimeID // Contains the exact runtime ID of the target block
   ```
2. **Survival Mode (GameMode 0):** The server checks if the player already carries that block in their inventory. If present, it selects the item; if not present, the server does nothing.

### Advantages
* Built specifically into the Bedrock protocol for non-destructive block queries.
* Zero interaction or placement side-effects.

---

## 4. Method 3: Passive SubChunk Palette Decoding (Zero Network Traffic)

### Mechanism
When players load into or move across the world, the server streams chunk data via `LevelChunkPacket` and `SubChunkPacket`.
* Each $16 \times 16 \times 16$ sub-chunk payload contains an internal 3D block palette mapping local coordinates $(0..15, 0..15, 0..15)$ to runtime IDs.
* If the bot decodes the sub-chunk for the target coordinate:
  * The runtime ID is read directly from local memory.
  * Zero packets are transmitted over the network.
  * Instantaneous ($< 1\text{µs}$).

### Architecture Trade-Off
* Decoding and storing all loaded sub-chunks consumes significant RAM.
* **On-Demand Solution:** Only decode the specific sub-chunk containing $(x, y, z)$ when requested by an inspection command, then discard or cache briefly.

---

## 5. Comparison Matrix

| Feature | Method 1: Placement Resync | Method 2: BlockPickRequest | Method 3: SubChunk Decoding | Legacy `mine learn` |
| :--- | :---: | :---: | :---: | :---: |
| **Destroys Block?** | **No** (0 damage) | **No** (0 damage) | **No** (Passive) | **Yes** (Breaks block) |
| **Survival Mode Support** | Yes | Bag items only | Yes | Yes |
| **Creative Mode Support** | Yes | Yes | Yes | Yes |
| **Network Traffic** | 1 packet sent / 1 received | 1 packet sent / 1 received | **0 network packets** | Multiple break packets |
| **Latency** | 1 RTT ($\approx 50\text{ms}$) | 1 RTT ($\approx 50\text{ms}$) | Instant ($< 1\text{µs}$) | $100\text{ms} - 300\text{ms}$ |
| **Implementation Complexity** | Low | Low | Medium | High |

---

## 6. Proposed Command Design

A proposed non-destructive chat command could be added to Bebot:

```text
bebot inspect <x y z>
bebot probe <x y z>
```

### Behavior:
1. **Local Check:** If `b.blockRuntimeIDs[pos]` is already known, return immediately:
   ```text
   Block at (255, 64, -220): Runtime ID 2150698529 (minecraft:stone).
   ```
2. **Creative Mode Fallback:** Send `packet.BlockPickRequest` and await `InventorySlot`.
3. **Universal Fallback:** Dispatch predictive collision probe and capture the corrective `UpdateBlock` response within a $250\text{ms}$ timeout.
