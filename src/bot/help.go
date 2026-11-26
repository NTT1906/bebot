package bot

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

var commandHelpDocs = map[string]string{
	"move": `§6=== Help: move ===§r
Description: Pathfinds and walks to the specified coordinates.
Syntax: move <x y z>
Examples:
  bebot move 250 64 -220
  bebot move 100 70 500`,

	"follow": `§6=== Help: follow ===§r
Description: Continuously follows a player. If no player name is given, follows the command sender.
Syntax: follow [player] or follow stop
Examples:
  bebot follow (follows command sender)
  bebot follow Arie1906
  bebot follow stop (or bebot stop)`,

	"mimic": `§6=== Help: mimic ===§r
Description: Mirrors a target player's movements, jumps, camera rotation, sneaking, and swimming.
Syntax: mimic <player>
Examples:
  bebot mimic Arie1906
  bebot stop (to stop mimicking)`,

	"camic": `§6=== Help: camic ===§r
Description: Mimics a target player's camera rotation (yaw and pitch) only without walking or moving.
Syntax: camic <player>
Examples:
  bebot camic Arie1906
  bebot stop (to stop camera mimic)`,

	"lookat": `§6=== Help: lookat ===§r
Description: Points the bot's head and eyes toward a player or specific coordinate.
Syntax: lookat <player> or lookat <x y z>
Examples:
  bebot lookat Arie1906
  bebot lookat 250 65 -220
  bebot stoplookat`,

	"stoplookat": `§6=== Help: stoplookat ===§r
Description: Stops tracking a player or point with the camera and restores default look behavior.
Syntax: stoplookat
Example:
  bebot stoplookat`,

	"sneak": `§6=== Help: sneak ===§r
Description: Sets or toggles the bot's crouching/sneaking state.
Syntax: sneak [on|off|toggle]
Examples:
  bebot sneak on
  bebot sneak off
  bebot sneak (toggles state)`,

	"unsneak": `§6=== Help: unsneak ===§r
Description: Stops sneaking and stands upright.
Syntax: unsneak
Example:
  bebot unsneak`,

	"jump": `§6=== Help: jump ===§r
Description: Makes the bot jump periodically every N seconds (default: 10s).
Syntax: jump [freq_seconds, default: 10] or jump stop
Examples:
  bebot jump (jumps every 10s)
  bebot jump 5 (jumps every 5s)
  bebot jump stop (or bebot stopjump)`,

	"stopjump": `§6=== Help: stopjump ===§r
Description: Stops periodic jumping.
Aliases: jump stop, jump off
Syntax: stopjump
Example:
  bebot stopjump`,

	"idle": `§6=== Help: idle / stop ===§r
Description: Stops active movement, following, mimicking, and mining.
Aliases: stop
Syntax: idle or stop [reset]
Examples:
  bebot stop
  bebot idle reset (stops and resets facing to north)`,

	"spin": `§6=== Help: spin ===§r
Description: Continuously rotates the bot's camera yaw every tick (360 degrees sweep). Useful to satisfy server FOV checks for waypoint updates.
Syntax: spin [clockwise|counterclockwise|reverse|stop] [speed]
Aliases: cw, ccw, rev, stop, off
Default speed: 10 degrees/tick (~200 deg/s)
Examples:
  bebot spin
  bebot spin counterclockwise 15
  bebot spin reverse
  bebot spin stop (or bebot stop)`,

	"coords": `§6=== Help: coords ===§r
Description: Displays the bot's current coordinates, dimension, and facing angles.
Aliases: pos, coord, coordinates, playerpos, where
Syntax: coords
Example:
  bebot coords`,

	"waypoint": `§6=== Help: waypoint ===§r
Description: Manages saved waypoint coordinates.
Aliases: wp, waypoints
Syntax:
  waypoint add <name> [x y z] - save current or specified position
  waypoint del <name> - delete a waypoint
  waypoint list - list all saved waypoints
  waypoint goto <name> - move to saved waypoint
Examples:
  bebot waypoint add base 250 64 -220
  bebot waypoint list
  bebot waypoint goto base`,

	"place": `§6=== Help: place ===§r
Description: Places the block the bot is currently holding in hand at specified coordinates.
Aliases: placeblock
Syntax: place <x y z>
Examples:
  bebot place 256 62 -219
  !b place 100 64 -200`,

	"break": `§6=== Help: break ===§r
Description: Breaks a single block at specified coordinates.
Aliases: breakat, breakcoord
Syntax: break <x y z> [animation:true|false]
Examples:
  bebot break 256 62 -219
  bebot break 256 62 -219 true
  bebot stopbreak`,

	"mine": `§6=== Help: mine ===§r
Description: Mines all solid blocks within a 3D bounding box from top to bottom.
Aliases: minearea, minespace, breakarea, breakspace
Syntax: mine <ax ay az> <bx by bz> [repeat:true|false] [faceAccurate:true|false]
Examples:
  bebot mine 250 64 -220 255 60 -215
  bebot mine 10 60 10 15 50 15 repeat
  bebot stopmine`,

	"stopmine": `§6=== Help: stopmine / stopbreak ===§r
Description: Cancels and aborts any active area mining or single-block break operation.
Aliases: stopbreak, abortbreak, cancelmine, cancelbreak
Syntax: stopmine
Example:
  bebot stopmine`,

	"minetimeout": `§6=== Help: mine timeout ===§r
Description: Sets per-block break timeout in seconds. Blocks exceeding this duration (e.g. bedrock/air) are skipped.
Aliases: minetimeout, timeout
Syntax: mine timeout <seconds>
Examples:
  bebot mine timeout 3.0
  bebot minetimeout 1.5`,

	"learn": `§6=== Help: mine learn ===§r
Description: Calibrates and confirms the server's air block runtime ID by breaking a solid block.
Aliases: mine learn, learnair, calibrate, minecalibrate
Syntax: mine learn [x y z]
Examples:
  bebot mine learn 256 62 -219
  bebot mine learn (breaks block in front of bot)`,

	"air": `§6=== Help: mine air ===§r
Description: Displays or manages the set of runtime IDs recognized as air by the miner.
Syntax:
  mine air - view recognized air IDs
  mine air add <id> - manually add an air runtime ID
  mine air remove <id> - remove a specific runtime ID
  mine air reset - restore default vanilla air hashes
Examples:
  bebot mine air
  bebot mine air remove 3727763636
  bebot mine air reset`,

	"sleep": `§6=== Help: sleep ===§r
Description: Interacts with a bed block to sleep and set spawn point.
Syntax: sleep [x y z]
Examples:
  bebot sleep 254 64 -222 (sleeps at exact bed coordinates)
  bebot sleep (interacts with block in front)
  bebot sleep wake (or 'wakeup' to leave bed)`,

	"wakeup": `§6=== Help: wakeup ===§r
Description: Wakes up and exits the bed.
Aliases: wake, unsleep
Syntax: wakeup
Example:
  bebot wakeup`,

	"hotbar": `§6=== Help: hotbar ===§r
Description: Selects an active hotbar slot (0 through 8).
Aliases: slot
Syntax: hotbar <0-8>
Examples:
  bebot hotbar 0 (selects first slot)
  bebot hotbar 4`,

	"dropall": `§6=== Help: dropall ===§r
Description: Drops all items from inventory. Defaults to dumping everything at once, or 1-by-1 if 'each' is specified.
Variants:
  dropall - via PlayerActionDropItem
  dropall2 - via container cursor slot
  dropall3 - via ItemStackRequest network action
Syntax: dropall[2|3] [all|each] [random:true|false]
Examples:
  bebot dropall (dumps full inventory at once)
  bebot dropall each (drops 1 slot per tick)
  bebot dropall2 random (cursor drop with random angles)`,

	"droploop": `§6=== Help: droploop ===§r
Description: Continuously drops the full inventory every 20 ticks (1s) at eye direction or random direction.
Aliases: loopdrop
Syntax: droploop [random] [intervalTicks]
Examples:
  bebot droploop (loops every 20 ticks forward)
  bebot droploop random (loops with random pitch/yaw)
  bebot droploop random 40 (loops every 40 ticks)
  bebot stopdroploop`,

	"stopdroploop": `§6=== Help: stopdroploop ===§r
Description: Stops the continuous dropping loop.
Aliases: stopdrop, stoploopdrop
Syntax: stopdroploop
Example:
  bebot stopdroploop`,

	"drop": `§6=== Help: drop ===§r
Description: Drops items from a specific inventory slot.
Variants: drop (player action), drop2 (cursor), drop3 (stack request)
Syntax: drop[2|3] <slot> [count] [random:true|false]
Examples:
  bebot drop 0 (drops 1 item from slot 0)
  bebot drop 0 64 (drops full stack from slot 0)
  bebot drop2 5 32 random`,

	"sort-inv": `§6=== Help: sort-inv ===§r
Description: Consolidates and merges partial stacks, grouping identical items into lowest slots starting from slot 0.
Aliases: sort, sortinv, sort-inventory
Syntax: sort-inv
Example:
  bebot sort`,

	"transfer": `§6=== Help: transfer ===§r
Description: Swaps or moves items between two inventory slots.
Syntax: transfer <slotA> <slotB>
Example:
  bebot transfer 0 9`,

	"open": `§6=== Help: open ===§r
Description: Opens a container (chest, barrel, dropper) and keeps it open for interaction until 'close'.
Aliases: opencontainer
Syntax: open [x y z]
Examples:
  bebot open 256 64 -219
  bebot close`,

	"close": `§6=== Help: close ===§r
Description: Closes the currently opened container.
Aliases: closecontainer
Syntax: close
Example:
  bebot close`,

	"loot": `§6=== Help: loot ===§r
Description: Opens a container, transfers all items into the bot's inventory, and closes it.
Aliases: steal
Syntax: loot [x y z]
Example:
  bebot loot 256 64 -219`,

	"take": `§6=== Help: take ===§r
Description: Takes items from a specific slot in a container.
Syntax: take [x y z] <containerSlot> [count]
Examples:
  bebot take 256 64 -219 0 (takes 1 item from slot 0)
  bebot take 256 64 -219 0 64 (takes stack from slot 0)`,

	"container": `§6=== Help: container ===§r
Description: Inspects and prints items inside a container without taking them.
Aliases: chest, viewchest, viewcontainer
Syntax: container [x y z]
Example:
  bebot container 256 64 -219`,

	"inv": `§6=== Help: inv ===§r
Description: Displays all non-empty item slots in the bot's inventory.
Aliases: inventory, myinv, items
Syntax: inv
Example:
  bebot inv`,

	"players": `§6=== Help: players / list ===§r
Description: Lists all players currently connected to the server.
Aliases: list, online
Syntax: players
Example:
  bebot players`,

	"nearby": `§6=== Help: nearby ===§r
Description: Lists all players within render range and their distance to the bot.
Aliases: near
Syntax: nearby
Example:
  bebot nearby`,

	"radar": `§6=== Help: radar ===§r
Description: Scans for other players, reporting their exact coordinates and distance while excluding the sender.
Syntax: radar
Example:
  bebot radar`,

	"focus": `§6=== Help: focus ===§r
Description: Focuses a specific bot by name or UUID. When focused, all other bots ignore commands.
Syntax: focus <bot name|bot uuid>
Examples:
  !b focus Bot1
  !b focus 550e8400-e29b-41d4-a716-446655440000`,

	"run-as": `§6=== Help: run-as ===§r
Description: Runs a command targeted at a specific bot by name or UUID, even when focus mode is active on another bot.
Aliases: runas, as
Syntax: run-as <bot name|bot uuid> <command...>
Examples:
  !b run-as CobbleGolem_53 coords
  !b run-as Bebot follow
  !b run-as 550e8400-e29b-41d4-a716-446655440000 spin`,

	"unfocus": `§6=== Help: unfocus ===§r
Description: Clears bot focus so all bots accept commands again.
Syntax: unfocus
Example:
  !b unfocus`,

	"say": `§6=== Help: say ===§r
Description: Makes the bot speak a message in public chat.
Syntax: say <message>
Example:
  bebot say Hello world!`,

	"payback": `§6=== Help: payback ===§r
Description: Toggles retaliatory combat; when attacked by an entity, the bot immediately hits back.
Syntax: payback [on|off]
Examples:
  bebot payback on
  bebot payback off`,

	"autofish": `§6=== Help: autofish ===§r
Description: Toggles auto-fishing mode when holding a fishing rod.
Syntax: autofish [on|off]
Examples:
  bebot autofish on
  bebot autofish off`,

	"kill": `§6=== Help: kill ===§r
Description: Attacks a player continuously at a specified CPS until target dies or stopkill is issued.
Syntax: kill <player> <cps>
Examples:
  bebot kill Steve 12
  bebot stopkill`,

	"stopkill": `§6=== Help: stopkill ===§r
Description: Cancels active combat attack mode.
Syntax: stopkill
Example:
  bebot stopkill`,

	"skin": `§6=== Help: skin ===§r
Description: Changes the bot's appearance to a PNG, skin pack, or default skin.
Syntax:
  skin <slim|normal> <path> - change classic PNG skin
  skin pack <folder> <skin_id> - apply skin from pack
  skin pack <folder> list - list skins in pack
  skin default - reset to standard slim skin
Examples:
  bebot skin slim skin/my_skin.png
  bebot skin default`,

	"emote": `§6=== Help: emote ===§r
Description: Plays a Bedrock emote by bookmark name or UUID, with support for saved bookmarks.
Syntax:
  emote <name|uuid> [repeat:true|false] [length] - play an emote
  emote add <name> <uuid> - save an emote bookmark (supports quoted names)
  emote remove <name> - delete an emote bookmark
  emote list - list all saved emote bookmarks
  emote clear / emote stop - stop active or repeating emote
Examples:
  bebot emote wave
  bebot emote "hello world"
  bebot emote add "hello world" c1819d2b-c8c3-4d69-b5f7-66a7b370605a
  bebot emote list
  bebot emote remove "hello world"
  bebot emote clear`,

	"headshake": `§6=== Help: headshake ===§r
Description: Shakes the bot's head vertically (yes/nod) or horizontally (no).
Aliases: shake
Syntax: headshake <yes|no> [repeat:true|false] or headshake clear
Examples:
  bebot headshake yes
  bebot headshake no repeat
  bebot headshake clear`,

	"nether": `§6=== Help: nether ===§r
Description: Calculates Overworld to Nether (x/8, z/8) or Nether to Overworld coordinate conversions.
Syntax: nether <x z> or nether <x y z>
Examples:
  bebot nether 800 1600
  bebot nether 800 64 1600`,

	"count": `§6=== Help: count ===§r
Description: Counts nearby entities (villagers, iron golems) or blocks (beds).
Syntax: count <beds|villagers|golems>
Examples:
  bebot count beds
  bebot count villagers
  bebot count golems`,

	"chunk": `§6=== Help: chunk ===§r
Description: Loads, unloads, or inspects the local 3x3x3 sub-chunk window (27 sub-chunks) in memory.
Syntax:
  chunk load - enable sub-chunk caching around current position
  chunk unload - disable caching and free all sub-chunk memory
  chunk status - view active sub-chunks and center coordinate
Examples:
  bebot chunk load
  bebot chunk status
  bebot chunk unload`,

	"inspect": `§6=== Help: inspect ===§r
Description: Instantly queries the block state and runtime ID at target coordinates using the local chunk cache.
Aliases: probe
Syntax: inspect <x> <y> <z>
Examples:
  bebot inspect 250 64 -220
  bebot probe 100 70 500`,

	"mem": `§6=== Help: mem ===§r
Description: Displays real-time Go runtime heap and OS memory metrics.
Aliases: memory
Syntax: mem
Example:
  !b mem`,

	"bundletest": `§6=== Help: bundletest ===§r
Description: Runs race-condition tests between two droppers/containers to analyze transfer timing.
Syntax: bundletest <posA> <posB> [attempts] or bundletest stop
Example:
  bebot bundletest 10 60 10 12 60 10 50`,

	"rejoin": `§6=== Help: rejoin ===§r
Description: Disconnects and reconnects the bot to the server.
Aliases: reconnect
Syntax: rejoin
Example:
  bebot rejoin`,

	"quit": `§6=== Help: quit / exit ===§r
Description: Disconnects the bot and terminates the program.
Aliases: exit
Syntax: quit or exit
Example:
  bebot quit`,
}

// aliasMapping maps command aliases to canonical documentation keys
var aliasMapping = map[string]string{
	"pos":             "coords",
	"coord":           "coords",
	"coordinates":     "coords",
	"playerpos":       "coords",
	"where":           "coords",
	"stop":            "idle",
	"wp":              "waypoint",
	"waypoints":       "waypoint",
	"breakat":         "break",
	"breakcoord":      "break",
	"placeblock":      "place",
	"minearea":        "mine",
	"minespace":       "mine",
	"breakarea":       "mine",
	"breakspace":      "mine",
	"stopbreak":       "stopmine",
	"abortbreak":      "stopmine",
	"cancelmine":      "stopmine",
	"cancelbreak":     "stopmine",
	"timeout":         "minetimeout",
	"learnair":        "learn",
	"calibrate":       "learn",
	"minecalibrate":   "learn",
	"minelearn":       "learn",
	"wake":            "wakeup",
	"unsleep":         "wakeup",
	"slot":            "hotbar",
	"dropall2":        "dropall",
	"dropall3":        "dropall",
	"loopdrop":        "droploop",
	"stopdrop":        "stopdroploop",
	"stoploopdrop":    "stopdroploop",
	"drop2":           "drop",
	"drop3":           "drop",
	"sort":            "sort-inv",
	"sortinv":         "sort-inv",
	"sort-inventory":  "sort-inv",
	"opencontainer":   "open",
	"closecontainer":  "close",
	"steal":           "loot",
	"chest":           "container",
	"viewchest":       "container",
	"viewcontainer":   "container",
	"inventory":       "inv",
	"myinv":           "inv",
	"items":           "inv",
	"list":            "players",
	"online":          "players",
	"near":            "nearby",
	"shake":           "headshake",
	"stopjump":        "stopjump",
	"probe":           "inspect",
	"memory":          "mem",
	"reconnect":       "rejoin",
	"exit":            "quit",
}

func getCommandHelp(cmd string) (string, bool) {
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	if target, ok := aliasMapping[cmd]; ok {
		cmd = target
	}
	doc, ok := commandHelpDocs[cmd]
	return doc, ok
}

func (b *Bot) helpCmd(p []string) {
	pages := [][]string{
		{
			"§6--- Bebot Help [1/4: Movement & Camera] ---§r",
			"§7Prefix: 'bebot <cmd>' or '!b <cmd>' (e.g. '!b say hi')§r",
			"move <x y z> - move to coordinates",
			"follow <player> - follow a player",
			"mimic <player> - mimic movement and actions",
			"camic <player> - mimic camera rotation only",
			"lookat [player|x y z] - look at player or coordinates",
			"stoplookat - stop looking at a player",
			"sneak [on|off|toggle] - toggle or set sneaking",
			"unsneak - stop sneaking",
			"jump [freq] - jump periodically (default: 10s)",
			"stopjump / jump stop - stop periodic jumping",
			"spin [cw|ccw] [speed] - spin camera continuously",
			"idle / stop - stop movement, jumping & spin",
			"coords - show bot coordinates",
			"waypoint help - waypoint add/remove/list",
			"rejoin / reconnect - reconnect to server",
			"§7(Type 'help 2' for Mining & Beds, or 'help <cmd>')§r",
		},
		{
			"§6--- Bebot Help [2/4: Mining & Building] ---§r",
			"place <x y z> - place held block at coordinates",
			"break <x y z> [animation] - break a single block",
			"mine <pos1> <pos2> [repeat] [faceAccurate] - area mine top-down",
			"mine timeout <seconds> - set per-block break timeout (default 3.0s)",
			"mine learn [x y z] - calibrate air runtime ID from block",
			"mine air [clear|add|remove] - view/manage known air IDs",
			"stopmine / stopbreak - stop mining",
			"sleep [x y z] - interact with bed to sleep & set spawn",
			"wakeup / sleep wake - wake up from bed",
			"§7(Type 'help 3' for Inventory & Items, or 'help <cmd>')§r",
		},
		{
			"§6--- Bebot Help [3/4: Inventory & Items] ---§r",
			"hotbar <0-9> - select hotbar slot",
			"dropall [all|each] [random] - drop full inventory",
			"dropall2 / dropall3 [all|each] - cursor/stack request drop",
			"droploop [random] [ticks] - continuous drop loop",
			"stopdroploop - stop drop loop",
			"drop <slot> [amount] [random] - drop from specific slot",
			"sort-inv / sort - merge and group items in inventory",
			"transfer <slot a> <slot b> - swap/move slots",
			"open <x y z> / close - open/close container",
			"loot <x y z> / take <x y z> <slot> - loot items from container",
			"container <x y z> - inspect container items",
			"§7(Type 'help 4' for Combat & Utility, or 'help <cmd>')§r",
		},
		{
			"§6--- Bebot Help [4/4: Combat & Utility] ---§r",
			"focus / unfocus - target commands to a specific bot",
			"run-as <bot> <cmd> - run command as specific bot",
			"players / list / nearby / radar - list/track online players",
			"say <message> - send a chat message",
			"payback [on|off] - fight back when attacked",
			"autofish [on|off] - toggle auto-fishing",
			"kill <target> <cps> / stopkill - attack a player",
			"skin <slim|normal> <path> - change skin PNG",
			"skin pack <folder> list - list skins in pack",
			"skin default - revert to default slim skin",
			"emote <name|uuid> [repeat] [length] / emote clear",
			"emote add <name> <uuid> / remove <name> / list",
			"headshake yes|no [repeat] / headshake clear",
			"mem / memory - show memory & heap stats",
			"quit / exit - disconnect bot",
		},
	}

	if len(p) >= 2 {
		arg := strings.ToLower(p[1])
		switch arg {
		case "1", "move", "movement":
			b.reply(strings.Join(pages[0], "\n"))
			return
		case "2", "mining":
			b.reply(strings.Join(pages[1], "\n"))
			return
		case "3", "inv", "inventory", "item", "items":
			b.reply(strings.Join(pages[2], "\n"))
			return
		case "4", "util", "utility", "combat", "misc":
			b.reply(strings.Join(pages[3], "\n"))
			return
		case "all":
			go func() {
				for i, page := range pages {
					b.reply(strings.Join(page, "\n"))
					if i < len(pages)-1 {
						time.Sleep(300 * time.Millisecond)
					}
				}
			}()
			return
		default:
			// Check if integer page number
			if n, err := strconv.Atoi(arg); err == nil && n >= 1 && n <= len(pages) {
				b.reply(strings.Join(pages[n-1], "\n"))
				return
			}

			// Check detailed command help
			if doc, ok := getCommandHelp(arg); ok {
				b.reply(doc)
				return
			}

			// If two words like "mine air" or "mine learn"
			if len(p) >= 3 {
				combined := arg + " " + strings.ToLower(p[2])
				if combined == "mine air" {
					if doc, ok := getCommandHelp("air"); ok {
						b.reply(doc)
						return
					}
				}
				if combined == "mine learn" {
					if doc, ok := getCommandHelp("learn"); ok {
						b.reply(doc)
						return
					}
				}
				if combined == "mine timeout" {
					if doc, ok := getCommandHelp("minetimeout"); ok {
						b.reply(doc)
						return
					}
				}
			}

			b.reply(fmt.Sprintf("Unknown command '%s'. Type 'bebot help' for command list.", p[1]))
			return
		}
	}

	// Default: show Page 1
	b.reply(strings.Join(pages[0], "\n"))
}
