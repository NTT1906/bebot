package bot

import (
	"path/filepath"
	"strings"
	"testing"

	"bebot/src/config"
)

func TestSkinCommands(t *testing.T) {
	tmpDir := t.TempDir()
	saveJSON := filepath.Join(tmpDir, "data", "skin.json")

	b := &Bot{
		cfg: config.Config{
			Skin: config.SkinConfig{
				Type: "slim",
				Path: "skin/skin.png",
			},
		},
		skinPath: saveJSON,
	}

	replies := make([]string, 0)
	b.testReplyHook = func(msg string) {
		replies = append(replies, msg)
	}

	// 1. Status command (no args)
	b.skinCmd(nil)
	if len(replies) == 0 || !strings.Contains(replies[0], "[Skin] Active:") {
		t.Fatalf("expected skin status reply, got %v", replies)
	}

	// 2. List command
	b.skinCmd([]string{"list"})
	lastReply := replies[len(replies)-1]
	if !strings.Contains(lastReply, "[Skin]") {
		t.Fatalf("expected skin list reply, got %v", lastReply)
	}

	// 3. Use command with missing file
	b.skinCmd([]string{"use", "missing_skin_xyz"})
	lastReply = replies[len(replies)-1]
	if !strings.Contains(lastReply, "Could not find skin file") {
		t.Fatalf("expected not found warning, got %s", lastReply)
	}

	// 4. SaveSkin when skinPath is .png redirects to data/skin.json
	b.skinPath = filepath.Join(tmpDir, "direct.png")
	if err := b.saveSkin(); err != nil {
		t.Fatalf("saveSkin failed: %v", err)
	}
	if b.skinPath != "data/skin.json" {
		t.Errorf("expected skinPath to redirect from .png to data/skin.json, got %s", b.skinPath)
	}
}
