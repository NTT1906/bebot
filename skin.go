package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// skinResourcePatchStandard points a classic (humanoid) skin at the built-in
// geometry. Geometry data is left empty so the client uses its own model.
const skinResourcePatchStandard = `{"geometry":{"default":"geometry.humanoid.custom"}}`

// loadSkinFromConfig builds a protocol.Skin from a skinConfig.
func loadSkinFromConfig(sc skinConfig) (protocol.Skin, error) {
	switch strings.ToLower(sc.Type) {
	case "", "slim":
		return buildClassicSkin(sc.Path, protocol.ArmSizeSlim)
	case "normal", "wide":
		return buildClassicSkin(sc.Path, protocol.ArmSizeWide)
	case "pack", "custom":
		return buildPackSkin(sc.Path, sc.Skin)
	default:
		return protocol.Skin{}, fmt.Errorf("unknown skin type %q", sc.Type)
	}
}

// buildClassicSkin loads a PNG skin texture and returns a classic skin with
// the given arm size. Geometry is left empty so the built-in humanoid model is
// used.
func buildClassicSkin(path string, armSize uint8) (protocol.Skin, error) {
	width, height, pixels, err := decodeSkinPNG(path)
	if err != nil {
		return protocol.Skin{}, err
	}
	return protocol.Skin{
		SkinID:            uuid.NewString(),
		SkinImageWidth:    uint32(width),
		SkinImageHeight:   uint32(height),
		SkinData:          pixels,
		SkinResourcePatch: []byte(skinResourcePatchStandard),
		ArmSize:           armSize,
		Trusted:           true,
	}, nil
}

// buildPackSkin loads a skin from a Bedrock skin pack by its id. The pack's
// skins.json maps skin ids to geometry and texture files. If the pack ships a
// custom geometry JSON, it is used; otherwise the built-in humanoid geometry
// is referenced.
func buildPackSkin(folder, skinID string) (protocol.Skin, error) {
	skins, err := parseSkinPack(folder)
	if err != nil {
		return protocol.Skin{}, err
	}
	entry, err := findPackSkin(skins, skinID)
	if err != nil {
		return protocol.Skin{}, err
	}
	texturePath := filepath.Join(folder, entry.Texture)
	if _, err := os.Stat(texturePath); err != nil {
		texturePath, err = findTextureFile(folder, entry.Texture)
		if err != nil {
			return protocol.Skin{}, err
		}
	}
	width, height, pixels, err := decodeSkinPNG(texturePath)
	if err != nil {
		return protocol.Skin{}, err
	}
	geometryID := normalizeGeometryID(entry.Geometry)
	geometry, gerr := findGeometryFile(folder)
	if gerr != nil {
		// No custom geometry in the pack: fall back to the standard humanoid.
		geometryID = "geometry.humanoid.custom"
		geometry = nil
	}
	patch, err := json.Marshal(struct {
		Geometry map[string]string `json:"geometry"`
	}{Geometry: map[string]string{"default": geometryID}})
	if err != nil {
		return protocol.Skin{}, err
	}
	geoVersion := []byte("1.8.0")
	if len(geometry) > 0 {
		var geoMeta struct {
			FormatVersion string `json:"format_version"`
		}
		if err := json.Unmarshal(geometry, &geoMeta); err == nil && geoMeta.FormatVersion != "" {
			geoVersion = []byte(geoMeta.FormatVersion)
		}
	}
	return protocol.Skin{
		SkinID:                    uuid.NewString(),
		SkinImageWidth:            uint32(width),
		SkinImageHeight:           uint32(height),
		SkinData:                  pixels,
		SkinResourcePatch:         patch,
		SkinGeometry:              geometry,
		GeometryDataEngineVersion: geoVersion,
		ArmSize:                   protocol.ArmSizeSlim,
		Trusted:                   true,
	}, nil
}

// skinPackSkin is one entry from a pack's skins.json.
type skinPackSkin struct {
	LocalizationName string `json:"localization_name"`
	Geometry         string `json:"geometry"`
	Texture          string `json:"texture"`
	Type             string `json:"type"`
}

// parseSkinPack reads and parses a skin pack's skins.json.
func parseSkinPack(folder string) ([]skinPackSkin, error) {
	path := filepath.Join(folder, "skins.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var meta struct {
		Skins []skinPackSkin `json:"skins"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return meta.Skins, nil
}

// findPackSkin locates a skin entry by id. The id may be the 0-based index in
// skins.json, the localization name, the geometry id (with or without the
// "geometry." prefix) or the texture name.
func findPackSkin(skins []skinPackSkin, id string) (skinPackSkin, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	for i, s := range skins {
		name := strings.ToLower(cleanChatName(s.LocalizationName))
		if id == fmt.Sprintf("%d", i) ||
			id == name ||
			id == strings.ToLower(s.Geometry) ||
			id == strings.ToLower(strings.TrimPrefix(s.Geometry, "geometry.")) ||
			id == strings.ToLower(strings.TrimSuffix(s.Texture, filepath.Ext(s.Texture))) {
			return s, nil
		}
	}
	names := make([]string, 0, len(skins))
	for _, s := range skins {
		names = append(names, s.LocalizationName)
	}
	return skinPackSkin{}, fmt.Errorf("skin %q not found in pack (available: %s)", id, strings.Join(names, ", "))
}

// findTextureFile searches the pack for a texture file by base name, in case
// the texture lives in a subfolder.
func findTextureFile(folder, texture string) (string, error) {
	base := strings.ToLower(texture)
	var found string
	_ = filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.ToLower(path) == strings.ToLower(filepath.Join(folder, texture)) {
			found = path
			return fs.SkipAll
		}
		if strings.ToLower(filepath.Base(path)) == base {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", fmt.Errorf("texture %s not found in skin pack %s", texture, folder)
	}
	return found, nil
}

// findGeometryFile walks the skin pack folder for a JSON file that holds
// Bedrock geometry data. It accepts both the modern format (contains a
// "minecraft:geometry" key) and the 1.8.0 format (top-level keys prefixed
// "geometry.").
func findGeometryFile(folder string) ([]byte, error) {
	var geometry []byte
	_ = filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".json") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if !isGeometryJSON(b) {
			return nil
		}
		geometry = b
		return fs.SkipAll
	})
	if len(geometry) == 0 {
		return nil, fmt.Errorf("no geometry JSON found under %s", folder)
	}
	return geometry, nil
}

// isGeometryJSON reports whether raw JSON holds Bedrock geometry data.
func isGeometryJSON(raw []byte) bool {
	if bytes.Contains(raw, []byte("minecraft:geometry")) {
		return true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	for key := range obj {
		if strings.HasPrefix(key, "geometry.") {
			return true
		}
	}
	return false
}

// normalizeGeometryID ensures the geometry identifier uses the "geometry.X"
// convention expected by Bedrock resource patches. Geometry ids legitimately
// contain dots (e.g. "geometry.n0"), so nothing after a dot is stripped.
func normalizeGeometryID(id string) string {
	id = strings.TrimSpace(id)
	if strings.HasPrefix(id, "geometry.") {
		return id
	}
	return "geometry." + id
}

// decodeSkinPNG reads a PNG and returns its width, height and packed RGBA
// pixel data.
func decodeSkinPNG(path string) (int, int, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, nil, err
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return 0, 0, nil, fmt.Errorf("decode %s: %w", path, err)
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	pixels := make([]byte, 0, width*height*4)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			pixels = append(pixels, byte(r>>8), byte(g>>8), byte(b>>8), byte(a>>8))
		}
	}
	return width, height, pixels, nil
}

// fillClientDataSkin copies a protocol.Skin into the login ClientData fields.
func fillClientDataSkin(data *login.ClientData, skin protocol.Skin) {
	data.SkinID = skin.SkinID
	data.SkinImageWidth, data.SkinImageHeight = int(skin.SkinImageWidth), int(skin.SkinImageHeight)
	data.SkinData = base64.StdEncoding.EncodeToString(skin.SkinData)
	data.SkinResourcePatch = base64.StdEncoding.EncodeToString(skin.SkinResourcePatch)
	data.SkinGeometry = base64.StdEncoding.EncodeToString(skin.SkinGeometry)
	data.SkinGeometryVersion = string(skin.GeometryDataEngineVersion)
	if skin.ArmSize == protocol.ArmSizeSlim {
		data.ArmSize = "slim"
	} else {
		data.ArmSize = "wide"
	}
}
