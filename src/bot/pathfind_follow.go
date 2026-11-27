package bot

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"bebot/src/pathfind"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// StartPath initiates an autonomous 3D path search and begins following it.
func (b *Bot) StartPath(target protocol.BlockPos, playerName string) {
	b.startPath(target, playerName)
}

// StopPath stops any active pathfinding navigation and halts movement.
func (b *Bot) StopPath() bool {
	return b.stopPath()
}

// IsPathActive returns true if the bot is currently navigating a path.
func (b *Bot) IsPathActive() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pathActive
}

// startPath plans and starts following a 3D path toward target.
func (b *Bot) startPath(target protocol.BlockPos, playerName string) {
	b.mu.Lock()
	curPos := b.pos
	b.mu.Unlock()
	subPos := protocol.SubChunkPos{
		int32(math.Floor(float64(curPos.X()))) >> 4,
		int32(math.Floor(float64(curPos.Y()))) >> 4,
		int32(math.Floor(float64(curPos.Z()))) >> 4,
	}

	// Auto-enable chunk cache if not already active to provide authentic voxel queries
	if b.chunkCache != nil {
		if !b.chunkCache.IsEnabled() {
			b.chunkCache.Enable(subPos)
		} else if b.chunkCache.Center() != subPos {
			b.chunkCache.ShiftCenter(subPos)
		}
		missing := b.chunkCache.MissingOffsets()
		if len(missing) > 0 {
			b.requestSubChunks(subPos, missing)
		}
	}

	// If the subchunk around the bot's current standing position is not loaded yet, wait briefly for incoming packets
	if b.chunkCache != nil && !b.chunkCache.HasSubChunk(subPos) {
		b.reply("[Bebot] Loading terrain chunks around bot...")
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			if b.chunkCache.HasSubChunk(subPos) {
				break
			}
		}
	}

	wv := NewBotWorldView(b)
	b.mu.Lock()
	if b.pathPlanner == nil {
		b.pathPlanner = pathfind.NewPlanner()
	}
	startVec := b.pos
	b.mu.Unlock()

	startNode := wv.FindStandingNode(vec3ToBlockPos(startVec))
	goalNode := wv.FindGoalNode(target)

	if startNode == goalNode {
		b.reply(fmt.Sprintf("[Bebot] Already at destination! (<%d, %d, %d>)", target.X(), target.Y(), target.Z()))
		return
	}

	if playerName != "" {
		b.reply(fmt.Sprintf("[Bebot] Planning path to %s at <%d, %d, %d>...", playerName, target.X(), target.Y(), target.Z()))
	} else {
		b.reply(fmt.Sprintf("[Bebot] Planning path to <%d, %d, %d>...", target.X(), target.Y(), target.Z()))
	}

	res, _ := b.pathPlanner.Plan(wv, startNode, goalNode, pathfind.DefaultSearchOptions())
	if len(res.Nodes) <= 1 {
		b.reply(fmt.Sprintf("[Bebot] Cannot find a path to <%d, %d, %d> (blocked or terrain not loaded).", target.X(), target.Y(), target.Z()))
		return
	}

	b.mu.Lock()
	b.pathActive = true
	b.pathMoves = res.Moves
	b.pathNodes = res.Nodes
	b.pathIndex = 0
	b.pathTargetPos = target
	b.pathTargetName = playerName
	b.pathStuckTicks = 0
	b.pathNodeTicks = 0
	b.pathReplanCount = 0
	tDx := float64(target.X()) + 0.5 - float64(b.pos.X())
	tDy := float64(target.Y()) + 0.5 - float64(b.pos.Y())
	tDz := float64(target.Z()) + 0.5 - float64(b.pos.Z())
	b.pathTargetMinDist = math.Sqrt(tDx*tDx + tDy*tDy + tDz*tDz)
	b.pathLastPos = b.pos
	b.mode = modeMoving
	b.followTarget = ""
	b.mimicTarget = ""
	b.lookAtTarget = ""
	b.lookAtPos = nil

	// If starting node is already occupied by the bot, start navigating directly towards the next waypoint
	wpIdx := 0
	if len(res.Nodes) > 1 {
		wpIdx = 1
	}
	b.pathIndex = wpIdx
	curWP := res.Nodes[wpIdx]
	b.moveTarget = mgl32.Vec3{float32(curWP.X()) + 0.5, float32(curWP.Y()), float32(curWP.Z()) + 0.5}
	b.mu.Unlock()

	log.Printf("bebot: path planned to <%d, %d, %d> (%d nodes, cost %.1f): %v\n",
		target.X(), target.Y(), target.Z(), len(res.Nodes), res.Cost, res.Nodes)

	if res.ReachedGoal {
		b.reply(fmt.Sprintf("[Bebot] Path found: %d nodes (length: %.1fm). Navigating...", len(res.Nodes), res.Cost))
	} else {
		lastNode := res.Nodes[len(res.Nodes)-1]
		dist := math.Sqrt(float64(
			(lastNode.X()-target.X())*(lastNode.X()-target.X()) +
				(lastNode.Y()-target.Y())*(lastNode.Y()-target.Y()) +
				(lastNode.Z()-target.Z())*(lastNode.Z()-target.Z()),
		))
		b.reply(fmt.Sprintf("[Bebot] Planning segment 1 towards <%d, %d, %d> (%d nodes to chunk edge, %.1fm remaining). Navigating...",
			target.X(), target.Y(), target.Z(), len(res.Nodes), dist))
	}
}

// stopPath cancels active pathfinding and restores idle mode.
func (b *Bot) stopPath() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	wasActive := b.pathActive
	b.pathActive = false
	b.pathMoves = nil
	b.pathNodes = nil
	b.pathIndex = 0
	b.pathTargetName = ""
	b.pathStuckTicks = 0
	b.pathNodeTicks = 0
	b.pathReplanCount = 0
	if b.mode == modeMoving && wasActive {
		b.mode = modeIdle
		b.moving = false
	}
	return wasActive
}

// formatFeetDebugMap constructs a 9x9 top-down ASCII map at bot feet level.
// Legend: bot '=', target '+', air 'o', else 'x'.
func formatFeetDebugMap(wv pathfind.WorldView, botFeet protocol.BlockPos, target protocol.BlockPos) string {
	var sb strings.Builder
	bx := botFeet.X()
	by := botFeet.Y()
	bz := botFeet.Z()

	sb.WriteString(fmt.Sprintf("[Bebot] Feet debug map (Y=%d, center <%d,%d>):\n", by, bx, bz))
	for dz := int32(-4); dz <= 4; dz++ {
		for dx := int32(-4); dx <= 4; dx++ {
			pos := protocol.BlockPos{bx + dx, by, bz + dz}
			if dx == 0 && dz == 0 {
				sb.WriteByte('=')
			} else if pos.X() == target.X() && pos.Z() == target.Z() {
				sb.WriteByte('+')
			} else if wv != nil && wv.GetBlockType(pos) == pathfind.BlockAir {
				sb.WriteByte('o')
			} else {
				sb.WriteByte('x')
			}
		}
		if dz < 4 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// pathFollowTick updates path waypoint tracking and kinematic jumps.
// Manages internal synchronization safely without holding locks during A* planner searches.
// Returns whether jump input should be triggered, and an optional status message to reply.
func (b *Bot) pathFollowTick(c *minecraft.Conn) (jump bool, replyMsg string) {
	b.mu.Lock()
	if !b.pathActive || len(b.pathNodes) == 0 {
		b.mu.Unlock()
		return false, ""
	}

	// Dynamic player tracking: replan if player moves > 3 blocks away or changes elevation in 3D
	if b.pathTargetName != "" {
		if targetPlayer, ok := b.players[strings.ToLower(b.pathTargetName)]; ok && (targetPlayer.id != 0 || targetPlayer.hasLocation) {
			playerPos := vec3ToBlockPos(targetPlayer.position)
			dx := playerPos.X() - b.pathTargetPos.X()
			dy := playerPos.Y() - b.pathTargetPos.Y()
			dz := playerPos.Z() - b.pathTargetPos.Z()
			if dx*dx+dy*dy+dz*dz > 9 || math.Abs(float64(dy)) >= 2 {
				target := playerPos
				botPos := b.pos
				planner := b.pathPlanner
				b.pathTargetPos = target
				b.mu.Unlock()

				wv := NewBotWorldView(b)
				startNode := wv.FindStandingNode(vec3ToBlockPos(botPos))
				goalNode := wv.FindGoalNode(target)
				res, _ := planner.Plan(wv, startNode, goalNode, pathfind.DefaultSearchOptions())

				b.mu.Lock()
				if len(res.Nodes) > 0 && b.pathActive {
					b.pathNodes = res.Nodes
					b.pathMoves = res.Moves
					wpIdx := 0
					if len(res.Nodes) > 1 {
						wpIdx = 1
					}
					b.pathIndex = wpIdx
					b.pathStuckTicks = 0
					b.pathNodeTicks = 0
					b.pathReplanCount = 0
					curWP := res.Nodes[wpIdx]
					b.moveTarget = mgl32.Vec3{float32(curWP.X()) + 0.5, float32(curWP.Y()), float32(curWP.Z()) + 0.5}
				}
			}
		}
	}

	// Completion check for current path segment
	if b.pathIndex >= len(b.pathNodes) {
		return b.onSegmentCompleteLocked()
	}

	curWP := b.pathNodes[b.pathIndex]
	targetVec := mgl32.Vec3{float32(curWP.X()) + 0.5, float32(curWP.Y()), float32(curWP.Z()) + 0.5}

	dx := targetVec.X() - b.pos.X()
	dy := targetVec.Y() - b.pos.Y()
	dz := targetVec.Z() - b.pos.Z()
	hDist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
	vDist := float32(math.Abs(float64(dy)))

	// Check if waypoint reached: horizontal < 0.45 blocks and vertical < 0.8 blocks
	if hDist < 0.45 && vDist < 0.8 {
		b.pathIndex++
		b.pathNodeTicks = 0
		b.pathStuckTicks = 0
		b.pathLastPos = b.pos

		// Only reset replan failure counter when genuine forward progress is made towards the destination
		tDx := float64(b.pathTargetPos.X()) + 0.5 - float64(b.pos.X())
		tDy := float64(b.pathTargetPos.Y()) + 0.5 - float64(b.pos.Y())
		tDz := float64(b.pathTargetPos.Z()) + 0.5 - float64(b.pos.Z())
		curDistToTarget := math.Sqrt(tDx*tDx + tDy*tDy + tDz*tDz)
		if curDistToTarget < b.pathTargetMinDist-0.5 {
			b.pathTargetMinDist = curDistToTarget
			b.pathReplanCount = 0
		}

		if b.pathIndex >= len(b.pathNodes) {
			return b.onSegmentCompleteLocked()
		}
		curWP = b.pathNodes[b.pathIndex]
		targetVec = mgl32.Vec3{float32(curWP.X()) + 0.5, float32(curWP.Y()), float32(curWP.Z()) + 0.5}
		dx = targetVec.X() - b.pos.X()
		dy = targetVec.Y() - b.pos.Y()
		dz = targetVec.Z() - b.pos.Z()
		hDist = float32(math.Sqrt(float64(dx*dx + dz*dz)))
		vDist = float32(math.Abs(float64(dy)))
	}

	// Update movement destination and mode
	b.moveTarget = targetVec
	b.mode = modeMoving

	// Step-up jump check: trigger jump when approaching a +1 Y elevation transition
	// The move leading to curWP is at index b.pathIndex - 1
	isStepUpMove := false
	if b.pathIndex > 0 && b.pathIndex-1 < len(b.pathMoves) {
		isStepUpMove = b.pathMoves[b.pathIndex-1].Type == pathfind.MoveStepUp
	}
	isHigherWP := float32(curWP.Y()) > b.pos.Y()+0.3
	if (isStepUpMove || isHigherWP) && hDist <= 1.25 {
		jump = true
	}

	// Anti-stuck and node progress timeout detection
	b.pathNodeTicks++
	progress := b.pos.Sub(b.pathLastPos)
	progressLen := float32(math.Sqrt(float64(progress.Dot(progress))))
	if progressLen < 0.02 {
		b.pathStuckTicks++
	} else {
		b.pathStuckTicks = 0
		b.pathLastPos = b.pos
	}

	// If position is stalled for 25 ticks (1.25s) or failed to reach next node in 50 ticks (2.5s), trigger replan
	if b.pathStuckTicks >= 25 || b.pathNodeTicks >= 50 {
		b.pathReplanCount++
		b.pathStuckTicks = 0
		b.pathNodeTicks = 0
		target := b.pathTargetPos

		// If failed to reach next node after 3 replan attempts, give up and broadcast
		if b.pathReplanCount > 3 {
			b.pathActive = false
			b.mode = modeIdle
			b.moving = false
			b.pathReplanCount = 0
			botPos := b.pos
			b.mu.Unlock()

			wv := NewBotWorldView(b)
			feetMap := formatFeetDebugMap(wv, vec3ToBlockPos(botPos), target)
			return false, fmt.Sprintf("[Bebot] Cannot reach destination <%d, %d, %d>! Failed to make progress after multiple replan attempts, giving up.\n%s",
				target.X(), target.Y(), target.Z(), feetMap)
		}

		log.Printf("bebot: stuck or node timeout at (%.2f, %.2f, %.2f) following path, replan attempt %d/3 to %v\n",
			b.pos.X(), b.pos.Y(), b.pos.Z(), b.pathReplanCount, target)
		botPos := b.pos
		planner := b.pathPlanner
		b.mu.Unlock()

		wv := NewBotWorldView(b)
		startNode := wv.FindStandingNode(vec3ToBlockPos(botPos))
		goalNode := wv.FindGoalNode(target)
		res, _ := planner.Plan(wv, startNode, goalNode, pathfind.DefaultSearchOptions())

		b.mu.Lock()
		if len(res.Nodes) > 1 && b.pathActive {
			b.pathNodes = res.Nodes
			b.pathMoves = res.Moves
			wpIdx := 1
			b.pathIndex = wpIdx
			curWP := res.Nodes[wpIdx]
			b.moveTarget = mgl32.Vec3{float32(curWP.X()) + 0.5, float32(curWP.Y()), float32(curWP.Z()) + 0.5}
			attempt := b.pathReplanCount
			b.mu.Unlock()
			return jump, fmt.Sprintf("[Bebot] Re-pathfinding (attempt %d/3) to <%d, %d, %d>...",
				attempt, target.X(), target.Y(), target.Z())
		} else {
			b.pathActive = false
			b.mode = modeIdle
			b.moving = false
			b.pathReplanCount = 0
			b.mu.Unlock()

			feetMap := formatFeetDebugMap(wv, vec3ToBlockPos(botPos), target)
			return false, fmt.Sprintf("[Bebot] Path blocked! Could not find detour to destination <%d, %d, %d>.\n%s",
				target.X(), target.Y(), target.Z(), feetMap)
		}
	}

	b.mu.Unlock()
	return jump, ""
}

// onSegmentCompleteLocked evaluates whether the final destination has been reached
// in X, Y, and Z, or if a multi-chunk continuation segment should be planned. Must be called with b.mu held;
// releases b.mu before returning.
func (b *Bot) onSegmentCompleteLocked() (jump bool, replyMsg string) {
	if len(b.pathNodes) == 0 {
		b.pathActive = false
		b.mode = modeIdle
		b.moving = false
		b.mu.Unlock()
		return false, ""
	}

	target := b.pathTargetPos
	curBlock := vec3ToBlockPos(b.pos)

	// Distance from bot's current standing position to the destination in 3D: X, Y, and Z
	dx := float64(target.X() - curBlock.X())
	dy := float64(target.Y() - curBlock.Y())
	dz := float64(target.Z() - curBlock.Z())
	hDistToFinal := math.Sqrt(dx*dx + dz*dz)
	vDistToFinal := math.Abs(dy)

	// Reached destination only when staying close to target in X, Y, and Z:
	// horizontal distance <= 1.25 blocks and vertical elevation <= 1.0 block.
	if hDistToFinal <= 1.25 && vDistToFinal <= 1.0 {
		b.pathActive = false
		b.mode = modeIdle
		b.moving = false
		b.mu.Unlock()
		return false, fmt.Sprintf("[Bebot] Destination reached! (<%d, %d, %d>)", target.X(), target.Y(), target.Z())
	}

	// Reached the edge of the previous chunk window or need to ascend/descend;
	// automatically plan the next segment towards b.pathTargetPos.
	botPos := b.pos
	planner := b.pathPlanner
	playerName := b.pathTargetName
	b.mu.Unlock()

	wv := NewBotWorldView(b)
	startNode := wv.FindStandingNode(vec3ToBlockPos(botPos))
	goalNode := wv.FindGoalNode(target)
	nextRes, _ := planner.Plan(wv, startNode, goalNode, pathfind.DefaultSearchOptions())

	b.mu.Lock()
	if len(nextRes.Nodes) > 1 && b.pathActive {
		b.pathNodes = nextRes.Nodes
		b.pathMoves = nextRes.Moves
		b.pathIndex = 1
		b.pathStuckTicks = 0
		b.pathNodeTicks = 0
		b.pathReplanCount = 0
		nextWP := nextRes.Nodes[1]
		tDx := float64(target.X()) + 0.5 - float64(b.pos.X())
		tDy := float64(target.Y()) + 0.5 - float64(b.pos.Y())
		tDz := float64(target.Z()) + 0.5 - float64(b.pos.Z())
		b.pathTargetMinDist = math.Sqrt(tDx*tDx + tDy*tDy + tDz*tDz)
		b.moveTarget = mgl32.Vec3{float32(nextWP.X()) + 0.5, float32(nextWP.Y()), float32(nextWP.Z()) + 0.5}
		b.mu.Unlock()

		if nextRes.ReachedGoal {
			return false, fmt.Sprintf("[Bebot] Destination in sight! Navigating final %d nodes to <%d, %d, %d>...",
				len(nextRes.Nodes), target.X(), target.Y(), target.Z())
		}
		distToFinal := math.Sqrt(dx*dx + dy*dy + dz*dz)
		return false, fmt.Sprintf("[Bebot] Continuing towards <%d, %d, %d> (next segment: %d nodes, %.1fm remaining)...",
			target.X(), target.Y(), target.Z(), len(nextRes.Nodes), distToFinal)
	}

	// No further progress could be made
	b.pathActive = false
	b.mode = modeIdle
	b.moving = false
	botPos = b.pos
	b.mu.Unlock()
	distToFinal := math.Sqrt(dx*dx + dy*dy + dz*dz)
	feetMap := formatFeetDebugMap(wv, vec3ToBlockPos(botPos), target)
	if playerName != "" {
		return false, fmt.Sprintf("[Bebot] Stopped near %s at <%d, %d, %d> (dist: %.1fm).\n%s",
			playerName, curBlock.X(), curBlock.Y(), curBlock.Z(), distToFinal, feetMap)
	}
	return false, fmt.Sprintf("[Bebot] Cannot progress further towards <%d, %d, %d> (reached <%d, %d, %d>, %.1fm away, blocked or unmapped terrain).\n%s",
		target.X(), target.Y(), target.Z(), curBlock.X(), curBlock.Y(), curBlock.Z(), distToFinal, feetMap)
}
