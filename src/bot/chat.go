package bot

import (
	"fmt"
	"log"
	"math"
	"sort"
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
		_ = b.conn.Flush()
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
	go func() {
		b.mu.Lock()
		b.fromConsole = false
		b.whisperTarget = ""
		b.mu.Unlock()
		b.command(text)
	}()
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
	if !strings.ContainsRune(name, '§') {
		return strings.TrimSpace(name)
	}
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
	lines := strings.Split(message, "\n")
	for i, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		log.Println("bebot:", line)

		b.mu.Lock()
		target := b.whisperTarget
		fromConsole := b.fromConsole
		b.mu.Unlock()

		if target != "" {
			b.whisper(target, line)
		} else if !fromConsole {
			b.say(line)
		}

		if len(lines) > 1 && i < len(lines)-1 {
			time.Sleep(60 * time.Millisecond)
		}
	}
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
		b.handleSleepCmd(p)
	case "wake", "wakeup", "unsleep":
		b.wakeUp()
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
		if len(p) >= 2 && strings.EqualFold(p[1], "air") {
			if len(p) >= 3 && (strings.EqualFold(p[2], "clear") || strings.EqualFold(p[2], "reset")) {
				b.mu.Lock()
				b.mineAirRuntimeIDs = defaultAirRuntimeIDs()
				b.mu.Unlock()
				b.reply("Reset air block runtime IDs to defaults (including minecraft:air = 3690217760).")
				return
			}
			if len(p) >= 3 && (strings.EqualFold(p[2], "remove") || strings.EqualFold(p[2], "rm") || strings.EqualFold(p[2], "del") || strings.EqualFold(p[2], "delete")) {
				if len(p) >= 4 {
					if id, err := strconv.ParseUint(p[3], 10, 32); err == nil {
						b.mu.Lock()
						if b.mineAirRuntimeIDs != nil {
							delete(b.mineAirRuntimeIDs, uint32(id))
						}
						b.mu.Unlock()
						b.reply(fmt.Sprintf("Removed %d from known air runtime IDs.", id))
						return
					}
				}
				b.reply("Usage: mine air remove <runtimeID>")
				return
			}
			if len(p) >= 3 && (strings.EqualFold(p[2], "add") || strings.EqualFold(p[2], "set")) {
				if len(p) >= 4 {
					if id, err := strconv.ParseUint(p[3], 10, 32); err == nil {
						b.mu.Lock()
						if b.mineAirRuntimeIDs == nil {
							b.mineAirRuntimeIDs = make(map[uint32]bool)
						}
						b.mineAirRuntimeIDs[uint32(id)] = true
						b.mu.Unlock()
						b.reply(fmt.Sprintf("Added %d to known air runtime IDs.", id))
						return
					}
				}
				b.reply("Usage: mine air add <runtimeID>")
				return
			}
			if len(p) >= 5 {
				var x, y, z int32
				if _, err := fmt.Sscanf(strings.Join(p[2:5], " "), "%d %d %d", &x, &y, &z); err == nil {
					target := protocol.BlockPos{x, y, z}
					b.mu.Lock()
					rid, known := b.blockRuntimeIDs[target]
					isAir := b.mineAirRuntimeIDs != nil && b.mineAirRuntimeIDs[rid]
					b.mu.Unlock()
					if known {
						statusStr := "solid"
						if isAir || rid == 0 {
							statusStr = "air"
						}
						b.reply(fmt.Sprintf("Block at (%d, %d, %d) has cached runtime ID %d (%s). To break & calibrate air, use: mine learn %d %d %d",
							x, y, z, rid, statusStr, x, y, z))
					} else {
						b.reply(fmt.Sprintf("Block at (%d, %d, %d) has no cached runtime ID. To break & calibrate air, use: mine learn %d %d %d",
							x, y, z, x, y, z))
					}
					return
				}
			}
			b.mu.Lock()
			var ids []uint32
			for id := range b.mineAirRuntimeIDs {
				ids = append(ids, id)
			}
			b.mu.Unlock()
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			b.reply(fmt.Sprintf("Known air runtime IDs: %v (Use 'mine learn <x y z>' to break a block & calibrate air)", ids))
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
	case "droploop", "loopdrop":
		b.dropLoopCmd(p)
	case "stopdroploop", "stoploopdrop", "stopdrop":
		if b.stopDropLoop() {
			b.reply("Drop loop stopped.")
		} else {
			b.reply("Drop loop was not running.")
		}
	case "dropall", "dropall2", "dropall3":
		method := DropMethodFirstPerson
		if strings.EqualFold(p[0], "dropall2") {
			method = DropMethodCursor
		} else if strings.EqualFold(p[0], "dropall3") {
			method = DropMethodDirectStack
		}
		random := false
		each := false
		for _, arg := range p[1:] {
			argLower := strings.ToLower(arg)
			if bVal, err := strconv.ParseBool(argLower); err == nil {
				random = bVal
			} else if argLower == "random" {
				random = true
			} else if argLower == "each" || argLower == "seq" || argLower == "sequence" || argLower == "sequential" || argLower == "1by1" {
				each = true
			} else if argLower == "all" || argLower == "batch" || argLower == "instant" {
				each = false
			}
		}
		b.dropSlots(0, len(b.inventory), 0, method, random, each)
	case "inv", "inventory", "myinv", "items":
		if len(p) >= 2 && (strings.EqualFold(p[1], "sort") || strings.EqualFold(p[1], "compact")) {
			b.sortInventoryCmd(p)
			return
		}
		b.inventoryCmd(p)
	case "sort-inv", "sortinv", "sort", "sort-inventory":
		b.sortInventoryCmd(p)
	case "transfer":
		b.transferCmd(p)
	case "loot", "steal":
		b.lootCmd(p)
	case "take":
		b.takeCmd(p)
	case "container", "chest", "viewchest", "viewcontainer":
		b.viewContainerCmd(p)
	case "open", "opencontainer":
		b.openContainerCmd(p)
	case "close", "closecontainer":
		b.closeContainerCmd(p)
	case "bundletest":
		b.bundleTestCmd(p)
	case "drop", "drop2", "drop3":
		method := DropMethodFirstPerson
		if strings.EqualFold(p[0], "drop2") {
			method = DropMethodCursor
		} else if strings.EqualFold(p[0], "drop3") {
			method = DropMethodDirectStack
		}
		if len(p) < 2 || len(p) > 4 {
			b.reply("Usage: drop <slot> [amount] [random: true|false] (or drop2 for cursor, drop3 for stack request)")
			return
		}
		slot, err := strconv.Atoi(p[1])
		if err != nil || slot < 0 || slot >= len(b.inventory) {
			b.reply("Usage: drop <slot> [amount] [random: true|false]")
			return
		}
		amount := 0
		random := false
		for _, arg := range p[2:] {
			if a, err := strconv.Atoi(arg); err == nil && a > 0 {
				amount = a
			} else if bVal, err := strconv.ParseBool(arg); err == nil {
				random = bVal
			} else if strings.EqualFold(arg, "random") {
				random = true
			}
		}
		b.dropSlots(slot, 1, amount, method, random, false)
	case "help", "commands", "commandlist":
		b.helpCmd(p)
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

func (b *Bot) handleSleepCmd(p []string) {
	if len(p) >= 2 && (strings.EqualFold(p[1], "wake") || strings.EqualFold(p[1], "stop") || strings.EqualFold(p[1], "up") || strings.EqualFold(p[1], "leave")) {
		b.wakeUp()
		return
	}
	if len(p) >= 4 {
		var x, y, z int32
		if _, err := fmt.Sscanf(strings.Join(p[1:4], " "), "%d %d %d", &x, &y, &z); err == nil {
			go b.interactBed(protocol.BlockPos{x, y, z})
			return
		}
	}
	// Fallback to the block directly in front of the bot
	b.mu.Lock()
	yaw := b.facingYaw
	botPos := b.pos
	b.mu.Unlock()
	rad := float64(yaw) * math.Pi / 180
	frontX := int32(math.Floor(float64(botPos.X() - float32(math.Sin(rad))*1.2)))
	frontY := int32(math.Floor(float64(botPos.Y())))
	frontZ := int32(math.Floor(float64(botPos.Z() + float32(math.Cos(rad))*1.2)))
	go b.interactBed(protocol.BlockPos{frontX, frontY, frontZ})
}
