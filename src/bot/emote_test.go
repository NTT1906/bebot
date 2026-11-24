package bot

import (
	"path/filepath"
	"testing"
)

func TestEmotesLoadAndSave(t *testing.T) {
	tmpDir := t.TempDir()
	emotesFile := filepath.Join(tmpDir, "emotes.json")

	// Initial load when file does not exist should create default bookmarks
	emotes := loadEmotes(emotesFile)
	if len(emotes) == 0 {
		t.Fatal("expected default starter emotes, got empty map")
	}
	if _, ok := emotes["wave"]; !ok {
		t.Error("expected 'wave' in starter emotes")
	}

	// Add custom emote
	emotes["hello world"] = "c1819d2b-c8c3-4d69-b5f7-66a7b370605a"
	if err := saveEmotes(emotesFile, emotes); err != nil {
		t.Fatalf("saveEmotes failed: %v", err)
	}

	// Reload and verify
	reloaded := loadEmotes(emotesFile)
	if reloaded["hello world"] != "c1819d2b-c8c3-4d69-b5f7-66a7b370605a" {
		t.Errorf("expected 'hello world' bookmark to persist, got %s", reloaded["hello world"])
	}

	// Remove emote
	delete(reloaded, "hello world")
	if err := saveEmotes(emotesFile, reloaded); err != nil {
		t.Fatalf("saveEmotes failed: %v", err)
	}

	afterRemove := loadEmotes(emotesFile)
	if _, ok := afterRemove["hello world"]; ok {
		t.Error("expected 'hello world' to be removed")
	}
}
