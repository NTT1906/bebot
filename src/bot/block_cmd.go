package bot

import (
	"fmt"
	"strconv"
	"strings"
)

// blockCmd handles "bebot block [lookup|inspect|count|load|unload|status] <args...>".
func (b *Bot) blockCmd(args []string) {
	if len(args) == 0 {
		b.reply("Usage: block lookup <name|id> | block inspect <x> <y> <z> | block count | block load | block unload | block status")
		return
	}

	action := strings.ToLower(args[0])
	switch action {
	case "load", "enable", "on":
		b.paletteCmd([]string{"load"})
	case "unload", "disable", "off", "clear":
		b.paletteCmd([]string{"unload"})
	case "palette":
		b.paletteCmd(args[1:])
	case "status", "info":
		b.paletteStatus()
	case "lookup", "find", "get", "id", "name":
		if len(args) < 2 {
			b.reply("Usage: block lookup <name|id>")
			return
		}
		if b.blockRegistry == nil || !b.blockRegistry.IsLoaded() {
			query := strings.ToLower(args[1])
			if query == "air" || query == "minecraft:air" || query == fmt.Sprintf("%d", b.ActualAirRID()) || query == "0" {
				b.reply(fmt.Sprintf("[Block] minecraft:air -> RuntimeID: %d (palette unloaded, air preserved)", b.ActualAirRID()))
				return
			}
			b.reply("[BlockPalette] Palette data is not loaded. Run 'bebot palette load' first.")
			return
		}
		query := args[1]
		// Check if query is a numeric runtime ID
		if rid, err := strconv.ParseUint(query, 10, 32); err == nil {
			if name, ok := b.blockRegistry.ResolveName(uint32(rid)); ok {
				b.reply(fmt.Sprintf("[Block] RuntimeID %d -> %s", rid, name))
			} else {
				b.reply(fmt.Sprintf("[Block] RuntimeID %d is unknown (not in local palette dictionary).", rid))
			}
			return
		}

		// Otherwise query is a block name
		if rid, ok := b.blockRegistry.ResolveRID(query); ok {
			b.reply(fmt.Sprintf("[Block] %s -> RuntimeID: %d", query, rid))
		} else {
			b.reply(fmt.Sprintf("[Block] Could not resolve runtime ID for %s.", query))
		}

	case "inspect", "probe":
		b.inspectCmd(args[1:])

	case "count", "stats":
		if b.blockRegistry == nil || !b.blockRegistry.IsLoaded() {
			b.reply(fmt.Sprintf("[BlockPalette] Palette data is not loaded (Status: UNLOADED, air RuntimeID: %d). Run 'bebot palette load' first.", b.ActualAirRID()))
			return
		}
		count := b.blockRegistry.Count()
		version := b.blockRegistry.Version()
		b.mu.Lock()
		useHashes := b.serverUseHashes
		b.mu.Unlock()
		if version != "" {
			b.reply(fmt.Sprintf("[Block] %d block states registered (Bedrock v%s, serverUseHashes=%v).", count, version, useHashes))
		} else {
			b.reply(fmt.Sprintf("[Block] %d block states registered (serverUseHashes=%v).", count, useHashes))
		}

	default:
		b.reply("Usage: block lookup <name|id> | block inspect <x> <y> <z> | block count | block load | block unload | block status")
	}
}
