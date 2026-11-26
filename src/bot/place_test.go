package bot

import (
	"strings"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

func TestIsPlaceableBlockItem(t *testing.T) {
	tests := []struct {
		name     string
		item     protocol.ItemInstance
		expected bool
	}{
		{
			name:     "empty item",
			item:     protocol.ItemInstance{},
			expected: false,
		},
		{
			name: "dirt block",
			item: protocol.ItemInstance{
				Stack: protocol.ItemStack{
					Count: 64,
				},
			},
			expected: true,
		},
		{
			name: "cobblestone block",
			item: protocol.ItemInstance{
				Stack: protocol.ItemStack{
					Count: 32,
				},
			},
			expected: true,
		},
		{
			name: "item with BlockRuntimeID",
			item: protocol.ItemInstance{
				Stack: protocol.ItemStack{
					BlockRuntimeID: 1234,
					Count:          1,
				},
			},
			expected: true,
		},
		{
			name: "diamond sword (unstackable tool)",
			item: protocol.ItemInstance{
				Stack: protocol.ItemStack{
					Count: 1,
				},
			},
			expected: false,
		},
		{
			name: "iron pickaxe (unstackable tool)",
			item: protocol.ItemInstance{
				Stack: protocol.ItemStack{
					Count: 1,
				},
			},
			expected: false,
		},
		{
			name: "stick",
			item: protocol.ItemInstance{
				Stack: protocol.ItemStack{
					Count: 64,
				},
			},
			expected: false,
		},
		{
			name: "raw iron",
			item: protocol.ItemInstance{
				Stack: protocol.ItemStack{
					Count: 16,
				},
			},
			expected: false,
		},
		{
			name: "wheat seeds",
			item: protocol.ItemInstance{
				Stack: protocol.ItemStack{
					Count: 64,
				},
			},
			expected: false,
		},
		{
			name: "cooked beef",
			item: protocol.ItemInstance{
				Stack: protocol.ItemStack{
					Count: 64,
				},
			},
			expected: false,
		},
		{
			name: "glass block",
			item: protocol.ItemInstance{
				Stack: protocol.ItemStack{
					Count: 10,
				},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var testItemName string
			switch {
			case strings.Contains(tc.name, "dirt"):
				testItemName = "minecraft:dirt"
			case strings.Contains(tc.name, "cobblestone"):
				testItemName = "minecraft:cobblestone"
			case strings.Contains(tc.name, "diamond sword"):
				testItemName = "minecraft:diamond_sword"
			case strings.Contains(tc.name, "iron pickaxe"):
				testItemName = "minecraft:iron_pickaxe"
			case strings.Contains(tc.name, "stick"):
				testItemName = "minecraft:stick"
			case strings.Contains(tc.name, "raw iron"):
				testItemName = "minecraft:raw_iron"
			case strings.Contains(tc.name, "wheat seeds"):
				testItemName = "minecraft:wheat_seeds"
			case strings.Contains(tc.name, "cooked beef"):
				testItemName = "minecraft:cooked_beef"
			case strings.Contains(tc.name, "glass"):
				testItemName = "minecraft:glass"
			default:
				testItemName = "minecraft:stone"
			}

			actual := isPlaceableBlockItem(testItemName, tc.item)
			if actual != tc.expected {
				t.Errorf("isPlaceableBlockItem(%q) = %v, want %v", testItemName, actual, tc.expected)
			}
		})
	}
}

func TestBlockPosOffsetAndOppositeFace(t *testing.T) {
	base := protocol.BlockPos{100, 64, -200}

	// Face 1: top (Y+1)
	up := blockPosOffset(base, 1)
	if up != (protocol.BlockPos{100, 65, -200}) {
		t.Errorf("blockPosOffset(base, 1) = %v, want (100, 65, -200)", up)
	}
	if oppositeFace(1) != 0 {
		t.Errorf("oppositeFace(1) = %d, want 0", oppositeFace(1))
	}

	// Face 0: bottom (Y-1)
	down := blockPosOffset(base, 0)
	if down != (protocol.BlockPos{100, 63, -200}) {
		t.Errorf("blockPosOffset(base, 0) = %v, want (100, 63, -200)", down)
	}

	// Face 2: north (Z-1)
	north := blockPosOffset(base, 2)
	if north != (protocol.BlockPos{100, 64, -201}) {
		t.Errorf("blockPosOffset(base, 2) = %v, want (100, 64, -201)", north)
	}

	// Face 3: south (Z+1)
	south := blockPosOffset(base, 3)
	if south != (protocol.BlockPos{100, 64, -199}) {
		t.Errorf("blockPosOffset(base, 3) = %v, want (100, 64, -199)", south)
	}

	// Face 4: west (X-1)
	west := blockPosOffset(base, 4)
	if west != (protocol.BlockPos{99, 64, -200}) {
		t.Errorf("blockPosOffset(base, 4) = %v, want (99, 64, -200)", west)
	}

	// Face 5: east (X+1)
	east := blockPosOffset(base, 5)
	if east != (protocol.BlockPos{101, 64, -200}) {
		t.Errorf("blockPosOffset(base, 5) = %v, want (101, 64, -200)", east)
	}
}

func TestPlaceCmdValidation(t *testing.T) {
	b := &Bot{
		fromConsole: true,
	}

	// Missing args
	b.placeCmd([]string{"place"})

	// Invalid args
	b.placeCmd([]string{"place", "abc", "64", "100"})

	// Bot not connected
	b.placeCmd([]string{"place", "100", "64", "-200"})
}

func TestPlaceOccupancyDetection(t *testing.T) {
	b := &Bot{
		fromConsole: true,
		blockRuntimeIDs: map[protocol.BlockPos]uint32{
			{100, 64, -200}: 1482, // solid block
		},
		mineAirRuntimeIDs: map[uint32]bool{
			0:          true,
			3690217760: true,
		},
		heldItem: protocol.ItemInstance{
			Stack: protocol.ItemStack{
				Count: 64,
			},
		},
	}

	// Should not panic and should detect solid block
	b.placeCmd([]string{"place", "100", "64", "-200"})
}
