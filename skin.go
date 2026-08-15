package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"os"

	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

func applySkin(data *login.ClientData, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
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
	data.SkinData = base64.StdEncoding.EncodeToString(pixels)
	data.SkinImageWidth, data.SkinImageHeight = width, height
	data.SkinID = uuid.NewString()
	data.ArmSize = "slim"
	// Leave geometry and resource patch empty. gophertunnel fills these with
	// Minecraft's valid classic-skin defaults; custom geometry names are often
	// silently rejected by BDS, which makes the player appear skinless.
	return nil
}
