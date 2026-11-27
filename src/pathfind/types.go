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

// Weights holds the relative costs of navigation moves. Non-positive values
// are treated as unset and replaced with the built-in defaults.
type Weights struct {
	WalkCardinal float64 // Level cardinal step
	WalkDiagonal float64 // Level diagonal step
	StepUp       float64 // +1 Y jump
	Drop1        float64 // Falling 1 block
	Drop2        float64 // Falling 2 blocks
	Drop3        float64 // Falling 3 blocks
	WaterPenalty float64 // Extra cost for passing through water
	Vertical     float64 // Vertical cost factor in the distance heuristic
}

// DefaultWeights returns the calibrated Bedrock movement costs.
func DefaultWeights() Weights {
	return Weights{
		WalkCardinal: CostWalkCardinal,
		WalkDiagonal: CostWalkDiagonal,
		StepUp:       CostStepUp,
		Drop1:        CostDrop1,
		Drop2:        CostDrop2,
		Drop3:        CostDrop3,
		WaterPenalty: CostWaterPenalty,
		Vertical:     DefaultVerticalCost,
	}
}

// normalized fills unset/invalid weights with their defaults.
func (w Weights) normalized() Weights {
	d := DefaultWeights()
	if w.WalkCardinal <= 0 {
		w.WalkCardinal = d.WalkCardinal
	}
	if w.WalkDiagonal <= 0 {
		w.WalkDiagonal = d.WalkDiagonal
	}
	if w.StepUp <= 0 {
		w.StepUp = d.StepUp
	}
	if w.Drop1 <= 0 {
		w.Drop1 = d.Drop1
	}
	if w.Drop2 <= 0 {
		w.Drop2 = d.Drop2
	}
	if w.Drop3 <= 0 {
		w.Drop3 = d.Drop3
	}
	if w.WaterPenalty <= 0 {
		w.WaterPenalty = d.WaterPenalty
	}
	if w.Vertical <= 0 {
		w.Vertical = d.Vertical
	}
	return w
}

// SearchOptions configures the A* path planning engine.
type SearchOptions struct {
	MaxNodes       int           // Maximum node expansions before best-effort fallback (default 5000)
	Timeout        time.Duration // Time budget for the search (default 50ms)
	AllowDiagonals bool          // Allow diagonal horizontal walking (default false)
	AllowDrops     bool          // Allow -1..-3 Y drops off ledges (default true)
	Weights        Weights       // Movement costs; zero values use defaults
}

// normalized fills unset/invalid search limits with their defaults.
func (o SearchOptions) normalized() SearchOptions {
	if o.MaxNodes <= 0 {
		o.MaxNodes = 5000
	}
	if o.Timeout <= 0 {
		o.Timeout = 50 * time.Millisecond
	}
	o.Weights = o.Weights.normalized()
	return o
}

// DefaultSearchOptions returns production-ready search limits.
func DefaultSearchOptions() SearchOptions {
	return SearchOptions{
		MaxNodes:       5000,
		Timeout:        50 * time.Millisecond,
		AllowDiagonals: false, // Cardinal-only navigation prevents 0.6x0.6 bounding box from cutting into wall corners
		AllowDrops:     true,
		Weights:        DefaultWeights(),
	}
}
