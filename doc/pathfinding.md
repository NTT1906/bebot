# 3D Pathfinding Architecture & Implementation Plan (Bebot)

This document provides the complete architectural blueprint and engineering roadmap for autonomous 3D pathfinding in **Bebot (Minecraft Bedrock Edition)**. It synthesizes lessons from state-of-the-art Minecraft navigation engines (**Baritone**, **Mineflayer Pathfinder**, and **Acacia**) tailored specifically to Bedrock's voxel geometry, protocol timings, and client-authoritative physics.

---

## 1. Executive Summary & Problem Formulation

### 1.1. Why Direct Movement Fails
The existing `move <x y z>` command operates via **direct straight-line vector steering**:
- It steps directly along the ray $\vec{V} = \text{Target} - \text{Pos}$.
- It has no awareness of obstacles, walls, cliffs, or hazards.
- If a 1-block elevation, wall, or trench exists between the bot and target, the bot walks straight into the wall and gets stuck forever, or steps into lava or falls off high cliffs.

### 1.2. Pathfinding Objectives
1. **Autonomous Voxel Navigation**: Safely traverse complex 3D terrain (climbing hills, descending drops, navigating corridors, exploring caves) without user intervention.
2. **Deterministic Obstacle Avoidance**: Steer around walls, trees, fences, and corners with strict bounding-box collision clearance.
3. **Hazard Avoidance**: Treat lava, fire, cactus, sweet berry bushes, and lethal drops ($> 3$ blocks) as impassable or heavily penalized.
4. **Target Tracking**: Support both static coordinate goals (`goto <x y z>`) and dynamic moving entities (`goto <player>`).
5. **Separation of Concerns**: Keep low-level vector movement (`move`) intact for precise scripting/testing, introducing dedicated high-level commands (`goto`, `pathfind`, `stoppath`).

---

## 2. Minecraft Bedrock Physics & Spatial Constraints

In Minecraft Bedrock Edition, the player entity is modeled as an axis-aligned bounding box (AABB):

$$\text{Width} = 0.6\text{ blocks}, \quad \text{Depth} = 0.6\text{ blocks}, \quad \text{Height} = 1.8\text{ blocks}$$

```
                ┌───────────────────────────┐  ▲
                │      Head Block (Y+1)     │  │ 1.8 blocks clearance needed
                │       (Must be Air)       │  │ (2 non-solid vertical voxels)
                ├───────────────────────────┤  │
                │      Feet Block (Y)       │  │
                │       (Must be Air)       │  ▼
                ├───────────────────────────┤
                │      Floor Block (Y-1)    │  ◄── Must be solid, walkable surface
                │      (Solid / Ground)     │
                └───────────────────────────┘
```

### Invariants for Walkable Standing Positions:
A block coordinate $P(x, y, z)$ is a **valid standing node** if and only if:
1. $P(x, y, z)$ is **non-solid** (Air, Water, or pass-through).
2. $P(x, y+1, z)$ is **non-solid** (Air, Water, or pass-through) to accommodate the $1.8\text{m}$ player height.
3. $P(x, y-1, z)$ is **solid** (provides ground reaction force and prevents falling).

---

## 3. The 3D Voxel Move Generator

Rather than constructing an explicit graph of the entire world, the A* planner uses an **implicit graph**: from any given node $P$, it computes all physically legal transitions to adjacent nodes.

```
                               ┌─────────────┐
                               │ Current Node│
                               │  P(x, y, z) │
                               └──────┬──────┘
         ┌───────────────────┬────────┴────────────┬───────────────────┐
         ▼                   ▼                     ▼                   ▼
   ┌───────────┐       ┌───────────┐         ┌───────────┐       ┌───────────┐
   │ 4 Cardinal│       │4 Diagonals│         │4 Step-Ups │       │ 12 Drops  │
   │   Walks   │       │(Corner Clr│         │  (+1 Y)   │       │ (-1..-3 Y)│
   └───────────┘       └───────────┘         └───────────┘       └───────────┘
```

### 3.1. Cardinal Walk ($\Delta X, \Delta Z \in \{\pm 1, 0\}, \Delta Y = 0$)
- **Condition**:
  - Target foot $T(x', y, z')$ and head $T(x', y+1, z')$ are air.
  - Target floor $T(x', y-1, z')$ is solid.
- **Cost**: $1.0\text{ block-time}$ ($\approx 3.56\text{ ticks}$).

### 3.2. Diagonal Walk ($\Delta X = \pm 1, \Delta Z = \pm 1, \Delta Y = 0$)
- In a diagonal move from $(x, y, z)$ to $(x+1, y, z+1)$, a naive algorithm only checks the destination.
- **The Corner-Cutting Trap**: Because the player is $0.6$ blocks wide, cutting diagonally past a solid corner block causes the player's bounding box to collide with the wall, halting progress!
- **Strict Corner Clearance Rule**:
  Both intermediate cardinal columns must be walkable:
  $$\text{IsTraversable}(x+1, y, z) \land \text{IsTraversable}(x, y, z+1)$$
- **Cost**: $\sqrt{2} \approx 1.414\text{ block-time}$.

### 3.3. Step Up (+1 Y Jump)
- To ascend 1 block: $\Delta X, \Delta Z \in \{\pm 1, 0\}, \Delta Y = +1$.
- **Clearance Requirements**:
  1. Target floor $T(x', y, z')$ is solid.
  2. Target feet $T(x', y+1, z')$ and target head $T(x', y+2, z')$ are air.
  3. **Headroom Clearance**: The block directly above the bot's current head $P(x, y+2, z)$ must be air so the bot doesn't bump its head during the jump!
- **Cost**: $2.0$ (jumping costs $\approx 6\text{ ticks}$ / $0.3\text{s}$).

### 3.4. Drop Down (-1 to -3 Y Steps)
- Stepping off an edge to descend: $\Delta X, \Delta Z \in \{\pm 1, 0\}, \Delta Y \in \{-1, -2, -3\}$.
- **Clearance Requirements**:
  1. Intermediate column $T(x', y, z')$ down to $T(x', y-\Delta Y, z')$ is air.
  2. Landing floor $T(x', y-\Delta Y-1, z')$ is solid.
  3. Drops $> 3$ blocks are strictly prohibited to prevent fall damage.
- **Cost**: $1.2$ for $1\text{ drop}$, $1.8$ for $2\text{ drop}$, $2.5$ for $3\text{ drop}$.

### 3.5. Parkour Gap Leaps (Phase 4)
- Sprinting jumps over 1 to 3 blocks of open air:
  - Cardinal leaps over 1-block, 2-block, and 3-block gaps.
  - Requires continuous clear corridor and flat runway for sprint velocity.

---

## 4. Voxel World Interface (`WorldView`)

Pathfinding requires querying the state of blocks around the bot. Rather than tightly coupling the A* algorithm to network packets or raw chunk slices, we introduce an abstract **`WorldView`** interface:

```go
type BlockType uint8

const (
    BlockAir BlockType = iota
    BlockSolid
    BlockLiquidWater
    BlockLiquidLava
    BlockHazard // Cactus, sweet berry, magma
)

type WorldView interface {
    GetBlockType(pos protocol.BlockPos) BlockType
}
```

### Backed by Bebot Subsystems:
1. **Active Chunk Cache (`b.chunkCache`)**:
   - Bitwise $O(1)$ block lookup: `(x & 15) | ((z & 15) << 4) | ((y & 15) << 8)` within the 3x3x3 sub-chunk window.
2. **Runtime Block Palette Probing**:
   - Resolves runtime IDs against `blockRegistry` to identify water, lava, leaves, and solid stone.
3. **Unloaded Chunk Handling**:
   - Blocks outside the loaded cache return `BlockSolid` or `BlockUnknown` to prevent the bot from stepping into unverified voids.

---

## 5. A* Algorithm Architecture & Library Analysis

### 5.1. Evaluation of Existing Go Libraries

#### 1. `beefsack/go-astar`
```go
type Pather interface {
    PathNeighbors() []Pather
    PathNeighborCost(to Pather) float64
    PathEstimatedCost(to Pather) float64
}
```
- **Why It Fails for Minecraft**:
  Every expanded node must instantiate and return `[]Pather`. In 3D Minecraft searches (2,000–10,000 nodes), boxing struct pointers into `[]Pather` interfaces creates **millions of heap allocations and intense GC nursery churn**.

#### 2. `fzipp/astar`
```go
func FindPath[Node comparable](graph Graph[Node], start, goal Node, cost, heuristic CostFunc[Node]) []Node
```
- Uses Go generics (`Node comparable`), avoiding interface boxing.
- **Why It Still Falls Short**:
  1. `Neighbours(node Node) []Node` still allocates neighbor slices on every expansion.
  2. **No Best-Effort Fallback**: If a goal is blocked or outside the search budget, generic A* returns `nil, false`. In a game, if a player is atop a wall, the bot needs a path to the **closest reachable point**, not an outright failure.

### 5.2. The Recommended Tailored Engine: `pathfind.Planner`

We implement a dedicated, allocation-efficient Minecraft A* engine using `container/heap`:

```
┌────────────────────────────────────────────────────────────────────────┐
│                        A* SEARCH ARCHITECTURE                          │
├────────────────────────────────────────────────────────────────────────┤
│ 1. Open Set:   Binary Min-Heap sorted by F-score (G + H)               │
│ 2. Closed Set: Fast 64-bit coordinate hash map (or flat spatial table) │
│ 3. Neighbor Stack Array: Fixed [16]protocol.BlockPos (Zero Allocs)     │
│ 4. Node Budget: Max 5,000 expansions or 50ms execution ceiling         │
│ 5. Best-Effort Recovery: Tracks node with lowest H-score if budget hits│
└────────────────────────────────────────────────────────────────────────┘
```

#### Node Structure
```go
type PathNode struct {
    Pos   protocol.BlockPos
    G     float64 // Cost from start
    H     float64 // Estimated cost to goal
    F     float64 // G + H
    Index int     // Heap index for container/heap
    Parent *PathNode
}
```

---

## 6. Heuristic Design ($H$-Score) & Cost Tuning

### 6.1. Admissible 3D Octile Distance
Because the bot can walk diagonally and vertically, standard Manhattan distance overestimates diagonal travel, while Euclidean distance is slightly slower to compute.
We use **3D Octile / Diagonal distance**:

Let $dx = |x_1 - x_2|$, $dy = |y_1 - y_2|$, $dz = |z_1 - z_2|$.
Let $h_{\min} = \min(dx, dz)$, $h_{\max} = \max(dx, dz)$.

$$H(P) = (\sqrt{2} - 1) \cdot h_{\min} + h_{\max} + 1.5 \cdot dy$$

This heuristic is strictly **admissible** (never overestimates the true travel cost) and directs the search beam directly toward the goal.

### 6.2. Danger & Penalty Multipliers
- **Adjacent to Lava**: $+100.0$ cost penalty.
- **Walking through Water**: $+3.0$ cost penalty per block (slowdown).
- **Near High Cliff**: $+10.0$ cost penalty if stepping along a 1-wide ledge with drops $> 4$.

---

## 7. Path Follower Controller (`follower.go`)

A planned path `[]protocol.BlockPos` is only a static list of waypoints. Executing it in the game requires a continuous **Path Follower** running inside `moveTick` (20 TPS / 50ms):

```
                   ┌─────────────────────────────┐
                   │   moveTick (Every 50ms)     │
                   └──────────────┬──────────────┘
                                  │
                                  ▼
                   ┌─────────────────────────────┐
                   │ Has Active Path Waypoints?  │
                   └──────────────┬──────────────┘
                                  │ Yes
                                  ▼
                   ┌─────────────────────────────┐
                   │ Distance to Current WP < 0.3│
                   └──────────────┬──────────────┘
                          Yes ┌───┴───┐ No
                              ▼       ▼
                     Advance to       Steer Toward Current WP:
                     Next WP          - Calculate Yaw to WP
                                      - Set Forward Move Input
                                      - If WP is StepUp (+1 Y):
                                        Trigger Jump at Edge
```

### Key Follower Mechanics:
1. **Waypoint Transition Radius**: When the bot's horizontal distance to the current waypoint is $< 0.35$ blocks, the waypoint is considered reached, and the follower advances to the next node.
2. **Jump Timing at Block Boundaries**: For a $+1\text{ Y}$ step-up, the follower walks forward until reaching the boundary ($0.15$ blocks from the block edge), then raises the jump flag (`InputFlagStartJumping` / `WantUp`).
3. **Anti-Stuck Replanning**: If the bot stays within the same $0.2\text{m}$ radius for $> 1.0\text{s}$ (20 ticks) while trying to reach a waypoint, the follower detects a stall and triggers an **instant replan** from the bot's actual current location.

---

## 8. Command Interface & User Experience

Pathfinding commands are completely separated from existing direct vector movement (`move`):

### 8.1. Command Specification

| Command | Arguments | Description |
|---|---|---|
| **`goto <x y z>`** | `x y z` (int/float) | Plans a 3D path to target coordinates and executes it. |
| **`goto <player>`** | `player_name` | Plans a path to within 2 blocks of the designated player. |
| **`pathfind <x y z>`** | `x y z` | Alias for `goto`. |
| **`stoppath`** | *(none)* | Immediately aborts the active path and halts movement. |
| **`cancelpath` / `path stop`** | *(none)* | Aliases for `stoppath`. |

### 8.2. Interactive Chat Feedback
```
> goto 120 64 -250
[Bebot] Planning path to <120, 64, -250>...
[Bebot] Path found: 38 nodes (length: 44.2m, est: 10.5s). Following...
[Bebot] Reached waypoint 20/38.
[Bebot] Destination reached!
```
If unreachable:
```
> goto 120 90 -250
[Bebot] Planning path to <120, 90, -250>...
[Bebot] Exact target unreachable (blocked/too high). Following best-effort path to <118, 72, -248> (dist: 18.2m).
```

---

## 9. Phased Implementation Roadmap

```
┌────────────────────────────────────────────────────────────────────────┐
│                        IMPLEMENTATION PHASES                           │
├─────────┬──────────────────────────────────────────────────────────────┤
│ Phase 1 │ Voxel WorldView Abstraction & Move Generator  [COMPLETED]    │
│         │ - Create `src/pathfind/` package                             │
│         │ - Implement `WorldView` reading from `chunkCache`            │
│         │ - Implement 16 neighbor moves with diagonal corner clearance │
│         │ - Unit tests: 100% test coverage of clearance & drop limits  │
├─────────┼──────────────────────────────────────────────────────────────┤
│ Phase 2 │ Zero-Allocation A* Solver                     [COMPLETED]    │
│         │ - Implement binary min-heap with container/heap              │
│         │ - Admissible 3D Octile heuristic                             │
│         │ - Search budget (max nodes / timeout) + best-effort fallback │
│         │ - Unit tests: maze navigation, elevation climb, drop routes  │
├─────────┼──────────────────────────────────────────────────────────────┤
│ Phase 3 │ Path Follower Controller                      [COMPLETED]    │
│         │ - Integrate into `b.moveTick` (20 TPS)                       │
│         │ - Waypoint steering, look-ahead vector, jump-at-edge trigger │
│         │ - Anti-stuck detection and dynamic replanning                │
├─────────┼──────────────────────────────────────────────────────────────┤
│ Phase 4 │ Command Integration & In-Game UX              [COMPLETED]    │
│         │ - Add `goto <x y z>`, `goto <player>`, `stoppath` commands   │
│         │ - Chat / Console status reporting                            │
│         │ - Documentation updates in README.md                         │
├─────────┼──────────────────────────────────────────────────────────────┤
│ Phase 5 │ Advanced Movement & Parkour                   [UPCOMING]     │
│         │ - Gap leaps (sprint jumping across 1-2 block voids)          │
│         │ - Sneak edge traversal on narrow bridges                     │
│         │ - Door, gate, and ladder navigation                          │
└─────────┴──────────────────────────────────────────────────────────────┘
```
