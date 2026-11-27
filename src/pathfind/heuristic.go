package pathfind

import (
	"math"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// Sqrt2 is the diagonal horizontal step cost.
const Sqrt2 = 1.41421356237309504880

// Octile3D calculates an admissible 3D diagonal distance heuristic.
// Because the player can move diagonally on the XZ plane while stepping vertically,
// octile distance strictly bounds the remaining travel time without overestimating.
func Octile3D(a, b protocol.BlockPos) float64 {
	dx := math.Abs(float64(a.X() - b.X()))
	dy := math.Abs(float64(a.Y() - b.Y()))
	dz := math.Abs(float64(a.Z() - b.Z()))

	hMin := math.Min(dx, dz)
	hMax := math.Max(dx, dz)

	// Horizontal diagonal step cost + remaining straight cardinal steps
	hDist := (Sqrt2-1.0)*hMin + hMax

	// Vertical elevation cost (jumping or descending costs ~1.2x to 1.5x horizontal time)
	return hDist + 1.2*dy
}
