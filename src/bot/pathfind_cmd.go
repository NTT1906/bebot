package bot

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// gotoCmd handles 'goto' and 'pathfind' commands.
// Syntax:
//   goto <x y z>     - pathfinds to coordinates
//   goto <player>    - pathfinds to designated player
//   goto stop        - stops active pathfinding
func (b *Bot) gotoCmd(args []string) {
	if len(args) == 0 {
		b.reply("Usage: goto <x y z> | goto <player> | goto stop")
		return
	}

	// 1. Single argument: check for stop/cancel or player name
	if len(args) == 1 {
		arg0 := strings.ToLower(args[0])
		if arg0 == "stop" || arg0 == "cancel" || arg0 == "clear" || arg0 == "off" {
			b.stopPathCmd()
			return
		}
		b.gotoPlayer(args[0])
		return
	}

	// 2. 3 arguments: x y z coordinates
	if len(args) >= 3 {
		// Clean any trailing commas (e.g. "goto 100, 64, -200")
		xStr := strings.TrimSuffix(args[0], ",")
		yStr := strings.TrimSuffix(args[1], ",")
		zStr := strings.TrimSuffix(args[2], ",")

		x, errX := strconv.ParseFloat(xStr, 64)
		y, errY := strconv.ParseFloat(yStr, 64)
		z, errZ := strconv.ParseFloat(zStr, 64)

		if errX != nil || errY != nil || errZ != nil {
			b.reply("Usage: goto <x y z> | goto <player>")
			return
		}

		targetPos := protocol.BlockPos{
			int32(math.Floor(x)),
			int32(math.Floor(y)),
			int32(math.Floor(z)),
		}
		go b.startPath(targetPos, "")
		return
	}

	b.reply("Usage: goto <x y z> | goto <player>")
}

// gotoPlayer locates an online player and pathfinds to their position.
func (b *Bot) gotoPlayer(targetName string) {
	cleanName := cleanChatName(strings.TrimSpace(targetName))
	if cleanName == "" {
		b.reply("Usage: goto <player>")
		return
	}

	// Cannot navigate to self
	if b.matchesIdentifier(cleanName) || strings.EqualFold(cleanChatName(b.botName), cleanName) {
		b.reply("Cannot pathfind to myself!")
		return
	}

	pEntry, found := b.resolvePlayer(cleanName)
	if !found {
		b.reply(fmt.Sprintf("Player '%s' not found or offline.", cleanName))
		return
	}

	if !pEntry.hasLocation && pEntry.id == 0 {
		b.reply(fmt.Sprintf("Player '%s' location is currently unknown.", cleanName))
		return
	}

	targetPos := vec3ToBlockPos(pEntry.position)
	displayName := pEntry.name
	if displayName == "" {
		displayName = cleanName
	}

	go b.startPath(targetPos, displayName)
}

// stopPathCmd stops pathfinding and replies to user.
func (b *Bot) stopPathCmd() {
	if b.stopPath() {
		b.reply("[Bebot] Path navigation stopped.")
	} else {
		b.reply("[Bebot] No active path to stop.")
	}
}
