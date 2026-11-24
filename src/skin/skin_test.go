package skin

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"bebot/src/config"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

func createTestPNG(path string, width, height int) error {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func TestBuildClassicSkin_ValidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	pngPath := filepath.Join(tmpDir, "skin.png")
	if err := createTestPNG(pngPath, 64, 64); err != nil {
		t.Fatalf("failed to create test PNG: %v", err)
	}

	skin, err := BuildClassicSkin(pngPath, protocol.ArmSizeSlim)
	if err != nil {
		t.Fatalf("BuildClassicSkin failed: %v", err)
	}

	// Verify SkinResourcePatch is valid JSON
	var patchObj map[string]any
	if err := json.Unmarshal(skin.SkinResourcePatch, &patchObj); err != nil {
		t.Errorf("SkinResourcePatch is not valid JSON: %v", err)
	}

	// Verify SkinGeometry is non-empty and valid JSON
	if len(skin.SkinGeometry) == 0 {
		t.Error("SkinGeometry is empty; must be non-empty valid JSON for Bedrock server")
	}
	var geoObj map[string]any
	if err := json.Unmarshal(skin.SkinGeometry, &geoObj); err != nil {
		t.Errorf("SkinGeometry is not valid JSON: %v", err)
	}

	// Verify Default Skin Config
	sc := config.SkinConfig{Type: "slim", Path: pngPath}
	loaded, err := LoadSkinFromConfig(sc)
	if err != nil {
		t.Fatalf("LoadSkinFromConfig failed: %v", err)
	}
	if len(loaded.SkinGeometry) == 0 {
		t.Error("loaded skin has empty SkinGeometry")
	}
}
