package bot

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func (b *Bot) say(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil {
		_ = b.conn.WritePacket(&packet.Text{TextType: packet.TextTypeChat, Message: s, NeedsTranslation: false})
	}
}

func (b *Bot) commandFromChat(sender, text string) {
	text = strings.TrimSpace(strings.Trim(text, "\ufeff\u200b\u200c\u200d"))
	if !strings.HasPrefix(strings.ToLower(text), "bebot") {
		return
	}
	if !b.isSuperuser(sender) && !isPublicCommand(text) {
		return
	}
	go b.command(text)
}

func isPublicCommand(line string) bool {
	p := strings.Fields(line)
	if len(p) == 0 {
		return false
	}
	if strings.EqualFold(p[0], "bebot") || strings.EqualFold(p[0], "bebop") {
		p = p[1:]
	}
	if len(p) == 0 {
		return false
	}
	switch strings.ToLower(p[0]) {
	case "nether", "day":
		return true
	}
	return false
}

func (b *Bot) isSuperuser(name string) bool {
	return b.cfg.Superuser != "" && strings.EqualFold(cleanChatName(name), cleanChatName(b.cfg.Superuser))
}

func (b *Bot) consoleCommand(line string) {
	b.mu.Lock()
	b.fromConsole = true
	b.whisperTarget = ""
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.fromConsole = false
		b.mu.Unlock()
	}()
	b.command(line)
}

// ConsoleCommand runs a bot command as if typed at the local interactive console.
func (b *Bot) ConsoleCommand(line string) {
	b.consoleCommand(line)
}

func parseWhisper(msg string) (sender, text string, ok bool) {
	low := strings.ToLower(msg)
	marker := "whispers to you"
	pos := strings.Index(low, marker)
	if pos < 0 {
		return "", "", false
	}
	sender = cleanChatName(msg[:pos])
	rest := strings.TrimPrefix(strings.TrimSpace(msg[pos+len(marker):]), ":")
	return sender, strings.TrimSpace(rest), true
}

func (b *Bot) handleWhisper(sender, message string) {
	sender = cleanChatName(sender)
	message = strings.TrimSpace(strings.Trim(message, "\ufeff\u200b\u200c\u200d"))
	if message == "" {
		return
	}
	fmt.Printf("[whisper - %s] <%s> %s\n", time.Now().Format("2006-01-02 15:04:05"), sender, message)
	if !b.isSuperuser(sender) || !strings.HasPrefix(strings.ToLower(message), "bebot") {
		return
	}
	b.mu.Lock()
	b.whisperTarget = sender
	b.fromConsole = false
	b.mu.Unlock()
	go func() {
		defer func() {
			b.mu.Lock()
			b.whisperTarget = ""
			b.mu.Unlock()
		}()
		b.command(message)
	}()
}

func (b *Bot) runCommand(cmd string) {
	b.mu.Lock()
	c := b.conn
	viaChat := b.cfg.CommandsViaChat != nil && *b.cfg.CommandsViaChat
	b.mu.Unlock()
	if c == nil {
		log.Println("bebot: not connected; cannot run command")
		return
	}
	if !strings.HasPrefix(cmd, "/") {
		cmd = "/" + cmd
	}
	var err error
	if viaChat {
		err = c.WritePacket(&packet.Text{TextType: packet.TextTypeChat, Message: cmd, NeedsTranslation: false})
	} else {
		err = c.WritePacket(&packet.CommandRequest{
			CommandLine: cmd,
			CommandOrigin: protocol.CommandOrigin{
				Origin:         protocol.CommandOriginPlayer,
				UUID:           uuid.New(),
				PlayerUniqueID: 0,
			},
			Internal: false,
			Version:  "latest",
		})
	}
	if err != nil {
		log.Println("bebot: command failed:", ExplainError(err))
		return
	}
	if err := c.Flush(); err != nil {
		log.Println("bebot: command flush failed:", ExplainError(err))
		return
	}
	log.Println("bebot: executed command:", cmd)
}

// RunCommand executes a Minecraft slash command on the server connection.
func (b *Bot) RunCommand(cmd string) {
	b.runCommand(cmd)
}

func (b *Bot) whisper(target, message string) {
	b.mu.Lock()
	c := b.conn
	viaChat := b.cfg.CommandsViaChat != nil && *b.cfg.CommandsViaChat
	b.mu.Unlock()
	if c == nil {
		return
	}
	cmd := "/w " + target + " " + message
	var err error
	if viaChat {
		err = c.WritePacket(&packet.Text{TextType: packet.TextTypeChat, Message: cmd, NeedsTranslation: false})
	} else {
		err = c.WritePacket(&packet.CommandRequest{
			CommandLine: cmd,
			CommandOrigin: protocol.CommandOrigin{
				Origin:         protocol.CommandOriginPlayer,
				UUID:           uuid.New(),
				PlayerUniqueID: 0,
			},
			Internal: false,
			Version:  "latest",
		})
	}
	if err != nil {
		log.Println("bebot: whisper failed:", ExplainError(err))
		return
	}
	_ = c.Flush()
}

func cleanChatName(name string) string {
	runes := []rune(name)
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\u00a7' && i+1 < len(runes) {
			i++
			continue
		}
		out = append(out, runes[i])
	}
	return strings.TrimSpace(string(out))
}

func (b *Bot) reply(message string) {
	log.Println("bebot:", message)
	b.mu.Lock()
	target := b.whisperTarget
	fromConsole := b.fromConsole
	b.mu.Unlock()
	if target != "" {
		b.whisper(target, message)
		return
	}
	if fromConsole {
		return
	}
	b.say(message)
}

func (b *Bot) command(line string) {
	line = strings.Trim(line, "\ufeff\u200b\u200c\u200d \t\r\n")
	p := strings.Fields(line)
	if len(p) == 0 {
		return
	}
	p[0] = strings.TrimSuffix(strings.TrimSuffix(p[0], ":"), ",")
	if strings.EqualFold(p[0], "bebot") || strings.EqualFold(p[0], "bebop") {
		p = p[1:]
	}
	if len(p) == 0 {
		return
	}
	switch strings.ToLower(p[0]) {
	case "say":
		if len(p) < 2 {
			b.reply("Usage: say <message>")
			return
		}
		b.say(strings.Join(p[1:], " "))
	case "stopkill":
		b.mu.Lock()
		b.killTarget = ""
		b.mu.Unlock()
		b.reply("Stopped killing.")
	case "kill":
		if len(p) < 3 {
			b.reply("Usage: kill <target> <cps>")
			return
		}
		targetName := strings.ToLower(p[1])
		cps, err := strconv.Atoi(p[2])
		if err != nil || cps <= 0 || cps > 100 {
			b.reply("Invalid CPS (must be 1-100).")
			return
		}
		b.mu.Lock()
		pEntry, ok := b.players[targetName]
		if !ok || !pEntry.online || pEntry.id == 0 {
			b.mu.Unlock()
			b.reply("Cannot find target nearby.")
			return
		}
		b.killTarget = targetName
		b.mu.Unlock()
		b.reply(fmt.Sprintf("Killing %s at %d CPS...", pEntry.name, cps))
		go b.killLoop(targetName, cps)
	case "payback":
		b.mu.Lock()
		b.payback = !b.payback
		if len(p) > 1 {
			b.payback = strings.EqualFold(p[1], "on") || strings.EqualFold(p[1], "true")
		}
		state := "off"
		if b.payback {
			state = "on"
		}
		b.mu.Unlock()
		b.reply("Payback mode is now " + state + ".")
	case "autofish":
		b.mu.Lock()
		b.autoFish = !b.autoFish
		if len(p) > 1 {
			b.autoFish = strings.EqualFold(p[1], "on") || strings.EqualFold(p[1], "true")
		}
		state := "off"
		if b.autoFish {
			state = "on"
		}
		b.mu.Unlock()
		b.reply("Auto-fish is now " + state + ".")
		if state == "on" {
			b.useItemInHand()
		}
	case "follow":
		if len(p) < 2 {
			b.reply("Usage: follow <playername>")
			return
		}
		b.followTarget = p[1]
		b.mimicTarget = p[1]
		b.mode = modeFollowing
		b.reply("Following " + p[1] + "…")
	case "mimic":
		if len(p) < 2 || strings.EqualFold(p[1], "clear") || strings.EqualFold(p[1], "stop") {
			b.mode = modeIdle
			b.followTarget = ""
			b.mimicTarget = ""
			b.reply("Cleared mimic mode.")
			return
		}
		b.mimicTarget = p[1]
		b.followTarget = p[1]
		b.mode = modeMimic
		b.reply("Mimicking " + p[1] + "!")
	case "camic":
		if len(p) < 2 || strings.EqualFold(p[1], "clear") || strings.EqualFold(p[1], "stop") {
			b.mode = modeIdle
			b.followTarget = ""
			b.mimicTarget = ""
			b.reply("Cleared camera mimic mode.")
			return
		}
		b.mimicTarget = p[1]
		b.followTarget = p[1]
		b.mode = modeCamic
		b.reply("Following camera rotation of " + p[1] + "!")
	case "lookat":
		if len(p) != 1 && len(p) != 2 && len(p) != 4 {
			b.reply("Usage: lookat [player] or lookat <x> <y> <z>")
			return
		}
		b.followTarget = ""
		b.mimicTarget = ""
		b.lookAtTarget = ""
		b.lookAtPos = nil
		if len(p) == 2 {
			b.lookAtTarget = p[1]
		} else if len(p) == 4 {
			var x, y, z float32
			if _, err := fmt.Sscanf(strings.Join(p[1:4], " "), "%f %f %f", &x, &y, &z); err != nil {
				b.reply("Usage: lookat [player] or lookat <x> <y> <z>")
				return
			}
			position := mgl32.Vec3{x, y, z}
			b.lookAtPos = &position
		}
		b.mode = modeLookAt
		if b.lookAtPos != nil {
			b.reply(fmt.Sprintf("Looking at (%.1f, %.1f, %.1f).", b.lookAtPos.X(), b.lookAtPos.Y(), b.lookAtPos.Z()))
		} else if b.lookAtTarget == "" {
			b.reply("Looking at the nearest player.")
		} else {
			b.reply("Looking at " + b.lookAtTarget + ".")
		}
	case "stoplookat":
		b.mode = modeIdle
		b.lookAtTarget = ""
		b.lookAtPos = nil
		b.reply("Stopped looking.")
	case "sneak":
		if len(p) > 2 {
			b.reply("Usage: sneak [on|off|toggle]")
			return
		}
		switch {
		case len(p) == 1, strings.EqualFold(p[1], "toggle"):
			b.sneaking = !b.sneaking
		case strings.EqualFold(p[1], "on"), strings.EqualFold(p[1], "true"):
			b.sneaking = true
		case strings.EqualFold(p[1], "off"), strings.EqualFold(p[1], "false"):
			b.sneaking = false
		default:
			b.reply("Usage: sneak [on|off|toggle]")
			return
		}
		b.reply(fmt.Sprintf("Sneaking %v.", b.sneaking))
	case "unsneak":
		b.sneaking = false
		b.reply("Sneaking disabled.")
	case "headshake", "shake":
		if len(p) >= 2 && strings.EqualFold(p[1], "clear") {
			b.headshake = headshakeNone
			b.headshakeTicks = 0
			b.reply("Headshake cleared.")
			return
		}
		if len(p) < 2 || len(p) > 3 {
			b.reply("Usage: headshake yes|no [repeat:true|false] | headshake clear")
			return
		}
		var mode headshakeMode
		switch strings.ToLower(p[1]) {
		case "yes", "y", "nod":
			mode = headshakeYes
		case "no", "n":
			mode = headshakeNo
		default:
			b.reply("Usage: headshake yes|no [repeat:true|false] | headshake clear")
			return
		}
		repeat := false
		if len(p) == 3 {
			repeat = strings.EqualFold(p[2], "true") || strings.EqualFold(p[2], "yes") || p[2] == "1"
		}
		b.headshake = mode
		b.headshakeRepeat = repeat
		b.headshakeTicks = 0
		b.reply(fmt.Sprintf("Headshaking %s (repeat=%v).", p[1], repeat))
	case "emote":
		if len(p) >= 2 && strings.EqualFold(p[1], "clear") {
			b.emoteID = ""
			b.emoteRepeat = false
			b.reply("Emote stopped.")
			return
		}
		if len(p) < 2 || len(p) > 4 {
			b.reply("Usage: emote <uuid> [repeat:true|false] [length] | emote clear")
			return
		}
		emoteUUID := strings.ToLower(p[1])
		if _, err := uuid.Parse(emoteUUID); err != nil {
			b.reply(fmt.Sprintf("Invalid emote uuid: %s (use a UUID from the Bedrock-Emotes list)", p[1]))
			return
		}
		repeat := false
		var length uint32 = defaultEmoteLength
		for _, arg := range p[2:] {
			switch {
			case strings.EqualFold(arg, "true") || strings.EqualFold(arg, "yes") || arg == "1":
				repeat = true
			case strings.EqualFold(arg, "false") || strings.EqualFold(arg, "no") || arg == "0":
				repeat = false
			default:
				n, err := strconv.Atoi(arg)
				if err != nil || n < 1 || n > 7200 {
					b.reply(fmt.Sprintf("Invalid length: %s (expected ticks 1-7200)", arg))
					return
				}
				length = uint32(n)
			}
		}
		if b.conn != nil {
			_ = b.conn.WritePacket(&packet.Emote{EntityRuntimeID: b.entityID, EmoteID: emoteUUID, EmoteLength: length, Flags: 0})
		}
		b.emoteRepeat = repeat
		if repeat {
			b.emoteID = emoteUUID
			b.emoteLength = length
			b.emoteNextTick = b.tick + uint64(length)
			b.reply(fmt.Sprintf("Emote %s repeating (length=%d).", emoteUUID, length))
		} else {
			b.emoteID = ""
			b.reply(fmt.Sprintf("Emote %s sent (length=%d).", emoteUUID, length))
		}
	case "skin":
		b.skinCmd(p[1:])
	case "nether":
		b.netherCmd(p[1:])
	case "renderdistance":
		if len(p) < 2 {
			b.reply("Usage: renderdistance <chunks>")
			return
		}
		dist, err := strconv.Atoi(p[1])
		if err != nil || dist < 1 {
			b.reply("Invalid render distance.")
			return
		}
		b.mu.Lock()
		b.cfg.Behaviour.RenderDistance = int32(dist)
		c := b.conn
		b.mu.Unlock()
		if c != nil {
			_ = c.WritePacket(&packet.RequestChunkRadius{ChunkRadius: int32(dist), MaxChunkRadius: uint8(dist)})
			b.reply(fmt.Sprintf("Requested chunk radius update to %d", dist))
		}
	case "sleep":
		var x, y, z int32
		if len(p) >= 4 {
			fmt.Sscanf(strings.Join(p[1:4], " "), "%d %d %d", &x, &y, &z)
		} else {
			x, y, z = int32(math.Floor(float64(b.pos.X()))), int32(math.Floor(float64(b.pos.Y()))), int32(math.Floor(float64(b.pos.Z())))
		}
		b.spawnPos = protocol.BlockPos{x, y, z}
		b.interactBlock(b.spawnPos)
		log.Printf("bebot: sleep/spawn checkpoint requested at %d %d %d\n", x, y, z)
	case "break", "breakat", "breakcoord", "mine", "mineat", "minearea", "minespace", "breakarea", "breakspace", "mineloop", "minefarm", "minetimeout", "learn", "learnair", "calibrate", "minelearn", "minecalibrate":
		if strings.EqualFold(p[0], "learn") || strings.EqualFold(p[0], "learnair") || strings.EqualFold(p[0], "calibrate") ||
			strings.EqualFold(p[0], "minelearn") || strings.EqualFold(p[0], "minecalibrate") ||
			(len(p) >= 2 && (strings.EqualFold(p[1], "learn") || strings.EqualFold(p[1], "calibrate"))) {
			var target *protocol.BlockPos
			startIdx := 2
			if strings.EqualFold(p[0], "learn") || strings.EqualFold(p[0], "learnair") || strings.EqualFold(p[0], "calibrate") ||
				strings.EqualFold(p[0], "minelearn") || strings.EqualFold(p[0], "minecalibrate") {
				startIdx = 1
				if len(p) >= 2 && strings.EqualFold(p[1], "air") {
					startIdx = 2
				}
			}
			if len(p) >= startIdx+3 {
				var x, y, z int32
				if _, err := fmt.Sscanf(strings.Join(p[startIdx:startIdx+3], " "), "%d %d %d", &x, &y, &z); err == nil {
					target = &protocol.BlockPos{x, y, z}
				}
			}
			b.startLearningAir(target)
			return
		}
		if len(p) == 2 && strings.EqualFold(p[1], "air") {
			b.mu.Lock()
			var ids []uint32
			for id := range b.mineAirRuntimeIDs {
				ids = append(ids, id)
			}
			b.mu.Unlock()
			b.reply(fmt.Sprintf("Known air runtime IDs: %v", ids))
			return
		}
		if strings.EqualFold(p[0], "minetimeout") || (len(p) >= 2 && strings.EqualFold(p[1], "timeout")) {
			argIdx := 2
			if strings.EqualFold(p[0], "minetimeout") {
				argIdx = 1
			}
			if len(p) <= argIdx {
				b.mu.Lock()
				to := b.mineTimeoutSec
				if to <= 0 {
					to = 3.0
				}
				b.mu.Unlock()
				b.reply(fmt.Sprintf("Current mine timeout: %.1fs. Usage: mine timeout <seconds>", to))
				return
			}
			sec, err := strconv.ParseFloat(p[argIdx], 64)
			if err != nil || sec <= 0 {
				b.reply("Usage: mine timeout <seconds> (e.g. mine timeout 3.0)")
				return
			}
			b.mu.Lock()
			b.mineTimeoutSec = sec
			b.cfg.Mine.TimeoutSec = sec
			b.mu.Unlock()
			_ = b.saveConfig()
			b.reply(fmt.Sprintf("Mine timeout set to %.1fs", sec))
			return
		}
		repeat := strings.EqualFold(p[0], "mineloop") || strings.EqualFold(p[0], "minefarm")
		if len(p) >= 7 && len(p) <= 11 {
			var ax, ay, az, bx, by, bz int32
			if _, err := fmt.Sscanf(strings.Join(p[1:7], " "), "%d %d %d %d %d %d", &ax, &ay, &az, &bx, &by, &bz); err != nil {
				b.reply("Usage: mine <ax> <ay> <az> <bx> <by> <bz> [repeat: true|false] [faceAccurate: true|false]")
				return
			}
			ticks := 0
			faceAccurate := false
			boolIdx := 0
			for _, rawArg := range p[7:] {
				arg := strings.ToLower(rawArg)
				if arg == "repeat" || arg == "loop" || arg == "repeat:true" || arg == "repeat=true" {
					repeat = true
				} else if arg == "repeat:false" || arg == "repeat=false" || arg == "norepeat" || arg == "once" {
					repeat = false
				} else if arg == "face" || arg == "faceaccurate" || arg == "accurate" || arg == "face:true" || arg == "face=true" {
					faceAccurate = true
				} else if arg == "face:false" || arg == "face=false" {
					faceAccurate = false
				} else if bVal, err := strconv.ParseBool(arg); err == nil {
					if boolIdx == 0 && !strings.EqualFold(p[0], "mineloop") && !strings.EqualFold(p[0], "minefarm") {
						repeat = bVal
						boolIdx++
					} else {
						faceAccurate = bVal
						boolIdx++
					}
				} else if t, err := strconv.Atoi(arg); err == nil && t >= 0 {
					ticks = t
				}
			}
			b.startMiningArea(ax, ay, az, bx, by, bz, ticks, repeat, faceAccurate)
			return
		}
		if len(p) < 5 || len(p) > 6 {
			b.reply("Usage: break <x> <y> <z> <animation: true|false> [repeat: true|false] OR mine <ax> <ay> <az> <bx> <by> <bz> [repeat: true|false] [faceAccurate: true|false]")
			return
		}
		var x, y, z int32
		if _, err := fmt.Sscanf(strings.Join(p[1:4], " "), "%d %d %d", &x, &y, &z); err != nil {
			b.reply("Usage: break <x> <y> <z> <animation: true|false> [repeat: true|false]")
			return
		}
		animate, err := strconv.ParseBool(p[4])
		if err != nil {
			b.reply("Usage: break <x> <y> <z> <animation: true|false> [repeat: true|false]")
			return
		}
		repeat = false
		if len(p) == 6 {
			repeat, err = strconv.ParseBool(p[5])
			if err != nil {
				b.reply("Usage: break <x> <y> <z> <animation: true|false> [repeat: true|false]")
				return
			}
		}
		b.startBreaking(protocol.BlockPos{x, y, z}, animate, repeat)
	case "stopbreak", "cancelbreak", "abortbreak", "stopmine", "cancelmine":
		b.stopMiningArea()
		b.stopBreaking()
	case "dropall":
		b.dropSlots(0, len(b.inventory), 0)
	case "transfer":
		b.transferCmd(p)
	case "drop":
		if len(p) < 2 || len(p) > 3 {
			b.reply("Usage: drop <slot> [amount]")
			return
		}
		slot, err := strconv.Atoi(p[1])
		if err != nil || slot < 0 || slot >= len(b.inventory) {
			b.reply("Usage: drop <slot> [amount]")
			return
		}
		amount := 0
		if len(p) == 3 {
			amount, err = strconv.Atoi(p[2])
			if err != nil || amount <= 0 {
				b.reply("Usage: drop <slot> [amount]")
				return
			}
		}
		b.dropSlots(slot, 1, amount)
	case "help", "commands", "commandlist":
		b.help()
	case "hotbar", "slot":
		if len(p) != 2 {
			b.reply("Usage: hotbar <0-9>")
			return
		}
		slot, err := strconv.Atoi(p[1])
		if err != nil || slot < 0 || slot > 9 {
			b.reply("Usage: hotbar <0-9>")
			return
		}
		b.selectHotbar(byte(slot))
	case "count":
		if len(p) < 2 {
			b.reply("Usage: count <beds|villagers|golems>")
			return
		}
		arg := strings.ToLower(p[1])
		switch arg {
		case "beds":
			log.Println("bebot: count beds is limited to decoded chunks; gophertunnel is receiving chunk packets")
		case "villagers", "villager":
			b.mu.Lock()
			count := 0
			for _, entityType := range b.entities {
				if entityType == "minecraft:villager" || entityType == "minecraft:villager_v2" {
					count++
				}
			}
			b.mu.Unlock()
			b.reply(fmt.Sprintf("There are %d villagers nearby.", count))
		case "golems", "golem", "irongolem", "irongolems":
			b.mu.Lock()
			count := 0
			for _, entityType := range b.entities {
				if entityType == "minecraft:iron_golem" {
					count++
				}
			}
			b.mu.Unlock()
			b.reply(fmt.Sprintf("There are %d iron golems nearby.", count))
		default:
			b.reply("Usage: count <beds|villagers|golems>")
		}
	case "online", "players", "list":
		b.online()
	case "nearby", "near":
		b.nearby()
	case "coords", "coord", "coordinates", "playercoords", "playerpos", "pos", "positions", "where":
		b.coords()
	case "idle", "stop":
		b.stopMiningArea()
		b.stopBreaking()
		b.mode = modeIdle
		b.followTarget = ""
		b.mimicTarget = ""
		b.lookAtTarget = ""
		b.lookAtPos = nil
		b.velocityY = 0
		if len(p) > 1 && strings.EqualFold(p[1], "reset") {
			b.idleYaw = 0
			b.idlePitch = 0
			b.reply("bebot: idle (reset).")
		} else {
			b.idleYaw = b.facingYaw
			b.idlePitch = b.facingPitch
			b.reply("bebot: idle.")
		}
	case "waypoint", "waypoints", "wp":
		b.waypointCmd(p[1:])
	case "move":
		if len(p) < 4 {
			b.reply("Usage: move <x> <y> <z>")
			return
		}
		if _, err := fmt.Sscanf(strings.Join(p[1:4], " "), "%f %f %f", &b.cfg.Target.X, &b.cfg.Target.Y, &b.cfg.Target.Z); err != nil {
			b.reply("Usage: move <x> <y> <z>")
			return
		}
		b.mode = modeMoving
		b.followTarget = ""
		b.mimicTarget = ""
		b.lookAtTarget = ""
		b.lookAtPos = nil
		b.reply(fmt.Sprintf("Moving to (%.1f, %.1f, %.1f)…", b.cfg.Target.X, b.cfg.Target.Y, b.cfg.Target.Z))
	case "rejoin", "reconnect":
		b.reply("Reconnecting...")
		b.Close()
	case "quit", "exit":
		b.reply("Disconnecting. Bye!")
		b.quitting = true
		b.Close()
	default:
		b.reply("Unknown command: " + p[0])
	}
}

func (b *Bot) netherCmd(args []string) {
	var x, y, z float32
	switch len(args) {
	case 2:
		if _, err := fmt.Sscanf(strings.Join(args[0:2], " "), "%f %f", &x, &z); err != nil {
			b.reply("Usage: nether <x z> or nether <x y z>")
			return
		}
		b.reply(fmt.Sprintf("Nether: (%.1f, %.1f)", x/8, z/8))
	case 3:
		if _, err := fmt.Sscanf(strings.Join(args[0:3], " "), "%f %f %f", &x, &y, &z); err != nil {
			b.reply("Usage: nether <x z> or nether <x y z>")
			return
		}
		b.reply(fmt.Sprintf("Nether: (%.1f, %.1f, %.1f)", x/8, y, z/8))
	default:
		b.reply("Usage: nether <x z> or nether <x y z>")
	}
}

func (b *Bot) help() {
	lines := []string{
		"Commands:",
		"move <x y z> - move to coordinates",
		"follow <player> - follow a player",
		"mimic <player> - mimic movement and actions",
		"camic <player> - mimic camera rotation only",
		"lookat [player] or lookat <x y z> - look at a player/point",
		"stoplookat - stop looking at a player",
		"sneak [on|off|toggle] - toggle or set sneaking",
		"unsneak - stop sneaking",
		"headshake yes|no [repeat:true|false] - shake the head (yes=up/down, no=left/right)",
		"headshake clear - stop headshaking",
		"emote <uuid> [repeat:true|false] [length] - send an emote (UUID from Bedrock-Emotes list); length = duration in ticks, default 60",
		"emote clear - stop repeating emote",
		"skin <slim|normal|wide> <path> [reload] - change to a classic skin PNG",
		"skin pack <folder> <skin_id> [reload] - change to a skin from a skin pack (skins.json)",
		"skin pack <folder> list - list skins in a skin pack",
		"skin default [reload] - revert to default slim skin",
		"skin toggle override - toggle whether skin changes override other players' view (default: on)",
		"idle / stop - stop movement (keeps current facing)",
		"idle reset - idle and reset facing to default",
		"break <x y z> <animation: true|false> - break a block",
		"mine <ax ay az> <bx by bz> [repeat: true|false] [faceAccurate: true|false] - mine all blocks in a space (top-down)",
		"mine timeout <seconds> - set per-block break timeout (default: 3.0s)",
		"mine learn [x y z] - calibrate and learn air block runtime ID from broken block",
		"mine air - display currently known air runtime IDs",
		"stopmine / stopbreak - stop mining",
		"hotbar <0-9> - select a hotbar slot",
		"dropall - drop all inventory items",
		"drop <slot> [amount] - drop items from a slot",
		"transfer <slot a> <slot b> - move items between inventory slots",
		"sleep [x y z] - set a sleep/spawn checkpoint",
		"players / list - list online players",
		"nearby - list nearby players",
		"coords - show coordinates",
		"waypoint help - waypoint add/remove/update/list/show",
		"say <message> - send chat",
		"payback [on|off] - toggle payback mode (attacks back if hurt)",
		"autofish [on|off] - toggle auto-fishing",
		"kill <target> <cps> - rapidly attack a player until they die",
		"stopkill - stop the current kill command",
		"quit / exit - disconnect",
	}
	for _, line := range lines {
		b.reply(line)
	}
}
