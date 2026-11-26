package bot

import (
	"runtime"
	"strings"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

func TestBlockRegistryLookup(t *testing.T) {
	reg := NewBlockRegistry()
	reg.InitFromGameData(true, []protocol.BlockEntry{
		{
			Name: "custom:ruby_ore",
			Properties: map[string]any{
				"custom_state": int32(1),
			},
		},
	}, "../../data/block_palette.nbt")

	// 1. Check air
	name, ok := reg.ResolveName(0)
	if !ok || name != "minecraft:air" {
		t.Errorf("expected 0 to resolve to minecraft:air, got %s (ok=%v)", name, ok)
	}

	// 2. Check stone
	stoneRID := HashBlockState("minecraft:stone", nil)
	name, ok = reg.ResolveName(stoneRID)
	if !ok || name != "minecraft:stone" {
		t.Errorf("expected %d to resolve to minecraft:stone, got %s (ok=%v)", stoneRID, name, ok)
	}

	// 3. Resolve RID by name
	rid, ok := reg.ResolveRID("stone")
	if !ok || rid != stoneRID {
		t.Errorf("expected 'stone' to resolve to RID %d, got %d (ok=%v)", stoneRID, rid, ok)
	}

	// 4. Custom block lookup
	customRID := HashBlockState("custom:ruby_ore", map[string]any{"custom_state": int32(1)})
	name, ok = reg.ResolveName(customRID)
	if !ok || name != "custom:ruby_ore" {
		t.Errorf("expected custom block to resolve to custom:ruby_ore, got %s", name)
	}
}

func TestBlockCmd(t *testing.T) {
	reg := NewBlockRegistry()
	reg.InitFromGameData(true, nil, "../../data/block_palette.nbt")

	b := &Bot{
		blockRegistry: reg,
	}

	replies := make([]string, 0)
	b.testReplyHook = func(msg string) {
		replies = append(replies, msg)
	}

	// 1. Lookup by name
	b.blockCmd([]string{"lookup", "stone"})
	if len(replies) == 0 || !strings.Contains(replies[len(replies)-1], "RuntimeID") {
		t.Fatalf("expected stone lookup reply, got %v", replies)
	}

	// 2. Lookup by ID
	dirtRID := HashBlockState("minecraft:dirt", nil)
	b.blockCmd([]string{"lookup", "2186211206"})
	lastReply := replies[len(replies)-1]
	if !strings.Contains(lastReply, "minecraft:dirt") {
		t.Errorf("expected dirt lookup for %d, got: %s", dirtRID, lastReply)
	}

	// 3. Count
	b.blockCmd([]string{"count"})
	lastReply = replies[len(replies)-1]
	if !strings.Contains(lastReply, "block states registered") {
		t.Errorf("expected block count reply, got: %s", lastReply)
	}
}

func TestBlockRegistryMemoryImpact(t *testing.T) {
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	reg := NewBlockRegistry()
	loaded := reg.LoadPaletteFile("../../data/block_palette.nbt", true)
	if !loaded {
		loaded = reg.LoadPaletteFile("data/block_palette.nbt", true)
	}
	if !loaded {
		t.Skip("data/block_palette.nbt not found")
	}

	runtime.GC()
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	diffKB := float64(m2.Alloc-m1.Alloc) / 1024.0
	t.Logf("Allocated %d block states. Memory growth: %.2f KB (%.2f MB)", reg.Count(), diffKB, diffKB/1024.0)

	// Verify lookup
	name, ok := reg.ResolveName(973836165)
	if !ok || name != "minecraft:cyan_terracotta" {
		t.Errorf("expected 973836165 to resolve to minecraft:cyan_terracotta, got %s (ok=%v)", name, ok)
	}
}
