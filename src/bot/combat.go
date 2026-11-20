package bot

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func (b *Bot) logPlayerEvent(event, name string) {
	if name == "" {
		return
	}
	fmt.Printf("[player - %s] %s %s\n", time.Now().Format("2006-01-02 15:04:05"), event, cleanChatName(name))
}

func (b *Bot) online() {
	b.mu.Lock()
	defer b.mu.Unlock()
	names := make([]string, 0, len(b.players))
	for key, p := range b.players {
		if !p.online {
			continue
		}
		name := p.name
		if name == "" {
			name = key
		}
		names = append(names, name)
	}
	log.Printf("bebot: online players (%d): %s\n", len(names), strings.Join(names, ", "))
}

func (b *Bot) nearby() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, p := range b.players {
		if !p.online || p.id == 0 {
			continue
		}
		d := p.position.Sub(b.pos)
		name := p.name
		if name == "" {
			name = key
		}
		log.Printf("bebot: %s @ (%.1f, %.1f, %.1f), %.1fm away\n", name, p.position.X(), p.position.Y(), p.position.Z(), float32(math.Sqrt(float64(d.Dot(d)))))
	}
}

func (b *Bot) nearestPlayer(maxDist float32) (uint64, mgl32.Vec3, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var closestID uint64
	var closestPos mgl32.Vec3
	minD2 := maxDist * maxDist
	found := false
	for _, p := range b.players {
		if !p.online || p.id == 0 || p.id == b.entityID {
			continue
		}
		d := p.position.Sub(b.pos)
		d2 := d.Dot(d)
		if d2 <= minD2 {
			minD2 = d2
			closestID = p.id
			closestPos = p.position
			found = true
		}
	}
	return closestID, closestPos, found
}

func (b *Bot) nearestAttacker(maxDist float32) (uint64, mgl32.Vec3, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var closestID uint64
	var closestPos mgl32.Vec3
	minD2 := maxDist * maxDist
	found := false
	now := time.Now()
	for _, p := range b.players {
		if !p.online || p.id == 0 || p.id == b.entityID {
			continue
		}
		if now.Sub(p.lastSwing) > time.Second {
			continue
		}
		d := p.position.Sub(b.pos)
		d2 := d.Dot(d)
		if d2 <= minD2 {
			minD2 = d2
			closestID = p.id
			closestPos = p.position
			found = true
		}
	}
	return closestID, closestPos, found
}

func (b *Bot) attack(targetID uint64, targetPos mgl32.Vec3) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return
	}

	lookDX := targetPos.X() - b.pos.X()
	lookDZ := targetPos.Z() - b.pos.Z()
	horizontal := math.Sqrt(float64(lookDX*lookDX + lookDZ*lookDZ))
	if horizontal > 0.001 {
		yaw := float32(math.Atan2(float64(-lookDX), float64(lookDZ)) * 180 / math.Pi)
		if yaw < 0 {
			yaw += 360
		}
		lookDY := (targetPos.Y() + 1.62) - (b.pos.Y() + 1.62)
		pitch := float32(-math.Atan2(float64(lookDY), horizontal) * 180 / math.Pi)
		b.idleYaw = yaw
		b.idlePitch = pitch
	}

	_ = b.conn.WritePacket(&packet.InventoryTransaction{
		TransactionData: &protocol.UseItemOnEntityTransactionData{
			TargetEntityRuntimeID: targetID,
			ActionType:            protocol.UseItemOnEntityActionAttack,
			HotBarSlot:            int32(b.hotbarSlot),
			HeldItem:              b.heldItem,
			Position:              b.pos,
			ClickedPosition:       targetPos,
		},
	})
	_ = b.conn.Flush()

	_ = b.conn.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: b.entityID})
	_ = b.conn.Flush()
}

func (b *Bot) killLoop(targetName string, cps int) {
	ticker := time.NewTicker(time.Second / time.Duration(cps))
	defer ticker.Stop()
	for {
		<-ticker.C
		b.mu.Lock()
		if b.killTarget != targetName {
			b.mu.Unlock()
			return
		}
		pEntry, ok := b.players[targetName]
		if !ok || !pEntry.online || pEntry.id == 0 {
			b.killTarget = ""
			b.mu.Unlock()
			b.reply("Target lost or died.")
			return
		}
		d := pEntry.position.Sub(b.pos)
		if d.Dot(d) > 36 {
			b.killTarget = ""
			b.mu.Unlock()
			b.reply("Target escaped (too far).")
			return
		}
		targetID := pEntry.id
		targetPos := pEntry.position
		b.mu.Unlock()

		b.attack(targetID, targetPos)
	}
}

func (b *Bot) useItemInHand() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return
	}

	_ = b.conn.WritePacket(&packet.InventoryTransaction{
		TransactionData: &protocol.UseItemTransactionData{
			ActionType:      protocol.UseItemActionClickAir,
			BlockPosition:   protocol.BlockPos{},
			BlockFace:       255,
			HotBarSlot:      int32(b.hotbarSlot),
			HeldItem:        b.heldItem,
			Position:        b.pos,
			ClickedPosition: b.pos.Add(mgl32.Vec3{0, 1, 0}),
			BlockRuntimeID:  0,
		},
	})
	_ = b.conn.Flush()

	_ = b.conn.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: b.entityID})
	_ = b.conn.Flush()
}

func (b *Bot) coords() {
	log.Printf("bebot: bot @ (%.1f, %.1f, %.1f)\n", b.pos.X(), b.pos.Y(), b.pos.Z())
	b.nearby()
}
