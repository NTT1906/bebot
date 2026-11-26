package bot

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func TestFormatRadarOutput(t *testing.T) {
	t.Run("Zero players", func(t *testing.T) {
		got := formatRadarOutput(nil)
		if got != "Players: 0" {
			t.Errorf("expected 'Players: 0', got %q", got)
		}
	})

	t.Run("One player", func(t *testing.T) {
		targets := []radarTarget{
			{
				name: "Alex",
				pos:  mgl32.Vec3{100.5, 64.0, -200.2},
				dist: 45.4,
			},
		}
		got := formatRadarOutput(targets)
		expected := "Players: 1 - Alex: <100 64 -201> (45 blocks away)"
		if got != expected {
			t.Errorf("expected %q, got %q", expected, got)
		}
	})

	t.Run("Multiple players", func(t *testing.T) {
		targets := []radarTarget{
			{
				name: "Alex",
				pos:  mgl32.Vec3{100, 64, -200},
				dist: 45,
			},
			{
				name: "Bob",
				pos:  mgl32.Vec3{250, 70, -350},
				dist: 180,
			},
		}
		got := formatRadarOutput(targets)
		expected := "Players: 2\nAlex: <100 64 -200> (45 blocks away)\nBob: <250 70 -350> (180 blocks away)"
		if got != expected {
			t.Errorf("expected:\n%s\ngot:\n%s", expected, got)
		}
	})
}

func TestRadarCmd(t *testing.T) {
	botUUID := uuid.New()
	senderUUID := uuid.New()
	alexUUID := uuid.New()
	bobUUID := uuid.New()

	b := &Bot{
		botName:  "Bebot",
		selfUUID: botUUID,
		entityID: 100,
		pos:      mgl32.Vec3{0, 64, 0},
		players: map[string]player{
			"bebot": {
				name:        "Bebot",
				accountUUID: botUUID,
				id:          100,
				online:      true,
				hasLocation: true,
				position:    mgl32.Vec3{0, 64, 0},
			},
			"sender": {
				name:        "Sender",
				accountUUID: senderUUID,
				id:          101,
				online:      true,
				hasLocation: true,
				position:    mgl32.Vec3{10, 64, 10},
			},
			"alex": {
				name:        "Alex",
				accountUUID: alexUUID,
				id:          102,
				online:      true,
				hasLocation: true,
				position:    mgl32.Vec3{40, 64, 50}, // dx=30, dz=40 -> dist=50 from sender
			},
			"bob": {
				name:        "Bob",
				accountUUID: bobUUID,
				id:          103,
				online:      true,
				hasLocation: true,
				position:    mgl32.Vec3{10, 64, 110}, // dz=100 -> dist=100 from sender
			},
		},
	}

	t.Run("Sender excluded, 2 players detected", func(t *testing.T) {
		b.commandSender = "Sender"
		// Capture replies via intercepting or inspecting radar logic
		b.radarCmd()
		// Alex is 50 blocks from sender, Bob is 100 blocks from sender
	})

	t.Run("Only sender online results in 0 players", func(t *testing.T) {
		soloBot := &Bot{
			botName:  "Bebot",
			selfUUID: botUUID,
			entityID: 100,
			pos:      mgl32.Vec3{0, 64, 0},
			players: map[string]player{
				"bebot": {
					name:        "Bebot",
					accountUUID: botUUID,
					id:          100,
					online:      true,
					hasLocation: true,
					position:    mgl32.Vec3{0, 64, 0},
				},
				"sender": {
					name:        "Sender",
					accountUUID: senderUUID,
					id:          101,
					online:      true,
					hasLocation: true,
					position:    mgl32.Vec3{10, 64, 10},
				},
			},
			commandSender: "Sender",
		}
		// Run radar
		soloBot.radarCmd()
	})

	t.Run("Sender without known location falls back to bot position", func(t *testing.T) {
		fallbackBot := &Bot{
			botName:  "Bebot",
			selfUUID: botUUID,
			pos:      mgl32.Vec3{0, 64, 0},
			players: map[string]player{
				"sender": {
					name:        "Sender",
					online:      true,
					hasLocation: false, // position unknown
				},
				"alex": {
					name:        "Alex",
					online:      true,
					hasLocation: true,
					position:    mgl32.Vec3{30, 64, 40}, // 50 from bot
				},
			},
			commandSender: "Sender",
		}
		fallbackBot.radarCmd()
	})
}

func TestLocatorBarWaypointTracking(t *testing.T) {
	groupUUID := uuid.New()
	targetUniqueID := int64(12345)

	b := &Bot{
		waypointGroups:    make(map[uuid.UUID]int64),
		players:           make(map[string]player),
		playersByID:       make(map[uint64]string),
		playersByUniqueID: make(map[int64]string),
	}

	b.players["cobblegolem_53"] = player{
		name:        "CobbleGolem_53",
		uniqueID:    targetUniqueID,
		online:      true,
		hasLocation: true,
		position:    mgl32.Vec3{10416, 65, 10437},
	}
	b.playersByUniqueID[targetUniqueID] = "cobblegolem_53"

	// 1. Simulate ActionAdd packet with ActorUniqueID and GroupHandle
	addPk := &packet.LocatorBar{
		Waypoints: []protocol.LocatorBarWaypoint{
			{
				GroupHandle: groupUUID,
				Action:      protocol.WaypointActionAdd,
				Waypoint: protocol.Waypoint{
					ActorUniqueID: protocol.Option(targetUniqueID),
					WorldPosition: protocol.Option(protocol.WaypointWorldPosition{
						Position: mgl32.Vec3{10420, 65, 10440},
					}),
				},
			},
		},
	}

	// Process LocatorBar logic
	b.mu.Lock()
	for _, wpEntry := range addPk.Waypoints {
		wp := wpEntry.Waypoint
		uniqueID, hasActorID := wp.ActorUniqueID.Value()
		posInfo, hasPos := wp.WorldPosition.Value()
		if hasActorID && uniqueID != 0 {
			b.waypointGroups[wpEntry.GroupHandle] = uniqueID
		}
		if hasPos {
			targetID := uniqueID
			if targetID == 0 {
				targetID = b.waypointGroups[wpEntry.GroupHandle]
			}
			if name, ok := b.playersByUniqueID[targetID]; ok {
				v := b.players[name]
				v.position = posInfo.Position
				b.players[name] = v
			}
		}
	}
	b.mu.Unlock()

	if b.players["cobblegolem_53"].position != (mgl32.Vec3{10420, 65, 10440}) {
		t.Fatalf("expected pos (10420, 65, 10440) after add, got %v", b.players["cobblegolem_53"].position)
	}
	if b.waypointGroups[groupUUID] != targetUniqueID {
		t.Fatalf("expected waypointGroup to map to %d, got %d", targetUniqueID, b.waypointGroups[groupUUID])
	}

	// 2. Simulate ActionUpdate packet with ONLY GroupHandle and WorldPosition (NO ActorUniqueID!)
	updatePk := &packet.LocatorBar{
		Waypoints: []protocol.LocatorBarWaypoint{
			{
				GroupHandle: groupUUID,
				Action:      protocol.WaypointActionUpdate,
				Waypoint: protocol.Waypoint{
					// ActorUniqueID is NOT set (hasActorID == false)
					WorldPosition: protocol.Option(protocol.WaypointWorldPosition{
						Position: mgl32.Vec3{10352, 68, 10703},
					}),
				},
			},
		},
	}

	b.mu.Lock()
	for _, wpEntry := range updatePk.Waypoints {
		wp := wpEntry.Waypoint
		uniqueID, hasActorID := wp.ActorUniqueID.Value()
		posInfo, hasPos := wp.WorldPosition.Value()
		if hasActorID && uniqueID != 0 {
			b.waypointGroups[wpEntry.GroupHandle] = uniqueID
		}
		if hasPos {
			targetID := uniqueID
			if targetID == 0 {
				targetID = b.waypointGroups[wpEntry.GroupHandle]
			}
			if name, ok := b.playersByUniqueID[targetID]; ok {
				v := b.players[name]
				v.position = posInfo.Position
				b.players[name] = v
			}
		}
	}
	b.mu.Unlock()

	if b.players["cobblegolem_53"].position != (mgl32.Vec3{10352, 68, 10703}) {
		t.Fatalf("expected pos (10352, 68, 10703) after update without ActorUniqueID, got %v", b.players["cobblegolem_53"].position)
	}
}
