package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig_UnversionedMigration(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Legacy v0 config without "version" field
	rawLegacy := `{
  "superuser": "Tester",
  "server": {
    "host": "127.0.0.1",
    "port": 19132
  },
  "behaviour": {
    "walkSpeed": 0.2
  }
}`
	if err := os.WriteFile(configPath, []byte(rawLegacy), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Version != CurrentConfigVersion {
		t.Errorf("expected version %d, got %d", CurrentConfigVersion, cfg.Version)
	}
	if cfg.Superuser != "Tester" {
		t.Errorf("expected superuser Tester, got %s", cfg.Superuser)
	}
	if cfg.Behaviour.WalkSpeed != 0.2 {
		t.Errorf("expected walkSpeed 0.2, got %f", cfg.Behaviour.WalkSpeed)
	}
	if cfg.Behaviour.RenderDistance != DefaultRenderDistance {
		t.Errorf("expected default renderDistance %d, got %d", DefaultRenderDistance, cfg.Behaviour.RenderDistance)
	}

	// Verify the file was upgraded on disk with snake_case layout
	diskData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read upgraded config: %v", err)
	}
	if strings.Contains(string(diskData), `"walkSpeed"`) {
		t.Errorf("expected legacy walkSpeed to be removed from disk, got: %s", string(diskData))
	}
	if !strings.Contains(string(diskData), `"walk_speed": 0.2`) {
		t.Errorf("expected walk_speed: 0.2 in upgraded file, got: %s", string(diskData))
	}

	reloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("reloading upgraded config failed: %v", err)
	}
	if reloaded.Version != CurrentConfigVersion {
		t.Errorf("expected upgraded version %d on disk, got %d", CurrentConfigVersion, reloaded.Version)
	}
}

func TestLoadConfig_Version1(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	rawV1 := `{
  "version": 1,
  "superuser": "Admin",
  "server": {
    "host": "192.168.1.100",
    "port": 19132
  }
}`
	if err := os.WriteFile(configPath, []byte(rawV1), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Version != CurrentConfigVersion {
		t.Errorf("expected version %d, got %d", CurrentConfigVersion, cfg.Version)
	}
	if cfg.Superuser != "Admin" {
		t.Errorf("expected superuser Admin, got %s", cfg.Superuser)
	}
}

func TestSaveConfig_SetsVersion(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	var cfg Config
	cfg.Server.Host = "localhost"

	if err := SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	reloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if reloaded.Version != CurrentConfigVersion {
		t.Errorf("expected saved version %d, got %d", CurrentConfigVersion, reloaded.Version)
	}
}

func TestLoadConfig_TOML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	rawTOML := `version = 1
superuser = "Arie1906"
free-mem-after-join = true

[server]
host = "127.0.0.1"
port = 19132

[behaviour]
walk-speed = 0.18
render-distance = 2
`
	if err := os.WriteFile(configPath, []byte(rawTOML), 0644); err != nil {
		t.Fatalf("failed to write TOML test file: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig for TOML failed: %v", err)
	}

	if cfg.Version != CurrentConfigVersion {
		t.Errorf("expected version %d, got %d", CurrentConfigVersion, cfg.Version)
	}
	if cfg.Superuser != "Arie1906" {
		t.Errorf("expected superuser Arie1906, got %s", cfg.Superuser)
	}
	if !FreeMemAfterJoinOn(cfg) {
		t.Error("expected FreeMemAfterJoinOn to be true")
	}
	if cfg.Behaviour.WalkSpeed != 0.18 {
		t.Errorf("expected walkSpeed 0.18, got %f", cfg.Behaviour.WalkSpeed)
	}
	if cfg.Behaviour.RenderDistance != 2 {
		t.Errorf("expected renderDistance 2, got %d", cfg.Behaviour.RenderDistance)
	}

	// Verify saving back to TOML
	if err := SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig to TOML failed: %v", err)
	}
	reloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("reloading saved TOML failed: %v", err)
	}
	if reloaded.Superuser != "Arie1906" {
		t.Errorf("expected reloaded superuser Arie1906, got %s", reloaded.Superuser)
	}
}

func TestLoadConfig_V1toV2Migration(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config_v1.json")

	// Legacy v1 config with "target" and legacy embedded "skin"
	rawV1 := `{
  "version": 1,
  "superuser": "TesterV1",
  "server": {
    "host": "127.0.0.1",
    "port": 19132
  },
  "target": {
    "X": 100,
    "Y": 64,
    "Z": -200
  },
  "skin": {
    "type": "slim",
    "path": "skin/steve.png"
  }
}`
	if err := os.WriteFile(configPath, []byte(rawV1), 0644); err != nil {
		t.Fatalf("failed to write v1 config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed for v1 config: %v", err)
	}

	if cfg.Version != 2 {
		t.Errorf("expected migrated version 2, got %d", cfg.Version)
	}
	if cfg.SkinFile != "data/skin.json" {
		t.Errorf("expected skin_file to be data/skin.json, got %s", cfg.SkinFile)
	}

	// Verify migrated file content on disk has version 2 and clean snake_case layout
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read migrated file: %v", err)
	}
	if !strings.Contains(string(data), `"version": 2`) {
		t.Errorf("expected file on disk to be updated to version 2, got: %s", string(data))
	}
	if strings.Contains(string(data), `"target"`) {
		t.Errorf("expected target to be removed from disk, got: %s", string(data))
	}
}

func TestLoadConfig_V2CamelCaseMigration(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config_v2_legacy.json")

	rawV2Legacy := `{
  "version": 2,
  "superuser": "TesterV2",
  "server": {
    "host": "127.0.0.1",
    "port": 19132
  },
  "behaviour": {
    "reconnectBaseDelayMs": 3000,
    "walkSpeed": 0.25,
    "renderDistance": 6,
    "serverStallThresholdMs": 300
  },
  "mine": {
    "timeout_sec": 7.5
  }
}`
	if err := os.WriteFile(configPath, []byte(rawV2Legacy), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Behaviour.WalkSpeed != 0.25 {
		t.Errorf("expected walk_speed 0.25, got %f", cfg.Behaviour.WalkSpeed)
	}
	if cfg.Behaviour.RenderDistance != 6 {
		t.Errorf("expected render_distance 6, got %d", cfg.Behaviour.RenderDistance)
	}
	if cfg.Behaviour.ReconnectBaseDelayMs != 3000 {
		t.Errorf("expected reconnect_base_delay_ms 3000, got %d", cfg.Behaviour.ReconnectBaseDelayMs)
	}
	if cfg.Behaviour.ServerStallThresholdMs != 300 {
		t.Errorf("expected server_stall_threshold_ms 300, got %d", cfg.Behaviour.ServerStallThresholdMs)
	}
	if cfg.Mining.TimeoutSec != 7.5 {
		t.Errorf("expected mining.timeout_sec 7.5, got %f", cfg.Mining.TimeoutSec)
	}

	// Verify the disk file was migrated to snake_case layout
	migratedBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read migrated file: %v", err)
	}
	s := string(migratedBytes)
	if strings.Contains(s, "walkSpeed") || strings.Contains(s, "reconnectBaseDelayMs") {
		t.Errorf("expected camelCase keys to be migrated away, got: %s", s)
	}
	if strings.Contains(s, `"mine":`) {
		t.Errorf("expected legacy 'mine' key to be migrated away, got: %s", s)
	}
	if !strings.Contains(s, `"mining": {`) && !strings.Contains(s, `"mining":`) {
		t.Errorf("expected 'mining' section in migrated file, got: %s", s)
	}
	if !strings.Contains(s, `"timeout_sec": 7.5`) {
		t.Errorf("expected timeout_sec: 7.5 in migrated file, got: %s", s)
	}
	if !strings.Contains(s, `"walk_speed": 0.25`) {
		t.Errorf("expected walk_speed in migrated file, got: %s", s)
	}
	if !strings.Contains(s, `"render_distance": 6`) {
		t.Errorf("expected render_distance in migrated file, got: %s", s)
	}
}

func TestLogToFile_DefaultAndOverrides(t *testing.T) {
	// 1. Default when nil should be true
	var cfgDefault Config
	if !LogToFileOn(cfgDefault) {
		t.Errorf("expected default LogToFileOn to be true")
	}

	// 2. Explicitly false
	f := false
	cfgFalse := Config{LogToFile: &f}
	if LogToFileOn(cfgFalse) {
		t.Errorf("expected LogToFileOn to be false when explicitly false")
	}

	// 3. Explicitly true
	tr := true
	cfgTrue := Config{LogToFile: &tr}
	if !LogToFileOn(cfgTrue) {
		t.Errorf("expected LogToFileOn to be true when explicitly true")
	}
}

func TestResolveSkinConfig_DirectPNGAndJSON(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Test direct .png resolution
	testPNG := filepath.Join(tmpDir, "custom.png")
	if err := os.WriteFile(testPNG, []byte("fake-png-data"), 0644); err != nil {
		t.Fatalf("failed to write test png: %v", err)
	}

	defaultSave := filepath.Join(tmpDir, "data", "skin.json")
	scPNG, savePathPNG, err := ResolveSkinConfig(testPNG, defaultSave, SkinConfig{})
	if err != nil {
		t.Fatalf("unexpected error resolving direct PNG: %v", err)
	}
	if scPNG.Path != testPNG {
		t.Errorf("expected skin path %s, got %s", testPNG, scPNG.Path)
	}
	if scPNG.Type != "slim" {
		t.Errorf("expected default type slim, got %s", scPNG.Type)
	}
	if savePathPNG != defaultSave {
		t.Errorf("expected save path %s, got %s", defaultSave, savePathPNG)
	}

	// 2. Test JSON config resolution
	testJSON := filepath.Join(tmpDir, "skin_profile.json")
	if err := SaveSkin(testJSON, SkinConfig{Type: "normal", Path: "steve.png"}); err != nil {
		t.Fatalf("failed to save test json: %v", err)
	}

	scJSON, savePathJSON, err := ResolveSkinConfig(testJSON, defaultSave, SkinConfig{})
	if err != nil {
		t.Fatalf("unexpected error resolving JSON config: %v", err)
	}
	if scJSON.Type != "normal" || scJSON.Path != "steve.png" {
		t.Errorf("unexpected skin config from JSON: %+v", scJSON)
	}
	if savePathJSON != testJSON {
		t.Errorf("expected save path to match input json %s, got %s", testJSON, savePathJSON)
	}

	// 3. Test legacy fallback when target and defaultSave are missing
	missingDefault := filepath.Join(tmpDir, "non_existent", "skin.json")
	legacy := SkinConfig{Type: "wide", Path: "legacy.png"}
	scLegacy, savePathLegacy, err := ResolveSkinConfig("", missingDefault, legacy)
	if err != nil {
		t.Fatalf("unexpected error with legacy fallback: %v", err)
	}
	if scLegacy.Path != "legacy.png" || scLegacy.Type != "wide" {
		t.Errorf("expected legacy skin config, got %+v", scLegacy)
	}
	if savePathLegacy != missingDefault {
		t.Errorf("expected save path %s, got %s", missingDefault, savePathLegacy)
	}
}
