package bot

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strings"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func (b *Bot) startBreaking(pos protocol.BlockPos, animate bool, repeat bool) {
	b.breakAnimation = animate
	b.breakRepeat = repeat
	if b.gameMode == 1 || b.gameMode == 4 {
		b.mu.Lock()
		c := b.conn
		entityID := b.entityID
		b.mu.Unlock()
		if c == nil {
			log.Println("bebot: cannot break block: not connected")
			return
		}
		face := determineBlockFace(b.playerEyePos(), pos)
		if err := c.WritePacket(&packet.PlayerAction{
			EntityRuntimeID: entityID,
			ActionType:      protocol.PlayerActionCreativePlayerDestroyBlock,
			BlockPosition:   pos,
			ResultPosition:  pos,
			BlockFace:       face,
		}); err != nil {
			log.Println("bebot: creative break failed:", ExplainError(err))
		}
		if animate {
			_ = c.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: entityID, SwingSource: packet.AnimateSwingSourceMine})
		}
		if err := c.Flush(); err != nil {
			log.Println("bebot: creative break flush failed:", ExplainError(err))
		}
		b.mu.Lock()
		b.breakTarget = &pos
		b.breakTicks = 0
		b.mu.Unlock()
		log.Printf("bebot: creative break requested action=%d at %d %d %d\n", protocol.PlayerActionCreativePlayerDestroyBlock, pos.X(), pos.Y(), pos.Z())
		return
	}
	b.mu.Lock()
	b.breakTarget = &pos
	b.breakTicks = 0
	b.mu.Unlock()
	log.Printf("bebot: breaking block at %d %d %d\n", pos.X(), pos.Y(), pos.Z())
}

func (b *Bot) stopBreaking() {
	b.mu.Lock()
	if b.breakTarget == nil {
		b.mineLearnActive = false
		b.mu.Unlock()
		log.Println("bebot: no block breaking operation is active")
		return
	}
	pos := *b.breakTarget
	b.abortBreakTarget = &pos
	b.breakTarget = nil
	b.breakTicks = 0
	b.mineLearnActive = false
	b.mu.Unlock()

	log.Printf("bebot: stopped breaking block at %d %d %d\n", pos.X(), pos.Y(), pos.Z())
}

func (b *Bot) getBreakDuration() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hasteLevel >= 2 {
		return 1
	}
	if b.hasteLevel == 1 {
		return 2
	}
	if b.gameMode == 1 || b.gameMode == 4 {
		return 2
	}
	if b.mineActive && b.mineTicksReq > 0 {
		return b.mineTicksReq
	}
	if b.mineActive && b.mineFastBreak {
		return 2
	}
	netID := b.heldItem.Stack.NetworkID
	name := b.itemNamesByID[netID]

	switch {
	case strings.Contains(name, "netherite_pickaxe"):
		return 4
	case strings.Contains(name, "diamond_pickaxe"):
		return 5
	case strings.Contains(name, "golden_pickaxe"):
		return 4
	case strings.Contains(name, "iron_pickaxe"):
		return 8
	case strings.Contains(name, "stone_pickaxe"):
		return 12
	case strings.Contains(name, "wooden_pickaxe"):
		return 20
	case strings.Contains(name, "axe"):
		return 4
	case strings.Contains(name, "shovel"):
		return 4
	case strings.Contains(name, "hoe"):
		return 4
	case strings.Contains(name, "shears"):
		return 2
	default:
		return 10
	}
}

func (b *Bot) isInMineBox(pos protocol.BlockPos) bool {
	return pos.X() >= b.mineBoxMin.X() && pos.X() <= b.mineBoxMax.X() &&
		pos.Y() >= b.mineBoxMin.Y() && pos.Y() <= b.mineBoxMax.Y() &&
		pos.Z() >= b.mineBoxMin.Z() && pos.Z() <= b.mineBoxMax.Z()
}

func (b *Bot) startMiningArea(ax, ay, az, bx, by, bz int32, ticksReq int, repeat bool, faceAccurate bool) {
	minX, maxX := ax, bx
	if minX > maxX {
		minX, maxX = maxX, minX
	}
	minY, maxY := ay, by
	if minY > maxY {
		minY, maxY = maxY, minY
	}
	minZ, maxZ := az, bz
	if minZ > maxZ {
		minZ, maxZ = maxZ, minZ
	}

	total := int((maxX - minX + 1) * (maxY - minY + 1) * (maxZ - minZ + 1))
	if total <= 0 || total > 100000 {
		b.reply(fmt.Sprintf("Invalid area size: %d blocks (limit 1-100,000).", total))
		return
	}

	queue := make([]protocol.BlockPos, 0, total)
	for y := maxY; y >= minY; y-- {
		forward := true
		for x := minX; x <= maxX; x++ {
			if forward {
				for z := minZ; z <= maxZ; z++ {
					queue = append(queue, protocol.BlockPos{x, y, z})
				}
			} else {
				for z := maxZ; z >= minZ; z-- {
					queue = append(queue, protocol.BlockPos{x, y, z})
				}
			}
			forward = !forward
		}
	}

	b.mu.Lock()
	b.mineQueue = queue
	b.mineTotal = total
	b.mineCount = 0
	b.mineActive = true
	b.mineInitialDone = false
	b.mineCurrent = nil
	b.mineStuckTicks = 0
	b.mineTicksReq = ticksReq
	b.mineBoxMin = protocol.BlockPos{minX, minY, minZ}
	b.mineBoxMax = protocol.BlockPos{maxX, maxY, maxZ}
	b.mineRepeat = repeat
	b.mineContinuous = repeat
	b.mineFaceAccurate = faceAccurate
	b.mineAttempts = make(map[protocol.BlockPos]int)
	b.knownAirBlocks = make(map[protocol.BlockPos]bool)
	if b.mineAirRuntimeIDs == nil {
		b.mineAirRuntimeIDs = map[uint32]bool{0: true}
	}
	b.mineFastBreak = false
	b.mineWaitingForBlocks = false
	b.mineLastWasBroken = false
	b.mineLastFinishedPos = protocol.BlockPos{}
	b.mineLastFinishedTick = 0
	b.mu.Unlock()

	modeStr := "once"
	if repeat {
		modeStr = "repeat"
	}
	faceStr := "standard face"
	if faceAccurate {
		faceStr = "face-accurate"
	}
	b.reply(fmt.Sprintf("Mining space (%d, %d, %d) to (%d, %d, %d): %d blocks queued (%s, %s).", minX, minY, minZ, maxX, maxY, maxZ, total, modeStr, faceStr))
}

func (b *Bot) stopMiningArea() {
	b.mu.Lock()
	if !b.mineActive && len(b.mineQueue) == 0 {
		b.mu.Unlock()
		return
	}
	b.mineActive = false
	b.mineRepeat = false
	b.mineContinuous = false
	b.mineFaceAccurate = false
	b.mineQueue = nil
	b.mineCurrent = nil
	b.mineStuckTicks = 0
	b.mineAttempts = nil
	b.breakTarget = nil
	b.breakTicks = 0
	b.mineInitialDone = false
	b.mineWaitingForBlocks = false
	b.mineLastWasBroken = false
	b.mineLastFinishedPos = protocol.BlockPos{}
	b.mineLastFinishedTick = 0
	b.mu.Unlock()
	b.reply("Stopped area mining.")
}

func (b *Bot) restartMiningQueue() {
	minX, maxX := b.mineBoxMin.X(), b.mineBoxMax.X()
	minY, maxY := b.mineBoxMin.Y(), b.mineBoxMax.Y()
	minZ, maxZ := b.mineBoxMin.Z(), b.mineBoxMax.Z()
	total := int((maxX - minX + 1) * (maxY - minY + 1) * (maxZ - minZ + 1))
	queue := make([]protocol.BlockPos, 0, total)
	for y := maxY; y >= minY; y-- {
		forward := true
		for x := minX; x <= maxX; x++ {
			if forward {
				for z := minZ; z <= maxZ; z++ {
					pos := protocol.BlockPos{x, y, z}
					if b.knownAirBlocks != nil && b.knownAirBlocks[pos] {
						continue
					}
					queue = append(queue, pos)
				}
			} else {
				for z := maxZ; z >= minZ; z-- {
					pos := protocol.BlockPos{x, y, z}
					if b.knownAirBlocks != nil && b.knownAirBlocks[pos] {
						continue
					}
					queue = append(queue, pos)
				}
			}
			forward = !forward
		}
	}
	b.mineQueue = queue
	b.mineTotal = len(queue)
	b.mineCount = 0
	b.mineCurrent = nil
	b.breakTarget = nil
	b.breakTicks = 0
	b.mineStuckTicks = 0
	b.mineInitialDone = false
	b.mineWaitingForBlocks = len(queue) == 0
	b.mineLastWasBroken = false
	b.mineAttempts = make(map[protocol.BlockPos]int)
	if len(queue) > 0 {
		log.Printf("bebot: repeating area mining (%d blocks re-queued)\n", len(queue))
	} else {
		log.Printf("bebot: repeating area mining: area is clear, waiting for new blocks to appear\n")
	}
}

func sortMineQueue(queue []protocol.BlockPos) {
	sort.SliceStable(queue, func(i, j int) bool {
		if queue[i].Y() != queue[j].Y() {
			return queue[i].Y() > queue[j].Y()
		}
		if queue[i].X() != queue[j].X() {
			return queue[i].X() < queue[j].X()
		}
		return queue[i].Z() < queue[j].Z()
	})
}

func (b *Bot) resolveSubChunkBlockPos(subPos protocol.BlockPos, blockPos protocol.BlockPos) protocol.BlockPos {
	if b.isInMineBox(blockPos) {
		return blockPos
	}
	candidateA := protocol.BlockPos{subPos.X()*16 + blockPos.X(), subPos.Y()*16 + blockPos.Y(), subPos.Z()*16 + blockPos.Z()}
	if b.isInMineBox(candidateA) {
		return candidateA
	}
	candidateB := protocol.BlockPos{subPos.X() + blockPos.X(), subPos.Y() + blockPos.Y(), subPos.Z() + blockPos.Z()}
	if b.isInMineBox(candidateB) {
		return candidateB
	}
	if blockPos.X() >= 0 && blockPos.X() <= 15 && blockPos.Z() >= 0 && blockPos.Z() <= 15 {
		if subPos.X() > -1000 && subPos.X() < 1000 {
			return candidateA
		}
		return candidateB
	}
	return blockPos
}

func (b *Bot) handleUpdateBlock(pos protocol.BlockPos, runtimeID uint32) {
	b.mu.Lock()
	if b.blockRuntimeIDs == nil {
		b.blockRuntimeIDs = make(map[protocol.BlockPos]uint32)
	}
	b.blockRuntimeIDs[pos] = runtimeID
	isContainerTarget := b.containerOpening && pos == b.containerPos
	ch := b.containerRuntimeIDUpdated
	b.mu.Unlock()

	if isContainerTarget && ch != nil {
		select {
		case ch <- runtimeID:
		default:
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.mineLearnActive && pos == b.mineLearnPos {
		if b.mineAirRuntimeIDs == nil {
			b.mineAirRuntimeIDs = make(map[uint32]bool)
		}
		b.mineAirRuntimeIDs[runtimeID] = true
		if b.knownAirBlocks == nil {
			b.knownAirBlocks = make(map[protocol.BlockPos]bool)
		}
		b.knownAirBlocks[pos] = true
		b.mineLearnActive = false
		b.breakTarget = nil
		b.breakTicks = 0
		log.Printf("bebot: calibration complete! Confirmed air block runtime ID: %d\n", runtimeID)
		b.reply(fmt.Sprintf("Calibration complete! Confirmed air block runtime ID: %d.", runtimeID))
		return
	}

	if !b.mineActive {
		return
	}

	isCurrentBreak := b.breakTarget != nil && pos == *b.breakTarget
	isRecentFinish := pos == b.mineLastFinishedPos && (b.tick-b.mineLastFinishedTick <= 5)

	if isRecentFinish && b.mineLastWasBroken {
		if b.mineAirRuntimeIDs == nil {
			b.mineAirRuntimeIDs = make(map[uint32]bool)
		}
		if !b.mineAirRuntimeIDs[runtimeID] {
			b.mineAirRuntimeIDs[runtimeID] = true
			log.Printf("bebot: learned air block runtime ID %d\n", runtimeID)
		}
	}

	isAir := runtimeID == 0 || (b.mineAirRuntimeIDs != nil && b.mineAirRuntimeIDs[runtimeID])

	if isAir {
		if b.knownAirBlocks == nil {
			b.knownAirBlocks = make(map[protocol.BlockPos]bool)
		}
		b.knownAirBlocks[pos] = true

		if isCurrentBreak {
			if b.breakTicks <= 3 {
				b.mineFastBreak = true
			}
			b.breakTarget = nil
			b.breakTicks = 0
			b.mineCurrent = nil
			b.mineCount++
			if b.mineCount%25 == 0 || len(b.mineQueue) == 0 {
				log.Printf("bebot: block broken (%d mined)\n", b.mineCount)
			}
		} else if b.mineCurrent != nil && *b.mineCurrent == pos {
			b.mineCurrent = nil
		}

		if b.mineAttempts != nil {
			b.mineAttempts[pos] = 2
		}
	} else if b.isInMineBox(pos) && !isRecentFinish && !isCurrentBreak {
		if b.knownAirBlocks != nil {
			delete(b.knownAirBlocks, pos)
		}
		if b.mineAttempts != nil {
			delete(b.mineAttempts, pos)
		}
		alreadyQueued := false
		if b.mineCurrent != nil && *b.mineCurrent == pos {
			alreadyQueued = true
		}
		if b.breakTarget != nil && *b.breakTarget == pos {
			alreadyQueued = true
		}
		if !alreadyQueued {
			for _, q := range b.mineQueue {
				if q == pos {
					alreadyQueued = true
					break
				}
			}
		}
		if !alreadyQueued {
			b.mineQueue = append(b.mineQueue, pos)
			b.mineTotal++
			b.mineInitialDone = false
			b.mineWaitingForBlocks = false
			sortMineQueue(b.mineQueue)
			log.Printf("bebot: new block detected at (%d, %d, %d), added to mine queue (queue len=%d)\n", pos.X(), pos.Y(), pos.Z(), len(b.mineQueue))
		}
	}
}

func determineBlockFace(eyePos mgl32.Vec3, blockPos protocol.BlockPos) int32 {
	dx := float32(blockPos.X()) + 0.5 - eyePos.X()
	dy := float32(blockPos.Y()) + 0.5 - eyePos.Y()
	dz := float32(blockPos.Z()) + 0.5 - eyePos.Z()

	absX := float32(math.Abs(float64(dx)))
	absY := float32(math.Abs(float64(dy)))
	absZ := float32(math.Abs(float64(dz)))

	if absY >= absX && absY >= absZ {
		if dy > 0 {
			return 0
		}
		return 1
	} else if absX >= absZ {
		if dx > 0 {
			return 4
		}
		return 5
	} else {
		if dz > 0 {
			return 2
		}
		return 3
	}
}

func faceClickPosition(face int32) mgl32.Vec3 {
	switch face {
	case 0: // bottom
		return mgl32.Vec3{0.5, 0.0, 0.5}
	case 1: // top
		return mgl32.Vec3{0.5, 1.0, 0.5}
	case 2: // north (Z-)
		return mgl32.Vec3{0.5, 0.5, 0.0}
	case 3: // south (Z+)
		return mgl32.Vec3{0.5, 0.5, 1.0}
	case 4: // west (X-)
		return mgl32.Vec3{0.0, 0.5, 0.5}
	case 5: // east (X+)
		return mgl32.Vec3{1.0, 0.5, 0.5}
	default:
		return mgl32.Vec3{0.5, 0.5, 0.5}
	}
}

func (b *Bot) pickNextMineBlock() *protocol.BlockPos {
	for len(b.mineQueue) > 0 {
		pos := b.mineQueue[0]
		if (b.knownAirBlocks != nil && b.knownAirBlocks[pos]) || (b.mineAttempts != nil && b.mineAttempts[pos] >= 2) {
			b.mineQueue = b.mineQueue[1:]
			continue
		}
		break
	}

	if len(b.mineQueue) == 0 {
		return nil
	}

	target := b.mineQueue[0]
	b.mineQueue = b.mineQueue[1:]
	return &target
}

func (b *Bot) startBreakingFront(animate bool, repeat bool) {
	yaw := b.facingYaw
	if b.mode == modeMimic {
		b.mu.Lock()
		if p := b.players[strings.ToLower(b.mimicTarget)]; p.id != 0 {
			yaw = p.yaw
		}
		b.mu.Unlock()
	}
	rad := float64(yaw) * math.Pi / 180
	x := int32(math.Floor(float64(b.pos.X() - float32(math.Sin(rad)))))
	y := int32(math.Floor(float64(b.pos.Y())))
	z := int32(math.Floor(float64(b.pos.Z() + float32(math.Cos(rad)))))
	b.startBreaking(protocol.BlockPos{x, y, z}, animate, repeat)
}

func (b *Bot) startLearningAir(pos *protocol.BlockPos) {
	b.mu.Lock()
	var target protocol.BlockPos
	if pos != nil {
		target = *pos
	} else {
		yaw := b.facingYaw
		if b.mode == modeMimic {
			if p := b.players[strings.ToLower(b.mimicTarget)]; p.id != 0 {
				yaw = p.yaw
			}
		}
		rad := float64(yaw) * math.Pi / 180
		x := int32(math.Floor(float64(b.pos.X() - float32(math.Sin(rad)))))
		y := int32(math.Floor(float64(b.pos.Y())))
		z := int32(math.Floor(float64(b.pos.Z() + float32(math.Cos(rad)))))
		target = protocol.BlockPos{x, y, z}
	}
	b.mineLearnActive = true
	b.mineLearnPos = target
	b.breakTarget = &target
	b.breakTicks = 0
	b.breakAnimation = true
	b.breakRepeat = false
	b.mu.Unlock()

	log.Printf("bebot: calibrating air block runtime ID at (%d, %d, %d)...\n", target.X(), target.Y(), target.Z())
	b.reply(fmt.Sprintf("Calibrating air block runtime ID at (%d, %d, %d)... breaking block.", target.X(), target.Y(), target.Z()))
}
