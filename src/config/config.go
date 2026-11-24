package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

// CurrentConfigVersion defines the latest schema version of config.json.
const CurrentConfigVersion = 1

// SkinConfig holds the bot's skin in a dedicated skin.json file.
type SkinConfig struct {
	Type               string `json:"type"`               // "slim", "normal"/"wide", "pack"/"custom"
	Path               string `json:"path"`               // PNG file (slim/normal) or skin pack folder (pack/custom)
	Skin               string `json:"skin"`               // skin id/index in the pack (pack/custom)
	OverrideAppearance *bool  `json:"overrideAppearance"` // nil defaults to true
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
	TimeoutSec float64 `json:"timeout_sec"`
}

// DefaultRenderDistance is the fallback chunk radius if unspecified or <= 0.
const DefaultRenderDistance = 4

// DefaultServerStallThresholdMs is the fallback duration without server packets before considering the server stalled.
const DefaultServerStallThresholdMs = 250

// Config represents the top-level configuration loaded from config.json.
type Config struct {
	Version   int    `json:"version"`
	Superuser string `json:"superuser"`
	Server    struct {
		Host    string `json:"host"`
		Port    int    `json:"port"`
		Version string `json:"version,omitempty"`
	} `json:"server"`
	Target    struct{ X, Y, Z float32 } `json:"target"`
	Behaviour struct {
		ReconnectBaseDelayMs int     `json:"reconnectBaseDelayMs"`
		AfkJumpIntervalMs    int     `json:"afkJumpIntervalMs,omitempty"` // deprecated
		WalkSpeed            float32 `json:"walkSpeed"`
		RenderDistance       int32   `json:"renderDistance"`
		ServerStallThresholdMs int   `json:"serverStallThresholdMs"`
	} `json:"behaviour"`
	CommandsViaChat *bool      `json:"commands_via_chat"`
	Skin            SkinConfig `json:"skin"`
	Mine            MineConfig `json:"mine"`
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
		}
	}
	return cfg
}

// LoadConfig reads config from a JSON file, validating schema and performing version migrations if needed.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
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

// SaveConfig writes config to a JSON file.
func SaveConfig(path string, cfg Config) error {
	if cfg.Version <= 0 {
		cfg.Version = CurrentConfigVersion
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
	b, err := os.ReadFile(path)
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
	return os.WriteFile(path, data, 0644)
}
