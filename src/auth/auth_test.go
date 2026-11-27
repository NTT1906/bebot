package auth

import (
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestOfflineUUIDPersistenceAndRenew(t *testing.T) {
	tmpDir := t.TempDir()
	uuidFile := filepath.Join(tmpDir, "sub", "offline_uuid.json")

	// First load generates a new UUID and persists it (testing MkdirAll)
	id1 := LoadOrGenerateOfflineUUID(uuidFile, "BotAlpha", false)
	if id1 == uuid.Nil {
		t.Fatal("expected valid UUID, got nil UUID")
	}

	// Second load restores the exact same UUID
	id2 := LoadOrGenerateOfflineUUID(uuidFile, "BotAlpha", false)
	if id2 != id1 {
		t.Fatalf("expected restored UUID %s, got %s", id1, id2)
	}

	// Load different bot name gets a distinct UUID
	idBeta := LoadOrGenerateOfflineUUID(uuidFile, "BotBeta", false)
	if idBeta == uuid.Nil || idBeta == id1 {
		t.Fatalf("expected distinct UUID for BotBeta, got %s", idBeta)
	}

	// Calling with renew generates a fresh UUID for BotAlpha
	idRenewed := LoadOrGenerateOfflineUUID(uuidFile, "BotAlpha", true)
	if idRenewed == uuid.Nil || idRenewed == id1 {
		t.Fatalf("expected fresh UUID after renew, got %s", idRenewed)
	}

	// Subsequent normal load gets the renewed UUID
	idAfterRenew := LoadOrGenerateOfflineUUID(uuidFile, "BotAlpha", false)
	if idAfterRenew != idRenewed {
		t.Fatalf("expected restored renewed UUID %s, got %s", idRenewed, idAfterRenew)
	}
}

func TestSaveXBLCacheNil(t *testing.T) {
	tmpDir := t.TempDir()
	cacheFile := filepath.Join(tmpDir, "sub", "xsts_cache.json")

	// Nil cache should not panic or fail
	SaveXBLCache(cacheFile, nil)
}
