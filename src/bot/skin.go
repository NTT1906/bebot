package bot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"bebot/src/config"
	bskin "bebot/src/skin"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// findSkinFile attempts to find a skin PNG file by checking exact path, .png extension,
// and candidate search directories (data/skins, skin, data).
func findSkinFile(query string) (string, bool) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return "", false
	}
	candidates := []string{
		trimmed,
		filepath.Join("data", "skins", trimmed),
		filepath.Join("skin", trimmed),
		filepath.Join("data", trimmed),
	}
	if !strings.HasSuffix(strings.ToLower(trimmed), ".png") {
		candidates = append(candidates,
			trimmed+".png",
			filepath.Join("data", "skins", trimmed+".png"),
			filepath.Join("skin", trimmed+".png"),
			filepath.Join("data", trimmed+".png"),
		)
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return filepath.ToSlash(c), true
		}
	}
	return trimmed, false
}

func (b *Bot) skinStatus() {
	b.mu.Lock()
	sc := b.cfg.Skin
	path := b.skinPath
	b.mu.Unlock()

	override := config.OverrideAppearanceOn(sc)
	b.reply(fmt.Sprintf("[Skin] Active: type=%s | path=%s | skin=%s | overrideAppearance=%v (save file: %s)",
		sc.Type, sc.Path, sc.Skin, override, path))
	b.reply("[Skin] Commands: skin use <name> [slim|normal] | skin list | skin <slim|normal|wide> <path> | skin default | skin toggle override")
}

func (b *Bot) skinList() {
	var found []string
	seen := make(map[string]bool)

	searchDirs := []string{
		filepath.Join("data", "skins"),
		"skin",
		"data",
	}
	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".png") {
				relPath := filepath.ToSlash(filepath.Join(dir, e.Name()))
				if !seen[relPath] {
					seen[relPath] = true
					found = append(found, relPath)
				}
			}
		}
	}

	if len(found) == 0 {
		b.reply("[Skin] No .png skin files found in skin/, data/skins/, or data/.")
		return
	}
	b.reply(fmt.Sprintf("[Skin] Available PNG skins (%d found):", len(found)))
	for _, f := range found {
		base := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		b.reply(fmt.Sprintf("  • %s  (use with: skin use %s)", f, base))
	}
}

func (b *Bot) skinCmd(args []string) {
	if len(args) == 0 {
		b.skinStatus()
		return
	}
	var err error
	sc := b.cfg.Skin
	switch strings.ToLower(args[0]) {
	case "status", "info":
		b.skinStatus()
		return
	case "list":
		b.skinList()
		return
	case "use", "set":
		if len(args) < 2 {
			b.reply("Usage: skin use <name_or_file> [slim|normal|wide] [reload]")
			return
		}
		resolvedPath, found := findSkinFile(args[1])
		if !found {
			b.reply(fmt.Sprintf("[Skin] Could not find skin file %q in skin/, data/skins/, or current folder.", args[1]))
			return
		}
		skinType := "slim"
		if len(args) >= 3 && (strings.EqualFold(args[2], "normal") || strings.EqualFold(args[2], "wide") || strings.EqualFold(args[2], "slim")) {
			skinType = strings.ToLower(args[2])
		} else if sc.Type != "" && sc.Type != "pack" && sc.Type != "custom" {
			skinType = sc.Type
		}
		armSize := uint8(protocol.ArmSizeSlim)
		if skinType == "normal" || skinType == "wide" {
			armSize = protocol.ArmSizeWide
		}
		_, err = bskin.BuildClassicSkin(resolvedPath, armSize)
		if err != nil {
			b.reply("[Skin] Build error: " + err.Error())
			return
		}
		sc.Type = skinType
		sc.Path = resolvedPath
		sc.Skin = ""
	case "toggle":
		if len(args) < 2 || !strings.EqualFold(args[1], "override") {
			b.reply("Usage: skin toggle override")
			return
		}
		on := !config.OverrideAppearanceOn(sc)
		sc.OverrideAppearance = &on
		b.cfg.Skin = sc
		if err := b.saveSkin(); err != nil {
			b.reply("skin toggle failed: " + err.Error())
			return
		}
		if on {
			b.reply("skin override appearance: ON (other players will see skin changes)")
		} else {
			b.reply("skin override appearance: OFF (other players will keep their cached skin)")
		}
		return
	case "default":
		sc = config.DefaultSkinConfig
		_, err = bskin.LoadSkinFromConfig(sc)
		if err != nil {
			b.reply("skin default failed: " + err.Error())
			return
		}
	case "slim", "normal", "wide":
		if len(args) < 2 {
			b.reply("Usage: skin <slim|normal|wide> <path> [reload]")
			return
		}
		resolvedPath, _ := findSkinFile(args[1])
		armSize := uint8(protocol.ArmSizeSlim)
		if strings.EqualFold(args[0], "normal") || strings.EqualFold(args[0], "wide") {
			armSize = protocol.ArmSizeWide
		}
		_, err = bskin.BuildClassicSkin(resolvedPath, armSize)
		if err != nil {
			b.reply("skin: " + err.Error())
			return
		}
		sc.Type = strings.ToLower(args[0])
		sc.Path = resolvedPath
		sc.Skin = ""
	case "pack", "custom":
		if len(args) < 3 {
			b.reply("Usage: skin pack <folder> <skin_id> [reload] | skin pack <folder> list")
			return
		}
		if strings.EqualFold(args[2], "list") {
			skins, err := bskin.ParseSkinPack(args[1])
			if err != nil {
				b.reply("skin: " + err.Error())
				return
			}
			if len(skins) == 0 {
				b.reply(fmt.Sprintf("No skins in %s.", args[1]))
				return
			}
			b.reply(fmt.Sprintf("Skins in %s (%d):", args[1], len(skins)))
			for i, s := range skins {
				b.reply(fmt.Sprintf("  [%d] %s  (%s)", i, s.LocalizationName, s.Geometry))
			}
			return
		}
		_, err = bskin.BuildPackSkin(args[1], args[2])
		if err != nil {
			b.reply("skin: " + err.Error())
			return
		}
		sc.Type = "pack"
		sc.Path = args[1]
		sc.Skin = args[2]
	default:
		b.reply("Usage: skin use <name> [slim|normal] | skin list | skin <slim|normal|wide> <path> [reload] | skin pack <folder> <skin_id> [reload] | skin pack <folder> list | skin default [reload] | skin toggle override")
		return
	}
	b.cfg.Skin = sc
	if err := b.saveSkin(); err != nil {
		b.reply("skin changed but skin save failed: " + err.Error())
		return
	}
	reload := isReloadArg(args)
	if reload {
		b.reply(fmt.Sprintf("skin changed to type=%s path=%s skin=%s (reloading)", sc.Type, sc.Path, sc.Skin))
		b.scheduleReconnect()
	} else {
		if err := b.sendSkin(sc); err != nil {
			b.reply(fmt.Sprintf("skin saved: type=%s path=%s skin=%s (apply failed: %v)", sc.Type, sc.Path, sc.Skin, err))
			return
		}
		b.reply(fmt.Sprintf("skin changed: type=%s path=%s skin=%s", sc.Type, sc.Path, sc.Skin))
	}
}

func (b *Bot) sendSkin(sc config.SkinConfig) error {
	b.mu.Lock()
	c := b.conn
	selfUUID := b.selfUUID
	b.mu.Unlock()
	if c == nil {
		return fmt.Errorf("not connected")
	}
	skin, err := bskin.LoadSkinFromConfig(sc)
	if err != nil {
		return err
	}
	skin.OverrideAppearance = config.OverrideAppearanceOn(sc)
	if err := c.WritePacket(&packet.PlayerSkin{
		UUID: selfUUID,
		Skin: skin,
	}); err != nil {
		return err
	}
	return c.Flush()
}

func isReloadArg(args []string) bool {
	if len(args) < 2 {
		return false
	}
	last := strings.ToLower(args[len(args)-1])
	return last == "true" || last == "yes" || last == "reload"
}

func (b *Bot) saveSkin() error {
	b.mu.Lock()
	sc := b.cfg.Skin
	path := b.skinPath
	if strings.EqualFold(filepath.Ext(path), ".png") {
		path = "data/skin.json"
		b.skinPath = path
	}
	b.mu.Unlock()
	return config.SaveSkin(path, sc)
}

func (b *Bot) saveConfig() error {
	b.mu.Lock()
	cfg := b.cfg
	path := b.configPath
	b.mu.Unlock()
	if path == "" {
		return nil
	}
	return config.SaveConfig(path, cfg)
}
