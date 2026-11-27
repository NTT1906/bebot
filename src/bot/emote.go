package bot

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// DefaultEmotesFile is the default filename used for persisting emote bookmarks.
const DefaultEmotesFile = "data/emotes.json"

// loadEmotes reads emote bookmarks from a JSON file.
func loadEmotes(path string) map[string]string {
	targetPath := path
	data, err := os.ReadFile(targetPath)
	if err != nil && (targetPath == DefaultEmotesFile || targetPath == filepath.Clean(DefaultEmotesFile)) {
		if fallbackData, fallbackErr := os.ReadFile("emotes.json"); fallbackErr == nil {
			data = fallbackData
			err = nil
			targetPath = "emotes.json"
		}
	}
	if err != nil {
		// Provide starter default bookmarks if file does not exist
		emotes := map[string]string{
			"wave":  "c1819d2b-c8c3-4d69-b5f7-66a7b370605a",
			"clap":  "408101ae-58fa-4a6c-9400-d85feeb7dd99",
			"cheer": "4f415fb5-0985-452a-9293-85f0e1f7a68a",
		}
		_ = saveEmotes(path, emotes)
		return emotes
	}
	var emotes map[string]string
	if err := json.Unmarshal(data, &emotes); err != nil {
		log.Println("bebot: ignoring invalid emotes file:", err)
		return make(map[string]string)
	}
	if emotes == nil {
		emotes = make(map[string]string)
	}
	return emotes
}

// saveEmotes writes emote bookmarks to a JSON file.
func saveEmotes(path string, emotes map[string]string) error {
	data, err := json.MarshalIndent(emotes, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
	return os.WriteFile(path, data, 0644)
}

func (b *Bot) emoteCmd(args []string) {
	if len(args) == 0 {
		b.emoteHelp()
		return
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "help":
		b.emoteHelp()
	case "list", "ls":
		b.emoteList()
	case "add":
		b.emoteAdd(args[1:])
	case "remove", "delete", "del", "rm":
		b.emoteRemove(args[1:])
	case "clear", "stop":
		b.mu.Lock()
		b.emoteID = ""
		b.emoteRepeat = false
		b.mu.Unlock()
		b.reply("Emote stopped.")
	default:
		// Play emote by bookmark name or raw UUID
		b.emotePlay(args)
	}
}

func (b *Bot) emoteAdd(args []string) {
	if len(args) < 2 {
		b.reply("Usage: emote add <name> <uuid>")
		return
	}
	name := strings.TrimSpace(args[0])
	if name == "" {
		b.reply("Usage: emote add <name> <uuid>")
		return
	}
	rawUUID := strings.ToLower(strings.TrimSpace(args[1]))
	parsed, err := uuid.Parse(rawUUID)
	if err != nil {
		b.reply(fmt.Sprintf("Invalid emote UUID: %s", rawUUID))
		return
	}

	b.mu.Lock()
	if b.emotes == nil {
		b.emotes = make(map[string]string)
	}
	key := strings.ToLower(name)
	b.emotes[key] = parsed.String()
	snapshot := make(map[string]string, len(b.emotes))
	for k, v := range b.emotes {
		snapshot[k] = v
	}
	b.mu.Unlock()

	if err := saveEmotes(DefaultEmotesFile, snapshot); err != nil {
		log.Println("bebot: failed to save emotes:", err)
	}
	b.reply(fmt.Sprintf("Saved emote bookmark %q -> %s", name, parsed.String()))
}

func (b *Bot) emoteRemove(args []string) {
	if len(args) < 1 {
		b.reply("Usage: emote remove <name>")
		return
	}
	name := strings.TrimSpace(args[0])
	key := strings.ToLower(name)

	b.mu.Lock()
	if b.emotes == nil {
		b.mu.Unlock()
		b.reply(fmt.Sprintf("Emote bookmark %q not found.", name))
		return
	}
	if _, ok := b.emotes[key]; !ok {
		b.mu.Unlock()
		b.reply(fmt.Sprintf("Emote bookmark %q not found.", name))
		return
	}
	delete(b.emotes, key)
	snapshot := make(map[string]string, len(b.emotes))
	for k, v := range b.emotes {
		snapshot[k] = v
	}
	b.mu.Unlock()

	if err := saveEmotes(DefaultEmotesFile, snapshot); err != nil {
		log.Println("bebot: failed to save emotes:", err)
	}
	b.reply(fmt.Sprintf("Removed emote bookmark %q.", name))
}

func (b *Bot) emoteList() {
	b.mu.Lock()
	if len(b.emotes) == 0 {
		b.mu.Unlock()
		b.reply("No emote bookmarks saved. Use: emote add <name> <uuid>")
		return
	}
	names := make([]string, 0, len(b.emotes))
	for name := range b.emotes {
		names = append(names, name)
	}
	sort.Strings(names)

	lines := make([]string, 0, len(names)+2)
	lines = append(lines, fmt.Sprintf("§6--- Emote Bookmarks (%d) ---§r", len(names)))
	for _, name := range names {
		lines = append(lines, fmt.Sprintf("- %q: %s", name, b.emotes[name]))
	}
	lines = append(lines, "§7(Play with: 'bebot emote <name>' or 'bebot emote <uuid>')§r")
	b.mu.Unlock()

	b.reply(strings.Join(lines, "\n"))
}

func (b *Bot) emotePlay(args []string) {
	target := strings.TrimSpace(args[0])
	targetLower := strings.ToLower(target)

	b.mu.Lock()
	bookmarkedUUID, isBookmark := b.emotes[targetLower]
	b.mu.Unlock()

	var emoteUUID string
	if isBookmark {
		emoteUUID = bookmarkedUUID
	} else if parsed, err := uuid.Parse(targetLower); err == nil {
		emoteUUID = parsed.String()
	} else {
		b.reply(fmt.Sprintf("Unknown emote %q. Use a valid UUID or bookmark with 'emote add %s <uuid>'. Type 'emote list' to view bookmarks.", target, target))
		return
	}

	repeat := false
	var length uint32 = defaultEmoteLength
	for _, arg := range args[1:] {
		switch {
		case strings.EqualFold(arg, "true") || strings.EqualFold(arg, "yes") || arg == "1" || strings.EqualFold(arg, "repeat"):
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

	b.mu.Lock()
	conn := b.conn
	entityID := b.entityID
	tick := b.tick
	b.mu.Unlock()

	if conn != nil {
		_ = conn.WritePacket(&packet.Emote{
			EntityRuntimeID: entityID,
			EmoteID:         emoteUUID,
			EmoteLength:     length,
			Flags:           0,
		})
		_ = conn.Flush()
	}

	b.mu.Lock()
	b.emoteRepeat = repeat
	if repeat {
		b.emoteID = emoteUUID
		b.emoteLength = length
		b.emoteNextTick = tick + uint64(length)
	} else {
		b.emoteID = ""
	}
	b.mu.Unlock()

	displayName := emoteUUID
	if isBookmark {
		displayName = fmt.Sprintf("%q (%s)", target, emoteUUID)
	}
	if repeat {
		b.reply(fmt.Sprintf("Emote %s repeating (length=%d).", displayName, length))
	} else {
		b.reply(fmt.Sprintf("Emote %s sent (length=%d).", displayName, length))
	}
}

func (b *Bot) emoteHelp() {
	lines := []string{
		"§6--- Emote Commands ---§r",
		"emote <name|uuid> [repeat] [length] - play an emote",
		"emote add <name> <uuid> - bookmark an emote",
		"emote remove <name> - remove a bookmarked emote",
		"emote list - list all bookmarked emotes",
		"emote clear / emote stop - stop active/repeating emote",
		"§7Examples: emote wave | emote \"hello world\" | emote add \"dance\" <uuid>§r",
	}
	b.reply(strings.Join(lines, "\n"))
}
