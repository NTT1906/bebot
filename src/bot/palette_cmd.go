package bot

import (
	"fmt"
	"strings"
)

// paletteCmd handles "bebot palette [load|unload|status]".
func (b *Bot) paletteCmd(args []string) {
	if len(args) == 0 {
		b.paletteStatus()
		return
	}

	action := strings.ToLower(args[0])
	switch action {
	case "load", "enable", "on", "start":
		count, version, err := b.LoadPalette()
		if err != nil {
			b.reply(fmt.Sprintf("[BlockPalette] Failed to load palette: %v", err))
			return
		}
		if version != "" {
			b.reply(fmt.Sprintf("[BlockPalette] Loaded block palette (%d states active, Bedrock v%s, air RuntimeID: %d).",
				count, version, b.ActualAirRID()))
		} else {
			b.reply(fmt.Sprintf("[BlockPalette] Loaded block palette (%d states active, air RuntimeID: %d).",
				count, b.ActualAirRID()))
		}

	case "unload", "disable", "off", "clear", "stop":
		b.UnloadPalette()
		b.reply(fmt.Sprintf("[BlockPalette] Unloaded palette data (air RuntimeID %d preserved). Memory freed.", b.ActualAirRID()))

	case "status", "info":
		b.paletteStatus()

	default:
		b.reply("Usage: palette load | unload | status")
	}
}

func (b *Bot) paletteStatus() {
	if b.blockRegistry == nil || !b.blockRegistry.IsLoaded() {
		b.reply(fmt.Sprintf("[BlockPalette] Status: UNLOADED (air RuntimeID: %d preserved). Run 'bebot palette load' to enable.", b.ActualAirRID()))
		return
	}

	count := b.blockRegistry.Count()
	version := b.blockRegistry.Version()
	b.mu.Lock()
	useHashes := b.serverUseHashes
	b.mu.Unlock()

	if version != "" {
		b.reply(fmt.Sprintf("[BlockPalette] Status: LOADED (%d block states active, Bedrock v%s, serverUseHashes=%v, air RuntimeID: %d).",
			count, version, useHashes, b.ActualAirRID()))
	} else {
		b.reply(fmt.Sprintf("[BlockPalette] Status: LOADED (%d block states active, serverUseHashes=%v, air RuntimeID: %d).",
			count, useHashes, b.ActualAirRID()))
	}
}
