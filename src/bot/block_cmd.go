package bot

import (
	"fmt"
	"strconv"
	"strings"
)

// blockCmd handles "bebot block [lookup|inspect|count] <args...>".
func (b *Bot) blockCmd(args []string) {
	if len(args) == 0 {
		b.reply("Usage: block lookup <name|id> | block inspect <x> <y> <z> | block count")
		return
	}

	action := strings.ToLower(args[0])
	switch action {
	case "lookup", "find", "get", "id", "name":
		if len(args) < 2 {
			b.reply("Usage: block lookup <name|id>")
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
		b.reply("Usage: block lookup <name|id> | block inspect <x> <y> <z> | block count")
	}
}
