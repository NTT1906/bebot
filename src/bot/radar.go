package bot

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/google/uuid"
)

type radarTarget struct {
	name string
	pos  mgl32.Vec3
	dist float64
}

// formatRadarOutput formats the radar targets according to the user specification:
// - 0 players:  "Players: 0"
// - 1 player:   "Players: 1 - A: <x y z> (n blocks away)"
// - 2+ players: "Players: 2\nA: <x y z> (n blocks away)\nB: <x y z> (n blocks away)"
func formatRadarOutput(targets []radarTarget) string {
	if len(targets) == 0 {
		return "Players: 0"
	}
	if len(targets) == 1 {
		t := targets[0]
		return fmt.Sprintf("Players: 1 - %s: <%d %d %d> (%d blocks away)",
			t.name,
			int(math.Floor(float64(t.pos.X()))),
			int(math.Floor(float64(t.pos.Y()))),
			int(math.Floor(float64(t.pos.Z()))),
			int(math.Round(t.dist)),
		)
	}

	lines := make([]string, 0, len(targets)+1)
	lines = append(lines, fmt.Sprintf("Players: %d", len(targets)))
	for _, t := range targets {
		lines = append(lines, fmt.Sprintf("%s: <%d %d %d> (%d blocks away)",
			t.name,
			int(math.Floor(float64(t.pos.X()))),
			int(math.Floor(float64(t.pos.Y()))),
			int(math.Floor(float64(t.pos.Z()))),
			int(math.Round(t.dist)),
		))
	}
	return strings.Join(lines, "\n")
}

func (b *Bot) radarCmd() {
	b.mu.Lock()
	sender := b.commandSender

	refPos := b.pos
	cleanSender := cleanChatName(sender)
	if cleanSender != "" {
		if sp, ok := b.players[strings.ToLower(cleanSender)]; ok && sp.hasLocation {
			refPos = sp.position
		}
	}

	var targets []radarTarget
	for key, p := range b.players {
		if !p.online || !p.hasLocation {
			continue
		}
		// Exclude bot itself
		if strings.EqualFold(p.name, b.botName) ||
			(b.selfUUID != uuid.Nil && p.accountUUID == b.selfUUID) ||
			(b.entityID != 0 && p.id == b.entityID) {
			continue
		}
		// Exclude sender
		if cleanSender != "" {
			if strings.EqualFold(cleanChatName(p.name), cleanSender) ||
				strings.EqualFold(key, strings.ToLower(cleanSender)) {
				continue
			}
		}

		d := p.position.Sub(refPos)
		dist := math.Sqrt(float64(d.Dot(d)))
		displayName := p.name
		if displayName == "" {
			displayName = key
		}
		targets = append(targets, radarTarget{
			name: displayName,
			pos:  p.position,
			dist: dist,
		})
	}
	b.mu.Unlock()

	// Sort targets by distance ascending, then by name
	sort.Slice(targets, func(i, j int) bool {
		if math.Abs(targets[i].dist-targets[j].dist) > 0.001 {
			return targets[i].dist < targets[j].dist
		}
		return targets[i].name < targets[j].name
	})

	output := formatRadarOutput(targets)
	b.reply(output)
}
