package bot

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// BlockRegistry provides bidirectional mapping between block names (with optional states)
// and their Bedrock network runtime IDs.
type BlockRegistry struct {
	mu           sync.RWMutex
	useHashes    bool
	loaded       bool
	actualAirRID uint32
	version      string
	namesByRID   map[uint32]string
	ridsByName   map[string]uint32
}

// NewBlockRegistry initializes a new BlockRegistry.
func NewBlockRegistry() *BlockRegistry {
	reg := &BlockRegistry{
		namesByRID: make(map[uint32]string),
		ridsByName: make(map[string]uint32),
	}
	reg.SetActualAirRID(0)
	return reg
}

// IsLoaded returns whether the full block palette dictionary is currently loaded in memory.
func (r *BlockRegistry) IsLoaded() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loaded
}

// ActualAirRID returns the active network runtime ID used for minecraft:air.
func (r *BlockRegistry) ActualAirRID() uint32 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.actualAirRID
}

// SetActualAirRID updates and preserves the actual air runtime ID.
func (r *BlockRegistry) SetActualAirRID(rid uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actualAirRID = rid
	r.namesByRID[rid] = "minecraft:air"
	r.ridsByName["minecraft:air"] = rid
	r.ridsByName["air"] = rid
	if rid != 0 {
		r.namesByRID[0] = "minecraft:air"
	}
}

// Unload unloads the full block palette dictionary from memory, retaining only the actual air runtime ID.
func (r *BlockRegistry) Unload() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loaded = false
	airRID := r.actualAirRID

	r.namesByRID = make(map[uint32]string, 2)
	r.ridsByName = make(map[string]uint32, 2)

	r.namesByRID[airRID] = "minecraft:air"
	r.ridsByName["minecraft:air"] = airRID
	r.ridsByName["air"] = airRID
	if airRID != 0 {
		r.namesByRID[0] = "minecraft:air"
	}
}

// Version returns the detected Bedrock version of the loaded palette data.
func (r *BlockRegistry) Version() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}

// Register maps a runtimeID to a namespaced block name.
func (r *BlockRegistry) Register(runtimeID uint32, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	clean := name
	if !strings.HasPrefix(clean, "minecraft:") && !strings.Contains(clean, ":") {
		clean = "minecraft:" + clean
	}
	r.namesByRID[runtimeID] = clean
	r.ridsByName[clean] = runtimeID
	r.ridsByName[strings.TrimPrefix(clean, "minecraft:")] = runtimeID
}

// ResolveName looks up the block name corresponding to a runtime ID.
func (r *BlockRegistry) ResolveName(runtimeID uint32) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	name, ok := r.namesByRID[runtimeID]
	return name, ok
}

// ResolveRID looks up the runtime ID for a given block name.
func (r *BlockRegistry) ResolveRID(name string) (uint32, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	clean := strings.ToLower(name)
	if rid, ok := r.ridsByName[clean]; ok {
		return rid, true
	}
	cleanWithPrefix := clean
	if !strings.Contains(cleanWithPrefix, ":") {
		cleanWithPrefix = "minecraft:" + cleanWithPrefix
	}
	if rid, ok := r.ridsByName[cleanWithPrefix]; ok {
		return rid, true
	}

	// If server uses hashes and registry is loaded, compute on the fly
	if r.loaded && r.useHashes {
		rid := HashBlockState(cleanWithPrefix, nil)
		return rid, true
	}

	return 0, false
}

// Count returns the number of registered runtime ID mappings.
func (r *BlockRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.loaded {
		return 0
	}
	return len(r.namesByRID)
}

// InitFromGameData initializes standard vanilla and custom block entries.
func (r *BlockRegistry) InitFromGameData(useHashes bool, customBlocks []protocol.BlockEntry, customPaths ...string) {
	r.mu.Lock()
	r.useHashes = useHashes
	if useHashes {
		r.actualAirRID = HashBlockState("minecraft:air", nil)
	} else {
		r.actualAirRID = 0
	}
	r.loaded = true
	r.mu.Unlock()

	// Always register 0 as minecraft:air fallback
	r.Register(0, "minecraft:air")

	// If using hashes, register mathematically certain air state
	if useHashes {
		r.Register(HashBlockState("minecraft:air", nil), "minecraft:air")
	}

	// Register custom blocks sent by the server in StartGame
	if useHashes {
		for _, cb := range customBlocks {
			rid := HashBlockState(cb.Name, cb.Properties)
			r.Register(rid, cb.Name)
		}
	} else {
		for i, cb := range customBlocks {
			r.Register(uint32(i+1), cb.Name)
		}
	}

	// 1. First priority: Check custom paths (from --block-palette CLI flag or config file)
	for _, cp := range customPaths {
		if cp != "" && r.LoadPaletteFile(cp, useHashes) {
			return
		}
	}

	// 2. Second priority: Standard fallback search paths
	defaultSearchPaths := []string{
		"block_palette.nbt",
		"block_palette.json",
		"block_hashes.json",
		"data/block_palette.nbt",
		"data/block_palette.json",
		"data/block_states.json",
		"data/block_hashes.json",
	}
	for _, path := range defaultSearchPaths {
		if r.LoadPaletteFile(path, useHashes) {
			break
		}
	}
}

// NBTPaletteEntry represents a single block permutation in standard Bedrock block_palette.nbt
type NBTPaletteEntry struct {
	Name      string         `nbt:"name"`
	NetworkID int32          `nbt:"network_id"`
	BlockID   int32          `nbt:"block_id"`
	States    map[string]any `nbt:"states"`
}

type NBTBlockPalette struct {
	Blocks []NBTPaletteEntry `nbt:"blocks"`
}

// LoadPaletteFile loads palette entries from an external NBT or JSON file.
func (r *BlockRegistry) LoadPaletteFile(filePath string, useHashes bool) bool {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return false
	}

	// Check if data is GZIP compressed (0x1f, 0x8b)
	payload := data
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		if gz, err := gzip.NewReader(bytes.NewReader(data)); err == nil {
			if decompressed, err := io.ReadAll(gz); err == nil && len(decompressed) > 0 {
				payload = decompressed
			}
			_ = gz.Close()
		}
	}

	// 1. Try NBT format (Cloudburst / Geyser / Altay block_palette.nbt)
	for _, enc := range []nbt.Encoding{nbt.BigEndian, nbt.NetworkLittleEndian, nbt.LittleEndian} {
		var raw map[string]any
		if err := nbt.UnmarshalEncoding(payload, &raw, enc); err == nil {
			if blocksList, ok := raw["blocks"].([]any); ok && len(blocksList) > 0 {
				for i, item := range blocksList {
					entry, ok := item.(map[string]any)
					if !ok {
						continue
					}
					if r.version == "" {
						if v, ok := entry["version"].(int32); ok {
							major := (v >> 24) & 0xFF
							minor := (v >> 16) & 0xFF
							patch := (v >> 8) & 0xFF
							rev := v & 0xFF
							r.version = fmt.Sprintf("%d.%d.%d.%d", major, minor, patch, rev)
						}
					}
					name, _ := entry["name"].(string)
					var rid uint32
					if useHashes {
						if netID, ok := entry["network_id"].(int32); ok {
							rid = uint32(netID)
						} else if netID64, ok := entry["network_id"].(int64); ok {
							rid = uint32(netID64)
						} else {
							rid = uint32(i)
						}
					} else {
						if bID, ok := entry["block_id"].(int32); ok && bID != 0 {
							rid = uint32(bID)
						} else if bID64, ok := entry["block_id"].(int64); ok && bID64 != 0 {
							rid = uint32(bID64)
						} else {
							rid = uint32(i)
						}
					}
					r.Register(rid, name)
				}
				log.Printf("bebot: loaded %d block states from NBT %s\n", len(blocksList), filePath)
				r.mu.Lock()
				r.loaded = true
				r.mu.Unlock()
				return true
			}
		}
	}

	// 2. Try Allay JSON format [{"blockStateHash": 123, "name": "..."}]
	type allayEntry struct {
		Name           string `json:"name"`
		BlockStateHash int64  `json:"blockStateHash"`
	}
	var allayFormat []allayEntry
	if err := json.Unmarshal(data, &allayFormat); err == nil && len(allayFormat) > 0 {
		for _, e := range allayFormat {
			r.Register(uint32(e.BlockStateHash), e.Name)
		}
		log.Printf("bebot: loaded %d block states from Allay JSON %s\n", len(allayFormat), filePath)
		r.mu.Lock()
		r.loaded = true
		r.mu.Unlock()
		return true
	}

	// 3. Try bedrock-crustaceans JSON format {"973836165": ["minecraft:cyan_terracotta", {...}]}
	var crustaceanFormat map[string][]any
	if err := json.Unmarshal(data, &crustaceanFormat); err == nil && len(crustaceanFormat) > 0 {
		count := 0
		for k, v := range crustaceanFormat {
			if len(v) > 0 {
				if name, ok := v[0].(string); ok {
					if id, err := strconv.ParseUint(k, 10, 32); err == nil {
						r.Register(uint32(id), name)
						count++
					}
				}
			}
		}
		if count > 0 {
			log.Printf("bebot: loaded %d block states from block_hashes JSON %s\n", count, filePath)
			r.mu.Lock()
			r.loaded = true
			r.mu.Unlock()
			return true
		}
	}

	// 3. Try map format {"123": "minecraft:stone"}
	var mapFormat map[string]string
	if err := json.Unmarshal(data, &mapFormat); err == nil && len(mapFormat) > 0 {
		count := 0
		for k, v := range mapFormat {
			if id, err := strconv.ParseUint(k, 10, 32); err == nil {
				r.Register(uint32(id), v)
				count++
			}
		}
		if count > 0 {
			log.Printf("bebot: loaded %d block states from map JSON %s\n", count, filePath)
			r.mu.Lock()
			r.loaded = true
			r.mu.Unlock()
			return true
		}
	}

	// 4. Try list format [{"id": 123, "name": "..."}]
	type entry struct {
		Name string `json:"name"`
		ID   uint32 `json:"id"`
	}
	var listFormat []entry
	if err := json.Unmarshal(data, &listFormat); err == nil && len(listFormat) > 0 {
		for _, e := range listFormat {
			r.Register(e.ID, e.Name)
		}
		log.Printf("bebot: loaded %d block states from list JSON %s\n", len(listFormat), filePath)
		r.mu.Lock()
		r.loaded = true
		r.mu.Unlock()
		return true
	}

	return false
}
