package bot

import (
	"fmt"
	"log"
	"sort"
	"strings"

	"bebot/src/waypoint"
)

func (b *Bot) waypointCmd(p []string) {
	if len(p) < 1 {
		b.waypointHelp()
		return
	}
	switch strings.ToLower(p[0]) {
	case "help", "?":
		b.waypointHelp()
	case "list", "all":
		b.waypointList()
	case "add", "new", "create":
		b.waypointAdd(p[1:])
	case "remove", "rm", "del", "delete":
		b.waypointRemove(p[1:])
	case "update", "set", "edit", "modify":
		b.waypointUpdate(p[1:])
	default:
		b.waypointShow(p[0])
	}
}

func (b *Bot) waypointLookup(name string) (string, waypoint.Waypoint, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if w, ok := b.waypoints[name]; ok {
		return name, w, true
	}
	for key, w := range b.waypoints {
		if strings.EqualFold(key, name) {
			return key, w, true
		}
	}
	return "", waypoint.Waypoint{}, false
}

func (b *Bot) waypointAdd(args []string) {
	if len(args) < 4 {
		b.reply("Usage: waypoint add <name> <x y z> [note]")
		return
	}
	name := strings.TrimSpace(args[0])
	if name == "" {
		b.reply("Usage: waypoint add <name> <x y z> [note]")
		return
	}
	var x, y, z float32
	if _, err := fmt.Sscanf(strings.Join(args[1:4], " "), "%f %f %f", &x, &y, &z); err != nil {
		b.reply("Usage: waypoint add <name> <x y z> [note]")
		return
	}
	note := ""
	if len(args) > 4 {
		note = strings.Join(args[4:], " ")
	}
	b.mu.Lock()
	b.waypoints[name] = waypoint.Waypoint{X: x, Y: y, Z: z, Note: note}
	b.mu.Unlock()
	b.saveWaypoints()
	b.reply(fmt.Sprintf("Added waypoint %q at (%.1f, %.1f, %.1f).", name, x, y, z))
}

func (b *Bot) waypointRemove(args []string) {
	if len(args) < 1 || strings.TrimSpace(args[0]) == "" {
		b.reply("Usage: waypoint remove <name>")
		return
	}
	key, _, ok := b.waypointLookup(args[0])
	if !ok {
		b.reply("Waypoint not found: " + args[0])
		return
	}
	b.mu.Lock()
	delete(b.waypoints, key)
	b.mu.Unlock()
	b.saveWaypoints()
	b.reply("Removed waypoint " + key + ".")
}

func (b *Bot) waypointUpdate(args []string) {
	if len(args) < 4 {
		b.reply("Usage: waypoint update <name> <x y z> [note]")
		return
	}
	name := strings.TrimSpace(args[0])
	var x, y, z float32
	if _, err := fmt.Sscanf(strings.Join(args[1:4], " "), "%f %f %f", &x, &y, &z); err != nil {
		b.reply("Usage: waypoint update <name> <x y z> [note]")
		return
	}
	key, _, ok := b.waypointLookup(name)
	if !ok {
		b.reply("Waypoint not found: " + name)
		return
	}
	b.mu.Lock()
	existing := b.waypoints[key]
	note := existing.Note
	if len(args) > 4 {
		note = strings.Join(args[4:], " ")
	}
	b.waypoints[key] = waypoint.Waypoint{X: x, Y: y, Z: z, Note: note}
	b.mu.Unlock()
	b.saveWaypoints()
	b.reply(fmt.Sprintf("Updated waypoint %q to (%.1f, %.1f, %.1f).", key, x, y, z))
}

func (b *Bot) waypointShow(name string) {
	key, w, ok := b.waypointLookup(name)
	if !ok {
		b.reply("Waypoint not found: " + name + " (use \"waypoint list\")")
		return
	}
	if w.Note != "" {
		b.reply(fmt.Sprintf("%s: (%.1f, %.1f, %.1f) - %s", key, w.X, w.Y, w.Z, w.Note))
	} else {
		b.reply(fmt.Sprintf("%s: (%.1f, %.1f, %.1f)", key, w.X, w.Y, w.Z))
	}
}

func (b *Bot) waypointList() {
	b.mu.Lock()
	names := make([]string, 0, len(b.waypoints))
	for name := range b.waypoints {
		names = append(names, name)
	}
	sort.Strings(names)
	b.mu.Unlock()
	if len(names) == 0 {
		b.reply("No waypoints defined. Use: waypoint add <name> <x y z> [note]")
		return
	}
	b.reply(fmt.Sprintf("Waypoints (%d):", len(names)))
	for _, name := range names {
		b.waypointShow(name)
	}
}

func (b *Bot) waypointHelp() {
	lines := []string{
		"Waypoint commands:",
		"waypoint add <name> <x y z> [note] - add a waypoint",
		"waypoint remove <name> - remove a waypoint by name",
		"waypoint update <name> <x y z> [note] - update coordinates and note",
		"waypoint list - list all waypoints",
		"waypoint <name> - show a waypoint's details",
		"waypoint help - show this help",
	}
	for _, line := range lines {
		b.reply(line)
	}
}

func (b *Bot) saveWaypoints() {
	b.mu.Lock()
	wps := make(map[string]waypoint.Waypoint, len(b.waypoints))
	for k, v := range b.waypoints {
		wps[k] = v
	}
	b.mu.Unlock()

	if err := waypoint.SaveWaypoints(waypoint.DefaultWaypointsFile, wps); err != nil {
		log.Println("bebot: failed to save waypoints:", err)
	}
}
