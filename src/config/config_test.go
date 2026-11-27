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

	// Verify the file was upgraded on disk
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

	// Verify migrated file content on disk has version 2
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read migrated file: %v", err)
	}
	if !strings.Contains(string(data), `"version": 2`) {
		t.Errorf("expected file on disk to be updated to version 2, got: %s", string(data))
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
