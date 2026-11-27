package bot

import (
	"fmt"
	"strings"

	"bebot/src/config"
	bskin "bebot/src/skin"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func (b *Bot) skinCmd(args []string) {
	if len(args) == 0 {
		b.reply("Usage: skin <slim|normal|wide> <path> [reload] | skin pack <folder> <skin_id> [reload] | skin pack <folder> list | skin default [reload] | skin toggle override")
		return
	}
	var err error
	sc := b.cfg.Skin
	switch strings.ToLower(args[0]) {
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
		armSize := uint8(protocol.ArmSizeSlim)
		if strings.EqualFold(args[0], "normal") || strings.EqualFold(args[0], "wide") {
			armSize = protocol.ArmSizeWide
		}
		_, err = bskin.BuildClassicSkin(args[1], armSize)
		if err != nil {
			b.reply("skin: " + err.Error())
			return
		}
		sc.Type = strings.ToLower(args[0])
		sc.Path = args[1]
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
		b.reply("Usage: skin <slim|normal|wide> <path> [reload] | skin pack <folder> <skin_id> [reload] | skin pack <folder> list | skin default [reload] | skin toggle override")
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
