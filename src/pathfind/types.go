package pathfind

import (
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// BlockType characterizes voxel properties for spatial navigation.
type BlockType uint8

const (
	// BlockAir indicates a passable, non-solid block (air, cave air, void).
	BlockAir BlockType = iota
	// BlockSolid indicates an impassable, solid obstacle that provides ground support.
	BlockSolid
	// BlockLiquidWater indicates water (passable with swimming slowdown).
	BlockLiquidWater
	// BlockLiquidLava indicates lethal lava (impassable/extreme penalty).
	BlockLiquidLava
	// BlockHazard indicates hazardous blocks (cactus, fire, sweet berry, magma).
	BlockHazard
)

// WorldView provides read-only voxel querying for pathfinding algorithms.
type WorldView interface {
	GetBlockType(pos protocol.BlockPos) BlockType
}

// MoveType identifies the kinematic category of a move transition.
type MoveType uint8

const (
	MoveWalkCardinal MoveType = iota
	MoveWalkDiagonal
	MoveStepUp
	MoveDropDown
)

// String returns human-readable name of the move type.
func (m MoveType) String() string {
	switch m {
	case MoveWalkCardinal:
		return "WalkCardinal"
	case MoveWalkDiagonal:
		return "WalkDiagonal"
	case MoveStepUp:
		return "StepUp"
	case MoveDropDown:
		return "DropDown"
	default:
		return "Unknown"
	}
}

// Move represents an atomic transition from one standing voxel to an adjacent one.
type Move struct {
	From protocol.BlockPos
	To   protocol.BlockPos
	Type MoveType
	Cost float64
}

// PathResult encapsulates the generated trajectory and search metadata.
type PathResult struct {
	Moves       []Move
	Nodes       []protocol.BlockPos
	Cost        float64
	ReachedGoal bool
}

// SearchOptions configures the A* path planning engine.
type SearchOptions struct {
	MaxNodes       int           // Maximum node expansions before best-effort fallback (default 5000)
	Timeout        time.Duration // Time budget for the search (default 50ms)
	AllowDiagonals bool          // Allow diagonal horizontal walking (default true)
	AllowDrops     bool          // Allow -1..-3 Y drops off ledges (default true)
}

// DefaultSearchOptions returns production-ready search limits.
func DefaultSearchOptions() SearchOptions {
	return SearchOptions{
		MaxNodes:       5000,
		Timeout:        50 * time.Millisecond,
		AllowDiagonals: true,
		AllowDrops:     true,
	}
}
