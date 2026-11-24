package config

import (
	"os"
	"path/filepath"
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

	if cfg.Version != 1 {
		t.Errorf("expected version 1, got %d", cfg.Version)
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

	if cfg.Version != 1 {
		t.Errorf("expected version 1, got %d", cfg.Version)
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
