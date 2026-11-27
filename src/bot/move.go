package bot

import (
	"fmt"
	"log"
	"math"
	"strings"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

var (
	baseIdleInputFlags = func() protocol.InputFlags {
		f := protocol.NewInputFlags(packet.InputFlagCount)
		f.Set(packet.InputFlagBlockBreakingDelayEnabled)
		f.Set(packet.InputFlagClientAckServerData)
		return f
	}()

	baseMiningInputFlags = func() protocol.InputFlags {
		f := protocol.NewInputFlags(packet.InputFlagCount)
		f.Set(packet.InputFlagBlockBreakingDelayEnabled)
		f.Set(packet.InputFlagClientAckServerData)
		f.Set(packet.InputFlagPerformBlockActions)
		return f
	}()
)

func entityFlagSet(metadata protocol.EntityMetadata, key uint32, flag int) bool {
	raw, ok := metadata[key]
	if !ok {
		return false
	}
	switch value := raw.(type) {
	case int64:
		return value&(int64(1)<<flag) != 0
	case int32:
		return value&(int32(1)<<flag) != 0
	case byte:
		return value&(byte(1)<<flag) != 0
	}
	return false
}

func entityPose(metadata protocol.EntityMetadata) (byte, bool) {
	raw, ok := metadata[protocol.EntityDataKeyPoseIndex]
	if !ok {
		return 0, false
	}
	switch value := raw.(type) {
	case int64:
		return byte(value), true
	case int32:
		return byte(value), true
	case byte:
		return value, true
	}
	return 0, false
}

func playerMetadataState(metadata protocol.EntityMetadata) (sneaking, sprinting, swimming, using bool) {
	sneaking = entityFlagSet(metadata, protocol.EntityDataKeyFlags, protocol.EntityDataFlagSneaking)
	sprinting = entityFlagSet(metadata, protocol.EntityDataKeyFlags, protocol.EntityDataFlagSprinting)
	swimming = entityFlagSet(metadata, protocol.EntityDataKeyFlags, protocol.EntityDataFlagSwimming)
	using = entityFlagSet(metadata, protocol.EntityDataKeyFlags, protocol.EntityDataFlagUsingItem)

	if pose, ok := entityPose(metadata); ok {
		switch pose {
		case 4: // ActorPose.Sneaking
			sneaking = true
			swimming = false
		case 2: // ActorPose.Swimming
			swimming = true
			sneaking = false
		}
	}
	return
}

func (b *Bot) moveTick(c *minecraft.Conn, jump bool) {
	stalled := b.isServerStalled()
	b.mu.Lock()
	if b.mineActive {
		if b.mineCurrent == nil {
			if len(b.mineQueue) == 0 {
				if b.mode == modeMoving {
					b.mode = modeIdle
				}
				b.breakTarget = nil
				b.breakTicks = 0
				count := b.mineCount
				if b.mineRepeat {
					if !b.mineWaitingForBlocks {
						b.mu.Unlock()
						b.reply(fmt.Sprintf("Area mining complete! Mined %d blocks. Repeating...", count))
						b.mu.Lock()
						b.restartMiningQueue()
						if len(b.mineQueue) == 0 {
							b.mineWaitingForBlocks = true
						}
					}
				} else {
					b.mineActive = false
					b.mineContinuous = false
					b.mineRepeat = false
					b.mineFaceAccurate = false
					b.mineQueue = nil
					b.mineCurrent = nil
					b.mineStuckTicks = 0
					b.mineAttempts = nil
					b.mu.Unlock()
					b.reply(fmt.Sprintf("Area mining complete! Mined %d blocks.", count))
					b.mu.Lock()
				}
			} else {
				b.mineCurrent = b.pickNextMineBlock()
				b.mineStuckTicks = 0
			}
		}

		if b.mineCurrent != nil {
			cur := *b.mineCurrent
			eyePos := mgl32.Vec3{b.pos.X(), b.pos.Y() + 1.62, b.pos.Z()}

			closestX := float32(math.Max(float64(cur.X()), math.Min(float64(eyePos.X()), float64(cur.X()+1))))
			closestY := float32(math.Max(float64(cur.Y()), math.Min(float64(eyePos.Y()), float64(cur.Y()+1))))
			closestZ := float32(math.Max(float64(cur.Z()), math.Min(float64(eyePos.Z()), float64(cur.Z()+1))))
			diff := mgl32.Vec3{closestX - eyePos.X(), closestY - eyePos.Y(), closestZ - eyePos.Z()}
			dist := float32(math.Sqrt(float64(diff.Dot(diff))))

			reachLimit := float32(5.0)
			if b.gameMode == 1 || b.gameMode == 4 {
				reachLimit = 6.5
			}

			if dist > reachLimit && closestY > eyePos.Y() && dist <= reachLimit+1.25 {
				jump = true
			}

			if dist > reachLimit {
				b.moveTarget = mgl32.Vec3{float32(cur.X()) + 0.5, float32(cur.Y()), float32(cur.Z()) + 0.5}
				b.mode = modeMoving
				b.mineStuckTicks++
				if b.mineStuckTicks > 100 {
					log.Printf("bebot: skipping unreachable block at %d %d %d\n", cur.X(), cur.Y(), cur.Z())
					b.mineCurrent = nil
					b.mineStuckTicks = 0
				}
			} else {
				if b.mode == modeMoving {
					b.mode = modeIdle
				}
				b.mineStuckTicks = 0

				centerDiff := mgl32.Vec3{float32(cur.X()) + 0.5 - eyePos.X(), float32(cur.Y()) + 0.5 - eyePos.Y(), float32(cur.Z()) + 0.5 - eyePos.Z()}
				yaw := float32(math.Atan2(float64(-centerDiff.X()), float64(centerDiff.Z())) * 180 / math.Pi)
				if yaw < 0 {
					yaw += 360
				}
				hLen := math.Sqrt(float64(centerDiff.X()*centerDiff.X() + centerDiff.Z()*centerDiff.Z()))
				pitch := float32(-math.Atan2(float64(centerDiff.Y()), hLen) * 180 / math.Pi)
				b.idleYaw = yaw
				b.idlePitch = pitch
				b.facingYaw = yaw
				b.facingPitch = pitch

				if b.breakTarget == nil {
					b.breakTarget = b.mineCurrent
					b.breakTicks = 0
					b.breakAnimation = true
					b.breakRepeat = false
				}
			}
		}
	}
	b.mu.Unlock()

	b.mu.Lock()
	target := b.moveTarget
	b.mu.Unlock()
	var targetPlayer player
	targetMoved := true
	if b.mode == modeFollowing || b.mode == modeMimic || b.mode == modeCamic || b.mode == modeLookAt {
		b.mu.Lock()
		lookup := b.followTarget
		switch b.mode {
		case modeMimic:
			lookup = b.mimicTarget
		case modeLookAt:
			lookup = b.lookAtTarget
		}
		if b.mode == modeLookAt && lookup == "" {
			nearestDistance := float32(math.MaxFloat32)
			for _, candidate := range b.players {
				if !candidate.online || candidate.id == 0 {
					continue
				}
				d := candidate.position.Sub(b.pos)
				distance := d.Dot(d)
				if distance < nearestDistance {
					nearestDistance = distance
					targetPlayer = candidate
				}
			}
		} else {
			targetPlayer = b.players[strings.ToLower(lookup)]
		}
		if b.mode == modeLookAt && b.lookAtPos != nil {
			targetPlayer = player{position: *b.lookAtPos, id: 1}
		}
		if b.mode == modeMimic {
			if b.mimicPosSet {
				d := targetPlayer.position.Sub(b.mimicLastPos)
				d[1] = 0
				targetMoved = d.Dot(d) > .0004
			}
			b.mimicLastPos = targetPlayer.position
			b.mimicPosSet = targetPlayer.id != 0
			v := b.players[strings.ToLower(b.mimicTarget)]
			v.swinging = false
			v.usingItem = false
			v.jumping = false
			b.players[strings.ToLower(b.mimicTarget)] = v
		} else if b.mode == modeFollowing && targetPlayer.id != 0 {
			v := b.players[strings.ToLower(b.followTarget)]
			v.jumping = false
			b.players[strings.ToLower(b.followTarget)] = v
		}
		if b.mode == modeFollowing && lookup != "" && !targetPlayer.online {
			b.mode = modeIdle
			b.followTarget = ""
			b.mimicTarget = ""
		}
		b.mu.Unlock()
		if (targetPlayer.id != 0 || targetPlayer.hasLocation) && b.mode != modeLookAt {
			target = targetPlayer.position
		}
	}
	if (b.mode == modeMimic && !targetMoved) || b.mode == modeCamic || b.mode == modeLookAt || b.mode == modeIdle {
		target = b.pos
	}
	dx, dy, dz := target.X()-b.pos.X(), target.Y()-b.pos.Y(), target.Z()-b.pos.Z()
	hDist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
	b.moving = hDist > .25
	if b.mode == modeIdle {
		b.moving = false
	}
	if b.moving && !stalled {
		step := b.cfg.Behaviour.WalkSpeed
		if step > hDist {
			step = hDist
		}
		b.pos = b.pos.Add(mgl32.Vec3{dx / hDist * step, 0, dz / hDist * step})
		b.updateChunkCenterIfMoved()
	}
	b.mu.Lock()
	if b.jumpIntervalTicks > 0 {
		b.jumpTicks++
		if b.jumpTicks >= b.jumpIntervalTicks {
			jump = true
			b.jumpTicks = 0
		}
	}
	b.mu.Unlock()

	if b.moving && dy > .4 && b.velocityY == 0 {
		jump = true
	}
	if (b.mode == modeMimic || b.mode == modeFollowing) && targetPlayer.id != 0 && targetPlayer.jumping {
		jump = true
	}
	if b.velocityY == 0 {
		b.jumpBaseY = b.pos.Y()
	}
	if jump && b.velocityY == 0 {
		b.velocityY = .42
	}
	baseY := target.Y()
	if b.mode == modeIdle || b.mode == modeCamic || b.mode == modeLookAt || !b.moving {
		baseY = b.jumpBaseY
	}
	if b.velocityY != 0 || b.pos.Y() > baseY+.6 {
		b.velocityY -= .08
		b.pos[1] += b.velocityY
		if b.pos.Y() <= baseY {
			b.pos[1] = baseY
			b.velocityY = 0
		}
	}

	progress := b.pos.Sub(b.last)
	progressLen := float32(math.Sqrt(float64(progress.Dot(progress))))
	if b.moving && progressLen < .02 {
		b.stuckTicks++
	} else {
		b.stuckTicks = 0
	}
	if b.moving && b.stuckTicks > 2 {
		jump = true
	}

	desiredSneaking := b.sneaking
	if b.mode == modeMimic {
		desiredSneaking = targetPlayer.sneaking
	}
	sneakStart := desiredSneaking && !b.sentSneaking
	sneakStop := !desiredSneaking && b.sentSneaking

	mirrorSwing := b.mode == modeMimic && targetPlayer.swinging
	mirrorUse := b.mode == modeMimic && targetPlayer.usingItem
	mimicActive := b.mode == modeMimic && (targetPlayer.sprinting || targetPlayer.swimming || b.mimicSwimming || mirrorSwing || mirrorUse)
	hasDynamicFlags := b.moving || jump || desiredSneaking || sneakStop || mimicActive

	var flags protocol.InputFlags
	if hasDynamicFlags {
		flags = protocol.NewInputFlags(packet.InputFlagCount)
		flags.Set(packet.InputFlagBlockBreakingDelayEnabled)
		flags.Set(packet.InputFlagClientAckServerData)
		if b.moving {
			flags.Set(packet.InputFlagUp)
			flags.Set(packet.InputFlagSprinting)
			flags.Set(packet.InputFlagStartSprinting)
		}
		if b.mode == modeMimic && targetPlayer.sprinting {
			flags.Set(packet.InputFlagSprinting)
			flags.Set(packet.InputFlagStartSprinting)
		}
		if desiredSneaking {
			flags.Set(packet.InputFlagSneaking)
			flags.Set(packet.InputFlagSneakDown)
			flags.Set(packet.InputFlagPersistSneak)
			flags.Set(packet.InputFlagSneakCurrentRaw)
			if sneakStart {
				flags.Set(packet.InputFlagStartSneaking)
				flags.Set(packet.InputFlagSneakPressedRaw)
			}
		} else if sneakStop {
			flags.Set(packet.InputFlagStopSneaking)
			flags.Set(packet.InputFlagSneakReleasedRaw)
		}
		if b.mode == modeMimic && targetPlayer.swimming {
			if !b.mimicSwimming {
				flags.Set(packet.InputFlagStartSwimming)
			}
		} else if b.mode == modeMimic && b.mimicSwimming {
			flags.Set(packet.InputFlagStopSwimming)
		}
		if mirrorSwing {
			flags.Set(packet.InputFlagMissedSwing)
		}
		if mirrorUse {
			flags.Set(packet.InputFlagPerformItemInteraction)
			flags.Set(packet.InputFlagStartUsingItem)
		}
		if jump {
			flags.Set(packet.InputFlagJumping)
			flags.Set(packet.InputFlagAscend)
			flags.Set(packet.InputFlagWantUp)
			flags.Set(packet.InputFlagStartJumping)
		}
	} else {
		flags = baseIdleInputFlags
	}
	b.tick++
	moveZ := float32(0)
	if b.moving {
		moveZ = 1
	}
	yaw := float32(0)
	if b.moving {
		yaw = float32(math.Atan2(float64(-dx), float64(dz)) * 180 / math.Pi)
		if yaw < 0 {
			yaw += 360
		}
	}
	if b.mode == modeMimic || b.mode == modeCamic {
		yaw = targetPlayer.yaw
	}
	lookPitch := float32(0)
	if b.mode == modeLookAt && targetPlayer.id != 0 {
		lookDX := targetPlayer.position.X() - b.pos.X()
		lookDY := targetPlayer.position.Y() - b.pos.Y()
		lookDZ := targetPlayer.position.Z() - b.pos.Z()
		horizontal := math.Sqrt(float64(lookDX*lookDX + lookDZ*lookDZ))
		if horizontal > .001 {
			yaw = float32(math.Atan2(float64(-lookDX), float64(lookDZ)) * 180 / math.Pi)
			if yaw < 0 {
				yaw += 360
			}
			lookPitch = float32(-math.Atan2(float64(lookDY), horizontal) * 180 / math.Pi)
		}
	}
	if b.spinActive {
		spd := b.spinSpeed
		if spd <= 0 {
			spd = 10.0
		}
		b.idleYaw += spd * b.spinDirection
		for b.idleYaw >= 360 {
			b.idleYaw -= 360
		}
		for b.idleYaw < 0 {
			b.idleYaw += 360
		}
		yaw = b.idleYaw
		b.facingYaw = yaw
	} else if b.mode == modeIdle {
		yaw = b.idleYaw
	}
	b.facingYaw = yaw
	swimStart := b.mode == modeMimic && targetPlayer.swimming && !b.mimicSwimming
	swimStop := b.mode == modeMimic && !targetPlayer.swimming && b.mimicSwimming
	pitch := float32(0)
	switch b.mode {
	case modeMimic, modeCamic:
		pitch = targetPlayer.pitch
	case modeLookAt:
		pitch = lookPitch
	}
	if b.mode == modeIdle {
		pitch = b.idlePitch
	}
	if b.headshake != headshakeNone {
		phase := float32(math.Sin(float64(b.headshakeTicks) * 2 * math.Pi / headshakePeriod))
		switch b.headshake {
		case headshakeNo:
			yaw += phase * 18
		case headshakeYes:
			pitch += phase * 22
		}
		b.headshakeTicks++
		if !b.headshakeRepeat && b.headshakeTicks >= headshakeOnceTicks {
			b.headshake = headshakeNone
			b.headshakeTicks = 0
		}
	}
	b.facingPitch = pitch

	b.mu.Lock()
	bt := b.breakTarget
	var abortBt *protocol.BlockPos
	if b.abortBreakTarget != nil {
		abortBt = b.abortBreakTarget
		b.abortBreakTarget = nil
	}
	b.mu.Unlock()

	if bt != nil && (!b.mineActive || b.mineFaceAccurate) {
		eyePos := b.playerEyePos()
		tX := float32(bt.X()) + 0.5
		tY := float32(bt.Y()) + 0.5
		tZ := float32(bt.Z()) + 0.5
		lookDX := tX - eyePos.X()
		lookDY := tY - eyePos.Y()
		lookDZ := tZ - eyePos.Z()
		hLen := math.Sqrt(float64(lookDX*lookDX + lookDZ*lookDZ))
		if hLen > .001 {
			yaw = float32(math.Atan2(float64(-lookDX), float64(lookDZ)) * 180 / math.Pi)
			if yaw < 0 {
				yaw += 360
			}
		}
		pitch = float32(-math.Atan2(float64(lookDY), hLen) * 180 / math.Pi)
		b.facingYaw = yaw
		b.facingPitch = pitch
	}

	radPitch := float64(pitch) * math.Pi / 180
	radYaw := float64(yaw) * math.Pi / 180
	cosPitch := math.Cos(radPitch)
	camOrientation := mgl32.Vec3{
		float32(-math.Sin(radYaw) * cosPitch),
		float32(-math.Sin(radPitch)),
		float32(math.Cos(radYaw) * cosPitch),
	}

	auth := packet.PlayerAuthInput{
		Position:           b.pos,
		Delta:              b.pos.Sub(b.last),
		Yaw:                yaw,
		HeadYaw:            yaw,
		Pitch:              pitch,
		InteractYaw:        yaw,
		InteractPitch:      pitch,
		CameraOrientation:  camOrientation,
		MoveVector:         mgl32.Vec2{0, moveZ},
		AnalogueMoveVector: mgl32.Vec2{0, moveZ},
		RawMoveVector:      mgl32.Vec2{0, moveZ},
		InputData:          flags,
		InputMode:          packet.InputModeMouse,
		PlayMode:           packet.PlayModeNormal,
		InteractionModel:   packet.InteractionModelClassic,
		Tick:               b.tick,
	}
	mineSwing := false

	var actions []protocol.PlayerBlockAction
	eyePos := b.playerEyePos()
	if abortBt != nil {
		abortFace := int32(1)
		if !b.mineActive || b.mineFaceAccurate {
			abortFace = determineBlockFace(eyePos, *abortBt)
		}
		actions = append(actions, protocol.PlayerBlockAction{Action: int32(protocol.PlayerActionAbortBreak), BlockPos: *abortBt, Face: abortFace})
	}

	if bt != nil {
		btFace := int32(1)
		if !b.mineActive || b.mineFaceAccurate {
			btFace = determineBlockFace(eyePos, *bt)
		}
		if actions == nil {
			actions = make([]protocol.PlayerBlockAction, 0, 2)
		}

		b.mu.Lock()
		ticks := b.breakTicks
		timeoutSec := b.mineTimeoutSec
		if timeoutSec <= 0 {
			timeoutSec = 3.0
		}
		timeoutTicks := int(math.Round(timeoutSec * 20))
		if timeoutTicks < 1 {
			timeoutTicks = 1
		}
		b.mu.Unlock()

		maxTicks := b.getBreakDuration()
		if timeoutTicks <= maxTicks {
			timeoutTicks = maxTicks + 4
		}

		if (b.mineActive || b.mineLearnActive) && ticks >= timeoutTicks && !stalled {
			if b.mineLearnActive {
				log.Printf("bebot: mine learn at (%d, %d, %d) timed out after %.1fs\n", bt.X(), bt.Y(), bt.Z(), timeoutSec)
				b.mu.Lock()
				b.abortBreakTarget = bt
				b.mineLearnActive = false
				b.mineLearnWasBroken = false
				b.mineLearnStartedBreak = false
				b.mineLearnInitialRuntimeID = 0
				b.breakTarget = nil
				b.breakTicks = 0
				b.breakRepeat = false
				b.mu.Unlock()
				b.reply(fmt.Sprintf("Air calibration timed out after %.1fs (block did not break or unreachable).", timeoutSec))
				return
			}
			log.Printf("bebot: block at (%d, %d, %d) timed out after %.1fs (cannot be mined: air/water/bedrock), skipping\n", bt.X(), bt.Y(), bt.Z(), timeoutSec)
			b.mu.Lock()
			b.abortBreakTarget = bt
			if b.mineAttempts != nil {
				b.mineAttempts[*bt] = 2
			}
			if b.knownAirBlocks == nil {
				b.knownAirBlocks = make(map[protocol.BlockPos]bool)
			}
			b.knownAirBlocks[*bt] = true
			b.mineLastFinishedPos = *bt
			b.mineLastFinishedTick = b.tick
			b.mineLastWasBroken = false
			b.mineCurrent = nil
			b.breakTarget = nil
			b.breakTicks = 0
			b.mineCount++
			if b.mineCount%25 == 0 || len(b.mineQueue) == 0 {
				log.Printf("bebot: mined %d/%d blocks\n", b.mineCount, b.mineTotal)
			}
			b.mu.Unlock()
		} else {
			if ticks == 0 {
				b.mu.Lock()
				mineSwing = b.breakAnimation
				if b.mineLearnActive {
					b.mineLearnStartedBreak = true
				}
				b.mu.Unlock()
				actions = append(actions,
					protocol.PlayerBlockAction{Action: int32(protocol.PlayerActionStartBreak), BlockPos: *bt, Face: btFace},
				)
			} else {
				actions = append(actions,
					protocol.PlayerBlockAction{Action: int32(protocol.PlayerActionContinueDestroyBlock), BlockPos: *bt, Face: btFace},
				)
			}

			if ticks >= maxTicks {
				actions = append(actions,
					protocol.PlayerBlockAction{Action: int32(protocol.PlayerActionPredictDestroyBlock), BlockPos: *bt, Face: btFace},
				)
				_ = c.WritePacket(&packet.InventoryTransaction{
					TransactionData: &protocol.UseItemTransactionData{
						ActionType:    protocol.UseItemActionBreakBlock,
						BlockPosition: *bt,
						BlockFace:     btFace,
						HotBarSlot:    int32(b.hotbarSlot),
						HeldItem:      b.heldItem,
					},
				})
				if b.gameMode == 1 || b.gameMode == 4 {
					_ = c.WritePacket(&packet.PlayerAction{
						EntityRuntimeID: b.entityID,
						ActionType:      protocol.PlayerActionCreativePlayerDestroyBlock,
						BlockPosition:   *bt,
						ResultPosition:  *bt,
						BlockFace:       btFace,
					})
				}

				b.mu.Lock()
				if b.breakRepeat {
					b.breakTicks = 0
				} else if b.mineLearnActive {
					b.mineLearnWasBroken = true
					b.breakTicks = maxTicks
				} else if b.mineActive {
					if b.mineAttempts != nil {
						b.mineAttempts[*bt] = 2
					}
					if b.knownAirBlocks == nil {
						b.knownAirBlocks = make(map[protocol.BlockPos]bool)
					}
					b.knownAirBlocks[*bt] = true
					b.mineLastFinishedPos = *bt
					b.mineLastFinishedTick = b.tick
					b.mineLastWasBroken = true
					b.mineCurrent = nil
					b.breakTarget = nil
					b.breakTicks = 0
					b.mineCount++
					if b.mineCount%25 == 0 || len(b.mineQueue) == 0 {
						log.Printf("bebot: mined %d/%d blocks\n", b.mineCount, b.mineTotal)
					}
				} else {
					b.breakTarget = nil
					b.breakTicks = 0
				}
				b.mu.Unlock()
			} else {
				b.mu.Lock()
				if !stalled {
					b.breakTicks++
				}
				b.mu.Unlock()
			}
		}
	}
	if len(actions) > 0 {
		auth.BlockActions = protocol.Option(actions)
		if hasDynamicFlags {
			auth.InputData.Set(packet.InputFlagPerformBlockActions)
		} else {
			auth.InputData = baseMiningInputFlags
		}
	}

	b.mu.Lock()
	placeItem := b.placePending
	placeSwing := b.placePendingSwing
	b.placePending = nil
	b.placePendingSwing = false
	b.mu.Unlock()

	if placeItem != nil {
		auth.ItemInteractionData = protocol.Option(*placeItem)
		auth.InputData.Set(packet.InputFlagPerformItemInteraction)
		auth.InteractPitch = pitch
		auth.InteractYaw = yaw
	}

	if stalled {
		b.mu.Lock()
		b.stallTicks++
		curStall := b.stallTicks
		b.mu.Unlock()

		isContinuous := len(actions) == 1 && actions[0].Action == int32(protocol.PlayerActionContinueDestroyBlock)
		throttleTicks := b.StallThrottleTicks()
		// During server stalls, throttle empty keepalive auth packets and repetitive continuous break
		// packets to match the stall threshold to prevent buffer bursts and anti-cheat kicks on unfreeze.
		if curStall%throttleTicks != 0 && (len(actions) == 0 || isContinuous) && !mineSwing {
			return
		}
	} else {
		b.mu.Lock()
		b.stallTicks = 0
		b.mu.Unlock()
	}

	_ = c.WritePacket(&auth)
	if placeSwing {
		_ = c.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: b.entityID})
	}
	if mineSwing || (bt != nil && b.mineActive && b.breakTicks%4 == 0) {
		_ = c.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: b.entityID, SwingSource: packet.AnimateSwingSourceMine})
		if mineSwing {
			if tracePackets {
				log.Printf("bebot: break input action=%d target=%d %d %d\n", protocol.PlayerActionStartBreak, bt.X(), bt.Y(), bt.Z())
			}
		}
	}
	b.sentSneaking = desiredSneaking
	if sneakStart {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStartSneak, BlockFace: -1})
	}
	if sneakStop {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStopSneak, BlockFace: -1})
	}
	if swimStart {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStartSwimming, BlockFace: -1})
	}
	if swimStop {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStopSwimming, BlockFace: -1})
	}
	if b.mode == modeMimic {
		b.mimicSneaking = targetPlayer.sneaking
		b.mimicSwimming = targetPlayer.swimming
	}
	if b.mode == modeMimic && mirrorSwing {
		_ = c.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: b.entityID, SwingSource: packet.AnimateSwingSourceAttack})
	}
	if b.mode == modeMimic && mirrorUse {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStartUsingItem, BlockFace: -1})
	}
	if b.emoteRepeat && b.emoteID != "" && b.tick >= b.emoteNextTick {
		_ = c.WritePacket(&packet.Emote{EntityRuntimeID: b.entityID, EmoteID: b.emoteID, EmoteLength: b.emoteLength, Flags: 0})
		b.emoteNextTick = b.tick + uint64(b.emoteLength)
	}
	b.last = b.pos
}

func inputFlagsValue(f protocol.InputFlags) string {
	ids := make([]int, 0, 8)
	for i := 0; i < f.Len(); i++ {
		if f.Load(i) {
			ids = append(ids, i)
		}
	}
	return fmt.Sprintf("%v", ids)
}
