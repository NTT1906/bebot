package pathfind

import (
	"math"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// Sqrt2 is the diagonal horizontal step cost.
const Sqrt2 = 1.41421356237309504880

// DefaultVerticalCost is the default vertical elevation factor in the heuristic.
const DefaultVerticalCost = 1.2

// Octile3D calculates the default admissible 3D diagonal distance heuristic.
// Because the player can move diagonally on the XZ plane while stepping vertically,
// octile distance strictly bounds the remaining travel time without overestimating.
func Octile3D(a, b protocol.BlockPos) float64 {
	return octile3D(a, b, Sqrt2, DefaultVerticalCost)
}

// octile3D is Octile3D with configurable diagonal and vertical costs.
func octile3D(a, b protocol.BlockPos, diagonalCost, verticalCost float64) float64 {
	dx := math.Abs(float64(a.X() - b.X()))
	dy := math.Abs(float64(a.Y() - b.Y()))
	dz := math.Abs(float64(a.Z() - b.Z()))

	hMin := math.Min(dx, dz)
	hMax := math.Max(dx, dz)

	// Horizontal diagonal step cost + remaining straight cardinal steps
	hDist := (diagonalCost-1.0)*hMin + hMax

	// Vertical elevation cost (jumping or descending costs ~1.2x to 1.5x horizontal time)
	return hDist + verticalCost*dy
}

// Heuristic calculates the distance heuristic using these weights.
func (w Weights) Heuristic(a, b protocol.BlockPos) float64 {
	w = w.normalized()
	return octile3D(a, b, w.WalkDiagonal, w.Vertical)
}
