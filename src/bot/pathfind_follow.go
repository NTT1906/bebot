package bot

import (
	"fmt"
	"log"
	"math"
	"strings"

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
	// Auto-enable chunk cache if not already active to provide authentic voxel queries
	if b.chunkCache != nil && !b.chunkCache.IsEnabled() {
		b.mu.Lock()
		curPos := b.pos
		b.mu.Unlock()
		subPos := protocol.SubChunkPos{int32(curPos.X()) >> 4, int32(curPos.Y()) >> 4, int32(curPos.Z()) >> 4}
		b.chunkCache.Enable(subPos)
		missing := b.chunkCache.MissingOffsets()
		if len(missing) > 0 {
			b.requestSubChunks(subPos, missing)
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

	if playerName != "" {
		b.reply(fmt.Sprintf("[Bebot] Planning path to %s at <%d, %d, %d>...", playerName, target.X(), target.Y(), target.Z()))
	} else {
		b.reply(fmt.Sprintf("[Bebot] Planning path to <%d, %d, %d>...", target.X(), target.Y(), target.Z()))
	}

	res, _ := b.pathPlanner.Plan(wv, startNode, goalNode, pathfind.DefaultSearchOptions())
	if len(res.Nodes) == 0 {
		b.reply(fmt.Sprintf("[Bebot] No path found to <%d, %d, %d>.", target.X(), target.Y(), target.Z()))
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
	b.pathLastPos = b.pos
	b.mode = modeMoving
	b.followTarget = ""
	b.mimicTarget = ""
	b.lookAtTarget = ""
	b.lookAtPos = nil

	// Set immediate target to first node
	firstWP := res.Nodes[0]
	b.moveTarget = mgl32.Vec3{float32(firstWP.X()) + 0.5, float32(firstWP.Y()), float32(firstWP.Z()) + 0.5}
	b.mu.Unlock()

	if res.ReachedGoal {
		b.reply(fmt.Sprintf("[Bebot] Path found: %d nodes (length: %.1fm). Following...", len(res.Nodes), res.Cost))
	} else {
		lastNode := res.Nodes[len(res.Nodes)-1]
		dist := math.Sqrt(float64(
			(lastNode.X()-target.X())*(lastNode.X()-target.X()) +
				(lastNode.Y()-target.Y())*(lastNode.Y()-target.Y()) +
				(lastNode.Z()-target.Z())*(lastNode.Z()-target.Z()),
		))
		b.reply(fmt.Sprintf("[Bebot] Exact target unreachable. Following best-effort path to <%d, %d, %d> (dist: %.1fm, %d nodes).",
			lastNode.X(), lastNode.Y(), lastNode.Z(), dist, len(res.Nodes)))
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
	if b.mode == modeMoving && wasActive {
		b.mode = modeIdle
		b.moving = false
	}
	return wasActive
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

	// Dynamic player tracking: replan if player moves > 3 blocks away
	if b.pathTargetName != "" {
		if targetPlayer, ok := b.players[strings.ToLower(b.pathTargetName)]; ok && (targetPlayer.id != 0 || targetPlayer.hasLocation) {
			playerPos := vec3ToBlockPos(targetPlayer.position)
			dx := playerPos.X() - b.pathTargetPos.X()
			dz := playerPos.Z() - b.pathTargetPos.Z()
			if dx*dx+dz*dz > 9 {
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
					b.pathIndex = 0
					b.pathStuckTicks = 0
				}
			}
		}
	}

	// Completion check
	if b.pathIndex >= len(b.pathNodes) {
		b.pathActive = false
		b.mode = modeIdle
		b.moving = false
		target := b.pathTargetPos
		b.mu.Unlock()
		return false, fmt.Sprintf("[Bebot] Destination reached! (<%d, %d, %d>)", target.X(), target.Y(), target.Z())
	}

	curWP := b.pathNodes[b.pathIndex]
	targetVec := mgl32.Vec3{float32(curWP.X()) + 0.5, float32(curWP.Y()), float32(curWP.Z()) + 0.5}

	dx := targetVec.X() - b.pos.X()
	dy := targetVec.Y() - b.pos.Y()
	dz := targetVec.Z() - b.pos.Z()
	hDist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
	vDist := float32(math.Abs(float64(dy)))

	// Check if waypoint reached: horizontal < 0.35 blocks and vertical < 0.8 blocks
	if hDist < 0.35 && vDist < 0.8 {
		b.pathIndex++
		if b.pathIndex >= len(b.pathNodes) {
			b.pathActive = false
			b.mode = modeIdle
			b.moving = false
			b.mu.Unlock()
			return false, fmt.Sprintf("[Bebot] Destination reached! (<%d, %d, %d>)", curWP.X(), curWP.Y(), curWP.Z())
		}
		curWP = b.pathNodes[b.pathIndex]
		targetVec = mgl32.Vec3{float32(curWP.X()) + 0.5, float32(curWP.Y()), float32(curWP.Z()) + 0.5}
		dx = targetVec.X() - b.pos.X()
		dy = targetVec.Y() - b.pos.Y()
		dz = targetVec.Z() - b.pos.Z()
		hDist = float32(math.Sqrt(float64(dx*dx + dz*dz)))
	}

	// Update movement destination and mode
	b.moveTarget = targetVec
	b.mode = modeMoving

	// Step-up jump check: trigger jump when approaching a +1 Y elevation transition
	if b.pathIndex < len(b.pathMoves) {
		move := b.pathMoves[b.pathIndex]
		if move.Type == pathfind.MoveStepUp {
			if hDist <= 0.8 && b.pos.Y() < float32(curWP.Y()) {
				jump = true
			}
		}
	}

	// Anti-stuck detection
	progress := b.pos.Sub(b.pathLastPos)
	progressLen := float32(math.Sqrt(float64(progress.Dot(progress))))
	if progressLen < 0.02 {
		b.pathStuckTicks++
	} else {
		b.pathStuckTicks = 0
		b.pathLastPos = b.pos
	}

	// If stuck for 20 ticks (1 second), trigger replan
	if b.pathStuckTicks >= 20 {
		log.Printf("bebot: stuck at (%.2f, %.2f, %.2f) following path, replanning to %v\n",
			b.pos.X(), b.pos.Y(), b.pos.Z(), b.pathTargetPos)
		b.pathStuckTicks = 0
		target := b.pathTargetPos
		botPos := b.pos
		planner := b.pathPlanner
		b.mu.Unlock()

		wv := NewBotWorldView(b)
		startNode := wv.FindStandingNode(vec3ToBlockPos(botPos))
		goalNode := wv.FindGoalNode(target)
		res, _ := planner.Plan(wv, startNode, goalNode, pathfind.DefaultSearchOptions())

		b.mu.Lock()
		if len(res.Nodes) > 0 && b.pathActive {
			b.pathNodes = res.Nodes
			b.pathMoves = res.Moves
			b.pathIndex = 0
			firstWP := res.Nodes[0]
			b.moveTarget = mgl32.Vec3{float32(firstWP.X()) + 0.5, float32(firstWP.Y()), float32(firstWP.Z()) + 0.5}
			b.mu.Unlock()
			return jump, ""
		} else {
			b.pathActive = false
			b.mode = modeIdle
			b.moving = false
			b.mu.Unlock()
			return false, "[Bebot] Path blocked! Could not find detour to destination."
		}
	}

	b.mu.Unlock()
	return jump, ""
}
