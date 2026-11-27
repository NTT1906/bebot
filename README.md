# Bebot

Bebot is a Minecraft Bedrock bot written in Go using the `gophertunnel` framework.

## Requirements

- [Go](https://golang.org/dl/) 1.21 or higher

## Configuration

Before running the bot, configure the `config.json` file in the project root. If the file doesn't exist, create it based on this structure:

```json
{
  "_comment": "Edit this file to configure the bot.",
  "version": 2,
  "superuser": "YourUsername",
  "_comment_superuser": "Only this player can control the bot via chat commands.",
  "commands_via_chat": false,
  "free_mem_after_join": true,
  "log_to_file": true,
  "server": {
    "host": "your.server.ip",
    "port": 19132
  },
  "skin_file": "data/skin.json",
  "behaviour": {
    "walk_speed": 0.15,
    "render_distance": 5,
    "reconnect_base_delay_ms": 5000,
    "server_stall_threshold_ms": 250,
    "palette_load": true,
    "chunk_load": false,
    "pathfinding": {
      "auto_load": false,
      "max_nodes": 5000,
      "timeout_ms": 50,
      "allow_diagonals": false,
      "allow_drops": true,
      "weights": {
        "walk_cardinal": 1.0,
        "walk_diagonal": 1.414,
        "step_up": 1.6,
        "drop_1": 1.1,
        "drop_2": 1.6,
        "drop_3": 2.2,
        "water_penalty": 0.2,
        "vertical": 1.2
      }
    }
  },
  "mining": {
    "timeout_sec": 3.0
  }
}
```

- **`superuser`**: Your Minecraft username. Only you will be able to issue commands to the bot.
- **`server`**: Set the `host`, `port`, and expected bedrock `version` of the server.
- **`skin`**: Set a custom skin. You can define the type (`slim`, `normal`) and point to a local `.png` or folder path.
- **`behaviour`**: Tweaks for how the bot operates in-game (like how often to jump to prevent AFK kicks).
  * `palette_load` (default `true`): load the block palette on join. If `false`, run `bebot palette load` before using commands that resolve block names or pathfind.
  * `chunk_load` (default `false`): enable the local 3x3x3 sub-chunk window on join. If `false`, run `bebot chunk load` before `goto`/`inspect`.
  * `pathfinding`: A* tuning. `auto_load` (default `false`) lets `goto` load a missing palette/chunk window on demand; otherwise it reports what to load. `max_nodes` and `timeout_ms` bound the search; `allow_diagonals` (default `false`) and `allow_drops` (default `true`) control move types. `weights` overrides relative move costs (`walk_cardinal`, `walk_diagonal`, `step_up`, `drop_1`, `drop_2`, `drop_3`, `water_penalty`, `vertical`); omitted or non-positive values use the built-in defaults.

## Usage

1. Clone or download this repository.
2. Open a terminal in the project directory.
3. Configure your `config.json`.
4. Run the bot directly using Go:

```bash
go run .
```

Alternatively, build the binary:

```bash
go build -o bebot.exe .
./bebot.exe
```

When connecting to an online server for the first time, it will prompt you for Microsoft Live / Xbox authentication. Follow the OAuth link provided in the console to authorize the bot's account.

Once connected, join the server to see the bot. If you are the `superuser`, you can issue commands directly to it in-game via chat or whisper!

---

## Command-Line Arguments

Bebot supports several flags when launched from the command line:

| Flag | Default | Description |
|---|---|---|
| `-local` | `false` | Automatically discovers and connects to a local single-player Bedrock LAN world via NetherNet broadcast (UDP port 7551). Enables `-offline` mode automatically so it does not conflict with your Microsoft account. |
| `-offline` | `false` | Connects in offline/LAN mode without requiring Microsoft Live or Xbox authentication. |
| `-name <name>` | `Bebot` | Sets the bot's in-game display name (used in offline/LAN mode). |
| `-renew` | `false` | Generates a fresh player UUID in offline/LAN mode instead of reusing the saved UUID from `data/offline_uuid.json`. |
| `-offline-uuid-file <file>` | `data/offline_uuid.json` | Path used to persist the offline player UUID cache. |
| `-host <host>` | Config | Overrides target server host IP or domain (e.g. `127.0.0.1` or LAN IP `192.168.1.5`). |
| `-port <port>` | Config | Overrides target server port (e.g. `19132`). |
| `-config <file>` | `config.json` | Path to custom bot configuration file. |
| `-skin <file>` | `data/skin.json` | Path to skin configuration (`.json`) or directly to a skin image (`.png`). |
| `-log-to-file` | `true` | Enables or disables writing log output to daily rotating files in `logs/` (e.g. `-log-to-file=false`). |
| `-no-file-log` | `false` | Convenience flag to disable logging to files. |
| `-token-cache <file>` | `data/token_cache.json` | Path used to persist the Microsoft OAuth session tokens (alias: `-auth-cache`). |
| `-xsts-cache <file>` | `data/xsts_cache.json` | Path used to persist Xbox Live XSTS device and session token cache. |
| `-block-palette <file>` | Auto-detected | Path to custom block palette file (`.nbt` or `.json`). |
| `-debug` | `false` | Prints detailed packet-level handshake and network diagnostics. |

### Joining a Local Single-Player World (LAN)

To have the bot join your local single-player Bedrock world:

1. Open your world in Minecraft Bedrock.
2. In game settings, ensure **Multiplayer Game** is toggled ON and **Visible to LAN Players** is ON.
3. Run the bot with the `-local` flag:
   ```powershell
   .\bebot.exe -local
   ```
   *(Optional: choose a custom bot name with `-name <Name>`)*

The bot automatically listens on UDP port 7551 for modern Bedrock NetherNet discovery broadcasts, acquires the world session Nonce and NetworkID, and joins your world instantly without requiring a second Microsoft account.

---

## Commands

Commands can be typed directly into the bot's terminal console or sent via in-game chat / whisper using the `bebot` prefix or `!b` alias (e.g. `!b say <msg>`, `!b coords`, `!b help`):

### 1. Mining & Block Breaking

* **`mine <ax> <ay> <az> <bx> <by> <bz> [repeat: true|false] [faceAccurate: true|false]`**
  *(Aliases: `mineloop`, `minefarm`, `minearea`, `minespace`, `breakarea`, `breakspace`)*
  Mines all blocks in the 3D cuboid defined by opposite corners A and B.
  * **Top-Down Traversal**: Mines in an efficient serpentine layer-by-layer sequence from the highest Y down to the lowest Y.
  * **Automatic Pathing**: If blocks are farther than reach distance, the bot automatically walks toward the block and faces it.
  * **Dynamic Block Detection**: Any new block that appears within the area (pushed in by a piston, generated by a cobblestone generator, or placed by a player) is automatically detected via server `UpdateBlock` packets and queued for mining without duplicate entries.
  * **Repeat Mode** (`[repeat: true|false]`, default: `false`): If `true`, upon completing the entire area, the bot reruns the mining area from the top indefinitely (ideal for repeated passes or regenerators). If `false` (default), the bot announces completion and stops mining.
  * **Face Accurate** (`[faceAccurate: true|false]`, default: `false`): When `false`, uses standard Face 1 mining. When `true`, calculates the exact visible block face (`Face 0` for overhead/bottom face, side faces, top face) and points camera directly at the block.
* **`mine timeout <seconds>`** *(Alias: `minetimeout`)*
  Gets or sets the per-block mining timeout in seconds (default: `3.0`s). When a block cannot be mined because it is air, water/lava liquid, bedrock, or protected, the bot halts breaking after this timeout, marks the block as unmineable, and skips immediately to the next block in the queue.
  * Unmineable coordinates are kept in a separate in-memory blacklist for the current mine command and are skipped by repeated passes and queue selection. The blacklist is cleared when the command ends and reset whenever a new mine command starts.
* **`break <x> <y> <z> [animate: true|false] [repeat: true|false]`**
  *(Aliases: `breakat`, `breakcoord`, `mineat`)*
  Breaks a single block at the given coordinate. Optional arm animation and repeat flag.
* **`stopmine` / `stopbreak` / `cancelmine` / `cancelbreak`**
  Cancels active area mining or single-block breaking.

### 2. Movement & Player Interaction

* **`goto <x y z>`** *(Alias: `pathfind`)*: Autonomous 3D voxel pathfinding to target coordinates, navigating around obstacles, stepping up elevation, descending safe ledges, and avoiding hazards. Requires the block palette and chunk data to be loaded (`bebot palette load` / `bebot chunk load` when `behaviour.palette_load` or `behaviour.chunk_load` are disabled); `goto` reports every missing prerequisite.
* **`goto <player>`**: Finds designated player on the server and pathfinds to their position with dynamic tracking.
* **`stoppath`** *(Aliases: `cancelpath`, `goto stop`)*: Aborts active 3D pathfinding navigation.
* **`move <x> <y> <z>`**: Straight-line vector movement to the specified target coordinates (without obstacle avoidance).
* **`follow <player>`**: Follows the specified player wherever they walk.
* **`mimic <player>`**: Follows the player and mirrors their movements and actions (`mimic clear` to stop).
* **`camic <player>`**: Mirrors the player's camera pitch and yaw (`camic clear` to stop).
* **`lookat [player | <x y z>]`**: Turns to look directly at a player or coordinate. If called without arguments, looks at the nearest player.
* **`stoplookat`**: Stops locking camera orientation on a target.
* **`sneak [on|off|toggle]` / `unsneak`**: Toggles or sets sneaking/crouching state.
* **`headshake <yes|no> [repeat: true|false]`**
  *(Aliases: `shake`, `nod`, `yes`, `no`)*
  Nods head vertically (`yes`/`nod`) or shakes head horizontally (`no`), with optional continuous repeat (`headshake clear` to stop).
* **`sleep [x y z]`**: Sleeps in or interacts with a bed at the specified coordinate (or current position).
* **`emote <uuid> [repeat: true|false] [length]`**: Plays a Bedrock emote by its UUID (`emote clear` to stop).
* **`idle` / `stop`**: Stops all active movement, mining, following, or combat routines.

### 3. Combat & Automation

* **`kill <target> <cps>`**: Attacks the target player or entity nearby at the specified Clicks Per Second (1–100 CPS).
* **`stopkill`**: Stops attacking.
* **`payback [on|off]`**: Toggles auto-retaliation. When enabled, the bot automatically attacks any entity or player that damages it.
* **`autofish [on|off]`**: Automatically detects fishing bobber bite motion, reels in the caught item, and casts the rod again.

### 4. Inventory & Items

* **`hotbar <0-8>`** *(Alias: `slot`)*: Selects an active hotbar slot.
* **`transfer <slotA> <slotB>`**: Moves or swaps items between two inventory slots.
* **`drop <slot> [amount]`**: Drops items from the given inventory slot.
* **`dropall`**: Drops all items in the bot's inventory.

### 5. Waypoints & Navigation

* **`waypoint list`** *(Aliases: `waypoints`, `wp list`)*: Lists all saved waypoints.
* **`waypoint add <name> [x y z]`**: Saves a waypoint at the specified coordinates (or current bot location if omitted).
* **`waypoint del <name>`** *(Aliases: `remove`, `rm`)*: Deletes a saved waypoint.
* **`waypoint update <name> [x y z]`**: Updates an existing waypoint to new coordinates.
* **`waypoint <name>` / `waypoint goto <name>`**: Pathfinds / moves the bot to the saved waypoint.

### 6. World & Entity Information

* **`coords`** *(Aliases: `pos`, `coord`, `where`, `playercoords`, `playerpos`)*: Displays the bot's current coordinates, dimension, health, and orientation.
* **`nearby`** *(Alias: `near`)*: Lists nearby entities within range.
* **`online`** *(Aliases: `players`, `list`)*: Lists all players currently connected to the server.
* **`count <beds|villagers|golems>`** *(Aliases: `beds`, `villagers`, `golems`)*: Counts nearby beds, villagers, or iron golems.
* **`nether <x z> [y]`**: Calculates corresponding Nether portal coordinates (Overworld / 8).
* **`chunk <load|unload|status>`**: Loads or unloads the local 3x3x3 sub-chunk window around the bot to inspect real-time block data without background memory overhead.
* **`inspect <x> <y> <z>`** *(Alias: `probe`)*: Inspects the block state and runtime ID at target coordinates in the loaded chunk window.
* **`palette <load|unload|status>`**: Loads or unloads the full block palette dictionary (23k+ block states) to/from memory. When unloaded, the actual `air` runtime ID is always retained.
* **`block lookup <name|id>`**: Looks up block names by runtime ID or vice versa.
* **`trim` / `gc` / `memory [trim|stats]`**: Manually triggers garbage collection and releases unneeded virtual memory back to the OS.

### 7. Customization & Settings

* **`skin`** *(Alias: `skin status`)*: Shows current skin status, texture path, and persistence file.
* **`skin list`**: Lists all available PNG skin files found in `data/skins/` and `skin/`.
* **`skin use <name> [slim|normal] [reload]`**: Quickly switches to a skin from `data/skins/` or `skin/` without typing the full path.
* **`skin <slim|normal|wide> <path> [reload]`**: Changes the bot's skin to a classic PNG file.
* **`skin pack <folder> <skin_id> [reload]`**: Applies a skin from a Bedrock skin pack folder.
* **`skin pack <folder> list`**: Lists all available skins inside a skin pack folder.
* **`skin default [reload]`**: Reverts skin to the default slim skin.
* **`skin toggle override`**: Toggles whether other players on the server see real-time skin updates.
* **`renderdistance <chunks>`**: Requests a chunk render distance change from the server.

### 8. Session Management

* **`say <message>`**: Sends a public chat message.
* **`rejoin`** *(Alias: `reconnect`)*: Forces the bot to disconnect and reconnect to the server.
* **`quit`** *(Alias: `exit`)*: Safely disconnects and shuts down the bot.
* **`help`** *(Aliases: `commands`, `commandlist`)*: Prints the quick command list.
