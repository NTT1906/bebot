package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml"
)

// CurrentConfigVersion defines the latest schema version of config.json / config.toml.
const CurrentConfigVersion = 2

// SkinConfig holds the bot's skin in a dedicated skin.json file.
type SkinConfig struct {
	Type               string `json:"type" toml:"type"`                                             // "slim", "normal"/"wide", "pack"/"custom"
	Path               string `json:"path" toml:"path"`                                             // PNG file (slim/normal) or skin pack folder (pack/custom)
	Skin               string `json:"skin" toml:"skin"`                                             // skin id/index in the pack (pack/custom)
	OverrideAppearance *bool  `json:"overrideAppearance,omitempty" toml:"override-appearance,omitempty"` // nil defaults to true
}

// DefaultSkinConfig is what "skin default" restores: the standard slim skin.
var DefaultSkinConfig = SkinConfig{Type: "slim", Path: "skin/skin.png"}

// OverrideAppearanceOn returns true unless sc.OverrideAppearance is explicitly set to false.
func OverrideAppearanceOn(sc SkinConfig) bool {
	if sc.OverrideAppearance == nil {
		return true
	}
	return *sc.OverrideAppearance
}

// MineConfig holds parameters for the mining controller.
type MineConfig struct {
	TimeoutSec float64 `json:"timeout_sec" toml:"timeout_sec"`
}

// DefaultRenderDistance is the fallback chunk radius if unspecified or <= 0.
const DefaultRenderDistance = 4

// DefaultServerStallThresholdMs is the fallback duration without server packets before considering the server stalled.
const DefaultServerStallThresholdMs = 250

// Config represents the top-level configuration loaded from config.json or config.toml.
type Config struct {
	Version          int        `json:"version" toml:"version"`
	Superuser        string     `json:"superuser" toml:"superuser"`
	BlockPalettePath string     `json:"block_palette_path,omitempty" toml:"block-palette-path,omitempty"`
	FreeMemAfterJoin *bool      `json:"free_mem_after_join,omitempty" toml:"free-mem-after-join,omitempty"`
	Server           struct {
		Host    string `json:"host" toml:"host"`
		Port    int    `json:"port" toml:"port"`
		Version string `json:"version,omitempty" toml:"version,omitempty"`
	} `json:"server" toml:"server"`
	Behaviour struct {
		ReconnectBaseDelayMs int     `json:"reconnectBaseDelayMs" toml:"reconnect-base-delay-ms"`
		AfkJumpIntervalMs    int     `json:"afkJumpIntervalMs,omitempty" toml:"afk-jump-interval-ms,omitempty"` // deprecated
		WalkSpeed            float32 `json:"walkSpeed" toml:"walk-speed"`
		RenderDistance       int32   `json:"renderDistance" toml:"render-distance"`
		ServerStallThresholdMs int   `json:"serverStallThresholdMs" toml:"server-stall-threshold-ms"`
	} `json:"behaviour" toml:"behaviour"`
	CommandsViaChat  *bool      `json:"commands_via_chat,omitempty" toml:"commands-via-chat,omitempty"`
	SkinFile         string     `json:"skin_file,omitempty" toml:"skin-file,omitempty"`
	Skin             SkinConfig `json:"skin,omitempty" toml:"skin,omitempty"`
	Mine             MineConfig `json:"mine" toml:"mine"`
}

// FreeMemAfterJoinOn returns true unless explicitly set to false (defaults to true).
func FreeMemAfterJoinOn(cfg Config) bool {
	if cfg.FreeMemAfterJoin == nil {
		return true
	}
	return *cfg.FreeMemAfterJoin
}

// ServerStallThreshold returns the configured duration without server packets before considering the server stalled.
func (cfg Config) ServerStallThreshold() time.Duration {
	ms := cfg.Behaviour.ServerStallThresholdMs
	if ms <= 0 {
		ms = DefaultServerStallThresholdMs
	}
	return time.Duration(ms) * time.Millisecond
}

// RenderDistance returns the configured chunk radius, falling back to DefaultRenderDistance if <= 0.
func (cfg Config) RenderDistance() int32 {
	if cfg.Behaviour.RenderDistance <= 0 {
		return DefaultRenderDistance
	}
	return cfg.Behaviour.RenderDistance
}

// BaseReconnectDelay returns the configured base reconnect backoff.
func BaseReconnectDelay(cfg Config) time.Duration {
	d := time.Duration(cfg.Behaviour.ReconnectBaseDelayMs) * time.Millisecond
	if d <= 0 {
		d = 5 * time.Second
	}
	return d
}

// migrateConfig migrates a config from an older schema version to the target version.
func migrateConfig(cfg Config, fromVer, toVer int) Config {
	for v := fromVer; v < toVer; v++ {
		switch v {
		case 0:
			// Migration from v0 (unversioned legacy) to v1
			cfg.Version = 1
			if cfg.Behaviour.RenderDistance <= 0 {
				cfg.Behaviour.RenderDistance = DefaultRenderDistance
			}
			if cfg.Behaviour.ServerStallThresholdMs <= 0 {
				cfg.Behaviour.ServerStallThresholdMs = DefaultServerStallThresholdMs
			}
			if cfg.Behaviour.WalkSpeed <= 0 {
				cfg.Behaviour.WalkSpeed = 0.15
			}
			if cfg.Behaviour.ReconnectBaseDelayMs <= 0 {
				cfg.Behaviour.ReconnectBaseDelayMs = 5000
			}
			if cfg.Mine.TimeoutSec <= 0 {
				cfg.Mine.TimeoutSec = 3.0
			}
			cfg.Behaviour.AfkJumpIntervalMs = 0
		case 1:
			// Migration from v1 to v2: remove legacy target coordinates, unify skin_file
			cfg.Version = 2
			if cfg.SkinFile == "" && cfg.Skin.Path != "" {
				cfg.SkinFile = "data/skin.json"
			}
		}
	}
	return cfg
}

// LoadConfig reads config from a JSON or TOML file, validating schema and performing version migrations if needed.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if strings.HasSuffix(strings.ToLower(path), ".toml") {
		if err := toml.Unmarshal(b, &cfg); err != nil {
			return cfg, err
		}
	} else {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return cfg, err
		}
	}

	migrated := false
	if cfg.Version < CurrentConfigVersion {
		migrated = true
		cfg = migrateConfig(cfg, cfg.Version, CurrentConfigVersion)
	} else if cfg.Version > CurrentConfigVersion {
		log.Printf("bebot: warning: config version %d is newer than supported version %d\n", cfg.Version, CurrentConfigVersion)
	}

	if cfg.Server.Host == "" {
		return cfg, fmt.Errorf("server.host is required")
	}
	if cfg.Behaviour.RenderDistance <= 0 {
		cfg.Behaviour.RenderDistance = DefaultRenderDistance
	}
	if cfg.Behaviour.ServerStallThresholdMs <= 0 {
		cfg.Behaviour.ServerStallThresholdMs = DefaultServerStallThresholdMs
	}
	if cfg.Behaviour.WalkSpeed <= 0 {
		cfg.Behaviour.WalkSpeed = 0.15
	}
	if cfg.Behaviour.ReconnectBaseDelayMs <= 0 {
		cfg.Behaviour.ReconnectBaseDelayMs = 5000
	}
	if cfg.Mine.TimeoutSec <= 0 {
		cfg.Mine.TimeoutSec = 3.0
	}

	if migrated {
		if err := SaveConfig(path, cfg); err == nil {
			log.Printf("bebot: upgraded %s to config version %d\n", path, cfg.Version)
		}
	}

	return cfg, nil
}

// SaveConfig writes config to a JSON or TOML file depending on the file extension.
func SaveConfig(path string, cfg Config) error {
	if cfg.Version <= 0 {
		cfg.Version = CurrentConfigVersion
	}
	if strings.HasSuffix(strings.ToLower(path), ".toml") {
		data, err := toml.Marshal(cfg)
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0644)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// LoadSkin reads the bot's skin config from its dedicated skin.json file.
func LoadSkin(path string) (SkinConfig, error) {
	var sc SkinConfig
	targetPath := path
	b, err := os.ReadFile(targetPath)
	if err != nil && (targetPath == "data/skin.json" || targetPath == filepath.Clean("data/skin.json")) {
		if fallbackB, fallbackErr := os.ReadFile("skin.json"); fallbackErr == nil {
			b = fallbackB
			err = nil
		}
	}
	if err != nil {
		return sc, err
	}
	if err := json.Unmarshal(b, &sc); err != nil {
		return sc, err
	}
	if sc.Type == "" {
		sc.Type = DefaultSkinConfig.Type
	}
	if sc.Path == "" {
		sc.Path = DefaultSkinConfig.Path
	}
	return sc, nil
}

// SaveSkin writes the skin config to its dedicated skin.json file.
func SaveSkin(path string, sc SkinConfig) error {
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
	return os.WriteFile(path, data, 0644)
}

// ResolveSkinConfig resolves a skin configuration from an input path, which can be:
// 1. Direct .png file path (e.g. "skin/skin.png" or "my_skin.png"):
//    Uses the PNG image directly as a classic skin, and directs future in-game skin saves
//    to defaultSavePath (e.g. "data/skin.json").
// 2. A .json skin configuration file:
//    Loads the JSON configuration and saves future in-game updates back to that JSON file.
// 3. Empty string:
//    Falls back to defaultSavePath, then legacy embedded config if provided, then DefaultSkinConfig.
func ResolveSkinConfig(rawTarget string, defaultSavePath string, legacyCfg SkinConfig) (SkinConfig, string, error) {
	if defaultSavePath == "" {
		defaultSavePath = "data/skin.json"
	}

	target := strings.TrimSpace(rawTarget)

	// Case 1: Direct .png image file
	if strings.EqualFold(filepath.Ext(target), ".png") {
		resolvedPNG := target
		if _, err := os.Stat(resolvedPNG); err != nil {
			// Try looking in common skin directories
			candidates := []string{
				filepath.Join("skin", target),
				filepath.Join("data", "skins", target),
				filepath.Join("data", target),
			}
			found := false
			for _, cand := range candidates {
				if _, errStat := os.Stat(cand); errStat == nil {
					resolvedPNG = cand
					found = true
					break
				}
			}
			if !found {
				return DefaultSkinConfig, defaultSavePath, fmt.Errorf("direct skin png %q not found", target)
			}
		}

		sc := SkinConfig{
			Type: "slim",
			Path: resolvedPNG,
		}
		// When skin is changed in-game, save to default JSON file
		return sc, defaultSavePath, nil
	}

	// Case 2: Explicit JSON file path
	if target != "" && target != defaultSavePath {
		sc, err := LoadSkin(target)
		if err == nil {
			return sc, target, nil
		}
		return DefaultSkinConfig, defaultSavePath, fmt.Errorf("failed to load skin config from %q: %w", target, err)
	}

	// Case 3: Default save path or fallback
	sc, err := LoadSkin(defaultSavePath)
	if err == nil {
		return sc, defaultSavePath, nil
	}

	// Check if legacy config had an embedded skin
	if legacyCfg.Path != "" {
		if legacyCfg.Type == "" {
			legacyCfg.Type = DefaultSkinConfig.Type
		}
		return legacyCfg, defaultSavePath, nil
	}

	return DefaultSkinConfig, defaultSavePath, nil
}
