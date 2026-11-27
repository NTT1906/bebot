package pathfind

import (
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// Base move costs calibrated against Bedrock player physics (20 TPS).
const (
	CostWalkCardinal = 1.00
	CostWalkDiagonal = Sqrt2 // ~1.414
	CostStepUp       = 1.60  // Jump initiation + step up
	CostDrop1        = 1.10  // Falling 1 block
	CostDrop2        = 1.60  // Falling 2 blocks
	CostDrop3        = 2.20  // Falling 3 blocks
	CostWaterPenalty = 0.20  // Mild swimming drag penalty (prefers short water shortcuts over land detours)
)

// cardinalOffsets define the 4 horizontal cardinal directions.
var cardinalOffsets = [4][2]int32{
	{0, -1}, // North (-Z)
	{0, 1},  // South (+Z)
	{-1, 0}, // West  (-X)
	{1, 0},  // East  (+X)
}

// diagonalOffsets define the 4 horizontal diagonal directions.
var diagonalOffsets = [4][2]int32{
	{-1, -1}, // North-West
	{1, -1},  // North-East
	{-1, 1},  // South-West
	{1, 1},   // South-East
}

// isPassable returns true if the block does not obstruct the player's bounding box.
func isPassable(t BlockType) bool {
	return t == BlockAir || t == BlockLiquidWater
}

// isStandableSurface returns true if the block provides a solid ground surface.
func isStandableSurface(t BlockType) bool {
	return t == BlockSolid
}

// isStandingPosValid checks whether a coordinate satisfies player navigation:
// 1. Feet voxel is passable (BlockAir or BlockLiquidWater)
// 2. Head voxel (Y+1) is passable (BlockAir or BlockLiquidWater)
// 3. If feet is in water, player can float/swim regardless of water depth.
//    If feet is in air, floor voxel (Y-1) must be solid ground.
func isStandingPosValid(w WorldView, pos protocol.BlockPos) bool {
	feet := w.GetBlockType(pos)
	if !isPassable(feet) {
		return false
	}
	head := w.GetBlockType(protocol.BlockPos{pos.X(), pos.Y() + 1, pos.Z()})
	if !isPassable(head) {
		return false
	}
	if feet == BlockLiquidWater {
		return true // Swimming / floating across water of any depth
	}
	floor := w.GetBlockType(protocol.BlockPos{pos.X(), pos.Y() - 1, pos.Z()})
	return isStandableSurface(floor)
}

// GetNeighbors computes all physically legal moves from the current standing voxel.
func GetNeighbors(w WorldView, curr protocol.BlockPos, opts SearchOptions) []Move {
	// Pre-allocate buffer for maximum expected transitions:
	// 4 cardinal + 4 diagonal + 4 step-up + 12 drops = 24 moves
	moves := make([]Move, 0, 24)

	currX := curr.X()
	currY := curr.Y()
	currZ := curr.Z()

	// -------------------------------------------------------------------------
	// 1. Cardinal Walks & Step-Ups & Drops
	// -------------------------------------------------------------------------
	for _, off := range cardinalOffsets {
		nx := currX + off[0]
		nz := currZ + off[1]
		targetLevel := protocol.BlockPos{nx, currY, nz}

		// A. Level Cardinal Walk (Y = 0)
		if isStandingPosValid(w, targetLevel) {
			cost := CostWalkCardinal
			if w.GetBlockType(targetLevel) == BlockLiquidWater {
				cost += CostWaterPenalty
			}
			moves = append(moves, Move{
				From: curr,
				To:   targetLevel,
				Type: MoveWalkCardinal,
				Cost: cost,
			})
		} else {
			// B. Step Up (+1 Y Jump)
			// Target floor is at currY (one block above current floor).
			// Clearance requirements:
			// - Target feet (currY+1) must be passable
			// - Target head (currY+2) must be passable
			// - Target floor (currY) must be solid
			// - Current headroom (currY+2 above current feet) must be passable so bot doesn't bump ceiling!
			targetUp := protocol.BlockPos{nx, currY + 1, nz}
			if isStandingPosValid(w, targetUp) {
				currentHeadroom := w.GetBlockType(protocol.BlockPos{currX, currY + 2, currZ})
				if isPassable(currentHeadroom) {
					moves = append(moves, Move{
						From: curr,
						To:   targetUp,
						Type: MoveStepUp,
						Cost: CostStepUp,
					})
				}
			}

			// C. Drop Down (-1 to -3 Y)
			// Stepping off the edge: target feet at currY must be passable air.
			if opts.AllowDrops {
				stepOffBlock := w.GetBlockType(targetLevel)
				stepOffHead := w.GetBlockType(protocol.BlockPos{nx, currY + 1, nz})
				if isPassable(stepOffBlock) && isPassable(stepOffHead) {
					// Check drop depths 1, 2, 3
					for drop := int32(1); drop <= 3; drop++ {
						dropPos := protocol.BlockPos{nx, currY - drop, nz}
						// Air or water column between currY and landing must remain passable
						dropType := w.GetBlockType(dropPos)
						if !isPassable(dropType) {
							break // blocked, cannot drop further
						}
						// Check landing: either solid floor below, OR dropping directly into water!
						landingFloor := w.GetBlockType(protocol.BlockPos{nx, currY - drop - 1, nz})
						if isStandableSurface(landingFloor) || dropType == BlockLiquidWater {
							// Valid landing found!
							var dropCost float64
							switch drop {
							case 1:
								dropCost = CostDrop1
							case 2:
								dropCost = CostDrop2
							case 3:
								dropCost = CostDrop3
							}
							if dropType == BlockLiquidWater {
								dropCost += CostWaterPenalty
							}
							moves = append(moves, Move{
								From: curr,
								To:   dropPos,
								Type: MoveDropDown,
								Cost: dropCost,
							})
							break // Once a floor or water is hit, cannot fall further
						}
					}
				}
			}
		}
	}

	// -------------------------------------------------------------------------
	// 2. Diagonal Walks (with Strict Corner Clearance)
	// -------------------------------------------------------------------------
	if opts.AllowDiagonals {
		for _, off := range diagonalOffsets {
			nx := currX + off[0]
			nz := currZ + off[1]
			targetDiag := protocol.BlockPos{nx, currY, nz}

			if isStandingPosValid(w, targetDiag) {
				// Strict Corner Clearance Rule:
				// To prevent 0.6x0.6 bounding box from clipping corner obstacles,
				// both intermediate cardinal columns must have passable feet and head!
				cardinalA := protocol.BlockPos{nx, currY, currZ}
				cardinalB := protocol.BlockPos{currX, currY, nz}

				cardinalAFeet := w.GetBlockType(cardinalA)
				cardinalAHead := w.GetBlockType(protocol.BlockPos{nx, currY + 1, currZ})

				cardinalBFeet := w.GetBlockType(cardinalB)
				cardinalBHead := w.GetBlockType(protocol.BlockPos{currX, currY + 1, nz})

				if isPassable(cardinalAFeet) && isPassable(cardinalAHead) &&
					isPassable(cardinalBFeet) && isPassable(cardinalBHead) {
					cost := CostWalkDiagonal
					if w.GetBlockType(targetDiag) == BlockLiquidWater {
						cost += CostWaterPenalty
					}
					moves = append(moves, Move{
						From: curr,
						To:   targetDiag,
						Type: MoveWalkDiagonal,
						Cost: cost,
					})
				}
			}
		}
	}

	return moves
}
