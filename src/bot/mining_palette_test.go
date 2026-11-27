package bot

import (
	"testing"
)

func TestHashBlockState(t *testing.T) {
	tests := []struct {
		name     string
		states   map[string]any
		expected uint32
	}{
		{
			name:     "minecraft:air",
			states:   nil,
			expected: 3690217760,
		},
		{
			name:     "minecraft:dirt",
			states:   nil,
			expected: 2186211206,
		},
		{
			name:     "minecraft:stone",
			states:   nil,
			expected: 2150698529,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := HashBlockState(tc.name, tc.states)
			if got != tc.expected {
				t.Errorf("HashBlockState(%q) = %d, want %d", tc.name, got, tc.expected)
			}
		})
	}
}

func TestComputeDefaultAirRuntimeIDs(t *testing.T) {
	t.Run("Hashed Server", func(t *testing.T) {
		ids := computeDefaultAirRuntimeIDs(true)
		expected := []uint32{0, 3690217760}
		for _, exp := range expected {
			if !ids[exp] {
				t.Errorf("expected runtime ID %d to be in hashed air map", exp)
			}
		}
		if len(ids) != len(expected) {
			t.Errorf("expected %d entries, got %d: %v", len(expected), len(ids), ids)
		}
	})

	t.Run("Index BDS Server", func(t *testing.T) {
		ids := computeDefaultAirRuntimeIDs(false)
		if !ids[0] {
			t.Errorf("expected runtime ID 0 to be in index air map")
		}
		if ids[3690217760] {
			t.Errorf("did not expect hash 3690217760 in pure index BDS air map")
		}
	})
}

func TestBlockRuntimeIDByName(t *testing.T) {
	bHashed := &Bot{serverUseHashes: true}
	if rid, ok := bHashed.BlockRuntimeIDByName("minecraft:air", nil); !ok || rid != 3690217760 {
		t.Errorf("expected (3690217760, true), got (%d, %v)", rid, ok)
	}

	bIndex := &Bot{serverUseHashes: false}
	if rid, ok := bIndex.BlockRuntimeIDByName("minecraft:air", nil); !ok || rid != 0 {
		t.Errorf("expected (0, true), got (%d, %v)", rid, ok)
	}
}
